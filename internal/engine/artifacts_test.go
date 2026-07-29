package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/artifacts"
	"apothecary/internal/event"
	"apothecary/internal/ports"
)

// fakeArtifacts is an in-memory engine.ArtifactStore.
type fakeArtifacts struct {
	metas []artifacts.Meta
	blobs map[string][]byte
}

func (f *fakeArtifacts) Associate(context.Context, []string, string, string, string) error {
	return nil
}
func (f *fakeArtifacts) Capture(_ context.Context, m artifacts.Meta, data []byte) (artifacts.Meta, error) {
	m.ID = fmt.Sprintf("cap%d", len(f.metas)+1)
	m.Size = int64(len(data))
	f.metas = append(f.metas, m)
	if f.blobs == nil {
		f.blobs = map[string][]byte{}
	}
	f.blobs[m.ID] = append([]byte(nil), data...)
	return m, nil
}
func (f *fakeArtifacts) ForRun(context.Context, string) ([]artifacts.Meta, error) {
	// Ticket-scoped artifacts are owned by the ticket (run_id empty), so the
	// real store's ForRun never returns them — they resolve via Meta only.
	var out []artifacts.Meta
	for _, m := range f.metas {
		if m.Scope != artifacts.ScopeTicketGoal {
			out = append(out, m)
		}
	}
	return out, nil
}
func (f *fakeArtifacts) ForPing(_ context.Context, pingID, scope string) (out []artifacts.Meta, _ error) {
	for _, m := range f.metas {
		if m.PingID == pingID && m.Scope == scope {
			out = append(out, m)
		}
	}
	return out, nil
}
func (f *fakeArtifacts) Bytes(_ context.Context, id string) ([]byte, artifacts.Meta, error) {
	for _, m := range f.metas {
		if m.ID == id {
			return f.blobs[id], m, nil
		}
	}
	return nil, artifacts.Meta{}, artifacts.ErrNotFound
}
func (f *fakeArtifacts) Meta(_ context.Context, id string) (artifacts.Meta, error) {
	for _, m := range f.metas {
		if m.ID == id {
			return m, nil
		}
	}
	return artifacts.Meta{}, artifacts.ErrNotFound
}
func (f *fakeArtifacts) DeleteForRun(context.Context, string) error { return nil }

// Goal artifacts are delivered into the workspace (under the git-excluded
// .apothecary/ tree) AND listed in the agent's prompt so it can read them.
func TestGoalArtifactsDeliveredAndPrompted(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.e.SetArtifacts(&fakeArtifacts{
		metas: []artifacts.Meta{{ID: "aaaa1111bbbb", Scope: artifacts.ScopeGoal, Filename: "spec.pdf", Mime: "application/pdf"}},
		blobs: map[string][]byte{"aaaa1111bbbb": []byte("%PDF-1.4")},
	})
	runID, err := h.e.CreateRun(context.Background(),
		Ticket{Title: "T", ArtifactIDs: []string{"aaaa1111bbbb"}}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	wantPath := ".apothecary/artifacts/goal/aaaa1111/spec.pdf"
	// The builder prompt lists the attachment (path + filename + mime).
	var prompt string
	for _, c := range h.runner.RunCalls {
		prompt = c.In.Prompt
	}
	if !strings.Contains(prompt, "# ATTACHED FILES") || !strings.Contains(prompt, wantPath) {
		t.Errorf("prompt missing attached-files section / path:\n%s", prompt)
	}
	// The file was actually written into the workspace.
	env := h.view(t, runID).EnvID
	data, err := h.envp.ReadFile(context.Background(), ports.EnvID(env), wantPath)
	if err != nil || string(data) != "%PDF-1.4" {
		t.Errorf("artifact not delivered into workspace: %q, %v", data, err)
	}
}

// TestAttemptSnapshotsSurviveRetries is the point of M27: attempt 1's output
// stays inspectable after attempt 2 overwrites the file. Before, a retry left
// nothing behind — the workspace holds one version per path and the event
// store kept only 8 KB tails, so "the plan the human rejected" was gone.
func TestAttemptSnapshotsSurviveRetries(t *testing.T) {
	doc := `name: snap
version: 2
defaults: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: plan, type: agent, agent: planner, input: { failure: $FAILURE }, on_success: check }
  - { id: check, type: command, run: verify, on_success: done,
      on_fail: { to: plan, max: 1 } }
`
	h := newHarness(t, mustFlow(t, doc))
	fa := &fakeArtifacts{}
	h.e.SetArtifacts(fa)

	attempt := 0
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"planner": func(c fake.RunCall) (ports.AgentResult, error) {
			attempt++
			h.writeFile(t, c.Env, "artifacts/plan.md", fmt.Sprintf("plan revision %d", attempt))
			return ports.AgentResult{}, nil
		},
	})
	// The check fails once, so the planner runs twice and rewrites the file.
	checks := 0
	h.envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if !strings.Contains(cmd.Script, "verify") {
			return ports.ExecResult{}, nil
		}
		checks++
		if checks == 1 {
			return ports.ExecResult{ExitCode: 1, Output: "boom"}, nil
		}
		return ports.ExecResult{}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "snap", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	// Both revisions are captured, attributed to their attempt.
	want := map[int]string{1: "plan revision 1", 2: "plan revision 2"}
	found := map[int]string{}
	for _, m := range fa.metas {
		if m.StageID == "plan" && m.Scope == artifacts.ScopeStageOutput {
			found[m.Iteration] = string(fa.blobs[m.ID])
			if m.OutputName != "plan" {
				t.Errorf("snapshot output name = %q, want %q", m.OutputName, "plan")
			}
		}
	}
	for iter, body := range want {
		if found[iter] != body {
			t.Errorf("attempt %d snapshot = %q, want %q", iter, found[iter], body)
		}
	}

	// The failing command's whole log is captured too, not just its tail.
	var log string
	for _, m := range fa.metas {
		if m.StageID == "check" && m.Scope == artifacts.ScopeStageLog {
			log = string(fa.blobs[m.ID])
		}
	}
	if !strings.Contains(log, "boom") {
		t.Errorf("check log = %q, want the command's output", log)
	}

	// Every snapshot is linked from the timeline by reference, never by value.
	published := 0
	for _, ev := range h.events(t, runID) {
		if p, ok := ev.Payload.(*event.ArtifactPublished); ok {
			published++
			if p.ArtifactID == "" {
				t.Errorf("artifact_published carries no artifact id: %+v", p)
			}
		}
	}
	if published < 3 { // 2 plan outputs + at least one log
		t.Errorf("artifact_published count = %d, want the outputs and logs of every attempt", published)
	}
}
