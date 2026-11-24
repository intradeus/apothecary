package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestTermRing(t *testing.T) {
	r := termRing{buf: make([]byte, 8)}
	if got := r.snapshot(); len(got) != 0 {
		t.Errorf("empty = %q", got)
	}
	r.write([]byte("abc"))
	r.write([]byte("def"))
	if got := string(r.snapshot()); got != "abcdef" {
		t.Errorf("before wrapping = %q", got)
	}
	r.write([]byte("gh")) // exactly full
	if got := string(r.snapshot()); got != "abcdefgh" {
		t.Errorf("full = %q", got)
	}
	r.write([]byte("ijk")) // wraps: the oldest three go
	if got := string(r.snapshot()); got != "defghijk" {
		t.Errorf("wrapped = %q", got)
	}
	r.write([]byte("0123456789")) // longer than the ring: its tail
	if got := string(r.snapshot()); got != "23456789" {
		t.Errorf("overrun = %q", got)
	}
	r.write([]byte("x"))
	if got := string(r.snapshot()); got != "3456789x" {
		t.Errorf("after an overrun = %q", got)
	}
	snap := r.snapshot()
	r.write([]byte("yz"))
	if string(snap) != "3456789x" {
		t.Errorf("a snapshot moved with the ring: %q", snap)
	}
}

// fakeSession is a session with no shell behind it: enough for the
// registry's bookkeeping and the fan-out.
func fakeSession(t *testing.T, reg *termRegistry, id string, n, pid int) *termSession {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	cmd := exec.Command("true")
	cmd.Process = &os.Process{Pid: pid}
	return &termSession{id: id, n: n, dir: "/w/" + id, started: time.Unix(int64(n), 0), cmd: cmd, master: f, reg: reg,
		readEnd: make(chan struct{}), ring: termRing{buf: make([]byte, 64)}, clients: map[*termClient]struct{}{}}
}

// The list is oldest first with each session's attached clients; numbers
// are reused once their session is gone; past the cap nothing starts.
func TestTermRegistryBookkeeping(t *testing.T) {
	old := procRoot
	procRoot = t.TempDir()
	t.Cleanup(func() { procRoot = old })
	reg := newTermRegistry()
	a, b := fakeSession(t, reg, "a", 1, 1<<30), fakeSession(t, reg, "b", 2, 1<<30+1)
	reg.order = []*termSession{a, b}
	ca, _, _ := a.attach(func() {})
	_, _, _ = a.attach(func() {})
	got := reg.list()
	if len(got) != 2 || got[0].ID != "a" || got[0].Attached != 2 || got[1].ID != "b" || got[1].Attached != 0 || got[0].Dir != "/w/a" {
		t.Fatalf("list = %+v", got)
	}
	a.detach(ca)
	a.detach(ca) // twice is harmless
	if got := reg.list(); got[0].Attached != 1 || got[0].Busy {
		t.Errorf("after a detach = %+v", got[0])
	}
	if reg.get("b") != b || reg.get("c") != nil {
		t.Error("get")
	}

	a.end()
	if reg.get("a") != nil {
		t.Error("an ended session is still registered")
	}
	if _, _, err := a.attach(func() {}); !errors.Is(err, errTermEnded) {
		t.Errorf("attach after the end = %v", err)
	}
	reg.mu.Lock()
	n := reg.freeNumber()
	reg.mu.Unlock()
	if n != 1 {
		t.Errorf("free number = %d, want the ended session's 1", n)
	}

	for i := len(reg.order); i < termMaxSessions; i++ {
		reg.order = append(reg.order, fakeSession(t, reg, strconv.Itoa(i), i+10, 1<<30+i))
	}
	if _, err := reg.create("", t.TempDir(), 80, 24); !errors.Is(err, errTermCap) {
		t.Errorf("past the cap = %v", err)
	}
	if got := newTermRegistry().list(); got == nil || len(got) != 0 {
		t.Errorf("no sessions = %#v, want an empty list", got)
	}
}

