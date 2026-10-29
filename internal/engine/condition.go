package engine

// Stage conditions (FLOWLANG §2 when). A stage whose condition does not hold
// completes as skipped — visibly, the way a tracker action on a ticket
// written by hand always did — and the chain follows on_success. The facts
// a condition reads are the run's: whether its ticket has a tracker issue,
// whether this phase run acts on one merge request, and the ticket's change
// type. Parameters folded at parse time, so the engine never sees them.

import (
	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
)

// stageCondition decides whether st runs now or completes as skipped: its
// `when:` against the run's facts, plus the implicit rule for a remote
// tracker action when the ticket has no issue to write to.
func (x *exec) stageCondition(st *flow.Stage) (skip bool, note string) {
	rs := x.rs
	tracker := rs.view.TicketSourceID != "" && rs.view.TicketKey != ""
	subject := rs.subject != "" && x.subjectMR() != nil
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

// skipStage records a stage that did not run: started and completed as
// skipped, so the timeline shows it, then on to on_success.
func (x *exec) skipStage(st *flow.Stage, note string) error {
	rs := x.rs
	iter := rs.iters[st.ID] + 1
	rs.iters[st.ID] = iter
	if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
		return err
	}
	if err := x.append(&event.StageCompleted{StageID: st.ID, Skipped: true, Note: note}); err != nil {
		return err
	}
	rs.elided[st.ID] = true
	x.e.log.Info("stage skipped", "run", rs.runID, "stage", st.ID, "note", note)
	x.advance(st)
	return nil
}
