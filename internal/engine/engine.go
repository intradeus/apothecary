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

	"apothecary/internal/egressproxy"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// EgressController is the engine's handle to the hostname-filtering egress
// proxy (M18): per-run allowlist policy, deterministic workspace
// credentials, and blocked-host visibility. nil disables proxy mode (the
// workspace firewall falls back to the derived IP allowlist).
type EgressController interface {
	SetPolicy(runID string, hosts []string)
	DropPolicy(runID string)
	AllowHost(runID, host string) bool
	Credentials(runID string) (user, pass string)
	Port() int
	Denials(runID string) []egressproxy.Denial
}

// ErrEgressOff marks an allow request when proxy mode is disabled.
var ErrEgressOff = errors.New("egress proxy mode is off")

type Ticket struct {
	Title string
	Goal  string
}

// Config is the engine's static wiring (DESIGN2 §6.1): project-shaped
// configuration lives in Resolutions, policy limits in the SettingsSource.
type Config struct {
	EngineURL string // reachable from workspaces; default http://127.0.0.1:8080 (reverse tunnel)
	// MRPollInterval is the auto_complete poll cadence (§3: 60s).
	MRPollInterval time.Duration
	// RunsDir receives stage logs (<run>/<stage>-<iter>.log, §12/§13);
	// empty disables log files.
	RunsDir string
}

// Limits are the §14 caps: runs beyond MaxConcurrentRuns queue in pending
// FIFO; at MaxEnvironments live environments new tickets are refused.
type Limits struct {
	MaxConcurrentRuns int
	MaxEnvironments   int
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.EngineURL == "" {
		// Workspaces reach the engine through the Coder adapter's reverse
		// tunnel: localhost on both ends (deviation from §8's
		// host.docker.internal path — see IMPLEMENTATION_NOTES).
		out.EngineURL = "http://127.0.0.1:8080"
	}
	if out.MRPollInterval == 0 {
		out.MRPollInterval = 60 * time.Second
	}
	return out
}

func (l Limits) withDefaults() Limits {
	if l.MaxConcurrentRuns == 0 {
		l.MaxConcurrentRuns = 3
	}
	if l.MaxEnvironments == 0 {
		l.MaxEnvironments = 6
	}
	return l
}

type Engine struct {
	cfg      Config
	store    event.Store
	resolver Resolver
	flows    flow.Source
	settings SettingsSource
	channels []ports.NotificationChannel

	clock  func() time.Time
	newID  func() string
	log    *slog.Logger
	sink   EventSink
	egress EgressController

	mu      sync.Mutex
	closing bool // Shutdown in progress: scheduleLocked stops admitting runs
	handles map[string]*runHandle
	queue   []string // pending runs waiting for a concurrency slot (§14 FIFO)
	pings   map[string]*Ping
	tokens  map[string]string // runID -> per-run bearer token (regenerated each boot, see notes)

	// §9 envcheck jobs: in-memory, last 20 (diagnostics, not history).
	envchecks     map[string]*envCheckState
	envcheckOrder []string
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
	StageID    string   // flow stage in flight when raised (PLAN4 M15)
	Options    []string // agent-declared answer choices (PLAN4 M16)
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
	errPaused   = errors.New("run paused")
	errShutdown = errors.New("engine shutting down")

	// Sentinel errors for API status mapping.
	ErrRunNotActive = errors.New("run is not active")
	ErrPingNotFound = errors.New("ping not found")
	ErrPingAnswered = errors.New("ping already answered")
	// ErrArtifactIsURL marks a gate whose payload is a link (mr_url), not a
	// workspace artifact — the UI keeps the link-out behavior (DESIGN3 §3).
	ErrArtifactIsURL = errors.New("ping payload is a URL, not an artifact")
	// ErrNoArtifact marks a ping without a readable payload artifact.
	ErrNoArtifact = errors.New("ping has no payload artifact")
	// ErrNoLiveLog marks a run whose provisioner does not implement the
	// optional ExecLogTailer capability, or which has no environment yet.
	ErrNoLiveLog = errors.New("no live workspace log")
)

