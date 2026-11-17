package kinds

import (
	"context"
	"encoding/json"
	"fmt"

	"apothecary/internal/account/providers"
	"apothecary/internal/adapters/acpagent"
	claudecodeadapter "apothecary/internal/adapters/claudecode"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/envport"
)

// ClaudeAgentACPVersion is the pinned ACP bridge
// (@agentclientprotocol/claude-agent-acp) the engine drives Claude Code
// through: the tools bundle ships its standalone release build
// (workspace/images/tools; TestToolsDockerfilePinsMatchKinds holds the
// Dockerfile's ARG to it).
const ClaudeAgentACPVersion = "0.101.0"

// ClaudeCodeVersion is the `claude` CLI that bridge release's archives carry
// (read from the binary, `claude --version`), both for humans in the
// terminal and as what the bridge runs (CLAUDE_CODE_EXECUTABLE). It moves
// with ClaudeAgentACPVersion: the conformance test holds the two together.
const ClaudeCodeVersion = "2.1.318"

// ClaudeCodeEditorExtension is Anthropic's Claude Code extension. It runs
// its own bundled claude, which reads ~/.claude/.credentials.json — the file
// SeedAuth writes — so a Claude login reaches the editor as it reaches the
// agent (and leaves with it when the run ends). An API key does not: it
// rides provider.env, which only the agent's process sources.
const ClaudeCodeEditorExtension = "Anthropic.claude-code"

// ClaudeCode is the harness/claude-code kind. Its credential is an Anthropic
// API key (rides ANTHROPIC_API_KEY via provider.env) or a Claude login
// (rendered into ~/.claude/.credentials.json, refresh token withheld) —
// never both: Claude Code prefers the key, so providers.Sink gives the two
// one sink and an integration holds at most one.
type ClaudeCode struct{}

func (ClaudeCode) Descriptor() Descriptor {
	return Descriptor{
		Port: "harness", Kind: providers.ClaudeCode, Label: "Claude Code",
		Help:   "Runs Claude Code " + ClaudeCodeVersion + " through claude-agent-acp " + ClaudeAgentACPVersion + ", the versions pinned by this apothecary release.",
		Fields: []Field{credentialsField(providers.ClaudeCode)},
	}
}

func (ClaudeCode) New(cfg Config, envp envport.EnvProvisioner) (Harness, error) {
	adapter := claudecodeadapter.New(envp, claudecodeadapter.Config{})
	h := &claudeCodeHarness{
		Runner:  acpagent.New(envp, adapter.Profile()),
		adapter: adapter,
		creds:   cfg.Credentials,
	}
	for _, b := range bind(providers.ClaudeCode, cfg.Credentials.Snapshot) {
		switch b.Kind {
		case providers.KindAPIKey:
			h.apiKey = b.Key
			h.held = append(h.held, b)
		case providers.KindOAuth:
			bb := b
			h.login = &bb
			h.held = append(h.held, b)
		}
	}
	return h, nil
}

// claudeCodeHarness runs as a long-lived ACP agent (claude-agent-acp) in the
// workspace sidecar; the embedded runner is the AgentRunner, the adapter its
// config.
type claudeCodeHarness struct {
	*acpagent.Runner
	adapter *claudecodeadapter.Adapter
	creds   Credentials
	held    []bound
	apiKey  string
	login   *bound // the Claude login, as of the snapshot
}

func (h *claudeCodeHarness) Test(_ context.Context) error {
	if h.apiKey == "" && h.login == nil {
		return fmt.Errorf("claude-code: no model provider credentials — add an Anthropic API key or a Claude login under System → Model providers and pick it here")
	}
	return nil
}

// Binary is what envcheck probes: the bridge is what the engine runs.
func (h *claudeCodeHarness) Binary() string { return "claude-agent-acp" }

// EgressDomains are the credentials' own: with nonessential traffic off
// (adapter Profile), Claude Code reaches nothing else of its own.
func (h *claudeCodeHarness) EgressDomains() []string { return uniqueSorted(domains(h.held)) }

// EnvVars: API-key auth rides the standard env var through provider.env.
func (h *claudeCodeHarness) EnvVars() map[string]string {
	if h.apiKey == "" {
		return nil
	}
	return map[string]string{"ANTHROPIC_API_KEY": h.apiKey}
}

// EditorExtensions (kinds.EditorExtender): the Claude Code extension.
func (h *claudeCodeHarness) EditorExtensions() []string {
	return []string{ClaudeCodeEditorExtension}
}

// AuthFiles (kinds.AuthLocator): .credentials.json, removed when the run
// ends. The API key needs no entry — it lives in provider.env, which the
// engine owns.
func (h *claudeCodeHarness) AuthFiles(ctx context.Context, env envport.EnvID) []string {
	return []string{h.adapter.AuthFilePath(ctx, env)}
}

