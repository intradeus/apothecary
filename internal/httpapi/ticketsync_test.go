package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
	"apothecary/internal/project/tickets"
	"apothecary/internal/run/event"
)

// stubWriters serves the fake tracker as every remote source's writer.
type stubWriters struct{ src trackerport.TicketWriter }

func (s stubWriters) TicketWriter(context.Context, string) (trackerport.TicketWriter, error) {
	return s.src, nil
}

// trackerEnv is the test workshop reading a remote ticket source (int-1, a
// fake tracker holding PROJ-7, PROJ-8 and PROJ-9) through one Live cache.
type trackerEnv struct {
	*env
	src      *fake.TicketSource
	internal string // the user's internal tracker
}

func newTrackerEnv(t *testing.T) *trackerEnv {
	t.Helper()
	x := newEnv(t, engine.Config{})
	x.seedIntegration(t, "int-1", confstore.PortTicketSource)
	at := time.Now().Add(-time.Hour)
	src := &fake.TicketSource{Tickets: []trackerport.SourceTicket{
		{RemoteID: "100", Key: "PROJ-7", Title: "Remote one", Body: "b", URL: "https://tracker.test/PROJ-7",
			Status: "To Do", StatusID: "1", Type: "Bug", TypeID: "11", Updated: at,
			Attachments: []trackerport.SourceAttachment{{ID: "a1", Filename: "spec.md", Mime: "text/markdown", Size: 5}}},
		{RemoteID: "101", Key: "PROJ-8", Title: "Remote two", Status: "To Do", StatusID: "1", Type: "Story", TypeID: "10", Updated: at},
		{RemoteID: "102", Key: "PROJ-9", Title: "Remote three", Status: "To Do", StatusID: "1", Type: "Story", TypeID: "10", Updated: at},
	}}
	sources := stubSources{src: src}
	x.srv.Sources = sources
	x.srv.Trackers = stubWriters{src: src}
	x.srv.Live = tickets.NewLive(x.srv.Conf, sources)
	x.useSource(t, "int-1")
	in, err := x.srv.Conf.InternalTracker(context.Background(), testUser)
	if err != nil {
		t.Fatal(err)
	}
	return &trackerEnv{env: x, src: src, internal: in.ID}
}

// useSource points the test workshop at a ticket source. The seeded
// project has no repository URL, which the service's validation wants, so
// it is written to the store directly.
func (x *env) useSource(t *testing.T, sourceID string) {
	t.Helper()
	ctx := context.Background()
	p, err := x.srv.Conf.GetProject(ctx, testProject)
	if err != nil {
		t.Fatal(err)
	}
	p.TicketSourceID = sourceID
	if err := x.srv.Conf.DB().UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
}

// page lists the test workshop's tickets.
func (x *env) page(t *testing.T, query string) TicketPage {
	t.Helper()
	res, data := x.do(t, "GET", "/api/projects/"+testProject+"/tickets"+query, nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list%s = %d %s", query, res.StatusCode, data)
	}
	return decode[TicketPage](t, data)
}

// detail reads one ticket of the test workshop.
func (x *env) detail(t *testing.T, key string) TicketDetail {
	t.Helper()
	res, data := x.do(t, "GET", ticketPath(testProject, key), nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get %s = %d %s", key, res.StatusCode, data)
	}
	return decode[TicketDetail](t, data)
}

func keys(p TicketPage) []string {
	out := make([]string, len(p.Tickets))
	for i, r := range p.Tickets {
		out[i] = r.Key
	}
	return out
}