func New(cfg Config, store event.Store, resolver Resolver, flows flow.Source,
	settings SettingsSource, channels []ports.NotificationChannel) *Engine {
	if settings == nil {
		settings = StaticSettings{L: Limits{}.withDefaults()}
	}
	return &Engine{
		cfg:       cfg.withDefaults(),
		store:     store,
		resolver:  resolver,
		flows:     flows,
		settings:  settings,
		channels:  channels,
		clock:     time.Now,
		newID:     func() string { return ulid.Make().String() },
		log:       slog.Default(),
		handles:   map[string]*runHandle{},
		pings:     map[string]*Ping{},
		tokens:    map[string]string{},
		envchecks: map[string]*envCheckState{},
	}
}

// limits reads the §6.4 policy settings, falling back to defaults on error
// (a broken settings row must not brick admission).
func (e *Engine) limits(ctx context.Context) Limits {
	l, err := e.settings.Limits(ctx)
	if err != nil {
		e.log.Warn("settings read failed; using defaults", "err", err)
		return Limits{}.withDefaults()
	}
	return l.withDefaults()
}

// EventSink receives every freshly appended event exactly once (§7
// telemetry projection). Replayed history is never re-emitted.
type EventSink interface {
	OnEvent(e event.Event)
}

// SetSink installs the telemetry sink; call before Recover. nil disables.
func (e *Engine) SetSink(s EventSink) { e.sink = s }

// SetEgress installs the egress-proxy controller (M18); call before
// Recover. nil = proxy mode off.
func (e *Engine) SetEgress(c EgressController) { e.egress = c }

// EgressDenials lists a run's proxy-blocked hosts (run page feedback).
func (e *Engine) EgressDenials(runID string) []egressproxy.Denial {
	if e.egress == nil {
		return nil
	}
	return e.egress.Denials(runID)
}

// AllowEgress adds a host to a run's live proxy allowlist (the "add to
// egress" fast path): instant, no restart. Returns ErrEgressOff when proxy
// mode is off, ErrRunNotActive when the run has no active policy.
// Persistence to the project (future runs) is the caller's job.
func (e *Engine) AllowEgress(runID, host string) error {
	if e.egress == nil {
		return ErrEgressOff
	}
	if !e.egress.AllowHost(runID, host) {
		return fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
	}
	return nil
}

// emit fans freshly appended events to the sink (nil-safe).
func (e *Engine) emit(evs []event.Event) {
	if e.sink == nil {
		return
	}
	for _, ev := range evs {
		e.sink.OnEvent(ev)
	}
}

// appendAndEmit is the store append for engine-side writers outside a run
// goroutine (Archive, ReapOnce).
func (e *Engine) appendAndEmit(ctx context.Context, runID string, ps ...event.Payload) error {
	evs, err := e.store.Append(ctx, runID, ps...)
	if err != nil {
		return err
	}
	e.emit(evs)
	return nil
}

