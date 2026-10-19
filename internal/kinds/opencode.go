package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"apothecary/internal/adapters/acpagent"
	opencodeadapter "apothecary/internal/adapters/opencode"
	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
)

// OpencodeVersion is the pinned harness version (kept in lockstep with the
// M2 image pin; the harness layer installs exactly this).
const OpencodeVersion = "1.17.8"

// Opencode is the harness/opencode kind (DESIGN2 §4.1).
type Opencode struct{}

func (Opencode) Descriptor() Descriptor {
	return Descriptor{
		Port: "harness", Kind: "opencode", Label: "opencode",
		Fields: []Field{
			{Name: "version", Label: "Version", Default: OpencodeVersion},
			// Harness-owned auth (user decision, post-M8): this one field
			// carries ALL model credentials — API keys and/or subscription
			// logins — in opencode's native format. The separate provider
			// port remains an API-level option, off the UI paved road.
			{Name: "auth_json", Label: "auth.json — model auth (API keys and/or subscription logins), e.g. contents of ~/.local/share/opencode/auth.json",
				Secret: true, Required: true,
				Placeholder: `{"openrouter":{"type":"api","key":"sk-or-…"}}`},
		},
	}
}

func (Opencode) New(cfg Config, envp ports.EnvProvisioner) (Harness, error) {
	return &opencodeHarness{
		Runner:   acpagent.New(envp, opencodeadapter.Profile()),
		adapter:  opencodeadapter.New(envp, opencodeadapter.Config{}),
		version:  cfg.Str("version", OpencodeVersion),
		authJSON: cfg.Secrets["auth_json"],
	}, nil
}

// opencodeHarness runs as a long-lived ACP agent (`opencode acp`) in the
// workspace sidecar; the embedded runner is the AgentRunner (plus reattach
// and stop), the adapter generates its config.
type opencodeHarness struct {
	*acpagent.Runner
	adapter  *opencodeadapter.Adapter
	version  string
	authJSON string
}

// Test statically validates the auth material (§4: no remote to probe).
func (h *opencodeHarness) Test(_ context.Context) error {
	if h.authJSON == "" {
		// The field is Required — failing here beats failing at the first
		// model listing or run.
		return fmt.Errorf("opencode: auth_json is required (harness-owned model auth)")
	}
	if !json.Valid([]byte(h.authJSON)) {
		return fmt.Errorf("opencode: auth_json is not valid JSON")
	}
	return nil
}

func (h *opencodeHarness) Binary() string { return "opencode" }

func (h *opencodeHarness) InstallLayer() []string {
	return []string{fmt.Sprintf("RUN npm install -g opencode-ai@%s", h.version)}
}

// providerDomains maps opencode/models.dev provider ids (auth_json top-level
// keys — the same ids Models() filters by) to their API hosts. Providers
// absent here (regional clouds like bedrock/vertex) need
// workspace.extra_domains.
var providerDomains = map[string][]string{
	"anthropic":    {"api.anthropic.com"},
	"openai":       {"api.openai.com"},
	"openrouter":   {"openrouter.ai"},
	"google":       {"generativelanguage.googleapis.com"},
	"xai":          {"api.x.ai"},
	"mistral":      {"api.mistral.ai"},
	"groq":         {"api.groq.com"},
	"deepseek":     {"api.deepseek.com"},
	"togetherai":   {"api.together.xyz"},
	"fireworks-ai": {"api.fireworks.ai"},
	"cerebras":     {"api.cerebras.ai"},
	"opencode":     {"opencode.ai"}, // OpenCode Zen gateway
	"opencode-go":  {"opencode.ai"},
}

// opencodeInfra are the opencode CLI's own runtime endpoints, needed
// regardless of model provider: models.dev (its model catalog) and the npm
// registry (it is an npm package and version/plugin-checks at runtime —
// found live as egress denials in a Go project). Always allowed.
var opencodeInfra = []string{"models.dev", "registry.npmjs.org"}

// egressBaseline is the provider fallback when auth_json is absent or
// unparseable at resolution time.
var egressBaseline = []string{"api.anthropic.com", "api.openai.com", "openrouter.ai"}

func (h *opencodeHarness) EgressDomains() []string {
	// Harness-owned auth: whatever auth.json can route to must not be
	// blocked by the very firewall that ships with it (the M3 openrouter
	// lesson, generalized) — so the provider allowlist is DERIVED from the
	// same auth_json keys Models() advertises. The harness's own infra is
	// always allowed on top.
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
	add(opencodeInfra...)

	auth := map[string]json.RawMessage{}
	if h.authJSON == "" || json.Unmarshal([]byte(h.authJSON), &auth) != nil || len(auth) == 0 {
		add(egressBaseline...)
	} else {
		for providerID := range auth {
			add(providerDomains[providerID]...)
		}
	}
	// M18: a missing provider domain is no longer a silent block — the
	// egress proxy records it as a visible denial the user fixes via the
	// project's extra_domains. So the static map plus extra_domains is
	// enough; no catalog fetch on the resolution path.
	sort.Strings(out)
	return out
}

