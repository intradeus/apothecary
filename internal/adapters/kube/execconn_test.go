package kube

import (
	"context"
	"io"
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
	if conn.SetDeadline(time.Now()) == nil || conn.SetReadDeadline(time.Now()) == nil || conn.SetWriteDeadline(time.Now()) == nil {
		t.Error("deadlines are not supported over a pipe")
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
