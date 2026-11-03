package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/browserport"
)

// hostFake is a Host doing nothing but saying what it was asked.
type hostFake struct {
	mu        sync.Mutex
	ensured   []string // project:img:replace
	stopped   []string
	stopErr   error
	ensureErr error
	running   []string
	runErr    error
	image     string
	isRunning bool
	inspErr   error
	listenErr error
	execErr   error
	execBlock chan struct{}
	reach     []string
}

func (h *hostFake) Name(p string) string { return "name-" + p }
func (h *hostFake) Ensure(_ context.Context, project, _, img string, replace bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := "keep"
	if replace {
		r = "replace"
	}
	h.ensured = append(h.ensured, project+":"+img+":"+r)
	return h.ensureErr
}
func (h *hostFake) Stop(_ context.Context, project string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = append(h.stopped, project)
	return h.stopErr
}
func (h *hostFake) Running(context.Context) ([]string, error) { return h.running, h.runErr }
func (h *hostFake) Inspect(context.Context, string) (string, bool, error) {
	return h.image, h.isRunning, h.inspErr
}
func (h *hostFake) Listen(context.Context, string, string) (net.Listener, []string, error) {
	if h.listenErr != nil {
		return nil, nil, h.listenErr
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	return ln, h.reach, err
}
func (h *hostFake) Exec(ctx context.Context, _ string, _ []string, _ io.Reader, _ io.Writer) error {
	if h.execBlock != nil {
		select {
		case <-h.execBlock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return h.execErr
}

func (h *hostFake) stops() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.stopped...)
}

func hostManager(t *testing.T, h *hostFake, idle time.Duration) *Manager {
	t.Helper()
	m := New(Config{Host: h, IdleAfter: idle, Log: slog.New(slog.DiscardHandler),
		Image: func(context.Context) (string, error) { return "img:1", nil }})
	t.Cleanup(m.Close)
	return m
}

func TestNewDefaults(t *testing.T) {
	m := New(Config{RunsDir: "/r"})
	if m.cfg.IdleAfter != 10*time.Minute || m.cfg.Log == nil || m.cfg.Run == nil || m.cfg.Stream == nil {
		t.Errorf("cfg = %+v", m.cfg)
	}
	if h, ok := m.cfg.Host.(*dockerHost); !ok || h.runsDir != "/r" {
		t.Errorf("host = %#v", m.cfg.Host)
	}
}

func TestSessionFailures(t *testing.T) {
	ctx := context.Background()
	s := browserport.BrowserSession{Project: "P1", Run: "r"}
	boom := errors.New("boom")

	bad := New(Config{Host: &hostFake{}, Log: slog.New(slog.DiscardHandler), Image: func(context.Context) (string, error) { return "", boom }})
	if err := bad.Session(ctx, s, nil); !errors.Is(err, boom) || !strings.Contains(err.Error(), "prepare the browser image") {
		t.Errorf("image = %v", err)
	}
	bad.Close()
	if err := hostManager(t, &hostFake{ensureErr: boom}, time.Hour).Session(ctx, s, nil); !errors.Is(err, boom) {
		t.Errorf("ensure = %v", err)
	}
	if err := hostManager(t, &hostFake{listenErr: boom}, time.Hour).Session(ctx, s, nil); !errors.Is(err, boom) {
		t.Errorf("listen = %v", err)
	}
	err := hostManager(t, &hostFake{execErr: boom}, time.Hour).Session(ctx, s, nil)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "browser session") {
		t.Errorf("exec = %v", err)
	}
}

