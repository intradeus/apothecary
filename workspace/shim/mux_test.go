package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// echoSock serves a unix socket that echoes one line back upper-cased.
func echoSock(t *testing.T, dir, name string) {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(dir, name))
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
				line, _ := bufio.NewReader(c).ReadString('\n')
				c.Write([]byte(strings.ToUpper(line)))
			}(c)
		}
	}()
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mux")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestMuxSplicesToTheNamedSocket(t *testing.T) {
	dir := shortTempDir(t)
	echoSock(t, dir, "sidecar.sock")
	ln, err := startMux("127.0.0.1:0", dir, "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Preamble and payload in ONE write: the mux must not lose the bytes it
	// buffered past the preamble line.
	c.Write([]byte("APO1 tok sidecar.sock\nhello\n"))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(c)
	ack, _ := br.ReadString('\n')
	got, _ := br.ReadString('\n')
	if ack != "OK\n" || got != "HELLO\n" {
		t.Fatalf("got %q %q", ack, got)
	}
}

func TestMuxRefusesBadTokensAndPaths(t *testing.T) {
	dir := shortTempDir(t)
	echoSock(t, dir, "sidecar.sock")
	ln, err := startMux("127.0.0.1:0", dir, "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	for _, pre := range []string{
		"APO1 wrong sidecar.sock\n",
		"APO1 tok ../sidecar.sock\n",
		"APO1 tok /etc/passwd\n",
		"HELLO tok sidecar.sock\n",
		"APO1 tok\n",
		"APO1 tok missing.sock\n", // nothing serves it: no ack, closed
	} {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte(pre + "hello\n"))
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		b, _ := io.ReadAll(c)
		c.Close()
		if len(b) != 0 {
			t.Errorf("%q: got %q, want the connection closed", pre, b)
		}
	}
}

func TestPumpStdio(t *testing.T) {
	dir := shortTempDir(t)
	echoSock(t, dir, "app-3000.sock")
	var out bytes.Buffer
	if err := pumpStdio(filepath.Join(dir, "app-3000.sock"), strings.NewReader("ping\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "PING\n" {
		t.Fatalf("got %q", out.String())
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shim")
	for range 2 {
		if err := seed(dir); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "shim")); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("shim not executable: %v %v", st, err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "apothecary-shim")); err != nil || target != "shim" {
		t.Fatalf("alias = %q, %v", target, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "shim.hash")); strings.TrimSpace(string(b)) != sourceHash {
		t.Fatalf("shim.hash = %q", b)
	}
}

// What opens a connection is what the mux reads, payload buffered behind it.
func TestPreambleRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		_ = writePreamble(client, "tok", "proxy")
		_, _ = client.Write([]byte("payload"))
	}()
	target, br, err := readPreamble(server, "tok")
	if err != nil || target != "proxy" {
		t.Fatalf("target = %q, %v", target, err)
	}
	buf := make([]byte, 7)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "payload" {
		t.Errorf("payload = %q, %v", buf, err)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWritePreambleReportsAFailedWrite(t *testing.T) {
	if err := writePreamble(brokenWriter{}, "tok", "proxy"); err == nil {
		t.Error("a failed write went unreported")
	}
}

// No token configured admits nothing, and neither does a preamble too long to
// be one or a client gone before it finished.
func TestReadPreambleRefuses(t *testing.T) {
	for _, c := range []struct{ line, token string }{
		{"APO1 tok sidecar.sock\n", ""},
		{"APO1 " + strings.Repeat("t", 600) + " sidecar.sock\n", "tok"},
		{"APO1 tok side", "tok"},
	} {
		client, server := net.Pipe()
		go func() {
			_, _ = io.WriteString(client, c.line)
			client.Close()
		}()
		if target, _, err := readPreamble(server, c.token); err == nil {
			t.Errorf("%q with token %q admitted, target %q", c.line, c.token, target)
		}
		server.Close()
	}
}

// Nothing listening means no port to serve from.
func TestStartMuxFailsOnABadAddress(t *testing.T) {
	if _, err := startMux("127.0.0.1:99999", t.TempDir(), "tok"); err == nil {
		t.Error("a mux on an impossible port started")
	}
}

func TestPumpStdioNeedsASocket(t *testing.T) {
	if err := pumpStdio(filepath.Join(t.TempDir(), "gone.sock"), strings.NewReader(""), io.Discard); err == nil {
		t.Error("pumping to a socket that is not there succeeded")
	}
}
