package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"apothecary/internal/artifacts"
	"apothecary/internal/engine"
	"apothecary/internal/run"
)

// A ticket's attachments must be CLAIMED for the ticket (scope=ticket_goal,
// no run_id): that takes them out of `orphan` so the artifact GC can't sweep
// them, and leaves them shareable by every run of the ticket.
func TestTicketArtifactsClaimedAndShared(t *testing.T) {
	x := newEnv(t, engine.Config{})
	db := &memArtifactDB{rows: map[string]artifacts.Meta{
		"tick0001": {ID: "tick0001", Scope: artifacts.ScopeOrphan, Filename: "design.md"},
		"runn0001": {ID: "runn0001", Scope: artifacts.ScopeOrphan, Filename: "trace.log"},
	}}
	store := artifacts.New(db, t.TempDir(), 1<<20, []string{"text/*"})
	x.srv.Artifacts = store // upload/claim side
	x.e.SetArtifacts(store) // run-association + delivery side

	// Create a ticket carrying the shared attachment.
	resp, data := x.do(t, "POST", "/api/tickets", map[string]any{
		"title": "T", "goal": "g", "project_id": testProject,
		"artifact_ids": []string{"tick0001"},
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create ticket = %d: %s", resp.StatusCode, data)
	}
	var tk struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &tk)

	// Claimed for the ticket: GC-safe (not orphan) and not bound to a run.
	if got := db.rows["tick0001"]; got.Scope != artifacts.ScopeTicketGoal || got.RunID != "" {
		t.Fatalf("ticket artifact = %+v, want scope=ticket_goal with empty run_id", got)
	}

	// Start a run with its OWN direction + attachment.
	resp, data = x.do(t, "POST", "/api/tickets/"+tk.ID+"/runs", map[string]any{
		"flow": "loop", "goal": "avoid the cache",
		"artifact_ids": []string{"runn0001"},
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start run = %d: %s", resp.StatusCode, data)
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(data, &out)

	// The run's own attachment binds to the run; the ticket's stays shared.
	if got := db.rows["runn0001"]; got.Scope != artifacts.ScopeGoal || got.RunID != out.RunID {
		t.Errorf("run artifact = %+v, want scope=goal bound to %s", got, out.RunID)
	}
	if got := db.rows["tick0001"]; got.RunID != "" {
		t.Errorf("ticket artifact was hijacked by a run: %+v", got)
	}

	// The direction is recorded on the run.
	_, data = x.do(t, "GET", "/api/runs?ticket="+tk.ID, nil, nil)
	var views []run.View
	if err := json.Unmarshal(data, &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].RunGoal != "avoid the cache" {
		t.Fatalf("run_goal not recorded: %+v", views)
	}

	// A SECOND run still gets the ticket's attachment (the old bug: the first
	// run claimed it, leaving later runs with nothing).
	resp, data = x.do(t, "POST", "/api/tickets/"+tk.ID+"/runs", map[string]any{"flow": "loop"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("second run = %d: %s", resp.StatusCode, data)
	}
	if got := db.rows["tick0001"]; got.Scope != artifacts.ScopeTicketGoal || got.RunID != "" {
		t.Errorf("ticket artifact after 2nd run = %+v, want still shared", got)
	}
}

// Restart accepts per-attempt direction and records it on the new run.
func TestRestartWithDirection(t *testing.T) {
	x := newEnv(t, engine.Config{})
	runID := x.createTicket(t, "T")
	x.e.Wait(runID)

	resp, data := x.do(t, "POST", "/api/runs/"+runID+"/restart",
		map[string]any{"goal": "try the other adapter"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("restart = %d: %s", resp.StatusCode, data)
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(data, &out)

	_, data = x.do(t, "GET", "/api/runs/"+out.RunID, nil, nil)
	var got struct {
		Run run.View `json:"run"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Run.RunGoal != "try the other adapter" {
		t.Errorf("restart run_goal = %q", got.Run.RunGoal)
	}
}
