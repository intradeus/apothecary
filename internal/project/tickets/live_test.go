package tickets

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// Every workshop on a source reads the same copies: a page and an issue are
// read from the tracker once, whoever asks, and a page carries the records
// Apothecary holds.
func TestWorkshopsOnOneSourceShareItsCopies(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, devSource())
	rec := w.record(t)
	q := trackerport.SearchQuery{Done: trackerport.DoneOpen, Limit: 10}
	for range 2 {
		page, err := w.live.List(ctx, remote, q, false)
		if err != nil || len(page.Rows) != 1 || page.Rows[0].Record == nil || page.Rows[0].Record.ID != rec.ID {
			t.Fatalf("page = %+v (%v)", page, err)
		}
		if _, err := w.live.Issue(ctx, remote, "PROJ-7", false); err != nil {
			t.Fatal(err)
		}
	}
	if searches, fetches := w.src.calls(); searches != 1 || fetches != 1 {
		t.Errorf("%d searches, %d fetches; want one of each", searches, fetches)
	}
	// Past the TTL the copy is still served, while one reread replaces it.
	w.clock.add(DefaultListTTL + time.Second)
	if _, err := w.live.List(ctx, remote, q, false); err != nil {
		t.Fatal(err)
	}
	w.live.cache.Wait()
	if searches, _ := w.src.calls(); searches != 2 {
		t.Errorf("%d searches after the TTL, want the one reread", searches)
	}
	// A refresh reads now.
	if _, err := w.live.List(ctx, remote, q, true); err != nil {
		t.Fatal(err)
	}
	if searches, _ := w.src.calls(); searches != 3 {
		t.Errorf("%d searches after a refresh", searches)
	}
}

// A write through the source's writer drops that issue's copies and every
// list page of the source, and nothing of another integration; a write that
// fails drops nothing.
func TestAWriteDropsTheIssueAndTheListPages(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, devSource())
	addTracker(t, w.conf, "int-2")
	other := &countingSource{TicketSource: devSource()}
	w.live.sources = stubSources{remote: w.src, "int-2": other}
	read := func() {
		t.Helper()
		for _, id := range []string{remote, "int-2"} {
			if _, err := w.live.List(ctx, id, trackerport.SearchQuery{}, false); err != nil {
				t.Fatal(err)
			}
			if _, err := w.live.Issue(ctx, id, "PROJ-7", false); err != nil {
				t.Fatal(err)
			}
		}
	}
	read()
	wr := w.live.Writer(remote, w.src.TicketSource)
	if err := wr.Transition(ctx, "PROJ-7", "Done"); err != nil {
		t.Fatal(err)
	}
	read()
	if searches, fetches := w.src.calls(); searches != 2 || fetches != 2 {
		t.Errorf("written source: %d searches, %d fetches; want each reread once", searches, fetches)
	}
	if searches, fetches := other.calls(); searches != 1 || fetches != 1 {
		t.Errorf("other source: %d searches, %d fetches; want its copies kept", searches, fetches)
	}
	if got, err := w.live.Issue(ctx, remote, "PROJ-7", false); err != nil || got.Status != "Done" || !got.Done {
		t.Errorf("after the move = %+v (%v)", got, err)
	}

	for name, write := range map[string]func() error{
		"comment": func() error { return wr.Comment(ctx, "PROJ-7", "hi") },
		"fields":  func() error { return wr.SetFields(ctx, "PROJ-7", map[string]string{"labels": "x"}) },
		"link":    func() error { return wr.AddLink(ctx, "PROJ-7", trackerport.TicketLink{URL: "https://x.test"}) },
		"attach": func() error {
			_, err := wr.(trackerport.TicketAttacher).Attach(ctx, "PROJ-7", "a.txt", "text/plain", []byte("a"))
			return err
		},
	} {
		_, before := w.src.calls()
		if err := write(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := w.live.Issue(ctx, remote, "PROJ-7", false); err != nil {
			t.Fatal(err)
		}
		if _, after := w.src.calls(); after != before+1 {
			t.Errorf("%s: the issue was not reread", name)
		}
	}

	w.src.WriteErr = errors.New("refused")
	_, before := w.src.calls()
	if err := wr.Transition(ctx, "PROJ-7", "To Do"); err == nil {
		t.Fatal("the refused write succeeded")
	}
	if _, err := w.live.Issue(ctx, remote, "PROJ-7", false); err != nil {
		t.Fatal(err)
	}
	if _, after := w.src.calls(); after != before {
		t.Error("a failed write dropped the copy")
	}
}

