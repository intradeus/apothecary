package engine

import (
	"context"
	"fmt"

	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
)

// TicketReader reads a ticket as it is now. RunTicket is what a run of it in
// workshop projectID starts from: title, goal, change type, base branch, the
// ticket's attachments and its tracker issue — the remote issue read live,
// falling back to the cached copy and then to the ticket's record, and a
// ticket with no base branch of its own given one rendered with that
// workshop's repo.branch_format. Run starts (freshRunTicket) and reruns ask
// it so an attempt works on the ticket as it is now — retitled, re-scoped,
// re-typed or relinked since — rather than on a stale copy. TicketNow is
// what a prompt shows beside the run's copy: the key, status, labels and
// comments as they are when the prompt is built, read live with a cache
// fallback. found=false is a deleted ticket. Implemented by tickets.Factory.
type TicketReader interface {
	RunTicket(ctx context.Context, ticketID, projectID string) (t runinput.Ticket, found bool, err error)
	TicketNow(ctx context.Context, ticketID string) (t runinput.TicketNow, found bool, err error)
}

// rerunTicket is the ticket a rerun of v starts from: the ticket as it is
// now, in v's workshop, when a TicketReader is wired and the ticket still
// exists, else the old run's copy (run_created's ticket fields). A failing
// read refuses the rerun rather than silently starting from a stale copy.
// Per-attempt fields (source branch, run goal, run attachments) are the
// caller's.
func (e *Engine) rerunTicket(ctx context.Context, v run.View) (runinput.Ticket, error) {
	if e.tickets != nil {
		t, found, err := e.tickets.RunTicket(ctx, v.TicketID, v.ProjectID)
		if err != nil {
			return runinput.Ticket{}, fmt.Errorf("ticket %s: %w", v.TicketID, err)
		}
		if found {
			t.TicketID = v.TicketID
			return t, nil
		}
	}
	return runinput.Ticket{
		TicketID: v.TicketID, Title: v.TicketTitle, Goal: v.TicketGoal, Branch: v.BranchBase,
		ChangeType: v.TicketChangeType,
		// The ticket's own attachments carry over to every attempt, and so
		// does its tracker issue.
		TicketAttachmentIDs: v.TicketAttachmentIDs,
		SourceID:            v.TicketSourceID,
		SourceKind:          v.TicketSourceKind,
		SourceKey:           v.TicketKey,
		SourceURL:           v.TicketURL,
	}, nil
}
