package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/ports/agentport"
)

// shipFlowDoc builds and opens a merge request: what an outside agent waits
// for.
const shipFlowDoc = `name: ship
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build the thing
    outputs:
      - { name: mr, type: file }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
`

// The phase's done-when (docs/assistant-plan.md, phase 4): an agent with a
// token that can do nothing else files a ticket, starts a run on it, waits
// for its merge request and reads its link; what it did is on its token's
// record.
func TestAnOutsideAgentShipsATicket(t *testing.T) {
	x := newEnvWith(t, engine.Config{}, nil, loopFlowDoc, shipFlowDoc)
	ctx := context.Background()

	// The token form's runs purpose, in this workshop only.
	var grant []string
	for _, p := range decode[[]Permission](t, x.expect(t, "permissions", nil, "GET", "/api/permissions", nil, 200)) {
		if slices.Contains(p.Purposes, "runs") {
			grant = append(grant, p.Action)
		}
	}
	secret, tok, err := x.srv.Auth.NewToken(ctx, testUser, "laptop agent", nil, auth.Grant{Actions: grant, ProjectIDs: []string{testProject}})
	if err != nil {
		t.Fatal(err)
	}
	agent := bearer(secret)
	// The builder writes the merge request's description, as an agent would.
	x.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if err := x.envp.WriteFile(ctx, call.Env, "artifacts/mr.md", []byte("Adds GET /healthz.\n"), 0o644); err != nil {
			return agentport.AgentResult{}, err
		}
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}

	names := x.mcpToolNames(t, agent)
	for _, want := range []string{"create_ticket", "start_run", "wait_run", "get_run", "abort_run"} {
		if !slices.Contains(names, want) {
			t.Errorf("the runs grant lacks %s: %v", want, names)
		}
	}
	for _, never := range []string{"create_flow", "update_flow", "create_skill", "create_dashboard", "create_automation", "fill"} {
		if slices.Contains(names, never) {
			t.Errorf("the runs grant lists %s", never)
		}
	}

	text := func(r mcpCallResult) string { return r.Content[0].Text }
	res := x.mcpTool(t, agent, "create_ticket", map[string]any{"id": testProject, "title": "Add a health endpoint",
		"goal": "GET /healthz answers 200.", "change_type": "feat"})
	if res.IsError {
		t.Fatalf("create_ticket = %s", text(res))
	}
	var ticket struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal([]byte(text(res)), &ticket); err != nil || ticket.Key == "" {
		t.Fatalf("create_ticket answered %s", text(res))
	}
	res = x.mcpTool(t, agent, "start_run", map[string]any{"id": testProject, "key": ticket.Key, "flow": "ship"})
	if res.IsError {
		t.Fatalf("start_run = %s", text(res))
	}
	var ref struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(text(res)), &ref); err != nil || ref.RunID == "" {
		t.Fatalf("start_run answered %s", text(res))
	}

	// Wait, passing seen back, until the merge request is there.
	seen := ""
	var mrURL string
	deadline := time.Now().Add(20 * time.Second)
	for mrURL == "" && time.Now().Before(deadline) {
		args := map[string]any{"id": ref.RunID, "timeout": 5}
		if seen != "" {
			args["seen"] = seen
		}
		res := x.mcpTool(t, agent, "wait_run", args)
		if res.IsError {
			t.Fatalf("wait_run = %s", text(res))
		}
		var w RunWait
		if err := json.Unmarshal([]byte(text(res)), &w); err != nil {
			t.Fatalf("wait_run answered %s", text(res))
		}
		if len(w.Run.MRs) > 0 {
			mrURL = w.Run.MRs[0].URL
		}
		if w.Run.State.Terminal() && mrURL == "" {
			t.Fatalf("the run ended with no merge request: %+v", w.Run)
		}
		seen = w.Seen
	}
	if mrURL == "" {
		t.Fatal("no merge request within the wait")
	}

	// Nothing else: settings, flows, a gate's answer, messages, egress,
	// secrets, tokens are refused, each for the action it lacks.
	held, _ := x.holdRun(t)
	ping, err := x.e.RaisePing(held, "clarification", "which db?", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{
		{"PUT", "/api/projects/" + testProject},
		{"PUT", "/api/projects/" + testProject + "/flows/loop"},
		{"POST", "/api/pings/" + ping.ID + "/answer"},
		{"POST", "/api/runs/" + held + "/messages"},
		{"POST", "/api/runs/" + held + "/egress-allow"},
		{"PUT", "/api/projects/" + testProject + "/secrets/x"},
		{"POST", "/api/me/tokens"},
	} {
		res, body := x.do(t, c.method, c.path, map[string]any{}, agent)
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("the agent's %s %s = %d %s, want 403", c.method, c.path, res.StatusCode, body)
		}
	}

	// Its record names the token for each call, newest first, refusals too.
	calls := decode[[]auth.TokenCall](t, x.expect(t, "calls", nil, "GET", "/api/me/tokens/"+tok.ID+"/calls", nil, 200))
	var tools []string
	for _, c := range calls {
		if c.Tool != "" && !slices.Contains(tools, c.Tool) {
			tools = append(tools, c.Tool)
		}
	}
	for _, want := range []string{"create_ticket", "start_run"} {
		if !slices.Contains(tools, want) {
			t.Errorf("the record lacks %s: %+v", want, tools)
		}
	}
	// Reads answered are not kept: hours of waiting would push the writes
	// out.
	if slices.Contains(tools, "wait_run") {
		t.Errorf("the record keeps reads: %+v", tools)
	}
	if c := calls[0]; c.Status != http.StatusForbidden || c.Method != "POST" || !strings.HasSuffix(c.Path, "/tokens") {
		t.Errorf("newest call = %+v, want the refused token mint", c)
	}
	// Another person's token's record is not there.
	x.expect(t, "unknown token", nil, "GET", "/api/me/tokens/nope/calls", nil, 404)
}

