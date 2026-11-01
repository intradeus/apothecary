package runports

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/portrelay"
)

const fixtureProject = "p1"

// fixture is a store, a fake provisioner and a service over a resolution
// the test can swap the provisioner of. No relay: a test sets one.
type fixture struct {
	store *event.MemStore
	envp  *fake.EnvProvisioner
	res   *resolution.Resolution
	s     *Service
}

func newFixture() *fixture {
	h := &fixture{store: event.NewMemStore(), envp: fake.NewEnvProvisioner()}
	h.res = &resolution.Resolution{ProjectID: fixtureProject, Envp: h.envp}
	h.s = New(&run.Reader{Store: h.store}, resolution.StaticResolver{R: h.res}, nil, Config{}, nil)
	return h
}

// exposerEnvp adds the WorkspacePortExposer capability to the fake.
type exposerEnvp struct {
	*fake.EnvProvisioner
	appPorts []int
	target   string // engine-side echo listener standing in for the workspace app
}

func (x *exposerEnvp) AppPorts(context.Context, envport.EnvID) []int { return x.appPorts }
func (x *exposerEnvp) ExposePort(context.Context, envport.EnvID, int) (envport.PortConn, error) {
	return tcpPortConn{addr: x.target}, nil
}

type tcpPortConn struct{ addr string }

func (t tcpPortConn) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", t.addr)
}
func (t tcpPortConn) Close() error { return nil }

// engFakeStore is a portrelay.Store for engine tests.
type engFakeStore struct{ rows map[string]map[int]int }

func (s engFakeStore) SaveExposedPort(_ context.Context, r string, ws, h int) error {
	if s.rows[r] == nil {
		s.rows[r] = map[int]int{}
	}
	s.rows[r][ws] = h
	return nil
}
func (s engFakeStore) DeleteExposedPorts(_ context.Context, r string) error {
	delete(s.rows, r)
	return nil
}
func (s engFakeStore) LoadExposedPorts(context.Context) (map[string]map[int]int, error) {
	return s.rows, nil
}

// scannerEnvp adds the SiblingPortScanner capability to the fake.
type scannerEnvp struct {
	*fake.EnvProvisioner
	siblings []envport.ObservedPort
}

func (s *scannerEnvp) ScanSiblingPorts(context.Context, envport.EnvID) []envport.ObservedPort {
	return s.siblings
}

func TestObservedPortsRoundTripAndTTL(t *testing.T) {
	h := newFixture()
	clk := time.Unix(1000, 0)
	h.s.Clock = func() time.Time { return clk }

	h.s.ReportObservedPorts("r1", []envport.ObservedPort{{Port: 3000, Process: "node"}})
	got := h.s.ObservedPorts("r1")
	if len(got) != 1 || got[0].Port != 3000 || got[0].Process != "node" {
		t.Fatalf("observed = %+v, want one {3000 node}", got)
	}

	// Past the TTL without a fresh heartbeat, the snapshot is considered dead.
	clk = clk.Add(observedTTL + time.Second)
	if got := h.s.ObservedPorts("r1"); got != nil {
		t.Errorf("stale snapshot must return nil, got %+v", got)
	}

	// forgetObserved drops it entirely (env destroyed).
	clk = time.Unix(1000, 0)
	h.s.ReportObservedPorts("r1", []envport.ObservedPort{{Port: 3000}})
	h.s.forgetObserved("r1")
	if got := h.s.ObservedPorts("r1"); got != nil {
		t.Errorf("after forget, observed = %+v, want nil", got)
	}
}

