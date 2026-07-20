package pgstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"factory/internal/confstore"
	"factory/internal/event"
)

// openConf reuses the gated open() and clears the config-plane tables
// (projects before integrations: FK).
func openConf(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	if _, err := s.pool.Exec(context.Background(),
		`TRUNCATE projects, integrations, flows, flow_versions, settings`); err != nil {
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

	p := confstore.Project{
		ID: "prj1", Name: "web", ForgeID: "int1", ProvisionerID: "int1", HarnessID: "int1",
		ProviderIDs: []string{"int1"},
		Repo:        confstore.RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42", DefaultBranch: "main", AgentsMDPath: "AGENTS.md"},
		Commands:    map[string]string{"verify": "make test"},
		Workspace:   confstore.WorkspaceConfig{Template: "factory", ExtraDomains: []string{"pypi.org"}},
		DefaultFlow: "v1", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	gp, err := s.GetProjectByName(ctx, "web")
	if err != nil || gp.Repo.Locator != "42" || gp.Workspace.ExtraDomains[0] != "pypi.org" {
		t.Fatalf("project roundtrip = %+v, %v", gp, err)
	}

	// Integration referenced (incl. provider_ids jsonb) → ErrReferenced.
	if err := s.DeleteIntegration(ctx, "int1"); !errors.Is(err, confstore.ErrReferenced) ||
		!strings.Contains(err.Error(), "web") {
		t.Fatalf("referenced delete = %v", err)
	}

	// Flows + immutable versions.
	if err := s.UpsertFlow(ctx, confstore.FlowRow{Name: "v1", YAML: "a", Hash: "h1", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
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
