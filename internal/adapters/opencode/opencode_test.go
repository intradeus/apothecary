package opencode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"factory/internal/adapters/fake"
	"factory/internal/ports"
)

// ndjson is a captured `opencode run --format json` stream (v1.17.8),
// verbatim except for a second turn appended to exercise accumulation.
const ndjson = `{"type":"step_start","timestamp":1784432171501,"sessionID":"ses_0878de","part":{"id":"prt_1","messageID":"msg_1","sessionID":"ses_0878de","type":"step-start"}}
{"type":"text","timestamp":1784432171599,"sessionID":"ses_0878de","part":{"id":"prt_2","messageID":"msg_1","sessionID":"ses_0878de","type":"text","text":"OK","time":{"start":1784432171498,"end":1784432171579}}}
{"type":"step_finish","timestamp":1784432171599,"sessionID":"ses_0878de","part":{"id":"prt_3","reason":"stop","messageID":"msg_1","sessionID":"ses_0878de","type":"step-finish","tokens":{"total":8152,"input":3,"output":4,"reasoning":0,"cache":{"write":8145,"read":0}},"cost":0.01020425}}
{"type":"step_finish","timestamp":1784432171700,"sessionID":"ses_0878de","part":{"id":"prt_4","reason":"stop","messageID":"msg_2","sessionID":"ses_0878de","type":"step-finish","tokens":{"total":100,"input":50,"output":30,"reasoning":10,"cache":{"write":0,"read":10}},"cost":0.005}}
some stderr noise that is not json
`

func TestParseEvents(t *testing.T) {
	r := parseEvents(ndjson)
	if r.Session != "ses_0878de" {
		t.Errorf("session = %q", r.Session)
	}
	if r.TurnsUsed != 2 {
		t.Errorf("turns = %d, want 2", r.TurnsUsed)
	}
	if got := r.Usage.CostUSD; got < 0.0152 || got > 0.0153 {
		t.Errorf("cost = %v", got)
	}
	if r.Usage.InputTokens != 3+8145+0+50+0+10 {
		t.Errorf("input tokens = %d", r.Usage.InputTokens)
	}
	if r.Usage.OutputTokens != 4+0+30+10 {
		t.Errorf("output tokens = %d", r.Usage.OutputTokens)
	}
	if r.Output != "OK" {
		t.Errorf("output = %q", r.Output)
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

func TestRunInvokesOpencodeWithPromptFile(t *testing.T) {
	envp, id := envWith(t, nil)
	envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "opencode run") {
			return ports.ExecResult{ExitCode: 0, Output: ndjson}, nil
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}
	a := New(envp, Config{})

	res, err := a.Run(context.Background(), id,
		ports.AgentSpec{Agent: "planner", Model: "openrouter/anthropic/claude-haiku-4.5", MaxTurns: 25},
		ports.AgentInput{Prompt: "# TASK\nDo the thing"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Session != "ses_0878de" || res.TurnsUsed != 2 {
		t.Errorf("result = %+v", res)
	}

	// The prompt traveled via file, not the command line.
	data, err := envp.ReadFile(context.Background(), id, "/factory/prompt.txt")
	if err != nil || string(data) != "# TASK\nDo the thing" {
		t.Errorf("prompt file = %q, %v", data, err)
	}
	var script string
	for _, c := range envp.ExecCalls {
		if strings.Contains(c.Cmd.Script, "opencode run") {
			script = c.Cmd.Script
			if c.Cmd.Dir != "repo" {
				t.Errorf("opencode must run in the repo root, got dir %q", c.Cmd.Dir)
			}
		}
	}
	for _, want := range []string{`--format json`, `--agent "planner"`, `-m "openrouter/anthropic/claude-haiku-4.5"`, `provider.env`, `cat /factory/prompt.txt`} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
}

func TestResumeUsesSessionFlag(t *testing.T) {
	envp, id := envWith(t, nil)
	envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		return ports.ExecResult{ExitCode: 0, Output: ndjson}, nil
	}
	a := New(envp, Config{})
	res, err := a.Resume(context.Background(), id, "ses_prev", "carry on")
	if err != nil {
		t.Fatal(err)
	}
	if res.Session != "ses_0878de" {
		t.Errorf("session = %q", res.Session)
	}
	script := envp.ExecCalls[len(envp.ExecCalls)-1].Cmd.Script
	if !strings.Contains(script, `--session "ses_prev"`) || !strings.Contains(script, "cat /factory/reply.txt") {
		t.Errorf("resume script:\n%s", script)
	}
}

func TestRunNonzeroExitIsError(t *testing.T) {
	envp, id := envWith(t, nil)
	envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "opencode run") {
			return ports.ExecResult{ExitCode: 1, Output: "ProviderAuthError: no key"}, nil
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}
	a := New(envp, Config{})
	_, err := a.Run(context.Background(), id, ports.AgentSpec{Agent: "builder", Model: "a/b"}, ports.AgentInput{})
	if err == nil || !strings.Contains(err.Error(), "ProviderAuthError") {
		t.Errorf("err = %v", err)
	}
}

const specYAML = `name: %s
description: %s
read_only: %v
allow_bash: %v
prompt: |
  %s
`

func TestPrepareEnvGeneratesConfigs(t *testing.T) {
	envp, id := envWith(t, nil)
	// Simulate the repo's .factory/agents directory via the exec that lists specs.
	specOut := "===FACTORY-SPEC .factory/agents/planner.yaml\n" +
		"name: planner\ndescription: plans\nread_only: true\nallow_bash: false\nprompt: |\n  Plan carefully.\n" +
		"===FACTORY-SPEC .factory/agents/builder.yaml\n" +
		"name: builder\ndescription: builds\nread_only: false\nallow_bash: true\nprompt: |\n  Build it.\n"
	envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "FACTORY-SPEC") {
			return ports.ExecResult{ExitCode: 0, Output: specOut}, nil
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}
	a := New(envp, Config{AuthJSON: []byte(`{"openrouter":{"type":"api"}}`)})
	if err := a.PrepareEnv(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	planner, err := envp.ReadFile(ctx, id, "/home/factory/.config/opencode/agent/planner.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode: primary", "edit: false", "patch: false", "write: true", "bash: false", "Plan carefully."} {
		if !strings.Contains(string(planner), want) {
			t.Errorf("planner.md missing %q:\n%s", want, planner)
		}
	}
	builder, _ := envp.ReadFile(ctx, id, "/home/factory/.config/opencode/agent/builder.md")
	if strings.Contains(string(builder), "edit: false") || !strings.Contains(string(builder), "bash: true") {
		t.Errorf("builder.md tool perms wrong:\n%s", builder)
	}

	cfgData, err := envp.ReadFile(ctx, id, "/home/factory/.config/opencode/opencode.json")
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
	if !ok || mcp.Type != "local" || len(mcp.Command) != 2 || mcp.Command[1] != "/opt/factory/ask-human.mjs" {
		t.Errorf("ask_human MCP registration = %+v", cfg.MCP)
	}

	auth, err := envp.ReadFile(ctx, id, "/home/factory/.local/share/opencode/auth.json")
	if err != nil || !strings.Contains(string(auth), "openrouter") {
		t.Errorf("auth.json = %q, %v", auth, err)
	}
}

func TestPrepareEnvFailsWithoutSpecs(t *testing.T) {
	envp, id := envWith(t, nil)
	envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		return ports.ExecResult{ExitCode: 0, Output: ""}, nil
	}
	a := New(envp, Config{})
	if err := a.PrepareEnv(context.Background(), id); err == nil {
		t.Error("expected error with no agent specs")
	}
}