// modelsCatalogURL is opencode's own model catalog source (models.dev —
// the same data the CLI bundles). Package var so tests can stub it.
var modelsCatalogURL = "https://models.dev/api.json"

// modelsCache is a package-level TTL cache of the catalog: the fetch is
// engine-side (trusted host, no firewall in play) and the catalog moves
// slowly.
var modelsCache struct {
	sync.Mutex
	fetchedAt time.Time
	catalog   map[string]catalogProvider
}

type catalogProvider struct {
	Name string `json:"name"`
	// API is the provider's OpenAI-compatible base URL (models.dev supplies
	// it, e.g. opencode-go → https://opencode.ai/zen/go/v1). Present ⇒ we can
	// ask the provider what it ACTUALLY serves right now.
	API    string                  `json:"api"`
	Models map[string]catalogModel `json:"models"`
}

// authEntry is the slice of one opencode auth.json value we need: an
// api-key entry carries a bearer we can use to query the provider's live
// model list; subscription/oauth entries carry no static key and fall back
// to the catalog.
type authEntry struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

type catalogModel struct {
	Name string `json:"name"`
}

func fetchCatalog(ctx context.Context) (map[string]catalogProvider, error) {
	modelsCache.Lock()
	defer modelsCache.Unlock()
	if modelsCache.catalog != nil && time.Since(modelsCache.fetchedAt) < time.Hour {
		return modelsCache.catalog, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsCatalogURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("model catalog unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog: %d", resp.StatusCode)
	}
	var catalog map[string]catalogProvider
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("model catalog: %w", err)
	}
	modelsCache.catalog = catalog
	modelsCache.fetchedAt = time.Now()
	return catalog, nil
}

// Models lists `provider/model` strings for exactly the providers the
// harness is authenticated against — the top-level keys of auth_json are
// opencode's provider ids, and the catalog is opencode's own (models.dev).
func (h *opencodeHarness) Models(ctx context.Context) ([]ModelInfo, error) {
	auth := map[string]json.RawMessage{}
	if h.authJSON != "" {
		if err := json.Unmarshal([]byte(h.authJSON), &auth); err != nil {
			return nil, fmt.Errorf("opencode: auth_json: %w", err)
		}
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("opencode: no providers configured — set auth_json on the integration first")
	}
	catalog, err := fetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	var out []ModelInfo
	for providerID, rawEntry := range auth {
		p, ok := catalog[providerID]
		if !ok {
			continue // provider unknown to the catalog (e.g. bespoke auth entries)
		}
		var entry authEntry
		_ = json.Unmarshal(rawEntry, &entry) // best-effort; oauth entries just yield an empty key
		for _, modelID := range providerModelIDs(ctx, p, entry) {
			name := modelID // catalog supplies a prettier name when it knows the model
			if m, ok := p.Models[modelID]; ok && m.Name != "" {
				name = m.Name
			}
			out = append(out, ModelInfo{
				ID:   providerID + "/" + modelID,
				Name: p.Name + " · " + name,
			})
		}
	}
	sortModels(out)
	return out, nil
}

// providerModelIDs returns a provider's model ids: the AUTHORITATIVE live
// list from the provider's own OpenAI-compatible /models endpoint when we
// can reach it, else the models.dev catalog's static list. models.dev lags
// real provider inventories — it kept offering a retired opencode-go model
// that the gateway then rejected at run time — so the live list wins and
// the catalog is fallback + name source.
func providerModelIDs(ctx context.Context, p catalogProvider, entry authEntry) []string {
	if live, err := liveModels(ctx, p, entry); err == nil && len(live) > 0 {
		return live
	}
	ids := make([]string, 0, len(p.Models))
	for id := range p.Models {
		ids = append(ids, id)
	}
	return ids
}

