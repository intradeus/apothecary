package confstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/flow"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/trackerport"
)

var errDB = errors.New("db down")

// brokenDB fails the calls a test names, by method name; a test turns the
// failure on once what it needs is in place.
type brokenDB struct {
	*Mem
	fail map[string]bool
}

func (d *brokenDB) err(name string) error {
	if d.fail[name] {
		return errDB
	}
	return nil
}

func (d *brokenDB) UpsertIntegration(ctx Ctx, r StoredIntegration) error {
	if err := d.err("UpsertIntegration"); err != nil {
		return err
	}
	return d.Mem.UpsertIntegration(ctx, r)
}

func (d *brokenDB) GetIntegration(ctx Ctx, id string) (StoredIntegration, error) {
	if err := d.err("GetIntegration"); err != nil {
		return StoredIntegration{}, err
	}
	return d.Mem.GetIntegration(ctx, id)
}

func (d *brokenDB) ListIntegrations(ctx Ctx, u string) ([]StoredIntegration, error) {
	if err := d.err("ListIntegrations"); err != nil {
		return nil, err
	}
	return d.Mem.ListIntegrations(ctx, u)
}

func (d *brokenDB) UpsertProject(ctx Ctx, p Project) error {
	if err := d.err("UpsertProject"); err != nil {
		return err
	}
	return d.Mem.UpsertProject(ctx, p)
}

func (d *brokenDB) GetProject(ctx Ctx, id string) (Project, error) {
	if err := d.err("GetProject"); err != nil {
		return Project{}, err
	}
	return d.Mem.GetProject(ctx, id)
}

func (d *brokenDB) UpsertTicket(ctx Ctx, t Ticket) error {
	if err := d.err("UpsertTicket"); err != nil {
		return err
	}
	return d.Mem.UpsertTicket(ctx, t)
}

func (d *brokenDB) UpsertFlow(ctx Ctx, r FlowRow) error {
	if err := d.err("UpsertFlow"); err != nil {
		return err
	}
	return d.Mem.UpsertFlow(ctx, r)
}

func (d *brokenDB) GetFlow(ctx Ctx, p, n string) (FlowRow, error) {
	if err := d.err("GetFlow"); err != nil {
		return FlowRow{}, err
	}
	return d.Mem.GetFlow(ctx, p, n)
}

func (d *brokenDB) GetFlowVersion(ctx Ctx, p, h string) (FlowVersion, error) {
	if err := d.err("GetFlowVersion"); err != nil {
		return FlowVersion{}, err
	}
	return d.Mem.GetFlowVersion(ctx, p, h)
}

func (d *brokenDB) DeleteFlowBinding(ctx Ctx, p, n string) error {
	if err := d.err("DeleteFlowBinding"); err != nil {
		return err
	}
	return d.Mem.DeleteFlowBinding(ctx, p, n)
}

func (d *brokenDB) PutFlowBinding(ctx Ctx, b FlowBinding) error {
	if err := d.err("PutFlowBinding"); err != nil {
		return err
	}
	return d.Mem.PutFlowBinding(ctx, b)
}

func (d *brokenDB) GetFlowBinding(ctx Ctx, p, n string) (FlowBinding, error) {
	if err := d.err("GetFlowBinding"); err != nil {
		return FlowBinding{}, err
	}
	return d.Mem.GetFlowBinding(ctx, p, n)
}

func (d *brokenDB) PutProjectSecret(ctx Ctx, p string, s StoredProjectSecret) error {
	if err := d.err("PutProjectSecret"); err != nil {
		return err
	}
	return d.Mem.PutProjectSecret(ctx, p, s)
}

func (d *brokenDB) ListProjectSecrets(ctx Ctx, p string) ([]StoredProjectSecret, error) {
	if err := d.err("ListProjectSecrets"); err != nil {
		return nil, err
	}
	return d.Mem.ListProjectSecrets(ctx, p)
}

func (d *brokenDB) UpsertAutomation(ctx Ctx, a Automation) error {
	if err := d.err("UpsertAutomation"); err != nil {
		return err
	}
	return d.Mem.UpsertAutomation(ctx, a)
}

func (d *brokenDB) GetAutomation(ctx Ctx, id string) (Automation, error) {
	if err := d.err("GetAutomation"); err != nil {
		return Automation{}, err
	}
	return d.Mem.GetAutomation(ctx, id)
}

func (d *brokenDB) ListAutomations(ctx Ctx, p string) ([]Automation, error) {
	if err := d.err("ListAutomations"); err != nil {
		return nil, err
	}
	return d.Mem.ListAutomations(ctx, p)
}

func (d *brokenDB) GetSettings(ctx Ctx, u string) (map[string]string, error) {
	if err := d.err("GetSettings"); err != nil {
		return nil, err
	}
	return d.Mem.GetSettings(ctx, u)
}

func (d *brokenDB) PutSetting(ctx Ctx, u, k, v string) error {
	if err := d.err("PutSetting"); err != nil {
		return err
	}
	return d.Mem.PutSetting(ctx, u, k, v)
}

func (d *brokenDB) UpsertCredential(ctx Ctx, c ProviderCredential) error {
	if err := d.err("UpsertCredential"); err != nil {
		return err
	}
	return d.Mem.UpsertCredential(ctx, c)
}

func (d *brokenDB) GetCredential(ctx Ctx, id string) (ProviderCredential, error) {
	if err := d.err("GetCredential"); err != nil {
		return ProviderCredential{}, err
	}
	return d.Mem.GetCredential(ctx, id)
}

func (d *brokenDB) ListCredentials(ctx Ctx, u string) ([]ProviderCredential, error) {
	if err := d.err("ListCredentials"); err != nil {
		return nil, err
	}
	return d.Mem.ListCredentials(ctx, u)
}

func newBroken(t *testing.T) (*Service, *brokenDB) {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	db := &brokenDB{Mem: NewMem(), fail: map[string]bool{}}
	return New(db, key), db
}

// anotherKey is the same store opened with the wrong master key.
func anotherKey(t *testing.T, db DB) *Service {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	return New(db, key)
}

func wantErr(t *testing.T, name string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: err = %v, want %v", name, err, target)
	}
}

// --- integrations -------------------------------------------------------------

func TestDBAccessor(t *testing.T) {
	svc, mem := newSvc(t)
	if svc.DB() != DB(mem) {
		t.Error("DB is the raw store")
	}
}