// The live list joins each issue to its record — its base branch and its
// active run, from any workshop — and pages with the tracker's cursor.
func TestLiveListJoinsRecordsAndPages(t *testing.T) {
	x := newTrackerEnv(t)
	if got := x.page(t, ""); !slices.Equal(keys(got), []string{"PROJ-7", "PROJ-8", "PROJ-9"}) || got.Next != "" {
		t.Fatalf("list = %+v", got)
	}
	for _, r := range x.page(t, "").Tickets {
		if r.TicketID != "" || r.ActiveRunID != "" {
			t.Errorf("%s has a record before anything needed one: %+v", r.Key, r)
		}
	}
	res, data := x.do(t, "PUT", ticketPath(testProject, "PROJ-7"), map[string]any{"base_branch": "fix/rounding"}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("base branch = %d %s", res.StatusCode, data)
	}
	rec := decode[TicketDetail](t, data).TicketID
	if _, err := x.store.Append(context.Background(), "r-live",
		&event.RunCreated{TicketTitle: "Remote one", FlowName: "loop", ProjectID: "elsewhere", UserID: testUser, TicketID: rec}); err != nil {
		t.Fatal(err)
	}

	first := x.page(t, "?limit=2")
	if !slices.Equal(keys(first), []string{"PROJ-7", "PROJ-8"}) || first.Next == "" {
		t.Fatalf("first page = %+v", first)
	}
	if r := first.Tickets[0]; r.TicketID != rec || r.BaseBranch != "fix/rounding" || r.ActiveRunID != "r-live" ||
		r.ChangeType != trackerport.ChangeFix || r.URL == "" || r.UpdatedAt == nil {
		t.Errorf("joined row = %+v", r)
	}
	if r := first.Tickets[1]; r.TicketID != "" || r.ActiveRunID != "" || r.ChangeType != trackerport.ChangeFeat {
		t.Errorf("row without a record = %+v", r)
	}
	second := x.page(t, "?limit=2&cursor="+first.Next)
	if !slices.Equal(keys(second), []string{"PROJ-9"}) || second.Next != "" {
		t.Errorf("second page = %+v", second)
	}
	if res, data := x.do(t, "GET", "/api/projects/"+testProject+"/tickets?done=maybe", nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad done = %d %s", res.StatusCode, data)
	}
}

// ?done filters on the tracker's done category; open is the default.
func TestLiveListDoneFilter(t *testing.T) {
	x := newTrackerEnv(t)
	x.src.Tickets[2].Status, x.src.Tickets[2].StatusID = "Done", "4"
	if got := keys(x.page(t, "")); !slices.Equal(got, []string{"PROJ-7", "PROJ-8"}) {
		t.Errorf("open = %v", got)
	}
	if got := x.page(t, "?done=done"); !slices.Equal(keys(got), []string{"PROJ-9"}) || !got.Tickets[0].Done {
		t.Errorf("done = %+v", got)
	}
	if got := keys(x.page(t, "?done=any")); len(got) != 3 {
		t.Errorf("any = %v", got)
	}
}

// A list page and a ticket are served from the cache; refresh=1 reads the
// tracker now.
func TestRefreshBypassesTheCache(t *testing.T) {
	x := newTrackerEnv(t)
	if got := x.page(t, ""); got.Tickets[0].Title != "Remote one" {
		t.Fatalf("list = %+v", got)
	}
	if got := x.detail(t, "PROJ-7"); got.Title != "Remote one" {
		t.Fatalf("ticket = %+v", got)
	}
	x.src.Tickets[0].Title = "Edited remotely"
	if got := x.page(t, ""); got.Tickets[0].Title != "Remote one" {
		t.Errorf("cached list = %q", got.Tickets[0].Title)
	}
	if got := x.page(t, "?refresh=1"); got.Tickets[0].Title != "Edited remotely" {
		t.Errorf("refreshed list = %q", got.Tickets[0].Title)
	}
	if got := x.detail(t, "PROJ-7"); got.Title != "Remote one" {
		t.Errorf("cached ticket = %q", got.Title)
	}
	res, data := x.do(t, "GET", ticketPath(testProject, "PROJ-7")+"?refresh=1", nil, nil)
	if got := decode[TicketDetail](t, data); res.StatusCode != http.StatusOK || got.Title != "Edited remotely" {
		t.Errorf("refreshed ticket = %d %s", res.StatusCode, data)
	}
}

