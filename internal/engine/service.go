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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
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

// Supervisor phases (sidecar.ServiceState.Phase).
const (
	svcStarting  = "starting"
	svcHealthy   = "healthy"
	svcUnhealthy = "unhealthy"
	svcExited    = "exited"
	svcStopping  = "stopping"
	svcStopped   = "stopped"
)

func svcLive(phase string) bool {
	switch phase {
	case svcStarting, svcHealthy, svcUnhealthy, svcStopping:
		return true
	}
	return false
}

// shimServiceRequest is what `apothecary-shim service start|stop` reads on
// stdin (the shim's serviceRequest; the shim builds standalone, so the two
// are kept in step by hand).
type shimServiceRequest struct {
	Name     string            `json:"name"`
	Start    string            `json:"start,omitempty"`
	Stop     string            `json:"stop,omitempty"`
	Health   shimServiceHealth `json:"health"`
	Timeout  string            `json:"timeout,omitempty"`
	Interval string            `json:"interval,omitempty"`
	By       string            `json:"by,omitempty"`
	Owner    string            `json:"owner,omitempty"`
	StageID  string            `json:"stage_id,omitempty"`
}

type shimServiceHealth struct {
	HTTP    string `json:"http,omitempty"`
	TCP     int    `json:"tcp,omitempty"`
	Command string `json:"command,omitempty"`
}

// serviceRequest is the shim request that starts or stops s; timeout <= 0
// uses the service's own start timeout.
func serviceRequest(s *resolution.Service, timeout time.Duration) shimServiceRequest {
	if timeout <= 0 {
		timeout = s.Health.Timeout
	}
	req := shimServiceRequest{Name: s.Name, Start: s.Start, Stop: s.Stop,
		Health: shimServiceHealth{HTTP: s.Health.HTTP, TCP: s.Health.TCP, Command: s.Health.Command}}
	if timeout > 0 {
		req.Timeout = timeout.String()
	}
	if s.Health.Interval > 0 {
		req.Interval = s.Health.Interval.String()
	}
	return req
}

// serviceStopBudget bounds a stop: the shim gives a stop script two minutes
// and what is left of the service ten seconds after it.
const serviceStopBudget = 3 * time.Minute

// shimService runs one `apothecary-shim service <verb>` in the primary
// checkout, the request on stdin (one JSON line, so no line of it can end
// the heredoc), and reads the state it prints.
func shimService(ctx context.Context, envp ports.EnvProvisioner, env ports.EnvID, verb string, flags []string,
	req shimServiceRequest, timeout time.Duration) (sidecar.ServiceState, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return sidecar.ServiceState{}, err
	}
	script := fmt.Sprintf("apothecary-shim service %s <<'APOTHECARY_SERVICE'\n%s\nAPOTHECARY_SERVICE\n",
		strings.Join(append([]string{verb}, flags...), " "), body)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := envp.Exec(cctx, env, ports.Command{Script: script, Dir: event.PrimaryCheckoutDir, Timeout: timeout})
	if err != nil {
		return sidecar.ServiceState{}, err
	}
	var st sidecar.ServiceState
	if res.ExitCode != 0 || !lastJSON(res.Output, &st) {
		return sidecar.ServiceState{}, fmt.Errorf("service %s %s: exit %d: %s", verb, req.Name, res.ExitCode, shell.Tail(strings.TrimSpace(res.Output), 1<<10))
	}
	return st, nil
}

// lastJSON decodes the last line of output that is a JSON value into v:
// what the shim prints comes last, after anything the shell said.
func lastJSON(output string, v any) bool {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if (strings.HasPrefix(l, "{") || strings.HasPrefix(l, "[")) && json.Unmarshal([]byte(l), v) == nil {
			return true
		}
	}
	return false
}

