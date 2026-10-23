package engine

// The scheduler (FLOWLANG §1). Every wait point — a phase's opening `on:`, a
// wait stage, a gate — is the same thing: a place a chain stops for something
// outside the run, with a policy for what happens when it comes. The idle
// loop is where all of them are decided. Each tick it observes the run's
// merge requests once, resumes the first parked phase run whose wait point
// settled, acts on answered confirmations, opens the first firing phase whose
// signal holds under its start policy (auto: now; confirm: after asking;
// manual: never — the hold is surfaced for the run page), and finally
// decides whether the run is over: nothing parked, nothing armed. One phase
// run holds the workspace at a time; the rest is waiting.

import (
	"fmt"
	"sync"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/signalspec"
)

// TriggerHold is one observation that a phase's signal holds for a subject:
// what the run page shows for a phase with start: manual, and the record
// behind every automatic start. Memory only — external state, re-observed
// on the next tick after a restart.
type TriggerHold struct {
	Subject string `json:"subject"`
	Key     string `json:"key"`
	Why     string `json:"why"`
	// Seen: a run already started (or a person declined) for this
	// occurrence, or the phase's runs on this subject are used up.
	Seen bool      `json:"seen"`
	At   time.Time `json:"observed_at"`
}

// holdsRegistry is the engine's per-run memory of the last tick's holds.
type holdsRegistry struct {
	mu    sync.Mutex
	byRun map[string]map[string][]TriggerHold // run → phase → holds
}

func (r *holdsRegistry) set(runID string, holds map[string][]TriggerHold) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byRun == nil {
		r.byRun = map[string]map[string][]TriggerHold{}
	}
	r.byRun[runID] = holds
}

func (r *holdsRegistry) of(runID, phase string) []TriggerHold {
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
	if err := x.append(&event.PhaseParked{Phase: rs.phase, Subject: rs.subject, StageID: st.ID, PingID: pingID}); err != nil {
		return err
	}
	rs.park(st.ID, pingID, x.e.clock())
	return nil
}

// tryResume checks one parked phase run's wait point against the latest
// observation and, when it settled, resumes the phase and routes the stage:
// a wait whose signal holds completes, one whose signal can no longer hold
// or that timed out fails (on_fail / on_timeout), a gate routes its answer —
// the human's, or the one a signal supplied (auto_approve / auto_reject).
// Reports whether the phase resumed.
func (x *exec) tryResume(pr *phaseRun, obs []mrObservation) (bool, error) {
	rs := x.rs
	if pr.pos < 0 || pr.pos >= len(rs.fl.Stages) {
		return false, nil
	}
	st := &rs.fl.Stages[pr.pos]
	resume := func() error {
		if err := x.append(&event.PhaseResumed{Phase: pr.phase, Subject: pr.subject, StageID: st.ID}); err != nil {
			return err
		}
		rs.unpark(pr.phase)
		return nil
	}
	switch st.Type {
	case flow.StageWait:
		holds, impossible, why := x.waitState(st.WaitFor, obs)
		switch {
		case holds:
			if err := resume(); err != nil {
				return true, err
			}
			return true, x.completeWait(st, pr.since)
		case impossible:
			if err := resume(); err != nil {
				return true, err
			}
			msg := fmt.Sprintf("%s can no longer happen: %s", st.WaitFor, why)
			if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInputInvalid, Error: msg}); err != nil {
				return true, err
			}
			return true, x.takeEdge(st, flow.EdgeFail, msg)
		case st.Timeout > 0 && !x.e.clock().Before(pr.since.Add(st.Timeout)):
			if err := resume(); err != nil {
				return true, err
			}
			if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonTimeout,
				Error: fmt.Sprintf("%s did not hold within %s", st.WaitFor, st.Timeout)}); err != nil {
				return true, err
			}
			return true, x.takeEdge(st, failEdgeKind(st, event.ReasonTimeout), "")
		}
	case flow.StageHumanGate:
		ans := rs.answeredPings[pr.ping]
		if ans == nil {
			// A signal answers the gate on the human's behalf: "they merged
			// it instead of clicking approve", "they closed it, which is a
			// no". Whichever lands first wins.
			for _, a := range []struct{ slug, answer string }{{st.AutoApprove, "approve"}, {st.AutoReject, "reject"}} {
				if a.slug == "" {
					continue
				}
				if holds, _, _ := x.waitState(a.slug, obs); holds {
					rec, err := x.answerFromEngine(pr.ping, PingAnswer{Answer: a.answer, Feedback: "auto: " + a.slug})
					if err != nil {
						return true, err
					}
					ans = rec
					break
				}
			}
		}
		if ans == nil {
			return false, nil
		}
		if err := resume(); err != nil {
			return true, err
		}
		return true, x.routeGate(st, PingAnswer{Answer: ans.Answer, Feedback: ans.Feedback + attachedFilesNote(x.pingDelivered(pr.ping))}, pr.since)
	}
	return false, nil
}

