package integrations

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
	"apothecary/internal/project/resolution"
	"apothecary/internal/workspace/wsimage"
)

// --- scriptable kinds ---------------------------------------------------------

var errKind = errors.New("kind refused")

type accountFails struct{ kinds.FakeRepoHost }

func (accountFails) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "repository", Kind: "no-account"}
}
func (accountFails) Account(kinds.Config) (kinds.RepoAccount, error) { return nil, errKind }

type repoHostFails struct {
	kinds.FakeRepoHost
	badLocator string
}

func (repoHostFails) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "repository", Kind: "picky"}
}
func (k repoHostFails) RepoHost(cfg kinds.Config, repo repoport.RepoRef) (repoport.RepoHost, error) {
	if repo.Locator == k.badLocator {
		return nil, errKind
	}
	return k.FakeRepoHost.RepoHost(cfg, repo)
}

type failingAccount struct{ kinds.RepoAccount }

func (failingAccount) Test(context.Context) error { return errKind }

type accountTestFails struct{ kinds.FakeRepoHost }

func (accountTestFails) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "repository", Kind: "bad-probe"}
}
func (k accountTestFails) Account(cfg kinds.Config) (kinds.RepoAccount, error) {
	ac, _ := k.FakeRepoHost.Account(cfg)
	return failingAccount{ac}, nil
}

type provFails struct{ kinds.FakeProvisioner }

func (provFails) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "provisioner", Kind: "no-prov"}
}
func (provFails) New(kinds.Config, kinds.ProvisionerDeps) (kinds.Provisioner, error) {
	return nil, errKind
}

type sourceFails struct{ kinds.FakeTicketSource }

func (sourceFails) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "ticketsource", Kind: "no-source"}
}
func (sourceFails) New(kinds.Config) (trackerport.TicketSource, error) { return nil, errKind }

// scriptHarness wraps the fake harness with chosen answers, capabilities and
// failures: what a real harness kind can do, without a real vendor.
type scriptHarness struct {
	kinds.Harness
	kind                      string
	testErr, seedErr, prepErr error
	installErr, modelsErr     error
	models                    []kinds.ModelInfo
	env                       map[string]string
	secrets                   []string
	authFiles                 []string
	check                     kinds.AuthResult
	logins                    bool
	seen                      *kinds.Credentials
}

func (h scriptHarness) Test(ctx context.Context) error { return h.testErr }
func (h scriptHarness) EnvVars() map[string]string     { return h.env }
func (h scriptHarness) Binary() string                 { return h.kind + "-bin" }
func (h scriptHarness) AcceptsAnyModel() bool          { return false }
func (h scriptHarness) SecretValues() []string         { return h.secrets }
func (h scriptHarness) SeedAuth(context.Context, envport.EnvID, func(string)) error {
	return h.seedErr
}
func (h scriptHarness) Prepare(context.Context, envport.EnvID, []*agentspec.Spec) error {
	return h.prepErr
}
func (h scriptHarness) InstallSkills(context.Context, envport.EnvID, []string) error {
	return h.installErr
}
func (h scriptHarness) Models(context.Context) ([]kinds.ModelInfo, error) {
	return h.models, h.modelsErr
}
func (h scriptHarness) AuthFiles(context.Context, envport.EnvID) []string    { return h.authFiles }
func (h scriptHarness) CheckAuth(context.Context, []string) kinds.AuthResult { return h.check }
func (h scriptHarness) CheckCredentials(context.Context) kinds.AuthResult    { return h.check }
func (h scriptHarness) HasLogins() bool                                      { return h.logins }
func (h scriptHarness) Broker(func(string)) http.Handler                     { return http.NotFoundHandler() }

type scriptKind struct {
	name   string
	newErr error
	h      scriptHarness
}

func (k scriptKind) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "harness", Kind: k.name}
}
func (k scriptKind) New(cfg kinds.Config, envp envport.EnvProvisioner) (kinds.Harness, error) {
	if k.newErr != nil {
		return nil, k.newErr
	}
	inner, err := kinds.FakeHarness{}.New(cfg, envp)
	if err != nil {
		return nil, err
	}
	h := k.h
	h.Harness, h.kind = inner, k.name
	if h.seen != nil {
		*h.seen = cfg.Credentials
	}
	return h, nil
}

