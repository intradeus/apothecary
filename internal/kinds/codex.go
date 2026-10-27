package kinds

import (
	"context"
	"encoding/json"
	"fmt"

	"apothecary/internal/adapters/acpagent"
	codexadapter "apothecary/internal/adapters/codex"
	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
)

// CodexVersion is the pinned codex CLI: the tools bundle ships this static
// build (workspace/images/tools; TestToolsDockerfilePinsMatchKinds holds the
// Dockerfile's ARG to it), both for humans in the terminal and as the
// app-server the ACP bridge below spawns (CODEX_PATH). Must satisfy the
// bridge's own @openai/codex requirement.
const CodexVersion = "0.160.1"

// CodexACPVersion is the pinned ACP bridge (@agentclientprotocol/codex-acp)
// the engine drives codex through, compiled into the tools bundle.
const CodexACPVersion = "2.2.2"

// Codex is the harness/codex kind (DESIGN2 §13). Harness-owned auth: an
// OpenAI API key (rides OPENAI_API_KEY via provider.env) and/or a ChatGPT
// login auth.json (seeded into $CODEX_HOME).
type Codex struct{}

func (Codex) Descriptor() Descriptor {
	return Descriptor{
		Port: "harness", Kind: "codex", Label: "codex",
		Help: "Runs codex " + CodexVersion + " through codex-acp " + CodexACPVersion + ", the versions pinned by this apothecary release.",
		Fields: []Field{
			{Name: "api_key", Label: "OpenAI API key (either this or auth.json)", Secret: true,
				Placeholder: "sk-…"},
			{Name: "auth_json", Label: "auth.json — ChatGPT subscription login, contents of ~/.codex/auth.json (optional)",
				Secret: true, Placeholder: `{"OPENAI_API_KEY":null,"tokens":{…}}`},
		},
	}
}

func (Codex) New(cfg Config, envp ports.EnvProvisioner) (Harness, error) {
	adapter := codexadapter.New(envp, codexadapter.Config{})
	return &codexHarness{
		Runner:   acpagent.New(envp, adapter.Profile()),
		adapter:  adapter,
		apiKey:   cfg.Secrets["api_key"],
		authJSON: cfg.Secrets["auth_json"],
	}, nil
}

// codexHarness runs as a long-lived ACP agent (codex-acp) in the workspace
// sidecar; the embedded runner is the AgentRunner, the adapter its config.
type codexHarness struct {
	*acpagent.Runner
	adapter  *codexadapter.Adapter
	apiKey   string
	authJSON string
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

// Binary is what envcheck probes: the bridge is what the engine runs.
func (h *codexHarness) Binary() string { return "codex-acp" }

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

// AuthFiles (kinds.AuthLocator): auth.json, removed when the run ends. The
// API key needs no entry — it lives in provider.env, which the engine owns.
func (h *codexHarness) AuthFiles(ctx context.Context, env ports.EnvID) []string {
	return []string{h.adapter.AuthFilePath(ctx, env)}
}

// SecretValues (kinds.SecretHolder): the ChatGPT-login auth.json. The API key
// needs no mention — it rides EnvVars, which the redactor already covers.
func (h *codexHarness) SecretValues() []string { return []string{h.authJSON} }

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

// CheckAuth (kinds.AuthChecker) probes the API key against OpenAI's model
// list. A ChatGPT login has no zero-token probe; when one is configured too,
// codex may well be using it, so a rejected key is only a warning.
func (h *codexHarness) CheckAuth(ctx context.Context, models []string) AuthResult {
	if len(models) == 0 {
		return AuthResult{}
	}
	return h.CheckCredentials(ctx)
}

// CheckCredentials (kinds.AuthChecker): codex has one credential whatever
// the model, so it is the same probe.
func (h *codexHarness) CheckCredentials(ctx context.Context) AuthResult {
	var out AuthResult
	if h.apiKey == "" {
		if h.authJSON != "" {
			out.Unverified = append(out.Unverified, "codex: ChatGPT login — no zero-token probe")
		}
		return out
	}
	rejected, reason, err := openaiModelsProbe(h.apiKey).run(ctx, h.apiKey)
	switch {
	case err != nil:
		out.Unverified = append(out.Unverified, "codex api_key: "+err.Error())
	case rejected && h.authJSON != "":
		out.Unverified = append(out.Unverified, "codex api_key: "+reason+" (the ChatGPT login may still work)")
	case rejected:
		out.Rejected = append(out.Rejected, "codex api_key: "+reason+" — fix api_key on the codex integration")
	}
	return out
}

var _ HarnessKind = Codex{}