// liveModels queries {p.API}/models (OpenAI-compatible) with the api key
// from auth.json and returns the model ids the provider actually serves.
// Only attempts providers that expose a base URL and for which we hold a
// static api key; the key is used solely as the request bearer and is
// never logged. Any failure returns an error so the caller falls back to
// the catalog.
func liveModels(ctx context.Context, p catalogProvider, entry authEntry) ([]string, error) {
	if p.API == "" || entry.Key == "" {
		return nil, fmt.Errorf("no live model endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.API, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+entry.Key)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider %s models: %d", p.Name, resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// CheckAuth (kinds.AuthChecker) probes every provider the flow's models
// route through with its auth.json credential. The OpenCode gateways are
// probed per model: their probe also says whether the model is still served,
// which their catalog entry (they have no live list) cannot.
func (h *opencodeHarness) CheckAuth(ctx context.Context, models []string) AuthResult {
	return h.checkAuth(ctx, func(map[string]json.RawMessage, map[string]catalogProvider) []string { return models }, false)
}

// CheckCredentials (kinds.AuthChecker) probes every provider auth.json
// carries. A gateway's probe needs a model, so it borrows one from the
// catalog; that model being retired leaves the key unverified.
func (h *opencodeHarness) CheckCredentials(ctx context.Context) AuthResult {
	return h.checkAuth(ctx, func(auth map[string]json.RawMessage, catalog map[string]catalogProvider) []string {
		models := make([]string, 0, len(auth))
		for providerID := range auth {
			ids := make([]string, 0, len(catalog[providerID].Models))
			for id := range catalog[providerID].Models {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			model := ""
			if len(ids) > 0 {
				model = ids[0]
			}
			models = append(models, providerID+"/"+model)
		}
		sort.Strings(models)
		return models
	}, true)
}

// checkAuth probes the models pick returns; credentialOnly reports a "model
// refused" verdict as unverified — the model was borrowed, not chosen.
func (h *opencodeHarness) checkAuth(ctx context.Context, pick func(map[string]json.RawMessage, map[string]catalogProvider) []string, credentialOnly bool) AuthResult {
	var out AuthResult
	auth := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(h.authJSON), &auth); err != nil {
		out.Unverified = append(out.Unverified, "opencode: auth_json: "+err.Error())
		return out
	}
	catalog, err := fetchCatalog(ctx)
	if err != nil {
		out.Unverified = append(out.Unverified, "opencode: "+err.Error())
		return out
	}
	models := pick(auth, catalog)
	done := map[string]bool{}
	for _, m := range models {
		providerID, modelID, ok := strings.Cut(m, "/")
		raw, known := auth[providerID]
		if !ok || !known {
			continue // the model check refuses a provider auth.json lacks
		}
		var entry authEntry
		_ = json.Unmarshal(raw, &entry)
		pr, probeable := providerAuthProbe(providerID, catalog[providerID], entry.Key, modelID)
		label, once := "opencode provider "+providerID, providerID
		if pr.kind == probeEmptyChat && !credentialOnly {
			label, once = "opencode model "+m, m
		}
		if done[once] {
			continue
		}
		done[once] = true
		switch {
		case entry.Type != "api" || entry.Key == "":
			out.Unverified = append(out.Unverified, label+": not an API key (subscription login) — no zero-token probe")
		case !probeable:
			out.Unverified = append(out.Unverified, label+": no zero-token probe for this provider")
		default:
			rejected, reason, err := pr.run(ctx, entry.Key)
			switch {
			case err != nil:
				out.Unverified = append(out.Unverified, fmt.Sprintf("%s: %v", label, err))
			case rejected && credentialOnly && strings.HasPrefix(reason, "model"):
				// The gateway checks the model before the key: this says
				// nothing about the credential.
				out.Unverified = append(out.Unverified, fmt.Sprintf("%s: probe model %s %s", label, modelID, reason))
			case rejected:
				fix := "fix auth_json on the opencode integration"
				if strings.HasPrefix(reason, "model") {
					fix = "pick another model in the flow"
				}
				out.Rejected = append(out.Rejected, fmt.Sprintf("%s: %s — %s", label, reason, fix))
			}
		}
	}
	return out
}

func sortModels(out []ModelInfo) {
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
}

// EnvVars: opencode's credentials live entirely in auth.json — nothing
// rides provider.env.
func (h *opencodeHarness) EnvVars() map[string]string { return nil }

// AuthFiles (kinds.AuthLocator): auth.json, removed when the run ends.
func (h *opencodeHarness) AuthFiles(ctx context.Context, env ports.EnvID) []string {
	return []string{h.adapter.AuthFilePath(ctx, env)}
}

// SecretValues (kinds.SecretHolder): auth.json is opencode's ONLY credential
// artifact and never passes through provider.env, so the redactor learns about
// its model keys from here or not at all.
func (h *opencodeHarness) SecretValues() []string { return []string{h.authJSON} }

// SeedAuth writes the stored auth_json — THE model-auth artifact
// (harness-owned auth) — into the workspace at bootstrap step 2.
func (h *opencodeHarness) SeedAuth(ctx context.Context, env ports.EnvID) error {
	if h.authJSON == "" {
		return nil
	}
	if !json.Valid([]byte(h.authJSON)) {
		return fmt.Errorf("opencode: auth_json is not valid JSON")
	}
	return h.adapter.SeedAuthJSON(ctx, env, []byte(h.authJSON))
}

func (h *opencodeHarness) Prepare(ctx context.Context, env ports.EnvID, specs []*agentspec.Spec) error {
	return h.adapter.PrepareEnv(ctx, env, specs)
}

var _ HarnessKind = Opencode{}
