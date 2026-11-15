package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/run/runinput"
)

// workshopTickets serves a ticket as tickets.Factory does: the issue as its
// tracker has it now, its base branch rendered with the asking workshop's
// branch format. It records the workshop every read was for.
type workshopTickets struct {
	mu       sync.Mutex
	found    bool
	err      error
	projects []string
}

func (w *workshopTickets) RunTicket(_ context.Context, ticketID, projectID string) (runinput.Ticket, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.projects = append(w.projects, projectID)
	return runinput.Ticket{TicketID: ticketID, Title: "Reopened upstream", Goal: "new goal", ChangeType: "fix",
		Branch: "fix/" + projectID + "-dev-1", TicketAttachmentIDs: []string{"att-copied"},
		SourceID: "int-1", SourceKind: "fake", SourceKey: "DEV-1", SourceURL: "https://tracker.local/browse/DEV-1",
	}, w.found, w.err
}

func (w *workshopTickets) TicketNow(context.Context, string) (runinput.TicketNow, bool, error) {
	return runinput.TicketNow{}, false, nil
}

func (w *workshopTickets) asked() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.projects)
}

// A run on a ticket starts from the ticket as the reader has it now: its
// title, goal, change type, issue and files over the caller's copy, the caller's
// per-attempt fields kept. Started without a branch, it takes the reader's,
// rendered for the run's workshop — two workshops, two names — and the
// reader is asked for exactly that workshop.
func TestRunStartReadsTheTicketForItsWorkshop(t *testing.T) {
	const otherWorkshop = "prj-other"
	reader := &workshopTickets{found: true}
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Tickets = reader }, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	for _, project := range []string{testProject, otherWorkshop} {
		in := runinput.Ticket{TicketID: "tk-1", Title: "Old title", Goal: "old", RunGoal: "this attempt"}
		id, err := h.e.CreateRun(ctx, in, "loop", project)
		if err != nil {
			t.Fatal(err)
		}
		h.e.Wait(id)
		v := h.view(t, id)
		if v.TicketTitle != "Reopened upstream" || v.TicketGoal != "new goal" || v.TicketChangeType != "fix" ||
			v.TicketSourceID != "int-1" || v.TicketKey != "DEV-1" || v.RunGoal != "this attempt" ||
			!slices.Equal(v.TicketAttachmentIDs, []string{"att-copied"}) {
			t.Errorf("run in %s started from %+v, want the reader's ticket and the caller's run goal", project, v)
		}
		if want := "fix/" + project + "-dev-1"; v.BranchBase != want {
			t.Errorf("run in %s: branch base %q, want %q", project, v.BranchBase, want)
		}
	}
	if got := reader.asked(); !slices.Equal(got, []string{testProject, otherWorkshop}) {
		t.Errorf("reader asked for workshops %v", got)
	}
}

// A branch the caller gives wins over the reader's.
func TestRunStartKeepsTheCallersBranch(t *testing.T) {
	reader := &workshopTickets{found: true}
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Tickets = reader }, mustFlow(t, loopFlowDoc))
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{TicketID: "tk-1", Title: "T", Branch: "feat/mine"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if v := h.view(t, id); v.BranchBase != "feat/mine" || v.TicketTitle != "Reopened upstream" {
		t.Errorf("branch base %q, title %q: want the caller's branch and the reader's title", v.BranchBase, v.TicketTitle)
	}
}

// A ticket the reader cannot read, or no longer has, never refuses the run:
// it starts from the caller's copy.
func TestRunStartFallsBackToTheCallersTicket(t *testing.T) {
	for name, reader := range map[string]*workshopTickets{
		"read fails": {found: true, err: errors.New("tracker down")},
		"not found":  {found: false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Tickets = reader }, mustFlow(t, loopFlowDoc))
			id, err := h.e.CreateRun(context.Background(), runinput.Ticket{TicketID: "tk-1", Title: "Old title", Goal: "old"}, "loop", testProject)
			if err != nil {
				t.Fatalf("run refused: %v", err)
			}
			h.e.Wait(id)
			v := h.view(t, id)
			if v.TicketTitle != "Old title" || v.TicketGoal != "old" || v.TicketSourceID != "" {
				t.Errorf("run started from %+v, want the caller's copy", v)
			}
			if strings.HasPrefix(v.BranchBase, "fix/") {
				t.Errorf("branch base %q came from the failed read", v.BranchBase)
			}
			if got := reader.asked(); !slices.Equal(got, []string{testProject}) {
				t.Errorf("reader asked for workshops %v", got)
			}
		})
	}
}
