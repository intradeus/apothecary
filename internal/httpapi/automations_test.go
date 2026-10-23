package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The automation endpoints: create (validated by the config store), list,
// get, update (marks kept), delete — scoped to the project in the path.
func TestAutomationAPI(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)
	base := "/api/projects/" + projectID + "/automations"

	resp, data := x.do(t, "POST", "/api/integrations", map[string]any{"port": "ticketsource", "kind": "fake", "name": "tracker"}, nil)
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &in); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("tracker integration = %d %v: %s", resp.StatusCode, err, data)
	}
	resp, data = x.do(t, "POST", "/api/tickets", map[string]any{"project_id": projectID, "title": "Nightly"}, nil)
	var tk struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &tk); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("ticket = %d %v: %s", resp.StatusCode, err, data)
	}

	resp, data = x.do(t, "GET", base, nil, nil)
	if resp.StatusCode != 200 || strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("empty list = %d: %s", resp.StatusCode, data)
	}
	// Refusals name what is wrong.
	for _, bad := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"name": "x", "kind": "tracker_query", "flow": "default"}, "source_id"},
		{map[string]any{"name": "x", "kind": "tracker_query", "source_id": in.ID, "flow": "default", "phase": "conflicts"}, "not an entrypoint"},
		{map[string]any{"name": "x", "kind": "cron", "schedule": "soon", "ticket_id": tk.ID, "flow": "default"}, "five fields"},
		{map[string]any{"name": "x", "kind": "tracker_query", "source_id": in.ID, "flow": "default", "params": map[string]string{"plan_gate": "maybe"}}, "plan_gate"},
	} {
		resp, data = x.do(t, "POST", base, bad.body, nil)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), bad.want) {
			t.Errorf("create %v = %d: %s", bad.body, resp.StatusCode, data)
		}
	}

	var a struct {
		ID      string            `json:"id"`
		Kind    string            `json:"kind"`
		Phase   string            `json:"phase"`
		Params  map[string]string `json:"params"`
		Enabled bool              `json:"enabled"`
	}
	resp, data = x.do(t, "POST", base, map[string]any{"name": "inbox", "kind": "tracker_query", "source_id": in.ID,
		"query": "assignee = factory", "flow": "default", "phase": "deliver", "params": map[string]string{"plan_gate": "false"}}, nil)
	if err := json.Unmarshal(data, &a); err != nil || resp.StatusCode != http.StatusCreated || a.ID == "" || !a.Enabled || a.Params["plan_gate"] != "false" || a.Phase != "deliver" {
		t.Fatalf("create = %d %v: %s", resp.StatusCode, err, data)
	}
	resp, data = x.do(t, "POST", base, map[string]any{"name": "nightly", "kind": "cron", "schedule": "0 3 * * *", "ticket_id": tk.ID, "flow": "default", "enabled": false}, nil)
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
	resp, data = x.do(t, "PUT", base+"/"+a.ID, map[string]any{"name": "inbox", "kind": "tracker_query", "source_id": in.ID, "flow": "default", "enabled": false}, nil)
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
