// Package fake provides in-memory implementations of the four domain ports
// (DESIGN.md §4). They are the test substrate for every §16 scenario: tests
// script behavior through function hooks and inspect recorded calls. The
// zero-value hooks succeed blandly.
package fake

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"apothecary/internal/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/egressport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
)

// --- EnvProvisioner ---------------------------------------------------------

type Env struct {
	ID     envport.EnvID
	Spec   envport.EnvSpec
	Status envport.EnvStatus
	Files  map[string][]byte
	// Modes holds the permission bits WriteTree recorded per path (WriteFile
	// records none: callers assert on Files alone there).
	Modes map[string]fs.FileMode
	// execLog is the live transcript (envport.ExecLogTailer): header + output
	// per Exec.
	execLog []byte
}

type ExecCall struct {
	Env envport.EnvID
	Cmd envport.Command
}

type EnvProvisioner struct {
	mu     sync.Mutex
	nextID int
	envs   map[envport.EnvID]*Env

	// ProvisionErrs fails the first len(ProvisionErrs) Provision calls in
	// order (nil entries succeed) — for testing the 1-retry rule.
	ProvisionErrs []error
	provisions    int
	// ExecFunc scripts command execution; nil means exit 0, empty output.
	// It runs with the env's lock NOT held; use the env's Files freely —
	// fakes are single-writer like the engine.
	ExecFunc func(env *Env, cmd envport.Command) (envport.ExecResult, error)

	ExecCalls []ExecCall
	Destroyed []envport.EnvID
	// Trees records WriteTree calls in order (env, dir, file count).
	Trees []TreeCall

	// Allowed records AllowEgress calls per env; Rules records SetEgressRule
	// calls ("host=allow|deny"); Denied seeds EgressDenials — for the run-page
	// egress endpoint tests (egressport.EgressController).
	Allowed map[envport.EnvID][]string
	Rules   map[envport.EnvID][]string
	Denied  map[envport.EnvID][]egressport.EgressDenial
	// Dirty seeds ConfigDirty (envport.WorkspaceDirtyChecker): which envs
	// report build inputs newer than their container.
	Dirty map[envport.EnvID]bool
	// SSHDial scripts the sidecar's SSH server (envport.WorkspaceSSH): what a
	// dial of an env's ssh.sock connects to. nil = the workspace serves no
	// ssh.
	SSHDial func(env envport.EnvID) (net.Conn, error)
}

// SSH implements envport.WorkspaceSSH over the SSHDial hook.
func (f *EnvProvisioner) SSH(_ context.Context, env envport.EnvID) (envport.PortConn, error) {
	if f.SSHDial == nil {
		return nil, fmt.Errorf("env %s: the fake workspace serves no ssh", env)
	}
	return sshPortConn{env: env, dial: f.SSHDial}, nil
}

type sshPortConn struct {
	env  envport.EnvID
	dial func(env envport.EnvID) (net.Conn, error)
}

func (c sshPortConn) Dial(context.Context) (net.Conn, error) { return c.dial(c.env) }
func (c sshPortConn) Close() error                           { return nil }

var _ envport.WorkspaceSSH = (*EnvProvisioner)(nil)

// ConfigDirty implements envport.WorkspaceDirtyChecker from the Dirty seed.
func (p *EnvProvisioner) ConfigDirty(_ context.Context, id envport.EnvID) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Dirty[id], nil
}

func NewEnvProvisioner() *EnvProvisioner {
	return &EnvProvisioner{envs: map[envport.EnvID]*Env{}}
}

func (p *EnvProvisioner) Provision(_ context.Context, spec envport.EnvSpec) (envport.EnvID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provisions < len(p.ProvisionErrs) {
		err := p.ProvisionErrs[p.provisions]
		p.provisions++
		if err != nil {
			return "", err
		}
	} else {
		p.provisions++
	}
	p.nextID++
	id := envport.EnvID(fmt.Sprintf("env-%d", p.nextID))
	p.envs[id] = &Env{ID: id, Spec: spec, Status: envport.EnvRunning, Files: map[string][]byte{}}
	return id, nil
}

// Env returns the fake environment for direct inspection/seeding in tests.
func (p *EnvProvisioner) Env(id envport.EnvID) *Env {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.envs[id]
}

func (p *EnvProvisioner) get(id envport.EnvID) (*Env, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.envs[id]
	if !ok {
		return nil, fmt.Errorf("env %s: %w", id, fs.ErrNotExist)
	}
	return e, nil
}

// Execs is a copy of the exec log, safe to read while runs execute.
func (p *EnvProvisioner) Execs() []ExecCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ExecCall(nil), p.ExecCalls...)
}

func (p *EnvProvisioner) Exec(_ context.Context, id envport.EnvID, cmd envport.Command) (envport.ExecResult, error) {
	e, err := p.get(id)
	if err != nil {
		return envport.ExecResult{}, err
	}
	p.mu.Lock()
	p.ExecCalls = append(p.ExecCalls, ExecCall{Env: id, Cmd: cmd})
	fn := p.ExecFunc
	e.execLog = append(e.execLog, []byte("--- [dir="+cmd.Dir+"]\n"+cmd.Script+"\n")...)
	p.mu.Unlock()
	// `rm -f` really removes from the env's Files, so a test about deleting
	// something (the engine's terminal credential scrub) can assert the file is
	// gone rather than that a string was sent; `rm -rf` removes a directory's
	// files too (a files output cleared before an attempt). Everything else
	// stays a no-op.
	if paths, recursive, ok := parseRemove(cmd.Script); ok {
		p.mu.Lock()
		for _, path := range paths {
			delete(e.Files, path)
			if recursive {
				dir := strings.TrimSuffix(path, "/") + "/"
				for f := range e.Files {
					if strings.HasPrefix(f, dir) {
						delete(e.Files, f)
					}
				}
			}
		}
		p.mu.Unlock()
		return envport.ExecResult{ExitCode: 0}, nil
	}
	if fn == nil {
		return envport.ExecResult{ExitCode: 0}, nil
	}
	res, err := fn(e, cmd)
	if err == nil && res.Output != "" {
		p.mu.Lock()
		e.execLog = append(e.execLog, []byte(res.Output+"\n")...)
		p.mu.Unlock()
	}
	return res, err
}

