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
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
)

// --- EnvProvisioner ---------------------------------------------------------

type Env struct {
	ID     ports.EnvID
	Spec   ports.EnvSpec
	Status ports.EnvStatus
	Files  map[string][]byte
	// execLog is the live transcript (ports.ExecLogTailer parity with the
	// coder adapter): header + output per Exec.
	execLog []byte
}

type ExecCall struct {
	Env ports.EnvID
	Cmd ports.Command
}

type EnvProvisioner struct {
	mu     sync.Mutex
	nextID int
	envs   map[ports.EnvID]*Env

	// ProvisionErrs fails the first len(ProvisionErrs) Provision calls in
	// order (nil entries succeed) — for testing the 1-retry rule.
	ProvisionErrs []error
	provisions    int
	// ExecFunc scripts command execution; nil means exit 0, empty output.
	// It runs with the env's lock NOT held; use the env's Files freely —
	// fakes are single-writer like the engine.
	ExecFunc func(env *Env, cmd ports.Command) (ports.ExecResult, error)

	ExecCalls []ExecCall
	Destroyed []ports.EnvID

	// Allowed records AllowEgress calls per env; Rules records SetEgressRule
	// calls ("host=allow|deny"); Denied seeds EgressDenials — for the run-page
	// egress endpoint tests (ports.EgressController).
	Allowed map[ports.EnvID][]string
	Rules   map[ports.EnvID][]string
	Denied  map[ports.EnvID][]ports.EgressDenial
	// Dirty seeds ConfigDirty (ports.WorkspaceDirtyChecker): which envs
	// report build inputs newer than their container.
	Dirty map[ports.EnvID]bool
}

// ConfigDirty implements ports.WorkspaceDirtyChecker from the Dirty seed.
func (p *EnvProvisioner) ConfigDirty(_ context.Context, id ports.EnvID) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Dirty[id], nil
}

func NewEnvProvisioner() *EnvProvisioner {
	return &EnvProvisioner{envs: map[ports.EnvID]*Env{}}
}

func (p *EnvProvisioner) Provision(_ context.Context, spec ports.EnvSpec) (ports.EnvID, error) {
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
	id := ports.EnvID(fmt.Sprintf("env-%d", p.nextID))
	p.envs[id] = &Env{ID: id, Spec: spec, Status: ports.EnvRunning, Files: map[string][]byte{}}
	return id, nil
}

// Env returns the fake environment for direct inspection/seeding in tests.
func (p *EnvProvisioner) Env(id ports.EnvID) *Env {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.envs[id]
}

