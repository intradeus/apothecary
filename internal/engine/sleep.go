package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"apothecary/internal/platform/logs"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
	"apothecary/internal/workspace/envstate"
	"apothecary/internal/workspace/idle"
)

// Stop-on-idle (docs/sandbox-runtimes-plan.md, phase 6). A run's sandbox
// that nothing has used for its project's idle period is stopped and kept
// (envport.EnvSleeper): its storage, secrets and egress identity stay, its
// processes go. The next use wakes it — a stage (ensureEnv), or a person
// opening a terminal, the editor, an exposed port or an SSH session
// (WakeEnv). Only runs without a live loop sleep: a loop ensures its
// workspace once and its stages expect it there.

// Use is the tracker the surfaces that use a sandbox report to (terminal,
// SSH, exposed ports, agent turns).
func (e *Engine) Use() *idle.Tracker { return e.use }

// HoldEnv holds the run's sandbox until release (ask.Engine).
func (e *Engine) HoldEnv(runID string) (release func()) { return e.use.Busy(runID) }

// idleFloor is the shortest idle period: below it a run is not even
// resolved to read its own period.
const idleFloor = time.Minute

// RunIdleSweep stops idle sandboxes every period until ctx ends.
func (e *Engine) RunIdleSweep(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.SleepIdle(ctx)
		}
	}
}

// SleepIdle is one sweep: every running sandbox whose run has no live loop,
// no workspace op in flight and no use for its project's idle period is
// stopped. It returns the runs it put to sleep.
func (e *Engine) SleepIdle(ctx context.Context) []string {
	var slept []string
	for runID, st := range e.envState.Statuses() {
		if st != envport.EnvRunning || e.loopLive(runID) || e.envState.InFlight(runID) {
			continue
		}
		idleFor := e.use.IdleFor(runID)
		if idleFor < idleFloor {
			continue
		}
		v, err := e.runs.View(ctx, runID)
		if err != nil || v.EnvID == "" || v.ProjectID == "" {
			continue
		}
		res, err := e.resolveForRun(ctx, v)
		if err != nil {
			continue
		}
		c := idle.Candidate{RunID: runID, Status: st, After: res.WorkspaceIdleAfter}
		if idle.Due(c, idleFor) {
			if err := e.sleepEnv(ctx, v, res, c.After); err != nil {
				e.log.WarnContext(ctx, "stopping an idle workspace failed", logs.RunID, runID, "err", err)
			} else {
				slept = append(slept, runID)
			}
		}
		res.Close()
	}
	return slept
}

func (e *Engine) loopLive(runID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, live := e.handles[runID]
	return live
}

// errCannotSleep: the provisioner keeps nothing of a stopped sandbox.
var errCannotSleep = errors.New("this provisioner's workspaces cannot sleep")

func (e *Engine) sleepEnv(ctx context.Context, v run.View, res *resolution.Resolution, after time.Duration) (err error) {
	sleeper, ok := res.Envp.(envport.EnvSleeper)
	if !ok {
		return errCannotSleep
	}
	if err := sleeper.CanSleep(); err != nil {
		return fmt.Errorf("%w: %v", errCannotSleep, err)
	}
	rel, err := e.envState.Begin(v.RunID, "sleeping")
	if err != nil {
		return err
	}
	defer func() { rel(err) }()
	// The exposures, the editor and the browser die with the sandbox's
	// processes: drop them now, a wake opens them again.
	for _, h := range e.teardown {
		h.Forget(v.RunID)
	}
	if err := sleeper.Sleep(ctx, envport.EnvID(v.EnvID)); err != nil {
		return err
	}
	e.use.Forget(v.RunID)
	e.envState.SetStatus(v.RunID, envport.EnvAsleep)
	return e.AppendEvents(ctx, v.RunID, &event.EnvSlept{EnvID: v.EnvID, IdleMinutes: int(after / time.Minute)})
}

