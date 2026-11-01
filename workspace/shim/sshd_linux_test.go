//go:build linux

package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

// A shell on a PTY: it runs in the repo, the window size the client asked
// for applies, and a window-change resizes it.
func TestSSHShellOnAPTY(t *testing.T) {
	client, _, repo, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	if err := ses.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	stdin, err := ses.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := ses.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := ses.Shell(); err != nil {
		t.Fatal(err)
	}
	read := readUntil(t, stdout)
	_, _ = io.WriteString(stdin, "pwd; stty size; echo marker-$((40+2))\n")
	out := read("marker-42")
	if !strings.Contains(out, repo) || !strings.Contains(out, "24 80") {
		t.Errorf("shell output lacks the repo or the size:\n%s", out)
	}
	if err := ses.WindowChange(30, 100); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(stdin, "stty size; echo marker-$((40+3))\n")
	if out := read("marker-43"); !strings.Contains(out, "30 100") {
		t.Errorf("resize not applied:\n%s", out)
	}
	_, _ = io.WriteString(stdin, "exit 3\n")
	done := make(chan error, 1)
	go func() { done <- ses.Wait() }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "3") {
			t.Errorf("shell exit = %v, want status 3", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the shell did not exit")
	}
}

// readUntil returns a reader of stdout that accumulates until a marker
// shows up (or 10 s pass).
func readUntil(t *testing.T, r io.Reader) func(marker string) string {
	t.Helper()
	var got strings.Builder
	chunks := make(chan []byte, 64)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				chunks <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(chunks)
				return
			}
		}
	}()
	return func(marker string) string {
		deadline := time.After(10 * time.Second)
		for !strings.Contains(got.String(), marker) {
			select {
			case c, ok := <-chunks:
				if !ok {
					t.Fatalf("stdout closed before %q:\n%s", marker, got.String())
				}
				got.Write(c)
			case <-deadline:
				t.Fatalf("no %q within 10s:\n%s", marker, got.String())
			}
		}
		return got.String()
	}
}

// A client hanging up on a shell hangs up the shell.
func TestSSHHangupStopsAShellOnAPTY(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := ses.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ses.StdinPipe(); err != nil { // open: only the hang-up ends it
		t.Fatal(err)
	}
	pid := startedCommand(t, ses, "exec sleep 60")
	ses.Close()
	waitGone(t, pid)
}

// With no term asked for, a pty is an xterm's.
func TestSSHPTYDefaultTerm(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	t.Setenv("SHELL", "/bin/sh")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	if err := ses.RequestPty("", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ses.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if out, err := ses.Output("echo $TERM"); err != nil || !strings.Contains(string(out), "xterm-256color") {
		t.Errorf("TERM = %q, %v", out, err)
	}
}

// A shell that cannot start is refused, on a pty as off one.
func TestSSHShellThatCannotStart(t *testing.T) {
	client, _, _, _ := newTestSSH(t)
	t.Setenv("SHELL", "/no/such/shell")
	ses, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	if err := ses.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	if err := ses.Shell(); err == nil {
		t.Error("a shell that does not exist started")
	}
}
