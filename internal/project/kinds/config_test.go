package kinds

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
)

func TestConfigReads(t *testing.T) {
	c := Config{Values: map[string]any{"s": "v", "empty": "", "n": 1, "ints": []any{float64(1), 2, "x"}, "not": "a list"}}
	for _, tc := range []struct{ key, def, want string }{
		{"s", "d", "v"}, {"empty", "d", "d"}, {"n", "d", "d"}, {"missing", "d", "d"},
	} {
		if got := c.Str(tc.key, tc.def); got != tc.want {
			t.Errorf("Str(%q) = %q", tc.key, got)
		}
	}
	if got := c.Ints("ints"); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("Ints = %v", got)
	}
	if got := c.Ints("not"); got != nil {
		t.Errorf("Ints of a string = %v", got)
	}
}

func TestRegistryLookups(t *testing.T) {
	r := NewRegistry()
	RegisterFakes(r)
	if k, err := r.RepoHost("fake"); err != nil || k.Descriptor().Port != "repository" {
		t.Errorf("repo host = %v, %v", k, err)
	}
	if k, err := r.Provisioner("fake"); err != nil || k.Descriptor().Port != "provisioner" {
		t.Errorf("provisioner = %v, %v", k, err)
	}
	if k, err := r.Harness("fake"); err != nil || k.Descriptor().Port != "harness" {
		t.Errorf("harness = %v, %v", k, err)
	}
	if k, err := r.TicketSource("fake"); err != nil || k.Descriptor().Port != "ticketsource" {
		t.Errorf("ticket source = %v, %v", k, err)
	}

	empty := NewRegistry()
	for name, err := range map[string]error{
		`unknown repository kind "x"`:    second(empty.RepoHost("x")),
		`unknown provisioner kind "x"`:   second(empty.Provisioner("x")),
		`unknown harness kind "x"`:       second(empty.Harness("x")),
		`unknown ticket source kind "x"`: second(empty.TicketSource("x")),
	} {
		if err == nil || err.Error() != name {
			t.Errorf("err = %v, want %q", err, name)
		}
	}
	for _, port := range []string{"repository", "provisioner", "harness", "ticketsource"} {
		if !r.Valid(port, "fake") || r.Valid(port, "other") {
			t.Errorf("Valid(%s) wrong", port)
		}
	}
	if r.Valid("nothing", "fake") {
		t.Error("an unknown port is not valid")
	}

	// GET /api/kinds: stable, by port then kind.
	ds := r.Descriptors()
	var order []string
	for _, d := range ds {
		order = append(order, d.Port+"/"+d.Kind)
	}
	if !slices.Equal(order, []string{"harness/fake", "provisioner/fake", "repository/fake", "ticketsource/fake"}) {
		t.Errorf("order = %v", order)
	}
	r.RegisterRepoHost(GitHub{})
	r.RegisterTicketSource(Jira{})
	r.RegisterTicketSource(GitHubIssues{})
	order = nil
	for _, d := range r.Descriptors() {
		order = append(order, d.Port+"/"+d.Kind)
	}
	if !slices.IsSorted(order) {
		t.Errorf("not sorted: %v", order)
	}
}

func second[T any](_ T, err error) error { return err }

