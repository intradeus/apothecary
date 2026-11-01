package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// withServicesDir points servicesDir at a temp dir for the test.
func withServicesDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := servicesDir
	servicesDir = dir
	t.Cleanup(func() { servicesDir = old })
	return dir
}

func TestValidRequest(t *testing.T) {
	for _, bad := range []serviceRequest{
		{Name: ""},
		{Name: "Bad Name"},
		{Name: "-leading"},
		{Name: "ok", Timeout: "nope"},
		{Name: "ok", Interval: "-5s"},
	} {
		if err := validRequest(&bad); err == nil {
			t.Errorf("validRequest(%+v) = nil, want an error", bad)
		}
	}
	req := serviceRequest{Name: "app", Timeout: "1m", Interval: "5s"}
	if err := validRequest(&req); err != nil {
		t.Fatal(err)
	}
	// An empty Dir defaults to the cwd.
	if req.Dir == "" {
		t.Error("Dir was not defaulted")
	}
}

func TestDurationOr(t *testing.T) {
	if got := durationOr("2s", time.Minute); got != 2*time.Second {
		t.Errorf("got %v", got)
	}
	for _, s := range []string{"", "garbage", "-1s", "0"} {
		if got := durationOr(s, time.Minute); got != time.Minute {
			t.Errorf("durationOr(%q) = %v, want the default", s, got)
		}
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("APO_TEST_KEY", "")
	if got := envOr("APO_TEST_KEY", "def"); got != "def" {
		t.Errorf("unset = %q", got)
	}
	t.Setenv("APO_TEST_KEY", "val")
	if got := envOr("APO_TEST_KEY", "def"); got != "val" {
		t.Errorf("set = %q", got)
	}
}

func TestServiceHealthNone(t *testing.T) {
	if !(serviceHealth{}).none() {
		t.Error("empty health is none")
	}
	for _, h := range []serviceHealth{{HTTP: "x"}, {TCP: 1}, {Command: "true"}} {
		if h.none() {
			t.Errorf("%+v is not none", h)
		}
	}
}

func TestServiceEnvDropsCmdMarkerAddsName(t *testing.T) {
	t.Setenv("APOTHECARY_CMD", "run:stage")
	t.Setenv("KEEP_ME", "yes")
	env := serviceEnv("app")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "APOTHECARY_CMD=") {
		t.Error("the command-stage marker must be dropped")
	}
	if !strings.Contains(joined, "KEEP_ME=yes") || !strings.Contains(joined, "APOTHECARY_SERVICE=app") {
		t.Errorf("env = %v", env)
	}
}

