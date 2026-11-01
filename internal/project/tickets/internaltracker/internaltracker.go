// Package internaltracker is Apothecary's own ticket tracker: the tracker of
// every ticket with no remote source (hand-written, or made for a merge
// request). It implements trackerport.TicketWriter over the ticket row, so a
// flow's tracker actions — and the UI — write to it exactly as they write to
// Jira, and a hand-written ticket's `ticket_transition to: done` really moves
// it (docs/ticket-sync-plan.md § Write path).
//
// A ticket's key on this tracker is its id. The writes:
//
//   - Transition moves to the status of that name on the owner's internal
//     tracker (case aside); no such status is *trackerport.ErrStatusUnreachable
//     listing the names there are. Already there is a no-op.
//   - Comment adds a comment. Made by a run (resolution.TrackerActor on the
//     context) it is keyed by the run and the stage occurrence, so posting it
//     again is a no-op and RunCommentPosted answers the engine's
//     reconciliation; made by a person it is theirs.
//   - SetFields / Fields know one field, labels; any other is
//     *trackerport.ErrNotApplicable (the stage skips, it does not fail — a
//     flow written for Jira's components still runs on a hand-written ticket).
//   - AddLink upserts a link on URL.
//   - A ticket deleted meanwhile is *trackerport.ErrNotApplicable too.
//
// Every write is one SQL statement (confstore), so a run and a person at once
// is last-write-wins on a whole value, never a torn row.
package internaltracker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/resolution"
)

// Tracker is the internal tracker of every user (the owner is the ticket's).
type Tracker struct {
	Conf *confstore.Service
}

// New is the internal tracker over conf.
func New(conf *confstore.Service) *Tracker { return &Tracker{Conf: conf} }

// fieldLabels is the one field the internal tracker has.
const fieldLabels = "labels"

// gone is the refusal for a ticket that no longer exists, or that is no
// longer this tracker's.
func gone(key string, err error) error {
	if errors.Is(err, confstore.ErrNotFound) {
		return &trackerport.ErrNotApplicable{Key: key, Reason: "the ticket was deleted"}
	}
	if errors.Is(err, confstore.ErrRemoteOwned) {
		return &trackerport.ErrNotApplicable{Key: key, Reason: "the ticket mirrors a remote issue now; the internal tracker no longer holds it"}
	}
	return err
}

// ticket reads the ticket the key names, refusing one this tracker does not
// hold.
func (t *Tracker) ticket(ctx context.Context, key string) (confstore.Ticket, error) {
	tk, err := t.Conf.GetTicket(ctx, key)
	if err != nil {
		return confstore.Ticket{}, gone(key, err)
	}
	if tk.SourceID != "" {
		return confstore.Ticket{}, gone(key, confstore.ErrRemoteOwned)
	}
	return tk, nil
}

// Transition moves the ticket to the internal tracker's status of that name.
func (t *Tracker) Transition(ctx context.Context, key, status string) error {
	tk, err := t.ticket(ctx, key)
	if err != nil {
		return err
	}
	trackerID, err := t.Conf.TicketTrackerID(ctx, tk)
	if err != nil {
		return err
	}
	list, err := t.Conf.TrackerStatuses(ctx, trackerID)
	if err != nil {
		return err
	}
	var to, from string
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name)
		if strings.EqualFold(s.Name, strings.TrimSpace(status)) {
			to = s.ID
		}
		if s.ID == tk.StatusID {
			from = s.Name
		}
	}
	if to == "" {
		return &trackerport.ErrStatusUnreachable{Key: key, From: from, To: status, Reachable: names}
	}
	by := confstore.ChangedByUser
	if a, ok := resolution.TrackerActorFrom(ctx); ok {
		by = a.ChangedBy()
	}
	return gone(key, t.Conf.SetTicketStatus(ctx, key, to, by))
}

// Comment adds a comment: a run's (keyed, so idempotent) or a person's.
func (t *Tracker) Comment(ctx context.Context, key, body string) error {
	if _, err := t.ticket(ctx, key); err != nil {
		return err
	}
	c := confstore.TicketComment{TicketID: key, Body: body, Origin: confstore.CommentUser}
	if a, ok := resolution.TrackerActorFrom(ctx); ok {
		c.Origin, c.Author, c.RunID, c.StageID = confstore.CommentApothecary, "apothecary", a.RunID, a.Stage
	}
	_, _, err := t.Conf.AddTicketComment(ctx, c)
	return gone(key, err)
}

// RunCommentPosted reports whether the run's stage occurrence on the context
// (resolution.TrackerActor) has already commented on the ticket: the
// engine's reconciliation for a comment whose completion was not recorded.
// The internal tracker keys a run's comment by its actor, so its body needs
// no trailer to be found again.
func (t *Tracker) RunCommentPosted(ctx context.Context, key string) (bool, error) {
	a, ok := resolution.TrackerActorFrom(ctx)
	if !ok {
		return false, errors.New("internal tracker: a run's comment needs its run and stage on the context")
	}
	list, err := t.Conf.TicketComments(ctx, key)
	if err != nil {
		return false, gone(key, err)
	}
	return slices.ContainsFunc(list, func(c confstore.TicketComment) bool {
		return c.RunID == a.RunID && c.StageID == a.Stage
	}), nil
}

// ListComments is the ticket's comments, oldest first.
func (t *Tracker) ListComments(ctx context.Context, key string) ([]trackerport.Comment, error) {
	if _, err := t.Conf.GetTicket(ctx, key); err != nil {
		return nil, gone(key, err)
	}
	list, err := t.Conf.TicketComments(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]trackerport.Comment, 0, len(list))
	for _, c := range list {
		out = append(out, trackerport.Comment{ID: c.ID, Author: c.Author, Body: c.Body, Created: c.CreatedAt, Updated: c.UpdatedAt})
	}
	return out, nil
}

// noField is the refusal for a field the internal tracker does not have.
func noField(key, field string) error {
	return &trackerport.ErrNotApplicable{Key: key,
		Reason: fmt.Sprintf("the internal tracker has no field %q; it has: %s", field, fieldLabels)}
}

// SetFields writes labels (a comma-separated list, replacing them).
func (t *Tracker) SetFields(ctx context.Context, key string, fields map[string]string) error {
	for name := range fields {
		if !strings.EqualFold(strings.TrimSpace(name), fieldLabels) {
			return noField(key, name)
		}
	}
	if _, err := t.ticket(ctx, key); err != nil {
		return err
	}
	for _, v := range fields {
		if _, err := t.Conf.SetTicketLabels(ctx, key, splitList(v), false); err != nil {
			return gone(key, err)
		}
	}
	return nil
}

// Fields reads labels, as the comma-separated text SetFields takes.
func (t *Tracker) Fields(ctx context.Context, key string, names []string) (map[string]string, error) {
	for _, name := range names {
		if !strings.EqualFold(strings.TrimSpace(name), fieldLabels) {
			return nil, noField(key, name)
		}
	}
	tk, err := t.ticket(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		out[name] = strings.Join(tk.Labels, ", ")
	}
	return out, nil
}

// AddLink upserts a link on the ticket, on URL.
func (t *Tracker) AddLink(ctx context.Context, key string, link trackerport.TicketLink) error {
	return gone(key, t.Conf.AddTicketLink(ctx, key, link))
}

// splitList reads a comma-separated list.
func splitList(v string) []string {
	var out []string
	for part := range strings.SplitSeq(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var _ trackerport.TicketWriter = (*Tracker)(nil)
