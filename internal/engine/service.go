package engine

// Project services (FLOWLANG §3 service): the app a run builds, its
// database — declared in the project, started by a flow's service stage or
// by a person on the run page, and supervised in the workspace by the shim
// (`apothecary-shim service`, workspace/shim/service.go), which outlives the
// exec that started it and the engine. The engine starts and stops them
// through Exec — every provisioner has it — and reads how they stand through
// the sidecar where there is one (no exec per look), else through Exec too.
//
// What the log records is history: started, ready, stopped, failed — and who
// did it. Health flipping while a service runs is the supervisor's live
// state, read on demand, never an event.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/resolution"
	"apothecary/internal/services"
	"apothecary/internal/shell"
	"apothecary/internal/sidecar"
)

// usesServiceStages: only a flow with service stages has services of its
// own to stop.
func usesServiceStages(fl *flow.Flow) bool {
	for i := range fl.Stages {
		if fl.Stages[i].Type == flow.StageService {
			return true
		}
	}
	return false
}

// --- the stage ---------------------------------------------------------------

// stageService starts, restarts or stops a project service (FLOWLANG §3
// service). A start succeeds once the service is healthy — at once when it
// already is — and fails when its start exits non-zero or it is not healthy
// within the stage's timeout (else the service's own), the failure carrying
// the service's log so the edge can feed an agent that fixes it. A service a
// stage started belongs to its chain: when the chain ends, it is stopped.
func (x *exec) stageService(st *flow.Stage) error {
	rs := x.rs
	fresh := rs.ph != phIn
	if fresh {
		iter := rs.iters[st.ID] + 1
		rs.iters[st.ID] = iter
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
	}
	rs.ph = phIn
	started := x.e.clock()
	svc := x.res.Service(st.Service)
	if svc == nil {
		// Removed from the project since run creation checked it.
		msg := fmt.Sprintf("service %q no longer exists in the project's services", st.Service)
		if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra, Error: msg}); err != nil {
			return err
		}
		return x.fail(fmt.Sprintf("stage %s: %s", st.ID, msg))
	}
	infra := func(err error) error {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra, Error: err.Error()}); aerr != nil {
			return aerr
		}
		return x.fail("infrastructure error: " + err.Error())
	}

	if st.Do == flow.ServiceStop {
		if _, err := services.Shim(x.ctx, x.res.Envp, rs.envID, "stop", nil, services.Request(svc, 0), services.StopBudget); err != nil {
			return infra(err)
		}
		if err := x.append(&event.ServiceStopped{Service: svc.Name, StageID: st.ID, By: event.ServiceByStage}); err != nil {
			return err
		}
		return x.completeService(st, started)
	}

	timeout := st.Timeout
	if timeout == 0 {
		timeout = svc.Health.Timeout
	}
	// A recovery re-entry waits on the restart it already made.
	restart := st.Do == flow.ServiceRestart && fresh
	flags := []string{"--wait"}
	if restart {
		flags = append(flags, "--restart")
	}
	req := services.Request(svc, timeout)
	req.By, req.Owner, req.StageID = event.ServiceByStage, rs.phase, st.ID
	state, err := services.Shim(x.ctx, x.res.Envp, rs.envID, "start", flags, req, timeout+services.StopBudget+30*time.Second)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return infra(err)
	}
	if state.Launched {
		if aerr := x.append(&event.ServiceStarted{Service: svc.Name, StageID: st.ID, Phase: rs.phase,
			By: event.ServiceByStage, Restart: restart}); aerr != nil {
			return aerr
		}
	}
	if err == nil && state.Phase == services.PhaseHealthy {
		services.Expose(x.ctx, x.e.runPorts, rs.runID, svc, x.e.log)
		if aerr := x.append(&event.ServiceReady{Service: svc.Name, StageID: st.ID, URL: svc.Address()}); aerr != nil {
			return aerr
		}
		return x.completeService(st, started)
	}

	reason, why := event.ReasonExitNonzero, ""
	var code *int
	switch {
	case err != nil, state.Phase == services.PhaseStarting, state.Phase == services.PhaseUnhealthy:
		reason, why = event.ReasonTimeout, fmt.Sprintf("not healthy within %s", timeout)
		if state.LastError != "" && state.Phase == services.PhaseUnhealthy {
			why = state.LastError
		}
	case state.Phase == services.PhaseExited:
		why, code = state.LastError, state.ExitCode
	default:
		why = "it was " + state.Phase + " before it came up"
	}
	logTail, lerr := services.Log(x.ctx, x.res.Envp, rs.envID, svc.Name, 200)
	if lerr != nil {
		logTail = "(its log could not be read: " + lerr.Error() + ")"
	}
	x.publishLog(st.ID, rs.iters[st.ID], logTail)
	feed := shell.Tail(fmt.Sprintf("service %s did not come up: %s\n--- its output ---\n%s", svc.Name, why, logTail), 8<<10)
	if aerr := x.append(&event.ServiceFailed{Service: svc.Name, StageID: st.ID, Reason: why, ExitCode: code}); aerr != nil {
		return aerr
	}
	if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: reason, ExitCode: code, Error: feed}); aerr != nil {
		return aerr
	}
	return x.takeEdge(st, failEdgeKind(st, reason), feed)
}