func (p *EnvProvisioner) get(id ports.EnvID) (*Env, error) {
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

func (p *EnvProvisioner) Exec(_ context.Context, id ports.EnvID, cmd ports.Command) (ports.ExecResult, error) {
	e, err := p.get(id)
	if err != nil {
		return ports.ExecResult{}, err
	}
	p.mu.Lock()
	p.ExecCalls = append(p.ExecCalls, ExecCall{Env: id, Cmd: cmd})
	fn := p.ExecFunc
	e.execLog = append(e.execLog, []byte("--- [dir="+cmd.Dir+"]\n"+cmd.Script+"\n")...)
	p.mu.Unlock()
	// `rm -f` really removes from the env's Files, so a test about deleting
	// something (the engine's terminal credential scrub) can assert the file is
	// gone rather than that a string was sent. Everything else stays a no-op.
	if paths, ok := parseRemove(cmd.Script); ok {
		p.mu.Lock()
		for _, path := range paths {
			delete(e.Files, path)
		}
		p.mu.Unlock()
		return ports.ExecResult{ExitCode: 0}, nil
	}
	if fn == nil {
		return ports.ExecResult{ExitCode: 0}, nil
	}
	res, err := fn(e, cmd)
	if err == nil && res.Output != "" {
		p.mu.Lock()
		e.execLog = append(e.execLog, []byte(res.Output+"\n")...)
		p.mu.Unlock()
	}
	return res, err
}

// parseRemove recognizes a bare `rm -f a b c` script and returns its operands
// with any shell quoting stripped. Anything more complex is not a remove as far
// as the fake is concerned.
func parseRemove(script string) ([]string, bool) {
	fields := strings.Fields(strings.TrimSpace(script))
	if len(fields) < 3 || fields[0] != "rm" || fields[1] != "-f" {
		return nil, false
	}
	out := make([]string, 0, len(fields)-2)
	for _, f := range fields[2:] {
		out = append(out, strings.ReplaceAll(strings.Trim(f, "'"), `'\''`, "'"))
	}
	return out, true
}

// TailExecLog implements the optional ports.ExecLogTailer capability.
func (p *EnvProvisioner) TailExecLog(_ context.Context, id ports.EnvID, n int) ([]byte, error) {
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

func (p *EnvProvisioner) ReadFile(_ context.Context, id ports.EnvID, path string) ([]byte, error) {
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

func (p *EnvProvisioner) WriteFile(_ context.Context, id ports.EnvID, path string, data []byte, _ fs.FileMode) error {
	e, err := p.get(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e.Files[path] = append([]byte(nil), data...)
	return nil
}

func (p *EnvProvisioner) Links(_ context.Context, id ports.EnvID) ([]ports.AccessLink, error) {
	if _, err := p.get(id); err != nil {
		return nil, err
	}
	return []ports.AccessLink{
		{Kind: "ide", URL: fmt.Sprintf("http://coder.local/%s/ide", id)},
		{Kind: "terminal", URL: fmt.Sprintf("http://coder.local/%s/terminal", id)},
	}, nil
}

func (p *EnvProvisioner) Status(_ context.Context, id ports.EnvID) (ports.EnvStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.envs[id]
	if !ok {
		return ports.EnvAbsent, nil
	}
	return e.Status, nil
}

func (p *EnvProvisioner) Destroy(_ context.Context, id ports.EnvID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.envs, id)
	p.Destroyed = append(p.Destroyed, id)
	return nil
}

// --- EgressController (in-memory, for the allow/denials endpoint tests) ------

// Allowed records AllowEgress calls per env; Denied seeds EgressDenials.
func (p *EnvProvisioner) EgressDenials(_ context.Context, id ports.EnvID) []ports.EgressDenial {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Denied[id]
}

func (p *EnvProvisioner) AllowEgress(_ context.Context, id ports.EnvID, host string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Allowed == nil {
		p.Allowed = map[ports.EnvID][]string{}
	}
	p.Allowed[id] = append(p.Allowed[id], host)
	return nil
}

// Rules records SetEgressRule calls per env as "host=allow|deny".
func (p *EnvProvisioner) SetEgressRule(_ context.Context, id ports.EnvID, host string, allow bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Rules == nil {
		p.Rules = map[ports.EnvID][]string{}
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
func (p *EnvProvisioner) EgressPolicy(_ context.Context, id ports.EnvID) (ports.EgressPolicy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pol := ports.EgressPolicy{Mode: "allow", Domains: append([]string(nil), p.Allowed[id]...)}
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

var _ ports.EgressController = (*EnvProvisioner)(nil)

// --- AgentRunner ------------------------------------------------------------

type RunCall struct {
	Env  ports.EnvID
	Spec ports.AgentSpec
	In   ports.AgentInput
}

type ResumeCall struct {
	Env     ports.EnvID
	Session ports.SessionID
	Reply   string
	In      ports.AgentInput
}

type AgentRunner struct {
	mu sync.Mutex
	// RunFunc scripts agent execution; nil returns a bland success
	// (1 turn, session "sess-<n>"). ctx is the engine's stage context —
	// honor cancellation to emulate a real harness being killed.
	RunFunc    func(ctx context.Context, call RunCall) (ports.AgentResult, error)
	ResumeFunc func(ctx context.Context, call ResumeCall) (ports.AgentResult, error)

	RunCalls    []RunCall
	ResumeCalls []ResumeCall
	// PreparedSpecs records the agent specs each Prepare call received
	// (DESIGN3 §1: proves the DB→harness path without a real adapter).
	PreparedSpecs [][]*agentspec.Spec
	sessions      int
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

func (r *AgentRunner) Run(ctx context.Context, env ports.EnvID, spec ports.AgentSpec, in ports.AgentInput) (ports.AgentResult, error) {
	r.mu.Lock()
	call := RunCall{Env: env, Spec: spec, In: in}
	r.RunCalls = append(r.RunCalls, call)
	r.sessions++
	session := ports.SessionID(fmt.Sprintf("sess-%d", r.sessions))
	fn := r.RunFunc
	r.mu.Unlock()
	if fn == nil {
		return ports.AgentResult{Session: session, TurnsUsed: 1}, nil
	}
	res, err := fn(ctx, call)
	if err == nil && res.Session == "" {
		res.Session = session
	}
	return res, err
}

func (r *AgentRunner) Resume(ctx context.Context, env ports.EnvID, s ports.SessionID, in ports.AgentInput) (ports.AgentResult, error) {
	r.mu.Lock()
	call := ResumeCall{Env: env, Session: s, Reply: in.Prompt, In: in}
	r.ResumeCalls = append(r.ResumeCalls, call)
	fn := r.ResumeFunc
	r.mu.Unlock()
	if fn == nil {
		return ports.AgentResult{Session: s, TurnsUsed: 1}, nil
	}
	res, err := fn(ctx, call)
	if err == nil && res.Session == "" {
		res.Session = s
	}
	return res, err
}

// --- Forge ------------------------------------------------------------------

type MR struct {
	Ref    ports.MRRef
	Repo   ports.RepoRef
	Branch string
	Target string
	Title  string
	Body   string
	State  ports.MRState
	// Mergeable / HeadSHA are what MRStatus reports (default clean, "").
	Mergeable ports.Mergeable
	HeadSHA   string
	TargetSHA string
	// MergeableNow and Draft are MRStatus's forge verdict; Checks what
	// MRChecks reports for the head; ApprovedBy who approved (MRApproval).
	MergeableNow bool
	Draft        bool
	Checks       []ports.Check
	ApprovedBy   []string
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
	Reviews   []ports.Review
	// Threads are the review threads MRThreads reports; replies and
	// resolutions land here.
	Threads []ports.ReviewThread
}

type Forge struct {
	mu sync.Mutex
	// AccessErr fails CheckAccess when set.
	AccessErr error
	// MissingFiles marks paths FileExists reports false for (default: all exist).
	MissingFiles map[string]bool
	// CreateErr fails CreateDraftMR when set.
	CreateErr error
	// MRStateErr fails MRState when set (signal poll failure paths).
	MRStateErr error
	// FindErr fails FindMR when set (reconciliation failure paths).
	FindErr error
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
}

// Reset forgets every merge request and scripted failure (test control, for
// a forge shared across tests).
func (f *Forge) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.MRs, f.nextID, f.ChecksCalls, f.NewHead, f.User = nil, 0, 0, "", ""
	f.AccessErr, f.CreateErr, f.MRStateErr, f.FindErr, f.ThreadsErr, f.ReplyErr = nil, nil, nil, nil, nil, nil
}

// AddOpenMR scripts a merge request someone else opened (test control): it
// gets the next iid, a head and a fetchable ref.
func (f *Forge) AddOpenMR(author, title, source, target, head string) ports.MRRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	ref := ports.MRRef{IID: f.nextID, URL: fmt.Sprintf("https://gitlab.local/mr/%d", f.nextID)}
	f.MRs = append(f.MRs, &MR{Ref: ref, Branch: source, Target: target, Title: title, Author: author, State: ports.MROpen, HeadSHA: head})
	return ref
}

func (f *Forge) info(m *MR) ports.MRInfo {
	author := m.Author
	if author == "" {
		author = f.whoami()
	}
	return ports.MRInfo{Ref: m.Ref, Title: m.Title, Body: m.Body, Author: author, State: m.State, Draft: m.Draft,
		Source: m.Branch, Target: m.Target, Head: m.HeadSHA, HeadRef: fmt.Sprintf("refs/merge-requests/%d/head", m.Ref.IID), Fork: m.Fork}
}

func (f *Forge) whoami() string {
	if f.User == "" {
		return "apothecary"
	}
	return f.User
}

func (f *Forge) GetMR(_ context.Context, iid int) (ports.MRInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return ports.MRInfo{}, f.MRStateErr
	}
	m := f.mr(iid)
	if m == nil {
		return ports.MRInfo{}, fmt.Errorf("mr !%d not found", iid)
	}
	return f.info(m), nil
}

func (f *Forge) ListMRs(_ context.Context) ([]ports.MRInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return nil, f.MRStateErr
	}
	var out []ports.MRInfo
	for i := len(f.MRs) - 1; i >= 0; i-- {
		if m := f.MRs[i]; m.State == ports.MROpen {
			out = append(out, f.info(m))
		}
	}
	return out, nil
}

func (f *Forge) Whoami(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.whoami(), nil
}

func (f *Forge) CheckAccess(_ context.Context, _ ports.RepoRef) error {
	return f.AccessErr
}

func (f *Forge) FileExists(_ context.Context, _ ports.RepoRef, path string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.MissingFiles[path], nil
}

func (f *Forge) CreateDraftMR(_ context.Context, repo ports.RepoRef, branch, target, title, body string) (ports.MRRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return ports.MRRef{}, f.CreateErr
	}
	if target == "" {
		target = repo.DefaultBranch
	}
	f.nextID++
	ref := ports.MRRef{IID: f.nextID, URL: fmt.Sprintf("https://gitlab.local/mr/%d", f.nextID)}
	f.MRs = append(f.MRs, &MR{Ref: ref, Repo: repo, Branch: branch, Target: target, Title: title, Body: body, State: ports.MROpen, HeadSHA: f.NewHead})
	return ref, nil
}

// FindMR reports the open MR for a branch, as the reconciliation path needs
// after a crash between creating one and recording it.
func (f *Forge) FindMR(_ context.Context, repo ports.RepoRef, branch, target string) (ports.MRRef, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FindErr != nil {
		return ports.MRRef{}, false, f.FindErr
	}
	if target == "" {
		target = repo.DefaultBranch
	}
	for _, mr := range f.MRs {
		if mr.Branch == branch && mr.State == ports.MROpen && (mr.Target == target || mr.Target == "") {
			return mr.Ref, true, nil
		}
	}
	return ports.MRRef{}, false, nil
}

// SnapshotMRs returns a locked copy of the merge requests opened so far.
func (f *Forge) SnapshotMRs() []MR {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]MR, 0, len(f.MRs))
	for _, mr := range f.MRs {
		out = append(out, *mr)
	}
	return out
}