// The caller going away ends a session quietly, and it stops counting.
func TestSessionEndsWithItsContext(t *testing.T) {
	h := &hostFake{execBlock: make(chan struct{})}
	m := hostManager(t, h, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Session(ctx, browserport.BrowserSession{Project: "P1"}, nil) }()
	waitFor(t, func() bool {
		st, _ := m.Status(context.Background(), "P1")
		return st.Sessions == 1
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session outlived its context")
	}
	if st, _ := m.Status(context.Background(), "P1"); st.Sessions != 0 {
		t.Errorf("sessions = %d", st.Sessions)
	}
}

// Only the sole user may replace a stale browser: a second session finds it
// running and leaves it to the running ones.
func TestEnsureReplacesOnlyWhenAlone(t *testing.T) {
	ctx := context.Background()
	h := &hostFake{}
	m := hostManager(t, h, time.Hour)
	p := m.acquire("P1")
	if err := m.ensure(ctx, "P1", "", p); err != nil {
		t.Fatal(err)
	}
	m.acquire("P1")
	if err := m.ensure(ctx, "P1", "", p); err != nil {
		t.Fatal(err)
	}
	want := "P1:img:1:replace,P1:img:1:keep"
	if got := strings.Join(h.ensured, ","); got != want {
		t.Errorf("ensured = %s", got)
	}
}

func TestIdleStopFiresOnceTheLastSessionLeaves(t *testing.T) {
	h := &hostFake{}
	m := hostManager(t, h, 5*time.Millisecond)
	p := m.acquire("P1")
	p2 := m.acquire("P1")
	m.release("P1", p2)
	if got := func() bool { m.mu.Lock(); defer m.mu.Unlock(); return p.idle != nil }(); got {
		t.Error("idle armed while a session remains")
	}
	m.release("P1", p)
	waitFor(t, func() bool { return len(h.stops()) == 1 })
	if h.stops()[0] != "P1" {
		t.Errorf("stopped = %v", h.stops())
	}
}

func TestAcquireCancelsTheIdleStop(t *testing.T) {
	h := &hostFake{}
	m := hostManager(t, h, time.Hour)
	p := m.acquire("P1")
	m.release("P1", p)
	m.mu.Lock()
	armed := p.idle != nil
	m.mu.Unlock()
	if !armed {
		t.Fatal("idle not armed")
	}
	m.acquire("P1")
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.idle != nil {
		t.Error("a new session left the idle stop armed")
	}
}

// A timer that fires after someone came back stops nothing. Project "marker"
// has a later timer: once it fires, the one under test has had its turn.
func TestIdleTimerRespectsARaceWithANewSession(t *testing.T) {
	h := &hostFake{}
	m := hostManager(t, h, time.Millisecond)
	p := m.project("P1")
	// Between the timer's two looks: it passed the first, and waits for p.mu
	// while a session arrives.
	p.mu.Lock()
	m.mu.Lock()
	m.armIdleLocked("P1", p)
	m.mu.Unlock()
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return p.idle == nil })
	m.mu.Lock()
	p.sessions = 1
	m.mu.Unlock()
	p.mu.Unlock()

	// A superseded timer: re-arming while one is pending.
	stale := m.project("P2")
	m.mu.Lock()
	m.armIdleLocked("P2", stale)
	stale.sessions = 1
	m.armIdleLocked("P2", stale)
	m.mu.Unlock()

	m.cfg.IdleAfter = 50 * time.Millisecond
	marker := m.project("marker")
	m.mu.Lock()
	m.armIdleLocked("marker", marker)
	m.mu.Unlock()
	waitFor(t, func() bool { return len(h.stops()) > 0 })
	if got := h.stops(); len(got) != 1 || got[0] != "marker" {
		t.Errorf("stopped = %v", got)
	}
}

type logBuf struct {
	mu sync.Mutex
	bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.Write(p)
}

func (l *logBuf) has(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Contains(l.Buffer.String(), s)
}

func TestIdleStopFailureIsLogged(t *testing.T) {
	h := &hostFake{stopErr: errors.New("daemon down")}
	var log logBuf
	m := New(Config{Host: h, IdleAfter: time.Millisecond, Log: slog.New(slog.NewTextHandler(&log, nil))})
	defer m.Close()
	p := m.acquire("P1")
	m.release("P1", p)
	waitFor(t, func() bool { return log.has("idle stop failed") })
}

