package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports"
)

func TestRegistryValidAndDescriptors(t *testing.T) {
	r := NewRegistry()
	r.RegisterRepoHost(GitLab{})
	r.RegisterRepoHost(GitHub{})
	r.RegisterProvisioner(Docker{})
	r.RegisterHarness(Opencode{})
	r.RegisterHarness(Codex{})
	r.RegisterHarness(Omp{})
	for _, ok := range [][2]string{
		{"repository", "gitlab"}, {"repository", "github"}, {"provisioner", "docker"},
		{"harness", "opencode"}, {"harness", "codex"}, {"harness", "omp"},
	} {
		if !r.Valid(ok[0], ok[1]) {
			t.Errorf("%s/%s not valid", ok[0], ok[1])
		}
	}
	if r.Valid("provider", "openrouter") {
		t.Error("the provider port was removed (harness-owned auth); nothing may register it")
	}
	ds := r.Descriptors()
	if len(ds) != 6 {
		t.Fatalf("descriptors = %d, want 6", len(ds))
	}
	// Secret fields are flagged so the UI renders write-only inputs.
	for _, d := range ds {
		if d.Kind == "gitlab" {
			found := false
			for _, f := range d.Fields {
				if f.Name == "token" && f.Secret {
					found = true
				}
			}
			if !found {
				t.Error("gitlab token field not marked secret")
			}
		}
	}
}