func (f *Forge) mr(iid int) *MR {
	for _, m := range f.MRs {
		if m.Ref.IID == iid {
			return m
		}
	}
	return nil
}

func (f *Forge) MRStatus(_ context.Context, mr ports.MRRef) (ports.MRStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return ports.MRStatus{}, f.MRStateErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return ports.MRStatus{}, fmt.Errorf("mr !%d not found", mr.IID)
	}
	mg := m.Mergeable
	if mg == "" {
		mg = ports.MergeClean
	}
	st := ports.MRStatus{State: m.State, Mergeable: mg, HeadSHA: m.HeadSHA, Target: m.Target,
		MergeableNow: m.MergeableNow && m.State == ports.MROpen, Draft: m.Draft}
	if mg == ports.MergeConflicts {
		st.TargetSHA = m.TargetSHA
	}
	return st, nil
}

func (f *Forge) MRChecks(_ context.Context, mr ports.MRRef, head string) (ports.Checks, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return ports.Checks{}, f.MRStateErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return ports.Checks{}, fmt.Errorf("mr !%d not found", mr.IID)
	}
	out := ports.Checks{Head: head, Items: append([]ports.Check(nil), m.Checks...)}
	out.State = ports.AggregateChecks(out.Items)
	f.ChecksCalls++
	return out, nil
}

