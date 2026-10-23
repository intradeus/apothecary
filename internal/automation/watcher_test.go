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
	"apothecary/internal/builtin"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/ports"
	"apothecary/internal/run"
	"apothecary/internal/secrets"
	"apothecary/internal/tickets"
)

// fakeEngine records what the watcher asked for and answers RunsOfTicket
// from it; Fail refuses every creation (a preflight refusal, say).
type fakeEngine struct {
	mu    sync.Mutex
	Fail  error
	calls []struct {
		Ticket engine.Ticket
		Flow   string
		In     engine.RunInput
	}
	runs map[string][]run.View
	next int
}

func (f *fakeEngine) CreateRunWith(_ context.Context, t engine.Ticket, flowName, _ string, in engine.RunInput) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return "", f.Fail
	}
	f.next++
	id := fmt.Sprintf("r%d", f.next)
	if f.runs == nil {
		f.runs = map[string][]run.View{}
	}
	f.runs[t.TicketID] = append(f.runs[t.TicketID], run.View{RunID: id, TicketID: t.TicketID})
	f.calls = append(f.calls, struct {
		Ticket engine.Ticket
		Flow   string
		In     engine.RunInput
	}{t, flowName, in})
	return id, nil
}

func (f *fakeEngine) RunsOfTicket(_ context.Context, ticketID string) ([]run.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[ticketID], nil
}

func (f *fakeEngine) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeSources struct{ src *fake.TicketSource }

func (s fakeSources) TicketSource(context.Context, string) (ports.TicketSource, error) {
	return s.src, nil
}
func (s fakeSources) TicketSourceKind(context.Context, string) (string, error) { return "fake", nil }

type fakeChannel struct {
	mu   sync.Mutex
	sent []ports.Notification
}

