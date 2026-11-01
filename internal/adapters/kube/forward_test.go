package kube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeKubectl writes an executable standing in for kubectl: Client.Bin points
// at it, so no real kubectl is ever run. `exec sleep` keeps one process per
// forward, so killing it closes the pipe the supervisor reads.
func fakeKubectl(t *testing.T, body string) *Client {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-kubectl")
	script := "#!/bin/sh\nDIR=" + dir + "\necho \"$@\" >> $DIR/argv\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Client{Bin: bin, Namespace: "ws"}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestForwardReportsTheLocalPort(t *testing.T) {
	c := fakeKubectl(t, `echo "some banner"
echo "Forwarding from 127.0.0.1:49152 -> 7070"
exec sleep 60`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, err := c.Forward(ctx, "pod/apo-x", 7070)
	if err != nil {
		t.Fatal(err)
	}
	if f.Addr() != "127.0.0.1:49152" {
		t.Errorf("addr = %q", f.Addr())
	}
	f.Close()
	select {
	case <-f.done:
	default:
		t.Error("Close returned with the supervisor alive")
	}
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(c.Bin), "argv"))
	if got := strings.TrimSpace(string(b)); got != "--namespace ws port-forward --address 127.0.0.1 pod/apo-x 0:7070" {
		t.Errorf("argv = %q", got)
	}
}

// kubectl dies whenever the pod restarts; the supervisor respawns it and Addr
// follows the new port, which is why Addr is read per dial.
func TestForwardRespawnsAndAddrFollows(t *testing.T) {
	c := fakeKubectl(t, `n=$(wc -l < $DIR/argv | tr -d ' ')
echo "Forwarding from 127.0.0.1:4000$n -> 7070"
if [ "$n" -ge 2 ]; then exec sleep 60; fi`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, err := c.Forward(ctx, "svc/gw", 9041)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	waitFor(t, "the respawned port", func() bool { return f.Addr() == "127.0.0.1:40002" })
}

// A forward that never prints a port fails when the caller gives up, and
// leaves nothing running.
func TestForwardGivesUpWithItsContext(t *testing.T) {
	c := fakeKubectl(t, `exec sleep 60`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Forward(ctx, "pod/x", 1)
		done <- err
	}()
	argv := filepath.Join(filepath.Dir(c.Bin), "argv")
	waitFor(t, "kubectl to start", func() bool { _, err := os.Stat(argv); return err == nil })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Forward did not return after cancel")
	}
}

// A binary that cannot start is retried with backoff, not spun on, and the
// caller's context still ends the wait.
func TestForwardStartFailureBacksOff(t *testing.T) {
	c := &Client{Bin: filepath.Join(t.TempDir(), "absent")}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, err := c.Forward(ctx, "pod/x", 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

// A kubectl dying at once is restarted on a growing backoff, not in a tight
// loop against the API server.
func TestForwardBacksOffBetweenRestarts(t *testing.T) {
	c := fakeKubectl(t, `exit 0`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Forward(ctx, "pod/x", 1)
		done <- err
	}()
	// The window opens at the first start, not at Forward: on a loaded machine
	// the fake kubectl can take longer than any fixed window to get going.
	argv := filepath.Join(filepath.Dir(c.Bin), "argv")
	waitFor(t, "kubectl to start", func() bool { _, err := os.Stat(argv); return err == nil })
	// Backoff is 250ms then 500ms: at most two starts fit in 400ms, where a
	// tight loop would make dozens. Slowness only lowers the count.
	time.Sleep(400 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Forward did not return after cancel")
	}
	b, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\n"); n < 1 || n > 3 {
		t.Errorf("%d starts in 400ms, want a handful at most", n)
	}
}

// The supervisor ignores anything that is not kubectl's own forwarding line,
// including an IPv6 line, and restarts through the seam.
func TestForwardRegexOnlyMatchesIPv4Loopback(t *testing.T) {
	for line, want := range map[string]string{
		"Forwarding from 127.0.0.1:5000 -> 80": "5000",
		"Forwarding from [::1]:5000 -> 80":     "",
		"Forwarding from 10.0.0.1:5000 -> 80":  "",
		"Handling connection for 5000":         "",
	} {
		m := forwardingRE.FindStringSubmatch(line)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("%q = %q, want %q", line, got, want)
		}
	}
}
