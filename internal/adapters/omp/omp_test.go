package omp

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

// omp reads no MCP config in ACP mode and has no session-level agents: the
// role rides in front of the first prompt, ask_human rides every session,
// and the process runs without approval prompts.
func TestProfile(t *testing.T) {
	envp, id := envWith(t)
	a := New(envp, Config{})
	if err := a.PrepareEnv(context.Background(), id, []*agentspec.Spec{{Name: "planner", Prompt: "Plan carefully.\n"}}); err != nil {
		t.Fatal(err)
	}
	p := a.Profile()
	if !strings.HasPrefix(p.Command, "env ") || !strings.Contains(p.Command, "omp acp --yolo") ||
		!strings.Contains(p.Command, "OMP_MCP_TIMEOUT_MS=0") || p.Mode != nil {
		t.Errorf("profile = %+v", p)
	}
	if len(p.MCPServers) != 1 || p.MCPServers[0].Name != "ask_human" ||
		strings.Join(p.MCPServers[0].Command, " ") != "/opt/apothecary/shim ask-human" ||
		p.MCPServers[0].Env["APOTHECARY_AGENT"] != "1" {
		t.Errorf("mcp servers = %+v", p.MCPServers)
	}
	// A read-only session is a second process that loads no mutating tool.
	if p.ReadOnlyBy != acpagent.ReadOnlyByProcess || !strings.Contains(p.ReadOnlyCommand, "omp acp --tools=") ||
		!strings.Contains(p.ReadOnlyCommand, "--approval-mode=always-ask") || strings.Contains(p.ReadOnlyCommand, "--yolo") {
		t.Errorf("read-only profile = %q (%s)", p.ReadOnlyCommand, p.ReadOnlyBy)
	}
	for _, tool := range []string{"bash", "edit", "write", "python", "task"} {
		if strings.Contains(p.ReadOnlyCommand, tool) {
			t.Errorf("read-only command loads %s: %q", tool, p.ReadOnlyCommand)
		}
	}
	pre, err := p.Preamble(context.Background(), id, agentport.AgentSpec{Agent: "planner"})
	if err != nil || !strings.Contains(pre, "Plan carefully.") {
		t.Errorf("preamble = %q, %v", pre, err)
	}
	if _, err := p.Preamble(context.Background(), id, agentport.AgentSpec{Agent: "ghost"}); err == nil {
		t.Error("an unknown agent must fail, not run role-less")
	}
}

// config.yml lands in the workspace user's real home, not a hardcoded one.
func TestPrepareEnvWritesConfigInWorkspaceHome(t *testing.T) {
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
	cfg, err := envp.ReadFile(ctx, id, "/home/vscode/.omp/agent/config.yml")
	if err != nil || !strings.Contains(string(cfg), "approvalMode: yolo") || !strings.Contains(string(cfg), "checkUpdate: false") {
		t.Errorf("config.yml = %q, %v", cfg, err)
	}
	if got := a.AuthFilePath(ctx, id); got != "/home/vscode/.omp/agent/agent.db" {
		t.Errorf("auth file = %s", got)
	}
}

// Logins are staged as files and imported by omp itself; a login omp skips
// fails the seed with omp's reason.

// Skills land in omp's native user-level skills directory.
func TestInstallSkillsTargetsAgentSkillsDir(t *testing.T) {
	envp := fake.NewEnvProvisioner()
	id, _ := envp.Provision(context.Background(), envport.EnvSpec{RunID: "r"})
	a := New(envp, Config{})
	if err := a.InstallSkills(context.Background(), id, []string{"pdf"}); err != nil {
		t.Fatal(err)
	}
	calls := envp.Execs()
	last := calls[len(calls)-1].Cmd.Script
	if !strings.Contains(last, "cp -R '/apothecary/skills/pdf' '/home/apothecary/.omp/agent/skills/pdf'") {
		t.Errorf("script = %q", last)
	}
}
