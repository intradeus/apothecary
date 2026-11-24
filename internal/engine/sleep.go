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
	"apothecary/internal/workspace/idle"
)

// Stop-on-idle (docs/sandbox-runtimes-plan.md, phase 6). A run's sandbox
// that nothing has used for its project's idle period is stopped and kept
// (envport.EnvSleeper): its storage, secrets and egress identity stay, its
// processes go. The next use wakes it — a stage (ensureEnv), or a person
// opening a terminal, the editor, an exposed port, an SSH session or the
// Files view (UseEnv). Only runs without a live loop sleep: a loop ensures
// its workspace once and its stages expect it there. What runs inside the
// workspace is asked last (EnvKeepAwake): a terminal session running a
// program — an agent's TUI left working, a build — is a use, though no
// page holds the workspace.
//
// A sweep and a use meet through two claims, taken in opposite orders: the
// sweep claims the "sleeping" op, then looks at the run's use and its loop
// again; a use takes the tracker (or registers its loop) first, then looks
// for a "sleeping" or "waking" op and waits it out. Whichever comes second
// sees the other: either the sweep finds the run in use and lets it be, or
// the use finds the sleep in flight and wakes the sandbox after it.

// The workspace ops stop-on-idle claims (envstate's gate and badge).
const (
	opSleeping = "sleeping"
	opWaking   = "waking"
)

// Use is the tracker the surfaces that use a sandbox report to (terminal,
// SSH, exposed ports, agent turns).
func (e *Engine) Use() *idle.Tracker { return e.use }

// HoldEnv holds the run's sandbox until release (ask.Engine).
func (e *Engine) HoldEnv(runID string) (release func()) { return e.use.Busy(runID) }

// UseEnv holds the run's sandbox for a use and wakes it if it was stopped
// when idle: the hold comes first, so a sweep that has not stopped it yet
// leaves it up, and one already stopping it is waited out. release ends the
// use; on an error there is nothing to release.
func (e *Engine) UseEnv(ctx context.Context, runID, by string) (release func(), err error) {
	release = e.use.Busy(runID)
	if err := e.WakeEnv(ctx, runID, by); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// EnvSleepHook is a teardown hook (EnvTeardown) with something to keep
// across a sleep: it is told Slept instead of Forget once the sandbox is
// stopped, and the wake brings back what it kept.
type EnvSleepHook interface {
	Slept(runID string)
}

// EnvKeepAwake looks inside a workspace that is due to stop for a use the
// engine cannot see from outside: a terminal session with a program running
// in it (wsinfo.Service). It is asked only for a run already due and able
// to sleep. Busy counts as a use (Touch), so the next look is an idle period
// away; an error is not busy: a broken probe must never pin a workspace up.
type EnvKeepAwake interface {
	TerminalsBusy(ctx context.Context, v run.View, res *resolution.Resolution) (bool, error)
}

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
		// A sandbox its provisioner cannot keep (emptyDir storage, a VM
		// runtime) is passed over quietly: an instance-wide idle default
		// reaches it too, and a warning every sweep would say nothing new.
		if sleeper, err := canSleep(res); err == nil && idle.Due(c, idleFor) {
			if e.keptAwake(ctx, v, res) {
				e.use.Touch(runID)
			} else {
				switch err := e.sleepEnv(ctx, v, sleeper, c.After); {
				case err == nil:
					slept = append(slept, runID)
				case errors.Is(err, errStillInUse):
					// Used, or its loop started, since the sweep looked: not idle.
				default:
					e.log.WarnContext(ctx, "stopping an idle workspace failed", logs.RunID, runID, "err", err)
				}
			}
		}
		res.Close()
	}
	return slept
}

// keptAwake asks EnvKeepAwake whether the run's workspace is busy inside.
func (e *Engine) keptAwake(ctx context.Context, v run.View, res *resolution.Resolution) bool {
	if e.keepAwake == nil {
		return false
	}
	busy, err := e.keepAwake.TerminalsBusy(ctx, v, res)
	if err != nil {
		e.log.WarnContext(ctx, "asking an idle workspace's terminals what runs in them failed; stopping it", logs.RunID, v.RunID, "err", err)
		return false
	}
	return busy
}

func (e *Engine) loopLive(runID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, live := e.handles[runID]
	return live
}

// errCannotSleep: the provisioner keeps nothing of a stopped sandbox.
var errCannotSleep = errors.New("this provisioner's workspaces cannot sleep")

// canSleep is the run's sleeper, or why its sandbox cannot be stopped and
// kept: a provisioner that keeps nothing, or none under the run's runtime.
func canSleep(res *resolution.Resolution) (envport.EnvSleeper, error) {
	sleeper, ok := res.Envp.(envport.EnvSleeper)
	if !ok {
		return nil, errCannotSleep
	}
	rt, err := res.SandboxRuntime()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCannotSleep, err)
	}
	if err := sleeper.CanSleep(rt); err != nil {
		return nil, fmt.Errorf("%w: %v", errCannotSleep, err)
	}
	return sleeper, nil
}

