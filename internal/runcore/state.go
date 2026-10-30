// Package runcore is the run interpreter's functional core: the state a run
// is in (State), the fold that builds it from the run's events (Apply,
// Replay), and the decisions the interpreter takes from it — where a stage's
// success or failure routes, whether the budget is spent, whether a stage
// runs, what the loop does next. Nothing here does I/O or reads a clock; the
// engine's run loop is the shell that carries the decisions out and records
// them as events. Keeping both halves of a run's memory — what replay builds
// and what the live loop holds — in one fold is what makes recovery exact:
// a restarted run is the same state its loop had.
package runcore

import (
	"fmt"
	"strings"
	"time"

	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/reviewfile"
	"apothecary/internal/run"
)

// StagePhase is where the interpreter stands relative to the stage at pos.
type StagePhase int

const (
	PhStart         StagePhase = iota // stage not yet started
	PhIn                              // stage_started open (recovery: re-enter)
	PhAfterComplete                   // stage_completed appended, routing not yet recorded
	PhAfterFail                       // stage_failed appended, edge decision not yet recorded (recovery: re-execute)
	PhDebug                           // parked on the debug leaf: the stage at pos failed, a human has the workspace (continue re-enters it)
)

// State is a run's working state, owned by its single-writer goroutine in
// the engine. Everything here is reconstructed from events (§2: counters and
// budgets are never stored separately), so a kill -9 at any point is
// recoverable by Replay: the run loop folds every event it appends with the
// same Apply that Replay uses.
//
// A few decisions are made in the state before the event that records them
// is appended, by the next step — each re-derived by recovery through the
// same code path:
//   - a completed stage's on_success (RouteSuccess; on recovery, the engine's
//     routing of a completion PhAfterComplete leaves unrouted),
//   - a phase's end decided without a terminal edge (EndPhase, a watcher's
//     dead end and its EndReason, an empty round; recorded by phase_completed),
//   - continuing from the debug leaf (the engine's debug stage; recorded by
//     stage_started).
//
// Two more are set without any event: a caretaker flow (no main chain)
// begins idle (the engine sets Idle at the start of each loop), and EnvID is
// known as soon as the workspace exists, before env_ready, so the run's
// attachments reach it first.
type State struct {
	RunID        string
	View         run.View
	Flow         *flow.Flow
	Branch       string
	SourceBranch string // branch this run forks off from (origin/<SourceBranch>)
	// Params are the parameter values the run runs with (run_created).
	Params map[string]string
	// Skillsets are the skillset versions the run installs (run_created).
	Skillsets []event.SkillsetPin
	EnvID     envport.EnvID
	// EnvRequested: the run asked for its workspace once (env_requested is
	// on the log), whatever its state is now.
	EnvRequested bool

	Pos int
	Ph  StagePhase
	// EndsRun: the cursor past the last stage means the RUN is done — the
	// flow routed to `done` — and not only the phase (`end`, or the chain
	// ran out); AbortsRun: the flow routed to `abort` from a success (an
	// outcome's `then: abort`, `on_success: abort`), AbortAt/AbortReason
	// saying where and why. EndReason, when set, is why a phase ended early:
	// a dead end in a watcher, recorded on its phase_completed.
	EndsRun     bool
	AbortsRun   bool
	AbortAt     string
	AbortReason string
	EndReason   string

	Iters      map[string]int // stage id -> times started
	EdgeCounts map[string]int // "from|kind|depth" -> times that chain link was taken
	// ActionPending marks action stages whose intent was recorded but whose
	// effect was never confirmed (PLAN5 M20) — the crash window between a
	// repository host call returning and its event being appended.
	ActionPending map[string]bool
	Sessions      map[string]agentport.SessionID // stage id -> last recorded harness session

	// Links are the workspace's app links from env_ready (ports exposed at
	// provision), kept for a gate or debug ping that hands out run.workspace.
	Links []event.Link

	Cost     float64
	Turns    int
	Unwanted int

	// Feed carries the content of the edge that re-entered the current stage
	// (§3.2); persisted in edge_taken, so replay restores it losslessly.
	// FeedSentinel is which input value it fills ($DECISION or $FAILURE),
	// derived from the edge kind — the target binds one of them or neither.
	Feed         string
	FeedSentinel string
	FeedAttempts int // total allowed attempts, for the "(iteration n of m)" line
	// CompletedFeed is the verdict the last replayed stage_completed
	// carries (an approval's), for recovery's routing of it.
	CompletedFeed string
	// VerdictRecorded: the stage in progress has its verdict_received on
	// the log, so recovery's routing does not record it twice.
	VerdictRecorded bool
	// CI is what failed in CI as the phase run started (run.ci.failure).
	CI *event.CIReport

	// LastFailure is the open stage_failed when the crash hit before its
	// edge decision; recovery routes it directly (the engine's routeAfterFail).
	LastFailure *event.StageFailed

	// Checkouts are the run's repositories (the primary first); MRs the
	// merge requests opened so far, in creation order.
	Checkouts []event.Checkout
	MRs       []MRRecord

	// Phases (PLAN6). Phase is the phase in progress (or the last one run),
	// Subject the checkout whose merge request it acts on ("" = all: the
	// main chain of a run started on a ticket); Idle is true between phases.
	// PhaseTip is the subject branch's head when the phase started
	// ($DIFF_PHASE counts from it); PhaseHead the newest head the phase run
	// knows there — PhaseTip, then each tip it pushed. A merge or a review
	// is pinned to PhaseHead: never a head the run has not seen.
	Phase     string
	Subject   string
	Idle      bool
	PhaseTip  string
	PhaseHead string
	// TriggerSeen holds every (phase, subject, key) a phase run started for
	// or a human declined (R6); PhaseRuns counts runs per (phase, subject).
	TriggerSeen map[string]bool
	PhaseRuns   map[string]int
	// Confirms are the phase_confirm pings raised, oldest first; their
	// answers sit in AnsweredPings until the idle loop acts on them.
	Confirms []*event.PingRaised
	// Round are the review threads this phase run loaded (M31), in its scope
	// (pending or all): a re-entry rewrites the same set, and replies and
	// continuations may name only these.
	Round       []event.LoadedThread
	RoundScope  string
	RoundLoaded bool
	// SelectPing is the thread-selection question this phase run raised.
	SelectPing *event.PingRaised
	// Skipped are the threads a human chose not to fix: thread id → its last
	// comment id at the time. Kept across rounds.
	Skipped map[string]string
	// Dropped are the review notes a person declined to post at a gate,
	// kept for the whole run: a re-review never proposes them again.
	Dropped []reviewfile.Note
	// Curated are the review documents a person approved at a gate, by
	// path, not yet written back: a gate has no workspace of its own, so the
	// next stage that needs the workspace and reads the path writes them
	// first. CuratedSeen is every ping whose curation is on the record, so a
	// gate routed again after a crash records it once.
	Curated     map[string]*event.ReviewCurated
	CuratedSeen map[string]bool
	// Pushed is the tip push_branch last pushed, per repo+branch.
	Pushed map[string]string
	// Adopted is the merge request the run was created on (nil when it
	// opens its own).
	Adopted *event.AdoptedMR
	// Heads are the commits the run knows on its merge requests, per
	// checkout: every tip it pushed, the head it opened or adopted a merge
	// request at, the tip a phase run began on. A head outside them is
	// someone else's push (mr_head_moved).
	Heads map[string]bool

	// ParkSince is, per stage, when its current attempt first parked: a
	// re-park (recovery re-entering the stage after phase_resumed) keeps it,
	// so a wait's timeout runs from the first park. Cleared by stage_started.
	ParkSince map[string]time.Time
	// Parked holds the phase runs stopped at a wait point mid-chain, by
	// phase: everything scoped to the run (Park / Unpark), while another
	// phase may hold State's own fields. At most one live run per phase.
	Parked map[string]*PhaseRun
	// AnyStarted: some stage has started (a stage_started is on the log).
	AnyStarted bool
	// Elided marks stages whose last start was a skip (a condition that did
	// not hold): they never touched the workspace.
	Elided map[string]bool

	// OpenPings are raised-but-unanswered pings to re-register on recovery.
	OpenPings []*event.PingRaised
	// GateAnswer is set when a gate's ping was answered but the crash hit
	// before the routing was recorded; the gate re-entry routes directly.
	GateAnswer *event.PingAnswered

	// RaisedPings / AnsweredPings are every ping's two halves, kept for the
	// re-entry that rebuilds a paused attempt's resume message. A run parked
	// on a question is answered while it has NO loop, so by the time one
	// exists again the answer is only a fact in the log — the live registry
	// never saw it.
	RaisedPings   map[string]*event.PingRaised
	AnsweredPings map[string]*event.PingAnswered
	// PausedAsks is the ping set a stage last parked on (stage_paused), by
	// stage id: what re-entry has to collect answers for before resuming.
	PausedAsks map[string][]string

	// Prompted marks a stage whose attempt in progress has told its agent
	// something (see AttemptBegun).
	Prompted map[string]bool

	// AgentCursor is, per stage, the position in the agent's own event
	// stream of the last transcript event recorded: after a restart, a turn
	// still in flight is followed again from right there (Reattach).
	AgentCursor map[string]string
	// PendingMsgs are human messages accepted but not yet handed to the
	// agent (message_queued without message_delivered), oldest first. They
	// ride into the next prompt.
	PendingMsgs []*event.MessageQueued

	// Holds is the take-over in force per stage (hold_taken without its
	// hold_released), with what its parked legs have banked so far.
	Holds map[string]*HeldState
	// PausedHold marks a stage parked on its holder (stage_paused held, no
	// stage_resumed since): re-entry waits for the holder's word.
	PausedHold map[string]bool
	// HandbackOwed marks a stage whose hold was released but whose session
	// has not yet been sent the handback prompt (agent_prompted handback).
	HandbackOwed map[string]bool

	Done bool
}