// Browsers a previous engine left behind have no session to count them out.
func TestStartArmsLeftBehindBrowsers(t *testing.T) {
	ctx := context.Background()
	h := &hostFake{running: []string{"left", "busy"}}
	m := hostManager(t, h, 5*time.Millisecond)
	m.acquire("busy")
	m.Start(ctx)
	waitFor(t, func() bool { return len(h.stops()) == 1 })
	if h.stops()[0] != "left" {
		t.Errorf("stopped = %v", h.stops())
	}
	var log logBuf
	bad := New(Config{Host: &hostFake{runErr: errors.New("no docker")}, Log: slog.New(slog.NewTextHandler(&log, nil))})
	bad.Start(ctx)
	if !log.has("listing running browsers failed") {
		t.Errorf("log = %q", log.String())
	}
}

func TestCloseDisarmsEveryTimer(t *testing.T) {
	h := &hostFake{}
	m := hostManager(t, h, time.Hour)
	p := m.acquire("P1")
	m.release("P1", p)
	m.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.idle != nil {
		t.Error("timer left armed")
	}
}

func TestWarmBringsTheBrowserUpAndLetsGo(t *testing.T) {
	h := &hostFake{}
	m := hostManager(t, h, 5*time.Millisecond)
	m.Warm("P1", "Proj")
	waitFor(t, func() bool { return len(h.stops()) == 1 })
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ensured) != 1 || !strings.HasPrefix(h.ensured[0], "P1:img:1") {
		t.Errorf("ensured = %v", h.ensured)
	}
}

func TestWarmFailureIsLogged(t *testing.T) {
	var log logBuf
	m := New(Config{Host: &hostFake{ensureErr: errors.New("no image")}, Log: slog.New(slog.NewTextHandler(&log, nil)), IdleAfter: time.Hour,
		Image: func(context.Context) (string, error) { return "i", nil }})
	defer m.Close()
	m.Warm("P1", "")
	waitFor(t, func() bool { return log.has("warming the project browser failed") })
}

func TestStatus(t *testing.T) {
	ctx := context.Background()
	h := &hostFake{}
	m := hostManager(t, h, time.Hour)
	m.cfg.Tag = func() string { return "img:2" }
	st, err := m.Status(ctx, "P1")
	if err != nil || st.State != "absent" || st.Container != "name-P1" || st.Current != "img:2" || st.Image != "" {
		t.Errorf("absent = %+v, %v", st, err)
	}
	h.image, h.isRunning = "img:1", true
	m.acquire("P1")
	st, _ = m.Status(ctx, "P1")
	if st.State != "running" || st.Image != "img:1" || st.Sessions != 1 {
		t.Errorf("running = %+v", st)
	}
	h.isRunning = false
	if st, _ := m.Status(ctx, "P1"); st.State != "stopped" {
		t.Errorf("stopped = %+v", st)
	}
	boom := errors.New("boom")
	h.inspErr = boom
	if _, err := m.Status(ctx, "P1"); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

// --- docker host -------------------------------------------------------------

func fixedShim(path string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return path, nil }
}

func TestDockerHostFailures(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("no docker")
	spawn := func(context.Context, []string) ([]byte, []byte, int, error) { return nil, nil, 0, boom }
	h := &dockerHost{runsDir: t.TempDir(), run: spawn, shim: fixedShim("/s/apothecary-shim")}
	if err := h.Ensure(ctx, "P1", "", "i", true); !errors.Is(err, boom) {
		t.Errorf("Ensure = %v", err)
	}
	if err := h.Stop(ctx, "P1"); !errors.Is(err, boom) {
		t.Errorf("Stop = %v", err)
	}
	if got, err := h.Running(ctx); !errors.Is(err, boom) || got != nil {
		t.Errorf("Running = %v, %v", got, err)
	}
	if _, _, err := h.Inspect(ctx, "P1"); !errors.Is(err, boom) {
		t.Errorf("Inspect = %v", err)
	}
	h.run = func(context.Context, []string) ([]byte, []byte, int, error) { return nil, []byte(" denied \n"), 1, nil }
	if err := h.Stop(ctx, "P1"); err == nil || err.Error() != "docker stop: exit 1: denied" {
		t.Errorf("exit = %v", err)
	}
	file := filepath.Join(t.TempDir(), "plain")
	os.WriteFile(file, nil, 0o644)
	if err := (&dockerHost{runsDir: file, run: spawn, shim: h.shim}).Ensure(ctx, "P1", "", "i", true); err == nil || errors.Is(err, boom) {
		t.Errorf("mount dir under a file = %v", err)
	}
	// No shim, no browser: the session runner lives in it.
	shimErr := errors.New("shim build failed")
	noShim := &dockerHost{runsDir: t.TempDir(), run: spawn, shim: func(context.Context) (string, error) { return "", shimErr }}
	if err := noShim.Ensure(ctx, "P1", "", "i", true); !errors.Is(err, shimErr) {
		t.Errorf("shim failure = %v", err)
	}
	if err := (&dockerHost{runsDir: t.TempDir(), run: spawn}).Ensure(ctx, "P1", "", "i", true); err == nil || errors.Is(err, boom) {
		t.Errorf("no shim source = %v", err)
	}
}

