package httpapi

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/tickets"
	"apothecary/internal/project/tickets/internaltracker"
)

// stubTrackers resolves a ticket's tracker as the resolver does: the fake
// remote for a ticket with a source, the internal tracker otherwise.
type stubTrackers struct {
	conf *confstore.Service
	src  *fake.TicketSource
}

func (s stubTrackers) TicketTracker(ctx context.Context, ticketID string) (trackerport.TicketWriter, string, error) {
	t, err := s.conf.GetTicket(ctx, ticketID)
	if err != nil {
		return nil, "", err
	}
	if t.SourceID == "" {
		return internaltracker.New(s.conf), t.ID, nil
	}
	return s.src, t.SourceKey, nil
}

// trackerEnv is a server with one remote ticket source (int-1, a fake
// tracker holding PROJ-7), an imported ticket mirroring PROJ-7 and a
// hand-written one on the internal tracker.
type trackerEnv struct {
	*env
	src      *fake.TicketSource
	remote   string // the imported ticket
	local    string // the hand-written ticket
	internal string // the user's internal tracker
}

func newTrackerEnv(t *testing.T) *trackerEnv {
	t.Helper()
	x := newEnv(t, engine.Config{})
	x.seedIntegration(t, "int-1", confstore.PortTicketSource)
	src := &fake.TicketSource{Tickets: []trackerport.SourceTicket{{
		RemoteID: "100", Key: "PROJ-7", Title: "Remote one", Body: "b", URL: "https://tracker.test/PROJ-7",
		Status: "To Do", StatusID: "1", Type: "Story", TypeID: "10", Updated: time.Now().Add(-time.Hour),
	}}}
	sources := stubSources{src: src}
	x.srv.Sources = sources
	x.srv.Trackers = stubTrackers{conf: x.srv.Conf, src: src}
	x.srv.Syncer = &tickets.Syncer{Conf: x.srv.Conf, Sources: sources}
	te := &trackerEnv{env: x, src: src}
	res, data := x.do(t, "POST", "/api/tickets", map[string]any{"project_id": testProject, "source_id": "int-1", "source_key": "PROJ-7"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("import = %d %s", res.StatusCode, data)
	}
	te.remote = decode[ticketResp](t, data).ID
	res, data = x.do(t, "POST", "/api/tickets", map[string]any{"project_id": testProject, "title": "Local one"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", res.StatusCode, data)
	}
	te.local = decode[ticketResp](t, data).ID
	in, err := x.srv.Conf.InternalTracker(context.Background(), testUser)
	if err != nil {
		t.Fatal(err)
	}
	te.internal = in.ID
	return te
}

func (x *trackerEnv) ticket(t *testing.T, id string) ticketResp {
	t.Helper()
	res, data := x.do(t, "GET", "/api/tickets/"+id, nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get %s = %d %s", id, res.StatusCode, data)
	}
	return decode[ticketResp](t, data)
}

// statusID is the id of the integration's status of that name.
func (x *trackerEnv) statusID(t *testing.T, integrationID, name string) string {
	t.Helper()
	res, data := x.do(t, "GET", "/api/integrations/"+integrationID+"/statuses", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("statuses = %d %s", res.StatusCode, data)
	}
	for _, st := range decode[TrackerStatusList](t, data).Statuses {
		if st.Name == name {
			return st.ID
		}
	}
	t.Fatalf("no status %q in %s", name, data)
	return ""
}

// The ticket JSON carries its status and type resolved from its tracker.
func TestTicketJSONResolvesStatusAndType(t *testing.T) {
	x := newTrackerEnv(t)
	got := x.ticket(t, x.remote)
	if got.Status == nil || got.Status.Name != "To Do" || got.Status.IsDone || got.Type == nil || got.Type.Name != "Story" {
		t.Errorf("remote ticket status/type = %+v / %+v", got.Status, got.Type)
	}
	if got.Links == nil || got.SyncState != confstore.SyncOK {
		t.Errorf("links = %v, sync state = %q", got.Links, got.SyncState)
	}
	local := x.ticket(t, x.local)
	if local.Status == nil || local.Status.FlowStep != trackerport.StepTodo || local.Type == nil {
		t.Errorf("local ticket status/type = %+v / %+v", local.Status, local.Type)
	}
}

// Status, labels and comments go through the ticket's tracker: the remote
// issue (and the mirror is refreshed), or the internal tracker.
func TestTicketWritesGoThroughTheTracker(t *testing.T) {
	x := newTrackerEnv(t)

	// Remote.
	res, data := x.do(t, "POST", "/api/tickets/"+x.remote+"/status", TicketStatusRequest{StatusID: x.statusID(t, "int-1", "In Progress")}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("remote status = %d %s", res.StatusCode, data)
	}
	if got := decode[ticketResp](t, data); got.Status == nil || got.Status.Name != "In Progress" || got.StatusChangedBy != confstore.ChangedBySync {
		t.Errorf("remote after the move = %+v by %q", got.Status, got.StatusChangedBy)
	}
	if !slices.Contains(x.src.Calls, "transition PROJ-7 In Progress") {
		t.Errorf("tracker calls = %v", x.src.Calls)
	}
	res, data = x.do(t, "PUT", "/api/tickets/"+x.remote+"/labels", TicketLabelsRequest{Labels: []string{"a", " b "}}, nil)
	if got := decode[ticketResp](t, data); res.StatusCode != http.StatusOK || !slices.Equal(got.Labels, []string{"a", "b"}) {
		t.Errorf("remote labels = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "POST", "/api/tickets/"+x.remote+"/comments", TicketCommentRequest{Body: "hello"}, nil)
	comments := decode[[]confstore.TicketComment](t, data)
	if res.StatusCode != http.StatusCreated || len(comments) != 1 || comments[0].Body != "hello" || comments[0].Origin != confstore.CommentRemote {
		t.Errorf("remote comment = %d %s", res.StatusCode, data)
	}

	// Internal.
	res, data = x.do(t, "POST", "/api/tickets/"+x.local+"/status", TicketStatusRequest{StatusID: x.statusID(t, x.internal, "In progress")}, nil)
	if got := decode[ticketResp](t, data); res.StatusCode != http.StatusOK || got.Status == nil || got.Status.Name != "In progress" || got.StatusChangedBy != confstore.ChangedByUser {
		t.Errorf("local status = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "PUT", "/api/tickets/"+x.local+"/labels", TicketLabelsRequest{Labels: []string{"x"}}, nil)
	if got := decode[ticketResp](t, data); res.StatusCode != http.StatusOK || !slices.Equal(got.Labels, []string{"x"}) {
		t.Errorf("local labels = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "POST", "/api/tickets/"+x.local+"/comments", TicketCommentRequest{Body: "note"}, nil)
	comments = decode[[]confstore.TicketComment](t, data)
	if res.StatusCode != http.StatusCreated || len(comments) != 1 || comments[0].Origin != confstore.CommentUser {
		t.Errorf("local comment = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "GET", "/api/tickets/"+x.local+"/comments", nil, nil)
	if res.StatusCode != http.StatusOK || len(decode[[]confstore.TicketComment](t, data)) != 1 {
		t.Errorf("comments = %d %s", res.StatusCode, data)
	}

	// Refusals.
	for _, tc := range []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"another tracker's status", "POST", "/api/tickets/" + x.local + "/status", TicketStatusRequest{StatusID: x.statusID(t, "int-1", "Done")}, http.StatusBadRequest},
		{"an empty comment", "POST", "/api/tickets/" + x.local + "/comments", TicketCommentRequest{Body: "  "}, http.StatusBadRequest},
		{"a label with a comma", "PUT", "/api/tickets/" + x.local + "/labels", TicketLabelsRequest{Labels: []string{"a,b"}}, http.StatusBadRequest},
		{"a missing ticket", "POST", "/api/tickets/nope/comments", TicketCommentRequest{Body: "x"}, http.StatusNotFound},
	} {
		if res, data := x.do(t, tc.method, tc.path, tc.body, nil); res.StatusCode != tc.want {
			t.Errorf("%s = %d %s, want %d", tc.name, res.StatusCode, data, tc.want)
		}
	}
	x.src.Reachable = map[string][]string{"In Progress": {"Done"}}
	if res, data := x.do(t, "POST", "/api/tickets/"+x.remote+"/status", TicketStatusRequest{StatusID: x.statusID(t, "int-1", "To Do")}, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("unreachable status = %d %s", res.StatusCode, data)
	}
}

// An edit of what a remote tracker owns is refused; the rest goes through.
func TestUpdateOfARemoteOwnedFieldIs409(t *testing.T) {
	x := newTrackerEnv(t)
	cur := x.ticket(t, x.remote)
	body := map[string]any{"project_id": testProject, "title": "Changed here", "goal": cur.Goal}
	if res, data := x.do(t, "PUT", "/api/tickets/"+x.remote, body, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("title edit = %d %s", res.StatusCode, data)
	}
	body["title"] = cur.Title
	body["branch"] = "feat/elsewhere"
	if res, data := x.do(t, "PUT", "/api/tickets/"+x.remote, body, nil); res.StatusCode != http.StatusOK {
		t.Errorf("branch edit = %d %s", res.StatusCode, data)
	}
}

// The refresh routes re-read the tracker.
func TestSyncRoutes(t *testing.T) {
	x := newTrackerEnv(t)
	x.src.Tickets[0].Title, x.src.Tickets[0].Updated = "Edited remotely", time.Now()
	res, data := x.do(t, "POST", "/api/tickets/"+x.remote+"/sync", nil, nil)
	if got := decode[ticketResp](t, data); res.StatusCode != http.StatusOK || got.Title != "Edited remotely" || got.SyncedAt == nil {
		t.Errorf("ticket sync = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "POST", "/api/integrations/int-1/sync", nil, nil)
	if got := decode[tickets.SyncCounts](t, data); res.StatusCode != http.StatusOK || got.Tickets != 1 || got.Synced != 1 {
		t.Errorf("integration sync = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "POST", "/api/integrations/"+x.internal+"/sync", nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("internal sync = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "POST", "/api/tickets/nope/sync", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("missing ticket sync = %d %s", res.StatusCode, data)
	}
}

// ?done filters on the status's Done flag.
func TestTicketListDoneFilter(t *testing.T) {
	x := newTrackerEnv(t)
	if res, data := x.do(t, "POST", "/api/tickets/"+x.local+"/status", TicketStatusRequest{StatusID: x.statusID(t, x.internal, "Done")}, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("done = %d %s", res.StatusCode, data)
	}
	ids := func(q string) []string {
		res, data := x.do(t, "GET", "/api/tickets"+q, nil, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("list%s = %d %s", q, res.StatusCode, data)
		}
		var out []string
		for _, v := range decode[[]TicketView](t, data) {
			out = append(out, v.ID)
		}
		slices.Sort(out)
		return out
	}
	all := []string{x.local, x.remote}
	slices.Sort(all)
	if got := ids(""); !slices.Equal(got, all) {
		t.Errorf("all = %v", got)
	}
	if got := ids("?done=1"); !slices.Equal(got, []string{x.local}) {
		t.Errorf("done = %v", got)
	}
	if got := ids("?done=0"); !slices.Equal(got, []string{x.remote}) {
		t.Errorf("not done = %v", got)
	}
}

// The Statuses table is validated on save.
func TestPutTrackerStatuses(t *testing.T) {
	x := newTrackerEnv(t)
	res, data := x.do(t, "GET", "/api/integrations/"+x.internal+"/statuses", nil, nil)
	list := decode[TrackerStatusList](t, data).Statuses
	if res.StatusCode != http.StatusOK || len(list) == 0 {
		t.Fatalf("internal statuses = %d %s", res.StatusCode, data)
	}
	twice := slices.Clone(list)
	twice[1].FlowStep = twice[0].FlowStep
	if res, data := x.do(t, "PUT", "/api/integrations/"+x.internal+"/statuses", TrackerStatusesUpdate{Statuses: twice}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a step held twice = %d %s", res.StatusCode, data)
	}
	todo := x.ticket(t, x.local).StatusID
	var without []confstore.TrackerStatus
	for _, st := range list {
		if st.ID != todo {
			without = append(without, st)
		}
	}
	if res, data := x.do(t, "PUT", "/api/integrations/"+x.internal+"/statuses", TrackerStatusesUpdate{Statuses: without}, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("a status in use deleted = %d %s", res.StatusCode, data)
	}
	without = append(without, confstore.TrackerStatus{Name: "Triage"})
	res, data = x.do(t, "PUT", "/api/integrations/"+x.internal+"/statuses",
		TrackerStatusesUpdate{Statuses: without, ReplaceWith: map[string]string{todo: without[0].ID}}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("replaced = %d %s", res.StatusCode, data)
	}
	if got := decode[TrackerStatusList](t, data).Statuses; len(got) != len(list) || got[len(got)-1].Name != "Triage" {
		t.Errorf("saved = %s", data)
	}
	if got := x.ticket(t, x.local).StatusID; got != without[0].ID {
		t.Errorf("the ticket is in %s, want %s", got, without[0].ID)
	}

	// A remote tracker's list: only Done and the step are the user's.
	res, data = x.do(t, "GET", "/api/integrations/int-1/statuses?refresh=1", nil, nil)
	remote := decode[TrackerStatusList](t, data).Statuses
	if res.StatusCode != http.StatusOK || len(remote) != len(fake.DefaultStatuses) {
		t.Fatalf("remote statuses = %d %s", res.StatusCode, data)
	}
	remote[1].IsDone = true
	res, data = x.do(t, "PUT", "/api/integrations/int-1/statuses", TrackerStatusesUpdate{Statuses: remote}, nil)
	if got := decode[TrackerStatusList](t, data).Statuses; res.StatusCode != http.StatusOK || !got[1].IsDone {
		t.Errorf("remote save = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "PUT", "/api/integrations/int-1/statuses", TrackerStatusesUpdate{Statuses: remote[1:]}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a remote status dropped = %d %s", res.StatusCode, data)
	}

	// Types: every one has a change type.
	res, data = x.do(t, "GET", "/api/integrations/"+x.internal+"/types", nil, nil)
	types := decode[TrackerTypeList](t, data).Types
	if res.StatusCode != http.StatusOK || len(types) == 0 {
		t.Fatalf("types = %d %s", res.StatusCode, data)
	}
	types = append(types, confstore.TrackerType{Name: "Spike", ChangeType: "nonsense"})
	if res, data := x.do(t, "PUT", "/api/integrations/"+x.internal+"/types", TrackerTypesUpdate{Types: types}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a type with no change type = %d %s", res.StatusCode, data)
	}
}

// A ticket source with linked tickets is deleted only with
// ?unlink_tickets=1, which moves them onto the internal tracker.
func TestDeleteTicketSourceUnlinks(t *testing.T) {
	x := newTrackerEnv(t)
	res, data := x.do(t, "DELETE", "/api/integrations/int-1", nil, nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("delete = %d %s", res.StatusCode, data)
	}
	if got := decode[ErrorResponse](t, data); got.Tickets == nil || *got.Tickets != 1 || got.ActiveRuns == nil || *got.ActiveRuns != 0 {
		t.Errorf("409 body = %s", data)
	}
	if res, data := x.do(t, "DELETE", "/api/integrations/int-1?unlink_tickets=1", nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("unlink = %d %s", res.StatusCode, data)
	}
	got := x.ticket(t, x.remote)
	if got.SourceID != "" || got.SourceKey != "" || got.Status == nil || got.Status.Name != "To do" || got.Title != "Remote one" {
		t.Errorf("unlinked = %+v status %+v", got.Ticket, got.Status)
	}
	if res, _ := x.do(t, "GET", "/api/integrations/int-1", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("integration still there: %d", res.StatusCode)
	}
	if res, data := x.do(t, "DELETE", "/api/integrations/"+x.internal, nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("internal delete = %d %s", res.StatusCode, data)
	}
}