// A base branch set on a remote issue makes its record, and only the base
// branch is taken; the issue page answers with it, and a read rewrites the
// record's last-seen columns.
func TestRemoteBaseBranchEditCreatesTheRecord(t *testing.T) {
	x := newTrackerEnv(t)
	ctx := context.Background()
	if _, found, _ := x.srv.Conf.TicketByRemote(ctx, "int-1", "100"); found {
		t.Fatal("a record before the edit")
	}
	res, data := x.do(t, "PUT", ticketPath(testProject, "PROJ-7"), map[string]any{"base_branch": "fix/rounding", "title": "Changed here"}, nil)
	got := decode[TicketDetail](t, data)
	if res.StatusCode != http.StatusOK || got.TicketID == "" || got.BaseBranch != "fix/rounding" || got.Title != "Remote one" || got.Internal {
		t.Fatalf("edit = %d %s", res.StatusCode, data)
	}
	rec, found, err := x.srv.Conf.TicketByRemote(ctx, "int-1", "100")
	if err != nil || !found || rec.ID != got.TicketID || rec.BaseBranch != "fix/rounding" || rec.SourceKey != "PROJ-7" {
		t.Fatalf("record = %+v %v %v", rec, found, err)
	}
	if res, data := x.do(t, "PUT", ticketPath(testProject, "PROJ-7"), map[string]any{"base_branch": "not a branch"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad branch = %d %s", res.StatusCode, data)
	}

	x.src.Tickets[0].Title = "Edited remotely"
	res, data = x.do(t, "GET", ticketPath(testProject, "PROJ-7")+"?refresh=1", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", res.StatusCode, data)
	}
	if rec, _, _ := x.srv.Conf.TicketByRemote(ctx, "int-1", "100"); rec.Title != "Edited remotely" {
		t.Errorf("last seen = %q", rec.Title)
	}
	if d := decode[TicketDetail](t, data); d.Source.ID != "int-1" || len(d.AttachmentIDs) != 0 || d.Links == nil {
		t.Errorf("detail = %s", data)
	}

	// The record by id names the workshops on its source.
	res, data = x.do(t, "GET", "/api/tickets/"+got.TicketID, nil, nil)
	if r := decode[TicketRecord](t, data); res.StatusCode != http.StatusOK || r.Key != "PROJ-7" || r.Source == nil || r.Source.ID != "int-1" ||
		len(r.Workshops) != 1 || r.Workshops[0].ID != testProject {
		t.Errorf("record = %d %s", res.StatusCode, data)
	}
}

// A run started on a remote issue makes its record first; the record's
// files are its own, the issue's are listed beside them.
func TestRunOnARemoteIssueCreatesTheRecord(t *testing.T) {
	x := newTrackerEnv(t)
	res, data := x.do(t, "GET", ticketPath(testProject, "PROJ-7")+"/attachments", nil, nil)
	if got := decode[TicketAttachments](t, data); res.StatusCode != http.StatusOK || len(got.Attachments) != 0 ||
		len(got.Remote) != 1 || got.Remote[0].Filename != "spec.md" {
		t.Errorf("attachments = %d %s", res.StatusCode, data)
	}
	resp, data := x.startRun(t, "PROJ-7", "loop")
	run := startedRun(t, resp, data)
	x.e.Wait(run)
	rec, found, err := x.srv.Conf.TicketByRemote(context.Background(), "int-1", "100")
	if err != nil || !found || rec.Title != "Remote one" || rec.SourceKind != "fake" {
		t.Fatalf("record = %+v %v %v", rec, found, err)
	}
	if got := x.detail(t, "PROJ-7"); got.TicketID != rec.ID {
		t.Errorf("detail = %+v", got)
	}
	if res, data := x.do(t, "POST", ticketPath(testProject, "NOPE-1")+"/runs", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("a run on an issue the tracker does not have = %d %s", res.StatusCode, data)
	}
}

// A workshop reading a remote tracker creates and deletes nothing.
func TestRemoteSourceRefusesCreateAndDelete(t *testing.T) {
	x := newTrackerEnv(t)
	if res, data := x.do(t, "POST", "/api/projects/"+testProject+"/tickets", map[string]any{"title": "t"}, nil); res.StatusCode != http.StatusBadRequest ||
		!strings.Contains(string(data), "int-1") {
		t.Errorf("create = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "DELETE", ticketPath(testProject, "PROJ-7"), nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("delete = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "GET", ticketPath(testProject, "NOPE-1"), nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("an issue the tracker does not have = %d %s", res.StatusCode, data)
	}
}

// A tracker problem never stops a run on an issue Apothecary already has a
// record of: with the tracker down and nothing cached (a restart), the run
// starts from the record found by the key it was last seen under. An issue
// with no record still needs the tracker.
func TestRunStartsFromTheRecordWhileTheTrackerIsDown(t *testing.T) {
	x := newTrackerEnv(t)
	res, data := x.do(t, "PUT", ticketPath(testProject, "PROJ-7"), map[string]any{"base_branch": "fix/rounding"}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("base branch = %d %s", res.StatusCode, data)
	}
	rec := decode[TicketDetail](t, data).TicketID
	x.srv.Live = tickets.NewLive(x.srv.Conf, stubSources{src: x.src})
	x.src.Err = errors.New("tracker down")
	resp, data := x.startRun(t, "PROJ-7", "loop")
	run := startedRun(t, resp, data)
	x.e.Wait(run)
	_, data = x.do(t, "GET", "/api/runs?ticket="+rec, nil, nil)
	if !strings.Contains(string(data), run) {
		t.Errorf("the run is not the record's: %s", data)
	}
	if res, data := x.do(t, "POST", ticketPath(testProject, "PROJ-8")+"/runs", nil, nil); res.StatusCode != http.StatusBadGateway {
		t.Errorf("a run on an issue with no record = %d %s", res.StatusCode, data)
	}
}

// Two workshops on one source share a remote issue: one record, whichever
// workshop made it, and one lock — a run going in one workshop refuses a
// run in the other.
func TestARemoteIssueIsOneRecordAndOneLockAcrossWorkshops(t *testing.T) {
	x := newTrackerEnv(t)
	const other = "prj-other"
	conftest.SeedProject(t, x.srv.Conf, confstore.Project{ID: other, UserID: testUser, Name: "other", TicketSourceID: "int-1"})
	res, data := x.do(t, "PUT", ticketPath(testProject, "PROJ-7"), map[string]any{"base_branch": "fix/rounding"}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("base branch = %d %s", res.StatusCode, data)
	}
	rec := decode[TicketDetail](t, data).TicketID
	res, data = x.do(t, "GET", ticketPath(other, "PROJ-7"), nil, nil)
	if got := decode[TicketDetail](t, data); res.StatusCode != http.StatusOK || got.TicketID != rec || got.BaseBranch != "fix/rounding" {
		t.Fatalf("the other workshop's detail = %d %s, want record %s", res.StatusCode, data, rec)
	}
	if _, err := x.store.Append(context.Background(), "r-live",
		&event.RunCreated{TicketTitle: "Remote one", FlowName: "loop", ProjectID: other, UserID: testUser, TicketID: rec}); err != nil {
		t.Fatal(err)
	}
	resp, data := x.startRun(t, "PROJ-7", "loop")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start while the other workshop runs = %d: %s", resp.StatusCode, data)
	}
	if got := decode[ErrorResponse](t, data); got.RunID != "r-live" || !strings.Contains(got.Error, "run r-live is still active") {
		t.Errorf("409 body = %s", data)
	}
	got, found, err := x.srv.Conf.TicketByRemote(context.Background(), "int-1", "100")
	if err != nil || !found || got.ID != rec {
		t.Errorf("record = %+v %v %v, want the one record %s", got, found, err, rec)
	}
}

// A write's answer rereads the issue, and its record takes what it shows.
func TestRemoteWriteRefreshesTheRecord(t *testing.T) {
	x := newTrackerEnv(t)
	if res, data := x.do(t, "PUT", ticketPath(testProject, "PROJ-7"), map[string]any{"base_branch": "fix/rounding"}, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("base branch = %d %s", res.StatusCode, data)
	}
	res, data := x.do(t, "PUT", ticketPath(testProject, "PROJ-7")+"/labels", TicketLabelsRequest{Labels: []string{"a", "b"}}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("labels = %d %s", res.StatusCode, data)
	}
	if rec, _, _ := x.srv.Conf.TicketByRemote(context.Background(), "int-1", "100"); !slices.Equal(rec.Labels, []string{"a", "b"}) {
		t.Errorf("record labels = %v", rec.Labels)
	}
}

// A tracker holding off for its rate limit, with nothing cached, is 503
// with Retry-After — on the first refusal and while the hold lasts.
func TestRateLimitWithNothingCachedIs503(t *testing.T) {
	x := newTrackerEnv(t)
	x.src.Err = &trackerport.ErrRateLimited{Tracker: "fake", RetryAfter: 30 * time.Second}
	for _, q := range []string{"", "?q=other"} {
		res, data := x.do(t, "GET", "/api/projects/"+testProject+"/tickets"+q, nil, nil)
		secs, err := strconv.Atoi(res.Header.Get("Retry-After"))
		if res.StatusCode != http.StatusServiceUnavailable || err != nil || secs < 1 || secs > 30 {
			t.Errorf("list%s = %d (Retry-After %q) %s", q, res.StatusCode, res.Header.Get("Retry-After"), data)
		}
	}
	res, _ := x.do(t, "GET", ticketPath(testProject, "PROJ-7"), nil, nil)
	if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") == "" {
		t.Errorf("ticket = %d", res.StatusCode)
	}
}

