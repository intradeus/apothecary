// Package engine is the factory engine: it interprets flows (DESIGN.md §3),
// drives runs through their lifecycle (§2) as single-writer goroutines, owns
// ping raising/answering (§10), and performs run-creation preflight (§1).
// All vendor interaction goes through the ports (§4).
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"factory/internal/event"
	"factory/internal/flow"
	"factory/internal/ports"
	"factory/internal/run"
)

type Ticket struct {
	Title string
	Goal  string
}

type Config struct {
	RepoURL       string
	DefaultBranch string
	AgentsMDPath  string
	RepoRef       ports.RepoRef
	// Commands is project.commands (§1): user-provided named shell commands.
	// "setup" is mandatory and runs at bootstrap step 6.
	Commands map[string]string
	// ProviderEnv maps env var names to secret values written into the
	// workspace at bootstrap (never through provisioner parameters, §11).
	ProviderEnv map[string]string
	EngineURL   string // reachable from workspaces; default http://host.docker.internal:8080
	// MRPollInterval is the auto_complete poll cadence (§3: 60s).
	MRPollInterval time.Duration
	// RunsDir receives stage logs (<run>/<stage>-<iter>.log, §12/§13);
	// empty disables log files.
	RunsDir string
	Limits  Limits
}

// Limits are the §14 caps: runs beyond MaxConcurrentRuns queue in pending
// FIFO; at MaxEnvironments live environments new tickets are refused.
type Limits struct {
	MaxConcurrentRuns int
	MaxEnvironments   int
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.AgentsMDPath == "" {
		out.AgentsMDPath = "AGENTS.md"
	}
	if out.EngineURL == "" {
		out.EngineURL = "http://host.docker.internal:8080"
	}
	if out.MRPollInterval == 0 {
		out.MRPollInterval = 60 * time.Second
	}
	if out.Limits.MaxConcurrentRuns == 0 {
		out.Limits.MaxConcurrentRuns = 3
	}
	if out.Limits.MaxEnvironments == 0 {
		out.Limits.MaxEnvironments = 6
	}
	return out
}

type Engine struct {
	cfg      Config
	store    event.Store
	envp     ports.EnvProvisioner
	runner   ports.AgentRunner
	forge    ports.Forge
	channels []ports.NotificationChannel
	flows    map[string]*flow.Flow

	clock func() time.Time
	newID func() string
	log   *slog.Logger

	mu      sync.Mutex
	handles map[string]*runHandle
	queue   []string // pending runs waiting for a concurrency slot (§14 FIFO)
	pings   map[string]*Ping
	tokens  map[string]string // runID -> per-run bearer token (regenerated each boot, see notes)
}

type runHandle struct {
	cancel  context.CancelCauseFunc
	mailbox chan raiseReq  // agent-raised pings (ask_human shim path)
	answers chan answerReq // answers for any of this run's open pings
	done    chan struct{}
}

// Ping is an open (or answered) human interaction. Gate pings are raised by
// the engine at human_gate stages; agent pings arrive via RaisePing (§10).
type Ping struct {
	ID         string
	RunID      string
	Type       string // plan_approval|final_review|clarification|blocker|permission
	Question   string
	Context    string
	PayloadRef string
	RaisedAt   time.Time

	gate     bool
	answered bool        // guarded by Engine.mu
	answer   *PingAnswer // set before done closes; read only after <-done
	done     chan struct{}
}

type PingAnswer struct {
	Answer   string // approve|reject for gates, free text for agent pings
	Feedback string
}

// Await blocks until the ping is answered or ctx expires. It is safe to call
// from any number of goroutines (long-poll handlers, the ask_human shim path).
func (p *Ping) Await(ctx context.Context) (PingAnswer, bool) {
	select {
	case <-p.done:
		return *p.answer, true
	case <-ctx.Done():
		return PingAnswer{}, false
	}
}

// Answered reports the answer without blocking, if there is one.
func (p *Ping) Answered() (PingAnswer, bool) {
	select {
	case <-p.done:
		return *p.answer, true
	default:
		return PingAnswer{}, false
	}
}

type raiseReq struct {
	p   *Ping
	ack chan struct{}
}

type answerReq struct {
	p   *Ping
	ans PingAnswer
}

