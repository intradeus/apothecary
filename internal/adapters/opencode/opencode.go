// Package opencode generates opencode's workspace config (agents, the
// ask_human MCP server, permissions, auth) and says how to run it: opencode
// is driven as a long-lived ACP agent (`opencode acp`, verified against
// opencode 1.17.8) through the workspace sidecar — see internal/adapters/
// acpagent. Its ACP session modes ARE its agents (the primary-mode agent
// markdown written here), so a stage selects its agent as the session mode.
//
// There is no --dangerously-skip-permissions: permissions are granted via
// generated config (and the sidecar auto-approves any stray request). There
// is no turn cap; max_turns is not enforceable per-invocation (budgets still
// bound the run engine-side).
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"apothecary/internal/adapters/acpagent"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/workspace/wshome"
)

// Config is reserved for adapter-level knobs; auth material arrives per
// call via SeedAuthJSON (composed by the opencode kind, DESIGN2 §4).
type Config struct{}

type Adapter struct {
	envp envport.EnvProvisioner
	cfg  Config
}

func New(envp envport.EnvProvisioner, cfg Config) *Adapter {
	return &Adapter{envp: envp, cfg: cfg}
}

// mcpTimeout is opencode's per-call MCP timeout. ask_human blocks until the
// human answers, which is fine: opencode RESETS this timer on every MCP
// progress notification, and the shim sends one every few seconds while it
// waits — so this only fires if the shim itself hangs.
const mcpTimeout = 2 * time.Minute

const (
	// defaultHome is a last-resort fallback: a repo-authored devcontainer
	// runs the REPO's user, so the real home is probed
	// per env (see configDir/authFile) and this is only the fallback.
	defaultHome = "/home/apothecary"
)

// configDir/authFile locate opencode's config in the home of the user it
// actually runs as — writing them under a hardcoded home makes opencode
// start with no provider and no agents.
func (a *Adapter) configDir(ctx context.Context, env envport.EnvID) string {
	return path.Join(wshome.Resolve(ctx, a.envp, env, defaultHome), ".config", "opencode")
}

func (a *Adapter) authFile(ctx context.Context, env envport.EnvID) string {
	return path.Join(wshome.Resolve(ctx, a.envp, env, defaultHome), ".local", "share", "opencode", "auth.json")
}

// InstallSkills copies the run's skills into ~/.config/opencode/skills,
// opencode's global skills directory. opencode also scans ~/.agents/skills
// (codex's), so with both harnesses in a project it sees each skill twice
// — same name, same bytes: it warns and keeps one.
func (a *Adapter) InstallSkills(ctx context.Context, env envport.EnvID, names []string) error {
	return acpagent.InstallSkills(ctx, a.envp, env, path.Join(a.configDir(ctx, env), "skills"), names)
}

