package omp

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/adapters/acpagent"
	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/flow/harnessspec"
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

// A stage's options become one JSON overlay (sorted, so one option set is
// one file) plus --advisor when an advisor is set.
func TestRenderOptions(t *testing.T) {
	overlay, flags, err := RenderOptions(map[string]string{"advisor": "acme/a", "advisor_review": "agent-end", "smol": "acme/s", "slow": "acme/l"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"advisor":{"reviewMode":"agent-end"},"modelRoles":{"advisor":"acme/a","slow":"acme/l","smol":"acme/s"}}`; string(overlay) != want {
		t.Errorf("overlay = %s, want %s", overlay, want)
	}
	if !slices.Equal(flags, []string{"--advisor"}) {
		t.Errorf("flags = %v", flags)
	}
	if _, flags, err := RenderOptions(map[string]string{"smol": "acme/s"}); err != nil || len(flags) != 0 {
		t.Errorf("smol alone = %v, %v; want no flags", flags, err)
	}
	if _, _, err := RenderOptions(map[string]string{"brain": "acme/b"}); err == nil {
		t.Error("an unknown option must fail")
	}
	// The registry and the renderer stay in step.
	for _, o := range harnessspec.Get(harnessspec.Omp).Options {
		if _, _, err := RenderOptions(map[string]string{o.Name: "acme/x"}); err != nil {
			t.Errorf("registered option %q: %v", o.Name, err)
		}
	}
}

// The overlay lands in the workspace under its hash, and the command line
// names it: the same options, the same path.
func TestOptionArgsWriteTheOverlay(t *testing.T) {
	envp, id := envWith(t)
	ctx := context.Background()
	p := New(envp, Config{}).Profile()
	opts := map[string]string{"advisor": "acme/a"}
	args, err := p.OptionArgs(ctx, id, opts)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`^--config=(/apothecary/harness-options/omp-[0-9a-f]{12}\.yml) --advisor$`).FindStringSubmatch(args)
	if m == nil {
		t.Fatalf("args = %q", args)
	}
	body, err := envp.ReadFile(ctx, id, m[1])
	if want, _, _ := RenderOptions(opts); err != nil || string(body) != string(want) {
		t.Errorf("overlay file = %q, %v", body, err)
	}
	if again, _ := p.OptionArgs(ctx, id, map[string]string{"advisor": "acme/a"}); again != args {
		t.Errorf("same options, different command line: %q vs %q", again, args)
	}
	if other, _ := p.OptionArgs(ctx, id, map[string]string{"advisor": "acme/b"}); other == args {
		t.Error("different options share a command line")
	}
}
