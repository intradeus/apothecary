package pgstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/builtin"
	"apothecary/internal/confstore"
	"apothecary/internal/event"
)

// A project may reference a BUILT-IN flow — compiled into the binary, no
// flows row. This is the case the 0004 FK wrongly rejected: the service
// layer validates default_flow against the DB-then-builtins layering, and
// the store must accept the same set. A name that resolves nowhere still
// bounces with ErrInvalid.
func TestProjectDefaultFlowBuiltin(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	in := confstore.StoredIntegration{
		ID: "int1", Port: "forge", Kind: "gitlab", Name: "gl",
		Config: map[string]any{}, Sealed: []byte{1}, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertIntegration(ctx, in); err != nil {
		t.Fatal(err)
	}
	p := confstore.Project{
		ID: "prj1", Name: "web", ForgeID: "int1", ProvisionerID: "int1", HarnessIDs: []string{"int1"},
		DefaultFlow: builtin.DefaultFlowName, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatalf("builtin default_flow upsert = %v", err)
	}
	if gp, err := s.GetProject(ctx, "prj1"); err != nil || gp.DefaultFlow != builtin.DefaultFlowName {
		t.Fatalf("roundtrip = %+v, %v", gp, err)
	}
	p.DefaultFlow = "nope"
	if err := s.UpsertProject(ctx, p); !errors.Is(err, confstore.ErrInvalid) {
		t.Fatalf("missing flow upsert = %v, want ErrInvalid", err)
	}
}

// openConf reuses the gated open() and clears the config-plane tables
// (projects before integrations: FK).
func openConf(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	if _, err := s.pool.Exec(context.Background(),
		// CASCADE: tickets (and through them attachments) FK projects, so a
		// plain TRUNCATE of the subset errors.
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

	// A flow belongs to a project, so the project comes first — and it starts
	// on the built-in template, which is the only thing that can resolve
	// before it has flows of its own.
	p := confstore.Project{
		ID: "prj1", Name: "web", ForgeID: "int1", ProvisionerID: "int1", HarnessIDs: []string{"int1"},
		Repo:        confstore.RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42", DefaultBranch: "main", AgentsMDPath: "AGENTS.md"},
		Commands:    map[string]string{"verify": "make test"},
		Workspace:   confstore.WorkspaceConfig{ExtraDomains: []string{"pypi.org"}},
		DefaultFlow: builtin.DefaultFlowName, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: p.ID, Name: "v1", YAML: "a", Hash: "h1", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p.DefaultFlow = "v1"
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
	if err := s.UpsertFlow(ctx, confstore.FlowRow{ProjectID: p.ID, Name: "v1", YAML: "b", Hash: "h2", UpdatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	versions, err := s.ListFlowVersions(ctx, p.ID, "v1")
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions = %v, %v", versions, err)
	}
	if err := s.DeleteFlow(ctx, p.ID, "v1"); !errors.Is(err, confstore.ErrReferenced) {
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
	// Deleting the FLOW keeps its versions (§7: immutable forever) — the
	// project has to stop pointing at it first, which is the reference the
	// guard above refuses to break.
	p.DefaultFlow = builtin.DefaultFlowName
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFlow(ctx, p.ID, "v1"); err != nil {
		t.Fatalf("flow delete = %v", err)
	}
	if _, err := s.GetFlowVersion(ctx, p.ID, "h1"); err != nil {
		t.Errorf("versions must survive flow deletion: %v", err)
	}

	// Deleting the PROJECT takes its flows and their history with it: a flow
	// is only meaningful inside the project whose commands it names, and
	// nothing else can reach it.
	if err := s.DeleteProject(ctx, "prj1"); err != nil {
		t.Fatalf("archived-run project delete = %v", err)
	}
	if _, err := s.GetFlowVersion(ctx, p.ID, "h2"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("a deleted project's flow history = %v, want gone", err)
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
