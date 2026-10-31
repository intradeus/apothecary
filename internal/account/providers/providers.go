// Package providers is the closed set of model providers apothecary manages
// credentials for, and what each harness calls them (docs/model-providers-
// plan.md). A provider credential belongs to a user; a harness integration
// references some of them; a harness renders the referenced ones into its own
// native format at provisioning. Nothing outside this table is accepted: an
// API key for a provider we do not list has no egress domain, no probe and no
// native mapping, and an OAuth login we do not list cannot be refreshed —
// which is the whole point of holding logins engine-side.
//
// Every mapping here is taken from the pinned harness's own source (opencode
// 1.17.8 auth plugins, codex 0.160.1, omp 18.13.1 rules/auth/*.kdl), not from
// documentation.
package providers

import (
	"sort"
)

// Kind is what a credential holds.
type Kind string

const (
	// KindAPIKey is a static key: nothing to refresh.
	KindAPIKey Kind = "api_key"
	// KindOAuth is a login: an access token the keeper refreshes from a
	// refresh token that never leaves the engine.
	KindOAuth Kind = "oauth"
)

// Flow names one OAuth flow the keeper knows end to end: its token endpoint,
// client id and rotation semantics (internal/account/logins).
type Flow string

const (
	FlowChatGPT     Flow = "openai-chatgpt"
	FlowAnthropic   Flow = "anthropic"
	FlowGeminiCLI   Flow = "google-gemini-cli"
	FlowAntigravity Flow = "google-antigravity"
	FlowCopilot     Flow = "github-copilot"
)

// Harness kinds the table maps (kinds.* descriptors' Kind).
const (
	Opencode = "opencode"
	Codex    = "codex"
	Omp      = "omp"
)

// Native is how one harness consumes one provider.
type Native struct {
	// ID is the provider's id in that harness: the model prefix flows write
	// (`anthropic/…`, `openai-codex/…`), the opencode auth.json key, the omp
	// broker snapshot's provider.
	ID string
	// Env is the variable an API key rides in provider.env (codex, omp).
	// Empty for opencode, which reads keys from its auth.json.
	Env string
	// Key / OAuth say which credential kinds the harness takes for this
	// provider. A harness that takes neither is not listed.
	Key, OAuth bool
}

// Provider is one model provider.
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Key: the provider takes an API key. Flow: the provider has a login the
	// keeper can refresh ("" = none).
	Key  bool `json:"key"`
	Flow Flow `json:"flow,omitempty"`
	// Catalog is the provider's models.dev id: the key of the shared probe
	// and catalog tables (kinds.providerAuthProbe, fetchCatalog).
	Catalog string `json:"-"`
	// Domains are the egress hosts a workspace reaches with its credential.
	// A login's token endpoints are not among them: the keeper refreshes
	// engine-side, and a workspace holding no refresh token has no use for
	// them.
	Domains []string `json:"-"`
	// Harnesses maps a harness kind to how it consumes this provider.
	Harnesses map[string]Native `json:"-"`
}

// Kinds lists the credential kinds the provider accepts.
func (p Provider) Kinds() []Kind {
	var out []Kind
	if p.Key {
		out = append(out, KindAPIKey)
	}
	if p.Flow != "" {
		out = append(out, KindOAuth)
	}
	return out
}

// Native returns how harness consumes the provider with a credential of
// kind, ok=false when it does not.
func (p Provider) Native(harness string, kind Kind) (Native, bool) {
	n, ok := p.Harnesses[harness]
	if !ok {
		return Native{}, false
	}
	switch kind {
	case KindAPIKey:
		return n, n.Key && p.Key
	case KindOAuth:
		return n, n.OAuth && p.Flow != ""
	}
	return Native{}, false
}

// key is shorthand for an API-key-only provider served by opencode (auth.json
// entry) and omp (env var).
func key(id, name, catalog string, domains []string, opencodeID, ompID, ompEnv string) Provider {
	h := map[string]Native{}
	if opencodeID != "" {
		h[Opencode] = Native{ID: opencodeID, Key: true}
	}
	if ompID != "" {
		h[Omp] = Native{ID: ompID, Env: ompEnv, Key: true}
	}
	return Provider{ID: id, Name: name, Key: true, Catalog: catalog, Domains: domains, Harnesses: h}
}

