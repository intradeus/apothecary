package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"apothecary/internal/project/kinds"
)

const mrFlowYAML = `name: on-mr
version: 5
operates_on: merge_request
parameters: { harness: fake, model: a/b }
agents:
  - name: reader
    prompt: read it
    inputs:
      - { name: changes, type: changeset }
main:
  - { id: read, type: agent, harness: $harness, model: $model, agent: reader, input: { changes: { from: run.git.diff_target, type: changeset } }, on_fail: debug }
outcomes:
  - { id: merged, on: mr_merged, then: done }
  - { id: closed, on: mr_closed, then: done }
`

// The repository's merge requests are a source of runs: each listed with
// the runs that went through it; any flow that begins on a merge request
// starts there, given its title and description — with no ticket made.
func TestMergeRequestsListAndStartARun(t *testing.T) {
	x := newConfEnv(t)
	pid := x.wizard(t)
	repoHost := kinds.FakeRepoHostOf("1")
	repoHost.Reset()
	t.Cleanup(repoHost.Reset)
	repoHost.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	repoHost.AddOpenMR("apothecary", "The factory's own", "apothecary/x", "main", "b1")
	if resp, data := x.do(t, "POST", "/api/projects/"+pid+"/flows", map[string]any{"name": "on-mr", "yaml": mrFlowYAML}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("save flow = %d: %s", resp.StatusCode, data)
	}
	type row struct {
		MR struct {
			Ref    struct{ IID int }
			Author string `json:"author"`
		} `json:"mr"`
		Runs int `json:"runs"`
		Run  *struct {
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
	if len(rows) != 2 || rows[0].MR.Author != "apothecary" || rows[1].MR.Author != "alice" || rows[1].Runs != 0 || rows[1].Run != nil {
		t.Fatalf("rows = %+v", rows)
	}

	resp, data := x.do(t, "POST", "/api/projects/"+pid+"/mrs/1/runs", map[string]any{"flow": "on-mr"}, nil)
	var started struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(data, &started); err != nil || resp.StatusCode != http.StatusCreated || started.RunID == "" {
		t.Fatalf("start = %d %v: %s", resp.StatusCode, err, data)
	}
	// Any second run on the merge request while the first is going — the
	// same flow or another — is refused, naming the run.
	resp, data = x.do(t, "POST", "/api/projects/"+pid+"/mrs/1/runs", map[string]any{"flow": "on-mr"}, nil)
	if resp.StatusCode != http.StatusConflict || decode[ErrorResponse](t, data).RunID != started.RunID {
		t.Errorf("same flow again = %d: %s", resp.StatusCode, data)
	}
	if r := list()[1]; r.Runs != 1 || r.Run == nil || r.Run.ID != started.RunID || r.Run.Flow != "on-mr" {
		t.Errorf("alice's row after the start = %+v", r)
	}
	// No ticket: the run carries the merge request as its work.
	if resp, data := x.do(t, "GET", "/api/tickets", nil, nil); resp.StatusCode != 200 || strings.Contains(string(data), "!1 by alice") {
		t.Errorf("tickets = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/runs/"+started.RunID, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), "(!1 by alice)") || strings.Contains(string(data), `"ticket_id"`) {
		t.Errorf("run = %d: %s", resp.StatusCode, data)
	}
	// One merge request, with its description and its runs.
	resp, data = x.do(t, "GET", "/api/projects/"+pid+"/mrs/1", nil, nil)
	if one := decode[MRRow](t, data); resp.StatusCode != 200 || one.MR.Title != "Add caching" || one.Runs != 1 || one.Run == nil || one.Run.ID != started.RunID {
		t.Errorf("one = %d: %s", resp.StatusCode, data)
	}
	for path, want := range map[string]int{"/mrs/x": 400, "/mrs/99": 400} {
		if resp, data := x.do(t, "GET", "/api/projects/"+pid+path, nil, nil); resp.StatusCode != want {
			t.Errorf("%s = %d: %s", path, resp.StatusCode, data)
		}
	}
	// The factory's own merge request takes a run like any other; a flow
	// whose runs are started on tickets is refused.
	plain := "name: plain\nversion: 5\nparameters: { harness: fake, model: a/b }\nmain:\n  - { id: s, type: command, run: \"true\" }\n"
	if resp, data := x.do(t, "POST", "/api/projects/"+pid+"/flows", map[string]any{"name": "plain", "yaml": plain}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("save plain = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/projects/"+pid+"/mrs/2/runs", map[string]any{"flow": "plain"}, nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "starts its runs on a ticket") {
		t.Errorf("plain flow = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/projects/"+pid+"/mrs/2/runs", map[string]any{"flow": "on-mr"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("a run on the factory's own = %d: %s", resp.StatusCode, data)
	}
}

// A merge request targeting another open one's branch is stacked on it: it
// merges into that branch, and the row says so.
func TestMergeRequestsListStackedOn(t *testing.T) {
	x := newConfEnv(t)
	pid := x.wizard(t)
	repoHost := kinds.FakeRepoHostOf("1")
	repoHost.Reset()
	t.Cleanup(repoHost.Reset)
	repoHost.AddOpenMR("alice", "Base", "feature/a", "main", "a1")
	repoHost.AddOpenMR("alice", "On top", "feature/b", "feature/a", "b1")
	resp, data := x.do(t, "GET", "/api/projects/"+pid+"/mrs", nil, nil)
	var rows []struct {
		MR struct {
			Ref struct{ IID int }
		} `json:"mr"`
		StackedOn *struct {
			IID    int    `json:"iid"`
			Branch string `json:"branch"`
		} `json:"stacked_on"`
	}
	if err := json.Unmarshal(data, &rows); err != nil || resp.StatusCode != 200 || len(rows) != 2 {
		t.Fatalf("mrs = %d %v: %s", resp.StatusCode, err, data)
	}
	for _, r := range rows {
		switch r.MR.Ref.IID {
		case 1:
			if r.StackedOn != nil {
				t.Errorf("!1 targets main, stacked on %+v", r.StackedOn)
			}
		case 2:
			if r.StackedOn == nil || r.StackedOn.IID != 1 || r.StackedOn.Branch != "feature/a" {
				t.Errorf("!2 stacked on %+v, want !1 feature/a", r.StackedOn)
			}
		}
	}
}
