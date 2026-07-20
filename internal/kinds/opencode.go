package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

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
		adapter:  opencodeadapter.New(envp, opencodeadapter.Config{}),
		version:  cfg.Str("version", OpencodeVersion),
		authJSON: cfg.Secrets["auth_json"],
	}, nil
}

type opencodeHarness struct {
	adapter  *opencodeadapter.Adapter
	version  string
	authJSON string
}

func (h *opencodeHarness) Run(ctx context.Context, env ports.EnvID, spec ports.AgentSpec, in ports.AgentInput) (ports.AgentResult, error) {
	return h.adapter.Run(ctx, env, spec, in)
}

func (h *opencodeHarness) Resume(ctx context.Context, env ports.EnvID, s ports.SessionID, reply string) (ports.AgentResult, error) {
	return h.adapter.Resume(ctx, env, s, reply)
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
}

// egressBaseline keeps the pre-derivation behavior when auth_json is absent
// or unparseable at resolution time.
var egressBaseline = []string{"api.anthropic.com", "api.openai.com", "openrouter.ai"}

func (h *opencodeHarness) EgressDomains() []string {
	// Harness-owned auth: whatever auth.json can route to must not be
	// blocked by the very firewall that ships with it (the M3 openrouter
	// lesson, generalized) — so the allowlist is DERIVED from the same
	// auth_json keys Models() advertises, keeping the two in agreement.
	auth := map[string]json.RawMessage{}
	if h.authJSON == "" || json.Unmarshal([]byte(h.authJSON), &auth) != nil || len(auth) == 0 {
		return egressBaseline
	}
	seen := map[string]bool{}
	var out []string
	for providerID := range auth {
		for _, d := range providerDomains[providerID] {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	if len(out) == 0 {
		return egressBaseline
	}
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
	Name   string                  `json:"name"`
	Models map[string]catalogModel `json:"models"`
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
	for providerID := range auth {
		p, ok := catalog[providerID]
		if !ok {
			continue // provider unknown to the catalog (e.g. bespoke auth entries)
		}
		for modelID, m := range p.Models {
			name := m.Name
			if name == "" {
				name = modelID
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

func sortModels(out []ModelInfo) {
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
}

// EnvVars: opencode's credentials live entirely in auth.json — nothing
// rides provider.env.
func (h *opencodeHarness) EnvVars() map[string]string { return nil }

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
