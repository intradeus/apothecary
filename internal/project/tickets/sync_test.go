package tickets

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// syncWorld is a tracker with two issues — one open, one done — both imported.
type syncWorld struct {
	f      *Factory
	att    *filestore.Attachments
	src    *fake.TicketSource
	s      *Syncer
	open   confstore.Ticket
	closed confstore.Ticket
	now    time.Time
}

func newSyncWorld(t *testing.T) *syncWorld {
	t.Helper()
	src := &fake.TicketSource{Tickets: []trackerport.SourceTicket{
		{RemoteID: "100", Key: "PROJ-7", Title: "Open one", Body: "b", URL: "https://tracker.test/PROJ-7",
			Status: "To Do", StatusID: "1", Type: "Story", TypeID: "10", Updated: t0},
		{RemoteID: "200", Key: "PROJ-8", Title: "Done one", Body: "b", URL: "https://tracker.test/PROJ-8",
			Status: "Done", StatusID: "4", Type: "Bug", TypeID: "11", Updated: t0},
	}}
	f, att := newFactory(t, src)
	w := &syncWorld{f: f, att: att, src: src, now: t0}
	w.s = &Syncer{Conf: f.Conf, Sources: f.Sources, Attachments: att, Now: func() time.Time { return w.now }}
	ctx := context.Background()
	var err error
	if w.open, _, err = f.CreateFromSource(ctx, "int-1", "PROJ-7", confstore.Ticket{ProjectID: testProject}, nil); err != nil {
		t.Fatal(err)
	}
	if w.closed, _, err = f.CreateFromSource(ctx, "int-1", "PROJ-8", confstore.Ticket{ProjectID: testProject}, nil); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *syncWorld) ticket(t *testing.T, id string) confstore.Ticket {
	t.Helper()
	got, err := w.f.Conf.GetTicket(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// edit changes the remote issue as a person on the tracker would.
func (w *syncWorld) edit(key string, fn func(*trackerport.SourceTicket)) {
	for i := range w.src.Tickets {
		if w.src.Tickets[i].Key == key {
			fn(&w.src.Tickets[i])
		}
	}
}

// The poll re-reads what is not done; a done ticket is only re-read when
// asked for by name — and then it is, done or not.
func TestSyncPollSkipsDoneAndRefreshDoesNot(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	for _, key := range []string{"PROJ-7", "PROJ-8"} {
		w.edit(key, func(s *trackerport.SourceTicket) { s.Title += " (edited)"; s.Updated = t0.Add(time.Hour) })
	}
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) { s.Status, s.StatusID = "In Progress", "2" })
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	open := w.ticket(t, w.open.ID)
	if open.Title != "Open one (edited)" || open.SyncState != confstore.SyncOK || open.SyncedAt == nil {
		t.Errorf("open ticket after the poll = %+v", open)
	}
	if open.StatusID == w.open.StatusID || open.StatusChangedBy != confstore.ChangedBySync {
		t.Errorf("status = %s by %q, want moved by sync", open.StatusID, open.StatusChangedBy)
	}
	if got := w.ticket(t, w.closed.ID); got.Title != "Done one" {
		t.Errorf("the poll re-read a done ticket: %q", got.Title)
	}
	if err := w.s.Refresh(ctx, w.closed.ID); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.closed.ID); got.Title != "Done one (edited)" {
		t.Errorf("a refresh by name skipped a done ticket: %q", got.Title)
	}
}

// RefreshIntegration covers every ticket of the integration, done included.
func TestRefreshIntegrationIncludesDone(t *testing.T) {
	w := newSyncWorld(t)
	w.edit("PROJ-8", func(s *trackerport.SourceTicket) {
		s.Title = "Reopened"
		s.Status, s.StatusID = "To Do", "1"
		s.Updated = t0.Add(time.Hour)
	})
	counts, err := w.s.RefreshIntegration(context.Background(), "int-1")
	if err != nil {
		t.Fatal(err)
	}
	if counts != (SyncCounts{Tickets: 2, Synced: 2}) {
		t.Errorf("counts = %+v", counts)
	}
	if got := w.ticket(t, w.closed.ID); got.Title != "Reopened" {
		t.Errorf("title = %q", got.Title)
	}
}

