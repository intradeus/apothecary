package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/artifacts"
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
