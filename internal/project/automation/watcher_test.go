package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/tickets"
	"apothecary/internal/run"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workitem"
	"apothecary/internal/workitem/items"
)

// fakeEngine records what the watcher asked for and answers RunsOf from
// it; Fail refuses every creation (a preflight refusal, say).
type fakeEngine struct {
	mu    sync.Mutex
	Fail  error
	calls []struct {
		Ticket runinput.Ticket
		Flow   string
		In     runinput.Input
	}
	// runs are the runs created on each work item.
	runs map[workitem.Ref][]run.View
	next int
	// MRs is what ListMRs reports (merge_request).
	MRs []repoport.MRInfo
}

func (f *fakeEngine) CreateRunWith(_ context.Context, t runinput.Ticket, flowName, projectID string, in runinput.Input) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return "", f.Fail
	}
	f.next++
	id := fmt.Sprintf("r%d", f.next)
	if f.runs == nil {
		f.runs = map[workitem.Ref][]run.View{}
	}
	for _, item := range items.All().Requested(projectID, t, in) {
		f.runs[item] = append(f.runs[item], run.View{RunID: id, TicketID: t.TicketID})
	}
	f.calls = append(f.calls, struct {
		Ticket runinput.Ticket
		Flow   string
		In     runinput.Input
	}{t, flowName, in})
	return id, nil
}

func (f *fakeEngine) RunsOf(_ context.Context, item workitem.Ref) ([]run.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[item], nil
}

func (f *fakeEngine) ListMRs(context.Context, string, string) ([]repoport.MRInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]repoport.MRInfo(nil), f.MRs...), nil
}

func (f *fakeEngine) RepoHostUser(context.Context, string, string) (string, error) {
	return "apothecary", nil
}

func (f *fakeEngine) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeSources struct{ src *fake.TicketSource }

func (s fakeSources) TicketSource(context.Context, string) (trackerport.TicketSource, error) {
	return s.src, nil
}
func (s fakeSources) TicketSourceKind(context.Context, string) (string, error) { return "fake", nil }

type fakeChannel struct {
	mu   sync.Mutex
	sent []notifyport.Notification
}

func (c *fakeChannel) Send(_ context.Context, n notifyport.Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, n)
	return nil
}

type harness struct {
	w       *Watcher
	conf    *confstore.Service
	eng     *fakeEngine
	src     *fake.TicketSource
	ch      *fakeChannel
	project confstore.Project
	source  confstore.Integration
	now     time.Time
}

// owner owns the harness's integrations and workshop.
const owner = "u1"

func newHarness(t *testing.T) *harness {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	conf := confstore.New(confstore.NewMem(), key)
	ctx := context.Background()
	mk := func(port, name string) confstore.Integration {
		var sec map[string]string
		if port != confstore.PortHarness { // a harness holds no secret of its own
			sec = map[string]string{"token": "t"}
		}
		in, err := conf.CreateIntegration(ctx, confstore.Integration{UserID: owner, Port: port, Kind: "fake", Name: name, Secrets: sec})
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	repoHost := mk(confstore.PortRepository, "f")
	prov := mk(confstore.PortProvisioner, "p")
	hs := mk(confstore.PortHarness, "h")
	source := mk(confstore.PortTicketSource, "tracker")
	p, err := conf.CreateProject(ctx, confstore.Project{
		UserID: owner, Name: "p1", RepoAccountID: repoHost.ID, ProvisionerID: prov.ID, HarnessIDs: []string{hs.ID},
		TicketSourceID: source.ID,
		Repo:           confstore.RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42"},
		DefaultFlow:    "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	src := &fake.TicketSource{Tickets: []trackerport.SourceTicket{
		{Key: "DEV-1", RemoteID: "1001", Title: "First", Body: "do one", URL: "https://tracker.local/DEV-1"},
		{Key: "DEV-2", RemoteID: "1002", Title: "Second", Body: "do two", URL: "https://tracker.local/DEV-2"},
	}}
	h := &harness{conf: conf, eng: &fakeEngine{}, src: src, ch: &fakeChannel{}, project: p, source: source,
		now: time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)}
	sources := fakeSources{src}
	h.w = &Watcher{
		Runs:    h.eng,
		MRs:     h.eng,
		Tickets: &tickets.Factory{Conf: conf, Sources: sources, Live: tickets.NewLive(conf, sources)},
		Conf:    conf,
		Notify:  h.ch,
		Clock:   func() time.Time { return h.now },
	}
	return h
}

