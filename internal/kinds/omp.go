package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"apothecary/internal/adapters/acpagent"
	ompadapter "apothecary/internal/adapters/omp"
	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
)

// OmpVersion is the pinned omp release; the harness layer installs exactly
// this standalone binary (the npm package needs bun at runtime).
const OmpVersion = "18.13.1"

// Omp is the harness/omp kind: oh-my-pi (github.com/can1357/oh-my-pi), driven
// as an ACP agent. Harness-owned auth in two shapes: provider API keys as
// env-var lines (ride provider.env — omp reads them natively) and/or
// subscription logins as CLIProxyAPI credential JSON, imported into omp's
// agent.db at bootstrap.
type Omp struct{}

func (Omp) Descriptor() Descriptor {
	return Descriptor{
		Port: "harness", Kind: "omp", Label: "omp (oh-my-pi)",
		Help: "oh-my-pi coding agent, installed as its standalone binary. Give it provider API keys, " +
			"subscription logins, or both — omp holds no other credential.",
		Fields: []Field{
			{Name: "version", Label: "Version", Default: OmpVersion},
			{Name: "env", Label: "Provider API keys — one KEY=VALUE per line", Secret: true, Multiline: true,
				Placeholder: "ANTHROPIC_API_KEY=sk-ant-…\nOPENROUTER_API_KEY=sk-or-…",
				Help:        "Read natively by omp: " + strings.Join(ompEnvKeyNames(), ", ") + "."},
			{Name: "oauth_json", Label: "Subscription logins — CLIProxyAPI credential JSON, one object or an array (optional)",
				Secret:      true,
				Placeholder: `[{"type":"claude","access_token":"…","refresh_token":"…","expired":"2026-11-01T00:00:00Z","email":"…"}]`,
				Help: "type is claude (Claude Pro/Max), codex (ChatGPT), gemini or antigravity; " +
					"imported with `omp auth-broker import`. omp refreshes the tokens itself."},
		},
	}
}

func (Omp) New(cfg Config, envp ports.EnvProvisioner) (Harness, error) {
	adapter := ompadapter.New(envp, ompadapter.Config{})
	return &ompHarness{
		Runner:    acpagent.New(envp, adapter.Profile()),
		adapter:   adapter,
		version:   cfg.Str("version", OmpVersion),
		env:       cfg.Secrets["env"],
		oauthJSON: cfg.Secrets["oauth_json"],
	}, nil
}

// ompHarness runs as a long-lived ACP agent (`omp acp`) in the workspace
// sidecar; the embedded runner is the AgentRunner, the adapter its config.
type ompHarness struct {
	*acpagent.Runner
	adapter   *ompadapter.Adapter
	version   string
	env       string
	oauthJSON string
}

// ompProvider is one omp provider: its id (the model prefix flows use, the
// key of omp's catalog) and its models.dev id — the key of the tables shared
// with opencode (providerDomains, providerAuthProbe); mostly the same,
// except where omp spells a provider its own way.
type ompProvider struct {
	id, devID string
}

// ompEnvProviders maps the env vars omp reads natively (its catalog's
// providers/<id>.kdl `env`) to the providers they unlock. One key can unlock
// two: OPENCODE_API_KEY serves both OpenCode gateways.
var ompEnvProviders = map[string][]ompProvider{
	"ANTHROPIC_API_KEY":  {{"anthropic", "anthropic"}},
	"OPENAI_API_KEY":     {{"openai", "openai"}},
	"OPENROUTER_API_KEY": {{"openrouter", "openrouter"}},
	"GEMINI_API_KEY":     {{"google", "google"}},
	"XAI_API_KEY":        {{"xai", "xai"}},
	"GROQ_API_KEY":       {{"groq", "groq"}},
	"MISTRAL_API_KEY":    {{"mistral", "mistral"}},
	"DEEPSEEK_API_KEY":   {{"deepseek", "deepseek"}},
	"CEREBRAS_API_KEY":   {{"cerebras", "cerebras"}},
	"TOGETHER_API_KEY":   {{"together", "togetherai"}},
	"FIREWORKS_API_KEY":  {{"fireworks", "fireworks-ai"}},
	"OPENCODE_API_KEY":   {{"opencode-go", "opencode-go"}, {"opencode-zen", "opencode"}},
}

// ompLogin is what a CLIProxyAPI credential `type` imports as (omp's
// CLIPROXY_TYPE_TO_PROVIDER) and the hosts its traffic and token refresh
// reach — a subscription has no API-key provider entry to derive them from.
type ompLogin struct {
	provider ompProvider
	domains  []string
}