func TestDockerHostEnsureSteps(t *testing.T) {
	ctx := context.Background()
	shim := fixedShim("/s/apothecary-shim")
	fail := func(word string) func(context.Context, []string) ([]byte, []byte, int, error) {
		return func(_ context.Context, args []string) ([]byte, []byte, int, error) {
			if args[0] == "ps" {
				return []byte("c1\told\t/s/apothecary-shim\n"), nil, 0, nil
			}
			if args[0] == word {
				return nil, []byte("nope"), 1, nil
			}
			return nil, nil, 0, nil
		}
	}
	if err := (&dockerHost{runsDir: t.TempDir(), run: fail("rm"), shim: shim}).Ensure(ctx, "P1", "", "new", true); err == nil {
		t.Error("rm failure swallowed")
	}
	if err := (&dockerHost{runsDir: t.TempDir(), run: fail("start"), shim: shim}).Ensure(ctx, "P1", "", "new", false); err == nil {
		t.Error("start failure swallowed")
	}
	if err := (&dockerHost{runsDir: t.TempDir(), run: fail("run"), shim: shim}).Ensure(ctx, "P1", "", "new", true); err == nil {
		t.Error("run failure swallowed")
	}
	// The container is locked down whoever asks; the name is a label only.
	var run []string
	h := &dockerHost{runsDir: t.TempDir(), shim: shim, run: func(_ context.Context, args []string) ([]byte, []byte, int, error) {
		if args[0] == "run" {
			run = args
		}
		return nil, nil, 0, nil
	}}
	if err := h.Ensure(ctx, "P1", "My App", "img", true); err != nil {
		t.Fatal(err)
	}
	line := strings.Join(run, " ")
	for _, want := range []string{"--name apo-browser-p1", "--network none", "--cap-drop ALL", "--security-opt no-new-privileges",
		"--label apothecary.project=P1", "--label apothecary.project.name=My App", "-v /s:/opt/apothecary/shim:ro"} {
		if !strings.Contains(line, want) {
			t.Errorf("run missing %q: %s", want, line)
		}
	}
}

// What the container was created from is read off its labels: a new image or
// a new shim makes it stale, and so does a container older than the labels.
func TestDockerHostStaleness(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, ps string
		replace  bool
		recreate bool
	}{
		{"current", "c1\timg@sha256:a\t/s/apothecary-shim", true, false},
		{"new image", "c1\timg@sha256:old\t/s/apothecary-shim", true, true},
		{"new shim", "c1\timg@sha256:a\t/old/apothecary-shim", true, true},
		{"new shim, busy", "c1\timg@sha256:a\t/old/apothecary-shim", false, false},
		{"unlabelled", "c1\t\t", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			h := &dockerHost{runsDir: t.TempDir(), shim: fixedShim("/s/apothecary-shim"), run: func(_ context.Context, args []string) ([]byte, []byte, int, error) {
				calls = append(calls, args[0])
				if args[0] == "ps" {
					return []byte(c.ps + "\n"), nil, 0, nil
				}
				return nil, nil, 0, nil
			}}
			if err := h.Ensure(ctx, "P1", "", "img@sha256:a", c.replace); err != nil {
				t.Fatal(err)
			}
			want := "ps start"
			if c.recreate {
				want = "ps rm run"
			}
			if got := strings.Join(calls, " "); got != want {
				t.Errorf("calls = %q, want %q", got, want)
			}
		})
	}
}