// parseRemove recognizes a bare `rm -f a b c` (or `rm -rf …`, recursive)
// script and returns its operands with any shell quoting stripped; whatever
// follows a `&&` is ignored. Anything more complex is not a remove as far as
// the fake is concerned.
func parseRemove(script string) (paths []string, recursive, ok bool) {
	fields := strings.Fields(strings.TrimSpace(script))
	if len(fields) < 3 || fields[0] != "rm" || (fields[1] != "-f" && fields[1] != "-rf") {
		return nil, false, false
	}
	out := make([]string, 0, len(fields)-2)
	for _, f := range fields[2:] {
		if f == "&&" {
			break
		}
		out = append(out, strings.ReplaceAll(strings.Trim(f, "'"), `'\''`, "'"))
	}
	return out, fields[1] == "-rf", true
}

// TailExecLog implements the optional envport.ExecLogTailer capability.
func (p *EnvProvisioner) TailExecLog(_ context.Context, id envport.EnvID, n int) ([]byte, error) {
	e, err := p.get(id)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	log := e.execLog
	if n <= 0 {
		n = 64 << 10
	}
	if len(log) > n {
		log = log[len(log)-n:]
	}
	return append([]byte(nil), log...), nil
}

func (p *EnvProvisioner) ReadFile(_ context.Context, id envport.EnvID, path string) ([]byte, error) {
	e, err := p.get(id)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	data, ok := e.Files[path]
	if !ok {
		return nil, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
	}
	return append([]byte(nil), data...), nil
}

// ListFiles lists the Files under dir; a directory with none is not there
// (the fake keeps files, not directories).
func (p *EnvProvisioner) ListFiles(_ context.Context, id envport.EnvID, dir string) ([]envport.FileEntry, error) {
	e, err := p.get(id)
	if err != nil {
		return nil, err
	}
	prefix := strings.TrimSuffix(dir, "/") + "/"
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []envport.FileEntry
	for path, data := range e.Files {
		if strings.HasPrefix(path, prefix) {
			out = append(out, envport.FileEntry{Path: path, Size: int64(len(data))})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: %w", dir, fs.ErrNotExist)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > envport.ListFilesLimit {
		out = out[:envport.ListFilesLimit]
	}
	return out, nil
}

func (p *EnvProvisioner) WriteFile(_ context.Context, id envport.EnvID, path string, data []byte, _ fs.FileMode) error {
	e, err := p.get(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e.Files[path] = append([]byte(nil), data...)
	return nil
}

func (p *EnvProvisioner) Status(_ context.Context, id envport.EnvID) (envport.EnvStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.envs[id]
	if !ok {
		return envport.EnvAbsent, nil
	}
	return e.Status, nil
}

func (p *EnvProvisioner) Destroy(_ context.Context, id envport.EnvID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.envs, id)
	p.Destroyed = append(p.Destroyed, id)
	return nil
}

// --- EgressController (in-memory, for the allow/denials endpoint tests) ------

// Allowed records AllowEgress calls per env; Denied seeds EgressDenials.
func (p *EnvProvisioner) EgressDenials(_ context.Context, id envport.EnvID) []egressport.EgressDenial {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]egressport.EgressDenial(nil), p.Denied[id]...)
}

func (p *EnvProvisioner) AllowEgress(_ context.Context, id envport.EnvID, host string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Allowed == nil {
		p.Allowed = map[envport.EnvID][]string{}
	}
	p.Allowed[id] = append(p.Allowed[id], host)
	return nil
}

// Rules records SetEgressRule calls per env as "host=allow|deny".
func (p *EnvProvisioner) SetEgressRule(_ context.Context, id envport.EnvID, host string, allow bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Rules == nil {
		p.Rules = map[envport.EnvID][]string{}
	}
	verb := "deny"
	if allow {
		verb = "allow"
	}
	p.Rules[id] = append(p.Rules[id], host+"="+verb)
	return nil
}