// record records the tracker's issue key as automationID's ("" = a person's).
func (h *harness) record(t *testing.T, key, automationID string) confstore.Ticket {
	t.Helper()
	ctx := context.Background()
	issue, err := h.src.Fetch(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := h.w.Tickets.Record(ctx, h.source.ID, issue, automationID)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// records are the workshop source's records of DEV-1 and DEV-2, by remote id.
func (h *harness) records(t *testing.T) map[string]confstore.Ticket {
	t.Helper()
	got, err := h.conf.TicketsByRemote(context.Background(), h.source.ID, []string{"1001", "1002"})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (h *harness) automation(t *testing.T, a confstore.Automation) confstore.Automation {
	t.Helper()
	a.ProjectID = h.project.ID
	if a.Flow == "" {
		a.Flow = "builder"
	}
	created, err := h.conf.CreateAutomation(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	// The store stamps the wall clock; a schedule counts from when the rule
	// was made, so pin that to the watcher's clock — otherwise the test
	// depends on the time of day it runs at. created_at is only ever written
	// by an insert, hence the delete.
	created.CreatedAt, created.UpdatedAt = h.now, h.now
	if err := h.conf.DB().DeleteAutomation(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.conf.DB().UpsertAutomation(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	return created
}

func (h *harness) pass(t *testing.T) {
	t.Helper()
	if err := h.w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) get(t *testing.T, id string) confstore.Automation {
	t.Helper()
	a, err := h.conf.GetAutomation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A tracker query records what it finds on the workshop's source, marked
// with the automation, and starts one run of its flow per new record with
// its params, by "automation". The next pass finds nothing new to do.
func TestTrackerQueryRecordsAndStartsOnce(t *testing.T) {
	h := newHarness(t)
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery,
		Query: "do", Params: map[string]string{"plan_gate": "false"}, Enabled: true})
	h.pass(t)
	if n := h.eng.count(); n != 2 {
		t.Fatalf("runs created = %d, want 2 (last error %q)", n, h.get(t, a.ID).LastError)
	}
	call := h.eng.calls[0]
	if call.Flow != "builder" || call.In.By != By || call.In.Params["plan_gate"] != "false" ||
		call.Ticket.SourceKey != "DEV-1" || call.Ticket.Title != "First" || call.Ticket.TicketID == "" {
		t.Errorf("first run = %+v", call)
	}
	recs := h.records(t)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	for _, tk := range recs {
		if tk.AutomationID != a.ID || tk.SourceID != h.source.ID {
			t.Errorf("record = %+v, want the automation's provenance", tk)
		}
	}
	got := h.get(t, a.ID)
	if got.LastPolledAt == nil || got.LastFiredAt == nil || got.LastError != "" {
		t.Errorf("marks = polled %v fired %v err %q", got.LastPolledAt, got.LastFiredAt, got.LastError)
	}
	// Nothing new on the tracker: its records have runs, nothing fires.
	h.now = h.now.Add(2 * time.Minute)
	h.pass(t)
	if n := h.eng.count(); n != 2 {
		t.Errorf("second pass created runs: %d", n)
	}
	if recs := h.records(t); len(recs) != 2 {
		t.Errorf("second pass recorded again: %d records", len(recs))
	}
	if len(h.ch.sent) != 0 {
		t.Errorf("notifications = %+v", h.ch.sent)
	}
}

// A refused run is a blocker, told once per streak, and the record is kept;
// a later pass that can create runs fires those records instead of making
// them twice, and the error clears.
func TestRefusedRunIsNotifiedOnceAndRecovered(t *testing.T) {
	h := newHarness(t)
	h.eng.Fail = errors.New("preflight: stage \"test\": run_ref \"verify\" not found in project.commands")
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, Enabled: true})
	h.pass(t)
	if recs := h.records(t); len(recs) != 2 {
		t.Fatalf("records = %d, want them kept", len(recs))
	}
	if got := h.get(t, a.ID); !strings.Contains(got.LastError, "preflight") || got.LastFiredAt != nil {
		t.Errorf("marks after refusal = %+v", got)
	}
	if len(h.ch.sent) != 1 || h.ch.sent[0].Type != "blocker" || !strings.Contains(h.ch.sent[0].Body, "preflight") {
		t.Fatalf("notifications = %+v, want one blocker", h.ch.sent)
	}
	h.pass(t)
	if len(h.ch.sent) != 1 {
		t.Errorf("a second refusal in the streak notified again: %d", len(h.ch.sent))
	}
	h.eng.Fail = nil
	h.pass(t)
	if n := h.eng.count(); n != 2 {
		t.Fatalf("runs after recovery = %d, want 2", n)
	}
	if recs := h.records(t); len(recs) != 2 {
		t.Errorf("recovery recorded again: %d records", len(recs))
	}
	if got := h.get(t, a.ID); got.LastError != "" || got.LastFiredAt == nil {
		t.Errorf("marks after recovery = %+v", got)
	}
}

// A record someone else made — a person, or another automation — is theirs:
// the automation never starts a run for it, however often the query returns
// it, and does not take it over.
func TestSomeoneElsesRecordIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	other := h.automation(t, confstore.Automation{Name: "other", Kind: confstore.AutomationTrackerQuery, Query: "nothing matches this"})
	h.record(t, "DEV-1", "")
	h.record(t, "DEV-2", other.ID)
	h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, Enabled: true})
	h.pass(t)
	h.pass(t)
	if n := h.eng.count(); n != 0 {
		t.Errorf("runs = %d (%+v), want none", n, h.eng.calls)
	}
	recs := h.records(t)
	if recs["1001"].AutomationID != "" || recs["1002"].AutomationID != other.ID {
		t.Errorf("records = %+v, want their makers kept", recs)
	}
}

