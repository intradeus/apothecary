package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/adapters/acpagent"
	codexadapter "apothecary/internal/adapters/codex"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/envport"
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

// CodexEditorExtension is OpenAI's Codex extension. It runs its own bundled
// codex, which reads ~/.codex/auth.json — the file SeedAuth writes — so a
// ChatGPT login reaches the editor as it reaches the agent (and leaves with
// it when the run ends). An API key does not: it rides provider.env, which
// only the agent's process sources.
const CodexEditorExtension = "openai.chatgpt"

// Codex is the harness/codex kind (DESIGN2 §13). Its credentials are an
// OpenAI API key (rides OPENAI_API_KEY via provider.env) and/or a ChatGPT
// login (rendered into $CODEX_HOME/auth.json, refresh token withheld).
type Codex struct{}

func (Codex) Descriptor() Descriptor {
	return Descriptor{
		Port: "harness", Kind: "codex", Label: "codex",
		Help:   "Runs codex " + CodexVersion + " through codex-acp " + CodexACPVersion + ", the versions pinned by this apothecary release.",
		Fields: []Field{credentialsField(providers.Codex)},
	}
}

func (Codex) New(cfg Config, envp envport.EnvProvisioner) (Harness, error) {
	adapter := codexadapter.New(envp, codexadapter.Config{})
	h := &codexHarness{
		Runner:  acpagent.New(envp, adapter.Profile()),
		adapter: adapter,
		creds:   cfg.Credentials,
	}
	for _, b := range bind(providers.Codex, cfg.Credentials.Snapshot) {
		switch b.Kind {
		case providers.KindAPIKey:
			h.apiKey = b.Key
		case providers.KindOAuth:
			bb := b
			h.login = &bb
		}
	}
	return h, nil
}

// codexHarness runs as a long-lived ACP agent (codex-acp) in the workspace
// sidecar; the embedded runner is the AgentRunner, the adapter its config.
type codexHarness struct {
	*acpagent.Runner
	adapter *codexadapter.Adapter
	creds   Credentials
	apiKey  string
	login   *bound // the ChatGPT login, as of the snapshot
}