// EgressPolicy derives the effective policy from the recorded calls, the way
// the real gateway folds them into its policy file: allow-mode, Allowed seeds
// the allowlist, then Rules replay in order (allow → unblock + allowlist,
// deny → block + drop from the allowlist).
func (p *EnvProvisioner) EgressPolicy(_ context.Context, id envport.EnvID) (egressport.EgressPolicy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pol := egressport.EgressPolicy{Mode: "allow", Domains: append([]string(nil), p.Allowed[id]...)}
	for _, r := range p.Rules[id] {
		host, verb, _ := strings.Cut(r, "=")
		if verb == "allow" {
			pol.BlockedDomains = withoutStr(pol.BlockedDomains, host)
			if !containsStr(pol.Domains, host) {
				pol.Domains = append(pol.Domains, host)
			}
		} else {
			pol.Domains = withoutStr(pol.Domains, host)
			if !containsStr(pol.BlockedDomains, host) {
				pol.BlockedDomains = append(pol.BlockedDomains, host)
			}
		}
	}
	return pol, nil
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func withoutStr(xs []string, x string) []string {
	out := xs[:0:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

var _ egressport.EgressController = (*EnvProvisioner)(nil)

// --- AgentRunner ------------------------------------------------------------

type RunCall struct {
	Env  envport.EnvID
	Spec agentport.AgentSpec
	In   agentport.AgentInput
}

type ResumeCall struct {
	Env     envport.EnvID
	Session agentport.SessionID
	Reply   string
	In      agentport.AgentInput
}

type AgentRunner struct {
	mu sync.Mutex
	// RunFunc scripts agent execution; nil returns a bland success
	// (1 turn, session "sess-<n>"). ctx is the engine's stage context —
	// honor cancellation to emulate a real harness being killed.
	RunFunc    func(ctx context.Context, call RunCall) (agentport.AgentResult, error)
	ResumeFunc func(ctx context.Context, call ResumeCall) (agentport.AgentResult, error)

	RunCalls    []RunCall
	ResumeCalls []ResumeCall
	// PreparedSpecs records the agent specs each Prepare call received
	// (DESIGN3 §1: proves the DB→harness path without a real adapter).
	PreparedSpecs [][]*agentspec.Spec
	// InstalledSkills records the skill names each InstallSkills call
	// received (TODO 38: proves the pins reach every harness).
	InstalledSkills [][]string
	sessions        int
}

// RecordInstallSkills stores one InstallSkills call's names (used by the
// fake harness kind).
func (r *AgentRunner) RecordInstallSkills(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.InstalledSkills = append(r.InstalledSkills, append([]string(nil), names...))
}

// RecordPrepare stores one Prepare call's specs (used by the fake harness
// kind).
func (r *AgentRunner) RecordPrepare(specs []*agentspec.Spec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.PreparedSpecs = append(r.PreparedSpecs, specs)
}

// SnapshotPreparedSpecs returns a locked copy.
func (r *AgentRunner) SnapshotPreparedSpecs() [][]*agentspec.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]*agentspec.Spec(nil), r.PreparedSpecs...)
}

// SnapshotRunCalls returns a locked copy — use instead of reading RunCalls
// directly while agents may still be running.
func (r *AgentRunner) SnapshotRunCalls() []RunCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RunCall(nil), r.RunCalls...)
}

func (r *AgentRunner) Run(ctx context.Context, env envport.EnvID, spec agentport.AgentSpec, in agentport.AgentInput) (agentport.AgentResult, error) {
	r.mu.Lock()
	call := RunCall{Env: env, Spec: spec, In: in}
	r.RunCalls = append(r.RunCalls, call)
	r.sessions++
	session := agentport.SessionID(fmt.Sprintf("sess-%d", r.sessions))
	fn := r.RunFunc
	r.mu.Unlock()
	if fn == nil {
		return agentport.AgentResult{Session: session, TurnsUsed: 1}, nil
	}
	res, err := fn(ctx, call)
	if err == nil && res.Session == "" {
		res.Session = session
	}
	return res, err
}

func (r *AgentRunner) Resume(ctx context.Context, env envport.EnvID, s agentport.SessionID, in agentport.AgentInput) (agentport.AgentResult, error) {
	r.mu.Lock()
	call := ResumeCall{Env: env, Session: s, Reply: in.Prompt, In: in}
	r.ResumeCalls = append(r.ResumeCalls, call)
	fn := r.ResumeFunc
	r.mu.Unlock()
	if fn == nil {
		return agentport.AgentResult{Session: s, TurnsUsed: 1}, nil
	}
	res, err := fn(ctx, call)
	if err == nil && res.Session == "" {
		res.Session = s
	}
	return res, err
}

// --- RepoHost ---------------------------------------------------------------

type MR struct {
	Ref    repoport.MRRef
	Repo   repoport.RepoRef
	Branch string
	Target string
	Title  string
	Body   string
	State  repoport.MRState
	// Mergeable / HeadSHA are what MRStatus reports (default clean, "").
	Mergeable repoport.Mergeable
	HeadSHA   string
	TargetSHA string
	// MergeableNow and Draft are MRStatus's repository host verdict; Checks
	// what MRChecks reports for the head; ApprovedBy who approved
	// (MRApproval).
	MergeableNow bool
	Draft        bool
	Checks       []repoport.Check
	ApprovedBy   []string
	// CIFailures, when set, is what CIFailures details for the head.
	CIFailures []repoport.CIFailure
	// Labels are what AddMRLabels added.
	Labels []string
	// MergedWith is the method MergeMR merged with, BranchDeleted whether it
	// removed the branch; MergeRefusal, when set, refuses every merge.
	MergedWith    string
	BranchDeleted bool
	MergeRefusal  string
	// Author is who opened it ("" = the integration itself); Fork marks a
	// merge request from another repository.
	Author string
	Fork   bool
	// DiffLines, when set, are the lines of each file a review note may go
	// on (nil: any line); Reviews what PostReview posted.
	DiffLines map[string][]int
	Reviews   []repoport.Review
	// Threads are the review threads MRThreads reports; replies and
	// resolutions land here.
	Threads []repoport.ReviewThread
}