func TestObservedPortsMergesSiblingScan(t *testing.T) {
	h := newFixture()
	h.res.Envp = &scannerEnvp{
		EnvProvisioner: h.envp,
		siblings:       []envport.ObservedPort{{Port: 5432, Service: "db"}, {Port: 3000, Service: "db"}},
	}
	ctx := context.Background()
	if _, err := h.store.Append(ctx, "r1",
		&event.RunCreated{ProjectID: fixtureProject, FlowName: "loop"},
		&event.EnvReady{EnvID: "env1"},
	); err != nil {
		t.Fatal(err)
	}

	// A shim report establishes liveness (drives the TTL); the workspace's own
	// :3000 must win over the sibling's :3000 on the merge.
	h.s.ReportObservedPorts("r1", []envport.ObservedPort{{Port: 3000, Process: "vite"}})
	h.s.refreshSiblingPorts("r1") // synchronous (bypass the async heartbeat trigger)

	got := h.s.ObservedPorts("r1")
	if len(got) != 2 {
		t.Fatalf("merged observed = %+v, want 3000 + 5432", got)
	}
	if got[0].Port != 3000 || got[0].Process != "vite" || got[0].Service != "" {
		t.Errorf("port 3000 should be the workspace's own (vite), got %+v", got[0])
	}
	if got[1].Port != 5432 || got[1].Service != "db" {
		t.Errorf("port 5432 should be sibling db, got %+v", got[1])
	}
}

func TestAutoExposeObservedCapAndDedupe(t *testing.T) {
	h := newFixture()
	h.s.Config.AutoExpose = true
	h.s.Config.MaxAuto = 2

	// A workspace "app" the fake PortConn dials, so ExposeRunPort succeeds.
	app, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	go func() {
		for {
			c, err := app.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(io.Discard, c) }(c)
		}
	}()
	h.res.Envp = &exposerEnvp{EnvProvisioner: h.envp, target: app.Addr().String()}

	ctx := context.Background()
	if _, err := h.store.Append(ctx, "r1",
		&event.RunCreated{ProjectID: fixtureProject, FlowName: "loop"},
		&event.EnvReady{EnvID: "env1"},
	); err != nil {
		t.Fatal(err)
	}
	relay := portrelay.New("127.0.0.1")
	defer relay.CloseAll()
	h.s.Relay = relay

	// Three discovered ports, cap of two.
	h.s.observed["r1"] = &observedRun{
		shim:        []envport.ObservedPort{{Port: 3000}, {Port: 3001}, {Port: 3002}},
		at:          h.s.now(),
		autoExposed: map[int]bool{},
	}
	h.s.autoExposeObserved("r1")
	if n := len(relay.List("r1")); n != 2 {
		t.Fatalf("auto-exposed %d ports, want cap of 2", n)
	}

	// A second pass exposes nothing new (mark-once + already-Existing).
	h.s.autoExposeObserved("r1")
	if n := len(relay.List("r1")); n != 2 {
		t.Errorf("second pass changed exposures to %d, want stable 2", n)
	}
}

// TestAutoExposeReExposesSavedManualPorts: on recovery, AutoExpose must
// re-create not just declared forwardPorts but also ports a run had exposed
// manually before the restart (which used to be lost entirely).
func TestAutoExposeReExposesSavedManualPorts(t *testing.T) {
	h := newFixture()
	h.res.Envp = &exposerEnvp{EnvProvisioner: h.envp, appPorts: []int{3000}}

	// The store remembers a MANUAL port (9999) that is NOT a declared port.
	store := engFakeStore{rows: map[string]map[int]int{"r1": {9999: 0}}}
	relay := portrelay.New("127.0.0.1")
	if err := relay.Persist(store); err != nil {
		t.Fatal(err)
	}
	defer relay.CloseAll()
	h.s.Relay = relay

	h.s.AutoExpose(context.Background(), "r1", h.res.Envp, "env1")

	exposed := map[int]bool{}
	for _, e := range relay.List("r1") {
		exposed[e.Port] = true
	}
	if !exposed[3000] {
		t.Error("declared port 3000 not exposed")
	}
	if !exposed[9999] {
		t.Error("saved manual port 9999 not re-exposed on recovery (regression)")
	}
}