// CreateRun performs §1 preflight against the project's resolved
// configuration and, if it passes, appends run_created and starts the run's
// writer goroutine. flowName "" means the project's default flow. Preflight
// failures refuse the run naming the exact missing item — no events are
// written.
func (e *Engine) CreateRun(ctx context.Context, t Ticket, flowName, projectID string) (string, error) {
	if strings.TrimSpace(t.Title) == "" {
		return "", errors.New("ticket title is required")
	}
	if projectID == "" {
		return "", errors.New("project_id is required")
	}
	res, err := e.resolver.Resolve(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("preflight: %w", err)
	}
	defer res.release()
	if flowName == "" {
		flowName = res.DefaultFlow
	}
	fl, err := e.flows.Get(ctx, flowName)
	if err != nil {
		return "", fmt.Errorf("flow %q: %w", flowName, err)
	}
	if err := e.preflight(ctx, res, fl); err != nil {
		return "", err
	}

	if err := e.checkEnvCapacity(ctx); err != nil {
		return "", err
	}

	runID := e.newID()
	branch := fmt.Sprintf("apothecary/%s-%s", strings.ToLower(runID), slug(t.Title, 24))
	if err := e.appendAndEmit(ctx, runID, &event.RunCreated{
		TicketTitle: t.Title,
		TicketGoal:  t.Goal,
		FlowName:    fl.Name,
		FlowHash:    fl.Hash,
		RepoURL:     res.RepoRef.URL,
		Branch:      branch,
		ProjectID:   projectID,
	}); err != nil {
		return "", err
	}
	lim := e.limits(ctx)
	e.mu.Lock()
	e.queue = append(e.queue, runID)
	e.scheduleLocked(lim)
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
	maxEnvs := e.limits(ctx).MaxEnvironments
	if live < maxEnvs {
		return nil
	}
	e.notify(ctx, ports.Notification{
		Type:  "blocker",
		Title: fmt.Sprintf("environment capacity reached (%d/%d)", live, maxEnvs),
		Body:  "archive finished runs to free environments: " + strings.Join(archivable, ", "),
	})
	return fmt.Errorf("%w (%d live, max %d); archivable runs: %s",
		ErrEnvCapacity, live, maxEnvs, strings.Join(archivable, ", "))
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
	if err := e.appendAndEmit(ctx, runID, &event.RunArchived{}); err != nil {
		return err
	}
	if v.EnvID != "" {
		if err := e.destroyEnv(ctx, v); err != nil {
			e.log.Warn("archive: env destroy failed", "run", runID, "env", v.EnvID, "err", err)
		} else if err := e.appendAndEmit(ctx, runID, &event.EnvDestroyed{EnvID: v.EnvID}); err != nil {
			return err
		}
	}
	return nil
}

