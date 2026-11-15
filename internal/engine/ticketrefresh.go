package engine

import (
	"context"

	"apothecary/internal/run/runinput"
)

// A remote ticket is not mirrored: its issue is read live when a run starts
// on it (TicketReader.RunTicket), so the run works on the issue as it is now
// — a reopened issue, an edited description. A tracker problem never refuses
// the run: the reader falls back to its cache, then to the ticket's record,
// and a failed read leaves the caller's copy as it is.

// freshRunTicket is the ticket a run in projectID starts from: the reader's
// title, goal, change type, issue (key, URL) and the ticket's own files — the
// reader copies the ones a remote issue gained — over the caller's copy,
// which was built from the record as it stood. The branch is the caller's
// when it gave one, else the reader's — the ticket's base branch, or one
// rendered with the workshop's repo.branch_format, which is why the reader is
// told the workshop. The per-attempt fields stay the caller's. An engine
// without a reader, a deleted ticket or a failed read starts from t
// unchanged.
func (e *Engine) freshRunTicket(ctx context.Context, t runinput.Ticket, projectID string) runinput.Ticket {
	if e.tickets == nil {
		return t
	}
	cur, found, err := e.tickets.RunTicket(ctx, t.TicketID, projectID)
	if err != nil {
		e.log.WarnContext(ctx, "reading the ticket a run starts from", "ticket", t.TicketID, "err", err)
		return t
	}
	if !found {
		return t
	}
	t.Title, t.Goal, t.ChangeType = cur.Title, cur.Goal, cur.ChangeType
	t.SourceID, t.SourceKind, t.SourceKey, t.SourceURL = cur.SourceID, cur.SourceKind, cur.SourceKey, cur.SourceURL
	t.TicketAttachmentIDs = cur.TicketAttachmentIDs
	if t.Branch == "" {
		t.Branch = cur.Branch
	}
	return t
}
