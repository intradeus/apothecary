package tickets

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
)

// legacyWorld is a tracker integration whose config still carries Jira's
// legacy maps.
func legacyWorld(t *testing.T, src *fake.TicketSource, statusMap, typeMap string) *Factory {
	t.Helper()
	f, _ := newFactory(t, src)
	cfg := map[string]any{"base_url": "https://j.example"}
	if statusMap != "" {
		cfg[legacyStatusMap] = statusMap
	}
	if typeMap != "" {
		cfg[legacyTypeMap] = typeMap
	}
	if err := f.Conf.DB().UpsertIntegration(context.Background(), confstore.StoredIntegration{ID: "int-1",
		Port: confstore.PortTicketSource, Kind: "jira", Name: "tracker", Config: cfg}); err != nil {
		t.Fatal(err)
	}
	return f
}

func stepsByName(t *testing.T, conf *confstore.Service) map[string]trackerport.FlowStep {
	t.Helper()
	rows, err := conf.TrackerStatuses(context.Background(), "int-1")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]trackerport.FlowStep{}
	for _, r := range rows {
		out[r.Name] = r.FlowStep
	}
	return out
}

func changeTypesByName(t *testing.T, conf *confstore.Service) map[string]trackerport.ChangeType {
	t.Helper()
	rows, err := conf.TrackerTypes(context.Background(), "int-1")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]trackerport.ChangeType{}
	for _, r := range rows {
		out[r.Name] = r.ChangeType
	}
	return out
}

func hasWarning(warnings []string, sub string) bool {
	for _, w := range warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

// The first list refresh applies the maps by name (case aside) — a mapped
// status takes its step from the auto-suggested holder — warns about what
// names nothing or does not parse, and drops the keys; a second refresh
// changes nothing.
func TestSyncListsAppliesLegacyJiraMaps(t *testing.T) {
	src := &fake.TicketSource{
		RemoteStatuses: []trackerport.RemoteStatus{
			{ID: "1", Name: "Backlog", Category: trackerport.CategoryNew},
			{ID: "2", Name: "Selected", Category: trackerport.CategoryNew},
			{ID: "3", Name: "Doing", Category: trackerport.CategoryIndeterminate},
			{ID: "4", Name: "Code Review", Category: trackerport.CategoryIndeterminate},
			{ID: "5", Name: "On Hold", Category: trackerport.CategoryIndeterminate},
			{ID: "6", Name: "Closed", Category: trackerport.CategoryDone},
		},
		RemoteTypes: []trackerport.RemoteType{
			{ID: "10", Name: "Story"}, {ID: "11", Name: "Bug"}, {ID: "12", Name: "Epic"}, {ID: "13", Name: "Task"},
		},
	}
	f := legacyWorld(t, src,
		"todo=selected, in_progress=Doing\nin_review=code review, blocked=On Hold, done=Nope, bogus=X, garbage",
		"Story=feat, BUG=fix, Epic=chore, Spike=feat, Task=bogus")
	ctx := context.Background()

	warnings, err := SyncLists(ctx, f.Conf, "int-1", src)
	if err != nil {
		t.Fatal(err)
	}
	wantSteps := map[string]trackerport.FlowStep{
		"Backlog": "", "Selected": trackerport.StepTodo, "Doing": trackerport.StepInProgress,
		"Code Review": trackerport.StepInReview, "On Hold": trackerport.StepBlocked, "Closed": trackerport.StepDone,
	}
	if got := stepsByName(t, f.Conf); !reflect.DeepEqual(got, wantSteps) {
		t.Errorf("steps = %v, want %v", got, wantSteps)
	}
	types := changeTypesByName(t, f.Conf)
	if types["Story"] != trackerport.ChangeFeat || types["Bug"] != trackerport.ChangeFix || types["Epic"] != trackerport.ChangeChore {
		t.Errorf("change types = %v", types)
	}
	for _, sub := range []string{"done=Nope", `"bogus"`, `"garbage"`, "Spike=feat", "Task=bogus"} {
		if !hasWarning(warnings, sub) {
			t.Errorf("no warning about %s in %q", sub, warnings)
		}
	}
	if len(warnings) != 5 {
		t.Errorf("warnings = %q", warnings)
	}
	cfg, err := f.Conf.IntegrationConfig(ctx, "int-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg[legacyStatusMap]; ok {
		t.Error("status_map was not dropped")
	}
	if _, ok := cfg[legacyTypeMap]; ok {
		t.Error("type_map was not dropped")
	}
	if cfg["base_url"] != "https://j.example" {
		t.Errorf("config = %v", cfg)
	}

	// The user moves a step afterwards; the next refresh leaves it alone.
	rows, _ := f.Conf.TrackerStatuses(ctx, "int-1")
	for i := range rows {
		switch rows[i].Name {
		case "Selected":
			rows[i].FlowStep = ""
		case "Backlog":
			rows[i].FlowStep = trackerport.StepTodo
		}
	}
	if _, _, err := f.Conf.PutTrackerStatuses(ctx, "int-1", rows, nil); err != nil {
		t.Fatal(err)
	}
	before := stepsByName(t, f.Conf)
	beforeTypes := changeTypesByName(t, f.Conf)
	warnings, err = SyncLists(ctx, f.Conf, "int-1", src)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("second refresh = %q, %v", warnings, err)
	}
	if got := stepsByName(t, f.Conf); !reflect.DeepEqual(got, before) {
		t.Errorf("second refresh moved steps: %v, want %v", got, before)
	}
	if got := changeTypesByName(t, f.Conf); !reflect.DeepEqual(got, beforeTypes) {
		t.Errorf("second refresh changed types: %v, want %v", got, beforeTypes)
	}
}

// The map was laid over the stock workflow: its defaults apply too (and
// warn about nothing when the tracker lacks them), and "step=" with no name
// takes the step away.
func TestSyncListsLegacyStatusMapDefaults(t *testing.T) {
	src := &fake.TicketSource{} // the fake's stock To Do / In Progress / In Review / Done
	f := legacyWorld(t, src, "done=, blocked=Parked", "")
	warnings, err := SyncLists(context.Background(), f.Conf, "int-1", src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]trackerport.FlowStep{
		"To Do": trackerport.StepTodo, "In Progress": trackerport.StepInProgress,
		"In Review": trackerport.StepInReview, "Done": "",
	}
	if got := stepsByName(t, f.Conf); !reflect.DeepEqual(got, want) {
		t.Errorf("steps = %v, want %v", got, want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "blocked=Parked") {
		t.Errorf("warnings = %q", warnings)
	}
}

// A tracker without metadata, and an integration with no legacy keys, are
// left alone.
func TestSyncListsWithoutLegacyMaps(t *testing.T) {
	src := &fake.TicketSource{}
	f, _ := newFactory(t, src)
	warnings, err := SyncLists(context.Background(), f.Conf, "int-1", src)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("refresh = %q, %v", warnings, err)
	}
	if got := stepsByName(t, f.Conf); got["To Do"] != trackerport.StepTodo || got["Done"] != trackerport.StepDone || got["In Progress"] != "" {
		t.Errorf("auto-suggested steps = %v", got)
	}
}
