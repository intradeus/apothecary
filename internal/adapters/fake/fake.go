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
	f.MRs = append(f.MRs, &MR{Ref: ref, Repo: repo, Branch: branch, Target: target, Title: title, Body: body, State: ports.MROpen})
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
	st := ports.MRStatus{State: m.State, Mergeable: mg, HeadSHA: m.HeadSHA, Target: m.Target}
	if mg == ports.MergeConflicts {
		st.TargetSHA = m.TargetSHA
	}
	return st, nil
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
		return fmt.Errorf("forge unavailable")
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
type TicketSource struct {
	mu      sync.Mutex
	Tickets []ports.SourceTicket
	Blobs   map[string][]byte
	Err     error // returned by every method when set
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

// Compile-time port conformance.
var (
	_ ports.EnvProvisioner      = (*EnvProvisioner)(nil)
	_ ports.AgentRunner         = (*AgentRunner)(nil)
	_ ports.Forge               = (*Forge)(nil)
	_ ports.NotificationChannel = (*Channel)(nil)
	_ ports.TicketSource        = (*TicketSource)(nil)
)
