package tickets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
)

const (
	owner       = "u1"
	testProject = "prj-1"
	// remote is the tracker integration every remote read goes to.
	remote = "int-1"
)

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// clock is the fake clock a test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// countingSource is a fake tracker that counts its reads and, while err is
// set, fails them all.
type countingSource struct {
	*fake.TicketSource
	mu                              sync.Mutex
	searches, fetches, opens, types int
	err                             error
}

func (c *countingSource) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// count bumps n and says whether the read fails.
func (c *countingSource) count(n *int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*n++
	return c.err
}

// calls is how many searches and fetches were made.
func (c *countingSource) calls() (searches, fetches int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.searches, c.fetches
}

func (c *countingSource) Search(ctx context.Context, q trackerport.SearchQuery) (trackerport.SearchPage, error) {
	if err := c.count(&c.searches); err != nil {
		return trackerport.SearchPage{}, err
	}
	return c.TicketSource.Search(ctx, q)
}

func (c *countingSource) Fetch(ctx context.Context, key string) (trackerport.SourceTicket, error) {
	if err := c.count(&c.fetches); err != nil {
		return trackerport.SourceTicket{}, err
	}
	return c.TicketSource.Fetch(ctx, key)
}

func (c *countingSource) OpenAttachment(ctx context.Context, a trackerport.SourceAttachment) (io.ReadCloser, error) {
	if err := c.count(&c.opens); err != nil {
		return nil, err
	}
	return c.TicketSource.OpenAttachment(ctx, a)
}

func (c *countingSource) Types(ctx context.Context) ([]trackerport.RemoteType, error) {
	if err := c.count(&c.types); err != nil {
		return nil, err
	}
	return c.TicketSource.Types(ctx)
}

// stubSources serves a tracker per integration id.
type stubSources map[string]trackerport.TicketSource

func (s stubSources) TicketSource(_ context.Context, id string) (trackerport.TicketSource, error) {
	src, ok := s[id]
	if !ok {
		return nil, fmt.Errorf("integration %s: %w", id, confstore.ErrNotFound)
	}
	return src, nil
}

func (s stubSources) TicketSourceKind(context.Context, string) (string, error) {
	return "faketracker", nil
}

// world is a config store with one workshop and one remote tracker
// integration (remote) of the owner's, a Live over it, and a Factory.
type world struct {
	f     *Factory
	live  *Live
	clock *clock
	src   *countingSource
	conf  *confstore.Service
	att   *filestore.Attachments
}

func newWorld(t *testing.T, src *fake.TicketSource) world {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	conf := confstore.New(confstore.NewMem(), key)
	conftest.SeedProject(t, conf, confstore.Project{ID: testProject, UserID: owner, Name: "p"})
	addTracker(t, conf, remote)
	cs := &countingSource{TicketSource: src}
	clk := &clock{t: t0}
	live := NewLive(conf, stubSources{remote: cs}, WithClock(clk.Now))
	att := filestore.NewAttachments(filestore.NewMem(), filestore.NewBlobs(t.TempDir()),
		1<<20, []string{"text/*", "image/png"})
	f := &Factory{Conf: conf, Attachments: att, Sources: stubSources{remote: cs}, Live: live}
	return world{f: f, live: live, clock: clk, src: cs, conf: conf, att: att}
}

