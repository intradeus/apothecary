package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/sidecar"
	"apothecary/internal/workspace/wsinfo"
)

const fixtureProject = "p1"

// appendTo appends straight to a store (the engine's AppendEvents without
// a run loop to serialize with).
type appendTo struct{ store *event.MemStore }

func (a appendTo) AppendEvents(ctx context.Context, runID string, ps ...event.Payload) error {
	_, err := a.store.Append(ctx, runID, ps...)
	return err
}

// fixture is a project with two services over a fake workspace supervisor,
// and the run page's control over it (no port exposure).
type fixture struct {
	store *event.MemStore
	envp  *fake.EnvProvisioner
	svcs  *fake.Services
	res   *resolution.Resolution
	c     *Control
}

func newFixture() *fixture {
	h := &fixture{store: event.NewMemStore(), envp: fake.NewEnvProvisioner(), svcs: fake.NewServices()}
	h.envp.ExecFunc = h.svcs.Exec(h.envp.ExecFunc)
	h.res = &resolution.Resolution{ProjectID: fixtureProject, Envp: h.envp, Services: []resolution.Service{
		{Name: "default", Start: "npm run dev", Health: resolution.ServiceHealth{HTTP: "http://localhost:3000/health", Timeout: 5 * time.Minute, Interval: 10 * time.Second},
			Ports: []resolution.ServicePort{{Port: 3000, Label: "app"}}},
		{Name: "db", Start: "docker compose up -d db", Stop: "docker compose stop db", Health: resolution.ServiceHealth{TCP: 5432, Timeout: time.Minute}},
	}}
	h.c = New(&run.Reader{Store: h.store}, resolution.StaticResolver{R: h.res}, appendTo{h.store}, nil, nil)
	return h
}

func TestServiceRefusedWithoutAWorkspace(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	if _, err := h.store.Append(ctx, "r1", &event.RunCreated{ProjectID: fixtureProject}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.StartService(ctx, "r1", "default", false); !errors.Is(err, wsinfo.ErrNoWorkspace) {
		t.Errorf("StartService = %v", err)
	}
	list, err := h.c.RunServices(ctx, "r1")
	if err != nil || list.Workspace || len(list.Services) != 2 || list.Services[0].State != "stopped" {
		t.Errorf("RunServices = %+v, %v", list, err)
	}
}

// addRun creates a run with a live env; returns the fixture's resolver pointing
// at it. envID is deterministic from the fake (env-1 for the first).
func (h *fixture) addRun(t *testing.T, runID string) {
	t.Helper()
	ctx := context.Background()
	id, err := h.envp.Provision(ctx, envport.EnvSpec{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "t", FlowName: "loop", ProjectID: fixtureProject},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(id)},
	} {
		if _, err := h.store.Append(ctx, runID, p); err != nil {
			t.Fatal(err)
		}
	}
}

// events reads a run's appended service events, as "type:service" strings.
func (h *fixture) events(t *testing.T, runID string) []string {
	t.Helper()
	evs, err := h.store.Events(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		switch p := e.Payload.(type) {
		case *event.ServiceStarted:
			out = append(out, "started:"+p.Service)
		case *event.ServiceReady:
			out = append(out, "ready:"+p.Service)
		case *event.ServiceStopped:
			out = append(out, "stopped:"+p.Service)
		case *event.ServiceFailed:
			out = append(out, "failed:"+p.Service+":"+p.Reason)
		}
	}
	return out
}