type RepoHost struct {
	mu sync.Mutex
	// AccessErr fails CheckAccess when set.
	AccessErr error
	// MissingFiles marks paths FileExists reports false for (default: all exist).
	MissingFiles map[string]bool
	// LabelErr fails AddMRLabels when set.
	LabelErr error
	// CreateErr fails CreateDraftMR when set.
	CreateErr error
	// MRStateErr fails MRState when set (signal poll failure paths).
	MRStateErr error
	// FindErr fails FindMR when set (reconciliation failure paths).
	FindErr error
	// Unpushed makes BranchURL report every branch as not on the host
	// (default: every branch is there); BranchErr fails it.
	Unpushed  bool
	BranchErr error
	// ThreadsErr / ReplyErr fail MRThreads / ReplyThread when set.
	ThreadsErr error
	ReplyErr   error
	// ReplyFailAfter, when > 0, fails every ReplyThread call after that many
	// successful ones (a crash in the middle of posting replies).
	ReplyFailAfter int
	replies        int

	MRs    []*MR
	nextID int
	// ChecksCalls counts MRChecks calls (the engine fetches checks only when
	// a flow reads them).
	ChecksCalls int
	// NewHead is the head commit a newly created merge request reports.
	NewHead string
	// User is who Whoami says the integration is (default "apothecary").
	User string
	// Uploads are the files UploadAttachment hosted, in order; UploadErr
	// fails it when set.
	Uploads   []Upload
	UploadErr error
}

// Upload is one file a fake repository host or tracker was given to host.
type Upload struct {
	Repo        repoport.RepoRef // repository host uploads
	Key         string           // tracker uploads: the issue
	Name        string
	ContentType string
	Data        []byte
	URL         string
}

// UploadAttachment hosts the file at a deterministic URL: the upload's
// ordinal and its name.
func (f *RepoHost) UploadAttachment(_ context.Context, repo repoport.RepoRef, name, contentType string, data []byte) (repoport.Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.UploadErr != nil {
		return repoport.Attachment{}, f.UploadErr
	}
	url := fmt.Sprintf("https://gitlab.local/uploads/%d/%s", len(f.Uploads)+1, name)
	f.Uploads = append(f.Uploads, Upload{Repo: repo, Name: name, ContentType: contentType, Data: append([]byte(nil), data...), URL: url})
	md := "[" + name + "](" + url + ")"
	if strings.HasPrefix(contentType, "image/") {
		md = "!" + md
	}
	return repoport.Attachment{URL: url, Markdown: md}, nil
}

// SnapshotUploads returns a locked copy of the uploads so far.
func (f *RepoHost) SnapshotUploads() []Upload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Upload(nil), f.Uploads...)
}

// Reset forgets every merge request and scripted failure (test control, for
// a repository host shared across tests).
func (f *RepoHost) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.MRs, f.nextID, f.ChecksCalls, f.NewHead, f.User, f.Uploads = nil, 0, 0, "", "", nil
	f.AccessErr, f.CreateErr, f.MRStateErr, f.FindErr, f.ThreadsErr, f.ReplyErr, f.UploadErr = nil, nil, nil, nil, nil, nil, nil
}

// AddOpenMR scripts a merge request someone else opened (test control): it
// gets the next iid, a head and a fetchable ref.
func (f *RepoHost) AddOpenMR(author, title, source, target, head string) repoport.MRRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	ref := repoport.MRRef{IID: f.nextID, URL: fmt.Sprintf("https://gitlab.local/mr/%d", f.nextID)}
	f.MRs = append(f.MRs, &MR{Ref: ref, Branch: source, Target: target, Title: title, Author: author, State: repoport.MROpen, HeadSHA: head})
	return ref
}

func (f *RepoHost) info(m *MR) repoport.MRInfo {
	author := m.Author
	if author == "" {
		author = f.whoami()
	}
	return repoport.MRInfo{Ref: m.Ref, Title: m.Title, Body: m.Body, Author: author, State: m.State, Draft: m.Draft,
		Source: m.Branch, Target: m.Target, Head: m.HeadSHA, HeadRef: fmt.Sprintf("refs/merge-requests/%d/head", m.Ref.IID), Fork: m.Fork}
}

func (f *RepoHost) whoami() string {
	if f.User == "" {
		return "apothecary"
	}
	return f.User
}

func (f *RepoHost) GetMR(_ context.Context, iid int) (repoport.MRInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return repoport.MRInfo{}, f.MRStateErr
	}
	m := f.mr(iid)
	if m == nil {
		return repoport.MRInfo{}, fmt.Errorf("mr !%d not found", iid)
	}
	return f.info(m), nil
}

func (f *RepoHost) ListMRs(_ context.Context) ([]repoport.MRInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return nil, f.MRStateErr
	}
	var out []repoport.MRInfo
	for i := len(f.MRs) - 1; i >= 0; i-- {
		if m := f.MRs[i]; m.State == repoport.MROpen {
			out = append(out, f.info(m))
		}
	}
	return out, nil
}

func (f *RepoHost) Whoami(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.whoami(), nil
}

func (f *RepoHost) CheckAccess(_ context.Context, _ repoport.RepoRef) error {
	return f.AccessErr
}

func (f *RepoHost) FileExists(_ context.Context, _ repoport.RepoRef, path string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.MissingFiles[path], nil
}

func (f *RepoHost) CreateDraftMR(_ context.Context, repo repoport.RepoRef, branch, target, title, body string) (repoport.MRRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return repoport.MRRef{}, f.CreateErr
	}
	if target == "" {
		target = repo.DefaultBranch
	}
	f.nextID++
	ref := repoport.MRRef{IID: f.nextID, URL: fmt.Sprintf("https://gitlab.local/mr/%d", f.nextID)}
	f.MRs = append(f.MRs, &MR{Ref: ref, Repo: repo, Branch: branch, Target: target, Title: title, Body: body, State: repoport.MROpen, HeadSHA: f.NewHead})
	return ref, nil
}

