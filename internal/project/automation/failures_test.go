package automation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run"
)

// brokenRuns answers RunsOfTicket with an error.
type brokenRuns struct {
	*fakeEngine
	err error
}

func (b brokenRuns) RunsOfTicket(context.Context, string) ([]run.View, error) {
	return nil, b.err
}

// brokenMRs fails one half of the merge request port.
type brokenMRs struct {
	*fakeEngine
	listErr, userErr error
}

func (b brokenMRs) ListMRs(ctx context.Context, p, r string) ([]repoport.MRInfo, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.fakeEngine.ListMRs(ctx, p, r)
}

func (b brokenMRs) RepoHostUser(ctx context.Context, p, r string) (string, error) {
	if b.userErr != nil {
		return "", b.userErr
	}
	return b.fakeEngine.RepoHostUser(ctx, p, r)
}

// brokenDB is the config store with chosen calls failing.
type brokenDB struct {
	confstore.DB
	listAutomations, findTicket, upsertAutomation error
	// kindOf reports rows under a kind the table's check would refuse: one a
	// later migration added, seen by a binary that doesn't know it.
	kindOf map[string]string
}

func (d *brokenDB) ListAutomations(ctx context.Context, p string) ([]confstore.Automation, error) {
	if d.listAutomations != nil {
		return nil, d.listAutomations
	}
	rows, err := d.DB.ListAutomations(ctx, p)
	for i := range rows {
		if kind, ok := d.kindOf[rows[i].ID]; ok {
			rows[i].Kind = kind
		}
	}
	return rows, err
}

func (d *brokenDB) GetTicketBySource(ctx context.Context, u, src, key string) (confstore.Ticket, error) {
	if d.findTicket != nil {
		return confstore.Ticket{}, d.findTicket
	}
	return d.DB.GetTicketBySource(ctx, u, src, key)
}

func (d *brokenDB) GetTicketByProjectKey(ctx context.Context, p, key string) (confstore.Ticket, error) {
	if d.findTicket != nil {
		return confstore.Ticket{}, d.findTicket
	}
	return d.DB.GetTicketByProjectKey(ctx, p, key)
}

func (d *brokenDB) UpsertAutomation(ctx context.Context, a confstore.Automation) error {
	if d.upsertAutomation != nil {
		return d.upsertAutomation
	}
	return d.DB.UpsertAutomation(ctx, a)
}

// breakDB swaps the harness's store for one that can fail.
func (h *harness) breakDB(t *testing.T) *brokenDB {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	db := &brokenDB{DB: h.conf.DB()}
	conf := confstore.New(db, key)
	h.w.Conf = conf
	h.w.Tickets.Conf = conf
	return db
}

func mrOf(iid int, author string) repoport.MRInfo {
	return repoport.MRInfo{Ref: repoport.MRRef{IID: iid, URL: fmt.Sprintf("https://git.example/g/p/-/merge_requests/%d", iid)},
		Title: fmt.Sprintf("change %d", iid), Author: author, State: repoport.MROpen, Source: "feature", Target: "main"}
}

func (h *harness) mrAutomation(t *testing.T) confstore.Automation {
	t.Helper()
	if _, err := h.conf.SaveFlow(context.Background(), h.project.ID, "rev", reviewerYAML, nil); err != nil {
		t.Fatal(err)
	}
	return h.automation(t, confstore.Automation{Name: "review", Kind: confstore.AutomationMergeRequest, Flow: "rev", Enabled: true})
}

func TestRunOnceStopsOnACancelledContext(t *testing.T) {
	h := newHarness(t)
	h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.w.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if h.eng.count() != 0 {
		t.Error("a cancelled pass started a run")
	}
}

func TestRunOnceReportsAListingFailure(t *testing.T) {
	h := newHarness(t)
	db := h.breakDB(t)
	db.listAutomations = errors.New("db down")
	if err := h.w.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("err = %v", err)
	}
}

