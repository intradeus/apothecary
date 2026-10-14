package opencode

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
)

// opencode's ACP session modes are its agents: a stage runs as its agent.
func TestProfileSelectsTheAgentAsMode(t *testing.T) {
	p := Profile()
	if !strings.HasPrefix(p.Command, "opencode acp") || p.Mode(ports.AgentSpec{Agent: "planner"}) != "planner" || p.Preamble != nil {
		t.Errorf("profile = %+v", p)
	}
}

func envWith(t *testing.T, files map[string]string) (*fake.EnvProvisioner, ports.EnvID) {
	t.Helper()
	envp := fake.NewEnvProvisioner()
	id, err := envp.Provision(context.Background(), ports.EnvSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	for p, c := range files {
		if err := envp.WriteFile(context.Background(), id, p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return envp, id
}

// opencode reads its config and auth from $HOME. The §8 pipeline image runs
// as `apothecary`, but a repo-authored devcontainer runs the REPO's user
// (root, vscode, node…) — writing to a hardcoded /home/apothecary there left
// opencode with no provider and no agents ("agent planner not found",
// "Model not found"). Config must follow the workspace's real $HOME.
func TestConfigFollowsWorkspaceHome(t *testing.T) {
	envp, id := envWith(t, nil)
	envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "$HOME") {
			return ports.ExecResult{Output: "/root\n"}, nil
		}
		return ports.ExecResult{}, nil
	}
	a := New(envp, Config{})
	ctx := context.Background()
	if err := a.PrepareEnv(ctx, id, []*agentspec.Spec{{Name: "planner", Prompt: "Plan.\n"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.SeedAuthJSON(ctx, id, []byte(`{"openrouter":{"type":"api"}}`)); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/root/.config/opencode/agent/planner.md",
		"/root/.config/opencode/opencode.json",
		"/root/.local/share/opencode/auth.json",
	} {
		if _, err := envp.ReadFile(ctx, id, p); err != nil {
			t.Errorf("not written to the workspace's real home: %s (%v)", p, err)
		}
	}
}

func TestPrepareEnvGeneratesConfigs(t *testing.T) {
	envp, id := envWith(t, nil)
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
	// Auth material arrives via SeedAuthJSON (DESIGN2 §4: the opencode kind
	// composes it; the adapter just writes).
	if err := a.SeedAuthJSON(context.Background(), id, []byte(`{"openrouter":{"type":"api"}}`)); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	planner, err := envp.ReadFile(ctx, id, "/home/apothecary/.config/opencode/agent/planner.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode: primary", "edit: false", "patch: false", "write: true", "bash: false", "Plan carefully."} {
		if !strings.Contains(string(planner), want) {
			t.Errorf("planner.md missing %q:\n%s", want, planner)
		}
	}
	builder, _ := envp.ReadFile(ctx, id, "/home/apothecary/.config/opencode/agent/builder.md")
	if strings.Contains(string(builder), "edit: false") || !strings.Contains(string(builder), "bash: true") {
		t.Errorf("builder.md tool perms wrong:\n%s", builder)
	}

	cfgData, err := envp.ReadFile(ctx, id, "/home/apothecary/.config/opencode/opencode.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCP map[string]struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatal(err)
	}
	mcp, ok := cfg.MCP["ask_human"]
	if !ok || mcp.Type != "local" ||
		!slices.Equal(mcp.Command, []string{"/opt/apothecary/shim", "ask-human"}) {
		t.Errorf("ask_human MCP registration = %+v", cfg.MCP)
	}

	auth, err := envp.ReadFile(ctx, id, "/home/apothecary/.local/share/opencode/auth.json")
	if err != nil || !strings.Contains(string(auth), "openrouter") {
		t.Errorf("auth.json = %q, %v", auth, err)
	}
}

func TestPrepareEnvZeroSpecsStillWritesConfig(t *testing.T) {
	// Zero agents is legal at the adapter level (preflight guards flows
	// that need them) — the ask_human MCP registration must exist anyway.
	envp, id := envWith(t, nil)
	a := New(envp, Config{})
	if err := a.PrepareEnv(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := envp.ReadFile(context.Background(), id, "/home/apothecary/.config/opencode/opencode.json")
	if err != nil || !strings.Contains(string(cfg), "ask_human") {
		t.Errorf("opencode.json missing ask_human MCP: %v\n%s", err, cfg)
	}
}