// waitState evaluates a waitable signal against the latest observation: it
// holds, it can no longer hold (with why), or neither yet. A run with several
// merge requests is merged when ALL are, and closed as soon as ANY is closed
// unmerged — someone decided against part of it.
func (x *exec) waitState(slug string, obs []mrObservation) (holds, impossible bool, why string) {
	rs := x.rs
	if sig := signalspec.Get(slug); sig != nil && sig.Needs == signalspec.NeedsMR && len(rs.mrs) == 0 {
		return false, true, "this run has opened no merge request"
	}
	if len(obs) == 0 {
		return false, false, ""
	}
	allMerged, closed := true, ""
	for _, o := range obs {
		switch o.status.State {
		case ports.MRMerged:
		case ports.MRClosed:
			allMerged = false
			if closed == "" {
				closed = o.mr.Ref.URL
			}
		default:
			allMerged = false
		}
	}
	switch slug {
	case signalspec.MRMerged:
		if allMerged {
			return true, false, ""
		}
		if closed != "" {
			return false, true, "merge request closed unmerged: " + closed
		}
	case signalspec.MRClosed:
		if closed != "" {
			return true, false, ""
		}
		if allMerged {
			return false, true, "every merge request merged"
		}
	}
	return false, false, ""
}

// answerFromEngine records an answer the engine supplies for a ping (a
// signal answering a gate) exactly as a person's would: claimed on the live
// ping, appended, remembered. An already answered ping returns its record.
func (x *exec) answerFromEngine(pingID string, ans PingAnswer) (*event.PingAnswered, error) {
	x.e.mu.Lock()
	p := x.e.pings[pingID]
	if p != nil && p.answered {
		x.e.mu.Unlock()
		return x.rs.answeredPings[pingID], nil
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
	x.rs.answeredPings[pingID] = rec
	if p != nil {
		close(p.done)
	}
	return rec, nil
}

// pingDelivered lists the files a human attached to a ping's answer, as
// delivered into the workspace (empty when none, or the ping is not live).
func (x *exec) pingDelivered(pingID string) []string {
	x.e.mu.Lock()
	defer x.e.mu.Unlock()
	if p := x.e.pings[pingID]; p != nil {
		return p.deliveredPaths
	}
	return nil
}

// anyArmed reports whether some firing phase may still open on its own: its
// policy is automatic (auto or confirm), it is not parked, and it has runs
// left on an open merge request — or a confirmation for it is still open.
func (x *exec) anyArmed(obs []mrObservation) bool {
	rs := x.rs
	for i := range rs.fl.Phases {
		ph := &rs.fl.Phases[i]
		if !ph.Fires() || ph.On.Start == flow.StartManual || rs.parked[ph.ID] != nil {
			continue
		}
		for _, cp := range rs.confirms {
			if cp.Phase == ph.ID && rs.answeredPings[cp.PingID] == nil {
				return true
			}
		}
		for _, o := range obs {
			if o.status.State != ports.MROpen {
				continue
			}
			if ph.On.Max == flow.UnlimitedMax || rs.phaseRuns[phaseRunKey(ph.ID, o.mr.Repo)] < ph.On.Max {
				return true
			}
		}
	}
	return false
}

// endCheck is the one rule for a run's end (FLOWLANG §1): an idle run with
// no phase run parked and no phase armed has nothing left to do, and
// completes. Whatever happened to its merge requests is the flow's business
// — a wait on mr_merged routes a close through on_fail; a flow that never
// waits completes as soon as nothing reacts.
func (x *exec) endCheck(obs []mrObservation) error {
	if x.rs.anyParked() || x.anyArmed(obs) {
		return nil
	}
	return x.complete()
}
