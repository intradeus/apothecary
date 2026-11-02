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

	"apothecary/internal/account/providers"
	"apothecary/internal/adapters/acpagent"
	opencodeadapter "apothecary/internal/adapters/opencode"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/platform/telemetry"
	"apothecary/internal/ports/envport"
)

// OpencodeVersion is the pinned opencode release: the tools bundle mounted
// into every workspace ships exactly this build (workspace/images/tools;
// TestToolsDockerfilePinsMatchKinds holds the Dockerfile's ARG to it).
const OpencodeVersion = "1.17.8"

// OpencodeEditorExtension is opencode's own extension: it opens a terminal
// running the `opencode` on PATH (the bundled one), which reads the
// auth.json SeedAuth writes — keys and logins alike.
const OpencodeEditorExtension = "sst-dev.opencode"

// Opencode is the harness/opencode kind (DESIGN2 §4.1).
type Opencode struct{}

func (Opencode) Descriptor() Descriptor {
	return Descriptor{
		Port: "harness", Kind: "opencode", Label: "opencode",
		Help:   "Runs opencode " + OpencodeVersion + ", the version pinned by this apothecary release.",
		Fields: []Field{credentialsField(providers.Opencode)},
	}
}

func (Opencode) New(cfg Config, envp envport.EnvProvisioner) (Harness, error) {
	return &opencodeHarness{
		Runner:  acpagent.New(envp, opencodeadapter.Profile()),
		adapter: opencodeadapter.New(envp, opencodeadapter.Config{}),
		creds:   cfg.Credentials,
		held:    bind(providers.Opencode, cfg.Credentials.Snapshot),
	}, nil
}

// opencodeHarness runs as a long-lived ACP agent (`opencode acp`) in the
// workspace sidecar; the embedded runner is the AgentRunner (plus reattach
// and stop), the adapter generates its config.
type opencodeHarness struct {
	*acpagent.Runner
	adapter *opencodeadapter.Adapter
	creds   Credentials
	// held is the snapshot, bound: what the static parts read.
	held []bound
}

// Test checks the harness has something to run on.
func (h *opencodeHarness) Test(_ context.Context) error {
	if len(h.held) == 0 {
		return fmt.Errorf("opencode: no model provider credentials — add one under System → Model providers and pick it here")
	}
	return nil
}

func (h *opencodeHarness) Binary() string { return "opencode" }

// opencodeInfra are the opencode CLI's own runtime endpoints, needed
// regardless of model provider: models.dev (its model catalog) and the npm
// registry (it is an npm package and version/plugin-checks at runtime —
// found live as egress denials in a Go project). Always allowed.
var opencodeInfra = []string{"models.dev", "registry.npmjs.org"}

func (h *opencodeHarness) EgressDomains() []string {
	// Whatever the credentials route to must not be blocked by the firewall
	// that ships with them; the harness's own infra is always allowed.
	return uniqueSorted(append(append([]string(nil), opencodeInfra...), domains(h.held)...))
}

// uniqueSorted dedupes and sorts host names.
func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range in {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
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
	Name string `json:"name"`
	// API is the provider's OpenAI-compatible base URL (models.dev supplies
	// it, e.g. opencode-go → https://opencode.ai/zen/go/v1). Present ⇒ we can
	// ask the provider what it ACTUALLY serves right now.
	API    string                  `json:"api"`
	Models map[string]catalogModel `json:"models"`
}

// authEntry is what a live model listing or a probe needs of a credential:
// an API key is a bearer we can query the provider with; a login lists from
// the catalog.
type authEntry struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