// AddMRLabels adds the labels the merge request does not have yet;
// LabelErr fails it.
func (f *RepoHost) AddMRLabels(_ context.Context, mr repoport.MRRef, labels []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.LabelErr != nil {
		return f.LabelErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return fmt.Errorf("mr !%d not found", mr.IID)
	}
	for _, l := range labels {
		if !slices.Contains(m.Labels, l) {
			m.Labels = append(m.Labels, l)
		}
	}
	return nil
}

// FindMR reports the open MR for a branch, as the reconciliation path needs
// after a crash between creating one and recording it.
func (f *RepoHost) FindMR(_ context.Context, repo repoport.RepoRef, branch, target string) (repoport.MRRef, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FindErr != nil {
		return repoport.MRRef{}, false, f.FindErr
	}
	if target == "" {
		target = repo.DefaultBranch
	}
	for _, mr := range f.MRs {
		if mr.Branch == branch && mr.State == repoport.MROpen && (mr.Target == target || mr.Target == "") {
			return mr.Ref, true, nil
		}
	}
	return repoport.MRRef{}, false, nil
}

// BranchURL is the branch's page at a deterministic URL, unless Unpushed.
func (f *RepoHost) BranchURL(_ context.Context, _ repoport.RepoRef, branch string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BranchErr != nil {
		return "", false, f.BranchErr
	}
	if f.Unpushed {
		return "", false, nil
	}
	return "https://gitlab.local/tree/" + branch, true, nil
}

// SnapshotMRs returns a locked copy of the merge requests opened so far.
func (f *RepoHost) SnapshotMRs() []MR {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]MR, 0, len(f.MRs))
	for _, mr := range f.MRs {
		out = append(out, *mr)
	}
	return out
}

func (f *RepoHost) mr(iid int) *MR {
	for _, m := range f.MRs {
		if m.Ref.IID == iid {
			return m
		}
	}
	return nil
}

func (f *RepoHost) MRStatus(_ context.Context, mr repoport.MRRef) (repoport.MRStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return repoport.MRStatus{}, f.MRStateErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return repoport.MRStatus{}, fmt.Errorf("mr !%d not found", mr.IID)
	}
	mg := m.Mergeable
	if mg == "" {
		mg = repoport.MergeClean
	}
	st := repoport.MRStatus{State: m.State, Mergeable: mg, HeadSHA: m.HeadSHA, Target: m.Target,
		MergeableNow: m.MergeableNow && m.State == repoport.MROpen, Draft: m.Draft}
	if mg == repoport.MergeConflicts {
		st.TargetSHA = m.TargetSHA
	}
	return st, nil
}

func (f *RepoHost) MRChecks(_ context.Context, mr repoport.MRRef, head string) (repoport.Checks, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return repoport.Checks{}, f.MRStateErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return repoport.Checks{}, fmt.Errorf("mr !%d not found", mr.IID)
	}
	out := repoport.Checks{Head: head, Items: append([]repoport.Check(nil), m.Checks...)}
	out.State = repoport.AggregateChecks(out.Items)
	f.ChecksCalls++
	return out, nil
}

// CIFailures is what Failures holds for the merge request — the failed
// checks' details a test sets — or, without it, its failed Checks by name.
func (f *RepoHost) CIFailures(_ context.Context, mr repoport.MRRef, _ string) ([]repoport.CIFailure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return nil, f.MRStateErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return nil, fmt.Errorf("mr !%d not found", mr.IID)
	}
	if m.CIFailures != nil {
		return append([]repoport.CIFailure(nil), m.CIFailures...), nil
	}
	var out []repoport.CIFailure
	for _, c := range m.Checks {
		if c.State == repoport.CheckFailure {
			out = append(out, repoport.CIFailure{Name: c.Name, URL: c.URL})
		}
	}
	return out, nil
}

// MergeMR merges unless refused, a draft, or the head moved past the pin.
func (f *RepoHost) MergeMR(_ context.Context, mr repoport.MRRef, opts repoport.MergeOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return f.MRStateErr
	}
	m := f.mr(mr.IID)
	switch {
	case m == nil:
		return fmt.Errorf("mr !%d not found", mr.IID)
	case m.MergeRefusal != "":
		return &repoport.ErrMergeRefused{Reason: m.MergeRefusal}
	case m.State != repoport.MROpen:
		return &repoport.ErrMergeRefused{Reason: "it is " + string(m.State)}
	case m.Draft:
		return &repoport.ErrMergeRefused{Reason: "it is a draft"}
	case opts.Head != "" && opts.Head != m.HeadSHA:
		return &repoport.ErrMergeRefused{Reason: "its head moved since " + opts.Head}
	}
	m.State, m.MergedWith, m.BranchDeleted = repoport.MRMerged, opts.Method, opts.DeleteBranch
	return nil
}

// PostReview records the review, its notes split by DiffLines, once per ID.
// As on a real host, a note posted on its line becomes a thread the next
// MRThreads lists, opened by the factory (its body carries the note's
// marker): what a re-review reads before it writes.
func (f *RepoHost) PostReview(_ context.Context, mr repoport.MRRef, rev repoport.Review) (repoport.ReviewPosted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReplyErr != nil {
		return repoport.ReviewPosted{}, f.ReplyErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return repoport.ReviewPosted{}, fmt.Errorf("mr !%d not found", mr.IID)
	}
	for _, r := range m.Reviews {
		if r.ID == rev.ID {
			return repoport.ReviewPosted{Already: true}, nil
		}
	}
	var out repoport.ReviewPosted
	now := time.Now()
	for i, n := range rev.Notes {
		if lines, ok := m.DiffLines[n.Path]; m.DiffLines == nil || (ok && slices.Contains(lines, n.Line)) {
			out.Inline++
			id := fmt.Sprintf("n%d", len(m.Threads)+1)
			m.Threads = append(m.Threads, repoport.ReviewThread{ID: id, Path: n.Path, Line: n.Line, Comments: []repoport.ReviewComment{{
				ID: id + "-0", Author: "factory", Body: strings.TrimSpace(n.Body) + "\n\n" + repoport.NoteMarker(rev.ID, i, n.Kind), CreatedAt: now}}})
		} else {
			out.Folded++
		}
	}
	m.Reviews = append(m.Reviews, rev)
	return out, nil
}

