package omp

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
		strings.Join(p.MCPServers[0].Command, " ") != "/opt/apothecary/shim ask-human" {
		t.Errorf("mcp servers = %+v", p.MCPServers)
	}
	pre, err := p.Preamble(context.Background(), id, ports.AgentSpec{Agent: "planner"})
	if err != nil || !strings.Contains(pre, "Plan carefully.") {
		t.Errorf("preamble = %q, %v", pre, err)
	}
	if _, err := p.Preamble(context.Background(), id, ports.AgentSpec{Agent: "ghost"}); err == nil {
		t.Error("an unknown agent must fail, not run role-less")
	}
}

// config.yml lands in the workspace user's real home, not a hardcoded one.
func TestPrepareEnvWritesConfigInWorkspaceHome(t *testing.T) {
	envp, id := envWith(t)
	envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "$HOME") {
			return ports.ExecResult{Output: "/home/vscode\n"}, nil
		}
		return ports.ExecResult{}, nil
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
func TestSeedOAuthImports(t *testing.T) {
	envp, id := envWith(t)
	var script string
	envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "auth-broker import") {
			script = cmd.Script
			return ports.ExecResult{Output: `{"dryRun":false,"imported":[{"provider":"anthropic","file":"/x/0.json"}],"plan":[],"skipped":[]}` + "\n"}, nil
		}
		return ports.ExecResult{}, nil
	}
	a := New(envp, Config{})
	ctx := context.Background()
	if err := a.SeedOAuth(ctx, id, [][]byte{[]byte(`{"type":"claude"}`)}); err != nil {
		t.Fatal(err)
	}
	staged, err := envp.ReadFile(ctx, id, "/home/apothecary/.omp/agent/import/0.json")
	if err != nil || string(staged) != `{"type":"claude"}` {
		t.Errorf("staged = %q, %v", staged, err)
	}
	if !strings.Contains(script, "omp auth-broker import '/home/apothecary/.omp/agent/import' --json") ||
		!strings.Contains(script, "rm -rf '/home/apothecary/.omp/agent/import'") {
		t.Errorf("script = %q", script)
	}

	envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "auth-broker import") {
			return ports.ExecResult{Output: `{"imported":[],"skipped":[{"file":"/x/0.json","reason":"cannot parse expired=?"}]}`}, nil
		}
		return ports.ExecResult{}, nil
	}
	if err := a.SeedOAuth(ctx, id, [][]byte{[]byte(`{}`)}); err == nil || !strings.Contains(err.Error(), "0.json: cannot parse expired") {
		t.Errorf("err = %v, want omp's skip reason", err)
	}
}