// serviceStates reads every service's state in the workspace: through the
// sidecar when the provisioner has one, else (or when it predates the
// services routes) through Exec.
func serviceStates(ctx context.Context, envp ports.EnvProvisioner, env ports.EnvID) ([]sidecar.ServiceState, error) {
	if sc, ok := envp.(ports.Sidecar); ok {
		if conn, err := sc.Sidecar(ctx, env); err == nil {
			if out, err := sidecar.New(conn).Services(ctx); err == nil {
				return out, nil
			}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := envp.Exec(cctx, env, ports.Command{Script: "apothecary-shim service status", Timeout: time.Minute})
	if err != nil {
		return nil, err
	}
	var out []sidecar.ServiceState
	if res.ExitCode != 0 || !lastJSON(res.Output, &out) {
		return nil, fmt.Errorf("service status: exit %d: %s", res.ExitCode, shell.Tail(strings.TrimSpace(res.Output), 1<<10))
	}
	return out, nil
}

// serviceState is one service's state; a service never started reads
// stopped.
func serviceState(ctx context.Context, envp ports.EnvProvisioner, env ports.EnvID, name string) (sidecar.ServiceState, error) {
	all, err := serviceStates(ctx, envp, env)
	if err != nil {
		return sidecar.ServiceState{}, err
	}
	for _, st := range all {
		if st.Name == name {
			return st, nil
		}
	}
	return sidecar.ServiceState{Name: name, Phase: svcStopped}, nil
}

// serviceLog is the last n lines of a service's output.
func serviceLog(ctx context.Context, envp ports.EnvProvisioner, env ports.EnvID, name string, n int) (string, error) {
	if n <= 0 {
		n = 200
	}
	if sc, ok := envp.(ports.Sidecar); ok {
		if conn, err := sc.Sidecar(ctx, env); err == nil {
			if out, err := sidecar.New(conn).ServiceLog(ctx, name, n); err == nil {
				return out, nil
			}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := envp.Exec(cctx, env, ports.Command{
		Script: fmt.Sprintf("apothecary-shim service logs --tail %d %s", n, name), Timeout: time.Minute})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("service logs %s: exit %d: %s", name, res.ExitCode, shell.Tail(res.Output, 1<<10))
	}
	return res.Output, nil
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
		if _, err := shimService(x.ctx, x.res.Envp, rs.envID, "stop", nil, serviceRequest(svc, 0), serviceStopBudget); err != nil {
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
	req := serviceRequest(svc, timeout)
	req.By, req.Owner, req.StageID = event.ServiceByStage, rs.phase, st.ID
	state, err := shimService(x.ctx, x.res.Envp, rs.envID, "start", flags, req, timeout+serviceStopBudget+30*time.Second)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return infra(err)
	}
	if state.Launched {
		if aerr := x.append(&event.ServiceStarted{Service: svc.Name, StageID: st.ID, Phase: rs.phase,
			By: event.ServiceByStage, Restart: restart}); aerr != nil {
			return aerr
		}
	}
	if err == nil && state.Phase == svcHealthy {
		x.e.exposeService(x.ctx, rs.runID, svc)
		if aerr := x.append(&event.ServiceReady{Service: svc.Name, StageID: st.ID, URL: svc.Address()}); aerr != nil {
			return aerr
		}
		return x.completeService(st, started)
	}

	reason, why := event.ReasonExitNonzero, ""
	var code *int
	switch {
	case err != nil, state.Phase == svcStarting, state.Phase == svcUnhealthy:
		reason, why = event.ReasonTimeout, fmt.Sprintf("not healthy within %s", timeout)
		if state.LastError != "" && state.Phase == svcUnhealthy {
			why = state.LastError
		}
	case state.Phase == svcExited:
		why, code = state.LastError, state.ExitCode
	default:
		why = "it was " + state.Phase + " before it came up"
	}
	logTail, lerr := serviceLog(x.ctx, x.res.Envp, rs.envID, svc.Name, 200)
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
		func(p event.Payload) error { return e.appendAndEmit(ctx, rs.runID, p) })
}

// stopStageServices stops every live service a stage started that pick
// keeps, recording each.
func (e *Engine) stopStageServices(ctx context.Context, res *resolution.Resolution, env ports.EnvID, runID, reason string,
	pick func(sidecar.ServiceState) bool, record func(event.Payload) error) {
	states, err := serviceStates(ctx, res.Envp, env)
	if err != nil {
		e.log.Warn("services: cannot read what runs to stop it", "run", runID, "err", err)
		return
	}
	for _, st := range states {
		if st.By != event.ServiceByStage || !svcLive(st.Phase) || !pick(st) {
			continue
		}
		req := shimServiceRequest{Name: st.Name}
		if svc := res.Service(st.Name); svc != nil {
			req = serviceRequest(svc, 0)
		}
		if _, err := shimService(ctx, res.Envp, env, "stop", nil, req, serviceStopBudget); err != nil {
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
	st, err := serviceState(x.ctx, x.res.Envp, x.rs.envID, name)
	switch {
	case err != nil:
		return fmt.Sprintf("(the service %q could not be checked: %v)", name, err)
	case st.Phase != svcHealthy:
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
	if st, err := serviceState(x.ctx, x.res.Envp, x.rs.envID, name); err != nil || st.Phase != svcHealthy {
		return ""
	}
	urls := x.e.exposeService(x.ctx, x.rs.runID, svc)
	return urls[svc.Ports[0].Port]
}

// exposeService publishes the service's ports for humans, the way the Ports
// view exposes any port, and returns each one's preview link (path
// included). Best effort: no port relay, no links.
func (e *Engine) exposeService(ctx context.Context, runID string, svc *resolution.Service) map[int]string {
	out := map[int]string{}
	if e.portRelay == nil {
		return out
	}
	for _, p := range svc.Ports {
		url, err := e.ExposeRunPort(ctx, runID, p.Port)
		if err != nil {
			// Read on every look at the pane: a provisioner that cannot
			// expose says so each time, which is not news.
			e.log.Debug("services: expose failed", "run", runID, "service", svc.Name, "port", p.Port, "err", err)
			continue
		}
		out[p.Port] = previewURL(url, p.Path)
	}
	return out
}

func previewURL(base, path string) string {
	if path == "" || path == "/" {
		return base
	}
	return strings.TrimSuffix(base, "/") + path
}

// --- the run page ------------------------------------------------------------

var (
	// ErrNoWorkspace: the run has no workspace for a service to run in.
	ErrNoWorkspace = errors.New("the run has no workspace")
	// ErrNoSuchService: the project declares no service of that name.
	ErrNoSuchService = errors.New("no such service in the project")
)

// ServiceInfo is one project service on the run page: what the project
// declares and how it stands in the workspace now.
type ServiceInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// State is the supervisor's: stopped (also: never started), starting,
	// healthy, unhealthy, exited (its start failed, or its supervisor is
	// gone), stopping — or unknown when the workspace could not be read.
	State   string `json:"state"`
	Healthy bool   `json:"healthy"`
	// By is who started it last (stage|human); a stage's names its StageID
	// and the flow phase (chain) whose end stops it.
	By        string     `json:"by,omitempty"`
	StageID   string     `json:"stage_id,omitempty"`
	FlowPhase string     `json:"flow_phase,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	HealthyAt *time.Time `json:"healthy_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	// Probe is what says it is up ("GET http://localhost:3000/health", "tcp
	// 5432", "command: …"; empty: up once started), and the last answer.
	Probe       string            `json:"probe,omitempty"`
	LastProbeAt *time.Time        `json:"last_probe_at,omitempty"`
	LastProbeOK bool              `json:"last_probe_ok"`
	LastError   string            `json:"last_error,omitempty"`
	Ports       []ServicePortInfo `json:"ports"`
}

// ServicePortInfo is one port of a service: its address in the workspace
// and, while the service is healthy, the human's preview link.
type ServicePortInfo struct {
	Port    int    `json:"port"`
	Label   string `json:"label,omitempty"`
	Path    string `json:"path,omitempty"`
	Address string `json:"address"`
	URL     string `json:"url,omitempty"`
}

// RunServices is the run page's Services pane: every project service with
// its live state. Workspace false: the run has none (nothing can start);
// Error: it has one, but its services could not be read just now.
type RunServices struct {
	Workspace bool          `json:"workspace"`
	Error     string        `json:"error,omitempty"`
	Services  []ServiceInfo `json:"services"`
}

// serviceRun is what a human's service action needs: the run's workspace
// and the resolved project (release it).
func (e *Engine) serviceRun(ctx context.Context, runID string) (run.View, *resolution.Resolution, error) {
	v, err := e.runs.View(ctx, runID)
	if err != nil {
		return v, nil, err
	}
	res, err := e.resolveForRun(ctx, v)
	if err != nil {
		return v, nil, err
	}
	return v, res, nil
}

func hasWorkspace(v run.View) bool { return v.EnvID != "" && v.State != run.Archived }

// RunServices lists the project's services with how each stands in the
// run's workspace. Healthy ones get their preview links (exposed on demand,
// so links come back after an engine restart).
func (e *Engine) RunServices(ctx context.Context, runID string) (RunServices, error) {
	v, res, err := e.serviceRun(ctx, runID)
	if err != nil {
		return RunServices{}, err
	}
	defer res.Close()
	out := RunServices{Workspace: hasWorkspace(v), Services: []ServiceInfo{}}
	states := map[string]sidecar.ServiceState{}
	// A project with no services has nothing to ask the workspace about.
	if out.Workspace && len(res.Services) > 0 {
		all, err := serviceStates(ctx, res.Envp, ports.EnvID(v.EnvID))
		if err != nil {
			out.Error = err.Error()
		}
		for _, st := range all {
			states[st.Name] = st
		}
	}
	for i := range res.Services {
		svc := &res.Services[i]
		st, ok := states[svc.Name]
		if !ok {
			st = sidecar.ServiceState{Name: svc.Name, Phase: svcStopped}
			if out.Error != "" {
				st.Phase = "unknown"
			}
		}
		var urls map[int]string
		if st.Phase == svcHealthy {
			urls = e.exposeService(ctx, runID, svc)
		}
		out.Services = append(out.Services, serviceInfo(svc, st, urls))
	}
	return out, nil
}

func serviceInfo(svc *resolution.Service, st sidecar.ServiceState, urls map[int]string) ServiceInfo {
	info := ServiceInfo{Name: svc.Name, Description: svc.Description, State: st.Phase, Healthy: st.Phase == svcHealthy,
		By: st.By, StageID: st.StageID, FlowPhase: st.Owner, StartedAt: st.StartedAt, HealthyAt: st.HealthyAt,
		EndedAt: st.EndedAt, ExitCode: st.ExitCode, Probe: svc.Probe(), LastProbeAt: st.LastProbeAt,
		LastProbeOK: st.LastProbeOK, LastError: st.LastError, Ports: []ServicePortInfo{}}
	for _, p := range svc.Ports {
		info.Ports = append(info.Ports, ServicePortInfo{Port: p.Port, Label: p.Label, Path: p.Path,
			Address: p.Address(), URL: urls[p.Port]})
	}
	return info
}

// StartService is a human starting (restart: restarting) a service from the
// run page. It returns once the supervisor runs it; whether it comes up is
// watched in the background — service_ready or service_failed lands on the
// run's log, its ports are exposed — and read live from RunServices. A
// service already up is left alone (unless restarted).
func (e *Engine) StartService(ctx context.Context, runID, name string, restart bool) (ServiceInfo, error) {
	v, res, err := e.serviceRun(ctx, runID)
	if err != nil {
		return ServiceInfo{}, err
	}
	defer res.Close()
	svc := res.Service(name)
	if svc == nil {
		return ServiceInfo{}, fmt.Errorf("%w: %q", ErrNoSuchService, name)
	}
	if !hasWorkspace(v) {
		return ServiceInfo{}, fmt.Errorf("run %s: %w", runID, ErrNoWorkspace)
	}
	env := ports.EnvID(v.EnvID)
	req := serviceRequest(svc, 0)
	req.By = event.ServiceByHuman
	var flags []string
	if restart {
		flags = []string{"--restart"}
	}
	st, err := shimService(ctx, res.Envp, env, "start", flags, req, serviceStopBudget+time.Minute)
	if err != nil {
		return ServiceInfo{}, err
	}
	if st.Launched {
		if err := e.appendAndEmit(ctx, runID, &event.ServiceStarted{Service: name, By: event.ServiceByHuman, Restart: restart}); err != nil {
			return ServiceInfo{}, err
		}
		go e.watchService(runID, v.ProjectID, *svc, env)
	}
	return serviceInfo(svc, st, nil), nil
}

// watchService follows a human's start until the service is up or failed,
// then says so on the run's log and exposes its ports. It resolves the
// project itself: the caller's resolution is released when it returns.
func (e *Engine) watchService(runID, projectID string, svc resolution.Service, env ports.EnvID) {
	ctx, cancel := context.WithTimeout(context.Background(), svc.Health.Timeout+time.Minute)
	defer cancel()
	res, err := e.resolver.Resolve(ctx, projectID)
	if err != nil {
		e.log.Warn("services: cannot watch a start", "run", runID, "service", svc.Name, "err", err)
		return
	}
	defer res.Close()
	for {
		st, err := serviceState(ctx, res.Envp, env, svc.Name)
		switch {
		case err != nil, st.Phase == svcStarting:
		case st.Phase == svcHealthy:
			e.exposeService(ctx, runID, &svc)
			_ = e.appendAndEmit(context.WithoutCancel(ctx), runID, &event.ServiceReady{Service: svc.Name, URL: svc.Address()})
			return
		case st.Phase == svcStopped, st.Phase == svcStopping:
			return // stopped by someone while starting: nothing failed
		default:
			_ = e.appendAndEmit(context.WithoutCancel(ctx), runID, &event.ServiceFailed{Service: svc.Name,
				Reason: firstNonEmpty(st.LastError, "it is "+st.Phase), ExitCode: st.ExitCode})
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(serviceWatchEvery):
		}
	}
}

// serviceWatchEvery paces watchService (a var: tests compress it).
var serviceWatchEvery = time.Second

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// StopService is a human stopping a service from the run page, whoever
// started it.
func (e *Engine) StopService(ctx context.Context, runID, name string) (ServiceInfo, error) {
	v, res, err := e.serviceRun(ctx, runID)
	if err != nil {
		return ServiceInfo{}, err
	}
	defer res.Close()
	svc := res.Service(name)
	if svc == nil {
		return ServiceInfo{}, fmt.Errorf("%w: %q", ErrNoSuchService, name)
	}
	if !hasWorkspace(v) {
		return ServiceInfo{}, fmt.Errorf("run %s: %w", runID, ErrNoWorkspace)
	}
	before, _ := serviceState(ctx, res.Envp, ports.EnvID(v.EnvID), name)
	st, err := shimService(ctx, res.Envp, ports.EnvID(v.EnvID), "stop", nil, serviceRequest(svc, 0), serviceStopBudget)
	if err != nil {
		return ServiceInfo{}, err
	}
	if svcLive(before.Phase) {
		if err := e.appendAndEmit(ctx, runID, &event.ServiceStopped{Service: name, By: event.ServiceByHuman}); err != nil {
			return ServiceInfo{}, err
		}
	}
	return serviceInfo(svc, st, nil), nil
}

// ServiceLog is the last n lines of a service's output in the run's
// workspace.
func (e *Engine) ServiceLog(ctx context.Context, runID, name string, n int) (string, error) {
	v, res, err := e.serviceRun(ctx, runID)
	if err != nil {
		return "", err
	}
	defer res.Close()
	if res.Service(name) == nil {
		return "", fmt.Errorf("%w: %q", ErrNoSuchService, name)
	}
	if !hasWorkspace(v) {
		return "", fmt.Errorf("run %s: %w", runID, ErrNoWorkspace)
	}
	return serviceLog(ctx, res.Envp, ports.EnvID(v.EnvID), name, n)
}

// --- one writer at a time ----------------------------------------------------

// appendLock serializes appends to one run's log. The run's goroutine is
// its writer, but a person acting on a service from the run page appends
// while it runs (a service's history is not the run loop's to sequence), and
// the store assigns seqs per call.
func (e *Engine) appendLock(runID string) func() {
	m, _ := e.appendLocks.LoadOrStore(runID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
