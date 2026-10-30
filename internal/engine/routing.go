package engine

import (
	"fmt"

	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
)

// The interpreter's decisions, pure: given the run's state and a stage, where
// a success goes, which link of an edge chain a failure or rejection takes,
// whether the budget is spent, whether a stage runs, and what the loop does
// next. The loop (runloop.go) carries them out and records them; nothing
// here does I/O or reads a clock.

// routeSuccess moves the cursor along st's on_success, carrying verdict — what
// an approval said (flow.GivesVerdict) — to the target's edge.verdict ("" for
// nothing). end parks the cursor past the last stage, which the loop reads as
// the phase's end; done does the same and marks the run done; abort marks it
// aborted. No event records this move: the next step does (see runState).
func (rs *runState) routeSuccess(st *flow.Stage, verdict string) {
	switch st.OnSuccess {
	case flow.TerminalEnd, flow.TerminalDone, flow.TerminalAbort:
		rs.pos = len(rs.fl.Stages)
		rs.endsRun = st.OnSuccess == flow.TerminalDone
		rs.abortsRun = st.OnSuccess == flow.TerminalAbort
		if rs.abortsRun {
			rs.abortAt, rs.abortReason = st.ID, "aborted by flow: on_success at stage "+st.ID
		}
	default:
		rs.pos = rs.fl.Index(st.OnSuccess) // parse-validated: always >= 0
	}
	rs.ph = phStart
	rs.feed, rs.feedSentinel, rs.feedAttempts = verdict, "", 0
	if verdict != "" {
		rs.feedSentinel = flow.SourceVerdict
	}
}

// endPhase puts the cursor past the chain: the phase in progress is over
// (its phase_completed, appended by the loop's next step, records it).
func (rs *runState) endPhase() {
	rs.pos = len(rs.fl.Stages)
	rs.endsRun, rs.abortsRun = false, false
	rs.ph = phStart
}

// budgetExceeded reports whether the run's spend is past the flow's §3
// ceilings, and says which.
func budgetExceeded(b flow.Budget, cost float64, turns int) (bool, string) {
	if cost > b.MaxCostUSD {
		return true, fmt.Sprintf("budget exceeded: cost %.2f USD > max %.2f", cost, b.MaxCostUSD)
	}
	if turns > b.MaxTotalTurns {
		return true, fmt.Sprintf("budget exceeded: %d turns > max %d", turns, b.MaxTotalTurns)
	}
	return false, ""
}

// edgeChoiceKind is what following a stage's edge of some kind comes to.
type edgeChoiceKind int

const (
	edgeNone      edgeChoiceKind = iota // the stage declares no edge of that kind
	edgeExhausted                       // every link of the chain is spent
	edgeEnd                             // a link to end or done
	edgeAbort                           // a link to abort
	edgeDebug                           // a link to the debug leaf
	edgeLink                            // a link to a stage
)

// edgeChoice is the link a stage's edge chain gives: which, at what depth,
// and — for a counted link — what its counter becomes.
type edgeChoice struct {
	kind  edgeChoiceKind
	edge  *flow.Edge
	depth int
	count int
}

// chooseEdge follows st's edge of the given kind (on_fail, on_reject,
// on_timeout). The edge is a chain: each link carries its own cumulative
// max, and a full link falls through to its then fallback
// (retry-then-escalate). end, done and abort are taken at once; the debug
// leaf is counted but never exhausted (the human decides when to stop).
func (rs *runState) chooseEdge(st *flow.Stage, kind string) edgeChoice {
	edge := st.Edge(kind)
	if edge == nil {
		return edgeChoice{kind: edgeNone}
	}
	depth := 0
	for e := edge; e != nil; e, depth = e.Then, depth+1 {
		switch e.To {
		case flow.TerminalEnd, flow.TerminalDone:
			return edgeChoice{kind: edgeEnd, edge: e, depth: depth}
		case flow.TerminalAbort:
			return edgeChoice{kind: edgeAbort, edge: e, depth: depth}
		}
		count := rs.edgeCounts[chainKey(st.ID, kind, depth)] + 1
		if e.To == flow.ReservedDebug {
			return edgeChoice{kind: edgeDebug, edge: e, depth: depth, count: count}
		}
		if e.Max != flow.UnlimitedMax && count > e.Max {
			continue // this link is exhausted; fall through to then
		}
		return edgeChoice{kind: edgeLink, edge: e, depth: depth, count: count}
	}
	return edgeChoice{kind: edgeExhausted, edge: edge}
}

// stageCondition decides whether st runs now or completes as skipped: its
// run_if against the run's facts, plus the implicit rule for a remote
// tracker action when the ticket has no issue to write to (FLOWLANG §2).
func (rs *runState) stageCondition(st *flow.Stage) (skip bool, note string) {
	tracker := rs.view.TicketSourceID != "" && rs.view.TicketKey != ""
	subject := rs.subject != "" && rs.subjectMR() != nil
	facts := flow.CondFacts{Tracker: tracker, Subject: subject, Change: rs.view.TicketChangeType}
	if !st.RunIf.Holds(facts) {
		return true, "skipped: run_if " + st.RunIf.String() + " does not hold for this run"
	}
	if st.Type == flow.StageAction && !tracker {
		if act := actionspec.Get(st.Action); act != nil && act.Requires == actionspec.RequiresTracker {
			return true, "the ticket has no tracker issue (it was written by hand); nothing to write to"
		}
	}
	return false, ""
}

// subjectCheckout is the checkout the phase run acts on, or nil (the main
// chain of a run started on a ticket).
func (rs *runState) subjectCheckout() *event.Checkout {
	if rs.subject == "" {
		return nil
	}
	return rs.checkout(rs.subject)
}

// subjectMR is the merge request the phase run acts on, or nil.
func (rs *runState) subjectMR() *mrRecord {
	co := rs.subjectCheckout()
	if co == nil {
		return nil
	}
	return rs.mrFor(co.Name, co.Branch)
}

// step is what the run loop does next.
type step int

const (
	stepIdle          step = iota // between phases: watch the merge requests
	stepGone                      // the current phase no longer exists in the flow
	stepChainOver                 // past the chain: end the phase, or the run
	stepDebug                     // parked on the debug leaf
	stepRouteComplete             // a stage completed, its routing not yet recorded
	stepRouteFail                 // a stage failed, its edge not yet recorded
	stepSkip                      // the stage's condition does not hold
	stepRun                       // run the stage
)

// nextStep is the loop's dispatch: what to do from where the run stands,
// with the stage it concerns and, for a skip, why.
func (rs *runState) nextStep() (step, *flow.Stage, string) {
	switch {
	case rs.idle:
		return stepIdle, nil, ""
	case rs.pos < 0:
		return stepGone, nil, ""
	case rs.pos >= len(rs.fl.Stages):
		return stepChainOver, nil, ""
	}
	st := &rs.fl.Stages[rs.pos]
	switch {
	case rs.ph == phDebug:
		return stepDebug, st, ""
	case rs.ph == phAfterComplete:
		return stepRouteComplete, st, ""
	case rs.ph == phAfterFail && st.Type != flow.StageAction:
		// An action re-enters its executor instead, which reconciles.
		return stepRouteFail, st, ""
	}
	if skip, note := rs.stageCondition(st); skip && rs.ph == phStart {
		return stepSkip, st, note
	}
	return stepRun, st, ""
}