func (h *codexHarness) Test(_ context.Context) error {
	if h.apiKey == "" && h.login == nil {
		return fmt.Errorf("codex: no model provider credentials — add an OpenAI API key or a ChatGPT login under System → Model providers and pick it here")
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

// EnvVars: API-key auth rides the standard env var through provider.env.
func (h *codexHarness) EnvVars() map[string]string {
	if h.apiKey == "" {
		return nil
	}
	return map[string]string{"OPENAI_API_KEY": h.apiKey}
}

// EditorExtensions (kinds.EditorExtender): the Codex extension.
func (h *codexHarness) EditorExtensions() []string { return []string{CodexEditorExtension} }

// AuthFiles (kinds.AuthLocator): auth.json, removed when the run ends. The
// API key needs no entry — it lives in provider.env, which the engine owns.
func (h *codexHarness) AuthFiles(ctx context.Context, env envport.EnvID) []string {
	return []string{h.adapter.AuthFilePath(ctx, env)}
}

// SecretValues (kinds.SecretHolder): the login's tokens. The API key needs
// no mention — it rides EnvVars, which the redactor already covers.
func (h *codexHarness) SecretValues() []string {
	if h.login == nil {
		return nil
	}
	return h.login.SecretValues()
}

// SeedAuth renders auth.json from the current ChatGPT login. codex re-reads
// the file before any refresh of its own and uses a changed one without
// calling the token endpoint (codex-rs auth manager, reload before refresh),
// so the keeper rewriting it ahead of codex's five-minute window means codex
// never refreshes inside the workspace — and with no refresh token, it
// could not.
func (h *codexHarness) SeedAuth(ctx context.Context, env envport.EnvID, register func(string)) error {
	if h.login == nil {
		return nil
	}
	creds, err := h.creds.current(ctx)
	if err != nil {
		return fmt.Errorf("codex: %w", err)
	}
	for _, b := range bind(providers.Codex, creds) {
		if b.Kind != providers.KindOAuth {
			continue
		}
		doc, err := renderCodexAuth(b, time.Now())
		if err != nil {
			return err
		}
		for _, s := range b.SecretValues() {
			register(s)
		}
		return h.adapter.SeedAuthJSON(ctx, env, doc)
	}
	return nil
}

// renderCodexAuth builds codex's auth.json (AuthDotJson): the login's tokens
// with refresh_token EMPTY — TokenData requires the string, and an empty one
// parses — the id_token codex parses its plan and account from, and
// last_refresh stamped now so codex's own staleness rule never fires.
func renderCodexAuth(b bound, now time.Time) ([]byte, error) {
	idToken := b.Get(providers.ExtraIDToken)
	if idToken == "" {
		return nil, fmt.Errorf("codex: the ChatGPT login %q carries no id_token, which codex needs — sign in again", b.Label())
	}
	tokens := map[string]any{
		"id_token": idToken, "access_token": b.Access, "refresh_token": "",
	}
	if a := b.Get(providers.ExtraAccountID); a != "" {
		tokens["account_id"] = a
	}
	return json.Marshal(map[string]any{
		"OPENAI_API_KEY": nil,
		"tokens":         tokens,
		"last_refresh":   now.UTC().Format(time.RFC3339),
	})
}

func (h *codexHarness) Prepare(ctx context.Context, env envport.EnvID, specs []*agentspec.Spec) error {
	return h.adapter.PrepareEnv(ctx, env, specs)
}

func (h *codexHarness) InstallSkills(ctx context.Context, env envport.EnvID, names []string) error {
	return h.adapter.InstallSkills(ctx, env, names)
}

// chatgptModelsURL is the account-scoped model catalog codex itself reads
// (codex-rs codex-api endpoint/models.rs: GET {base}/models?client_version=),
// a variable for tests.
var chatgptModelsURL = "https://chatgpt.com/backend-api/codex/models"

// chatgptModels asks the account which models its plan offers — the
// credential's verdict and the plan's model list in one zero-token call.
func chatgptModels(ctx context.Context, access, account string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chatgptModelsURL+"?client_version="+url.QueryEscape(CodexVersion), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("originator", "codex_cli_rs")
	if account != "" {
		req.Header.Set("chatgpt-account-id", account)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &loginRefused{status: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ChatGPT models: %d", resp.StatusCode)
	}
	var body struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("ChatGPT models: %w", err)
	}
	var out []string
	for _, m := range body.Models {
		if m.Slug != "" && m.Visibility != "hide" && m.Visibility != "none" {
			out = append(out, m.Slug)
		}
	}
	sort.Strings(out)
	return out, nil
}

type loginRefused struct{ status int }

func (e *loginRefused) Error() string {
	return fmt.Sprintf("ChatGPT refused the login (%d) — sign in again", e.status)
}

// Models: codex routes OpenAI models; ids surface as `openai/<model>` (the
// flow format) and the adapter strips the prefix at invocation. A ChatGPT
// login lists what its plan offers; an API key, the catalog's OpenAI models.
func (h *codexHarness) Models(ctx context.Context) ([]ModelInfo, error) {
	if h.login != nil {
		if slugs, err := chatgptModels(ctx, h.login.Access, h.login.Get(providers.ExtraAccountID)); err == nil && len(slugs) > 0 {
			out := make([]ModelInfo, 0, len(slugs))
			for _, s := range slugs {
				out = append(out, ModelInfo{ID: "openai/" + s, Name: "ChatGPT · " + s})
			}
			return out, nil
		}
	}
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

// CheckAuth (kinds.AuthChecker): the ChatGPT login is asked for its plan's
// models — a refused login is a verdict, and so is a flow model the plan
// does not offer; the API key is probed against OpenAI's model list.
func (h *codexHarness) CheckAuth(ctx context.Context, models []string) AuthResult {
	if len(models) == 0 {
		return AuthResult{}
	}
	out := h.CheckCredentials(ctx)
	if h.login == nil || h.apiKey != "" {
		return out // an API key reaches the whole OpenAI catalog
	}
	slugs, err := chatgptModels(ctx, h.login.Access, h.login.Get(providers.ExtraAccountID))
	if err != nil {
		return out // CheckCredentials already said why
	}
	offered := map[string]bool{}
	for _, s := range slugs {
		offered[s] = true
	}
	for _, m := range models {
		if _, slug, ok := strings.Cut(m, "/"); ok && !offered[slug] {
			out.Rejected = append(out.Rejected, fmt.Sprintf("codex model %s: not offered by the ChatGPT plan of %s (offered: %s) — pick another model in the flow",
				m, h.login.Label(), strings.Join(slugs, ", ")))
		}
	}
	return out
}

// CheckCredentials (kinds.AuthChecker): every credential codex holds.
func (h *codexHarness) CheckCredentials(ctx context.Context) AuthResult {
	var out AuthResult
	for _, msg := range sortedProblems(h.creds.Problems) {
		out.Rejected = append(out.Rejected, "codex: "+msg)
	}
	if h.login != nil {
		if _, err := chatgptModels(ctx, h.login.Access, h.login.Get(providers.ExtraAccountID)); err != nil {
			if _, refused := err.(*loginRefused); refused && h.apiKey == "" {
				out.Rejected = append(out.Rejected, "codex ChatGPT login: "+err.Error())
			} else {
				out.Unverified = append(out.Unverified, "codex ChatGPT login: "+err.Error())
			}
		}
	}
	if h.apiKey == "" {
		return out
	}
	rejected, reason, err := openaiModelsProbe(h.apiKey).run(ctx, h.apiKey)
	switch {
	case err != nil:
		out.Unverified = append(out.Unverified, "codex OpenAI key: "+err.Error())
	case rejected && h.login != nil:
		out.Unverified = append(out.Unverified, "codex OpenAI key: "+reason+" (the ChatGPT login may still work)")
	case rejected:
		out.Rejected = append(out.Rejected, "codex OpenAI key: "+reason+" — fix it under System → Model providers")
	}
	return out
}

var _ HarnessKind = Codex{}