var ompLogins = map[string]ompLogin{
	"claude": {ompProvider{"anthropic", "anthropic"},
		[]string{"api.anthropic.com", "console.anthropic.com", "platform.claude.com"}},
	"codex": {ompProvider{"openai-codex", "openai"},
		[]string{"chatgpt.com", "auth.openai.com", "api.openai.com"}},
	"gemini": {ompProvider{"google-gemini-cli", "google"},
		[]string{"cloudcode-pa.googleapis.com", "oauth2.googleapis.com"}},
	"gemini-cli": {ompProvider{"google-gemini-cli", "google"},
		[]string{"cloudcode-pa.googleapis.com", "oauth2.googleapis.com"}},
	"antigravity": {ompProvider{"google-antigravity", "google"},
		[]string{"daily-cloudcode-pa.googleapis.com", "cloudcode-pa.googleapis.com", "oauth2.googleapis.com"}},
}

// ompInfra are omp's own runtime endpoints, needed whatever the provider: its
// model catalog refresh. Always allowed.
var ompInfra = []string{"catalog.stencil.so"}

func ompEnvKeyNames() []string {
	names := make([]string, 0, len(ompEnvProviders))
	for k := range ompEnvProviders {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseEnvLines reads dotenv-style KEY=VALUE lines: blank lines and #
// comments skipped, an `export ` prefix and surrounding quotes tolerated.
func parseEnvLines(raw string) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !envName.MatchString(k) {
			return nil, fmt.Errorf("line %d: want KEY=VALUE", i+1)
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, nil
}

// oauthLogin is the slice of a CLIProxyAPI credential omp's importer needs
// (it skips a document missing any of these).
type oauthLogin struct {
	Type         string `json:"type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Expired      string `json:"expired"`
}

// parseLogins reads oauth_json — one credential object or an array — and
// returns each document verbatim (what gets imported) with its decoded form.
func parseLogins(raw string) ([]json.RawMessage, []oauthLogin, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, nil
	}
	var docs []json.RawMessage
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &docs); err != nil {
			return nil, nil, fmt.Errorf("oauth_json: %w", err)
		}
	} else {
		docs = []json.RawMessage{json.RawMessage(raw)}
	}
	logins := make([]oauthLogin, len(docs))
	for i, d := range docs {
		if err := json.Unmarshal(d, &logins[i]); err != nil {
			return nil, nil, fmt.Errorf("oauth_json[%d]: %w", i, err)
		}
		l := logins[i]
		if _, ok := ompLogins[strings.ToLower(l.Type)]; !ok {
			return nil, nil, fmt.Errorf("oauth_json[%d]: type %q is not one omp imports (claude, codex, gemini, antigravity)", i, l.Type)
		}
		if l.AccessToken == "" || l.RefreshToken == "" || l.Expired == "" {
			return nil, nil, fmt.Errorf("oauth_json[%d]: needs access_token, refresh_token and expired", i)
		}
	}
	return docs, logins, nil
}

// heldProvider is a provider the configured auth unlocks; key is its API
// key, empty for a subscription login.
type heldProvider struct {
	ompProvider
	key     string
	domains []string
}

// held lists the providers env and oauth_json unlock, by omp id. A provider
// with both keeps the key (the probeable credential); omp itself prefers the
// login.
func (h *ompHarness) held() []heldProvider {
	byID := map[string]*heldProvider{}
	var order []string
	add := func(p ompProvider, key string, domains []string) {
		if hp, ok := byID[p.id]; ok {
			if hp.key == "" {
				hp.key = key
			}
			hp.domains = append(hp.domains, domains...)
			return
		}
		byID[p.id] = &heldProvider{ompProvider: p, key: key, domains: domains}
		order = append(order, p.id)
	}
	env, _ := parseEnvLines(h.env)
	for name, v := range env {
		for _, p := range ompEnvProviders[name] {
			if v != "" {
				add(p, v, providerDomains[p.devID])
			}
		}
	}
	_, logins, _ := parseLogins(h.oauthJSON)
	for _, l := range logins {
		lg := ompLogins[strings.ToLower(l.Type)]
		add(lg.provider, "", lg.domains)
	}
	sort.Strings(order)
	out := make([]heldProvider, len(order))
	for i, id := range order {
		out[i] = *byID[id]
	}
	return out
}

// Test statically validates the auth material (no remote to probe).
func (h *ompHarness) Test(_ context.Context) error {
	if strings.TrimSpace(h.env) == "" && strings.TrimSpace(h.oauthJSON) == "" {
		return fmt.Errorf("omp: configure env (provider API keys) or oauth_json — the harness owns model auth")
	}
	if _, err := parseEnvLines(h.env); err != nil {
		return fmt.Errorf("omp: env: %w", err)
	}
	if _, _, err := parseLogins(h.oauthJSON); err != nil {
		return fmt.Errorf("omp: %w", err)
	}
	return nil
}

func (h *ompHarness) Binary() string { return "omp" }

// InstallLayer downloads the release's standalone binary for this platform
// (glibc or musl, x64 or arm64) and checks it against the release's
// SHA256SUMS. curl is the line's first token, so it is the only prerequisite
// the harness feature provides.
func (h *ompHarness) InstallLayer() []string {
	base := "https://github.com/can1357/oh-my-pi/releases/download/v" + h.version
	return []string{"RUN curl -fsSL -o /tmp/omp-SHA256SUMS " + base + "/SHA256SUMS.txt" +
		` && a=omp-linux-$([ -f /etc/alpine-release ] && echo musl-)$(uname -m | sed -e s/x86_64/x64/ -e s/aarch64/arm64/)` +
		` && curl -fsSL -o /tmp/$a ` + base + `/$a` +
		` && (cd /tmp && grep " $a\$" omp-SHA256SUMS | sha256sum -c -)` +
		` && install -m 0755 /tmp/$a /usr/local/bin/omp && rm -f /tmp/$a /tmp/omp-SHA256SUMS` +
		` && { [ ! -f /etc/alpine-release ] || apk add --no-cache libstdc++ libgcc; }`}
}

// EgressDomains derives the allowlist from the same auth the models come
// from — whatever omp can route to must not be blocked by the firewall that
// ships with it — plus omp's own infrastructure.
func (h *ompHarness) EgressDomains() []string {
	seen := map[string]bool{}
	var out []string
	add := func(ds ...string) {
		for _, d := range ds {
			if d != "" && !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	add(ompInfra...)
	held := h.held()
	if len(held) == 0 {
		add(egressBaseline...)
	}
	for _, p := range held {
		add(p.domains...)
	}
	sort.Strings(out)
	return out
}

// Models lists `provider/model` for exactly the providers the auth unlocks,
// from omp's own bundled catalog at the pinned version — the list its ACP
// `model` option offers, in its provider ids. A subscription serves its own
// models (a ChatGPT login: openai-codex's handful, not the API's catalog),
// which only omp's catalog knows.
func (h *ompHarness) Models(ctx context.Context) ([]ModelInfo, error) {
	held := h.held()
	if len(held) == 0 {
		return nil, fmt.Errorf("omp: no providers configured — set env or oauth_json on the integration first")
	}
	catalog, err := fetchOmpCatalog(ctx, h.version)
	if err != nil {
		return nil, err
	}
	var out []ModelInfo
	for _, hp := range held {
		label := hp.id
		if hp.key == "" {
			label += " (subscription)"
		}
		for modelID, m := range catalog[hp.id] {
			name := m.Name
			if name == "" {
				name = modelID
			}
			out = append(out, ModelInfo{ID: hp.id + "/" + modelID, Name: label + " · " + name})
		}
	}
	sortModels(out)
	return out, nil
}

// ompCatalogURL is omp's bundled model catalog at a release tag (%s: the
// version) — exactly what the pinned binary ships. Package var so tests can
// stub it.
var ompCatalogURL = "https://raw.githubusercontent.com/can1357/oh-my-pi/v%s/packages/catalog/src/models.json"

// ompModel is the slice of a catalog entry we need.
type ompModel struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
}

// ompCatalogs caches the catalog per version: a release's never changes, and
// the document is large (~12 MB), so it is fetched once per process.
var ompCatalogs struct {
	sync.Mutex
	byVersion map[string]map[string]map[string]ompModel
}

func fetchOmpCatalog(ctx context.Context, version string) (map[string]map[string]ompModel, error) {
	ompCatalogs.Lock()
	defer ompCatalogs.Unlock()
	if c, ok := ompCatalogs.byVersion[version]; ok {
		return c, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(ompCatalogURL, version), nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("omp model catalog unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("omp model catalog (v%s): %d", version, resp.StatusCode)
	}
	var c map[string]map[string]ompModel
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, fmt.Errorf("omp model catalog: %w", err)
	}
	if ompCatalogs.byVersion == nil {
		ompCatalogs.byVersion = map[string]map[string]map[string]ompModel{}
	}
	ompCatalogs.byVersion[version] = c
	return c, nil
}

// CheckAuth (kinds.AuthChecker) probes the API key of every provider the
// flow's models route through. A subscription login has no zero-token probe.
func (h *ompHarness) CheckAuth(ctx context.Context, models []string) AuthResult {
	want := map[string]string{}
	for _, m := range models {
		if id, model, ok := strings.Cut(m, "/"); ok {
			want[id] = model
		}
	}
	return h.checkAuth(ctx, want, false)
}

// CheckCredentials (kinds.AuthChecker) probes every configured credential.
func (h *ompHarness) CheckCredentials(ctx context.Context) AuthResult {
	want := map[string]string{}
	for _, hp := range h.held() {
		want[hp.id] = ""
	}
	return h.checkAuth(ctx, want, true)
}

// checkAuth probes the held providers in want (omp id → the model routed
// through it, "" to borrow one from the catalog); credentialOnly reports a
// "model refused" verdict as unverified — the model was borrowed.
func (h *ompHarness) checkAuth(ctx context.Context, want map[string]string, credentialOnly bool) AuthResult {
	var out AuthResult
	var catalog map[string]map[string]ompModel
	for _, hp := range h.held() {
		model, ok := want[hp.id]
		if !ok {
			continue
		}
		label := "omp provider " + hp.id
		if hp.key == "" {
			out.Unverified = append(out.Unverified, label+": subscription login — no zero-token probe")
			continue
		}
		if catalog == nil {
			c, err := fetchOmpCatalog(ctx, h.version)
			if err != nil {
				out.Unverified = append(out.Unverified, "omp: "+err.Error())
				return out
			}
			catalog = c
		}
		models := catalog[hp.id]
		if model == "" {
			ids := make([]string, 0, len(models))
			for id := range models {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			if len(ids) > 0 {
				model = ids[0]
			}
		}
		pr, probeable := providerAuthProbe(hp.devID, catalogProvider{Name: hp.id, API: ompAPIBase(models)}, hp.key, model)
		if !probeable {
			out.Unverified = append(out.Unverified, label+": no zero-token probe for this provider")
			continue
		}
		rejected, reason, err := pr.run(ctx, hp.key)
		switch {
		case err != nil:
			out.Unverified = append(out.Unverified, fmt.Sprintf("%s: %v", label, err))
		case rejected && credentialOnly && strings.HasPrefix(reason, "model"):
			out.Unverified = append(out.Unverified, fmt.Sprintf("%s: probe model %s %s", label, model, reason))
		case rejected:
			fix := "fix env on the omp integration"
			if strings.HasPrefix(reason, "model") {
				fix = "pick another model in the flow"
			}
			out.Rejected = append(out.Rejected, fmt.Sprintf("%s: %s — %s", label, reason, fix))
		}
	}
	return out
}

// ompAPIBase is a provider's OpenAI-compatible base URL from its catalog
// models — the most common one ending in /v1 (a provider can serve a few
// APIs), else the most common at all.
func ompAPIBase(models map[string]ompModel) string {
	count := map[string]int{}
	for _, m := range models {
		count[m.BaseURL]++
	}
	best := ""
	better := func(u string) bool {
		bv, uv := strings.HasSuffix(best, "/v1"), strings.HasSuffix(u, "/v1")
		if uv != bv {
			return uv
		}
		return count[u] > count[best] || (count[u] == count[best] && u < best)
	}
	for u := range count {
		if u != "" && (best == "" || better(u)) {
			best = u
		}
	}
	return best
}

// EnvVars: the API keys ride provider.env, where omp reads them natively.
func (h *ompHarness) EnvVars() map[string]string {
	env, err := parseEnvLines(h.env)
	if err != nil || len(env) == 0 {
		return nil
	}
	return env
}

// AuthFiles (kinds.AuthLocator): agent.db, which holds the imported logins
// (and omp's refreshed tokens), removed when the run ends.
func (h *ompHarness) AuthFiles(ctx context.Context, env ports.EnvID) []string {
	return []string{h.adapter.AuthFilePath(ctx, env)}
}

// SecretValues (kinds.SecretHolder): the logins never pass through
// provider.env. The API keys need no mention — they ride EnvVars.
func (h *ompHarness) SecretValues() []string { return []string{h.oauthJSON} }

// SeedAuth imports the subscription logins into omp's agent.db.
func (h *ompHarness) SeedAuth(ctx context.Context, env ports.EnvID) error {
	docs, _, err := parseLogins(h.oauthJSON)
	if err != nil {
		return fmt.Errorf("omp: %w", err)
	}
	creds := make([][]byte, len(docs))
	for i, d := range docs {
		creds[i] = d
	}
	return h.adapter.SeedOAuth(ctx, env, creds)
}

func (h *ompHarness) Prepare(ctx context.Context, env ports.EnvID, specs []*agentspec.Spec) error {
	return h.adapter.PrepareEnv(ctx, env, specs)
}

var _ HarnessKind = Omp{}
