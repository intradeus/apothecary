package tickets

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
)

// TicketNow reads an internal ticket as a prompt shows it: its status by
// name, its labels and its comments oldest first, apothecary's own marked.
func TestTicketNow(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, devSource())
	tk, err := w.f.Create(ctx, confstore.Ticket{UserID: owner, Title: "T", Goal: "g", Labels: []string{"backend"}}, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, c := range []confstore.TicketComment{
		{TicketID: tk.ID, Author: "ana", Body: "first", Origin: confstore.CommentUser, CreatedAt: at},
		{TicketID: tk.ID, Author: "apothecary", Body: "moved", Origin: confstore.CommentApothecary, RunID: "r1", StageID: "main-1/s", CreatedAt: at.Add(time.Hour)},
	} {
		if _, _, err := w.conf.AddTicketComment(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	now, found, err := w.f.TicketNow(ctx, tk.ID)
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

	if _, found, err := w.f.TicketNow(ctx, "missing"); found || err != nil {
		t.Errorf("a missing ticket: found=%v err=%v", found, err)
	}
}

// A remote issue's status and comments are read live; a tracker that cannot
// be read leaves the record's key and labels, never an error.
func TestTicketNowOfARemoteIssue(t *testing.T) {
	ctx := context.Background()
	src := devSource()
	src.Tickets[0].Labels = []string{"ui"}
	src.Thread = map[string][]trackerport.Comment{"PROJ-7": {{ID: "c1", Author: "bea", Body: "please hurry", Created: t0}}}
	w := newWorld(t, src)
	rec := w.record(t)
	now, found, err := w.f.TicketNow(ctx, rec.ID)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if now.Key != "PROJ-7" || now.Status != "To Do" || !slices.Equal(now.Labels, []string{"ui"}) ||
		len(now.Comments) != 1 || now.Comments[0].Body != "please hurry" || now.Comments[0].Own {
		t.Errorf("now = %+v", now)
	}

	w2 := newWorld(t, devSource())
	rec2 := w2.record(t)
	w2.src.fail(errors.New("tracker down"))
	now, found, err = w2.f.TicketNow(ctx, rec2.ID)
	if err != nil || !found || now.Key != "PROJ-7" || now.Status != "" || len(now.Comments) != 0 {
		t.Errorf("tracker down: now = %+v, found=%v err=%v", now, found, err)
	}
}

// A run starts from the issue as it is now, and what it shows is written to
// the record: title, goal, key, type and change type.
func TestRunStartReadsTheIssueAndRecordsIt(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, devSource())
	rec := w.record(t)
	w.src.Tickets[0].Title, w.src.Tickets[0].Type, w.src.Tickets[0].TypeID = "Say goodbye", "Bug", "11"
	got, found, err := w.f.RunTicket(ctx, rec.ID, testProject)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got.TicketID != rec.ID || got.Title != "Say goodbye" || got.ChangeType != string(trackerport.ChangeFix) ||
		got.SourceID != remote || got.SourceKind != "faketracker" || got.SourceKey != "PROJ-7" || got.SourceURL == "" {
		t.Errorf("run ticket = %+v", got)
	}
	stored, err := w.conf.GetTicket(ctx, rec.ID)
	if err != nil || stored.Title != "Say goodbye" || stored.SourceType != "Bug" || stored.ChangeType != trackerport.ChangeFix {
		t.Errorf("record = %+v (%v), want what was seen", stored, err)
	}
	// A moved issue is found by its stable id, and its key follows.
	w.src.Tickets[0].Key = "NEW-7"
	if got, _, err := w.f.RunTicket(ctx, rec.ID, testProject); err != nil || got.SourceKey != "NEW-7" {
		t.Errorf("moved: key %q (%v)", got.SourceKey, err)
	}
	if _, found, err := w.f.RunTicket(ctx, "missing", testProject); found || err != nil {
		t.Errorf("a missing ticket: found=%v err=%v", found, err)
	}
}

// A tracker problem never refuses a run: it starts from the cached copy, and
// with none from what the record last saw.
func TestRunStartSurvivesTheTracker(t *testing.T) {
	ctx := context.Background()
	t.Run("from the cached copy", func(t *testing.T) {
		w := newWorld(t, devSource())
		rec := w.record(t)
		w.src.Tickets[0].Title = "Cached title"
		if _, err := w.live.IssueOf(ctx, rec, false); err != nil {
			t.Fatal(err)
		}
		w.src.fail(errors.New("tracker down"))
		got, found, err := w.f.RunTicket(ctx, rec.ID, testProject)
		if err != nil || !found || got.Title != "Cached title" || got.SourceKey != "PROJ-7" {
			t.Errorf("run ticket = %+v, found=%v err=%v", got, found, err)
		}
	})
	t.Run("from the record", func(t *testing.T) {
		w := newWorld(t, devSource())
		rec := w.record(t)
		w.src.Tickets[0].Title = "Never seen"
		w.src.fail(&trackerport.ErrRateLimited{Tracker: "fake", RetryAfter: time.Minute})
		got, found, err := w.f.RunTicket(ctx, rec.ID, testProject)
		if err != nil || !found || got.Title != rec.Title || got.Goal != rec.Goal || got.SourceKey != "PROJ-7" {
			t.Errorf("run ticket = %+v, found=%v err=%v", got, found, err)
		}
	})
}

// The issue's files are copied at a run's start, once: a file the ticket got
// before remote ids were kept is adopted rather than copied again, and a
// repeated start copies nothing.
func TestRunStartCopiesTheFilesOnce(t *testing.T) {
	ctx := context.Background()
	src := devSource()
	src.Tickets[0].Attachments = append(src.Tickets[0].Attachments,
		trackerport.SourceAttachment{ID: "a2", Filename: "notes.md", Mime: "text/markdown", Size: 5},
		trackerport.SourceAttachment{ID: "a1", Filename: "spec.md", Mime: "text/markdown", Size: 5}) // listed twice
	src.Blobs["a2"] = []byte("notes")
	w := newWorld(t, src)
	rec := w.record(t)
	old, err := w.att.Save(ctx, owner, "notes.md", "text/markdown", strings.NewReader("notes"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.att.ClaimForTicket(ctx, owner, []string{old.ID}, rec.ID); err != nil {
		t.Fatal(err)
	}

	got, _, err := w.f.RunTicket(ctx, rec.ID, testProject)
	if err != nil {
		t.Fatal(err)
	}
	files := ticketAttachments(t, w.att, rec.ID)
	byRemote := map[string]string{}
	for _, a := range files {
		byRemote[a.RemoteID] = a.ID
	}
	if len(files) != 2 || byRemote["a2"] != old.ID || byRemote["a1"] == "" {
		t.Errorf("files = %+v, want spec.md copied and notes.md adopted", files)
	}
	if len(got.TicketAttachmentIDs) != 2 {
		t.Errorf("the run's files = %v", got.TicketAttachmentIDs)
	}
	if w.src.opens != 1 {
		t.Errorf("%d downloads, want only spec.md's", w.src.opens)
	}

	if _, _, err := w.f.RunTicket(ctx, rec.ID, testProject); err != nil {
		t.Fatal(err)
	}
	if n := len(ticketAttachments(t, w.att, rec.ID)); n != 2 || w.src.opens != 1 {
		t.Errorf("a repeated start: %d files, %d downloads", n, w.src.opens)
	}
}

// Each workshop names a remote issue's base branch by its own format; a base
// branch the user set wins in both. An internal ticket has no key.
func TestEachWorkshopNamesTheBaseBranch(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, devSource())
	conftest.SeedProject(t, w.conf, confstore.Project{ID: "prj-2", UserID: owner, Name: "q",
		Repo: confstore.RepoConfig{BranchFormat: "{ticket_number}/{ticket_title}"}})
	rec := w.record(t)
	hand, err := w.f.Create(ctx, confstore.Ticket{UserID: owner, Title: "Add keys", Goal: "g"}, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ticket, project, want string
	}{
		{rec.ID, testProject, "feat/proj-7-add-a-farewell-function"},
		{rec.ID, "prj-2", "proj-7/add-a-farewell-function"},
		{hand.ID, testProject, "feat/add-keys"},
		{hand.ID, "prj-2", "add-keys"},
		{rec.ID, "gone", "feat/proj-7-add-a-farewell-function"}, // the default format
	} {
		got, _, err := w.f.RunTicket(ctx, tc.ticket, tc.project)
		if err != nil || got.Branch != tc.want {
			t.Errorf("%s in %s: branch %q (%v), want %q", tc.ticket, tc.project, got.Branch, err, tc.want)
		}
	}

	for _, id := range []string{rec.ID, hand.ID} {
		if _, err := w.f.Update(ctx, id, confstore.Ticket{Title: "Add keys", Goal: "g", BaseBranch: "mine/farewell"}, owner, nil); err != nil {
			t.Fatal(err)
		}
		for _, project := range []string{testProject, "prj-2"} {
			if got, _, err := w.f.RunTicket(ctx, id, project); err != nil || got.Branch != "mine/farewell" {
				t.Errorf("%s in %s: branch %q (%v), want the user's", id, project, got.Branch, err)
			}
		}
	}
}

// The run's copy of an internal ticket carries no source fields: they mean a
// remote tracker.
func TestAnInternalRunTicketHasNoSource(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, devSource())
	hand, err := w.f.Create(ctx, confstore.Ticket{UserID: owner, Title: "T", Goal: "g"}, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := w.f.RunTicket(ctx, hand.ID, testProject)
	if err != nil || !found || got.SourceID != "" || got.SourceKind != "" || got.SourceKey != "" || got.SourceURL != "" {
		t.Errorf("run ticket = %+v, found=%v err=%v", got, found, err)
	}
	if searches, fetches := w.src.calls(); searches+fetches != 0 {
		t.Errorf("the remote tracker was read: %d searches, %d fetches", searches, fetches)
	}
}
