package kinds

import (
	"context"
	"encoding/json"
	"fmt"

	codexadapter "apothecary/internal/adapters/codex"
	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
)

// CodexVersion is the pinned harness version (flags + JSONL verified
// against it, see the adapter's package comment).
const CodexVersion = "0.144.6"

// Codex is the harness/codex kind (DESIGN2 §13). Harness-owned auth: an
// OpenAI API key (rides OPENAI_API_KEY via provider.env) and/or a ChatGPT
// login auth.json (seeded into $CODEX_HOME).
type Codex struct{}

func (Codex) Descriptor() Descriptor {
	return Descriptor{
		Port: "harness", Kind: "codex", Label: "codex",
		Fields: []Field{
			{Name: "version", Label: "Version", Default: CodexVersion},
			{Name: "api_key", Label: "OpenAI API key (either this or auth.json)", Secret: true,
				Placeholder: "sk-…"},
			{Name: "auth_json", Label: "auth.json — ChatGPT subscription login, contents of ~/.codex/auth.json (optional)",
				Secret: true, Placeholder: `{"OPENAI_API_KEY":null,"tokens":{…}}`},
		},
	}
}

func (Codex) New(cfg Config, envp ports.EnvProvisioner) (Harness, error) {
	return &codexHarness{
		adapter:  codexadapter.New(envp, codexadapter.Config{}),
		version:  cfg.Str("version", CodexVersion),
		apiKey:   cfg.Secrets["api_key"],
		authJSON: cfg.Secrets["auth_json"],
	}, nil
}

type codexHarness struct {
	adapter  *codexadapter.Adapter
	version  string
	apiKey   string
	authJSON string
}

func (h *codexHarness) Run(ctx context.Context, env ports.EnvID, spec ports.AgentSpec, in ports.AgentInput) (ports.AgentResult, error) {
	return h.adapter.Run(ctx, env, spec, in)
}

func (h *codexHarness) Resume(ctx context.Context, env ports.EnvID, s ports.SessionID, reply string) (ports.AgentResult, error) {
	return h.adapter.Resume(ctx, env, s, reply)
}

func (h *codexHarness) Test(_ context.Context) error {
	if h.authJSON != "" && !json.Valid([]byte(h.authJSON)) {
		return fmt.Errorf("codex: auth_json is not valid JSON")
	}
	if h.apiKey == "" && h.authJSON == "" {
		return fmt.Errorf("codex: configure api_key or auth_json — the harness owns model auth")
	}
	return nil
}

func (h *codexHarness) Binary() string { return "codex" }

func (h *codexHarness) InstallLayer() []string {
	return []string{fmt.Sprintf("RUN npm install -g @openai/codex@%s", h.version)}
}

func (h *codexHarness) EgressDomains() []string {
	// api endpoints + the npm registry (codex is an npm CLI and
	// version-checks at runtime, same class as opencode's denials).
	return []string{"api.openai.com", "chatgpt.com", "auth.openai.com", "registry.npmjs.org"}
}

// EnvVars: API-key auth rides the standard env var through provider.env —
// the harness feeds it (harness-owned auth; the mechanism the provider-port
// removal deliberately kept).
func (h *codexHarness) EnvVars() map[string]string {
	if h.apiKey == "" {
		return nil
	}
	return map[string]string{"OPENAI_API_KEY": h.apiKey}
}

func (h *codexHarness) SeedAuth(ctx context.Context, env ports.EnvID) error {
	if h.authJSON == "" {
		return nil
	}
	if !json.Valid([]byte(h.authJSON)) {
		return fmt.Errorf("codex: auth_json is not valid JSON")
	}
	return h.adapter.SeedAuthJSON(ctx, env, []byte(h.authJSON))
}

func (h *codexHarness) Prepare(ctx context.Context, env ports.EnvID, specs []*agentspec.Spec) error {
	return h.adapter.PrepareEnv(ctx, env, specs)
}

// Models: codex routes OpenAI models; ids surface as `openai/<model>` (the
// flow format) and the adapter strips the prefix at invocation.
func (h *codexHarness) Models(ctx context.Context) ([]ModelInfo, error) {
	catalog, err := fetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := catalog["openai"]
	if !ok {
		return nil, fmt.Errorf("model catalog has no openai provider")
	}
	var out []ModelInfo
	for modelID, m := range p.Models {
		name := m.Name
		if name == "" {
			name = modelID
		}
		out = append(out, ModelInfo{ID: "openai/" + modelID, Name: p.Name + " · " + name})
	}
	sortModels(out)
	return out, nil
}

var _ HarnessKind = Codex{}
