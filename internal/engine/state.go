package engine

import (
	"fmt"
	"strings"

	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
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
	envID        ports.EnvID
	// envRequested: the run asked for its workspace once (env_requested is
	// on the log), whatever its state is now.
	envRequested bool

	pos int
	ph  phase

	iters      map[string]int // stage id -> times started
	edgeCounts map[string]int // "from|kind|depth" -> times that chain link was taken
	// actionPending marks action stages whose intent was recorded but whose
	// effect was never confirmed (PLAN5 M20) — the crash window between a
	// forge call returning and its event being appended.
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

	// lastFailure is the open stage_failed when the crash hit before its
	// edge decision; recovery routes it directly (runloop.routeAfterFail).
	lastFailure *event.StageFailed

	// checkouts are the run's repositories (the primary first); mrs the
	// merge requests opened so far, in creation order.
	checkouts []event.Checkout
	mrs       []mrRecord

	// Phases (PLAN6). phase is the phase in progress (or the last one run),
	// subject the checkout whose merge request it acts on ("" = all: the
	// entry); idle is true between phases. phaseTip is the subject branch's
	// head when the phase started ($CHANGED inside it).
	phase    string
	subject  string
	idle     bool
	phaseTip string
	// triggerSeen holds every (phase, subject, key) a phase run started for
	// or a human declined (R6); phaseRuns counts runs per (phase, subject).
	triggerSeen map[string]bool
	phaseRuns   map[string]int
	// confirms are the phase_confirm pings raised, oldest first; their
	// answers sit in answeredPings until the idle loop acts on them.
	confirms []*event.PingRaised
	// roundThreads are the review threads this phase run loaded (M31): a
	// re-entry rewrites the same set, and replies may name only these.
	roundThreads []string
	roundLoaded  bool
	// selectPing is the thread-selection question this phase run raised.
	selectPing *event.PingRaised
	// skipped are the threads a human chose not to fix: thread id → its last
	// comment id at the time. Kept across rounds.
	skipped map[string]string
	// pushed is the tip push_branch last pushed, per repo+branch.
	pushed map[string]string

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

	done bool
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

// startPhase moves the cursor to a phase's first stage, fresh: its edge
// counters reset (R7 — a review round's retries are per round), the feed
// cleared. Shared by replay and the live start so they cannot disagree.
func (rs *runState) startPhase(p *event.PhaseStarted, fl *flow.Flow) {
	rs.phase, rs.subject, rs.phaseTip, rs.idle = p.Phase, p.Subject, p.Tip, false
	rs.roundThreads, rs.roundLoaded, rs.selectPing = nil, false, nil
	rs.phaseRuns[phaseRunKey(p.Phase, p.Subject)]++
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
		pushed:        map[string]string{},
		skipped:       map[string]string{},
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
			rs.checkouts = checkoutsOf(p)
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
		case *event.StageResumed:
			// The pause it answers is consumed: leaving it set would make a
			// LATER attempt of the same stage re-enter as if it were still
			// parked on those long-answered questions.
			delete(rs.pausedAsks, p.StageID)
			// Bookkeeping only — the pause already set everything the
			// re-entry needs, and the invocation this event opens reports its
			// own spend when it ends (stage_paused again, or stage_completed).
			rs.ph = phIn
		case *event.EdgeTaken:
			rs.edgeCounts[chainKey(p.From, p.Edge, p.Depth)] = p.Count
			if p.To == flow.ReservedDebug {
				// The debug leaf: the cursor stays on the stage that took the
				// link, parked for a human (runloop.stageDebug).
				if fl != nil {
					rs.pos = fl.Index(p.From)
				}
				rs.ph = phDebug
				rs.feed = p.Feed
				rs.feedSentinel = flow.FeedSentinel(p.Edge)
				rs.feedAttempts = 0
				break
			}
			if fl != nil {
				rs.pos = fl.Index(p.To)
			}
			rs.ph = phStart
			rs.feed = p.Feed
			rs.feedSentinel = flow.FeedSentinel(p.Edge)
			rs.feedAttempts = p.Max + 1
		case *event.PhaseStarted:
			rs.startPhase(p, fl)
		case *event.PhaseCompleted:
			rs.idle = true
		case *event.BranchPushed:
			rs.pushed[p.Repo+"\x00"+p.Branch] = p.Tip
		case *event.ReviewCommentsLoaded:
			rs.roundThreads, rs.roundLoaded = append([]string(nil), p.ThreadIDs...), true
			for id, last := range p.Skipped {
				rs.skipped[id] = last
			}
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
			// action's completion event below; anything still set after
			// replay means a crash landed in that window, so the executor
			// must reconcile with the forge instead of re-running blind.
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
		if st := &rs.fl.Stages[i]; needsWorkspace(st) && rs.iters[st.ID] > 0 {
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
		for _, in := range st.Inputs {
			if sig := act.Input(in.Name); sig != nil && sig.Kind == actionspec.KindFile {
				return true
			}
		}
		return false
	}
	return true
}

// mrRecord is one merge request the run opened: which checkout and branch,
// onto what, and the forge's handle.
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

func (rs *runState) addMR(p *event.MRCreated) {
	repo := p.Repo
	if repo == "" {
		repo = event.PrimaryCheckoutName
	}
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