// A snapshot older than the one the mirror holds is dropped.
func TestSyncDropsAnOlderSnapshot(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) { s.Title = "Newer"; s.Updated = t0.Add(2 * time.Hour) })
	if err := w.s.Refresh(ctx, w.open.ID); err != nil {
		t.Fatal(err)
	}
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) { s.Title = "Stale"; s.Updated = t0.Add(time.Hour) })
	if err := w.s.Refresh(ctx, w.open.ID); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.open.ID); got.Title != "Newer" {
		t.Errorf("title = %q, want the newer snapshot kept", got.Title)
	}
}

// An issue that moved (404 on its key) is found by its remote id, and the
// mirror takes its new key and URL; one gone for good stops being polled.
func TestSyncNotFound(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) {
		s.Key, s.URL, s.Updated = "NEW-1", "https://tracker.test/NEW-1", t0.Add(time.Hour)
	})
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got := w.ticket(t, w.open.ID)
	if got.SourceKey != "NEW-1" || got.SourceURL != "https://tracker.test/NEW-1" || got.SyncState != confstore.SyncOK {
		t.Errorf("moved issue = %s %s %s", got.SourceKey, got.SourceURL, got.SyncState)
	}

	w.src.Tickets = w.src.Tickets[1:] // NEW-1 deleted on the tracker
	if err := w.s.Refresh(ctx, w.open.ID); err != nil {
		t.Fatalf("gone is not an error: %v", err)
	}
	if got := w.ticket(t, w.open.ID); got.SyncState != confstore.SyncGone || got.Title != "Open one" {
		t.Errorf("deleted issue = %s %q", got.SyncState, got.Title)
	}
	syncable, err := w.f.Conf.ListSyncable(ctx, "int-1")
	if err != nil || len(syncable) != 0 {
		t.Errorf("syncable = %v, %v; want none (one done, one gone)", syncable, err)
	}
}

// A ticket without a remote id is gone at the first 404.
func TestSyncNotFoundWithoutRemoteID(t *testing.T) {
	w := newSyncWorld(t)
	w.src.Tickets[0].RemoteID = ""
	w.src.Tickets[0].Key = "ELSEWHERE-1"
	ctx := context.Background()
	if _, err := w.f.Conf.MirrorTicket(ctx, w.open.ID, confstore.TicketMirror{Title: "Open one", SourceKey: "PROJ-7"}); err != nil {
		t.Fatal(err)
	}
	if err := w.s.Refresh(ctx, w.open.ID); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.open.ID); got.SyncState != confstore.SyncGone {
		t.Errorf("sync state = %q, want gone", got.SyncState)
	}
}

