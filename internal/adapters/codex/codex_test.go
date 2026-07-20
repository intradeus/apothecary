package codex

import (
	"context"
	"strings"
	"testing"

	"factory/internal/adapters/fake"
	"factory/internal/ports"
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

// The JSONL stream probed against codex-cli 0.144.6.
const streamFixture = `Reading prompt from stdin...
{"type":"thread.started","thread_id":"019f-abc"}
{"type":"turn.started"}
2026-07-20T03:52:32Z ERROR codex_api::something: noise line
{"type":"item.completed","item":{"type":"reasoning","text":"thinking"}}
{"type":"item.completed","item":{"type":"agent_message","text":"I added the function."}}
{"type":"turn.completed","usage":{"input_tokens":1200,"cached_input_tokens":300,"output_tokens":80}}
{"type":"item.completed","item":{"type":"agent_message","text":"Done."}}
{"type":"turn.completed","usage":{"input_tokens":500,"cached_input_tokens":0,"output_tokens":40}}
`

func TestParseEvents(t *testing.T) {
	r := parseEvents(streamFixture)
	if r.Session != "019f-abc" {
		t.Errorf("session = %q", r.Session)
	}
	if r.TurnsUsed != 2 {
		t.Errorf("turns = %d", r.TurnsUsed)
	}
	// input includes cached tokens (what was actually sent), cost stays 0
	// (codex reports tokens, not dollars — budgets ride max_total_turns).
	if r.Usage.InputTokens != 2000 || r.Usage.OutputTokens != 120 || r.Usage.CostUSD != 0 {
		t.Errorf("usage = %+v", r.Usage)
	}
	if !strings.Contains(r.Output, "added the function") || strings.Contains(r.Output, "thinking") {
		t.Errorf("output = %q (agent messages only)", r.Output)
	}
}

func TestRunScriptShape(t *testing.T) {
	envp, id := envWith(t)
	envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		return ports.ExecResult{ExitCode: 0, Output: streamFixture}, nil
	}
	a := New(envp, Config{})
	res, err := a.Run(context.Background(), id, ports.AgentSpec{Agent: "builder", Model: "openai/gpt-5-codex"}, ports.AgentInput{Prompt: "build it"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Session != "019f-abc" {
		t.Errorf("session = %q", res.Session)
	}
	var script string
	for _, c := range envp.ExecCalls {
		if strings.Contains(c.Cmd.Script, "codex exec") {
			script = c.Cmd.Script
		}
	}
	// Role preamble + prompt piped via stdin (`-`); model prefix stripped;
	// bypass flag (the workspace IS the sandbox); provider.env sourced.
	for _, want := range []string{
		`cat "/factory/agents/builder.role" "/factory/prompt.txt" |`,
		`-m "gpt-5-codex"`,
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check -",
		"/factory/provider.env",
		"--json",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	if data, err := envp.ReadFile(context.Background(), id, "/factory/prompt.txt"); err != nil || string(data) != "build it" {
		t.Errorf("prompt file = %q, %v", data, err)
	}
}

func TestResumeUsesThreadID(t *testing.T) {
	envp, id := envWith(t)
	envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		return ports.ExecResult{ExitCode: 0, Output: streamFixture}, nil
	}
	a := New(envp, Config{})
	if _, err := a.Resume(context.Background(), id, "019f-abc", "continue"); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, c := range envp.ExecCalls {
		if strings.Contains(c.Cmd.Script, "codex exec resume") {
			script = c.Cmd.Script
		}
	}
	if !strings.Contains(script, `resume "019f-abc"`) || !strings.Contains(script, `cat "/factory/reply.txt" |`) {
		t.Errorf("resume script:\n%s", script)
	}
}

func TestFailureCarriesRawOutput(t *testing.T) {
	envp, id := envWith(t)
	envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "codex exec") {
			return ports.ExecResult{ExitCode: 1, Output: `{"type":"error","message":"401 Unauthorized"}`}, nil
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}
	a := New(envp, Config{})
	res, err := a.Run(context.Background(), id, ports.AgentSpec{Agent: "builder", Model: "openai/gpt-5"}, ports.AgentInput{})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(res.Output, "401 Unauthorized") {
		t.Errorf("failed-run Output = %q, want raw output (the opencode log lesson)", res.Output)
	}
}

func TestPrepareEnvWritesRolesAndConfig(t *testing.T) {
	envp, id := envWith(t)
	specOut := "===FACTORY-SPEC .factory/agents/planner.yaml\n" +
		"name: planner\ndescription: plans\nread_only: true\nallow_bash: false\nprompt: |\n  Plan carefully.\n" +
		"===FACTORY-SPEC .factory/agents/builder.yaml\n" +
		"name: builder\ndescription: builds\nread_only: false\nallow_bash: true\nprompt: |\n  Build it.\n"
	envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "FACTORY-SPEC") {
			return ports.ExecResult{ExitCode: 0, Output: specOut}, nil
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}
	a := New(envp, Config{})
	if err := a.PrepareEnv(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	planner, err := envp.ReadFile(ctx, id, "/factory/agents/planner.role")
	if err != nil {
		t.Fatal(err)
	}
	// read_only/allow_bash are instruction-level for codex (§13 residual).
	for _, want := range []string{"# ROLE", "Plan carefully.", "READ-ONLY", "Do not run shell commands"} {
		if !strings.Contains(string(planner), want) {
			t.Errorf("planner.role missing %q:\n%s", want, planner)
		}
	}
	builder, _ := envp.ReadFile(ctx, id, "/factory/agents/builder.role")
	if strings.Contains(string(builder), "READ-ONLY") {
		t.Error("builder.role must not be read-only")
	}
	cfg, err := envp.ReadFile(ctx, id, "/home/factory/.codex/config.toml")
	if err != nil || !strings.Contains(string(cfg), "[mcp_servers.ask_human]") ||
		!strings.Contains(string(cfg), "/opt/factory/ask-human.mjs") {
		t.Errorf("config.toml = %q, %v", cfg, err)
	}
}