var (
	errAborted  = errors.New("run aborted")
	errShutdown = errors.New("engine shutting down")

	// Sentinel errors for API status mapping.
	ErrRunNotActive = errors.New("run is not active")
	ErrPingNotFound = errors.New("ping not found")
	ErrPingAnswered = errors.New("ping already answered")
)

func New(cfg Config, store event.Store, envp ports.EnvProvisioner, runner ports.AgentRunner,
	forge ports.Forge, channels []ports.NotificationChannel, flows map[string]*flow.Flow) *Engine {
	return &Engine{
		cfg:      cfg.withDefaults(),
		store:    store,
		envp:     envp,
		runner:   runner,
		forge:    forge,
		channels: channels,
		flows:    flows,
		clock:    time.Now,
		newID:    func() string { return ulid.Make().String() },
		log:      slog.Default(),
		handles:  map[string]*runHandle{},
		pings:    map[string]*Ping{},
		tokens:   map[string]string{},
	}
}

// CreateRun performs §1 preflight and, if it passes, appends run_created and
// starts the run's writer goroutine. Preflight failures refuse the run
// naming the exact missing item — no events are written.
func (e *Engine) CreateRun(ctx context.Context, t Ticket, flowName string) (string, error) {
	if strings.TrimSpace(t.Title) == "" {
		return "", errors.New("ticket title is required")
	}
	fl, ok := e.flows[flowName]
	if !ok {
		return "", fmt.Errorf("flow %q not found", flowName)
	}
	if err := e.preflight(ctx, fl); err != nil {
		return "", err
	}

	if err := e.checkEnvCapacity(ctx); err != nil {
		return "", err
	}

	runID := e.newID()
	branch := fmt.Sprintf("factory/%s-%s", strings.ToLower(runID), slug(t.Title, 24))
	if _, err := e.store.Append(ctx, runID, &event.RunCreated{
		TicketTitle: t.Title,
		TicketGoal:  t.Goal,
		FlowName:    fl.Name,
		FlowHash:    fl.Hash,
		RepoURL:     e.cfg.RepoURL,
		Branch:      branch,
	}); err != nil {
		return "", err
	}
	e.mu.Lock()
	e.queue = append(e.queue, runID)
	e.scheduleLocked()
	e.mu.Unlock()
	return runID, nil
}

// ErrEnvCapacity refuses new tickets at the §14 environment cap (HTTP 409).
var ErrEnvCapacity = errors.New("environment capacity reached")

// checkEnvCapacity counts live environments (env_ready without a later
// env_destroyed) across all runs; at the cap it refuses and raises a
// blocker notification naming the runs whose archiving would free envs.
func (e *Engine) checkEnvCapacity(ctx context.Context) error {
	ids, err := e.store.RunIDs(ctx)
	if err != nil {
		return err
	}
	live := 0
	var archivable []string
	for _, id := range ids {
		evs, err := e.store.Events(ctx, id, 0)
		if err != nil {
			return err
		}
		v := run.Reduce(evs)
		if v.EnvID == "" {
			continue
		}
		live++
		if v.State.Terminal() {
			archivable = append(archivable, id)
		}
	}
	if live < e.cfg.Limits.MaxEnvironments {
		return nil
	}
	e.notify(ctx, ports.Notification{
		Type:  "blocker",
		Title: fmt.Sprintf("environment capacity reached (%d/%d)", live, e.cfg.Limits.MaxEnvironments),
		Body:  "archive finished runs to free environments: " + strings.Join(archivable, ", "),
	})
	return fmt.Errorf("%w (%d live, max %d); archivable runs: %s",
		ErrEnvCapacity, live, e.cfg.Limits.MaxEnvironments, strings.Join(archivable, ", "))
}