// Comments follow the tracker: new ones land, edited ones change, deleted
// ones go.
func TestSyncReconcilesComments(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	w.src.Thread = map[string][]trackerport.Comment{"PROJ-7": {
		{ID: "c1", Author: "ann", Body: "first", Created: t0, Updated: t0},
		{ID: "c2", Author: "bob", Body: "second", Created: t0, Updated: t0},
	}}
	if err := w.s.Refresh(ctx, w.open.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.f.Conf.TicketComments(ctx, w.open.ID); len(got) != 2 {
		t.Fatalf("comments = %+v", got)
	}
	w.src.Thread["PROJ-7"] = []trackerport.Comment{{ID: "c2", Author: "bob", Body: "second, edited", Created: t0, Updated: t0.Add(time.Minute)}}
	if err := w.s.Refresh(ctx, w.open.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := w.f.Conf.TicketComments(ctx, w.open.ID)
	if len(got) != 1 || got[0].RemoteID != "c2" || got[0].Body != "second, edited" || got[0].Origin != confstore.CommentRemote {
		t.Errorf("comments = %+v", got)
	}
}

// A tracker that cannot be read marks the ticket in error, and the poll
// leaves the integration alone for the backoff; a refresh by name does not.
func TestSyncErrorBacksOff(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	w.src.Err = errors.New("401 unauthorized")
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.open.ID); got.SyncState != confstore.SyncError || got.SyncError == "" {
		t.Fatalf("after a failed pass = %q %q", got.SyncState, got.SyncError)
	}
	w.src.Err = nil
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) { s.Title = "Back"; s.Updated = t0.Add(time.Hour) })
	w.now = t0.Add(DefaultErrorBackoff - time.Minute)
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.open.ID); got.Title == "Back" {
		t.Error("the poll did not back off")
	}
	w.now = t0.Add(DefaultErrorBackoff + time.Minute)
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.open.ID); got.Title != "Back" || got.SyncState != confstore.SyncOK || got.SyncError != "" {
		t.Errorf("after the backoff = %q %q %q", got.Title, got.SyncState, got.SyncError)
	}
}

// A rate-limited tracker records nothing on the tickets, and the poll leaves
// the integration alone for the Retry-After it asked — longer than the error
// backoff here — then reads it again.
func TestSyncRateLimitHonoursRetryAfter(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	before := w.ticket(t, w.open.ID)
	w.src.Err = fmt.Errorf("search: %w", &trackerport.ErrRateLimited{Tracker: "fake", RetryAfter: 40 * time.Minute})
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.open.ID); got.SyncState != before.SyncState || got.SyncError != before.SyncError {
		t.Fatalf("a rate limit was recorded on the ticket: %q %q", got.SyncState, got.SyncError)
	}
	w.src.Err = nil
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) { s.Title = "Back"; s.Updated = t0.Add(time.Hour) })
	w.now = t0.Add(DefaultErrorBackoff + time.Minute)
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.open.ID); got.Title == "Back" {
		t.Error("the poll read the tracker before its Retry-After")
	}
	w.now = t0.Add(41 * time.Minute)
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.open.ID); got.Title != "Back" || got.SyncState != confstore.SyncOK {
		t.Errorf("after the Retry-After = %q %q", got.Title, got.SyncState)
	}
}

// A rate limit that names no wait holds the poll for the error backoff; a
// manual refresh meeting one counts the tickets RateLimited, records nothing,
// and holds the poll too.
func TestSyncRateLimitWithoutRetryAfter(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	before := w.ticket(t, w.closed.ID)
	w.src.Err = &trackerport.ErrRateLimited{Tracker: "fake"}
	counts, err := w.s.RefreshIntegration(ctx, "int-1")
	var rl *trackerport.ErrRateLimited
	if !errors.As(err, &rl) {
		// The fake fails the list refresh first; the pass is the same path.
		t.Fatalf("refresh = %+v, %v", counts, err)
	}
	if got := w.ticket(t, w.closed.ID); got.SyncState != before.SyncState || got.SyncError != before.SyncError {
		t.Errorf("a rate limit was recorded on the ticket: %q %q", got.SyncState, got.SyncError)
	}
	counts, _, _ = w.s.pass(ctx, w.src, []confstore.Ticket{w.open, w.closed})
	if counts.RateLimited != 2 || counts.Failed != 0 {
		t.Errorf("pass counts = %+v", counts)
	}
	w.src.Err = nil
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) { s.Title = "Back"; s.Updated = t0.Add(time.Hour) })
	w.now = t0.Add(DefaultErrorBackoff - time.Minute)
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.open.ID); got.Title == "Back" {
		t.Error("the poll did not hold off after the manual refresh's rate limit")
	}
	w.now = t0.Add(DefaultErrorBackoff + time.Minute)
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, w.open.ID); got.Title != "Back" {
		t.Errorf("after the backoff = %q", got.Title)
	}
}