// errStillInUse: between the sweep's look and its claim, the run was used,
// its loop started, or another workspace op claimed it.
var errStillInUse = errors.New("the workspace is in use")

// sleepEnv stops the run's sandbox through sleeper (canSleep's).
func (e *Engine) sleepEnv(ctx context.Context, v run.View, sleeper envport.EnvSleeper, after time.Duration) (err error) {
	rel, err := e.envState.Begin(v.RunID, opSleeping)
	if err != nil {
		return errStillInUse // another op claimed it since the sweep looked
	}
	defer func() { rel(err) }()
	// The claim is up: from here a use waits for the sleep and wakes the
	// sandbox after it. One that came before the claim is seen now.
	if e.loopLive(v.RunID) || e.use.IdleFor(v.RunID) < after {
		rel(nil)
		return errStillInUse
	}
	if err := sleeper.Sleep(ctx, envport.EnvID(v.EnvID)); err != nil {
		return err
	}
	// The exposures, the editor and the browser died with the sandbox's
	// processes: drop them, or keep them for the wake (EnvSleepHook).
	for _, h := range e.teardown {
		if s, ok := h.(EnvSleepHook); ok {
			s.Slept(v.RunID)
		} else {
			h.Forget(v.RunID)
		}
	}
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
	WokeByFiles    = "files"  // the run page's Files, Changes and Diff
	WokeByPerson   = "person" // the run page's Wake
)

// WakeEnv starts a run's sleeping sandbox again and opens what died with it
// (run.env's token, the run's attachments, its exposures and editor). A
// sleep or a wake in flight is waited out (bounded by ctx), so the caller
// meets the sandbox up. A sandbox that is not asleep, or a run whose loop
// is live (its next stage wakes it), is left as it is. Another workspace op
// in flight (a rebuild, a destroy) is envstate.ErrInFlight. A caller that
// goes on to use the sandbox holds it first (UseEnv).
func (e *Engine) WakeEnv(ctx context.Context, runID, by string) error {
	// The op before the status: a sleep releases its op only after it
	// recorded the sandbox asleep.
	op := e.envState.Op(runID)
	if st, ok := e.envState.Status(runID); ok && st != envport.EnvAsleep && op != opSleeping && op != opWaking {
		return nil
	}
	for {
		if err := e.envState.WaitOps(ctx, runID, opSleeping, opWaking); err != nil {
			return err
		}
		if e.loopLive(runID) {
			return nil
		}
		err := e.wakeOnce(ctx, runID, by)
		if !errors.Is(err, errWakeRace) {
			return err
		}
	}
}

// errWakeRace: a sleep or another wake claimed the run between WakeEnv's
// wait and its own claim; it waits again.
var errWakeRace = errors.New("another sleep or wake of the workspace is in flight")

func (e *Engine) wakeOnce(ctx context.Context, runID, by string) (err error) {
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
	// Asked before the claim, so a sandbox that is up claims nothing (a
	// rebuild starting now is not refused over a look), and again under it:
	// another wake may have finished in between.
	if st, err := res.Envp.Status(ctx, id); err != nil || st != envport.EnvAsleep {
		return err
	}
	rel, err := e.envState.Begin(runID, opWaking)
	if err != nil {
		if e.sleepOrWake(runID) {
			return errWakeRace
		}
		return err
	}
	defer func() { rel(err) }()
	if st, err := res.Envp.Status(ctx, id); err != nil || st != envport.EnvAsleep {
		return err
	}
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

// sleepOrWake says whether the op that refused a wake's claim is a sleep or
// another wake, which the wake waits out ("" too: it ended in between), as
// opposed to a rebuild or a destroy, which it reports.
func (e *Engine) sleepOrWake(runID string) bool {
	op := e.envState.Op(runID)
	return op == "" || op == opSleeping || op == opWaking
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

// wakeInLoop is ensureEnv's wake of a sandbox it found asleep: the "waking"
// op, after any other sleep or wake of it (a person's, started before the
// loop), and only if it is still asleep then.
func (x *exec) wakeInLoop() (err error) {
	var rel func(error)
	for {
		rel, err = x.e.envState.Begin(x.rs.RunID, opWaking)
		if err == nil {
			break
		}
		if !x.e.sleepOrWake(x.rs.RunID) {
			return err
		}
		if werr := x.e.envState.WaitOps(x.ctx, x.rs.RunID, opSleeping, opWaking); werr != nil {
			return werr
		}
	}
	defer func() { rel(err) }()
	if st, err := x.res.Envp.Status(x.ctx, x.rs.EnvID); err != nil || st != envport.EnvAsleep {
		return err
	}
	spec, err := runEnvSpec(x.rs.View, x.res)
	if err != nil {
		return err
	}
	return x.e.wake(x.ctx, x.rs.View, x.res, spec, WokeByStage)
}
