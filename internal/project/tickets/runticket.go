package tickets

import (
	"context"
	"errors"

	"apothecary/internal/project/confstore"
	"apothecary/internal/run/runinput"
)

// RunTicket reads a ticket's current row as what a run of it starts from
// (engine.TicketReader): title, goal, change type, base branch, the ticket's
// own attachments and the tracker issue it mirrors. A rerun asks it so the
// new attempt works on the ticket as it is now, not on the old run's copy.
// found=false is a ticket that no longer exists. The per-attempt fields
// (source branch, run goal, run attachments) are left to the caller.
//
// An attachment listing that fails is logged and the run starts without the
// ticket's files, as a run started from the ticket page does.
func (f *Factory) RunTicket(ctx context.Context, ticketID string) (runinput.Ticket, bool, error) {
	t, err := f.Conf.GetTicket(ctx, ticketID)
	if errors.Is(err, confstore.ErrNotFound) {
		return runinput.Ticket{}, false, nil
	}
	if err != nil {
		return runinput.Ticket{}, false, err
	}
	var attIDs []string
	if f.Attachments != nil {
		atts, err := f.Attachments.ForTicket(ctx, t.ID)
		if err != nil {
			f.log().Warn("ticket attachments", "ticket", t.ID, "err", err)
		}
		for _, a := range atts {
			attIDs = append(attIDs, a.ID)
		}
	}
	return runinput.Ticket{
		TicketID:            t.ID,
		Title:               t.Title,
		Goal:                t.Goal,
		ChangeType:          string(t.ChangeType),
		Branch:              t.BaseBranch,
		TicketAttachmentIDs: attIDs,
		SourceID:            t.SourceID,
		SourceKind:          t.SourceKind,
		SourceKey:           t.SourceKey,
		SourceURL:           t.SourceURL,
	}, true, nil
}

// TicketNow reads what an agent's prompt shows of a ticket beside the run's
// copy (engine.TicketReader): its key, the name of its status, its labels
// and its comments, oldest first, each marked Own when apothecary posted it
// to the internal tracker. found=false is a ticket that no longer exists.
func (f *Factory) TicketNow(ctx context.Context, ticketID string) (runinput.TicketNow, bool, error) {
	t, err := f.Conf.GetTicket(ctx, ticketID)
	if errors.Is(err, confstore.ErrNotFound) {
		return runinput.TicketNow{}, false, nil
	}
	if err != nil {
		return runinput.TicketNow{}, false, err
	}
	now := runinput.TicketNow{Key: t.SourceKey, Labels: t.Labels}
	if t.StatusID != "" {
		trackerID, err := f.Conf.TicketTrackerID(ctx, t)
		if err != nil {
			return runinput.TicketNow{}, false, err
		}
		statuses, err := f.Conf.TrackerStatuses(ctx, trackerID)
		if err != nil {
			return runinput.TicketNow{}, false, err
		}
		for _, s := range statuses {
			if s.ID == t.StatusID {
				now.Status = s.Name
				break
			}
		}
	}
	comments, err := f.Conf.TicketComments(ctx, t.ID)
	if err != nil {
		return runinput.TicketNow{}, false, err
	}
	now.Comments = make([]runinput.TicketComment, 0, len(comments))
	for _, c := range comments {
		now.Comments = append(now.Comments, runinput.TicketComment{
			Author: c.Author, Body: c.Body, At: c.CreatedAt, Own: c.Origin == confstore.CommentApothecary,
		})
	}
	return now, true, nil
}