// MergeMR merges unless refused, a draft, or the head moved past the pin.
func (f *Forge) MergeMR(_ context.Context, mr ports.MRRef, opts ports.MergeOptions) error {
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
		return &ports.ErrMergeRefused{Reason: m.MergeRefusal}
	case m.State != ports.MROpen:
		return &ports.ErrMergeRefused{Reason: "it is " + string(m.State)}
	case m.Draft:
		return &ports.ErrMergeRefused{Reason: "it is a draft"}
	case opts.Head != "" && opts.Head != m.HeadSHA:
		return &ports.ErrMergeRefused{Reason: "its head moved since " + opts.Head}
	}
	m.State, m.MergedWith, m.BranchDeleted = ports.MRMerged, opts.Method, opts.DeleteBranch
	return nil
}

// PostReview records the review, its notes split by DiffLines, once per ID.
func (f *Forge) PostReview(_ context.Context, mr ports.MRRef, rev ports.Review) (ports.ReviewPosted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReplyErr != nil {
		return ports.ReviewPosted{}, f.ReplyErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return ports.ReviewPosted{}, fmt.Errorf("mr !%d not found", mr.IID)
	}
	for _, r := range m.Reviews {
		if r.ID == rev.ID {
			return ports.ReviewPosted{Already: true}, nil
		}
	}
	var out ports.ReviewPosted
	for _, n := range rev.Notes {
		if lines, ok := m.DiffLines[n.Path]; m.DiffLines == nil || (ok && slices.Contains(lines, n.Line)) {
			out.Inline++
		} else {
			out.Folded++
		}
	}
	m.Reviews = append(m.Reviews, rev)
	return out, nil
}

