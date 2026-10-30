package engine

import (
	"fmt"
	"strings"
	"time"

	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/reviewfile"
	"apothecary/internal/run"
)

// phase is where the interpreter stands relative to the stage at pos.
type phase int

const (
	phStart         phase = iota // stage not yet started
	phIn                         // stage_started open (recovery: re-enter)
	phAfterComplete              // stage_completed appended, routing not yet recorded
	phAfterFail                  // stage_failed appended, edge decision not yet recorded (recovery: re-execute)
	phDebug                      // parked on the debug leaf: the stage at pos failed, a human has the workspace (continue re-enters it)
)

// runState is the single-writer goroutine's working state. Everything here
// is reconstructed from events (§2: counters and budgets are never stored
// separately), so a kill -9 at any point is recoverable by replay.
type runState struct {
	runID        string
	view         run.View
	fl           *flow.Flow
	branch       string
	sourceBranch string // branch this run forks off from (origin/<sourceBranch>)
	// params are the parameter values the run runs with (run_created).
	params map[string]string
	// skillsets are the skillset versions the run installs (run_created).
	skillsets []event.SkillsetPin
	envID     ports.EnvID
	// envRequested: the run asked for its workspace once (env_requested is
	// on the log), whatever its state is now.
	envRequested bool

	pos int
	ph  phase
	// endsRun: the cursor past the last stage means the RUN is done — the
	// flow routed to `done` — and not only the phase (`end`, or the chain
	// ran out); abortsRun: the flow routed to `abort` from a success (an
	// outcome's `then: abort`, `on_success: abort`), abortAt/abortReason
	// saying where and why. endReason, when set, is why a phase ended early:
	// a dead end in a watcher, recorded on its phase_completed.
	endsRun     bool
	abortsRun   bool
	abortAt     string
	abortReason string
	endReason   string

	iters      map[string]int // stage id -> times started
	edgeCounts map[string]int // "from|kind|depth" -> times that chain link was taken
	// actionPending marks action stages whose intent was recorded but whose
	// effect was never confirmed (PLAN5 M20) — the crash window between a
	// repository host call returning and its event being appended.
	actionPending map[string]bool
	sessions      map[string]ports.SessionID // stage id -> last recorded harness session

	// links are the workspace access links from env_ready, kept for a
	// human_gate whose input is $WORKSPACE (the terminal/IDE hand-off).
	links []event.Link

	cost     float64
	turns    int
	unwanted int

	// feed carries the content of the edge that re-entered the current stage
	// (§3.2); persisted in edge_taken, so replay restores it losslessly.
	// feedSentinel is which input value it fills ($DECISION or $FAILURE),
	// derived from the edge kind — the target binds one of them or neither.
	feed         string
	feedSentinel string
	feedAttempts int // total allowed attempts, for the "(iteration n of m)" line
	// completedFeed is the verdict the last replayed stage_completed
	// carries (an approval's), for recovery's routing of it.
	completedFeed string
	// ci is what failed in CI as the phase run started (run.ci.failure).
	ci *event.CIReport

	// lastFailure is the open stage_failed when the crash hit before its
	// edge decision; recovery routes it directly (runloop.routeAfterFail).
	lastFailure *event.StageFailed

	// checkouts are the run's repositories (the primary first); mrs the
	// merge requests opened so far, in creation order.
	checkouts []event.Checkout
	mrs       []mrRecord

	// Phases (PLAN6). phase is the phase in progress (or the last one run),
	// subject the checkout whose merge request it acts on ("" = all: the
	// main chain of a run started on a ticket); idle is true between phases.
	// phaseTip is the subject branch's head when the phase started
	// ($DIFF_PHASE counts from it); phaseHead the newest head the phase run
	// knows there — phaseTip, then each tip it pushed. A merge or a review
	// is pinned to phaseHead: never a head the run has not seen.
	phase     string
	subject   string
	idle      bool
	phaseTip  string
	phaseHead string
	// triggerSeen holds every (phase, subject, key) a phase run started for
	// or a human declined (R6); phaseRuns counts runs per (phase, subject).
	triggerSeen map[string]bool
	phaseRuns   map[string]int
	// confirms are the phase_confirm pings raised, oldest first; their
	// answers sit in answeredPings until the idle loop acts on them.
	confirms []*event.PingRaised
	// round are the review threads this phase run loaded (M31), in its scope
	// (pending or all): a re-entry rewrites the same set, and replies and
	// continuations may name only these.
	round       []event.LoadedThread
	roundScope  string
	roundLoaded bool
	// selectPing is the thread-selection question this phase run raised.
	selectPing *event.PingRaised
	// skipped are the threads a human chose not to fix: thread id → its last
	// comment id at the time. Kept across rounds.
	skipped map[string]string
	// dropped are the review notes a person declined to post at a gate,
	// kept for the whole run: a re-review never proposes them again.
	dropped []reviewfile.Note
	// curated are the review documents a person approved at a gate, by
	// path, not yet written back: a gate has no workspace of its own, so the
	// next stage that needs the workspace and reads the path writes them
	// first. curatedSeen is every ping whose curation is on the record, so a
	// gate routed again after a crash records it once.
	curated     map[string]*event.ReviewCurated
	curatedSeen map[string]bool
	// pushed is the tip push_branch last pushed, per repo+branch.
	pushed map[string]string
	// adopted is the merge request the run was created on (nil when it
	// opens its own); followed the head its checkout was last brought to.
	adopted  *event.AdoptedMR
	followed string
	// heads are the commits the run knows on its merge requests, per
	// checkout: every tip it pushed, the head it opened or adopted a merge
	// request at, the tip a phase run began on. A head outside them is
	// someone else's push (mr_head_moved).
	heads map[string]bool

	// parked holds the phase runs stopped at a wait point mid-chain, by
	// phase: everything scoped to the run (park / unpark), while another
	// phase may hold runState's own fields. At most one live run per phase.
	parked map[string]*phaseRun
	// anyStarted: some stage has started (a stage_started is on the log).
	anyStarted bool
	// elided marks stages whose last start was a skip (a condition that did
	// not hold): they never touched the workspace.
	elided map[string]bool

	// openPings are raised-but-unanswered pings to re-register on recovery.
	openPings []*event.PingRaised
	// gateAnswer is set when a gate's ping was answered but the crash hit
	// before the routing was recorded; the gate re-entry routes directly.
	gateAnswer *event.PingAnswered

	// raisedPings / answeredPings are every ping's two halves, kept for the
	// re-entry that rebuilds a paused attempt's resume message. A run parked
	// on a question is answered while it has NO loop, so by the time one
	// exists again the answer is only a fact in the log — the live registry
	// never saw it.
	raisedPings   map[string]*event.PingRaised
	answeredPings map[string]*event.PingAnswered
	// pausedAsks is the ping set a stage last parked on (stage_paused), by
	// stage id: what re-entry has to collect answers for before resuming.
	pausedAsks map[string][]string

	// agentCursor is, per stage, the position in the agent's own event
	// stream of the last transcript event recorded: after a restart, a turn
	// still in flight is followed again from right there (Reattach).
	agentCursor map[string]string
	// pendingMsgs are human messages accepted but not yet handed to the
	// agent (message_queued without message_delivered), oldest first. They
	// ride into the next prompt.
	pendingMsgs []*event.MessageQueued

	// holds is the take-over in force per stage (hold_taken without its
	// hold_released), with what its parked legs have banked so far.
	holds map[string]*heldState
	// pausedHold marks a stage parked on its holder (stage_paused held, no
	// stage_resumed since): re-entry waits for the holder's word.
	pausedHold map[string]bool
	// handbackOwed marks a stage whose hold was released but whose session
	// has not yet been sent the handback prompt (agent_prompted handback).
	handbackOwed map[string]bool

	done bool
}

