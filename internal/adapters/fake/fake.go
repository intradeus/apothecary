// Package fake provides in-memory implementations of the four domain ports
// (DESIGN.md §4). They are the test substrate for every §16 scenario: tests
// script behavior through function hooks and inspect recorded calls. The
// zero-value hooks succeed blandly.
package fake

import (
	"context"
	"fmt"
	"io/fs"
	"sync"

	"factory/internal/ports"
)

// --- EnvProvisioner ---------------------------------------------------------

type Env struct {
	ID     ports.EnvID
	Spec   ports.EnvSpec
	Status ports.EnvStatus
	Files  map[string][]byte
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
	p.mu.Unlock()
	if fn == nil {
		return ports.ExecResult{ExitCode: 0}, nil
	}
	return fn(e, cmd)
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
	sessions    int
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

func (r *AgentRunner) Resume(ctx context.Context, env ports.EnvID, s ports.SessionID, reply string) (ports.AgentResult, error) {
	r.mu.Lock()
	call := ResumeCall{Env: env, Session: s, Reply: reply}
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
	Title  string
	Body   string
	State  ports.MRState
}

type Forge struct {
	mu sync.Mutex
	// AccessErr fails CheckAccess when set.
	AccessErr error
	// MissingFiles marks paths FileExists reports false for (default: all exist).
	MissingFiles map[string]bool
	// CreateErr fails CreateDraftMR when set.
	CreateErr error

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

func (f *Forge) CreateDraftMR(_ context.Context, repo ports.RepoRef, branch, title, body string) (ports.MRRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return ports.MRRef{}, f.CreateErr
	}
	f.nextID++
	ref := ports.MRRef{IID: f.nextID, URL: fmt.Sprintf("https://gitlab.local/mr/%d", f.nextID)}
	f.MRs = append(f.MRs, &MR{Ref: ref, Repo: repo, Branch: branch, Title: title, Body: body, State: ports.MROpen})
	return ref, nil
}

func (f *Forge) MRState(_ context.Context, mr ports.MRRef) (ports.MRState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.MRs {
		if m.Ref.IID == mr.IID {
			return m.State, nil
		}
	}
	return "", fmt.Errorf("mr !%d not found", mr.IID)
}

// SetMRState transitions an MR (test control, e.g. simulating a merge for
// auto_complete polling).
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

// Compile-time port conformance.
var (
	_ ports.EnvProvisioner      = (*EnvProvisioner)(nil)
	_ ports.AgentRunner         = (*AgentRunner)(nil)
	_ ports.Forge               = (*Forge)(nil)
	_ ports.NotificationChannel = (*Channel)(nil)
)