// HeldState is a take-over as replay knows it: who took it, when, and the
// spend of every leg that parked (each stage_paused{held} banks its own).
type HeldState struct {
	Taken   *event.HoldTaken
	TakenAt time.Time
	Banked  event.HoldSpend
}

// ChainKey identifies one link of an edge chain: depth 0 is the edge
// itself, 1+ its then fallbacks. Pre-chain edge_taken events carry no
// depth and land on 0, matching what they were.
func ChainKey(from, kind string, depth int) string {
	return fmt.Sprintf("%s|%s|%d", from, kind, depth)
}

func GatePingType(t string) bool  { return t == event.PingGate }
func DebugPingType(t string) bool { return t == event.PingDebug }

// DecidesHere reports whether an answer to this ping is the routing decision
// the stage at pos is waiting for: its gate's ping while the gate is open, or
// the debug leaf's while the run is parked there — and the stage's own ping,
// never a stale one an earlier attempt raised.
func (rs *State) DecidesHere(r *event.PingRaised) bool {
	if rs.Flow == nil || rs.Pos < 0 || rs.Pos >= len(rs.Flow.Stages) || r.StageID != rs.Flow.Stages[rs.Pos].ID {
		return false
	}
	return (GatePingType(r.Type) && rs.Ph == PhIn) || (DebugPingType(r.Type) && rs.Ph == PhDebug)
}