// A record of this automation's whose ticket has a run — whoever started it
// — is not fired again.
func TestARecordWithRunsIsNotFiredAgain(t *testing.T) {
	h := newHarness(t)
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, Enabled: true})
	rec := h.record(t, "DEV-1", a.ID)
	if _, err := h.eng.CreateRunWith(context.Background(), runinput.Ticket{TicketID: rec.ID}, "builder", h.project.ID, runinput.Input{}); err != nil {
		t.Fatal(err)
	}
	h.pass(t)
	if n := h.eng.count(); n != 2 || h.eng.calls[1].Ticket.SourceKey != "DEV-2" {
		t.Errorf("runs = %d (%+v), want only DEV-2's on top", n, h.eng.calls)
	}
}

// A match past the first page is found: issues already handled stay in the
// list ahead of it, so the poll reads on to the source's last page. The
// limit caps the runs a pass starts; what is left fires on the next pass.
func TestTrackerQueryReadsPastTheFirstPage(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.src.Tickets = append(h.src.Tickets,
		trackerport.SourceTicket{Key: "DEV-3", RemoteID: "1003", Title: "Third", Body: "do three"},
		trackerport.SourceTicket{Key: "DEV-4", RemoteID: "1004", Title: "Fourth", Body: "do four"})
	h.w.Limit = 2
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, Enabled: true})
	for _, key := range []string{"DEV-1", "DEV-2"} {
		rec := h.record(t, key, a.ID)
		if _, err := h.eng.CreateRunWith(ctx, runinput.Ticket{TicketID: rec.ID}, "builder", h.project.ID, runinput.Input{}); err != nil {
			t.Fatal(err)
		}
	}
	h.pass(t)
	if n := h.eng.count(); n != 4 || h.eng.calls[2].Ticket.SourceKey != "DEV-3" || h.eng.calls[3].Ticket.SourceKey != "DEV-4" {
		t.Fatalf("runs = %d (%+v), want DEV-3's and DEV-4's from the second page", n, h.eng.calls)
	}
	recs, err := h.conf.TicketsByRemote(ctx, h.source.ID, []string{"1003", "1004"})
	if err != nil || len(recs) != 2 || recs["1003"].AutomationID != a.ID {
		t.Errorf("records = %+v (%v), want both recorded as the automation's", recs, err)
	}
	if got := h.get(t, a.ID); got.LastError != "" || got.LastFiredAt == nil {
		t.Errorf("marks = %+v", got)
	}

	// One run a pass: the second new issue waits for the next.
	h.src.Tickets = append(h.src.Tickets,
		trackerport.SourceTicket{Key: "DEV-5", RemoteID: "1005", Title: "Fifth", Body: "do five"},
		trackerport.SourceTicket{Key: "DEV-6", RemoteID: "1006", Title: "Sixth", Body: "do six"})
	h.w.Limit = 1 // a new page size: no page the first pass read is reused
	h.pass(t)
	if n := h.eng.count(); n != 5 || h.eng.calls[4].Ticket.SourceKey != "DEV-5" {
		t.Fatalf("runs = %d (%+v), want DEV-5's alone", n, h.eng.calls)
	}
	h.pass(t)
	if n := h.eng.count(); n != 6 || h.eng.calls[5].Ticket.SourceKey != "DEV-6" {
		t.Errorf("runs = %d (%+v), want DEV-6's on the next pass", n, h.eng.calls)
	}
}