func (x *exec) completeService(st *flow.Stage, started time.Time) error {
	if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
		return err
	}
	x.advance(st)
	return nil
}

// stopChainServices stops the services this run's stages started: the
// ending chain's (phase), or every one when the run ends with it. A human's
// are left alone — they stay up until a human stops them or the workspace
// goes.
func (x *exec) stopChainServices(phase string, all bool) {
	if x.res == nil || x.rs.fl == nil || x.rs.envID == "" || !usesServiceStages(x.rs.fl) {
		return
	}
	reason := "its chain ended"
	if all {
		reason = "the run ended"
	}
	x.e.stopStageServices(x.ctx, x.res, x.rs.envID, x.rs.runID, reason, func(st sidecar.ServiceState) bool {
		return all || st.Owner == phase
	}, func(p event.Payload) error { return x.append(p) })
}

// stopRunServices is stopChainServices for a run that ended any other way
// (failed, abandoned, aborted): after its loop, on a resolution of its own.
func (e *Engine) stopRunServices(ctx context.Context, rs *runState) {
	if rs == nil || rs.fl == nil || rs.envID == "" || !usesServiceStages(rs.fl) {
		return
	}
	res, err := e.resolveForRun(ctx, rs.view)
	if err != nil {
		return
	}
	defer res.Close()
	e.stopStageServices(ctx, res, rs.envID, rs.runID, "the run ended", func(sidecar.ServiceState) bool { return true },
		func(p event.Payload) error { return e.AppendEvents(ctx, rs.runID, p) })
}

// stopStageServices stops every live service a stage started that pick
// keeps, recording each.
func (e *Engine) stopStageServices(ctx context.Context, res *resolution.Resolution, env ports.EnvID, runID, reason string,
	pick func(sidecar.ServiceState) bool, record func(event.Payload) error) {
	states, err := services.States(ctx, res.Envp, env)
	if err != nil {
		e.log.Warn("services: cannot read what runs to stop it", "run", runID, "err", err)
		return
	}
	for _, st := range states {
		if st.By != event.ServiceByStage || !services.Live(st.Phase) || !pick(st) {
			continue
		}
		req := services.ShimRequest{Name: st.Name}
		if svc := res.Service(st.Name); svc != nil {
			req = services.Request(svc, 0)
		}
		if _, err := services.Shim(ctx, res.Envp, env, "stop", nil, req, services.StopBudget); err != nil {
			e.log.Warn("services: stop failed", "run", runID, "service", st.Name, "err", err)
			continue
		}
		_ = record(&event.ServiceStopped{Service: st.Name, By: event.ServiceByEngine, Reason: reason})
	}
}

// serviceInput renders a run.service.<name> input for an agent: its
// address in the workspace while it is up, else plainly that it is not.
func (x *exec) serviceInput(name string) string {
	svc := x.res.Service(name)
	if svc == nil {
		return fmt.Sprintf("(the project has no service %q)", name)
	}
	st, err := services.State(x.ctx, x.res.Envp, x.rs.envID, name)
	switch {
	case err != nil:
		return fmt.Sprintf("(the service %q could not be checked: %v)", name, err)
	case st.Phase != services.PhaseHealthy:
		msg := fmt.Sprintf("(the service %q is not running: it is %s", name, st.Phase)
		if st.LastError != "" {
			msg += " — " + st.LastError
		}
		return msg + ")"
	case svc.Address() == "":
		return fmt.Sprintf("(the service %q is running; it declares no port)", name)
	}
	return svc.Address() + fmt.Sprintf("\nThe project's service %q is running and healthy at this address in your workspace.", name)
}

// serviceLink is a run.service.<name> source for a human: the preview link
// of its first port while it is up, else "".
func (x *exec) serviceLink(name string) string {
	svc := x.res.Service(name)
	if svc == nil || len(svc.Ports) == 0 {
		return ""
	}
	if st, err := services.State(x.ctx, x.res.Envp, x.rs.envID, name); err != nil || st.Phase != services.PhaseHealthy {
		return ""
	}
	urls := services.Expose(x.ctx, x.e.runPorts, x.rs.runID, svc, x.e.log)
	return urls[svc.Ports[0].Port]
}
