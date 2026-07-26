package resolve

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/kinds"
	"apothecary/internal/ports"
	"apothecary/internal/secrets"
)

const testFlowDoc = `name: v1
version: 2
defaults: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build, type: agent, agent: builder }
`

// countingProvisioner observes construction/close for the cache tests.
type countingProvisioner struct {
	kinds.Provisioner
	closed *atomic.Int32
}

func (c countingProvisioner) Close() { c.closed.Add(1) }

type countingProvKind struct {
	made   *atomic.Int32
	closed *atomic.Int32
}

func (countingProvKind) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "provisioner", Kind: "counting", Label: "counting"}
}

func (k countingProvKind) New(kinds.Config, kinds.ProvisionerDeps) (kinds.Provisioner, error) {
	k.made.Add(1)
	inner, _ := kinds.FakeProvisioner{}.New(kinds.Config{}, kinds.ProvisionerDeps{})
	return countingProvisioner{Provisioner: inner, closed: k.closed}, nil
}

func setup(t *testing.T) (*Resolver, *confstore.Service, confstore.Project, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	key, _ := secrets.ParseKey(secrets.GenerateKey())
	svc := confstore.New(confstore.NewMem(), key)
	reg := kinds.NewRegistry()
	kinds.RegisterFakes(reg)
	made, closed := &atomic.Int32{}, &atomic.Int32{}
	reg.RegisterProvisioner(countingProvKind{made: made, closed: closed})
	svc.KindValid = reg.Valid

	ctx := context.Background()
	if _, err := svc.SaveFlow(ctx, "v1", testFlowDoc, nil); err != nil {
		t.Fatal(err)
	}
	mk := func(port, kind, name string, sec map[string]string) string {
		in, err := svc.CreateIntegration(ctx, confstore.Integration{Port: port, Kind: kind, Name: name, Secrets: sec})
		if err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	p, err := svc.CreateProject(ctx, confstore.Project{
		Name:          "p1",
		ForgeID:       mk(confstore.PortForge, "fake", "f", nil),
		ProvisionerID: mk(confstore.PortProvisioner, "counting", "c", nil),
		HarnessIDs:    []string{mk(confstore.PortHarness, "fake", "h", nil)},
		Repo:          confstore.RepoConfig{URL: "https://gitlab.example.com/g/p.git", Locator: "1"},
		Workspace:     confstore.WorkspaceConfig{ExtraDomains: []string{"pypi.org"}},
		DefaultFlow:   "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(svc, reg, "", 8080), svc, p, made, closed
}

// altHarnessKind is a second harness kind for per-step-harness tests: it
// delegates behaviour to the fake harness but reports its own kind, binary,
// install layer and egress domain so the resolved union is observable.
type altHarnessKind struct{}

func (altHarnessKind) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "harness", Kind: "alt", Label: "Alt harness"}
}
func (altHarnessKind) New(cfg kinds.Config, envp ports.EnvProvisioner) (kinds.Harness, error) {
	inner, err := kinds.FakeHarness{}.New(cfg, envp)
	if err != nil {
		return nil, err
	}
	return altHarness{inner}, nil
}

type altHarness struct{ kinds.Harness }

func (altHarness) Binary() string          { return "alt-bin" }
func (altHarness) InstallLayer() []string  { return []string{"RUN install-alt"} }
func (altHarness) EgressDomains() []string { return []string{"alt.example.com"} }

// The harness install is handed to provisioners that don't get the §8 image
// as plain shell, with its prerequisites derived from the command itself —
// so nothing assumes a runtime a given harness may not need.
func TestHarnessInstallShellAndRequires(t *testing.T) {
	if got := shellLines([]string{"RUN npm install -g opencode-ai@1.2.3", "ENV X=1"}); !slices.Equal(
		got, []string{"npm install -g opencode-ai@1.2.3"}) {
		t.Errorf("shellLines = %q (RUN unwrapped, non-RUN dropped)", got)
	}
	cases := []struct {
		cmds []string
		want []string
	}{
		{[]string{"npm install -g opencode-ai@1"}, []string{"npm"}},
		{[]string{"curl -fsSL https://x.dev/i | sh"}, []string{"curl"}},
		{[]string{"pip install x", "pip install y"}, []string{"pip"}}, // deduped
		{[]string{"sh -c 'echo hi'"}, nil},                            // always-present shell noise
	}
	for _, tc := range cases {
		if got := requiredTools(tc.cmds); !slices.Equal(got, tc.want) {
			t.Errorf("requiredTools(%q) = %q, want %q", tc.cmds, got, tc.want)
		}
	}
}

func TestResolveMultiHarness(t *testing.T) {
	key, _ := secrets.ParseKey(secrets.GenerateKey())
	svc := confstore.New(confstore.NewMem(), key)
	reg := kinds.NewRegistry()
	kinds.RegisterFakes(reg)
	reg.RegisterProvisioner(countingProvKind{made: &atomic.Int32{}, closed: &atomic.Int32{}})
	reg.RegisterHarness(altHarnessKind{})
	svc.KindValid = reg.Valid
	ctx := context.Background()
	if _, err := svc.SaveFlow(ctx, "v1", testFlowDoc, nil); err != nil {
		t.Fatal(err)
	}
	mk := func(port, kind, name string) string {
		in, err := svc.CreateIntegration(ctx, confstore.Integration{Port: port, Kind: kind, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	def := mk(confstore.PortHarness, "fake", "h")
	alt := mk(confstore.PortHarness, "alt", "h2")
	p, err := svc.CreateProject(ctx, confstore.Project{
		Name:          "multi",
		ForgeID:       mk(confstore.PortForge, "fake", "f"),
		ProvisionerID: mk(confstore.PortProvisioner, "counting", "c"),
		HarnessIDs:    []string{def, alt},
		Repo:          confstore.RepoConfig{URL: "https://gitlab.example.com/g/p.git", Locator: "1"},
		Workspace:     confstore.WorkspaceConfig{},
		DefaultFlow:   "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := New(svc, reg, "", 8080).Resolve(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Release()

	if res.HarnessKind != "fake" {
		t.Errorf("default harness kind = %q, want fake", res.HarnessKind)
	}
	if !slices.Equal(res.HarnessKinds, []string{"alt", "fake"}) {
		t.Errorf("harness kinds = %v, want [alt fake]", res.HarnessKinds)
	}
	if res.Runners["fake"] == nil || res.Runners["alt"] == nil {
		t.Errorf("runners missing a kind: %v", res.Runners)
	}
	if !slices.Contains(res.HarnessBinaries, "alt-bin") || !slices.Contains(res.HarnessBinaries, "true") {
		t.Errorf("binaries = %v, want both harnesses", res.HarnessBinaries)
	}
	if !slices.Contains(res.EgressDomains, "alt.example.com") {
		t.Errorf("egress %v missing alt harness domain", res.EgressDomains)
	}
}

func TestResolveFillsResolution(t *testing.T) {
	r, _, p, _, _ := setup(t)
	res, err := r.Resolve(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Release()
	if res.ProjectID != p.ID || res.HarnessKind != "fake" || res.RepoRef.Locator != "1" {
		t.Errorf("resolution = %+v", res)
	}
	// Egress: repo host + project extras (fake adapters contribute none).
	for _, want := range []string{"gitlab.example.com", "pypi.org"} {
		if !slices.Contains(res.EgressDomains, want) {
			t.Errorf("egress %v missing %s", res.EgressDomains, want)
		}
	}
	if res.Envp == nil || res.Runner == nil || res.Forge == nil {
		t.Error("adapters missing")
	}
	// The full port chain works: provision + run through the resolution.
	env, err := res.Envp.Provision(context.Background(), ports.EnvSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.Runner.Run(context.Background(), env, ports.AgentSpec{Agent: "builder"}, ports.AgentInput{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := res.SeedAuth(context.Background(), env); err != nil {
		t.Fatal(err)
	}
}

// §6.1: config edits apply to new resolutions; in-flight runs keep their
// provisioner, which closes only when the last holder releases it.
func TestProvisionerCacheGenerations(t *testing.T) {
	r, svc, p, made, closed := setup(t)
	ctx := context.Background()

	res1, err := r.Resolve(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := r.Resolve(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if made.Load() != 1 {
		t.Fatalf("constructed %d provisioners for one generation, want 1", made.Load())
	}
	if res1.Envp != res2.Envp {
		t.Fatal("same generation must share the instance")
	}

	// Edit the provisioner integration -> new generation for new resolves.
	time.Sleep(2 * time.Millisecond) // updated_at must move
	if _, err := svc.UpdateIntegration(ctx, p.ProvisionerID, "", map[string]any{"note": "edited"}, nil); err != nil {
		t.Fatal(err)
	}
	res3, err := r.Resolve(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if made.Load() != 2 {
		t.Fatalf("edit did not construct a new generation (made=%d)", made.Load())
	}
	if res3.Envp == res1.Envp {
		t.Fatal("new resolution got the stale instance")
	}
	if closed.Load() != 0 {
		t.Fatal("stale instance closed while still held")
	}
	res1.Release()
	if closed.Load() != 0 {
		t.Fatal("stale instance closed while res2 still holds it")
	}
	res2.Release()
	if closed.Load() != 1 {
		t.Fatalf("stale instance not closed after last release (closed=%d)", closed.Load())
	}
	res3.Release()
	if closed.Load() != 1 {
		t.Fatal("live generation must not close on release")
	}
	r.Close()
	if closed.Load() != 2 {
		t.Fatalf("resolver Close must close cached provisioners (closed=%d)", closed.Load())
	}
}

func TestUnresolvableMapping(t *testing.T) {
	r, svc, _, _, _ := setup(t)
	ctx := context.Background()
	if _, err := r.Resolve(ctx, "no-such-project"); !errors.Is(err, engine.ErrUnresolvable) {
		t.Errorf("missing project err = %v", err)
	}
	// An unregistered kind never heals under the running process: seed an
	// integration of an unknown kind directly (bypassing write-time
	// validation) and point a project at it.
	svc.KindValid = nil
	ghost, err := svc.CreateIntegration(ctx, confstore.Integration{
		Port: confstore.PortHarness, Kind: "ghost", Name: "ghost",
	})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(port, name string) string {
		in, err := svc.CreateIntegration(ctx, confstore.Integration{Port: port, Kind: "fake", Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	p2, err := svc.CreateProject(ctx, confstore.Project{
		Name: "p2", ForgeID: mk(confstore.PortForge, "f2"),
		ProvisionerID: mk(confstore.PortProvisioner, "c2"), HarnessIDs: []string{ghost.ID},
		Repo:        confstore.RepoConfig{URL: "https://x.example/g/p.git"},
		Workspace:   confstore.WorkspaceConfig{},
		DefaultFlow: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, p2.ID); !errors.Is(err, engine.ErrUnresolvable) {
		t.Errorf("unregistered kind err = %v", err)
	}
}

func TestIntegrationOps(t *testing.T) {
	r, _, p, _, _ := setup(t)
	ctx := context.Background()
	if err := r.TestIntegration(ctx, p.ForgeID); err != nil {
		t.Errorf("forge test = %v", err)
	}
	if err := r.TestIntegration(ctx, p.HarnessIDs[0]); err != nil {
		t.Errorf("harness test = %v", err)
	}
	repos, err := r.ListRepos(ctx, p.ForgeID)
	if err != nil || len(repos) == 0 {
		t.Fatalf("ListRepos = %v, %v", repos, err)
	}
	if _, err := r.ListRepos(ctx, p.HarnessIDs[0]); err == nil {
		t.Error("ListRepos on a non-forge must refuse")
	}
}

var _ = fake.Forge{} // keep the fake import for future scenarios