func TestOpencodeHarnessCapabilities(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	id, err := envp.Provision(context.Background(), ports.EnvSpec{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := Opencode{}.New(Config{
		Secrets: map[string]string{"auth_json": `{"opencode":{"type":"oauth"}}`},
	}, envp)
	if err != nil {
		t.Fatal(err)
	}
	if h.Binary() != "opencode" {
		t.Errorf("binary = %q", h.Binary())
	}
	// Harness-owned auth: the derived egress allowlist (also the M18 proxy
	// allowlist) carries the provider's domain — auth.json credentials are
	// never firewalled out. The opencode provider maps to opencode.ai.
	domains := h.EgressDomains()
	// opencode.ai from the opencode provider; models.dev + npm are the
	// harness's own always-allowed infrastructure.
	for _, want := range []string{"opencode.ai", "models.dev", "registry.npmjs.org"} {
		if !slices.Contains(domains, want) {
			t.Errorf("egress %v missing %s", domains, want)
		}
	}

	// SeedAuth writes the stored auth_json verbatim — it IS the model auth
	// (harness-owned auth).
	if err := h.SeedAuth(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	data, err := envp.ReadFile(context.Background(), id, "/home/apothecary/.local/share/opencode/auth.json")
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]map[string]any
	if err := json.Unmarshal(data, &auth); err != nil {
		t.Fatal(err)
	}
	if _, ok := auth["opencode"]; !ok {
		t.Errorf("auth.json not written verbatim: %s", data)
	}

	// Bad auth_json fails Test statically and refuses to seed.
	bad, _ := Opencode{}.New(Config{Secrets: map[string]string{"auth_json": "{nope"}}, envp)
	if err := bad.Test(context.Background()); err == nil {
		t.Error("invalid auth_json passed Test")
	}
	if err := bad.SeedAuth(context.Background(), id); err == nil {
		t.Error("invalid auth_json seeded without error")
	}
}

func TestOpencodeModels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"openrouter": {"name": "OpenRouter", "models": {
				"anthropic/claude-sonnet-4-5": {"name": "Claude Sonnet 4.5"},
				"openai/gpt-5": {"name": "GPT-5"}
			}},
			"anthropic": {"name": "Anthropic", "models": {
				"claude-sonnet-4-5": {"name": "Claude Sonnet 4.5"}
			}}
		}`))
	}))
	defer ts.Close()
	oldURL := modelsCatalogURL
	modelsCatalogURL = ts.URL
	modelsCache.Lock()
	modelsCache.catalog = nil
	modelsCache.Unlock()
	defer func() {
		modelsCatalogURL = oldURL
		modelsCache.Lock()
		modelsCache.catalog = nil
		modelsCache.Unlock()
	}()

	// Only providers present in auth_json are listed; unknown auth entries
	// (subscription logins the catalog doesn't know) are skipped silently.
	h, _ := Opencode{}.New(Config{Secrets: map[string]string{
		"auth_json": `{"openrouter":{"type":"api","key":"k"},"bespoke-login":{"type":"oauth"}}`,
	}}, nil)
	models, err := h.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	want := []string{"openrouter/anthropic/claude-sonnet-4-5", "openrouter/openai/gpt-5"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v (anthropic must be absent: not in auth_json)", ids, want)
	}

	// No auth configured → a named error, not an avalanche of 167 providers.
	bare, _ := Opencode{}.New(Config{}, nil)
	if _, err := bare.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "auth_json") {
		t.Errorf("no-auth err = %v", err)
	}
}

// TestOpencodeModelsLive covers the exact failure that motivated this: the
// catalog (models.dev) keeps offering a retired model the provider no longer
// serves. When the provider exposes an OpenAI-compatible /models endpoint and
// we hold its api key, the LIVE list wins and the stale entry disappears.
func TestOpencodeModelsLive(t *testing.T) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen3.6-plus"},{"id":"qwen3.7-plus"}]}`))
	}))
	defer live.Close()
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"opencode-go":{"name":"OpenCode Go","api":%q,"models":{
			"qwen3.5-plus":{"name":"Qwen 3.5 Plus (retired)"},
			"qwen3.6-plus":{"name":"Qwen 3.6 Plus"}
		}}}`, live.URL)
	}))
	defer catalog.Close()

	oldURL := modelsCatalogURL
	modelsCatalogURL = catalog.URL
	modelsCache.Lock()
	modelsCache.catalog = nil
	modelsCache.Unlock()
	defer func() {
		modelsCatalogURL = oldURL
		modelsCache.Lock()
		modelsCache.catalog = nil
		modelsCache.Unlock()
	}()

	h, _ := Opencode{}.New(Config{Secrets: map[string]string{
		"auth_json": `{"opencode-go":{"type":"api","key":"k"}}`,
	}}, nil)
	models, err := h.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(models))
	byID := map[string]string{}
	for i, m := range models {
		ids[i] = m.ID
		byID[m.ID] = m.Name
	}
	want := []string{"opencode-go/qwen3.6-plus", "opencode-go/qwen3.7-plus"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v (retired qwen3.5-plus must be gone; live list wins)", ids, want)
	}
	// Catalog still supplies the display name when it knows the model; the
	// live-only model falls back to its id.
	if byID["opencode-go/qwen3.6-plus"] != "OpenCode Go · Qwen 3.6 Plus" {
		t.Errorf("name = %q", byID["opencode-go/qwen3.6-plus"])
	}
	if byID["opencode-go/qwen3.7-plus"] != "OpenCode Go · qwen3.7-plus" {
		t.Errorf("name = %q", byID["opencode-go/qwen3.7-plus"])
	}
}

// TestOpencodeModelsFallback: when the live endpoint is unreachable/errors,
// the catalog's static list is served — the picker never goes empty.
func TestOpencodeModelsFallback(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"opencode-go":{"name":"OpenCode Go","api":%q,"models":{
			"qwen3.6-plus":{"name":"Qwen 3.6 Plus"}
		}}}`, dead.URL)
	}))
	defer catalog.Close()

	oldURL := modelsCatalogURL
	modelsCatalogURL = catalog.URL
	modelsCache.Lock()
	modelsCache.catalog = nil
	modelsCache.Unlock()
	defer func() {
		modelsCatalogURL = oldURL
		modelsCache.Lock()
		modelsCache.catalog = nil
		modelsCache.Unlock()
	}()

	h, _ := Opencode{}.New(Config{Secrets: map[string]string{
		"auth_json": `{"opencode-go":{"type":"api","key":"k"}}`,
	}}, nil)
	models, err := h.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "opencode-go/qwen3.6-plus" {
		t.Fatalf("fallback models = %v, want [opencode-go/qwen3.6-plus]", models)
	}
}

