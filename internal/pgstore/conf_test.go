package pgstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/agentspec"
	"apothecary/internal/confstore"
	"apothecary/internal/event"
)

// TestAgentDeclColumnsRoundtrip pins that the inputs/outputs jsonb columns
// (migration 0016) survive an upsert/read cycle intact.
func TestAgentDeclColumnsRoundtrip(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := s.UpsertFlow(ctx, confstore.FlowRow{Name: "f", YAML: "a", Hash: "h", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIntegration(ctx, confstore.StoredIntegration{ID: "i", Port: "forge", Kind: "fake", Name: "f", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProject(ctx, confstore.Project{
		ID: "p", Name: "web", ForgeID: "i", ProvisionerID: "i", HarnessIDs: []string{"i"},
		Repo:        confstore.RepoConfig{URL: "https://x/p.git", Locator: "1", DefaultBranch: "main"},
		DefaultFlow: "f", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	a := confstore.Agent{
		ProjectID: "p", Name: "builder", Prompt: "build",
		Inputs:    []agentspec.InputDecl{{Name: "plan", Description: "the plan"}, {Name: "feedback", Optional: true}},
		Outputs:   []agentspec.OutputDecl{{Name: "patch", Path: "artifacts/patch.md"}},
		UpdatedAt: now,
	}
	a.Hash = confstore.AgentHash(a)
	if err := s.UpsertAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgent(ctx, "p", "builder")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Inputs) != 2 || got.Inputs[0].Name != "plan" || !got.Inputs[1].Optional ||
		len(got.Outputs) != 1 || got.Outputs[0].Path != "artifacts/patch.md" {
		t.Fatalf("decl columns did not round-trip: %+v", got)
	}
	// A decl-less agent reads back with empty (not garbage) decls.
	b := confstore.Agent{ProjectID: "p", Name: "plain", Prompt: "x", UpdatedAt: now}
	b.Hash = confstore.AgentHash(b)
	if err := s.UpsertAgent(ctx, b); err != nil {
		t.Fatal(err)
	}
	plain, err := s.GetAgent(ctx, "p", "plain")
	if err != nil || len(plain.Inputs) != 0 || len(plain.Outputs) != 0 {
		t.Fatalf("decl-less agent = %+v, %v", plain, err)
	}
}

// openConf reuses the gated open() and clears the config-plane tables
// (projects before integrations: FK).
func openConf(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	if _, err := s.pool.Exec(context.Background(),
		// CASCADE: later migrations (agents, project_harnesses) added tables
		// with FKs into this set, so a plain TRUNCATE of the subset errors.
		`TRUNCATE projects, integrations, flows, flow_versions, settings CASCADE`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConfCRUDRoundtrip(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	in := confstore.StoredIntegration{
		ID: "int1", Port: "forge", Kind: "gitlab", Name: "gl",
		Config: map[string]any{"base_url": "https://gitlab.com"},
		Sealed: []byte{1, 2, 3}, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertIntegration(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIntegration(ctx, "int1")
	if err != nil || got.Config["base_url"] != "https://gitlab.com" || len(got.Sealed) != 3 {
		t.Fatalf("roundtrip = %+v, %v", got, err)
	}

	// Unique (port, name) → ErrConflict.
	dup := in
	dup.ID = "int2"
	if err := s.UpsertIntegration(ctx, dup); !errors.Is(err, confstore.ErrConflict) {
		t.Fatalf("dup err = %v", err)
	}

	// The default_flow FK (migration 0004) requires the flow to exist before
	// the project that references it.
	if err := s.UpsertFlow(ctx, confstore.FlowRow{Name: "v1", YAML: "a", Hash: "h1", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p := confstore.Project{
		ID: "prj1", Name: "web", ForgeID: "int1", ProvisionerID: "int1", HarnessIDs: []string{"int1"},
		Repo:        confstore.RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42", DefaultBranch: "main", AgentsMDPath: "AGENTS.md"},
		Commands:    map[string]string{"verify": "make test"},
		Workspace:   confstore.WorkspaceConfig{ExtraDomains: []string{"pypi.org"}},
		DefaultFlow: "v1", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	gp, err := s.GetProjectByName(ctx, "web")
	if err != nil || gp.Repo.Locator != "42" || gp.Workspace.ExtraDomains[0] != "pypi.org" ||
		len(gp.HarnessIDs) != 1 || gp.HarnessIDs[0] != "int1" {
		t.Fatalf("project roundtrip = %+v, %v", gp, err)
	}

	// Integration referenced by the project → ErrReferenced.
	if err := s.DeleteIntegration(ctx, "int1"); !errors.Is(err, confstore.ErrReferenced) ||
		!strings.Contains(err.Error(), "web") {
		t.Fatalf("referenced delete = %v", err)
	}

	// A second flow version accumulates immutable history.
	if err := s.UpsertFlow(ctx, confstore.FlowRow{Name: "v1", YAML: "b", Hash: "h2", UpdatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	versions, err := s.ListFlowVersions(ctx, "v1")
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions = %v, %v", versions, err)
	}
	if err := s.DeleteFlow(ctx, "v1"); !errors.Is(err, confstore.ErrReferenced) {
		t.Fatalf("default_flow delete = %v", err)
	}

	// Settings.
	if err := s.PutSetting(ctx, "limits.max_concurrent_runs", "7"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.GetSettings(ctx)
	if err != nil || rows["limits.max_concurrent_runs"] != "7" {
		t.Fatalf("settings = %v, %v", rows, err)
	}

	// Project deletion blocked by a non-archived run (runs projection).
	if _, err := s.Append(ctx, "r1", &event.RunCreated{TicketTitle: "t", FlowName: "v1", ProjectID: "prj1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, "prj1"); !errors.Is(err, confstore.ErrReferenced) {
		t.Fatalf("project delete with run = %v", err)
	}
	if _, err := s.Append(ctx, "r1", &event.RunFailed{Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "r1", &event.RunArchived{}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, "prj1"); err != nil {
		t.Fatalf("archived-run project delete = %v", err)
	}
	if err := s.DeleteFlow(ctx, "v1"); err != nil {
		t.Fatalf("flow delete after project gone = %v", err)
	}
	if _, err := s.GetFlowVersion(ctx, "h1"); err != nil {
		t.Errorf("versions must survive flow deletion: %v", err)
	}
}

// runs.project_id flows through the projection (§6.3).
func TestRunsProjectionCarriesProjectID(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	if _, err := s.Append(ctx, "r2", &event.RunCreated{TicketTitle: "t", FlowName: "v1", ProjectID: "prjX"}); err != nil {
		t.Fatal(err)
	}
	views, err := s.RunViews(ctx)
	if err != nil || len(views) != 1 || views[0].ProjectID != "prjX" {
		t.Fatalf("views = %+v, %v", views, err)
	}
	if _, err := s.RebuildProjections(ctx); err != nil {
		t.Fatal(err)
	}
	views, _ = s.RunViews(ctx)
	if len(views) != 1 || views[0].ProjectID != "prjX" {
		t.Fatalf("rebuilt views = %+v", views)
	}
}