func (f *Forge) MRApproval(_ context.Context, mr ports.MRRef) (ports.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MRStateErr != nil {
		return ports.Approval{}, f.MRStateErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return ports.Approval{}, fmt.Errorf("mr !%d not found", mr.IID)
	}
	return ports.Approval{Approved: len(m.ApprovedBy) > 0, By: append([]string(nil), m.ApprovedBy...)}, nil
}

func (f *Forge) MRThreads(_ context.Context, mr ports.MRRef) ([]ports.ReviewThread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ThreadsErr != nil {
		return nil, f.ThreadsErr
	}
	m := f.mr(mr.IID)
	if m == nil {
		return nil, fmt.Errorf("mr !%d not found", mr.IID)
	}
	out := make([]ports.ReviewThread, len(m.Threads))
	for i, t := range m.Threads {
		t.Comments = append([]ports.ReviewComment(nil), t.Comments...)
		out[i] = t
	}
	return out, nil
}

func (f *Forge) ReplyThread(_ context.Context, mr ports.MRRef, threadID, body string) error {
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
			m.Threads[i].Comments = append(m.Threads[i].Comments, ports.ReviewComment{
				ID: fmt.Sprintf("r%d", f.replies), Author: "factory", Body: body, CreatedAt: time.Now()})
			return nil
		}
	}
	return fmt.Errorf("thread %s not found", threadID)
}

func (f *Forge) ResolveThread(_ context.Context, mr ports.MRRef, threadID string) error {
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
func (f *Forge) SetMergeable(iid int, mg ports.Mergeable, headSHA string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.Mergeable, m.HeadSHA = mg, headSHA
	}
}

// SetHead scripts the merge request's head commit (someone pushed).
func (f *Forge) SetHead(iid int, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.HeadSHA = sha
	}
}

// SetChecks scripts the CI checks MRChecks reports.
func (f *Forge) SetChecks(iid int, checks ...ports.Check) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.Checks = checks
	}
}

