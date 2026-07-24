package engine

import (
	"context"
	"strings"
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
	h.res.EgressDomains = []string{"gitlab.local", "openrouter.ai"}
	h.res.HarnessBinary = "opencode"

	var steps []CheckStep
	failed := RunEnvCheck(context.Background(), h.res, "envck-test",
		EnvCheckOptions{EngineURL: "http://127.0.0.1:9", EnginePort: 9},
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
	for _, want := range []string{"provision", "bootstrap", "verify",
		"egress_deny", "egress:gitlab.local",
		"egress:openrouter.ai", "harness_binary", "shim", "tunnel",
		"rw_absolute", "rw_relative", "links", "destroy"} {
		if s, ok := byID[want]; !ok || s.Status != "pass" {
			t.Errorf("step %s = %+v", want, s)
		}
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
		EnvCheckOptions{EngineURL: "http://127.0.0.1:9", EnginePort: 9},
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
		EnvCheckOptions{EnginePort: 9}, func(s CheckStep) { steps = append(steps, s) })
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
