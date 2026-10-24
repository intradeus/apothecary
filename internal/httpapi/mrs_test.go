package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"apothecary/internal/kinds"
)

const mrFlowYAML = `name: on-mr
version: 4
parameters: { harness: fake, model: a/b }
agents:
  - name: reader
    prompt: read it
    inputs:
      - { name: changes }
phases:
  - id: look
    on: mr_opened
    stages:
      - { id: read, type: agent, agent: reader, input: { changes: $DIFF_TARGET }, on_fail: debug }
  - { id: merged, on: mr_merged, then: done }
  - { id: closed, on: mr_closed, then: done }
`

// The repository's merge requests are a source of runs: each listed with
// the factory's own marked and the runs on it; any flow that begins on a
// merge request starts there, filed under the merge request's ticket.
func TestMergeRequestsListAndStartARun(t *testing.T) {
	x := newConfEnv(t)
	pid := x.wizard(t)
	forge := kinds.FakeForgeOf("1")
	forge.Reset()
	t.Cleanup(forge.Reset)
	forge.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	forge.AddOpenMR("apothecary", "The factory's own", "apothecary/x", "main", "b1")
	if resp, data := x.do(t, "POST", "/api/projects/"+pid+"/flows", map[string]any{"name": "on-mr", "yaml": mrFlowYAML}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("save flow = %d: %s", resp.StatusCode, data)
	}
	type row struct {
		MR struct {
			Ref    struct{ IID int }
			Author string `json:"author"`
		} `json:"mr"`
		Mine     bool   `json:"mine"`
		TicketID string `json:"ticket_id"`
		Run      *struct {
			ID   string `json:"id"`
			Flow string `json:"flow"`
		} `json:"run"`
	}
	list := func() []row {
		t.Helper()
		resp, data := x.do(t, "GET", "/api/projects/"+pid+"/mrs", nil, nil)
		var rows []row
		if err := json.Unmarshal(data, &rows); err != nil || resp.StatusCode != 200 {
			t.Fatalf("mrs = %d %v: %s", resp.StatusCode, err, data)
		}
		return rows
	}
	rows := list()
	if len(rows) != 2 || rows[0].MR.Author != "apothecary" || !rows[0].Mine || rows[1].MR.Author != "alice" || rows[1].Mine || rows[1].TicketID != "" {
		t.Fatalf("rows = %+v", rows)
	}

	resp, data := x.do(t, "POST", "/api/projects/"+pid+"/mrs/1/runs", map[string]any{"flow": "on-mr"}, nil)
	var started struct {
		RunID    string `json:"run_id"`
		TicketID string `json:"ticket_id"`
	}
	if err := json.Unmarshal(data, &started); err != nil || resp.StatusCode != http.StatusCreated || started.RunID == "" || started.TicketID == "" {
		t.Fatalf("start = %d %v: %s", resp.StatusCode, err, data)
	}
	resp, data = x.do(t, "POST", "/api/projects/"+pid+"/mrs/1/runs", map[string]any{"flow": "on-mr"}, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(data), "already going") {
		t.Errorf("same flow again = %d: %s", resp.StatusCode, data)
	}
	if r := list()[1]; r.TicketID != started.TicketID || r.Run == nil || r.Run.ID != started.RunID || r.Run.Flow != "on-mr" {
		t.Errorf("alice's row after the start = %+v", r)
	}
	resp, data = x.do(t, "GET", "/api/tickets/"+started.TicketID, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"source_kind":"merge_request"`) || !strings.Contains(string(data), "(!1 by alice)") {
		t.Errorf("ticket = %d: %s", resp.StatusCode, data)
	}
	// The factory's own merge request takes a run like any other; a flow
	// that cannot begin on a merge request is refused and leaves no ticket.
	plain := "name: plain\nversion: 4\nparameters: { harness: fake, model: a/b }\nstages:\n  - { id: s, type: command, run: \"true\" }\n"
	if resp, data := x.do(t, "POST", "/api/projects/"+pid+"/flows", map[string]any{"name": "plain", "yaml": plain}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("save plain = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/projects/"+pid+"/mrs/2/runs", map[string]any{"flow": "plain"}, nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "no entrypoint that begins a run on a merge request") {
		t.Errorf("plain flow = %d: %s", resp.StatusCode, data)
	}
	if rows := list(); rows[0].TicketID != "" {
		t.Errorf("a refused start left ticket %s", rows[0].TicketID)
	}
	resp, data = x.do(t, "POST", "/api/projects/"+pid+"/mrs/2/runs", map[string]any{"flow": "on-mr"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("a run on the factory's own = %d: %s", resp.StatusCode, data)
	}
}