// heldState is a take-over as replay knows it: who took it, when, and the
// spend of every leg that parked (each stage_paused{held} banks its own).
type heldState struct {
	taken   *event.HoldTaken
	takenAt time.Time
	banked  event.HoldSpend
}

// chainKey identifies one link of an edge chain: depth 0 is the edge
// itself, 1+ its then fallbacks. Pre-chain edge_taken events carry no
// depth and land on 0, matching what they were.
func chainKey(from, kind string, depth int) string {
	return fmt.Sprintf("%s|%s|%d", from, kind, depth)
}

// PingGate and PingDebug are re-exported for the engine's callers (the HTTP
// layer and tests); the vocabulary itself belongs to the event catalog.
const (
	PingGate  = event.PingGate
	PingDebug = event.PingDebug
)

func gatePingType(t string) bool  { return t == PingGate }
func debugPingType(t string) bool { return t == PingDebug }

// decidesHere reports whether an answer to this ping is the routing decision
// the stage at pos is waiting for: its gate's ping while the gate is open, or
// the debug leaf's while the run is parked there — and the stage's own ping,
// never a stale one an earlier attempt raised.
func (rs *runState) decidesHere(r *event.PingRaised) bool {
	if rs.fl == nil || rs.pos < 0 || rs.pos >= len(rs.fl.Stages) || r.StageID != rs.fl.Stages[rs.pos].ID {
		return false
	}
	return (gatePingType(r.Type) && rs.ph == phIn) || (debugPingType(r.Type) && rs.ph == phDebug)
}