// PrepareEnv generates harness-native config from the project's agent
// library specs (§5.2, DESIGN3 §1): one agent markdown per spec plus the
// ask_human MCP registration (written even for zero specs). Config
// lands in the workspace user's config dir — outside the repo, so nothing
// needs git-excluding, regenerated every provision.
func (a *Adapter) PrepareEnv(ctx context.Context, env envport.EnvID, specs []*agentspec.Spec) error {
	cfgDir := a.configDir(ctx, env)
	servers := mcpNames(specs)
	for _, s := range specs {
		md := agentMarkdown(s, servers)
		p := path.Join(cfgDir, "agent", s.Name+".md")
		if err := a.envp.WriteFile(ctx, env, p, []byte(md), 0o644); err != nil {
			return fmt.Errorf("write agent config %s: %w", s.Name, err)
		}
	}

	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		// Non-interactive runs must never hang on a permission prompt;
		// restriction happens per-agent via tools:false.
		"permission": map[string]any{"edit": "allow", "bash": "allow", "webfetch": "allow"},
		"mcp": map[string]any{
			"ask_human": map[string]any{
				"type":    "local",
				"command": []string{"/opt/apothecary/shim", "ask-human"},
				"enabled": true,
				// Reset by every progress notification (see mcpTimeout).
				"timeout": mcpTimeout.Milliseconds(),
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := a.envp.WriteFile(ctx, env, path.Join(cfgDir, "opencode.json"), data, 0o644); err != nil {
		return fmt.Errorf("write opencode.json: %w", err)
	}

	return nil
}

// AuthFilePath is where SeedAuthJSON puts the credential, exposed so the
// engine can remove it when a run ends (the workspace outlives the run for
// inspection; the model keys should not).
func (a *Adapter) AuthFilePath(ctx context.Context, env envport.EnvID) string {
	return a.authFile(ctx, env)
}

// SeedAuthJSON writes opencode's auth.json (DESIGN2 §4 Harness.SeedAuth,
// bootstrap step 2). The caller (the opencode kind) composes the content
// from the harness's stored auth_json secret and provider integrations;
// empty data is a no-op.
func (a *Adapter) SeedAuthJSON(ctx context.Context, env envport.EnvID, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if err := a.envp.WriteFile(ctx, env, a.authFile(ctx, env), data, 0o600); err != nil {
		return fmt.Errorf("seed auth.json: %w", err)
	}
	return nil
}

// mcpNames is every MCP server some agent of the flow lists (`mcp:`).
func mcpNames(specs []*agentspec.Spec) []string {
	var out []string
	for _, s := range specs {
		for _, m := range s.MCP {
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	slices.Sort(out)
	return out
}

// agentMarkdown renders one opencode agent definition (frontmatter + prompt).
//
// A stage's MCP servers arrive on its ACP session (acpagent), but opencode
// keeps a session's servers for the whole process — every later session sees
// their tools until another session registers the same name. So each agent
// turns off, by tool prefix (opencode names an MCP tool <server>_<tool>),
// the servers of the flow it does not list itself.
func agentMarkdown(s *agentspec.Spec, servers []string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "description: %q\n", s.Description)
	b.WriteString("mode: primary\n")
	b.WriteString("tools:\n")
	fmt.Fprintf(&b, "  bash: %v\n", s.AllowBash)
	if s.Name == agentspec.AskAgent {
		// The Ask pane answers; a question it raised would land on the
		// stage's run as if the stage had asked it.
		b.WriteString("  \"ask_human_*\": false\n")
	}
	for _, m := range servers {
		if !slices.Contains(s.MCP, m) {
			fmt.Fprintf(&b, "  %q: false\n", m+"_*")
		}
	}
	if s.ReadOnly {
		b.WriteString(readOnlyPermission(s.OutputPaths))
	}
	b.WriteString("---\n")
	b.WriteString(s.Prompt)
	if s.Name != agentspec.AskAgent {
		// opencode namespaces MCP tools <server>_<tool>, so the agent is
		// told the exact name it will see (FLOWLANG §9).
		b.WriteString("\n")
		b.WriteString(acpagent.AskHumanRules(askHumanListedAs))
	}
	return b.String()
}

// readOnlyPermission is a read-only agent's file permission: no edit but of
// its own outputs — §5.3's read-only agents (planner, reviewer, redactor)
// must still deliver their files, and the Ask pane's, with none, edits
// nothing. opencode has ONE file permission, `edit`, for its edit, write and
// patch tools alike (`tools: {edit: false, write: true}` re-allowed every
// edit), so the rule is per path, matched against the file's path relative
// to the repository — the form the engine's output contract names it in:
// deny everything, then allow each output, a files output's whole directory.
// The last matching rule wins, so the allows follow the deny.
func readOnlyPermission(outputs []string) string {
	if len(outputs) == 0 {
		return "permission:\n  edit: deny\n"
	}
	var b strings.Builder
	b.WriteString("permission:\n  edit:\n    \"*\": deny\n")
	for _, p := range outputs {
		if strings.HasSuffix(p, "/") {
			p += "*"
		}
		fmt.Fprintf(&b, "    %q: allow\n", p)
	}
	return b.String()
}

// askHumanListedAs is the name opencode lists the shim's ask_human tool
// under: the MCP server's name, an underscore, the tool's.
const askHumanListedAs = "ask_human_ask_human"

// --- running it ----------------------------------------------------------------

// Profile is how the ACP runner starts and addresses opencode.
func Profile() acpagent.Profile {
	return acpagent.Profile{
		Harness: "opencode",
		// Logs on stderr (kept by the sidecar): opencode reports a failed
		// model call ONLY in its log and ends the turn as if it succeeded,
		// so the sidecar reads them to tell a silent failure from a turn.
		Command: "opencode acp --print-logs --log-level WARN",
		Mode:    func(spec agentport.AgentSpec) string { return spec.Agent },
		// A read-only session runs a read-only agent definition (its tools
		// denied above): the engine prepares one for the Ask pane.
		ReadOnlyBy: acpagent.ReadOnlyByAgent,
	}
}
