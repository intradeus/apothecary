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

// svcShim runs the built shim's service subcommand against a private state
// dir, the request (if any) on stdin.
type svcShim struct {
	t    *testing.T
	bin  string
	dir  string
	repo string
}

func newSvcShim(t *testing.T) *svcShim {
	t.Helper()
	s := &svcShim{t: t, bin: buildShim(t), dir: t.TempDir(), repo: t.TempDir()}
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

func (s *svcShim) run(req *serviceRequest, args ...string) ([]byte, error) {
	cmd := exec.Command(s.bin, append([]string{"service"}, args...)...)
	cmd.Dir = s.repo
	cmd.Env = append(os.Environ(), "APOTHECARY_SERVICES="+s.dir, "APOTHECARY_CMD=run:stage")
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
	if !strings.Contains(log, "up in "+s.repo+" as app marker=none") {
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