// agentPingType: a question an agent raised through ask_human — the kind a
// well-briefed agent should not need, hence counted as unwanted. Everything
// else (a stage's gate, a phase confirmation, a selection) is the engine
// asking on purpose.
func agentPingType(t string) bool {
	return t == event.PingClarification || t == event.PingBlocker || t == event.PingPermission
}

func triggerKey(phase, subject, key string) string { return phase + "\x00" + subject + "\x00" + key }
func phaseRunKey(phase, subject string) string     { return phase + "\x00" + subject }

// phaseEverRan reports whether the phase ran at all, on any subject.
func (rs *runState) phaseEverRan(phase string) bool {
	for k, n := range rs.phaseRuns {
		if n > 0 && strings.HasPrefix(k, phase+"\x00") {
			return true
		}
	}
	return false
}

// phaseRun is what is scoped to one phase run: where it stands and what it
// carries. The active run's lives in runState's own fields; a parked run's
// is kept here until it resumes. Stage-keyed maps (iters, edgeCounts,
// sessions, …) stay shared: stage ids are one namespace across phases.
type phaseRun struct {
	phase, subject, tip, head string
	pos                       int
	ph                        phase
	feed, feedSentinel        string
	feedAttempts              int
	lastFailure               *event.StageFailed
	gateAnswer                *event.PingAnswered
	round                     []event.LoadedThread
	roundScope                string
	roundLoaded               bool
	selectPing                *event.PingRaised
	ci                        *event.CIReport
	// parkedAt is the stage the run waits at (a wait or a gate), ping the
	// decision a gate waits on, since when it parked (a wait's timeout runs
	// from there).
	parkedAt string
	ping     string
	since    time.Time
}

// park stores the active phase run as parked at stage and leaves the run
// idle. Shared by replay and the live park so they cannot disagree.
func (rs *runState) park(stage, ping string, since time.Time) {
	if rs.parked == nil {
		rs.parked = map[string]*phaseRun{}
	}
	rs.parked[rs.phase] = &phaseRun{
		phase: rs.phase, subject: rs.subject, tip: rs.phaseTip, head: rs.phaseHead, pos: rs.pos, ph: rs.ph,
		feed: rs.feed, feedSentinel: rs.feedSentinel, feedAttempts: rs.feedAttempts,
		lastFailure: rs.lastFailure, gateAnswer: rs.gateAnswer,
		round: rs.round, roundScope: rs.roundScope, roundLoaded: rs.roundLoaded, selectPing: rs.selectPing, ci: rs.ci,
		parkedAt: stage, ping: ping, since: since,
	}
	rs.idle = true
}

// unpark makes the parked run of phase the active one again. False when
// no run of that phase is parked.
func (rs *runState) unpark(phase string) bool {
	pr := rs.parked[phase]
	if pr == nil {
		return false
	}
	delete(rs.parked, phase)
	rs.phase, rs.subject, rs.phaseTip, rs.phaseHead, rs.pos, rs.ph = pr.phase, pr.subject, pr.tip, pr.head, pr.pos, pr.ph
	rs.feed, rs.feedSentinel, rs.feedAttempts = pr.feed, pr.feedSentinel, pr.feedAttempts
	rs.lastFailure, rs.gateAnswer = pr.lastFailure, pr.gateAnswer
	rs.round, rs.roundScope, rs.roundLoaded, rs.selectPing = pr.round, pr.roundScope, pr.roundLoaded, pr.selectPing
	rs.ci = pr.ci
	rs.endsRun, rs.abortsRun, rs.endReason = false, false, ""
	rs.idle = false
	return true
}

