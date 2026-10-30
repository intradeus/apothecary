package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/event"
	"apothecary/internal/runinput"
)

func TestCheckBranch(t *testing.T) {
	valid := []string{
		"feat/PROJ-123-login-form",
		"fix/issue_42.hotfix",
		"a/b/c",
		"release-1.0",
	}
	for _, s := range valid {
		if err := CheckBranch(s); err != nil {
			t.Errorf("CheckBranch(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{
		"/leading", "trailing/", "a//b",
		"a..b", "a@{b",
		"has space", "tab\there",
		"seg/.hidden", "ends.", "x.lock",
		"car^et", "col:on", "ast*erisk", "back\\slash",
	}
	for _, s := range invalid {
		if err := CheckBranch(s); err == nil {
			t.Errorf("CheckBranch(%q) = nil, want error", s)
		}
	}
}

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
