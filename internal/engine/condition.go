package engine

// Stage conditions (FLOWLANG §2 run_if). A stage whose condition does not
// hold completes as skipped — visibly, the way a tracker action on a ticket
// written by hand always did — and the chain follows on_success. The facts
// a condition reads are the run's: whether its ticket has a tracker issue,
// whether this phase run acts on one merge request, and the ticket's change
// type (runcore.State.StageCondition). Parameters folded at parse time, so the
// engine never sees them.

import (
	"apothecary/internal/event"
	"apothecary/internal/flow"
)

// skipStage records a stage that did not run: started and completed as
// skipped, so the timeline shows it, then on to on_success.
func (x *exec) skipStage(st *flow.Stage, note string) error {
	rs := x.rs
	iter := rs.Iters[st.ID] + 1
	if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
		return err
	}
	if err := x.append(&event.StageCompleted{StageID: st.ID, Skipped: true, Note: note}); err != nil {
		return err
	}
	x.e.log.Info("stage skipped", "run", rs.RunID, "stage", st.ID, "note", note)
	x.rs.RouteSuccess(st, "")
	return nil
}