// AttemptBegun reports whether the agent stage's attempt in progress reached
// its agent: it was prompted, or a harness session exists for it. A crash
// between stage_started and the first prompt leaves one that did not.
func (rs *State) AttemptBegun(stageID string) bool {
	_, session := rs.Sessions[stageID]
	return session || rs.Prompted[stageID]
}

// AgentPingType: a question an agent raised through ask_human — the kind a
// well-briefed agent should not need, hence counted as unwanted. Everything
// else (a stage's gate, a phase confirmation, a selection) is the engine
// asking on purpose.
func AgentPingType(t string) bool {
	return t == event.PingClarification || t == event.PingBlocker || t == event.PingPermission
}

func TriggerKey(phase, subject, key string) string { return phase + "\x00" + subject + "\x00" + key }
func PhaseRunKey(phase, subject string) string     { return phase + "\x00" + subject }

// PhaseEverRan reports whether the phase ran at all, on any subject.
func (rs *State) PhaseEverRan(phase string) bool {
	for k, n := range rs.PhaseRuns {
		if n > 0 && strings.HasPrefix(k, phase+"\x00") {
			return true
		}
	}
	return false
}

// PhaseRun is what is scoped to one phase run: where it stands and what it
// carries. The active run's lives in State's own fields; a parked run's
// is kept here until it resumes. Stage-keyed maps (iters, EdgeCounts,
// sessions, …) stay shared: stage ids are one namespace across phases.
type PhaseRun struct {
	Phase, Subject, Tip, Head string
	Pos                       int
	Ph                        StagePhase
	Feed, FeedSentinel        string
	FeedAttempts              int
	LastFailure               *event.StageFailed
	GateAnswer                *event.PingAnswered
	Round                     []event.LoadedThread
	RoundScope                string
	RoundLoaded               bool
	SelectPing                *event.PingRaised
	CI                        *event.CIReport
	// ParkedAt is the stage the run waits at (a wait or a gate), ping the
	// decision a gate waits on, since when it parked (a wait's timeout runs
	// from there).
	ParkedAt string
	Ping     string
	Since    time.Time
}

// Park stores the active phase run as parked at stage and leaves the run
// idle. Shared by replay and the live park so they cannot disagree.
func (rs *State) Park(stage, ping string, since time.Time) {
	if rs.Parked == nil {
		rs.Parked = map[string]*PhaseRun{}
	}
	rs.Parked[rs.Phase] = &PhaseRun{
		Phase: rs.Phase, Subject: rs.Subject, Tip: rs.PhaseTip, Head: rs.PhaseHead, Pos: rs.Pos, Ph: rs.Ph,
		Feed: rs.Feed, FeedSentinel: rs.FeedSentinel, FeedAttempts: rs.FeedAttempts,
		LastFailure: rs.LastFailure, GateAnswer: rs.GateAnswer,
		Round: rs.Round, RoundScope: rs.RoundScope, RoundLoaded: rs.RoundLoaded, SelectPing: rs.SelectPing, CI: rs.CI,
		ParkedAt: stage, Ping: ping, Since: since,
	}
	rs.Idle = true
}

