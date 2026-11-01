package browser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"localhost": true, "LOCALHOST": true, "localhost.": true, "app.localhost": true, "a.b.localhost.": true,
		"127.0.0.1": true, "127.1.2.3": true, "::1": true, "[::1]": true,
		"": false, "example.com": false, "localhost.example.com": false, "notlocalhost": false,
		"10.0.0.1": false, "0.0.0.0": false, "169.254.169.254": false, "127.0.0.1.nip.io": false, "::": false,
	}
	for host, want := range cases {
		if got := isLoopback(host); got != want {
			t.Errorf("isLoopback(%q) = %v", host, got)
		}
	}
}

func TestSplitTarget(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
		bad  bool
	}{
		{"localhost:3000", "localhost", 3000, false},
		{"localhost", "localhost", 80, false},
		{"[::1]:8080", "::1", 8080, false},
		{"[::1]", "[::1]", 80, false},
		{"localhost:0", "", 0, true},
		{"localhost:65536", "", 0, true},
		{"localhost:http", "", 0, true},
	}
	for _, c := range cases {
		host, port, err := splitTarget(c.in, "80")
		if host != c.host || port != c.port || (err != nil) != c.bad {
			t.Errorf("splitTarget(%q) = %q %d %v", c.in, host, port, err)
		}
	}
	if _, _, err := splitTarget("h", "nope"); err == nil {
		t.Error("a bad default port accepted")
	}
}

func testProxy(dial Dial) *proxy { return newProxy(dial, slog.New(slog.DiscardHandler)) }

func noDial(t *testing.T) Dial {
	return func(context.Context, int) (net.Conn, error) {
		t.Error("dialed the run")
		return nil, errors.New("no")
	}
}

// A browser request that does not read as a proxy request is turned away
// before anything is dialed.
func TestProxyRefusesMalformedRequests(t *testing.T) {
	cases := []struct {
		name string
		req  func() *http.Request
		code int
		want string
	}{
		{"origin form", func() *http.Request { return httptest.NewRequest("GET", "/path", nil) }, 400, "names its host"},
		{"https in the clear", func() *http.Request {
			r := httptest.NewRequest("GET", "/x", nil)
			r.URL.Scheme, r.URL.Host = "https", "localhost:3000"
			return r
		}, 400, "only http"},
		{"ftp", func() *http.Request {
			r := httptest.NewRequest("GET", "/x", nil)
			r.URL.Scheme, r.URL.Host = "ftp", "localhost"
			return r
		}, 400, "only http"},
		{"bad port", func() *http.Request {
			r := httptest.NewRequest("GET", "/x", nil)
			r.URL.Scheme, r.URL.Host = "http", "localhost:99999"
			return r
		}, 400, "bad port"},
		{"connect bad port", func() *http.Request {
			r := httptest.NewRequest("CONNECT", "/", nil)
			r.Host = "localhost:0"
			return r
		}, 400, "bad port"},
		{"connect outside", func() *http.Request {
			r := httptest.NewRequest("CONNECT", "/", nil)
			r.Host = "metadata.internal:443"
			return r
		}, 403, "outside the run"},
		{"metadata address", func() *http.Request {
			r := httptest.NewRequest("GET", "/latest/meta-data", nil)
			r.URL.Scheme, r.URL.Host = "http", "169.254.169.254"
			return r
		}, 403, "outside the run"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			testProxy(noDial(t)).ServeHTTP(w, c.req())
			if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("= %d %q", w.Code, w.Body.String())
			}
		})
	}
}

// CONNECT with no port means https: the run's 443.
func TestConnectDefaultsToTLSPort(t *testing.T) {
	var port int
	w := httptest.NewRecorder()
	r := httptest.NewRequest("CONNECT", "/", nil)
	r.Host = "localhost"
	testProxy(func(_ context.Context, p int) (net.Conn, error) { port = p; return nil, errors.New("closed") }).ServeHTTP(w, r)
	if port != 443 || w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "port 443 did not answer") {
		t.Errorf("port=%d code=%d body=%q", port, w.Code, w.Body.String())
	}
}

// pipeUp is the run's end of a dial, watching for the close.
func pipeUp() (net.Conn, net.Conn) { return net.Pipe() }

func TestConnectWithoutHijackClosesTheDial(t *testing.T) {
	client, up := pipeUp()
	defer client.Close()
	w := httptest.NewRecorder() // no Hijacker
	r := httptest.NewRequest("CONNECT", "/", nil)
	r.Host = "localhost:3000"
	testProxy(func(context.Context, int) (net.Conn, error) { return up, nil }).ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("code = %d", w.Code)
	}
	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("the dial stayed open: %v", err)
	}
}

type hijackFails struct{ *httptest.ResponseRecorder }

func (hijackFails) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("no hijack")
}