// A client that falls behind is dropped and disconnected; the others get
// every chunk, and the ring keeps it all the same.
func TestTermSlowClientIsDropped(t *testing.T) {
	reg := newTermRegistry()
	s := fakeSession(t, reg, "a", 1, 1<<30)
	kicked := 0
	slow, _, _ := s.attach(func() { kicked++ })
	fast, _, _ := s.attach(func() { t.Error("the fast client was kicked") })
	var got bytes.Buffer
	for i := range termClientQueue + 5 {
		s.publish([]byte{byte('a' + i%26)})
		got.Write(<-fast.out)
	}
	if kicked != 1 || s.attached() != 1 {
		t.Errorf("kicked %d, attached %d", kicked, s.attached())
	}
	n := 0
	for range slow.out {
		n++
	}
	if n != termClientQueue {
		t.Errorf("the slow client's queue held %d", n)
	}
	if got.Len() != termClientQueue+5 {
		t.Errorf("the fast client got %d chunks", got.Len())
	}
	s.end()
	if _, ok := <-fast.out; ok || !fast.ended {
		t.Error("the end did not close the remaining client's queue as ended")
	}
	if slow.ended {
		t.Error("a dropped client was told the session ended")
	}
}

// writeProc lays a fake /proc/<pid> down: its stat and, when comm is not
// empty, its comm file.
func writeProc(t *testing.T, root string, pid int, stat, comm string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if comm != "" {
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionBusy(t *testing.T) {
	old := procRoot
	t.Cleanup(func() { procRoot = old })
	procRoot = t.TempDir()
	const shell = 100
	writeProc(t, procRoot, shell, "100 (bash) S 1 100 100 34816 120 0", "bash")
	writeProc(t, procRoot, 200, "200 (sleep) S 1 200 200 0 -1 0", "sleep")     // another session
	writeProc(t, procRoot, 101, "101 (defunct) Z 100 101 100 34816 120 0", "") // a zombie in it
	_ = os.MkdirAll(filepath.Join(procRoot, "self"), 0o755)                    // not a pid
	_ = os.MkdirAll(filepath.Join(procRoot, "300"), 0o755)                     // no stat
	writeProc(t, procRoot, 400, "garbage", "")
	writeProc(t, procRoot, 401, "401 (x) S", "")
	writeProc(t, procRoot, 402, "402 (x) S 1 two 3 0", "")
	if busy, cmd := sessionBusy(scanProcs(), shell, shell); busy || cmd != "" {
		t.Errorf("at the prompt: %v %q", busy, cmd)
	}

	// A background job, its comm read from stat when the file is gone.
	writeProc(t, procRoot, 110, "110 (bg job) S 100 110 100 34816 120 0", "")
	if busy, cmd := sessionBusy(scanProcs(), shell, shell); !busy || cmd != "bg job" {
		t.Errorf("a background job: %v %q", busy, cmd)
	}
	// The foreground program wins, by the group leader's name; a comm with
	// spaces and parentheses does not throw the fields off.
	writeProc(t, procRoot, 121, "121 (node) S 120 120 100 34816 120 0", "node")
	writeProc(t, procRoot, 120, "120 (my (tui) x) R 100 120 100 34816 120 0", "my (tui) x")
	if busy, cmd := sessionBusy(scanProcs(), shell, 120); !busy || cmd != "my (tui) x" {
		t.Errorf("a foreground program: %v %q", busy, cmd)
	}
	// Its leader gone, another of the group names it.
	_ = os.RemoveAll(filepath.Join(procRoot, "120"))
	if busy, cmd := sessionBusy(scanProcs(), shell, 120); !busy || cmd != "node" {
		t.Errorf("without its leader: %v %q", busy, cmd)
	}

	procRoot = filepath.Join(procRoot, "missing")
	if busy, cmd := sessionBusy(scanProcs(), shell, shell); busy || cmd != "" {
		t.Errorf("no /proc: %v %q", busy, cmd)
	}
}
