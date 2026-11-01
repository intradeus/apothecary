package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// A ticket's uploads must be CLAIMED for the ticket (ticket_id set, no
// run_id): out of the unclaimed state so the GC can't sweep them, and
// shareable by every run of the ticket.
func TestTicketAttachmentsClaimedAndShared(t *testing.T) {
	files := newTestFiles(t, 1<<20, []string{"text/*"})
	x := newEnvWith(t, engine.Config{}, files.deps)
	files.serve(x)
	att := files.att
	ctx := context.Background()
	tickUp, err := att.Save(ctx, testUser, "design.md", "text/markdown", strings.NewReader("# D"))
	if err != nil {
		t.Fatal(err)
	}
	runUp, err := att.Save(ctx, testUser, "trace.log", "text/plain", strings.NewReader("t"))
	if err != nil {
		t.Fatal(err)
	}

	// Create a ticket carrying the shared attachment.
	resp, data := x.do(t, "POST", "/api/tickets", map[string]any{
		"title": "T", "goal": "g", "project_id": testProject,
		"attachment_ids": []string{tickUp.ID},
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create ticket = %d: %s", resp.StatusCode, data)
	}
	var tk struct {
		ID            string   `json:"id"`
		AttachmentIDs []string `json:"attachment_ids"`
	}
	_ = json.Unmarshal(data, &tk)
	if len(tk.AttachmentIDs) != 1 || tk.AttachmentIDs[0] != tickUp.ID {
		t.Fatalf("ticket response attachment_ids = %v", tk.AttachmentIDs)
	}

	// Claimed for the ticket: not unclaimed, not bound to a run.
	if got, _ := att.Get(ctx, tickUp.ID); got.TicketID != tk.ID || got.RunID != "" {
		t.Fatalf("ticket attachment = %+v, want claimed by the ticket", got)
	}

	// Start a run with its OWN direction + attachment.
	resp, data = x.do(t, "POST", "/api/tickets/"+tk.ID+"/runs", map[string]any{
		"flow": "loop", "goal": "avoid the cache",
		"attachment_ids": []string{runUp.ID},
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start run = %d: %s", resp.StatusCode, data)
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(data, &out)

	// The run's own attachment binds to the run; the ticket's stays shared.
	if got, _ := att.Get(ctx, runUp.ID); got.RunID != out.RunID || got.TicketID != "" {
		t.Errorf("run attachment = %+v, want claimed by run %s", got, out.RunID)
	}
	if got, _ := att.Get(ctx, tickUp.ID); got.RunID != "" {
		t.Errorf("ticket attachment was hijacked by a run: %+v", got)
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
	// run claimed it, leaving later runs with nothing). It starts once the
	// first has ended: a ticket has one active run at a time.
	x.e.Wait(out.RunID)
	resp, data = x.do(t, "POST", "/api/tickets/"+tk.ID+"/runs", map[string]any{"flow": "loop"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("second run = %d: %s", resp.StatusCode, data)
	}
	var second struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(data, &second)
	if got, _ := att.Get(ctx, tickUp.ID); got.TicketID != tk.ID || got.RunID != "" {
		t.Errorf("ticket attachment after 2nd run = %+v, want still shared", got)
	}
	// The run keeps writing files under the t.TempDir() the store was built
	// on; leaving it in flight races the directory's removal.
	x.e.Wait(second.RunID)

	// Deleting the ticket removes its attachment rows — the leak fix.
	resp, data = x.do(t, "DELETE", "/api/tickets/"+tk.ID, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete ticket = %d: %s", resp.StatusCode, data)
	}
	if got, err := att.Get(ctx, tickUp.ID); err == nil {
		t.Errorf("ticket attachment survived ticket delete: %+v", got)
	}
}

// A ticket with an active run refuses another: 409, the error naming the
// run so the UI can offer to open or abort it.
func TestStartRunOnABusyTicket(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ticket := x.newTicket(t, "T", "")
	if _, err := x.store.Append(context.Background(), "r-live",
		&event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: testProject, UserID: testUser, TicketID: ticket}); err != nil {
		t.Fatal(err)
	}
	resp, data := x.startRun(t, ticket, "loop")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start on a busy ticket = %d: %s", resp.StatusCode, data)
	}
	got := decode[ErrorResponse](t, data)
	if got.RunID != "r-live" || !strings.Contains(got.Error, "run r-live is still active") {
		t.Errorf("409 body = %s", data)
	}
}

// --- ticket sources ---------------------------------------------------------

// stubSources serves one fake tracker under any integration id.
type stubSources struct{ src trackerport.TicketSource }

func (s stubSources) TicketSource(context.Context, string) (trackerport.TicketSource, error) {
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
	files := newTestFiles(t, 1<<20, []string{"text/*"})
	x := newEnvWith(t, engine.Config{}, files.deps)
	x.seedIntegration(t, "int-1", confstore.PortTicketSource)
	files.serve(x)
	att := files.att
	x.srv.Sources = stubSources{src: &fake.TicketSource{
		Tickets: []trackerport.SourceTicket{{
			Key: "PROJ-7", Title: "Add a Farewell function",
			Body: "The greeter only says hello.", URL: "https://tracker.test/browse/PROJ-7",
			Status: "To Do", Type: "Story", Updated: time.Now(),
			Attachments: []trackerport.SourceAttachment{
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
		ID            string   `json:"id"`
		Title         string   `json:"title"`
		Goal          string   `json:"goal"`
		SourceKey     string   `json:"source_key"`
		SourceKind    string   `json:"source_kind"`
		SourceURL     string   `json:"source_url"`
		AttachmentIDs []string `json:"attachment_ids"`
		Warnings      []string `json:"warnings"`
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
	if len(tk.AttachmentIDs) != 1 {
		t.Fatalf("attachment_ids = %v", tk.AttachmentIDs)
	}
	if got, err := att.Get(context.Background(), tk.AttachmentIDs[0]); err != nil ||
		got.Filename != "spec.md" || got.TicketID != tk.ID || got.RunID != "" {
		t.Errorf("imported attachment = %+v (%v), want spec.md claimed for the ticket", got, err)
	}

	// The ticket lists its own attachments (what both the ticket page and the
	// edit form render) — bytes stay on the ordinary artifact route.
	resp, data = x.do(t, "GET", "/api/tickets/"+tk.ID+"/attachments", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ticket attachments = %d: %s", resp.StatusCode, data)
	}
	var listed struct {
		Attachments []struct {
			ID, Filename, Mime, TicketID string `json:"-"`
		} `json:"attachments"`
	}
	var envl struct {
		Attachments []map[string]any `json:"attachments"`
	}
	_ = listed
	if err := json.Unmarshal(data, &envl); err != nil {
		t.Fatal(err)
	}
	if len(envl.Attachments) != 1 || envl.Attachments[0]["filename"] != "spec.md" ||
		envl.Attachments[0]["ticket_id"] != tk.ID {
		t.Fatalf("ticket attachments = %s", data)
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
	x.seedIntegration(t, "int-1", confstore.PortTicketSource)
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
func TestRerunWithDirection(t *testing.T) {
	x := newEnv(t, engine.Config{})
	runID := x.createTicket(t, "T")
	x.e.Wait(runID)

	resp, data := x.do(t, "POST", "/api/runs/"+runID+"/rerun",
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
