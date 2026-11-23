package kube

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeKubectl writes a kubectl that records its arguments and runs body.
func fakeExecKubectl(t *testing.T, body string) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\necho \"$@\" > " + args + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Client{Bin: bin, Kubeconfig: "/k", Namespace: "ns"}, args
}

// A connection over kubectl exec: the argv it was given, what the far end
// echoes, half-close passed on as end of input.
func TestExecConn(t *testing.T) {
	c, args := fakeExecKubectl(t, "exec tr a-z A-Z")
	conn, err := c.ExecConn(context.Background(), "p1", "relay", []string{"/shim", "relay", "--stdio-ack", "/s"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "ping\n"); err != nil {
		t.Fatal(err)
	}
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(conn)
	if err != nil || string(out) != "PING\n" {
		t.Errorf("read %q, %v", out, err)
	}
	got, _ := os.ReadFile(args)
	if want := "--kubeconfig /k --namespace ns exec -i p1 -c relay -- /shim relay --stdio-ack /s\n"; string(got) != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	if conn.RemoteAddr().String() != "pod/p1" || conn.LocalAddr().Network() != "kubectl-exec" {
		t.Errorf("addrs = %v %v", conn.LocalAddr(), conn.RemoteAddr())
	}
}

// A deadline bounds a read (and a write) like a socket's: past it the call
// fails with a timeout, and a later deadline lets the connection go on.
func TestExecConnDeadlines(t *testing.T) {
	c, _ := fakeExecKubectl(t, "exec cat")
	conn, err := c.ExecConn(context.Background(), "p1", "relay", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var ne net.Error
	if _, err := conn.Read(make([]byte, 1)); !errors.As(err, &ne) || !ne.Timeout() || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read past its deadline: %v", err)
	}
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "x"); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(conn, b); err != nil || b[0] != 'x' {
		t.Errorf("after a new deadline: %q, %v", b, err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "y"); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("write past its deadline: %v", err)
	}
}

// Close ends a kubectl that ignores the end of its input.
func TestExecConnCloseKillsALingeringKubectl(t *testing.T) {
	c, _ := fakeExecKubectl(t, "exec sleep 30")
	conn, err := c.ExecConn(context.Background(), "p1", "relay", []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("close took %s", d)
	}
	_ = conn.Close() // twice is fine
}

func TestExecConnRefusals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := fakeExecKubectl(t, "exit 0")
	if _, err := c.ExecConn(ctx, "p", "c", []string{"x"}); err == nil {
		t.Error("a cancelled dial starts nothing")
	}
	missing := &Client{Bin: filepath.Join(t.TempDir(), "no-kubectl")}
	if _, err := missing.ExecConn(context.Background(), "p", "c", []string{"x"}); err == nil || !strings.Contains(err.Error(), "no-kubectl") {
		t.Errorf("no kubectl: %v", err)
	}
}