func TestExpose(t *testing.T) {
	h := newFixture()
	ctx := context.Background()

	// The "workspace app": an echo listener the fake PortConn dials.
	app, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	go func() {
		for {
			c, err := app.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				data, _ := io.ReadAll(c)
				_, _ = c.Write(append([]byte("app-"), data...))
			}(c)
		}
	}()
	h.res.Envp = &exposerEnvp{EnvProvisioner: h.envp, target: app.Addr().String()}

	// A run with an environment, written directly (no live goroutine — the
	// expose path reads events only).
	if _, err := h.store.Append(ctx, "r1",
		&event.RunCreated{ProjectID: fixtureProject, FlowName: "loop"},
		&event.EnvReady{EnvID: "env1"},
	); err != nil {
		t.Fatal(err)
	}

	// No relay configured: honest 503-shaped error.
	if _, err := h.s.Expose(ctx, "r1", 3000); !errors.Is(err, ErrPortsOff) {
		t.Fatalf("want ErrPortsOff, got %v", err)
	}

	relay := portrelay.New("127.0.0.1")
	defer relay.CloseAll()
	h.s.Relay = relay

	u, err := h.s.Expose(ctx, "r1", 3000)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("ping"))
	_ = c.(*net.TCPConn).CloseWrite()
	got, _ := io.ReadAll(c)
	c.Close()
	if string(got) != "app-ping" {
		t.Errorf("round trip through relay = %q", got)
	}

	// Idempotent; listed; torn down with the env.
	if u2, err := h.s.Expose(ctx, "r1", 3000); err != nil || u2 != u {
		t.Errorf("second expose = %q, %v", u2, err)
	}
	if l := h.s.List("r1"); len(l) != 1 || l[0].Port != 3000 {
		t.Errorf("List = %+v", l)
	}
}

// editorEnvp adds EditorStarter + WorkspacePortExposer to the fake.
type editorEnvp struct {
	*fake.EnvProvisioner
	target       string
	startErr     error
	starts       int
	lastSettings envport.EditorSettings
}

func (e *editorEnvp) StartEditor(_ context.Context, _ envport.EnvID, _ int, s envport.EditorSettings) error {
	e.starts++
	e.lastSettings = s
	return e.startErr
}
func (e *editorEnvp) EditorExtensions(context.Context, envport.EnvID, envport.EditorSettings) []string {
	return nil
}
func (e *editorEnvp) AppPorts(context.Context, envport.EnvID) []int { return nil }
func (e *editorEnvp) ExposePort(context.Context, envport.EnvID, int) (envport.PortConn, error) {
	return tcpPortConn{addr: e.target}, nil
}

func addRun(t *testing.T, h *fixture, runID string) {
	t.Helper()
	if _, err := h.store.Append(context.Background(), runID,
		&event.RunCreated{ProjectID: fixtureProject, FlowName: "loop"},
		&event.EnvReady{EnvID: "env-" + runID},
	); err != nil {
		t.Fatal(err)
	}
}