func TestCodexKindCapabilities(t *testing.T) {
	h, err := Codex{}.New(Config{Secrets: map[string]string{"api_key": "sk-x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The engine drives codex through its ACP bridge, so that is what has
	// to be on PATH; the CLI is installed alongside for humans.
	if h.Binary() != "codex-acp" {
		t.Errorf("binary = %q", h.Binary())
	}
	// Both harnesses run as ACP agents in the workspace sidecar.
	if _, ok := h.(ports.AgentReattacher); !ok {
		t.Error("codex harness cannot reattach after an engine restart")
	}
	// Harness-owned auth: the API key rides provider.env as OPENAI_API_KEY.
	if env := h.EnvVars(); env["OPENAI_API_KEY"] != "sk-x" {
		t.Errorf("env vars = %v", env)
	}
	if err := h.Test(context.Background()); err != nil {
		t.Errorf("test with api_key = %v", err)
	}
	// No auth at all is a named refusal.
	bare, _ := Codex{}.New(Config{}, nil)
	if err := bare.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "api_key or auth_json") {
		t.Errorf("no-auth err = %v", err)
	}
	// opencode declares no env vars (auth.json only).
	oc, _ := Opencode{}.New(Config{Secrets: map[string]string{"auth_json": "{}"}}, nil)
	if len(oc.EnvVars()) != 0 {
		t.Errorf("opencode env vars = %v", oc.EnvVars())
	}
}

func TestOmpKindCapabilities(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	id, err := envp.Provision(context.Background(), ports.EnvSpec{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	var imported string
	envp.ExecFunc = func(e *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "auth-broker import") {
			imported = string(e.Files["/home/apothecary/.omp/agent/import/0.json"])
			return ports.ExecResult{Output: `{"imported":[{"provider":"openai-codex"}],"skipped":[]}`}, nil
		}
		return ports.ExecResult{}, nil
	}
	login := `{"type":"codex","access_token":"at-123456","refresh_token":"rt-123456","expired":"2026-11-01T00:00:00Z"}`
	h, err := Omp{}.New(Config{Secrets: map[string]string{
		"env":        "# keys\nexport ANTHROPIC_API_KEY=\"sk-ant-x\"\n\nOPENCODE_API_KEY=oc-x\n",
		"oauth_json": login,
	}}, envp)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Test(context.Background()); err != nil {
		t.Fatalf("test = %v", err)
	}
	if h.Binary() != "omp" {
		t.Errorf("binary = %q", h.Binary())
	}
	// API keys ride provider.env verbatim (quotes and export stripped).
	if env := h.EnvVars(); env["ANTHROPIC_API_KEY"] != "sk-ant-x" || env["OPENCODE_API_KEY"] != "oc-x" || len(env) != 2 {
		t.Errorf("env vars = %v", env)
	}
	// Egress follows the auth: key providers, the login's hosts, omp's catalog.
	domains := h.EgressDomains()
	for _, want := range []string{"api.anthropic.com", "opencode.ai", "chatgpt.com", "auth.openai.com", "catalog.stencil.so"} {
		if !slices.Contains(domains, want) {
			t.Errorf("egress %v missing %s", domains, want)
		}
	}
	if sv, ok := h.(SecretHolder); !ok || !slices.Contains(sv.SecretValues(), login) {
		t.Error("the logins are invisible to the redactor")
	}
	// The login is imported verbatim into agent.db; agent.db goes at run end.
	if err := h.SeedAuth(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if imported != login {
		t.Errorf("imported %q, want the login verbatim", imported)
	}
	if al, ok := h.(AuthLocator); !ok || !slices.Equal(al.AuthFiles(context.Background(), id), []string{"/home/apothecary/.omp/agent/agent.db"}) {
		t.Error("agent.db is not removed at run end")
	}

	for name, secrets := range map[string]map[string]string{
		"no auth":     {},
		"bad env":     {"env": "not a line"},
		"bad login":   {"oauth_json": `{"type":"claude"}`},
		"login type":  {"oauth_json": `[{"type":"copilot","access_token":"a","refresh_token":"r","expired":"x"}]`},
		"broken json": {"oauth_json": `[{`},
	} {
		bad, _ := Omp{}.New(Config{Secrets: secrets}, envp)
		if err := bad.Test(context.Background()); err == nil {
			t.Errorf("%s passed Test", name)
		}
	}
}

func TestOmpModels(t *testing.T) {
	var asked string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		_, _ = w.Write([]byte(`{
			"anthropic": {"claude-sonnet-4-5": {"name": "Claude Sonnet 4.5", "baseUrl": "https://api.anthropic.com"}},
			"openai": {"gpt-5": {"name": "GPT-5"}, "gpt-image": {"name": "Image"}},
			"openai-codex": {"gpt-5.6-luna": {"name": "GPT-5.6 Luna", "baseUrl": "https://chatgpt.com/backend-api"}},
			"together": {"kimi-k2": {"name": "Kimi K2"}},
			"groq": {"llama": {"name": "Llama"}}
		}`))
	}))
	defer ts.Close()
	oldURL := ompCatalogURL
	ompCatalogURL = ts.URL + "/v%s/models.json"
	reset := func() {
		ompCatalogs.Lock()
		ompCatalogs.byVersion = nil
		ompCatalogs.Unlock()
	}
	reset()
	defer func() { ompCatalogURL = oldURL; reset() }()

	// omp's own catalog and provider ids: a ChatGPT login lists
	// openai-codex's models, not the API's; groq holds no key.
	h, _ := Omp{}.New(Config{Secrets: map[string]string{
		"env":        "ANTHROPIC_API_KEY=k\nTOGETHER_API_KEY=k",
		"oauth_json": `{"type":"codex","access_token":"a","refresh_token":"r","expired":"2026-11-01T00:00:00Z"}`,
	}}, nil)
	models, err := h.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if asked != "/v"+OmpVersion+"/models.json" {
		t.Errorf("catalog fetched at %s, want the pinned version's", asked)
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	want := []string{"anthropic/claude-sonnet-4-5", "openai-codex/gpt-5.6-luna", "together/kimi-k2"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	// A login has no zero-token probe: unverified, never rejected.
	res := h.(AuthChecker).CheckAuth(context.Background(), []string{"openai-codex/gpt-5.6-luna"})
	if len(res.Rejected) != 0 || len(res.Unverified) != 1 || !strings.Contains(res.Unverified[0], "subscription login") {
		t.Errorf("check auth = %+v", res)
	}

	bare, _ := Omp{}.New(Config{}, nil)
	if _, err := bare.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "oauth_json") {
		t.Errorf("no-auth err = %v", err)
	}
}

func TestOmpAPIBase(t *testing.T) {
	got := ompAPIBase(map[string]ompModel{
		"a": {BaseURL: "https://opencode.ai/inference/go"},
		"b": {BaseURL: "https://opencode.ai/inference/go"},
		"c": {BaseURL: "https://opencode.ai/inference/go/v1"},
	})
	if got != "https://opencode.ai/inference/go/v1" {
		t.Errorf("api base = %s, want the OpenAI-compatible one", got)
	}
}

func TestGitHubRepoHostKind(t *testing.T) {
	gh := GitHub{}
	acc, err := gh.Account(Config{Secrets: map[string]string{"token": "ghp-x"}})
	if err != nil {
		t.Fatal(err)
	}
	user, pass := acc.GitCredential()
	if user != "x-access-token" || pass != "ghp-x" {
		t.Errorf("credential = %s:%s", user, pass)
	}
	if d := acc.EgressDomains(); len(d) != 3 || d[0] != "github.com" {
		t.Errorf("egress = %v", d)
	}
	if _, err := gh.RepoHost(Config{}, ports.RepoRef{Locator: "no-slash"}); err == nil {
		t.Error("bad github locator accepted")
	}
	if _, err := gh.RepoHost(Config{}, ports.RepoRef{Locator: "lu/greeter"}); err != nil {
		t.Errorf("owner/repo locator refused: %v", err)
	}
}

func TestGitLabRepoHostKind(t *testing.T) {
	gl := GitLab{}
	acc, err := gl.Account(Config{
		Values:  map[string]any{"base_url": "https://gitlab.example.com"},
		Secrets: map[string]string{"token": "glpat-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	user, pass := acc.GitCredential()
	if user != "oauth2" || pass != "glpat-x" {
		t.Errorf("credential = %s:%s", user, pass)
	}
	if d := acc.EgressDomains(); len(d) != 1 || d[0] != "gitlab.example.com" {
		t.Errorf("egress = %v", d)
	}
	if _, err := gl.RepoHost(Config{}, ports.RepoRef{Locator: "not-a-number"}); err == nil {
		t.Error("non-numeric gitlab locator accepted")
	}
	if _, err := gl.RepoHost(Config{}, ports.RepoRef{Locator: "42"}); err != nil {
		t.Errorf("numeric locator refused: %v", err)
	}
}

func TestGitHubIssuesKind(t *testing.T) {
	gi := GitHubIssues{}
	d := gi.Descriptor()
	if d.Port != "ticketsource" || d.Kind != "github" || d.Help == "" {
		t.Errorf("descriptor = %+v", d)
	}
	src, err := gi.New(Config{
		Values:  map[string]any{"repos": "acme/app, acme/docs,"},
		Secrets: map[string]string{"token": "ghp-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The write side rides on the same adapter: what a flow's tracker
	// actions resolve through the integration.
	if _, ok := src.(ports.TicketWriter); !ok {
		t.Error("the GitHub Issues source does not implement ports.TicketWriter")
	}
	reg := NewRegistry()
	reg.RegisterTicketSource(gi)
	reg.RegisterTicketSource(Jira{})
	if !reg.Valid("ticketsource", "github") || !reg.Valid("ticketsource", "jira") {
		t.Error("ticketsource kinds not valid in the registry")
	}
	if _, ok := must(Jira{}.New(Config{Values: map[string]any{"base_url": "https://acme.atlassian.net"}, Secrets: map[string]string{"api_token": "t"}})).(ports.TicketWriter); !ok {
		t.Error("the Jira source does not implement ports.TicketWriter")
	}
	if _, ok := must(FakeTicketSource{}.New(Config{})).(ports.TicketWriter); !ok {
		t.Error("the fake source does not implement ports.TicketWriter (dev needs a writable tracker)")
	}
}

func TestGitLabIssuesKind(t *testing.T) {
	gi := GitLabIssues{}
	d := gi.Descriptor()
	if d.Port != "ticketsource" || d.Kind != "gitlab" || d.Help == "" {
		t.Errorf("descriptor = %+v", d)
	}
	src, err := gi.New(Config{
		Values:  map[string]any{"projects": "acme/app, acme/platform/api,", "groups": "acme"},
		Secrets: map[string]string{"token": "glpat-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(ports.TicketWriter); !ok {
		t.Error("the GitLab Issues source does not implement ports.TicketWriter")
	}
	if _, ok := src.(ports.TicketAttacher); !ok {
		t.Error("the GitLab Issues source does not implement ports.TicketAttacher")
	}
	// The ticketsource kind shares its name with the repository kind.
	reg := NewRegistry()
	reg.RegisterTicketSource(gi)
	reg.RegisterRepoHost(GitLab{})
	if !reg.Valid("ticketsource", "gitlab") || !reg.Valid("repository", "gitlab") {
		t.Error("gitlab kinds not valid in the registry")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
