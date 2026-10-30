package engine

// The scheduler (FLOWLANG §1). The idle loop observes the run's merge requests
// once a tick; runcore.State.PlanIdle says what that observation means — the
// wait point that settled, the confirmation answered, the phase that opens,
// the run's end — and this file carries out the parked phases' part of it.
// One phase run holds the workspace at a time; the rest is waiting.

import (
	"sync"

	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/runcore"
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

// parkAt stops the active phase run at a wait point mid-chain — a wait stage
// or a gate (pingID its decision) — and leaves the run idle: the workspace is
// free for other phases, and the idle loop resumes this one when its wait
// point settles (tryResume).
func (x *exec) parkAt(st *flow.Stage, pingID string) error {
	rs := x.rs
	return x.append(&event.PhaseParked{Phase: rs.Phase, Subject: rs.Subject, StageID: st.ID, PingID: pingID})
}

// resumeParked carries out a settled wait point: the phase resumes and its
// stage routes. A gate a signal answers takes the answer first; a person's
// answer that got there before it (claimed, not yet on the log) settles it
// next tick.
func (x *exec) resumeParked(p runcore.ResumePlan) error {
	st, pr := p.Stage, p.Run
	ans := p.Answer
	if p.Kind == runcore.ResumeGateAuto {
		rec, err := x.answerFromEngine(pr.Ping, PingAnswer{Answer: p.AutoAnswer, Feedback: p.AutoFeedback})
		if err != nil || rec == nil {
			return err
		}
		ans = rec
	}
	if err := x.append(&event.PhaseResumed{Phase: pr.Phase, Subject: pr.Subject, StageID: st.ID}); err != nil {
		return err
	}
	switch p.Kind {
	case runcore.ResumeWaitHolds:
		return x.completeWait(st, pr.Since)
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
	return x.routeGate(st, ans, pr.Since)
}

// answerFromEngine records an answer the engine supplies for a ping (a
// signal answering a gate) exactly as a person's would: claimed on the live
// ping, appended, remembered. An already answered ping returns its record.
func (x *exec) answerFromEngine(pingID string, ans PingAnswer) (*event.PingAnswered, error) {
	x.e.mu.Lock()
	p := x.e.pings[pingID]
	if p != nil && p.answered {
		x.e.mu.Unlock()
		return x.rs.AnsweredPings[pingID], nil
	}
	if p != nil {
		p.answered = true
		p.answer = &ans
	}
	x.e.mu.Unlock()
	rec := &event.PingAnswered{PingID: pingID, Answer: ans.Answer, Feedback: ans.Feedback}
	if err := x.append(rec); err != nil {
		if p != nil {
			x.e.mu.Lock()
			p.answered, p.answer = false, nil
			x.e.mu.Unlock()
		}
		return nil, err
	}
	if p != nil {
		close(p.done)
	}
	return rec, nil
}
