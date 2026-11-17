package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports/trackerport"
)

const issueJSONBody = `{"id":1005,"number":5,"title":"Flaky router test","body":"It fails **sometimes**.","html_url":"https://github.com/lu/greeter/issues/5",
	"state":"open","updated_at":"2026-07-30T11:22:33Z","repository_url":"https://api.github.com/repos/lu/greeter",
	"assignee":{"login":"lu"},"assignees":[{"login":"lu"}],"labels":[{"name":"bug"}],"milestone":{"title":"v1","number":2}}`

func issuesStub(t *testing.T) (*httptest.Server, map[string][]map[string]any, *[]string) {
	t.Helper()
	writes := map[string][]map[string]any{}
	var queries []string
	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request, status int, resp string) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		writes[r.Method+" "+r.URL.Path] = append(writes[r.Method+" "+r.URL.Path], body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"login":"lu"}`))
	})
	mux.HandleFunc("GET /search/issues", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("q"))
		_, _ = w.Write([]byte(`{"items":[` + issueJSONBody + `,{"number":6,"title":"a PR","pull_request":{"url":"x"},"repository_url":"https://api.github.com/repos/lu/greeter"}]}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/issues/5", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(issueJSONBody))
	})
	mux.HandleFunc("GET /repos/lu/greeter/issues/6", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"number":6,"title":"a PR","pull_request":{"url":"x"}}`))
	})
	mux.HandleFunc("PATCH /repos/lu/greeter/issues/5", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, 200, issueJSONBody)
	})
	mux.HandleFunc("POST /repos/lu/greeter/issues/5/comments", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, 201, `{"id":1}`)
	})
	mux.HandleFunc("GET /repos/lu/greeter/issues/5/comments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":11,"user":{"login":"ann"},"body":"first","created_at":"2026-07-30T10:00:00Z","updated_at":"2026-07-30T11:00:00Z"},{"id":12,"body":"Merge request: feat/x: https://github.com/lu/greeter/pull/7\n\n<!-- apothecary link=https://github.com/lu/greeter/pull/7 -->","created_at":"2026-07-30T12:00:00Z"}]`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/milestones", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"number":2,"title":"v1"},{"number":3,"title":"v2"}]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, writes, &queries
}