func TestDockerHostRunningAndInspect(t *testing.T) {
	ctx := context.Background()
	out := "p1\np2\n"
	h := &dockerHost{run: func(context.Context, []string) ([]byte, []byte, int, error) { return []byte(out), nil, 0, nil }}
	if got, err := h.Running(ctx); err != nil || strings.Join(got, ",") != "p1,p2" {
		t.Errorf("running = %v, %v", got, err)
	}
	// docker reports a digest-pinned image by its repository; the label has the reference.
	out = "exited\tmcr.example/mcp\tmcr.example/mcp:v1@sha256:a\n"
	if img, running, err := h.Inspect(ctx, "P1"); err != nil || img != "mcr.example/mcp:v1@sha256:a" || running {
		t.Errorf("inspect = %q %v %v", img, running, err)
	}
	out = "running\tapothecary/browser:old\t\n"
	if img, running, err := h.Inspect(ctx, "P1"); err != nil || img != "apothecary/browser:old" || !running {
		t.Errorf("unlabelled = %q %v %v", img, running, err)
	}
	out = ""
	if img, running, err := h.Inspect(ctx, "P1"); err != nil || img != "" || running {
		t.Errorf("absent = %q %v %v", img, running, err)
	}
	if h.Name("ABC") != "apo-browser-abc" {
		t.Errorf("name = %q", h.Name("ABC"))
	}
}

// The image is pulled once: a daemon that has it is not asked again.
func TestPulled(t *testing.T) {
	ctx := context.Background()
	var calls []string
	have := false
	pullCode, pullErr := 0, error(nil)
	run := func(_ context.Context, args []string) ([]byte, []byte, int, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "image":
			if have {
				return []byte("sha256:x"), nil, 0, nil
			}
			return nil, []byte("No such image"), 1, nil
		case "pull":
			return nil, []byte(" denied \n"), pullCode, pullErr
		}
		return nil, nil, 0, nil
	}
	pull := Pulled("ref@sha256:a", run)
	if ref, err := pull(ctx); err != nil || ref != "ref@sha256:a" || len(calls) != 2 || calls[1] != "pull --quiet ref@sha256:a" {
		t.Errorf("missing = %q %v %v", ref, err, calls)
	}
	have, calls = true, nil
	if ref, err := pull(ctx); err != nil || ref != "ref@sha256:a" || len(calls) != 1 {
		t.Errorf("present = %q %v %v", ref, err, calls)
	}
	have, pullCode = false, 1
	if _, err := pull(ctx); err == nil || err.Error() != "docker pull ref@sha256:a: exit 1: denied" {
		t.Errorf("refused = %v", err)
	}
	boom := errors.New("no docker")
	pullCode, pullErr = 0, boom
	if _, err := pull(ctx); !errors.Is(err, boom) {
		t.Errorf("spawn = %v", err)
	}
}

func TestDockerHostListen(t *testing.T) {
	ctx := context.Background()
	long := &dockerHost{runsDir: "/" + strings.Repeat("x", 120)}
	if _, _, err := long.Listen(ctx, "P1", "s"); err == nil || !strings.Contains(err.Error(), "longer than a unix socket allows") {
		t.Errorf("long = %v", err)
	}
	// No directory: the session's listener cannot open.
	missing := &dockerHost{runsDir: "/nonexistent-apo"}
	if _, _, err := missing.Listen(ctx, "P1", "s"); err == nil || !strings.Contains(err.Error(), "proxy socket") {
		t.Errorf("no dir = %v", err)
	}
	dir, err := os.MkdirTemp("", "br")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	h := &dockerHost{runsDir: dir}
	os.MkdirAll(h.dir("P1"), 0o755)
	sock := filepath.Join(h.dir("P1"), "s-abc.sock")
	os.WriteFile(sock, nil, 0o644) // stale, from a crash
	ln, reach, err := h.Listen(ctx, "P1", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reach, " ") != "--sock "+sock {
		t.Errorf("reach = %v", reach)
	}
	if info, _ := os.Stat(sock); info.Mode().Perm() != 0o666 {
		t.Errorf("mode = %v: the container's user could not connect", info.Mode())
	}
	if err := ln.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Error("the socket outlived its listener")
	}
}