// A ticket whose run (in any workshop) is still going is not fired; the
// automation says so on its last_error and fires it once the run ends.
func TestTrackerQueryOnABusyTicket(t *testing.T) {
	h := newHarness(t)
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, Query: "do one", Enabled: true})
	h.eng.Fail = fmt.Errorf("preflight: %w", busyErr{"r-live"})
	h.pass(t)
	if got := h.get(t, a.ID); got.LastError != "DEV-1: ticket busy: run r-live" || got.LastFiredAt != nil {
		t.Fatalf("busy ticket: marks = fired %v err %q", got.LastFiredAt, got.LastError)
	}
	h.eng.Fail = nil
	h.pass(t)
	if n := h.eng.count(); n != 1 || h.eng.calls[0].Ticket.SourceKey != "DEV-1" {
		t.Errorf("after the run ended: runs = %d (%+v)", n, h.eng.calls)
	}
	if got := h.get(t, a.ID); got.LastError != "" {
		t.Errorf("last_error after firing = %q", got.LastError)
	}
}

// A schedule fires once per slot — not before it, not twice for it, with no
// catch-up after a gap, and across a restart of the watcher — and the firing
// is recorded before the run is created, so a refusal does not re-fire.
func TestCronFiresOncePerSlot(t *testing.T) {
	h := newHarness(t)
	tk := h.record(t, "DEV-1", "")
	a := h.automation(t, confstore.Automation{Name: "nightly", Kind: confstore.AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, Enabled: true})
	h.pass(t) // 08:00: before the slot
	if h.eng.count() != 0 {
		t.Fatal("fired before the slot")
	}
	h.now = h.now.Add(90 * time.Minute) // 09:30
	h.pass(t)
	if n := h.eng.count(); n != 1 || h.eng.calls[0].Ticket.TicketID != tk.ID || h.eng.calls[0].In.By != By {
		t.Fatalf("runs at 09:30 = %d (%+v)", n, h.eng.calls)
	}
	if got := h.get(t, a.ID); got.LastFiredAt == nil || !got.LastFiredAt.Equal(h.now) {
		t.Errorf("last_fired_at = %v, want 09:30", got.LastFiredAt)
	}
	h.now = h.now.Add(15 * time.Minute) // 09:45: same slot
	h.pass(t)
	if h.eng.count() != 1 {
		t.Error("fired twice for one slot")
	}
	// A restart: a fresh watcher over the same store, two days later at
	// 10:00 — one slot passed meanwhile; it fires once, not twice.
	h.now = h.now.AddDate(0, 0, 2).Truncate(24 * time.Hour).Add(10 * time.Hour)
	h.w = &Watcher{Runs: h.eng, MRs: h.eng, Tickets: h.w.Tickets, Conf: h.conf, Clock: func() time.Time { return h.now }}
	h.pass(t)
	if h.eng.count() != 2 {
		t.Errorf("after a restart over a gap: runs = %d, want 2 (one firing, no catch-up)", h.eng.count())
	}
	// Mark before create: a refusal consumes the slot.
	h.eng.Fail = errors.New("preflight: boom")
	h.now = h.now.AddDate(0, 0, 1).Truncate(24 * time.Hour).Add(9*time.Hour + time.Minute)
	h.pass(t)
	got := h.get(t, a.ID)
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(h.now) || !strings.Contains(got.LastError, "boom") {
		t.Errorf("refused firing: marks = %+v", got)
	}
	h.eng.Fail = nil
	h.now = h.now.Add(10 * time.Minute)
	h.pass(t)
	if h.eng.count() != 2 {
		t.Errorf("a refused slot was re-fired: runs = %d", h.eng.count())
	}
}

