package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// svcShim runs the shim's service subcommand against a private state dir, the
// request (if any) on stdin.
type svcShim struct {
	t    *testing.T
	dir  string
	repo string
	env  []string
}

func newSvcShim(t *testing.T) *svcShim {
	t.Helper()
	s := &svcShim{t: t, dir: t.TempDir(), repo: t.TempDir()}
	t.Cleanup(func() {
		// Whatever a test left running goes with it.
		out, _ := s.run(nil, "status")
		var all []serviceState
		_ = json.Unmarshal(out, &all)
		for _, st := range all {
			if st.live() {
				s.run(&serviceRequest{Name: st.Name}, "stop")
			}
		}
	})
	return s
}

// fastSvcShim is one whose supervisors notice things in milliseconds.
func fastSvcShim(t *testing.T) *svcShim {
	t.Helper()
	s := newSvcShim(t)
	s.env = []string{shimFastSupport + "=1"}
	return s
}

func (s *svcShim) cmd(args ...string) *exec.Cmd {
	cmd := shimCmd(s.t, append([]string{"service"}, args...)...)
	cmd.Dir = s.repo
	cmd.Env = append(append(cmd.Env, "APOTHECARY_SERVICES="+s.dir, "APOTHECARY_CMD=run:stage"), s.env...)
	return cmd
}

func (s *svcShim) run(req *serviceRequest, args ...string) ([]byte, error) {
	cmd := s.cmd(args...)
	if req != nil {
		data, _ := json.Marshal(req)
		cmd.Stdin = strings.NewReader(string(data))
	}
	return cmd.Output()
}

func (s *svcShim) state(req *serviceRequest, args ...string) serviceState {
	s.t.Helper()
	out, err := s.run(req, args...)
	if err != nil {
		s.t.Fatalf("service %v: %v\n%s", args, err, out)
	}
	var st serviceState
	if err := json.Unmarshal(out, &st); err != nil {
		s.t.Fatalf("service %v: %v: %q", args, err, out)
	}
	return st
}

func (s *svcShim) logs(name string) string {
	out, _ := s.run(nil, "logs", "--tail", "50", name)
	return string(out)
}

// A start that keeps running is the service: healthy once its probe
// answers, still up after the exec that started it is gone, stopped by
// signalling its group — and started twice, it is started once.
func TestServiceKeepsRunning(t *testing.T) {
	s := newSvcShim(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	req := &serviceRequest{Name: "app", Start: `echo "up in $PWD as $APOTHECARY_SERVICE marker=${APOTHECARY_CMD:-none}"; exec sleep 60`,
		Health: serviceHealth{TCP: port}, Timeout: "20s", By: "stage", Owner: "main", StageID: "up"}

	st := s.state(req, "start", "--wait")
	if st.Phase != svcHealthy || !st.Launched || st.By != "stage" || st.Owner != "main" || st.StageID != "up" || st.PGID == 0 {
		t.Fatalf("start = %+v", st)
	}
	if again := s.state(req, "start", "--wait"); again.Launched || again.Phase != svcHealthy || again.PGID != st.PGID {
		t.Errorf("a second start = %+v, want the running one left alone", again)
	}
	log := s.logs("app")
	// The shell reports the physical cwd; a temp dir may sit behind a
	// symlink (macOS: /var -> /private/var).
	real, _ := filepath.EvalSymlinks(s.repo)
	if !strings.Contains(log, "up in "+s.repo+" as app marker=none") && !strings.Contains(log, "up in "+real+" as app marker=none") {
		t.Errorf("the start ran in the checkout, named, without the stage marker; log:\n%s", log)
	}
	if !groupAlive(st.PGID) {
		t.Fatal("the service died with the exec that started it")
	}

	out, err := s.run(nil, "status")
	var all []serviceState
	if err != nil || json.Unmarshal(out, &all) != nil || len(all) != 1 || all[0].Phase != svcHealthy {
		t.Fatalf("status = %s, %v", out, err)
	}

	stopped := s.state(&serviceRequest{Name: "app"}, "stop")
	if stopped.Phase != svcStopped || stopped.EndedAt == nil {
		t.Fatalf("stop = %+v", stopped)
	}
	waitGone(t, st.PGID)
	if again := s.state(&serviceRequest{Name: "app"}, "stop"); again.Phase != svcStopped {
		t.Errorf("stopping a stopped service = %+v", again)
	}
}

// A start that exits 0 left the service in the background: with no probe
// it is up at once, and stop runs the stop script and clears what is left
// of the group.
func TestServiceDaemonizes(t *testing.T) {
	s := newSvcShim(t)
	marker := filepath.Join(s.repo, "stopped")
	req := &serviceRequest{Name: "db", Start: `sleep 60 >/dev/null 2>&1 & echo started`,
		Stop: "echo bye > " + marker, By: "human"}
	st := s.state(req, "start", "--wait")
	if st.Phase != svcHealthy || st.ExitCode == nil || *st.ExitCode != 0 {
		// The probe-less grace may beat the start's exit: wait for it.
		time.Sleep(300 * time.Millisecond)
		out, _ := s.run(nil, "status", "db")
		var all []serviceState
		_ = json.Unmarshal(out, &all)
		if len(all) != 1 || all[0].Phase != svcHealthy {
			t.Fatalf("start = %+v; status %s", st, out)
		}
		st = all[0]
	}
	if !groupAlive(st.PGID) {
		t.Fatal("the background process is gone")
	}
	if got := s.state(req, "stop"); got.Phase != svcStopped {
		t.Fatalf("stop = %+v", got)
	}
	if data, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(data)) != "bye" {
		t.Errorf("stop script did not run: %q %v", data, err)
	}
	waitGone(t, st.PGID)
}