func TestDockerHostExecPassesTheSession(t *testing.T) {
	var args []string
	h := &dockerHost{stream: func(_ context.Context, a []string, _ io.Reader, _ io.Writer) error { args = a; return nil }}
	if err := h.Exec(context.Background(), "P1", []string{"shim", "x"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "exec -i apo-browser-p1 shim x" {
		t.Errorf("args = %v", args)
	}
}

// dockerRun and dockerStream on a stand-in binary ahead of PATH: their job is
// the exit-code contract, and no docker daemon is involved.
func fakeDockerBin(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestDockerRun(t *testing.T) {
	fakeDockerBin(t, `echo "out $1"; echo err >&2; exit 4`)
	out, errb, code, err := dockerRun(context.Background(), []string{"ps"})
	if err != nil || code != 4 || string(out) != "out ps\n" || string(errb) != "err\n" {
		t.Errorf("run = %q %q %d %v", out, errb, code, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, _, _, err := dockerRun(context.Background(), []string{"ps"}); err == nil {
		t.Error("a docker that does not exist must be an error")
	}
}

func TestDockerStream(t *testing.T) {
	fakeDockerBin(t, `cat; echo gone >&2; exit 2`)
	var out bytes.Buffer
	err := dockerStream(context.Background(), []string{"exec"}, strings.NewReader("in"), &out)
	if err == nil || !strings.Contains(err.Error(), "gone") || out.String() != "in" {
		t.Errorf("err = %v, out = %q", err, out.String())
	}
	fakeDockerBin(t, `exit 3`)
	if err := dockerStream(context.Background(), nil, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("quiet failure = %v", err)
	}
	fakeDockerBin(t, `exit 0`)
	if err := dockerStream(context.Background(), nil, strings.NewReader(""), io.Discard); err != nil {
		t.Errorf("clean = %v", err)
	}
}

// --- Kubernetes host ---------------------------------------------------------

type kubeCalls struct {
	mu    sync.Mutex
	calls []string
	stdin [][]byte
	pod   string
	pods  string
	fail  string
}

func (k *kubeCalls) Run(_ context.Context, _ string, args []string, stdin []byte, _ io.Writer, _ ...string) ([]byte, []byte, int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	line := strings.Join(args, " ")
	k.calls = append(k.calls, line)
	k.stdin = append(k.stdin, stdin)
	if k.fail != "" && strings.Contains(line, k.fail) {
		return nil, []byte("boom"), 1, nil
	}
	switch {
	case strings.Contains(line, "get --request-timeout=30s pod "):
		return []byte(k.pod), nil, 0, nil
	case strings.Contains(line, "pods -o json"):
		return []byte(k.pods), nil, 0, nil
	}
	return nil, nil, 0, nil
}

func (k *kubeCalls) saw(sub string) bool {
	for _, c := range k.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func k8sHost(k *kubeCalls) *K8sHost {
	return &K8sHost{Kube: &kube.Client{Namespace: "ws", R: k}, ShimImage: "shim:1", PullPolicy: "Always"}
}

const runningPod = `{"spec":{"initContainers":[{"name":"shim-seed","image":"shim:1"}],"containers":[{"name":"browser","image":"img:1"}]},"status":{"phase":"Running"}}`

func TestK8sEnsure(t *testing.T) {
	ctx := context.Background()
	oldShim := strings.Replace(runningPod, "shim:1", "shim:0", 1)
	unseeded := `{"spec":{"containers":[{"name":"browser","image":"img:1"}]},"status":{"phase":"Running"}}`
	cases := []struct {
		name    string
		pod     string
		img     string
		replace bool
		create  bool
	}{
		{"absent", "", "img:1", false, true},
		{"current", runningPod, "img:1", true, false},
		{"stale, replace", runningPod, "img:2", true, true},
		{"stale, busy", runningPod, "img:2", false, false},
		{"stale shim, replace", oldShim, "img:1", true, true},
		{"stale shim, busy", oldShim, "img:1", false, false},
		{"no shim seed, replace", unseeded, "img:1", true, true},
		{"ended", `{"spec":{"containers":[{"image":"img:1"}]},"status":{"phase":"Failed"}}`, "img:1", false, true},
		{"finished", `{"spec":{"containers":[{"image":"img:1"}]},"status":{"phase":"Succeeded"}}`, "img:1", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := &kubeCalls{pod: c.pod}
			if err := k8sHost(k).Ensure(ctx, "P1", "My App", c.img, c.replace); err != nil {
				t.Fatal(err)
			}
			if got := k.saw("create"); got != c.create {
				t.Errorf("create = %v: %v", got, k.calls)
			}
			if got := k.saw("delete pod"); got != (c.pod != "" && c.create) {
				t.Errorf("delete = %v: %v", got, k.calls)
			}
			if !k.saw("wait --for=condition=Ready pod/apo-browser-p1") {
				t.Errorf("no wait: %v", k.calls)
			}
		})
	}
}

func TestK8sEnsureManifest(t *testing.T) {
	k := &kubeCalls{}
	if err := k8sHost(k).Ensure(context.Background(), "P1", "My App", "img:1", false); err != nil {
		t.Fatal(err)
	}
	var created string
	for i, c := range k.calls {
		if strings.HasPrefix(c, "--namespace ws create") {
			created = string(k.stdin[i])
		}
	}
	for _, want := range []string{`"image":"img:1"`, `"imagePullPolicy":"Always"`, `"apothecary.io/project-name":"My App"`, `"apothecary.io/browser":"p1"`,
		`"allowPrivilegeEscalation":false`, `"drop":["ALL"]`, `"automountServiceAccountToken":false`, `"medium":"Memory"`,
		`"initContainers":[{"name":"shim-seed","image":"shim:1"`, `"args":["seed","/opt/apothecary/shim"]`,
		`"command":["sleep","infinity"]`, `"mountPath":"/opt/apothecary/shim","readOnly":true`} {
		if !strings.Contains(created, want) {
			t.Errorf("pod missing %s:\n%s", want, created)
		}
	}
}

func TestK8sEnsureFailures(t *testing.T) {
	ctx := context.Background()
	for _, fail := range []string{"apply", "get --request-timeout", "delete pod", "create", "wait"} {
		k := &kubeCalls{fail: fail, pod: `{"spec":{"containers":[{"image":"old"}]},"status":{"phase":"Running"}}`}
		if err := k8sHost(k).Ensure(ctx, "P1", "", "new", true); err == nil {
			t.Errorf("failure at %q went unreported", fail)
		}
	}
}

// No ingress at all; egress to the engine's mux and the DNS naming it, and
// nothing else: the docker host's --network none, with one door.
func TestK8sNetworkPolicy(t *testing.T) {
	h := &K8sHost{EngineNamespace: "apo"}
	np := h.networkPolicy("P1")
	b, _ := json.Marshal(np)
	s := string(b)
	for _, want := range []string{`"policyTypes":["Ingress","Egress"]`, `"apothecary.io/browser":"p1"`, `"kubernetes.io/metadata.name":"apo"`, `"port":7071`, `"k8s-app":"kube-dns"`} {
		if !strings.Contains(s, want) {
			t.Errorf("policy missing %s: %s", want, s)
		}
	}
	if len(np.Spec.Ingress) != 0 {
		t.Errorf("ingress = %+v", np.Spec.Ingress)
	}
	m, err := NewSessionMux("127.0.0.1:0", "engine.svc:9123")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	h = &K8sHost{Mux: m, EngineCIDR: "192.168.1.5/32", EngineNamespace: "ignored"}
	np = h.networkPolicy("P1")
	to := np.Spec.Egress[0]
	if to.Ports[0].Port != 9123 || to.To[0].IPBlock == nil || to.To[0].IPBlock.CIDR != "192.168.1.5/32" || to.To[0].PodSelector != nil {
		t.Errorf("engine rule = %+v", to)
	}
}

func TestK8sStopRunningInspect(t *testing.T) {
	ctx := context.Background()
	k := &kubeCalls{pod: runningPod, pods: `{"items":[
		{"metadata":{"labels":{"apothecary.io/project":"p1"}},"status":{"phase":"Running"}},
		{"metadata":{"labels":{"apothecary.io/project":"p2"}},"status":{"phase":"Pending"}}]}`}
	h := k8sHost(k)
	if h.Name("ABC") != "apo-browser-abc" {
		t.Errorf("name = %q", h.Name("ABC"))
	}
	if err := h.Stop(ctx, "P1"); err != nil || !k.saw("delete pod apo-browser-p1") {
		t.Errorf("stop = %v, %v", err, k.calls)
	}
	got, err := h.Running(ctx)
	if err != nil || strings.Join(got, ",") != "p1" {
		t.Errorf("running = %v, %v", got, err)
	}
	img, running, err := h.Inspect(ctx, "P1")
	if err != nil || img != "img:1" || !running {
		t.Errorf("inspect = %q %v %v", img, running, err)
	}
	k.pod = ""
	if img, running, err := h.Inspect(ctx, "P1"); err != nil || img != "" || running {
		t.Errorf("absent = %q %v %v", img, running, err)
	}
	k.fail = "pod"
	if _, _, err := h.Inspect(ctx, "P1"); err == nil {
		t.Error("API failure read as absent")
	}
	k.fail = "pods"
	if _, err := h.Running(ctx); err == nil {
		t.Error("API failure read as none running")
	}
}

func TestK8sListen(t *testing.T) {
	ctx := context.Background()
	if _, _, err := (&K8sHost{}).Listen(ctx, "P1", "s"); err == nil || !strings.Contains(err.Error(), "APOTHECARY_K8S_BROWSER_PROXY_LISTEN") {
		t.Errorf("no mux = %v", err)
	}
	m, err := NewSessionMux("127.0.0.1:0", "engine.svc:7071")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ln, reach, err := (&K8sHost{Mux: m}).Listen(ctx, "P1", "s")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if len(reach) != 4 || reach[0] != "--engine" || reach[1] != "engine.svc:7071" || reach[2] != "--token" || len(reach[3]) != 48 {
		t.Errorf("reach = %v", reach)
	}
}

// kubectl is a stand-in executable named by Client.Bin: no real kubectl runs.
func standIn(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-kubectl")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestK8sExec(t *testing.T) {
	ctx := context.Background()
	h := &K8sHost{Kube: &kube.Client{Namespace: "ws", Bin: standIn(t, `echo "$@"; cat; exit 0`)}}
	var out bytes.Buffer
	if err := h.Exec(ctx, "P1", []string{"shim", "go"}, strings.NewReader("stdin"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--namespace ws exec -i --pod-running-timeout=5s apo-browser-p1 -c browser -- shim go") ||
		!strings.HasSuffix(out.String(), "stdin") {
		t.Errorf("out = %q", out.String())
	}
	h.Kube.Bin = standIn(t, `echo denied >&2; exit 1`)
	if err := h.Exec(ctx, "P1", nil, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("err = %v", err)
	}
	h.Kube.Bin = standIn(t, `exit 1`)
	if err := h.Exec(ctx, "P1", nil, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("quiet = %v", err)
	}
}

func TestKubectlBin(t *testing.T) {
	if kubectlBin(&kube.Client{}) != "kubectl" || kubectlBin(&kube.Client{Bin: "/x"}) != "/x" {
		t.Error("bin choice is wrong")
	}
}
