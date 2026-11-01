package envcheck

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/project/resolution"
)

const fixtureProject = "p1"

// loopDoc is the project's default flow; modelsDoc routes a second agent
// stage through a model the workspace agent does not offer.
const loopDoc = `name: loop
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - { name: builder, prompt: "You build." }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`

const modelsDoc = `name: models
version: 5
parameters: { harness: fake, model: acme/alpha-1 }
agents:
  - { name: builder, prompt: "You build." }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: review, type: agent, harness: $harness, agent: builder, model: acme/alpha-3 }
`

func mustParse(t *testing.T, doc string) *flow.Flow {
	t.Helper()
	f, err := flow.Parse("test.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// flows serves one flow to the registry, as a project binds it.
type flows struct{ f *flow.Flow }

func (s flows) BoundFlow(context.Context, string, string) (*flow.Flow, error) { return s.f, nil }

// fixture is a project resolution over a fake provisioner, and a registry
// over it.
type fixture struct {
	envp   *fake.EnvProvisioner
	runner *fake.AgentRunner
	res    *resolution.Resolution
	reg    *Registry
}

func newFixture(t *testing.T) *fixture {
	h := &fixture{envp: fake.NewEnvProvisioner(), runner: &fake.AgentRunner{}}
	h.res = &resolution.Resolution{
		ProjectID:    fixtureProject,
		ProjectName:  "test",
		RepoRef:      repoport.RepoRef{URL: "https://gitlab.local/g/p.git", DefaultBranch: "main", Locator: "1"},
		AgentsMDPath: "AGENTS.md",
		DefaultFlow:  "loop",
		Commands:     map[string]string{"verify": "make lint && make test"},
		ProviderEnv:  map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		HarnessKind:  "fake",
		Envp:         h.envp,
		Runner:       h.runner,
	}
	h.reg = NewRegistry(resolution.StaticResolver{R: h.res}, flows{mustParse(t, loopDoc)}, "")
	return h
}

// listingRunner is a runner whose workspace agent lists its models.
type listingRunner struct {
	*fake.AgentRunner
	models []string
}

func (r *listingRunner) AgentModels(context.Context, envport.EnvID) ([]string, error) {
	return r.models, nil
}

// firewallAwareExec scripts a compliant workspace: the egress-deny probe
// (example.com) fails because the gateway blocks it, everything else succeeds.
func firewallAwareExec(failDomains ...string) func(*fake.Env, envport.Command) (envport.ExecResult, error) {
	return func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "example.com") {
			return envport.ExecResult{ExitCode: 1, Output: "blocked"}, nil
		}
		for _, d := range failDomains {
			if strings.Contains(cmd.Script, d) {
				return envport.ExecResult{ExitCode: 6, Output: "could not resolve host"}, nil
			}
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}
}

func TestRunDerivedCatalog(t *testing.T) {
	h := newFixture(t)
	h.envp.ExecFunc = firewallAwareExec()
	h.res.EgressDomains = []string{"gitlab.local", "openrouter.ai", "*.docker.io"}
	h.res.HarnessBinary = "opencode"

	var steps []CheckStep
	failed := Run(context.Background(), h.res, "envck-test",
		Options{EngineURL: "http://127.0.0.1:9"},
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
func TestRunDenyModeSkipsEgressDeny(t *testing.T) {
	h := newFixture(t)
	h.envp.ExecFunc = firewallAwareExec()
	h.res.EgressMode = "deny"
	h.res.EgressDomains = []string{"gitlab.local"}
	h.res.HarnessBinary = "opencode"

	var steps []CheckStep
	failed := Run(context.Background(), h.res, "envck-deny",
		Options{EngineURL: "http://127.0.0.1:9"},
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

func TestRunNamesInducedFailure(t *testing.T) {
	h := newFixture(t)
	// doesnotexist.invalid is in the allowlist but unreachable — the check
	// must fail on exactly that named step.
	h.envp.ExecFunc = firewallAwareExec("doesnotexist.invalid")
	h.res.EgressDomains = []string{"gitlab.local", "doesnotexist.invalid"}

	var steps []CheckStep
	failed := Run(context.Background(), h.res, "envck-bad",
		Options{}, func(s CheckStep) { steps = append(steps, s) })
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

func TestRegistry(t *testing.T) {
	h := newFixture(t)
	h.envp.ExecFunc = firewallAwareExec()
	id, err := h.reg.Start(context.Background(), fixtureProject, false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := 200
	for ; deadline > 0; deadline-- {
		snap, ok := h.reg.Get(id)
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

func waitCheck(t *testing.T, h *fixture, id string, done func(CheckRun) bool) CheckRun {
	t.Helper()
	for i := 0; i < 500; i++ {
		snap, ok := h.reg.Get(id)
		if !ok {
			t.Fatal("check vanished")
		}
		if done(snap) {
			return snap
		}
		time.Sleep(5 * time.Millisecond)
	}
	snap, _ := h.reg.Get(id)
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
func TestNewCheckSupersedesTheRunningOne(t *testing.T) {
	h := newFixture(t)
	inner := firewallAwareExec()
	release := make(chan struct{})
	var once sync.Once
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		blocked := false
		once.Do(func() { blocked = true })
		if blocked {
			<-release // the first check's bootstrap hangs (a setup that never exits)
		}
		return inner(env, cmd)
	}
	first, err := h.reg.Start(context.Background(), fixtureProject, false)
	if err != nil {
		t.Fatal(err)
	}
	waitCheck(t, h, first, func(r CheckRun) bool { return hasStep(r, "provision") })

	second, err := h.reg.Start(context.Background(), fixtureProject, false)
	if err != nil {
		t.Fatalf("a second check was refused: %v", err)
	}
	if snap, _ := h.reg.Get(first); snap.Status != "cancelled" || snap.FinishedAt == nil {
		t.Fatalf("superseded check = %+v", snap)
	}
	if latest, ok := h.reg.Latest(fixtureProject); !ok || latest.ID != second {
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

func TestCancel(t *testing.T) {
	h := newFixture(t)
	release := make(chan struct{})
	defer close(release)
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		<-release
		return envport.ExecResult{}, nil
	}
	id, _ := h.reg.Start(context.Background(), fixtureProject, false)
	waitCheck(t, h, id, func(r CheckRun) bool { return hasStep(r, "provision") })
	if !h.reg.Cancel(id) {
		t.Fatal("cancel refused")
	}
	if snap, _ := h.reg.Get(id); snap.Status != "cancelled" {
		t.Fatalf("status = %s", snap.Status)
	}
	if h.reg.Cancel("nope") {
		t.Error("cancelled a check that does not exist")
	}
}

// A provision that fails (or is cancelled mid-build) may leave half a
// workspace under its name: the check removes it by that name.
func TestCleansUpAFailedProvision(t *testing.T) {
	h := newFixture(t)
	h.envp.ProvisionErrs = []error{errors.New("build cancelled")}
	var steps []CheckStep
	failed := Run(context.Background(), h.res, "envck-pf", Options{},
		func(s CheckStep) { steps = append(steps, s) })
	if failed == 0 || len(h.envp.Destroyed) != 1 || !strings.HasPrefix(string(h.envp.Destroyed[0]), "apo-") {
		t.Fatalf("failed=%d destroyed=%v steps=%+v", failed, h.envp.Destroyed, steps)
	}
	if last := steps[len(steps)-1]; last.ID != "destroy" || last.Status != "pass" {
		t.Errorf("last step = %+v", last)
	}
}

func TestAgentModelsStep(t *testing.T) {
	h := newFixture(t)
	h.envp.ExecFunc = firewallAwareExec()
	h.res.Runner = &listingRunner{AgentRunner: h.runner, models: []string{"acme/alpha-3"}}
	var step CheckStep
	Run(context.Background(), h.res, "envck-m",
		Options{Stages: mustParse(t, modelsDoc).Stages},
		func(s CheckStep) {
			if s.ID == "agent_models" {
				step = s
			}
		})
	if step.Status != "fail" || !strings.Contains(step.Detail, "acme/alpha-1") || strings.Contains(step.Detail, `"review"`) {
		t.Fatalf("agent_models = %+v", step)
	}
}

// urlEnvp adds EngineURLProvider to the fake; rwFail makes a path's read-back
// mismatch, to drive the rw round-trip failure.
type urlEnvp struct {
	*fake.EnvProvisioner
	url    string
	rwFail string
}

func (e *urlEnvp) EngineURL(envport.EnvID) string { return e.url }
func (e *urlEnvp) ReadFile(ctx context.Context, id envport.EnvID, path string) ([]byte, error) {
	if e.rwFail != "" && strings.HasSuffix(path, e.rwFail) {
		return []byte("corrupted"), nil
	}
	return e.EnvProvisioner.ReadFile(ctx, id, path)
}

// Keep leaves the workspace for inspection (destroy is skipped, not run), the
// EngineURL comes from the provisioner, and the engine-reachability step names
// that URL.
func TestRunKeepAndEngineURLProvider(t *testing.T) {
	h := newFixture(t)
	h.envp.ExecFunc = firewallAwareExec()
	urlp := &urlEnvp{EnvProvisioner: h.envp, url: "http://engine.svc:8080"}
	h.res.Envp = urlp
	h.res.EgressDomains = []string{"gitlab.local"}
	h.res.HarnessBinary = "opencode"

	var steps []CheckStep
	failed := Run(context.Background(), h.res, "envck-keep", Options{Keep: true, EngineURL: "http://ignored"},
		func(s CheckStep) { steps = append(steps, s) })
	if failed != 0 {
		t.Fatalf("failed = %d: %+v", failed, steps)
	}
	byID := map[string]CheckStep{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	if byID["destroy"].Status != "skip" || !strings.Contains(byID["destroy"].Detail, "kept") {
		t.Errorf("destroy = %+v, want skip", byID["destroy"])
	}
	if len(h.envp.Destroyed) != 0 {
		t.Errorf("Keep must not destroy: %v", h.envp.Destroyed)
	}
	if !strings.Contains(byID["tunnel"].Detail, "http://engine.svc:8080") {
		t.Errorf("tunnel = %+v, want the provider's URL", byID["tunnel"])
	}
}

// Multiple harness binaries each get their own named step; the extra repos are
// cloned too.
func TestRunMultipleBinariesAndExtras(t *testing.T) {
	h := newFixture(t)
	h.envp.ExecFunc = firewallAwareExec()
	h.res.HarnessBinaries = []string{"opencode", "codex"}
	h.res.ExtraRepos = []resolution.ExtraRepo{{Name: "api", URL: "https://gitlab.local/g/api.git", DefaultBranch: "main"}}

	var steps []CheckStep
	failed := Run(context.Background(), h.res, "envck-multi", Options{},
		func(s CheckStep) { steps = append(steps, s) })
	if failed != 0 {
		t.Fatalf("failed = %d: %+v", failed, steps)
	}
	byID := map[string]CheckStep{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	if byID["harness_binary:opencode"].Status != "pass" || byID["harness_binary:codex"].Status != "pass" {
		t.Errorf("per-binary steps missing: %+v", steps)
	}
	// The extra repo was cloned during bootstrap.
	var clonedAPI bool
	for _, c := range h.envp.Execs() {
		if strings.Contains(c.Cmd.Script, "api.git") {
			clonedAPI = true
		}
	}
	if !clonedAPI {
		t.Error("the extra repo was not cloned")
	}
}

// No harness binary declared: the harness_binary step is skipped honestly.
func TestRunNoHarnessBinary(t *testing.T) {
	h := newFixture(t)
	h.envp.ExecFunc = firewallAwareExec()
	var skipped bool
	Run(context.Background(), h.res, "envck-nob", Options{}, func(s CheckStep) {
		if s.ID == "harness_binary" && s.Status == "skip" {
			skipped = true
		}
	})
	if !skipped {
		t.Error("no binary must skip the harness_binary step")
	}
}

// A workspace that reads back different bytes than it wrote fails the
// round-trip check.
func TestRunRoundTripMismatchFails(t *testing.T) {
	h := newFixture(t)
	h.envp.ExecFunc = firewallAwareExec()
	h.res.Envp = &urlEnvp{EnvProvisioner: h.envp, url: "http://e", rwFail: "roundtrip.bin"}
	var step CheckStep
	failed := Run(context.Background(), h.res, "envck-rw", Options{}, func(s CheckStep) {
		if s.ID == "rw_absolute" {
			step = s
		}
	})
	if failed == 0 || step.Status != "fail" || !strings.Contains(step.Detail, "content mismatch") {
		t.Fatalf("rw_absolute = %+v (failed=%d)", step, failed)
	}
}

func TestRegistryLatestAndGetMisses(t *testing.T) {
	h := newFixture(t)
	if _, ok := h.reg.Get("nope"); ok {
		t.Error("Get of an unknown id must miss")
	}
	if _, ok := h.reg.Latest("no-project"); ok {
		t.Error("Latest for a project with no checks must miss")
	}
	h.envp.ExecFunc = firewallAwareExec()
	id, err := h.reg.Start(context.Background(), fixtureProject, false)
	if err != nil {
		t.Fatal(err)
	}
	got := waitCheck(t, h, id, func(r CheckRun) bool { return r.Status != "running" && !r.TearingDown })
	if got.Status != "pass" {
		t.Fatalf("status = %s", got.Status)
	}
	latest, ok := h.reg.Latest(fixtureProject)
	if !ok || latest.ID != id {
		t.Errorf("Latest = %+v", latest)
	}
}

// A project the resolver cannot resolve fails Start.
func TestRegistryStartUnresolvable(t *testing.T) {
	reg := NewRegistry(resolution.StaticResolver{}, flows{mustParse(t, loopDoc)}, "")
	if _, err := reg.Start(context.Background(), "nope", false); err == nil {
		t.Fatal("Start of an unresolvable project must fail")
	}
}