// Status (by name), labels and comments go through the workshop's tracker:
// the remote issue, whose copies the write drops.
func TestRemoteWritesGoThroughTheTracker(t *testing.T) {
	x := newTrackerEnv(t)
	if got := x.detail(t, "PROJ-7"); got.Status != "To Do" {
		t.Fatalf("before = %+v", got)
	}
	res, data := x.do(t, "POST", ticketPath(testProject, "PROJ-7")+"/status", TicketStatusRequest{Status: "In Progress"}, nil)
	if got := decode[TicketDetail](t, data); res.StatusCode != http.StatusOK || got.Status != "In Progress" || got.StatusID != "2" {
		t.Errorf("status = %d %s", res.StatusCode, data)
	}
	if !slices.Contains(x.src.Calls, "transition PROJ-7 In Progress") {
		t.Errorf("tracker calls = %v", x.src.Calls)
	}
	res, data = x.do(t, "PUT", ticketPath(testProject, "PROJ-7")+"/labels", TicketLabelsRequest{Labels: []string{"a", " b "}}, nil)
	if got := decode[TicketDetail](t, data); res.StatusCode != http.StatusOK || !slices.Equal(got.Labels, []string{"a", "b"}) {
		t.Errorf("labels = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "POST", ticketPath(testProject, "PROJ-7")+"/comments", TicketCommentRequest{Body: "hello"}, nil)
	comments := decode[[]TicketComment](t, data)
	if res.StatusCode != http.StatusCreated || len(comments) != 1 || comments[0].Body != "hello" || comments[0].Origin != "" {
		t.Errorf("comment = %d %s", res.StatusCode, data)
	}
	if _, found, _ := x.srv.Conf.TicketByRemote(context.Background(), "int-1", "100"); found {
		t.Error("a write made a record")
	}

	x.src.Reachable = map[string][]string{"In Progress": {"Done"}}
	res, data = x.do(t, "POST", ticketPath(testProject, "PROJ-7")+"/status", TicketStatusRequest{Status: "To Do"}, nil)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "Done") {
		t.Errorf("unreachable status = %d %s", res.StatusCode, data)
	}
	x.src.WriteErr = trackerport.ErrTicketNotFound
	if res, data := x.do(t, "POST", ticketPath(testProject, "PROJ-8")+"/comments", TicketCommentRequest{Body: "x"}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("a write to an issue gone = %d %s", res.StatusCode, data)
	}
}

// The internal tracker takes the same writes, by the same names, and its
// tickets are created, edited and deleted here.
func TestInternalTicketsAndWrites(t *testing.T) {
	x := newEnv(t, engine.Config{})
	key := x.newTicket(t, "Local one", "")
	res, data := x.do(t, "POST", ticketPath(testProject, key)+"/status", TicketStatusRequest{Status: "In progress"}, nil)
	if got := decode[TicketDetail](t, data); res.StatusCode != http.StatusOK || got.Status != "In progress" || !got.Internal || got.Source.Kind != confstore.KindInternal {
		t.Errorf("status = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "POST", ticketPath(testProject, key)+"/status", TicketStatusRequest{Status: "Nowhere"}, nil); res.StatusCode != http.StatusBadRequest ||
		!strings.Contains(string(data), "In progress") {
		t.Errorf("an unknown status = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "PUT", ticketPath(testProject, key)+"/labels", TicketLabelsRequest{Labels: []string{"x"}}, nil)
	if got := decode[TicketDetail](t, data); res.StatusCode != http.StatusOK || !slices.Equal(got.Labels, []string{"x"}) {
		t.Errorf("labels = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "POST", ticketPath(testProject, key)+"/comments", TicketCommentRequest{Body: "note"}, nil)
	if got := decode[[]TicketComment](t, data); res.StatusCode != http.StatusCreated || len(got) != 1 || got[0].Origin != confstore.CommentUser {
		t.Errorf("comment = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "GET", ticketPath(testProject, key)+"/comments", nil, nil); res.StatusCode != http.StatusOK || len(decode[[]TicketComment](t, data)) != 1 {
		t.Errorf("comments = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "PUT", ticketPath(testProject, key), map[string]any{"title": "Renamed", "goal": "g2", "base_branch": "fix/x", "change_type": "fix"}, nil)
	if got := decode[TicketDetail](t, data); res.StatusCode != http.StatusOK || got.Title != "Renamed" || got.Body != "g2" ||
		got.BaseBranch != "fix/x" || got.ChangeType != trackerport.ChangeFix {
		t.Errorf("edit = %d %s", res.StatusCode, data)
	}
	if got := x.page(t, ""); len(got.Tickets) != 1 || got.Tickets[0].TicketID != key || got.Tickets[0].Status != "In progress" {
		t.Errorf("list = %+v", got)
	}

	// Refusals.
	for _, tc := range []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"an empty status", "POST", ticketPath(testProject, key) + "/status", TicketStatusRequest{}, http.StatusBadRequest},
		{"an empty comment", "POST", ticketPath(testProject, key) + "/comments", TicketCommentRequest{Body: "  "}, http.StatusBadRequest},
		{"a label with a comma", "PUT", ticketPath(testProject, key) + "/labels", TicketLabelsRequest{Labels: []string{"a,b"}}, http.StatusBadRequest},
		{"a missing ticket", "POST", ticketPath(testProject, "nope") + "/comments", TicketCommentRequest{Body: "x"}, http.StatusNotFound},
		{"a bad base branch", "POST", "/api/projects/" + testProject + "/tickets", map[string]any{"title": "t", "base_branch": "a..b"}, http.StatusBadRequest},
	} {
		if res, data := x.do(t, tc.method, tc.path, tc.body, nil); res.StatusCode != tc.want {
			t.Errorf("%s = %d %s, want %d", tc.name, res.StatusCode, data, tc.want)
		}
	}

	if res, data := x.do(t, "DELETE", ticketPath(testProject, key), nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d %s", res.StatusCode, data)
	}
	if res, _ := x.do(t, "GET", ticketPath(testProject, key), nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("deleted ticket = %d", res.StatusCode)
	}
}

// The routes of the mirror are gone.
func TestRemovedTicketRoutes(t *testing.T) {
	x := newTrackerEnv(t)
	key := "anything"
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/tickets"},
		{"POST", "/api/tickets"},
		{"PUT", "/api/tickets/" + key},
		{"DELETE", "/api/tickets/" + key},
		{"POST", "/api/tickets/" + key + "/sync"},
		{"POST", "/api/tickets/" + key + "/runs"},
		{"POST", "/api/tickets/" + key + "/status"},
		{"GET", "/api/tickets/" + key + "/comments"},
		{"POST", "/api/integrations/int-1/sync"},
		{"GET", "/api/integrations/int-1/tickets"},
		{"GET", "/api/integrations/int-1/tickets/PROJ-7"},
	} {
		if res, data := x.do(t, rt.method, rt.path, nil, nil); res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d %s", rt.method, rt.path, res.StatusCode, data)
		}
	}
}

// A remote tracker's statuses are read live and read-only; the internal
// tracker's are edited, and validated on save.
func TestTrackerStatusesAndTypes(t *testing.T) {
	x := newTrackerEnv(t)
	res, data := x.do(t, "GET", "/api/integrations/int-1/statuses?refresh=1", nil, nil)
	remote := decode[TrackerStatusList](t, data)
	if res.StatusCode != http.StatusOK || !remote.ReadOnly || len(remote.Statuses) != len(fake.DefaultStatuses) ||
		remote.Statuses[0].Name != "To Do" || remote.Statuses[0].IsDone || !remote.Statuses[3].IsDone {
		t.Fatalf("remote statuses = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "PUT", "/api/integrations/int-1/statuses", TrackerStatusesUpdate{Statuses: remote.Statuses}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a remote save = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "GET", "/api/integrations/int-1/types", nil, nil)
	if got := decode[TrackerTypeList](t, data).Types; res.StatusCode != http.StatusOK || len(got) != len(fake.DefaultTypes) {
		t.Errorf("remote types = %d %s", res.StatusCode, data)
	}

	res, data = x.do(t, "GET", "/api/integrations/"+x.internal+"/statuses", nil, nil)
	internal := decode[TrackerStatusList](t, data)
	if res.StatusCode != http.StatusOK || internal.ReadOnly || len(internal.Statuses) == 0 {
		t.Fatalf("internal statuses = %d %s", res.StatusCode, data)
	}
	list := internal.Statuses
	in, err := x.srv.Conf.CreateTicket(context.Background(), confstore.Ticket{UserID: testUser, Title: "Local one"})
	if err != nil {
		t.Fatal(err)
	}
	var without []confstore.TrackerStatus
	for _, st := range list {
		if st.ID != in.StatusID {
			without = append(without, st)
		}
	}
	if res, data := x.do(t, "PUT", "/api/integrations/"+x.internal+"/statuses", TrackerStatusesUpdate{Statuses: without}, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("a status in use deleted = %d %s", res.StatusCode, data)
	}
	without = append(without, confstore.TrackerStatus{Name: "Triage"})
	res, data = x.do(t, "PUT", "/api/integrations/"+x.internal+"/statuses",
		TrackerStatusesUpdate{Statuses: without, ReplaceWith: map[string]string{in.StatusID: without[0].ID}}, nil)
	if got := decode[TrackerStatusList](t, data); res.StatusCode != http.StatusOK || got.ReadOnly || len(got.Statuses) != len(list) || got.Statuses[len(list)-1].Name != "Triage" {
		t.Errorf("replaced = %d %s", res.StatusCode, data)
	}
	if got, _ := x.srv.Conf.GetTicket(context.Background(), in.ID); got.StatusID != without[0].ID {
		t.Errorf("the ticket is in %s, want %s", got.StatusID, without[0].ID)
	}

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

// A workshop's ticket source round-trips on the project, and an integration
// a workshop reads its tickets from is not deleted.
func TestProjectTicketSource(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)
	res, data := x.do(t, "GET", "/api/projects/"+projectID, nil, nil)
	full := decode[map[string]any](t, data)
	internal, _ := full["ticket_source_id"].(string)
	if res.StatusCode != http.StatusOK || internal == "" {
		t.Fatalf("project = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "POST", "/api/integrations", map[string]any{"port": confstore.PortTicketSource, "kind": "fake", "name": "tracker"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create tracker = %d %s", res.StatusCode, data)
	}
	tracker := decode[integrationJSON](t, data).ID

	full["ticket_source_id"] = tracker
	res, data = x.do(t, "PUT", "/api/projects/"+projectID, full, nil)
	if got := decode[confstore.Project](t, data); res.StatusCode != http.StatusOK || got.TicketSourceID != tracker {
		t.Fatalf("set the source = %d %s", res.StatusCode, data)
	}
	if _, data := x.do(t, "GET", "/api/projects/"+projectID, nil, nil); decode[confstore.Project](t, data).TicketSourceID != tracker {
		t.Errorf("read back = %s", data)
	}
	full["ticket_source_id"] = full["repo_account_id"]
	if res, data := x.do(t, "PUT", "/api/projects/"+projectID, full, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a repository as ticket source = %d %s", res.StatusCode, data)
	}

	res, data = x.do(t, "DELETE", "/api/integrations/"+tracker, nil, nil)
	if res.StatusCode != http.StatusConflict || !strings.Contains(string(data), "web") {
		t.Errorf("delete a source in use = %d %s", res.StatusCode, data)
	}
	full["ticket_source_id"] = internal
	if res, data := x.do(t, "PUT", "/api/projects/"+projectID, full, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("back to the internal tracker = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "DELETE", "/api/integrations/"+tracker, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete a source no workshop uses = %d %s", res.StatusCode, data)
	}
	if res, data := x.do(t, "DELETE", "/api/integrations/"+internal, nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("delete the internal tracker = %d %s", res.StatusCode, data)
	}
}