// anyParked reports whether some phase run waits at a wait point.
func (rs *runState) anyParked() bool { return len(rs.parked) > 0 }

// roundIDs lists the threads this phase run loaded, by id.
func (rs *runState) roundIDs() []string {
	out := make([]string, 0, len(rs.round))
	for _, t := range rs.round {
		out = append(out, t.ID)
	}
	return out
}

// roundThread is the loaded thread with that id, or nil.
func (rs *runState) roundThread(id string) *event.LoadedThread {
	for i := range rs.round {
		if rs.round[i].ID == id {
			return &rs.round[i]
		}
	}
	return nil
}

// noteCurated folds a gate's curation into the run: what a person dropped
// stays dropped for the run, the threads they held a reply back from are
// left alone until someone writes there again, and an approved document
// waits to be written back. Shared by replay and the live gate so they
// cannot disagree.
func (rs *runState) noteCurated(p *event.ReviewCurated) {
	rs.curatedSeen[p.PingID] = true
	rs.dropped = append(rs.dropped, p.Dropped...)
	for id, last := range p.Skipped {
		rs.skipped[id] = last
	}
	if p.Approved {
		rs.curated[p.Path] = p
	}
}

// forgetCurated drops the curated documents waiting on paths the starting
// stage writes: what it writes is the new document, and a curation of the
// old one must never be written over it.
func (rs *runState) forgetCurated(fl *flow.Flow, stageID string) {
	if fl == nil || len(rs.curated) == 0 {
		return
	}
	if i := fl.Index(stageID); i >= 0 {
		for _, o := range fl.Stages[i].Outputs {
			delete(rs.curated, flow.FilePath(o.Value))
		}
	}
}

// startPhase moves the cursor to a phase's first stage, fresh: its edge
// counters reset (R7 — a review round's retries are per round), the feed
// cleared. Shared by replay and the live start so they cannot disagree.
func (rs *runState) startPhase(p *event.PhaseStarted, fl *flow.Flow) {
	rs.phase, rs.subject, rs.phaseTip, rs.phaseHead, rs.idle = p.Phase, p.Subject, p.Tip, p.Tip, false
	rs.ci = p.CI
	rs.round, rs.roundScope, rs.roundLoaded, rs.selectPing = nil, "", false, nil
	rs.endsRun, rs.abortsRun, rs.endReason = false, false, ""
	rs.phaseRuns[phaseRunKey(p.Phase, p.Subject)]++
	rs.noteHead(p.Subject, p.Tip)
	if p.Key != "" {
		rs.triggerSeen[triggerKey(p.Phase, p.Subject, p.Key)] = true
	}
	if fl == nil {
		return
	}
	ph := fl.Phase(p.Phase)
	if ph == nil {
		rs.pos = -1
		return
	}
	for i := ph.First; i <= ph.Last; i++ {
		prefix := fl.Stages[i].ID + "|"
		for k := range rs.edgeCounts {
			if strings.HasPrefix(k, prefix) {
				delete(rs.edgeCounts, k)
			}
		}
	}
	rs.pos, rs.ph = ph.First, phStart
	rs.feed, rs.feedSentinel, rs.feedAttempts = "", "", 0
	rs.lastFailure, rs.gateAnswer = nil, nil
	if ph.Then != "" && ph.Last < ph.First {
		// An outcome with no chain: its start is its end — the run's.
		rs.pos = len(fl.Stages)
		rs.endsRun = ph.Then == flow.TerminalDone
		rs.abortsRun = ph.Then == flow.TerminalAbort
		rs.abortAt, rs.abortReason = "", fmt.Sprintf("aborted by flow: outcome %s", ph.ID)
	}
}

