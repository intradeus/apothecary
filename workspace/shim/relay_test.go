package main

import (
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
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