func entryOf(b bound) authEntry {
	if b.Kind == providers.KindAPIKey {
		return authEntry{Type: "api", Key: b.Key}
	}
	return authEntry{Type: "oauth"}
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
	client := &http.Client{Transport: telemetry.HTTPTransport(http.DefaultTransport), Timeout: 20 * time.Second}
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
// harness holds credentials for, under opencode's provider ids; the catalog
// is opencode's own (models.dev).
func (h *opencodeHarness) Models(ctx context.Context) ([]ModelInfo, error) {
	if len(h.held) == 0 {
		return nil, fmt.Errorf("opencode: no model provider credentials — pick some on the integration first")
	}
	catalog, err := fetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	var out []ModelInfo
	for _, b := range h.held {
		p, ok := catalog[b.provider.Catalog]
		if !ok {
			continue
		}
		for _, modelID := range providerModelIDs(ctx, p, entryOf(b)) {
			name := modelID // catalog supplies a prettier name when it knows the model
			if m, ok := p.Models[modelID]; ok && m.Name != "" {
				name = m.Name
			}
			out = append(out, ModelInfo{ID: b.native.ID + "/" + modelID, Name: p.Name + " · " + name})
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
	client := &http.Client{Transport: telemetry.HTTPTransport(http.DefaultTransport), Timeout: 10 * time.Second}
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

// CheckAuth (kinds.AuthChecker) probes the credential of every provider the
// flow's models route through. The OpenCode gateways are probed per model:
// their probe also says whether the model is still served, which their
// catalog entry (they have no live list) cannot.
func (h *opencodeHarness) CheckAuth(ctx context.Context, models []string) AuthResult {
	return h.checkAuth(ctx, func(map[string]catalogProvider) []string { return models }, false)
}

// CheckCredentials (kinds.AuthChecker) probes every credential held. A
// gateway's probe needs a model, so it borrows one from the catalog; that
// model being retired leaves the key unverified.
func (h *opencodeHarness) CheckCredentials(ctx context.Context) AuthResult {
	return h.checkAuth(ctx, func(catalog map[string]catalogProvider) []string {
		var models []string
		for _, b := range h.held {
			ids := make([]string, 0, len(catalog[b.provider.Catalog].Models))
			for id := range catalog[b.provider.Catalog].Models {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			model := ""
			if len(ids) > 0 {
				model = ids[0]
			}
			models = append(models, b.native.ID+"/"+model)
		}
		sort.Strings(models)
		return models
	}, true)
}

// checkAuth probes the models pick returns; credentialOnly reports a "model
// refused" verdict as unverified — the model was borrowed, not chosen. A
// login needs no probe: the keeper just proved its refresh token (a disabled
// one is a Problem, refused at run creation).
func (h *opencodeHarness) checkAuth(ctx context.Context, pick func(map[string]catalogProvider) []string, credentialOnly bool) AuthResult {
	var out AuthResult
	for _, msg := range sortedProblems(h.creds.Problems) {
		out.Rejected = append(out.Rejected, "opencode: "+msg)
	}
	byNative := map[string]bound{}
	for _, b := range h.held {
		if _, taken := byNative[b.native.ID]; !taken || b.Kind == providers.KindAPIKey {
			byNative[b.native.ID] = b
		}
	}
	catalog, err := fetchCatalog(ctx)
	if err != nil {
		out.Unverified = append(out.Unverified, "opencode: "+err.Error())
		return out
	}
	done := map[string]bool{}
	for _, m := range pick(catalog) {
		nativeID, modelID, ok := strings.Cut(m, "/")
		b, held := byNative[nativeID]
		if !ok || !held {
			continue // the model check refuses a provider the harness lacks
		}
		if b.Kind == providers.KindOAuth {
			continue
		}
		pr, probeable := providerAuthProbe(b.provider.Catalog, catalog[b.provider.Catalog], b.Key, modelID)
		label, once := "opencode provider "+nativeID, nativeID
		if pr.kind == probeEmptyChat && !credentialOnly {
			label, once = "opencode model "+m, m
		}
		if done[once] {
			continue
		}
		done[once] = true
		if !probeable {
			out.Unverified = append(out.Unverified, label+": no zero-token probe for this provider")
			continue
		}
		rejected, reason, err := pr.run(ctx, b.Key)
		switch {
		case err != nil:
			out.Unverified = append(out.Unverified, fmt.Sprintf("%s: %v", label, err))
		case rejected && credentialOnly && strings.HasPrefix(reason, "model"):
			// The gateway checks the model before the key: this says
			// nothing about the credential.
			out.Unverified = append(out.Unverified, fmt.Sprintf("%s: probe model %s %s", label, modelID, reason))
		case rejected:
			fix := "fix the " + b.provider.Name + " API key under System → Model providers"
			if strings.HasPrefix(reason, "model") {
				fix = "pick another model in the flow"
			}
			out.Rejected = append(out.Rejected, fmt.Sprintf("%s: %s — %s", label, reason, fix))
		}
	}
	return out
}

// sortedProblems lists credential problems in a stable order.
func sortedProblems(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func sortModels(out []ModelInfo) {
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
}

// EnvVars: opencode's credentials live entirely in auth.json — nothing
// rides provider.env.
func (h *opencodeHarness) EnvVars() map[string]string { return nil }

// EditorExtensions (kinds.EditorExtender): opencode's extension.
func (h *opencodeHarness) EditorExtensions() []string { return []string{OpencodeEditorExtension} }

// AuthFiles (kinds.AuthLocator): auth.json, removed when the run ends.
func (h *opencodeHarness) AuthFiles(ctx context.Context, env envport.EnvID) []string {
	return []string{h.adapter.AuthFilePath(ctx, env)}
}

// SecretValues (kinds.SecretHolder): auth.json never passes through
// provider.env, so the redactor learns its keys and tokens from here.
func (h *opencodeHarness) SecretValues() []string { return secretValues(h.held) }

// SeedAuth renders auth.json from the current credentials and writes it.
// opencode re-reads the file on every request (its Auth.all), so writing it
// again after the keeper refreshed a login reaches the live agent.
func (h *opencodeHarness) SeedAuth(ctx context.Context, env envport.EnvID, register func(string)) error {
	creds, err := h.creds.current(ctx)
	if err != nil {
		return fmt.Errorf("opencode: %w", err)
	}
	doc, secrets, err := renderOpencodeAuth(bind(providers.Opencode, creds))
	if err != nil {
		return err
	}
	for _, s := range secrets {
		register(s)
	}
	return h.adapter.SeedAuthJSON(ctx, env, doc)
}

// renderOpencodeAuth builds opencode's auth.json (its Auth.Info union): an
// API key is {type: api, key}; a login is {type: oauth, access, expires,
// refresh} with refresh EMPTY — the keeper holds it, and an empty string is
// what opencode's schema accepts. A Copilot login is the exception that
// proves the rule: opencode sends `refresh` as the Copilot bearer, and the
// GitHub token is both — nothing rotates it, nothing is withheld.
func renderOpencodeAuth(bs []bound) ([]byte, []string, error) {
	doc := map[string]map[string]any{}
	var secrets []string
	for _, b := range bs {
		switch b.Kind {
		case providers.KindAPIKey:
			doc[b.native.ID] = map[string]any{"type": "api", "key": b.Key}
		case providers.KindOAuth:
			e := map[string]any{"type": "oauth", "access": b.Access, "refresh": "", "expires": expiresMillis(b.Expires)}
			if b.provider.Flow == providers.FlowCopilot {
				e["refresh"], e["expires"] = b.Access, 0
			}
			if a := b.Get(providers.ExtraAccountID); a != "" {
				e["accountId"] = a
			}
			if u := b.Get(providers.ExtraEnterprise); u != "" {
				e["enterpriseUrl"] = u
			}
			doc[b.native.ID] = e
		}
		secrets = append(secrets, b.SecretValues()...)
	}
	out, err := json.Marshal(doc)
	return out, secrets, err
}

// expiresMillis is an expiry in Unix milliseconds, 0 for none.
func expiresMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func (h *opencodeHarness) Prepare(ctx context.Context, env envport.EnvID, specs []*agentspec.Spec) error {
	return h.adapter.PrepareEnv(ctx, env, specs)
}

func (h *opencodeHarness) InstallSkills(ctx context.Context, env envport.EnvID, names []string) error {
	return h.adapter.InstallSkills(ctx, env, names)
}

var _ HarnessKind = Opencode{}