// Unpark makes the parked run of phase the active one again. False when
// no run of that phase is parked.
func (rs *State) Unpark(phase string) bool {
	pr := rs.Parked[phase]
	if pr == nil {
		return false
	}
	delete(rs.Parked, phase)
	rs.Phase, rs.Subject, rs.PhaseTip, rs.PhaseHead, rs.Pos, rs.Ph = pr.Phase, pr.Subject, pr.Tip, pr.Head, pr.Pos, pr.Ph
	rs.Feed, rs.FeedSentinel, rs.FeedAttempts = pr.Feed, pr.FeedSentinel, pr.FeedAttempts
	rs.LastFailure, rs.GateAnswer = pr.LastFailure, pr.GateAnswer
	rs.Round, rs.RoundScope, rs.RoundLoaded, rs.SelectPing = pr.Round, pr.RoundScope, pr.RoundLoaded, pr.SelectPing
	rs.CI = pr.CI
	rs.EndsRun, rs.AbortsRun, rs.EndReason = false, false, ""
	rs.Idle = false
	// A gate answered while its phase was parked: the answer is the decision
	// the resumed stage routes by (the parked record predates it).
	if pr.Ping != "" {
		if a := rs.AnsweredPings[pr.Ping]; a != nil {
			rs.GateAnswer = a
		}
	}
	return true
}

// AnyParked reports whether some phase run waits at a wait point.
func (rs *State) AnyParked() bool { return len(rs.Parked) > 0 }

// RoundIDs lists the threads this phase run loaded, by id.
func (rs *State) RoundIDs() []string {
	out := make([]string, 0, len(rs.Round))
	for _, t := range rs.Round {
		out = append(out, t.ID)
	}
	return out
}

// RoundThread is the loaded thread with that id, or nil.
func (rs *State) RoundThread(id string) *event.LoadedThread {
	for i := range rs.Round {
		if rs.Round[i].ID == id {
			return &rs.Round[i]
		}
	}
	return nil
}

// NoteCurated folds a gate's curation into the run: what a person dropped
// stays dropped for the run, the threads they held a reply back from are
// left alone until someone writes there again, and an approved document
// waits to be written back. Shared by replay and the live gate so they
// cannot disagree.
func (rs *State) NoteCurated(p *event.ReviewCurated) {
	rs.CuratedSeen[p.PingID] = true
	rs.Dropped = append(rs.Dropped, p.Dropped...)
	for id, last := range p.Skipped {
		rs.Skipped[id] = last
	}
	if p.Approved {
		rs.Curated[p.Path] = p
	}
}

// ForgetCurated drops the curated documents waiting on paths the starting
// stage writes: what it writes is the new document, and a curation of the
// old one must never be written over it.
func (rs *State) ForgetCurated(fl *flow.Flow, stageID string) {
	if fl == nil || len(rs.Curated) == 0 {
		return
	}
	if i := fl.Index(stageID); i >= 0 {
		for _, o := range fl.Stages[i].Outputs {
			delete(rs.Curated, flow.FilePath(o.Value))
		}
	}
}

// StartPhase moves the cursor to a phase's first stage, fresh: its edge
// counters reset (R7 — a review round's retries are per round), the feed
// cleared. Shared by replay and the live start so they cannot disagree.
func (rs *State) StartPhase(p *event.PhaseStarted, fl *flow.Flow) {
	rs.Phase, rs.Subject, rs.PhaseTip, rs.PhaseHead, rs.Idle = p.Phase, p.Subject, p.Tip, p.Tip, false
	rs.CI = p.CI
	rs.Round, rs.RoundScope, rs.RoundLoaded, rs.SelectPing = nil, "", false, nil
	rs.EndsRun, rs.AbortsRun, rs.EndReason = false, false, ""
	rs.PhaseRuns[PhaseRunKey(p.Phase, p.Subject)]++
	rs.NoteHead(p.Subject, p.Tip)
	if p.Key != "" {
		rs.TriggerSeen[TriggerKey(p.Phase, p.Subject, p.Key)] = true
	}
	if fl == nil {
		return
	}
	ph := fl.Phase(p.Phase)
	if ph == nil {
		rs.Pos = -1
		return
	}
	for i := ph.First; i <= ph.Last; i++ {
		prefix := fl.Stages[i].ID + "|"
		for k := range rs.EdgeCounts {
			if strings.HasPrefix(k, prefix) {
				delete(rs.EdgeCounts, k)
			}
		}
	}
	rs.Pos, rs.Ph = ph.First, PhStart
	rs.Feed, rs.FeedSentinel, rs.FeedAttempts = "", "", 0
	rs.LastFailure, rs.GateAnswer = nil, nil
	if ph.Then != "" && ph.Last < ph.First {
		// An outcome with no chain: its start is its end — the run's.
		rs.Pos = len(fl.Stages)
		rs.EndsRun = ph.Then == flow.TerminalDone
		rs.AbortsRun = ph.Then == flow.TerminalAbort
		rs.AbortAt, rs.AbortReason = "", fmt.Sprintf("aborted by flow: outcome %s", ph.ID)
	}
}

