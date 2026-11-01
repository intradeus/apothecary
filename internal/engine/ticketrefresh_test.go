package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
)

// refreshes records the tickets the engine asked to refresh.
type refreshes struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (r *refreshes) Refresh(_ context.Context, ticketID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, ticketID)
	return r.err
}

func (r *refreshes) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ids)
}

func refreshHarness(t *testing.T, doc string, rec *refreshes) *harness {
	t.Helper()
	return newHarnessWith(t, func(_ *harness, d *Deps) { d.Refresher = rec }, mustFlow(t, doc))
}

// A ticket stage that wrote to a remote issue refreshes the ticket's mirror,
// and a failed refresh fails nothing.
func TestRemoteTicketActionRefreshesTheTicket(t *testing.T) {
	rec := &refreshes{err: errors.New("tracker down")}
	h := refreshHarness(t, transitionFirstDoc, rec)
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s", v.State)
	}
	if got := rec.got(); !slices.Equal(got, []string{"tk-1"}) {
		t.Errorf("refreshed = %v, want tk-1 once", got)
	}
}

// The internal tracker wrote the row itself: nothing to refresh.
func TestInternalTicketActionDoesNotRefresh(t *testing.T) {
	rec := &refreshes{}
	h := refreshHarness(t, strings.Replace(transitionFirstDoc, "to: in_progress", "to: done", 1), rec)
	h.tickets.handWritten("tk-2")
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{TicketID: "tk-2", Title: "T"}, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s", v.State)
	}
	if got := rec.got(); len(got) != 0 {
		t.Errorf("refreshed = %v, want none", got)
	}
}

// rowTickets serves a ticket's current row, as the ticket factory does; the
// row changes when the ticket is refreshed (a remote edit landing).
type rowTickets struct {
	mu  sync.Mutex
	row runinput.Ticket
}

func (r *rowTickets) RunTicket(_ context.Context, id string) (runinput.Ticket, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.row.TicketID {
		return runinput.Ticket{}, false, nil
	}
	return r.row, true, nil
}

func (r *rowTickets) TicketNow(context.Context, string) (runinput.TicketNow, bool, error) {
	return runinput.TicketNow{}, false, nil
}

// refreshThen is a refresher that applies a remote edit to the row.
type refreshThen struct {
	refreshes
	apply func()
}

func (r *refreshThen) Refresh(ctx context.Context, id string) error {
	r.apply()
	return r.refreshes.Refresh(ctx, id)
}

// A run on a remote ticket starts from the ticket as its tracker has it now:
// refreshed first, the mirrored fields re-read; the caller's own fields stay.
// A ticket with no source is not refreshed.
func TestRunStartRefreshesARemoteTicket(t *testing.T) {
	rows := &rowTickets{row: runinput.Ticket{TicketID: "tk-1", Title: "Old title", Goal: "old", SourceID: "int-1",
		SourceKind: "fake", SourceKey: "DEV-1", SourceURL: "https://tracker.local/browse/DEV-1"}}
	rec := &refreshThen{apply: func() {
		rows.mu.Lock()
		rows.row.Title, rows.row.Goal = "Reopened upstream", "new goal"
		rows.mu.Unlock()
	}}
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Refresher, d.Tickets = rec, rows }, mustFlow(t, transitionFirstDoc))
	in := linkedTicket
	in.Title, in.RunGoal = "Old title", "this attempt"
	runID, err := h.e.CreateRun(context.Background(), in, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	v := h.view(t, runID)
	if v.TicketTitle != "Reopened upstream" || v.TicketGoal != "new goal" {
		t.Errorf("run started from %q / %q, want the refreshed ticket", v.TicketTitle, v.TicketGoal)
	}
	if got := rec.got(); len(got) == 0 || got[0] != "tk-1" {
		t.Errorf("refreshed = %v", got)
	}

	local := &refreshes{}
	h2 := newHarnessWith(t, func(_ *harness, d *Deps) {
		d.Refresher, d.Tickets = local, &rowTickets{row: runinput.Ticket{TicketID: "tk-2", Title: "T"}}
	}, mustFlow(t, strings.Replace(transitionFirstDoc, "to: in_progress", "to: done", 1)))
	h2.tickets.handWritten("tk-2")
	runID, err = h2.e.CreateRun(context.Background(), runinput.Ticket{TicketID: "tk-2", Title: "T"}, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h2.e.Wait(runID)
	if got := local.got(); len(got) != 0 {
		t.Errorf("a sourceless ticket was refreshed: %v", got)
	}
}
