package main

// Terminal sessions: the login shells the sidecar keeps for the run page's
// terminal. A session outlives the WebSocket that opened it — a refresh, a
// tab left, a browser closed or an engine restart only detaches — and lives
// until its shell exits, someone closes it, or the workspace stops. Its
// recent output is kept in a ring and replayed to whoever attaches next;
// any number of clients may attach to one session at once, each seeing the
// same output and typing into the same shell.
//
// A session is busy while a process other than its shell runs in it (its
// session id is the shell's pid; startPTY makes the shell a session leader):
// a foreground program, an agent's TUI waiting for its next prompt, a
// background job. The engine asks before it stops an idle workspace, and a
// busy session keeps the workspace up.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// termMaxSessions is the most live sessions one sidecar keeps.
	termMaxSessions = 16
	// termRingSize is how much recent output a session replays on attach.
	termRingSize = 512 << 10
	// termClientQueue is how many output chunks a client may fall behind by
	// before it is dropped: the PTY reader never waits for a client.
	termClientQueue = 256
	// termExitGrace is how long a session whose shell exited waits for its
	// last output before it hangs up whatever still holds the terminal.
	termExitGrace = 500 * time.Millisecond
)

// errTermCap: the sidecar already keeps termMaxSessions sessions.
var errTermCap = errors.New("too many terminal sessions; close one first")

// errTermEnded: the session ended before the client attached.
var errTermEnded = errors.New("the terminal session has ended")

// termRegistry is the sidecar's live terminal sessions.
type termRegistry struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	order []*termSession // oldest first
}

func newTermRegistry() *termRegistry {
	ctx, cancel := context.WithCancel(context.Background())
	return &termRegistry{ctx: ctx, cancel: cancel}
}

// termSession is one shell on a PTY.
type termSession struct {
	id      string
	n       int
	repo    string // the checkout's name; "" the primary
	dir     string
	started time.Time
	cmd     *exec.Cmd
	master  *os.File
	reg     *termRegistry
	ended   sync.Once
	readEnd chan struct{} // closed when the PTY reader stops

	mu      sync.Mutex
	ring    termRing
	clients map[*termClient]struct{}
	closed  bool
}

// termClient is one attached WebSocket's queue of output.
type termClient struct {
	out chan []byte
	// kick disconnects the client: what dropping one that fell behind
	// does, so its writer is not left blocked on a peer that never reads.
	kick func()
	// ended: the queue was closed because the session ended (the client is
	// told), not because the client fell behind. Written under the
	// session's lock before out is closed.
	ended bool
}

// termSessionInfo is a session as GET /term/sessions lists it (the engine's
// sidecar.TerminalSession).
type termSessionInfo struct {
	ID        string    `json:"id"`
	N         int       `json:"n"`
	Repo      string    `json:"repo"`
	Dir       string    `json:"dir"`
	StartedAt time.Time `json:"started_at"`
	Attached  int       `json:"attached"`
	Busy      bool      `json:"busy"`
	Command   string    `json:"command,omitempty"`
}

func newTermID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// create starts a login shell in dir, as the remote user's terminal: the
// registry's, so it outlives the request that asked for it.
func (r *termRegistry) create(repo, dir string, cols, rows int) (*termSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) >= termMaxSessions {
		return nil, errTermCap
	}
	cmd := exec.CommandContext(r.ctx, shellPath(), "-l")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGHUP) }
	master, err := startPTY(cmd, cols, rows)
	if err != nil {
		return nil, err
	}
	s := &termSession{
		id: newTermID(), n: r.freeNumber(), repo: repo, dir: dir, started: time.Now(),
		cmd: cmd, master: master, reg: r, readEnd: make(chan struct{}),
		ring: termRing{buf: make([]byte, termRingSize)}, clients: map[*termClient]struct{}{},
	}
	r.order = append(r.order, s)
	go s.read()
	go s.wait()
	return s, nil
}

// freeNumber is the lowest display number no live session uses. r.mu held.
func (r *termRegistry) freeNumber() int {
	for n := 1; ; n++ {
		if !slices.ContainsFunc(r.order, func(s *termSession) bool { return s.n == n }) {
			return n
		}
	}
}

func (r *termRegistry) get(id string) *termSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.order {
		if s.id == id {
			return s
		}
	}
	return nil
}

func (r *termRegistry) remove(s *termSession) {
	r.mu.Lock()
	r.order = slices.DeleteFunc(r.order, func(o *termSession) bool { return o == s })
	r.mu.Unlock()
}

// closeAll ends every session (a test's cleanup).
func (r *termRegistry) closeAll() {
	r.cancel()
	r.mu.Lock()
	all := slices.Clone(r.order)
	r.mu.Unlock()
	for _, s := range all {
		s.kill()
	}
}

// read copies the PTY's output into the ring and out to every client.
func (s *termSession) read() {
	defer close(s.readEnd)
	buf := make([]byte, 32<<10)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			s.publish(slices.Clone(buf[:n]))
		}
		if err != nil {
			s.end()
			return
		}
	}
}

// publish keeps chunk in the ring and queues it for every client. A client
// that has fallen termClientQueue chunks behind is dropped, so a slow one
// never holds up the shell or the others.
func (s *termSession) publish(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring.write(chunk)
	for c := range s.clients {
		select {
		case c.out <- chunk:
		default:
			delete(s.clients, c)
			close(c.out)
			c.kick()
		}
	}
}

