package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// A ticket's free-text branch overrides the generated name end to end:
// run_created, the provisioned workspace, and the MR-side state all carry it.
func TestCreateRunCustomBranch(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))

	runID, err := h.e.CreateRun(context.Background(),
		runinput.Ticket{Title: "T", Branch: "feat/PROJ-123-login"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	rc, ok := findPayload[*event.RunCreated](h.events(t, runID))
	if !ok {
		t.Fatal("no run_created")
	}
	if rc.Branch != "feat/PROJ-123-login" {
		t.Errorf("branch = %q, want the ticket's own", rc.Branch)
	}

	// Invalid names are refused before any event is written.
	if _, err := h.e.CreateRun(context.Background(),
		runinput.Ticket{Title: "T", Branch: "bad name"}, "loop", testProject); err == nil {
		t.Error("CreateRun accepted an invalid branch")
	}

	// Empty keeps the generated format.
	runID2, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "Add login"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID2)
	rc2, _ := findPayload[*event.RunCreated](h.events(t, runID2))
	if !strings.HasPrefix(rc2.Branch, "apothecary/") || !strings.Contains(rc2.Branch, "add-login") {
		t.Errorf("generated branch = %q", rc2.Branch)
	}
}