func TestValidateIntegration(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	svc.KindValid = func(port, kind string) bool { return kind != "unknown" }
	for _, tc := range []struct {
		name string
		in   Integration
		want string
	}{
		{"no name", Integration{Name: "  ", Port: PortRepository, Kind: "k"}, "name is required"},
		{"bad port", Integration{Name: "n", Port: "provider", Kind: "k"}, "port must be one of"},
		{"no kind", Integration{Name: "n", Port: PortRepository}, "kind is required"},
		{"unknown kind", Integration{Name: "n", Port: PortRepository, Kind: "unknown"}, `unknown kind "unknown"`},
		{"masked secret", Integration{Name: "n", Port: PortRepository, Kind: "k", Secrets: map[string]string{"token": maskPrefix + "abcd"}}, "masked placeholder"},
		{"a harness with a secret", Integration{Name: "n", Port: PortHarness, Kind: "k", Secrets: map[string]string{"key": "v"}}, "a harness holds no secrets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateIntegration(ctx, tc.in)
			wantErr(t, tc.name, err, ErrInvalid)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestUpdateIntegrationPatchesSecrets(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	in, err := svc.CreateIntegration(ctx, Integration{Name: "n", Port: PortRepository, Kind: "k", Config: map[string]any{"a": "1"},
		Secrets: map[string]string{"keep": "k", "drop": "d", "swap": "s"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.UpdateIntegration(ctx, in.ID, "", nil, map[string]string{"drop": "", "swap": "new", "add": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "n" || got.Config["a"] != "1" {
		t.Errorf("name and config stay when not sent: %+v", got)
	}
	if want := map[string]string{"keep": "k", "swap": "new", "add": "a"}; !reflect.DeepEqual(got.Secrets, want) {
		t.Errorf("secrets = %v", got.Secrets)
	}
	// What was sealed is what opens.
	back, err := svc.GetIntegration(ctx, in.ID)
	if err != nil || !reflect.DeepEqual(back.Secrets, got.Secrets) {
		t.Errorf("stored = %v, %v", back.Secrets, err)
	}
	got, err = svc.UpdateIntegration(ctx, in.ID, "renamed", map[string]any{"b": "2"}, nil)
	if err != nil || got.Name != "renamed" || got.Config["b"] != "2" || got.Config["a"] != nil {
		t.Errorf("config replaces whole: %+v, %v", got, err)
	}
	// The mask round-trips from a GET; it must not replace the real value.
	if _, err := svc.UpdateIntegration(ctx, in.ID, "", nil, map[string]string{"keep": maskPrefix + "k"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("masked = %v", err)
	}
	if _, err := svc.UpdateIntegration(ctx, "missing", "", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
}

func TestIntegrationStoreFailures(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	in, err := svc.CreateIntegration(ctx, Integration{Name: "n", Port: PortRepository, Kind: "k"})
	if err != nil {
		t.Fatal(err)
	}
	db.fail["UpsertIntegration"] = true
	_, err = svc.CreateIntegration(ctx, Integration{Name: "m", Port: PortRepository, Kind: "k"})
	wantErr(t, "create", err, errDB)
	_, err = svc.UpdateIntegration(ctx, in.ID, "x", nil, nil)
	wantErr(t, "update", err, errDB)
	db.fail["ListIntegrations"] = true
	_, err = svc.ListIntegrations(ctx, "")
	wantErr(t, "list", err, errDB)
}

// A store opened with the wrong key refuses, and says which row: it never
// hands back garbage.
func TestWrongKeyIsRefusedAndNamesTheRow(t *testing.T) {
	ctx := context.Background()
	svc, mem := newSvc(t)
	in, err := svc.CreateIntegration(ctx, Integration{Name: "n", Port: PortRepository, Kind: "gitlab", Secrets: map[string]string{"token": "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	other := anotherKey(t, mem)
	for name, err := range map[string]error{
		"get":  second(other.GetIntegration(ctx, in.ID)),
		"list": second(other.ListIntegrations(ctx, "")),
	} {
		wantErr(t, name, err, secrets.ErrKeyMismatch)
		if err != nil && !strings.Contains(err.Error(), in.ID) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: the error carries the secret", name)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func TestOpenGivesAMapForNoConfig(t *testing.T) {
	ctx := context.Background()
	svc, mem := newSvc(t)
	sealed, err := secrets.Seal(svc.key, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.UpsertIntegration(ctx, StoredIntegration{ID: "i", Port: PortRepository, Kind: "k", Name: "n", Sealed: sealed}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetIntegration(ctx, "i")
	if err != nil || got.Config == nil {
		t.Errorf("config = %v, %v", got.Config, err)
	}
}

func TestListIntegrationsByOwner(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	for _, u := range []string{"a", "b"} {
		if _, err := svc.CreateIntegration(ctx, Integration{UserID: u, Name: "n", Port: PortRepository, Kind: "k"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := svc.ListIntegrations(ctx, "a")
	if err != nil || len(got) != 1 || got[0].UserID != "a" {
		t.Errorf("a = %+v, %v", got, err)
	}
	if all, _ := svc.ListIntegrations(ctx, ""); len(all) != 2 {
		t.Errorf("all = %+v", all)
	}
}

func TestDeleteIntegrationNamesItsProjects(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p := seedProject(t, svc)
	for _, id := range []string{p.RepoAccountID, p.ProvisionerID, p.HarnessIDs[0]} {
		err := svc.DeleteIntegration(ctx, id)
		wantErr(t, "referenced", err, ErrReferenced)
		if err != nil && !strings.Contains(err.Error(), p.Name) {
			t.Errorf("err = %v", err)
		}
	}
	wantErr(t, "missing", svc.DeleteIntegration(ctx, "missing"), ErrNotFound)
}

func TestIntegrationNamesAreUniquePerOwnerAndPort(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	mk := func(u, port string) error {
		_, err := svc.CreateIntegration(ctx, Integration{UserID: u, Name: "same", Port: port, Kind: "k"})
		return err
	}
	if err := mk("a", PortRepository); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "again", mk("a", PortRepository), ErrConflict)
	if err := mk("b", PortRepository); err != nil {
		t.Errorf("another owner = %v", err)
	}
	if err := mk("a", PortTicketSource); err != nil {
		t.Errorf("another port = %v", err)
	}
}

// --- projects -----------------------------------------------------------------

func TestRepoNameFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/Docs.git": "docs",
		"git@github.com:acme/api.git":      "api",
		" https://x/y/ ":                   "y",
		"https://x/my.repo":                "my-repo",
		"plain":                            "plain",
		"":                                 "",
		"https://x/...":                    "",
	} {
		if got := RepoNameFromURL(in); got != want {
			t.Errorf("RepoNameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// goodProject returns a valid project on fresh integrations.
func goodProject(t *testing.T, svc *Service, owner string) Project {
	t.Helper()
	ctx := context.Background()
	mk := func(port string) string {
		in, err := svc.CreateIntegration(ctx, Integration{UserID: owner, Port: port, Kind: "k", Name: port + "-" + owner})
		if err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	return Project{UserID: owner, Name: "p-" + owner, RepoAccountID: mk(PortRepository), ProvisionerID: mk(PortProvisioner), HarnessIDs: []string{mk(PortHarness)},
		Repo: RepoConfig{URL: "https://git.example/g/p.git"}, DefaultFlow: "builder"}
}

func TestValidateProject(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	base := goodProject(t, svc, "o1")
	foreign := goodProject(t, svc, "o2")
	in2, err := svc.CreateIntegration(ctx, Integration{UserID: "o1", Port: PortHarness, Kind: "k", Name: "same-kind"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Project)
		want string
	}{
		{"no name", func(p *Project) { p.Name = " " }, "name is required"},
		{"no url", func(p *Project) { p.Repo.URL = "" }, "repo.url is required"},
		{"extra with no url", func(p *Project) { p.Repos = []ExtraRepo{{URL: " "}} }, "repos[0]: url is required"},
		{"extra with a bad name", func(p *Project) { p.Repos = []ExtraRepo{{Name: "Bad Name", URL: "u"}} }, "must be a slug"},
		{"extra named like the primary", func(p *Project) { p.Repos = []ExtraRepo{{Name: "repo", URL: "u"}} }, "already used"},
		{"extras sharing a name", func(p *Project) { p.Repos = []ExtraRepo{{URL: "https://x/a.git"}, {URL: "https://y/a.git"}} }, `name "a" is already used`},
		{"no repository account", func(p *Project) { p.RepoAccountID = "" }, "repo_account_id is required"},
		{"dangling account", func(p *Project) { p.RepoAccountID = "gone" }, "repo_account_id: integration gone"},
		{"someone else's account", func(p *Project) { p.RepoAccountID = foreign.RepoAccountID }, "repo_account_id"},
		{"account on the wrong port", func(p *Project) { p.RepoAccountID = p.ProvisionerID }, "need port repository"},
		{"no provisioner", func(p *Project) { p.ProvisionerID = "" }, "provisioner_id is required"},
		{"no harness", func(p *Project) { p.HarnessIDs = []string{""} }, "harness_ids is required"},
		{"dangling harness", func(p *Project) { p.HarnessIDs = []string{"gone"} }, "harness_ids: integration gone"},
		{"someone else's harness", func(p *Project) { p.HarnessIDs = foreign.HarnessIDs }, "harness_ids"},
		{"harness on the wrong port", func(p *Project) { p.HarnessIDs = []string{p.ProvisionerID} }, "need port harness"},
		{"two of a kind", func(p *Project) { p.HarnessIDs = []string{p.HarnessIDs[0], in2.ID} }, `two "k" harnesses`},
		{"no default flow", func(p *Project) { p.DefaultFlow = "" }, "default_flow is required"},
		{"unknown default flow", func(p *Project) { p.DefaultFlow = "no-such-flow" }, `default_flow "no-such-flow"`},
		{"bad service", func(p *Project) { p.Workspace.Services = []ServiceConfig{{Name: "Web"}} }, "must be a slug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.Repos, p.HarnessIDs = nil, slices.Clone(base.HarnessIDs)
			tc.edit(&p)
			_, err := svc.CreateProject(ctx, p)
			wantErr(t, tc.name, err, ErrInvalid)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestValidateProjectFillsDefaults(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p := goodProject(t, svc, "o1")
	p.HarnessIDs = []string{p.HarnessIDs[0], p.HarnessIDs[0], ""}
	p.Repos = []ExtraRepo{{URL: " https://x/Docs.git ", Locator: " 9 "}, {Name: "ctx", URL: "https://x/c.git", DefaultBranch: "dev"}}
	got, err := svc.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo.DefaultBranch != "main" || got.Repo.AgentsMDPath != "AGENTS.md" || got.Commands == nil {
		t.Errorf("defaults = %+v", got.Repo)
	}
	if len(got.HarnessIDs) != 1 {
		t.Errorf("the set dedupes: %v", got.HarnessIDs)
	}
	x := got.Repos[0]
	if x.Name != "docs" || x.URL != "https://x/Docs.git" || x.Locator != "9" || x.DefaultBranch != "main" {
		t.Errorf("extra = %+v", x)
	}
	if got.Repos[1].DefaultBranch != "dev" {
		t.Errorf("a stated branch stays: %+v", got.Repos[1])
	}
}

func TestProjectStoreFailures(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	good := goodProject(t, svc, "")
	p, err := svc.CreateProject(ctx, good)
	if err != nil {
		t.Fatal(err)
	}

	db.fail["GetIntegration"] = true
	_, err = svc.CreateProject(ctx, good)
	wantErr(t, "reference read", err, errDB)
	if errors.Is(err, ErrInvalid) {
		t.Errorf("a store failure is not the caller's: %v", err)
	}
	db.fail["GetIntegration"] = false

	// The harness reference fails on its own read.
	failHarness := &harnessReadFails{brokenDB: db, id: good.HarnessIDs[0]}
	byHarness := New(failHarness, svc.key)
	_, err = byHarness.CreateProject(ctx, good)
	wantErr(t, "harness read", err, errDB)

	db.fail["GetFlow"] = true
	_, err = svc.CreateProject(ctx, good)
	wantErr(t, "flow read", err, errDB)
	db.fail["GetFlow"] = false

	db.fail["UpsertProject"] = true
	_, err = svc.CreateProject(ctx, good)
	wantErr(t, "create", err, errDB)
	_, err = svc.UpdateProject(ctx, p.ID, p)
	wantErr(t, "update", err, errDB)
	db.fail["UpsertProject"] = false

	_, err = svc.UpdateProject(ctx, "missing", p)
	wantErr(t, "missing", err, ErrNotFound)
	bad := p
	bad.Name = ""
	_, err = svc.UpdateProject(ctx, p.ID, bad)
	wantErr(t, "invalid", err, ErrInvalid)
}

type harnessReadFails struct {
	*brokenDB
	id string
}

func (d *harnessReadFails) GetIntegration(ctx Ctx, id string) (StoredIntegration, error) {
	if id == d.id {
		return StoredIntegration{}, errDB
	}
	return d.brokenDB.GetIntegration(ctx, id)
}

func TestProjectOwnerNeverChanges(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p, err := svc.CreateProject(ctx, goodProject(t, svc, "o1"))
	if err != nil {
		t.Fatal(err)
	}
	p.UserID = "thief"
	got, err := svc.UpdateProject(ctx, p.ID, p)
	if err != nil || got.UserID != "o1" {
		t.Errorf("project = %+v, %v", got, err)
	}
}

func TestProjectLookups(t *testing.T) {
	ctx := context.Background()
	svc, mem := newSvc(t)
	a, err := svc.CreateProject(ctx, goodProject(t, svc, "o1"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateProject(ctx, goodProject(t, svc, "o2"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := svc.GetProjectByName(ctx, "o1", a.Name); err != nil || got.ID != a.ID {
		t.Errorf("by name = %+v, %v", got, err)
	}
	if got, err := svc.GetProjectByName(ctx, "", b.Name); err != nil || got.ID != b.ID {
		t.Errorf("any owner = %+v, %v", got, err)
	}
	if _, err := svc.GetProjectByName(ctx, "o2", a.Name); !errors.Is(err, ErrNotFound) {
		t.Errorf("another's = %v", err)
	}
	if got, err := svc.ListProjects(ctx, "o1"); err != nil || len(got) != 1 || got[0].ID != a.ID {
		t.Errorf("list = %+v, %v", got, err)
	}
	if got, _ := svc.ListProjects(ctx, ""); len(got) != 2 {
		t.Errorf("all = %+v", got)
	}

	// A second project of the name, for one owner, conflicts.
	dup := a
	dup.ID = "other"
	wantErr(t, "name", mem.UpsertProject(ctx, dup), ErrConflict)
}

// Deleting a project takes what is its own with it, and a run it still has
// refuses the delete.
func TestDeleteProject(t *testing.T) {
	ctx := context.Background()
	svc, mem := newSvc(t)
	p := seedProject(t, svc)
	if _, err := svc.SetProjectSecret(ctx, p.ID, "tok", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutFlowBinding(ctx, p.ID, "v1", map[string]string{"model": "fake/other"}); err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	cron, err := svc.CreateAutomation(ctx, Automation{ProjectID: p.ID, Name: "c", Kind: AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, Flow: "v1"})
	if err != nil {
		t.Fatal(err)
	}

	mem.BlockingRuns = func(string) []string { return []string{"r1", "r2"} }
	err = svc.DeleteProject(ctx, p.ID)
	wantErr(t, "blocked", err, ErrReferenced)
	if err == nil || !strings.Contains(err.Error(), "r1, r2") {
		t.Errorf("err = %v", err)
	}
	mem.BlockingRuns = func(string) []string { return nil }
	if err := svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetAutomation(ctx, cron.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("automation = %v", err)
	}
	if got, _ := mem.ListProjectSecrets(ctx, p.ID); len(got) != 0 {
		t.Errorf("secrets = %+v", got)
	}
	if got, _ := svc.ListFlowBindings(ctx, p.ID); len(got) != 0 {
		t.Errorf("bindings = %+v", got)
	}
	wantErr(t, "missing", svc.DeleteProject(ctx, p.ID), ErrNotFound)
}

func TestValidateServices(t *testing.T) {
	ok := ServiceConfig{Name: "web", Start: "run"}
	with := func(edit func(*ServiceConfig)) []ServiceConfig {
		s := ok
		edit(&s)
		return []ServiceConfig{s}
	}
	for _, tc := range []struct {
		name string
		in   []ServiceConfig
		want string
	}{
		{"unnamed", with(func(s *ServiceConfig) { s.Name = " " }), "service #1"},
		{"bad name", with(func(s *ServiceConfig) { s.Name = "Web" }), "must be a slug"},
		{"twice", []ServiceConfig{ok, ok}, "declared twice"},
		{"no start", with(func(s *ServiceConfig) { s.Start = " " }), "start is required"},
		{"two probes", with(func(s *ServiceConfig) { s.Health = ServiceHealth{TCP: 80, Command: "x"} }), "one probe"},
		{"probe not http", with(func(s *ServiceConfig) { s.Health.HTTP = "ftp://x/" }), "must be an http(s) URL"},
		{"probe with no host", with(func(s *ServiceConfig) { s.Health.HTTP = "http://" }), "must be an http(s) URL"},
		{"probe unparsable", with(func(s *ServiceConfig) { s.Health.HTTP = "http://[bad" }), "must be an http(s) URL"},
		{"tcp negative", with(func(s *ServiceConfig) { s.Health.TCP = -1 }), "must be a port"},
		{"tcp too high", with(func(s *ServiceConfig) { s.Health.TCP = 65536 }), "must be a port"},
		{"bad timeout", with(func(s *ServiceConfig) { s.Health.Timeout = "soon" }), "health.timeout"},
		{"zero interval", with(func(s *ServiceConfig) { s.Health.Interval = "0s" }), "health.interval"},
		{"port too low", with(func(s *ServiceConfig) { s.Ports = []ServicePort{{Port: 0}} }), "must be 1-65535"},
		{"port twice", with(func(s *ServiceConfig) { s.Ports = []ServicePort{{Port: 80}, {Port: 80}} }), "listed twice"},
		{"relative path", with(func(s *ServiceConfig) { s.Ports = []ServicePort{{Port: 80, Path: "x"}} }), "must start with /"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateServices(tc.in)
			wantErr(t, tc.name, err, ErrInvalid)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
	good := []ServiceConfig{{Name: " web ", Start: "run", Health: ServiceHealth{HTTP: "http://localhost:3000/health", Timeout: "30s", Interval: "5s"},
		Ports: []ServicePort{{Port: 3000, Path: "/app"}, {Port: 3001}}}, {Name: "db", Start: "run", Health: ServiceHealth{TCP: 5432}}}
	if err := validateServices(good); err != nil {
		t.Errorf("good = %v", err)
	}
	if good[0].Name != "web" {
		t.Errorf("the name is trimmed in place: %q", good[0].Name)
	}
}

func TestServiceHealthDurations(t *testing.T) {
	h := ServiceHealth{Timeout: "30s", Interval: "nonsense"}
	if h.TimeoutOr() != 30*time.Second || h.IntervalOr() != DefaultServiceInterval {
		t.Errorf("durations = %v, %v", h.TimeoutOr(), h.IntervalOr())
	}
	if (ServiceHealth{Timeout: "-1s"}).TimeoutOr() != DefaultServiceTimeout {
		t.Error("a negative duration is the default")
	}
}

// --- flows --------------------------------------------------------------------

func TestSaveFlowRefusals(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p := seedProject(t, svc)
	for _, tc := range []struct {
		name, project, flowName, doc string
		layout                       json.RawMessage
		target                       error
		want                         string
	}{
		{"no project", "", "v1", testFlowDoc, nil, ErrInvalid, "a project is required"},
		{"unknown project", "gone", "v1", testFlowDoc, nil, ErrNotFound, "project gone"},
		{"unparsable", p.ID, "v1", "name: [", nil, ErrInvalid, ""},
		{"misnamed", p.ID, "v2", testFlowDoc, nil, ErrInvalid, "names are immutable ids"},
		{"bad layout", p.ID, "v1", testFlowDoc, json.RawMessage(`{`), ErrInvalid, "layout is not valid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SaveFlow(ctx, tc.project, tc.flowName, tc.doc, tc.layout)
			wantErr(t, tc.name, err, tc.target)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestSaveFlowKeepsItsLayoutAndVersions(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p := seedProject(t, svc)
	layout := json.RawMessage(`{"nodes":[]}`)
	saved, err := svc.SaveFlow(ctx, p.ID, "v1", testFlowDoc, layout)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved.Layout) != string(layout) {
		t.Errorf("layout = %s", saved.Layout)
	}
	edited := strings.Replace(testFlowDoc, "build the thing", "build it well", 1)
	again, err := svc.SaveFlow(ctx, p.ID, "v1", edited, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Layout) != string(layout) {
		t.Errorf("a YAML edit wiped the layout: %s", again.Layout)
	}
	if again.Hash == saved.Hash {
		t.Error("an edit is a new hash")
	}
	versions, err := svc.FlowVersions(ctx, p.ID, "v1")
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions = %+v, %v", versions, err)
	}
	v, err := svc.FlowVersion(ctx, p.ID, saved.Hash)
	if err != nil || v.YAML != testFlowDoc {
		t.Errorf("version = %+v, %v", v, err)
	}
	if _, err := svc.FlowVersion(ctx, p.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
}

func TestSaveFlowStoreFailure(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	p := seedProject(t, svc)
	db.fail["UpsertFlow"] = true
	_, err := svc.SaveFlow(ctx, p.ID, "v1", testFlowDoc, nil)
	wantErr(t, "save", err, errDB)
}

func TestCopyFlow(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p := seedProject(t, svc)

	_, err := svc.CopyFlow(ctx, p.ID, "v1", p.ID, "")
	wantErr(t, "onto itself", err, ErrInvalid)

	copied, err := svc.CopyFlow(ctx, p.ID, "v1", p.ID, "v2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(copied.YAML, "name: v2") {
		t.Errorf("the copy names itself: %q", copied.YAML[:20])
	}
	_, err = svc.CopyFlow(ctx, p.ID, "v1", p.ID, "v2")
	wantErr(t, "name taken", err, ErrConflict)
	_, err = svc.CopyFlow(ctx, p.ID, "no-such-flow", p.ID, "v3")
	wantErr(t, "no source", err, ErrNotFound)
	// A template underneath is a source too.
	if _, err := svc.CopyFlow(ctx, "", "builder", p.ID, "mine"); err != nil {
		t.Errorf("from a template: %v", err)
	}
}

func TestCopyFlowStoreFailure(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	p := seedProject(t, svc)
	db.fail["GetFlow"] = true
	_, err := svc.CopyFlow(ctx, p.ID, "v1", p.ID, "v2")
	wantErr(t, "flow text", err, errDB)
	_, err = svc.saveCopy(ctx, testFlowDoc, "v1", p.ID, "v1")
	wantErr(t, "target read", err, errDB)
}

func TestSaveCopyRefusesAnUnrenamableDocument(t *testing.T) {
	svc, _ := newSvc(t)
	p := seedProject(t, svc)
	_, err := svc.saveCopy(context.Background(), "not: a flow\n", "a", p.ID, "b")
	wantErr(t, "rename", err, ErrInvalid)
}

func TestCopyTemplate(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p := seedProject(t, svc)
	got, err := svc.CopyTemplate(ctx, "builder", p.ID, "")
	if err != nil || got.Name != "builder" {
		t.Errorf("own name = %+v, %v", got, err)
	}
	got, err = svc.CopyTemplate(ctx, "builder", p.ID, "mine")
	if err != nil || got.Name != "mine" {
		t.Errorf("new name = %+v, %v", got, err)
	}
	_, err = svc.CopyTemplate(ctx, "builder", p.ID, "mine")
	wantErr(t, "taken", err, ErrConflict)
	_, err = svc.CopyTemplate(ctx, "no-such-template", p.ID, "")
	wantErr(t, "unknown", err, ErrNotFound)
}

// Deleting a project's flow takes its binding with it, unless a template is
// underneath: then the delete is a reset, and the configuration stands.
func TestDeleteFlowAndItsBinding(t *testing.T) {
	ctx := context.Background()
	svc, mem := newSvc(t)
	p := seedProject(t, svc)
	if _, err := svc.CopyFlow(ctx, p.ID, "v1", p.ID, "extra"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutFlowBinding(ctx, p.ID, "extra", map[string]string{"model": "fake/other"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFlow(ctx, p.ID, "extra"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetFlowBinding(ctx, p.ID, "extra"); !errors.Is(err, ErrNotFound) {
		t.Errorf("binding = %v", err)
	}
	// With no binding to go, nothing fails.
	if _, err := svc.CopyFlow(ctx, p.ID, "v1", p.ID, "bare"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFlow(ctx, p.ID, "bare"); err != nil {
		t.Errorf("no binding = %v", err)
	}

	if _, err := svc.CopyTemplate(ctx, "builder", p.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutFlowBinding(ctx, p.ID, "builder", map[string]string{"model": "fake/other"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteFlow(ctx, p.ID, "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetFlowBinding(ctx, p.ID, "builder"); err != nil {
		t.Errorf("a reset to the template keeps the binding: %v", err)
	}

	// The refusals, in the order the store checks them.
	wantErr(t, "missing", svc.DeleteFlow(ctx, p.ID, "nope"), ErrNotFound)
	err := svc.DeleteFlow(ctx, p.ID, "v1") // the project's default
	wantErr(t, "default", err, ErrReferenced)
	mem.FlowPinnedBy = func(string, string) []string { return []string{"r1"} }
	if _, err := svc.CopyFlow(ctx, p.ID, "v1", p.ID, "pinned"); err != nil {
		t.Fatal(err)
	}
	err = svc.DeleteFlow(ctx, p.ID, "pinned")
	wantErr(t, "pinned", err, ErrReferenced)
	if err != nil && !strings.Contains(err.Error(), "run r1") {
		t.Errorf("err = %v", err)
	}
}

func TestDeleteFlowBindingStoreFailure(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	p := seedProject(t, svc)
	if _, err := svc.CopyFlow(ctx, p.ID, "v1", p.ID, "extra"); err != nil {
		t.Fatal(err)
	}
	db.fail["DeleteFlowBinding"] = true
	wantErr(t, "binding delete", svc.DeleteFlow(ctx, p.ID, "extra"), errDB)
}

func TestFlowSource(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	p := seedProject(t, svc)
	src := svc.FlowSource()
	f, err := src.Get(ctx, p.ID, "v1")
	if err != nil || f.Name != "v1" {
		t.Fatalf("get = %+v, %v", f, err)
	}
	again, err := src.Get(ctx, p.ID, "v1")
	if err != nil || again != f {
		t.Errorf("a hash parses once: %p, %p, %v", f, again, err)
	}
	pinned, ok := src.(flow.PinnedSource)
	if !ok {
		t.Fatal("the source pins versions")
	}
	old := f.Hash
	if _, err := svc.SaveFlow(ctx, p.ID, "v1", strings.Replace(testFlowDoc, "build the thing", "changed", 1), nil); err != nil {
		t.Fatal(err)
	}
	if got, err := pinned.GetVersion(ctx, p.ID, "v1", old); err != nil || got != f {
		t.Errorf("a run keeps its document: %v", err)
	}
	if got, err := src.Get(ctx, p.ID, "v1"); err != nil || got.Hash == old {
		t.Errorf("current = %v, %v", got, err)
	}
	if _, err := src.Get(ctx, p.ID, "nope"); !errors.Is(err, flow.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	if _, err := pinned.GetVersion(ctx, p.ID, "v1", "nope"); !errors.Is(err, flow.ErrNotFound) {
		t.Errorf("missing version = %v", err)
	}

	db.fail["GetFlow"], db.fail["GetFlowVersion"] = true, true
	if _, err := src.Get(ctx, p.ID, "v1"); !errors.Is(err, errDB) {
		t.Errorf("store failure = %v", err)
	}
	if _, err := pinned.GetVersion(ctx, p.ID, "v1", old); !errors.Is(err, errDB) {
		t.Errorf("store failure = %v", err)
	}
	db.fail["GetFlow"], db.fail["GetFlowVersion"] = false, false

	// A row that does not parse is the parser's error, never a cached nil.
	if err := db.UpsertFlow(ctx, FlowRow{ProjectID: p.ID, Name: "bad", YAML: "name: [", Hash: "h-bad"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if f, err := src.Get(ctx, p.ID, "bad"); err == nil || f != nil {
			t.Errorf("bad = %v, %v", f, err)
		}
	}
}

// --- bindings -----------------------------------------------------------------

func TestPutFlowBinding(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	p := seedProject(t, svc)

	_, err := svc.PutFlowBinding(ctx, "gone", "v1", nil)
	wantErr(t, "no project", err, ErrNotFound)
	_, err = svc.PutFlowBinding(ctx, p.ID, "nope", nil)
	wantErr(t, "no flow", err, ErrNotFound)
	_, err = svc.PutFlowBinding(ctx, p.ID, "v1", map[string]string{"no_such_param": "x"})
	wantErr(t, "unknown parameter", err, ErrInvalid)

	if err := db.UpsertFlow(ctx, FlowRow{ProjectID: p.ID, Name: "bad", YAML: "name: ["}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.PutFlowBinding(ctx, p.ID, "bad", nil)
	wantErr(t, "unparsable", err, ErrInvalid)
	if err == nil || !strings.Contains(err.Error(), "cannot be configured") {
		t.Errorf("err = %v", err)
	}

	got, err := svc.PutFlowBinding(ctx, p.ID, "v1", map[string]string{" model ": " fake/other ", "harness": "  "})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Params, map[string]string{"model": "fake/other"}) {
		t.Errorf("trimmed, blanks dropped: %v", got.Params)
	}
	values, err := svc.Binding(ctx, p.ID, "v1")
	if err != nil || values["model"] != "fake/other" {
		t.Errorf("binding = %v, %v", values, err)
	}
	values["model"] = "changed"
	if again, _ := svc.Binding(ctx, p.ID, "v1"); again["model"] != "fake/other" {
		t.Error("a read must not alias the store")
	}
	if list, err := svc.ListFlowBindings(ctx, p.ID); err != nil || len(list) != 1 {
		t.Errorf("list = %+v, %v", list, err)
	}

	// Binding nothing removes the row, and again is no failure.
	for range 2 {
		got, err = svc.PutFlowBinding(ctx, p.ID, "v1", map[string]string{"model": " "})
		if err != nil || len(got.Params) != 0 {
			t.Fatalf("unbind = %+v, %v", got, err)
		}
	}
	if values, err := svc.Binding(ctx, p.ID, "v1"); err != nil || values != nil {
		t.Errorf("no binding = %v, %v", values, err)
	}
	if err := svc.DeleteFlowBinding(ctx, p.ID, "v1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete = %v", err)
	}

	db.fail["DeleteFlowBinding"] = true
	_, err = svc.PutFlowBinding(ctx, p.ID, "v1", nil)
	wantErr(t, "unbind store", err, errDB)
	db.fail["PutFlowBinding"] = true
	_, err = svc.PutFlowBinding(ctx, p.ID, "v1", map[string]string{"model": "fake/other"})
	wantErr(t, "bind store", err, errDB)
	db.fail["GetFlowBinding"] = true
	_, err = svc.Binding(ctx, p.ID, "v1")
	wantErr(t, "binding read", err, errDB)
}

func TestBindingNeedsAProject(t *testing.T) {
	err := NewMem().PutFlowBinding(context.Background(), FlowBinding{ProjectID: "gone", FlowName: "f"})
	wantErr(t, "binding", err, ErrInvalid)
}

// --- tickets ------------------------------------------------------------------

func TestValidateTicket(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p := seedProject(t, svc)
	for _, tc := range []struct {
		name string
		in   Ticket
		want string
	}{
		{"no title", Ticket{ProjectID: p.ID, Title: " "}, "title is required"},
		{"no project", Ticket{Title: "t"}, "project_id is required"},
		{"unknown project", Ticket{ProjectID: "gone", Title: "t"}, "project gone not found"},
		{"bad change type", Ticket{ProjectID: p.ID, Title: "t", ChangeType: "mend"}, `change type "mend"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateTicket(ctx, tc.in)
			wantErr(t, tc.name, err, ErrInvalid)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
	got, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: " t ", BaseBranch: " b ", ChangeType: " FIX "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "t" || got.BaseBranch != "b" || got.ChangeType != trackerport.ChangeFix {
		t.Errorf("normalised = %+v", got)
	}
	plain, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: "t"})
	if err != nil || plain.ChangeType != trackerport.ChangeFeat {
		t.Errorf("default = %+v, %v", plain, err)
	}
}

func TestTicketStoreFailures(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	p := seedProject(t, svc)
	tk, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateTicket(ctx, "missing", tk)
	wantErr(t, "missing", err, ErrNotFound)
	bad := tk
	bad.Title = ""
	_, err = svc.UpdateTicket(ctx, tk.ID, bad)
	wantErr(t, "invalid", err, ErrInvalid)
	db.fail["UpsertTicket"] = true
	_, err = svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: "t"})
	wantErr(t, "create", err, errDB)
	_, err = svc.UpdateTicket(ctx, tk.ID, tk)
	wantErr(t, "update", err, errDB)
}

func TestTicketsByOwner(t *testing.T) {
	ctx := context.Background()
	svc, mem := newSvc(t)
	a, err := svc.CreateProject(ctx, goodProject(t, svc, "o1"))
	if err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateTicket(ctx, Ticket{ProjectID: a.ID, Title: "t", UserID: "someone else"})
	if err != nil {
		t.Fatal(err)
	}
	if tk.UserID != "o1" {
		t.Errorf("a ticket belongs to its project's owner: %+v", tk)
	}
	if got, _ := svc.ListTickets(ctx, "o1"); len(got) != 1 {
		t.Errorf("mine = %+v", got)
	}
	if got, _ := svc.ListTickets(ctx, "o2"); len(got) != 0 {
		t.Errorf("theirs = %+v", got)
	}
	wantErr(t, "missing project", mem.UpsertTicket(ctx, Ticket{ID: "x", ProjectID: "gone"}), ErrInvalid)
	wantErr(t, "missing ticket", svc.DeleteTicket(ctx, "missing"), ErrNotFound)
}

// An edit form that does not know about provenance must not erase it, and an
// automation's mark is how its next poll knows the ticket for its own.
func TestUpdateTicketKeepsTheAutomation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p := seedProject(t, svc)
	tk, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: "t", AutomationID: "auto-1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.UpdateTicket(ctx, tk.ID, Ticket{ProjectID: p.ID, Title: "edited"})
	if err != nil || got.AutomationID != "auto-1" {
		t.Errorf("ticket = %+v, %v", got, err)
	}
}

// --- settings -----------------------------------------------------------------

func TestSettingsWindows(t *testing.T) {
	s := DefaultSettings()
	if s.CompletedWindow() != 48*time.Hour || s.OrphanWindow() != 24*time.Hour {
		t.Errorf("defaults = %v, %v", s.CompletedWindow(), s.OrphanWindow())
	}
	if _, ok := s.SnapshotWindow(); ok {
		t.Error("snapshots are kept by default")
	}
	s = Settings{DestroyCompletedAfter: "2h", OrphanUploadsAfter: "3h", SnapshotsAfter: "4h"}
	d, ok := s.SnapshotWindow()
	if s.CompletedWindow() != 2*time.Hour || s.OrphanWindow() != 3*time.Hour || !ok || d != 4*time.Hour {
		t.Errorf("set = %v, %v, %v, %v", s.CompletedWindow(), s.OrphanWindow(), d, ok)
	}
	// What cannot be parsed falls back to the safe reading: keep things.
	s = Settings{DestroyCompletedAfter: "x", OrphanUploadsAfter: "-1h", SnapshotsAfter: "x"}
	if s.CompletedWindow() != 48*time.Hour || s.OrphanWindow() != 24*time.Hour {
		t.Errorf("fallback = %v, %v", s.CompletedWindow(), s.OrphanWindow())
	}
	if _, ok := s.SnapshotWindow(); ok {
		t.Error("an unreadable retention keeps forever")
	}
	if _, ok := (Settings{SnapshotsAfter: "0s"}).SnapshotWindow(); ok {
		t.Error("zero is not a window")
	}
	if _, ok := (Settings{}).SnapshotWindow(); ok {
		t.Error("empty keeps forever")
	}
}

func TestSettingsValidate(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Errorf("defaults = %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Settings)
		want string
	}{
		{"destroy after", func(s *Settings) { s.DestroyCompletedAfter = "soon" }, "destroy_completed_after"},
		{"destroy negative", func(s *Settings) { s.DestroyCompletedAfter = "-1h" }, "destroy_completed_after"},
		{"abandoned", func(s *Settings) { s.DestroyAbandoned = "48h" }, "destroy_abandoned"},
		{"orphans", func(s *Settings) { s.OrphanUploadsAfter = "0s" }, "orphan_uploads_after"},
		{"snapshots", func(s *Settings) { s.SnapshotsAfter = "later" }, "snapshots_after"},
	} {
		s := DefaultSettings()
		tc.edit(&s)
		err := s.Validate()
		wantErr(t, tc.name, err, ErrInvalid)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	s := DefaultSettings()
	s.SnapshotsAfter = "72h"
	if err := s.Validate(); err != nil {
		t.Errorf("a duration = %v", err)
	}
}

func TestSettingsReadAndWrite(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)

	s := DefaultSettings()
	s.DestroyCompletedAfter = "1h"
	if err := svc.PutSettings(ctx, "u1", s); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Settings(ctx, "u1"); err != nil || got != s {
		t.Errorf("read back = %+v, %v", got, err)
	}
	if got, _ := svc.Settings(ctx, "u2"); got != DefaultSettings() {
		t.Errorf("settings are per user: %+v", got)
	}
	bad := s
	bad.DestroyCompletedAfter = "x"
	wantErr(t, "invalid", svc.PutSettings(ctx, "u1", bad), ErrInvalid)

	// A corrupt row reads as the default, an empty one (an older UI) too.
	if err := db.PutSetting(ctx, "u3", keyDestroyAfter, `7`); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSetting(ctx, "u3", keyOrphanUploads, `""`); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSetting(ctx, "u3", keySnapshotsAfter, `""`); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Settings(ctx, "u3"); err != nil || got != DefaultSettings() {
		t.Errorf("corrupt = %+v, %v", got, err)
	}

	db.fail["PutSetting"] = true
	wantErr(t, "put", svc.PutSettings(ctx, "u1", s), errDB)
	db.fail["GetSettings"] = true
	got, err := svc.Settings(ctx, "u1")
	wantErr(t, "get", err, errDB)
	if got != DefaultSettings() {
		t.Errorf("a failed read still has an answer: %+v", got)
	}

	if err := NewMem().PutSetting(ctx, "u", "k", "not json"); err == nil {
		t.Error("the column is jsonb")
	}
}

// --- credentials --------------------------------------------------------------

func TestCredentialRefIDs(t *testing.T) {
	for _, tc := range []struct {
		cfg  map[string]any
		want []string
	}{
		{nil, nil},
		{map[string]any{ConfigCredentialIDs: "not a list"}, nil},
		{map[string]any{ConfigCredentialIDs: []any{"a", 7, "", "b"}}, []string{"a", "b"}},
		{map[string]any{ConfigCredentialIDs: []string{"c"}}, []string{"c"}},
	} {
		if got := CredentialRefIDs(tc.cfg); !slices.Equal(got, tc.want) {
			t.Errorf("%v = %v", tc.cfg, got)
		}
	}
}

func TestCreateCredentialValidation(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	key := func(k string) providers.Credential {
		return providers.Credential{Provider: "anthropic", Kind: providers.KindAPIKey, Key: k}
	}
	for _, tc := range []struct {
		name string
		c    providers.Credential
		want string
	}{
		{"blank key", key("  "), "the API key is required"},
		{"masked key", key(maskPrefix + "abcd"), "masked placeholder"},
		{"login without tokens", providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth, Access: "a"}, "needs an access and a refresh"},
		{"unknown kind", providers.Credential{Provider: "anthropic", Kind: "cookie"}, `kind "cookie"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateCredential(ctx, "u1", tc.c, "")
			wantErr(t, tc.name, err, ErrInvalid)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
	got, err := svc.CreateCredential(ctx, "u1", key("sk"), "  mine  ")
	if err != nil || got.Name != "mine" || got.Sealed != nil || !got.Meta.LastRefresh.IsZero() {
		t.Errorf("named = %+v, %v", got, err)
	}
	got, err = svc.CreateCredential(ctx, "u1", key("sk"), "")
	if err != nil || got.Name != "Anthropic" {
		t.Errorf("unnamed = %+v, %v", got, err)
	}
	db.fail["UpsertCredential"] = true
	_, err = svc.CreateCredential(ctx, "u1", key("sk"), "")
	wantErr(t, "store", err, errDB)
}

func TestCredentialReadsNeverCarryTheSeal(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	for _, name := range []string{"b", "a"} {
		if _, err := svc.CreateCredential(ctx, "u1", providers.Credential{Provider: "anthropic", Kind: providers.KindAPIKey, Key: "sk"}, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.CreateCredential(ctx, "u2", providers.Credential{Provider: "anthropic", Kind: providers.KindAPIKey, Key: "sk"}, "c"); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListCredentials(ctx, "u1")
	if err != nil || len(list) != 2 || list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	for _, c := range list {
		if c.Sealed != nil {
			t.Errorf("sealed bytes in a listing: %s", c.Name)
		}
	}
	if all, _ := svc.ListCredentials(ctx, ""); len(all) != 3 {
		t.Errorf("all = %+v", all)
	}
	if one, err := svc.GetCredential(ctx, list[0].ID); err != nil || one.Sealed != nil {
		t.Errorf("get = %+v, %v", one, err)
	}
	if _, err := svc.GetCredential(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	db.fail["ListCredentials"] = true
	_, err = svc.ListCredentials(ctx, "u1")
	wantErr(t, "list", err, errDB)
}

func TestOpenCredentialFailures(t *testing.T) {
	ctx := context.Background()
	svc, mem := newSvc(t)
	pc, err := svc.CreateCredential(ctx, "u1", providers.Credential{Provider: "anthropic", Kind: providers.KindAPIKey, Key: "sk-s3cret"}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = svc.OpenCredential(ctx, "missing")
	wantErr(t, "missing", err, ErrNotFound)
	_, _, err = anotherKey(t, mem).OpenCredential(ctx, pc.ID)
	wantErr(t, "wrong key", err, secrets.ErrKeyMismatch)
	if err != nil && (strings.Contains(err.Error(), "sk-s3cret") || !strings.Contains(err.Error(), pc.ID)) {
		t.Errorf("err = %v", err)
	}
}

func TestCredentialUpdates(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	login := providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth, Access: "a", Refresh: "r", Extra: map[string]string{providers.ExtraEmail: "me@example.org"}}
	pc, err := svc.CreateCredential(ctx, "u1", login, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DisableCredential(ctx, pc.ID, "refresh refused"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.GetCredential(ctx, pc.ID); got.Meta.Status != CredentialDisabled || got.Meta.Error != "refresh refused" {
		t.Errorf("disabled = %+v", got.Meta)
	}

	// A refresh clears the failure; one that names no account keeps the old.
	login.ID, login.Access, login.Extra = pc.ID, "a2", nil
	got, err := svc.SaveRefreshed(ctx, login)
	if err != nil || got.Meta.Status != CredentialOK || got.Meta.Error != "" || got.Meta.Account != "me@example.org" || got.Sealed != nil {
		t.Errorf("refreshed = %+v, %v", got, err)
	}
	opened, _, err := svc.OpenCredential(ctx, pc.ID)
	if err != nil || opened.Access != "a2" {
		t.Errorf("opened = %+v, %v", opened, err)
	}

	got, err = svc.RenameCredential(ctx, pc.ID, "  new  ")
	if err != nil || got.Name != "new" || got.Sealed != nil {
		t.Errorf("renamed = %+v, %v", got, err)
	}
	_, err = svc.RenameCredential(ctx, pc.ID, " ")
	wantErr(t, "blank name", err, ErrInvalid)

	_, err = svc.RenameCredential(ctx, "missing", "x")
	wantErr(t, "rename missing", err, ErrNotFound)
	_, err = svc.SaveRefreshed(ctx, providers.Credential{ID: "missing"})
	wantErr(t, "refresh missing", err, ErrNotFound)
	wantErr(t, "disable missing", svc.DisableCredential(ctx, "missing", "x"), ErrNotFound)

	db.fail["UpsertCredential"] = true
	_, err = svc.RenameCredential(ctx, pc.ID, "x")
	wantErr(t, "rename store", err, errDB)
	_, err = svc.SaveRefreshed(ctx, login)
	wantErr(t, "refresh store", err, errDB)
	if err := svc.DisableCredential(ctx, pc.ID, "x"); !errors.Is(err, errDB) {
		t.Errorf("disable store = %v", err)
	}
}

func TestDeleteCredentialFailures(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	pc, err := svc.CreateCredential(ctx, "u1", providers.Credential{Provider: "anthropic", Kind: providers.KindAPIKey, Key: "sk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	wantErr(t, "missing", svc.DeleteCredential(ctx, "missing"), ErrNotFound)
	db.fail["ListIntegrations"] = true
	wantErr(t, "referrers", svc.DeleteCredential(ctx, pc.ID), errDB)
	if _, err := svc.IntegrationsUsing(ctx, pc.ID); !errors.Is(err, errDB) {
		t.Errorf("using = %v", err)
	}
	if _, err := svc.IntegrationsUsing(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("using missing = %v", err)
	}
	db.fail["ListIntegrations"] = false

	in, err := svc.CreateIntegration(ctx, Integration{UserID: "u1", Port: PortHarness, Kind: "opencode", Name: "oc",
		Config: map[string]any{ConfigCredentialIDs: []any{pc.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	using, err := svc.IntegrationsUsing(ctx, pc.ID)
	if err != nil || !slices.Equal(using, []string{in.ID}) {
		t.Errorf("using = %v, %v", using, err)
	}
	err = svc.DeleteCredential(ctx, pc.ID)
	wantErr(t, "referenced", err, ErrReferenced)
	if err != nil && !strings.Contains(err.Error(), "oc") {
		t.Errorf("err = %v", err)
	}
	if err := svc.DeleteIntegration(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteCredential(ctx, pc.ID); err != nil {
		t.Errorf("unreferenced = %v", err)
	}
	wantErr(t, "gone", db.Mem.DeleteCredential(ctx, pc.ID), ErrNotFound)
}

func TestValidateCredentialRefs(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	cred := func(user, provider, key string) string {
		pc, err := svc.CreateCredential(ctx, user, providers.Credential{Provider: provider, Kind: providers.KindAPIKey, Key: key}, provider+"-"+user)
		if err != nil {
			t.Fatal(err)
		}
		return pc.ID
	}
	mine := cred("u1", "anthropic", "k")
	theirs := cred("u2", "anthropic", "k")
	zen, goKey := cred("u1", "opencode-zen", "k"), cred("u1", "opencode-go", "k")
	if err := db.Mem.UpsertCredential(ctx, ProviderCredential{ID: "retired", UserID: "u1", Provider: "bygone", Kind: providers.KindAPIKey, Name: "old"}); err != nil {
		t.Fatal(err)
	}
	harness := func(kind string, ids ...any) (Integration, error) {
		return svc.CreateIntegration(ctx, Integration{UserID: "u1", Port: PortHarness, Kind: kind, Name: "h-" + t.Name(), Config: map[string]any{ConfigCredentialIDs: ids}})
	}
	for _, tc := range []struct {
		name string
		kind string
		ids  []any
		want string
	}{
		{"dangling", "omp", []any{"gone"}, "credential gone"},
		{"someone else's", "omp", []any{theirs}, "credential " + theirs},
		{"retired provider", "omp", []any{"retired"}, "no longer manages"},
		{"not taken by the harness", "codex", []any{mine}, "codex cannot use"},
		{"two in one place", "omp", []any{zen, goKey}, "in the same place"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := harness(tc.kind, tc.ids...)
			wantErr(t, tc.name, err, ErrInvalid)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
	got, err := harness("omp", mine, mine)
	if err != nil {
		t.Fatal(err)
	}
	if ids := got.Config[ConfigCredentialIDs].([]any); len(ids) != 1 {
		t.Errorf("deduped = %v", ids)
	}
	// A nil config is a harness with no credentials yet.
	empty, err := svc.CreateIntegration(ctx, Integration{Port: PortHarness, Kind: "omp", Name: "empty"})
	if err != nil || empty.Config == nil {
		t.Errorf("empty = %+v, %v", empty, err)
	}
	direct := Integration{Port: PortHarness, Kind: "omp"}
	if err := svc.validateCredentialRefs(ctx, &direct); err != nil || direct.Config == nil {
		t.Errorf("direct = %+v, %v", direct, err)
	}

	db.fail["GetCredential"] = true
	_, err = harness("omp", mine)
	wantErr(t, "store", err, errDB)
}

func TestInstanceSecret(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	first, err := svc.InstanceSecret(ctx, "broker")
	if err != nil || len(first) != 32 {
		t.Fatalf("first = %d bytes, %v", len(first), err)
	}
	again, err := svc.InstanceSecret(ctx, "broker")
	if err != nil || string(again) != string(first) {
		t.Errorf("a secret is stable: %v", err)
	}
	other, err := svc.InstanceSecret(ctx, "other")
	if err != nil || string(other) == string(first) {
		t.Errorf("each name its own: %v", err)
	}
	// Survives a restart, and refuses the wrong key rather than making a new one.
	if _, err := anotherKey(t, db).InstanceSecret(ctx, "broker"); !errors.Is(err, secrets.ErrKeyMismatch) {
		t.Errorf("wrong key = %v", err)
	}

	store := func(value string) {
		t.Helper()
		if err := db.PutSetting(ctx, "", "instance.secret.x", value); err != nil {
			t.Fatal(err)
		}
	}
	sealedEmpty, err := secrets.Seal(svc.key, map[string]string{"x": ""})
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := json.Marshal(base64.StdEncoding.EncodeToString(sealedEmpty))
	for _, tc := range []struct{ name, value, want string }{
		{"not a string", `7`, "instance secret x"},
		{"not base64", `"!!!"`, "instance secret x"},
		{"empty value", string(enc), "unreadable"},
	} {
		store(tc.value)
		if _, err := svc.InstanceSecret(ctx, "x"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}

	db.fail["PutSetting"] = true
	_, err = svc.InstanceSecret(ctx, "fresh")
	wantErr(t, "put", err, errDB)
	db.fail["GetSettings"] = true
	_, err = svc.InstanceSecret(ctx, "broker")
	wantErr(t, "get", err, errDB)
}

// --- project secrets ----------------------------------------------------------

func TestProjectSecretsValidationAndFailures(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	p := seedProject(t, svc)

	for _, tc := range []struct{ name, secret, value, want string }{
		{"bad name", "Bad Name", "v", "lowercase slug"},
		{"no value", "tok", "", "a value is required"},
		{"masked", "tok", maskPrefix + "abcd", "masked placeholder"},
	} {
		_, err := svc.SetProjectSecret(ctx, p.ID, tc.secret, tc.value)
		wantErr(t, tc.name, err, ErrInvalid)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	_, err := svc.SetProjectSecret(ctx, "gone", "tok", "v")
	wantErr(t, "no project", err, ErrNotFound)
	_, err = svc.ListProjectSecrets(ctx, "gone")
	wantErr(t, "no project", err, ErrNotFound)

	got, err := svc.SetProjectSecret(ctx, p.ID, "tok", "s3cret")
	if err != nil || got.Name != "tok" || got.UpdatedAt.IsZero() {
		t.Fatalf("set = %+v, %v", got, err)
	}
	open, err := svc.ProjectSecrets(ctx, p.ID)
	if err != nil || open["tok"] != "s3cret" {
		t.Errorf("open = %v, %v", open, err)
	}
	if list, err := svc.ListProjectSecrets(ctx, p.ID); err != nil || len(list) != 1 || list[0].Name != "tok" {
		t.Errorf("list = %+v, %v", list, err)
	}
	if err := svc.DeleteProjectSecret(ctx, p.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing = %v", err)
	}

	_, err = anotherKey(t, db).ProjectSecrets(ctx, p.ID)
	wantErr(t, "wrong key", err, secrets.ErrKeyMismatch)
	if err != nil && strings.Contains(err.Error(), "s3cret") {
		t.Errorf("err = %v", err)
	}

	db.fail["ListProjectSecrets"] = true
	_, err = svc.ListProjectSecrets(ctx, p.ID)
	wantErr(t, "list", err, errDB)
	_, err = svc.ProjectSecrets(ctx, p.ID)
	wantErr(t, "open", err, errDB)
	db.fail["PutProjectSecret"] = true
	_, err = svc.SetProjectSecret(ctx, p.ID, "tok", "v")
	wantErr(t, "put", err, errDB)

	wantErr(t, "no project", NewMem().PutProjectSecret(ctx, "gone", StoredProjectSecret{Name: "x"}), ErrInvalid)
}

// --- automations --------------------------------------------------------------

const onMRFlow = `name: rev
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
agents:
  - name: pr_reviewer
    prompt: review it
    inputs:
      - { name: changes, type: changeset }
    outputs:
      - { name: review, type: review_threads }
main:
  - { id: read, type: agent, harness: $harness, model: $model, agent: pr_reviewer, input: { changes: { from: run.git.diff_target, type: changeset } }, output: { review: { to: artifacts/review.yaml, type: review_threads } } }
  - { id: post, type: action, action: post_review, input: { review: { from: artifacts/review.yaml, type: review_threads } } }
outcomes:
  - { id: merged, on: mr_merged, then: done }
`

func TestValidateAutomation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p := seedProject(t, svc)
	other, err := svc.CreateProject(ctx, goodProject(t, svc, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveFlow(ctx, p.ID, "rev", onMRFlow, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.DB().UpsertFlow(ctx, FlowRow{ProjectID: p.ID, Name: "bad", YAML: "name: ["}); err != nil {
		t.Fatal(err)
	}
	source, err := svc.CreateIntegration(ctx, Integration{Port: PortTicketSource, Kind: "k", Name: "tracker"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	foreignTicket, err := svc.CreateTicket(ctx, Ticket{ProjectID: other.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	query := func(a Automation) Automation {
		a.ProjectID, a.Name, a.Kind, a.SourceID, a.Flow = p.ID, "a", AutomationTrackerQuery, source.ID, "v1"
		return a
	}
	cron := func(a Automation) Automation {
		a.ProjectID, a.Name, a.Kind, a.Schedule, a.TicketID, a.Flow = p.ID, "a", AutomationCron, "0 9 * * *", tk.ID, "v1"
		return a
	}
	mr := func(a Automation) Automation {
		a.ProjectID, a.Name, a.Kind, a.Flow = p.ID, "a", AutomationMergeRequest, "rev"
		return a
	}
	for _, tc := range []struct {
		name   string
		in     Automation
		target error
		want   string
	}{
		{"no name", Automation{ProjectID: p.ID}, ErrInvalid, "needs a name"},
		{"no project", Automation{ProjectID: "gone", Name: "a"}, ErrNotFound, "project"},
		{"a repo on another kind", func() Automation { a := query(Automation{}); a.Repo = "x"; return a }(), ErrInvalid, "watches a repository"},
		{"query without a source", func() Automation { a := query(Automation{}); a.SourceID = ""; return a }(), ErrInvalid, "needs source_id"},
		{"query with a missing source", func() Automation { a := query(Automation{}); a.SourceID = "gone"; return a }(), ErrNotFound, "gone"},
		{"query on the wrong port", func() Automation { a := query(Automation{}); a.SourceID = p.RepoAccountID; return a }(), ErrInvalid, "not a ticket source"},
		{"query with a schedule", query(Automation{Schedule: "0 9 * * *"}), ErrInvalid, "takes no schedule"},
		{"bad schedule", cron(Automation{}), nil, ""},
		{"cron with a bad schedule", func() Automation { a := cron(Automation{}); a.Schedule = "whenever"; return a }(), ErrInvalid, ""},
		{"cron without a ticket", func() Automation { a := cron(Automation{}); a.TicketID = ""; return a }(), ErrInvalid, "needs ticket_id"},
		{"cron with a missing ticket", func() Automation { a := cron(Automation{}); a.TicketID = "gone"; return a }(), ErrNotFound, "gone"},
		{"cron with another project's ticket", func() Automation { a := cron(Automation{}); a.TicketID = foreignTicket.ID; return a }(), ErrInvalid, "another project"},
		{"cron with a source", cron(Automation{SourceID: "x"}), ErrInvalid, "takes no source_id"},
		{"merge request with a query", mr(Automation{Query: "x"}), ErrInvalid, "takes only a repo"},
		{"merge request on an unknown repo", mr(Automation{Repo: "ghost"}), ErrInvalid, `no repository "ghost"`},
		{"unknown kind", Automation{ProjectID: p.ID, Name: "a", Kind: "telepathy"}, ErrInvalid, `kind "telepathy"`},
		{"no flow", func() Automation { a := query(Automation{}); a.Flow = " "; return a }(), ErrInvalid, "needs a flow"},
		{"unknown flow", func() Automation { a := query(Automation{}); a.Flow = "no-such"; return a }(), ErrNotFound, "no-such"},
		{"flow that does not parse", func() Automation { a := query(Automation{}); a.Flow = "bad"; return a }(), ErrInvalid, "does not parse"},
		{"unknown parameter", query(Automation{Params: map[string]string{"no_such": "x"}}), ErrInvalid, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.CreateAutomation(ctx, tc.in)
			if tc.target == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if err := svc.DeleteAutomation(ctx, got.ID); err != nil {
					t.Fatal(err)
				}
				return
			}
			wantErr(t, tc.name, err, tc.target)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestAutomationNormalisation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	p := seedProject(t, svc)
	if _, err := svc.SaveFlow(ctx, p.ID, "rev", onMRFlow, nil); err != nil {
		t.Fatal(err)
	}
	// "repo" is the primary's own name; the store keeps it as "".
	got, err := svc.CreateAutomation(ctx, Automation{ProjectID: p.ID, Name: " a ", Kind: AutomationMergeRequest, Flow: " rev ", Repo: " repo ",
		Params: map[string]string{" model ": " fake/other ", "harness": "  "}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "a" || got.Flow != "rev" || got.Repo != "" || !reflect.DeepEqual(got.Params, map[string]string{"model": "fake/other"}) {
		t.Errorf("normalised = %+v", got)
	}
	// No params left is none at all.
	bare, err := svc.CreateAutomation(ctx, Automation{ProjectID: p.ID, Name: "b", Kind: AutomationMergeRequest, Flow: "rev", Params: map[string]string{"model": " "}})
	if err != nil || bare.Params != nil {
		t.Errorf("no params = %+v, %v", bare, err)
	}

	// An extra repository of the project is watchable by name.
	p.Repos = []ExtraRepo{{Name: "docs", URL: "https://x/docs.git"}}
	if _, err := svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	extra, err := svc.CreateAutomation(ctx, Automation{ProjectID: p.ID, Name: "c", Kind: AutomationMergeRequest, Flow: "rev", Repo: "docs"})
	if err != nil || extra.Repo != "docs" {
		t.Errorf("extra = %+v, %v", extra, err)
	}
}

func TestUpdateAutomationKeepsWhatTheWatcherMarked(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	p := seedProject(t, svc)
	tk, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.CreateAutomation(ctx, Automation{ProjectID: p.ID, Name: "a", Kind: AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, Flow: "v1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if err := svc.MarkAutomation(ctx, a.ID, AutomationMark{PolledAt: at, FiredAt: at, Err: "boom"}); err != nil {
		t.Fatal(err)
	}
	upd := a
	upd.Schedule = "0 10 * * *"
	upd.LastError = "forged"
	got, err := svc.UpdateAutomation(ctx, a.ID, upd)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastError != "boom" || got.LastFiredAt == nil || !got.LastFiredAt.Equal(at) || got.CreatedAt != a.CreatedAt || got.Schedule != "0 10 * * *" {
		t.Errorf("updated = %+v", got)
	}

	// A mark with no stamps clears the error and leaves the times.
	if err := svc.MarkAutomation(ctx, a.ID, AutomationMark{}); err != nil {
		t.Fatal(err)
	}
	cleared, _ := svc.GetAutomation(ctx, a.ID)
	if cleared.LastError != "" || cleared.LastPolledAt == nil || !cleared.LastPolledAt.Equal(at) {
		t.Errorf("cleared = %+v", cleared)
	}

	_, err = svc.UpdateAutomation(ctx, "missing", upd)
	wantErr(t, "missing", err, ErrNotFound)
	bad := upd
	bad.Name = ""
	_, err = svc.UpdateAutomation(ctx, a.ID, bad)
	wantErr(t, "invalid", err, ErrInvalid)
	wantErr(t, "mark missing", svc.MarkAutomation(ctx, "missing", AutomationMark{}), ErrNotFound)

	db.fail["UpsertAutomation"] = true
	_, err = svc.CreateAutomation(ctx, upd)
	wantErr(t, "create", err, errDB)
	_, err = svc.UpdateAutomation(ctx, a.ID, upd)
	wantErr(t, "update", err, errDB)
	wantErr(t, "mark", svc.MarkAutomation(ctx, a.ID, AutomationMark{}), errDB)
}

func TestAutomationLists(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	p := seedProject(t, svc)
	other, err := svc.CreateProject(ctx, goodProject(t, svc, ""))
	if err != nil {
		t.Fatal(err)
	}
	source, err := svc.CreateIntegration(ctx, Integration{Port: PortTicketSource, Kind: "k", Name: "tracker"})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(project, name string, enabled bool) Automation {
		a, err := svc.CreateAutomation(ctx, Automation{ProjectID: project, Name: name, Kind: AutomationTrackerQuery, SourceID: source.ID, Flow: "builder", Enabled: enabled})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	mk(p.ID, "on", true)
	off := mk(p.ID, "off", false)
	mk(other.ID, "there", true)
	if got, err := svc.ListAutomations(ctx, p.ID); err != nil || len(got) != 2 {
		t.Errorf("project = %+v, %v", got, err)
	}
	enabled, err := svc.EnabledAutomations(ctx)
	if err != nil || len(enabled) != 2 {
		t.Fatalf("enabled = %+v, %v", enabled, err)
	}
	for _, a := range enabled {
		if a.ID == off.ID {
			t.Error("a disabled rule is not walked")
		}
	}
	wantErr(t, "delete", svc.DeleteAutomation(ctx, "missing"), ErrNotFound)
	db.fail["ListAutomations"] = true
	_, err = svc.EnabledAutomations(ctx)
	wantErr(t, "enabled", err, errDB)
}

func TestAutomationRequiresItsProject(t *testing.T) {
	wantErr(t, "upsert", NewMem().UpsertAutomation(context.Background(), Automation{ID: "a", ProjectID: "gone"}), ErrInvalid)
}

func TestValidateAutomationStoreFailures(t *testing.T) {
	ctx := context.Background()
	svc, db := newBroken(t)
	p := seedProject(t, svc)
	db.fail["GetFlow"] = true
	_, err := svc.CreateAutomation(ctx, Automation{ProjectID: p.ID, Name: "a", Kind: AutomationMergeRequest, Flow: "rev"})
	wantErr(t, "flow", err, errDB)
	db.fail["GetFlow"] = false

	// The project read every automation starts from.
	counting := &projectReadFails{brokenDB: db, after: 0}
	byCount := New(counting, svc.key)
	_, err = byCount.CreateAutomation(ctx, Automation{ProjectID: p.ID, Name: "a", Kind: AutomationMergeRequest, Flow: "rev", Repo: "docs"})
	wantErr(t, "project", err, errDB)
}

type projectReadFails struct {
	*brokenDB
	after int
}

func (d *projectReadFails) GetProject(ctx Ctx, id string) (Project, error) {
	if d.after == 0 {
		return Project{}, errDB
	}
	d.after--
	return d.brokenDB.GetProject(ctx, id)
}
