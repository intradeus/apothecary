package confstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func seedTracker(t *testing.T, svc *Service) Integration {
	t.Helper()
	in, err := svc.CreateIntegration(context.Background(), Integration{Port: PortTicketSource, Kind: "fake", Name: "tracker"})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// An automation is validated the way a firing would be — the kind's own
// needs, a flow whose operates_on fits it, params it accepts — so the watcher
// is never the first to find a mistake.
func TestAutomationValidates(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)
	other := seedNamedProject(t, svc, "p2")
	tracker := seedTracker(t, svc)
	tk, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	repoHost, _ := svc.GetProject(ctx, p.ID)
	for _, tc := range []struct {
		name string
		a    Automation
		want string
	}{
		{"no name", Automation{Kind: AutomationTrackerQuery, SourceID: tracker.ID, Flow: "builder"}, "needs a name"},
		{"bad kind", Automation{Name: "x", Kind: "webhook", Flow: "builder"}, `kind "webhook"`},
		{"no source", Automation{Name: "x", Kind: AutomationTrackerQuery, Flow: "builder"}, "needs source_id"},
		{"wrong port", Automation{Name: "x", Kind: AutomationTrackerQuery, SourceID: repoHost.RepoAccountID, Flow: "builder"}, "not a ticket source"},
		{"query with schedule", Automation{Name: "x", Kind: AutomationTrackerQuery, SourceID: tracker.ID, Schedule: "0 9 * * *", Flow: "builder"}, "takes no schedule"},
		{"bad schedule", Automation{Name: "x", Kind: AutomationCron, Schedule: "soon", TicketID: tk.ID, Flow: "builder"}, "five fields"},
		{"no ticket", Automation{Name: "x", Kind: AutomationCron, Schedule: "0 9 * * *", Flow: "builder"}, "needs ticket_id"},
		{"ticket elsewhere", Automation{Name: "x", Kind: AutomationCron, Schedule: "0 9 * * *", TicketID: tk.ID, ProjectID: other.ID, Flow: "builder"}, "another project"},
		{"no flow", Automation{Name: "x", Kind: AutomationTrackerQuery, SourceID: tracker.ID}, "needs a flow"},
		{"no such flow", Automation{Name: "x", Kind: AutomationTrackerQuery, SourceID: tracker.ID, Flow: "nope"}, "nope"},
		{"started on merge requests", Automation{Name: "x", Kind: AutomationTrackerQuery, SourceID: tracker.ID, Flow: "reviewer"}, "starts its runs on a merge request"},
		{"started on tickets", Automation{Name: "x", Kind: AutomationMergeRequest, Flow: "builder"}, "starts its runs on a ticket"},
		{"bad param", Automation{Name: "x", Kind: AutomationTrackerQuery, SourceID: tracker.ID, Flow: "builder", Params: map[string]string{"plan_gate": "maybe"}}, "plan_gate"},
		{"unknown param", Automation{Name: "x", Kind: AutomationTrackerQuery, SourceID: tracker.ID, Flow: "builder", Params: map[string]string{"nope": "1"}}, "nope"},
	} {
		a := tc.a
		if a.ProjectID == "" {
			a.ProjectID = p.ID
		}
		_, err := svc.CreateAutomation(ctx, a)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
		if tc.name != "ticket elsewhere" && tc.name != "no such flow" && tc.name != "wrong port" && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
		}
	}
	if _, err := svc.CreateAutomation(ctx, Automation{ProjectID: "nope", Name: "x", Kind: AutomationTrackerQuery, SourceID: tracker.ID, Flow: "builder"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: err = %v", err)
	}
}

// Create, list (per project and enabled overall), update keeping the marks,
// mark, delete — and the project takes its automations with it.
func TestAutomationLifecycle(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)
	tracker := seedTracker(t, svc)
	tk, err := svc.CreateTicket(ctx, Ticket{ProjectID: p.ID, Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	q, err := svc.CreateAutomation(ctx, Automation{ProjectID: p.ID, Name: " inbox ", Kind: AutomationTrackerQuery,
		SourceID: tracker.ID, Query: "assignee = me", Flow: "builder",
		Params: map[string]string{"plan_gate": "false", "verify": " "}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if q.ID == "" || q.Name != "inbox" || len(q.Params) != 1 || q.Params["plan_gate"] != "false" || q.CreatedAt.IsZero() || q.LastPolledAt != nil {
		t.Fatalf("created = %+v", q)
	}
	c, err := svc.CreateAutomation(ctx, Automation{ProjectID: p.ID, Name: "nightly", Kind: AutomationCron,
		Schedule: "0 3 * * *", TicketID: tk.ID, Flow: "builder"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Params != nil || c.Enabled {
		t.Errorf("cron created = %+v, want no params and disabled as given", c)
	}
	list, err := svc.ListAutomations(ctx, p.ID)
	if err != nil || len(list) != 2 || list[0].ID != q.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	enabled, err := svc.EnabledAutomations(ctx)
	if err != nil || len(enabled) != 1 || enabled[0].ID != q.ID {
		t.Fatalf("enabled = %+v, %v", enabled, err)
	}

	fired := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	if err := svc.MarkAutomation(ctx, q.ID, AutomationMark{PolledAt: fired, FiredAt: fired, Err: "boom"}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetAutomation(ctx, q.ID)
	if got.LastFiredAt == nil || !got.LastFiredAt.Equal(fired) || got.LastError != "boom" || !got.UpdatedAt.Equal(q.UpdatedAt) {
		t.Errorf("marked = %+v (a mark is not an edit)", got)
	}
	if err := svc.MarkAutomation(ctx, q.ID, AutomationMark{PolledAt: fired.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got, _ = svc.GetAutomation(ctx, q.ID); got.LastError != "" || !got.LastFiredAt.Equal(fired) {
		t.Errorf("second mark = %+v, want the error cleared and the firing kept", got)
	}

	// An update replaces the rule and keeps the marks; the project and the
	// creation time are not the caller's to change.
	upd, err := svc.UpdateAutomation(ctx, q.ID, Automation{ProjectID: "other", Name: "inbox-2", Kind: AutomationTrackerQuery,
		SourceID: tracker.ID, Flow: "builder", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if upd.ProjectID != p.ID || upd.Name != "inbox-2" || upd.Enabled || upd.LastFiredAt == nil || !upd.CreatedAt.Equal(q.CreatedAt) || !upd.UpdatedAt.After(q.UpdatedAt) {
		t.Errorf("updated = %+v", upd)
	}
	if _, err := svc.UpdateAutomation(ctx, q.ID, Automation{Name: "x", Kind: AutomationCron, Schedule: "nope", TicketID: tk.ID, Flow: "builder"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad update: err = %v", err)
	}
	if err := svc.DeleteAutomation(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteAutomation(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v", err)
	}
	if err := svc.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := svc.ListAutomations(ctx, p.ID); err != nil || len(list) != 0 {
		t.Errorf("automations outlived their project: %+v, %v", list, err)
	}
}
