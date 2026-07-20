package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"factory/internal/adapters/fake"
	"factory/internal/event"
	"factory/internal/ports"
	"factory/internal/run"
)

// firewallAwareExec scripts a compliant workspace: deny-probes fail (the
// firewall blocks them), everything else succeeds.
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
	ensured := 0
	h.res.Image = "apothecary/ws:test"
	h.res.EnsureImage = func(context.Context) (bool, error) { ensured++; return ensured > 1, nil }

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
	// harness binary step, the image step, and destroy at the end.
	for _, want := range []string{"image", "provision", "bootstrap", "verify",
		"firewall_deny_http", "firewall_deny_dns", "egress:gitlab.local",
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

// §8.3: the run loop ensures the image between env_requested and env_ready,
// recording image_built; a build failure is a deterministic env_failed.
func TestImageBuiltEventAndFailure(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.res.Image = "apothecary/ws:happy"
	h.res.EnsureImage = func(context.Context) (bool, error) { return false, nil }
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	idx := map[event.Type]int{}
	for i, ev := range evs {
		if _, seen := idx[ev.Type]; !seen {
			idx[ev.Type] = i
		}
	}
	ib, ok := findPayload[*event.ImageBuilt](evs)
	if !ok || ib.Tag != "apothecary/ws:happy" || ib.Cached {
		t.Fatalf("image_built = %+v", ib)
	}
	if !(idx[event.TypeEnvRequested] < idx[event.TypeImageBuilt] && idx[event.TypeImageBuilt] < idx[event.TypeEnvReady]) {
		t.Errorf("ordering wrong: %v", eventTypes(evs))
	}
	if v := h.view(t, runID); v.State != run.Completed {
		t.Errorf("state = %s", v.State)
	}

	// Build failure ⇒ env_failed with the build error.
	h2 := newHarness(t, mustFlow(t, loopFlowDoc))
	h2.res.Image = "apothecary/ws:sad"
	h2.res.EnsureImage = func(context.Context) (bool, error) {
		return false, errors.New("fragment line 3 exploded")
	}
	runID2, err := h2.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h2.e.Wait(runID2)
	ef, ok := findPayload[*event.EnvFailed](h2.events(t, runID2))
	if !ok || !strings.Contains(ef.Error, "fragment line 3 exploded") {
		t.Fatalf("env_failed = %+v", ef)
	}
	if v := h2.view(t, runID2); v.State != run.Failed {
		t.Errorf("state = %s, want failed", v.State)
	}
}
