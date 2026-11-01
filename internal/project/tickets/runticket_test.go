package tickets

import (
	"context"
	"testing"
	"time"

	"apothecary/internal/project/confstore"
)

// TicketNow reads the ticket as a prompt shows it: its status by name, its
// labels and its comments oldest first, apothecary's own marked.
func TestTicketNow(t *testing.T) {
	ctx := context.Background()
	f, _ := newFactory(t, nil)
	tk, err := f.Create(ctx, confstore.Ticket{ProjectID: testProject, Title: "T", Goal: "g", Labels: []string{"backend"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, c := range []confstore.TicketComment{
		{TicketID: tk.ID, Author: "ana", Body: "first", Origin: confstore.CommentUser, CreatedAt: at},
		{TicketID: tk.ID, Author: "apothecary", Body: "moved", Origin: confstore.CommentApothecary, RunID: "r1", StageID: "main-1/s", CreatedAt: at.Add(time.Hour)},
	} {
		if _, _, err := f.Conf.AddTicketComment(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	now, found, err := f.TicketNow(ctx, tk.ID)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if now.Status != "To do" || now.Key != "" || len(now.Labels) != 1 || now.Labels[0] != "backend" {
		t.Errorf("now = %+v", now)
	}
	if len(now.Comments) != 2 || now.Comments[0].Body != "first" || now.Comments[0].Own ||
		now.Comments[1].Body != "moved" || !now.Comments[1].Own || now.Comments[0].Author != "ana" {
		t.Errorf("comments = %+v", now.Comments)
	}

	if _, found, err := f.TicketNow(ctx, "missing"); found || err != nil {
		t.Errorf("a missing ticket: found=%v err=%v", found, err)
	}
}
