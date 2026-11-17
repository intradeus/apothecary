package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/run/runinput"
	"apothecary/internal/workspace/wsname"
)

const nothingDoc = `name: sandbox
version: 5
operates_on: nothing
parameters: {}
main:
  - { id: work, type: command, run: "true" }
`

// A flow whose runs are started on nothing: a start names no ticket and no
// merge request, the run gets a branch of its own named after it, and any
// number run at once — there is no work item to be busy on.
func TestRunOnNothing(t *testing.T) {
	h := newHarness(t, mustFlow(t, nothingDoc))
	ctx := context.Background()

	first, err := h.e.CreateRunWith(ctx, runinput.Ticket{Title: "manual run - 2026-11-17 18:40"}, "sandbox", testProject,
		runinput.Input{Automation: "auto-1", By: "automation"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "manual run - 2026-11-17 18:40"}, "sandbox", testProject)
	if err != nil {
		t.Fatalf("a second run beside the first: %v", err)
	}
	h.e.Wait(first)
	h.e.Wait(second)
	v := h.view(t, first)
	if v.TicketID != "" || v.Adopted != nil || v.BranchBase != "apothecary/manual-run-2026-11-17-18-40" ||
		v.Branch != v.BranchBase+"-"+wsname.RunIDTail(first) || v.SourceBranch != "main" {
		t.Errorf("run = branch %q base %q source %q ticket %q", v.Branch, v.BranchBase, v.SourceBranch, v.TicketID)
	}
	if h.view(t, second).Branch == v.Branch {
		t.Errorf("two runs of one name share the branch %q", v.Branch)
	}
	// Which automation created it rides on the run: what a schedule asks
	// before starting another.
	mine, err := h.e.AutomationRuns(ctx, "auto-1")
	if err != nil || len(mine) != 1 || mine[0].RunID != first || v.AutomationID != "auto-1" {
		t.Errorf("automation runs = %+v, %v", mine, err)
	}
	if none, _ := h.e.AutomationRuns(ctx, ""); len(none) != 0 {
		t.Errorf("runs of no automation = %d", len(none))
	}

	// A ticket's work is a ticket flow's, a merge request's a merge request
	// flow's: neither starts this one.
	_, err = h.e.CreateRun(ctx, runinput.Ticket{TicketID: "tk-1", Title: "T"}, "sandbox", testProject)
	if err == nil || !strings.Contains(err.Error(), `starts its runs on nothing (operates_on: nothing), not on a ticket`) {
		t.Errorf("on a ticket: %v", err)
	}
	_, err = h.e.CreateRunWith(ctx, runinput.Ticket{Title: "T"}, "sandbox", testProject, runinput.Input{MR: &runinput.MR{IID: 7}})
	if err == nil || !strings.Contains(err.Error(), `not on a merge request`) {
		t.Errorf("on a merge request: %v", err)
	}
}

// A start may name the branch it works on — the one it starts from, itself:
// nothing is forked, the workspace switches to it, and a rerun stays on it.
func TestRunOnNothingOnTheBranchItself(t *testing.T) {
	h := newHarness(t, mustFlow(t, nothingDoc))
	ctx := context.Background()
	id, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "poke at release", Branch: "release/1.x", SourceBranch: "release/1.x"}, "sandbox", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	v := h.view(t, id)
	if v.Branch != "release/1.x" || v.SourceBranch != "release/1.x" || len(v.Checkouts) == 0 ||
		v.Checkouts[0].Branch != "release/1.x" || v.Checkouts[0].Source != "release/1.x" {
		t.Fatalf("run = branch %q source %q checkouts %+v", v.Branch, v.SourceBranch, v.Checkouts)
	}
	again, err := h.e.Rerun(ctx, id, runinput.Input{})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(again)
	if nv := h.view(t, again); nv.Branch != "release/1.x" || nv.SourceBranch != "release/1.x" || nv.TicketTitle != "poke at release" {
		t.Errorf("rerun = branch %q source %q title %q", nv.Branch, nv.SourceBranch, nv.TicketTitle)
	}
}

// A start may name its fresh branch: used as written, forked from the source
// branch, and a rerun keeps it.
func TestRunOnNothingOnANamedBranch(t *testing.T) {
	h := newHarness(t, mustFlow(t, nothingDoc))
	ctx := context.Background()
	id, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "poke at the cache", Branch: "spike/cache", SourceBranch: "dev"}, "sandbox", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	v := h.view(t, id)
	if v.Branch != "spike/cache" || v.SourceBranch != "dev" || len(v.Checkouts) == 0 ||
		v.Checkouts[0].Branch != "spike/cache" || v.Checkouts[0].Source != "dev" {
		t.Fatalf("run = branch %q source %q checkouts %+v", v.Branch, v.SourceBranch, v.Checkouts)
	}
	again, err := h.e.Rerun(ctx, id, runinput.Input{})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(again)
	if nv := h.view(t, again); nv.Branch != "spike/cache" || nv.SourceBranch != "dev" {
		t.Errorf("rerun = branch %q source %q", nv.Branch, nv.SourceBranch)
	}
}

// A name with nothing a branch can carry still gets one.
func TestRunOnNothingWithAnUnsluggableName(t *testing.T) {
	h := newHarness(t, mustFlow(t, nothingDoc))
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "¿?"}, "sandbox", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if b := h.view(t, id).Branch; b != "apothecary/run-"+wsname.RunIDTail(id) {
		t.Errorf("branch = %q", b)
	}
}
