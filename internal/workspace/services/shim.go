// Package services runs a project's services in a run's workspace (FLOWLANG
// §3 service): the protocol with the workspace's supervisor (`apothecary-shim
// service`), and the run page's start/stop/log for a person. The engine's
// service stage speaks the same protocol through this package.
package services

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
	"fmt"
	"strings"
	"time"

	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/envport"
	"apothecary/internal/resolution"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/sidecar"
)

// Supervisor phases (sidecar.ServiceState.Phase).
const (
	PhaseStarting  = "starting"
	PhaseHealthy   = "healthy"
	PhaseUnhealthy = "unhealthy"
	PhaseExited    = "exited"
	PhaseStopping  = "stopping"
	PhaseStopped   = "stopped"
)

// Live reports whether a supervisor phase is a service that is (or is
// becoming, or still) up.
func Live(phase string) bool {
	switch phase {
	case PhaseStarting, PhaseHealthy, PhaseUnhealthy, PhaseStopping:
		return true
	}
	return false
}

// ShimRequest is what `apothecary-shim service start|stop` reads on
// stdin (the shim's Request; the shim builds standalone, so the two
// are kept in step by hand).
type ShimRequest struct {
	Name     string     `json:"name"`
	Start    string     `json:"start,omitempty"`
	Stop     string     `json:"stop,omitempty"`
	Health   ShimHealth `json:"health"`
	Timeout  string     `json:"timeout,omitempty"`
	Interval string     `json:"interval,omitempty"`
	By       string     `json:"by,omitempty"`
	Owner    string     `json:"owner,omitempty"`
	StageID  string     `json:"stage_id,omitempty"`
}

// ShimHealth is the probe part of a ShimRequest.
type ShimHealth struct {
	HTTP    string `json:"http,omitempty"`
	TCP     int    `json:"tcp,omitempty"`
	Command string `json:"command,omitempty"`
}

// Request is the shim request that starts or stops s; timeout <= 0
// uses the service's own start timeout.
func Request(s *resolution.Service, timeout time.Duration) ShimRequest {
	if timeout <= 0 {
		timeout = s.Health.Timeout
	}
	req := ShimRequest{Name: s.Name, Start: s.Start, Stop: s.Stop,
		Health: ShimHealth{HTTP: s.Health.HTTP, TCP: s.Health.TCP, Command: s.Health.Command}}
	if timeout > 0 {
		req.Timeout = timeout.String()
	}
	if s.Health.Interval > 0 {
		req.Interval = s.Health.Interval.String()
	}
	return req
}

// StopBudget bounds a stop: the shim gives a stop script two minutes
// and what is left of the service ten seconds after it.
const StopBudget = 3 * time.Minute

// Shim runs one `apothecary-shim service <verb>` in the primary
// checkout, the request on stdin (one JSON line, so no line of it can end
// the heredoc), and reads the state it prints.
func Shim(ctx context.Context, envp envport.EnvProvisioner, env envport.EnvID, verb string, flags []string,
	req ShimRequest, timeout time.Duration) (sidecar.ServiceState, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return sidecar.ServiceState{}, err
	}
	script := fmt.Sprintf("apothecary-shim service %s <<'APOTHECARY_SERVICE'\n%s\nAPOTHECARY_SERVICE\n",
		strings.Join(append([]string{verb}, flags...), " "), body)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := envp.Exec(cctx, env, envport.Command{Script: script, Dir: event.PrimaryCheckoutDir, Timeout: timeout})
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

// States reads every service's state in the workspace: through the
// sidecar when the provisioner has one, else (or when it predates the
// services routes) through Exec.
func States(ctx context.Context, envp envport.EnvProvisioner, env envport.EnvID) ([]sidecar.ServiceState, error) {
	if sc, ok := envp.(envport.Sidecar); ok {
		if conn, err := sc.Sidecar(ctx, env); err == nil {
			if out, err := sidecar.New(conn).Services(ctx); err == nil {
				return out, nil
			}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := envp.Exec(cctx, env, envport.Command{Script: "apothecary-shim service status", Timeout: time.Minute})
	if err != nil {
		return nil, err
	}
	var out []sidecar.ServiceState
	if res.ExitCode != 0 || !lastJSON(res.Output, &out) {
		return nil, fmt.Errorf("service status: exit %d: %s", res.ExitCode, shell.Tail(strings.TrimSpace(res.Output), 1<<10))
	}
	return out, nil
}

// State is one service's state; a service never started reads
// stopped.
func State(ctx context.Context, envp envport.EnvProvisioner, env envport.EnvID, name string) (sidecar.ServiceState, error) {
	all, err := States(ctx, envp, env)
	if err != nil {
		return sidecar.ServiceState{}, err
	}
	for _, st := range all {
		if st.Name == name {
			return st, nil
		}
	}
	return sidecar.ServiceState{Name: name, Phase: PhaseStopped}, nil
}

// Log is the last n lines of a service's output.
func Log(ctx context.Context, envp envport.EnvProvisioner, env envport.EnvID, name string, n int) (string, error) {
	if n <= 0 {
		n = 200
	}
	if sc, ok := envp.(envport.Sidecar); ok {
		if conn, err := sc.Sidecar(ctx, env); err == nil {
			if out, err := sidecar.New(conn).ServiceLog(ctx, name, n); err == nil {
				return out, nil
			}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := envp.Exec(cctx, env, envport.Command{
		Script: fmt.Sprintf("apothecary-shim service logs --tail %d %s", n, name), Timeout: time.Minute})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("service logs %s: exit %d: %s", name, res.ExitCode, shell.Tail(res.Output, 1<<10))
	}
	return res.Output, nil
}