// An integration that does not resolve is an error on the ticket.
func TestSyncUnresolvableIntegration(t *testing.T) {
	w := newSyncWorld(t)
	w.s.Sources = stubSources{err: confstore.ErrNotFound}
	ctx := context.Background()
	if err := w.s.Refresh(ctx, w.open.ID); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("refresh = %v", err)
	}
	if got := w.ticket(t, w.open.ID); got.SyncState != confstore.SyncError {
		t.Errorf("sync state = %q", got.SyncState)
	}
}

// A ticket with no source has nothing to sync.
func TestSyncSourcelessIsANoOp(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	tk, err := w.f.Create(ctx, confstore.Ticket{ProjectID: testProject, Title: "By hand"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.Refresh(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	if got := w.ticket(t, tk.ID); got.SyncState != "" || got.SyncedAt != nil {
		t.Errorf("sourceless ticket = %+v", got)
	}
}

// gatedSource holds every Fetch until released, counting them.
type gatedSource struct {
	*fake.TicketSource
	entered chan struct{}
	release chan struct{}
	fetches atomic.Int32
}

func (g *gatedSource) Fetch(ctx context.Context, key string) (trackerport.SourceTicket, error) {
	g.fetches.Add(1)
	g.entered <- struct{}{}
	<-g.release
	return g.TicketSource.Fetch(ctx, key)
}

// parkSpy is a context that says when its Done channel is first asked for:
// a Refresh waiting on another caller's flight asks for it before anything
// else, a Refresh doing the work does not.
type parkSpy struct {
	context.Context
	once   sync.Once
	parked chan struct{}
}

func (p *parkSpy) Done() <-chan struct{} {
	p.once.Do(func() { close(p.parked) })
	return p.Context.Done()
}

// Two refreshes of one ticket at once read the tracker once.
func TestRefreshIsSingleFlight(t *testing.T) {
	w := newSyncWorld(t)
	g := &gatedSource{TicketSource: w.src, entered: make(chan struct{}, 2), release: make(chan struct{})}
	w.s.Sources = stubSources{src: g, kind: "faketracker"}
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = w.s.Refresh(ctx, w.open.ID) }()
	<-g.entered
	spy := &parkSpy{Context: ctx, parked: make(chan struct{})}
	go func() { defer wg.Done(); errs[1] = w.s.Refresh(spy, w.open.ID) }()
	select {
	case <-spy.parked:
	case <-g.entered:
		t.Error("the second refresh read the tracker too")
	case <-time.After(5 * time.Second):
		t.Fatal("the second refresh neither waited nor fetched")
	}
	close(g.release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v", errs)
	}
	if n := g.fetches.Load(); n != 1 {
		t.Errorf("fetches = %d, want 1", n)
	}
}

// remoteFiles lists a ticket's stored files as remote id → bytes.
func (w *syncWorld) remoteFiles(t *testing.T, ticketID string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, a := range ticketAttachments(t, w.att, ticketID) {
		data, _, err := w.att.Bytes(context.Background(), a.ID)
		if err != nil {
			t.Fatal(err)
		}
		out[a.RemoteID] = string(data)
	}
	return out
}

// A file the issue gains is copied by the next sync — the poll's batch and a
// refresh by name alike — once: a repeat adds nothing, and one the issue
// drops stays, since runs pin attachment ids. A done ticket is left to a
// refresh by name, as for everything else.
func TestSyncCopiesNewAttachments(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	w.src.Blobs = map[string][]byte{"f1": []byte("one"), "f2": []byte("two"), "d1": []byte("done")}
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) {
		s.Attachments = []trackerport.SourceAttachment{{ID: "f1", Filename: "one.md", Mime: "text/markdown"}}
	})
	w.edit("PROJ-8", func(s *trackerport.SourceTicket) {
		s.Attachments = []trackerport.SourceAttachment{{ID: "d1", Filename: "done.md", Mime: "text/markdown"}}
	})
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.remoteFiles(t, w.open.ID); !maps.Equal(got, map[string]string{"f1": "one"}) {
		t.Errorf("after the poll = %v", got)
	}
	if got := w.remoteFiles(t, w.closed.ID); len(got) != 0 {
		t.Errorf("the poll copied a done ticket's file: %v", got)
	}
	// Again, by the poll and by name: nothing new.
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.s.Refresh(ctx, w.open.ID); err != nil {
		t.Fatal(err)
	}
	if got := ticketAttachments(t, w.att, w.open.ID); len(got) != 1 {
		t.Errorf("a repeat sync copied again: %+v", got)
	}
	// The issue swaps f1 for f2 (listed twice): f2 lands once, f1 stays.
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) {
		s.Attachments = []trackerport.SourceAttachment{
			{ID: "f2", Filename: "two.md", Mime: "text/markdown"}, {ID: "f2", Filename: "two.md", Mime: "text/markdown"}}
	})
	if err := w.s.Refresh(ctx, w.open.ID); err != nil {
		t.Fatal(err)
	}
	if got := w.remoteFiles(t, w.open.ID); !maps.Equal(got, map[string]string{"f1": "one", "f2": "two"}) {
		t.Errorf("after the swap = %v", got)
	}
	if got := w.ticket(t, w.open.ID); got.SyncState != confstore.SyncOK || got.SyncError != "" {
		t.Errorf("sync state = %q %q", got.SyncState, got.SyncError)
	}
	if err := w.s.Refresh(ctx, w.closed.ID); err != nil {
		t.Fatal(err)
	}
	if got := w.remoteFiles(t, w.closed.ID); !maps.Equal(got, map[string]string{"d1": "done"}) {
		t.Errorf("a refresh by name of a done ticket = %v", got)
	}
}

