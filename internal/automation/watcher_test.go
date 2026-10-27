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
	// MRs is what ListMRs reports (merge_request).
	MRs []ports.MRInfo
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

func (f *fakeEngine) ListMRs(context.Context, string, string) ([]ports.MRInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ports.MRInfo(nil), f.MRs...), nil
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
	repoHost := mk(confstore.PortRepository, "f")
	prov := mk(confstore.PortProvisioner, "p")
	hs := mk(confstore.PortHarness, "h")
	source := mk(confstore.PortTicketSource, "tracker")
	p, err := conf.CreateProject(ctx, confstore.Project{
		Name: "p1", RepoAccountID: repoHost.ID, ProvisionerID: prov.ID, HarnessIDs: []string{hs.ID},
		Repo:        confstore.RepoConfig{URL: "https://gitlab.com/g/p.git", Locator: "42"},
		DefaultFlow: "builder",
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
		a.Flow = "builder"
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
// with the automation, and starts one run of its flow per import with its
// params, by "automation". The next pass finds nothing new to do.
func TestTrackerQueryImportsAndStartsOnce(t *testing.T) {
	h := newHarness(t)
	a := h.automation(t, confstore.Automation{Name: "inbox", Kind: confstore.AutomationTrackerQuery,
		SourceID: h.source.ID, Query: "do", Params: map[string]string{"plan_gate": "false"}, Enabled: true})
	h.pass(t)
	if n := h.eng.count(); n != 2 {
		t.Fatalf("runs created = %d, want 2 (last error %q)", n, h.get(t, a.ID).LastError)
	}
	call := h.eng.calls[0]
	if call.Flow != "builder" || call.In.By != By || call.In.Params["plan_gate"] != "false" ||
		call.Ticket.SourceKey != "DEV-1" || call.Ticket.Title != "First" || call.Ticket.TicketID == "" {
		t.Errorf("first run = %+v", call)
	}
	all, _ := h.conf.ListTickets(context.Background(), "")
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
	if all, _ := h.conf.ListTickets(context.Background(), ""); len(all) != 2 {
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
	if all, _ := h.conf.ListTickets(context.Background(), ""); len(all) != 2 {
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
	if all, _ := h.conf.ListTickets(context.Background(), ""); len(all) != 2 {
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

// Disabled automations are not walked.
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

const reviewerYAML = `name: rev
version: 5
operates_on: merge_request
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
agents:
  - name: pr_reviewer
    prompt: review it
    inputs:
      - { name: changes, type: changeset }
    outputs:
      - { name: review, type: review_threads }
main:
  - { id: read, type: agent, agent: pr_reviewer, input: { changes: { from: run.git.diff_target, type: changeset } }, output: { review: { to: artifacts/review.yaml, type: review_threads } } }
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
	mr := func(iid int, author string, draft bool) ports.MRInfo {
		return ports.MRInfo{Ref: ports.MRRef{IID: iid, URL: fmt.Sprintf("https://gitlab.com/g/p/-/merge_requests/%d", iid)},
			Title: fmt.Sprintf("change %d", iid), Author: author, State: ports.MROpen, Draft: draft, Source: "feature", Target: "main"}
	}
	h.eng.MRs = []ports.MRInfo{mr(5, "bob", true), mr(4, "alice", false), mr(3, "apothecary", false)}
	a := h.automation(t, confstore.Automation{Name: "review others", Kind: confstore.AutomationMergeRequest, Flow: "rev", Enabled: true})
	h.pass(t)
	if n := h.eng.count(); n != 1 {
		t.Fatalf("runs = %d, want one (alice's)", n)
	}
	c := h.eng.calls[0]
	if c.In.MR == nil || c.In.MR.IID != 4 || c.In.By != By || c.Flow != "rev" || !strings.Contains(c.Ticket.Title, "(!4 by alice)") ||
		c.Ticket.SourceKind != confstore.TicketKindMR || c.Ticket.SourceKey != mr(4, "", false).Ref.URL {
		t.Errorf("call = %+v", c)
	}
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
	_, err = h.conf.CreateAutomation(ctx, confstore.Automation{ProjectID: h.project.ID, Name: "y", Kind: confstore.AutomationTrackerQuery, SourceID: h.source.ID, Flow: "rev"})
	if err == nil || !strings.Contains(err.Error(), "starts its runs on a merge request") {
		t.Errorf("tracker_query on a reviewer flow: %v", err)
	}
	_, err = h.conf.CreateAutomation(ctx, confstore.Automation{ProjectID: h.project.ID, Name: "z", Kind: confstore.AutomationMergeRequest, Flow: "rev", Repo: "ghost"})
	if err == nil || !strings.Contains(err.Error(), `no repository "ghost"`) {
		t.Errorf("unknown repo: %v", err)
	}
}
