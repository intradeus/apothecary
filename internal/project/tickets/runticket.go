package tickets

import (
	"context"
	"errors"
	"strings"

	"apothecary/internal/project/branchname"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run/runinput"
)

// RunTicket is what a run of the ticket in workshop projectID starts from
// (engine.TicketReader): title, goal, change type, base branch, the ticket's
// own attachments and, for a remote issue, its key and URL. A rerun asks it
// too, so the new attempt works on the ticket as it is now, not on the old
// run's copy. found=false is a ticket that no longer exists. The per-attempt
// fields (source branch, run goal, run attachments) are left to the caller.
//
// A remote issue is read live (bypassing the cache); when its tracker cannot
// be read the run starts from the cached copy, else from what the record last
// saw — a tracker problem never refuses a run. A live read is written to the
// record (Seen), and the files the issue gained are copied (copyIssueFiles).
//
// The base branch is the user's when they set one, else named by the
// workshop's branch format (package branchname) from the change type, the
// issue's key and the title — so two workshops sharing a source each name the
// branch their way.
//
// An attachment listing that fails is logged and the run starts without the
// ticket's files, as a run started from the ticket page does. The run's copy
// of an internal ticket carries no source fields: run.ticket.tracker and the
// analytics tracker kind mean a remote tracker.
func (f *Factory) RunTicket(ctx context.Context, ticketID, projectID string) (runinput.Ticket, bool, error) {
	t, err := f.Conf.GetTicket(ctx, ticketID)
	if errors.Is(err, confstore.ErrNotFound) {
		return runinput.Ticket{}, false, nil
	}
	if err != nil {
		return runinput.Ticket{}, false, err
	}
	if !t.Internal() {
		t = f.seeAtRunStart(ctx, t)
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
	out := runinput.Ticket{
		TicketID:            t.ID,
		Title:               t.Title,
		Goal:                t.Goal,
		ChangeType:          string(t.ChangeType),
		Branch:              f.baseBranch(ctx, t, projectID),
		TicketAttachmentIDs: attIDs,
	}
	if !t.Internal() {
		out.SourceID, out.SourceKind, out.SourceKey, out.SourceURL = t.SourceID, t.SourceKind, t.SourceKey, t.SourceURL
	}
	return out, true, nil
}

// seeAtRunStart reads a record's issue now and writes what it shows to the
// record, copying the files it gained; the record as it was when the tracker
// can be read neither now nor from the cache.
func (f *Factory) seeAtRunStart(ctx context.Context, t confstore.Ticket) confstore.Ticket {
	if f.Live == nil {
		return t
	}
	log := f.log().With("ticket", t.ID, "key", t.SourceKey)
	issue, err := f.Live.IssueOf(ctx, t, true)
	if err != nil {
		log.Warn("run start: the ticket's tracker cannot be read; starting from what was last seen", "err", err)
		return t
	}
	if cur, err := f.Seen(ctx, t.ID, issue); err != nil {
		log.Warn("run start: record what the tracker shows", "err", err)
	} else {
		t = cur
	}
	if f.Sources != nil {
		src, err := f.Sources.TicketSource(ctx, t.SourceID)
		if err != nil {
			log.Warn("run start: the ticket's files", "err", err)
			return t
		}
		f.copyIssueFiles(ctx, src, t, issue)
	}
	return t
}

// baseBranch is the ticket's base branch for a run in workshop projectID: the
// user's, else rendered with that workshop's branch format (the default one
// when the workshop cannot be read).
func (f *Factory) baseBranch(ctx context.Context, t confstore.Ticket, projectID string) string {
	if strings.TrimSpace(t.BaseBranch) != "" {
		return t.BaseBranch
	}
	format := ""
	if p, err := f.Conf.GetProject(ctx, projectID); err == nil {
		format = p.Repo.BranchFormat
	} else {
		f.log().Warn("run start: the workshop's branch format", "ticket", t.ID, "project", projectID, "err", err)
	}
	key := ""
	if !t.Internal() {
		key = t.SourceKey
	}
	return branchname.Render(format, string(t.ChangeType), key, t.Title)
}

// TicketNow reads what an agent's prompt shows of a ticket beside the run's
// copy (engine.TicketReader): its key, the name of its status, its labels
// and its comments, oldest first, each marked Own when apothecary posted it
// to the internal tracker. A remote issue is read now, falling back to the
// cache; with neither, the record's labels and no status or comments.
// found=false is a ticket that no longer exists.
func (f *Factory) TicketNow(ctx context.Context, ticketID string) (runinput.TicketNow, bool, error) {
	t, err := f.Conf.GetTicket(ctx, ticketID)
	if errors.Is(err, confstore.ErrNotFound) {
		return runinput.TicketNow{}, false, nil
	}
	if err != nil {
		return runinput.TicketNow{}, false, err
	}
	if !t.Internal() {
		return f.remoteNow(ctx, t), true, nil
	}
	now := runinput.TicketNow{Labels: t.Labels}
	if t.StatusID != "" {
		statuses, err := f.Conf.TrackerStatuses(ctx, t.SourceID)
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

// remoteNow is TicketNow of a record. A comment apothecary posted to a remote
// tracker is anybody's here; the engine knows it by its trailer.
func (f *Factory) remoteNow(ctx context.Context, t confstore.Ticket) runinput.TicketNow {
	now := runinput.TicketNow{Key: t.SourceKey, Labels: t.Labels}
	if f.Live == nil {
		return now
	}
	log := f.log().With("ticket", t.ID, "key", t.SourceKey)
	issue, err := f.Live.IssueOf(ctx, t, true)
	if err != nil {
		log.Warn("ticket now: the issue cannot be read", "err", err)
	} else {
		now.Key, now.Status, now.Labels = issue.Key, issue.Status, issue.Labels
	}
	comments, err := f.Live.Comments(ctx, t.SourceID, now.Key, true)
	if err != nil {
		log.Warn("ticket now: the comments cannot be read", "err", err)
		return now
	}
	now.Comments = make([]runinput.TicketComment, 0, len(comments))
	for _, c := range comments {
		now.Comments = append(now.Comments, runinput.TicketComment{Author: c.Author, Body: c.Body, At: c.Created})
	}
	return now
}