// addTracker adds a remote tracker integration of the owner's.
func addTracker(t *testing.T, conf *confstore.Service, id string) {
	t.Helper()
	if err := conf.DB().UpsertIntegration(context.Background(), confstore.StoredIntegration{ID: id, UserID: owner,
		Port: confstore.PortTicketSource, Kind: "faketracker", Name: "tracker " + id, Config: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
}

// internalSource is the owner's internal tracker.
func (w world) internalSource(t *testing.T) string {
	t.Helper()
	in, err := w.conf.InternalTracker(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	return in.ID
}

// record records the source's first issue.
func (w world) record(t *testing.T) confstore.Ticket {
	t.Helper()
	issue, err := w.src.TicketSource.Fetch(context.Background(), w.src.Tickets[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := w.f.Record(context.Background(), remote, issue, "")
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func devSource() *fake.TicketSource {
	return &fake.TicketSource{
		Tickets: []trackerport.SourceTicket{{
			RemoteID: "10007", Key: "PROJ-7", Title: "Add a Farewell function",
			Body: "The greeter only says hello.", URL: "https://tracker.test/browse/PROJ-7",
			Status: "To Do", StatusID: "1", Type: "Story", TypeID: "10",
			Attachments: []trackerport.SourceAttachment{
				{ID: "a1", Filename: "spec.md", Mime: "text/markdown", Size: 5},
			},
		}},
		Blobs: map[string][]byte{"a1": []byte("hello")},
	}
}

func ticketAttachments(t *testing.T, att *filestore.Attachments, ticketID string) []filestore.Attachment {
	t.Helper()
	out, err := att.ForTicket(context.Background(), ticketID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// --- tests ------------------------------------------------------------------

// A hand-written ticket lands on its owner's internal tracker, its uploads
// claimed for it; no base branch is named until a run's workshop names it.
func TestCreateWritesAnInternalTicket(t *testing.T) {
	w := newWorld(t, devSource())
	ctx := context.Background()
	up, err := w.att.Save(ctx, owner, "design.md", "text/markdown", strings.NewReader("# d"))
	if err != nil {
		t.Fatal(err)
	}
	tk, err := w.f.Create(ctx, confstore.Ticket{UserID: owner, Title: "T", Goal: "g"}, owner, []string{up.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !tk.Internal() || tk.SourceID != w.internalSource(t) || tk.RemoteID != tk.ID || tk.SourceKey != "" {
		t.Errorf("ticket = %+v, want the internal tracker's", tk)
	}
	if tk.BaseBranch != "" || tk.ChangeType != trackerport.ChangeFeat {
		t.Errorf("branch %q, type %q: want none and feat", tk.BaseBranch, tk.ChangeType)
	}
	if got, _ := w.att.Get(ctx, up.ID); got.TicketID != tk.ID || got.RunID != "" {
		t.Errorf("upload = %+v, want claimed for the ticket", got)
	}
}

// A collaborator writing the owner's ticket attaches their own uploads, on
// create and on update; an id of the owner's they send stays the owner's.
func TestTicketFilesAreTheWritersUploads(t *testing.T) {
	w := newWorld(t, devSource())
	ctx := context.Background()
	save := func(user, name string) filestore.Attachment {
		t.Helper()
		a, err := w.att.Save(ctx, user, name, "text/markdown", strings.NewReader("# "+name))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	mine, owners := save("collab", "mine.md"), save(owner, "owners.md")
	tk, err := w.f.Create(ctx, confstore.Ticket{UserID: owner, Title: "T", Goal: "g"}, "collab", []string{mine.ID, owners.ID})
	if err != nil {
		t.Fatal(err)
	}
	later := save("collab", "later.md")
	if _, err := w.f.Update(ctx, tk.ID, confstore.Ticket{Title: "T", Goal: "g"}, "collab", []string{mine.ID, later.ID}); err != nil {
		t.Fatal(err)
	}
	if tk.UserID != owner {
		t.Errorf("ticket owner = %q, want the workshop owner", tk.UserID)
	}
	for _, a := range []filestore.Attachment{mine, later} {
		if got, _ := w.att.Get(ctx, a.ID); got.TicketID != tk.ID {
			t.Errorf("%s = %+v, want claimed for the ticket", a.Filename, got)
		}
	}
	if got, _ := w.att.Get(ctx, owners.ID); !got.Unclaimed() {
		t.Errorf("the owner's upload, sent by another = %+v, want unclaimed", got)
	}
}

// A change type is one of the canonical ones, spelled any case, or the write
// is refused.
func TestChangeTypeIsValidated(t *testing.T) {
	w := newWorld(t, devSource())
	_, err := w.f.Create(context.Background(), confstore.Ticket{UserID: owner, Title: "T", Goal: "g", ChangeType: "bugfix"}, owner, nil)
	if !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("err = %v, want invalid", err)
	}
	tk, err := w.f.Create(context.Background(), confstore.Ticket{UserID: owner, Title: "Crash", Goal: "g", ChangeType: "FIX"}, owner, nil)
	if err != nil || tk.ChangeType != trackerport.ChangeFix {
		t.Errorf("ticket = %+v (%v)", tk, err)
	}
}

// A record is made once per issue: recording it again rewrites what was seen
// of it and keeps the automation that made it.
func TestRecordIsOncePerIssue(t *testing.T) {
	w := newWorld(t, devSource())
	ctx := context.Background()
	issue := w.src.Tickets[0]
	first, created, err := w.f.Record(ctx, remote, issue, "auto-1")
	if err != nil || !created {
		t.Fatalf("first = %+v, created %v (%v)", first, created, err)
	}
	if first.SourceID != remote || first.SourceKind != "faketracker" || first.RemoteID != "10007" || first.SourceKey != "PROJ-7" ||
		first.Title != issue.Title || first.Goal != issue.Body || first.SourceType != "Story" || first.ChangeType != trackerport.ChangeFeat ||
		first.UserID != owner || first.AutomationID != "auto-1" {
		t.Errorf("record = %+v", first)
	}
	issue.Title, issue.Type, issue.TypeID = "Renamed", "Bug", "11"
	again, created, err := w.f.Record(ctx, remote, issue, "auto-2")
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("again = %+v, created %v (%v)", again, created, err)
	}
	if again.Title != "Renamed" || again.ChangeType != trackerport.ChangeFix || again.AutomationID != "auto-1" {
		t.Errorf("again = %+v, want the new title and type, the first automation", again)
	}
	// A moved issue keeps its record: the key follows.
	issue.Key = "NEW-1"
	moved, err := w.f.Seen(ctx, first.ID, issue)
	if err != nil || moved.SourceKey != "NEW-1" || moved.RemoteID != "10007" {
		t.Errorf("moved = %+v (%v)", moved, err)
	}
}

// A record takes only its base branch from an edit: the rest is its
// tracker's.
func TestUpdateOfARecordTakesTheBaseBranch(t *testing.T) {
	w := newWorld(t, devSource())
	rec := w.record(t)
	upd, err := w.f.Update(context.Background(), rec.ID, confstore.Ticket{Title: "renamed", BaseBranch: "feat/elsewhere"}, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if upd.BaseBranch != "feat/elsewhere" || upd.Title != rec.Title || upd.SourceKey != "PROJ-7" {
		t.Errorf("after edit = %+v", upd)
	}
}

// Delete removes the ticket AND its attachment rows — the old leak.
func TestDeleteReclaimsAttachments(t *testing.T) {
	w := newWorld(t, devSource())
	ctx := context.Background()
	rec := w.record(t)
	if _, _, err := w.f.RunTicket(ctx, rec.ID, testProject); err != nil {
		t.Fatal(err)
	}
	atts := ticketAttachments(t, w.att, rec.ID)
	if len(atts) != 1 {
		t.Fatalf("setup: attachments = %+v", atts)
	}
	if err := w.f.Delete(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.conf.GetTicket(ctx, rec.ID); err == nil {
		t.Error("ticket row survived delete")
	}
	if _, err := w.att.Get(ctx, atts[0].ID); !errors.Is(err, filestore.ErrNotFound) {
		t.Errorf("attachment row survived ticket delete: %v", err)
	}
}

// With no tracker wired at all, a live read says so instead of failing
// obscurely.
func TestReadsWithoutSourcesAreHonest(t *testing.T) {
	w := newWorld(t, devSource())
	ctx := context.Background()
	bare := NewLive(w.conf, nil)
	if _, err := bare.List(ctx, remote, trackerport.SearchQuery{}, false); !errors.Is(err, ErrNoSources) {
		t.Errorf("live list err = %v, want ErrNoSources", err)
	}
}
