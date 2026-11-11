package httpapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/platform/wstunnel"
	"apothecary/internal/ports/envport"
)

// portEnv reaches workspace ports at target (an engine-side stand-in).
type portEnv struct {
	*fake.EnvProvisioner
	target string
	err    error
}

func (p portEnv) ExposePort(context.Context, envport.EnvID, int) (envport.PortConn, error) {
	return dialTCP(p.target), p.err
}
func (portEnv) AppPorts(context.Context, envport.EnvID) []int { return nil }

func (x *env) tunnel(t *testing.T, path string, hdr http.Header) (net.Conn, error) {
	t.Helper()
	if hdr == nil {
		hdr = http.Header{}
	}
	if hdr.Get("Authorization") == "" {
		hdr.Set("Authorization", "Bearer "+x.token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return wstunnel.Dial(ctx, "ws"+strings.TrimPrefix(x.ts.URL, "http")+path, hdr)
}

func status(err error) int {
	var he *wstunnel.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// A workspace port reaches the caller's machine through the tunnel, with
// no relay and nothing published; every refusal is an HTTP answer.
func TestPortTunnel(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	app, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	go func() {
		c, err := app.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4)
		_, _ = io.ReadFull(c, buf)
		_, _ = c.Write([]byte("pong"))
	}()
	x.res.Envp = portEnv{EnvProvisioner: x.envp, target: app.Addr().String()}

	c, err := x.tunnel(t, "/api/runs/r1/ports/5432/tunnel", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "pong" {
		t.Fatalf("read %q, %v", buf, err)
	}
	c.Close()

	for name, tc := range map[string]struct {
		path string
		hdr  http.Header
		want int
	}{
		"not a port":        {"/api/runs/r1/ports/http/tunnel", nil, http.StatusBadRequest},
		"out of range":      {"/api/runs/r1/ports/70000/tunnel", nil, http.StatusBadRequest},
		"unknown run":       {"/api/runs/nope/ports/5432/tunnel", nil, http.StatusNotFound},
		"another site":      {"/api/runs/r1/ports/5432/tunnel", http.Header{"Origin": {"https://evil.example"}}, http.StatusForbidden},
		"signed out":        {"/api/runs/r1/ports/5432/tunnel", http.Header{"Authorization": {"Bearer nope"}}, http.StatusUnauthorized},
		"nothing listening": {"/api/runs/r1/ports/1/tunnel", nil, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "nothing listening" {
				x.res.Envp = portEnv{EnvProvisioner: x.envp, target: "127.0.0.1:1"}
				tc.want = http.StatusBadGateway
			}
			if _, err := x.tunnel(t, tc.path, tc.hdr); status(err) != tc.want {
				t.Errorf("err = %v, want %d", err, tc.want)
			}
		})
	}
	x.srv.Ports = nil
	if _, err := x.tunnel(t, "/api/runs/r1/ports/5432/tunnel", nil); status(err) != http.StatusServiceUnavailable {
		t.Errorf("no ports service = %v", err)
	}
}

// The SSH tunnel hands the stream to the door for the named workspace,
// saying who opened it.
func TestSSHTunnel(t *testing.T) {
	x := newEnv(t, engine.Config{})
	if _, err := x.tunnel(t, "/api/ssh/tunnel?workspace=apo-w", nil); status(err) != http.StatusServiceUnavailable {
		t.Errorf("no door = %v", err)
	}
	door := &stubDoor{}
	x.srv.SSH = door
	if _, err := x.tunnel(t, "/api/ssh/tunnel", nil); status(err) != http.StatusBadRequest {
		t.Errorf("no workspace = %v", err)
	}
	if _, err := x.tunnel(t, "/api/ssh/tunnel?workspace=apo-w", http.Header{"Authorization": {"Bearer nope"}}); status(err) != http.StatusUnauthorized {
		t.Errorf("signed out = %v", err)
	}
	c, err := x.tunnel(t, "/api/ssh/tunnel?workspace=apo-w", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	banner, _ := io.ReadAll(c)
	if !strings.HasPrefix(string(banner), "SSH-2.0-stub") {
		t.Errorf("banner = %q", banner)
	}
	if len(door.served) != 1 || door.served[0] != "apo-w user:"+testUser {
		t.Errorf("served = %v", door.served)
	}
}
