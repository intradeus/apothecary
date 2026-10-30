package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/ports/envport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/sidecar"
	"apothecary/internal/wsinfo"
)

// Events appends to a run's log from outside its loop (the engine's
// AppendEvents, which serializes with the run's own writer).
type Events interface {
	AppendEvents(ctx context.Context, runID string, ps ...event.Payload) error
}

// Control is the run page's service actions: a person starting, stopping
// and reading a project service in a run's workspace. What it does is run
// history (service_started/ready/failed/stopped), appended through Events.
type Control struct {
	Runs     *run.Reader
	Resolver resolution.Resolver
	Events   Events
	Ports    Exposer
	// Log is nil for slog.Default.
	Log *slog.Logger
}

func New(runs *run.Reader, resolver resolution.Resolver, events Events, ports Exposer, log *slog.Logger) *Control {
	return &Control{Runs: runs, Resolver: resolver, Events: events, Ports: ports, Log: log}
}

func (c *Control) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// Exposer publishes one workspace port of a run and returns its URL. An
// exposer that can say it is off (Enabled() false) is not asked at all.
type Exposer interface {
	Expose(ctx context.Context, runID string, port int) (string, error)
}

// Expose publishes the service's ports for humans, the way the Ports view
// exposes any port, and returns each one's preview link (path included).
// Best effort: no exposure, no links.
func Expose(ctx context.Context, exposer Exposer, runID string, svc *resolution.Service, log *slog.Logger) map[int]string {
	out := map[int]string{}
	if exposer == nil {
		return out
	}
	if o, ok := exposer.(interface{ Enabled() bool }); ok && !o.Enabled() {
		return out
	}
	for _, p := range svc.Ports {
		url, err := exposer.Expose(ctx, runID, p.Port)
		if err != nil {
			// Read on every look at the pane: a provisioner that cannot
			// expose says so each time, which is not news.
			log.Debug("services: expose failed", "run", runID, "service", svc.Name, "port", p.Port, "err", err)
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

var (
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
func (c *Control) serviceRun(ctx context.Context, runID string) (run.View, *resolution.Resolution, error) {
	v, err := c.Runs.View(ctx, runID)
	if err != nil {
		return v, nil, err
	}
	res, err := resolution.ForRun(ctx, c.Resolver, v)
	if err != nil {
		return v, nil, err
	}
	return v, res, nil
}

func hasWorkspace(v run.View) bool { return v.EnvID != "" && v.State != run.Archived }

// RunServices lists the project's services with how each stands in the
// run's workspace. Healthy ones get their preview links (exposed on demand,
// so links come back after an engine restart).
func (c *Control) RunServices(ctx context.Context, runID string) (RunServices, error) {
	v, res, err := c.serviceRun(ctx, runID)
	if err != nil {
		return RunServices{}, err
	}
	defer res.Close()
	out := RunServices{Workspace: hasWorkspace(v), Services: []ServiceInfo{}}
	states := map[string]sidecar.ServiceState{}
	// A project with no services has nothing to ask the workspace about.
	if out.Workspace && len(res.Services) > 0 {
		all, err := States(ctx, res.Envp, envport.EnvID(v.EnvID))
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
			st = sidecar.ServiceState{Name: svc.Name, Phase: PhaseStopped}
			if out.Error != "" {
				st.Phase = "unknown"
			}
		}
		var urls map[int]string
		if st.Phase == PhaseHealthy {
			urls = Expose(ctx, c.Ports, runID, svc, c.log())
		}
		out.Services = append(out.Services, serviceInfo(svc, st, urls))
	}
	return out, nil
}

func serviceInfo(svc *resolution.Service, st sidecar.ServiceState, urls map[int]string) ServiceInfo {
	info := ServiceInfo{Name: svc.Name, Description: svc.Description, State: st.Phase, Healthy: st.Phase == PhaseHealthy,
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
func (c *Control) StartService(ctx context.Context, runID, name string, restart bool) (ServiceInfo, error) {
	v, res, err := c.serviceRun(ctx, runID)
	if err != nil {
		return ServiceInfo{}, err
	}
	defer res.Close()
	svc := res.Service(name)
	if svc == nil {
		return ServiceInfo{}, fmt.Errorf("%w: %q", ErrNoSuchService, name)
	}
	if !hasWorkspace(v) {
		return ServiceInfo{}, fmt.Errorf("run %s: %w", runID, wsinfo.ErrNoWorkspace)
	}
	env := envport.EnvID(v.EnvID)
	req := Request(svc, 0)
	req.By = event.ServiceByHuman
	var flags []string
	if restart {
		flags = []string{"--restart"}
	}
	st, err := Shim(ctx, res.Envp, env, "start", flags, req, StopBudget+time.Minute)
	if err != nil {
		return ServiceInfo{}, err
	}
	if st.Launched {
		if err := c.Events.AppendEvents(ctx, runID, &event.ServiceStarted{Service: name, By: event.ServiceByHuman, Restart: restart}); err != nil {
			return ServiceInfo{}, err
		}
		go c.watchService(runID, v.ProjectID, *svc, env)
	}
	return serviceInfo(svc, st, nil), nil
}

// watchService follows a human's start until the service is up or failed,
// then says so on the run's log and exposes its ports. It resolves the
// project itself: the caller's resolution is released when it returns.
func (c *Control) watchService(runID, projectID string, svc resolution.Service, env envport.EnvID) {
	ctx, cancel := context.WithTimeout(context.Background(), svc.Health.Timeout+time.Minute)
	defer cancel()
	res, err := c.Resolver.Resolve(ctx, projectID)
	if err != nil {
		c.log().Warn("services: cannot watch a start", "run", runID, "service", svc.Name, "err", err)
		return
	}
	defer res.Close()
	for {
		st, err := State(ctx, res.Envp, env, svc.Name)
		switch {
		case err != nil, st.Phase == PhaseStarting:
		case st.Phase == PhaseHealthy:
			Expose(ctx, c.Ports, runID, &svc, c.log())
			_ = c.Events.AppendEvents(context.WithoutCancel(ctx), runID, &event.ServiceReady{Service: svc.Name, URL: svc.Address()})
			return
		case st.Phase == PhaseStopped, st.Phase == PhaseStopping:
			return // stopped by someone while starting: nothing failed
		default:
			_ = c.Events.AppendEvents(context.WithoutCancel(ctx), runID, &event.ServiceFailed{Service: svc.Name,
				Reason: firstNonEmpty(st.LastError, "it is "+st.Phase), ExitCode: st.ExitCode})
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(WatchEvery):
		}
	}
}

// WatchEvery paces a human start's watch (a var: tests compress it).
var WatchEvery = time.Second

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
func (c *Control) StopService(ctx context.Context, runID, name string) (ServiceInfo, error) {
	v, res, err := c.serviceRun(ctx, runID)
	if err != nil {
		return ServiceInfo{}, err
	}
	defer res.Close()
	svc := res.Service(name)
	if svc == nil {
		return ServiceInfo{}, fmt.Errorf("%w: %q", ErrNoSuchService, name)
	}
	if !hasWorkspace(v) {
		return ServiceInfo{}, fmt.Errorf("run %s: %w", runID, wsinfo.ErrNoWorkspace)
	}
	before, _ := State(ctx, res.Envp, envport.EnvID(v.EnvID), name)
	st, err := Shim(ctx, res.Envp, envport.EnvID(v.EnvID), "stop", nil, Request(svc, 0), StopBudget)
	if err != nil {
		return ServiceInfo{}, err
	}
	if Live(before.Phase) {
		if err := c.Events.AppendEvents(ctx, runID, &event.ServiceStopped{Service: name, By: event.ServiceByHuman}); err != nil {
			return ServiceInfo{}, err
		}
	}
	return serviceInfo(svc, st, nil), nil
}

// ServiceLog is the last n lines of a service's output in the run's
// workspace.
func (c *Control) ServiceLog(ctx context.Context, runID, name string, n int) (string, error) {
	v, res, err := c.serviceRun(ctx, runID)
	if err != nil {
		return "", err
	}
	defer res.Close()
	if res.Service(name) == nil {
		return "", fmt.Errorf("%w: %q", ErrNoSuchService, name)
	}
	if !hasWorkspace(v) {
		return "", fmt.Errorf("run %s: %w", runID, wsinfo.ErrNoWorkspace)
	}
	return Log(ctx, res.Envp, envport.EnvID(v.EnvID), name, n)
}
