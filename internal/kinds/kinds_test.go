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
	r.RegisterForge(GitLab{})
	r.RegisterForge(GitHub{})
	r.RegisterProvisioner(Devcontainer{})
	r.RegisterHarness(Opencode{})
	r.RegisterHarness(Codex{})
	for _, ok := range [][2]string{
		{"forge", "gitlab"}, {"forge", "github"}, {"provisioner", "devcontainer"},
		{"harness", "opencode"}, {"harness", "codex"},
	} {
		if !r.Valid(ok[0], ok[1]) {
			t.Errorf("%s/%s not valid", ok[0], ok[1])
		}
	}
	if r.Valid("provider", "openrouter") {
		t.Error("the provider port was removed (harness-owned auth); nothing may register it")
	}
	ds := r.Descriptors()
	if len(ds) != 5 {
		t.Fatalf("descriptors = %d, want 5", len(ds))
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
	layer := strings.Join(h.InstallLayer(), "\n")
	if !strings.Contains(layer, "npm install -g opencode-ai@"+OpencodeVersion) {
		t.Errorf("install layer = %q", layer)
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
	if h.Binary() != "codex" {
		t.Errorf("binary = %q", h.Binary())
	}
	if layer := strings.Join(h.InstallLayer(), "\n"); !strings.Contains(layer, "@openai/codex@"+CodexVersion) {
		t.Errorf("install layer = %q", layer)
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

func TestGitHubForgeKind(t *testing.T) {
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
	if _, err := gh.Forge(Config{}, ports.RepoRef{Locator: "no-slash"}); err == nil {
		t.Error("bad github locator accepted")
	}
	if _, err := gh.Forge(Config{}, ports.RepoRef{Locator: "lu/greeter"}); err != nil {
		t.Errorf("owner/repo locator refused: %v", err)
	}
}

func TestGitLabForgeKind(t *testing.T) {
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
	if _, err := gl.Forge(Config{}, ports.RepoRef{Locator: "not-a-number"}); err == nil {
		t.Error("non-numeric gitlab locator accepted")
	}
	if _, err := gl.Forge(Config{}, ports.RepoRef{Locator: "42"}); err != nil {
		t.Errorf("numeric locator refused: %v", err)
	}
}
