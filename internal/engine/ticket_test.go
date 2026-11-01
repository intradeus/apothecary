package engine

import (
	"context"
	"testing"

	"apothecary/internal/run/runinput"
	"apothecary/internal/workspace/wsname"
)

// A run created under a ticket gets a unique actual branch = base + per-run
// tail, with the stem preserved as BranchBase — the collision-free, readable
// scheme the ticket model exists for.
func TestTicketedRunDerivesSuffixedBranch(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx,
		runinput.Ticket{TicketID: "tk-1", Title: "Add a health endpoint", Branch: "fix/task-123"},
		"loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id) // to its end: the golden log must not depend on when the test stopped
	v := h.view(t, id)
	tail := wsname.RunIDTail(id)
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
		runinput.Ticket{TicketID: "tk-2", Title: "Add a health endpoint"}, // no branch
		"loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	v := h.view(t, id)
	if v.BranchBase != "apothecary/add-a-health-endpoint" {
		t.Errorf("branch_base = %q, want apothecary/add-a-health-endpoint", v.BranchBase)
	}
	tail := wsname.RunIDTail(id)
	if want := v.BranchBase + "-" + tail; v.Branch != want {
		t.Errorf("branch = %q, want %q", v.Branch, want)
	}
}

// A run's source branch (the base it forks off from) is per-run: an empty one
// defaults to the project's default branch, an explicit one is kept verbatim,
// and a restart carries the original run's source branch forward.
func TestRunSourceBranch(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	// Empty ⇒ project default (testProject's repo default_branch is "main").
	def, err := h.e.CreateRun(ctx, runinput.Ticket{TicketID: "tk-s1", Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	if v := h.view(t, def); v.SourceBranch != "main" {
		t.Errorf("default source_branch = %q, want main", v.SourceBranch)
	}

	// Explicit source branch is kept.
	exp, err := h.e.CreateRun(ctx,
		runinput.Ticket{TicketID: "tk-s2", Title: "T", SourceBranch: "develop"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	if v := h.view(t, exp); v.SourceBranch != "develop" {
		t.Errorf("explicit source_branch = %q, want develop", v.SourceBranch)
	}

	// Restart carries the original source branch forward.
	h.e.Wait(exp)
	newID, err := h.e.Rerun(ctx, exp, runinput.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if v := h.view(t, newID); v.SourceBranch != "develop" {
		t.Errorf("restart source_branch = %q, want develop", v.SourceBranch)
	}

	// An invalid source branch is rejected.
	if _, err := h.e.CreateRun(ctx,
		runinput.Ticket{TicketID: "tk-s3", Title: "T", SourceBranch: "bad branch"}, "loop", testProject); err == nil {
		t.Error("expected invalid source branch to be rejected")
	}
}

// Restart of a ticketed run is a fresh attempt under the SAME ticket: same
// base, same ticket id, a NEW tail (so the two attempts never collide).
func TestRerunTicketedRunKeepsBaseNewTail(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	id, err := h.e.CreateRun(ctx,
		runinput.Ticket{TicketID: "tk-3", Title: "T", Branch: "feat/login"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	orig := h.view(t, id)

	newID, err := h.e.Rerun(ctx, id, runinput.Input{})
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
	if want := "feat/login-" + wsname.RunIDTail(newID); nv.Branch != want {
		t.Errorf("restart branch = %q, want %q", nv.Branch, want)
	}
}