// Whatever the failure, the pass records it and walks on to the next rule.
func TestOneAutomationsFailureDoesNotStopThePass(t *testing.T) {
	h := newHarness(t)
	bad := h.automation(t, confstore.Automation{Name: "broken", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	h.breakDB(t).kindOf = map[string]string{bad.ID: "telepathy"}
	good := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	h.pass(t)
	if got := h.get(t, bad.ID); !strings.Contains(got.LastError, `unknown automation kind "telepathy"`) || got.LastPolledAt == nil {
		t.Errorf("broken rule = %+v", got)
	}
	if got := h.get(t, good.ID); got.LastError != "" || h.eng.count() != 2 {
		t.Errorf("good rule = %+v, runs %d", got, h.eng.count())
	}
	if len(h.ch.sent) != 1 || !strings.Contains(h.ch.sent[0].Title, `"broken"`) {
		t.Errorf("notifications = %+v", h.ch.sent)
	}
}

func TestWatcherDefaults(t *testing.T) {
	w := &Watcher{}
	if w.limit() != 50 {
		t.Errorf("limit = %d", w.limit())
	}
	if w.log() != slog.Default() {
		t.Error("no logger means the default one")
	}
	before := time.Now()
	if got := w.now(); got.Before(before) {
		t.Errorf("now = %v", got)
	}
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	w = &Watcher{Limit: 3, Log: l}
	if w.limit() != 3 || w.log() != l {
		t.Errorf("limit %d", w.limit())
	}
}

func TestTrackerSearchFailureIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.src.Err = errors.New("tracker down")
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	h.pass(t)
	got := h.get(t, a.ID)
	if !strings.Contains(got.LastError, "search: tracker down") || got.LastFiredAt != nil {
		t.Errorf("marks = %+v", got)
	}
}

// missingOne is a tracker that lists an issue it then cannot serve.
type missingOne struct {
	*fake.TicketSource
	key string
}

func (m missingOne) Fetch(ctx context.Context, key string) (trackerport.SourceTicket, error) {
	if key == m.key {
		return trackerport.SourceTicket{}, errors.New("issue vanished")
	}
	return m.TicketSource.Fetch(ctx, key)
}

// One issue that cannot be imported is named; the others still start, and the
// pass counts as having fired.
func TestAFailedImportDoesNotStopTheOthers(t *testing.T) {
	h := newHarness(t)
	h.w.Tickets.Sources = fakeSources2{missingOne{h.src, "DEV-1"}}
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	h.pass(t)
	if n := h.eng.count(); n != 1 || h.eng.calls[0].Ticket.SourceKey != "DEV-2" {
		t.Fatalf("runs = %+v", h.eng.calls)
	}
	got := h.get(t, a.ID)
	if !strings.Contains(got.LastError, "DEV-1: ") || !strings.Contains(got.LastError, "issue vanished") || got.LastFiredAt == nil {
		t.Errorf("marks = %+v", got)
	}
}

type fakeSources2 struct{ src trackerport.TicketSource }

func (s fakeSources2) TicketSource(context.Context, string) (trackerport.TicketSource, error) {
	return s.src, nil
}
func (s fakeSources2) TicketSourceKind(context.Context, string) (string, error) { return "fake", nil }

// A restart after importing and before starting: asked whether the ticket has
// runs, a store that cannot say must not start a second one.
func TestAdoptionNeedsToKnowTheRuns(t *testing.T) {
	h := newHarness(t)
	h.eng.Fail = errors.New("refused")
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	h.pass(t)
	h.eng.Fail = nil
	h.w.Runs = brokenRuns{h.eng, errors.New("runs unreadable")}
	h.pass(t)
	if h.eng.count() != 0 {
		t.Errorf("runs = %d", h.eng.count())
	}
	if got := h.get(t, a.ID); !strings.Contains(got.LastError, "runs unreadable") {
		t.Errorf("marks = %+v", got)
	}
}

func TestAdoptionNeedsToFindTheTicket(t *testing.T) {
	h := newHarness(t)
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	h.pass(t)
	if h.eng.count() != 2 {
		t.Fatalf("setup: runs = %d", h.eng.count())
	}
	db := h.breakDB(t)
	db.findTicket = errors.New("tickets unreadable")
	h.pass(t)
	if h.eng.count() != 2 {
		t.Errorf("runs = %d", h.eng.count())
	}
	if got := h.get(t, a.ID); !strings.Contains(got.LastError, "tickets unreadable") {
		t.Errorf("marks = %+v", got)
	}
}

func TestMergeRequestListingFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		port brokenMRs
		want string
	}{
		{"listing", brokenMRs{listErr: errors.New("host down")}, "list merge requests: host down"},
		{"account", brokenMRs{userErr: errors.New("token revoked")}, "repository account: token revoked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			a := h.mrAutomation(t)
			h.eng.MRs = []repoport.MRInfo{mrOf(1, "alice")}
			tc.port.fakeEngine = h.eng
			h.w.MRs = tc.port
			h.pass(t)
			if got := h.get(t, a.ID); !strings.Contains(got.LastError, tc.want) {
				t.Errorf("marks = %+v", got)
			}
			if h.eng.count() != 0 {
				t.Errorf("runs = %d", h.eng.count())
			}
		})
	}
}

