package pgstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/confstore"
	"apothecary/internal/event"
	"apothecary/internal/providers"
	"apothecary/internal/secrets"
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
		ID: "int1", Port: "repository", Kind: "gitlab", Name: "gl",
		Config: map[string]any{}, Sealed: []byte{1}, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertIntegration(ctx, in); err != nil {
		t.Fatal(err)
	}
	p := confstore.Project{
		ID: "prj1", Name: "web", RepoAccountID: "int1", ProvisionerID: "int1", HarnessIDs: []string{"int1"},
		DefaultFlow: "builder", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatalf("builtin default_flow upsert = %v", err)
	}
	if gp, err := s.GetProject(ctx, "prj1"); err != nil || gp.DefaultFlow != "builder" {
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
		`TRUNCATE projects, integrations, flows, flow_versions, flow_bindings, automations, settings, provider_credentials CASCADE`); err != nil {
		t.Fatal(err)
	}
	return s
}

// An automation row round-trips with its nullable marks and jsonb params,
// lists per project and overall, and dies with the project; a ticket keeps
// the automation that imported it.
func TestAutomationRoundtripAndCascade(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	in := confstore.StoredIntegration{ID: "int1", Port: "repository", Kind: "gitlab", Name: "gl",
		Config: map[string]any{}, Sealed: []byte{1}, CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertIntegration(ctx, in); err != nil {
		t.Fatal(err)
	}
	p := confstore.Project{
		ID: "prj1", Name: "web", RepoAccountID: "int1", ProvisionerID: "int1", HarnessIDs: []string{"int1"},
		Repo:        confstore.RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42", DefaultBranch: "main", AgentsMDPath: "AGENTS.md"},
		DefaultFlow: "builder", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	tk := confstore.Ticket{ID: "tk1", ProjectID: p.ID, Title: "T", AutomationID: "au1", CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertTicket(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetTicket(ctx, "tk1"); err != nil || got.AutomationID != "au1" {
		t.Fatalf("ticket automation_id = %q, %v", got.AutomationID, err)
	}
	a := confstore.Automation{ID: "au1", ProjectID: p.ID, Name: "nightly", Kind: confstore.AutomationCron,
		Schedule: "0 3 * * *", TicketID: "tk1", Flow: "default", Params: map[string]string{"plan_gate": "false"},
		Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAutomation(ctx, "au1")
	if err != nil || got.Params["plan_gate"] != "false" || got.LastFiredAt != nil || got.LastPolledAt != nil || !got.Enabled {
		t.Fatalf("roundtrip = %+v, %v", got, err)
	}
	fired := now.Add(time.Hour)
	got.LastFiredAt, got.LastPolledAt, got.LastError, got.Params = &fired, &fired, "boom", nil
	if err := s.UpsertAutomation(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, err := s.GetAutomation(ctx, "au1"); err != nil || again.LastFiredAt == nil || !again.LastFiredAt.Equal(fired) || again.LastError != "boom" || again.Params != nil {
		t.Fatalf("marked = %+v, %v", again, err)
	}
	// A merge_request automation passes the kind check (0006) and keeps its
	// repo.
	mrw := confstore.Automation{ID: "au2", ProjectID: p.ID, Name: "review others", Kind: confstore.AutomationMergeRequest,
		Repo: "api", Flow: "rev", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertAutomation(ctx, mrw); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetAutomation(ctx, "au2"); err != nil || got.Repo != "api" || got.Kind != confstore.AutomationMergeRequest {
		t.Fatalf("merge_request roundtrip = %+v, %v", got, err)
	}
	if err := s.DeleteAutomation(ctx, "au2"); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListAutomations(ctx, p.ID); err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list, err := s.ListAutomations(ctx, ""); err != nil || len(list) != 1 {
		t.Fatalf("list all = %+v, %v", list, err)
	}
	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAutomation(ctx, "au1"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("automation outlived its project: %v", err)
	}
	if err := s.DeleteAutomation(ctx, "au1"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("delete of nothing = %v", err)
	}
}

// Bindings live beside the flow and die with the project: an upsert, the
// per-project listing, a delete, and the cascade.
func TestFlowBindingRoundtripAndCascade(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	in := confstore.StoredIntegration{
		ID: "int1", Port: "repository", Kind: "gitlab", Name: "gl",
		Config: map[string]any{"base_url": "https://gitlab.com"},
		Sealed: []byte{1, 2, 3}, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertIntegration(ctx, in); err != nil {
		t.Fatal(err)
	}
	p := confstore.Project{
		ID: "prj1", Name: "web", RepoAccountID: "int1", ProvisionerID: "int1", HarnessIDs: []string{"int1"},
		Repo:        confstore.RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42", DefaultBranch: "main", AgentsMDPath: "AGENTS.md"},
		Commands:    map[string]string{"verify": "make test"},
		DefaultFlow: "builder", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFlowBinding(ctx, p.ID, "default"); !errors.Is(err, confstore.ErrNotFound) {
		t.Fatalf("missing binding err = %v, want ErrNotFound", err)
	}
	b := confstore.FlowBinding{ProjectID: p.ID, FlowName: "default", Params: map[string]string{"harness": "fake"}, UpdatedAt: now}
	if err := s.PutFlowBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	b.Params["plan_gate"] = "false"
	b.UpdatedAt = now.Add(time.Second)
	if err := s.PutFlowBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFlowBinding(ctx, p.ID, "default")
	if err != nil || len(got.Params) != 2 || got.Params["plan_gate"] != "false" || !got.UpdatedAt.Equal(b.UpdatedAt) {
		t.Fatalf("roundtrip = %+v, %v", got, err)
	}
	if err := s.PutFlowBinding(ctx, confstore.FlowBinding{ProjectID: p.ID, FlowName: "knobs", Params: map[string]string{}, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListFlowBindings(ctx, p.ID)
	if err != nil || len(list) != 2 || list[0].FlowName != "default" || list[1].FlowName != "knobs" || list[1].Params == nil {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := s.DeleteFlowBinding(ctx, p.ID, "knobs"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFlowBinding(ctx, p.ID, "knobs"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("second delete err = %v, want ErrNotFound", err)
	}
	// The project takes its bindings with it.
	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFlowBinding(ctx, p.ID, "default"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("binding outlived its project: %v", err)
	}
}

func TestProjectSecretsRoundtripAndCascade(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.UpsertIntegration(ctx, confstore.StoredIntegration{
		ID: "int1", Port: "repository", Kind: "gitlab", Name: "gl", Config: map[string]any{},
		Sealed: []byte{1}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	p := confstore.Project{
		ID: "prj1", Name: "web", RepoAccountID: "int1", ProvisionerID: "int1", HarnessIDs: []string{"int1"},
		Repo:        confstore.RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42"},
		DefaultFlow: "builder", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProjectSecret(ctx, "ghost", confstore.StoredProjectSecret{Name: "x", Sealed: []byte{1}, UpdatedAt: now}); !errors.Is(err, confstore.ErrInvalid) {
		t.Fatalf("secret of a missing project = %v, want ErrInvalid", err)
	}
	for i, name := range []string{"token", "api_key", "token"} {
		if err := s.PutProjectSecret(ctx, p.ID, confstore.StoredProjectSecret{Name: name, Sealed: []byte{byte(i)}, UpdatedAt: now.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListProjectSecrets(ctx, p.ID)
	if err != nil || len(got) != 2 || got[0].Name != "api_key" || got[1].Name != "token" ||
		got[1].Sealed[0] != 2 || !got[1].UpdatedAt.Equal(now.Add(2*time.Second)) {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if err := s.DeleteProjectSecret(ctx, p.ID, "api_key"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProjectSecret(ctx, p.ID, "api_key"); !errors.Is(err, confstore.ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
	if err := s.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListProjectSecrets(ctx, p.ID); len(got) != 0 {
		t.Fatalf("secrets outlived their project: %+v", got)
	}
}

func TestConfCRUDRoundtrip(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	in := confstore.StoredIntegration{
		ID: "int1", Port: "repository", Kind: "gitlab", Name: "gl",
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
		ID: "prj1", Name: "web", RepoAccountID: "int1", ProvisionerID: "int1", HarnessIDs: []string{"int1"},
		Repo:        confstore.RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42", DefaultBranch: "main", AgentsMDPath: "AGENTS.md"},
		Commands:    map[string]string{"verify": "make test"},
		Workspace:   confstore.WorkspaceConfig{ExtraDomains: []string{"pypi.org"}},
		DefaultFlow: "builder", CreatedAt: now, UpdatedAt: now,
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
	gp, err := s.GetProjectByName(ctx, "", "web")
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
	if err := s.PutSetting(ctx, "u1", "reaper.destroy_completed_after", "\"7h\""); err != nil {
		t.Fatal(err)
	}
	rows, err := s.GetSettings(ctx, "u1")
	if err != nil || rows["reaper.destroy_completed_after"] != `"7h"` {
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
	p.DefaultFlow = "builder"
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

// Provider credentials round-trip, scoped by owner; an update cannot change
// owner, provider or kind.
func TestCredentialRoundtrip(t *testing.T) {
	s := openConf(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := confstore.ProviderCredential{ID: "c1", UserID: "u1", Provider: "anthropic", Kind: providers.KindOAuth, Name: "Anthropic",
		Sealed: []byte("sealed"), Meta: confstore.CredentialMeta{Account: "me@x", ExpiresAt: now.Add(time.Hour), Status: confstore.CredentialOK},
		CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertCredential(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.UserID, c.Provider, c.Name, c.Sealed = "u2", "openai", "Renamed", []byte("sealed-2")
	if err := s.UpsertCredential(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCredential(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u1" || got.Provider != "anthropic" || got.Name != "Renamed" || string(got.Sealed) != "sealed-2" ||
		got.Meta.Account != "me@x" || !got.Meta.ExpiresAt.Equal(now.Add(time.Hour)) || got.Kind != providers.KindOAuth {
		t.Errorf("got %+v", got)
	}
	if mine, _ := s.ListCredentials(ctx, "u1"); len(mine) != 1 {
		t.Errorf("u1 lists %d", len(mine))
	}
	if theirs, _ := s.ListCredentials(ctx, "u2"); len(theirs) != 0 {
		t.Errorf("u2 lists %d of u1's", len(theirs))
	}
	if err := s.DeleteCredential(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCredential(ctx, "c1"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

// The instance secret lives in the settings table, whose values are JSON:
// it must be stored as one, and read back the same across a restart.
func TestInstanceSecretRoundtrip(t *testing.T) {
	s := openConf(t)
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	a, err := confstore.New(s, key).InstanceSecret(context.Background(), "auth-broker")
	if err != nil {
		t.Fatal(err)
	}
	b, err := confstore.New(s, key).InstanceSecret(context.Background(), "auth-broker")
	if err != nil || string(a) != string(b) || len(a) != 32 {
		t.Errorf("second read = %x, %v; first %x", b, err, a)
	}
}