// Archive marks a terminal run archived and destroys its environment
// (§2, §14: archived envs are reclaimed immediately). The run has no writer
// goroutine anymore, so appending directly is safe.
func (e *Engine) Archive(ctx context.Context, runID string) error {
	e.mu.Lock()
	_, active := e.handles[runID]
	e.mu.Unlock()
	if active {
		return fmt.Errorf("run %s is still active; abort it first", runID)
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		return fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.State == run.Archived {
		return fmt.Errorf("run %s is already archived", runID)
	}
	if !v.State.Terminal() {
		return fmt.Errorf("run %s is %s; only terminal runs can be archived", runID, v.State)
	}
	if _, err := e.store.Append(ctx, runID, &event.RunArchived{}); err != nil {
		return err
	}
	if v.EnvID != "" {
		if err := e.envp.Destroy(ctx, ports.EnvID(v.EnvID)); err != nil {
			e.log.Warn("archive: env destroy failed", "run", runID, "env", v.EnvID, "err", err)
		} else if _, err := e.store.Append(ctx, runID, &event.EnvDestroyed{EnvID: v.EnvID}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) preflight(ctx context.Context, fl *flow.Flow) error {
	if err := e.forge.CheckAccess(ctx, e.cfg.RepoRef); err != nil {
		return fmt.Errorf("preflight: forge access: %w", err)
	}
	for _, path := range []string{e.cfg.AgentsMDPath, ".factory/agents/"} {
		ok, err := e.forge.FileExists(ctx, e.cfg.RepoRef, path)
		if err != nil {
			return fmt.Errorf("preflight: checking %s: %w", path, err)
		}
		if !ok {
			return fmt.Errorf("preflight: %s not found in repo", path)
		}
	}
	if _, ok := e.cfg.Commands["setup"]; !ok {
		return errors.New(`preflight: project.commands.setup is not configured (mandatory)`)
	}
	for i := range fl.Stages {
		st := &fl.Stages[i]
		if st.Type == flow.StageCommand && st.RunRef != "" {
			if _, ok := e.cfg.Commands[st.RunRef]; !ok {
				return fmt.Errorf("preflight: stage %q: run_ref %q not found in project.commands", st.ID, st.RunRef)
			}
		}
	}
	return nil
}

// Recover replays events on boot (§2) and restarts every non-terminal run.
// Runs that already hold resources (provisioning and beyond) restart
// unconditionally; still-pending runs re-queue FIFO by creation order and
// start as §14 slots allow.
func (e *Engine) Recover(ctx context.Context) error {
	ids, err := e.store.RunIDs(ctx)
	if err != nil {
		return err
	}
	type pend struct {
		id      string
		created time.Time
	}
	var pending []pend
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range ids {
		evs, err := e.store.Events(ctx, id, 0)
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			continue
		}
		v := run.Reduce(evs)
		if v.State.Terminal() {
			continue
		}
		if v.State == run.Pending {
			pending = append(pending, pend{id, v.CreatedAt})
			continue
		}
		e.startLocked(id)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].created.Before(pending[j].created) })
	for _, p := range pending {
		e.queue = append(e.queue, p.id)
	}
	e.scheduleLocked()
	return nil
}

// scheduleLocked starts queued pending runs while concurrency slots are
// free (§14). Callers hold e.mu.
func (e *Engine) scheduleLocked() {
	for len(e.handles) < e.cfg.Limits.MaxConcurrentRuns && len(e.queue) > 0 {
		id := e.queue[0]
		e.queue = e.queue[1:]
		e.startLocked(id)
	}
}