// Not everything open is picked: a merged or closed one is past review, and
// the integration's own author matches case aside.
func TestMergeRequestAutomationSkips(t *testing.T) {
	h := newHarness(t)
	h.mrAutomation(t)
	merged, closed := mrOf(1, "alice"), mrOf(2, "alice")
	merged.State, closed.State = repoport.MRMerged, repoport.MRClosed
	h.eng.MRs = []repoport.MRInfo{merged, closed, mrOf(3, "APOTHECARY")}
	h.pass(t)
	if h.eng.count() != 0 {
		t.Errorf("runs = %+v", h.eng.calls)
	}
}

// A poll looks at Limit merge requests, however many are open.
func TestMergeRequestAutomationIsLimited(t *testing.T) {
	h := newHarness(t)
	h.w.Limit = 2
	h.mrAutomation(t)
	h.eng.MRs = []repoport.MRInfo{mrOf(1, "a"), mrOf(2, "b"), mrOf(3, "c")}
	h.pass(t)
	if h.eng.count() != 2 {
		t.Errorf("runs = %d", h.eng.count())
	}
}

// A refused run keeps its ticket; the next pass adopts it instead of making
// a second; a person's ticket for the same merge request stays theirs.
func TestMergeRequestTicketIsAdoptedOrLeftAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.mrAutomation(t)
	h.eng.MRs = []repoport.MRInfo{mrOf(1, "alice")}
	h.eng.Fail = errors.New("preflight: boom")
	h.pass(t)
	all, _ := h.conf.ListTickets(ctx, "")
	if len(all) != 1 || all[0].AutomationID != a.ID {
		t.Fatalf("tickets = %+v", all)
	}
	if got := h.get(t, a.ID); !strings.Contains(got.LastError, "preflight: boom") || !strings.Contains(got.LastError, mrOf(1, "").Ref.URL) || got.LastFiredAt != nil {
		t.Errorf("marks = %+v", got)
	}
	h.eng.Fail = nil
	h.pass(t)
	if all, _ := h.conf.ListTickets(ctx, ""); len(all) != 1 || h.eng.count() != 1 || h.eng.calls[0].Ticket.TicketID != all[0].ID {
		t.Fatalf("tickets %+v, runs %+v", all, h.eng.calls)
	}

	theirs := mrOf(2, "bob")
	if _, _, err := h.w.Tickets.ForMR(ctx, h.project.ID, theirs); err != nil {
		t.Fatal(err)
	}
	h.eng.MRs = []repoport.MRInfo{theirs}
	h.pass(t)
	if h.eng.count() != 1 {
		t.Errorf("a person's ticket got an automation's run: %+v", h.eng.calls)
	}

	h.eng.MRs = []repoport.MRInfo{mrOf(1, "alice")}
	h.w.Runs = brokenRuns{h.eng, errors.New("runs unreadable")}
	h.pass(t)
	if got := h.get(t, a.ID); !strings.Contains(got.LastError, "runs unreadable") {
		t.Errorf("marks = %+v", got)
	}
}

