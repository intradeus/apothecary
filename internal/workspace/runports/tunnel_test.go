package runports

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run/event"
)

// countingEnvp counts the paths opened to workspace ports; each one dials
// target, and a test can break the ones already handed out.
type countingEnvp struct {
	*exposerEnvp
	mu     sync.Mutex
	opened int
	err    error
	conns  []*breakableConn
}

func (c *countingEnvp) ExposePort(ctx context.Context, id envport.EnvID, port int) (envport.PortConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	c.opened++
	pc := &breakableConn{addr: c.target}
	c.conns = append(c.conns, pc)
	return pc, nil
}

func (c *countingEnvp) breakAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, pc := range c.conns {
		pc.addr = "127.0.0.1:1" // nothing listens: the relay behind it died
	}
}

type breakableConn struct {
	addr   string
	closed bool
}

func (b *breakableConn) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", b.addr)
}
func (b *breakableConn) Close() error { b.closed = true; return nil }

// echoApp is a workspace app answering "app-" + what it read.
func echoApp(t *testing.T) string {
	t.Helper()
	app, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
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
	return app.Addr().String()
}

func roundTrip(t *testing.T, c net.Conn) string {
	t.Helper()
	defer c.Close()
	_, _ = c.Write([]byte("ping"))
	_ = c.(*net.TCPConn).CloseWrite()
	got, _ := io.ReadAll(c)
	return string(got)
}

// A tunnel reaches the port with no relay configured, and every connection
// after the first reuses the path to it instead of opening another.
func TestDialReachesThePortAndKeepsThePath(t *testing.T) {
	ctx := context.Background()
	h := newFixture()
	envp := &countingEnvp{exposerEnvp: &exposerEnvp{EnvProvisioner: h.envp, target: echoApp(t)}}
	h.res.Envp = envp
	addRun(t, h, "r1")
	for range 3 {
		c, err := h.s.Dial(ctx, "r1", 5432)
		if err != nil {
			t.Fatal(err)
		}
		if got := roundTrip(t, c); got != "app-ping" {
			t.Fatalf("round trip = %q", got)
		}
	}
	if envp.opened != 1 {
		t.Errorf("paths opened = %d, want 1", envp.opened)
	}

	// The relay behind the kept path died: the next connection opens a new
	// one, once, and works.
	envp.breakAll()
	envp.target = envp.exposerEnvp.target
	c, err := h.s.Dial(ctx, "r1", 5432)
	if err != nil {
		t.Fatal(err)
	}
	if got := roundTrip(t, c); got != "app-ping" {
		t.Errorf("after reopen = %q", got)
	}
	if envp.opened != 2 || !envp.conns[0].closed {
		t.Errorf("opened %d, first closed %v", envp.opened, envp.conns[0].closed)
	}
}

// Teardown and rebuild forget the kept paths: their relays died with the
// workspace.
func TestForgetAndRebuildDropKeptPaths(t *testing.T) {
	ctx := context.Background()
	h := newFixture()
	envp := &countingEnvp{exposerEnvp: &exposerEnvp{EnvProvisioner: h.envp, target: echoApp(t)}}
	h.res.Envp = envp
	addRun(t, h, "r1")
	c, err := h.s.Dial(ctx, "r1", 3000)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	h.s.Forget("r1")
	if !envp.conns[0].closed || h.s.keptConn("r1", 3000) != nil {
		t.Error("Forget must drop and close the kept path")
	}
	if c, err = h.s.Dial(ctx, "r1", 3000); err != nil {
		t.Fatal(err)
	}
	c.Close()
	h.s.ReopenAfterRebuild(ctx, "r1", envp, "env-r1", envport.EditorSettings{})
	if !envp.conns[1].closed || h.s.keptConn("r1", 3000) != nil {
		t.Error("a rebuild must drop and close the kept path")
	}
}

func TestDialRefusals(t *testing.T) {
	ctx := context.Background()
	h := newFixture()
	envp := &countingEnvp{exposerEnvp: &exposerEnvp{EnvProvisioner: h.envp, target: "127.0.0.1:1"}}
	h.res.Envp = envp
	addRun(t, h, "r1")
	for _, port := range []int{0, 70000} {
		if _, err := h.s.Dial(ctx, "r1", port); err == nil || !strings.Contains(err.Error(), "1-65535") {
			t.Errorf("port %d: %v", port, err)
		}
	}
	if _, err := h.s.Dial(ctx, "nope", 3000); err == nil {
		t.Error("unknown run")
	}
	if _, err := h.store.Append(ctx, "no-env", &event.RunCreated{ProjectID: fixtureProject}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Dial(ctx, "no-env", 3000); err == nil || !strings.Contains(err.Error(), "no workspace") {
		t.Errorf("no env: %v", err)
	}
	// Nothing listens on the port: the error names it, and the path is not kept.
	if _, err := h.s.Dial(ctx, "r1", 3000); err == nil || !strings.Contains(err.Error(), "port 3000") {
		t.Errorf("closed port: %v", err)
	}
	if h.s.keptConn("r1", 3000) != nil {
		t.Error("a path that never answered must not be kept")
	}
	envp.err = errors.New("relay would not start")
	if _, err := h.s.Dial(ctx, "r1", 3000); err == nil || !strings.Contains(err.Error(), "relay would not start") {
		t.Errorf("expose failure: %v", err)
	}
	h.res.Envp = h.envp // no WorkspacePortExposer
	if _, err := h.s.Dial(ctx, "r1", 3000); err == nil || !strings.Contains(err.Error(), "does not reach workspace ports") {
		t.Errorf("no exposer: %v", err)
	}
	h.s.Resolver = resolution.StaticResolver{}
	if _, err := h.s.Dial(ctx, "r1", 3000); err == nil {
		t.Error("unresolvable")
	}
}

// Two connections racing to open the first path keep one; the loser's is
// closed.
func TestOpenConnKeepsTheFirst(t *testing.T) {
	ctx := context.Background()
	h := newFixture()
	envp := &countingEnvp{exposerEnvp: &exposerEnvp{EnvProvisioner: h.envp, target: echoApp(t)}}
	h.res.Envp = envp
	addRun(t, h, "r1")
	first, err := h.s.openConn(ctx, "r1", 3000)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.s.openConn(ctx, "r1", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if second != first || !envp.conns[1].closed {
		t.Errorf("kept %v, loser closed %v", second == first, envp.conns[1].closed)
	}
}
