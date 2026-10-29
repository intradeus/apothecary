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
	h, err := Opencode{}.New(withCreds(
		apiKey("opencode-zen", "sk-zen"),
		login("openai-chatgpt", "chatgpt-access", map[string]string{"account_id": "acct-1"}),
		login("github-copilot", "gho_token", nil),
	), envp)
	if err != nil {
		t.Fatal(err)
	}
	if h.Binary() != "opencode" {
		t.Errorf("binary = %q", h.Binary())
	}
	// The egress allowlist follows the credentials' providers; models.dev and
	// npm are the harness's own always-allowed infrastructure.
	domains := h.EgressDomains()
	for _, want := range []string{"opencode.ai", "chatgpt.com", "api.githubcopilot.com", "models.dev", "registry.npmjs.org"} {
		if !slices.Contains(domains, want) {
			t.Errorf("egress %v missing %s", domains, want)
		}
	}

	// auth.json is rendered from the credentials; every secret is registered
	// before the file is written, and no login's refresh token is in it —
	// except Copilot's, whose GitHub token IS the bearer.
	var registered []string
	writtenBeforeRegister := false
	if err := h.SeedAuth(context.Background(), id, func(s string) {
		if _, err := envp.ReadFile(context.Background(), id, "/home/apothecary/.local/share/opencode/auth.json"); err == nil {
			writtenBeforeRegister = true
		}
		registered = append(registered, s)
	}); err != nil {
		t.Fatal(err)
	}
	if writtenBeforeRegister {
		t.Error("auth.json was written before its secrets were registered")
	}
	for _, want := range []string{"sk-zen", "chatgpt-access", "gho_token"} {
		if !slices.Contains(registered, want) {
			t.Errorf("registered %v missing %s", registered, want)
		}
	}
	data, err := envp.ReadFile(context.Background(), id, "/home/apothecary/.local/share/opencode/auth.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "refresh-chatgpt-access") {
		t.Fatalf("a refresh token reached the workspace: %s", data)
	}
	var auth map[string]map[string]any
	if err := json.Unmarshal(data, &auth); err != nil {
		t.Fatal(err)
	}
	if e := auth["opencode"]; e["type"] != "api" || e["key"] != "sk-zen" {
		t.Errorf("zen entry = %v", e)
	}
	if e := auth["openai"]; e["type"] != "oauth" || e["access"] != "chatgpt-access" || e["refresh"] != "" || e["accountId"] != "acct-1" || e["expires"].(float64) <= 0 {
		t.Errorf("chatgpt entry = %v", e)
	}
	if e := auth["github-copilot"]; e["refresh"] != "gho_token" || e["access"] != "gho_token" || e["expires"].(float64) != 0 {
		t.Errorf("copilot entry = %v", e)
	}

	// No credential is a named refusal.
	bare, _ := Opencode{}.New(Config{}, envp)
	if err := bare.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "no model provider credentials") {
		t.Errorf("bare Test = %v", err)
	}
}