// replay folds a run's event history into runState. fl may be nil when the
// flow is unknown; callers must check.
func replay(evs []event.Event, fl *flow.Flow) *runState {
	rs := &runState{
		fl:            fl,
		iters:         map[string]int{},
		edgeCounts:    map[string]int{},
		actionPending: map[string]bool{},
		sessions:      map[string]ports.SessionID{},
		raisedPings:   map[string]*event.PingRaised{},
		answeredPings: map[string]*event.PingAnswered{},
		pausedAsks:    map[string][]string{},
		agentCursor:   map[string]string{},
		triggerSeen:   map[string]bool{},
		phaseRuns:     map[string]int{},
		elided:        map[string]bool{},
		pushed:        map[string]string{},
		heads:         map[string]bool{},
		skipped:       map[string]string{},
		curated:       map[string]*event.ReviewCurated{},
		curatedSeen:   map[string]bool{},
		holds:         map[string]*heldState{},
		pausedHold:    map[string]bool{},
		handbackOwed:  map[string]bool{},
	}
	raised := map[string]*event.PingRaised{}
	for i := range evs {
		ev := &evs[i]
		rs.runID = ev.RunID
		run.Apply(&rs.view, ev)
		switch p := ev.Payload.(type) {
		case *event.RunCreated:
			rs.branch = p.Branch
			rs.sourceBranch = p.SourceBranch
			rs.params = p.Params
			rs.skillsets = p.Skillsets
			rs.checkouts = checkoutsOf(p)
			if m := p.MR; m != nil {
				rs.adopted = m
				if co := rs.checkout(m.Repo); co != nil {
					rs.addMR(&event.MRCreated{Repo: m.Repo, Branch: co.Branch, Target: co.Source, MRURL: m.URL, MRIID: m.IID, Head: m.Head})
				}
			}
		case *event.EnvRequested:
			rs.envRequested = true
		case *event.EnvReady:
			rs.envID = ports.EnvID(p.EnvID)
			rs.links = p.Links
		case *event.EnvFailed:
			rs.done = true
			if p.EnvID != "" {
				rs.envID = ports.EnvID(p.EnvID)
			}
		case *event.SnapshotCaptured:
			// Keeps pseudo-stage counters monotonic across restarts: the
			// provisioning log has no stage_started to replay its iteration
			// from. A real stage's snapshots never exceed its started count,
			// so this is a no-op for them.
			if p.Iteration > rs.iters[p.StageID] {
				rs.iters[p.StageID] = p.Iteration
			}
		case *event.StageStarted:
			rs.anyStarted = true
			delete(rs.elided, p.StageID)
			rs.forgetCurated(fl, p.StageID)
			if fl != nil {
				rs.pos = fl.Index(p.StageID)
			}
			rs.ph = phIn
			if p.Iteration > rs.iters[p.StageID] {
				rs.iters[p.StageID] = p.Iteration
			}
			rs.gateAnswer = nil
			rs.lastFailure = nil
			delete(rs.pausedAsks, p.StageID)
		case *event.StageCompleted:
			rs.ph = phAfterComplete
			// What the routing that follows (advance) sets: the verdict the
			// approval carries, or nothing — a feed the stage was entered with
			// is spent.
			rs.completedFeed = p.Feed
			rs.feed, rs.feedSentinel, rs.feedAttempts = p.Feed, "", 0
			if p.Feed != "" {
				rs.feedSentinel = flow.SourceVerdict
			}
			if p.Skipped {
				rs.elided[p.StageID] = true
			}
			// A completed stage settled whatever intent it recorded: a later
			// run of the same action (a second review round) starts clean.
			delete(rs.actionPending, p.StageID)
			if p.SessionID != "" {
				rs.sessions[p.StageID] = ports.SessionID(p.SessionID)
			}
			if p.Usage != nil {
				rs.cost += p.Usage.CostUSD
			}
			rs.turns += p.TurnsUsed
		case *event.StageFailed:
			rs.ph = phAfterFail
			rs.lastFailure = p
		case *event.StagePaused:
			rs.pausedAsks[p.StageID] = p.PingIDs
			// The attempt is parked, not finished: ph stays phIn so recovery
			// re-enters this stage. The session it recorded is what the
			// re-entry resumes — without it a restart would have to start the
			// agent over and lose everything it had done before the question.
			// Cost and turns are banked here because the invocation that just
			// ended really did spend them.
			rs.ph = phIn
			if p.SessionID != "" {
				rs.sessions[p.StageID] = ports.SessionID(p.SessionID)
			}
			if p.Usage != nil {
				rs.cost += p.Usage.CostUSD
			}
			rs.turns += p.TurnsUsed
			// A park on the holder of a take-over: re-entry waits for their
			// word. Either park banks what the hold's leg spent.
			if p.Reason == event.PauseHeld {
				rs.pausedHold[p.StageID] = true
			}
			if h := rs.holds[p.StageID]; h != nil && p.Hold != nil {
				h.banked.Add(*p.Hold)
			}
		case *event.StageResumed:
			// The pause it answers is consumed: leaving it set would make a
			// LATER attempt of the same stage re-enter as if it were still
			// parked on those long-answered questions.
			delete(rs.pausedAsks, p.StageID)
			delete(rs.pausedHold, p.StageID)
			// Bookkeeping only — the pause already set everything the
			// re-entry needs, and the invocation this event opens reports its
			// own spend when it ends (stage_paused again, or stage_completed).
			rs.ph = phIn
		case *event.EdgeTaken:
			rs.edgeCounts[chainKey(p.From, p.Edge, p.Depth)] = p.Count
			if p.To == flow.TerminalEnd || p.To == flow.TerminalDone {
				// The edge ended the phase — or, with done, the run.
				if fl != nil {
					rs.pos = len(fl.Stages)
				}
				rs.endsRun = p.To == flow.TerminalDone
				rs.ph = phStart
				rs.feed, rs.feedSentinel, rs.feedAttempts = "", "", 0
				break
			}
			if p.To == flow.ReservedDebug {
				// The debug leaf: the cursor stays on the stage that took the
				// link, parked for a human (runloop.stageDebug).
				if fl != nil {
					rs.pos = fl.Index(p.From)
				}
				rs.ph = phDebug
				rs.feed = p.Feed
				rs.feedSentinel = flow.FeedSource(p.Edge)
				rs.feedAttempts = 0
				break
			}
			if fl != nil {
				rs.pos = fl.Index(p.To)
			}
			rs.ph = phStart
			rs.feed = p.Feed
			rs.feedSentinel = flow.FeedSource(p.Edge)
			rs.feedAttempts = p.Max + 1
		case *event.PhaseStarted:
			if rs.phase == "" && rs.anyStarted {
				// A start recorded after the fact (beginRun on a log that had
				// stage events but no phase start): the cursor stays where the
				// stage events put it.
				rs.phase, rs.subject, rs.idle = p.Phase, p.Subject, false
				rs.phaseRuns[phaseRunKey(p.Phase, p.Subject)]++
				break
			}
			rs.startPhase(p, fl)
		case *event.PhaseCompleted:
			rs.idle = true
			rs.endsRun, rs.abortsRun, rs.endReason = false, false, ""
		case *event.PhaseParked:
			rs.park(p.StageID, p.PingID, ev.TS)
		case *event.PhaseResumed:
			rs.unpark(p.Phase)
		case *event.BranchPushed:
			rs.notePush(p.Repo, p.Branch, p.Tip)
		case *event.ReviewCommentsLoaded:
			rs.round, rs.roundScope, rs.roundLoaded = append([]event.LoadedThread(nil), p.Threads...), p.Scope, true
			for id, last := range p.Skipped {
				rs.skipped[id] = last
			}
		case *event.ReviewCurated:
			rs.noteCurated(p)
		case *event.PhaseDeclined:
			rs.triggerSeen[triggerKey(p.Phase, p.Subject, p.Key)] = true
		case *event.PingRaised:
			if p.Type == event.PingPhaseConfirm {
				rs.confirms = append(rs.confirms, p)
			}
			if p.Type == event.PingSelect {
				rs.selectPing = p
			}
			raised[p.PingID] = p
			rs.raisedPings[p.PingID] = p
			rs.openPings = append(rs.openPings, p)
			if agentPingType(p.Type) {
				rs.unwanted++
			}
		case *event.PingAnswered:
			rs.answeredPings[p.PingID] = p
			for j, op := range rs.openPings {
				if op.PingID == p.PingID {
					rs.openPings = append(rs.openPings[:j], rs.openPings[j+1:]...)
					break
				}
			}
			if r, ok := raised[p.PingID]; ok && rs.decidesHere(r) {
				rs.gateAnswer = p
			}
		case *event.ActionStarted:
			// Intent recorded, effect not yet confirmed. Cleared by the
			// action's completion event below; anything still set after replay
			// means a crash landed in that window, so the executor must
			// reconcile with the repository host instead of re-running blind.
			rs.actionPending[p.StageID] = true
		case *event.MRCreated:
			rs.addMR(p)
			// The action's intent is settled only once every merge request it
			// publishes exists; a partial publication keeps reconciling.
		case *event.RunCompleted, *event.RunAbandoned, *event.RunFailed, *event.RunArchived:
			rs.done = true
			// The run ended with them open: withdrawn, as the projection reads
			// them (run.ReducePings). A retry must not re-register a ping
			// nothing can route anymore.
			rs.openPings = nil
		case *event.RunRetried:
			// "Try again": un-terminal and re-enter the failed stage fresh.
			// Clearing edgeCounts restores every stage's on_fail/on_reject
			// retry budget from the resume point; feed/lastFailure reset so
			// the stage runs clean, not as a routed recovery.
			rs.done = false
			rs.ph = phStart
			rs.feed = ""
			rs.feedSentinel = ""
			rs.feedAttempts = 0
			rs.lastFailure = nil
			rs.gateAnswer = nil
			rs.edgeCounts = map[string]int{}
			if fl != nil {
				rs.pos = fl.Index(p.StageID)
			}
		case *event.AgentSession:
			rs.sessions[p.StageID] = ports.SessionID(p.SessionID)
		case *event.AgentMessage:
			rs.noteCursor(p.StageID, p.Cursor)
		case *event.AgentTool:
			rs.noteCursor(p.StageID, p.Cursor)
		case *event.AgentPlan:
			rs.noteCursor(p.StageID, p.Cursor)
		case *event.AgentNotice:
			rs.noteCursor(p.StageID, p.Cursor)
		case *event.MessageQueued:
			rs.pendingMsgs = append(rs.pendingMsgs, p)
		case *event.MessageDelivered:
			rs.delivered(p.MessageIDs)
		case *event.HoldTaken:
			rs.holds[p.StageID] = &heldState{taken: p, takenAt: ev.TS}
		case *event.HoldReleased:
			delete(rs.holds, p.StageID)
			if p.Reason == event.HoldReleasedByUser {
				rs.handbackOwed[p.StageID] = true
			}
		case *event.AgentPrompted:
			if p.Kind == event.PromptHandback {
				delete(rs.handbackOwed, p.StageID)
			}
		case *event.RunPaused, *event.RunUnpaused:
			// View-level only: ph/pos/counters stay exactly as the stage
			// events left them, so play re-enters like crash recovery.
		}
	}
	return rs
}