// Wake causes: what woke a sleeping sandbox (EnvWoke.By).
const (
	WokeByStage    = "stage"
	WokeByTerminal = "terminal"
	WokeByEditor   = "editor"
	WokeByPort     = "port"
	WokeBySSH      = "ssh"
	WokeByPerson   = "person" // the run page's Wake
)

// WakeEnv starts a run's sleeping sandbox again and opens what died with it
// (run.env's token, the run's attachments, its exposures and editor). A
// sandbox that is not asleep, or a run whose loop is live (its next stage
// wakes it), is left as it is. A wake already in flight is ErrInFlight: the
// caller shows "waking" and asks again.
func (e *Engine) WakeEnv(ctx context.Context, runID, by string) (err error) {
	if e.loopLive(runID) {
		return nil
	}
	v, err := e.runs.View(ctx, runID)
	if err != nil {
		return err
	}
	if v.EnvID == "" || v.ProjectID == "" {
		return nil
	}
	res, err := e.resolveForRun(ctx, v)
	if err != nil {
		return err
	}
	defer res.Close()
	id := envport.EnvID(v.EnvID)
	if st, err := res.Envp.Status(ctx, id); err != nil || st != envport.EnvAsleep {
		return err
	}
	rel, err := e.envState.Begin(runID, "waking")
	if err != nil {
		return err
	}
	defer func() { rel(err) }()
	spec, err := runEnvSpec(v, res)
	if err != nil {
		return err
	}
	if err := e.wake(ctx, v, res, spec, by); err != nil {
		return err
	}
	fl, err := e.RunFlow(ctx, v)
	if err != nil {
		return err
	}
	// What a loop's recovery re-opens (provision.go), through a minimal exec
	// as a rebuild does; none of these append events.
	x := &exec{e: e, rs: &runcore.State{RunID: runID, View: v, Flow: fl, EnvID: id}, res: res, ctx: ctx, bg: ctx}
	if err := x.writeRunEnv(id); err != nil {
		e.log.WarnContext(ctx, "refreshing run.env after a wake failed", logs.RunID, runID, "err", err)
	}
	x.deliverRunAttachments()
	e.runPorts.ReopenAfterRebuild(ctx, runID, res.Envp, id, res.EditorSettings)
	return nil
}

// wake provisions the sandbox again from its spec and records it; the
// caller holds the "waking" op.
func (e *Engine) wake(ctx context.Context, v run.View, res *resolution.Resolution, spec envport.EnvSpec, by string) error {
	if _, err := res.Envp.Provision(ctx, spec); err != nil {
		_ = e.AppendEvents(ctx, v.RunID, &event.EnvWoke{EnvID: v.EnvID, By: by, Error: err.Error()})
		return fmt.Errorf("wake the workspace: %w", err)
	}
	e.use.Touch(v.RunID)
	if st, err := res.Envp.Status(ctx, envport.EnvID(v.EnvID)); err == nil {
		e.envState.SetStatus(v.RunID, st)
	}
	return e.AppendEvents(ctx, v.RunID, &event.EnvWoke{EnvID: v.EnvID, By: by})
}

// wakeInLoop is ensureEnv's wake: the "waking" op, waiting out a sleep still
// in flight (a sweep that picked the run just before its loop started).
func (x *exec) wakeInLoop() error {
	var rel func(error)
	for {
		var err error
		rel, err = x.e.envState.Begin(x.rs.RunID, "waking")
		if err == nil {
			break
		}
		if !errors.Is(err, envstate.ErrInFlight) {
			return err
		}
		select {
		case <-time.After(time.Second):
		case <-x.ctx.Done():
			return context.Cause(x.ctx)
		}
	}
	spec, err := runEnvSpec(x.rs.View, x.res)
	if err == nil {
		err = x.e.wake(x.ctx, x.rs.View, x.res, spec, WokeByStage)
	}
	rel(err)
	return err
}
