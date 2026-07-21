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
	runID  string
	view   run.View
	fl     *flow.Flow
	branch string
	envID  ports.EnvID

	pos int
	ph  phase

	iters      map[string]int             // stage id -> times started
	edgeCounts map[string]int             // "from|kind|depth" -> times that chain link was taken
	sessions   map[string]ports.SessionID // stage id -> last recorded harness session

	// links are the workspace access links from env_ready, kept for
	// human_gate payload: workspace (manual_debug's SSH/terminal hand-off).
	links []event.Link

	cost     float64
	turns    int
	unwanted int

	// feed carries edge feedback into the next stage entry (§3.2); persisted
	// in edge_taken, so replay restores it losslessly.
	feed         string
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

func gatePingType(t string) bool {
	return t == string(flow.PingPlanApproval) || t == string(flow.PingFinalReview) ||
		t == string(flow.PingManualDebug)
}

// replay folds a run's event history into runState. fl may be nil when the
// flow is unknown; callers must check.
func replay(evs []event.Event, fl *flow.Flow) *runState {
	rs := &runState{
		fl:         fl,
		iters:      map[string]int{},
		edgeCounts: map[string]int{},
		sessions:   map[string]ports.SessionID{},
	}
	raised := map[string]*event.PingRaised{}
	for i := range evs {
		ev := &evs[i]
		rs.runID = ev.RunID
		run.Apply(&rs.view, ev)
		switch p := ev.Payload.(type) {
		case *event.RunCreated:
			rs.branch = p.Branch
		case *event.EnvReady:
			rs.envID = ports.EnvID(p.EnvID)
			rs.links = p.Links
		case *event.EnvFailed:
			rs.done = true
			if p.EnvID != "" {
				rs.envID = ports.EnvID(p.EnvID)
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
		case *event.MRCreated:
			rs.mr = &ports.MRRef{IID: p.MRIID, URL: p.MRURL}
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

// openGatePing returns the still-open gate ping for the given type, if any.
func (rs *runState) openGatePing(t flow.PingType) *event.PingRaised {
	for _, p := range rs.openPings {
		if p.Type == string(t) {
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
