package claudecode

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/adapters/acpagent"
	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
)

func envWith(t *testing.T) (*fake.EnvProvisioner, envport.EnvID) {
	t.Helper()
	envp := fake.NewEnvProvisioner()
	id, err := envp.Provision(context.Background(), envport.EnvSpec{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	return envp, id
}

// Claude Code has no session-level agents over ACP: the role rides in front
// of the first prompt, ask_human rides every session, and every stage runs
// without permission prompts — also as root.
func TestProfile(t *testing.T) {
	envp, id := envWith(t)
	a := New(envp, Config{})
	if err := a.PrepareEnv(context.Background(), id, []*agentspec.Spec{{Name: "planner", Prompt: "Plan carefully.\n"}}); err != nil {
		t.Fatal(err)
	}
	p := a.Profile()
	if p.Harness != "claude-code" || !strings.HasPrefix(p.Command, "env ") || !strings.HasSuffix(p.Command, " claude-agent-acp") ||
		!strings.Contains(p.Command, " IS_SANDBOX=1 ") || !strings.Contains(p.Command, " MCP_TOOL_TIMEOUT=86400000 ") {
		t.Errorf("command = %q", p.Command)
	}
	for _, agent := range []string{"planner", "builder", ""} {
		if m := p.Mode(agentport.AgentSpec{Agent: agent}); m != "bypassPermissions" {
			t.Errorf("mode of %q = %q", agent, m)
		}
	}
	if p.ReadOnlyBy != acpagent.ReadOnlyByMode || p.ReadOnlyMode != "plan" {
		t.Errorf("read-only = %s %q", p.ReadOnlyBy, p.ReadOnlyMode)
	}
	if len(p.MCPServers) != 1 || p.MCPServers[0].Name != "ask_human" ||
		strings.Join(p.MCPServers[0].Command, " ") != "/opt/apothecary/shim ask-human" ||
		p.MCPServers[0].Env["APOTHECARY_AGENT"] != "1" {
		t.Errorf("mcp servers = %+v", p.MCPServers)
	}
	pre, err := p.Preamble(context.Background(), id, agentport.AgentSpec{Agent: "planner"})
	if err != nil || !strings.Contains(pre, "Plan carefully.") {
		t.Errorf("preamble = %q, %v", pre, err)
	}
	if _, err := p.Preamble(context.Background(), id, agentport.AgentSpec{Agent: "ghost"}); err == nil {
		t.Error("an unknown agent must fail, not run role-less")
	}
	if pre, err := p.Preamble(context.Background(), id, agentport.AgentSpec{}); err != nil || pre != "" {
		t.Errorf("no agent = %q, %v", pre, err)
	}
}

func TestPrepareEnvWritesRolesAndSettings(t *testing.T) {
	envp, id := envWith(t)
	ctx := context.Background()
	a := New(envp, Config{})
	// No spec at all still leaves the terminal's claude its settings.
	if err := a.PrepareEnv(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := envp.ReadFile(ctx, id, "/home/apothecary/.claude/settings.json")
	if err != nil || !strings.Contains(string(cfg), `"DISABLE_AUTOUPDATER": "1"`) ||
		!strings.Contains(string(cfg), `"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"`) {
		t.Errorf("settings.json = %q, %v", cfg, err)
	}

	specs := []*agentspec.Spec{
		{Name: "builder", AllowBash: true, Prompt: "Build it.\n"},
		{Name: "planner", ReadOnly: true, Prompt: "Plan carefully.\n"},
	}
	if err := a.PrepareEnv(ctx, id, specs); err != nil {
		t.Fatal(err)
	}
	planner, err := envp.ReadFile(ctx, id, "/apothecary/agents/planner.role")
	if err != nil {
		t.Fatal(err)
	}
	// read_only/allow_bash are instruction-level here (the codex/omp residual).
	for _, want := range []string{"# ROLE", "Plan carefully.", "READ-ONLY", "Do not run shell commands", "# ASKING THE HUMAN"} {
		if !strings.Contains(string(planner), want) {
			t.Errorf("planner.role missing %q:\n%s", want, planner)
		}
	}
	builder, _ := envp.ReadFile(ctx, id, "/apothecary/agents/builder.role")
	if strings.Contains(string(builder), "READ-ONLY") || strings.Contains(string(builder), "Do not run shell commands") {
		t.Errorf("builder.role = %s", builder)
	}
}

// Claude Code reads $HOME/.claude. A repo-authored devcontainer runs the
// REPO's user, so a hardcoded /home/apothecary would leave it with no
// settings and no login: everything follows the real $HOME.
func TestConfigFollowsWorkspaceHome(t *testing.T) {
	envp, id := envWith(t)
	envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "$HOME") {
			return envport.ExecResult{Output: "/home/vscode\n"}, nil
		}
		return envport.ExecResult{}, nil
	}
	a := New(envp, Config{})
	ctx := context.Background()
	if err := a.PrepareEnv(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.SeedCredentials(ctx, id, []byte(`{"claudeAiOauth":{}}`)); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/home/vscode/.claude/settings.json", "/home/vscode/.claude/.credentials.json"} {
		if _, err := envp.ReadFile(ctx, id, p); err != nil {
			t.Errorf("not written to the workspace's real home: %s (%v)", p, err)
		}
	}
	if got := a.AuthFilePath(ctx, id); got != "/home/vscode/.claude/.credentials.json" {
		t.Errorf("auth file = %s", got)
	}
}

// Nothing is written for no login (its mode: TestSeedCredentialsIsPrivate).
func TestSeedCredentials(t *testing.T) {
	envp, id := envWith(t)
	ctx := context.Background()
	a := New(envp, Config{})
	p := a.AuthFilePath(ctx, id)
	if err := a.SeedCredentials(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := envp.Env(id).Files[p]; ok {
		t.Error("an empty credential was written")
	}
	if err := a.SeedCredentials(ctx, id, []byte(`{"t":1}`)); err != nil {
		t.Fatal(err)
	}
	if string(envp.Env(id).Files[p]) != `{"t":1}` {
		t.Errorf("files = %v", envp.Env(id).Files)
	}
}

// Skills land in Claude Code's user-level skills directory.
func TestInstallSkillsTargetsClaudeSkillsDir(t *testing.T) {
	envp, id := envWith(t)
	if err := New(envp, Config{}).InstallSkills(context.Background(), id, []string{"pdf"}); err != nil {
		t.Fatal(err)
	}
	calls := envp.Execs()
	last := calls[len(calls)-1].Cmd.Script
	if !strings.Contains(last, "cp -R '/apothecary/skills/pdf' '/home/apothecary/.claude/skills/pdf'") {
		t.Errorf("script = %q", last)
	}
}
