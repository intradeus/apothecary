package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"apothecary/internal/project/confstore"
)

// The automation endpoints: create (validated by the config store), list,
// get, update (marks kept), delete — scoped to the project in the path.
func TestAutomationAPI(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)
	base := "/api/projects/" + projectID + "/automations"

	tk := struct{ ID string }{x.newTicketOn(t, projectID, "Nightly", "")}

	resp, data := x.do(t, "GET", base, nil, nil)
	if resp.StatusCode != 200 || strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("empty list = %d: %s", resp.StatusCode, data)
	}
	// Refusals name what is wrong.
	for _, bad := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"name": "x", "kind": "tracker_query", "schedule": "0 3 * * *", "flow": "builder"}, "takes no schedule"},
		{map[string]any{"name": "x", "kind": "tracker_query", "flow": "reviewer"}, "starts its runs on a merge request"},
		{map[string]any{"name": "x", "kind": "cron", "schedule": "soon", "ticket_id": tk.ID, "flow": "builder"}, "five fields"},
		{map[string]any{"name": "x", "kind": "tracker_query", "flow": "builder", "params": map[string]string{"plan_gate": "maybe"}}, "plan_gate"},
	} {
		resp, data = x.do(t, "POST", base, bad.body, nil)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), bad.want) {
			t.Errorf("create %v = %d: %s", bad.body, resp.StatusCode, data)
		}
	}

	var a struct {
		ID      string            `json:"id"`
		Kind    string            `json:"kind"`
		Params  map[string]string `json:"params"`
		Enabled bool              `json:"enabled"`
	}
	resp, data = x.do(t, "POST", base, map[string]any{"name": "inbox", "kind": "tracker_query",
		"query": "assignee = factory", "flow": "builder", "params": map[string]string{"plan_gate": "false"}}, nil)
	if err := json.Unmarshal(data, &a); err != nil || resp.StatusCode != http.StatusCreated || a.ID == "" || !a.Enabled || a.Params["plan_gate"] != "false" {
		t.Fatalf("create = %d %v: %s", resp.StatusCode, err, data)
	}
	resp, data = x.do(t, "POST", base, map[string]any{"name": "nightly", "kind": "cron", "schedule": "0 3 * * *", "ticket_id": tk.ID, "flow": "builder", "enabled": false}, nil)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(string(data), `"enabled":false`) {
		t.Fatalf("create cron = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", base, nil, nil)
	if resp.StatusCode != 200 || strings.Count(string(data), `"kind"`) != 2 {
		t.Fatalf("list = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", base+"/"+a.ID, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"name":"inbox"`) {
		t.Fatalf("get = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "PUT", base+"/"+a.ID, map[string]any{"name": "inbox", "kind": "tracker_query", "flow": "builder", "enabled": false}, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"enabled":false`) || strings.Contains(string(data), "plan_gate") {
		t.Fatalf("update = %d: %s", resp.StatusCode, data)
	}
	// Another project's path does not see it.
	if resp, _ = x.do(t, "GET", "/api/projects/nope/automations/"+a.ID, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-project get = %d", resp.StatusCode)
	}
	if resp, _ = x.do(t, "DELETE", base+"/"+a.ID, nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d", resp.StatusCode)
	}
	if resp, _ = x.do(t, "GET", base+"/"+a.ID, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("get after delete = %d", resp.StatusCode)
	}
}

// A cron automation names its ticket by a key of the workshop's live list:
// a remote issue with no record gets one, whose id the automation keeps; an
// internal ticket's key is its id; a key the source does not have is 400.
func TestCronAutomationByTicketKey(t *testing.T) {
	x := newTrackerEnv(t)
	ctx := context.Background()
	base := "/api/projects/" + testProject + "/automations"
	if _, found, _ := x.srv.Conf.TicketByRemote(ctx, "int-1", "101"); found {
		t.Fatal("a record before the automation")
	}
	cron := func(key string) map[string]any {
		return map[string]any{"name": "nightly", "kind": "cron", "schedule": "0 3 * * *", "ticket_key": key, "flow": "builder"}
	}
	res, data := x.do(t, "POST", base, cron("PROJ-8"), nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", res.StatusCode, data)
	}
	rec, found, err := x.srv.Conf.TicketByRemote(ctx, "int-1", "101")
	if err != nil || !found || rec.SourceKey != "PROJ-8" {
		t.Fatalf("record = %+v %v %v", rec, found, err)
	}
	a := decode[confstore.Automation](t, data)
	if a.TicketID != rec.ID {
		t.Errorf("ticket_id = %q, want the record %q", a.TicketID, rec.ID)
	}
	// Again on the same issue: the record is reused.
	res, data = x.do(t, "PUT", base+"/"+a.ID, cron("PROJ-8"), nil)
	if got := decode[confstore.Automation](t, data); res.StatusCode != http.StatusOK || got.TicketID != rec.ID {
		t.Errorf("update = %d %s", res.StatusCode, data)
	}

	for _, bad := range []struct {
		body map[string]any
		want string
	}{
		{cron("NOPE-1"), "ticket_key"},
		{map[string]any{"name": "x", "kind": "tracker_query", "ticket_key": "PROJ-8", "flow": "builder"}, "only a cron"},
		{map[string]any{"name": "x", "kind": "cron", "schedule": "0 3 * * *", "ticket_key": "PROJ-8", "ticket_id": rec.ID, "flow": "builder"}, "not both"},
	} {
		if res, data := x.do(t, "POST", base, bad.body, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), bad.want) {
			t.Errorf("create %v = %d %s", bad.body, res.StatusCode, data)
		}
	}

	x.useSource(t, x.internal)
	res, data = x.do(t, "POST", "/api/projects/"+testProject+"/tickets", map[string]any{"title": "Internal"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("ticket = %d %s", res.StatusCode, data)
	}
	id := decode[TicketDetail](t, data).TicketID
	res, data = x.do(t, "POST", base, cron(id), nil)
	if got := decode[confstore.Automation](t, data); res.StatusCode != http.StatusCreated || got.TicketID != id {
		t.Errorf("internal = %d %s", res.StatusCode, data)
	}
}