// noAttach is a tracker that cannot host files.
type noAttach struct{ trackerport.TicketWriter }

// A wrapped writer hosts files exactly when the tracker's does.
func TestTheWriterKeepsTheAttachCapability(t *testing.T) {
	w := newWorld(t, devSource())
	if _, ok := w.live.Writer(remote, w.src.TicketSource).(trackerport.TicketAttacher); !ok {
		t.Error("the fake's attach capability was lost")
	}
	if _, ok := w.live.Writer(remote, noAttach{w.src.TicketSource}).(trackerport.TicketAttacher); ok {
		t.Error("a writer gained an attach capability")
	}
}

// A tracker's rate limit holds its integration for the wait it asked: the
// cached page is served and nothing is read until the wait ends; a page with
// no copy answers the rate limit.
func TestARateLimitServesTheCopyUntilTheWaitEnds(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, devSource())
	q := trackerport.SearchQuery{Limit: 10}
	if _, err := w.live.List(ctx, remote, q, false); err != nil {
		t.Fatal(err)
	}
	w.src.fail(&trackerport.ErrRateLimited{Tracker: "fake", RetryAfter: 5 * time.Minute})
	w.clock.add(2 * time.Minute)
	if page, err := w.live.List(ctx, remote, q, false); err != nil || len(page.Rows) != 1 {
		t.Fatalf("stale page = %+v (%v)", page, err)
	}
	w.live.cache.Wait() // the reread met the rate limit
	if searches, _ := w.src.calls(); searches != 2 {
		t.Fatalf("%d searches", searches)
	}
	w.src.fail(nil)
	w.clock.add(time.Minute)
	if page, err := w.live.List(ctx, remote, q, true); err != nil || len(page.Rows) != 1 {
		t.Errorf("held refresh = %+v (%v), want the copy", page, err)
	}
	_, err := w.live.List(ctx, remote, trackerport.SearchQuery{Text: "other"}, false)
	var rl *trackerport.ErrRateLimited
	if !errors.As(err, &rl) {
		t.Errorf("a page with no copy = %v, want the rate limit", err)
	}
	w.live.cache.Wait()
	if searches, _ := w.src.calls(); searches != 2 {
		t.Errorf("%d searches while held, want none", searches)
	}
	w.clock.add(5 * time.Minute) // past the hold, set two minutes in for five
	if _, err := w.live.List(ctx, remote, q, false); err != nil {
		t.Fatal(err)
	}
	w.live.cache.Wait()
	if searches, _ := w.src.calls(); searches != 3 {
		t.Errorf("%d searches after the wait, want the reread", searches)
	}
}

// A rate limit that names no wait holds for RateLimitBackoff.
func TestARateLimitWithNoWaitBacksOff(t *testing.T) {
	if d, ok := rateLimitHold(&trackerport.ErrRateLimited{Tracker: "fake"}); !ok || d != RateLimitBackoff {
		t.Errorf("hold = %v, %v", d, ok)
	}
	if d, ok := rateLimitHold(&trackerport.ErrRateLimited{RetryAfter: time.Hour}); !ok || d != time.Hour {
		t.Errorf("hold = %v, %v", d, ok)
	}
	if _, ok := rateLimitHold(errors.New("500")); ok {
		t.Error("a plain failure holds")
	}
}

