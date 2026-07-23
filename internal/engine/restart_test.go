package engine

import (
	"context"
	"strings"
	"testing"
)

// A user-chosen branch (fix/task-123-…) must survive a restart verbatim —
// restart re-derived it as apothecary/<new-id>-<slug> before, which also
// mangled the branch-slug-derived container name.
func TestRestartPreservesUserBranch(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx,
		Ticket{Title: "Add a health endpoint", Branch: "fix/task-123-add-a-health-endpoint"},
		"loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if b := h.view(t, id).Branch; b != "fix/task-123-add-a-health-endpoint" {
		t.Fatalf("setup: original branch = %q", b)
	}

	newID, err := h.e.RestartRun(ctx, id, RunInput{})
	if err != nil {
		t.Fatal(err)
	}
	if newID == id {
		t.Fatal("restart must mint a new run id")
	}
	if b := h.view(t, newID).Branch; b != "fix/task-123-add-a-health-endpoint" {
		t.Errorf("restart branch = %q, want the original preserved", b)
	}
}

// An auto-generated branch, by contrast, must NOT carry the old run's id into
// the new run — CreateRun mints a fresh apothecary/<new-id>-<slug>.
func TestRestartRegeneratesAutoBranch(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx, Ticket{Title: "Add health"}, "loop", testProject) // no branch → generated
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	orig := h.view(t, id).Branch
	if !strings.HasPrefix(orig, "apothecary/"+strings.ToLower(id)+"-") {
		t.Fatalf("setup: expected generated branch, got %q", orig)
	}

	newID, err := h.e.RestartRun(ctx, id, RunInput{})
	if err != nil {
		t.Fatal(err)
	}
	nb := h.view(t, newID).Branch
	if nb == orig {
		t.Errorf("auto branch not regenerated: still carries the old run id (%q)", nb)
	}
	if !strings.HasPrefix(nb, "apothecary/"+strings.ToLower(newID)+"-") {
		t.Errorf("new branch %q should be freshly generated for the new run id", nb)
	}
}
