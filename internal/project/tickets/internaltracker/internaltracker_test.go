package internaltracker_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/resolution"
	"apothecary/internal/project/tickets/internaltracker"
)

const owner = "u1"

// world is a config store holding a hand-written ticket on its owner's
// internal tracker.
type world struct {
	conf *confstore.Service
	tr   *internaltracker.Tracker
	id   string
}

func newWorld(t *testing.T) world {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	conf := confstore.New(confstore.NewMem(), key)
	tk, err := conf.CreateTicket(context.Background(), confstore.Ticket{UserID: owner, Title: "T", Goal: "g",
		ChangeType: trackerport.ChangeFeat})
	if err != nil {
		t.Fatal(err)
	}
	return world{conf: conf, tr: internaltracker.New(conf), id: tk.ID}
}

func (w world) ticket(t *testing.T) confstore.Ticket {
	t.Helper()
	tk, err := w.conf.GetTicket(context.Background(), w.id)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// statusName is the name of the ticket's status.
func (w world) statusName(t *testing.T) string {
	t.Helper()
	tk := w.ticket(t)
	in, err := w.conf.InternalTracker(context.Background(), tk.UserID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := w.conf.TrackerStatuses(context.Background(), in.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == tk.StatusID {
			return s.Name
		}
	}
	return ""
}

func actor(stage string) context.Context {
	return resolution.WithTrackerActor(context.Background(), resolution.TrackerActor{RunID: "r1", Stage: stage})
}

func wantNotApplicable(t *testing.T, what string, err error) {
	t.Helper()
	var na *trackerport.ErrNotApplicable
	if !errors.As(err, &na) {
		t.Errorf("%s: err = %v, want the skip-class ErrNotApplicable", what, err)
	}
}

// A hand-written ticket starts in the tracker's first status; a move names a
// status of the tracker, case aside, and records who made it.
func TestTransitionByName(t *testing.T) {
	w := newWorld(t)
	if got := w.statusName(t); got != "To do" {
		t.Fatalf("new ticket's status = %q, want To do", got)
	}
	if err := w.tr.Transition(context.Background(), w.id, "in PROGRESS"); err != nil {
		t.Fatal(err)
	}
	if got, by := w.statusName(t), w.ticket(t).StatusChangedBy; got != "In progress" || by != confstore.ChangedByUser {
		t.Errorf("status = %q by %q, want In progress by user", got, by)
	}
	if err := w.tr.Transition(actor("main-1/kick"), w.id, "done"); err != nil {
		t.Fatal(err)
	}
	if got, by := w.statusName(t), w.ticket(t).StatusChangedBy; got != "Done" || by != "run:r1/main-1/kick" {
		t.Errorf("status = %q by %q, want Done by the run's stage", got, by)
	}
}

func TestTransitionToAnUnknownNameNamesTheStatuses(t *testing.T) {
	w := newWorld(t)
	err := w.tr.Transition(context.Background(), w.id, "Shipped")
	var ur *trackerport.ErrStatusUnreachable
	if !errors.As(err, &ur) {
		t.Fatalf("err = %v, want ErrStatusUnreachable", err)
	}
	want := []string{"To do", "In progress", "In review", "Done", "Blocked"}
	if ur.From != "To do" || !slices.Equal(ur.Reachable, want) {
		t.Errorf("from %q, reachable %v; want To do, %v", ur.From, ur.Reachable, want)
	}
	if got := w.statusName(t); got != "To do" {
		t.Errorf("status moved to %q", got)
	}
}

// A run's comment is keyed by its run and stage occurrence: posting it again
// is a no-op, and the reconciliation read finds it. Another occurrence, or a
// person, comments anew.
func TestCommentIsIdempotentPerRunAndStage(t *testing.T) {
	w := newWorld(t)
	ctx := actor("main-1/post")
	posted, err := w.tr.RunCommentPosted(ctx, w.id)
	if err != nil || posted {
		t.Fatalf("before = %v (%v)", posted, err)
	}
	for range 2 {
		if err := w.tr.Comment(ctx, w.id, "hello"); err != nil {
			t.Fatal(err)
		}
	}
	if posted, err := w.tr.RunCommentPosted(ctx, w.id); err != nil || !posted {
		t.Errorf("after = %v (%v)", posted, err)
	}
	if err := w.tr.Comment(actor("main-2/post"), w.id, "again"); err != nil {
		t.Fatal(err)
	}
	if err := w.tr.Comment(context.Background(), w.id, "a person's"); err != nil {
		t.Fatal(err)
	}
	list, err := w.tr.ListComments(context.Background(), w.id)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range list {
		bodies = append(bodies, c.Body)
	}
	if !slices.Equal(bodies, []string{"hello", "again", "a person's"}) {
		t.Errorf("comments = %q", bodies)
	}
	stored, err := w.conf.TicketComments(context.Background(), w.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 || stored[0].Origin != confstore.CommentApothecary || stored[2].Origin != confstore.CommentUser {
		t.Errorf("origins = %+v", stored)
	}
}

// Labels are the one field: a write replaces them; the read gives the text a
// write takes, which is what append mode merges into.
func TestLabelsReplaceAndReadBack(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if err := w.tr.SetFields(ctx, w.id, map[string]string{"Labels": "backend, ui"}); err != nil {
		t.Fatal(err)
	}
	if err := w.tr.SetFields(ctx, w.id, map[string]string{"labels": "ops"}); err != nil {
		t.Fatal(err)
	}
	got, err := w.tr.Fields(ctx, w.id, []string{"labels"})
	if err != nil || got["labels"] != "ops" {
		t.Fatalf("after replace = %v (%v)", got, err)
	}
	if err := w.tr.SetFields(ctx, w.id, map[string]string{"labels": got["labels"] + ", sec"}); err != nil {
		t.Fatal(err)
	}
	if labels := w.ticket(t).Labels; !slices.Equal(labels, []string{"ops", "sec"}) {
		t.Errorf("after append = %v", labels)
	}
}

// Any field but labels means nothing here: the skip-class refusal, so a flow
// written for Jira's fields still runs; the ticket is untouched.
func TestOtherFieldsAreNotApplicable(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	err := w.tr.SetFields(ctx, w.id, map[string]string{"labels": "a", "Components": "api"})
	wantNotApplicable(t, "set", err)
	if err != nil && !strings.Contains(err.Error(), "labels") {
		t.Errorf("the refusal should name the field there is: %v", err)
	}
	if labels := w.ticket(t).Labels; len(labels) != 0 {
		t.Errorf("a refused write landed: %v", labels)
	}
	_, err = w.tr.Fields(ctx, w.id, []string{"Components"})
	wantNotApplicable(t, "read", err)
}

// A link is upserted on URL.
func TestAddLinkUpsertsOnURL(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for _, title := range []string{"Branch", "Branch (web)"} {
		if err := w.tr.AddLink(ctx, w.id, trackerport.TicketLink{URL: "https://git.test/b", Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.tr.AddLink(ctx, w.id, trackerport.TicketLink{URL: "https://git.test/mr/1", Title: "MR"}); err != nil {
		t.Fatal(err)
	}
	links, err := w.conf.TicketLinks(ctx, w.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 || !slices.ContainsFunc(links, func(l confstore.TicketLink) bool {
		return l.URL == "https://git.test/b" && l.Title == "Branch (web)"
	}) {
		t.Errorf("links = %+v", links)
	}
}

// A ticket deleted under a run is no longer this tracker's: every write is
// the skip-class refusal.
func TestAGoneTicketIsNotApplicable(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if err := w.conf.DeleteTicket(ctx, w.id); err != nil {
		t.Fatal(err)
	}
	wantNotApplicable(t, "transition", w.tr.Transition(ctx, w.id, "Done"))
	wantNotApplicable(t, "comment", w.tr.Comment(ctx, w.id, "x"))
	wantNotApplicable(t, "set", w.tr.SetFields(ctx, w.id, map[string]string{"labels": "a"}))
	wantNotApplicable(t, "link", w.tr.AddLink(ctx, w.id, trackerport.TicketLink{URL: "https://x"}))
	_, err := w.tr.ListComments(ctx, w.id)
	wantNotApplicable(t, "list comments", err)
}

// A remote issue's record is held by its own tracker, whatever its key: the
// internal tracker refuses to write it, and names the issue.
func TestARemoteRecordIsNotThisTrackers(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if err := w.conf.DB().UpsertIntegration(ctx, confstore.StoredIntegration{ID: "jira-1", UserID: owner,
		Port: confstore.PortTicketSource, Kind: "jira", Name: "jira", Config: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	rec, _, err := w.conf.RecordTicket(ctx, "jira-1", confstore.TicketSeen{RemoteID: "10001", Key: "PROJ-1", Title: "Remote"}, "")
	if err != nil {
		t.Fatal(err)
	}
	err = w.tr.Transition(ctx, rec.ID, "Done")
	wantNotApplicable(t, "transition", err)
	if err == nil || !strings.Contains(err.Error(), "PROJ-1") {
		t.Errorf("err = %v, want it to name the issue", err)
	}
	wantNotApplicable(t, "comment", w.tr.Comment(ctx, rec.ID, "x"))
	wantNotApplicable(t, "set", w.tr.SetFields(ctx, rec.ID, map[string]string{"labels": "a"}))
	if _, err := w.tr.Fields(ctx, rec.ID, []string{"labels"}); err == nil {
		t.Error("fields of a remote record were read")
	}
}
