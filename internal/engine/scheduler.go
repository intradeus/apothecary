package engine

// The scheduler (FLOWLANG §1). The idle loop observes the run's merge requests
// once a tick; runcore.State.PlanIdle says what that observation means — the
// wait point that settled, the confirmation answered, the phase that opens,
// the run's end — and this file carries out the parked chain's part of it.
// The run has one cursor: one chain at a time, parked or moving, holds it.

import (
	"sync"

	"apothecary/internal/flow"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
)

// holdsRegistry is the engine's per-run memory of the last tick's holds.
type holdsRegistry struct {
	mu    sync.Mutex
	byRun map[string]map[string][]runcore.TriggerHold // run → phase → holds
}

func (r *holdsRegistry) set(runID string, holds map[string][]runcore.TriggerHold) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byRun == nil {
		r.byRun = map[string]map[string][]runcore.TriggerHold{}
	}
	r.byRun[runID] = holds
}

func (r *holdsRegistry) of(runID, phase string) []runcore.TriggerHold {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byRun[runID][phase]
}

func (r *holdsRegistry) clear(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byRun, runID)
}

// parkAt stops the cursor at a wait point mid-chain — a wait stage or a gate
// (pingID its decision) — and leaves the run idle: nothing else opens, and
// the idle loop resumes this chain when its wait point settles
// (runcore.State.PlanResume).
func (x *exec) parkAt(st *flow.Stage, pingID string) error {
	rs := x.rs
	return x.append(&event.PhaseParked{Phase: rs.Phase, Subject: rs.Subject, StageID: st.ID, PingID: pingID})
}

// resumeParked carries out a settled wait point: the phase resumes and its
// stage routes.
func (x *exec) resumeParked(p runcore.ResumePlan) error {
	rs, st, at := x.rs, p.Stage, *x.rs.Parked
	if err := x.append(&event.PhaseResumed{Phase: rs.Phase, Subject: rs.Subject, StageID: st.ID}); err != nil {
		return err
	}
	switch p.Kind {
	case runcore.ResumeWaitHolds:
		return x.completeWait(st, at.Since)
	case runcore.ResumeWaitImpossible:
		if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInputInvalid, Error: p.Why}); err != nil {
			return err
		}
		return x.takeEdge(st, flow.EdgeFail, p.Why)
	case runcore.ResumeWaitTimedOut:
		if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonTimeout, Error: p.Why}); err != nil {
			return err
		}
		return x.takeEdge(st, runcore.FailEdgeKind(st, event.ReasonTimeout), "")
	}
	return x.routeGate(st, p.Answer, at.Since)
}
