package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/artifacts"
	"apothecary/internal/engine"
	"apothecary/internal/ports"
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
	var second struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(data, &second)
	if got := db.rows["tick0001"]; got.Scope != artifacts.ScopeTicketGoal || got.RunID != "" {
		t.Errorf("ticket artifact after 2nd run = %+v, want still shared", got)
	}
	// Both runs keep writing artifacts under the t.TempDir() the store was
	// built on; leaving them in flight races the directory's removal
	// ("TempDir RemoveAll cleanup: directory not empty").
	x.e.Wait(out.RunID)
	x.e.Wait(second.RunID)
}

// --- ticket sources ---------------------------------------------------------

// stubSources serves one fake tracker under any integration id.
type stubSources struct{ src ports.TicketSource }

func (s stubSources) TicketSource(context.Context, string) (ports.TicketSource, error) {
	return s.src, nil
}
func (s stubSources) TicketSourceKind(context.Context, string) (string, error) {
	return "faketracker", nil
}

// The import route is the whole point of the tracker integration: search it,
// then create a ticket that carries the issue's title, body, provenance and
// attachment bytes — with the attachment claimed for the ticket like any
// hand-uploaded file.
func TestImportTicketFromSource(t *testing.T) {
	x := newEnv(t, engine.Config{})
	db := &memArtifactDB{rows: map[string]artifacts.Meta{}}
	store := artifacts.New(db, t.TempDir(), 1<<20, []string{"text/*"})
	x.srv.Artifacts = store
	x.srv.Sources = stubSources{src: &fake.TicketSource{
		Tickets: []ports.SourceTicket{{
			Key: "PROJ-7", Title: "Add a Farewell function",
			Body: "The greeter only says hello.", URL: "https://tracker.test/browse/PROJ-7",
			Status: "To Do", Type: "Story", Updated: time.Now(),
			Attachments: []ports.SourceAttachment{
				{ID: "a1", Filename: "spec.md", Mime: "text/markdown", Size: 5},
			},
		}},
		Blobs: map[string][]byte{"a1": []byte("hello")},
	}}

	// Search the tracker (the picker).
	resp, data := x.do(t, "GET", "/api/integrations/int-1/tickets?q=farewell", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search = %d: %s", resp.StatusCode, data)
	}
	var found []struct {
		Key, Title, Status string
	}
	if err := json.Unmarshal(data, &found); err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Key != "PROJ-7" || found[0].Status != "To Do" {
		t.Fatalf("search results = %s", data)
	}

	// Preview one issue: the attachment list is what the form promises to copy.
	_, data = x.do(t, "GET", "/api/integrations/int-1/tickets/PROJ-7", nil, nil)
	var preview struct {
		Attachments []struct{ Filename string } `json:"attachments"`
	}
	_ = json.Unmarshal(data, &preview)
	if len(preview.Attachments) != 1 || preview.Attachments[0].Filename != "spec.md" {
		t.Fatalf("preview attachments = %s", data)
	}

	// Import it.
	resp, data = x.do(t, "POST", "/api/tickets", map[string]any{
		"project_id": testProject, "source_id": "int-1", "source_key": "PROJ-7",
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import = %d: %s", resp.StatusCode, data)
	}
	var tk struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Goal        string   `json:"goal"`
		SourceKey   string   `json:"source_key"`
		SourceKind  string   `json:"source_kind"`
		SourceURL   string   `json:"source_url"`
		ArtifactIDs []string `json:"artifact_ids"`
		Warnings    []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &tk); err != nil {
		t.Fatal(err)
	}
	if tk.Title != "Add a Farewell function" || tk.Goal != "The greeter only says hello." {
		t.Errorf("imported ticket = %+v", tk)
	}
	if tk.SourceKey != "PROJ-7" || tk.SourceKind != "faketracker" || tk.SourceURL == "" {
		t.Errorf("provenance = %+v", tk)
	}
	if len(tk.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", tk.Warnings)
	}
	if len(tk.ArtifactIDs) != 1 {
		t.Fatalf("artifact_ids = %v", tk.ArtifactIDs)
	}
	if got := db.rows[tk.ArtifactIDs[0]]; got.Filename != "spec.md" ||
		got.Scope != artifacts.ScopeTicketGoal || got.RunID != "" {
		t.Errorf("imported artifact = %+v, want spec.md claimed for the ticket", got)
	}

	// The ticket lists its own attachments (what both the ticket page and the
	// edit form render) — bytes stay on the ordinary artifact route.
	resp, data = x.do(t, "GET", "/api/tickets/"+tk.ID+"/artifacts", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ticket artifacts = %d: %s", resp.StatusCode, data)
	}
	var listed []struct {
		ID, Filename, Mime, Scope string
		Size                      int64
	}
	if err := json.Unmarshal(data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Filename != "spec.md" || listed[0].Scope != artifacts.ScopeTicketGoal {
		t.Fatalf("ticket artifacts = %s", data)
	}

	// A second import of the same issue is refused, naming the existing one.
	resp, data = x.do(t, "POST", "/api/tickets", map[string]any{
		"project_id": testProject, "source_id": "int-1", "source_key": "PROJ-7",
	}, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(data), tk.ID) {
		t.Fatalf("duplicate import = %d: %s", resp.StatusCode, data)
	}
}

// Without ticket-source wiring the routes report that plainly (503) rather
// than 404ing or pretending the tracker is empty.
func TestTicketSourceRoutesWithoutWiring(t *testing.T) {
	x := newEnv(t, engine.Config{})
	resp, data := x.do(t, "GET", "/api/integrations/int-1/tickets", nil, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("search = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "POST", "/api/tickets", map[string]any{
		"project_id": testProject, "source_id": "int-1", "source_key": "PROJ-7",
	}, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("import = %d: %s", resp.StatusCode, data)
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
