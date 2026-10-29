package confstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/secrets"
)

func newSvc(t *testing.T) (*Service, *Mem) {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	mem := NewMem()
	return New(mem, key), mem
}

const testFlowDoc = `name: v1
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build the thing
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`

// seedProject creates the integrations + flow a valid project needs and
// returns the project.
func seedProject(t *testing.T, svc *Service) Project {
	t.Helper()
	ctx := context.Background()
	mk := func(port, kind, name string, sec map[string]string) Integration {
		in, err := svc.CreateIntegration(ctx, Integration{Port: port, Kind: kind, Name: name, Secrets: sec})
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	repoHost := mk(PortRepository, "gitlab", "gl", map[string]string{"token": "glpat-supersecret99"})
	prov := mk(PortProvisioner, "devcontainer", "dc", map[string]string{"token": "prov-tok"})
	harness := mk(PortHarness, "opencode", "oc", nil)
	p, err := svc.CreateProject(ctx, Project{
		Name: "p1", RepoAccountID: repoHost.ID, ProvisionerID: prov.ID, HarnessIDs: []string{harness.ID},
		Repo:      RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42"},
		Workspace: WorkspaceConfig{},
		// A project starts on the built-in template: its own flows cannot
		// exist before it does.
		DefaultFlow: "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveFlow(ctx, p.ID, "v1", testFlowDoc, nil); err != nil {
		t.Fatal(err)
	}
	p.DefaultFlow = "v1"
	if p, err = svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIntegrationSecretsSealedAtRest(t *testing.T) {
	svc, mem := newSvc(t)
	seedProject(t, svc)
	rows, _ := mem.ListIntegrations(context.Background(), "")
	for _, r := range rows {
		if strings.Contains(string(r.Sealed), "glpat") || strings.Contains(string(r.Sealed), "sk-or") {
			t.Fatalf("integration %s stores plaintext secrets", r.Name)
		}
	}
	// And they decrypt through the service.
	list, err := svc.ListIntegrations(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, in := range list {
		if in.Name == "gl" && in.Secrets["token"] == "glpat-supersecret99" {
			found = true
		}
	}
	if !found {
		t.Fatal("secrets did not roundtrip through the service")
	}
}

// §3 merge semantics: present replaces, "" deletes, absent keeps.
func TestUpdateIntegrationSecretMerge(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	in, err := svc.CreateIntegration(ctx, Integration{
		Port: PortRepository, Kind: "gitlab", Name: "gl",
		Secrets: map[string]string{"token": "old-token", "extra": "keep-me"},
	})
	if err != nil {
		t.Fatal(err)
	}
	upd, err := svc.UpdateIntegration(ctx, in.ID, "", map[string]any{"base_url": "https://gl.example"}, map[string]string{"token": "new-token"})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Secrets["token"] != "new-token" || upd.Secrets["extra"] != "keep-me" {
		t.Errorf("merge = %v (absent keys must keep)", upd.Secrets)
	}
	upd, err = svc.UpdateIntegration(ctx, in.ID, "", nil, map[string]string{"extra": ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := upd.Secrets["extra"]; ok {
		t.Error(`"" must delete the key`)
	}
	if upd.Config["base_url"] != "https://gl.example" {
		t.Error("nil config on update must keep the stored config")
	}
}

func TestProjectValidation(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	repoHost, _ := svc.CreateIntegration(ctx, Integration{Port: PortRepository, Kind: "gitlab", Name: "gl"})
	prov, _ := svc.CreateIntegration(ctx, Integration{Port: PortProvisioner, Kind: "devcontainer", Name: "c"})
	harness, _ := svc.CreateIntegration(ctx, Integration{Port: PortHarness, Kind: "opencode", Name: "oc"})

	base := Project{
		Name: "p", RepoAccountID: repoHost.ID, ProvisionerID: prov.ID, HarnessIDs: []string{harness.ID},
		Repo:        RepoConfig{URL: "https://gitlab.com/g/p.git"},
		Workspace:   WorkspaceConfig{},
		DefaultFlow: "builder",
	}

	// A project names its default flow: the code knows no flow to fall back
	// to.
	fb := base
	fb.Name = "p-no-default-flow"
	fb.DefaultFlow = ""
	if _, err := svc.CreateProject(ctx, fb); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "default_flow is required") {
		t.Errorf("empty default_flow = %v, want a refusal naming it", err)
	}

	// Wrong-port reference is refused naming the field.
	bad := base
	bad.HarnessIDs = []string{repoHost.ID}
	if _, err := svc.CreateProject(ctx, bad); err == nil || !strings.Contains(err.Error(), "harness_id") {
		t.Errorf("wrong-port ref err = %v", err)
	}

	// Unknown default flow refused.
	bad = base
	bad.DefaultFlow = "nope"
	if _, err := svc.CreateProject(ctx, bad); err == nil || !errors.Is(err, ErrInvalid) {
		t.Errorf("default_flow err = %v", err)
	}

	// setup stays optional: no commands at all is a valid project.
	good := base
	if _, err := svc.CreateProject(ctx, good); err != nil {
		t.Errorf("valid project refused: %v", err)
	}
}

func TestProjectHarnessSet(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	repoHost, _ := svc.CreateIntegration(ctx, Integration{Port: PortRepository, Kind: "gitlab", Name: "gl"})
	prov, _ := svc.CreateIntegration(ctx, Integration{Port: PortProvisioner, Kind: "devcontainer", Name: "c"})
	oc, _ := svc.CreateIntegration(ctx, Integration{Port: PortHarness, Kind: "opencode", Name: "oc"})
	cx, _ := svc.CreateIntegration(ctx, Integration{Port: PortHarness, Kind: "codex", Name: "cx"})
	oc2, _ := svc.CreateIntegration(ctx, Integration{Port: PortHarness, Kind: "opencode", Name: "oc2"})

	base := Project{
		Name: "p", RepoAccountID: repoHost.ID, ProvisionerID: prov.ID, HarnessIDs: []string{oc.ID},
		Repo:        RepoConfig{URL: "https://gitlab.com/g/p.git"},
		Workspace:   WorkspaceConfig{},
		DefaultFlow: "builder",
	}

	// A single-harness set round-trips (no default designation).
	p, err := svc.CreateProject(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.HarnessIDs, []string{oc.ID}) {
		t.Errorf("harness set = %v, want [oc]", p.HarnessIDs)
	}

	// An empty set is refused — a project needs at least one harness.
	empty := base
	empty.Name = "empty"
	empty.HarnessIDs = nil
	if _, err := svc.CreateProject(ctx, empty); err == nil || !strings.Contains(err.Error(), "harness_ids is required") {
		t.Errorf("empty-set err = %v", err)
	}

	// A valid two-kind set round-trips.
	multi := base
	multi.Name = "multi"
	multi.HarnessIDs = []string{oc.ID, cx.ID}
	pm, err := svc.CreateProject(ctx, multi)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pm.HarnessIDs, []string{oc.ID, cx.ID}) {
		t.Errorf("set = %v, want [oc cx]", pm.HarnessIDs)
	}

	// Two integrations of the SAME kind is refused (kind→integration must be
	// unambiguous).
	dup := base
	dup.Name = "dup"
	dup.HarnessIDs = []string{oc.ID, oc2.ID}
	if _, err := svc.CreateProject(ctx, dup); err == nil || !strings.Contains(err.Error(), "one per kind") {
		t.Errorf("dup-kind err = %v", err)
	}

	// A non-harness integration in the set is refused, naming the field.
	wrong := base
	wrong.Name = "wrong"
	wrong.HarnessIDs = []string{oc.ID, repoHost.ID}
	if _, err := svc.CreateProject(ctx, wrong); err == nil || !strings.Contains(err.Error(), "harness_ids") {
		t.Errorf("wrong-port set err = %v", err)
	}
}

func TestDeleteRules(t *testing.T) {
	svc, mem := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)

	// Integration referenced by the project: 409-shaped error naming it.
	if err := svc.DeleteIntegration(ctx, p.RepoAccountID); !errors.Is(err, ErrReferenced) || !strings.Contains(err.Error(), "p1") {
		t.Errorf("delete referenced integration = %v", err)
	}

	// Flow referenced as default_flow.
	if err := svc.DeleteFlow(ctx, p.ID, "v1"); !errors.Is(err, ErrReferenced) {
		t.Errorf("delete referenced flow = %v", err)
	}

	// Project with non-archived runs.
	mem.BlockingRuns = func(id string) []string { return []string{"01RUN"} }
	if err := svc.DeleteProject(ctx, p.ID); !errors.Is(err, ErrReferenced) {
		t.Errorf("delete project with runs = %v", err)
	}
	mem.BlockingRuns = nil

	// Orderly teardown works; flow versions survive flow deletion. The
	// project has to stop pointing at the flow first — that reference is
	// exactly what the guard above refuses to break.
	row, _ := svc.GetFlow(ctx, p.ID, "v1")
	p.DefaultFlow = "builder"
	if _, err := svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFlow(ctx, p.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FlowVersion(ctx, p.ID, row.Hash); err != nil {
		t.Errorf("flow versions must be immutable forever: %v", err)
	}
	if err := svc.DeleteIntegration(ctx, p.RepoAccountID); err != nil {
		t.Fatal(err)
	}
}

func TestFlowNameIsImmutableID(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)
	if _, err := svc.SaveFlow(ctx, p.ID, "other", testFlowDoc, nil); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("name mismatch err = %v", err)
	}
	// Line-precise parser errors surface verbatim.
	_, err := svc.SaveFlow(ctx, p.ID, "v1", "name: v1\nversion: 5\nparameters: { harness: fake, model: nope }\nmain: []\n", nil)
	if err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Errorf("parser err = %v", err)
	}
}

func TestSettingsDefaultsAndValidation(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	set, err := svc.Settings(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if set != DefaultSettings() {
		t.Errorf("empty table settings = %+v", set)
	}
	set.DestroyCompletedAfter = "5h"
	if err := svc.PutSettings(ctx, "u1", set); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Settings(ctx, "u1")
	if got.DestroyCompletedAfter != "5h" {
		t.Errorf("roundtrip = %+v", got)
	}
	// Settings are per user: another account still reads the defaults.
	if other, _ := svc.Settings(ctx, "u2"); other != DefaultSettings() {
		t.Errorf("u2 sees u1's settings: %+v", other)
	}
	set.DestroyAbandoned = "1h"
	if err := svc.PutSettings(ctx, "u1", set); err == nil {
		t.Error(`destroy_abandoned must accept only "never"`)
	}
	set = DefaultSettings()
	set.DestroyCompletedAfter = "banana"
	if err := svc.PutSettings(ctx, "u1", set); err == nil {
		t.Error("invalid duration accepted")
	}
}

func TestKindValidation(t *testing.T) {
	svc, _ := newSvc(t)
	svc.KindValid = func(port, kind string) bool { return kind == "gitlab" && port == PortRepository }
	ctx := context.Background()
	if _, err := svc.CreateIntegration(ctx, Integration{Port: PortRepository, Kind: "gitea", Name: "x"}); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := svc.CreateIntegration(ctx, Integration{Port: "sideways", Kind: "gitlab", Name: "x"}); err == nil {
		t.Error("unknown port accepted")
	}
	if _, err := svc.CreateIntegration(ctx, Integration{Port: PortRepository, Kind: "gitlab", Name: "ok"}); err != nil {
		t.Errorf("valid kind refused: %v", err)
	}
}
