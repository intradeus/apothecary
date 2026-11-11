package wstunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// tunnelTo serves a tunnel whose far end is handle (given the accepted conn).
func tunnelTo(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("refuse") != "" {
			http.Error(w, "not your run", http.StatusForbidden)
			return
		}
		c, err := Accept(w, r)
		if err != nil {
			return
		}
		handle(c)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dial(t *testing.T, url string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, url, http.Header{"Authorization": {"Bearer t"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// A TCP server behind the tunnel, piped exactly as the engine pipes a
// workspace port: what goes in one end comes out of the other, both ways,
// and the far end closing is the near end's EOF.
func TestTunnelCarriesAStreamBothWays(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		_, _ = c.Write(bytes.ToUpper(buf))
		// A reply bigger than one message, then the server hangs up.
		_, _ = c.Write(bytes.Repeat([]byte("x"), 200<<10))
	}()
	url := tunnelTo(t, func(ws net.Conn) {
		target, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			ws.Close()
			return
		}
		Pipe(ws, target)
	})
	c := dial(t, url)
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.HasPrefix(got, []byte("HELLO")) || len(got) != 5+200<<10 {
		t.Errorf("got %d bytes, prefix %q", len(got), got[:min(len(got), 8)])
	}
}

// A refusal before the upgrade reaches the client with its status and the
// engine's words, not as a bare handshake failure.
func TestRefusalCarriesTheEnginesAnswer(t *testing.T) {
	url := tunnelTo(t, func(c net.Conn) { c.Close() })
	_, err := Dial(context.Background(), url+"?refuse=1", nil)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusForbidden || !strings.Contains(he.Error(), "not your run") {
		t.Fatalf("err = %v", err)
	}
	if (&HTTPError{Status: http.StatusBadGateway}).Error() != "Bad Gateway" {
		t.Error("an empty body reads as the status text")
	}
	if _, err := Dial(context.Background(), "ws://127.0.0.1:1/nothing", nil); err == nil {
		t.Error("nothing listening must fail")
	}
}

// A quiet tunnel stays open while both ends ping, and data written after
// the silence still arrives.
func TestIdleTunnelSurvivesOnPings(t *testing.T) {
	old := PingEvery
	PingEvery = 20 * time.Millisecond
	t.Cleanup(func() { PingEvery = old })
	release := make(chan struct{})
	url := tunnelTo(t, func(c net.Conn) {
		defer c.Close()
		<-release // silent for many pings
		_, _ = c.Write([]byte("late"))
		// Drain until the client hangs up: closing with its pings unread
		// would reset the connection under the reply.
		_, _ = io.Copy(io.Discard, c)
	})
	c := dial(t, url)
	time.Sleep(10 * PingEvery)
	close(release)
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "late" {
		t.Fatalf("read %q, %v", buf, err)
	}
}

func TestConnSurface(t *testing.T) {
	url := tunnelTo(t, func(c net.Conn) {
		defer c.Close()
		_, _ = io.Copy(io.Discard, c)
	})
	c := dial(t, url)
	if c.LocalAddr() == nil || c.RemoteAddr() == nil {
		t.Error("addresses")
	}
	if err := c.SetDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Error(err)
	}
	if err := c.SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Error(err)
	}
	if err := c.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
		t.Error(err)
	}
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Error("a passed read deadline must fail the read")
	}
	if err := c.Close(); err != nil {
		t.Error(err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("a second close is a no-op: %v", err)
	}
	if _, err := c.Write([]byte("x")); err == nil {
		t.Error("write after close must fail")
	}
}

// A request that is not a WebSocket upgrade is refused by Accept.
func TestAcceptRefusesAPlainRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	if _, err := Accept(rec, httptest.NewRequest("GET", "/tunnel", nil)); err == nil {
		t.Fatal("a plain GET must not become a tunnel")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rec.Code)
	}
}

// Pipe between conns without half-close closes the far side when one ends.
func TestPipeWithoutHalfClose(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	done := make(chan struct{})
	go func() { Pipe(a2, b1); close(done) }()
	go func() { _, _ = a1.Write([]byte("hi")); a1.Close() }()
	got, _ := io.ReadAll(b2)
	if string(got) != "hi" {
		t.Errorf("got %q", got)
	}
	b2.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pipe never ended")
	}
}

// Text frames are not the stream and are skipped; an abnormal close is an
// error, not a clean EOF.
func TestReadSkipsTextAndReportsAbnormalClose(t *testing.T) {
	url := tunnelTo(t, func(c net.Conn) {
		ws := c.(*conn).ws
		_ = ws.WriteMessage(websocket.TextMessage, []byte("ignored"))
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte("data"))
		_ = ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "boom"), time.Now().Add(time.Second))
		_, _ = io.Copy(io.Discard, c)
	})
	c := dial(t, url)
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "data" {
		t.Fatalf("read %q, %v", buf, err)
	}
	if _, err := c.Read(buf); err == nil || errors.Is(err, io.EOF) {
		t.Errorf("abnormal close = %v, want an error other than EOF", err)
	}
}

// A ping that cannot be written closes the tunnel.
func TestFailedPingClosesTheTunnel(t *testing.T) {
	old := PingEvery
	PingEvery = 10 * time.Millisecond
	t.Cleanup(func() { PingEvery = old })
	url := tunnelTo(t, func(c net.Conn) {
		_, _ = io.Copy(io.Discard, c)
		c.Close()
	})
	c := dial(t, url).(*conn)
	_ = c.ws.NetConn().Close() // the socket dies under the tunnel
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("a dead socket must end the tunnel at the next ping")
	}
}
