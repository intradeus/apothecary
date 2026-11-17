package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workspace/wsname"
)

// A user-chosen branch (fix/task-123-…) must survive a restart verbatim —
// restart re-derived it as apothecary/<new-id>-<slug> before, which also
// mangled the branch-slug-derived container name.
func TestRerunPreservesUserBranch(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx,
		runinput.Ticket{Title: "Add a health endpoint", Branch: "fix/task-123-add-a-health-endpoint"},
		"loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if b := h.view(t, id).Branch; b != "fix/task-123-add-a-health-endpoint" {
		t.Fatalf("setup: original branch = %q", b)
	}

	newID, err := h.e.Rerun(ctx, id, runinput.Input{})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(newID) // to its end: the golden log must not depend on when the test stopped
	if newID == id {
		t.Fatal("restart must mint a new run id")
	}
	if b := h.view(t, newID).Branch; b != "fix/task-123-add-a-health-endpoint" {
		t.Errorf("restart branch = %q, want the original preserved", b)
	}
}

// An auto-generated branch, by contrast, must NOT carry the old run into
// the new one — CreateRun names a fresh apothecary/<slug>-<new run's tail>.
func TestRerunRegeneratesAutoBranch(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "Add health"}, "loop", testProject) // no branch → generated
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	orig := h.view(t, id)
	if orig.BranchBase != "apothecary/add-health" || orig.Branch != "apothecary/add-health-"+wsname.RunIDTail(id) {
		t.Fatalf("setup: expected a generated branch, got %q (base %q)", orig.Branch, orig.BranchBase)
	}

	newID, err := h.e.Rerun(ctx, id, runinput.Input{})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(newID) // to its end: the golden log must not depend on when the test stopped
	nb := h.view(t, newID).Branch
	if nb == orig.Branch {
		t.Errorf("auto branch not regenerated: still the old run's (%q)", nb)
	}
	if nb != "apothecary/add-health-"+wsname.RunIDTail(newID) {
		t.Errorf("new branch %q should be freshly generated for the new run", nb)
	}
}

// A run recorded before branches took the run's tail carried its whole id:
// that one too is named again, not reused.
func TestRerunRegeneratesALegacyAutoBranch(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	old := "01LEGACY0000000000000000AA"
	if err := h.e.AppendEvents(ctx, old,
		&event.RunCreated{TicketTitle: "Add health", FlowName: "loop", ProjectID: testProject,
			Branch: "apothecary/" + strings.ToLower(old) + "-add-health", SourceBranch: "main"},
		&event.RunAbandoned{Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	newID, err := h.e.Rerun(ctx, old, runinput.Input{})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(newID)
	if nb := h.view(t, newID).Branch; nb != "apothecary/add-health-"+wsname.RunIDTail(newID) {
		t.Errorf("new branch = %q", nb)
	}
}