// The internal tracker is read from the store, never cached: a ticket written
// is listed at once, done is its status's Done, and the keyset cursor is
// stable under a ticket created between pages.
func TestTheInternalTrackerIsReadFromTheStore(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, devSource())
	w.live.sources = nil // a remote read would fail
	internal := w.internalSource(t)
	var ids []string
	for _, title := range []string{"one", "two", "three", "four"} {
		tk, err := w.f.Create(ctx, confstore.Ticket{UserID: owner, Title: title, Goal: "g"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tk.ID)
	}
	statuses, err := w.conf.TrackerStatuses(ctx, internal)
	if err != nil {
		t.Fatal(err)
	}
	done := statuses[slices.IndexFunc(statuses, func(s confstore.TrackerStatus) bool { return s.IsDone })]
	if err := w.conf.SetTicketStatus(ctx, ids[1], done.ID, confstore.ChangedByUser); err != nil {
		t.Fatal(err)
	}

	open := trackerport.SearchQuery{Done: trackerport.DoneOpen, Limit: 2}
	first, err := w.live.List(ctx, internal, open, false)
	if err != nil || len(first.Rows) != 2 || first.Next == "" {
		t.Fatalf("first page = %+v (%v)", first, err)
	}
	if got := []string{first.Rows[0].Issue.Title, first.Rows[1].Issue.Title}; !slices.Equal(got, []string{"four", "three"}) {
		t.Errorf("first page = %v, want the newest open ones", got)
	}
	row := first.Rows[0]
	if row.Record == nil || row.Record.ID != row.Issue.Key || row.Issue.RemoteID != row.Issue.Key || row.Issue.Status != "To do" || row.Issue.Done {
		t.Errorf("row = %+v", row)
	}
	if _, err := w.f.Create(ctx, confstore.Ticket{UserID: owner, Title: "five", Goal: "g"}, nil); err != nil {
		t.Fatal(err)
	}
	open.Cursor = first.Next
	second, err := w.live.List(ctx, internal, open, false)
	if err != nil || len(second.Rows) != 1 || second.Rows[0].Issue.Title != "one" || second.Next != "" {
		t.Errorf("second page = %+v (%v), want one alone", second, err)
	}
	all, err := w.live.List(ctx, internal, trackerport.SearchQuery{Done: trackerport.DoneOnly}, false)
	if err != nil || len(all.Rows) != 1 || all.Rows[0].Issue.Title != "two" || !all.Rows[0].Issue.Done || all.Rows[0].Issue.Status != done.Name {
		t.Errorf("done = %+v (%v)", all, err)
	}
	if _, err := w.live.List(ctx, internal, trackerport.SearchQuery{Cursor: "not-a-cursor"}, false); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("a bad cursor = %v", err)
	}

	got, err := w.live.Issue(ctx, internal, ids[0], true)
	if err != nil || got.Title != "one" || got.Body != "g" {
		t.Errorf("issue = %+v (%v)", got, err)
	}
	if _, err := w.live.Issue(ctx, internal, "missing", false); !errors.Is(err, trackerport.ErrTicketNotFound) {
		t.Errorf("a missing ticket = %v", err)
	}
	sts, err := w.live.Statuses(ctx, internal, false)
	if err != nil || len(sts) != len(statuses) || sts[0].Category != trackerport.CategoryNew {
		t.Errorf("statuses = %+v (%v)", sts, err)
	}
	for _, s := range sts {
		if (s.ID == done.ID) != (s.Category == trackerport.CategoryDone) {
			t.Errorf("status %+v", s)
		}
	}
	if types, err := w.live.Types(ctx, internal, true); err != nil || len(types) == 0 {
		t.Errorf("types = %+v (%v)", types, err)
	}
	if comments, err := w.live.Comments(ctx, internal, ids[0], true); err != nil || len(comments) != 0 {
		t.Errorf("comments = %+v (%v)", comments, err)
	}
}