func TestIssuesSearchAndFetch(t *testing.T) {
	srv, _, queries := issuesStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok", Repos: []string{"lu/greeter"}})
	ctx := context.Background()

	got, err := s.Search(ctx, trackerport.SearchQuery{Text: "router", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tickets) != 1 || got.Tickets[0].Key != "lu/greeter#5" || got.Tickets[0].Assignee != "lu" || got.Tickets[0].Status != "open" || got.Tickets[0].Done {
		t.Errorf("search = %+v (the pull request must be dropped)", got)
	}
	if (*queries)[0] != "is:issue repo:lu/greeter router" {
		t.Errorf("query = %q", (*queries)[0])
	}
	if _, err := s.Search(ctx, trackerport.SearchQuery{Done: trackerport.DoneOpen, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if (*queries)[1] != "is:issue repo:lu/greeter state:open assignee:@me" {
		t.Errorf("empty query = %q", (*queries)[1])
	}
	// A key looks the issue up directly; a bare number uses the one repo.
	byKey, err := s.Search(ctx, trackerport.SearchQuery{Text: "#5", Limit: 10})
	if err != nil || len(byKey.Tickets) != 1 || byKey.Tickets[0].Key != "lu/greeter#5" {
		t.Errorf("search by key = %+v, %v", byKey, err)
	}
	// ...and the done filter holds for it: issue 5 is open.
	if closed, err := s.Search(ctx, trackerport.SearchQuery{Text: "#5", Done: trackerport.DoneOnly}); err != nil || len(closed.Tickets) != 0 {
		t.Errorf("closed by key = %+v, %v", closed, err)
	}
	if len(*queries) != 2 {
		t.Errorf("a key search still hit the search endpoint: %q", *queries)
	}

	tk, err := s.Fetch(ctx, "lu/greeter#5")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Body != "It fails **sometimes**." || tk.URL != "https://github.com/lu/greeter/issues/5" || tk.Updated.IsZero() {
		t.Errorf("fetch = %+v", tk)
	}
	if tk.RemoteID != "1005" || tk.StatusID != "open" || tk.TypeID != "issue" || strings.Join(tk.Labels, ",") != "bug" {
		t.Errorf("ids and labels = %q %q %q %q", tk.RemoteID, tk.StatusID, tk.TypeID, tk.Labels)
	}
	if _, err := s.Fetch(ctx, "lu/greeter#404"); !errors.Is(err, trackerport.ErrTicketNotFound) {
		t.Errorf("a missing issue = %v, want ErrTicketNotFound", err)
	}
	if _, err := s.Fetch(ctx, "lu/greeter#6"); err == nil || !strings.Contains(err.Error(), "pull request") {
		t.Errorf("a PR behind the number was not refused: %v", err)
	}
	if _, err := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"}).Fetch(ctx, "#5"); err == nil {
		t.Error("a bare number with no configured repo was accepted")
	}
	// A bare number is an issue NUMBER here: a stable id (1005, issue 5's)
	// handed to Fetch would read an unrelated issue, so the adapter offers no
	// fetch by it and a live read never retries a missing key that way.
	if _, ok := any(s).(trackerport.TicketByRemoteID); ok {
		t.Error("GitHub Issues claims a fetch by stable id")
	}
}

func TestIssuesTransitionOpenClosedOnly(t *testing.T) {
	srv, writes, _ := issuesStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"})
	ctx := context.Background()
	if err := s.Transition(ctx, "lu/greeter#5", "Closed"); err != nil {
		t.Fatal(err)
	}
	patches := writes["PATCH /repos/lu/greeter/issues/5"]
	if len(patches) != 1 || patches[0]["state"] != "closed" || patches[0]["state_reason"] != "completed" {
		t.Errorf("patches = %+v", patches)
	}
	if err := s.Transition(ctx, "lu/greeter#5", "open"); err != nil {
		t.Fatal(err)
	}
	if len(writes["PATCH /repos/lu/greeter/issues/5"]) != 1 {
		t.Error("an already-open issue was patched")
	}
	var unreachable *trackerport.ErrStatusUnreachable
	if err := s.Transition(ctx, "lu/greeter#5", "In Progress"); !errors.As(err, &unreachable) || strings.Join(unreachable.Reachable, ",") != "open,closed" {
		t.Errorf("err = %v", err)
	}
}

// An issue's whole status list is open/closed, its type list one "issue".
func TestIssuesMetadataAndComments(t *testing.T) {
	srv, _, _ := issuesStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"})
	ctx := context.Background()
	statuses, err := s.Statuses(ctx)
	if err != nil || len(statuses) != 2 || statuses[0] != (trackerport.RemoteStatus{ID: "open", Name: "Open", Category: trackerport.CategoryNew}) ||
		statuses[1] != (trackerport.RemoteStatus{ID: "closed", Name: "Closed", Category: trackerport.CategoryDone}) {
		t.Errorf("statuses = %+v, %v", statuses, err)
	}
	types, err := s.Types(ctx)
	if err != nil || len(types) != 1 || types[0] != (trackerport.RemoteType{ID: "issue", Name: "Issue"}) {
		t.Errorf("types = %+v, %v", types, err)
	}
	comments, err := s.ListComments(ctx, "lu/greeter#5")
	if err != nil || len(comments) != 2 {
		t.Fatalf("comments = %+v, %v", comments, err)
	}
	if c := comments[0]; c.ID != "11" || c.Author != "ann" || c.Body != "first" || c.Updated.Sub(c.Created) != time.Hour {
		t.Errorf("first = %+v", c)
	}
	if c := comments[1]; c.ID != "12" || c.Author != "" || !c.Updated.Equal(c.Created) {
		t.Errorf("second = %+v", c)
	}
}

func TestIssuesFieldsAndLink(t *testing.T) {
	srv, writes, _ := issuesStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"})
	ctx := context.Background()
	if err := s.SetFields(ctx, "lu/greeter#5", map[string]string{"labels": "bug, needs-review", "milestone": "V2", "assignees": "lu"}); err != nil {
		t.Fatal(err)
	}
	p := writes["PATCH /repos/lu/greeter/issues/5"][0]
	if ls, _ := p["labels"].([]any); len(ls) != 2 || ls[1] != "needs-review" {
		t.Errorf("labels = %v", p["labels"])
	}
	if p["milestone"] != float64(3) {
		t.Errorf("milestone = %v, want 3 (v2 by title)", p["milestone"])
	}
	var unsupported *trackerport.ErrFieldUnsupported
	if err := s.SetFields(ctx, "lu/greeter#5", map[string]string{"Feature flags": "x"}); !errors.As(err, &unsupported) || len(unsupported.Supported) != 3 {
		t.Errorf("custom field: err = %v", err)
	}
	var unknown *trackerport.ErrFieldUnknown
	if err := s.SetFields(ctx, "lu/greeter#5", map[string]string{"milestone": "v9"}); !errors.As(err, &unknown) {
		t.Errorf("unknown milestone: err = %v", err)
	}
	cur, err := s.Fields(ctx, "lu/greeter#5", []string{"labels", "milestone", "assignees"})
	if err != nil {
		t.Fatal(err)
	}
	if cur["labels"] != "bug" || cur["milestone"] != "v1" || cur["assignees"] != "lu" {
		t.Errorf("fields = %v", cur)
	}

	// The link already posted (its marker is in the comments) is not posted again.
	if err := s.AddLink(ctx, "lu/greeter#5", trackerport.TicketLink{Title: "Merge request: feat/x", URL: "https://github.com/lu/greeter/pull/7"}); err != nil {
		t.Fatal(err)
	}
	if len(writes["POST /repos/lu/greeter/issues/5/comments"]) != 0 {
		t.Error("an already-linked merge request was posted again")
	}
	if err := s.AddLink(ctx, "lu/greeter#5", trackerport.TicketLink{Title: "Merge request: feat/y", URL: "https://github.com/lu/greeter/pull/8"}); err != nil {
		t.Fatal(err)
	}
	posts := writes["POST /repos/lu/greeter/issues/5/comments"]
	if len(posts) != 1 || !strings.Contains(posts[0]["body"].(string), linkMarker("https://github.com/lu/greeter/pull/8")) {
		t.Errorf("link comment = %+v", posts)
	}
}

// A comment's attachment goes up for the issue's own repository.
func TestIssuesAttachUploadsForTheIssueRepository(t *testing.T) {
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/tracker", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":99}`))
	})
	mux.HandleFunc("POST /user-attachments/assets", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("repository_id")
		_, _ = w.Write([]byte(`{"url":"https://github.com/user-attachments/assets/1"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"})
	s.a.uploads = srv.URL
	att, err := s.Attach(context.Background(), "lu/tracker#4", "home.png", "image/png", []byte("x"))
	if err != nil || query != "99" || att.URL != "https://github.com/user-attachments/assets/1" {
		t.Fatalf("att %+v err %v repository_id %q", att, err, query)
	}
}

// Search pages by page number while GitHub has more, closed issues are
// Done, and the 1000-result cap of the search API ends the paging.
func TestIssuesSearchPagesAndFiltersDone(t *testing.T) {
	var pages, queries []string
	total := 5
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search/issues", func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		queries = append(queries, r.URL.Query().Get("q"))
		_, _ = fmt.Fprintf(w, `{"total_count":%d,"items":[{"number":1,"state":"closed","repository_url":"https://api.github.com/repos/lu/a"},{"number":2,"state":"closed","repository_url":"https://api.github.com/repos/lu/a"}]}`, total)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok", Repos: []string{"lu/a"}})
	ctx := context.Background()

	q := trackerport.SearchQuery{Text: "router", Done: trackerport.DoneOnly, Limit: 2}
	first, err := s.Search(ctx, q)
	if err != nil || len(first.Tickets) != 2 || !first.Tickets[0].Done || first.Next != "2" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	q.Cursor = first.Next
	second, err := s.Search(ctx, q)
	if err != nil || second.Next != "3" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	q.Cursor = second.Next
	if third, err := s.Search(ctx, q); err != nil || third.Next != "" {
		t.Fatalf("third (5 of 5 read) = %+v, %v", third, err)
	}
	if strings.Join(pages, ",") != "1,2,3" || queries[0] != "is:issue repo:lu/a state:closed router" {
		t.Errorf("pages %v, query %q", pages, queries[0])
	}

	// GitHub serves no result past the thousandth, whatever total it reports.
	total = 5000
	if last, err := s.Search(ctx, trackerport.SearchQuery{Text: "x", Limit: 2, Cursor: "500"}); err != nil || last.Next != "" {
		t.Errorf("at the cap = %+v, %v", last, err)
	}
	if _, err := s.Search(ctx, trackerport.SearchQuery{Cursor: "zero"}); err == nil {
		t.Error("a cursor that is no page number was accepted")
	}
}

// Rate limits come back typed with how long to wait: a 429's Retry-After, a
// spent quota's reset time, GraphQL's RATE_LIMITED error.
func TestIssuesRateLimited(t *testing.T) {
	reset := strconv.FormatInt(time.Now().Add(90*time.Second).Unix(), 10)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/greeter/issues/1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	mux.HandleFunc("GET /repos/lu/greeter/issues/2", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Reset", reset)
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Reset", reset)
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"})
	ctx := context.Background()

	var rl *trackerport.ErrRateLimited
	if _, err := s.Fetch(ctx, "lu/greeter#1"); !errors.As(err, &rl) || rl.RetryAfter != 2*time.Minute {
		t.Errorf("429 = %v", err)
	}
	if _, err := s.Fetch(ctx, "lu/greeter#2"); !errors.As(err, &rl) || rl.RetryAfter < 80*time.Second || rl.RetryAfter > 90*time.Second {
		t.Errorf("spent quota = %v (%+v)", err, rl)
	}
	if err := s.a.graphql(ctx, "query{viewer{login}}", nil, nil); !errors.As(err, &rl) || rl.RetryAfter < 80*time.Second || rl.RetryAfter > 90*time.Second {
		t.Errorf("RATE_LIMITED = %v (%+v)", err, rl)
	}
}
