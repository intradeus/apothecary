package engine

import (
	"cmp"
	"context"
	"fmt"
	"sync"

	"apothecary/internal/run/event"
)

// ErrTicketBusy refuses a run on a ticket that already has one going: a
// ticket has at most one non-terminal run (pending, provisioning, running,
// waiting on a human, in review or paused). Two runs on one ticket write
// conflicting meanings to its tracker — run A merges and moves the ticket to
// done, then run B's merge request is closed and its outcome moves the same
// ticket back to todo — so the second attempt waits for the first to end, or
// for a person to abort it. Runs without a ticket are exempt.
//
// It is a value the caller takes apart with errors.As: the HTTP API answers
// 409 naming RunID, and the cron automation records the skipped firing.
type ErrTicketBusy struct {
	TicketID string
	RunID    string // the ticket's active run
}

func (e ErrTicketBusy) Error() string {
	return fmt.Sprintf("ticket %s is busy: run %s is still active; abort it or wait for it to end", e.TicketID, e.RunID)
}

// ActiveRunID names the run holding the ticket. It is how a caller that may
// not import the engine (the automation watcher, by the layering rules)
// recognises the refusal: errors.As against interface{ ActiveRunID() string }.
func (e ErrTicketBusy) ActiveRunID() string { return e.RunID }

// ticketLock serializes run creation per ticket: held from the busy check
// until run_created is appended, so two concurrent starts on one ticket
// cannot both see it idle. It is a lock and not a unique index on purpose:
// the runs table is a projection rebuilt from the event log, and older
// history may hold two live runs of one ticket.
func (e *Engine) ticketLock(ticketID string) func() {
	m, _ := e.ticketLocks.LoadOrStore(ticketID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// checkTicketIdle refuses with ErrTicketBusy when the ticket has a
// non-terminal run. The caller holds ticketLock. The read model keeps no
// index by ticket (its rows carry the ticket id inside the view only), so
// this scans every run's row — one query on the database store, the same
// scan RunsOfTicket does — which is cheap next to the preflight that follows.
func (e *Engine) checkTicketIdle(ctx context.Context, ticketID string) error {
	all, err := e.runs.All(ctx)
	if err != nil {
		return fmt.Errorf("ticket %s: listing its runs: %w", ticketID, err)
	}
	for _, v := range all {
		if v.TicketID == ticketID && !v.State.Terminal() {
			return ErrTicketBusy{TicketID: ticketID, RunID: v.RunID}
		}
	}
	return nil
}

// ErrMRBusy refuses a run on a merge request a run is still working on —
// one started on it, or the one that opened it: two runs pushing to one
// branch undo each other. Like ErrTicketBusy it names the run in the way.
type ErrMRBusy struct {
	Repo  string
	IID   int
	RunID string // the merge request's active run
}

func (e ErrMRBusy) Error() string {
	return fmt.Sprintf("merge request !%d is busy: run %s is still working on it; abort it or wait for it to end", e.IID, e.RunID)
}

// ActiveRunID names the run holding the merge request.
func (e ErrMRBusy) ActiveRunID() string { return e.RunID }

// mrLock serializes run creation per merge request, like ticketLock.
func (e *Engine) mrLock(projectID, repo string, iid int) func() {
	repo = cmp.Or(repo, event.PrimaryCheckoutName)
	m, _ := e.ticketLocks.LoadOrStore(fmt.Sprintf("mr\x00%s\x00%s\x00%d", projectID, repo, iid), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// checkMRIdle refuses with ErrMRBusy when a non-terminal run of the
// project has the merge request in its data. The caller holds mrLock.
func (e *Engine) checkMRIdle(ctx context.Context, projectID, repo string, iid int) error {
	runs, err := e.RunsOnMR(ctx, projectID, repo, iid)
	if err != nil {
		return fmt.Errorf("merge request !%d: listing its runs: %w", iid, err)
	}
	for _, v := range runs {
		if !v.State.Terminal() {
			return ErrMRBusy{Repo: repo, IID: iid, RunID: v.RunID}
		}
	}
	return nil
}
