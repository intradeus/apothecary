package engine

import (
	"fmt"

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

	mr *ports.MRRef

	// openPings are raised-but-unanswered pings to re-register on recovery.
	openPings []*event.PingRaised
	// gateAnswer is set when a gate's ping was answered but the crash hit
	// before the routing was recorded; the gate re-entry routes directly.
	gateAnswer *event.PingAnswered

	done bool
}

// chainKey identifies one link of an edge chain: depth 0 is the edge
// itself, 1+ its then fallbacks. Pre-chain edge_taken events carry no
// depth and land on 0, matching what they were.
func chainKey(from, kind string, depth int) string {
	return fmt.Sprintf("%s|%s|%d", from, kind, depth)
}

// PingGate is re-exported for the engine's callers (the HTTP layer and tests);
// the vocabulary itself belongs to the event catalog.
const PingGate = event.PingGate

func gatePingType(t string) bool { return t == PingGate }

// replay folds a run's event history into runState. fl may be nil when the
// flow is unknown; callers must check.
func replay(evs []event.Event, fl *flow.Flow) *runState {
	rs := &runState{
		fl:            fl,
		iters:         map[string]int{},
		edgeCounts:    map[string]int{},
		actionPending: map[string]bool{},
		sessions:      map[string]ports.SessionID{},
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
		case *event.EnvReady:
			rs.envID = ports.EnvID(p.EnvID)
			rs.links = p.Links
		case *event.EnvFailed:
			rs.done = true
			if p.EnvID != "" {
				rs.envID = ports.EnvID(p.EnvID)
			}
		case *event.ArtifactPublished:
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
		case *event.StageCompleted:
			rs.ph = phAfterComplete
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
		case *event.EdgeTaken:
			rs.edgeCounts[chainKey(p.From, p.Edge, p.Depth)] = p.Count
			if fl != nil {
				rs.pos = fl.Index(p.To)
			}
			rs.ph = phStart
			rs.feed = p.Feed
			rs.feedSentinel = flow.FeedSentinel(p.Edge)
			rs.feedAttempts = p.Max + 1
		case *event.PingRaised:
			raised[p.PingID] = p
			rs.openPings = append(rs.openPings, p)
			if !gatePingType(p.Type) {
				rs.unwanted++
			}
		case *event.PingAnswered:
			for j, op := range rs.openPings {
				if op.PingID == p.PingID {
					rs.openPings = append(rs.openPings[:j], rs.openPings[j+1:]...)
					break
				}
			}
			if r, ok := raised[p.PingID]; ok && gatePingType(r.Type) && rs.ph == phIn {
				rs.gateAnswer = p
			}
		case *event.ActionStarted:
			// Intent recorded, effect not yet confirmed. Cleared by the
			// action's completion event below; anything still set after
			// replay means a crash landed in that window, so the executor
			// must reconcile with the forge instead of re-running blind.
			rs.actionPending[p.StageID] = true
		case *event.MRCreated:
			rs.mr = &ports.MRRef{IID: p.MRIID, URL: p.MRURL}
			for id := range rs.actionPending {
				delete(rs.actionPending, id)
			}
		case *event.RunCompleted, *event.RunAbandoned, *event.RunFailed, *event.RunArchived:
			rs.done = true
		case *event.RunResumed:
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
		case *event.RunPaused, *event.RunUnpaused:
			// View-level only: ph/pos/counters stay exactly as the stage
			// events left them, so play re-enters like crash recovery.
		}
	}
	return rs
}

// openGatePing returns the still-open gate ping raised by the given stage, if
// any. Keyed by stage, not by kind: a flow may have many gates, and only the
// one this stage raised may be re-armed on recovery.
func (rs *runState) openGatePing(stageID string) *event.PingRaised {
	for _, p := range rs.openPings {
		if p.StageID == stageID && gatePingType(p.Type) {
			return p
		}
	}
	return nil
}

// openAgentPings returns open ask_human pings (non-gate).
func (rs *runState) openAgentPings() []*event.PingRaised {
	var out []*event.PingRaised
	for _, p := range rs.openPings {
		if !gatePingType(p.Type) {
			out = append(out, p)
		}
	}
	return out
}
