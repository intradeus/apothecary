package browsermcp

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
)

// dialer is a browser whose session dials the workspace's port 3000 thrice.
type dialer struct{ session ports.BrowserSession }

func (b *dialer) Session(ctx context.Context, s ports.BrowserSession, _ io.ReadWriter) error {
	b.session = s
	for range 3 {
		c, err := s.Dial(ctx, 3000)
		if err != nil {
			return err
		}
		c.Close()
	}
	return nil
}
func (b *dialer) Warm(string, string) {}
func (b *dialer) Status(context.Context, string) (ports.BrowserStatus, error) {
	return ports.BrowserStatus{}, nil
}

// exposer counts exposures; each one dials an in-memory pipe.
type exposer struct {
	*fake.EnvProvisioner
	exposed atomic.Int32
}

func (x *exposer) ExposePort(context.Context, ports.EnvID, int) (ports.PortConn, error) {
	x.exposed.Add(1)
	return pipeConn{}, nil
}
func (x *exposer) AppPorts(context.Context, ports.EnvID) []int { return nil }

type pipeConn struct{}

func (pipeConn) Dial(context.Context) (net.Conn, error) {
	a, b := net.Pipe()
	go b.Close()
	return a, nil
}
func (pipeConn) Close() error { return nil }

// A session reaches the run's workspace ports through one cached exposure,
// and what the service refuses it refuses before serving.
func TestServeReusesOneExposurePerPort(t *testing.T) {
	ctx := context.Background()
	store := event.NewMemStore()
	if _, err := store.Append(ctx, "r1", &event.RunCreated{ProjectID: "p1"}, &event.EnvReady{EnvID: "env1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "r2", &event.RunCreated{ProjectID: "p1"}); err != nil {
		t.Fatal(err)
	}
	x := &exposer{EnvProvisioner: fake.NewEnvProvisioner()}
	res := &resolution.Resolution{ProjectID: "p1", ProjectName: "test", Envp: x, Browser: true}
	runs, resolver := &run.Reader{Store: store}, resolution.StaticResolver{R: res}

	if err := New(runs, resolver, nil).Serve(ctx, "r1", nil); !errors.Is(err, ErrOff) {
		t.Fatalf("no browser: err = %v, want ErrOff", err)
	}
	b := &dialer{}
	s := New(runs, resolver, b)
	if err := s.Serve(ctx, "r2", nil); err == nil || !strings.Contains(err.Error(), "no environment") {
		t.Fatalf("run without a workspace: err = %v", err)
	}
	if err := s.Serve(ctx, "r1", nil); err != nil {
		t.Fatal(err)
	}
	if b.session.Project != "p1" || b.session.Run != "r1" {
		t.Errorf("session = %+v", b.session)
	}
	if n := x.exposed.Load(); n != 1 {
		t.Errorf("port exposed %d times over three dials, want once", n)
	}
	// Forget (the workspace is destroyed) drops the cached exposure.
	s.Forget("r1")
	if err := s.Serve(ctx, "r1", nil); err != nil {
		t.Fatal(err)
	}
	if n := x.exposed.Load(); n != 2 {
		t.Errorf("after forget, exposed %d times, want a fresh exposure", n)
	}
	res.Browser = false
	if err := s.Serve(ctx, "r1", nil); err == nil || !strings.Contains(err.Error(), "browser is off") {
		t.Errorf("browser off: err = %v", err)
	}
}
