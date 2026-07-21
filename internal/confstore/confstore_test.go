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
defaults: { model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build, type: agent, agent: builder }
`

// seedProject creates the integrations + flow a valid project needs and
// returns the project.
func seedProject(t *testing.T, svc *Service) Project {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.SaveFlow(ctx, "v1", testFlowDoc, nil); err != nil {
		t.Fatal(err)
	}
	mk := func(port, kind, name string, sec map[string]string) Integration {
		in, err := svc.CreateIntegration(ctx, Integration{Port: port, Kind: kind, Name: name, Secrets: sec})
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	forge := mk(PortForge, "gitlab", "gl", map[string]string{"token": "glpat-supersecret99"})
	prov := mk(PortProvisioner, "coder", "coder", map[string]string{"token": "coder-tok"})
	harness := mk(PortHarness, "opencode", "oc", map[string]string{"auth_json": `{"openrouter":{"type":"api","key":"sk-or-abc"}}`})
	p, err := svc.CreateProject(ctx, Project{
		Name: "p1", ForgeID: forge.ID, ProvisionerID: prov.ID, HarnessID: harness.ID,
		Repo:        RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42"},
		Workspace:   WorkspaceConfig{Template: "custom-tmpl"},
		DefaultFlow: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIntegrationSecretsSealedAtRest(t *testing.T) {
	svc, mem := newSvc(t)
	seedProject(t, svc)
	rows, _ := mem.ListIntegrations(context.Background())
	for _, r := range rows {
		if strings.Contains(string(r.Sealed), "glpat") || strings.Contains(string(r.Sealed), "sk-or") {
			t.Fatalf("integration %s stores plaintext secrets", r.Name)
		}
	}
	// And they decrypt through the service.
	list, err := svc.ListIntegrations(context.Background())
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
		Port: PortForge, Kind: "gitlab", Name: "gl",
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
	if _, err := svc.SaveFlow(ctx, "v1", testFlowDoc, nil); err != nil {
		t.Fatal(err)
	}
	forge, _ := svc.CreateIntegration(ctx, Integration{Port: PortForge, Kind: "gitlab", Name: "gl"})
	prov, _ := svc.CreateIntegration(ctx, Integration{Port: PortProvisioner, Kind: "coder", Name: "c"})
	harness, _ := svc.CreateIntegration(ctx, Integration{Port: PortHarness, Kind: "opencode", Name: "oc"})

	base := Project{
		Name: "p", ForgeID: forge.ID, ProvisionerID: prov.ID, HarnessID: harness.ID,
		Repo:        RepoConfig{URL: "https://gitlab.com/g/p.git"},
		Workspace:   WorkspaceConfig{Template: "custom-tmpl"},
		DefaultFlow: "v1",
	}

	// Flows are user-defined: an empty default_flow is refused (no built-in
	// fallback exists).
	bad0 := base
	bad0.DefaultFlow = ""
	if _, err := svc.CreateProject(ctx, bad0); err == nil || !strings.Contains(err.Error(), "default_flow is required") {
		t.Errorf("empty default_flow err = %v", err)
	}

	// Wrong-port reference is refused naming the field.
	bad := base
	bad.HarnessID = forge.ID
	if _, err := svc.CreateProject(ctx, bad); err == nil || !strings.Contains(err.Error(), "harness_id") {
		t.Errorf("wrong-port ref err = %v", err)
	}

	// Fragment rules (§8.2).
	bad = base
	bad.Workspace.DockerfileFragment = "RUN apt-get install -y python3\nCOPY sneaky /etc/"
	if _, err := svc.CreateProject(ctx, bad); err == nil || !strings.Contains(err.Error(), "COPY") {
		t.Errorf("fragment err = %v", err)
	}

	// Since M9 the template is optional: empty = the managed image pipeline.
	pipeline := base
	pipeline.Name = "p-pipeline"
	pipeline.Workspace.Template = ""
	if _, err := svc.CreateProject(ctx, pipeline); err != nil {
		t.Errorf("pipeline-path project refused: %v", err)
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
	if _, err := svc.SaveFlow(ctx, "v1", testFlowDoc, nil); err != nil {
		t.Fatal(err)
	}
	forge, _ := svc.CreateIntegration(ctx, Integration{Port: PortForge, Kind: "gitlab", Name: "gl"})
	prov, _ := svc.CreateIntegration(ctx, Integration{Port: PortProvisioner, Kind: "coder", Name: "c"})
	oc, _ := svc.CreateIntegration(ctx, Integration{Port: PortHarness, Kind: "opencode", Name: "oc"})
	cx, _ := svc.CreateIntegration(ctx, Integration{Port: PortHarness, Kind: "codex", Name: "cx"})
	oc2, _ := svc.CreateIntegration(ctx, Integration{Port: PortHarness, Kind: "opencode", Name: "oc2"})

	base := Project{
		Name: "p", ForgeID: forge.ID, ProvisionerID: prov.ID, HarnessID: oc.ID,
		Repo:        RepoConfig{URL: "https://gitlab.com/g/p.git"},
		Workspace:   WorkspaceConfig{Template: "custom-tmpl"},
		DefaultFlow: "v1",
	}

	// Empty set ⇒ the default is auto-added, so a project always has one.
	p, err := svc.CreateProject(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.HarnessIDs, []string{oc.ID}) {
		t.Errorf("default not auto-added to set: %v", p.HarnessIDs)
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
	wrong.HarnessIDs = []string{oc.ID, forge.ID}
	if _, err := svc.CreateProject(ctx, wrong); err == nil || !strings.Contains(err.Error(), "harness_ids") {
		t.Errorf("wrong-port set err = %v", err)
	}
}

func TestValidateFragment(t *testing.T) {
	ok := "RUN curl -fsSL https://go.dev/dl/go1.24.5.linux-amd64.tar.gz | tar -C /usr/local -xz\nENV PATH=/usr/local/go/bin:$PATH\n# comment\n"
	if err := ValidateFragment(ok); err != nil {
		t.Errorf("valid fragment refused: %v", err)
	}
	for _, bad := range []string{"FROM alpine", "  copy a b", "USER root", "ENTRYPOINT [\"x\"]", "cmd echo", "ADD x y"} {
		if err := ValidateFragment(bad); err == nil {
			t.Errorf("fragment %q accepted", bad)
		}
	}
	// A continuation line starting with COPY is part of a RUN's shell text —
	// harmless (the shell has no COPY builtin), and allowed.
	if err := ValidateFragment("RUN echo hello \\\n  world"); err != nil {
		t.Errorf("continuation refused: %v", err)
	}
}

func TestDeleteRules(t *testing.T) {
	svc, mem := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)

	// Integration referenced by the project: 409-shaped error naming it.
	if err := svc.DeleteIntegration(ctx, p.ForgeID); !errors.Is(err, ErrReferenced) || !strings.Contains(err.Error(), "p1") {
		t.Errorf("delete referenced integration = %v", err)
	}

	// Flow referenced as default_flow.
	if err := svc.DeleteFlow(ctx, "v1"); !errors.Is(err, ErrReferenced) {
		t.Errorf("delete referenced flow = %v", err)
	}

	// Project with non-archived runs.
	mem.BlockingRuns = func(id string) []string { return []string{"01RUN"} }
	if err := svc.DeleteProject(ctx, p.ID); !errors.Is(err, ErrReferenced) {
		t.Errorf("delete project with runs = %v", err)
	}
	mem.BlockingRuns = nil

	// Orderly teardown works; flow versions survive flow deletion.
	row, _ := svc.GetFlow(ctx, "v1")
	if err := svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFlow(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FlowVersion(ctx, row.Hash); err != nil {
		t.Errorf("flow versions must be immutable forever: %v", err)
	}
	if err := svc.DeleteIntegration(ctx, p.ForgeID); err != nil {
		t.Fatal(err)
	}
}

func TestFlowNameIsImmutableID(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	if _, err := svc.SaveFlow(ctx, "other", testFlowDoc, nil); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("name mismatch err = %v", err)
	}
	// Line-precise parser errors surface verbatim.
	_, err := svc.SaveFlow(ctx, "v1", "name: v1\ndefaults: { model: nope }\nstages: []\n", nil)
	if err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Errorf("parser err = %v", err)
	}
}

func TestSettingsDefaultsAndValidation(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	set, err := svc.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if set != DefaultSettings() {
		t.Errorf("empty table settings = %+v", set)
	}
	set.MaxConcurrentRuns = 5
	if err := svc.PutSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Settings(ctx)
	if got.MaxConcurrentRuns != 5 {
		t.Errorf("roundtrip = %+v", got)
	}
	set.DestroyAbandoned = "1h"
	if err := svc.PutSettings(ctx, set); err == nil {
		t.Error(`destroy_abandoned must accept only "never"`)
	}
	set = DefaultSettings()
	set.DestroyCompletedAfter = "banana"
	if err := svc.PutSettings(ctx, set); err == nil {
		t.Error("invalid duration accepted")
	}
}

func TestKindValidation(t *testing.T) {
	svc, _ := newSvc(t)
	svc.KindValid = func(port, kind string) bool { return kind == "gitlab" && port == PortForge }
	ctx := context.Background()
	if _, err := svc.CreateIntegration(ctx, Integration{Port: PortForge, Kind: "gitea", Name: "x"}); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := svc.CreateIntegration(ctx, Integration{Port: "sideways", Kind: "gitlab", Name: "x"}); err == nil {
		t.Error("unknown port accepted")
	}
	if _, err := svc.CreateIntegration(ctx, Integration{Port: PortForge, Kind: "gitlab", Name: "ok"}); err != nil {
		t.Errorf("valid kind refused: %v", err)
	}
}