// wait reaps the shell and ends the session: once the reader has drained
// what the shell wrote, or after termExitGrace when a job it left behind
// still holds the terminal open (the end's hang-up reaches that job).
func (s *termSession) wait() {
	_ = s.cmd.Wait()
	select {
	case <-s.readEnd:
	case <-time.After(termExitGrace):
	}
	s.end()
}

// end takes the session out of the registry, tells its clients and closes
// the master, which hangs up the terminal. Once.
func (s *termSession) end() {
	s.ended.Do(func() {
		s.reg.remove(s)
		s.mu.Lock()
		s.closed = true
		for c := range s.clients {
			c.ended = true
			close(c.out)
		}
		s.clients = nil
		s.mu.Unlock()
		_ = s.master.Close()
	})
}

// kill hangs the shell up (SIGHUP, as a terminal closing does) and ends the
// session.
func (s *termSession) kill() {
	_ = s.cmd.Process.Signal(syscall.SIGHUP)
	s.end()
}

// attach registers a client, kick its disconnect, and returns the output
// to replay: the ring as it stands when the client starts receiving, so the
// replay and the live stream neither miss nor repeat a byte.
func (s *termSession) attach(kick func()) (*termClient, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errTermEnded
	}
	c := &termClient{out: make(chan []byte, termClientQueue), kick: kick}
	s.clients[c] = struct{}{}
	return c, s.ring.snapshot(), nil
}

// detach unregisters a client, if it still is.
func (s *termSession) detach(c *termClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		close(c.out)
	}
}

func (s *termSession) attached() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// redraw asks the program in the foreground to draw itself again (SIGWINCH,
// as a resize would; the kernel sends none when the size did not change):
// what a client attaching to a full-screen program needs.
func (s *termSession) redraw() {
	if pg, err := ptyForeground(s.master); err == nil && pg > 0 {
		_ = syscall.Kill(-pg, syscall.SIGWINCH)
	}
}

func (s *termSession) info() termSessionInfo {
	return termSessionInfo{ID: s.id, N: s.n, Repo: s.repo, Dir: s.dir, StartedAt: s.started, Attached: s.attached()}
}

// list is every live session, oldest first, each with what runs in it: one
// scan of /proc for all of them, read outside the registry's lock.
func (r *termRegistry) list() []termSessionInfo {
	r.mu.Lock()
	all := slices.Clone(r.order)
	r.mu.Unlock()
	out := make([]termSessionInfo, 0, len(all))
	if len(all) == 0 {
		return out
	}
	procs := scanProcs()
	for _, s := range all {
		in := s.info()
		fg, _ := ptyForeground(s.master)
		in.Busy, in.Command = sessionBusy(procs, s.cmd.Process.Pid, fg)
		out = append(out, in)
	}
	return out
}

// procStat is what /proc/<pid>/stat says of one process.
type procStat struct {
	pid, pgrp, sid int
	state          byte
	name           string // stat's comm field
}

// scanProcs reads every process's stat, ordered by pid. An unreadable
// /proc is no processes.
func scanProcs() []procStat {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var out []procStat
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
		if err != nil {
			continue
		}
		if p, ok := parseProcStat(pid, string(data)); ok {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	return out
}

// parseProcStat reads "pid (comm) state ppid pgrp session …". comm may hold
// spaces and parentheses, so the fields after it are found from its last ')'.
func parseProcStat(pid int, stat string) (procStat, bool) {
	open, end := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return procStat{}, false
	}
	f := strings.Fields(stat[end+1:])
	if len(f) < 4 || len(f[0]) != 1 {
		return procStat{}, false
	}
	pgrp, err1 := strconv.Atoi(f[2])
	sid, err2 := strconv.Atoi(f[3])
	if err1 != nil || err2 != nil {
		return procStat{}, false
	}
	return procStat{pid: pid, pgrp: pgrp, sid: sid, state: f[0][0], name: stat[open+1 : end]}, true
}

// sessionBusy says whether a live process other than the shell runs in the
// shell's session, and names it: the terminal's foreground program when
// that is not the shell (fg, its process group), else a background job.
func sessionBusy(procs []procStat, shell, fg int) (busy bool, command string) {
	var fgName, bgName string
	for _, p := range procs {
		if p.sid != shell || p.pid == shell || p.state == 'Z' || p.state == 'X' {
			continue
		}
		busy = true
		if fg > 0 && fg != shell && p.pgrp == fg {
			if fgName == "" || p.pid == fg {
				fgName = procName(p)
			}
		} else if bgName == "" {
			bgName = procName(p)
		}
	}
	if fgName != "" {
		return busy, fgName
	}
	return busy, bgName
}

// procName is the process's comm, stat's copy when the file is gone.
func procName(p procStat) string {
	if b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(p.pid), "comm")); err == nil {
		if n := strings.TrimSpace(string(b)); n != "" {
			return n
		}
	}
	return p.name
}

// termRing keeps the newest len(buf) bytes written to it.
type termRing struct {
	buf  []byte
	next int  // where the next byte goes
	full bool // the ring has wrapped
}

func (r *termRing) write(p []byte) {
	if len(p) >= len(r.buf) {
		copy(r.buf, p[len(p)-len(r.buf):])
		r.next, r.full = 0, true
		return
	}
	n := copy(r.buf[r.next:], p)
	if n < len(p) {
		copy(r.buf, p[n:])
		r.full = true
	}
	r.next = (r.next + len(p)) % len(r.buf)
	if r.next == 0 && len(p) > 0 {
		r.full = true
	}
}

// snapshot is the ring's bytes, oldest first.
func (r *termRing) snapshot() []byte {
	if !r.full {
		return slices.Clone(r.buf[:r.next])
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	return append(out, r.buf[:r.next]...)
}
