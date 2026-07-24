package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports"
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

// openEgressEnvp wraps the fake with an EgressReporter that reports open.
type openEgressEnvp struct{ *fake.EnvProvisioner }

func (openEgressEnvp) EgressOpen(context.Context, ports.EnvID) (bool, error) { return true, nil }

func TestRunEnvCheckSkipsFirewallWhenEgressOpen(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	// An open-egress workspace would PASS the deny-probes (nothing blocks) —
	// so if the firewall steps ran they would FAIL. They must be skipped.
	h.envp.ExecFunc = func(_ *fake.Env, _ ports.Command) (ports.ExecResult, error) {
		return ports.ExecResult{ExitCode: 0}, nil
	}
	h.res.Envp = openEgressEnvp{h.envp}
	h.res.EgressDomains = nil
	h.res.HarnessBinary = "opencode"

	var steps []CheckStep
	failed := RunEnvCheck(context.Background(), h.res, "envck-open",
		EnvCheckOptions{EngineURL: "http://127.0.0.1:9", EnginePort: 9},
		func(s CheckStep) { steps = append(steps, s) })
	if failed != 0 {
		t.Fatalf("failed = %d, steps = %+v", failed, steps)
	}
	for _, s := range steps {
		switch s.ID {
		case "firewall_deny_http", "firewall_deny_dns", "firewall_initialized":
			if s.Status != "skip" {
				t.Errorf("firewall step %s = %s, want skip", s.ID, s.Status)
			}
		}
	}
}

// In proxy mode the check must route egress through the proxy (write
// proxy.env) and NOT assert DNS is dead — proxy mode keeps the loopback
// resolver up on purpose.
func TestRunEnvCheckProxyModeWiresProxyAndSkipsDNS(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.envp.ExecFunc = firewallAwareExec()
	h.res.EgressDomains = []string{"gitlab.local"}
	h.res.HarnessBinary = "opencode"
	proxyAddr := "http://run:sig@127.0.0.1:8079"
	proxyEnv := map[string]string{
		"HTTP_PROXY": proxyAddr, "HTTPS_PROXY": proxyAddr, "NO_PROXY": "127.0.0.1,localhost,::1",
		"http_proxy": proxyAddr, "https_proxy": proxyAddr, "no_proxy": "127.0.0.1,localhost,::1",
	}

	var steps []CheckStep
	failed := RunEnvCheck(context.Background(), h.res, "envck-proxy",
		EnvCheckOptions{EngineURL: "http://127.0.0.1:9", EnginePort: 9, Keep: true, ProxyEnv: proxyEnv},
		func(s CheckStep) { steps = append(steps, s) })
	if failed != 0 {
		t.Fatalf("failed = %d, steps = %+v", failed, steps)
	}
	byID := map[string]CheckStep{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	if byID["firewall_deny_dns"].Status != "skip" {
		t.Errorf("firewall_deny_dns = %+v, want skip in proxy mode", byID["firewall_deny_dns"])
	}
	if byID["firewall_deny_http"].Status != "pass" || byID["egress:gitlab.local"].Status != "pass" {
		t.Errorf("proxy-mode egress: deny_http=%+v gitlab=%+v", byID["firewall_deny_http"], byID["egress:gitlab.local"])
	}
	// proxy.env was written into the (kept) workspace so the clone + probes
	// route through the proxy.
	data, err := h.envp.ReadFile(context.Background(), "env-1", "/apothecary/proxy.env")
	if err != nil || !strings.Contains(string(data), "127.0.0.1:8079") {
		t.Errorf("proxy.env = %q, %v", data, err)
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
