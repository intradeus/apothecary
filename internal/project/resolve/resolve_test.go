package resolve

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
	"apothecary/internal/project/resolution"
)

const testFlowDoc = `name: v1
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build the thing
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
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
	mk := func(port, kind, name string, sec map[string]string) string {
		in, err := svc.CreateIntegration(ctx, confstore.Integration{Port: port, Kind: kind, Name: name, Secrets: sec})
		if err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	p, err := svc.CreateProject(ctx, confstore.Project{
		Name:          "p1",
		RepoAccountID: mk(confstore.PortRepository, "fake", "f", nil),
		ProvisionerID: mk(confstore.PortProvisioner, "counting", "c", nil),
		HarnessIDs:    []string{mk(confstore.PortHarness, "fake", "h", nil)},
		Repo:          confstore.RepoConfig{URL: "https://gitlab.example.com/g/p.git", Locator: "1"},
		Workspace:     confstore.WorkspaceConfig{ExtraDomains: []string{"pypi.org"}},
		DefaultFlow:   "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(svc, reg, "", 8080, ""), svc, p, made, closed
}

// altHarnessKind is a second harness kind for per-step-harness tests: it
// delegates behaviour to the fake harness but reports its own kind, binary,
// install layer and egress domain so the resolved union is observable.
type altHarnessKind struct{}

func (altHarnessKind) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "harness", Kind: "alt", Label: "Alt harness"}
}
func (altHarnessKind) New(cfg kinds.Config, envp envport.EnvProvisioner) (kinds.Harness, error) {
	inner, err := kinds.FakeHarness{}.New(cfg, envp)
	if err != nil {
		return nil, err
	}
	return altHarness{inner}, nil
}

type altHarness struct{ kinds.Harness }

func (altHarness) Binary() string             { return "alt-bin" }
func (altHarness) EgressDomains() []string    { return []string{"alt.example.com"} }
func (altHarness) EditorExtensions() []string { return []string{"alt.ext"} }

func TestResolveMultiHarness(t *testing.T) {
	key, _ := secrets.ParseKey(secrets.GenerateKey())
	svc := confstore.New(confstore.NewMem(), key)
	reg := kinds.NewRegistry()
	kinds.RegisterFakes(reg)
	reg.RegisterProvisioner(countingProvKind{made: &atomic.Int32{}, closed: &atomic.Int32{}})
	reg.RegisterHarness(altHarnessKind{})
	svc.KindValid = reg.Valid
	ctx := context.Background()
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
		RepoAccountID: mk(confstore.PortRepository, "fake", "f"),
		ProvisionerID: mk(confstore.PortProvisioner, "counting", "c"),
		HarnessIDs:    []string{def, alt},
		Repo:          confstore.RepoConfig{URL: "https://gitlab.example.com/g/p.git", Locator: "1"},
		Workspace:     confstore.WorkspaceConfig{CodeServerExtensions: []string{"golang.go"}},
		DefaultFlow:   "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := New(svc, reg, "", 8080, "").Resolve(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Release()

	// The project's extensions and its harnesses' own (alt is an
	// EditorExtender, the fake is not) reach the editor through its settings
	// (the adapter installs them when the editor opens; nothing is baked).
	if !slices.Equal(res.EditorSettings.Extensions, []string{"golang.go", "alt.ext"}) {
		t.Errorf("editor extensions = %v", res.EditorSettings.Extensions)
	}
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
	if res.Envp == nil || res.Runner == nil || res.RepoHost == nil {
		t.Error("adapters missing")
	}
	// The full port chain works: provision + run through the resolution.
	env, err := res.Envp.Provision(context.Background(), envport.EnvSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.Runner.Run(context.Background(), env, agentport.AgentSpec{Agent: "builder"}, agentport.AgentInput{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := res.SeedAuth(context.Background(), env, func(string) {}); err != nil {
		t.Fatal(err)
	}
	// The harness's reachable models, as flows spell them (run-creation check).
	// The fake runs any model: no list, so nothing is refused.
	if models, err := res.Models(context.Background(), "fake"); err != nil || models != nil {
		t.Errorf("fake models = %v, %v; want no limit", models, err)
	}
	if _, err := res.Models(context.Background(), "nope"); err == nil {
		t.Error("models for a harness outside the project's set")
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
	if _, err := r.Resolve(ctx, "no-such-project"); !errors.Is(err, resolution.ErrUnresolvable) {
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
		Name: "p2", RepoAccountID: mk(confstore.PortRepository, "f2"),
		ProvisionerID: mk(confstore.PortProvisioner, "c2"), HarnessIDs: []string{ghost.ID},
		Repo:        confstore.RepoConfig{URL: "https://x.example/g/p.git"},
		Workspace:   confstore.WorkspaceConfig{},
		DefaultFlow: "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, p2.ID); !errors.Is(err, resolution.ErrUnresolvable) {
		t.Errorf("unregistered kind err = %v", err)
	}
}

func TestIntegrationOps(t *testing.T) {
	r, _, p, _, _ := setup(t)
	ctx := context.Background()
	if _, err := r.TestIntegration(ctx, p.RepoAccountID); err != nil {
		t.Errorf("repository host test = %v", err)
	}
	if _, err := r.TestIntegration(ctx, p.HarnessIDs[0]); err != nil {
		t.Errorf("harness test = %v", err)
	}
	repos, err := r.ListRepos(ctx, p.RepoAccountID)
	if err != nil || len(repos) == 0 {
		t.Fatalf("ListRepos = %v, %v", repos, err)
	}
	if _, err := r.ListRepos(ctx, p.HarnessIDs[0]); err == nil {
		t.Error("ListRepos on a non-repository integration must refuse")
	}
}

var _ = fake.RepoHost{} // keep the fake import for future scenarios

// A ticketsource integration's write side is the same adapter, asserted for
// the capability; one without it is refused by name.
func TestTicketWriterResolvesTheWriteSide(t *testing.T) {
	ctx := context.Background()
	key, _ := secrets.ParseKey(secrets.GenerateKey())
	svc := confstore.New(confstore.NewMem(), key)
	reg := kinds.NewRegistry()
	kinds.RegisterFakes(reg)
	reg.RegisterTicketSource(readOnlyKind{})
	svc.KindValid = reg.Valid
	r := New(svc, reg, t.TempDir(), 0, "")

	writable, err := svc.CreateIntegration(ctx, confstore.Integration{Name: "tracker", Port: confstore.PortTicketSource, Kind: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.TicketWriter(ctx, writable.ID); err != nil {
		t.Errorf("fake tracker: %v", err)
	}
	ro, err := svc.CreateIntegration(ctx, confstore.Integration{Name: "legacy", Port: confstore.PortTicketSource, Kind: "readonly"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.TicketWriter(ctx, ro.ID); err == nil || !strings.Contains(err.Error(), "legacy (readonly)") {
		t.Errorf("read-only tracker: err = %v, want the integration named", err)
	}
	if _, err := r.TicketWriter(ctx, "nope"); !errors.Is(err, resolution.ErrUnresolvable) {
		t.Errorf("missing integration: err = %v, want unresolvable", err)
	}
}

// readOnlyKind is a ticketsource kind whose adapter never learned to write.
type readOnlyKind struct{}

func (readOnlyKind) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "ticketsource", Kind: "readonly", Label: "read-only"}
}

func (readOnlyKind) New(kinds.Config) (trackerport.TicketSource, error) {
	return fake.ReadOnlyTicketSource{Src: &fake.TicketSource{}}, nil
}