// Every kind's form states its secrets as secret and what it requires: a
// token rendered as a plain input would sit in the page.
func TestDescriptorsFlagSecretsAndRequired(t *testing.T) {
	for _, tc := range []struct {
		d        Descriptor
		secret   []string
		required []string
	}{
		{GitHub{}.Descriptor(), []string{"token"}, []string{"base_url", "token"}},
		{GitLab{}.Descriptor(), []string{"token"}, []string{"base_url", "token"}},
		{GitHubIssues{}.Descriptor(), []string{"token"}, []string{"base_url", "token"}},
		{GitLabIssues{}.Descriptor(), []string{"token"}, []string{"base_url", "token"}},
		{Jira{}.Descriptor(), []string{"api_token"}, []string{"base_url", "api_token"}},
		{Kubernetes{}.Descriptor(), []string{"kubeconfig"}, nil},
		{Docker{}.Descriptor(), nil, nil},
	} {
		t.Run(tc.d.Port+"/"+tc.d.Kind, func(t *testing.T) {
			var secret, required []string
			for _, f := range tc.d.Fields {
				if f.Secret {
					secret = append(secret, f.Name)
				}
				if f.Required {
					required = append(required, f.Name)
				}
			}
			if !slices.Equal(secret, tc.secret) || !slices.Equal(required, tc.required) {
				t.Errorf("secret %v required %v", secret, required)
			}
		})
	}
	for _, d := range []Descriptor{Opencode{}.Descriptor(), Codex{}.Descriptor(), Omp{}.Descriptor()} {
		if len(d.Fields) != 1 || d.Fields[0].Name != "credential_ids" || d.Fields[0].Type != "credentials" || !d.Fields[0].Required {
			t.Errorf("%s fields = %+v", d.Kind, d.Fields)
		}
	}
}

func TestCredentialsFieldNamesWhatTheHarnessTakes(t *testing.T) {
	f := credentialsField(providers.Codex)
	for _, want := range []string{"OpenAI", "ChatGPT Plus/Pro (login)"} {
		if !strings.Contains(f.Help, want) {
			t.Errorf("help = %q", f.Help)
		}
	}
	if joinNames(nil) != "" || joinNames([]string{"a"}) != "a" || joinNames([]string{"a", "b"}) != "a, b" {
		t.Error("joinNames")
	}
}

func TestGitHubLocatorValidation(t *testing.T) {
	cfg := Config{Secrets: map[string]string{"token": "t"}}
	for locator, ok := range map[string]bool{
		"owner/repo":                    true,
		" owner/repo ":                  true,
		"owner/repo.git":                true,
		"https://github.com/owner/repo": false,
		"owner":                         false,
		"owner/repo/sub":                false,
		"/owner":                        false,
		"owner/":                        false,
		"":                              false,
	} {
		host, err := GitHub{}.RepoHost(cfg, repoport.RepoRef{Locator: locator})
		if ok != (err == nil) {
			t.Errorf("%q: %v", locator, err)
		}
		if !ok && (host != nil || !strings.Contains(err.Error(), `"owner/repo"`)) {
			t.Errorf("%q: host %v, err %v", locator, host, err)
		}
	}
}

func TestGitLabLocatorValidation(t *testing.T) {
	for locator, ok := range map[string]bool{"42": true, "group/project": false, "": false} {
		host, err := GitLab{}.RepoHost(Config{}, repoport.RepoRef{Locator: locator})
		if ok != (err == nil) {
			t.Errorf("%q: %v", locator, err)
		}
		if !ok && (host != nil || !strings.Contains(err.Error(), "numeric project id")) {
			t.Errorf("%q: %v, %v", locator, host, err)
		}
	}
}

// The repository host a kind builds talks to the configured host, with the
// configured token.
func TestRepoHostsUseTheConfiguredBaseAndToken(t *testing.T) {
	var ghAuth, glToken string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		ghAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"login":"lu"}`))
	})
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		glToken = r.Header.Get("PRIVATE-TOKEN")
		_, _ = w.Write([]byte(`{"username":"lu"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	cfg := Config{Values: map[string]any{"base_url": ts.URL}, Secrets: map[string]string{"token": "s3cret"}}
	ctx := context.Background()

	gh, err := GitHub{}.RepoHost(cfg, repoport.RepoRef{Locator: "a/b"})
	if err != nil {
		t.Fatal(err)
	}
	if who, err := gh.(interface {
		Whoami(context.Context) (string, error)
	}).Whoami(ctx); err != nil || who != "lu" || ghAuth != "Bearer s3cret" {
		t.Errorf("github = %q, %v, %q", who, err, ghAuth)
	}
	gl, err := GitLab{}.RepoHost(cfg, repoport.RepoRef{Locator: "7"})
	if err != nil {
		t.Fatal(err)
	}
	if who, err := gl.(interface {
		Whoami(context.Context) (string, error)
	}).Whoami(ctx); err != nil || who != "lu" || glToken != "s3cret" {
		t.Errorf("gitlab = %q, %v, %q", who, err, glToken)
	}
}

