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
	ompadapter "apothecary/internal/adapters/omp"
	"apothecary/internal/agentspec"
	"apothecary/internal/ports/envport"
	"apothecary/internal/providers"
)

// OmpVersion is the pinned omp release: the tools bundle ships exactly this
// standalone binary (the npm package needs bun at runtime; see
// workspace/images/tools — TestToolsDockerfilePinsMatchKinds holds the
// Dockerfile's ARG to it). Its model catalog is read at this tag too.
const OmpVersion = "18.13.1"

// Omp is the harness/omp kind: oh-my-pi (github.com/can1357/oh-my-pi), driven
// as an ACP agent. Its credentials are provider API keys, rendered as the env
// vars omp reads natively (provider.env), and logins, which never enter the
// workspace as files: omp runs in auth-broker mode against the engine, which
// serves it access tokens and refreshes them itself (ompbroker.go).
type Omp struct{}

func (Omp) Descriptor() Descriptor {
	return Descriptor{
		Port: "harness", Kind: "omp", Label: "omp (oh-my-pi)",
		Help:   "oh-my-pi coding agent " + OmpVersion + " (the version pinned by this apothecary release).",
		Fields: []Field{credentialsField(providers.Omp)},
	}
}

func (Omp) New(cfg Config, envp envport.EnvProvisioner) (Harness, error) {
	adapter := ompadapter.New(envp, ompadapter.Config{})
	return &ompHarness{
		Runner:  acpagent.New(envp, adapter.Profile()),
		adapter: adapter,
		creds:   cfg.Credentials,
		held:    bind(providers.Omp, cfg.Credentials.Snapshot),
	}, nil
}

// ompHarness runs as a long-lived ACP agent (`omp acp`) in the workspace
// sidecar; the embedded runner is the AgentRunner, the adapter its config.
type ompHarness struct {
	*acpagent.Runner
	adapter *ompadapter.Adapter
	creds   Credentials
	held    []bound
}

// ompInfra are omp's own runtime endpoints, needed whatever the provider: its
// model catalog refresh. Always allowed.
var ompInfra = []string{"catalog.stencil.so"}

// Test checks the harness has something to run on.
func (h *ompHarness) Test(_ context.Context) error {
	if len(h.held) == 0 {
		return fmt.Errorf("omp: no model provider credentials — add one under System → Model providers and pick it here")
	}
	return nil
}

func (h *ompHarness) Binary() string { return "omp" }

// EgressDomains derives the allowlist from the same credentials the models
// come from, plus omp's own infrastructure.
func (h *ompHarness) EgressDomains() []string {
	return uniqueSorted(append(append([]string(nil), ompInfra...), domains(h.held)...))
}

// HasLogins (kinds.AuthBrokered): omp needs the engine's auth broker when it
// holds at least one login.
func (h *ompHarness) HasLogins() bool {
	for _, b := range h.held {
		if b.Kind == providers.KindOAuth {
			return true
		}
	}
	return false
}

// Models lists `provider/model` for exactly the providers the credentials
// unlock, from omp's own bundled catalog at the pinned version — the list its
// ACP `model` option offers, in its provider ids. A login serves its own
// models (a ChatGPT login: openai-codex's handful, not the API's catalog),
// which only omp's catalog knows.
func (h *ompHarness) Models(ctx context.Context) ([]ModelInfo, error) {
	if len(h.held) == 0 {
		return nil, fmt.Errorf("omp: no model provider credentials — pick some on the integration first")
	}
	catalog, err := fetchOmpCatalog(ctx, OmpVersion)
	if err != nil {
		return nil, err
	}
	var out []ModelInfo
	for _, b := range h.held {
		label := b.native.ID
		if b.Kind == providers.KindOAuth {
			label += " (login)"
		}
		for modelID, m := range catalog[b.native.ID] {
			name := m.Name
			if name == "" {
				name = modelID
			}
			out = append(out, ModelInfo{ID: b.native.ID + "/" + modelID, Name: label + " · " + name})
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
// flow's models route through. A login needs no probe: the keeper just
// proved its refresh token.
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
	for _, b := range h.held {
		want[b.native.ID] = ""
	}
	return h.checkAuth(ctx, want, true)
}

// checkAuth probes the held keys in want (omp id → the model routed through
// it, "" to borrow one from the catalog); credentialOnly reports a "model
// refused" verdict as unverified — the model was borrowed.
func (h *ompHarness) checkAuth(ctx context.Context, want map[string]string, credentialOnly bool) AuthResult {
	var out AuthResult
	for _, msg := range sortedProblems(h.creds.Problems) {
		out.Rejected = append(out.Rejected, "omp: "+msg)
	}
	var catalog map[string]map[string]ompModel
	for _, b := range h.held {
		model, ok := want[b.native.ID]
		if !ok || b.Kind != providers.KindAPIKey {
			continue
		}
		label := "omp provider " + b.native.ID
		if catalog == nil {
			c, err := fetchOmpCatalog(ctx, OmpVersion)
			if err != nil {
				out.Unverified = append(out.Unverified, "omp: "+err.Error())
				return out
			}
			catalog = c
		}
		models := catalog[b.native.ID]
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
		pr, probeable := providerAuthProbe(b.provider.Catalog, catalogProvider{Name: b.native.ID, API: ompAPIBase(models)}, b.Key, model)
		if !probeable {
			out.Unverified = append(out.Unverified, label+": no zero-token probe for this provider")
			continue
		}
		rejected, reason, err := pr.run(ctx, b.Key)
		switch {
		case err != nil:
			out.Unverified = append(out.Unverified, fmt.Sprintf("%s: %v", label, err))
		case rejected && credentialOnly && strings.HasPrefix(reason, "model"):
			out.Unverified = append(out.Unverified, fmt.Sprintf("%s: probe model %s %s", label, model, reason))
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
// The broker's address and token are per run, so the engine adds them.
func (h *ompHarness) EnvVars() map[string]string {
	env := map[string]string{}
	for _, b := range h.held {
		if b.Kind == providers.KindAPIKey && b.native.Env != "" {
			env[b.native.Env] = b.Key
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// AuthFiles (kinds.AuthLocator): agent.db, omp's credential store (in broker
// mode it holds no login, but an older workspace may), removed when the run
// ends.
func (h *ompHarness) AuthFiles(ctx context.Context, env envport.EnvID) []string {
	return []string{h.adapter.AuthFilePath(ctx, env)}
}

// SecretValues (kinds.SecretHolder): the logins' access tokens as of the
// snapshot; the broker registers every token it serves before serving it.
func (h *ompHarness) SecretValues() []string { return secretValues(h.held) }

// SeedAuth: nothing to write — keys ride provider.env and logins the broker.
func (h *ompHarness) SeedAuth(context.Context, envport.EnvID, func(string)) error { return nil }

func (h *ompHarness) Prepare(ctx context.Context, env envport.EnvID, specs []*agentspec.Spec) error {
	return h.adapter.PrepareEnv(ctx, env, specs)
}

func (h *ompHarness) InstallSkills(ctx context.Context, env envport.EnvID, names []string) error {
	return h.adapter.InstallSkills(ctx, env, names)
}

var _ HarnessKind = Omp{}