func TestOpenEditor(t *testing.T) {
	ctx := context.Background()
	t.Run("no relay is ports-off", func(t *testing.T) {
		h := newFixture()
		if _, err := h.s.OpenEditor(ctx, "r1"); !errors.Is(err, ErrPortsOff) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("starts, exposes, and is idempotent", func(t *testing.T) {
		h := newFixture()
		h.s.Relay = portrelay.New("127.0.0.1")
		defer h.s.Relay.CloseAll()
		ed := &editorEnvp{EnvProvisioner: h.envp, target: echoListener(t)}
		h.res.Envp = ed
		addRun(t, h, "r1")

		u, err := h.s.OpenEditor(ctx, "r1")
		if err != nil {
			t.Fatal(err)
		}
		if u == "" || ed.starts != 1 {
			t.Fatalf("url = %q, starts = %d", u, ed.starts)
		}
		// Second open reuses the URL but STILL restarts code-server (a crash
		// must not hand out a dead editor).
		u2, err := h.s.OpenEditor(ctx, "r1")
		if err != nil || u2 != u {
			t.Fatalf("second open = %q, %v", u2, err)
		}
		if ed.starts != 2 {
			t.Errorf("starts = %d, want a restart on every open", ed.starts)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		h := newFixture()
		h.s.Relay = portrelay.New("127.0.0.1")
		defer h.s.Relay.CloseAll()
		if _, err := h.s.OpenEditor(ctx, "nope"); err == nil {
			t.Error("unknown run")
		}
		if _, err := h.store.Append(ctx, "no-env", &event.RunCreated{ProjectID: fixtureProject}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.s.OpenEditor(ctx, "no-env"); err == nil {
			t.Error("a run with no env must error")
		}
		addRun(t, h, "r1")
		h.s.Resolver = resolution.StaticResolver{}
		if _, err := h.s.OpenEditor(ctx, "r1"); err == nil {
			t.Error("unresolvable project")
		}
	})
	t.Run("provisioner without the editor", func(t *testing.T) {
		h := newFixture()
		h.s.Relay = portrelay.New("127.0.0.1")
		defer h.s.Relay.CloseAll()
		addRun(t, h, "r1") // the plain fake has no EditorStarter
		if _, err := h.s.OpenEditor(ctx, "r1"); err == nil || !strings.Contains(err.Error(), "does not support the editor") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a start failure surfaces", func(t *testing.T) {
		h := newFixture()
		h.s.Relay = portrelay.New("127.0.0.1")
		defer h.s.Relay.CloseAll()
		h.res.Envp = &editorEnvp{EnvProvisioner: h.envp, startErr: errors.New("no code-server")}
		addRun(t, h, "r1")
		if _, err := h.s.OpenEditor(ctx, "r1"); err == nil || !strings.Contains(err.Error(), "no code-server") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestExposeRefusals(t *testing.T) {
	ctx := context.Background()
	h := newFixture()
	h.s.Relay = portrelay.New("127.0.0.1")
	defer h.s.Relay.CloseAll()
	if _, err := h.s.Expose(ctx, "nope", 3000); err == nil {
		t.Error("unknown run")
	}
	if _, err := h.store.Append(ctx, "no-env", &event.RunCreated{ProjectID: fixtureProject}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Expose(ctx, "no-env", 3000); err == nil {
		t.Error("no env")
	}
	addRun(t, h, "plain") // the fake has no WorkspacePortExposer
	if _, err := h.s.Expose(ctx, "plain", 3000); err == nil || !strings.Contains(err.Error(), "does not support port exposure") {
		t.Errorf("err = %v", err)
	}
	h.s.Resolver = resolution.StaticResolver{}
	if _, err := h.s.Expose(ctx, "plain", 3000); err == nil {
		t.Error("unresolvable")
	}
}

func TestEnabledAndListWithoutRelay(t *testing.T) {
	h := newFixture()
	if h.s.Enabled() {
		t.Error("no relay: not enabled")
	}
	if h.s.List("r1") != nil {
		t.Error("no relay: nil list")
	}
	h.s.Relay = portrelay.New("127.0.0.1")
	defer h.s.Relay.CloseAll()
	if !h.s.Enabled() {
		t.Error("a relay means enabled")
	}
}

func TestReopenAfterRebuildBringsBackEditorAndPorts(t *testing.T) {
	ctx := context.Background()
	h := newFixture()
	h.s.Relay = portrelay.New("127.0.0.1")
	defer h.s.Relay.CloseAll()
	ed := &editorEnvp{EnvProvisioner: h.envp, target: echoListener(t)}
	h.res.Envp = ed
	addRun(t, h, "r1")

	// Open the editor so it is in the saved set, then rebuild.
	if _, err := h.s.OpenEditor(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	before := ed.starts
	h.s.ReopenAfterRebuild(ctx, "r1", ed, envport.EnvID("env-r1"), envport.EditorSettings{})
	if ed.starts <= before {
		t.Error("an editor left open must be restarted after a rebuild")
	}
	// A run that never opened the editor: no restart.
	ed2 := &editorEnvp{EnvProvisioner: h.envp, target: echoListener(t)}
	addRun(t, h, "r2")
	h.s.ReopenAfterRebuild(ctx, "r2", ed2, envport.EnvID("env-r2"), envport.EditorSettings{})
	if ed2.starts != 0 {
		t.Errorf("r2 editor started %d times, want 0", ed2.starts)
	}
	// A provisioner without exposure: ReopenAfterRebuild is a no-op, no panic.
	h.s.ReopenAfterRebuild(ctx, "r2", h.envp, envport.EnvID("env-r2"), envport.EditorSettings{})
}

func TestAutoExposeWithoutExposerOrRelay(t *testing.T) {
	ctx := context.Background()
	h := newFixture()
	// No relay: nil.
	if got := h.s.AutoExpose(ctx, "r1", h.envp, "env1"); got != nil {
		t.Errorf("no relay: %v", got)
	}
	h.s.Relay = portrelay.New("127.0.0.1")
	defer h.s.Relay.CloseAll()
	// Provisioner without the exposer capability: nil.
	if got := h.s.AutoExpose(ctx, "r1", h.envp, "env1"); got != nil {
		t.Errorf("no exposer: %v", got)
	}
}

func TestForgetAndBeforeDestroy(t *testing.T) {
	h := newFixture()
	h.s.Relay = portrelay.New("127.0.0.1")
	defer h.s.Relay.CloseAll()
	// Forget with no relay must not panic.
	plain := newFixture()
	plain.s.Forget("r1")
	// With a relay, Forget tears down and clears observed.
	h.s.ReportObservedPorts("r1", []envport.ObservedPort{{Port: 3000}})
	h.s.Forget("r1")
	if got := h.s.ObservedPorts("r1"); got != nil {
		t.Errorf("observed after Forget = %v", got)
	}
	h.s.BeforeDestroy(context.Background(), run.View{}, nil) // no-op
}

func TestConfigMaxAuto(t *testing.T) {
	if (Config{}).maxAuto() != 5 {
		t.Error("default maxAuto")
	}
	if (Config{MaxAuto: 2}).maxAuto() != 2 {
		t.Error("explicit maxAuto")
	}
}

func echoListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	return ln.Addr().String()
}

// resolveScanner returns ok=false for every way a run can't be scanned.
func TestResolveScannerRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("unknown run", func(t *testing.T) {
		h := newFixture()
		if _, _, _, ok := h.s.resolveScanner(ctx, "nope"); ok {
			t.Error("unknown run must not resolve")
		}
	})
	t.Run("no env", func(t *testing.T) {
		h := newFixture()
		if _, err := h.store.Append(ctx, "r1", &event.RunCreated{ProjectID: fixtureProject}); err != nil {
			t.Fatal(err)
		}
		if _, _, _, ok := h.s.resolveScanner(ctx, "r1"); ok {
			t.Error("no env must not resolve")
		}
	})
	t.Run("unresolvable", func(t *testing.T) {
		h := newFixture()
		addRun(t, h, "r1")
		h.s.Resolver = resolution.StaticResolver{}
		if _, _, _, ok := h.s.resolveScanner(ctx, "r1"); ok {
			t.Error("unresolvable project must not resolve")
		}
	})
	t.Run("provisioner without the scanner", func(t *testing.T) {
		h := newFixture()
		addRun(t, h, "r1") // plain fake has no SiblingPortScanner
		if _, _, _, ok := h.s.resolveScanner(ctx, "r1"); ok {
			t.Error("a provisioner without the capability must not resolve")
		}
	})
	t.Run("resolves when the provisioner scans", func(t *testing.T) {
		h := newFixture()
		h.res.Envp = &scannerEnvp{EnvProvisioner: h.envp, siblings: []envport.ObservedPort{{Port: 1}}}
		addRun(t, h, "r1")
		env, scanner, release, ok := h.s.resolveScanner(ctx, "r1")
		if !ok {
			t.Fatal("must resolve")
		}
		defer release()
		if env != "env-r1" || len(scanner.ScanSiblingPorts(ctx, env)) != 1 {
			t.Errorf("env=%q scanner=%v", env, scanner)
		}
	})
}

// A report in auto-expose mode exposes discovered ports up to the cap, once each.
func TestReportObservedPortsAutoExposes(t *testing.T) {
	h := newFixture()
	h.s.Config = Config{AutoExpose: true, MaxAuto: 2}
	h.s.Relay = portrelay.New("127.0.0.1")
	defer h.s.Relay.CloseAll()
	h.res.Envp = &exposerEnvp{EnvProvisioner: h.envp, target: echoListener(t)}
	addRun(t, h, "r1")

	h.s.ReportObservedPorts("r1", []envport.ObservedPort{{Port: 3000}, {Port: 3001}, {Port: 3002}})
	// AutoExpose runs in a goroutine; wait for the cap to be reached.
	deadline := time.Now().Add(10 * time.Second)
	for len(h.s.List("r1")) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("auto-exposed %d, want the cap of 2", len(h.s.List("r1")))
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if got := len(h.s.List("r1")); got != 2 {
		t.Fatalf("auto-exposed %d, want exactly the cap", got)
	}
}

// autoExposeObserved on a run with no snapshot is a no-op.
func TestAutoExposeObservedNoSnapshot(t *testing.T) {
	h := newFixture()
	h.s.Relay = portrelay.New("127.0.0.1")
	defer h.s.Relay.CloseAll()
	h.s.autoExposeObserved("ghost") // nothing observed: returns at once
	if got := h.s.List("ghost"); len(got) != 0 {
		t.Errorf("exposed %v", got)
	}
}

func TestNilClockAndLogFallBack(t *testing.T) {
	s := &Service{}
	if s.now().IsZero() || s.log() == nil {
		t.Error("nil clock/log must fall back")
	}
}

// AutoExpose publishes the declared ports and the saved manual ports, each on
// its persisted URL, and dedupes across the two.
func TestAutoExposeDeclaredAndSaved(t *testing.T) {
	ctx := context.Background()
	h := newFixture()
	h.s.Relay = portrelay.New("127.0.0.1")
	defer h.s.Relay.CloseAll()
	ed := &exposerEnvp{EnvProvisioner: h.envp, appPorts: []int{3000, 3000}, target: echoListener(t)}

	links := h.s.AutoExpose(ctx, "r1", ed, envport.EnvID("env1"))
	if len(links) != 1 || links[0].Kind != "app" {
		t.Fatalf("links = %+v, want one deduped app link", links)
	}
	// A second pass returns the same URL (Existing short-circuits).
	again := h.s.AutoExpose(ctx, "r1", ed, envport.EnvID("env1"))
	if len(again) != 1 || again[0].URL != links[0].URL {
		t.Fatalf("second pass = %+v", again)
	}
}

// A port the workspace declares but whose exposure fails is a warning, not a
// failed run: AutoExpose keeps going and returns what it could expose.
func TestAutoExposeToleratesAFailure(t *testing.T) {
	ctx := context.Background()
	h := newFixture()
	h.s.Relay = portrelay.New("127.0.0.1")
	defer h.s.Relay.CloseAll()
	ed := &failExposer{EnvProvisioner: h.envp, appPorts: []int{3000}}
	if links := h.s.AutoExpose(ctx, "r1", ed, envport.EnvID("env1")); len(links) != 0 {
		t.Fatalf("a failing exposer produced links: %+v", links)
	}
}

// failExposer declares ports but refuses to expose them.
type failExposer struct {
	*fake.EnvProvisioner
	appPorts []int
}

func (e *failExposer) AppPorts(context.Context, envport.EnvID) []int { return e.appPorts }
func (e *failExposer) ExposePort(context.Context, envport.EnvID, int) (envport.PortConn, error) {
	return nil, errors.New("cannot expose")
}
