package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/tickets"
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
	resp, data := x.do(t, "POST", "/api/projects/"+testProject+"/tickets", map[string]any{
		"title": "T", "goal": "g", "attachment_ids": []string{tickUp.ID},
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create ticket = %d: %s", resp.StatusCode, data)
	}
	created := decode[TicketDetail](t, data)
	tk := struct{ ID string }{created.TicketID}
	if len(created.AttachmentIDs) != 1 || created.AttachmentIDs[0] != tickUp.ID {
		t.Fatalf("ticket response attachment_ids = %v", created.AttachmentIDs)
	}

	// Claimed for the ticket: not unclaimed, not bound to a run.
	if got, _ := att.Get(ctx, tickUp.ID); got.TicketID != tk.ID || got.RunID != "" {
		t.Fatalf("ticket attachment = %+v, want claimed by the ticket", got)
	}

	// Start a run with its OWN direction + attachment.
	resp, data = x.do(t, "POST", ticketPath(testProject, tk.ID)+"/runs", map[string]any{
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
	resp, data = x.do(t, "POST", ticketPath(testProject, tk.ID)+"/runs", map[string]any{"flow": "loop"}, nil)
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

	// The ticket lists its own attachments — bytes stay on the ordinary
	// artifact route.
	resp, data = x.do(t, "GET", ticketPath(testProject, tk.ID)+"/attachments", nil, nil)
	if got := decode[TicketAttachments](t, data); resp.StatusCode != http.StatusOK || len(got.Attachments) != 1 ||
		got.Attachments[0].ID != tickUp.ID || len(got.Remote) != 0 {
		t.Fatalf("ticket attachments = %d: %s", resp.StatusCode, data)
	}

	// Deleting the ticket removes its attachment rows — the leak fix.
	resp, data = x.do(t, "DELETE", ticketPath(testProject, tk.ID), nil, nil)
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

// Without ticket-source wiring the routes report that plainly (503) rather
// than 404ing or pretending the tracker is empty.
func TestTicketRoutesWithoutWiring(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedIntegration(t, "int-1", confstore.PortTicketSource)
	ctx := context.Background()
	p, err := x.srv.Conf.GetProject(ctx, testProject)
	if err != nil {
		t.Fatal(err)
	}
	p.TicketSourceID = "int-1"
	if _, err := x.srv.Conf.UpdateProject(ctx, testProject, p); err != nil {
		t.Fatal(err)
	}
	// A Live with no sources reads no remote tracker.
	if resp, data := x.do(t, "GET", "/api/projects/"+testProject+"/tickets", nil, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("list = %d: %s", resp.StatusCode, data)
	}
	if resp, data := x.do(t, "GET", "/api/integrations/int-1/statuses", nil, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("statuses = %d: %s", resp.StatusCode, data)
	}
	// No Live at all: every workshop ticket route says so.
	x.srv.Live = nil
	if resp, data := x.do(t, "GET", ticketPath(testProject, "PROJ-7"), nil, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("ticket = %d: %s", resp.StatusCode, data)
	}
	// Writes to a remote tracker need its writer.
	x.srv.Live = tickets.NewLive(x.srv.Conf, stubSources{src: &fake.TicketSource{Tickets: []trackerport.SourceTicket{{RemoteID: "1", Key: "PROJ-7", Title: "t"}}}})
	if resp, data := x.do(t, "POST", ticketPath(testProject, "PROJ-7")+"/comments", TicketCommentRequest{Body: "x"}, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("comment = %d: %s", resp.StatusCode, data)
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