func TestMergeRequestTicketLookupFailure(t *testing.T) {
	h := newHarness(t)
	a := h.mrAutomation(t)
	h.eng.MRs = []repoport.MRInfo{mrOf(1, "alice")}
	db := h.breakDB(t)
	db.findTicket = errors.New("tickets unreadable")
	h.pass(t)
	if got := h.get(t, a.ID); !strings.Contains(got.LastError, "tickets unreadable") {
		t.Errorf("marks = %+v", got)
	}
}

func TestCronFailures(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	tk, err := h.conf.CreateTicket(ctx, confstore.Ticket{ProjectID: h.project.ID, Title: "Nightly"})
	if err != nil {
		t.Fatal(err)
	}
	a := h.automation(t, confstore.Automation{Name: "nightly", Kind: confstore.AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, Enabled: true})

	// The rule's schedule went bad behind validation's back.
	broken := a
	broken.Schedule = "not cron"
	if err := h.conf.DB().UpsertAutomation(ctx, broken); err != nil {
		t.Fatal(err)
	}
	h.pass(t)
	if got := h.get(t, a.ID); got.LastError == "" || got.LastFiredAt != nil {
		t.Errorf("bad schedule = %+v", got)
	}

	// The ticket is gone: the slot is spent, and said so.
	h = newHarness(t)
	tk, err = h.conf.CreateTicket(ctx, confstore.Ticket{ProjectID: h.project.ID, Title: "Nightly"})
	if err != nil {
		t.Fatal(err)
	}
	a = h.automation(t, confstore.Automation{Name: "nightly", Kind: confstore.AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, Enabled: true})
	if err := h.conf.DeleteTicket(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(2 * time.Hour)
	h.pass(t)
	if got := h.get(t, a.ID); !strings.Contains(got.LastError, "ticket "+tk.ID) || got.LastFiredAt == nil {
		t.Errorf("missing ticket = %+v", got)
	}
	if h.eng.count() != 0 {
		t.Errorf("runs = %d", h.eng.count())
	}
}

// The slot is spent before the run exists: if it cannot be recorded, there is
// no run, or a crash could fire it twice.
func TestCronFiresNothingWhenTheSlotCannotBeRecorded(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	tk, err := h.conf.CreateTicket(ctx, confstore.Ticket{ProjectID: h.project.ID, Title: "Nightly"})
	if err != nil {
		t.Fatal(err)
	}
	h.automation(t, confstore.Automation{Name: "nightly", Kind: confstore.AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, Enabled: true})
	var logged bytes.Buffer
	h.w.Log = slog.New(slog.NewTextHandler(&logged, nil))
	db := h.breakDB(t)
	db.upsertAutomation = errors.New("db read-only")
	h.now = h.now.Add(2 * time.Hour)
	h.pass(t)
	if h.eng.count() != 0 {
		t.Errorf("runs = %d", h.eng.count())
	}
	// The mark at the end of the pass failed too: logged, never fatal.
	if !strings.Contains(logged.String(), "automation mark failed") {
		t.Errorf("log = %q", logged.String())
	}
}

// A rule edited after its last firing counts from the edit.
func TestCronCountsFromTheLastEdit(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	tk, err := h.conf.CreateTicket(ctx, confstore.Ticket{ProjectID: h.project.ID, Title: "Nightly"})
	if err != nil {
		t.Fatal(err)
	}
	a := h.automation(t, confstore.Automation{Name: "nightly", Kind: confstore.AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, Enabled: true})
	a.UpdatedAt = h.now.Add(2 * time.Hour) // 10:00, past the 09:00 slot
	if err := h.conf.DB().UpsertAutomation(ctx, a); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(3 * time.Hour) // 11:00
	h.pass(t)
	if h.eng.count() != 0 {
		t.Error("the slot before the edit fired")
	}
}

// A cron run carries the ticket's own files, and a watcher with no ticket
// factory still fires.
func TestStartCarriesTheTicketsAttachments(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	att := filestore.NewAttachments(filestore.NewMem(), filestore.NewBlobs(t.TempDir()), 1<<20, []string{"text/*"})
	h.w.Tickets.Attachments = att
	up, err := att.Save(ctx, "", "spec.md", "text/markdown", strings.NewReader("# s"))
	if err != nil {
		t.Fatal(err)
	}
	tk, err := h.w.Tickets.Create(ctx, confstore.Ticket{ProjectID: h.project.ID, Title: "Nightly", Goal: "g"}, []string{up.ID})
	if err != nil {
		t.Fatal(err)
	}
	h.automation(t, confstore.Automation{Name: "nightly", Kind: confstore.AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, Enabled: true})
	h.now = h.now.Add(2 * time.Hour)
	h.pass(t)
	if h.eng.count() != 1 {
		t.Fatalf("runs = %d", h.eng.count())
	}
	if got := h.eng.calls[0].Ticket.TicketAttachmentIDs; len(got) != 1 || got[0] != up.ID {
		t.Errorf("attachments = %v", got)
	}

	h.w.Tickets = nil
	h.now = h.now.Add(24 * time.Hour)
	h.pass(t)
	if h.eng.count() != 2 {
		t.Errorf("runs without a factory = %d", h.eng.count())
	}
}

type failingChannel struct{}

func (failingChannel) Send(context.Context, notifyport.Notification) error {
	return errors.New("channel down")
}

// A blocker goes to the project's owner; a channel failing is logged, and with
// no channel there is simply nobody to tell.
func TestBlockerReachesTheOwner(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	p, err := h.conf.GetProject(ctx, h.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The owner never changes on an update: this is a new row.
	if err := h.conf.DB().DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	// The tracker an automation polls is its project owner's: a new row too.
	tracker, err := h.conf.DB().GetIntegration(ctx, h.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.conf.DB().DeleteIntegration(ctx, tracker.ID); err != nil {
		t.Fatal(err)
	}
	tracker.UserID = "owner-1"
	if err := h.conf.DB().UpsertIntegration(ctx, tracker); err != nil {
		t.Fatal(err)
	}
	p.UserID = "owner-1"
	if err := h.conf.DB().UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	h.src.Err = errors.New("tracker down")
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	h.pass(t)
	if len(h.ch.sent) != 1 || h.ch.sent[0].UserID != "owner-1" || !strings.Contains(h.ch.sent[0].Body, "tracker down") {
		t.Fatalf("notifications = %+v", h.ch.sent)
	}

	if err := h.conf.MarkAutomation(ctx, a.ID, confstore.AutomationMark{}); err != nil { // clears the streak
		t.Fatal(err)
	}
	var logged bytes.Buffer
	h.w.Log = slog.New(slog.NewTextHandler(&logged, nil))
	h.w.Notify = failingChannel{}
	h.pass(t)
	if !strings.Contains(logged.String(), "automation blocker notification failed") {
		t.Errorf("log = %q", logged.String())
	}
	if got := h.get(t, a.ID); !strings.Contains(got.LastError, "tracker down") {
		t.Errorf("the notification failing must not lose the mark: %+v", got)
	}

	if err := h.conf.MarkAutomation(ctx, a.ID, confstore.AutomationMark{}); err != nil {
		t.Fatal(err)
	}
	h.w.Notify = nil
	h.pass(t)
	if got := h.get(t, a.ID); !strings.Contains(got.LastError, "tracker down") {
		t.Errorf("marks = %+v", got)
	}
}

// A rule that recovers says so in the log and clears its error.
func TestRecoveryIsLogged(t *testing.T) {
	h := newHarness(t)
	h.src.Err = errors.New("tracker down")
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	h.pass(t)
	h.src.Err = nil
	var logged bytes.Buffer
	h.w.Log = slog.New(slog.NewTextHandler(&logged, nil))
	h.pass(t)
	if !strings.Contains(logged.String(), "automation recovered") {
		t.Errorf("log = %q", logged.String())
	}
	if got := h.get(t, a.ID); got.LastError != "" {
		t.Errorf("marks = %+v", got)
	}
}
