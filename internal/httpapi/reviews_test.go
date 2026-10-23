package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"apothecary/internal/kinds"
)

const reviewFlowYAML = `name: rev
version: 4
parameters: { harness: fake, model: a/b }
agents:
  - name: pr_reviewer
    prompt: review it
    inputs:
      - { name: changes }
    outputs:
      - { name: review, type: review_threads, path: artifacts/review.yaml }
phases:
  - id: first_look
    on: mr_opened
    stages:
      - { id: read, type: agent, agent: pr_reviewer, input: { changes: $DIFF_TARGET }, on_fail: debug }
      - { id: post, type: action, action: post_review, input: { review: artifacts/review.yaml } }
  - { id: merged, on: mr_merged, then: done }
  - { id: closed, on: mr_closed, then: done }
`

// The Reviews tab lists the repository's open merge requests with the
// factory's own marked; starting a review makes the merge request's ticket
// and a run on it, and a second start while it runs is refused.
func TestReviewsListAndStart(t *testing.T) {
	x := newConfEnv(t)
	pid := x.wizard(t)
	forge := kinds.FakeForgeOf("1")
	forge.Reset()
	t.Cleanup(forge.Reset)
	forge.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	forge.AddOpenMR("apothecary", "The factory's own", "apothecary/x", "main", "b1")
	if resp, data := x.do(t, "POST", "/api/projects/"+pid+"/flows", map[string]any{"name": "rev", "yaml": reviewFlowYAML}, nil); resp.StatusCode != http.StatusCreated {
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
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"run"`
	}
	list := func() []row {
		t.Helper()
		resp, data := x.do(t, "GET", "/api/projects/"+pid+"/reviews", nil, nil)
		var rows []row
		if err := json.Unmarshal(data, &rows); err != nil || resp.StatusCode != 200 {
			t.Fatalf("reviews = %d %v: %s", resp.StatusCode, err, data)
		}
		return rows
	}
	rows := list()
	if len(rows) != 2 || rows[0].MR.Author != "apothecary" || !rows[0].Mine || rows[1].MR.Author != "alice" || rows[1].Mine || rows[1].TicketID != "" {
		t.Fatalf("rows = %+v", rows)
	}

	resp, data := x.do(t, "POST", "/api/projects/"+pid+"/mrs/1/review", map[string]any{"flow": "rev"}, nil)
	var started struct {
		RunID    string `json:"run_id"`
		TicketID string `json:"ticket_id"`
	}
	if err := json.Unmarshal(data, &started); err != nil || resp.StatusCode != http.StatusCreated || started.RunID == "" || started.TicketID == "" {
		t.Fatalf("start = %d %v: %s", resp.StatusCode, err, data)
	}
	resp, data = x.do(t, "POST", "/api/projects/"+pid+"/mrs/1/review", map[string]any{"flow": "rev"}, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(data), "already running") {
		t.Errorf("second start = %d: %s", resp.StatusCode, data)
	}
	rows = list()
	if r := rows[1]; r.TicketID != started.TicketID || r.Run == nil || r.Run.ID != started.RunID {
		t.Errorf("alice's row after the start = %+v", r)
	}
	resp, data = x.do(t, "GET", "/api/tickets/"+started.TicketID, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), `"source_kind":"merge_request"`) || !strings.Contains(string(data), "(!1 by alice)") {
		t.Errorf("ticket = %d: %s", resp.StatusCode, data)
	}
	// A flow with no entrypoint on a merge request cannot review one — and a
	// refused start leaves no ticket behind.
	plain := "name: plain\nversion: 4\nparameters: { harness: fake, model: a/b }\nstages:\n  - { id: s, type: command, run: \"true\" }\n"
	if resp, data := x.do(t, "POST", "/api/projects/"+pid+"/flows", map[string]any{"name": "plain", "yaml": plain}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("save plain = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/projects/"+pid+"/mrs/2/review", map[string]any{"flow": "plain"}, nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "no entrypoint that begins a run on a merge request") {
		t.Errorf("plain flow = %d: %s", resp.StatusCode, data)
	}
	if rows := list(); rows[0].TicketID != "" {
		t.Errorf("a refused start left ticket %s", rows[0].TicketID)
	}
}