func TestConnectHijackFailureClosesTheDial(t *testing.T) {
	client, up := pipeUp()
	defer client.Close()
	r := httptest.NewRequest("CONNECT", "/", nil)
	r.Host = "localhost:3000"
	testProxy(func(context.Context, int) (net.Conn, error) { return up, nil }).ServeHTTP(hijackFails{httptest.NewRecorder()}, r)
	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("the dial stayed open: %v", err)
	}
}

// hijacked hands the proxy an already-closed client connection.
type hijacked struct {
	*httptest.ResponseRecorder
	c net.Conn
}

func (h hijacked) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.c, bufio.NewReadWriter(bufio.NewReader(h.c), bufio.NewWriter(h.c)), nil
}

func TestConnectClientGoneClosesBothEnds(t *testing.T) {
	gone, peer := net.Pipe()
	peer.Close()
	gone.Close()
	client, up := pipeUp()
	defer client.Close()
	r := httptest.NewRequest("CONNECT", "/", nil)
	r.Host = "localhost:3000"
	testProxy(func(context.Context, int) (net.Conn, error) { return up, nil }).ServeHTTP(hijacked{httptest.NewRecorder(), gone}, r)
	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("the dial stayed open: %v", err)
	}
}

// A full round trip where the app answers after the client half-closed:
// neither direction's EOF may cut the other.
func TestConnectHalfCloseDrains(t *testing.T) {
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
		b, _ := io.ReadAll(c) // until the client's half-close arrives
		fmt.Fprintf(c, "got %d bytes", len(b))
	}()
	dial := func(ctx context.Context, _ int) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", app.Addr().String())
	}
	ps := httptest.NewServer(testProxy(dial))
	defer ps.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(ps.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(c, "CONNECT localhost:3000 HTTP/1.1\r\nHost: localhost:3000\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT = %v, %v", resp, err)
	}
	c.Write([]byte("hello"))
	c.(*net.TCPConn).CloseWrite()
	got, _ := io.ReadAll(br)
	if string(got) != "got 5 bytes" {
		t.Errorf("answer = %q", got)
	}
}

func TestHalfCloseFallsBackToClose(t *testing.T) {
	a, b := net.Pipe() // no CloseWrite
	defer b.Close()
	halfClose(a)
	b.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := b.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("read = %v", err)
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 4}
	if n, err := b.Write([]byte("ab")); n != 2 || err != nil {
		t.Fatalf("write = %d, %v", n, err)
	}
	b.Write([]byte("cdef"))
	if b.String() != "cdef" {
		t.Errorf("tail = %q", b.String())
	}
}

func TestSessionMuxDeliversBufferedBytes(t *testing.T) {
	m, err := NewSessionMux("127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Addr() != m.ln.Addr().String() {
		t.Errorf("advertised = %q", m.Addr())
	}
	ln, tok := m.Register()
	defer ln.Close()
	if ln.Addr().String() != m.ln.Addr().String() {
		t.Errorf("listener addr = %v", ln.Addr())
	}
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- err.Error()
			return
		}
		defer c.Close()
		b := make([]byte, 5)
		io.ReadFull(c, b)
		got <- string(b)
	}()
	c, err := net.Dial("tcp", m.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The payload rides in the preamble's own write: it must not be lost to
	// the preamble reader's buffer.
	fmt.Fprintf(c, "APO1 %s proxy\nhello", tok)
	select {
	case s := <-got:
		if s != "hello" {
			t.Errorf("read = %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")
	}
}

// A connection routed to a session that closes before accepting it is
// dropped, not left hanging.
func TestSessionMuxDropsConnectionsForASessionClosing(t *testing.T) {
	m, err := NewSessionMux("127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ln, tok := m.Register()
	c, err := net.Dial("tcp", m.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "APO1 %s proxy\n", tok)
	// Either route is blocked handing c over, and closing lets it go, or it
	// arrives after the close and finds no session: closed both ways.
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil { // idempotent
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("read = %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) != 0 {
		t.Errorf("sessions = %v", m.sessions)
	}
}

func TestSessionMuxListenFailure(t *testing.T) {
	if _, err := NewSessionMux("256.0.0.1:0", ""); err == nil {
		t.Error("listened on nothing")
	}
}

func TestSessionMuxRejectsMalformedPreambles(t *testing.T) {
	m, err := NewSessionMux("127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ln, tok := m.Register()
	defer ln.Close()
	for _, pre := range []string{"APO2 " + tok + " proxy\n", "APO1 " + tok + " other\n", "APO1 " + tok + "\n"} {
		c, err := net.Dial("tcp", m.Addr())
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte(pre))
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		// Closed by the mux, not left waiting for a session to take it.
		if b, err := io.ReadAll(c); err != nil || len(b) != 0 {
			t.Errorf("%q: got %q, %v", pre, b, err)
		}
		c.Close()
	}
}
