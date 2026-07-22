package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/portrelay"
	"apothecary/internal/ports"
)

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

// TestAutoExposeReExposesSavedManualPorts: on recovery, autoExpose must
// re-create not just declared forwardPorts but also ports a run had exposed
// manually before the restart (which used to be lost entirely).
func TestAutoExposeReExposesSavedManualPorts(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.res.Envp = &exposerEnvp{EnvProvisioner: h.envp, appPorts: []int{3000}}

	// The store remembers a MANUAL port (9999) that is NOT a declared port.
	store := engFakeStore{rows: map[string]map[int]int{"r1": {9999: 0}}}
	relay := portrelay.New("127.0.0.1")
	if err := relay.Persist(store); err != nil {
		t.Fatal(err)
	}
	defer relay.CloseAll()
	h.e.SetPortRelay(relay)

	x := &exec{e: h.e, rs: &runState{runID: "r1"}, res: h.res, ctx: context.Background(), bg: context.Background()}
	x.autoExpose("env1")

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

func TestExposeRunPort(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
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
		&event.RunCreated{ProjectID: testProject, FlowName: "loop"},
		&event.EnvReady{EnvID: "env1"},
	); err != nil {
		t.Fatal(err)
	}

	// No relay configured: honest 503-shaped error.
	if _, err := h.e.ExposeRunPort(ctx, "r1", 3000); !errors.Is(err, ErrPortsOff) {
		t.Fatalf("want ErrPortsOff, got %v", err)
	}

	relay := portrelay.New("127.0.0.1")
	defer relay.CloseAll()
	h.e.SetPortRelay(relay)

	u, err := h.e.ExposeRunPort(ctx, "r1", 3000)
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
	if u2, err := h.e.ExposeRunPort(ctx, "r1", 3000); err != nil || u2 != u {
		t.Errorf("second expose = %q, %v", u2, err)
	}
	if l := h.e.RunPorts("r1"); len(l) != 1 || l[0].Port != 3000 {
		t.Errorf("RunPorts = %+v", l)
	}
}