// NewState is the state of a run with no history yet, on flow fl.
func NewState(fl *flow.Flow) *State {
	return &State{
		Flow:          fl,
		Iters:         map[string]int{},
		EdgeCounts:    map[string]int{},
		ActionPending: map[string]bool{},
		Sessions:      map[string]agentport.SessionID{},
		RaisedPings:   map[string]*event.PingRaised{},
		AnsweredPings: map[string]*event.PingAnswered{},
		PausedAsks:    map[string][]string{},
		Prompted:      map[string]bool{},
		AgentCursor:   map[string]string{},
		TriggerSeen:   map[string]bool{},
		PhaseRuns:     map[string]int{},
		Elided:        map[string]bool{},
		Pushed:        map[string]string{},
		Heads:         map[string]bool{},
		Skipped:       map[string]string{},
		Curated:       map[string]*event.ReviewCurated{},
		CuratedSeen:   map[string]bool{},
		Holds:         map[string]*HeldState{},
		PausedHold:    map[string]bool{},
		HandbackOwed:  map[string]bool{},
		ParkSince:     map[string]time.Time{},
	}
}

// Replay folds a run's event history into State. fl may be nil when the
// flow is unknown; callers must check.
func Replay(evs []event.Event, fl *flow.Flow) *State {
	rs := NewState(fl)
	for i := range evs {
		rs.Apply(&evs[i])
	}
	return rs
}

