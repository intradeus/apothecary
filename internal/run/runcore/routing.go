package runcore

import (
	"fmt"

	"apothecary/internal/flow"
	"apothecary/internal/flow/actionspec"
	"apothecary/internal/run/event"
)

// The interpreter's decisions, pure: given the run's state and a stage, where
// a success goes, which link of an edge chain a failure or rejection takes,
// whether the budget is spent, whether a stage runs, and what the loop does
// next. The engine's run loop carries them out and records them; nothing
// here does I/O or reads a clock.

// RouteSuccess moves the cursor along st's on_success, carrying verdict — what
// an approval said (flow.GivesVerdict) — to the target's edge.verdict ("" for
// nothing). end parks the cursor past the last stage, which the loop reads as
// the phase's end; done does the same and marks the run done; abort marks it
// aborted. No event records this move: the next step does (see State).
func (rs *State) RouteSuccess(st *flow.Stage, verdict string) {
	switch st.OnSuccess {
	case flow.TerminalEnd, flow.TerminalDone, flow.TerminalAbort:
		rs.Pos = len(rs.Flow.Stages)
		rs.EndsRun = st.OnSuccess == flow.TerminalDone
		rs.AbortsRun = st.OnSuccess == flow.TerminalAbort
		if rs.AbortsRun {
			rs.AbortAt, rs.AbortReason = st.ID, "aborted by flow: on_success at stage "+st.ID
		}
	default:
		rs.Pos = rs.Flow.Index(st.OnSuccess) // parse-validated: always >= 0
	}
	rs.Ph = PhStart
	rs.Feed, rs.FeedSentinel, rs.FeedAttempts = verdict, "", 0
	if verdict != "" {
		rs.FeedSentinel = flow.SourceVerdict
	}
}

// EndPhase puts the cursor past the chain: the phase in progress is over
// (its phase_completed, appended by the loop's next step, records it).
func (rs *State) EndPhase() {
	rs.Pos = len(rs.Flow.Stages)
	rs.EndsRun, rs.AbortsRun = false, false
	rs.Ph = PhStart
}

// BudgetExceeded reports whether the run's spend is past the flow's §3
// ceilings, and says which.
func BudgetExceeded(b flow.Budget, cost float64, turns int) (bool, string) {
	if cost > b.MaxCostUSD {
		return true, fmt.Sprintf("budget exceeded: cost %.2f USD > max %.2f", cost, b.MaxCostUSD)
	}
	if turns > b.MaxTotalTurns {
		return true, fmt.Sprintf("budget exceeded: %d turns > max %d", turns, b.MaxTotalTurns)
	}
	return false, ""
}

// EdgeChoiceKind is what following a stage's edge of some kind comes to.
type EdgeChoiceKind int

const (
	EdgeNone      EdgeChoiceKind = iota // the stage declares no edge of that kind
	EdgeExhausted                       // every link of the chain is spent
	EdgeEnd                             // a link to end or done
	EdgeAbort                           // a link to abort
	EdgeDebug                           // a link to the debug leaf
	EdgeLink                            // a link to a stage
)

// EdgeChoice is the link a stage's edge chain gives: which, at what depth,
// and — for a counted link — what its counter becomes.
type EdgeChoice struct {
	Kind  EdgeChoiceKind
	Edge  *flow.Edge
	Depth int
	Count int
}

// ChooseEdge follows st's edge of the given kind (on_fail, on_reject,
// on_timeout). The edge is a chain: each link carries its own cumulative
// max, and a full link falls through to its then fallback
// (retry-then-escalate). end, done and abort are taken at once; the debug
// leaf is counted but never exhausted (the human decides when to stop).
func (rs *State) ChooseEdge(st *flow.Stage, kind string) EdgeChoice {
	edge := st.Edge(kind)
	if edge == nil {
		return EdgeChoice{Kind: EdgeNone}
	}
	depth := 0
	for e := edge; e != nil; e, depth = e.Then, depth+1 {
		switch e.To {
		case flow.TerminalEnd, flow.TerminalDone:
			return EdgeChoice{Kind: EdgeEnd, Edge: e, Depth: depth}
		case flow.TerminalAbort:
			return EdgeChoice{Kind: EdgeAbort, Edge: e, Depth: depth}
		}
		count := rs.EdgeCounts[ChainKey(st.ID, kind, depth)] + 1
		if e.To == flow.ReservedDebug {
			return EdgeChoice{Kind: EdgeDebug, Edge: e, Depth: depth, Count: count}
		}
		if e.Max != flow.UnlimitedMax && count > e.Max {
			continue // this link is exhausted; fall through to then
		}
		return EdgeChoice{Kind: EdgeLink, Edge: e, Depth: depth, Count: count}
	}
	return EdgeChoice{Kind: EdgeExhausted, Edge: edge}
}

// StageCondition decides whether st runs now or completes as skipped: its
// run_if against the run's facts, plus the implicit rule for a remote
// tracker action when the ticket has no issue to write to (FLOWLANG §2).
func (rs *State) StageCondition(st *flow.Stage) (skip bool, note string) {
	tracker := rs.View.TicketSourceID != "" && rs.View.TicketKey != ""
	subject := rs.Subject != "" && rs.SubjectMR() != nil
	facts := flow.CondFacts{Tracker: tracker, Subject: subject, Change: rs.View.TicketChangeType}
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

// SubjectCheckout is the checkout the phase run acts on, or nil (the main
// chain of a run started on a ticket).
func (rs *State) SubjectCheckout() *event.Checkout {
	if rs.Subject == "" {
		return nil
	}
	return rs.Checkout(rs.Subject)
}

// SubjectMR is the merge request the phase run acts on, or nil.
func (rs *State) SubjectMR() *MRRecord {
	co := rs.SubjectCheckout()
	if co == nil {
		return nil
	}
	return rs.MRFor(co.Name, co.Branch)
}

// Step is what the run loop does next.
type Step int

const (
	StepIdle          Step = iota // between phases: watch the merge requests
	StepGone                      // the current phase no longer exists in the flow
	StepChainOver                 // past the chain: end the phase, or the run
	StepDebug                     // parked on the debug leaf
	StepRouteComplete             // a stage completed, its routing not yet recorded
	StepRouteFail                 // a stage failed, its edge not yet recorded
	StepSkip                      // the stage's condition does not hold
	StepRun                       // run the stage
)

// NextStep is the loop's dispatch: what to do from where the run stands,
// with the stage it concerns and, for a skip, why.
func (rs *State) NextStep() (Step, *flow.Stage, string) {
	switch {
	case rs.Idle:
		return StepIdle, nil, ""
	case rs.Pos < 0:
		return StepGone, nil, ""
	case rs.Pos >= len(rs.Flow.Stages):
		return StepChainOver, nil, ""
	}
	st := &rs.Flow.Stages[rs.Pos]
	switch {
	case rs.Ph == PhDebug:
		return StepDebug, st, ""
	case rs.Ph == PhAfterComplete:
		return StepRouteComplete, st, ""
	case rs.Ph == PhAfterFail && st.Type != flow.StageAction:
		// An action re-enters its executor instead, which reconciles.
		return StepRouteFail, st, ""
	}
	if skip, note := rs.StageCondition(st); skip && rs.Ph == PhStart {
		return StepSkip, st, note
	}
	return StepRun, st, ""
}