func (f *RepoHost) MRApproval(_ context.Context, mr repoport.MRRef) (repoport.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return repoport.Approval{}, f.MRStateErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return repoport.Approval{}, fmt.Errorf("mr !%d not found", mr.IID)
	}
	return repoport.Approval{Approved: len(m.ApprovedBy) > 0, By: append([]string(nil), m.ApprovedBy...)}, nil
}

func (f *RepoHost) MRThreads(_ context.Context, mr repoport.MRRef) ([]repoport.ReviewThread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ThreadsErr != nil {
		return nil, f.ThreadsErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return nil, fmt.Errorf("mr !%d not found", mr.IID)
	}
	out := make([]repoport.ReviewThread, len(m.Threads))
	for i, t := range m.Threads {
		t.Comments = append([]repoport.ReviewComment(nil), t.Comments...)
		out[i] = t
	}
	return out, nil
}

func (f *RepoHost) ReplyThread(_ context.Context, mr repoport.MRRef, threadID, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReplyErr != nil {
		return f.ReplyErr
	}
	if f.ReplyFailAfter > 0 && f.replies >= f.ReplyFailAfter {
		return fmt.Errorf("repository unavailable")
	}
	m := f.mr(mr.IID)
	if m == nil {
		return fmt.Errorf("mr !%d not found", mr.IID)
	}
	for i := range m.Threads {
		if m.Threads[i].ID == threadID {
			f.replies++
			m.Threads[i].Comments = append(m.Threads[i].Comments, repoport.ReviewComment{
				ID: fmt.Sprintf("r%d", f.replies), Author: "factory", Body: body, CreatedAt: time.Now()})
			return nil
		}
	}
	return fmt.Errorf("thread %s not found", threadID)
}

func (f *RepoHost) ResolveThread(_ context.Context, mr repoport.MRRef, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.mr(mr.IID)
	if m == nil {
		return fmt.Errorf("mr !%d not found", mr.IID)
	}
	for i := range m.Threads {
		if m.Threads[i].ID == threadID {
			m.Threads[i].Resolved = true
			return nil
		}
	}
	return fmt.Errorf("thread %s not found", threadID)
}

// SetMergeable scripts what MRStatus reports about merging (test control).
func (f *RepoHost) SetMergeable(iid int, mg repoport.Mergeable, headSHA string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.Mergeable, m.HeadSHA = mg, headSHA
	}
}

// SetHead scripts the merge request's head commit (someone pushed).
func (f *RepoHost) SetHead(iid int, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.HeadSHA = sha
	}
}

// SetChecks scripts the CI checks MRChecks reports.
func (f *RepoHost) SetChecks(iid int, checks ...repoport.Check) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.Checks = checks
	}
}

// SetCIFailures scripts what CIFailures details for the merge request.
func (f *RepoHost) SetCIFailures(iid int, failures ...repoport.CIFailure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.CIFailures = failures
	}
}

// SetMergeableNow scripts the repository host's "it can be merged now" verdict.
func (f *RepoHost) SetMergeableNow(iid int, now bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.MergeableNow = now
	}
}

// SetMergeRefusal scripts a reason MergeMR declines with ("" merges).
func (f *RepoHost) SetMergeRefusal(iid int, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.MergeRefusal = reason
	}
}

// SetApprovedBy scripts who approved the merge request (none: unapproved).
func (f *RepoHost) SetApprovedBy(iid int, who ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.ApprovedBy = who
	}
}

// SetTargetSHA scripts the target branch tip reported with a conflict.
func (f *RepoHost) SetTargetSHA(iid int, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.TargetSHA = sha
	}
}

// AddComment adds a reviewer comment to a thread, creating the thread when
// it does not exist (test control).
func (f *RepoHost) AddComment(iid int, threadID, path, author, body string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.mr(iid)
	if m == nil {
		return
	}
	c := repoport.ReviewComment{ID: fmt.Sprintf("%s-%d", threadID, at.UnixNano()), Author: author, Body: body, CreatedAt: at}
	for i := range m.Threads {
		if m.Threads[i].ID == threadID {
			m.Threads[i].Comments = append(m.Threads[i].Comments, c)
			m.Threads[i].Resolved = false
			return
		}
	}
	m.Threads = append(m.Threads, repoport.ReviewThread{ID: threadID, Path: path, Line: 1, Comments: []repoport.ReviewComment{c}})
}

// SnapshotThreads returns a copy of an MR's threads (assertions).
func (f *RepoHost) SnapshotThreads(iid int) []repoport.ReviewThread {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.mr(iid)
	if m == nil {
		return nil
	}
	out := make([]repoport.ReviewThread, len(m.Threads))
	for i, t := range m.Threads {
		t.Comments = append([]repoport.ReviewComment(nil), t.Comments...)
		out[i] = t
	}
	return out
}

// SetMRStateErr injects/clears an MRState failure while the engine is
// polling (race-safe test control).
func (f *RepoHost) SetMRStateErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.MRStateErr = err
}