// A remote tracker's types fill the integration's type list, a new one with
// the change type its name reads as; the list is reread once per TTL.
func TestTypesAddTheTrackersNewTypes(t *testing.T) {
	ctx := context.Background()
	src := devSource()
	src.RemoteTypes = []trackerport.RemoteType{{ID: "10", Name: "Story"}, {ID: "11", Name: "Bug"}}
	w := newWorld(t, src)
	types, err := w.live.Types(ctx, remote, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := changeTypes(types); got["Story"] != trackerport.ChangeFeat || got["Bug"] != trackerport.ChangeFix || len(got) != 2 {
		t.Errorf("types = %v", got)
	}
	src.RemoteTypes = append(src.RemoteTypes, trackerport.RemoteType{ID: "12", Name: "Incident"})
	if types, err = w.live.Types(ctx, remote, false); err != nil || len(types) != 2 || w.src.types != 1 {
		t.Errorf("within the TTL: %v (%v), %d reads", changeTypes(types), err, w.src.types)
	}
	types, err = w.live.Types(ctx, remote, true)
	if got := changeTypes(types); err != nil || got["Incident"] != trackerport.ChangeHotfix || len(got) != 3 {
		t.Errorf("refreshed = %v (%v)", got, err)
	}
}

func changeTypes(types []confstore.TrackerType) map[string]trackerport.ChangeType {
	out := map[string]trackerport.ChangeType{}
	for _, ty := range types {
		out[ty.Name] = ty.ChangeType
	}
	return out
}

// A remote tracker's statuses and comments are read live and cached.
func TestStatusesAndCommentsOfARemoteTracker(t *testing.T) {
	ctx := context.Background()
	src := devSource()
	src.Thread = map[string][]trackerport.Comment{"PROJ-7": {{ID: "c1", Body: "hi"}}}
	w := newWorld(t, src)
	sts, err := w.live.Statuses(ctx, remote, false)
	if err != nil || len(sts) != len(fake.DefaultStatuses) {
		t.Errorf("statuses = %+v (%v)", sts, err)
	}
	cs, err := w.live.Comments(ctx, remote, "PROJ-7", false)
	if err != nil || len(cs) != 1 || cs[0].Body != "hi" {
		t.Errorf("comments = %+v (%v)", cs, err)
	}
	// Invalidate drops them: the next read sees the new comment.
	src.Thread["PROJ-7"] = append(src.Thread["PROJ-7"], trackerport.Comment{ID: "c2", Body: "again"})
	if cs, _ := w.live.Comments(ctx, remote, "PROJ-7", false); len(cs) != 1 {
		t.Errorf("within the TTL = %+v", cs)
	}
	w.live.Invalidate(remote, "PROJ-7")
	if cs, _ := w.live.Comments(ctx, remote, "PROJ-7", false); len(cs) != 2 {
		t.Errorf("after Invalidate = %+v", cs)
	}
}

// A record whose key the tracker no longer has is retried by its stable id
// only where the tracker fetches by one (Jira). On a tracker that cannot
// (GitHub, GitLab), where Fetch reads a bare number as an issue number, the
// id is never handed to Fetch: the unrelated issue numbered like it is not
// taken for the moved one.
func TestAMissingKeyNeverAliasesAnIssueNumberedLikeItsID(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, devSource())
	rec := w.record(t)
	// The issue moved; an unrelated one answers to its id as a key.
	w.src.Tickets[0].Key = "NEW-7"
	w.src.Tickets = append(w.src.Tickets, trackerport.SourceTicket{RemoteID: "555", Key: rec.RemoteID, Title: "Unrelated"})

	got, err := w.live.IssueOf(ctx, rec, true)
	if err != nil || got.Key != "NEW-7" || got.RemoteID != rec.RemoteID {
		t.Errorf("by stable id = %+v (%v), want the moved issue", got, err)
	}
	w.live.Invalidate(remote, rec.SourceKey) // no copy to fall back on
	w.live.sources = stubSources{remote: fake.ReadOnlyTicketSource{Src: w.src.TicketSource}}
	got, err = w.live.IssueOf(ctx, rec, true)
	if !errors.Is(err, trackerport.ErrTicketNotFound) {
		t.Errorf("no stable-id fetch = %+v (%v), want not found", got, err)
	}
}