// SetMergeableNow scripts the forge's "it can be merged now" verdict.
func (f *Forge) SetMergeableNow(iid int, now bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.MergeableNow = now
	}
}

// SetMergeRefusal scripts a reason MergeMR declines with ("" merges).
func (f *Forge) SetMergeRefusal(iid int, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.MergeRefusal = reason
	}
}

// SetApprovedBy scripts who approved the merge request (none: unapproved).
func (f *Forge) SetApprovedBy(iid int, who ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.ApprovedBy = who
	}
}

// SetTargetSHA scripts the target branch tip reported with a conflict.
func (f *Forge) SetTargetSHA(iid int, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.mr(iid); m != nil {
		m.TargetSHA = sha
	}
}

// AddComment adds a reviewer comment to a thread, creating the thread when
// it does not exist (test control).
func (f *Forge) AddComment(iid int, threadID, path, author, body string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.mr(iid)
	if m == nil {
		return
	}
	c := ports.ReviewComment{ID: fmt.Sprintf("%s-%d", threadID, at.UnixNano()), Author: author, Body: body, CreatedAt: at}
	for i := range m.Threads {
		if m.Threads[i].ID == threadID {
			m.Threads[i].Comments = append(m.Threads[i].Comments, c)
			m.Threads[i].Resolved = false
			return
		}
	}
	m.Threads = append(m.Threads, ports.ReviewThread{ID: threadID, Path: path, Line: 1, Comments: []ports.ReviewComment{c}})
}

// SnapshotThreads returns a copy of an MR's threads (assertions).
func (f *Forge) SnapshotThreads(iid int) []ports.ReviewThread {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.mr(iid)
	if m == nil {
		return nil
	}
	out := make([]ports.ReviewThread, len(m.Threads))
	for i, t := range m.Threads {
		t.Comments = append([]ports.ReviewComment(nil), t.Comments...)
		out[i] = t
	}
	return out
}

// SetMRStateErr injects/clears an MRState failure while the engine is
// polling (race-safe test control).
func (f *Forge) SetMRStateErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.MRStateErr = err
}

// SetMRState transitions an MR (test control, e.g. simulating a merge for
// signal polling).
func (f *Forge) SetMRState(iid int, s ports.MRState) {
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
	sent []ports.Notification
}

func (c *Channel) Send(_ context.Context, n ports.Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	c.sent = append(c.sent, n)
	return nil
}

func (c *Channel) Sent() []ports.Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ports.Notification(nil), c.sent...)
}

// --- TicketSource -----------------------------------------------------------

// TicketSource is an in-memory tracker: Tickets is the whole corpus, searched
// by a case-insensitive substring of key/title/body. Attachment bytes come
// from Blobs, keyed by attachment id.
//
// It also implements the write side (ports.TicketWriter), so engine scenarios
// can script a flow's tracker actions: Reachable says which statuses a
// Transition may reach from each one (nil = any), StatusMap which of its
// statuses each canonical one means (nil = the stock Jira words: To Do, In
// Progress, In Review, Done; blocked unsupported), KnownFields which field
// names exist (nil = any), and every write lands in FieldValues/Posted/Linked
// and is logged to Calls ("transition DEV-1 In Progress") for assertions.
// WriteErr fails every write when set; Err fails everything.
type TicketSource struct {
	mu      sync.Mutex
	Tickets []ports.SourceTicket
	Blobs   map[string][]byte
	Err     error // returned by every method when set

	Reachable   map[string][]string
	StatusMap   map[ports.TicketStatus]string
	KnownFields []string
	FieldValues map[string]map[string]string
	Posted      map[string][]string
	Linked      map[string][]ports.TicketLink
	Calls       []string
	WriteErr    error
}

func (s *TicketSource) Test(context.Context) error { return s.Err }

