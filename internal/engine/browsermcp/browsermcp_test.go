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
	"apothecary/internal/ports/browserport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// dialer is a browser whose session dials the workspace's port 3000 thrice.
type dialer struct{ session browserport.BrowserSession }

func (b *dialer) Session(ctx context.Context, s browserport.BrowserSession, _ io.ReadWriter) error {
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
func (b *dialer) Status(context.Context, string) (browserport.BrowserStatus, error) {
	return browserport.BrowserStatus{}, nil
}

// exposer counts exposures; each one dials an in-memory pipe.
type exposer struct {
	*fake.EnvProvisioner
	exposed atomic.Int32
}

func (x *exposer) ExposePort(context.Context, envport.EnvID, int) (envport.PortConn, error) {
	x.exposed.Add(1)
	return pipeConn{}, nil
}
func (x *exposer) AppPorts(context.Context, envport.EnvID) []int { return nil }

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

// failDial is an exposure that stopped dialling (its container was rebuilt).
type failDial struct {
	closed *atomic.Int32
}

func (f failDial) Dial(context.Context) (net.Conn, error) { return nil, errors.New("relay gone") }
func (f failDial) Close() error {
	f.closed.Add(1)
	return nil
}

// A cached exposure that no longer dials is replaced, once, and the dead one
// is closed: its relay must not leak.
func TestDeadExposureIsReplacedAndClosed(t *testing.T) {
	ctx := context.Background()
	var closed atomic.Int32
	c := &portCache{conns: map[string]envport.PortConn{}}
	c.conns["r1\x003000"] = failDial{closed: &closed}
	var exposed int
	conn, err := c.dial(ctx, "r1", 3000, func(context.Context) (envport.PortConn, error) {
		exposed++
		return pipeConn{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if exposed != 1 {
		t.Errorf("exposed %d times", exposed)
	}
	if closed.Load() != 1 {
		t.Errorf("the dead exposure was closed %d times", closed.Load())
	}
	if _, ok := c.conns["r1\x003000"].(pipeConn); !ok {
		t.Errorf("cache holds %T", c.conns["r1\x003000"])
	}
}

func TestExposureFailureReachesTheBrowser(t *testing.T) {
	c := &portCache{conns: map[string]envport.PortConn{}}
	_, err := c.dial(context.Background(), "r1", 80, func(context.Context) (envport.PortConn, error) {
		return nil, errors.New("port closed")
	})
	if err == nil || !strings.Contains(err.Error(), "port closed") {
		t.Errorf("err = %v", err)
	}
	if len(c.conns) != 0 {
		t.Errorf("a failed exposure was cached: %v", c.conns)
	}
}

// Forget drops one run's exposures, not another's. A cache never built is a
// no-op.
func TestForgetIsScopedToTheRun(t *testing.T) {
	var closedA, closedB atomic.Int32
	c := &portCache{conns: map[string]envport.PortConn{
		"r1\x0080":   failDial{closed: &closedA},
		"r10\x0080":  failDial{closed: &closedB},
		"r1\x003000": failDial{closed: &closedA},
	}}
	c.forget("r1")
	if closedA.Load() != 2 || closedB.Load() != 0 {
		t.Errorf("closed r1: %d, r10: %d", closedA.Load(), closedB.Load())
	}
	if len(c.conns) != 1 {
		t.Errorf("left = %v", c.conns)
	}
	var none *portCache
	none.forget("r1")
}

// resolving counts the resolutions it hands out, against those released.
type resolving struct {
	res    resolution.Resolution
	err    error
	opened int
	closed int
}

func (r *resolving) Resolve(context.Context, string) (*resolution.Resolution, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.opened++
	res := r.res
	res.Release = func() { r.closed++ }
	return &res, nil
}

func TestOpenRefusesAndReleases(t *testing.T) {
	ctx := context.Background()
	store := event.NewMemStore()
	if _, err := store.Append(ctx, "r1", &event.RunCreated{ProjectID: "p1"}, &event.EnvReady{EnvID: "env1"}); err != nil {
		t.Fatal(err)
	}
	runs := &run.Reader{Store: store}

	t.Run("unknown run", func(t *testing.T) {
		if _, err := New(runs, &resolving{}, &dialer{}).Open(ctx, "ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("project will not resolve", func(t *testing.T) {
		r := &resolving{err: errors.New("vault sealed")}
		if _, err := New(runs, r, &dialer{}).Open(ctx, "r1"); err == nil || !strings.Contains(err.Error(), "vault sealed") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("provisioner that cannot reach ports", func(t *testing.T) {
		r := &resolving{res: resolution.Resolution{Browser: true, Envp: fake.NewEnvProvisioner()}}
		_, err := New(runs, r, &dialer{}).Open(ctx, "r1")
		if err == nil || !strings.Contains(err.Error(), "cannot reach the workspace's ports") {
			t.Errorf("err = %v", err)
		}
		if r.opened != 1 || r.closed != 1 {
			t.Errorf("opened %d, closed %d", r.opened, r.closed)
		}
	})
	t.Run("a session never served is given up", func(t *testing.T) {
		x := &exposer{EnvProvisioner: fake.NewEnvProvisioner()}
		r := &resolving{res: resolution.Resolution{Browser: true, Envp: x}}
		sess, err := New(runs, r, &dialer{}).Open(ctx, "r1")
		if err != nil {
			t.Fatal(err)
		}
		sess.Close()
		sess.Close() // twice: the resolution is released once
		if r.closed != 1 {
			t.Errorf("released %d times", r.closed)
		}
	})
}

type statusBrowser struct {
	dialer
	got string
}

func (b *statusBrowser) Status(_ context.Context, project string) (browserport.BrowserStatus, error) {
	b.got = project
	return browserport.BrowserStatus{}, errors.New("not running")
}

func TestStatus(t *testing.T) {
	ctx := context.Background()
	if _, err := New(nil, nil, nil).Status(ctx, "p1"); !errors.Is(err, ErrOff) {
		t.Errorf("no browser: %v", err)
	}
	b := &statusBrowser{}
	if _, err := New(nil, nil, b).Status(ctx, "p1"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("err = %v", err)
	}
	if b.got != "p1" {
		t.Errorf("asked about %q", b.got)
	}
}
