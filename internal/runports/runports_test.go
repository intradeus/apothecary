package runports

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/portrelay"
	"apothecary/internal/ports"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
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

func (x *exposerEnvp) AppPorts(context.Context, ports.EnvID) []int { return x.appPorts }
func (x *exposerEnvp) ExposePort(context.Context, ports.EnvID, int) (ports.PortConn, error) {
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
	siblings []ports.ObservedPort
}

func (s *scannerEnvp) ScanSiblingPorts(context.Context, ports.EnvID) []ports.ObservedPort {
	return s.siblings
}

func TestObservedPortsRoundTripAndTTL(t *testing.T) {
	h := newFixture()
	clk := time.Unix(1000, 0)
	h.s.Clock = func() time.Time { return clk }

	h.s.ReportObservedPorts("r1", []ports.ObservedPort{{Port: 3000, Process: "node"}})
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
	h.s.ReportObservedPorts("r1", []ports.ObservedPort{{Port: 3000}})
	h.s.forgetObserved("r1")
	if got := h.s.ObservedPorts("r1"); got != nil {
		t.Errorf("after forget, observed = %+v, want nil", got)
	}
}

func TestObservedPortsMergesSiblingScan(t *testing.T) {
	h := newFixture()
	h.res.Envp = &scannerEnvp{
		EnvProvisioner: h.envp,
		siblings:       []ports.ObservedPort{{Port: 5432, Service: "db"}, {Port: 3000, Service: "db"}},
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
	h.s.ReportObservedPorts("r1", []ports.ObservedPort{{Port: 3000, Process: "vite"}})
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
		shim:        []ports.ObservedPort{{Port: 3000}, {Port: 3001}, {Port: 3002}},
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