// Apply folds one event into the state: the only way a State learns
// what happened. Replay is apply over the log; the run loop applies each
// event it appends.
func (rs *State) Apply(ev *event.Event) {
	rs.RunID = ev.RunID
	run.Apply(&rs.View, ev)
	switch p := ev.Payload.(type) {
	case *event.RunCreated:
		rs.Branch = p.Branch
		rs.SourceBranch = p.SourceBranch
		rs.Params = p.Params
		rs.Skillsets = p.Skillsets
		rs.Checkouts = CheckoutsOf(p)
		if m := p.MR; m != nil {
			rs.Adopted = m
			if co := rs.Checkout(m.Repo); co != nil {
				rs.AddMR(&event.MRCreated{Repo: m.Repo, Branch: co.Branch, Target: co.Source, MRURL: m.URL, MRIID: m.IID, Head: m.Head})
			}
		}
	case *event.EnvRequested:
		rs.EnvRequested = true
	case *event.EnvReady:
		rs.EnvID = envport.EnvID(p.EnvID)
		rs.Links = p.Links
	case *event.EnvFailed:
		rs.Done = true
		if p.EnvID != "" {
			rs.EnvID = envport.EnvID(p.EnvID)
		}
	case *event.SnapshotCaptured:
		// Keeps pseudo-stage counters monotonic across restarts: the
		// provisioning log has no stage_started to replay its iteration
		// from. A real stage's snapshots never exceed its started count,
		// so this is a no-op for them.
		if p.Iteration > rs.Iters[p.StageID] {
			rs.Iters[p.StageID] = p.Iteration
		}
	case *event.StageStarted:
		if rs.Ph == PhDebug {
			// Continued from the debug leaf: back into the stage fresh, the
			// failure that parked it spent (the engine's routeDebug).
			rs.Feed, rs.FeedSentinel, rs.FeedAttempts = "", "", 0
		}
		rs.AnyStarted = true
		rs.VerdictRecorded = false
		delete(rs.ParkSince, p.StageID)
		delete(rs.Elided, p.StageID)
		rs.ForgetCurated(rs.Flow, p.StageID)
		if rs.Flow != nil {
			rs.Pos = rs.Flow.Index(p.StageID)
		}
		rs.Ph = PhIn
		if p.Iteration > rs.Iters[p.StageID] {
			rs.Iters[p.StageID] = p.Iteration
		}
		rs.GateAnswer = nil
		rs.LastFailure = nil
		delete(rs.PausedAsks, p.StageID)
		// A session belongs to one attempt: a retry never re-enters the last.
		delete(rs.Sessions, p.StageID)
		delete(rs.Prompted, p.StageID)
	case *event.StageCompleted:
		rs.Ph = PhAfterComplete
		rs.GateAnswer = nil // the decision it waited on is routed
		// What the routing that follows (advance) sets: the verdict the
		// approval carries, or nothing — a feed the stage was entered with
		// is spent.
		rs.CompletedFeed = p.Feed
		rs.Feed, rs.FeedSentinel, rs.FeedAttempts = p.Feed, "", 0
		if p.Feed != "" {
			rs.FeedSentinel = flow.SourceVerdict
		}
		if p.Skipped {
			rs.Elided[p.StageID] = true
		}
		// A completed stage settled whatever intent it recorded: a later
		// run of the same action (a second review round) starts clean.
		delete(rs.ActionPending, p.StageID)
		if p.SessionID != "" {
			rs.Sessions[p.StageID] = agentport.SessionID(p.SessionID)
		}
		if p.Usage != nil {
			rs.Cost += p.Usage.CostUSD
		}
		rs.Turns += p.TurnsUsed
	case *event.VerdictReceived:
		rs.VerdictRecorded = true
	case *event.StageFailed:
		rs.Ph = PhAfterFail
		rs.LastFailure = p
	case *event.StagePaused:
		rs.PausedAsks[p.StageID] = p.PingIDs
		// The attempt is parked, not finished: ph stays PhIn so recovery
		// re-enters this stage. The session it recorded is what the
		// re-entry resumes — without it a restart would have to start the
		// agent over and lose everything it had done before the question.
		// Cost and turns are banked here because the invocation that just
		// ended really did spend them.
		rs.Ph = PhIn
		if p.SessionID != "" {
			rs.Sessions[p.StageID] = agentport.SessionID(p.SessionID)
		}
		if p.Usage != nil {
			rs.Cost += p.Usage.CostUSD
		}
		rs.Turns += p.TurnsUsed
		// A park on the holder of a take-over: re-entry waits for their
		// word. Either park banks what the hold's leg spent.
		if p.Reason == event.PauseHeld {
			rs.PausedHold[p.StageID] = true
		}
		if h := rs.Holds[p.StageID]; h != nil && p.Hold != nil {
			h.Banked.Add(*p.Hold)
		}
	case *event.StageResumed:
		// The pause it answers is consumed: leaving it set would make a
		// LATER attempt of the same stage re-enter as if it were still
		// parked on those long-answered questions.
		delete(rs.PausedAsks, p.StageID)
		delete(rs.PausedHold, p.StageID)
		// Bookkeeping only — the pause already set everything the
		// re-entry needs, and the invocation this event opens reports its
		// own spend when it ends (stage_paused again, or stage_completed).
		rs.Ph = PhIn
	case *event.EdgeTaken:
		rs.GateAnswer = nil // the decision it waited on is routed
		rs.EdgeCounts[ChainKey(p.From, p.Edge, p.Depth)] = p.Count
		if p.To == flow.TerminalEnd || p.To == flow.TerminalDone {
			// The edge ended the phase — or, with done, the run.
			if rs.Flow != nil {
				rs.Pos = len(rs.Flow.Stages)
			}
			rs.EndsRun = p.To == flow.TerminalDone
			rs.Ph = PhStart
			rs.Feed, rs.FeedSentinel, rs.FeedAttempts = "", "", 0
			break
		}
		if p.To == flow.ReservedDebug {
			// The debug leaf: the cursor stays on the stage that took the
			// link, parked for a human (the engine's stageDebug).
			if rs.Flow != nil {
				rs.Pos = rs.Flow.Index(p.From)
			}
			rs.Ph = PhDebug
			rs.Feed = p.Feed
			rs.FeedSentinel = flow.FeedSource(p.Edge)
			rs.FeedAttempts = 0
			break
		}
		if rs.Flow != nil {
			rs.Pos = rs.Flow.Index(p.To)
		}
		rs.Ph = PhStart
		rs.Feed = p.Feed
		rs.FeedSentinel = flow.FeedSource(p.Edge)
		rs.FeedAttempts = p.Max + 1
	case *event.PhaseStarted:
		if rs.Phase == "" && rs.AnyStarted {
			// A start recorded after the fact (the engine's beginRun on a log that had
			// stage events but no phase start): the cursor stays where the
			// stage events put it.
			rs.Phase, rs.Subject, rs.Idle = p.Phase, p.Subject, false
			rs.PhaseRuns[PhaseRunKey(p.Phase, p.Subject)]++
			break
		}
		rs.StartPhase(p, rs.Flow)
	case *event.PhaseCompleted:
		// The chain is over: the cursor is past it, where the loop put it
		// when it decided the phase's end (EndPhase, a completed last stage).
		if rs.Flow != nil {
			rs.Pos = len(rs.Flow.Stages)
		}
		rs.Ph = PhStart
		rs.Idle = true
		rs.GateAnswer = nil
		rs.EndsRun, rs.AbortsRun, rs.EndReason = false, false, ""
	case *event.PhaseParked:
		since, ok := rs.ParkSince[p.StageID]
		if !ok {
			since = ev.TS
			rs.ParkSince[p.StageID] = since
		}
		rs.Park(p.StageID, p.PingID, since)
	case *event.PhaseResumed:
		rs.Unpark(p.Phase)
	case *event.BranchPushed:
		rs.NotePush(p.Repo, p.Branch, p.Tip)
	case *event.ReviewCommentsLoaded:
		rs.Round, rs.RoundScope, rs.RoundLoaded = append([]event.LoadedThread(nil), p.Threads...), p.Scope, true
		for id, last := range p.Skipped {
			rs.Skipped[id] = last
		}
	case *event.ReviewCurated:
		rs.NoteCurated(p)
	case *event.PhaseDeclined:
		rs.TriggerSeen[TriggerKey(p.Phase, p.Subject, p.Key)] = true
	case *event.PingRaised:
		if p.Type == event.PingPhaseConfirm {
			rs.Confirms = append(rs.Confirms, p)
		}
		if p.Type == event.PingSelect {
			rs.SelectPing = p
		}
		rs.RaisedPings[p.PingID] = p
		rs.OpenPings = append(rs.OpenPings, p)
		if AgentPingType(p.Type) {
			rs.Unwanted++
		}
	case *event.PingAnswered:
		rs.AnsweredPings[p.PingID] = p
		for j, op := range rs.OpenPings {
			if op.PingID == p.PingID {
				rs.OpenPings = append(rs.OpenPings[:j], rs.OpenPings[j+1:]...)
				break
			}
		}
		if r, ok := rs.RaisedPings[p.PingID]; ok && rs.DecidesHere(r) {
			rs.GateAnswer = p
		}
	case *event.ActionStarted:
		// Intent recorded, effect not yet confirmed. Cleared by the
		// action's completion event below; anything still set after replay
		// means a crash landed in that window, so the executor must
		// reconcile with the repository host instead of re-running blind.
		rs.ActionPending[p.StageID] = true
	case *event.MRCreated:
		rs.AddMR(p)
		// The action's intent is settled only once every merge request it
		// publishes exists; a partial publication keeps reconciling.
	case *event.RunCompleted, *event.RunAbandoned, *event.RunFailed, *event.RunArchived:
		rs.Done = true
		// The run ended with them open: withdrawn, as the projection reads
		// them (run.ReducePings). A retry must not re-register a ping
		// nothing can route anymore.
		rs.OpenPings = nil
	case *event.RunRetried:
		// "Try again": un-terminal and re-enter the failed stage fresh.
		// Clearing EdgeCounts restores every stage's on_fail/on_reject
		// retry budget from the resume point; feed/LastFailure reset so
		// the stage runs clean, not as a routed recovery.
		rs.Done = false
		rs.Ph = PhStart
		rs.Feed = ""
		rs.FeedSentinel = ""
		rs.FeedAttempts = 0
		rs.LastFailure = nil
		rs.GateAnswer = nil
		rs.EdgeCounts = map[string]int{}
		if rs.Flow != nil {
			rs.Pos = rs.Flow.Index(p.StageID)
		}
	case *event.AgentSession:
		rs.Sessions[p.StageID] = agentport.SessionID(p.SessionID)
	case *event.AgentMessage:
		rs.NoteCursor(p.StageID, p.Cursor)
	case *event.AgentTool:
		rs.NoteCursor(p.StageID, p.Cursor)
	case *event.AgentPlan:
		rs.NoteCursor(p.StageID, p.Cursor)
	case *event.AgentNotice:
		rs.NoteCursor(p.StageID, p.Cursor)
	case *event.MessageQueued:
		rs.PendingMsgs = append(rs.PendingMsgs, p)
	case *event.MessageDelivered:
		rs.Delivered(p.MessageIDs)
	case *event.HoldTaken:
		rs.Holds[p.StageID] = &HeldState{Taken: p, TakenAt: ev.TS}
	case *event.HoldReleased:
		delete(rs.Holds, p.StageID)
		if p.Reason == event.HoldReleasedByUser {
			rs.HandbackOwed[p.StageID] = true
		}
	case *event.AgentPrompted:
		rs.Prompted[p.StageID] = true
		if p.Kind == event.PromptHandback {
			delete(rs.HandbackOwed, p.StageID)
		}
	case *event.RunPaused, *event.RunUnpaused:
		// View-level only: ph/pos/counters stay exactly as the stage
		// events left them, so play re-enters like crash recovery.
	}
}