// SetMRState transitions an MR (test control, e.g. simulating a merge for
// signal polling).
func (f *RepoHost) SetMRState(iid int, s repoport.MRState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.MRs {
		if m.Ref.IID == iid {
			m.State = s
		}
	}
}

// --- NotificationChannel ----------------------------------------------------

type Channel struct {
	mu   sync.Mutex
	Err  error // returned by Send when set (must never fail the run)
	sent []notifyport.Notification
}

func (c *Channel) Send(_ context.Context, n notifyport.Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	c.sent = append(c.sent, n)
	return nil
}

func (c *Channel) Sent() []notifyport.Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]notifyport.Notification(nil), c.sent...)
}

// --- TicketSource -----------------------------------------------------------

// TicketSource is an in-memory tracker: Tickets is the whole corpus, searched
// by a case-insensitive substring of key/title/body. Attachment bytes come
// from Blobs, keyed by attachment id.
//
// It also implements the write side (trackerport.TicketWriter), so engine scenarios
// can script a flow's tracker actions: Reachable says which statuses a
// Transition may reach from each one (nil = any), StatusMap which of its
// statuses each canonical one means (nil = the stock Jira words: To Do, In
// Progress, In Review, Done; blocked unsupported), KnownFields which field
// names exist (nil = any), and every write lands in FieldValues/Posted/Linked
// and is logged to Calls ("transition DEV-1 In Progress") for assertions.
// WriteErr fails every write when set; Err fails everything.
type TicketSource struct {
	mu      sync.Mutex
	Tickets []trackerport.SourceTicket
	Blobs   map[string][]byte
	Err     error // returned by every method when set

	Reachable   map[string][]string
	StatusMap   map[trackerport.TicketStatus]string
	KnownFields []string
	FieldValues map[string]map[string]string
	Posted      map[string][]string
	Linked      map[string][]trackerport.TicketLink
	Calls       []string
	WriteErr    error
	// Attached are the files Attach hosted, in order.
	Attached []Upload
}

func (s *TicketSource) Test(context.Context) error { return s.Err }