// A file that fails to download leaves the rest of the sync written, is
// named on the ticket, makes the sync a *PartialSyncError counted Failed, and
// is copied by the next poll — which does not back off for it. One the upload
// policy refuses is no failure of the sync: it is only logged.
func TestSyncRetriesAFailedAttachment(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	w.src.Blobs = map[string][]byte{"f1": []byte("one"), "z1": []byte("PK\x03\x04zip")}
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) {
		s.Title, s.Updated = "Edited", t0.Add(time.Hour)
		s.Attachments = []trackerport.SourceAttachment{
			{ID: "f1", Filename: "one.md", Mime: "text/markdown"},
			{ID: "f2", Filename: "two.md", Mime: "text/markdown"}, // no blob yet: the download fails
			{ID: "z1", Filename: "archive.zip", Mime: "application/zip"},
		}
	})
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got := w.ticket(t, w.open.ID)
	if got.Title != "Edited" {
		t.Errorf("a failed file undid the mirror: title %q", got.Title)
	}
	if got.SyncState != confstore.SyncError || !strings.Contains(got.SyncError, "two.md") ||
		strings.Contains(got.SyncError, "archive.zip") {
		t.Errorf("sync state = %q %q, want an error naming two.md alone", got.SyncState, got.SyncError)
	}
	if files := w.remoteFiles(t, w.open.ID); !maps.Equal(files, map[string]string{"f1": "one"}) {
		t.Errorf("after the failure = %v", files)
	}
	// The pass counts it failed; a refresh by name returns the typed error.
	counts, err := w.s.RefreshIntegration(ctx, "int-1")
	if err != nil {
		t.Fatal(err)
	}
	if counts != (SyncCounts{Tickets: 2, Synced: 1, Failed: 1}) {
		t.Errorf("counts = %+v", counts)
	}
	var partial *PartialSyncError
	if err := w.s.Refresh(ctx, w.open.ID); !errors.As(err, &partial) || partial.Key != "PROJ-7" ||
		len(partial.Failures) != 1 || !strings.Contains(partial.Failures[0], "two.md") {
		t.Errorf("refresh = %v, want a partial sync naming two.md", err)
	}
	if got := w.ticket(t, w.open.ID); got.Title != "Edited" || got.SyncState != confstore.SyncError {
		t.Errorf("after the refresh = %q %q", got.Title, got.SyncState)
	}
	w.src.Blobs["f2"] = []byte("two")
	if err := w.s.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if files := w.remoteFiles(t, w.open.ID); !maps.Equal(files, map[string]string{"f1": "one", "f2": "two"}) {
		t.Errorf("after the retry = %v", files)
	}
	if got := w.ticket(t, w.open.ID); got.SyncState != confstore.SyncOK || got.SyncError != "" {
		t.Errorf("sync state after the retry = %q %q", got.SyncState, got.SyncError)
	}
}