// accountStub is a host answering both vendors' account calls.
func accountStub(t *testing.T, status int) *httptest.Server {
	t.Helper()
	reply := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user", reply(`{"login":"lu"}`))
	mux.HandleFunc("GET /user/repos", reply(`[{"full_name":"lu/a","clone_url":"https://git.example/lu/a.git","default_branch":"dev"}]`))
	mux.HandleFunc("GET /repos/lu/a", reply(`{"default_branch":"dev"}`))
	mux.HandleFunc("GET /repos/lu/a/branches", reply(`[{"name":"dev"},{"name":"x"}]`))
	mux.HandleFunc("GET /api/v4/user", reply(`{"username":"lu"}`))
	mux.HandleFunc("GET /api/v4/projects", reply(`[{"id":7,"path_with_namespace":"lu/a","http_url_to_repo":"https://git.example/lu/a.git","default_branch":"dev"}]`))
	mux.HandleFunc("GET /api/v4/projects/7/repository/branches", reply(`[{"name":"dev","default":true},{"name":"x"}]`))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestRepoAccounts(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		kind    RepoHostKind
		locator string
		user    string
		repo    RepoInfo
	}{
		{"github", GitHub{}, "lu/a", "x-access-token", RepoInfo{Name: "lu/a", URL: "https://git.example/lu/a.git", Locator: "lu/a", DefaultBranch: "dev"}},
		{"gitlab", GitLab{}, "7", "oauth2", RepoInfo{Name: "lu/a", URL: "https://git.example/lu/a.git", Locator: "7", DefaultBranch: "dev"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := accountStub(t, 200)
			cfg := Config{Values: map[string]any{"base_url": ts.URL, "git_name": "Lu", "git_email": "lu@example.org"}, Secrets: map[string]string{"token": "s3cret"}}
			ac, err := tc.kind.Account(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := ac.Test(ctx); err != nil {
				t.Errorf("test = %v", err)
			}
			repos, err := ac.ListRepos(ctx)
			if err != nil || len(repos) != 1 || repos[0] != tc.repo {
				t.Errorf("repos = %+v, %v", repos, err)
			}
			branches, err := ac.ListBranches(ctx, tc.locator)
			if err != nil || !reflect.DeepEqual(branches, []BranchInfo{{Name: "dev", Default: true}, {Name: "x"}}) {
				t.Errorf("branches = %+v, %v", branches, err)
			}
			if user, pass := ac.GitCredential(); user != tc.user || pass != "s3cret" {
				t.Errorf("credential = %q, %q", user, pass)
			}
			if name, email := ac.GitIdentity(); name != "Lu" || email != "lu@example.org" {
				t.Errorf("identity = %q, %q", name, email)
			}

			down := accountStub(t, 500)
			bad, err := tc.kind.Account(Config{Values: map[string]any{"base_url": down.URL}, Secrets: map[string]string{"token": "s3cret"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := bad.Test(ctx); err == nil || strings.Contains(err.Error(), "s3cret") {
				t.Errorf("test = %v", err)
			}
			if got, err := bad.ListRepos(ctx); err == nil || got != nil {
				t.Errorf("repos = %v, %v", got, err)
			}
			if got, err := bad.ListBranches(ctx, tc.locator); err == nil || got != nil {
				t.Errorf("branches = %v, %v", got, err)
			}

			// No token: public repositories only, and nothing to store.
			public, err := tc.kind.Account(Config{Values: map[string]any{"base_url": ts.URL}})
			if err != nil {
				t.Fatal(err)
			}
			if user, pass := public.GitCredential(); user != "" || pass != "" {
				t.Errorf("public credential = %q, %q", user, pass)
			}
		})
	}
}

func TestRepoAccountEgress(t *testing.T) {
	gh, _ := GitHub{}.Account(Config{})
	if got := gh.EgressDomains(); !slices.Contains(got, "github.com") || !slices.Contains(got, "codeload.github.com") {
		t.Errorf("github = %v", got)
	}
	for base, want := range map[string][]string{
		"https://gitlab.acme.example:8443/": {"gitlab.acme.example"},
		"gitlab.acme.example":               nil, // no scheme, no host
		"://bad":                            nil,
	} {
		ac, _ := GitLab{}.Account(Config{Values: map[string]any{"base_url": base}})
		if got := ac.EgressDomains(); !slices.Equal(got, want) {
			t.Errorf("%q = %v", base, got)
		}
	}
	gl, _ := GitLab{}.Account(Config{})
	if got := gl.EgressDomains(); !slices.Equal(got, []string{"gitlab.com"}) {
		t.Errorf("default = %v", got)
	}
}

// What the Jira kind accepts reaches the adapter: the version and the scope.
// The labels and the issue type's id come back for the integration's lists.
func TestJiraConfigReachesTheAdapter(t *testing.T) {
	var path, jql string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rest/api/2/search/jql", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		path, jql = r.URL.Path, b["jql"].(string)
		_, _ = w.Write([]byte(`{"issues":[{"id":"100","key":"P-1","fields":{"issuetype":{"id":"7","name":"Outage"},"labels":["ops"]}}]}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	src, err := Jira{}.New(Config{
		Values:  map[string]any{"base_url": ts.URL, "api_version": "2", "jql": "project = P"},
		Secrets: map[string]string{"api_token": "s3cret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Search(context.Background(), "x", 5)
	if err != nil || len(got) != 1 || got[0].TypeID != "7" || got[0].RemoteID != "100" || !slices.Equal(got[0].Labels, []string{"ops"}) {
		t.Fatalf("search = %+v, %v", got, err)
	}
	if path == "" || !strings.HasPrefix(jql, "(project = P)") {
		t.Errorf("path %q jql %q", path, jql)
	}
	if _, ok := src.(trackerport.TicketMetadata); !ok {
		t.Error("the Jira source lists its statuses and types")
	}
}

func TestSplitList(t *testing.T) {
	if got := splitList(" a, ,b ,"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("list = %q", got)
	}
	if got := splitList(""); got != nil {
		t.Errorf("empty = %q", got)
	}
}

// The docker kind's paths reach the adapter: its Test names the CLI it tried.
// Neither exists, so nothing runs, and a context ending early cuts the spawn
// retries short.
func TestDockerNamesItsConfiguredCLIs(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-cli")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, err := Docker{}.New(Config{Values: map[string]any{"devcontainer_path": missing}}, ProvisionerDeps{RunsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Test(ctx); err == nil || !strings.Contains(err.Error(), "devcontainer CLI not runnable") || !strings.Contains(err.Error(), missing) {
		t.Errorf("devcontainer = %v", err)
	}
	p, err = Docker{}.New(Config{Values: map[string]any{"devcontainer_path": "true", "docker_path": missing, "firewall": "off"}}, ProvisionerDeps{RunsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	short, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := p.Test(short); err == nil || !strings.Contains(err.Error(), "docker CLI not runnable") || !strings.Contains(err.Error(), missing) {
		t.Errorf("docker = %v", err)
	}
}

func TestKubernetesDescriptorShowsTheDeploymentsAnswers(t *testing.T) {
	d := Kubernetes{Defaults: KubernetesDefaults{Namespace: "work", PullPolicy: "Always"}}.Descriptor()
	byName := map[string]Field{}
	for _, f := range d.Fields {
		byName[f.Name] = f
	}
	if byName["namespace"].Placeholder != "work" || byName["image_pull_policy"].Placeholder != "Always" {
		t.Errorf("fields = %+v", byName["namespace"])
	}
	bare := Kubernetes{}.Descriptor()
	for _, f := range bare.Fields {
		if f.Name == "image_pull_policy" && f.Placeholder != "IfNotPresent" {
			t.Errorf("default pull policy = %q", f.Placeholder)
		}
	}
}

const testKubeconfig = "apiVersion: v1\nkind: Config\n"

// kubeconfigs lists the materialized kubeconfig files under a runs dir.
func kubeconfigs(t *testing.T, runsDir string) []string {
	t.Helper()
	got, err := filepath.Glob(filepath.Join(runsDir, "k8s", "*.kubeconfig"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Each refusal names the setting, and leaves no credential on disk: a
// materialized kubeconfig goes with the failed construction.
func TestKubernetesValidation(t *testing.T) {
	good := map[string]any{"namespace": "w", "engine_url": "http://engine:8080"}
	with := func(k, v string) map[string]any {
		out := map[string]any{}
		for key, val := range good {
			out[key] = val
		}
		if v == "" {
			delete(out, k)
		} else {
			out[k] = v
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		values map[string]any
		want   string
	}{
		{"no namespace", with("namespace", ""), "no namespace"},
		{"no engine url", with("engine_url", ""), "no engine URL"},
		{"bad storage", with("storage", "nfs"), `storage must be pvc or emptydir, got "nfs"`},
		{"unprivileged without a runtime", with("privileged", "off"), "privileged=off needs a runtime_class"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p, err := Kubernetes{}.New(Config{Values: tc.values, Secrets: map[string]string{"kubeconfig": testKubeconfig}}, ProvisionerDeps{RunsDir: dir})
			if err == nil || p != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("provisioner %v, err %v", p, err)
			}
			if left := kubeconfigs(t, dir); len(left) != 0 {
				t.Errorf("a refused integration left its kubeconfig: %v", left)
			}
		})
	}

	// A runs dir that cannot hold the kubeconfig is a kubeconfig failure.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Kubernetes{}.New(Config{Values: good, Secrets: map[string]string{"kubeconfig": testKubeconfig}}, ProvisionerDeps{RunsDir: file})
	if err == nil || !strings.Contains(err.Error(), "kubernetes: kubeconfig") {
		t.Errorf("err = %v", err)
	}
}

func TestKubernetesBuildsAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	// The integration's values win; the deployment answers the rest.
	k := Kubernetes{Defaults: KubernetesDefaults{Namespace: "from-env", EngineURL: "http://env:8080", Transport: "portforward"}}
	p, err := k.New(Config{
		Values:  map[string]any{"storage": "EmptyDir", "privileged": "off", "runtime_class": "sysbox-runc"},
		Secrets: map[string]string{"kubeconfig": testKubeconfig},
	}, ProvisionerDeps{RunsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	left := kubeconfigs(t, dir)
	if len(left) != 1 {
		t.Fatalf("kubeconfigs = %v", left)
	}
	if info, err := os.Stat(left[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("a kubeconfig is a credential: %v, %v", info.Mode(), err)
	}
	p.Close()
	if left := kubeconfigs(t, dir); len(left) != 0 {
		t.Errorf("close left %v", left)
	}

	// A path is the operator's file: used, never written, never removed.
	path := filepath.Join(dir, "mine.kubeconfig")
	if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	k.Defaults.Kubeconfig = path
	p, err = k.New(Config{}, ProvisionerDeps{RunsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the operator's kubeconfig is gone: %v", err)
	}
}

// --- fakes -------------------------------------------------------------------

func TestFakeAccountAndRepoHost(t *testing.T) {
	ac, err := FakeRepoHost{}.Account(Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ac.Test(ctx); err != nil {
		t.Error(err)
	}
	repos, _ := ac.ListRepos(ctx)
	branches, _ := ac.ListBranches(ctx, "1")
	if len(repos) != 1 || repos[0].Locator != "1" || len(branches) != 2 || !branches[0].Default {
		t.Errorf("repos %+v branches %+v", repos, branches)
	}
	if u, p := ac.GitCredential(); u != "" || p != "" {
		t.Error("the fake has no credential")
	}
	if n, e := ac.GitIdentity(); n != "" || e != "" {
		t.Error("the fake has no identity")
	}
	if ac.EgressDomains() != nil {
		t.Error("the fake reaches nothing")
	}
	// One repository host per locator, so what a run opened is still there.
	a, _ := FakeRepoHost{}.RepoHost(Config{}, repoport.RepoRef{Locator: "kinds-test"})
	b, _ := FakeRepoHost{}.RepoHost(Config{}, repoport.RepoRef{Locator: "kinds-test"})
	c, _ := FakeRepoHost{}.RepoHost(Config{}, repoport.RepoRef{Locator: "kinds-test-2"})
	if a != b || a == c || a != repoport.RepoHost(FakeRepoHostOf("kinds-test")) {
		t.Error("fake repository hosts are per locator")
	}
}

func TestFakeProvisioner(t *testing.T) {
	p, err := FakeProvisioner{}.New(Config{}, ProvisionerDeps{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Test(context.Background()); err != nil {
		t.Error(err)
	}
	if id, err := p.Provision(context.Background(), envport.EnvSpec{RunID: "r"}); err != nil || id == "" {
		t.Errorf("provision = %q, %v", id, err)
	}
}

func TestFakeTicketSourceServesTheImportFlow(t *testing.T) {
	src, err := FakeTicketSource{}.New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	got, err := src.Fetch(ctx, "DEV-1")
	if err != nil || len(got.Attachments) != 1 {
		t.Fatalf("fetch = %+v, %v", got, err)
	}
	rc, err := src.OpenAttachment(ctx, got.Attachments[0])
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if w, ok := src.(trackerport.TicketWriter); !ok {
		t.Error("the fake tracker takes writes")
	} else if err := w.Transition(ctx, "DEV-1", "In Progress"); err != nil {
		t.Errorf("transition = %v", err)
	}
}

func newFakeHarness(t *testing.T) (Harness, *fake.EnvProvisioner, envport.EnvID) {
	t.Helper()
	envp := fake.NewEnvProvisioner()
	id, err := envp.Provision(context.Background(), envport.EnvSpec{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := FakeHarness{}.New(Config{}, envp)
	if err != nil {
		t.Fatal(err)
	}
	return h, envp, id
}

func TestFakeHarnessWritesWhatTheContractAsks(t *testing.T) {
	h, envp, id := newFakeHarness(t)
	ctx := context.Background()
	prompt := "you MUST write these files (relative to repo root):\n- artifacts/plan.md — plan: the plan\n\nrest"
	res, err := h.Run(ctx, id, agentport.AgentSpec{Agent: "dev"}, agentport.AgentInput{Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	if res.TurnsUsed != 3 || res.Usage.CostUSD == 0 {
		t.Errorf("result = %+v", res)
	}
	data, err := envp.ReadFile(ctx, id, "artifacts/plan.md")
	if err != nil || !strings.Contains(string(data), `agent "dev"`) {
		t.Errorf("artifact = %q, %v", data, err)
	}
	if _, err := h.Resume(ctx, id, "sess-1", agentport.AgentInput{}); err != nil {
		t.Errorf("resume = %v", err)
	}
}

func TestFakeHarnessHonoursCancellation(t *testing.T) {
	h, _, id := newFakeHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Run(ctx, id, agentport.AgentSpec{Agent: "dev"}, agentport.AgentInput{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestFakeHarnessCapabilities(t *testing.T) {
	h, _, id := newFakeHarness(t)
	ctx := context.Background()
	if err := h.Test(ctx); err != nil {
		t.Error(err)
	}
	if h.Binary() != "true" || h.EnvVars() != nil || h.EgressDomains() != nil {
		t.Errorf("binary %q env %v", h.Binary(), h.EnvVars())
	}
	if err := h.SeedAuth(ctx, id, func(string) { t.Error("the fake has no secret") }); err != nil {
		t.Error(err)
	}
	if err := h.Prepare(ctx, id, nil); err != nil {
		t.Error(err)
	}
	if err := h.InstallSkills(ctx, id, []string{"pdf"}); err != nil {
		t.Error(err)
	}
	fh := h.(*fakeHarness)
	if len(fh.runner.SnapshotPreparedSpecs()) != 1 || !slices.Equal(fh.runner.InstalledSkills[0], []string{"pdf"}) {
		t.Errorf("recorded %v, %v", fh.runner.PreparedSpecs, fh.runner.InstalledSkills)
	}
	if !h.(AnyModel).AcceptsAnyModel() {
		t.Error("the fake runs any model")
	}
	models, err := h.Models(ctx)
	if err != nil || len(models) != 2 || !strings.HasPrefix(models[0].ID, "fake/") {
		t.Errorf("models = %+v, %v", models, err)
	}
}

func TestDeclaredOutputsEdges(t *testing.T) {
	if got := declaredOutputs("no contract here"); got != nil {
		t.Errorf("no contract = %+v", got)
	}
	head := "you MUST write these files (relative to repo root):\n"
	got := declaredOutputs(head + "-  stray\n- a.md\n- b.yaml — x (not_a_type): y\n- c.yaml — x (verdict\nthe end")
	want := []declaredOutput{{path: "a.md"}, {path: "b.yaml"}, {path: "c.yaml"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("outputs = %+v", got)
	}
}

func TestPromptThreadIDsSkipsWhatIsNotThreads(t *testing.T) {
	prompt := "# INPUT: no file named\nbody\n# INPUT: broken (file: a.yaml)\n: : :\n# OTHER: x\ny\n# INPUT: last\n"
	if got := promptThreadIDs(prompt); got != nil {
		t.Errorf("ids = %v", got)
	}
}

// --- credentials -------------------------------------------------------------

func TestCredentialsCurrent(t *testing.T) {
	ctx := context.Background()
	snap := []providers.Credential{apiKey("anthropic", "k")}
	if got, err := (Credentials{Snapshot: snap}).current(ctx); err != nil || len(got) != 1 {
		t.Errorf("no keeper = %v, %v", got, err)
	}
	fresh := []providers.Credential{apiKey("openai", "k2")}
	got, err := (Credentials{Snapshot: snap, Fresh: func(context.Context) ([]providers.Credential, error) { return fresh, nil }}).current(ctx)
	if err != nil || got[0].Provider != "openai" {
		t.Errorf("keeper wins = %v, %v", got, err)
	}
}

// A credential a harness cannot take is dropped, not guessed at; what stays
// is in a stable order.
func TestBindKeepsWhatTheHarnessTakes(t *testing.T) {
	got := bind(providers.Codex, []providers.Credential{
		{Provider: "no-such-provider", Kind: providers.KindAPIKey},
		apiKey("groq", "k"),          // codex takes no groq
		login("anthropic", "a", nil), // codex takes no Anthropic login
		login("openai-chatgpt", "a", nil),
		apiKey("openai", "k"),
	})
	if len(got) != 2 || got[0].Kind != providers.KindAPIKey || got[1].Kind != providers.KindOAuth {
		t.Errorf("bound = %+v", got)
	}
	both := bind(providers.Omp, []providers.Credential{apiKey("openai", "k"), apiKey("anthropic", "k")})
	if both[0].native.ID != "anthropic" || both[1].native.ID != "openai" {
		t.Errorf("order = %s, %s", both[0].native.ID, both[1].native.ID)
	}
	if got := domains(both); !slices.Contains(got, "api.anthropic.com") || !slices.Contains(got, "api.openai.com") {
		t.Errorf("domains = %v", got)
	}
}

func TestExpiresMillis(t *testing.T) {
	if expiresMillis(time.Time{}) != 0 {
		t.Error("no expiry is 0")
	}
	at := time.UnixMilli(1_700_000_000_123)
	if expiresMillis(at) != 1_700_000_000_123 {
		t.Errorf("millis = %d", expiresMillis(at))
	}
}