// busyErr is the engine's refusal of a run on a ticket that has one going
// (engine.ErrTicketBusy), as the watcher sees it: an error naming the run.
type busyErr struct{ run string }

func (b busyErr) Error() string       { return "ticket is busy: run " + b.run + " is still active" }
func (b busyErr) ActiveRunID() string { return b.run }

// A slot that falls while the ticket's previous run is still going is
// skipped and says so — "ticket busy: run <id>" — and is not made up once
// the run ends; the next slot fires as usual.
func TestCronSkipsASlotOnABusyTicket(t *testing.T) {
	h := newHarness(t)
	tk := h.record(t, "DEV-1", "")
	a := h.automation(t, confstore.Automation{Name: "nightly", Kind: confstore.AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, Enabled: true})
	h.eng.Fail = fmt.Errorf("preflight: %w", busyErr{"r-live"})
	h.now = h.now.Add(90 * time.Minute) // 09:30
	h.pass(t)
	got := h.get(t, a.ID)
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(h.now) || got.LastError != "ticket busy: run r-live" {
		t.Fatalf("busy slot: marks = fired %v err %q", got.LastFiredAt, got.LastError)
	}
	// The run ended: the skipped slot is not caught up.
	h.eng.Fail = nil
	h.now = h.now.Add(15 * time.Minute)
	h.pass(t)
	if h.eng.count() != 0 {
		t.Fatalf("a skipped slot was made up: runs = %d", h.eng.count())
	}
	h.now = h.now.AddDate(0, 0, 1)
	h.pass(t)
	if h.eng.count() != 1 {
		t.Fatalf("next slot: runs = %d, want 1", h.eng.count())
	}
	if got := h.get(t, a.ID); got.LastError != "" {
		t.Errorf("last_error after the next slot fired = %q", got.LastError)
	}
}

// A pinned ticket the workshop no longer shows — its ticket source changed
// since the pin, or the ticket is another owner's — is refused: the slot is
// spent, nothing runs, and last_error says why.
func TestCronRefusesATicketTheWorkshopNoLongerShows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tk := h.record(t, "DEV-1", "")
	a := h.automation(t, confstore.Automation{Name: "nightly", Kind: confstore.AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, Enabled: true})

	p, err := h.conf.GetProject(ctx, h.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := h.conf.InternalTracker(ctx, p.UserID)
	if err != nil {
		t.Fatal(err)
	}
	p.TicketSourceID = internal.ID
	if err := h.conf.DB().UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(90 * time.Minute) // 09:30
	h.pass(t)
	got := h.get(t, a.ID)
	if h.eng.count() != 0 || got.LastFiredAt == nil || !strings.Contains(got.LastError, "not the workshop's") {
		t.Fatalf("another source: runs %d, fired %v, err %q", h.eng.count(), got.LastFiredAt, got.LastError)
	}

	// Back on the ticket's source, but the ticket is someone else's.
	p.TicketSourceID = tk.SourceID
	if err := h.conf.DB().UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	theirs := tk
	theirs.UserID = "someone-else"
	if err := h.conf.DB().UpsertTicket(ctx, theirs); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.AddDate(0, 0, 1)
	h.pass(t)
	if got := h.get(t, a.ID); h.eng.count() != 0 || !strings.Contains(got.LastError, "not the workshop owner's") {
		t.Fatalf("another owner: runs %d, err %q", h.eng.count(), got.LastError)
	}

	// The workshop's own ticket again: the next slot fires.
	if err := h.conf.DB().UpsertTicket(ctx, tk); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.AddDate(0, 0, 1)
	h.pass(t)
	if got := h.get(t, a.ID); h.eng.count() != 1 || got.LastError != "" {
		t.Errorf("own ticket: runs %d, err %q", h.eng.count(), got.LastError)
	}
}

// Disabled automations are not walked.
func TestDisabledAndStale(t *testing.T) {
	h := newHarness(t)
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, Enabled: false})
	h.pass(t)
	if h.eng.count() != 0 {
		t.Fatal("a disabled automation fired")
	}
	if got := h.get(t, a.ID); got.LastPolledAt != nil {
		t.Errorf("a disabled automation was polled: %+v", got)
	}
}