func TestOpencodeModels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"openrouter": {"name": "OpenRouter", "models": {
				"acme/alpha-1": {"name": "Alpha One"},
				"beta/gamma-2": {"name": "Gamma Two"}
			}},
			"anthropic": {"name": "Anthropic", "models": {
				"alpha-1": {"name": "Alpha One"}
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
	h, _ := Opencode{}.New(withCreds(apiKey("openrouter", "k")), nil)
	models, err := h.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	want := []string{"openrouter/acme/alpha-1", "openrouter/beta/gamma-2"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v (anthropic must be absent: no credential for it)", ids, want)
	}

	// No auth configured → a named error, not an avalanche of 167 providers.
	bare, _ := Opencode{}.New(Config{}, nil)
	if _, err := bare.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "no model provider credentials") {
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
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"go-model-6"},{"id":"go-model-7"}]}`))
	}))
	defer live.Close()
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"opencode-go":{"name":"OpenCode Go","api":%q,"models":{
			"go-model-5":{"name":"Go Model 5 (retired)"},
			"go-model-6":{"name":"Go Model 6"}
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

	h, _ := Opencode{}.New(withCreds(apiKey("opencode-go", "k")), nil)
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
	want := []string{"opencode-go/go-model-6", "opencode-go/go-model-7"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v (retired go-model-5 must be gone; live list wins)", ids, want)
	}
	// Catalog still supplies the display name when it knows the model; the
	// live-only model falls back to its id.
	if byID["opencode-go/go-model-6"] != "OpenCode Go · Go Model 6" {
		t.Errorf("name = %q", byID["opencode-go/go-model-6"])
	}
	if byID["opencode-go/go-model-7"] != "OpenCode Go · go-model-7" {
		t.Errorf("name = %q", byID["opencode-go/go-model-7"])
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
			"go-model-6":{"name":"Go Model 6"}
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

	h, _ := Opencode{}.New(withCreds(apiKey("opencode-go", "k")), nil)
	models, err := h.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "opencode-go/go-model-6" {
		t.Fatalf("fallback models = %v, want [opencode-go/go-model-6]", models)
	}
}

func TestCodexKindCapabilities(t *testing.T) {
	h, err := Codex{}.New(withCreds(apiKey("openai", "sk-x")), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The engine drives codex through its ACP bridge, so that is what has
	// to be on PATH; the CLI is installed alongside for humans.
	if h.Binary() != "codex-acp" {
		t.Errorf("binary = %q", h.Binary())
	}
	if _, ok := h.(ports.AgentReattacher); !ok {
		t.Error("codex harness cannot reattach after an engine restart")
	}
	// The API key rides provider.env as OPENAI_API_KEY.
	if env := h.EnvVars(); env["OPENAI_API_KEY"] != "sk-x" {
		t.Errorf("env vars = %v", env)
	}
	if err := h.Test(context.Background()); err != nil {
		t.Errorf("test with a key = %v", err)
	}
	bare, _ := Codex{}.New(Config{}, nil)
	if err := bare.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "no model provider credentials") {
		t.Errorf("no-auth err = %v", err)
	}
	// opencode declares no env vars (auth.json only).
	oc, _ := Opencode{}.New(withCreds(apiKey("openrouter", "k")), nil)
	if len(oc.EnvVars()) != 0 {
		t.Errorf("opencode env vars = %v", oc.EnvVars())
	}
}

// A ChatGPT login becomes codex's auth.json: the tokens codex parses, an
// empty refresh token, last_refresh now — and nothing it can redeem.
func TestCodexRendersTheLogin(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	id, _ := envp.Provision(context.Background(), ports.EnvSpec{RunID: "r"})
	h, _ := Codex{}.New(withCreds(login("openai-chatgpt", "acc-tok", map[string]string{"id_token": "id-tok", "account_id": "acct-7"})), envp)
	var registered []string
	if err := h.SeedAuth(context.Background(), id, func(s string) { registered = append(registered, s) }); err != nil {
		t.Fatal(err)
	}
	data, err := envp.ReadFile(context.Background(), id, "/home/apothecary/.codex/auth.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Key    *string           `json:"OPENAI_API_KEY"`
		Tokens map[string]string `json:"tokens"`
		Last   string            `json:"last_refresh"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Key != nil || doc.Tokens["access_token"] != "acc-tok" || doc.Tokens["id_token"] != "id-tok" ||
		doc.Tokens["refresh_token"] != "" || doc.Tokens["account_id"] != "acct-7" || doc.Last == "" {
		t.Errorf("auth.json = %s", data)
	}
	if strings.Contains(string(data), "refresh-acc-tok") {
		t.Error("the refresh token reached the workspace")
	}
	if !slices.Contains(registered, "acc-tok") || !slices.Contains(registered, "id-tok") {
		t.Errorf("registered = %v", registered)
	}
	// A login without the id_token codex parses is refused, not seeded broken.
	noID, _ := Codex{}.New(withCreds(login("openai-chatgpt", "acc", nil)), envp)
	if err := noID.SeedAuth(context.Background(), id, func(string) {}); err == nil || !strings.Contains(err.Error(), "id_token") {
		t.Errorf("no id_token: %v", err)
	}
}