func (e *Engine) startLocked(runID string) {
	if _, exists := e.handles[runID]; exists {
		return
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	h := &runHandle{
		cancel:  cancel,
		mailbox: make(chan raiseReq, 8),
		answers: make(chan answerReq, 8),
		done:    make(chan struct{}),
	}
	e.handles[runID] = h
	go e.runLoop(ctx, runID, h)
}

// Abort cancels the run context, kills any in-flight exec and marks the run
// failed with reason "aborted" (§2). The environment is kept.
func (e *Engine) Abort(runID string) error {
	e.mu.Lock()
	h, ok := e.handles[runID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
	}
	h.cancel(errAborted)
	return nil
}

// Shutdown stops all run goroutines without marking runs failed — they
// resume via Recover on next boot.
func (e *Engine) Shutdown() {
	e.mu.Lock()
	hs := make([]*runHandle, 0, len(e.handles))
	for _, h := range e.handles {
		hs = append(hs, h)
	}
	e.mu.Unlock()
	for _, h := range hs {
		h.cancel(errShutdown)
		<-h.done
	}
}

// Wait blocks until the run is neither active nor queued (test/CLI
// convenience).
func (e *Engine) Wait(runID string) {
	for {
		e.mu.Lock()
		h, active := e.handles[runID]
		queued := slices.Contains(e.queue, runID)
		e.mu.Unlock()
		switch {
		case active:
			<-h.done
		case queued:
			time.Sleep(time.Millisecond)
		default:
			return
		}
	}
}

// RaisePing is the ask_human entry point (§10): an agent in the workspace
// asks a question; the stage parks (its timeout suspends) until answered.
// The returned Ping's Result channel yields the answer exactly once.
func (e *Engine) RaisePing(runID, ptype, question, context_ string) (*Ping, error) {
	switch ptype {
	case "clarification", "blocker", "permission":
	default:
		return nil, fmt.Errorf("invalid ask_human ping type %q", ptype)
	}
	e.mu.Lock()
	h, ok := e.handles[runID]
	if !ok {
		e.mu.Unlock()
		return nil, fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
	}
	p := &Ping{
		ID: e.newID(), RunID: runID, Type: ptype, Question: question, Context: context_,
		RaisedAt: e.clock(), done: make(chan struct{}),
	}
	e.pings[p.ID] = p
	e.mu.Unlock()

	req := raiseReq{p: p, ack: make(chan struct{})}
	h.mailbox <- req
	<-req.ack
	return p, nil
}

// GetPing looks up a ping by id (nil if unknown).
func (e *Engine) GetPing(id string) *Ping {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pings[id]
}

// AnswerPing records a human answer. Gate pings accept approve|reject;
// agent pings accept free text.
func (e *Engine) AnswerPing(pingID string, ans PingAnswer) error {
	e.mu.Lock()
	p, ok := e.pings[pingID]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("ping %s: %w", pingID, ErrPingNotFound)
	}
	if p.answered {
		e.mu.Unlock()
		return fmt.Errorf("ping %s: %w", pingID, ErrPingAnswered)
	}
	h, active := e.handles[p.RunID]
	e.mu.Unlock()
	if !active {
		return fmt.Errorf("run %s: %w", p.RunID, ErrRunNotActive)
	}
	if p.gate && ans.Answer != "approve" && ans.Answer != "reject" {
		return fmt.Errorf("gate ping %s requires answer approve|reject", pingID)
	}
	h.answers <- answerReq{p: p, ans: ans}
	return nil
}

// PendingPings returns open pings (the CPUI inbox source until the DB
// projection exists).
func (e *Engine) PendingPings() []*Ping {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*Ping
	for _, p := range e.pings {
		if !p.answered {
			out = append(out, p)
		}
	}
	return out
}

// RunToken returns the per-run bearer token for §8 ping-endpoint auth.
func (e *Engine) RunToken(runID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tokens[runID]
}

// ReapOnce implements the §14 reaper pass: destroy environments of
// completed runs older than the window. Abandoned runs are never reaped
// (inspection material); archived runs lose their env at archive time.
// Appending env_destroyed for a terminal run is safe — no writer goroutine
// exists anymore.
func (e *Engine) ReapOnce(ctx context.Context, destroyCompletedAfter time.Duration) error {
	ids, err := e.store.RunIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		evs, err := e.store.Events(ctx, id, 0)
		if err != nil {
			return err
		}
		v := run.Reduce(evs)
		if v.EnvID == "" || v.State != run.Completed {
			continue
		}
		if e.clock().Sub(v.UpdatedAt) < destroyCompletedAfter {
			continue
		}
		if err := e.envp.Destroy(ctx, ports.EnvID(v.EnvID)); err != nil {
			e.log.Warn("reaper: env destroy failed", "run", id, "env", v.EnvID, "err", err)
			continue
		}
		if _, err := e.store.Append(ctx, id, &event.EnvDestroyed{EnvID: v.EnvID}); err != nil {
			return err
		}
		e.log.Info("reaper: destroyed env of completed run", "run", id, "env", v.EnvID)
	}
	return nil
}

func (e *Engine) notify(ctx context.Context, n ports.Notification) {
	for _, ch := range e.channels {
		if err := ch.Send(ctx, n); err != nil {
			e.log.Error("notification send failed", "run", n.RunID, "err", err)
		}
	}
}

func (e *Engine) finish(runID string, h *runHandle) {
	e.mu.Lock()
	delete(e.handles, runID)
	e.scheduleLocked() // a slot freed; admit the next queued run
	e.mu.Unlock()
	close(h.done)
}

func slug(s string, maxLen int) string {
	var b strings.Builder
	prevDash := true // suppress leading dash
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
		if b.Len() >= maxLen {
			break
		}
	}
	return strings.TrimRight(b.String(), "-")
}