// SecretValues (kinds.SecretHolder): the login's access token. The API key
// needs no mention — it rides EnvVars, which the redactor already covers.
func (h *claudeCodeHarness) SecretValues() []string {
	if h.login == nil {
		return nil
	}
	return h.login.SecretValues()
}

// SeedAuth renders .credentials.json from the current Claude login, and the
// keeper's push rewrites it ahead of expiry, as for codex's auth.json: the
// file carries no refresh token, so Claude Code can only ever use the access
// token the engine hands it (that a running Claude Code picks up the
// rewritten file is held by TestClaudeCodeConformance).
func (h *claudeCodeHarness) SeedAuth(ctx context.Context, env envport.EnvID, register func(string)) error {
	if h.login == nil {
		return nil
	}
	creds, err := h.creds.current(ctx)
	if err != nil {
		return fmt.Errorf("claude-code: %w", err)
	}
	for _, b := range bind(providers.ClaudeCode, creds) {
		if b.Kind != providers.KindOAuth {
			continue
		}
		doc, err := renderClaudeCredentials(b)
		if err != nil {
			return err
		}
		for _, s := range b.SecretValues() {
			register(s)
		}
		return h.adapter.SeedCredentials(ctx, env, doc)
	}
	return nil
}

// claudeScopes are the scopes the engine's Anthropic sign-in grants
// (internal/account/logins): Claude Code uses a login for inference only when
// its scopes include user:inference.
var claudeScopes = []string{"org:create_api_key", "user:profile", "user:inference", "user:sessions:claude_code", "user:mcp_servers", "user:file_upload"}

// renderClaudeCredentials builds Claude Code's .credentials.json: the
// login's access token with refreshToken EMPTY, and expiresAt in ms (null
// for a token with no expiry, which Claude Code reads as never expired).
func renderClaudeCredentials(b bound) ([]byte, error) {
	var expiresAt any
	if !b.Expires.IsZero() {
		expiresAt = b.Expires.UnixMilli()
	}
	return json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  b.Access,
			"refreshToken": "",
			"expiresAt":    expiresAt,
			"scopes":       claudeScopes,
		},
	})
}

func (h *claudeCodeHarness) Prepare(ctx context.Context, env envport.EnvID, specs []*agentspec.Spec) error {
	return h.adapter.PrepareEnv(ctx, env, specs)
}

func (h *claudeCodeHarness) InstallSkills(ctx context.Context, env envport.EnvID, names []string) error {
	return h.adapter.InstallSkills(ctx, env, names)
}

// Models: Claude Code routes Anthropic models; ids surface as
// `anthropic/<model>` (the flow format), from the models.dev catalog. The
// bridge resolves the value against the models it offers: one it does not
// offer fails the session's start, naming those it does.
func (h *claudeCodeHarness) Models(ctx context.Context) ([]ModelInfo, error) {
	catalog, err := fetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := catalog["anthropic"]
	if !ok {
		return nil, fmt.Errorf("model catalog has no anthropic provider")
	}
	var out []ModelInfo
	for modelID, m := range p.Models {
		name := m.Name
		if name == "" {
			name = modelID
		}
		out = append(out, ModelInfo{ID: "anthropic/" + modelID, Name: p.Name + " · " + name})
	}
	sortModels(out)
	return out, nil
}

// CheckAuth (kinds.AuthChecker): Claude Code serves one provider, so a flow
// with any model on it probes what CheckCredentials does.
func (h *claudeCodeHarness) CheckAuth(ctx context.Context, models []string) AuthResult {
	if len(models) == 0 {
		return AuthResult{}
	}
	return h.CheckCredentials(ctx)
}

// CheckCredentials (kinds.AuthChecker): the API key is probed against
// Anthropic's model list. A login needs no probe: the keeper just proved its
// refresh token.
func (h *claudeCodeHarness) CheckCredentials(ctx context.Context) AuthResult {
	var out AuthResult
	for _, msg := range sortedProblems(h.creds.Problems) {
		out.Rejected = append(out.Rejected, "claude-code: "+msg)
	}
	if h.apiKey == "" {
		return out
	}
	pr, _ := providerAuthProbe("anthropic", catalogProvider{}, h.apiKey, "")
	rejected, reason, err := pr.run(ctx, h.apiKey)
	switch {
	case err != nil:
		out.Unverified = append(out.Unverified, "claude-code Anthropic key: "+err.Error())
	case rejected:
		out.Rejected = append(out.Rejected, "claude-code Anthropic key: "+reason+" — fix it under System → Model providers")
	}
	return out
}

var _ HarnessKind = ClaudeCode{}