func (s *TicketSource) Search(_ context.Context, query string, limit int) ([]ports.SourceTicket, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 25
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []ports.SourceTicket
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

func (s *TicketSource) Fetch(_ context.Context, key string) (ports.SourceTicket, error) {
	if s.Err != nil {
		return ports.SourceTicket{}, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.Tickets {
		if strings.EqualFold(t.Key, key) {
			return t, nil
		}
	}
	return ports.SourceTicket{}, fmt.Errorf("fake: no ticket %q", key)
}

func (s *TicketSource) OpenAttachment(_ context.Context, a ports.SourceAttachment) (io.ReadCloser, error) {
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

// --- TicketSource write side (ports.TicketWriter) ----------------------------

func (s *TicketSource) ticket(key string) *ports.SourceTicket {
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
			return &ports.ErrStatusUnreachable{Key: t.Key, From: t.Status, To: status, Reachable: reachable}
		}
		target = found
	}
	s.record("transition %s %s", t.Key, target)
	t.Status = target
	return nil
}

// DefaultStatusMap is the fake's stock vocabulary (StatusMap nil).
var DefaultStatusMap = map[ports.TicketStatus]string{
	ports.StatusTodo:       "To Do",
	ports.StatusInProgress: "In Progress",
	ports.StatusInReview:   "In Review",
	ports.StatusDone:       "Done",
}

// TransitionTo moves the ticket to the status StatusMap gives the canonical
// one; unmapped is unsupported, like a tracker with no such column.
func (s *TicketSource) TransitionTo(ctx context.Context, key string, to ports.TicketStatus) error {
	m := s.StatusMap
	if m == nil {
		m = DefaultStatusMap
	}
	name := m[to]
	if name == "" {
		var supported []ports.TicketStatus
		for _, st := range ports.CanonicalStatuses() {
			if m[st] != "" {
				supported = append(supported, st)
			}
		}
		return &ports.ErrStatusUnsupported{Key: strings.TrimSpace(key), Status: to, Kind: "the fake tracker", Supported: supported}
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
			return &ports.ErrFieldUnknown{Key: t.Key, Field: name, Candidates: s.KnownFields}
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
			return nil, &ports.ErrFieldUnknown{Key: t.Key, Field: name, Candidates: s.KnownFields}
		}
		out[name] = s.FieldValues[t.Key][name]
	}
	return out, nil
}

// AddLink upserts on URL, the way Jira's remote links do on globalId.
func (s *TicketSource) AddLink(_ context.Context, key string, link ports.TicketLink) error {
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
		s.Linked = map[string][]ports.TicketLink{}
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

// ReadOnlyTicketSource is a tracker with no write side — what an adapter
// that never implemented ports.TicketWriter looks like to the resolver (the
// "this integration cannot write back" path). It serves Src's corpus.
type ReadOnlyTicketSource struct{ Src *TicketSource }

func (r ReadOnlyTicketSource) Test(ctx context.Context) error { return r.Src.Test(ctx) }
func (r ReadOnlyTicketSource) Search(ctx context.Context, q string, limit int) ([]ports.SourceTicket, error) {
	return r.Src.Search(ctx, q, limit)
}
func (r ReadOnlyTicketSource) Fetch(ctx context.Context, key string) (ports.SourceTicket, error) {
	return r.Src.Fetch(ctx, key)
}
func (r ReadOnlyTicketSource) OpenAttachment(ctx context.Context, a ports.SourceAttachment) (io.ReadCloser, error) {
	return r.Src.OpenAttachment(ctx, a)
}

// Compile-time port conformance.
var (
	_ ports.EnvProvisioner      = (*EnvProvisioner)(nil)
	_ ports.AgentRunner         = (*AgentRunner)(nil)
	_ ports.Forge               = (*Forge)(nil)
	_ ports.NotificationChannel = (*Channel)(nil)
	_ ports.TicketSource        = (*TicketSource)(nil)
	_ ports.TicketWriter        = (*TicketSource)(nil)
	_ ports.TicketSource        = ReadOnlyTicketSource{}
)