func (s *TicketSource) Search(_ context.Context, query string, limit int) ([]trackerport.SourceTicket, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 25
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []trackerport.SourceTicket
	for _, t := range s.Tickets {
		hay := strings.ToLower(t.Key + " " + t.Title + " " + t.Body)
		if q == "" || strings.Contains(hay, q) {
			// Search results carry no attachments (Fetch is authoritative),
			// mirroring what a real tracker's list endpoint returns.
			t.Attachments = nil
			out = append(out, t)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *TicketSource) Fetch(_ context.Context, key string) (trackerport.SourceTicket, error) {
	if s.Err != nil {
		return trackerport.SourceTicket{}, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.Tickets {
		if strings.EqualFold(t.Key, key) {
			return t, nil
		}
	}
	return trackerport.SourceTicket{}, fmt.Errorf("fake: no ticket %q", key)
}

func (s *TicketSource) OpenAttachment(_ context.Context, a trackerport.SourceAttachment) (io.ReadCloser, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.Blobs[a.ID]
	if !ok {
		return nil, fmt.Errorf("fake: no blob for attachment %q", a.ID)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// --- TicketSource write side (trackerport.TicketWriter) ----------------------------

func (s *TicketSource) ticket(key string) *trackerport.SourceTicket {
	for i := range s.Tickets {
		if strings.EqualFold(s.Tickets[i].Key, key) {
			return &s.Tickets[i]
		}
	}
	return nil
}

func (s *TicketSource) record(format string, args ...any) {
	s.Calls = append(s.Calls, fmt.Sprintf(format, args...))
}

func (s *TicketSource) writeErr() error {
	if s.Err != nil {
		return s.Err
	}
	return s.WriteErr
}

// Transition moves the ticket when the status is reachable (Reachable's
// vocabulary wins over the flow's spelling); already there is a no-op that
// is still recorded, so a test can tell "called" from "moved".
func (s *TicketSource) Transition(_ context.Context, key, status string) error {
	if err := s.writeErr(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ticket(key)
	if t == nil {
		return fmt.Errorf("fake: no ticket %q", key)
	}
	if strings.EqualFold(t.Status, status) {
		s.record("transition %s %s (already there)", t.Key, t.Status)
		return nil
	}
	target := status
	if s.Reachable != nil {
		reachable := s.Reachable[t.Status]
		found := ""
		for _, r := range reachable {
			if strings.EqualFold(r, status) {
				found = r
			}
		}
		if found == "" {
			return &trackerport.ErrStatusUnreachable{Key: t.Key, From: t.Status, To: status, Reachable: reachable}
		}
		target = found
	}
	s.record("transition %s %s", t.Key, target)
	t.Status = target
	return nil
}

// DefaultStatusMap is the fake's stock vocabulary (StatusMap nil).
var DefaultStatusMap = map[trackerport.TicketStatus]string{
	trackerport.StatusTodo:       "To Do",
	trackerport.StatusInProgress: "In Progress",
	trackerport.StatusInReview:   "In Review",
	trackerport.StatusDone:       "Done",
}

// TransitionTo moves the ticket to the status StatusMap gives the canonical
// one; unmapped is unsupported, like a tracker with no such column.
func (s *TicketSource) TransitionTo(ctx context.Context, key string, to trackerport.TicketStatus) error {
	m := s.StatusMap
	if m == nil {
		m = DefaultStatusMap
	}
	name := m[to]
	if name == "" {
		var supported []trackerport.TicketStatus
		for _, st := range trackerport.CanonicalStatuses() {
			if m[st] != "" {
				supported = append(supported, st)
			}
		}
		return &trackerport.ErrStatusUnsupported{Key: strings.TrimSpace(key), Status: to, Kind: "the fake tracker", Supported: supported}
	}
	return s.Transition(ctx, key, name)
}

func (s *TicketSource) Comment(_ context.Context, key, body string) error {
	if err := s.writeErr(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ticket(key)
	if t == nil {
		return fmt.Errorf("fake: no ticket %q", key)
	}
	if s.Posted == nil {
		s.Posted = map[string][]string{}
	}
	s.Posted[t.Key] = append(s.Posted[t.Key], body)
	s.record("comment %s", t.Key)
	return nil
}

func (s *TicketSource) Comments(_ context.Context, key string) ([]string, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ticket(key)
	if t == nil {
		return nil, fmt.Errorf("fake: no ticket %q", key)
	}
	return append([]string(nil), s.Posted[t.Key]...), nil
}

func (s *TicketSource) knows(field string) bool {
	if s.KnownFields == nil {
		return true
	}
	for _, f := range s.KnownFields {
		if strings.EqualFold(f, field) {
			return true
		}
	}
	return false
}

func (s *TicketSource) SetFields(_ context.Context, key string, fields map[string]string) error {
	if err := s.writeErr(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ticket(key)
	if t == nil {
		return fmt.Errorf("fake: no ticket %q", key)
	}
	if s.FieldValues == nil {
		s.FieldValues = map[string]map[string]string{}
	}
	if s.FieldValues[t.Key] == nil {
		s.FieldValues[t.Key] = map[string]string{}
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !s.knows(name) {
			return &trackerport.ErrFieldUnknown{Key: t.Key, Field: name, Candidates: s.KnownFields}
		}
		s.FieldValues[t.Key][name] = fields[name]
		s.record("set %s %s=%s", t.Key, name, fields[name])
	}
	return nil
}

func (s *TicketSource) Fields(_ context.Context, key string, names []string) (map[string]string, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ticket(key)
	if t == nil {
		return nil, fmt.Errorf("fake: no ticket %q", key)
	}
	out := map[string]string{}
	for _, name := range names {
		if !s.knows(name) {
			return nil, &trackerport.ErrFieldUnknown{Key: t.Key, Field: name, Candidates: s.KnownFields}
		}
		out[name] = s.FieldValues[t.Key][name]
	}
	return out, nil
}

// AddLink upserts on URL, the way Jira's remote links do on globalId.
func (s *TicketSource) AddLink(_ context.Context, key string, link trackerport.TicketLink) error {
	if err := s.writeErr(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ticket(key)
	if t == nil {
		return fmt.Errorf("fake: no ticket %q", key)
	}
	if s.Linked == nil {
		s.Linked = map[string][]trackerport.TicketLink{}
	}
	s.record("link %s %s", t.Key, link.URL)
	for i, l := range s.Linked[t.Key] {
		if l.URL == link.URL {
			s.Linked[t.Key][i] = link
			return nil
		}
	}
	s.Linked[t.Key] = append(s.Linked[t.Key], link)
	return nil
}

// Attach hosts a file for the issue at a deterministic URL and links it the
// way Jira does: a link, never an embedded image.
func (s *TicketSource) Attach(_ context.Context, key, name, contentType string, data []byte) (repoport.Attachment, error) {
	if err := s.writeErr(); err != nil {
		return repoport.Attachment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ticket(key)
	if t == nil {
		return repoport.Attachment{}, fmt.Errorf("fake: no ticket %q", key)
	}
	url := fmt.Sprintf("https://tracker.local/%s/attachments/%d/%s", t.Key, len(s.Attached)+1, name)
	s.Attached = append(s.Attached, Upload{Key: t.Key, Name: name, ContentType: contentType, Data: append([]byte(nil), data...), URL: url})
	s.record("attach %s %s", t.Key, name)
	return repoport.Attachment{URL: url, Markdown: "[" + name + "](" + url + ")"}, nil
}

// ReadOnlyTicketSource is a tracker with no write side — what an adapter
// that never implemented trackerport.TicketWriter looks like to the resolver (the
// "this integration cannot write back" path). It serves Src's corpus.
type ReadOnlyTicketSource struct{ Src *TicketSource }

func (r ReadOnlyTicketSource) Test(ctx context.Context) error { return r.Src.Test(ctx) }
func (r ReadOnlyTicketSource) Search(ctx context.Context, q string, limit int) ([]trackerport.SourceTicket, error) {
	return r.Src.Search(ctx, q, limit)
}
func (r ReadOnlyTicketSource) Fetch(ctx context.Context, key string) (trackerport.SourceTicket, error) {
	return r.Src.Fetch(ctx, key)
}
func (r ReadOnlyTicketSource) OpenAttachment(ctx context.Context, a trackerport.SourceAttachment) (io.ReadCloser, error) {
	return r.Src.OpenAttachment(ctx, a)
}

// Compile-time port conformance.
var (
	_ envport.EnvProvisioner         = (*EnvProvisioner)(nil)
	_ agentport.AgentRunner          = (*AgentRunner)(nil)
	_ repoport.RepoHost              = (*RepoHost)(nil)
	_ notifyport.NotificationChannel = (*Channel)(nil)
	_ trackerport.TicketSource       = (*TicketSource)(nil)
	_ trackerport.TicketWriter       = (*TicketSource)(nil)
	_ trackerport.TicketAttacher     = (*TicketSource)(nil)
	_ trackerport.TicketSource       = ReadOnlyTicketSource{}
)