// A ticket imported before remote ids were kept holds its files with none.
// The sync recognises them — by name and the tracker's size, or by name
// alone when that is unambiguous — and gives them their ids instead of
// copying them again; what it cannot tell apart it copies.
func TestSyncAdoptsFilesImportedBeforeRemoteIDs(t *testing.T) {
	w := newSyncWorld(t)
	ctx := context.Background()
	legacy := func(name, body string) string {
		t.Helper()
		a, err := w.att.Save(ctx, w.open.UserID, name, "text/markdown", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := w.att.ClaimForTicket(ctx, w.open.UserID, []string{a.ID}, w.open.ID); err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	spec := legacy("spec.md", "hello")  // the tracker's size matches
	notes := legacy("notes.md", "abc")  // no size from the tracker, one candidate
	dupA := legacy("shot.md", "first")  // no size, two candidates: ambiguous
	dupB := legacy("shot.md", "second") // (the other one)
	sized := legacy("size.md", "12345") // the tracker's size differs
	// Bytes only for what must be downloaded: an adoption reads nothing.
	w.src.Blobs = map[string][]byte{"r-shot": []byte("third"), "r-size": []byte("123")}
	w.edit("PROJ-7", func(s *trackerport.SourceTicket) {
		s.Attachments = []trackerport.SourceAttachment{
			{ID: "r-spec", Filename: "spec.md", Mime: "text/markdown", Size: 5},
			{ID: "r-notes", Filename: "notes.md", Mime: "text/markdown"},
			{ID: "r-shot", Filename: "shot.md", Mime: "text/markdown"},
			{ID: "r-size", Filename: "size.md", Mime: "text/markdown", Size: 3},
		}
	})
	check := func(when string) {
		t.Helper()
		rows := ticketAttachments(t, w.att, w.open.ID)
		got := map[string]string{} // row id (or the bytes of a new row) → remote id
		for _, a := range rows {
			key := a.ID
			if !slices.Contains([]string{spec, notes, dupA, dupB, sized}, a.ID) {
				data, _, err := w.att.Bytes(ctx, a.ID)
				if err != nil {
					t.Fatal(err)
				}
				key = "new:" + string(data)
			}
			got[key] = a.RemoteID
		}
		want := map[string]string{spec: "r-spec", notes: "r-notes", dupA: "", dupB: "", sized: "",
			"new:third": "r-shot", "new:123": "r-size"}
		if !maps.Equal(got, want) {
			t.Errorf("%s: rows = %v, want %v", when, got, want)
		}
	}
	if err := w.s.Refresh(ctx, w.open.ID); err != nil {
		t.Fatal(err)
	}
	check("first refresh")
	if err := w.s.Refresh(ctx, w.open.ID); err != nil {
		t.Fatal(err)
	}
	check("second refresh")
	if got := w.ticket(t, w.open.ID); got.SyncState != confstore.SyncOK {
		t.Errorf("sync state = %q %q", got.SyncState, got.SyncError)
	}
}