func TestLoadAndSaveServiceState(t *testing.T) {
	withServicesDir(t)
	// A missing service reads stopped, ok=false.
	st, ok := loadServiceState("ghost")
	if ok || st.Phase != svcStopped || st.Name != "ghost" {
		t.Fatalf("missing = %+v, %v", st, ok)
	}
	if err := os.MkdirAll(serviceDir("app"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(time.Second)
	if err := saveServiceState(serviceState{Name: "app", Phase: svcHealthy, PID: 123, HealthyAt: &now, Launched: true}); err != nil {
		t.Fatal(err)
	}
	st, ok = loadServiceState("app")
	if !ok || st.Phase != svcHealthy || st.PID != 123 {
		t.Fatalf("loaded = %+v", st)
	}
	// Launched is never persisted (it is start's answer only).
	if st.Launched {
		t.Error("Launched must not be saved")
	}
	// A corrupt state file reads stopped.
	if err := os.WriteFile(filepath.Join(serviceDir("app"), "state.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadServiceState("app"); ok {
		t.Error("a corrupt state file must read as not-ok")
	}
}

func TestLoadSpec(t *testing.T) {
	withServicesDir(t)
	if _, ok := loadSpec("ghost"); ok {
		t.Error("missing spec must be not-ok")
	}
	if err := os.MkdirAll(serviceDir("app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir("app"), "spec.json"), []byte(`{"name":"app","start":"run"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, ok := loadSpec("app")
	if !ok || spec.Start != "run" {
		t.Fatalf("spec = %+v, %v", spec, ok)
	}
}

func TestObservedMarksDeadSupervisorExited(t *testing.T) {
	// A live phase whose PID is not a supervisor of this service is "exited".
	st := serviceState{Name: "app", Phase: svcHealthy, PID: 999999}
	got := observed(st)
	if got.Phase != svcExited || !strings.Contains(got.LastError, "supervisor is gone") {
		t.Fatalf("observed = %+v", got)
	}
	// A stopped service is left alone.
	if got := observed(serviceState{Name: "app", Phase: svcStopped}); got.Phase != svcStopped {
		t.Errorf("stopped = %+v", got)
	}
}

func TestSupervisorAlive(t *testing.T) {
	if supervisorAlive(serviceState{PID: 0}) {
		t.Error("pid 0 is not alive")
	}
	if supervisorAlive(serviceState{Name: "x", PID: 999999}) {
		t.Error("a nonexistent pid is not alive")
	}
	// This test process is alive but its cmdline is not a supervisor of "x",
	// so the /proc check says no; the final kill(0) fallback still sees the
	// process. The function returns the /proc verdict first.
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skipf("no procfs here, only the kill(0) fallback: %v", err)
	}
	if supervisorAlive(serviceState{Name: "no-such-service", PID: os.Getpid()}) {
		t.Error("the test process is not a supervisor of this service")
	}
}

func TestGroupAlive(t *testing.T) {
	if groupAlive(0) || groupAlive(-1) {
		t.Error("non-positive pgid is never alive")
	}
	if !groupAlive(syscall.Getpgrp()) {
		t.Error("our own process group is alive")
	}
}

func TestKillGroupNoOpOnDeadGroup(t *testing.T) {
	// Nothing in this group but us; a bogus pgid is a no-op (no panic).
	killGroup(0, nil)
	killGroup(999999, nil)
}

func TestStartServiceValidation(t *testing.T) {
	withServicesDir(t)
	if _, err := startService(serviceRequest{Name: "Bad Name", Start: "run"}, false, false); err == nil {
		t.Error("an invalid name must fail")
	}
	if _, err := startService(serviceRequest{Name: "app", Start: "   "}, false, false); err == nil || !strings.Contains(err.Error(), "no start script") {
		t.Errorf("empty start = %v", err)
	}
}

func TestStopServiceOnStoppedIsNotAnError(t *testing.T) {
	withServicesDir(t)
	st, err := stopService(serviceRequest{Name: "never-started"})
	if err != nil || st.Phase != svcStopped {
		t.Fatalf("stop of a stopped service = %+v, %v", st, err)
	}
	// A recorded-but-already-stopped service: stopLocked returns it as-is.
	if err := os.MkdirAll(serviceDir("app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveServiceState(serviceState{Name: "app", Phase: svcStopped}); err != nil {
		t.Fatal(err)
	}
	if st, err := stopService(serviceRequest{Name: "app"}); err != nil || st.Phase != svcStopped {
		t.Fatalf("stop = %+v, %v", st, err)
	}
}

func TestStopServiceInvalidName(t *testing.T) {
	withServicesDir(t)
	if _, err := stopService(serviceRequest{Name: "Bad Name"}); err == nil {
		t.Error("an invalid name must fail")
	}
}

func TestServiceStatusAndLogs(t *testing.T) {
	dir := withServicesDir(t)
	if err := os.MkdirAll(serviceDir("app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveServiceState(serviceState{Name: "app", Phase: svcStopped}); err != nil {
		t.Fatal(err)
	}
	// A non-service dir and a non-slug name are ignored by the listing.
	if err := os.MkdirAll(filepath.Join(dir, "Not A Service"), 0o755); err != nil {
		t.Fatal(err)
	}
	all, err := serviceStatus(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "app" {
		t.Fatalf("status = %+v", all)
	}
	// Named lookup of a missing service reads stopped.
	one, err := serviceStatus([]string{"other"})
	if err != nil || one[0].Phase != svcStopped {
		t.Fatalf("named = %+v, %v", one, err)
	}
	if _, err := serviceStatus([]string{"Bad Name"}); err == nil {
		t.Error("a non-slug name must error")
	}

	// Logs: present, missing (no error), and a bad name.
	if err := os.WriteFile(filepath.Join(serviceDir("app"), "output.log"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := serviceLogs(&sb, "app", 2); err != nil || sb.String() != "b\nc\n" {
		t.Fatalf("logs = %q, %v", sb.String(), err)
	}
	sb.Reset()
	if err := serviceLogs(&sb, "app", 0); err != nil || sb.String() != "a\nb\nc\n" {
		t.Fatalf("logs default n = %q, %v", sb.String(), err)
	}
	sb.Reset()
	if err := serviceLogs(&sb, "no-log", 5); err != nil || sb.String() != "" {
		t.Fatalf("missing log = %q, %v", sb.String(), err)
	}
	if err := serviceLogs(&sb, "Bad Name", 5); err == nil {
		t.Error("a non-slug name must error")
	}
}

func TestStatusStatePhases(t *testing.T) {
	for _, p := range []string{svcStarting, svcHealthy, svcUnhealthy, svcStopping} {
		if !(&serviceState{Phase: p}).live() {
			t.Errorf("%s must be live", p)
		}
	}
	for _, p := range []string{svcStopped, svcExited, ""} {
		if (&serviceState{Phase: p}).live() {
			t.Errorf("%s must not be live", p)
		}
	}
}

// probeService with no probe configured is trivially healthy.
func TestProbeServiceNoProbe(t *testing.T) {
	if ok, err := probeService(serviceRequest{Name: "x"}); !ok || err != nil {
		t.Fatalf("no probe = %v, %v", ok, err)
	}
}
