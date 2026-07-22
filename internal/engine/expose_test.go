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
