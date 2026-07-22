package portrelay

import (
	"context"
	"io"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/ports"
)

// echoConn is a PortConn backed by a real TCP echo listener.
type echoConn struct {
	addr   string
	closed atomic.Bool
}

func newEchoConn(t *testing.T) *echoConn {
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
			go func(c net.Conn) {
				defer c.Close()
				data, _ := io.ReadAll(c)
				_, _ = c.Write(append([]byte("pong-"), data...))
			}(c)
		}
	}()
	return &echoConn{addr: ln.Addr().String()}
}

func (e *echoConn) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", e.addr)
}
func (e *echoConn) Close() error { e.closed.Store(true); return nil }

func dialURL(t *testing.T, u string) net.Conn {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExposePumpsAndIsIdempotent(t *testing.T) {
	m := New("127.0.0.1")
	defer m.CloseAll()
	pc := newEchoConn(t)
	u, err := m.Expose("run1", 3000, pc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Errorf("url = %q", u)
	}

	c := dialURL(t, u)
	_, _ = c.Write([]byte("ping"))
	if cw, ok := c.(*net.TCPConn); ok {
		_ = cw.CloseWrite()
	}
	got, _ := io.ReadAll(c)
	c.Close()
	if string(got) != "pong-ping" {
		t.Errorf("round trip = %q", got)
	}

	// Second expose of the same port: same URL, redundant PortConn closed.
	pc2 := newEchoConn(t)
	u2, err := m.Expose("run1", 3000, pc2)
	if err != nil || u2 != u {
		t.Fatalf("idempotent expose = %q, %v", u2, err)
	}
	if !pc2.closed.Load() {
		t.Error("redundant PortConn must be closed")
	}
	if got := m.Existing("run1", 3000); got != u {
		t.Errorf("Existing = %q", got)
	}
	if list := m.List("run1"); len(list) != 1 || list[0].Port != 3000 || list[0].URL != u {
		t.Errorf("List = %+v", list)
	}
}

func TestCloseRunTearsDownListenersAndConns(t *testing.T) {
	m := New("127.0.0.1")
	pc := newEchoConn(t)
	u, err := m.Expose("run1", 8000, pc)
	if err != nil {
		t.Fatal(err)
	}
	m.CloseRun("run1")
	if !pc.closed.Load() {
		t.Error("PortConn not closed")
	}
	parsed, _ := url.Parse(u)
	if _, err := net.Dial("tcp", parsed.Host); err == nil {
		t.Error("listener still accepting after CloseRun")
	}
	if len(m.List("run1")) != 0 {
		t.Error("exposures survived CloseRun")
	}
}

var _ ports.PortConn = (*echoConn)(nil)