func waitEvents(t *testing.T, h *fixture, runID string, want int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := h.events(t, runID); len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s: have %v, want %d events", runID, h.events(t, runID), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRunServicesListsLiveState(t *testing.T) {
	h := newFixture()
	WatchEvery = time.Millisecond
	h.addRun(t, "r1")
	ctx := context.Background()

	list, err := h.c.RunServices(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if !list.Workspace || len(list.Services) != 2 {
		t.Fatalf("list = %+v", list)
	}
	// Both are stopped, and each carries what the project declares.
	byName := map[string]ServiceInfo{}
	for _, s := range list.Services {
		byName[s.Name] = s
	}
	if byName["default"].State != PhaseStopped || byName["default"].Healthy {
		t.Errorf("default = %+v", byName["default"])
	}
	if got := byName["default"].Ports; len(got) != 1 || got[0].Port != 3000 || got[0].URL != "" {
		t.Errorf("ports = %+v (no exposure, no URL)", got)
	}
	if byName["default"].Probe == "" {
		t.Error("the probe description is missing")
	}
}

func TestStartServiceHealthyAppendsReadyAndExposes(t *testing.T) {
	h := newFixture()
	WatchEvery = time.Millisecond
	exp := &fakeExposer{urls: map[int]string{3000: "http://host:40000"}}
	h.c.Ports = exp
	h.addRun(t, "r1")
	ctx := context.Background()

	info, err := h.c.StartService(ctx, "r1", "default", false)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != PhaseStarting && info.State != PhaseHealthy {
		t.Fatalf("info = %+v", info)
	}
	got := waitEvents(t, h, "r1", 2)
	if got[0] != "started:default" || got[1] != "ready:default" {
		t.Fatalf("events = %v", got)
	}
	if n := exp.count(); n == 0 {
		t.Error("a healthy service's ports were never exposed")
	}
	// The pane now shows it healthy with a preview URL.
	list, _ := h.c.RunServices(ctx, "r1")
	for _, s := range list.Services {
		if s.Name == "default" {
			if !s.Healthy || len(s.Ports) == 0 || s.Ports[0].URL != "http://host:40000" {
				t.Fatalf("default = %+v", s)
			}
		}
	}
}

func TestStartServiceThatFailsAppendsFailed(t *testing.T) {
	h := newFixture()
	WatchEvery = time.Millisecond
	h.svcs.Outcomes["db"] = []string{fake.OutcomeExits}
	h.addRun(t, "r1")
	if _, err := h.c.StartService(context.Background(), "r1", "db", false); err != nil {
		t.Fatal(err)
	}
	got := waitEvents(t, h, "r1", 2)
	if got[0] != "started:db" || !strings.HasPrefix(got[1], "failed:db:") {
		t.Fatalf("events = %v", got)
	}
}

func TestStartServiceAlreadyUpDoesNotRestart(t *testing.T) {
	h := newFixture()
	WatchEvery = time.Millisecond
	h.addRun(t, "r1")
	ctx := context.Background()
	if _, err := h.c.StartService(ctx, "r1", "default", false); err != nil {
		t.Fatal(err)
	}
	waitEvents(t, h, "r1", 2)
	// Start again without restart: the supervisor leaves it alone, so no new
	// started event lands (Launched is false).
	if _, err := h.c.StartService(ctx, "r1", "default", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := h.events(t, "r1"); len(got) != 2 {
		t.Fatalf("events = %v, want no second start", got)
	}
	// Restart does relaunch.
	if _, err := h.c.StartService(ctx, "r1", "default", true); err != nil {
		t.Fatal(err)
	}
	got := waitEvents(t, h, "r1", 3)
	if got[2] != "started:default" {
		t.Fatalf("events = %v", got)
	}
}

func TestStopService(t *testing.T) {
	h := newFixture()
	WatchEvery = time.Millisecond
	h.addRun(t, "r1")
	ctx := context.Background()
	if _, err := h.c.StartService(ctx, "r1", "default", false); err != nil {
		t.Fatal(err)
	}
	waitEvents(t, h, "r1", 2)
	if _, err := h.c.StopService(ctx, "r1", "default"); err != nil {
		t.Fatal(err)
	}
	got := waitEvents(t, h, "r1", 3)
	if got[len(got)-1] != "stopped:default" {
		t.Fatalf("events = %v", got)
	}
	// Stopping an already-stopped service appends nothing.
	if _, err := h.c.StopService(ctx, "r1", "default"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := h.events(t, "r1"); len(got) != 3 {
		t.Fatalf("a no-op stop appended: %v", got)
	}
}

func TestServiceActionsRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("unknown service", func(t *testing.T) {
		h := newFixture()
		h.addRun(t, "r1")
		if _, err := h.c.StartService(ctx, "r1", "ghost", false); !errors.Is(err, ErrNoSuchService) {
			t.Errorf("Start = %v", err)
		}
		if _, err := h.c.StopService(ctx, "r1", "ghost"); !errors.Is(err, ErrNoSuchService) {
			t.Errorf("Stop = %v", err)
		}
		if _, err := h.c.ServiceLog(ctx, "r1", "ghost", 10); !errors.Is(err, ErrNoSuchService) {
			t.Errorf("Log = %v", err)
		}
	})
	t.Run("unknown run", func(t *testing.T) {
		h := newFixture()
		if _, err := h.c.RunServices(ctx, "nope"); err == nil {
			t.Error("want an error")
		}
		if _, err := h.c.StartService(ctx, "nope", "default", false); err == nil {
			t.Error("want an error")
		}
	})
	t.Run("no workspace", func(t *testing.T) {
		h := newFixture()
		if _, err := h.store.Append(ctx, "r1", &event.RunCreated{ProjectID: fixtureProject}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.c.ServiceLog(ctx, "r1", "default", 10); !errors.Is(err, wsinfo.ErrNoWorkspace) {
			t.Errorf("Log = %v", err)
		}
		if _, err := h.c.StopService(ctx, "r1", "default"); !errors.Is(err, wsinfo.ErrNoWorkspace) {
			t.Errorf("Stop = %v", err)
		}
	})
}

func TestServiceLog(t *testing.T) {
	h := newFixture()
	h.svcs.Log["default"] = "listening on :3000\n"
	h.addRun(t, "r1")
	got, err := h.c.ServiceLog(context.Background(), "r1", "default", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "listening on :3000") {
		t.Fatalf("log = %q", got)
	}
}

func TestRunServicesReportsAWorkspaceReadError(t *testing.T) {
	h := newFixture()
	h.addRun(t, "r1")
	// A provisioner whose exec fails: the states can't be read.
	h.envp.ExecFunc = func(*fake.Env, envport.Command) (envport.ExecResult, error) {
		return envport.ExecResult{}, errors.New("docker gone")
	}
	list, err := h.c.RunServices(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if list.Error == "" {
		t.Error("a failed read must surface as Error")
	}
	for _, s := range list.Services {
		if s.State != "unknown" {
			t.Errorf("service %q state = %q, want unknown", s.Name, s.State)
		}
	}
}

// fakeExposer records exposures and hands back scripted URLs.
type fakeExposer struct {
	urls    map[int]string
	calls   atomic.Int32
	enabled bool
	off     bool
}

func (e *fakeExposer) Expose(_ context.Context, _ string, port int) (string, error) {
	e.calls.Add(1)
	if u, ok := e.urls[port]; ok {
		return u, nil
	}
	return "", errors.New("no such port")
}
func (e *fakeExposer) count() int { return int(e.calls.Load()) }

func TestExposeHelper(t *testing.T) {
	ctx := context.Background()
	svc := &resolution.Service{Name: "app", Ports: []resolution.ServicePort{{Port: 3000, Path: "/app"}, {Port: 9999}}}
	log := slog.New(slog.DiscardHandler)
	t.Run("nil exposer", func(t *testing.T) {
		if got := Expose(ctx, nil, "r1", svc, log); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("maps ports to preview links, skipping failures", func(t *testing.T) {
		exp := &fakeExposer{urls: map[int]string{3000: "http://host:40000/"}}
		got := Expose(ctx, exp, "r1", svc, log)
		if got[3000] != "http://host:40000/app" {
			t.Errorf("3000 = %q", got[3000])
		}
		if _, ok := got[9999]; ok {
			t.Errorf("a failed port got a URL: %v", got)
		}
	})
	t.Run("an exposer that is off is not asked", func(t *testing.T) {
		exp := &offExposer{}
		if got := Expose(ctx, exp, "r1", svc, log); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
		if exp.asked {
			t.Error("an off exposer must not be asked")
		}
	})
}

type offExposer struct{ asked bool }

func (e *offExposer) Enabled() bool { return false }
func (e *offExposer) Expose(context.Context, string, int) (string, error) {
	e.asked = true
	return "", nil
}

func TestPreviewURL(t *testing.T) {
	for _, c := range []struct{ base, path, want string }{
		{"http://h:3000", "", "http://h:3000"},
		{"http://h:3000", "/", "http://h:3000"},
		{"http://h:3000/", "/app", "http://h:3000/app"},
		{"http://h:3000", "/app", "http://h:3000/app"},
	} {
		if got := previewURL(c.base, c.path); got != c.want {
			t.Errorf("previewURL(%q,%q) = %q, want %q", c.base, c.path, got, c.want)
		}
	}
}

func TestLive(t *testing.T) {
	for phase, want := range map[string]bool{
		PhaseStarting: true, PhaseHealthy: true, PhaseUnhealthy: true, PhaseStopping: true,
		PhaseStopped: false, PhaseExited: false, "": false,
	} {
		if got := Live(phase); got != want {
			t.Errorf("Live(%q) = %v", phase, got)
		}
	}
}

func TestRequest(t *testing.T) {
	svc := &resolution.Service{Name: "app", Start: "run", Stop: "halt",
		Health: resolution.ServiceHealth{HTTP: "http://localhost:3000", Timeout: 2 * time.Minute, Interval: 5 * time.Second}}
	// The service's own timeout when none is given...
	req := Request(svc, 0)
	if req.Timeout != "2m0s" || req.Interval != "5s" || req.Health.HTTP != "http://localhost:3000" {
		t.Fatalf("req = %+v", req)
	}
	// ...and an explicit one overrides it.
	if got := Request(svc, time.Minute).Timeout; got != "1m0s" {
		t.Errorf("timeout = %q", got)
	}
	// No timeout and no service default: the field stays empty.
	bare := Request(&resolution.Service{Name: "x"}, 0)
	if bare.Timeout != "" || bare.Interval != "" {
		t.Errorf("bare = %+v", bare)
	}
}

func TestLastJSON(t *testing.T) {
	var st sidecar.ServiceState
	if !lastJSON("warning: something\n{\"name\":\"app\",\"phase\":\"healthy\"}\n", &st) || st.Name != "app" {
		t.Fatalf("st = %+v", st)
	}
	// The LAST JSON line wins, after any shell noise.
	if !lastJSON(`{"name":"old"}`+"\n"+`{"name":"new","phase":"x"}`, &st) || st.Name != "new" {
		t.Fatalf("st = %+v", st)
	}
	if lastJSON("no json here\njust text", &st) {
		t.Error("plain text must not decode")
	}
	var arr []int
	if !lastJSON("[1,2,3]", &arr) || len(arr) != 3 {
		t.Errorf("arr = %v", arr)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "x", "y"); got != "x" {
		t.Errorf("got %q", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("got %q", got)
	}
}

// Shim surfaces a non-zero exit as an error with the output tail.
func TestShimReportsFailure(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	id, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r1"})
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		return envport.ExecResult{ExitCode: 2, Output: "shim: boom"}, nil
	}
	_, err := Shim(ctx, h.envp, id, "start", nil, ShimRequest{Name: "x"}, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	// An exec that errors outright surfaces too.
	h.envp.ExecFunc = func(*fake.Env, envport.Command) (envport.ExecResult, error) {
		return envport.ExecResult{}, errors.New("docker gone")
	}
	if _, err := Shim(ctx, h.envp, id, "stop", nil, ShimRequest{Name: "x"}, time.Minute); err == nil {
		t.Fatal("want an error")
	}
}

// States reads through the sidecar when the provisioner has one.
func TestStatesViaSidecar(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/services" {
			_, _ = io.WriteString(w, `[{"name":"app","phase":"healthy"}]`)
			return
		}
		_, _ = io.WriteString(w, `log line`)
	}))
	defer srv.Close()
	envp := &sidecarEnvp{EnvProvisioner: fake.NewEnvProvisioner(), addr: strings.TrimPrefix(srv.URL, "http://")}
	id, _ := envp.Provision(ctx, envport.EnvSpec{RunID: "r1"})

	got, err := States(ctx, envp, id)
	if err != nil || len(got) != 1 || got[0].Phase != "healthy" {
		t.Fatalf("States = %+v, %v", got, err)
	}
	st, err := State(ctx, envp, id, "app")
	if err != nil || st.Phase != "healthy" {
		t.Fatalf("State = %+v, %v", st, err)
	}
	// A name the workspace does not have reads stopped.
	if st, _ := State(ctx, envp, id, "other"); st.Phase != PhaseStopped {
		t.Errorf("unknown service = %+v", st)
	}
	log, err := Log(ctx, envp, id, "app", 0)
	if err != nil || log != "log line" {
		t.Fatalf("Log = %q, %v", log, err)
	}
}

// sidecarEnvp is a fake that also serves the sidecar over loopback.
type sidecarEnvp struct {
	*fake.EnvProvisioner
	addr string
}

func (e *sidecarEnvp) Sidecar(context.Context, envport.EnvID) (envport.PortConn, error) {
	return dialPortConn{e.addr}, nil
}

type dialPortConn struct{ addr string }

func (d dialPortConn) Dial(ctx context.Context) (net.Conn, error) {
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", d.addr)
}
func (d dialPortConn) Close() error { return nil }

// States falls back to Exec when the sidecar read fails.
func TestStatesFallsBackToExec(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	id, _ := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r1"})
	// The default fake has no Sidecar capability, so States goes straight to
	// Exec, which the svcs supervisor answers.
	if _, err := h.c.StartService(ctx, "r1", "default", false); err != nil {
		// no run yet; just drive States/Log directly
	}
	got, err := States(ctx, h.envp, id)
	if err != nil {
		t.Fatal(err)
	}
	_ = got
	if _, err := Log(ctx, h.envp, id, "default", 5); err != nil {
		t.Fatal(err)
	}
	if got := h.svcs.Snapshot(); len(got) == 0 {
		t.Error("the fallback never execed the shim")
	}
}