// openPing returns the still-open ping of this type raised by (or for) the
// given stage, if any. Keyed by stage: a flow may have many gates, and only
// the one this stage raised may be re-armed on recovery.
func (rs *runState) openPing(stageID, ptype string) *event.PingRaised {
	for _, p := range rs.openPings {
		if p.StageID == stageID && p.Type == ptype {
			return p
		}
	}
	return nil
}

// openAgentPings returns open ask_human pings (non-gate).
func (rs *runState) openAgentPings() []*event.PingRaised {
	var out []*event.PingRaised
	for _, p := range rs.openPings {
		if agentPingType(p.Type) {
			out = append(out, p)
		}
	}
	return out
}

func (rs *runState) noteCursor(stageID, cursor string) {
	if cursor != "" {
		rs.agentCursor[stageID] = cursor
	}
}

// delivered drops handed-over messages from the pending queue.
func (rs *runState) delivered(ids []string) {
	if len(ids) == 0 {
		return
	}
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	kept := rs.pendingMsgs[:0]
	for _, m := range rs.pendingMsgs {
		if !gone[m.MessageID] {
			kept = append(kept, m)
		}
	}
	rs.pendingMsgs = kept
}

// anyWorkspaceStageStarted reports whether a stage that USES the workspace
// has run yet (the provisioning log's pseudo-stage counter does not count) —
// what decides whether a fresh run's model check is still due. Gates, waits
// and remote actions start without one, so counting them would skip the
// check on a flow that opens with one.
func (rs *runState) anyWorkspaceStageStarted() bool {
	if rs.fl == nil {
		return true
	}
	for i := range rs.fl.Stages {
		if st := &rs.fl.Stages[i]; needsWorkspace(st) && rs.iters[st.ID] > 0 && !rs.elided[st.ID] {
			return true
		}
	}
	return false
}

