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
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	askstore "apothecary/internal/ask"
	"apothecary/internal/event"
	"apothecary/internal/filestore"
	"apothecary/internal/flow"
	"apothecary/internal/portrelay"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// ErrEgressOff marks an allow request when proxy mode is disabled.
var ErrEgressOff = errors.New("egress proxy mode is off")

type Ticket struct {
	// TicketID is the parent ticket this run belongs to. When set, Branch is
	// treated as the ticket's BASE branch and the run gets its own unique
	// actual branch = base + "-" + <run-id tail>. Empty (direct/test path) =
	// legacy behavior: Branch is used verbatim / generated with the run id.
	TicketID string
	Title    string
	Goal     string
	// Branch is the base branch when TicketID is set, else the run's branch
	// override ("" = the default apothecary/<run-id>-<title-slug>). Names git
	// check-ref-format would refuse are rejected.
	Branch string
	// SourceBranch is the branch this run forks off from (the base of
	// `git switch -c <branch> origin/<SourceBranch>`). Per-run; "" defaults to
	// the project's default branch. Names git check-ref-format would refuse are
	// rejected.
	SourceBranch string
	// RunGoal is extra direction for THIS attempt, additive to Goal (which
	// stays the enduring spec). Rendered as its own prompt section.
	RunGoal string
	// AttachmentIDs are uploaded files attached to THIS RUN: claimed for the
	// run and delivered into its workspace.
	AttachmentIDs []string
	// TicketAttachmentIDs are the parent ticket's attachments — owned by the
	// ticket, delivered to every run of it. Snapshotted onto run_created;
	// NOT associated to this run.
	TicketAttachmentIDs []string
	// Source* snapshot the tracker issue the ticket was imported from — the
	// ticketsource integration id, its vendor, the issue key and its page —
	// so a flow's tracker actions (FLOWLANG §2) know what to write to. All
	// empty for a ticket written by hand: those stages then skip.
	SourceID   string
	SourceKind string
	SourceKey  string
	SourceURL  string
}

// RunInput is the optional per-attempt input when starting a run from a
// ticket or restarting one: a flow override plus this attempt's direction and
// attachments.
type RunInput struct {
	Flow string // "" = the ticket/original run's flow
	Goal string // extra direction for this attempt
	// SourceBranch overrides the branch the restarted run forks off from;
	// "" reuses the original run's source branch.
	SourceBranch  string
	AttachmentIDs []string
	// Params override the project's parameter bindings for this attempt;
	// By records who created the run on run_created ("" = a person,
	// "automation" for an automation).
	Params map[string]string
	By     string
	// MR starts the run on a merge request someone opened, for a flow with
	// operates_on: merge_request: Repo is the checkout it belongs to ("" =
	// the primary), IID its number.
	MR *RunMR
}

// RunMR names a merge request to create a run on.
type RunMR struct {
	Repo string `json:"repo,omitempty"`
	IID  int    `json:"iid"`
}

// Config is the engine's static wiring (DESIGN2 §6.1): project-shaped
// configuration lives in Resolutions, policy limits in the SettingsSource.
type Config struct {
	EngineURL string // reachable from workspaces; default http://127.0.0.1:8080 (reverse tunnel)
	// MRPollInterval is the signal poll cadence, shared by wait stages and a
	// gate's auto_answer (§3: 60s).
	MRPollInterval time.Duration
	// ThreadPollInterval is how often an idle run lists a merge request's
	// review threads (a second, heavier call than its status) when a phase
	// reacts to review comments. Default 4 × MRPollInterval.
	ThreadPollInterval time.Duration
	// RunsDir receives stage logs (<run>/<stage>-<iter>.log, §12/§13);
	// empty disables log files.
	RunsDir string

	// PortDiscoveryAutoExpose, when set, auto-exposes newly discovered
	// listening ports (up to PortDiscoveryMaxAuto per run) instead of only
	// listing them for click-to-expose. Default off (list-only).
	PortDiscoveryAutoExpose bool
	// PortDiscoveryMaxAuto caps auto-exposures per run; default 5.
	PortDiscoveryMaxAuto int

	// AskHumanHold is how long an agent's turn stays live, blocked in
	// ask_human, before the engine parks the run: it cancels the turn,
	// gives the concurrency slot back, and resumes the same session with
	// the answer when it lands. Default 30m.
	AskHumanHold time.Duration
}

// Limits are the §14 caps: runs beyond MaxConcurrentRuns queue in pending
// FIFO; at MaxEnvironments live environments new tickets are refused. They
// protect the host, so they are the deployment's (the boot env), the same
// for every user. Negative means no cap; zero takes the default.
type Limits struct {
	MaxConcurrentRuns int
	MaxEnvironments   int
}

// Unlimited is the Limits value meaning "no cap".
const Unlimited = -1

// RunsCapped reports whether concurrent runs are capped at all.
func (l Limits) RunsCapped() bool { return l.MaxConcurrentRuns >= 0 }

// EnvsCapped reports whether live environments are capped at all.
func (l Limits) EnvsCapped() bool { return l.MaxEnvironments >= 0 }