func (c *fakeChannel) Send(_ context.Context, n ports.Notification) error {
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

func newHarness(t *testing.T) *harness {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	conf := confstore.New(confstore.NewMem(), key)
	ctx := context.Background()
	mk := func(port, name string) confstore.Integration {
		in, err := conf.CreateIntegration(ctx, confstore.Integration{Port: port, Kind: "fake", Name: name, Secrets: map[string]string{"token": "t"}})
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	forge := mk(confstore.PortForge, "f")
	prov := mk(confstore.PortProvisioner, "p")
	hs := mk(confstore.PortHarness, "h")
	source := mk(confstore.PortTicketSource, "tracker")
	p, err := conf.CreateProject(ctx, confstore.Project{
		Name: "p1", ForgeID: forge.ID, ProvisionerID: prov.ID, HarnessIDs: []string{hs.ID},
		Repo:        confstore.RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42"},
		DefaultFlow: builtin.DefaultFlowName,
	})
	if err != nil {
		t.Fatal(err)
	}
	src := &fake.TicketSource{Tickets: []ports.SourceTicket{
		{Key: "DEV-1", Title: "First", Body: "do one", URL: "https://tracker.local/DEV-1"},
		{Key: "DEV-2", Title: "Second", Body: "do two", URL: "https://tracker.local/DEV-2"},
	}}
	h := &harness{conf: conf, eng: &fakeEngine{}, src: src, ch: &fakeChannel{}, project: p, source: source,
		now: time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)}
	h.w = &Watcher{
		Engine:  h.eng,
		Tickets: &tickets.Factory{Conf: conf, Sources: fakeSources{src}},
		Conf:    conf,
		Notify:  h.ch,
		Clock:   func() time.Time { return h.now },
	}
	return h
}

func (h *harness) automation(t *testing.T, a confstore.Automation) confstore.Automation {
	t.Helper()
	a.ProjectID = h.project.ID
	if a.Flow == "" {
		a.Flow = builtin.DefaultFlowName
	}
	created, err := h.conf.CreateAutomation(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	// The store stamps the wall clock; a schedule counts from when the rule
	// was made, so pin that to the watcher's clock — otherwise the test
	// depends on the time of day it runs at.
	created.CreatedAt, created.UpdatedAt = h.now, h.now
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

// A tracker query imports what it finds as tickets of the project, marked
// with the automation, and starts one run per import at the automation's
// entrypoint with its params, by "automation". The next pass finds nothing
// new to do.
func TestTrackerQueryImportsAndStartsOnce(t *testing.T) {
	h := newHarness(t)
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery,
		SourceID: h.source.ID, Query: "do", Phase: "deliver", Params: map[string]string{"plan_gate": "false"}, Enabled: true})
	h.pass(t)
	if n := h.eng.count(); n != 2 {
		t.Fatalf("runs created = %d, want 2 (last error %q)", n, h.get(t, a.ID).LastError)
	}
	call := h.eng.calls[0]
	if call.Flow != builtin.DefaultFlowName || call.In.By != By || call.In.Phase != "deliver" || call.In.Params["plan_gate"] != "false" ||
		call.Ticket.SourceKey != "DEV-1" || call.Ticket.Title != "First" || call.Ticket.TicketID == "" {
		t.Errorf("first run = %+v", call)
	}
	all, _ := h.conf.ListTickets(context.Background())
	if len(all) != 2 {
		t.Fatalf("tickets = %d, want 2", len(all))
	}
	for _, tk := range all {
		if tk.AutomationID != a.ID || tk.ProjectID != h.project.ID || tk.SourceID != h.source.ID {
			t.Errorf("ticket = %+v, want the automation's provenance", tk)
		}
	}
	got := h.get(t, a.ID)
	if got.LastPolledAt == nil || got.LastFiredAt == nil || got.LastError != "" {
		t.Errorf("marks = polled %v fired %v err %q", got.LastPolledAt, got.LastFiredAt, got.LastError)
	}
	// Nothing new on the tracker: nothing new here.
	h.now = h.now.Add(2 * time.Minute)
	h.pass(t)
	if n := h.eng.count(); n != 2 {
		t.Errorf("second pass created runs: %d", n)
	}
	if all, _ := h.conf.ListTickets(context.Background()); len(all) != 2 {
		t.Errorf("second pass imported again: %d tickets", len(all))
	}
	if len(h.ch.sent) != 0 {
		t.Errorf("notifications = %+v", h.ch.sent)
	}
}

// A refused run is a blocker, told once per streak, and the imported ticket
// is kept; a later pass that can create runs adopts those tickets instead
// of importing them twice, and the error clears.
func TestRefusedRunIsNotifiedOnceAndRecovered(t *testing.T) {
	h := newHarness(t)
	h.eng.Fail = errors.New("preflight: stage \"test\": run_ref \"verify\" not found in project.commands")
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	h.pass(t)
	if all, _ := h.conf.ListTickets(context.Background()); len(all) != 2 {
		t.Fatalf("tickets = %d, want the imports kept", len(all))
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
	if all, _ := h.conf.ListTickets(context.Background()); len(all) != 2 {
		t.Errorf("recovery imported again: %d tickets", len(all))
	}
	if got := h.get(t, a.ID); got.LastError != "" || got.LastFiredAt == nil {
		t.Errorf("marks after recovery = %+v", got)
	}
}

// An issue a person imported is theirs: the automation never starts a run
// for it, however often the query returns it.
func TestAPersonsImportIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, _, err := h.w.Tickets.CreateFromSource(ctx, h.source.ID, "DEV-1", confstore.Ticket{ProjectID: h.project.ID}, nil); err != nil {
		t.Fatal(err)
	}
	h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: true})
	h.pass(t)
	if n := h.eng.count(); n != 1 || h.eng.calls[0].Ticket.SourceKey != "DEV-2" {
		t.Errorf("runs = %d (%+v), want only DEV-2's", n, h.eng.calls)
	}
}

// A schedule fires once per slot — not before it, not twice for it, with no
// catch-up after a gap, and across a restart of the watcher — and the firing
// is recorded before the run is created, so a refusal does not re-fire.
func TestCronFiresOncePerSlot(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tk, err := h.conf.CreateTicket(ctx, confstore.Ticket{ProjectID: h.project.ID, Title: "Nightly"})
	if err != nil {
		t.Fatal(err)
	}
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
	h.w = &Watcher{Engine: h.eng, Tickets: h.w.Tickets, Conf: h.conf, Clock: func() time.Time { return h.now }}
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

// Disabled automations are not walked; a stale phase is refused by the
// engine at firing time and recorded, never silently repointed.
func TestDisabledAndStale(t *testing.T) {
	h := newHarness(t)
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Enabled: false})
	h.pass(t)
	if h.eng.count() != 0 {
		t.Fatal("a disabled automation fired")
	}
	if got := h.get(t, a.ID); got.LastPolledAt != nil {
		t.Errorf("a disabled automation was polled: %+v", got)
	}
}