func TestOmpKindCapabilities(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	id, err := envp.Provision(context.Background(), ports.EnvSpec{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := Omp{}.New(withCreds(
		apiKey("anthropic", "sk-ant-x"), apiKey("opencode-go", "oc-x"),
		login("openai-chatgpt", "at-123456", nil),
	), envp)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Test(context.Background()); err != nil {
		t.Fatalf("test = %v", err)
	}
	if h.Binary() != "omp" {
		t.Errorf("binary = %q", h.Binary())
	}
	// API keys ride provider.env under the variables omp reads natively.
	if env := h.EnvVars(); env["ANTHROPIC_API_KEY"] != "sk-ant-x" || env["OPENCODE_API_KEY"] != "oc-x" || len(env) != 2 {
		t.Errorf("env vars = %v", env)
	}
	domains := h.EgressDomains()
	for _, want := range []string{"api.anthropic.com", "opencode.ai", "chatgpt.com", "catalog.stencil.so"} {
		if !slices.Contains(domains, want) {
			t.Errorf("egress %v missing %s", domains, want)
		}
	}
	// The login reaches omp over the engine's broker, never as a file.
	if ab, ok := h.(AuthBrokered); !ok || !ab.HasLogins() {
		t.Error("omp with a login does not ask for the auth broker")
	}
	if err := h.SeedAuth(context.Background(), id, func(string) { t.Error("omp seeds no secret") }); err != nil {
		t.Fatal(err)
	}
	if al, ok := h.(AuthLocator); !ok || !slices.Equal(al.AuthFiles(context.Background(), id), []string{"/home/apothecary/.omp/agent/agent.db"}) {
		t.Error("agent.db is not removed at run end")
	}
	keysOnly, _ := Omp{}.New(withCreds(apiKey("anthropic", "k")), envp)
	if keysOnly.(AuthBrokered).HasLogins() {
		t.Error("omp with keys only needs no broker")
	}
}

// The browser editor gets each harness's own extension — one that reads the
// auth the harness seeds — and omp, which has none, adds nothing.
func TestHarnessEditorExtensions(t *testing.T) {
	for _, c := range []struct {
		kind HarnessKind
		want []string
	}{
		{Codex{}, []string{"openai.chatgpt"}},
		{Opencode{}, []string{"sst-dev.opencode"}},
		{Omp{}, nil},
	} {
		h, err := c.kind.New(Config{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		if ee, ok := h.(EditorExtender); ok {
			got = ee.EditorExtensions()
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s editor extensions = %v, want %v", c.kind.Descriptor().Kind, got, c.want)
		}
	}
}

func TestOmpModels(t *testing.T) {
	var asked string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		_, _ = w.Write([]byte(`{
			"anthropic": {"alpha-1": {"name": "Alpha One", "baseUrl": "https://api.anthropic.com"}},
			"openai": {"gamma-2": {"name": "Gamma Two"}, "gamma-image": {"name": "Image"}},
			"openai-codex": {"luna-6": {"name": "Luna Six", "baseUrl": "https://chatgpt.com/backend-api"}},
			"together": {"kappa-2": {"name": "Kappa Two"}},
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
	h, _ := Omp{}.New(withCreds(apiKey("anthropic", "k"), apiKey("together", "k"), login("openai-chatgpt", "a", nil)), nil)
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
	want := []string{"anthropic/alpha-1", "openai-codex/luna-6", "together/kappa-2"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	// A login needs no probe: the keeper proved its refresh token.
	res := h.(AuthChecker).CheckAuth(context.Background(), []string{"openai-codex/luna-6"})
	if len(res.Rejected) != 0 || len(res.Unverified) != 0 {
		t.Errorf("check auth = %+v", res)
	}

	bare, _ := Omp{}.New(Config{}, nil)
	if _, err := bare.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "no model provider credentials") {
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
