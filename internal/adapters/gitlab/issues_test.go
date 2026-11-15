package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"apothecary/internal/ports/trackerport"
)

const uploadSecret = "0123456789abcdef0123456789abcdef"

const glIssueBody = `{"id":905,"iid":5,"project_id":42,"title":"Flaky router test",
	"description":"It fails **sometimes**.\n\n![trace](/uploads/` + uploadSecret + `/trace%20one.png) and [again](/uploads/` + uploadSecret + `/trace%20one.png)",
	"web_url":"https://gitlab.example.com/lu/greeter/-/issues/5","state":"opened","issue_type":"issue",
	"updated_at":"2026-07-30T11:22:33.000Z","references":{"full":"lu/greeter#5"},
	"assignee":{"username":"lu"},"assignees":[{"username":"lu"}],"labels":["bug"],"milestone":{"id":2,"title":"v1"}}`

func glIssuesStub(t *testing.T) (*httptest.Server, map[string][]map[string]any, *[]url.Values) {
	t.Helper()
	writes := map[string][]map[string]any{}
	var queries []url.Values
	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request, status int, resp string) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		k := r.Method + " " + r.PathValue("project") + " " + r.PathValue("rest")
		writes[k] = append(writes[k], body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"username":"lu"}`))
	})
	list := func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		_, _ = w.Write([]byte(`[` + glIssueBody + `]`))
	}
	mux.HandleFunc("GET /api/v4/projects/{project}/issues", list)
	mux.HandleFunc("GET /api/v4/groups/{group}/issues", list)
	mux.HandleFunc("GET /api/v4/issues", list)
	mux.HandleFunc("GET /api/v4/projects/{project}/issues/{iid}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("project") != "lu/greeter" || r.PathValue("iid") != "5" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(glIssueBody))
	})
	mux.HandleFunc("PUT /api/v4/projects/{project}/issues/{rest}", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, 200, glIssueBody)
	})
	mux.HandleFunc("POST /api/v4/projects/{project}/issues/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, 201, `{"id":1}`)
	})
	mux.HandleFunc("GET /api/v4/projects/{project}/issues/5/notes", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"body":"first"},{"body":"closed","system":true},
			{"body":"Merge request: feat/x: https://gitlab.example.com/lu/greeter/-/merge_requests/7\n\n<!-- apothecary link=https://gitlab.example.com/lu/greeter/-/merge_requests/7 -->"}]`))
	})
	mux.HandleFunc("GET /api/v4/projects/{project}/milestones", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":2,"title":"v1"},{"id":3,"title":"v2"}]`))
	})
	mux.HandleFunc("GET /api/v4/users", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("username") == "lu" {
			_, _ = w.Write([]byte(`[{"id":11}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /api/v4/projects/42/uploads", func(w http.ResponseWriter, _ *http.Request) {
		writes["POST 42 uploads"] = append(writes["POST 42 uploads"], nil)
		_, _ = w.Write([]byte(`{"url":"/uploads/` + uploadSecret + `/home.png","markdown":"![home](/uploads/` + uploadSecret + `/home.png)"}`))
	})
	mux.HandleFunc("GET /api/v4/projects/42/uploads/{secret}/{file}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" || r.PathValue("file") != "trace one.png" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte("png-bytes"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, writes, &queries
}

func TestGitLabIssuesSearchAndFetch(t *testing.T) {
	srv, _, queries := glIssuesStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok", Projects: []string{"lu/greeter"}, Groups: []string{"lu"}})
	ctx := context.Background()

	if err := s.Test(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(ctx, trackerport.SearchQuery{Text: "router ~bug is:open assignee:@me", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// The same issue found through the project and its group is listed once.
	if len(got.Tickets) != 1 || got.Tickets[0].Key != "lu/greeter#5" || got.Tickets[0].Assignee != "lu" || got.Tickets[0].Status != "opened" || got.Tickets[0].Done {
		t.Errorf("search = %+v", got)
	}
	if len(*queries) != 2 {
		t.Fatalf("want one call per scope, got %d", len(*queries))
	}
	q := (*queries)[0]
	if q.Get("search") != "router" || q.Get("labels") != "bug" || q.Get("state") != "opened" || q.Get("scope") != "assigned_to_me" {
		t.Errorf("query = %v", q)
	}
	if _, err := s.Search(ctx, trackerport.SearchQuery{Done: trackerport.DoneOpen, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if q := (*queries)[2]; q.Get("scope") != "assigned_to_me" || q.Get("state") != "opened" || q.Has("search") {
		t.Errorf("empty query = %v", q)
	}
	// A key looks the issue up directly; a bare number uses the one project.
	byKey, err := s.Search(ctx, trackerport.SearchQuery{Text: "#5", Limit: 10})
	if err != nil || len(byKey.Tickets) != 1 || byKey.Tickets[0].Key != "lu/greeter#5" {
		t.Errorf("search by key = %+v, %v", byKey, err)
	}
	// ...and the done filter holds for it: issue 5 is open.
	if closed, err := s.Search(ctx, trackerport.SearchQuery{Text: "#5", Done: trackerport.DoneOnly}); err != nil || len(closed.Tickets) != 0 {
		t.Errorf("closed by key = %+v, %v", closed, err)
	}
	if len(*queries) != 4 {
		t.Errorf("a key search still hit the list endpoint: %v", *queries)
	}
	// No scope configured: the token's own issues list.
	if _, err := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"}).Search(ctx, trackerport.SearchQuery{Text: "x", Limit: 10}); err != nil {
		t.Fatal(err)
	}

	tk, err := s.Fetch(ctx, "lu/greeter#5")
	if err != nil {
		t.Fatal(err)
	}
	if tk.URL != "https://gitlab.example.com/lu/greeter/-/issues/5" || tk.Updated.IsZero() || tk.Type != "Issue" || tk.TypeID != "issue" {
		t.Errorf("fetch = %+v", tk)
	}
	if tk.RemoteID != "905" || tk.StatusID != "opened" || strings.Join(tk.Labels, ",") != "bug" {
		t.Errorf("ids and labels = %q %q %q", tk.RemoteID, tk.StatusID, tk.Labels)
	}
	if _, err := s.Fetch(ctx, "lu/greeter#404"); !errors.Is(err, trackerport.ErrTicketNotFound) {
		t.Errorf("a missing issue = %v, want ErrTicketNotFound", err)
	}
	// Upload links become absolute, and each file is listed once.
	if !strings.Contains(tk.Body, "]("+srv.URL+"/-/project/42/uploads/"+uploadSecret+"/trace%20one.png)") {
		t.Errorf("body = %q", tk.Body)
	}
	if len(tk.Attachments) != 1 || tk.Attachments[0].Filename != "trace one.png" {
		t.Fatalf("attachments = %+v", tk.Attachments)
	}
	rc, err := s.OpenAttachment(ctx, tk.Attachments[0])
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "png-bytes" {
		t.Errorf("attachment = %q", data)
	}
	if _, err := s.OpenAttachment(ctx, trackerport.SourceAttachment{Filename: "x", URL: "https://evil.example/x"}); err == nil {
		t.Error("an attachment on another host was opened with the token")
	}
	if _, err := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"}).Fetch(ctx, "#5"); err == nil {
		t.Error("a bare number with no configured project was accepted")
	}
}

func TestGitLabIssuesTransition(t *testing.T) {
	srv, writes, _ := glIssuesStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"})
	ctx := context.Background()
	if err := s.Transition(ctx, "lu/greeter#5", "closed"); err != nil {
		t.Fatal(err)
	}
	puts := writes["PUT lu/greeter 5"]
	if len(puts) != 1 || puts[0]["state_event"] != "close" {
		t.Errorf("puts = %+v", puts)
	}
	// The stub's issue reads opened, so "opened" (and "open") are already there.
	if err := s.Transition(ctx, "lu/greeter#5", "opened"); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(ctx, "lu/greeter#5", "Open"); err != nil {
		t.Fatal(err)
	}
	if len(writes["PUT lu/greeter 5"]) != 1 {
		t.Error("an already-opened issue was updated")
	}
	var unreachable *trackerport.ErrStatusUnreachable
	if err := s.Transition(ctx, "lu/greeter#5", "In Progress"); !errors.As(err, &unreachable) || strings.Join(unreachable.Reachable, ",") != "opened,closed" {
		t.Errorf("err = %v", err)
	}
}

func TestGitLabIssuesFieldsAndLink(t *testing.T) {
	srv, writes, _ := glIssuesStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"})
	ctx := context.Background()
	if err := s.SetFields(ctx, "lu/greeter#5", map[string]string{"labels": "bug, needs-review", "milestone": "V2", "assignees": "@lu"}); err != nil {
		t.Fatal(err)
	}
	p := writes["PUT lu/greeter 5"][0]
	if p["labels"] != "bug,needs-review" || p["milestone_id"] != float64(3) {
		t.Errorf("update = %v", p)
	}
	if ids, _ := p["assignee_ids"].([]any); len(ids) != 1 || ids[0] != float64(11) {
		t.Errorf("assignee_ids = %v", p["assignee_ids"])
	}
	var unsupported *trackerport.ErrFieldUnsupported
	if err := s.SetFields(ctx, "lu/greeter#5", map[string]string{"Feature flags": "x"}); !errors.As(err, &unsupported) || len(unsupported.Supported) != 3 {
		t.Errorf("custom field: err = %v", err)
	}
	var unknown *trackerport.ErrFieldUnknown
	if err := s.SetFields(ctx, "lu/greeter#5", map[string]string{"milestone": "v9"}); !errors.As(err, &unknown) {
		t.Errorf("unknown milestone: err = %v", err)
	}
	if err := s.SetFields(ctx, "lu/greeter#5", map[string]string{"assignees": "nobody"}); !errors.As(err, &unknown) {
		t.Errorf("unknown user: err = %v", err)
	}
	cur, err := s.Fields(ctx, "lu/greeter#5", []string{"labels", "milestone", "assignees"})
	if err != nil {
		t.Fatal(err)
	}
	if cur["labels"] != "bug" || cur["milestone"] != "v1" || cur["assignees"] != "lu" {
		t.Errorf("fields = %v", cur)
	}

	comments, err := s.ListComments(ctx, "lu/greeter#5")
	if err != nil || len(comments) != 2 || comments[0].Body != "first" {
		t.Errorf("comments = %+v (system notes must be dropped), %v", comments, err)
	}
	// The link already posted (its marker is in the notes) is not posted again.
	mr7 := "https://gitlab.example.com/lu/greeter/-/merge_requests/7"
	if err := s.AddLink(ctx, "lu/greeter#5", trackerport.TicketLink{Title: "Merge request: feat/x", URL: mr7}); err != nil {
		t.Fatal(err)
	}
	if len(writes["POST lu/greeter 5/notes"]) != 0 {
		t.Error("an already-linked merge request was posted again")
	}
	mr8 := "https://gitlab.example.com/lu/greeter/-/merge_requests/8"
	if err := s.AddLink(ctx, "lu/greeter#5", trackerport.TicketLink{Title: "Merge request: feat/y", URL: mr8}); err != nil {
		t.Fatal(err)
	}
	posts := writes["POST lu/greeter 5/notes"]
	if len(posts) != 1 || !strings.Contains(posts[0]["body"].(string), linkMarker(mr8)) {
		t.Errorf("link note = %+v", posts)
	}
}

// A note's attachment goes up to the issue's own project.
func TestGitLabIssuesAttachUploadsToTheIssueProject(t *testing.T) {
	srv, writes, _ := glIssuesStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"})
	att, err := s.Attach(context.Background(), "lu/greeter#5", "home.png", "image/png", []byte("x"))
	if err != nil || len(writes["POST 42 uploads"]) != 1 || att.URL != srv.URL+"/-/project/42/uploads/"+uploadSecret+"/home.png" {
		t.Fatalf("att %+v err %v", att, err)
	}
}

// An issue's status list is opened/closed; its types are the work item
// types the issues API serves, a type GitLab added since read as itself.
func TestGitLabIssuesMetadata(t *testing.T) {
	s := NewIssues(IssuesConfig{BaseURL: "https://gitlab.example.com", Token: "tok"})
	ctx := context.Background()
	statuses, err := s.Statuses(ctx)
	if err != nil || len(statuses) != 2 || statuses[0] != (trackerport.RemoteStatus{ID: "opened", Name: "Open", Category: trackerport.CategoryNew}) ||
		statuses[1] != (trackerport.RemoteStatus{ID: "closed", Name: "Closed", Category: trackerport.CategoryDone}) {
		t.Errorf("statuses = %+v, %v", statuses, err)
	}
	types, err := s.Types(ctx)
	if err != nil || len(types) != 4 || types[0] != (trackerport.RemoteType{ID: "issue", Name: "Issue"}) || types[1].ID != "incident" {
		t.Errorf("types = %+v, %v", types, err)
	}
	types[0].Name = "changed"
	if again, _ := s.Types(ctx); again[0].Name != "Issue" {
		t.Error("Types shares storage")
	}
	if got := issueTypeOf("key_result"); got != (trackerport.RemoteType{ID: "key_result", Name: "key_result"}) {
		t.Errorf("unknown type = %+v", got)
	}
}

// Search pages by page number while a scope still serves full pages; the
// done filter is the state, overriding one typed in the search box, and a
// closed issue is Done.
func TestGitLabIssuesSearchPagesAndFiltersDone(t *testing.T) {
	var queries []url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{project}/issues", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id":13,"iid":3,"state":"closed","references":{"full":"lu/greeter#3"}}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":11,"iid":1,"state":"closed","references":{"full":"lu/greeter#1"}},{"id":12,"iid":2,"state":"closed","references":{"full":"lu/greeter#2"}}]`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s := NewIssues(IssuesConfig{BaseURL: ts.URL, Token: "tok", Projects: []string{"lu/greeter"}})
	ctx := context.Background()

	q := trackerport.SearchQuery{Text: "router is:open", Done: trackerport.DoneOnly, Limit: 2}
	first, err := s.Search(ctx, q)
	if err != nil || len(first.Tickets) != 2 || !first.Tickets[0].Done || first.Next != "2" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	q.Cursor = first.Next
	second, err := s.Search(ctx, q)
	if err != nil || len(second.Tickets) != 1 || second.Tickets[0].Key != "lu/greeter#3" || second.Next != "" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	for i, v := range queries {
		if v.Get("state") != "closed" || v.Get("per_page") != "2" || v.Get("page") != strconv.Itoa(i+1) {
			t.Errorf("query %d = %v", i, v)
		}
	}
	if _, err := s.Search(ctx, trackerport.SearchQuery{Cursor: "-1"}); err == nil {
		t.Error("a cursor that is no page number was accepted")
	}
}