// engineURLFor is the URL env's workspace reaches the engine at: the
// provisioner's own when it supplies one (a Kubernetes pod reaches a Service,
// not a loopback relay), the configured one otherwise.
func (e *Engine) engineURLFor(envp ports.EnvProvisioner, env ports.EnvID) string {
	if p, ok := envp.(ports.EngineURLProvider); ok {
		if u := p.EngineURL(env); u != "" {
			return u
		}
	}
	return e.cfg.EngineURL
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.EngineURL == "" {
		// Workspaces reach the engine through the provisioner's bridge:
		// localhost on both ends (deviation from §8's host.docker.internal
		// path — see IMPLEMENTATION_NOTES).
		out.EngineURL = "http://127.0.0.1:8080"
	}
	if out.MRPollInterval == 0 {
		out.MRPollInterval = 60 * time.Second
	}
	if out.ThreadPollInterval == 0 {
		out.ThreadPollInterval = 4 * out.MRPollInterval
	}
	if out.PortDiscoveryMaxAuto == 0 {
		out.PortDiscoveryMaxAuto = 5
	}
	if out.AskHumanHold == 0 {
		out.AskHumanHold = 30 * time.Minute
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
	bindings FlowBindings // nil = no project bindings
	// holds is the idle loop's latest observation of which phase signals
	// hold, per run (the run page's "pending" for start: manual phases).
	holds    holdsRegistry
	settings SettingsSource
	channels []ports.NotificationChannel

	clock       func() time.Time
	newID       func() string
	log         *slog.Logger
	sink        EventSink
	portRelay   *portrelay.Manager
	attachments AttachmentStore
	snapshots   SnapshotStore
	pingIndex   PingLookup
	// wakeMu serializes the dormant answer path: two humans answering the
	// two questions of one parked run must not both decide they were the
	// last, append, and wake it twice.
	wakeMu      sync.Mutex
	egressAudit EgressAuditStore
	// projectSecrets re-reads a project's secrets when an agent stage opens
	// its MCP servers; browser is the project browser and browserPorts the
	// workspace ports it reaches (mcp.go). nil = off.
	projectSecrets ProjectSecretSource
	browser        ports.Browser
	browserPorts   *portCache
	// askStore keeps the Ask panes' conversations; asks is their in-flight
	// state and askHub their viewers (ask.go).
	askStore askstore.Store
	asks     map[string]*askRun
	askHub   askHub

	mu      sync.Mutex
	closing bool // Shutdown in progress: scheduleLocked stops admitting runs
	handles map[string]*runHandle
	// live fans out ephemeral agent progress (LiveDelta) to run viewers.
	live   liveHub
	queue  []string // pending runs waiting for a concurrency slot (§14 FIFO)
	pings  map[string]*Ping
	tokens map[string]string // runID -> per-run bearer token (regenerated each boot, see notes)

	// §9 envcheck jobs: in-memory, last 20 (diagnostics, not history).
	envchecks     map[string]*envCheckState
	envcheckOrder []string

	// envStatus is the reconciled live container status per run (keyed by
	// run id), refreshed by ReconcileEnvsOnce. It is deliberately NOT part of
	// the event-sourced projection: a container stopped/removed out-of-band
	// is external state, not a run-authored transition, so it is tracked in
	// memory and surfaced to the workspace badge, never folded into the log.
	// Empty for a run means "not yet reconciled" — the read path falls back
	// to the env_id-based liveness guess.
	envStatusMu sync.Mutex
	envStatus   map[string]ports.EnvStatus
	// envDirty is, per run, whether the workspace's build inputs changed
	// since its container was built (ports.WorkspaceDirtyChecker), refreshed
	// with envStatus. Same discipline: external state, memory only.
	envDirty map[string]bool

	// resources is the per-run rolling window of workspace resource samples
	// (cpu/mem/net), appended by SampleResourcesOnce and served to the
	// run-page graphs. Memory-only like envStatus: usage is external state,
	// not a run-authored transition, so it never enters the event log. A
	// failed sample appends NOTHING — a gap in the series is honest, a
	// carried-forward value is the stale lie the reconciler comment warns
	// against.
	resourcesMu sync.Mutex
	resources   map[string][]ports.ResourceSample

	// observed is the per-run set of listening ports discovered at runtime
	// (the in-workspace shim scanner + engine-side sibling scans), surfaced on
	// the run page with click-to-expose. Memory-only and TTL-aged, like
	// envStatus: a discovered listener is external state, not a run-authored
	// transition, so it never enters the event log. Empty/stale means "no
	// live scanner" — the read path returns nothing rather than a stale lie.
	observedMu sync.Mutex
	observed   map[string]*observedRun

	// ops is the per-run in-flight workspace-operation gate (keyed by run id).
	// It serializes provision/rebuild/destroy on one env — the reaper,
	// reconciler and user actions otherwise race on the same workspace — and
	// surfaces the transitional badge (rebuilding/destroying) that wins over
	// the 30s docker-observed status. A CLAIM, not a held mutex: opMu is taken
	// only for O(1) map ops, NEVER across a Provision/Destroy/Rebuild/Status
	// call and NEVER nested with mu or envStatusMu. Memory-only like envStatus:
	// a crash mid-op leaves no stuck state (re-appears as unreconciled).
	opMu sync.Mutex
	ops  map[string]*envOp

	// appendLocks holds one mutex per run serializing its appends (see
	// appendLock): runID -> *sync.Mutex.
	appendLocks sync.Map
}

// envOp is a run's in-flight (or recently-failed) workspace operation.
type envOp struct {
	kind   string    // "provisioning" | "rebuilding" | "destroying"
	failed bool      // op ended in error; the entry lingers as the "error" badge
	at     time.Time // claim/failure time (failed-badge TTL sweep + diagnostics)
}

// envOpErrorTTL bounds how long a failed op shows the "error" badge before it
// ages out (mirrors observedTTL — a permanent error badge would be the exact
// stale-lie the reconciler comment warns against).
const envOpErrorTTL = 60 * time.Second

// ErrEnvOpInFlight refuses a workspace op when another is already running on
// the same run's env.
var ErrEnvOpInFlight = errors.New("another workspace operation is in progress")

// beginEnvOp claims the per-run workspace-op slot. It refuses with
// ErrEnvOpInFlight when a non-failed op is already in flight; a lingering
// FAILED entry does not block (a retry overwrites it). The returned release
// records the terminal outcome — success clears the slot, failure leaves the
// "error" badge for envOpErrorTTL — and is idempotent (sync.Once), so a
// double release (e.g. defer + explicit) is harmless. Callers MUST pair it
// with an immediately-following `defer rel(err)` on a named error return.
func (e *Engine) beginEnvOp(runID, kind string) (func(error), error) {
	e.opMu.Lock()
	if op := e.ops[runID]; op != nil && !op.failed {
		e.opMu.Unlock()
		return nil, fmt.Errorf("run %s: %w", runID, ErrEnvOpInFlight)
	}
	e.ops[runID] = &envOp{kind: kind, at: e.clock()}
	e.opMu.Unlock()

	var once sync.Once
	return func(opErr error) {
		once.Do(func() {
			e.opMu.Lock()
			defer e.opMu.Unlock()
			if opErr != nil {
				e.ops[runID] = &envOp{kind: kind, failed: true, at: e.clock()}
			} else {
				delete(e.ops, runID)
			}
		})
	}, nil
}

type runHandle struct {
	cancel  context.CancelCauseFunc
	mailbox chan raiseReq  // agent-raised pings (ask_human shim path)
	answers chan answerReq // answers for any of this run's open pings
	// messages carries a human's messages to the running agent. Unbuffered:
	// a send succeeds only when an agent invocation is there to take it.
	messages chan msgReq
	// holds carries a take-over (or its release) to the running agent's
	// invocation, unbuffered for the same reason.
	holds chan holdReq
	// agentLive is set while an agent invocation runs (messages can land).
	agentLive atomic.Bool
	// idle is set while the run sits between phases with its merge requests
	// open (PLAN6): the loop only polls the repository host, so it does not
	// hold a concurrency slot.
	idle atomic.Bool
	// phaseStarts carries a human's "run this phase now" to an idle loop.
	phaseStarts chan phaseStartReq
	done        chan struct{}
}

// Ping is an open (or answered) human interaction. Gate pings are raised by
// the engine at human_gate stages; agent pings arrive via RaisePing (§10).
type Ping struct {
	ID       string
	RunID    string
	Type     string // event.Ping*: gate|debug|phase_confirm|select|clarification|blocker|permission
	Question string
	Context  string
	// Files are the attached workspace paths (first = the primary reviewed
	// document); Link is a resolved URL payload (a gate's mr_url/workspace
	// sentinel). Typed at the source — never sniffed apart (DESIGN-FILES §4).
	Files   []string
	Link    string
	StageID string      // flow stage in flight when raised (PLAN4 M15)
	Form    *event.Form // what a question asks (AnswerKind form)
	// AnswerKind is the shape of the answer this ping takes (event.Answer*):
	// what the UI renders and the one thing validation reads.
	AnswerKind string
	// Phase is the phase the ping belongs to; EndsPhase, on a debug ping,
	// says giving up ends that phase run rather than the run (the card
	// words its button by it).
	Phase     string
	EndsPhase bool
	RaisedAt  time.Time

	key            string       // the raiser's idempotency key (RaisePing)
	answered       bool         // guarded by Engine.mu
	answer         *PingAnswer  // set before done closes; read only after <-done
	deliveredFiles []answerFile // the answer's attachments, where delivered; set before done closes
	done           chan struct{}
	// handedBack: the agent's turn ended without the answer — the engine
	// parked the run after the live hold (Config.AskHumanHold), or the turn
	// ended on its own. An answer arriving after that point was NEVER seen
	// by the agent, however fast it lands, so the stage must pause and
	// deliver it on resume rather than treat the question as closed. The
	// ask_human long-poll reads it (410: stop waiting). Atomic, not
	// mutex-guarded: the HTTP side reads it without the engine lock.
	handedBack atomic.Bool
}

// HandOff records how a ping's tool call ended: delivered=false when the
// agent was handed back its turn without the answer, true when the answer
// went back in-band and the agent has it.
func (p *Ping) HandOff(delivered bool) { p.handedBack.Store(!delivered) }

// HandedBack reports whether the agent was given its turn back without an
// answer — the run parked on it — i.e. whether an answer arriving now is
// news to the agent, delivered by resuming its session.
func (p *Ping) HandedBack() bool { return p.handedBack.Load() }

// Primary is the ping's primary payload for notifications and legacy JSON:
// the link, or the first attached file.
func (p *Ping) Primary() string {
	if p.Link != "" {
		return p.Link
	}
	if len(p.Files) > 0 {
		return p.Files[0]
	}
	return ""
}

// DeliveredArtifacts are the workspace paths the human's answer attachments
// were written to. Valid only after Await returns true (set before done closes).
func (p *Ping) DeliveredArtifacts() []string { return answerPaths(p.deliveredFiles) }

type PingAnswer struct {
	Answer   string // approve|reject (yes_no), accept|decline (form)
	Feedback string
	// Content is an accepted form's values, by field id (validateForm).
	Content map[string]any
	// AttachmentIDs are the answer's attached files (claimed for the ping by
	// the API before the answer is submitted); recorded on ping_answered so
	// the association is a replayable fact.
	AttachmentIDs []string
	// Review is the person's comments on the files the ping showed
	// (validateReview); Feedback is then its summary.
	Review []event.ReviewComment
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

// ComposeAnswer renders a ping's answer the way the agent that asked it should
// read it. ONE renderer for both delivery paths — the ask_human tool return
// (the shim prints this text verbatim) and the resume message of a stage that
// paused on the question — so what the agent is told never depends on which
// path the answer happened to take.
func ComposeAnswer(p *Ping) string {
	ans, ok := p.Answered()
	if !ok {
		return ""
	}
	return composeAnswerText(p.AnswerKind, p.Form, ans, p.Files, p.deliveredFiles)
}

// composeAnswerText is the renderer proper. It takes the form and the answer
// rather than a Ping because the resume path reads them off the ping_raised
// and ping_answered EVENTS — the run had no loop when the human answered, so
// there is no live ping to ask.
func composeAnswerText(kind string, form *event.Form, ans PingAnswer, shown []string, delivered []answerFile) string {
	var text string
	switch {
	case kind == event.AnswerForm:
		text = composeFormText(form, ans.Answer, ans.Feedback, ans.Content)
	case ans.Feedback != "":
		text = ans.Answer + "\n(note: " + ans.Feedback + ")"
	default:
		text = ans.Answer
	}
	// A review of the files the question showed follows the answer, each
	// comment where it was made.
	var b strings.Builder
	if len(ans.Review) > 0 {
		if text != "" {
			b.WriteString("\n\n")
		}
		named := renderReview(&b, shown, ans.Review, delivered)
		return text + strings.TrimRight(b.String(), "\n") + attachedFilesNote(unnamed(delivered, named))
	}
	return text + attachedFilesNote(answerPaths(delivered))
}

type raiseReq struct {
	p   *Ping
	ack chan struct{}
}

// answerReq hands one answer to the run's loop. ack is how the loop says what
// it did with it: nil (recorded), or an error the caller must act on. Without
// it an answer could be dropped into the buffered channel of a loop that is
// already exiting and the human would be told it landed — which is the
// failure mode this whole path exists to remove, one level down.
type answerReq struct {
	p   *Ping
	ans PingAnswer
	ack chan error // buffered 1
}

// answerSettle bounds how long AnswerPing keeps trying across a run's
// live/dormant transition before telling the human to retry.
const (
	answerSettle     = 5 * time.Second
	answerSettleStep = 50 * time.Millisecond
)

var (
	errAborted  = errors.New("run aborted")
	errPaused   = errors.New("run paused")
	errShutdown = errors.New("engine shutting down")
	// errWaitingOnHuman parks a run that has nothing left to do but wait for
	// an answer. Not a failure and not the pause button: the loop exits, the
	// concurrency slot goes back to the pool, and the answer itself brings
	// the run back (Engine.AnswerPing's dormant path). Everything needed to
	// re-enter is already in the log — that is what makes giving up the
	// goroutine safe rather than clever.
	errWaitingOnHuman = errors.New("run waiting on a human")
	// errNoLoop is internal: the run's loop went away between the handle
	// lookup and the answer reaching it, so the caller retries as dormant.
	errNoLoop = errors.New("run loop gone")
	// errRunBusy is internal: the run is mid-transition — a loop is starting,
	// or one is exiting into dormancy — and neither path owns the answer yet.
	// Retried within answerSettle rather than shown to a human, who would
	// only be told to click the same button again.
	errRunBusy = errors.New("run busy")

	// Sentinel errors for API status mapping.
	ErrRunNotActive = errors.New("run is not active")
	// ErrBuildCacheOff: this provisioner keeps no build cache of its own.
	ErrBuildCacheOff = errors.New("this provisioner has no build cache")
	ErrPingNotFound  = errors.New("ping not found")
	ErrPingAnswered  = errors.New("ping already answered")
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
		ops:       map[string]*envOp{},
		envchecks: map[string]*envCheckState{},
		envStatus: map[string]ports.EnvStatus{},
		envDirty:  map[string]bool{},
		resources: map[string][]ports.ResourceSample{},
		observed:  map[string]*observedRun{},
		askStore:  askstore.NewMemStore(),
		asks:      map[string]*askRun{},
	}
}

// runOwner is a run's owner (its run_created user_id), "" when unknown:
// the store's row when it keeps one, else the reduced log.
func (e *Engine) runOwner(ctx context.Context, runID string) string {
	if vl, ok := e.store.(run.ViewLookup); ok {
		if v, found, err := vl.RunView(ctx, runID); err == nil && found {
			return v.UserID
		}
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil || len(evs) == 0 {
		return ""
	}
	return run.Reduce(evs).UserID
}

// limits reads the deployment's caps, falling back to defaults on error
// (a broken source must not brick admission).
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

// SetPortRelay installs the workspace-port publisher; call before Recover.
// nil = port exposure off.
func (e *Engine) SetPortRelay(m *portrelay.Manager) { e.portRelay = m }

// AttachmentStore is the engine's delivery+claims port over human-given
// files (DESIGN-FILES §5.2). nil = attachments off (delivery a no-op).
// Implemented by *filestore.Attachments.
type AttachmentStore interface {
	// ClaimForRun binds still-unclaimed uploads to a run (goal files) or,
	// with a pingID, to an answer within that run. One-shot per attachment.
	ClaimForRun(ctx context.Context, ids []string, runID, pingID string) error
	// Get resolves one attachment by id — ticket attachments belong to the
	// ticket, not the run, so ForRun cannot find them; the ids ride on
	// run_created.
	Get(ctx context.Context, id string) (filestore.Attachment, error)
	Bytes(ctx context.Context, id string) ([]byte, filestore.Attachment, error)
	ForRun(ctx context.Context, runID string) ([]filestore.Attachment, error)
	ForPing(ctx context.Context, pingID string) ([]filestore.Attachment, error)
	DeleteForRun(ctx context.Context, runID string) error
}

// SnapshotStore is the engine's capture port: the engine only ever WRITES
// snapshots — reads live in the API against the concrete store. Implemented
// by *filestore.Snapshots.
type SnapshotStore interface {
	Capture(ctx context.Context, s filestore.Snapshot, scrub func(string) string, data []byte) (filestore.Snapshot, error)
}

// PingLookup reads the DURABLE ping projection. The engine needs it because a
// run parked on a human has no loop and no registry entry, yet its question
// must still be listable and answerable: the log is the only thing that knows
// about it. nil falls back to folding the event store, which is correct but
// scans (pgstore's SQL view is the real implementation).
type PingLookup interface {
	GetPing(ctx context.Context, id string) (run.PingView, error)
	// ListPings is one user's pings ("" = everyone's: the engine's own passes).
	ListPings(ctx context.Context, userID, status string, limit int) ([]run.PingView, error)
}

// SetPingLookup installs the durable ping projection; call before Recover.
func (e *Engine) SetPingLookup(l PingLookup) { e.pingIndex = l }

// pingViews lists projected pings of a status, from the index when one is
// wired and by folding the log when not.
func (e *Engine) pingViews(ctx context.Context, status string) []run.PingView {
	if e.pingIndex != nil {
		vs, err := e.pingIndex.ListPings(ctx, "", status, 500)
		if err != nil {
			e.log.Warn("ping projection list failed", "err", err)
			return nil
		}
		return vs
	}
	ids, err := e.store.RunIDs(ctx)
	if err != nil {
		e.log.Warn("ping fold: listing runs", "err", err)
		return nil
	}
	var out []run.PingView
	for _, id := range ids {
		evs, err := e.store.Events(ctx, id, 0)
		if err != nil {
			continue
		}
		for _, v := range run.ReducePings(evs) {
			if v.Status == status {
				out = append(out, v)
			}
		}
	}
	return out
}

// pingView resolves one projected ping by id.
func (e *Engine) pingView(ctx context.Context, id string) (run.PingView, bool) {
	if e.pingIndex != nil {
		v, err := e.pingIndex.GetPing(ctx, id)
		return v, err == nil && v.PingID != ""
	}
	for _, v := range e.pingViews(ctx, "pending") {
		if v.PingID == id {
			return v, true
		}
	}
	return run.PingView{}, false
}

// SetFiles installs the file stores; call before Recover. nil disables the
// respective half (delivery no-ops / captures skipped).
func (e *Engine) SetFiles(a AttachmentStore, s SnapshotStore) {
	e.attachments = a
	e.snapshots = s
}

// EgressAuditStore persists and queries the durable egress audit trail (what
// each workspace reached / was blocked from). nil = audit off. Implemented by
// *pgstore.Store.
type EgressAuditStore interface {
	UpsertEgressAudit(ctx context.Context, rows []ports.EgressAuditRow) error
	QueryEgressAudit(ctx context.Context, f ports.EgressAuditFilter) ([]ports.EgressAuditRow, error)
}

// SetEgressAudit installs the audit store; call before Recover. nil disables.
func (e *Engine) SetEgressAudit(s EgressAuditStore) { e.egressAudit = s }

// ErrPortsOff marks an expose request when no port relay is configured.
var ErrPortsOff = errors.New("port exposure is not configured")

// ExposeRunPort publishes a workspace port of a live run and returns the
// browser URL (the run-page "open the app" path). Idempotent per
// (run, port). The live listener is engine-process-scoped and never
// event-recorded; only the host-port ALLOCATION is persisted (portrelay's
// Store), so the same URL comes back after a restart. See portrelay's
// package comment.
func (e *Engine) ExposeRunPort(ctx context.Context, runID string, port int) (string, error) {
	if e.portRelay == nil {
		return "", ErrPortsOff
	}
	if url := e.portRelay.Existing(runID, port); url != "" {
		return url, nil
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return "", err
	}
	if len(evs) == 0 {
		return "", fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.EnvID == "" || v.ProjectID == "" {
		return "", fmt.Errorf("run %s has no environment", runID)
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return "", err
	}
	defer res.release()
	exposer, ok := res.Envp.(ports.WorkspacePortExposer)
	if !ok {
		return "", fmt.Errorf("run %s: provisioner does not support port exposure", runID)
	}
	pc, err := exposer.ExposePort(ctx, ports.EnvID(v.EnvID), port)
	if err != nil {
		return "", err
	}
	return e.portRelay.Expose(runID, port, pc)
}

// RunPorts lists a run's live exposures (the run-page ports panel).
func (e *Engine) RunPorts(runID string) []portrelay.Exposure {
	if e.portRelay == nil {
		return nil
	}
	return e.portRelay.List(runID)
}

// envNameForRun returns the run's env id, or — while it is still provisioning
// (EnvReady not yet emitted) — the deterministic workspace name the provisioner
// will use. This lets egress control and the workspace-log tail work DURING the
// build, not only after the env is ready. Provisioners whose env id isn't this
// name simply see a name that doesn't match, degrading to their pre-ready state.
func (e *Engine) envNameForRun(v run.View, runID string) string {
	if v.EnvID != "" {
		return v.EnvID
	}
	nameBranch := v.BranchBase
	if nameBranch == "" {
		nameBranch = v.Branch
	}
	return workspaceName(nameBranch, runID)
}

// egressCtrl resolves a run to its provisioner's egress controller (the
// per-project gateway), or ErrEgressOff when the run isn't live or the
// provisioner has no gateway.
func (e *Engine) egressCtrl(ctx context.Context, runID string) (ports.EgressController, ports.EnvID, func(), error) {
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return nil, "", nil, err
	}
	if len(evs) == 0 {
		return nil, "", nil, fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.ProjectID == "" {
		return nil, "", nil, fmt.Errorf("run %s: %w", runID, ErrEgressOff)
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, "", nil, err
	}
	ctrl, ok := res.Envp.(ports.EgressController)
	if !ok {
		res.release()
		return nil, "", nil, ErrEgressOff
	}
	// Works during provisioning too (EnvID not yet set): the most important time
	// to "add to egress" is when the build is blocked on a denied domain.
	return ctrl, ports.EnvID(e.envNameForRun(v, runID)), res.release, nil
}

// buildCacheCtrl resolves a project to its provisioner's build-cache
// controller. ErrBuildCacheOff when the provisioner has none — a k8s or remote
// provisioner may cache elsewhere, or not at all.
// It returns the project id alongside: the gateway that holds the cache is
// keyed by it (the same value Provision stamped on the run's EnvSpec).
func (e *Engine) buildCacheCtrl(ctx context.Context, projectID string) (ports.BuildCacheController, string, func(), error) {
	res, err := e.resolver.Resolve(ctx, projectID)
	if err != nil {
		return nil, "", nil, err
	}
	ctrl, ok := res.Envp.(ports.BuildCacheController)
	if !ok {
		res.release()
		return nil, "", nil, ErrBuildCacheOff
	}
	return ctrl, res.ProjectID, res.release, nil
}

// BuildCache reports what the project's devcontainer build cache holds. The
// cache is per project and outlives its runs: it is what stops every run
// rebuilding the image into its own throwaway daemon.
func (e *Engine) BuildCache(ctx context.Context, projectID string) (ports.BuildCacheInfo, error) {
	ctrl, project, release, err := e.buildCacheCtrl(ctx, projectID)
	if err != nil {
		return ports.BuildCacheInfo{}, err
	}
	defer release()
	return ctrl.BuildCache(ctx, project)
}

// ClearBuildCache empties it. Safe by construction — the next build refills it,
// so the cost of clearing when you did not need to is one slow build.
func (e *Engine) ClearBuildCache(ctx context.Context, projectID string) error {
	ctrl, project, release, err := e.buildCacheCtrl(ctx, projectID)
	if err != nil {
		return err
	}
	defer release()
	return ctrl.ClearBuildCache(ctx, project)
}

// EgressDenials lists a run's gateway-blocked hosts (run page feedback).
func (e *Engine) EgressDenials(ctx context.Context, runID string) []ports.EgressDenial {
	ctrl, env, release, err := e.egressCtrl(ctx, runID)
	if err != nil {
		return nil
	}
	defer release()
	return ctrl.EgressDenials(ctx, env)
}

// AllowEgress adds a host to a run's project egress policy live — the gateway
// hot-reloads it, so the next request succeeds with no restart. Project-wide by
// design. Returns ErrEgressOff when the run isn't live / has no gateway.
// Persistence to the project (future runs) is the caller's job.
func (e *Engine) AllowEgress(ctx context.Context, runID, host string) error {
	ctrl, env, release, err := e.egressCtrl(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	return ctrl.AllowEgress(ctx, env, host)
}

// SetEgressRule live-toggles a host's egress rule for a run (allow or force-deny),
// project-wide via the gateway, no restart. Returns ErrEgressOff when the run has
// no gateway. Persistence to the project is the caller's job.
func (e *Engine) SetEgressRule(ctx context.Context, runID, host string, allow bool) error {
	ctrl, env, release, err := e.egressCtrl(ctx, runID)
	if err != nil {
		return err
	}
	defer release()
	return ctrl.SetEgressRule(ctx, env, host, allow)
}

// RunEgressPolicy returns the egress policy the gateway enforces for a run
// RIGHT NOW — the run page renders each host's current rule from it (the
// audit trail only records past verdicts, which never change when the rule
// does). Returns ErrEgressOff when the run has no live gateway.
func (e *Engine) RunEgressPolicy(ctx context.Context, runID string) (ports.EgressPolicy, error) {
	ctrl, env, release, err := e.egressCtrl(ctx, runID)
	if err != nil {
		return ports.EgressPolicy{}, err
	}
	defer release()
	return ctrl.EgressPolicy(ctx, env)
}

// auditRows converts a run's provisioner audit records into durable rows tagged
// with the project + engine run id. Empty when the provisioner has no auditor.
// Uses envNameForRun so it works DURING provisioning too (EnvID not yet set).
func (e *Engine) auditRows(ctx context.Context, v run.View, envp ports.EnvProvisioner) []ports.EgressAuditRow {
	auditor, ok := envp.(ports.EgressAuditor)
	if !ok {
		return nil
	}
	recs := auditor.EgressAudit(ctx, ports.EnvID(e.envNameForRun(v, v.RunID)))
	rows := make([]ports.EgressAuditRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, ports.EgressAuditRow{
			Project: v.ProjectID, Run: v.RunID, Host: r.Host, DstIP: r.DstIP, Port: r.Port,
			Proto: r.Proto, Verdict: r.Verdict, Count: r.Count, BytesOut: r.BytesOut,
			BytesIn: r.BytesIn, First: r.First, Last: r.Last,
		})
	}
	return rows
}

// IngestEgressAuditOnce pulls the aggregated egress audit of every ACTIVE run
// (from the in-memory handle set — cheap, no full-history scan) and upserts it
// into durable storage. This is the SINGLE audit path: the gateway writes a file
// (the only channel out of the container), the engine ingests it to the DB, and
// the UI reads the DB — so every audit fact is PERSISTED before it is delivered.
// Run on a fast ticker for near-live freshness, and once more before env destroy.
func (e *Engine) IngestEgressAuditOnce(ctx context.Context) error {
	if e.egressAudit == nil {
		return nil
	}
	e.mu.Lock()
	ids := make([]string, 0, len(e.handles))
	for id := range e.handles {
		ids = append(ids, id)
	}
	e.mu.Unlock()

	var rows []ports.EgressAuditRow
	for _, id := range ids {
		evs, err := e.store.Events(ctx, id, 0)
		if err != nil {
			continue
		}
		v := run.Reduce(evs)
		if v.ProjectID == "" {
			continue
		}
		res, err := e.resolver.Resolve(ctx, v.ProjectID)
		if err != nil {
			continue
		}
		rows = append(rows, e.auditRows(ctx, v, res.Envp)...)
		res.release()
	}
	if len(rows) == 0 {
		return nil
	}
	return e.egressAudit.UpsertEgressAudit(ctx, rows)
}

// RunEgressAudit returns a run's egress audit from the durable store — the
// single source of truth, kept near-live by IngestEgressAuditOnce. Filterable.
func (e *Engine) RunEgressAudit(ctx context.Context, runID, verdict, host string, limit int) ([]ports.EgressAuditRow, error) {
	if e.egressAudit == nil {
		return nil, nil
	}
	return e.egressAudit.QueryEgressAudit(ctx, ports.EgressAuditFilter{Run: runID, Verdict: verdict, Host: host, Limit: limit})
}

// ProjectEgressAudit returns the durable audit rows for a whole project (filterable).
func (e *Engine) ProjectEgressAudit(ctx context.Context, projectID, verdict, host string, limit int) ([]ports.EgressAuditRow, error) {
	if e.egressAudit == nil {
		return nil, nil
	}
	return e.egressAudit.QueryEgressAudit(ctx, ports.EgressAuditFilter{Project: projectID, Verdict: verdict, Host: host, Limit: limit})
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
	unlock := e.appendLock(runID)
	evs, err := e.store.Append(ctx, runID, ps...)
	unlock()
	if err != nil {
		return err
	}
	e.emit(evs)
	return nil
}

// CreateRun is CreateRunWith on a ticket, with the project's parameter
// bindings and nothing overridden.
func (e *Engine) CreateRun(ctx context.Context, t Ticket, flowName, projectID string) (string, error) {
	return e.CreateRunWith(ctx, t, flowName, projectID, RunInput{})
}

// CreateRunWith performs §1 preflight against the project's resolved
// configuration and, if it passes, appends run_created and starts the run's
// writer goroutine. flowName "" means the project's default flow; in.MR the
// merge request a flow with operates_on: merge_request is started on, and
// in.Params this attempt's parameter overrides, on top of the project's
// bindings. The flow
// is resolved with those values before preflight, so a bound harness, model
// or run_ref is checked exactly like a literal one. Preflight failures
// refuse the run naming the exact missing item — no events are written.
func (e *Engine) CreateRunWith(ctx context.Context, t Ticket, flowName, projectID string, in RunInput) (string, error) {
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
	fl, err := e.flows.Get(ctx, projectID, flowName)
	if err != nil {
		return "", fmt.Errorf("flow %q: %w", flowName, err)
	}
	if err := startsOn(fl, in.MR != nil); err != nil {
		return "", err
	}
	params, err := e.resolveParams(ctx, fl, projectID, in.Params)
	if err != nil {
		return "", fmt.Errorf("preflight: parameters: %w", err)
	}
	if fl, err = fl.Resolve(params); err != nil {
		return "", fmt.Errorf("preflight: parameters: %w", err)
	}
	// A model or a harness the flow left to the project, still unbound: the
	// run cannot say what its agents run on. Refused here, before any
	// workspace, naming where a value comes from.
	if err := flow.UnboundError(fl.Unbound()); err != nil {
		return "", fmt.Errorf("preflight: parameters: %w", err)
	}
	if err := e.preflight(ctx, res, fl); err != nil {
		return "", err
	}
	if err := e.preflightTicket(ctx, res, fl, t); err != nil {
		return "", err
	}
	e.warmBrowser(res, fl)
	by := in.By
	if by == "" {
		by = "human"
	}

	if err := e.checkEnvCapacity(ctx, res.UserID); err != nil {
		return "", err
	}

	runID := e.newID()
	// base is the branch stem; the run's ACTUAL branch appends a per-run tail
	// when the run belongs to a ticket, so each attempt is a unique, readable
	// branch (no remote collisions) and the container name — slugified from
	// the base — stays sane. The direct/test path (no ticket) is unchanged.
	base := strings.TrimSpace(t.Branch)
	branch := base
	if t.TicketID != "" {
		if base == "" {
			base = "apothecary/" + slug(t.Title, maxBranchSlug)
		}
		branch = base + "-" + runIDTail(runID)
		if err := CheckBranch(branch); err != nil {
			return "", fmt.Errorf("branch %q: %v", branch, err)
		}
	} else if branch == "" {
		base = ""
		branch = fmt.Sprintf("apothecary/%s-%s", strings.ToLower(runID), slug(t.Title, maxBranchSlug))
	} else if err := CheckBranch(branch); err != nil {
		return "", fmt.Errorf("branch %q: %v", branch, err)
	}
	// SourceBranch is the base this run forks off from; empty falls back to the
	// project's default branch (itself "main" by default).
	sourceBranch := strings.TrimSpace(t.SourceBranch)
	if sourceBranch == "" {
		sourceBranch = res.RepoRef.DefaultBranch
	} else if err := CheckBranch(sourceBranch); err != nil {
		return "", fmt.Errorf("source branch %q: %v", sourceBranch, err)
	}
	checkouts := runCheckouts(res, branch, sourceBranch)
	var adopted *event.AdoptedMR
	if in.MR != nil {
		// Created on someone's merge request: the run works on its branch,
		// checked out from the ref the repository host keeps for its head.
		var co *event.Checkout
		if co, adopted, err = adoptMR(ctx, res, checkouts, in.MR); err != nil {
			return "", fmt.Errorf("preflight: %w", err)
		}
		if co.Primary {
			branch, base, sourceBranch = co.Branch, "", co.Source
		}
	}
	if err := e.appendAndEmit(ctx, runID, &event.RunCreated{
		TicketTitle:         t.Title,
		TicketGoal:          t.Goal,
		RunGoal:             t.RunGoal,
		FlowName:            fl.Name,
		FlowHash:            fl.Hash,
		RepoURL:             res.RepoRef.URL,
		Branch:              branch,
		BranchBase:          base,
		SourceBranch:        sourceBranch,
		Checkouts:           checkouts,
		ProjectID:           projectID,
		UserID:              res.UserID,
		TicketID:            t.TicketID,
		TicketAttachmentIDs: t.TicketAttachmentIDs,
		TicketSourceID:      t.SourceID,
		TicketSourceKind:    t.SourceKind,
		TicketSourceKey:     t.SourceKey,
		TicketSourceURL:     t.SourceURL,
		Params:              params,
		By:                  by,
		MR:                  adopted,
	}); err != nil {
		return "", err
	}
	if e.attachments != nil && len(t.AttachmentIDs) > 0 {
		if err := e.attachments.ClaimForRun(ctx, t.AttachmentIDs, runID, ""); err != nil {
			e.log.Warn("claim goal attachments", "run", runID, "err", err)
		}
	}
	lim := e.limits(ctx)
	e.mu.Lock()
	e.queue = append(e.queue, runID)
	e.scheduleLocked(lim)
	e.mu.Unlock()
	return runID, nil
}

// runCheckouts is what a new run checks out: the primary on the branch it
// forks from, and every extra repo on the same branch name forked from its
// own default branch.
func runCheckouts(res *Resolution, branch, sourceBranch string) []event.Checkout {
	out := []event.Checkout{{Name: event.PrimaryCheckoutName, Dir: event.PrimaryCheckoutDir,
		URL: res.RepoRef.URL, Locator: res.RepoRef.Locator, Branch: branch, Source: sourceBranch, Primary: true}}
	for _, x := range res.ExtraRepos {
		out = append(out, event.Checkout{Name: x.Name, Dir: "repos/" + x.Name,
			URL: x.URL, Locator: x.Locator, Branch: branch, Source: x.DefaultBranch})
	}
	return out
}

// adoptMR reads the merge request a run is being created on and points its
// checkout at it: the branch is the merge request's own (a fork's, which has
// none here, a local mr-<iid>), forked from nothing — its head is fetched from
// the repository host's ref — and its target is what it merges into.
func adoptMR(ctx context.Context, res *Resolution, checkouts []event.Checkout, want *RunMR) (*event.Checkout, *event.AdoptedMR, error) {
	repo := want.Repo
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	var co *event.Checkout
	for i := range checkouts {
		if checkouts[i].Name == repo {
			co = &checkouts[i]
		}
	}
	if co == nil {
		return nil, nil, fmt.Errorf("merge request: the project has no repository %q", repo)
	}
	repoHost := res.repoHostFor(repo)
	if repoHost == nil {
		return nil, nil, fmt.Errorf("merge request: no integration reaches repository %q", repo)
	}
	info, err := repoHost.GetMR(ctx, want.IID)
	if err != nil {
		return nil, nil, fmt.Errorf("merge request !%d: %w", want.IID, err)
	}
	if info.State != ports.MROpen {
		return nil, nil, fmt.Errorf("merge request %s is %s: a run is created on an open one", info.Ref.URL, info.State)
	}
	co.Branch, co.Source, co.Fetch, co.Fork = info.Source, info.Target, info.HeadRef, info.Fork
	if info.Fork || CheckBranch(info.Source) != nil {
		co.Branch = fmt.Sprintf("mr-%d", info.Ref.IID)
	}
	return co, &event.AdoptedMR{Repo: repo, IID: info.Ref.IID, URL: info.Ref.URL, Head: info.Head, Author: info.Author, Title: info.Title}, nil
}

// ListMRs is the open merge requests of a project's repository ("" = the
// primary), for a person picking one to start a run on.
func (e *Engine) ListMRs(ctx context.Context, projectID, repo string) ([]ports.MRInfo, error) {
	res, err := e.resolver.Resolve(ctx, projectID)
	if err != nil {
		return nil, err
	}
	defer res.release()
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	repoHost := res.repoHostFor(repo)
	if repoHost == nil {
		return nil, fmt.Errorf("no integration reaches repository %q", repo)
	}
	return repoHost.ListMRs(ctx)
}

// MR reads one merge request of a project's repository ("" = the primary).
func (e *Engine) MR(ctx context.Context, projectID, repo string, iid int) (ports.MRInfo, error) {
	res, err := e.resolver.Resolve(ctx, projectID)
	if err != nil {
		return ports.MRInfo{}, err
	}
	defer res.release()
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	repoHost := res.repoHostFor(repo)
	if repoHost == nil {
		return ports.MRInfo{}, fmt.Errorf("no integration reaches repository %q", repo)
	}
	return repoHost.GetMR(ctx, iid)
}

// RepoHostUser is who a project's repository host integration acts as in a
// repository ("" = the primary): the account the factory's own merge requests
// are opened by.
func (e *Engine) RepoHostUser(ctx context.Context, projectID, repo string) (string, error) {
	res, err := e.resolver.Resolve(ctx, projectID)
	if err != nil {
		return "", err
	}
	defer res.release()
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	repoHost := res.repoHostFor(repo)
	if repoHost == nil {
		return "", fmt.Errorf("no integration reaches repository %q", repo)
	}
	return repoHost.Whoami(ctx)
}

// ErrEnvCapacity refuses new tickets at the §14 environment cap (HTTP 409).
var ErrEnvCapacity = errors.New("environment capacity reached")

// checkEnvCapacity counts live environments (env_ready without a later
// env_destroyed) across all runs; at the cap it refuses and raises a
// blocker notification, to the would-be run's owner, naming the runs whose
// archiving would free envs.
func (e *Engine) checkEnvCapacity(ctx context.Context, userID string) error {
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
	lim := e.limits(ctx)
	maxEnvs := lim.MaxEnvironments
	if !lim.EnvsCapped() || live < maxEnvs {
		return nil
	}
	e.notify(ctx, ports.Notification{
		UserID: userID,
		Type:   "blocker",
		Title:  fmt.Sprintf("environment capacity reached (%d/%d)", live, maxEnvs),
		Body:   "archive finished runs to free environments: " + strings.Join(archivable, ", "),
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
	if e.attachments != nil {
		if err := e.attachments.DeleteForRun(ctx, runID); err != nil {
			e.log.Warn("archive: attachment cleanup failed", "run", runID, "err", err)
		}
	}
	return nil
}

// Rerun starts a terminal run over from scratch: a FRESH run (new
// id, branch, workspace, events) with the original ticket, project and
// flow name — resolved against the CURRENT config and flow version, and
// re-preflighted like any new ticket. History is immutable; the old run
// keeps its record. `in` carries this attempt's optional direction, extra
// attachments and flow override — a rerun after a failure is exactly when a
// human wants to steer the next try.
//
// Vocabulary: "rerun" is a new run of the same ticket, "retry" (RetryRun)
// re-enters a failed stage in place, "play" continues a paused run, and
// "restart" is reserved for the engine PROCESS restarting (recovery).
func (e *Engine) Rerun(ctx context.Context, runID string, in RunInput) (string, error) {
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
	// A ticketed run restarts as a fresh attempt under the same ticket: reuse
	// the ticket id + base branch, and CreateRun derives a new per-run tail
	// (new unique branch, same readable stem). Legacy runs with no ticket keep
	// the old behavior: preserve a user branch, but regenerate an auto branch
	// so the OLD run's id doesn't get baked into the new run's name.
	flowName := in.Flow
	if flowName == "" {
		flowName = v.FlowName
	}
	// A restart reuses the original run's source branch unless overridden.
	sourceBranch := v.SourceBranch
	if in.SourceBranch != "" {
		sourceBranch = in.SourceBranch
	}
	// The attempt runs with the original's parameter values — unless the
	// flow changed, or the request says otherwise. Values the flow no
	// longer declares drop.
	next := RunInput{Params: map[string]string{}, By: in.By, MR: in.MR}
	// A run started on someone's merge request is rerun on it.
	if next.MR == nil && v.Adopted != nil {
		next.MR = &RunMR{Repo: v.Adopted.Repo, IID: v.Adopted.IID}
	}
	if in.Flow == "" || in.Flow == v.FlowName {
		if fl, ferr := e.flows.Get(ctx, v.ProjectID, flowName); ferr == nil {
			for k, val := range v.Params {
				if fl.Param(k) != nil {
					next.Params[k] = val
				}
			}
		}
	}
	maps.Copy(next.Params, in.Params)
	if v.TicketID != "" {
		return e.CreateRunWith(ctx, Ticket{
			TicketID: v.TicketID, Title: v.TicketTitle, Goal: v.TicketGoal, Branch: v.BranchBase,
			SourceBranch: sourceBranch,
			RunGoal:      in.Goal, AttachmentIDs: in.AttachmentIDs,
			// The ticket's own attachments carry over to every attempt, and so
			// does the tracker issue it was imported from.
			TicketAttachmentIDs: v.TicketAttachmentIDs,
			SourceID:            v.TicketSourceID,
			SourceKind:          v.TicketSourceKind,
			SourceKey:           v.TicketKey,
			SourceURL:           v.TicketURL,
		}, flowName, v.ProjectID, next)
	}
	branch := v.Branch
	if branch == "" || strings.HasPrefix(branch, "apothecary/"+strings.ToLower(runID)+"-") {
		branch = ""
	}
	return e.CreateRunWith(ctx, Ticket{
		Title: v.TicketTitle, Goal: v.TicketGoal, Branch: branch,
		SourceBranch: sourceBranch,
		RunGoal:      in.Goal, AttachmentIDs: in.AttachmentIDs,
	}, flowName, v.ProjectID, next)
}

// RetryRun re-enters the stage where an abandoned/failed run stalled,
// in the SAME workspace ("try again"): the plan/build/earlier stages and
// the agent's commits are reused; only the failed stage re-runs, with
// retry budgets reset. Distinct from Rerun (a fresh run from stage
// one). Refused when the run is active, not terminal, completed/archived,
// has no stalled stage, or its workspace is gone.
func (e *Engine) RetryRun(ctx context.Context, runID string) error {
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
		return fmt.Errorf("run %s is %s; only abandoned or failed runs can be retried (completed runs have nothing to retry)", runID, v.State)
	}
	if v.CurrentStage == "" {
		return fmt.Errorf("run %s has no stage to retry (it never started one); rerun it as a new run instead", runID)
	}
	if v.EnvID == "" {
		return fmt.Errorf("run %s has no workspace to retry in; rerun it as a new run instead", runID)
	}
	// Re-pin to the flow's current version: a retry is human-initiated, so it
	// is where an edited flow (an edited prompt, a fixed binding) legitimately
	// reaches a run in flight — unlike crash recovery or play, which must
	// never change behavior underneath a running stage (PLAN5 M19). A flow
	// that no longer resolves keeps the old pin rather than blocking the retry.
	hash := ""
	if fl, ferr := e.flows.Get(ctx, v.ProjectID, v.FlowName); ferr == nil {
		if fl.Stage(v.CurrentStage) == nil {
			return fmt.Errorf("run %s cannot resume: flow %q no longer has a stage %q — rerun it as a new run instead",
				runID, v.FlowName, v.CurrentStage)
		}
		if _, rerr := fl.Resolve(v.Params); rerr != nil {
			e.log.Warn("retry keeps the pinned flow version: the current one does not resolve with the run's parameters",
				"run", runID, "flow", v.FlowName, "err", rerr)
		} else {
			hash = fl.Hash
		}
	}
	if err := e.appendAndEmit(ctx, runID, &event.RunRetried{StageID: v.CurrentStage, FlowHash: hash}); err != nil {
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
func (e *Engine) destroyEnv(ctx context.Context, v run.View) (derr error) {
	// Claim the op-gate first: this is the single choke point for env
	// destruction (Archive + the reaper both funnel here), so the claim
	// serializes destroy against a concurrent rebuild and against each other.
	// A rebuild in flight refuses the destroy (reaper skips this pass, Archive
	// surfaces the error) rather than yanking ports/containers mid-rebuild.
	rel, err := e.beginEnvOp(v.RunID, "destroying")
	if err != nil {
		return err
	}
	defer func() { rel(derr) }()

	if e.portRelay != nil {
		e.portRelay.CloseRun(v.RunID) // exposures die with the env
	}
	e.browserPorts.forget(v.RunID) // and so do the browser's
	e.forgetObserved(v.RunID)      // discovered-port snapshot dies with the env
	if v.ProjectID == "" {
		return fmt.Errorf("run %s has no project (pre-v2); destroy its environment manually", v.RunID)
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return err
	}
	defer res.release()
	// Final audit flush before the env (and possibly the project gateway) is
	// torn down, so the trail is durable even for the last activity.
	if e.egressAudit != nil {
		if rows := e.auditRows(ctx, v, res.Envp); len(rows) > 0 {
			_ = e.egressAudit.UpsertEgressAudit(ctx, rows)
		}
	}
	return res.Envp.Destroy(ctx, ports.EnvID(v.EnvID))
}

// preflight refuses a run naming the exact missing item (§1, extended by
// DESIGN2 §6.3): repository host access, repo files, run_ref resolution, and
// the harness pin check. Model strings are deliberately NOT validated against
// provider kinds — harnesses route provider/model themselves (openrouter
// serves anthropic/*), so any check would false-negative. hasDevcontainer
// reports whether the repo carries a devcontainer config at either standard
// path — the workspace definition apothecary requires.
func hasDevcontainer(ctx context.Context, res *Resolution) (bool, error) {
	for _, p := range []string{".devcontainer/devcontainer.json", ".devcontainer.json"} {
		ok, err := res.RepoHost.FileExists(ctx, res.RepoRef, p)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (e *Engine) preflight(ctx context.Context, res *Resolution, fl *flow.Flow) error {
	if err := res.RepoHost.CheckAccess(ctx, res.RepoRef); err != nil {
		return fmt.Errorf("preflight: repository access: %w", err)
	}
	for _, x := range res.ExtraRepos {
		if f := res.RepoHosts[x.Name]; f != nil {
			if err := f.CheckAccess(ctx, ports.RepoRef{URL: x.URL, DefaultBranch: x.DefaultBranch, Locator: x.Locator}); err != nil {
				return fmt.Errorf("preflight: repository access to repo %s: %w", x.Name, err)
			}
		}
	}
	// AGENTS.md stays a repo file (it describes the repo); agents themselves
	// live in the project's library since v3 — the .apothecary/agents/ repo
	// contract is retired.
	ok, err := res.RepoHost.FileExists(ctx, res.RepoRef, res.AgentsMDPath)
	if err != nil {
		return fmt.Errorf("preflight: checking %s: %w", res.AgentsMDPath, err)
	}
	if !ok {
		return fmt.Errorf("preflight: %s not found in repo", res.AgentsMDPath)
	}
	// Every workspace runs the repo's OWN devcontainer — there is no
	// apothecary-built fallback image.
	if dc, derr := hasDevcontainer(ctx, res); derr != nil {
		return fmt.Errorf("preflight: checking for a devcontainer: %w", derr)
	} else if !dc {
		return fmt.Errorf("preflight: repo has no .devcontainer/devcontainer.json — apothecary runs the repo's own devcontainer")
	}
	for i := range fl.Phases {
		if err := res.phaseAvailable(&fl.Phases[i]); err != nil {
			return fmt.Errorf("preflight: %s: %w", fl.Phases[i].Label(), err)
		}
	}
	if err := res.servicesAvailable(fl); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	// The Ask pane's harness (FLOWLANG §1 ask) is checked like a stage's.
	if fl.Ask != nil && !res.harnessInSet(fl.Ask.Harness) {
		return fmt.Errorf("ask: harness %q is not in the project's harness set %v", fl.Ask.Harness, res.harnessSet())
	}
	for i := range fl.Stages {
		st := &fl.Stages[i]
		if st.Type == flow.StageCommand && st.RunRef != "" {
			if _, ok := res.Commands[st.RunRef]; !ok {
				return fmt.Errorf("preflight: stage %q: run_ref %q not found in project.commands", st.ID, st.RunRef)
			}
		}
		// Agents need no preflight: since v3 the flow declares them, so a
		// stage naming an undeclared agent is a parse error (FLOWLANG §1).
		// Per-step harness (§6.3): a stage may pin any harness KIND in the
		// project's set; one outside it is refused here, not at parse time —
		// flows are portable data.
		if st.Type == flow.StageAgent && st.Harness != "" && !res.harnessInSet(st.Harness) {
			return fmt.Errorf("preflight: stage %q pins harness %q, which is not in the project's harness set %v", st.ID, st.Harness, res.harnessSet())
		}
		// Actions and signals resolve like agents and run_refs: the flow names
		// one, the project must have the integration it needs.
		if st.Type == flow.StageAction {
			if err := res.actionAvailable(st); err != nil {
				return fmt.Errorf("preflight: stage %q: %w", st.ID, err)
			}
		}
		for _, slug := range []string{st.WaitFor, st.AutoApprove, st.AutoReject} {
			if slug == "" {
				continue
			}
			if err := res.signalAvailable(slug); err != nil {
				return fmt.Errorf("preflight: stage %q: %w", st.ID, err)
			}
		}
	}
	if err := res.mcpAvailable(fl, e.browser != nil); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	// Every agent stage's model must be one its harness can reach — checked
	// now, before a workspace is built, not when the stage opens its session.
	// A list that cannot be fetched is not a reason to refuse the run.
	mc := checkModels(ctx, res, fl)
	if len(mc.missing) > 0 {
		return fmt.Errorf("preflight: %s", strings.Join(mc.missing, "; "))
	}
	if len(mc.unverified) > 0 {
		e.log.Warn("preflight: could not verify the flow's models", "flow", fl.Name,
			"harnesses", strings.Join(mc.unverified, "; "))
	}
	// And the providers must accept the harness credentials — probed without
	// spending tokens. A probe that says nothing is not a reason to refuse.
	rejected, unverified := checkAuth(ctx, res, fl.Stages)
	if len(rejected) > 0 {
		return fmt.Errorf("preflight: %s", strings.Join(rejected, "; "))
	}
	if len(unverified) > 0 {
		e.log.Warn("preflight: could not verify harness credentials", "flow", fl.Name,
			"why", strings.Join(unverified, "; "))
	}
	// The agent I/O contract is enforced at parse time now that the flow
	// carries its agents; what is left here is advisory.
	if len(fl.Warnings) > 0 {
		e.log.Warn("flow warnings", "flow", fl.Name, "count", len(fl.Warnings), "first", fl.Warnings[0])
	}
	return nil
}

// RunsOfTicket lists the runs created for a ticket, newest last — what an
// automation asks before starting a run for a ticket it imported earlier.
func (e *Engine) RunsOfTicket(ctx context.Context, ticketID string) ([]run.View, error) {
	all, err := e.views(ctx)
	if err != nil {
		return nil, err
	}
	var out []run.View
	for _, v := range all {
		if v.TicketID == ticketID {
			out = append(out, v)
		}
	}
	return out, nil
}

// views is every run's read-model row: one query when the store keeps the
// projection (run.ViewSource), else each run's log reduced — the in-memory
// store's path, and the reference the projection is checked against.
func (e *Engine) views(ctx context.Context) ([]run.View, error) {
	if vs, ok := e.store.(run.ViewSource); ok {
		return vs.RunViews(ctx)
	}
	ids, err := e.store.RunIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]run.View, 0, len(ids))
	for _, id := range ids {
		evs, err := e.store.Events(ctx, id, 0)
		if err != nil {
			return nil, err
		}
		if len(evs) == 0 {
			continue
		}
		out = append(out, run.Reduce(evs))
	}
	return out, nil
}

// Recover replays events on boot (§2) and restarts every non-terminal run.
// Runs that already hold resources (provisioning and beyond) restart
// unconditionally; still-pending runs re-queue FIFO by creation order and
// start as §14 slots allow. A workspace rebuild the previous process never
// finished is closed as failed first: nothing is rebuilding it anymore.
func (e *Engine) Recover(ctx context.Context) error {
	views, err := e.views(ctx)
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
	for _, v := range views {
		id := v.RunID
		if v.Rebuilding {
			// The op-gate that owned this rebuild died with the process, so
			// the record would otherwise show a rebuild that never ends.
			// Appending here is safe: a rebuild only runs on a run with no
			// loop, and this one has none yet.
			if err := e.appendAndEmit(ctx, id, &event.EnvRebuildFailed{EnvID: v.EnvID,
				Error: "interrupted: the engine restarted while the workspace was being rebuilt"}); err != nil {
				return err
			}
		}
		if v.State.Terminal() {
			continue
		}
		if v.Dormant {
			// Parked: either the user pressed pause (only play re-admits it)
			// or the run is waiting on a human. A waiting run whose answers
			// have all landed IS owed a loop, though — that is the crash
			// window between recording an answer and re-queuing the run, and
			// skipping it here would park the run forever.
			if v.State == run.Paused {
				continue
			}
			evs, err := e.store.Events(ctx, id, 0)
			if err != nil {
				return err
			}
			if !answersAllIn(evs) {
				continue
			}
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
	for (!lim.RunsCapped() || e.busyLocked() < lim.MaxConcurrentRuns) && len(e.queue) > 0 {
		id := e.queue[0]
		e.queue = e.queue[1:]
		e.startLocked(id)
	}
}

// busyLocked counts the resident loops that hold a concurrency slot: every
// one except runs idling between phases.
func (e *Engine) busyLocked() int {
	n := 0
	for _, h := range e.handles {
		if !h.idle.Load() {
			n++
		}
	}
	return n
}

func (e *Engine) startLocked(runID string) {
	if _, exists := e.handles[runID]; exists {
		return
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	h := &runHandle{
		cancel:      cancel,
		mailbox:     make(chan raiseReq, 8),
		answers:     make(chan answerReq, 8),
		messages:    make(chan msgReq),
		holds:       make(chan holdReq),
		phaseStarts: make(chan phaseStartReq),
		done:        make(chan struct{}),
	}
	e.handles[runID] = h
	go e.runLoop(ctx, runID, h)
}

// Abort cancels the run context, kills any in-flight exec and ABANDONS the
// run (§2). The environment is kept. Runs without a live goroutine (paused,
// or still queued pending) are aborted directly: they are dequeued and the
// terminal event is appended here — safe, no writer goroutine exists.
//
// Abandoned rather than failed (PLAN5 M24): `failed` is reserved for the
// engine's own faults — infrastructure, an unresolvable project, a flow that
// no longer parses. A person deliberately stopping a run is a decision, and
// reporting it as a failure both misstates the history and makes the two
// spellings of "abort" (this verb and the flow's `abort` terminal) land in
// opposite states. Resumability is unchanged: RetryRun accepts both.
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
	if err := e.appendAndEmit(ctx, runID, &event.RunAbandoned{
		StalledAt: v.CurrentStage, Reason: "aborted by user"}); err != nil {
		return err
	}
	// Aborting an INACTIVE run (queued, or parked by pause) never enters the
	// run loop, so its terminal scrub has to happen here. A paused run has a
	// live workspace full of credentials, which is exactly the case that
	// matters.
	v.State = run.Abandoned
	e.ScrubWorkspaceSecrets(ctx, v)
	return nil
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
	if ok {
		h.cancel(errPaused)
		return nil
	}
	// No loop: a run parked on a human is still pausable, and pausing it is
	// meaningful — it says "do not pick this up when the answer lands". The
	// answer is still recorded; the run just will not wake on it.
	ctx := context.Background()
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return err
	}
	v := run.Reduce(evs)
	if !v.Dormant || v.State.Terminal() {
		return fmt.Errorf("run %s: %w (only running runs can be paused)", runID, ErrRunNotActive)
	}
	if v.State == run.Paused {
		return fmt.Errorf("run %s: %w (already paused)", runID, ErrRunNotActive)
	}
	e.wakeMu.Lock()
	defer e.wakeMu.Unlock()
	return e.appendAndEmit(ctx, runID, &event.RunPaused{StageID: v.CurrentStage, Reason: event.PauseUser})
}

// Play restarts a paused run through the normal scheduling queue; the loop
// replays events and re-enters mid-stage like crash recovery. Distinct
// from RetryRun ("try again"): play resets no retry budgets — the run
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
		if v.Dormant {
			// Parked on a human, not by one. Playing it would re-enter the
			// stage and park again on the same unanswered question; the thing
			// that moves it is the answer.
			return fmt.Errorf("run %s is waiting for an answer, not paused — answering it resumes the run", runID)
		}
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
	if err := e.appendAndEmit(ctx, runID, &event.RunUnpaused{Waiting: waiting, Reason: event.UnpausePlay}); err != nil {
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
	// Cancel every loop first, then wait: a harness that ignores its
	// cancellation gets a 60s grace (runloop.awaitAgent), and paying that
	// once per run in sequence would stretch a shutdown to minutes.
	for _, h := range hs {
		h.cancel(errShutdown)
	}
	for _, h := range hs {
		<-h.done
	}
}

// Wait blocks until the run is neither active nor queued (test/CLI
// convenience).
// active reports whether a run currently holds a loop goroutine (and with it a
// concurrency slot). Exported to tests only through the package boundary; the
// distinction it names — running vs parked — is the point of dormancy.
func (e *Engine) active(runID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.handles[runID]
	if ok {
		return true
	}
	return slices.Contains(e.queue, runID)
}

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
// options declare a closed answer set the human picks one of (a one-field
// form); artifact points the question at a workspace file for the review
// workbench. The returned Ping's Result channel yields the answer exactly
// once.
func (e *Engine) RaisePing(runID, ptype, question, context_ string, options, artifactRefs []string) (*Ping, error) {
	return e.RaisePingKeyed(runID, "", ptype, question, context_, options, artifactRefs)
}

// RaisePingKeyed is RaisePing with the raiser's idempotency key.
func (e *Engine) RaisePingKeyed(runID, key, ptype, question, context_ string, options, artifactRefs []string) (*Ping, error) {
	q := Question{Key: key, Type: ptype, Question: question, Context: context_, Artifacts: artifactRefs}
	if len(options) > 0 {
		fd := event.Field{ID: AnswerField, Type: event.FieldString, Required: true}
		for _, o := range options {
			fd.Options = append(fd.Options, event.Option{ID: o, Label: o})
		}
		q.Fields = []event.Field{fd}
	}
	return e.RaiseQuestion(runID, q)
}

// Question is what an agent asks through ask_human (§10).
type Question struct {
	// Key is the raiser's idempotency key: a raise retried with the same key
	// (its response was lost in transit) returns the ping the first one
	// created instead of asking the human twice.
	Key      string
	Type     string // clarification|blocker|permission
	Question string
	Context  string
	// Fields are what it asks, in order (event.Form); none is one free-text
	// answer.
	Fields    []event.Field
	Artifacts []string
}

// RaiseQuestion raises an agent's question and hands it to the running
// agent stage.
func (e *Engine) RaiseQuestion(runID string, q Question) (*Ping, error) {
	key, ptype, question, context_, artifactRefs := q.Key, q.Type, q.Question, q.Context, q.Artifacts
	if key != "" {
		e.mu.Lock()
		for _, p := range e.pings {
			if p.RunID == runID && p.key == key {
				e.mu.Unlock()
				return p, nil
			}
		}
		e.mu.Unlock()
	}
	switch ptype {
	case "clarification", "blocker", "permission":
	default:
		return nil, fmt.Errorf("invalid ask_human ping type %q", ptype)
	}
	form := &event.Form{Fields: q.Fields}
	if len(form.Fields) == 0 {
		form.Fields = []event.Field{{ID: AnswerField, Type: event.FieldString, Required: true}}
	}
	if err := normalizeForm(form); err != nil {
		return nil, fmt.Errorf("ask_human: %w", err)
	}
	var refs []string
	for _, a := range artifactRefs {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		if !flow.ValidArtifactPath(a) {
			return nil, fmt.Errorf("ask_human: artifact %q must be a repo-relative path", a)
		}
		refs = append(refs, a)
	}
	if len(refs) > 8 {
		return nil, fmt.Errorf("ask_human: at most 8 artifacts (got %d)", len(refs))
	}
	e.mu.Lock()
	h, ok := e.handles[runID]
	if !ok {
		e.mu.Unlock()
		return nil, fmt.Errorf("run %s: %w", runID, ErrRunNotActive)
	}
	p := &Ping{
		ID: e.newID(), RunID: runID, Type: ptype, Question: question, Context: context_,
		Form: form, AnswerKind: event.AnswerForm, Files: refs, key: key,
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
	ctx := context.Background()
	// A ping is answered either by the loop that is holding it or, when the
	// run is parked on it, against the log. The only thing between those two
	// worlds is a transition — a loop starting, or exiting into dormancy —
	// and a human who clicked answer inside that window deserves it to work,
	// not to be told the run is busy.
	deadline := e.clock().Add(answerSettle)
	for attempt := 0; ; attempt++ {
		err := e.answerLive(pingID, ans)
		if !errors.Is(err, errNoLoop) {
			return err
		}
		err = e.answerDormant(ctx, pingID, ans)
		if !errors.Is(err, errRunBusy) {
			return err
		}
		if e.clock().After(deadline) {
			return fmt.Errorf("run is busy; the answer was not accepted — try again")
		}
		time.Sleep(answerSettleStep)
	}
}

// validateAnswer enforces the answer contract: a yes/no takes
// approve|reject, a form accept with its fields or decline with a note
// (validateForm, which tidies the content in place). One implementation,
// because the live and dormant paths must not be able to disagree about what
// a valid answer is.
func validateAnswer(kind string, form *event.Form, ans *PingAnswer) error {
	switch kind {
	case event.AnswerYesNo:
		if ans.Answer != "approve" && ans.Answer != "reject" {
			return fmt.Errorf("this question is answered approve|reject")
		}
	case event.AnswerForm:
		return validateForm(form, ans)
	}
	return nil
}

// answerLive hands the answer to the run's loop and waits to hear that it
// landed. Returns errNoLoop when there is no loop to hand it to (or it went
// away mid-flight), which is the caller's cue to take the dormant path.
func (e *Engine) answerLive(pingID string, ans PingAnswer) error {
	e.mu.Lock()
	p, known := e.pings[pingID]
	if !known {
		e.mu.Unlock()
		return errNoLoop
	}
	if p.answered {
		e.mu.Unlock()
		return fmt.Errorf("ping %s: %w", pingID, ErrPingAnswered)
	}
	h, active := e.handles[p.RunID]
	e.mu.Unlock()
	if !active {
		return errNoLoop
	}
	if err := validateAnswer(p.AnswerKind, p.Form, &ans); err != nil {
		return fmt.Errorf("ping %s: %w", pingID, err)
	}
	if err := validateReview(p.Files, ans); err != nil {
		return fmt.Errorf("ping %s: %w", pingID, err)
	}
	// The answers channel is drained only in agent stages and gates. Don't
	// report success for an answer nobody will ever process, and don't wedge
	// the HTTP handler if the buffer is full.
	ack := make(chan error, 1)
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	select {
	case h.answers <- answerReq{p: p, ans: ans, ack: ack}:
	case <-h.done:
		return errNoLoop
	case <-timeout.C:
		return fmt.Errorf("run %s is busy; the answer was not accepted — try again", p.RunID)
	}
	// Wait for the loop to say what it did with it. An ack that is already
	// there wins over a closed done: the loop can record the answer and exit
	// in the same breath (that is exactly what a stage pausing on it does).
	select {
	case err := <-ack:
		return err
	case <-h.done:
		select {
		case err := <-ack:
			return err
		default:
			return errNoLoop
		}
	case <-timeout.C:
		return fmt.Errorf("run %s is busy; the answer was not accepted — try again", p.RunID)
	}
}

// answerDormant records an answer for a run with no loop and wakes it.
//
// This is the one place outside a run's own goroutine that appends to its log,
// and it is safe for the same reason the loop's monopoly is: a dormant run has
// no writer, and nothing can start one behind our back — Recover skips parked
// runs, Play refuses a run that is merely waiting, and the only path that
// re-queues one is this function, serialized by wakeMu.
func (e *Engine) answerDormant(ctx context.Context, pingID string, ans PingAnswer) error {
	e.wakeMu.Lock()
	defer e.wakeMu.Unlock()

	v, ok := e.pingView(ctx, pingID)
	if !ok {
		return fmt.Errorf("ping %s: %w", pingID, ErrPingNotFound)
	}
	switch v.Status {
	case "answered":
		return fmt.Errorf("ping %s: %w", pingID, ErrPingAnswered)
	case "withdrawn":
		// The run ended without it; the projection already says so.
		return fmt.Errorf("run %s: %w", v.RunID, ErrRunNotActive)
	}
	if err := validateAnswer(v.AnswerKind, v.Form, &ans); err != nil {
		return fmt.Errorf("ping %s: %w", pingID, err)
	}
	if err := validateReview(v.Files, ans); err != nil {
		return fmt.Errorf("ping %s: %w", pingID, err)
	}

	e.mu.Lock()
	_, active := e.handles[v.RunID]
	e.mu.Unlock()
	if active {
		// A loop appeared between the live path giving up and here. It owns
		// the log; refusing beats writing behind it.
		return fmt.Errorf("run %s: %w", v.RunID, errRunBusy)
	}

	evs, err := e.store.Events(ctx, v.RunID, 0)
	if err != nil {
		return err
	}
	view := run.Reduce(evs)
	if view.State.Terminal() {
		return fmt.Errorf("run %s: %w", v.RunID, ErrRunNotActive)
	}
	if !view.Dormant {
		// Non-terminal, no loop, not parked: the run is mid-admission and a
		// loop is owed to it. Its own goroutine will take the answer.
		return fmt.Errorf("run %s: %w", v.RunID, errRunBusy)
	}

	payloads := []event.Payload{&event.PingAnswered{PingID: pingID, Answer: ans.Answer,
		Feedback: ans.Feedback, Content: ans.Content, AttachmentIDs: ans.AttachmentIDs, Review: ans.Review}}
	// Wake only when this was the last thing the run was waiting for, and only
	// if a human has not deliberately parked it: an agent may have asked two
	// questions, and pause means pause.
	wake := view.State != run.Paused && lastPendingPing(evs, pingID)
	if wake {
		payloads = append(payloads, &event.RunUnpaused{Reason: event.UnpauseAnswered})
	}
	if err := e.appendAndEmit(ctx, v.RunID, payloads...); err != nil {
		return err
	}
	if !wake {
		return nil
	}
	lim := e.limits(ctx)
	e.mu.Lock()
	e.queue = append(e.queue, v.RunID)
	e.scheduleLocked(lim)
	e.mu.Unlock()
	return nil
}

// answersAllIn reports whether a parked run has every question it was waiting
// on answered — the signal that it has work to do and is owed a loop.
func answersAllIn(evs []event.Event) bool {
	for _, v := range run.ReducePings(evs) {
		if v.Status == "pending" {
			return false
		}
	}
	return true
}

// lastPendingPing reports whether pingID is the only question of its run still
// open — i.e. whether answering it leaves the run with work to do.
func lastPendingPing(evs []event.Event, pingID string) bool {
	for _, v := range run.ReducePings(evs) {
		if v.Status == "pending" && v.PingID != pingID {
			return false
		}
	}
	return true
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
	if v.ProjectID == "" {
		return nil, fmt.Errorf("run %s: %w", runID, ErrNoLiveLog)
	}
	env := e.envNameForRun(v, runID)
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, err
	}
	defer res.release()
	tailer, ok := res.Envp.(ports.ExecLogTailer)
	if !ok {
		return nil, fmt.Errorf("run %s: provisioner has no live log: %w", runID, ErrNoLiveLog)
	}
	return tailer.TailExecLog(ctx, ports.EnvID(env), n)
}

// PendingPings returns open pings (the CPUI inbox source until the DB
// projection exists).
func (e *Engine) PendingPings() []*Ping {
	e.mu.Lock()
	live := map[string]bool{}
	var out []*Ping
	for _, p := range e.pings {
		if !p.answered {
			live[p.ID] = true
			out = append(out, p)
		}
	}
	e.mu.Unlock()
	// A run parked on a human has no loop and therefore no registry entries,
	// but its question is precisely the one somebody has to see. Fold the
	// durable projection in for those — pending means answerable, and this
	// list would otherwise hide exactly the pings that are waiting longest.
	for _, v := range e.pingViews(context.Background(), "pending") {
		if live[v.PingID] {
			continue
		}
		out = append(out, &Ping{
			ID: v.PingID, RunID: v.RunID, Type: v.Type, Question: v.Question,
			Context: v.Context, Files: v.Files, Link: v.Link, StageID: v.StageID,
			Form: v.Form, AnswerKind: v.AnswerKind, Phase: v.Phase, EndsPhase: v.EndsPhase,
			RaisedAt: v.RaisedAt,
		})
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
// Window is the one-window-for-everyone shape of ReapOnce's argument
// (tests, a single-user install).
func Window(d time.Duration) func(string) time.Duration {
	return func(string) time.Duration { return d }
}

func (e *Engine) ReapOnce(ctx context.Context, destroyCompletedAfter func(userID string) time.Duration) error {
	views, err := e.views(ctx)
	if err != nil {
		return err
	}
	for _, v := range views {
		id := v.RunID
		if v.EnvID == "" || v.State != run.Completed {
			continue
		}
		// The window is the run owner's setting: each person decides how
		// long their finished workspaces stand.
		if e.clock().Sub(v.UpdatedAt) < destroyCompletedAfter(v.UserID) {
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

// ReconcileEnvsOnce refreshes the live container status of every run that
// still holds an env_id, by querying the provisioner (docker/kubernetes) rather
// than the event log. This is what lets the workspace badge tell "live" from
// "stopped/removed out-of-band" — the projection only knows an env was
// provisioned and not yet destroyed, never whether the container is actually
// up. The whole map is rebuilt each pass (runs whose env_id is gone drop out).
//
// A failed probe (Resolve or Status errored: provisioner unreachable, expired
// cluster credentials, deleted project) records EnvUnknown — NEVER the run's
// prior value. Carrying the last-known status forward turns a persistent
// failure into a permanent lie: a workspace last seen "running" before the
// credentials expired would stay green "live" forever. Honest "unknown" beats a
// stale "live"; a genuinely transient blip self-heals on the next 30s pass.
func (e *Engine) ReconcileEnvsOnce(ctx context.Context) error {
	views, err := e.views(ctx)
	if err != nil {
		return err
	}
	next := map[string]ports.EnvStatus{}
	dirty := map[string]bool{}
	// Runs with an op in flight (rebuild/destroy) are skipped: their docker
	// state is churning and the EnvBadges overlay already covers the badge, so
	// probing would only manufacture a spurious EnvUnknown mid-op.
	e.opMu.Lock()
	inflight := make(map[string]bool, len(e.ops))
	for id, op := range e.ops {
		if !op.failed {
			inflight[id] = true
		}
	}
	e.opMu.Unlock()
	// Resolutions are cached per project within the pass — many runs share
	// one project, and Resolve is not free.
	resolved := map[string]*Resolution{}
	defer func() {
		for _, res := range resolved {
			res.release()
		}
	}()
	for _, v := range views {
		id := v.RunID
		if inflight[id] {
			continue
		}
		// A run with no env_id has no workspace to report on; pre-v2 runs with
		// no project can't be resolved to a provisioner (§6.1).
		if v.EnvID == "" || v.ProjectID == "" {
			continue
		}
		res, ok := resolved[v.ProjectID]
		if !ok {
			res, err = e.resolver.Resolve(ctx, v.ProjectID)
			if err != nil {
				e.log.Debug("env reconcile: resolve failed", "run", id, "project", v.ProjectID, "err", err)
				next[id] = ports.EnvUnknown
				continue
			}
			resolved[v.ProjectID] = res
		}
		st, err := res.Envp.Status(ctx, ports.EnvID(v.EnvID))
		if err != nil {
			e.log.Debug("env reconcile: status probe failed", "run", id, "env", v.EnvID, "err", err)
			next[id] = ports.EnvUnknown
			continue
		}
		next[id] = st
		// Same pass, same honesty: a failed check records nothing, so the
		// hint disappears rather than carrying a stale "changed" forward.
		if dc, ok := res.Envp.(ports.WorkspaceDirtyChecker); ok {
			if d, derr := dc.ConfigDirty(ctx, ports.EnvID(v.EnvID)); derr == nil {
				dirty[id] = d
			} else {
				e.log.Debug("env reconcile: config dirty check failed", "run", id, "env", v.EnvID, "err", derr)
			}
		}
	}
	e.envStatusMu.Lock()
	e.envStatus = next
	e.envDirty = dirty
	e.envStatusMu.Unlock()
	return nil
}

// EnvDirty returns a snapshot copy of which runs' workspaces have build
// inputs newer than their container (keyed by run id); absent means "no" or
// "not reconciled yet".
func (e *Engine) EnvDirty() map[string]bool {
	e.envStatusMu.Lock()
	defer e.envStatusMu.Unlock()
	out := make(map[string]bool, len(e.envDirty))
	maps.Copy(out, e.envDirty)
	return out
}

// EnvStatuses returns a snapshot copy of the reconciled per-run live
// container status (keyed by run id). A run absent from the map has not been
// reconciled yet; callers should fall back to the env_id-based guess.
func (e *Engine) EnvStatuses() map[string]ports.EnvStatus {
	e.envStatusMu.Lock()
	defer e.envStatusMu.Unlock()
	out := make(map[string]ports.EnvStatus, len(e.envStatus))
	maps.Copy(out, e.envStatus)
	return out
}

// resourceWindow caps the per-run sample buffer: at the ~10s sampler cadence,
// 360 samples ≈ the last hour — plenty for "what is this run costing right
// now" graphs without unbounded growth.
const resourceWindow = 360

// SampleResourcesOnce takes one resource reading (cpu/mem/net) of every
// active run's environment via the provisioner's OPTIONAL ResourceSampler
// capability, feeding the per-run in-memory window behind the run-page
// graphs. Unlike the reconciler it does NOT skip runs with an op in flight:
// provisioning/rebuilding is exactly when the operator watches the graphs,
// and a probe failure here is just a gap, never a spurious badge. Buffers of
// terminal (or vanished) runs are dropped at the end of the pass — the
// workspace may outlive the run for inspection, but the graphs are working
// feedback, not history.
func (e *Engine) SampleResourcesOnce(ctx context.Context) error {
	views, err := e.views(ctx)
	if err != nil {
		return err
	}
	live := map[string]bool{}
	// Resolutions are cached per project within the pass, as in ReconcileEnvsOnce.
	resolved := map[string]*Resolution{}
	defer func() {
		for _, res := range resolved {
			res.release()
		}
	}()
	for _, v := range views {
		id := v.RunID
		// Pending runs have no container yet; pre-v2 runs with no project
		// can't be resolved to a provisioner.
		if v.ProjectID == "" || v.State == run.Pending || v.State.Terminal() {
			continue
		}
		live[id] = true
		res, ok := resolved[v.ProjectID]
		if !ok {
			res, err = e.resolver.Resolve(ctx, v.ProjectID)
			if err != nil {
				e.log.Debug("resource sample: resolve failed", "run", id, "project", v.ProjectID, "err", err)
				continue
			}
			resolved[v.ProjectID] = res
		}
		sampler, ok := res.Envp.(ports.ResourceSampler)
		if !ok {
			continue
		}
		s, err := sampler.SampleResources(ctx, ports.EnvID(e.envNameForRun(v, id)))
		if err != nil {
			e.log.Debug("resource sample failed", "run", id, "err", err)
			continue
		}
		s.At = e.clock()
		e.resourcesMu.Lock()
		buf := append(e.resources[id], s)
		if len(buf) > resourceWindow {
			buf = buf[len(buf)-resourceWindow:]
		}
		e.resources[id] = buf
		e.resourcesMu.Unlock()
	}
	e.resourcesMu.Lock()
	for id := range e.resources {
		if !live[id] {
			delete(e.resources, id)
		}
	}
	e.resourcesMu.Unlock()
	return nil
}

// RunResources returns a snapshot copy of the run's resource-sample window
// (the run-page graphs). Empty means "nothing sampled yet": no env, a
// provisioner without the capability, or an engine restart.
func (e *Engine) RunResources(runID string) []ports.ResourceSample {
	e.resourcesMu.Lock()
	defer e.resourcesMu.Unlock()
	return append([]ports.ResourceSample(nil), e.resources[runID]...)
}

// EnvBadges returns the per-run workspace badge string: the reconciled docker
// status, OVERRIDDEN by any in-flight op ("rebuilding"/"destroying"/
// "provisioning") or a recently-failed op ("error"). The op overlay wins so a
// transient action shows immediately without touching the 30s reconciler map
// or the event log. Locks are taken SEQUENTIALLY (opMu, released, then
// envStatusMu via EnvStatuses) — never nested.
func (e *Engine) EnvBadges() map[string]string {
	now := e.clock()
	e.opMu.Lock()
	ops := make(map[string]*envOp, len(e.ops))
	for id, op := range e.ops {
		if op.failed && now.Sub(op.at) > envOpErrorTTL {
			delete(e.ops, id) // lazy TTL sweep, like observedTTL
			continue
		}
		ops[id] = op
	}
	e.opMu.Unlock()

	st := e.EnvStatuses() // takes envStatusMu — sequential, not nested with opMu
	out := make(map[string]string, len(st)+len(ops))
	for id, s := range st {
		out[id] = string(s)
	}
	for id, op := range ops {
		if op.failed {
			out[id] = "error"
		} else {
			out[id] = op.kind
		}
	}
	return out
}

func (e *Engine) notify(ctx context.Context, n ports.Notification) {
	// A run's notifications go to its owner's channels: fill the owner in
	// from the run when the caller did not.
	if n.UserID == "" && n.RunID != "" {
		n.UserID = e.runOwner(ctx, n.RunID)
	}
	for _, ch := range e.channels {
		if err := ch.Send(ctx, n); err != nil {
			e.log.Error("notification send failed", "run", n.RunID, "err", err)
		}
	}
}

func (e *Engine) finish(runID string, h *runHandle) {
	lim := e.limits(context.Background())
	e.mu.Lock()
	delete(e.handles, runID)
	// Answers that arrived as the loop was leaving: nobody will read them, so
	// tell their senders rather than let the buffer swallow them. errNoLoop
	// sends the caller down the dormant path, which is where the run now is.
	for {
		select {
		case ar := <-h.answers:
			if ar.ack != nil {
				ar.ack <- errNoLoop
			}
			continue
		default:
		}
		break
	}
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
	e.holds.clear(runID)
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

	fl, err := e.flows.Get(ctx, res.ProjectID, res.DefaultFlow)
	steps = append(steps, step("flow:"+res.DefaultFlow, err))
	if fl != nil {
		// The project's parameter bindings, applied: a bound harness, model
		// or run_ref is then checked below exactly like a literal one.
		bound, perr := e.bindFlow(ctx, fl, res.ProjectID)
		steps = append(steps, step("parameters", perr))
		fl = bound // nil when the bindings do not resolve
	}
	steps = append(steps, step("repo_access", res.RepoHost.CheckAccess(ctx, res.RepoRef)))
	for _, x := range res.ExtraRepos {
		if f := res.RepoHosts[x.Name]; f != nil {
			steps = append(steps, step("repo_access:"+x.Name,
				f.CheckAccess(ctx, ports.RepoRef{URL: x.URL, DefaultBranch: x.DefaultBranch, Locator: x.Locator})))
		}
	}
	ok, ferr := res.RepoHost.FileExists(ctx, res.RepoRef, res.AgentsMDPath)
	if ferr == nil && !ok {
		ferr = fmt.Errorf("%s not found in repo", res.AgentsMDPath)
	}
	steps = append(steps, step("repo_file:"+res.AgentsMDPath, ferr))
	dc, dcErr := hasDevcontainer(ctx, res)
	if dcErr == nil && !dc {
		dcErr = fmt.Errorf("no .devcontainer/devcontainer.json in repo")
	}
	steps = append(steps, step("devcontainer", dcErr))
	if fl != nil {
		steps = append(steps, step("flow_refs", e.preflightFlowRefs(res, fl)))
		if res.Models != nil {
			mc := checkModels(ctx, res, fl)
			var merr error
			switch {
			case len(mc.missing) > 0:
				merr = errors.New(strings.Join(mc.missing, "; "))
			case len(mc.unverified) > 0:
				merr = fmt.Errorf("could not list models to check against — %s", strings.Join(mc.unverified, "; "))
			}
			steps = append(steps, step("models", merr))
		}
		if res.CheckAuth != nil {
			// Only a refusal fails: a subscription login has no probe, and
			// must not fail the project's preflight forever.
			var aerr error
			if rejected, _ := checkAuth(ctx, res, fl.Stages); len(rejected) > 0 {
				aerr = errors.New(strings.Join(rejected, "; "))
			}
			steps = append(steps, step("harness_auth", aerr))
		}
	}
	return steps
}

func (e *Engine) preflightFlowRefs(res *Resolution, fl *flow.Flow) error {
	for i := range fl.Phases {
		if err := res.phaseAvailable(&fl.Phases[i]); err != nil {
			return fmt.Errorf("%s: %w", fl.Phases[i].Label(), err)
		}
	}
	if err := res.servicesAvailable(fl); err != nil {
		return err
	}
	if err := res.mcpAvailable(fl, e.browser != nil); err != nil {
		return err
	}
	// The Ask pane's harness (FLOWLANG §1 ask) is checked like a stage's.
	if fl.Ask != nil && !res.harnessInSet(fl.Ask.Harness) {
		return fmt.Errorf("ask: harness %q is not in the project's harness set %v", fl.Ask.Harness, res.harnessSet())
	}
	for i := range fl.Stages {
		st := &fl.Stages[i]
		if st.Type == flow.StageCommand && st.RunRef != "" {
			if _, ok := res.Commands[st.RunRef]; !ok {
				return fmt.Errorf("stage %q: run_ref %q not found in project.commands", st.ID, st.RunRef)
			}
		}
		if st.Type == flow.StageAgent && st.Harness != "" && !res.harnessInSet(st.Harness) {
			return fmt.Errorf("stage %q pins harness %q, which is not in the project's harness set %v", st.ID, st.Harness, res.harnessSet())
		}
		if st.Type == flow.StageAction {
			if err := res.actionAvailable(st); err != nil {
				return fmt.Errorf("stage %q: %w", st.ID, err)
			}
		}
		for _, slug := range []string{st.WaitFor, st.AutoApprove, st.AutoReject} {
			if slug == "" {
				continue
			}
			if err := res.signalAvailable(slug); err != nil {
				return fmt.Errorf("stage %q: %w", st.ID, err)
			}
		}
	}
	return nil
}

// CheckBranch refuses user-supplied branch names git check-ref-format
// would: the run would otherwise die much later, at bootstrap's git switch.
func CheckBranch(s string) error {
	if strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") || strings.Contains(s, "//") {
		return errors.New(`"/" cannot lead, trail or repeat`)
	}
	if strings.Contains(s, "..") || strings.Contains(s, "@{") {
		return errors.New(`".." and "@{" are not allowed`)
	}
	for seg := range strings.SplitSeq(s, "/") {
		if strings.HasPrefix(seg, ".") {
			return errors.New(`path segments cannot start with "."`)
		}
		if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return errors.New(`path segments cannot end with "." or ".lock"`)
		}
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r) {
			return fmt.Errorf("character %q is not allowed", r)
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

// maxBranchSlug bounds a branch derived from a ticket title. It is NOT a
// readability cap — a branch name may be as long as the title needs — only a
// filesystem backstop: a loose ref is a file, so a path component over 255
// bytes is unwritable on ext4/APFS. The container name has its own, much
// tighter budget and is truncated separately (workspaceName).
const maxBranchSlug = 200

// runIDTail is the 2-char per-run suffix: the last 2 chars of the lowercased
// run id. Shared by the branch name (base + "-" + tail) and the workspace
// name so both stay aligned and unique per run.
func runIDTail(runID string) string {
	s := strings.ToLower(runID)
	if len(s) > 2 {
		s = s[len(s)-2:]
	}
	return s
}

// workspaceName is the human-friendly workspace/container name for a run: an
// "apo-" marker, the slugified branch, and a 2-char run-id tail to keep two
// runs that share a branch (branches are free-text per ticket, so not unique)
// on distinct names — which keeps reuse-by-name a same-run operation. Capped
// at 32 chars; adapters use it verbatim.
func workspaceName(branch, runID string) string {
	suffix := runIDTail(runID)
	s := slug(branch, 32-len("apo-")-1-len(suffix)) // room for prefix + "-" + suffix
	parts := []string{"apo"}
	if s != "" {
		parts = append(parts, s)
	}
	if suffix != "" {
		parts = append(parts, suffix)
	}
	return strings.Join(parts, "-")
}
