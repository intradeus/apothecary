package codex

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
)

func envWith(t *testing.T) (*fake.EnvProvisioner, ports.EnvID) {
	t.Helper()
	envp := fake.NewEnvProvisioner()
	id, err := envp.Provision(context.Background(), ports.EnvSpec{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	return envp, id
}

// codex has no named agents: the role rides in front of the first prompt,
// and the session runs without approvals or an inner sandbox.
func TestProfile(t *testing.T) {
	envp, id := envWith(t)
	a := New(envp, Config{})
	if err := a.PrepareEnv(context.Background(), id, []*agentspec.Spec{{Name: "planner", Prompt: "Plan carefully.\n"}}); err != nil {
		t.Fatal(err)
	}
	p := a.Profile()
	if p.Command != "codex-acp" || p.Mode(ports.AgentSpec{Agent: "planner"}) != "agent-full-access" {
		t.Errorf("profile = %+v", p)
	}
	pre, err := p.Preamble(context.Background(), id, ports.AgentSpec{Agent: "planner"})
	if err != nil || !strings.Contains(pre, "Plan carefully.") {
		t.Errorf("preamble = %q, %v", pre, err)
	}
	if _, err := p.Preamble(context.Background(), id, ports.AgentSpec{Agent: "ghost"}); err == nil {
		t.Error("an unknown agent must fail, not run role-less")
	}
}

func TestPrepareEnvWritesRolesAndConfig(t *testing.T) {
	envp, id := envWith(t)
	// Specs arrive from the project's agent library (DESIGN3 §1), not the
	// repo — no exec involved.
	specs := []*agentspec.Spec{
		{Name: "builder", Description: "builds", ReadOnly: false, AllowBash: true, Prompt: "Build it.\n"},
		{Name: "planner", Description: "plans", ReadOnly: true, AllowBash: false, Prompt: "Plan carefully.\n"},
	}
	a := New(envp, Config{})
	if err := a.PrepareEnv(context.Background(), id, specs); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	planner, err := envp.ReadFile(ctx, id, "/apothecary/agents/planner.role")
	if err != nil {
		t.Fatal(err)
	}
	// read_only/allow_bash are instruction-level for codex (§13 residual).
	for _, want := range []string{"# ROLE", "Plan carefully.", "READ-ONLY", "Do not run shell commands", "# ASKING THE HUMAN", "ask_human"} {
		if !strings.Contains(string(planner), want) {
			t.Errorf("planner.role missing %q:\n%s", want, planner)
		}
	}
	builder, _ := envp.ReadFile(ctx, id, "/apothecary/agents/builder.role")
	if strings.Contains(string(builder), "READ-ONLY") {
		t.Error("builder.role must not be read-only")
	}
	cfg, err := envp.ReadFile(ctx, id, "/home/apothecary/.codex/config.toml")
	if err != nil || !strings.Contains(string(cfg), "[mcp_servers.ask_human]") ||
		!strings.Contains(string(cfg), `command = "/opt/apothecary/shim"`) ||
		!strings.Contains(string(cfg), `args = ["ask-human"]`) ||
		!strings.Contains(string(cfg), "tool_timeout_sec = 86400") ||
		!strings.Contains(string(cfg), `approval_policy = "never"`) {
		t.Errorf("config.toml = %q, %v", cfg, err)
	}
}

// codex reads $CODEX_HOME, defaulting to $HOME/.codex. A repo-authored
// devcontainer runs the REPO's user, so a hardcoded /home/apothecary left
// codex with no config and no auth — it must follow the real $HOME.
func TestCodexHomeFollowsWorkspaceHome(t *testing.T) {
	envp, id := envWith(t)
	envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "$HOME") {
			return ports.ExecResult{Output: "/home/vscode\n"}, nil
		}
		return ports.ExecResult{}, nil
	}
	a := New(envp, Config{})
	ctx := context.Background()
	if err := a.PrepareEnv(ctx, id, []*agentspec.Spec{{Name: "planner", Prompt: "Plan.\n"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.SeedAuthJSON(ctx, id, []byte(`{"tokens":{}}`)); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/home/vscode/.codex/config.toml", "/home/vscode/.codex/auth.json"} {
		if _, err := envp.ReadFile(ctx, id, p); err != nil {
			t.Errorf("not written to the workspace's real home: %s (%v)", p, err)
		}
	}
}
