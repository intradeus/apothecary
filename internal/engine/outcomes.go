package engine

// Outcomes are terminal (FLOWLANG §1), so they override what a run is doing.
// The idle tick fires them for a run at the hub or parked; this watches the
// runs it cannot reach: one in a stage, and one with no loop at all, stopped
// on a question, the debug leaf or a pause. Only a take-over is left alone:
// a person has the agent. Either way the outcome's phase_started moves the
// cursor and a fresh loop picks the run up there, through recovery.

import (
	"context"
	"fmt"
	"slices"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
)

// outcomeFired is the cause that stops a running stage for an outcome.
type outcomeFired struct{ phase, subject, key string }

func (f outcomeFired) Error() string { return fmt.Sprintf("outcome %q takes the run", f.phase) }

// RunOutcomes watches for outcomes at the signal poll cadence until ctx ends.
func (e *Engine) RunOutcomes(ctx context.Context) {
	t := time.NewTicker(e.cfg.MRPollInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := e.OutcomesOnce(ctx); err != nil {
				e.log.Warn("outcomes: pass failed", "err", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// OutcomesOnce is one pass over the runs the idle tick does not reach. A run
// it cannot read is left for the next pass.
func (e *Engine) OutcomesOnce(ctx context.Context) error {
	views, err := e.runs.All(ctx)
	if err != nil {
		return err
	}
	for _, v := range views {
		if v.State.Terminal() || len(v.MRs) == 0 {
			continue
		}
		if err := e.fireOutcome(ctx, v); err != nil {
			e.log.Warn("outcomes: run skipped", "run", v.RunID, "err", err)
		}
	}
	return nil
}

// fireOutcome moves the run's cursor to the outcome that holds, if one does.
func (e *Engine) fireOutcome(ctx context.Context, v run.View) error {
	e.mu.Lock()
	h := e.handles[v.RunID]
	queued := slices.Contains(e.queue, v.RunID)
	e.mu.Unlock()
	if queued || h != nil && h.idle.Load() {
		return nil
	}
	evs, err := e.store.Events(ctx, v.RunID, 0)
	if err != nil {
		return err
	}
	fl, err := e.RunFlow(ctx, v)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(fl.Phases, func(p flow.Phase) bool { return p.IsOutcome() }) {
		return nil
	}
	view := run.Reduce(evs)
	if h == nil && !view.Dormant {
		return nil // mid-admission: a loop is owed, and it will look
	}
	rs := runcore.Replay(evs, fl)
	obs, err := e.observeFate(ctx, v, rs)
	if err != nil {
		return err
	}
	a := rs.PlanOutcome(obs, e.clock(), h == nil)
	if a == nil {
		return nil
	}
	if h != nil {
		e.cancelLoop(v.RunID, outcomeFired{phase: a.Phase.ID, subject: a.Subject, key: a.Key}, nil)
		return nil
	}
	return e.wakeForOutcome(ctx, v.RunID, a)
}

// observeFate reads what an outcome can name on the run's merge requests.
func (e *Engine) observeFate(ctx context.Context, v run.View, rs *runcore.State) ([]runcore.MRObservation, error) {
	res, err := resolution.ForRun(ctx, e.resolver, v)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	reads := readsOf(rs.Flow)
	obs := make([]runcore.MRObservation, 0, len(rs.MRs))
	for i := range rs.MRs {
		mr := &rs.MRs[i]
		repoHost := res.RepoHostFor(mr.Repo)
		if repoHost == nil {
			continue
		}
		status, err := repoHost.MRStatus(ctx, mr.Ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", mr.Ref.URL, err)
		}
		o := runcore.MRObservation{MR: mr, Status: status}
		if reads.approval && status.State == repoport.MROpen {
			ap, err := repoHost.MRApproval(ctx, mr.Ref)
			if err != nil {
				return nil, fmt.Errorf("%s approval: %w", mr.Ref.URL, err)
			}
			o.Approval = &ap
		}
		if reads.checks && status.State == repoport.MROpen {
			checks, err := repoHost.MRChecks(ctx, mr.Ref, status.HeadSHA)
			if err != nil {
				return nil, fmt.Errorf("%s checks: %w", mr.Ref.URL, err)
			}
			o.Checks = &checks
		}
		obs = append(obs, o)
	}
	return obs, nil
}

// wakeForOutcome starts the outcome on a run with no loop. Like answerDormant
// it writes from outside a run's goroutine, safe for the same reason: a
// dormant run has no writer, and wakeMu keeps it that way.
func (e *Engine) wakeForOutcome(ctx context.Context, runID string, a *runcore.IdleAction) error {
	e.wakeMu.Lock()
	defer e.wakeMu.Unlock()
	e.mu.Lock()
	_, active := e.handles[runID]
	e.mu.Unlock()
	if active {
		return nil
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return err
	}
	if v := run.Reduce(evs); !v.Dormant || v.State.Terminal() || v.Hold != nil {
		return nil
	}
	if err := e.AppendEvents(ctx, runID,
		&event.PhaseStarted{Phase: a.Phase.ID, Subject: a.Subject, Key: a.Key, By: "signal"},
		&event.RunUnpaused{Reason: event.UnpauseOutcome}); err != nil {
		return err
	}
	lim := e.limits(ctx)
	e.mu.Lock()
	e.queue = append(e.queue, runID)
	e.scheduleLocked(lim)
	e.mu.Unlock()
	return nil
}

// takeCursor is a running chain stopped for an outcome: the loop records the
// outcome's start and leaves, and the next one picks the run up there. The
// idle tick may have fired it while the cancel was on its way.
func (x *exec) takeCursor(f outcomeFired) {
	x.endHolds()
	if !x.rs.TriggerSeen[runcore.TriggerKey(f.phase, f.subject, f.key)] {
		if err := x.beginPhase(f.phase, f.subject, f.key, "signal", ""); err != nil {
			x.e.log.Error("outcome not started; run left for recovery", "run", x.rs.RunID, "err", err)
			return
		}
	}
	x.h.relaunch = true
}