// RestartRun starts a terminal run over from scratch: a FRESH run (new
// id, branch, workspace, events) with the original ticket, project and
// flow name — resolved against the CURRENT config and flow version, and
// re-preflighted like any new ticket. History is immutable; the old run
// keeps its record.
func (e *Engine) RestartRun(ctx context.Context, runID string) (string, error) {
	e.mu.Lock()
	_, active := e.handles[runID]
	e.mu.Unlock()
	if active {
		return "", fmt.Errorf("run %s is still active; abort it first", runID)
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return "", err
	}
	if len(evs) == 0 {
		return "", fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if !v.State.Terminal() {
		return "", fmt.Errorf("run %s is %s; only terminal runs can be restarted", runID, v.State)
	}
	if v.ProjectID == "" {
		return "", fmt.Errorf("run %s has no project; cannot restart", runID)
	}
	return e.CreateRun(ctx, Ticket{Title: v.TicketTitle, Goal: v.TicketGoal}, v.FlowName, v.ProjectID)
}

// ResumeRun re-enters the stage where an abandoned/failed run stalled,
// in the SAME workspace ("try again"): the plan/build/earlier stages and
// the agent's commits are reused; only the failed stage re-runs, with
// retry budgets reset. Distinct from RestartRun (a fresh run from stage
// one). Refused when the run is active, not terminal, completed/archived,
// has no stalled stage, or its workspace is gone.
func (e *Engine) ResumeRun(ctx context.Context, runID string) error {
	e.mu.Lock()
	_, active := e.handles[runID]
	e.mu.Unlock()
	if active {
		return fmt.Errorf("run %s is still active", runID)
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		return fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.State != run.Abandoned && v.State != run.Failed {
		return fmt.Errorf("run %s is %s; only abandoned or failed runs can be resumed (completed runs have nothing to retry)", runID, v.State)
	}
	if v.CurrentStage == "" {
		return fmt.Errorf("run %s has no stage to resume (it never started one); restart it instead", runID)
	}
	if v.EnvID == "" {
		return fmt.Errorf("run %s has no workspace to resume into; restart it instead", runID)
	}
	if err := e.appendAndEmit(ctx, runID, &event.RunResumed{StageID: v.CurrentStage}); err != nil {
		return err
	}
	lim := e.limits(ctx)
	e.mu.Lock()
	e.queue = append(e.queue, runID)
	e.scheduleLocked(lim)
	e.mu.Unlock()
	return nil
}

// destroyEnv resolves the run's project to reach its provisioner (§6.1:
// no global envp anymore). Pre-v2 runs with no project cannot be destroyed
// through the engine — their envs outlived the config that owned them.
func (e *Engine) destroyEnv(ctx context.Context, v run.View) error {
	if v.ProjectID == "" {
		return fmt.Errorf("run %s has no project (pre-v2); destroy its environment manually", v.RunID)
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return err
	}
	defer res.release()
	return res.Envp.Destroy(ctx, ports.EnvID(v.EnvID))
}

// preflight refuses a run naming the exact missing item (§1, extended by
// DESIGN2 §6.3): forge access, repo files, run_ref resolution, and the
// harness pin check. Model strings are deliberately NOT validated against
// provider kinds — harnesses route provider/model themselves (openrouter
// serves anthropic/*), so any check would false-negative.
func (e *Engine) preflight(ctx context.Context, res *Resolution, fl *flow.Flow) error {
	if err := res.Forge.CheckAccess(ctx, res.RepoRef); err != nil {
		return fmt.Errorf("preflight: forge access: %w", err)
	}
	// AGENTS.md stays a repo file (it describes the repo); agents themselves
	// live in the project's library since v3 — the .apothecary/agents/ repo
	// contract is retired.
	ok, err := res.Forge.FileExists(ctx, res.RepoRef, res.AgentsMDPath)
	if err != nil {
		return fmt.Errorf("preflight: checking %s: %w", res.AgentsMDPath, err)
	}
	if !ok {
		return fmt.Errorf("preflight: %s not found in repo", res.AgentsMDPath)
	}
	agents := map[string]bool{}
	for _, n := range res.AgentNames {
		agents[n] = true
	}
	for i := range fl.Stages {
		st := &fl.Stages[i]
		if st.Type == flow.StageCommand && st.RunRef != "" {
			if _, ok := res.Commands[st.RunRef]; !ok {
				return fmt.Errorf("preflight: stage %q: run_ref %q not found in project.commands", st.ID, st.RunRef)
			}
		}
		// Agents resolve like run_refs: the flow names them, the project
		// defines them (DESIGN3 §1).
		if st.Type == flow.StageAgent && !agents[st.Agent] {
			return fmt.Errorf("preflight: stage %q: agent %q not found in the project's agent library", st.ID, st.Agent)
		}
		// Per-step harness (§6.3): a stage may pin any harness KIND in the
		// project's set; one outside it is refused here, not at parse time —
		// flows are portable data.
		if st.Type == flow.StageAgent && st.Harness != "" && !res.harnessInSet(st.Harness) {
			return fmt.Errorf("preflight: stage %q pins harness %q, which is not in the project's harness set %v", st.ID, st.Harness, res.harnessSet())
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
	lim := e.limits(ctx)
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
		if v.State == run.Paused {
			// The user parked it; only play (run_unpaused) re-admits it.
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
	e.scheduleLocked(lim)
	return nil
}

// scheduleLocked starts queued pending runs while concurrency slots are
// free (§14). Callers hold e.mu and pass limits read outside the lock
// (settings live in the DB, §6.4).
func (e *Engine) scheduleLocked(lim Limits) {
	if e.closing {
		// A run finishing during Shutdown must not admit a queued run that
		// nobody will cancel or wait for; it recovers on next boot.
		return
	}
	for len(e.handles) < lim.MaxConcurrentRuns && len(e.queue) > 0 {
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
// failed with reason "aborted" (§2). The environment is kept. Runs without
// a live goroutine (paused, or still queued pending) are aborted directly:
// they are dequeued and the terminal event is appended here — safe, no
// writer goroutine exists.
func (e *Engine) Abort(runID string) error {
	e.mu.Lock()
	h, ok := e.handles[runID]
	if ok {
		e.mu.Unlock()
		h.cancel(errAborted)
		return nil
	}
	// Not active: dequeue under the lock so scheduleLocked can never start
	// it between our check and the append.
	if i := slices.Index(e.queue, runID); i >= 0 {
		e.queue = append(e.queue[:i], e.queue[i+1:]...)
	}
	e.mu.Unlock()

	ctx := context.Background()
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		return fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.State.Terminal() {
		return fmt.Errorf("run %s is already %s: %w", runID, v.State, ErrRunNotActive)
	}
	return e.appendAndEmit(ctx, runID, &event.RunFailed{Reason: "aborted"})
}

// Pause parks an active run: its goroutine exits cleanly (a controlled
// crash — the event log stays consistent at every point) and the run is
// NOT recovered on boot until Play. The workspace is kept as-is for
// inspection; an in-flight agent attempt is cancelled and re-entered on
// play via its recorded session, exactly like crash recovery.
func (e *Engine) Pause(runID string) error {
	e.mu.Lock()
	h, ok := e.handles[runID]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("run %s: %w (only running runs can be paused)", runID, ErrRunNotActive)
	}
	h.cancel(errPaused)
	return nil
}

// Play restarts a paused run through the normal scheduling queue; the loop
// replays events and re-enters mid-stage like crash recovery. Distinct
// from ResumeRun ("try again"): play resets no retry budgets — the run
// simply continues.
func (e *Engine) Play(ctx context.Context, runID string) error {
	e.mu.Lock()
	_, active := e.handles[runID]
	e.mu.Unlock()
	if active {
		return fmt.Errorf("run %s is still active", runID)
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		return fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.State != run.Paused {
		return fmt.Errorf("run %s is %s; only paused runs can be played", runID, v.State)
	}
	// A paused gate re-arms without a fresh ping_raised, so the event must
	// carry whether the run lands back on waiting_on_human.
	waiting := false
	for _, p := range run.ReducePings(evs) {
		if p.Status == "pending" {
			waiting = true
			break
		}
	}
	if err := e.appendAndEmit(ctx, runID, &event.RunUnpaused{Waiting: waiting}); err != nil {
		return err
	}
	lim := e.limits(ctx)
	e.mu.Lock()
	e.queue = append(e.queue, runID)
	e.scheduleLocked(lim)
	e.mu.Unlock()
	return nil
}

// Shutdown stops all run goroutines without marking runs failed — they
// resume via Recover on next boot.
func (e *Engine) Shutdown() {
	e.mu.Lock()
	e.closing = true
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
// options (PLAN4 M16) declare a closed answer set the human must pick
// from; artifact points the question at a workspace file for the review
// workbench. The returned Ping's Result channel yields the answer exactly
// once.
func (e *Engine) RaisePing(runID, ptype, question, context_ string, options []string, artifact string) (*Ping, error) {
	switch ptype {
	case "clarification", "blocker", "permission":
	default:
		return nil, fmt.Errorf("invalid ask_human ping type %q", ptype)
	}
	if len(options) > 8 {
		return nil, fmt.Errorf("ask_human: at most 8 options (got %d)", len(options))
	}
	seen := map[string]bool{}
	var opts []string
	for _, o := range options {
		o = strings.TrimSpace(o)
		if o == "" || len(o) > 200 {
			return nil, fmt.Errorf("ask_human: options must be non-empty strings of at most 200 chars")
		}
		if !seen[o] {
			seen[o] = true
			opts = append(opts, o)
		}
	}
	if artifact != "" && !flow.ValidArtifactPath(artifact) {
		return nil, fmt.Errorf("ask_human: artifact %q must be a repo-relative path", artifact)
	}
	e.mu.Lock()
	h, ok := e.handles[runID]
	if !ok {
		e.mu.Unlock()
		return nil, fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
	}
	p := &Ping{
		ID: e.newID(), RunID: runID, Type: ptype, Question: question, Context: context_,
		Options: opts, PayloadRef: artifact,
		RaisedAt: e.clock(), done: make(chan struct{}),
	}
	e.pings[p.ID] = p
	e.mu.Unlock()

	req := raiseReq{p: p, ack: make(chan struct{})}
	// The mailbox is only drained while an agent stage is executing. Never
	// block the caller (an HTTP handler + the workspace shim) forever: if no
	// agent stage picks the ping up in time, withdraw it.
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	select {
	case h.mailbox <- req:
	case <-h.done:
		e.dropPing(p.ID)
		return nil, fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
	case <-timeout.C:
		e.dropPing(p.ID)
		return nil, fmt.Errorf("run %s is not in an agent stage; ask_human is unavailable right now", runID)
	}
	select {
	case <-req.ack:
	case <-h.done:
		e.dropPing(p.ID)
		return nil, fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
	}
	return p, nil
}

// dropPing withdraws a never-delivered ping (RaisePing failure paths).
func (e *Engine) dropPing(id string) {
	e.mu.Lock()
	delete(e.pings, id)
	e.mu.Unlock()
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
	if len(p.Options) > 0 && !slices.Contains(p.Options, ans.Answer) {
		// The agent asked a closed question (PLAN4 M16): determinism is the
		// point — the note field carries anything beyond the choice.
		return fmt.Errorf("ping %s requires one of: %s", pingID, strings.Join(p.Options, " | "))
	}
	// The answers channel is drained only in agent stages and gates. Don't
	// report success for an answer nobody will ever process, and don't wedge
	// the HTTP handler if the buffer is full.
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	select {
	case h.answers <- answerReq{p: p, ans: ans}:
		return nil
	case <-h.done:
		return fmt.Errorf("run %s: %w", p.RunID, ErrRunNotActive)
	case <-timeout.C:
		return fmt.Errorf("run %s is busy; the answer was not accepted — try again", p.RunID)
	}
}

// PingArtifact reads a gate ping's payload artifact out of the (still
// alive) workspace so the CPUI can render it for review (DESIGN3 §3.1).
// The path comes from the flow document, never from the request. Content
// is capped at 1 MiB (tail beyond — the end of a plan beats the start).
func (e *Engine) PingArtifact(ctx context.Context, pingID string) (path string, content []byte, err error) {
	e.mu.Lock()
	p := e.pings[pingID]
	e.mu.Unlock()
	if p == nil {
		return "", nil, fmt.Errorf("ping %s: %w", pingID, ErrPingNotFound)
	}
	ref := p.PayloadRef
	switch {
	case ref == "":
		return "", nil, fmt.Errorf("ping %s: %w", pingID, ErrNoArtifact)
	case strings.HasPrefix(ref, "http://"), strings.HasPrefix(ref, "https://"):
		return "", nil, fmt.Errorf("ping %s: %w", pingID, ErrArtifactIsURL)
	}
	evs, err := e.store.Events(ctx, p.RunID, 0)
	if err != nil {
		return "", nil, err
	}
	v := run.Reduce(evs)
	if v.EnvID == "" {
		return "", nil, fmt.Errorf("run %s has no environment: %w", p.RunID, ErrNoArtifact)
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return "", nil, err
	}
	defer res.release()
	data, err := res.Envp.ReadFile(ctx, ports.EnvID(v.EnvID), ref)
	if err != nil {
		return "", nil, fmt.Errorf("reading %s: %w", ref, err)
	}
	const maxLen = 1 << 20
	if len(data) > maxLen {
		data = data[len(data)-maxLen:]
	}
	return ref, data, nil
}

// WorkspaceLogTail serves the live tail of a run's workspace exec output
// via the provisioner's OPTIONAL ExecLogTailer capability — the run-page
// "is it actually doing something" feedback.
func (e *Engine) WorkspaceLogTail(ctx context.Context, runID string, n int) ([]byte, error) {
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.EnvID == "" || v.ProjectID == "" {
		return nil, fmt.Errorf("run %s: %w", runID, ErrNoLiveLog)
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, err
	}
	defer res.release()
	tailer, ok := res.Envp.(ports.ExecLogTailer)
	if !ok {
		return nil, fmt.Errorf("run %s: provisioner has no live log: %w", runID, ErrNoLiveLog)
	}
	return tailer.TailExecLog(ctx, ports.EnvID(v.EnvID), n)
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
		if err := e.destroyEnv(ctx, v); err != nil {
			e.log.Warn("reaper: env destroy failed", "run", id, "env", v.EnvID, "err", err)
			continue
		}
		if err := e.appendAndEmit(ctx, id, &event.EnvDestroyed{EnvID: v.EnvID}); err != nil {
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
	lim := e.limits(context.Background())
	if e.egress != nil {
		e.egress.DropPolicy(runID)
	}
	e.mu.Lock()
	delete(e.handles, runID)
	// Prune this run's pings: an open ping of a dead run is a permanently
	// unanswerable inbox entry (recovery re-registers open pings from the
	// event log), and answered ones are only read while the run lives.
	for id, p := range e.pings {
		if p.RunID == runID {
			delete(e.pings, id)
		}
	}
	e.scheduleLocked(lim) // a slot freed; admit the next queued run
	e.mu.Unlock()
	close(h.done)
}

// PreflightProject runs the §6.3 checks for a project's default flow
// without creating a run (POST /api/projects/{id}/preflight). Each step
// reports independently so the UI can show exactly what failed.
type PreflightStep struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (e *Engine) PreflightProject(ctx context.Context, projectID string) []PreflightStep {
	step := func(id string, err error) PreflightStep {
		s := PreflightStep{ID: id, OK: err == nil}
		if err != nil {
			s.Error = err.Error()
		}
		return s
	}
	res, err := e.resolver.Resolve(ctx, projectID)
	if err != nil {
		return []PreflightStep{step("resolve", err)}
	}
	defer res.release()
	steps := []PreflightStep{step("resolve", nil)}

	fl, err := e.flows.Get(ctx, res.DefaultFlow)
	steps = append(steps, step("flow:"+res.DefaultFlow, err))
	steps = append(steps, step("forge_access", res.Forge.CheckAccess(ctx, res.RepoRef)))
	ok, ferr := res.Forge.FileExists(ctx, res.RepoRef, res.AgentsMDPath)
	if ferr == nil && !ok {
		ferr = fmt.Errorf("%s not found in repo", res.AgentsMDPath)
	}
	steps = append(steps, step("repo_file:"+res.AgentsMDPath, ferr))
	if fl != nil {
		steps = append(steps, step("flow_refs", e.preflightFlowRefs(res, fl)))
	}
	return steps
}

func (e *Engine) preflightFlowRefs(res *Resolution, fl *flow.Flow) error {
	agents := map[string]bool{}
	for _, n := range res.AgentNames {
		agents[n] = true
	}
	for i := range fl.Stages {
		st := &fl.Stages[i]
		if st.Type == flow.StageCommand && st.RunRef != "" {
			if _, ok := res.Commands[st.RunRef]; !ok {
				return fmt.Errorf("stage %q: run_ref %q not found in project.commands", st.ID, st.RunRef)
			}
		}
		if st.Type == flow.StageAgent && !agents[st.Agent] {
			return fmt.Errorf("stage %q: agent %q not found in the project's agent library", st.ID, st.Agent)
		}
		if st.Type == flow.StageAgent && st.Harness != "" && !res.harnessInSet(st.Harness) {
			return fmt.Errorf("stage %q pins harness %q, which is not in the project's harness set %v", st.ID, st.Harness, res.harnessSet())
		}
	}
	return nil
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
