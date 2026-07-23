package engine

import (
	"context"
	"strings"
	"testing"
)

// A run created under a ticket gets a unique actual branch = base + per-run
// tail, with the stem preserved as BranchBase — the collision-free, readable
// scheme the ticket model exists for.
func TestTicketedRunDerivesSuffixedBranch(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx,
		Ticket{TicketID: "tk-1", Title: "Add a health endpoint", Branch: "fix/task-123"},
		"loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	v := h.view(t, id)
	tail := strings.ToLower(id)[len(id)-2:]
	if want := "fix/task-123-" + tail; v.Branch != want {
		t.Errorf("branch = %q, want %q", v.Branch, want)
	}
	if v.BranchBase != "fix/task-123" {
		t.Errorf("branch_base = %q, want fix/task-123", v.BranchBase)
	}
	if v.TicketID != "tk-1" {
		t.Errorf("ticket_id = %q, want tk-1", v.TicketID)
	}
}

// An empty base branch on a ticketed run is generated from the title (no run
// id baked in) and still gets the per-run tail.
func TestTicketedRunGeneratesBaseFromTitle(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx,
		Ticket{TicketID: "tk-2", Title: "Add a health endpoint"}, // no branch
		"loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	v := h.view(t, id)
	if v.BranchBase != "apothecary/add-a-health-endpoint" {
		t.Errorf("branch_base = %q, want apothecary/add-a-health-endpoint", v.BranchBase)
	}
	tail := strings.ToLower(id)[len(id)-2:]
	if want := v.BranchBase + "-" + tail; v.Branch != want {
		t.Errorf("branch = %q, want %q", v.Branch, want)
	}
}

// Restart of a ticketed run is a fresh attempt under the SAME ticket: same
// base, same ticket id, a NEW tail (so the two attempts never collide).
func TestRestartTicketedRunKeepsBaseNewTail(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx,
		Ticket{TicketID: "tk-3", Title: "T", Branch: "feat/login"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	orig := h.view(t, id)

	newID, err := h.e.RestartRun(ctx, id, RunInput{})
	if err != nil {
		t.Fatal(err)
	}
	nv := h.view(t, newID)
	if nv.TicketID != "tk-3" {
		t.Errorf("restart ticket_id = %q, want tk-3", nv.TicketID)
	}
	if nv.BranchBase != "feat/login" {
		t.Errorf("restart branch_base = %q, want feat/login", nv.BranchBase)
	}
	if nv.Branch == orig.Branch {
		t.Errorf("restart reused the same branch %q — tails must differ", nv.Branch)
	}
	if want := "feat/login-" + strings.ToLower(newID)[len(newID)-2:]; nv.Branch != want {
		t.Errorf("restart branch = %q, want %q", nv.Branch, want)
	}
}
