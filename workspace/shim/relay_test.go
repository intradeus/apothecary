package main

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// echoUnix runs a doubling echo server on a unix socket.
func echoServer(t *testing.T, ln net.Listener) {
	t.Helper()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				data, _ := io.ReadAll(c)
				_, _ = c.Write(append([]byte("echo:"), data...))
			}(c)
		}
	}()
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func roundTrip(t *testing.T, dial func() (net.Conn, error), payload string) string {
	t.Helper()
	c, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func TestForwardPairTCPToUnix(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	echoServer(t, ln)

	port := freePort(t)
	startForward(strconv.Itoa(port) + ":" + sock)

	got := roundTrip(t, func() (net.Conn, error) {
		return net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	}, "ping")
	if got != "echo:ping" {
		t.Errorf("round trip = %q", got)
	}
}

func TestReversePairUnixToTCP(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	echoServer(t, target)

	sock := filepath.Join(t.TempDir(), "app-3000.sock")
	startReverse(sock + ":" + target.Addr().String())

	got := roundTrip(t, func() (net.Conn, error) {
		return net.Dial("unix", sock)
	}, "web")
	if got != "echo:web" {
		t.Errorf("round trip = %q", got)
	}
}

// startForward binds a loopback TCP port and pumps each conn into the unix
// socket; startReverse does the reverse. Both drive real round trips.
func TestStartForwardAndReverse(t *testing.T) {
	// The "engine side": a unix socket that echoes.
	sock := filepath.Join(t.TempDir(), "engine.sock")
	uln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer uln.Close()
	go echoServer(t, uln)

	port := freePort(t)
	startForward(strconv.Itoa(port) + ":" + sock)
	// Give the listener a moment to bind.
	deadline := time.Now().Add(5 * time.Second)
	var c net.Conn
	for {
		c, err = net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("forward listener never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := roundTrip(t, func() (net.Conn, error) { return c, nil }, "hello-fwd")
	if got != "echo:hello-fwd" {
		t.Errorf("forward round trip = %q", got)
	}

	// Reverse: a unix socket listener that pumps to an in-container TCP echo.
	tcpEcho, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpEcho.Close()
	go echoServer(t, tcpEcho)
	rsock := filepath.Join(t.TempDir(), "reverse.sock")
	startReverse(rsock + ":" + tcpEcho.Addr().String())
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := net.Dial("unix", rsock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reverse listener never came up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	got = roundTrip(t, func() (net.Conn, error) { return net.Dial("unix", rsock) }, "hello-rev")
	if got != "echo:hello-rev" {
		t.Errorf("reverse round trip = %q", got)
	}
}

// A reverse serve whose dial fails drops the connection without a panic.
func TestServeDialFailureClosesTheConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serve(ln, func() (net.Conn, error) { return nil, errors.New("upstream down") })
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, _ := c.Read(make([]byte, 4)); n != 0 {
		t.Error("a failed dial must close the client with no data")
	}
}

func TestHalfCloseOnAPlainConn(t *testing.T) {
	// A net.Conn that is not a TCP/unix conn has no CloseWrite: halfClose is a
	// no-op, never a panic.
	halfClose(plainConn{})
}

type plainConn struct{ net.Conn }

// What a relay cannot serve it says so on stderr and exits 1, for the
// provisioner's log.
func TestRelayRefusesBadArguments(t *testing.T) {
	dir := shortTempDir(t)
	token := filepath.Join(dir, "token")
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(token, []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")
	serve := func(addr, tokenFile string) []string {
		return []string{"--serve", addr, "--dir", dir, "--token-file", tokenFile}
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "no relay pairs given"},
		{[]string{"--stdio"}, "--stdio needs <sock>"},
		{[]string{"--stdio", missing}, "stdio " + missing},
		{[]string{"--reverse"}, "--reverse needs <sock>:<host>:<port>"},
		{[]string{"--reverse", "just-a-socket"}, "bad reverse spec"},
		{[]string{"--reverse", "no-port.sock:localhost"}, "bad reverse spec"},
		{[]string{"--reverse", filepath.Join(missing, "s.sock") + ":127.0.0.1:1"}, "listen unix"},
		{[]string{"8080"}, "bad forward pair"},
		{[]string{"not-a-port:/tmp/s.sock"}, "listen 127.0.0.1:not-a-port"},
		{[]string{"--serve"}, "--serve needs a value"},
		{[]string{"--dir"}, "--dir needs a value"},
		{[]string{"--token-file"}, "--token-file needs a value"},
		{[]string{"--serve", "127.0.0.1:0"}, "--serve needs --dir and --token-file"},
		{[]string{"--serve", "127.0.0.1:0", "--dir", dir}, "--serve needs --dir and --token-file"},
		{serve("127.0.0.1:0", missing), "token file"},
		{serve("127.0.0.1:0", empty), "is empty"},
		{serve("127.0.0.1:99999", token), "serve 127.0.0.1:99999"},
	} {
		r := shim(t, "", append([]string{"relay"}, c.args...)...)
		if r.code != 1 || !strings.HasPrefix(r.stderr, "relay: ") || !strings.Contains(r.stderr, c.want) {
			t.Errorf("relay %v = %d %q, want %q", c.args, r.code, r.stderr, c.want)
		}
	}
}

// A port already held means a relay from an earlier provision serves it: this
// one bows out quietly.
func TestRelayExitsQuietlyWhenThePortIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if r := shim(t, "", "relay", strconv.Itoa(port)+":/tmp/unused.sock"); r.code != 0 || r.stderr != "" {
		t.Errorf("relay on a taken port = %+v", r)
	}
}

// One connection over stdio, then the process is done.
func TestRelayStdio(t *testing.T) {
	dir := shortTempDir(t)
	echoSock(t, dir, "app-3000.sock")
	r := shim(t, "ping\n", "relay", "--stdio", filepath.Join(dir, "app-3000.sock"))
	if r.code != 0 || r.stdout != "PING\n" {
		t.Errorf("relay --stdio = %+v", r)
	}
}

// All three kinds of pump in one process, until it is reaped.
func TestRelayServesForwardReverseAndMux(t *testing.T) {
	dir := shortTempDir(t)
	engine := filepath.Join(dir, "engine.sock")
	uln, err := net.Listen("unix", engine)
	if err != nil {
		t.Fatal(err)
	}
	defer uln.Close()
	echoServer(t, uln)
	app, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	echoServer(t, app)
	echoSock(t, dir, "sidecar.sock")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("  tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reverse := filepath.Join(dir, "app-3000.sock")
	if err := os.WriteFile(reverse, nil, 0o644); err != nil { // stale, from a previous relay
		t.Fatal(err)
	}
	forward, mux := freePort(t), freePort(t)

	startShim(t, shimCmd(t, "relay", strconv.Itoa(forward)+":"+engine,
		"--reverse", reverse+":"+app.Addr().String(),
		"--serve", "127.0.0.1:"+strconv.Itoa(mux), "--dir", dir, "--token-file", token))

	muxAddr := "127.0.0.1:" + strconv.Itoa(mux)
	var c net.Conn
	waitFor(t, func() bool { c, err = net.Dial("tcp", muxAddr); return err == nil })
	defer c.Close()

	got := roundTrip(t, func() (net.Conn, error) { return net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(forward)) }, "fwd")
	if got != "echo:fwd" {
		t.Errorf("forward = %q", got)
	}
	got = roundTrip(t, func() (net.Conn, error) { return net.Dial("unix", reverse) }, "rev")
	if got != "echo:rev" {
		t.Errorf("reverse = %q", got)
	}
	if info, err := os.Stat(reverse); err != nil || info.Mode().Perm() != 0o666 {
		t.Errorf("the socket's mode lets only its owner in: %v, %v", info, err)
	}
	if err := writePreamble(c, "tok", "sidecar.sock"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(c, "mux\n"); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if b, _ := io.ReadAll(c); string(b) != "OK\nMUX\n" {
		t.Errorf("mux = %q", b)
	}
}
