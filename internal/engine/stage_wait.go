package engine

import (
	"fmt"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/runcore"
	"apothecary/internal/signalspec"
)

// stageWait is a wait point mid-chain (FLOWLANG §2): the phase parks until
// the signal holds, and the idle loop resumes it (runcore.State.PlanResume) — at once when
// the signal already holds, since the loop evaluates before its first tick.
// A signal that can never hold for this run fails the stage now instead of
// waiting forever; a timeout is counted from the park.
func (x *exec) stageWait(st *flow.Stage) error {
	rs := x.rs
	if rs.Ph != runcore.PhIn {
		iter := rs.Iters[st.ID] + 1
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
	}
	if sig := signalspec.Get(st.WaitFor); sig != nil && sig.Needs == signalspec.NeedsMR && len(rs.MRs) == 0 {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInputMissing,
			Error: fmt.Sprintf("cannot wait for %s: %s", st.WaitFor, waitPrereq(st.WaitFor))}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, "")
	}
	return x.parkAt(st, "")
}

func (x *exec) completeWait(st *flow.Stage, started time.Time) error {
	if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
		return err
	}
	x.rs.RouteSuccess(st, "")
	return nil
}

// waitPrereq explains what a run must have done before a signal can fire.
func waitPrereq(slug string) string {
	if sig := signalspec.Get(slug); sig != nil && sig.Needs == signalspec.NeedsMR {
		return "this run has opened no merge request (an action: create_mr stage must run first)"
	}
	return "its prerequisite has not happened in this run"
}