var table = []Provider{
	{
		ID: "anthropic", Name: "Anthropic", Key: true, Flow: FlowAnthropic, Catalog: "anthropic",
		Domains: []string{"api.anthropic.com"},
		Harnesses: map[string]Native{
			// opencode 1.17.8 ships no Claude Pro/Max login: key only.
			Opencode: {ID: "anthropic", Key: true},
			Omp:      {ID: "anthropic", Env: "ANTHROPIC_API_KEY", Key: true, OAuth: true},
		},
	},
	{
		ID: "openai", Name: "OpenAI", Key: true, Catalog: "openai",
		Domains: []string{"api.openai.com"},
		Harnesses: map[string]Native{
			Opencode: {ID: "openai", Key: true},
			Codex:    {ID: "openai", Env: "OPENAI_API_KEY", Key: true},
			Omp:      {ID: "openai", Env: "OPENAI_API_KEY", Key: true},
		},
	},
	{
		ID: "openai-chatgpt", Name: "ChatGPT Plus/Pro (login)", Flow: FlowChatGPT, Catalog: "openai",
		Domains: []string{"chatgpt.com", "api.openai.com"},
		Harnesses: map[string]Native{
			Codex:    {ID: "openai", OAuth: true},
			Opencode: {ID: "openai", OAuth: true},
			Omp:      {ID: "openai-codex", OAuth: true},
		},
	},
	key("openrouter", "OpenRouter", "openrouter", []string{"openrouter.ai"}, "openrouter", "openrouter", "OPENROUTER_API_KEY"),
	key("google", "Google AI Studio (Gemini API)", "google", []string{"generativelanguage.googleapis.com"}, "google", "google", "GEMINI_API_KEY"),
	{
		ID: "google-gemini-cli", Name: "Google Gemini CLI (login)", Flow: FlowGeminiCLI, Catalog: "google",
		Domains:   []string{"cloudcode-pa.googleapis.com"},
		Harnesses: map[string]Native{Omp: {ID: "google-gemini-cli", OAuth: true}},
	},
	{
		ID: "google-antigravity", Name: "Google Antigravity (login)", Flow: FlowAntigravity, Catalog: "google",
		Domains:   []string{"daily-cloudcode-pa.googleapis.com", "cloudcode-pa.googleapis.com"},
		Harnesses: map[string]Native{Omp: {ID: "google-antigravity", OAuth: true}},
	},
	{
		ID: "github-copilot", Name: "GitHub Copilot (login)", Flow: FlowCopilot, Catalog: "github-copilot",
		Domains: []string{"api.githubcopilot.com", "api.github.com"},
		Harnesses: map[string]Native{
			Opencode: {ID: "github-copilot", OAuth: true},
			Omp:      {ID: "github-copilot", OAuth: true},
		},
	},
	key("xai", "xAI", "xai", []string{"api.x.ai"}, "xai", "xai", "XAI_API_KEY"),
	key("groq", "Groq", "groq", []string{"api.groq.com"}, "groq", "groq", "GROQ_API_KEY"),
	key("mistral", "Mistral", "mistral", []string{"api.mistral.ai"}, "mistral", "mistral", "MISTRAL_API_KEY"),
	key("deepseek", "DeepSeek", "deepseek", []string{"api.deepseek.com"}, "deepseek", "deepseek", "DEEPSEEK_API_KEY"),
	key("cerebras", "Cerebras", "cerebras", []string{"api.cerebras.ai"}, "cerebras", "cerebras", "CEREBRAS_API_KEY"),
	key("together", "Together AI", "togetherai", []string{"api.together.xyz"}, "togetherai", "together", "TOGETHER_API_KEY"),
	key("fireworks", "Fireworks AI", "fireworks-ai", []string{"api.fireworks.ai"}, "fireworks-ai", "fireworks", "FIREWORKS_API_KEY"),
	// The two OpenCode gateways share one key variable in omp: a harness
	// integration may reference only one of them there (Conflicts).
	key("opencode-zen", "OpenCode Zen", "opencode", []string{"opencode.ai"}, "opencode", "opencode-zen", "OPENCODE_API_KEY"),
	key("opencode-go", "OpenCode Go", "opencode-go", []string{"opencode.ai"}, "opencode-go", "opencode-go", "OPENCODE_API_KEY"),
}

var byID = func() map[string]Provider {
	m := make(map[string]Provider, len(table))
	for _, p := range table {
		m[p.ID] = p
	}
	return m
}()

// All lists every provider, by id.
func All() []Provider {
	out := append([]Provider(nil), table...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get returns one provider.
func Get(id string) (Provider, bool) {
	p, ok := byID[id]
	return p, ok
}

// For lists the providers a harness kind consumes with at least one
// credential kind, by id.
func For(harness string) []Provider {
	var out []Provider
	for _, p := range All() {
		if _, ok := p.Harnesses[harness]; ok {
			out = append(out, p)
		}
	}
	return out
}

// ByNative finds the provider a harness calls nativeID for a credential of
// kind: what an importer uses to read a harness's own auth file.
func ByNative(harness, nativeID string, kind Kind) (Provider, bool) {
	for _, p := range table {
		if n, ok := p.Native(harness, kind); ok && n.ID == nativeID {
			return p, true
		}
	}
	return Provider{}, false
}

// ByEnv finds the provider whose API key a harness reads from env.
func ByEnv(harness, env string) (Provider, bool) {
	for _, p := range table {
		if n, ok := p.Native(harness, KindAPIKey); ok && n.Env != "" && n.Env == env {
			return p, true
		}
	}
	return Provider{}, false
}

// Sink is where a harness puts one credential: two credentials with the same
// sink would overwrite each other (opencode's auth.json entry "openai" for
// both an API key and a ChatGPT login; omp's OPENCODE_API_KEY for both
// OpenCode gateways). An integration may reference at most one per sink.
func Sink(harness string, p Provider, kind Kind) string {
	n, ok := p.Native(harness, kind)
	if !ok {
		return ""
	}
	switch {
	case harness == Opencode:
		return "auth.json:" + n.ID
	case kind == KindAPIKey && n.Env != "":
		return "env:" + n.Env
	case harness == Codex && kind == KindOAuth:
		return "auth.json"
	default:
		return "login:" + n.ID
	}
}
