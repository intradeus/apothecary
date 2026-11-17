// Package claudecode generates Claude Code's workspace config (roles,
// settings, auth) and says how to run it: Claude Code is driven as a
// long-lived ACP agent through its ACP bridge (`claude-agent-acp`,
// @agentclientprotocol/claude-agent-acp), hosted by the workspace sidecar —
// see internal/adapters/acpagent. The bridge runs the `claude` CLI the tools
// bundle ships beside it (CLAUDE_CODE_EXECUTABLE, set by its wrapper).
//
// Through ACP a session cannot start as a Claude Code named agent, so a
// stage's role prompt is prepended to the session's first prompt, as for
// codex and omp (acpagent.RolePreamble). ask_human rides every session
// (Profile.MCPServers), which the bridge forwards whatever settings sources
// it loads. Claude Code reports a cumulative USD cost per session
// (usage_update), so budgets work as for opencode.
package claudecode

import (
	"context"
	"fmt"
	"path"

	"apothecary/internal/adapters/acpagent"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/workspace/wshome"
)

type Config struct{}

type Adapter struct {
	envp envport.EnvProvisioner
	cfg  Config
}

func New(envp envport.EnvProvisioner, cfg Config) *Adapter {
	return &Adapter{envp: envp, cfg: cfg}
}

const (
	// defaultHome is a last-resort fallback: a repo-authored devcontainer
	// runs the REPO's user, so the real home is probed
	// per env and this is only the fallback.
	defaultHome = "/home/apothecary"
)

// claudeHome locates ~/.claude in the home of the user Claude Code actually
// runs as — under a hardcoded home the settings and the login are silently
// invisible to it.
func (a *Adapter) claudeHome(ctx context.Context, env envport.EnvID) string {
	return path.Join(wshome.Resolve(ctx, a.envp, env, defaultHome), ".claude")
}

// settings is Claude Code's user settings.json, for a human running `claude`
// in the workspace terminal: no auto-update (the workspace is rebuilt from a
// pinned version, never updated in place) and no nonessential traffic (the
// egress gateway allows only the credentials' domains). The engine's own
// process gets the same through its command line (Profile).
const settings = `{
  "env": {
    "DISABLE_AUTOUPDATER": "1",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"
  }
}
`

// InstallSkills copies the run's skills into ~/.claude/skills, Claude Code's
// user-level skills directory (its project-level one is the repo's own
// .claude/skills, which stays the repo's).
func (a *Adapter) InstallSkills(ctx context.Context, env envport.EnvID, names []string) error {
	return acpagent.InstallSkills(ctx, a.envp, env, path.Join(a.claudeHome(ctx, env), "skills"), names)
}

// PrepareEnv generates Claude Code's config: settings.json, plus one role
// file per agent spec (delivered in-band, see the package doc). A flow's
// mcp_servers and ask_human are not here: they ride the session (acpagent).
func (a *Adapter) PrepareEnv(ctx context.Context, env envport.EnvID, specs []*agentspec.Spec) error {
	if err := acpagent.WriteRoles(ctx, a.envp, env, specs); err != nil {
		return err
	}
	if err := a.envp.WriteFile(ctx, env, path.Join(a.claudeHome(ctx, env), "settings.json"), []byte(settings), 0o644); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}
	return nil
}

// AuthFilePath is where SeedCredentials puts the login, exposed so the
// engine can remove it when a run ends (the workspace outlives the run for
// inspection; the login's tokens should not).
func (a *Adapter) AuthFilePath(ctx context.Context, env envport.EnvID) string {
	return path.Join(a.claudeHome(ctx, env), ".credentials.json")
}

// SeedCredentials writes Claude Code's .credentials.json (a Claude login);
// API-key auth rides ANTHROPIC_API_KEY via provider.env instead.
func (a *Adapter) SeedCredentials(ctx context.Context, env envport.EnvID, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if err := a.envp.WriteFile(ctx, env, a.AuthFilePath(ctx, env), data, 0o600); err != nil {
		return fmt.Errorf("seed .credentials.json: %w", err)
	}
	return nil
}

// --- running it ----------------------------------------------------------------

// Session modes of claude-agent-acp: bypassPermissions runs every tool
// without asking, Claude Code's posture for an externally sandboxed
// environment — exactly the factory workspace (container + egress gateway);
// plan is its read-only planning mode (the Ask pane's session).
const (
	stageMode    = "bypassPermissions"
	readOnlyMode = "plan"
)

// mcpToolTimeoutMS bounds one MCP tool call (MCP_TOOL_TIMEOUT, in ms).
// ask_human blocks until the human answers (the engine parks the run if that
// takes too long, by cancelling the turn), so it must not cut that short: a
// day, as codex's tool_timeout_sec.
const mcpToolTimeoutMS = "86400000"

// Profile is how the ACP runner starts and addresses Claude Code.
//
// IS_SANDBOX=1: the bridge refuses bypassPermissions to a process running as
// root (a repo devcontainer's user may be) unless it is set, and silently
// falls back to asking — which the sidecar's auto-approve would still answer,
// noisily. DISABLE_AUTOUPDATER and CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC:
// as settings.json, for this process whatever settings sources it loads.
// ENABLE_TOOL_SEARCH=false: otherwise Claude Code defers MCP tools behind
// its ToolSearch tool, unloaded — and a deferred ask_human fails when called
// by the name the role gives it (TestClaudeCodeConformance).
func (a *Adapter) Profile() acpagent.Profile {
	return acpagent.Profile{
		Harness: "claude-code",
		Command: "env IS_SANDBOX=1 DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 " +
			"ENABLE_TOOL_SEARCH=false MCP_TOOL_TIMEOUT=" + mcpToolTimeoutMS + " claude-agent-acp",
		Mode:         func(agentport.AgentSpec) string { return stageMode },
		Preamble:     acpagent.RolePreamble(a.envp, "claude-code"),
		MCPServers:   []agentport.MCPServer{acpagent.AskHumanServer()},
		ReadOnlyBy:   acpagent.ReadOnlyByMode,
		ReadOnlyMode: readOnlyMode,
	}
}
