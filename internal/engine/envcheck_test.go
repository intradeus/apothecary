package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports"
)

// firewallAwareExec scripts a compliant workspace: the egress-deny probe
// (example.com) fails because the gateway blocks it, everything else succeeds.
func firewallAwareExec(failDomains ...string) func(*fake.Env, ports.Command) (ports.ExecResult, error) {
	return func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "example.com") {
			return ports.ExecResult{ExitCode: 1, Output: "blocked"}, nil
		}
		for _, d := range failDomains {
			if strings.Contains(cmd.Script, d) {
				return ports.ExecResult{ExitCode: 6, Output: "could not resolve host"}, nil
			}
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}
}

func TestRunEnvCheckDerivedCatalog(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ExecFunc = firewallAwareExec()
	h.res.EgressDomains = []string{"gitlab.local", "openrouter.ai", "*.docker.io"}
	h.res.HarnessBinary = "opencode"

	var steps []CheckStep
	failed := RunEnvCheck(context.Background(), h.res, "envck-test",
		EnvCheckOptions{EngineURL: "http://127.0.0.1:9"},
		func(s CheckStep) { steps = append(steps, s) })
	if failed != 0 {
		t.Fatalf("failed = %d, steps = %+v", failed, steps)
	}
	byID := map[string]CheckStep{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	// The catalog is DERIVED: one egress step per resolved domain, the
	// harness binary step, and destroy at the end.
	for _, want := range []string{"provision", "bootstrap",
		"egress_deny", "egress:gitlab.local",
		"egress:openrouter.ai", "harness_binary", "shim", "tunnel",
		"rw_absolute", "rw_relative", "destroy"} {
		if s, ok := byID[want]; !ok || s.Status != "pass" {
			t.Errorf("step %s = %+v", want, s)
		}
	}
	// A wildcard pattern is not a host: skipped, never curl'ed as a URL.
	if s := byID["egress:*.docker.io"]; s.Status != "skip" {
		t.Errorf("wildcard step = %+v, want skip", s)
	}
	if len(h.envp.Destroyed) != 1 {
		t.Errorf("workspace not destroyed: %v", h.envp.Destroyed)
	}
}

// In deny mode (allow-all + denylist) a generic host is expected reachable, so
// the egress_deny negative is skipped honestly rather than failing.
func TestRunEnvCheckDenyModeSkipsEgressDeny(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ExecFunc = firewallAwareExec()
	h.res.EgressMode = "deny"
	h.res.EgressDomains = []string{"gitlab.local"}
	h.res.HarnessBinary = "opencode"

	var steps []CheckStep
	failed := RunEnvCheck(context.Background(), h.res, "envck-deny",
		EnvCheckOptions{EngineURL: "http://127.0.0.1:9"},
		func(s CheckStep) { steps = append(steps, s) })
	if failed != 0 {
		t.Fatalf("failed = %d, steps = %+v", failed, steps)
	}
	byID := map[string]CheckStep{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	if byID["egress_deny"].Status != "skip" {
		t.Errorf("egress_deny = %+v, want skip in deny mode", byID["egress_deny"])
	}
	if byID["egress:gitlab.local"].Status != "pass" {
		t.Errorf("egress:gitlab.local = %+v, want pass", byID["egress:gitlab.local"])
	}
}

func TestRunEnvCheckNamesInducedFailure(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	// doesnotexist.invalid is in the allowlist but unreachable — the check
	// must fail on exactly that named step.
	h.envp.ExecFunc = firewallAwareExec("doesnotexist.invalid")
	h.res.EgressDomains = []string{"gitlab.local", "doesnotexist.invalid"}

	var steps []CheckStep
	failed := RunEnvCheck(context.Background(), h.res, "envck-bad",
		EnvCheckOptions{}, func(s CheckStep) { steps = append(steps, s) })
	if failed != 1 {
		t.Fatalf("failed = %d", failed)
	}
	found := false
	for _, s := range steps {
		if s.ID == "egress:doesnotexist.invalid" && s.Status == "fail" && strings.Contains(s.Detail, "could not resolve") {
			found = true
		}
	}
	if !found {
		t.Errorf("no named failing step: %+v", steps)
	}
}

func TestEngineEnvCheckRegistry(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ExecFunc = firewallAwareExec()
	id, err := h.e.StartEnvCheck(context.Background(), testProject, false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := 200
	for ; deadline > 0; deadline-- {
		snap, ok := h.e.EnvCheck(id)
		if !ok {
			t.Fatal("check vanished")
		}
		if snap.Status != "running" {
			if snap.Status != "pass" {
				t.Fatalf("status = %s, steps = %+v", snap.Status, snap.Steps)
			}
			return
		}
		<-time.After(10 * time.Millisecond)
	}
	t.Fatal("envcheck never finished")
}

func waitCheck(t *testing.T, h *harness, id string, done func(CheckRun) bool) CheckRun {
	t.Helper()
	for i := 0; i < 500; i++ {
		snap, ok := h.e.EnvCheck(id)
		if !ok {
			t.Fatal("check vanished")
		}
		if done(snap) {
			return snap
		}
		time.Sleep(5 * time.Millisecond)
	}
	snap, _ := h.e.EnvCheck(id)
	t.Fatalf("check %s never got there: %+v", id, snap)
	return CheckRun{}
}

func hasStep(r CheckRun, id string) bool {
	for _, s := range r.Steps {
		if s.ID == id {
			return true
		}
	}
	return false
}

// Starting a check while one runs supersedes it — no waiting it out — and the
// superseded check still destroys its workspace.
func TestNewEnvCheckSupersedesTheRunningOne(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	inner := firewallAwareExec()
	release := make(chan struct{})
	var once sync.Once
	h.envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		blocked := false
		once.Do(func() { blocked = true })
		if blocked {
			<-release // the first check's bootstrap hangs (a setup that never exits)
		}
		return inner(env, cmd)
	}
	first, err := h.e.StartEnvCheck(context.Background(), testProject, false)
	if err != nil {
		t.Fatal(err)
	}
	waitCheck(t, h, first, func(r CheckRun) bool { return hasStep(r, "provision") })

	second, err := h.e.StartEnvCheck(context.Background(), testProject, false)
	if err != nil {
		t.Fatalf("a second check was refused: %v", err)
	}
	if snap, _ := h.e.EnvCheck(first); snap.Status != "cancelled" || snap.FinishedAt == nil {
		t.Fatalf("superseded check = %+v", snap)
	}
	if latest, ok := h.e.LatestEnvCheck(testProject); !ok || latest.ID != second {
		t.Errorf("latest = %+v, want the new check", latest)
	}
	close(release)
	got := waitCheck(t, h, second, func(r CheckRun) bool { return r.Status != "running" && !r.TearingDown })
	if got.Status != "pass" || !hasStep(got, "destroy") {
		t.Fatalf("new check = %+v", got)
	}
	old := waitCheck(t, h, first, func(r CheckRun) bool { return hasStep(r, "destroy") })
	if old.Status != "cancelled" {
		t.Errorf("superseded check ended %s", old.Status)
	}
	// It stopped at its next step instead of failing every remaining probe.
	for _, s := range old.Steps {
		if strings.HasPrefix(s.ID, "egress:") {
			t.Errorf("superseded check kept probing: %+v", s)
		}
	}
}

func TestCancelEnvCheck(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	release := make(chan struct{})
	defer close(release)
	h.envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		<-release
		return ports.ExecResult{}, nil
	}
	id, _ := h.e.StartEnvCheck(context.Background(), testProject, false)
	waitCheck(t, h, id, func(r CheckRun) bool { return hasStep(r, "provision") })
	if !h.e.CancelEnvCheck(id) {
		t.Fatal("cancel refused")
	}
	if snap, _ := h.e.EnvCheck(id); snap.Status != "cancelled" {
		t.Fatalf("status = %s", snap.Status)
	}
	if h.e.CancelEnvCheck("nope") {
		t.Error("cancelled a check that does not exist")
	}
}

// A provision that fails (or is cancelled mid-build) may leave half a
// workspace under its name: the check removes it by that name.
func TestEnvCheckCleansUpAFailedProvision(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ProvisionErrs = []error{errors.New("build cancelled")}
	var steps []CheckStep
	failed := RunEnvCheck(context.Background(), h.res, "envck-pf", EnvCheckOptions{},
		func(s CheckStep) { steps = append(steps, s) })
	if failed == 0 || len(h.envp.Destroyed) != 1 || !strings.HasPrefix(string(h.envp.Destroyed[0]), "apo-") {
		t.Fatalf("failed=%d destroyed=%v steps=%+v", failed, h.envp.Destroyed, steps)
	}
	if last := steps[len(steps)-1]; last.ID != "destroy" || last.Status != "pass" {
		t.Errorf("last step = %+v", last)
	}
}