// bench is a store, a registry and a resolver with every failing kind known.
type bench struct {
	t     *testing.T
	svc   *confstore.Service
	reg   *kinds.Registry
	r     *Resolver
	db    *flakyDB
	made  *atomic.Int32
	close *atomic.Int32
}

// flakyDB fails what a test names.
type flakyDB struct {
	confstore.DB
	getProject, getIntegration, listSecrets error
}

func (d *flakyDB) GetProject(ctx context.Context, id string) (confstore.Project, error) {
	if d.getProject != nil {
		return confstore.Project{}, d.getProject
	}
	return d.DB.GetProject(ctx, id)
}

func (d *flakyDB) GetIntegration(ctx context.Context, id string) (confstore.StoredIntegration, error) {
	if d.getIntegration != nil {
		return confstore.StoredIntegration{}, d.getIntegration
	}
	return d.DB.GetIntegration(ctx, id)
}

func (d *flakyDB) ListProjectSecrets(ctx context.Context, id string) ([]confstore.StoredProjectSecret, error) {
	if d.listSecrets != nil {
		return nil, d.listSecrets
	}
	return d.DB.ListProjectSecrets(ctx, id)
}

func newBench(t *testing.T, harnesses ...scriptKind) *bench {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	db := &flakyDB{DB: confstore.NewMem()}
	svc := confstore.New(db, key)
	reg := kinds.NewRegistry()
	kinds.RegisterFakes(reg)
	made, closed := &atomic.Int32{}, &atomic.Int32{}
	reg.RegisterProvisioner(countingProvKind{made: made, closed: closed})
	reg.RegisterRepoHost(accountFails{})
	reg.RegisterRepoHost(repoHostFails{badLocator: "bad"})
	reg.RegisterRepoHost(accountTestFails{})
	reg.RegisterProvisioner(provFails{})
	reg.RegisterTicketSource(sourceFails{})
	for _, k := range harnesses {
		reg.RegisterHarness(k)
	}
	svc.KindValid = reg.Valid
	return &bench{t: t, svc: svc, reg: reg, r: New(svc, reg, t.TempDir(), 8080, "", ""), db: db, made: made, close: closed}
}

func (b *bench) integration(port, kind string) string {
	b.t.Helper()
	in, err := b.svc.CreateIntegration(context.Background(), confstore.Integration{Port: port, Kind: kind, Name: port + "-" + kind + "-" + b.t.Name()})
	if err != nil {
		b.t.Fatal(err)
	}
	return in.ID
}

// project makes a project on the given kinds; empty names the fakes.
func (b *bench) project(repoKind, provKind string, harnessIDs []string, edit func(*confstore.Project)) confstore.Project {
	b.t.Helper()
	if repoKind == "" {
		repoKind = "fake"
	}
	if provKind == "" {
		provKind = "counting"
	}
	if harnessIDs == nil {
		harnessIDs = []string{b.integration(confstore.PortHarness, "fake")}
	}
	p := confstore.Project{Name: "p", RepoAccountID: b.integration(confstore.PortRepository, repoKind),
		ProvisionerID: b.integration(confstore.PortProvisioner, provKind), HarnessIDs: harnessIDs,
		Repo: confstore.RepoConfig{URL: "https://git.example/g/p.git", Locator: "1"}, DefaultFlow: "builder"}
	if edit != nil {
		edit(&p)
	}
	made, err := b.svc.CreateProject(context.Background(), p)
	if err != nil {
		b.t.Fatal(err)
	}
	return made
}

// --- resolve ------------------------------------------------------------------

func TestResolveFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("a store failure is transient", func(t *testing.T) {
		b := newBench(t)
		p := b.project("", "", nil, nil)
		boom := errors.New("db down")
		b.db.getProject = boom
		_, err := b.r.Resolve(ctx, p.ID)
		if !errors.Is(err, boom) || errors.Is(err, resolution.ErrUnresolvable) {
			t.Errorf("project = %v", err)
		}
		b.db.getProject = nil
		b.db.getIntegration = boom
		if _, err := b.r.Resolve(ctx, p.ID); !errors.Is(err, boom) || errors.Is(err, resolution.ErrUnresolvable) {
			t.Errorf("integration = %v", err)
		}
	})

	for _, tc := range []struct {
		name       string
		repoKind   string
		provKind   string
		edit       func(*bench, *confstore.Project)
		harnesses  []scriptKind
		harnessIDs func(*bench) []string
		want       string
		sentinel   error
	}{
		{name: "the account cannot be built", repoKind: "no-account", want: "kind refused"},
		{name: "the repository locator is refused", repoKind: "picky",
			edit: func(_ *bench, p *confstore.Project) { p.Repo.Locator = "bad" }, want: "kind refused", sentinel: resolution.ErrUnresolvable},
		{name: "an extra repository's locator is refused", repoKind: "picky",
			edit: func(_ *bench, p *confstore.Project) {
				p.Repos = []confstore.ExtraRepo{{Name: "docs", URL: "https://git.example/g/docs.git", Locator: "bad"}}
			}, want: "repo docs", sentinel: resolution.ErrUnresolvable},
		{name: "the provisioner cannot be built", provKind: "no-prov", want: "provisioner", sentinel: resolution.ErrUnresolvable},
		{name: "the harness cannot be built", harnesses: []scriptKind{{name: "broken", newErr: errKind}},
			harnessIDs: func(b *bench) []string { return []string{b.integration(confstore.PortHarness, "broken")} },
			want:       "kind refused", sentinel: resolution.ErrUnresolvable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t, tc.harnesses...)
			var ids []string
			if tc.harnessIDs != nil {
				ids = tc.harnessIDs(b)
			}
			p := b.project(tc.repoKind, tc.provKind, ids, func(p *confstore.Project) {
				if tc.edit != nil {
					tc.edit(b, p)
				}
			})
			res, err := b.r.Resolve(ctx, p.ID)
			if err == nil || res != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("res %v, err %v", res, err)
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Errorf("err = %v, want %v", err, tc.sentinel)
			}
		})
	}
}

// The service refuses both of these at write time; the resolver holds the line
// for a row that got there some other way.
func TestResolveChecksTheHarnessSet(t *testing.T) {
	ctx := context.Background()
	b := newBench(t)
	p := b.project("", "", nil, nil)
	in, err := b.svc.CreateIntegration(ctx, confstore.Integration{Port: confstore.PortHarness, Kind: "fake", Name: "again"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ids  []string
		want string
	}{
		{[]string{}, "no harness integrations"},
		{[]string{p.HarnessIDs[0], in.ID}, `two "fake" harness integrations`},
	} {
		p.HarnessIDs = tc.ids
		if err := b.db.UpsertProject(ctx, p); err != nil {
			t.Fatal(err)
		}
		res, err := b.r.Resolve(ctx, p.ID)
		if !errors.Is(err, resolution.ErrUnresolvable) || res != nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("res %v, err %v", res, err)
		}
	}
}