// A start that fails is over: exited, with its code and its output in the
// log.
func TestServiceStartFails(t *testing.T) {
	s := newSvcShim(t)
	st := s.state(&serviceRequest{Name: "app", Start: "echo boom >&2; exit 3", Health: serviceHealth{TCP: freePort(t)}}, "start", "--wait")
	if st.Phase != svcExited || st.ExitCode == nil || *st.ExitCode != 3 || !strings.Contains(st.LastError, "exited with 3") {
		t.Fatalf("start = %+v", st)
	}
	if log := s.logs("app"); !strings.Contains(log, "boom") {
		t.Errorf("log = %q", log)
	}
}

// Never healthy within its timeout: unhealthy, and still running. A start
// of an unhealthy service starts it again.
func TestServiceNeverHealthy(t *testing.T) {
	s := newSvcShim(t)
	req := &serviceRequest{Name: "app", Start: "exec sleep 60", Health: serviceHealth{TCP: freePort(t)}, Timeout: "2s"}
	st := s.state(req, "start", "--wait")
	if st.Phase != svcUnhealthy || !strings.Contains(st.LastError, "not healthy within 2s") {
		t.Fatalf("start = %+v", st)
	}
	if !groupAlive(st.PGID) {
		t.Error("an unhealthy service is left running")
	}
	again := s.state(req, "start")
	if !again.Launched || again.PGID == st.PGID {
		t.Errorf("start of an unhealthy service = %+v, want a fresh one", again)
	}
	waitGone(t, st.PGID)
}

// A supervisor that is gone (killed with the container's processes, say)
// leaves a state that reads as exited, not as up.
func TestServiceSupervisorGone(t *testing.T) {
	s := newSvcShim(t)
	st := s.state(&serviceRequest{Name: "app", Start: "exec sleep 60"}, "start", "--wait")
	if st.Phase != svcHealthy {
		t.Fatalf("start = %+v", st)
	}
	_ = syscall.Kill(st.PID, syscall.SIGKILL)
	_ = syscall.Kill(-st.PGID, syscall.SIGKILL)
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _ := s.run(nil, "status", "app")
		var all []serviceState
		_ = json.Unmarshal(out, &all)
		if len(all) == 1 && all[0].Phase == svcExited && strings.Contains(all[0].LastError, "supervisor is gone") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status after the supervisor died = %s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for groupAlive(pgid) {
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still alive", pgid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestProbeService(t *testing.T) {
	code := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code == http.StatusFound {
			w.Header().Set("Location", "/login")
		}
		w.WriteHeader(code)
	}))
	defer srv.Close()
	for _, c := range []struct {
		code int
		ok   bool
	}{{200, true}, {204, true}, {302, true}, {404, false}, {503, false}} {
		code = c.code
		if ok, err := probeService(serviceRequest{Health: serviceHealth{HTTP: srv.URL + "/health"}}); ok != c.ok {
			t.Errorf("http %d: ok=%v err=%v", c.code, ok, err)
		}
	}
	port, _ := strconv.Atoi(srv.URL[strings.LastIndexByte(srv.URL, ':')+1:])
	if ok, err := probeService(serviceRequest{Health: serviceHealth{TCP: port}}); !ok {
		t.Errorf("tcp: %v", err)
	}
	if ok, _ := probeService(serviceRequest{Health: serviceHealth{TCP: freePort(t)}}); ok {
		t.Error("tcp on a closed port is not healthy")
	}
	dir := t.TempDir()
	if ok, err := probeService(serviceRequest{Dir: dir, Health: serviceHealth{Command: "true"}}); !ok {
		t.Errorf("command true: %v", err)
	}
	if ok, err := probeService(serviceRequest{Dir: dir, Health: serviceHealth{Command: "echo not yet; exit 1"}}); ok || !strings.Contains(err.Error(), "not yet") {
		t.Errorf("command exit 1: ok=%v err=%v", ok, err)
	}
}

func TestTailLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	var b strings.Builder
	for i := range 10 {
		b.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(p)
	defer f.Close()
	if got := tailLines(f, 3); got != "line 7\nline 8\nline 9\n" {
		t.Errorf("tail 3 = %q", got)
	}
}

// The sidecar serves the same states and logs without an exec.
func TestServiceSidecarRoutes(t *testing.T) {
	dir := t.TempDir()
	old := servicesDir
	servicesDir = dir
	t.Cleanup(func() { servicesDir = old })
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveServiceState(serviceState{Name: "app", Phase: svcStopped, By: "human"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "output.log"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", serveServiceStatus)
	mux.HandleFunc("GET /services/{name}/log", serveServiceLog)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/services", nil))
	var all []serviceState
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &all) != nil || len(all) != 1 || all[0].By != "human" {
		t.Errorf("GET /services = %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/services/app/log?tail=2", nil))
	if rec.Code != 200 || rec.Body.String() != "b\nc\n" {
		t.Errorf("GET log = %d %q", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/services/..%2Fetc/log", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a name that is not a slug = %d", rec.Code)
	}
}

// status is one service's state as `service status` prints it.
func (s *svcShim) status(name string) serviceState {
	s.t.Helper()
	out, err := s.run(nil, "status", name)
	var all []serviceState
	if err != nil || json.Unmarshal(out, &all) != nil || len(all) != 1 {
		s.t.Fatalf("status %s = %s, %v", name, out, err)
	}
	return all[0]
}

func (s *svcShim) waitPhase(name, phase string) serviceState {
	s.t.Helper()
	var st serviceState
	waitFor(s.t, func() bool {
		st = s.status(name)
		return st.Phase == phase
	})
	return st
}

func TestRunServiceRefusesBadArguments(t *testing.T) {
	s := newSvcShim(t)
	for _, args := range [][]string{nil, {"nope"}, {"logs"}, {"logs", "a", "b"}, {"supervise"}, {"supervise", "a", "b"}} {
		r := runShim(t, "", s.cmd(args...))
		if r.code != 2 || !strings.Contains(r.stderr, "usage: shim service") {
			t.Errorf("service %v = %+v", args, r)
		}
	}
	for _, sub := range []string{"start", "stop"} {
		if r := runShim(t, "{nope", s.cmd(sub)); r.code != 2 || !strings.Contains(r.stderr, "reading the service from stdin") {
			t.Errorf("%s with a bad request = %+v", sub, r)
		}
		if r := runShim(t, `{"name":"Bad Name","start":"x"}`, s.cmd(sub)); r.code != 1 || !strings.Contains(r.stderr, "service: ") {
			t.Errorf("%s of a bad name = %+v", sub, r)
		}
	}
	if r := runShim(t, "", s.cmd("status", "Bad Name")); r.code != 1 || !strings.HasPrefix(r.stderr, "service: ") {
		t.Errorf("status of a bad name = %+v", r)
	}
	if r := runShim(t, "", s.cmd("logs", "Bad Name")); r.code != 1 || !strings.HasPrefix(r.stderr, "service: ") {
		t.Errorf("logs of a bad name = %+v", r)
	}
	if r := runShim(t, "", s.cmd("status")); r.code != 0 || strings.TrimSpace(r.stdout) != "[]" {
		t.Errorf("status of nothing = %+v", r)
	}
}

// A healthy service whose probe keeps failing turns unhealthy, stays so while
// it is probed, and comes back when its probe does; its stop script runs on
// the way out; the log has the story.
func TestServiceHealthOverItsLife(t *testing.T) {
	s := fastSvcShim(t)
	up := filepath.Join(s.repo, "up")
	marker := filepath.Join(s.repo, "stopped")
	req := &serviceRequest{Name: "app", Start: "exec sleep 60", Stop: "echo bye > " + marker,
		Health: serviceHealth{Command: "test -f " + up}, Interval: "5ms", Timeout: "20s"}

	st := s.state(req, "start")
	if st.Phase != svcStarting || !st.Launched || st.PGID == 0 {
		t.Fatalf("start = %+v", st)
	}
	if err := os.WriteFile(up, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s.waitPhase("app", svcHealthy)
	if err := os.Remove(up); err != nil {
		t.Fatal(err)
	}
	down := s.waitPhase("app", svcUnhealthy)
	if !strings.Contains(down.LastError, "health command") || down.LastProbeOK {
		t.Errorf("unhealthy = %+v", down)
	}
	// It stays so, and keeps being probed.
	waitFor(t, func() bool {
		st := s.status("app")
		return st.Phase == svcUnhealthy && st.LastProbeAt.After(*down.LastProbeAt)
	})
	if err := os.WriteFile(up, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if back := s.waitPhase("app", svcHealthy); back.LastError != "" {
		t.Errorf("recovered = %+v", back)
	}
	if got := s.state(&serviceRequest{Name: "app"}, "stop"); got.Phase != svcStopped {
		t.Fatalf("stop = %+v", got)
	}
	waitGone(t, st.PGID)
	if data, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(data)) != "bye" {
		t.Errorf("the stop script did not run: %q, %v", data, err)
	}
	log := s.logs("app")
	for _, want := range []string{"app is healthy", "app is unhealthy", "stopping app", "app stopped"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

// --restart stops what runs and starts it afresh; a service already starting
// or up is otherwise left alone.
func TestServiceRestart(t *testing.T) {
	s := fastSvcShim(t)
	req := &serviceRequest{Name: "app", Start: "exec sleep 60"}
	first := s.state(req, "start", "--wait")
	second := s.state(req, "start", "--restart", "--wait")
	if !second.Launched || second.PGID == first.PGID || second.Phase != svcHealthy {
		t.Fatalf("restart = %+v, first %+v", second, first)
	}
	waitGone(t, first.PGID)
}

// A health check still failing at its deadline is unhealthy, with why.
func TestServiceDeadlineWithAFailingProbe(t *testing.T) {
	s := fastSvcShim(t)
	st := s.state(&serviceRequest{Name: "app", Start: "exec sleep 60", Timeout: "30ms",
		Health: serviceHealth{Command: "echo not yet; exit 1"}}, "start", "--wait")
	if st.Phase != svcUnhealthy || !strings.Contains(st.LastError, "not healthy within 30ms") {
		t.Errorf("start = %+v", st)
	}
}

// The supervisor is what `service start` detaches; run by hand it has
// nothing to do without a spec, and records a start that cannot run.
func TestSuperviseWithoutWhatItNeeds(t *testing.T) {
	s := newSvcShim(t)
	if r := runShim(t, "", s.cmd("supervise", "ghost")); r.code != 1 || !strings.Contains(r.stderr, "no spec for ghost") {
		t.Errorf("no spec = %+v", r)
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := `{"name":"app","start":"true","dir":"` + filepath.Join(s.repo, "missing") + `"}`
	if err := os.WriteFile(filepath.Join(s.dir, "app", "spec.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runShim(t, "", s.cmd("supervise", "app")); r.code != 1 {
		t.Errorf("a start that cannot run = %+v", r)
	}
	if st := s.status("app"); st.Phase != svcExited || !strings.HasPrefix(st.LastError, "start: ") {
		t.Errorf("state = %+v", st)
	}
}

// startGroup is a process group of its own, standing in for what a service
// left behind; it is reaped when it dies, so groupAlive tells.
func startGroup(t *testing.T, script string) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := stdout.Read(make([]byte, 1)); err != nil { // its traps are set
		t.Fatal(err)
	}
	go cmd.Wait()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd.Process.Pid
}

func recordService(t *testing.T, st serviceState, spec *serviceRequest) {
	t.Helper()
	if err := os.MkdirAll(serviceDir(st.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveServiceState(st); err != nil {
		t.Fatal(err)
	}
	if spec != nil {
		data, _ := json.Marshal(spec)
		if err := os.WriteFile(filepath.Join(serviceDir(st.Name), "spec.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// With its supervisor gone, stop does the supervisor's part: the stop script
// the service was started with, then what is left of its group.
func TestStopWithTheSupervisorGone(t *testing.T) {
	withServicesDir(t)
	repo := t.TempDir()
	marker := filepath.Join(repo, "stopped")
	pgid := startGroup(t, "echo up; exec sleep 60")
	now := time.Now()
	recordService(t, serviceState{Name: "app", Phase: svcHealthy, PID: 999999, PGID: pgid, StartedAt: &now},
		&serviceRequest{Name: "app", Stop: "echo bye > " + marker})

	st, err := stopService(serviceRequest{Name: "app", Dir: repo})
	if err != nil || st.Phase != svcStopped || st.EndedAt == nil {
		t.Fatalf("stop = %+v, %v", st, err)
	}
	waitGone(t, pgid)
	if data, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(data)) != "bye" {
		t.Errorf("stop script: %q, %v", data, err)
	}
	log, _ := os.ReadFile(filepath.Join(serviceDir("app"), "output.log"))
	if !strings.Contains(string(log), "running its stop script") {
		t.Errorf("log = %q", log)
	}
	if got, ok := loadServiceState("app"); !ok || got.Phase != svcStopped {
		t.Errorf("recorded = %+v", got)
	}
}

// A service that never got as far as starting has no stop script to run; a
// script given with the request wins over the one it was started with.
func TestStopScriptChoice(t *testing.T) {
	withServicesDir(t)
	repo := t.TempDir()
	marker := filepath.Join(repo, "stopped")
	recordService(t, serviceState{Name: "early", Phase: svcStarting}, &serviceRequest{Stop: "touch " + marker})
	if st, err := stopService(serviceRequest{Name: "early", Dir: repo}); err != nil || st.Phase != svcStopped {
		t.Fatalf("stop = %+v, %v", st, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the stop script of a service that never started ran")
	}
	now := time.Now()
	recordService(t, serviceState{Name: "app", Phase: svcExited, StartedAt: &now}, &serviceRequest{Stop: "echo spec > " + marker})
	if _, err := stopService(serviceRequest{Name: "app", Dir: repo, Stop: "echo request > " + marker}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(marker); strings.TrimSpace(string(data)) != "request" {
		t.Errorf("ran %q", data)
	}
}

// A supervisor that will not stop is killed, its group with it, and the
// state says so.
func TestStopKillsAStuckSupervisor(t *testing.T) {
	withServicesDir(t)
	oldTimeout, oldGrace := stopTimeout, stopGrace
	stopTimeout, stopGrace = 20*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { stopTimeout, stopGrace = oldTimeout, oldGrace })
	group := startGroup(t, "echo up; exec sleep 60")
	// Its cmdline is what supervisorAlive looks for; TERM does nothing to it.
	cmd := exec.Command("sh", "-c", `trap '' TERM; echo up; while :; do sleep 0.01; done`, "service", "supervise", "app")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := stdout.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	cur := serviceState{Name: "app", Phase: svcHealthy, PID: cmd.Process.Pid, PGID: group}
	recordService(t, cur, nil)
	if !supervisorAlive(cur) {
		t.Fatalf("not taken for the supervisor: %+v", cur)
	}

	st, err := stopService(serviceRequest{Name: "app", Dir: t.TempDir()})
	if err != nil || st.Phase != svcStopped || !strings.Contains(st.LastError, "did not stop in time") || st.EndedAt == nil {
		t.Fatalf("stop = %+v, %v", st, err)
	}
	waitGone(t, group)
}

// TERM first; what ignores it gets KILL once the grace is up.
func TestKillGroup(t *testing.T) {
	old := stopGrace
	stopGrace = 20 * time.Millisecond
	t.Cleanup(func() { stopGrace = old })
	polite := startGroup(t, "echo up; exec sleep 60")
	killGroup(polite, nil)
	waitGone(t, polite)
	stubborn := startGroup(t, `trap '' TERM; echo up; while :; do sleep 0.01; done`)
	exited := make(chan struct{})
	close(exited)
	killGroup(stubborn, exited)
	waitGone(t, stubborn)
}

func TestLockAndLaunchFailures(t *testing.T) {
	dir := withServicesDir(t)
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A service dir that cannot exist.
	old := servicesDir
	servicesDir = file
	if _, err := startService(serviceRequest{Name: "app", Start: "x"}, false, false); err == nil {
		t.Error("start under a file succeeded")
	}
	if _, err := stopService(serviceRequest{Name: "app"}); err == nil {
		t.Error("stop under a file succeeded")
	}
	servicesDir = old

	occupied := func(name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(serviceDir("app"), name), 0o755); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(serviceDir("app"))
		if _, err := startService(serviceRequest{Name: "app", Start: "x", Dir: t.TempDir()}, false, false); err == nil {
			t.Errorf("start past a directory at %s succeeded", name)
		}
	}
	for _, name := range []string{"lock", "spec.json", "output.log"} {
		occupied(name)
	}
	if err := saveServiceState(serviceState{Name: "no-dir"}); err == nil {
		t.Error("a state with no directory saved")
	}
	// A supervisor that cannot start in the service's directory.
	if _, err := startService(serviceRequest{Name: "app", Start: "x", Dir: filepath.Join(dir, "missing")}, false, false); err == nil ||
		!strings.Contains(err.Error(), "start the supervisor") {
		t.Errorf("no directory = %v", err)
	}
}

func TestShellCommandWithoutBash(t *testing.T) {
	dir := t.TempDir()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sh, filepath.Join(dir, "sh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	out, err := shellCommand(t.Context(), "echo $0; false; echo not reached").CombinedOutput()
	if err == nil || strings.TrimSpace(string(out)) != "sh" {
		t.Errorf("sh -e = %q, %v", out, err)
	}
}

func TestRunStopScript(t *testing.T) {
	var log strings.Builder
	runStopScript("echo out; exit 4", t.TempDir(), "app", &log)
	if !strings.Contains(log.String(), "out") || !strings.Contains(log.String(), "the stop script failed: exit status 4") {
		t.Errorf("log = %q", log.String())
	}
	marker := filepath.Join(t.TempDir(), "ran")
	runStopScript("touch "+marker, filepath.Dir(marker), "app", nil)
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("with no log: %v", err)
	}
}

// A stop script that hangs is cut off at its limit, children and all: it
// cannot wedge a stop.
func TestRunStopScriptIsBounded(t *testing.T) {
	old := stopTimeout
	stopTimeout = 20 * time.Millisecond
	t.Cleanup(func() { stopTimeout = old })
	var log strings.Builder
	start := time.Now()
	runStopScript("sleep 60 & wait", t.TempDir(), "app", &log)
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("the stop took %s: its children held it open", took)
	}
	if !strings.Contains(log.String(), "the stop script failed") {
		t.Errorf("log = %q", log.String())
	}
}

func TestProbeServiceFailures(t *testing.T) {
	dir := t.TempDir()
	if ok, err := probeService(serviceRequest{Dir: dir, Health: serviceHealth{Command: "exit 1"}}); ok || err == nil || strings.HasSuffix(err.Error(), ": ") {
		t.Errorf("silent failure: %v, %v", ok, err)
	}
	long := strings.Repeat("x", 600) + "the end"
	ok, err := probeService(serviceRequest{Dir: dir, Interval: "1m", Health: serviceHealth{Command: "echo " + long + "; exit 1"}})
	if ok || !strings.HasSuffix(err.Error(), "the end") || len(err.Error()) > 560 {
		t.Errorf("long failure: %v, %q", ok, err)
	}
	if ok, err := probeService(serviceRequest{Health: serviceHealth{HTTP: "http://127.0.0.1:" + strconv.Itoa(freePort(t))}}); ok || err == nil {
		t.Errorf("nothing listening: %v, %v", ok, err)
	}
}

// A log past the window is read from its tail, the first line it lands in
// half-way through dropped by the count.
func TestTailLinesOfABigLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	line := strings.Repeat("x", 1023) + "\n"
	if err := os.WriteFile(p, []byte(strings.Repeat(line, 2000)+"last\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := tailLines(f, 2); got != line+"last\n" {
		t.Errorf("tail = %d bytes", len(got))
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := os.Open(empty)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if got := tailLines(e, 2); got != "" {
		t.Errorf("empty = %q", got)
	}
}

// What cannot be read comes back as an error, on the sidecar's routes too.
func TestServiceReadFailures(t *testing.T) {
	dir := withServicesDir(t)
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := servicesDir
	servicesDir = file
	defer func() { servicesDir = old }()
	if _, err := serviceStatus(nil); err == nil {
		t.Error("a services dir that is a file listed")
	}
	rec := httptest.NewRecorder()
	serveServiceStatus(rec, httptest.NewRequest("GET", "/services", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("GET /services = %d", rec.Code)
	}
}
