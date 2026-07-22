package main

import (
	"io"
	"net"
	"path/filepath"
	"strconv"
	"testing"
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