// A project stored before validateProject refused credentialed repository
// URLs never resolves: the URL would reach the clone script, the exec log and
// errors. The refusal says why without quoting it.
func TestResolveRefusesARepositoryURLWithCredentials(t *testing.T) {
	ctx := context.Background()
	b := newBench(t)
	p := b.project("", "", nil, nil)
	const token = "glpat-STORED-SECRET"
	for name, edit := range map[string]func(*confstore.Project){
		"primary": func(p *confstore.Project) { p.Repo.URL = "https://oauth2:" + token + "@git.example/g/p.git" },
		"extra": func(p *confstore.Project) {
			p.Repos = []confstore.ExtraRepo{{Name: "docs", URL: "https://oauth2:" + token + "@git.example/g/docs.git"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			stored := p
			edit(&stored)
			if err := b.db.UpsertProject(ctx, stored); err != nil {
				t.Fatal(err)
			}
			res, err := b.r.Resolve(ctx, p.ID)
			if !errors.Is(err, resolution.ErrUnresolvable) || res != nil || !strings.Contains(err.Error(), "embeds credentials") {
				t.Fatalf("res %v, err %v", res, err)
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("the refusal quotes the credential: %v", err)
			}
		})
	}
}

// A resolution that fails after taking the provisioner gives it back: a stale
// generation would otherwise never close.
func TestResolveReleasesTheProvisionerOnFailure(t *testing.T) {
	ctx := context.Background()
	b := newBench(t)
	p := b.project("", "", nil, nil)
	boom := errors.New("secrets unreadable")
	b.db.listSecrets = boom
	if _, err := b.r.Resolve(ctx, p.ID); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	b.r.mu.Lock()
	entry := b.r.cache[p.ProvisionerID]
	b.r.mu.Unlock()
	if entry == nil || entry.refs != 0 {
		t.Errorf("entry = %+v, want a released one", entry)
	}
}

// Extra repositories: each with a locator gets its own bound port, one
// without is context only; both reach the egress allowlist.
func TestResolveExtraRepositories(t *testing.T) {
	ctx := context.Background()
	b := newBench(t)
	p := b.project("", "", nil, func(p *confstore.Project) {
		p.Repos = []confstore.ExtraRepo{
			{Name: "docs", URL: "https://docs.example/g/docs.git", Locator: "2", DefaultBranch: "main"},
			{Name: "ctx", URL: "https://ctx.example/g/ctx.git"},
		}
	})
	res, err := b.r.Resolve(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if len(res.ExtraRepos) != 2 || res.RepoHosts["docs"] == nil {
		t.Fatalf("extras %+v hosts %v", res.ExtraRepos, res.RepoHosts)
	}
	if _, has := res.RepoHosts["ctx"]; has {
		t.Error("an extra with no locator is never published")
	}
	if res.RepoHostFor("ctx") != nil || res.RepoHostFor("docs") == nil {
		t.Error("repo host lookup")
	}
	for _, want := range []string{"git.example", "docs.example", "ctx.example"} {
		if !slices.Contains(res.EgressDomains, want) {
			t.Errorf("egress %v lacks %s", res.EgressDomains, want)
		}
	}
}

func TestResolvedClosures(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("harness failed")
	real := scriptKind{name: "real", h: scriptHarness{
		models: []kinds.ModelInfo{{ID: "fake/one"}, {ID: "fake/two"}}, env: map[string]string{"KEY": "v"},
		secrets: []string{"tok"}, authFiles: []string{"/home/x/auth.json"},
		check: kinds.AuthResult{Rejected: []string{"no"}, Unverified: []string{"maybe"}}, logins: true}}
	b := newBench(t, real)
	p := b.project("", "", []string{b.integration(confstore.PortHarness, "fake"), b.integration(confstore.PortHarness, "real")}, nil)
	res, err := b.r.Resolve(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()

	if ids, err := res.Models(ctx, "real"); err != nil || !slices.Equal(ids, []string{"fake/one", "fake/two"}) {
		t.Errorf("models = %v, %v", ids, err)
	}
	if ids, err := res.Models(ctx, "fake"); err != nil || ids != nil {
		t.Errorf("any model = %v, %v", ids, err)
	}
	if _, err := res.Models(ctx, "other"); err == nil || !strings.Contains(err.Error(), `"other" is not in the project's set`) {
		t.Errorf("unknown kind = %v", err)
	}
	rejected, unverified := res.CheckAuth(ctx, "real", nil)
	if !slices.Equal(rejected, []string{"no"}) || !slices.Equal(unverified, []string{"maybe"}) {
		t.Errorf("check = %v, %v", rejected, unverified)
	}
	if rejected, unverified := res.CheckAuth(ctx, "fake", nil); rejected != nil || unverified != nil {
		t.Errorf("nothing to probe = %v, %v", rejected, unverified)
	}
	if res.ProviderEnv["KEY"] != "v" || !slices.Contains(res.HarnessSecrets, "tok") {
		t.Errorf("env %v secrets %v", res.ProviderEnv, res.HarnessSecrets)
	}
	if got := res.AuthFiles(ctx, "e"); !slices.Equal(got, []string{"/home/x/auth.json"}) {
		t.Errorf("auth files = %v", got)
	}
	if res.AuthBroker == nil || res.AuthBroker(func(string) {}) == nil {
		t.Error("a harness with logins gets the broker")
	}
	if err := res.SeedAuth(ctx, "e", func(string) {}); err != nil {
		t.Errorf("seed = %v", err)
	}
	if err := res.Prepare(ctx, "e", nil); err != nil {
		t.Errorf("prepare = %v", err)
	}
	if err := res.InstallSkills(ctx, "e", nil); err != nil {
		t.Errorf("skills = %v", err)
	}

	// Each set-wide step names the harness that failed.
	failing := scriptKind{name: "real", h: scriptHarness{seedErr: boom, prepErr: boom, installErr: boom, modelsErr: boom}}
	b = newBench(t, failing)
	p = b.project("", "", []string{b.integration(confstore.PortHarness, "real")}, nil)
	res, err = b.r.Resolve(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	for name, err := range map[string]error{
		"seed real auth":      res.SeedAuth(ctx, "e", func(string) {}),
		"prepare real config": res.Prepare(ctx, "e", nil),
		"install real skills": res.InstallSkills(ctx, "e", nil),
	} {
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := res.Models(ctx, "real"); !errors.Is(err, boom) {
		t.Errorf("models = %v", err)
	}
	if res.AuthBroker != nil {
		t.Error("no logins, no broker")
	}
}

func TestEgressDomains(t *testing.T) {
	p := confstore.Project{Repo: confstore.RepoConfig{URL: "://bad"}, Repos: []confstore.ExtraRepo{{URL: "not a url"}, {URL: "https://x.example/a.git"}},
		Workspace: confstore.WorkspaceConfig{ExtraDomains: []string{"", "b.example", "a.example"}}}
	got := egressDomains(p, []string{"", "a.example"}, nil)
	if !slices.Equal(got, []string{"a.example", "b.example", "x.example"}) {
		t.Errorf("domains = %v", got)
	}
}

func TestServicesDefaultDurations(t *testing.T) {
	got := services([]confstore.ServiceConfig{{Name: "web", Start: "go run .", Stop: "kill", Description: "d",
		Health: confstore.ServiceHealth{HTTP: "http://localhost:1/", Timeout: "1m"},
		Ports:  []confstore.ServicePort{{Port: 1, Label: "app", Path: "/x"}}}})
	if len(got) != 1 || got[0].Health.Timeout != time.Minute || got[0].Health.Interval <= 0 || got[0].Ports[0].Label != "app" || got[0].Stop != "kill" {
		t.Errorf("services = %+v", got)
	}
	if got := services(nil); len(got) != 0 {
		t.Errorf("none = %+v", got)
	}
}

// --- provisioner cache --------------------------------------------------------

func TestProvisionerAcquireFailsForAnUnknownKind(t *testing.T) {
	b := newBench(t)
	p := b.project("", "", nil, nil)
	r := New(b.svc, func() *kinds.Registry {
		reg := kinds.NewRegistry()
		kinds.RegisterFakes(reg)
		return reg
	}(), t.TempDir(), 0, "", "")
	if _, err := r.Resolve(context.Background(), p.ID); !errors.Is(err, resolution.ErrUnresolvable) || !strings.Contains(err.Error(), "counting") {
		t.Errorf("err = %v", err)
	}
}

// --- credentials --------------------------------------------------------------

type keeper struct {
	fresh map[string]providers.Credential
	calls atomic.Int32
}

func (k *keeper) Fresh(_ context.Context, id string) (providers.Credential, error) {
	k.calls.Add(1)
	c, ok := k.fresh[id]
	if !ok {
		return providers.Credential{}, errors.New("login disabled")
	}
	return c, nil
}

func TestCredentialsThroughTheKeeper(t *testing.T) {
	ctx := context.Background()
	var seen kinds.Credentials
	b := newBench(t, scriptKind{name: "real", h: scriptHarness{seen: &seen}})
	k := &keeper{fresh: map[string]providers.Credential{"ok": {ID: "ok", Provider: "anthropic", Kind: providers.KindAPIKey, Key: "k"}}}
	b.r.SetLogins(k)
	in := confstore.Integration{Name: "h", Config: map[string]any{confstore.ConfigCredentialIDs: []any{"ok", "gone"}}}
	got := b.r.credentials(ctx, in)
	if len(got.Snapshot) != 1 || got.Snapshot[0].Key != "k" {
		t.Errorf("snapshot = %+v", got.Snapshot)
	}
	if got.Problems["gone"] != "login disabled" {
		t.Errorf("problems = %v", got.Problems)
	}
	// Seeding skips what cannot be used rather than failing the workspace.
	fresh, err := got.Fresh(ctx)
	if err != nil || len(fresh) != 1 || fresh[0].ID != "ok" {
		t.Errorf("fresh = %+v, %v", fresh, err)
	}
}

func TestCredentialsWithoutAKeeperOpenTheStore(t *testing.T) {
	ctx := context.Background()
	b := newBench(t)
	c, err := b.svc.CreateCredential(ctx, "u1", providers.Credential{Provider: "anthropic", Kind: providers.KindAPIKey, Key: "sk-stored"}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := b.r.credentials(ctx, confstore.Integration{Config: map[string]any{confstore.ConfigCredentialIDs: []any{c.ID, "missing"}}})
	if len(got.Snapshot) != 1 || got.Snapshot[0].Key != "sk-stored" || got.Problems["missing"] == "" {
		t.Errorf("credentials = %+v", got)
	}
}

// --- images -------------------------------------------------------------------

type refusingDocker struct{ calls chan []string }

func (d refusingDocker) Run(_ context.Context, _ string, args ...string) (string, int, error) {
	select {
	case d.calls <- args:
	default:
	}
	return "", 1, errors.New("no docker here")
}

// The warm-up builds through the swapped builder, in the background, and a
// failure leaves the resolver standing for the provision that retries it.
func TestWarmImagesUsesTheBuilder(t *testing.T) {
	b := newBench(t)
	d := refusingDocker{calls: make(chan []string, 64)}
	builder := wsimage.NewWithRunner(d, t.TempDir())
	b.r.SetImageBuilder(builder)
	b.r.WarmImages(context.Background(), nil)
	select {
	case args := <-d.calls:
		if len(args) == 0 {
			t.Error("an empty docker call")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the warm-up never reached the builder")
	}
	// The builder runs one build at a time: this waits the warm-up out, and
	// the failure it logged left the builder usable.
	if _, err := builder.EnsureTools(context.Background()); err == nil {
		t.Error("nothing can build here")
	}
}

// --- integration operations ---------------------------------------------------

func TestTestIntegration(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("bad auth material")
	b := newBench(t,
		scriptKind{name: "static-fails", h: scriptHarness{testErr: boom}},
		scriptKind{name: "rejected", h: scriptHarness{check: kinds.AuthResult{Rejected: []string{"key one", "key two"}, Unverified: []string{"maybe"}}}},
		scriptKind{name: "unverified", h: scriptHarness{check: kinds.AuthResult{Unverified: []string{"maybe"}}}},
		scriptKind{name: "unbuildable", newErr: errKind},
	)
	if _, err := b.r.TestIntegration(ctx, "missing"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	if warnings, err := b.r.TestIntegration(ctx, b.integration(confstore.PortHarness, "static-fails")); !errors.Is(err, boom) || warnings != nil {
		t.Errorf("static = %v, %v", warnings, err)
	}
	warnings, err := b.r.TestIntegration(ctx, b.integration(confstore.PortHarness, "rejected"))
	if err == nil || err.Error() != "key one; key two" || !slices.Equal(warnings, []string{"maybe"}) {
		t.Errorf("rejected = %v, %v", warnings, err)
	}
	warnings, err = b.r.TestIntegration(ctx, b.integration(confstore.PortHarness, "unverified"))
	if err != nil || !slices.Equal(warnings, []string{"maybe"}) {
		t.Errorf("unverified = %v, %v", warnings, err)
	}
	if _, err := b.r.TestIntegration(ctx, b.integration(confstore.PortHarness, "unbuildable")); !errors.Is(err, errKind) {
		t.Errorf("unbuildable = %v", err)
	}
	if _, err := b.r.TestIntegration(ctx, b.integration(confstore.PortHarness, "fake")); err != nil {
		t.Errorf("no auth checker = %v", err)
	}

	for _, tc := range []struct {
		port, kind string
		ok         bool
	}{
		{confstore.PortRepository, "bad-probe", false},
		{confstore.PortRepository, "no-account", false},
		{confstore.PortProvisioner, "counting", true},
		{confstore.PortProvisioner, "no-prov", false},
		{confstore.PortTicketSource, "fake", true},
		{confstore.PortTicketSource, "no-source", false},
	} {
		_, err := b.r.TestIntegration(ctx, b.integration(tc.port, tc.kind))
		if tc.ok != (err == nil) || !tc.ok && !errors.Is(err, errKind) {
			t.Errorf("%s/%s = %v", tc.port, tc.kind, err)
		}
	}
	if err := b.r.testIntegration(ctx, confstore.Integration{ID: "x", Port: "telepathy"}); err == nil || !strings.Contains(err.Error(), `unknown port "telepathy"`) {
		t.Errorf("unknown port = %v", err)
	}
}

// Kinds the running process does not know are named on every path that looks
// one up.
func TestUnregisteredKinds(t *testing.T) {
	ctx := context.Background()
	b := newBench(t)
	repo := b.integration(confstore.PortRepository, "fake")
	prov := b.integration(confstore.PortProvisioner, "counting")
	src := b.integration(confstore.PortTicketSource, "fake")
	hs := b.integration(confstore.PortHarness, "fake")
	r := New(b.svc, kinds.NewRegistry(), t.TempDir(), 0, "", "")
	for name, err := range map[string]error{
		"repository":  second(r.TestIntegration(ctx, repo)),
		"provisioner": second(r.TestIntegration(ctx, prov)),
		"ticket":      second(r.TestIntegration(ctx, src)),
		"harness":     second(r.TestIntegration(ctx, hs)),
		"repos":       second(r.ListRepos(ctx, repo)),
		"models":      second(r.ListModels(ctx, hs)),
		"source":      second(r.TicketSource(ctx, src)),
	} {
		if err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func TestListRepos(t *testing.T) {
	ctx := context.Background()
	b := newBench(t)
	if _, err := b.r.ListRepos(ctx, "missing"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	if _, err := b.r.ListRepos(ctx, b.integration(confstore.PortRepository, "no-account")); !errors.Is(err, errKind) {
		t.Errorf("account = %v", err)
	}
}

func TestListBranches(t *testing.T) {
	ctx := context.Background()
	b := newBench(t)
	p := b.project("", "", nil, nil)
	got, err := b.r.ListBranches(ctx, p.ID)
	if err != nil || len(got) != 2 || !got[0].Default {
		t.Errorf("branches = %+v, %v", got, err)
	}
	if _, err := b.r.ListBranches(ctx, "missing"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	other := newBench(t)
	broken := other.project("no-account", "", nil, nil)
	if _, err := other.r.ListBranches(ctx, broken.ID); !errors.Is(err, errKind) {
		t.Errorf("account = %v", err)
	}
	r := New(b.svc, kinds.NewRegistry(), t.TempDir(), 0, "", "")
	if _, err := r.ListBranches(ctx, p.ID); !errors.Is(err, resolution.ErrUnresolvable) {
		t.Errorf("unknown kind = %v", err)
	}
}

func TestTicketSources(t *testing.T) {
	ctx := context.Background()
	b := newBench(t)
	id := b.integration(confstore.PortTicketSource, "fake")
	src, err := b.r.TicketSource(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := src.Fetch(ctx, "DEV-1"); err != nil || got.Key != "DEV-1" {
		t.Errorf("fetch = %+v, %v", got, err)
	}
	if kind, err := b.r.TicketSourceKind(ctx, id); err != nil || kind != "fake" {
		t.Errorf("kind = %q, %v", kind, err)
	}
	if _, err := b.r.TicketSource(ctx, "missing"); !errors.Is(err, resolution.ErrUnresolvable) {
		t.Errorf("missing = %v", err)
	}
	if kind, err := b.r.TicketSourceKind(ctx, "missing"); !errors.Is(err, resolution.ErrUnresolvable) || kind != "" {
		t.Errorf("missing kind = %q, %v", kind, err)
	}
	// A ticket source is addressed by id, so the port is checked.
	if _, err := b.r.TicketSource(ctx, b.integration(confstore.PortRepository, "fake")); !errors.Is(err, resolution.ErrUnresolvable) {
		t.Errorf("wrong port = %v", err)
	}
	if _, err := b.r.TicketWriter(ctx, b.integration(confstore.PortTicketSource, "no-source")); !errors.Is(err, errKind) {
		t.Errorf("unbuildable = %v", err)
	}
}

func TestListModels(t *testing.T) {
	ctx := context.Background()
	b := newBench(t,
		scriptKind{name: "real", h: scriptHarness{models: []kinds.ModelInfo{{ID: "acme/one"}}}},
		scriptKind{name: "unbuildable", newErr: errKind})
	got, err := b.r.ListModels(ctx, b.integration(confstore.PortHarness, "real"))
	if err != nil || len(got) != 1 || got[0].ID != "acme/one" {
		t.Errorf("models = %+v, %v", got, err)
	}
	if _, err := b.r.ListModels(ctx, "missing"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	if _, err := b.r.ListModels(ctx, b.integration(confstore.PortRepository, "fake")); err == nil || !strings.Contains(err.Error(), "needs a harness") {
		t.Errorf("wrong port = %v", err)
	}
	if _, err := b.r.ListModels(ctx, b.integration(confstore.PortHarness, "unbuildable")); !errors.Is(err, errKind) {
		t.Errorf("unbuildable = %v", err)
	}
}