// OpenPing returns the still-open ping of this type raised by (or for) the
// given stage, if any. Keyed by stage: a flow may have many gates, and only
// the one this stage raised may be re-armed on recovery.
func (rs *State) OpenPing(stageID, ptype string) *event.PingRaised {
	for _, p := range rs.OpenPings {
		if p.StageID == stageID && p.Type == ptype {
			return p
		}
	}
	return nil
}

// OpenAgentPings returns open ask_human pings (non-gate).
func (rs *State) OpenAgentPings() []*event.PingRaised {
	var out []*event.PingRaised
	for _, p := range rs.OpenPings {
		if AgentPingType(p.Type) {
			out = append(out, p)
		}
	}
	return out
}

func (rs *State) NoteCursor(stageID, cursor string) {
	if cursor != "" {
		rs.AgentCursor[stageID] = cursor
	}
}

// Delivered drops handed-over messages from the pending queue.
func (rs *State) Delivered(ids []string) {
	if len(ids) == 0 {
		return
	}
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	kept := rs.PendingMsgs[:0]
	for _, m := range rs.PendingMsgs {
		if !gone[m.MessageID] {
			kept = append(kept, m)
		}
	}
	rs.PendingMsgs = kept
}

// AnyWorkspaceStageStarted reports whether a stage that USES the workspace
// has run yet (the provisioning log's pseudo-stage counter does not count) —
// what decides whether a fresh run's model check is still due. Gates, waits
// and remote actions start without one, so counting them would skip the
// check on a flow that opens with one.
func (rs *State) AnyWorkspaceStageStarted() bool {
	if rs.Flow == nil {
		return true
	}
	for i := range rs.Flow.Stages {
		if st := &rs.Flow.Stages[i]; NeedsWorkspace(st) && rs.Iters[st.ID] > 0 && !rs.Elided[st.ID] {
			return true
		}
	}
	return false
}

