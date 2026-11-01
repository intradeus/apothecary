package engine

import (
	"context"
	"time"

	"apothecary/internal/run/runinput"
)

// A ticket with a source mirrors a remote issue (docs/ticket-sync-plan.md §
// Sync). Besides the daemon's periodic poll, the engine refreshes that mirror
// at the moments its freshness matters: after a run's ticket_* stage wrote to
// the remote tracker (so the ticket page shows the move at once), before a
// prompt reads the ticket's comments, and when a run starts on the ticket
// (the one moment a done ticket's mirror — reopened remotely — matters).
// A refresh is best-effort: a tracker that is down or slow never fails a
// stage or refuses a run; the failure is logged and recorded on the ticket
// (sync_state) by the refresher.

// TicketRefresher re-mirrors one ticket from its remote tracker
// (tickets.Syncer.Refresh). A ticket with no source is a no-op.
type TicketRefresher interface {
	Refresh(ctx context.Context, ticketID string) error
}

// refreshTimeout bounds one refresh, so a hanging tracker delays a stage or a
// run start by at most this long.
const refreshTimeout = 20 * time.Second

// refreshTicket re-mirrors the ticket from its remote tracker. Best-effort:
// logs a failure, never returns one. No-op without a ticket or a refresher.
func (e *Engine) refreshTicket(ctx context.Context, ticketID string) {
	if ticketID == "" || e.refresher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	if err := e.refresher.Refresh(ctx, ticketID); err != nil {
		e.log.Warn("ticket refresh failed", "ticket", ticketID, "err", err)
	}
}

// freshRunTicket is the ticket a run starts from, re-read after a refresh
// when the ticket mirrors a remote issue: the caller built t from the row as
// it stood, and the remote may have moved on since (a reopened issue, an
// edited description). Only the mirrored fields are taken from the refreshed
// row — title, goal, change type and the issue's key and URL; the branch and
// the per-attempt fields stay the caller's. A ticket with no source, a build
// without a refresher, or a failed read starts from t unchanged.
func (e *Engine) freshRunTicket(ctx context.Context, t runinput.Ticket) runinput.Ticket {
	if e.refresher == nil || e.tickets == nil {
		return t
	}
	cur, found, err := e.tickets.RunTicket(ctx, t.TicketID)
	if err != nil || !found || cur.SourceID == "" {
		return t
	}
	e.refreshTicket(ctx, t.TicketID)
	if cur, found, err = e.tickets.RunTicket(ctx, t.TicketID); err != nil || !found {
		return t
	}
	t.Title, t.Goal, t.ChangeType = cur.Title, cur.Goal, cur.ChangeType
	t.SourceID, t.SourceKind, t.SourceKey, t.SourceURL = cur.SourceID, cur.SourceKind, cur.SourceKey, cur.SourceURL
	return t
}
