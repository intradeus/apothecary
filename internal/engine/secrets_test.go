package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// seedFakeAuth stands in for a harness's SeedAuth: an auth.json somewhere under
// the workspace user's home, which only the harness knows the path of.
const fakeAuthPath = "/home/vscode/.local/share/opencode/auth.json"

func withHarnessAuth(h *harness) {
	// A repository host credential, so the git-credentials assertions are
	// about a file bootstrap actually writes (the base harness configures no
	// git auth).
	h.res.GitUser, h.res.GitPass = "oauth2", "glpat-SECRETVALUE"
	h.res.SeedAuth = func(ctx context.Context, env envport.EnvID, _ func(string)) error {
		return h.envp.WriteFile(ctx, env, fakeAuthPath, []byte(`{"k":"sk-secret"}`), 0o600)
	}
	h.res.AuthFiles = func(context.Context, envport.EnvID) []string {
		return []string{fakeAuthPath}
	}
}

// files a run's workspace holds, for the assertions below.
func workspaceHas(t *testing.T, h *harness, env envport.EnvID, path string) bool {
	t.Helper()
	e := h.envp.Env(env)
	if e == nil {
		t.Fatalf("no fake env %s", env)
	}
	_, ok := e.Files[path]
	return ok
}

// A workspace outlives its run (kept for inspection, reaped much later), so a
// finished run must not leave live credentials sitting in it.
func TestTerminalRunScrubsWorkspaceCredentials(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	withHarnessAuth(h)
	ctx := context.Background()

	// Watch the workspace while it works, so "absent at the end" can't pass
	// just because the file was never written.
	seenDuringWork := map[string]bool{}
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		for path := range env.Files {
			seenDuringWork[path] = true
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}

	runID, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	for _, path := range []string{"/apothecary/provider.env", "/apothecary/git-credentials", fakeAuthPath} {
		if !seenDuringWork[path] {
			t.Fatalf("setup: %s was never in the workspace, so its absence proves nothing", path)
		}
	}
	v := h.view(t, runID)
	if v.State != run.Completed {
		t.Fatalf("state = %s, want completed", v.State)
	}
	env := envport.EnvID(v.EnvID)
	for _, path := range []string{"/apothecary/provider.env", "/apothecary/git-credentials", fakeAuthPath} {
		if workspaceHas(t, h, env, path) {
			t.Errorf("%s still in the workspace of a finished run", path)
		}
	}
	// Only the credentials go: the workspace stays inspectable, and the
	// run-scoped token is not a standing credential.
	if !workspaceHas(t, h, env, "/apothecary/run.env") {
		t.Error("run.env was scrubbed; it is the run-scoped token and identifies the workspace")
	}
	if len(h.envp.Destroyed) != 0 {
		t.Errorf("scrub destroyed the workspace: %v", h.envp.Destroyed)
	}
}

// "Try again" re-enters the scrubbed workspace, so the credentials must come
// back — otherwise the retry runs with no model auth and no git token. The
// assertion is what the workspace HAD WHILE WORKING, not its end state: the
// resumed run scrubs again when it finishes.
func TestResumeReseedsScrubbedCredentials(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	withHarnessAuth(h)
	ctx := context.Background()

	// Probe the workspace at the moment a stage runs in it. `rm -f` is handled
	// inside the fake and never reaches this hook, so the scrub can't trip it.
	var credsPresentDuringWork bool
	failVerify := true
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if _, ok := env.Files["/apothecary/provider.env"]; ok {
			credsPresentDuringWork = true
		}
		if failVerify && strings.Contains(cmd.Script, "make lint") {
			return envport.ExecResult{ExitCode: 1, Output: "FAIL"}, nil
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}

	// First attempt: the retry edge exhausts and the run ends resumable.
	runID, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	v := h.view(t, runID)
	if v.State != run.Abandoned && v.State != run.Failed {
		t.Fatalf("setup: state = %s, want a resumable terminal state", v.State)
	}
	env := envport.EnvID(v.EnvID)
	for _, path := range []string{"/apothecary/provider.env", fakeAuthPath} {
		if workspaceHas(t, h, env, path) {
			t.Fatalf("setup: %s was not scrubbed when the run ended", path)
		}
	}

	// Retry, this time letting it pass.
	credsPresentDuringWork = false
	failVerify = false
	if err := h.e.RetryRun(ctx, runID); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	if !credsPresentDuringWork {
		t.Error("the resumed run worked in a workspace with no credentials — re-seed did not happen")
	}
	if got := h.view(t, runID); got.State != run.Completed {
		t.Errorf("resumed run state = %s, want completed", got.State)
	}
	// And the retry cleans up after itself just like the first attempt.
	if workspaceHas(t, h, env, "/apothecary/provider.env") {
		t.Error("credentials left behind after the resumed run finished")
	}
}

// Aborting a PAUSED run never enters the run loop, and a paused workspace is
// full of live credentials — the case the loop's scrub cannot cover.
func TestAbortInactiveRunScrubsCredentials(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	withHarnessAuth(h)
	ctx := context.Background()

	// A run with a live workspace and no goroutine: append the events directly,
	// which is what a paused/queued run looks like to Abort.
	envID, err := h.envp.Provision(ctx, envport.EnvSpec{RunID: "r-paused"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.res.SeedAuth(ctx, envID, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if err := h.envp.WriteFile(ctx, envID, "/apothecary/provider.env", []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "paused", FlowName: "loop", ProjectID: testProject},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(envID)},
		&event.RunPaused{StageID: "build"},
	} {
		if _, err := h.store.Append(ctx, "r-paused", p); err != nil {
			t.Fatal(err)
		}
	}

	if err := h.e.Abort("r-paused"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/apothecary/provider.env", fakeAuthPath} {
		if workspaceHas(t, h, envID, path) {
			t.Errorf("%s survived the abort of a paused run", path)
		}
	}
}

// The run token is minted after the redactor is built; an agent that echoes
// it (it is in run.env) must still leave it off the record.
func TestAgentOutputNeverCarriesTheRunToken(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	var tok string
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		data, err := h.envp.ReadFile(ctx, call.Env, "/apothecary/run.env")
		if err != nil {
			return agentport.AgentResult{}, err
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "APOTHECARY_RUN_TOKEN="); ok {
				tok = v
			}
		}
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "token " + tok})
		return agentport.AgentResult{TurnsUsed: 1, Session: "s1"}, nil
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if tok == "" {
		t.Fatal("run.env carried no run token")
	}
	redacted := false
	for _, ev := range h.events(t, id) {
		if m, ok := ev.Payload.(*event.AgentMessage); ok {
			if strings.Contains(m.Text, tok) {
				t.Errorf("the run token reached the record: %q", m.Text)
			}
			redacted = redacted || strings.Contains(m.Text, "[redacted:run-token]")
		}
	}
	if !redacted {
		t.Error("no message carries [redacted:run-token]")
	}
}