// wait_run answers at once for a run already past what the caller saw, or
// ended; at the time limit with changed false; refuses a bad timeout.
func TestWaitRun(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id, _ := x.holdRun(t)
	w := decode[RunWait](t, x.expect(t, "now", nil, "GET", "/api/runs/"+id+"/wait?timeout=1", nil, 200))
	if w.Changed || w.Seen == "" || w.Run.RunID != id {
		t.Errorf("a quiet run = %+v", w)
	}
	w = decode[RunWait](t, x.expect(t, "stale", nil, "GET", "/api/runs/"+id+"/wait?seen="+url.QueryEscape("pending|pings=|mrs="), nil, 200))
	if !w.Changed {
		t.Errorf("a run past what was seen = %+v", w)
	}
	x.expect(t, "bad timeout", nil, "GET", "/api/runs/"+id+"/wait?timeout=60", nil, 400)
	x.expect(t, "zero timeout", nil, "GET", "/api/runs/"+id+"/wait?timeout=0", nil, 400)
	x.expect(t, "no run", nil, "GET", "/api/runs/nope/wait", nil, 404)

	// A change while waiting: an abort ends the wait at once.
	done := make(chan RunWait, 1)
	go func() {
		done <- decode[RunWait](t, x.expect(t, "waiting", nil, "GET", "/api/runs/"+id+"/wait?timeout=20&seen="+url.QueryEscape(w.Seen), nil, 200))
	}()
	time.Sleep(200 * time.Millisecond)
	x.expect(t, "abort", nil, "POST", "/api/runs/"+id+"/abort", nil, 202)
	select {
	case got := <-done:
		if !got.Changed || !got.Run.State.Terminal() {
			t.Errorf("after an abort = %+v", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the wait did not end with the run")
	}
}

// The Authoring purpose writes flows, but not one that reads the workshop's
// secrets (that needs project:secrets, never a tool's), and not an
// automation (it starts runs: run:operate). The account itself does both.
func TestAuthoringStopsAtSecretsAndRuns(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	var grant []string
	for _, p := range decode[[]Permission](t, x.expect(t, "permissions", nil, "GET", "/api/permissions", nil, 200)) {
		if slices.Contains(p.Purposes, "authoring") {
			grant = append(grant, p.Action)
		}
	}
	secret, _, err := x.srv.Auth.NewToken(ctx, testUser, "author", nil, auth.Grant{Actions: grant, ProjectIDs: []string{testProject}})
	if err != nil {
		t.Fatal(err)
	}
	author := bearer(secret)
	plain := strings.Replace(loopFlowDoc, "name: loop", "name: plain", 1)
	if res := x.mcpTool(t, author, "create_flow", map[string]any{"id": testProject, "name": "plain", "yaml": plain}); res.IsError {
		t.Errorf("a plain flow = %s", res.Content[0].Text)
	}
	reads := `name: reads
version: 5
parameters: { harness: fake, model: fake/model }
mcp_servers:
  - { name: tracker, command: [tracker-mcp], env: { T: "${secrets.deploy_key}" } }
agents:
  - name: builder
    prompt: build the thing
    mcp: [tracker]
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`
	res := x.mcpTool(t, author, "create_flow", map[string]any{"id": testProject, "name": "reads", "yaml": reads})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "project:secrets") {
		t.Errorf("a flow reading a secret = %+v", res)
	}
	res = x.mcpTool(t, author, "update_flow", map[string]any{"id": testProject, "name": "plain", "yaml": strings.Replace(reads, "name: reads", "name: plain", 1)})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "project:secrets") {
		t.Errorf("a flow edited to read a secret = %+v", res)
	}
	auto := map[string]any{"id": testProject, "name": "nightly", "kind": "cron", "schedule": "0 3 * * *", "flow": "plain",
		"ticket_id": x.newTicket(t, "Nightly", "")}
	res = x.mcpTool(t, author, "create_automation", auto)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "run:operate") {
		t.Errorf("an automation = %+v", res)
	}
	// The account itself holds both.
	x.expect(t, "a person's flow", nil, "POST", "/api/projects/"+testProject+"/flows", map[string]any{"name": "reads", "yaml": reads}, 201)
	delete(auto, "id")
	x.expect(t, "a person's automation", nil, "POST", "/api/projects/"+testProject+"/automations", auto, 201)
}