const reviewerYAML = `name: rev
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
agents:
  - name: pr_reviewer
    prompt: review it
    inputs:
      - { name: changes, type: changeset }
    outputs:
      - { name: review, type: review_threads }
main:
  - { id: read, type: agent, harness: $harness, model: $model, agent: pr_reviewer, input: { changes: { from: run.git.diff_target, type: changeset } }, output: { review: { to: artifacts/review.yaml, type: review_threads } } }
  - { id: post, type: action, action: post_review, input: { review: { from: artifacts/review.yaml, type: review_threads } } }
outcomes:
  - { id: merged, on: mr_merged, then: done }
  - { id: closed, on: mr_closed, then: done }
`

// merge_request creates a run on each merge request someone else opened, once:
// drafts wait, the integration's own are never picked, and each gets a
// ticket of its own keyed by the merge request.
func TestMergeRequestAutomationStartsOnOthersMergeRequestsOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.conf.SaveFlow(ctx, h.project.ID, "rev", reviewerYAML, nil); err != nil {
		t.Fatal(err)
	}
	mr := func(iid int, author string, draft bool) repoport.MRInfo {
		return repoport.MRInfo{Ref: repoport.MRRef{IID: iid, URL: fmt.Sprintf("https://gitlab.com/g/p/-/merge_requests/%d", iid)},
			Title: fmt.Sprintf("change %d", iid), Author: author, State: repoport.MROpen, Draft: draft, Source: "feature", Target: "main"}
	}
	h.eng.MRs = []repoport.MRInfo{mr(5, "bob", true), mr(4, "alice", false), mr(3, "apothecary", false)}
	a := h.automation(t, confstore.Automation{Name: "review others", Kind: confstore.AutomationMergeRequest, Flow: "rev", Enabled: true})
	h.pass(t)
	if n := h.eng.count(); n != 1 {
		t.Fatalf("runs = %d, want one (alice's)", n)
	}
	c := h.eng.calls[0]
	if c.In.MR == nil || c.In.MR.IID != 4 || c.In.By != By || c.Flow != "rev" || !strings.Contains(c.Ticket.Title, "(!4 by alice)") ||
		c.Ticket.TicketID != "" || !strings.Contains(c.Ticket.Goal, mr(4, "", false).Ref.URL) {
		t.Errorf("call = %+v", c)
	}
	// A run on a merge request belongs to no ticket (TicketID "" above).
	h.pass(t)
	if n := h.eng.count(); n != 1 {
		t.Errorf("a second pass started %d more", n-1)
	}
	// Marked ready: now it is picked.
	h.eng.MRs[0].Draft = false
	h.pass(t)
	if n := h.eng.count(); n != 2 || h.eng.calls[1].In.MR.IID != 5 {
		t.Errorf("after the draft was marked ready: %d runs", n)
	}
	if got := h.get(t, a.ID); got.LastError != "" || got.LastFiredAt == nil {
		t.Errorf("automation = %+v", got)
	}
}

// Each kind starts the flows whose runs are started on what it finds: a
// merge request, or a ticket (operates_on).
func TestAutomationOperatesOnKinds(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.conf.SaveFlow(ctx, h.project.ID, "rev", reviewerYAML, nil); err != nil {
		t.Fatal(err)
	}
	_, err := h.conf.CreateAutomation(ctx, confstore.Automation{ProjectID: h.project.ID, Name: "x", Kind: confstore.AutomationMergeRequest, Flow: "builder"})
	if err == nil || !strings.Contains(err.Error(), "starts its runs on a ticket") {
		t.Errorf("merge_request on the builder flow: %v", err)
	}
	_, err = h.conf.CreateAutomation(ctx, confstore.Automation{ProjectID: h.project.ID, Name: "y", Kind: confstore.AutomationTrackerQuery, Flow: "rev"})
	if err == nil || !strings.Contains(err.Error(), "starts its runs on a merge request") {
		t.Errorf("tracker_query on a reviewer flow: %v", err)
	}
	_, err = h.conf.CreateAutomation(ctx, confstore.Automation{ProjectID: h.project.ID, Name: "z", Kind: confstore.AutomationMergeRequest, Flow: "rev", Repo: "ghost"})
	if err == nil || !strings.Contains(err.Error(), `no repository "ghost"`) {
		t.Errorf("unknown repo: %v", err)
	}
}