// needsWorkspace reports whether running st requires the environment: an
// agent or a command always; a gate or a wait never (they park on something
// outside the run); an action unless it is remote with no file input bound
// (a tracker call needs no checkout — and must not re-provision a reaped
// one after the final gate).
func needsWorkspace(st *flow.Stage) bool {
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

// mrRecord is one merge request the run opened: which checkout and branch,
// onto what, and the repository host's handle.
type mrRecord struct {
	Repo   string
	Branch string
	Target string
	Ref    ports.MRRef
}

// checkoutsOf is the run's checkout list: as recorded, or — for runs older
// than multi-repo support — the one primary the event's flat fields describe.
func checkoutsOf(p *event.RunCreated) []event.Checkout {
	if len(p.Checkouts) > 0 {
		return append([]event.Checkout(nil), p.Checkouts...)
	}
	return []event.Checkout{{Name: event.PrimaryCheckoutName, Dir: event.PrimaryCheckoutDir,
		URL: p.RepoURL, Branch: p.Branch, Source: p.SourceBranch, Primary: true}}
}

func (rs *runState) checkout(name string) *event.Checkout {
	if name == "" {
		name = event.PrimaryCheckoutName
	}
	for i := range rs.checkouts {
		if rs.checkouts[i].Name == name {
			return &rs.checkouts[i]
		}
	}
	return nil
}

// checkoutDirs lists every checkout's directory (primary first), for a
// command that runs in each.
func (rs *runState) checkoutDirs() []string {
	dirs := make([]string, 0, len(rs.checkouts))
	for _, c := range rs.checkouts {
		dirs = append(dirs, c.Dir)
	}
	if len(dirs) == 0 {
		dirs = []string{event.PrimaryCheckoutDir}
	}
	return dirs
}

// notePush records a tip push_branch pushed: the run knows it, and so does
// the phase run whose subject's branch it went to. Shared by replay and the
// live push so they cannot disagree.
func (rs *runState) notePush(repo, branch, tip string) {
	rs.pushed[repo+"\x00"+branch] = tip
	rs.noteHead(repo, tip)
	if repo == rs.subject {
		rs.phaseHead = tip
	}
}

// noteHead records a commit the run knows on a checkout's branch.
func (rs *runState) noteHead(repo, sha string) {
	if sha == "" {
		return
	}
	if rs.heads == nil {
		rs.heads = map[string]bool{}
	}
	rs.heads[repo+"\x00"+sha] = true
}

// knownHead reports whether sha is a commit the run knows on repo's branch.
func (rs *runState) knownHead(repo, sha string) bool { return rs.heads[repo+"\x00"+sha] }

func (rs *runState) addMR(p *event.MRCreated) {
	repo := p.Repo
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	rs.noteHead(repo, p.Head)
	for i := range rs.mrs {
		if rs.mrs[i].Repo == repo && rs.mrs[i].Branch == p.Branch {
			rs.mrs[i].Ref = ports.MRRef{IID: p.MRIID, URL: p.MRURL}
			rs.mrs[i].Target = p.Target
			return
		}
	}
	rs.mrs = append(rs.mrs, mrRecord{Repo: repo, Branch: p.Branch, Target: p.Target, Ref: ports.MRRef{IID: p.MRIID, URL: p.MRURL}})
}

func (rs *runState) mrFor(repo, branch string) *mrRecord {
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
	for i := range rs.mrs {
		if rs.mrs[i].Repo == repo && rs.mrs[i].Branch == branch {
			return &rs.mrs[i]
		}
	}
	return nil
}

// firstMR is the merge request a single-MR world would call "the" MR: the
// first one opened (the primary repository's).
func (rs *runState) firstMR() *ports.MRRef {
	if len(rs.mrs) == 0 {
		return nil
	}
	return &rs.mrs[0].Ref
}