// NeedsWorkspace reports whether running st requires the environment: an
// agent or a command always; a gate or a wait never (they park on something
// outside the run); an action unless it is remote with no file input bound
// (a tracker call needs no checkout — and must not re-provision a reaped
// one after the final gate).
func NeedsWorkspace(st *flow.Stage) bool {
	switch st.Type {
	case flow.StageHumanGate, flow.StageWait:
		return false
	case flow.StageAction:
		act := actionspec.Get(st.Action)
		if act == nil || !act.Remote {
			return true
		}
		// Every input of an action is a file it reads in the workspace.
		return len(st.Inputs) > 0
	}
	return true
}

// MRRecord is one merge request the run opened: which checkout and branch,
// onto what, and the repository host's handle.
type MRRecord struct {
	Repo   string
	Branch string
	Target string
	Ref    repoport.MRRef
}

// CheckoutsOf is the run's checkout list: as recorded, or — for runs older
// than multi-repo support — the one primary the event's flat fields describe.
func CheckoutsOf(p *event.RunCreated) []event.Checkout {
	if len(p.Checkouts) > 0 {
		return append([]event.Checkout(nil), p.Checkouts...)
	}
	return []event.Checkout{{Name: event.PrimaryCheckoutName, Dir: event.PrimaryCheckoutDir,
		URL: p.RepoURL, Branch: p.Branch, Source: p.SourceBranch, Primary: true}}
}

func (rs *State) Checkout(name string) *event.Checkout {
	if name == "" {
		name = event.PrimaryCheckoutName
	}
	for i := range rs.Checkouts {
		if rs.Checkouts[i].Name == name {
			return &rs.Checkouts[i]
		}
	}
	return nil
}

// CheckoutDirs lists every checkout's directory (primary first), for a
// command that runs in each.
func (rs *State) CheckoutDirs() []string {
	dirs := make([]string, 0, len(rs.Checkouts))
	for _, c := range rs.Checkouts {
		dirs = append(dirs, c.Dir)
	}
	if len(dirs) == 0 {
		dirs = []string{event.PrimaryCheckoutDir}
	}
	return dirs
}

// NotePush records a tip push_branch pushed: the run knows it, and so does
// the phase run whose subject's branch it went to. Shared by replay and the
// live push so they cannot disagree.
func (rs *State) NotePush(repo, branch, tip string) {
	rs.Pushed[repo+"\x00"+branch] = tip
	rs.NoteHead(repo, tip)
	if repo == rs.Subject {
		rs.PhaseHead = tip
	}
}

// NoteHead records a commit the run knows on a checkout's branch.
func (rs *State) NoteHead(repo, sha string) {
	if sha == "" {
		return
	}
	if rs.Heads == nil {
		rs.Heads = map[string]bool{}
	}
	rs.Heads[repo+"\x00"+sha] = true
}

// KnownHead reports whether sha is a commit the run knows on repo's branch.
func (rs *State) KnownHead(repo, sha string) bool { return rs.Heads[repo+"\x00"+sha] }

func (rs *State) AddMR(p *event.MRCreated) {
	repo := p.Repo
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	rs.NoteHead(repo, p.Head)
	for i := range rs.MRs {
		if rs.MRs[i].Repo == repo && rs.MRs[i].Branch == p.Branch {
			rs.MRs[i].Ref = repoport.MRRef{IID: p.MRIID, URL: p.MRURL}
			rs.MRs[i].Target = p.Target
			return
		}
	}
	rs.MRs = append(rs.MRs, MRRecord{Repo: repo, Branch: p.Branch, Target: p.Target, Ref: repoport.MRRef{IID: p.MRIID, URL: p.MRURL}})
}

func (rs *State) MRFor(repo, branch string) *MRRecord {
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	for i := range rs.MRs {
		if rs.MRs[i].Repo == repo && rs.MRs[i].Branch == branch {
			return &rs.MRs[i]
		}
	}
	return nil
}

// FirstMR is the merge request a single-MR world would call "the" MR: the
// first one opened (the primary repository's).
func (rs *State) FirstMR() *repoport.MRRef {
	if len(rs.MRs) == 0 {
		return nil
	}
	return &rs.MRs[0].Ref
}

// UnskippedRound is the round's threads a reply is still owed to: the ones
// a person did not hold a reply back from at the gate (skipped at their
// last comment).
func (rs *State) UnskippedRound() []string {
	var out []string
	for _, t := range rs.Round {
		if last, ok := rs.Skipped[t.ID]; ok && last == t.Last {
			continue
		}
		out = append(out, t.ID)
	}
	return out
}
