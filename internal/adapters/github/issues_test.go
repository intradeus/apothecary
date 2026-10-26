package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apothecary/internal/ports"
)

const issueJSONBody = `{"number":5,"title":"Flaky router test","body":"It fails **sometimes**.","html_url":"https://github.com/lu/greeter/issues/5",
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
		_, _ = w.Write([]byte(`[{"body":"first"},{"body":"Merge request: feat/x: https://github.com/lu/greeter/pull/7\n\n<!-- apothecary link=https://github.com/lu/greeter/pull/7 -->"}]`))
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

	got, err := s.Search(ctx, "router", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "lu/greeter#5" || got[0].Assignee != "lu" || got[0].Status != "open" {
		t.Errorf("search = %+v (the pull request must be dropped)", got)
	}
	if (*queries)[0] != "is:issue repo:lu/greeter router" {
		t.Errorf("query = %q", (*queries)[0])
	}
	if _, err := s.Search(ctx, "", 10); err != nil {
		t.Fatal(err)
	}
	if (*queries)[1] != "is:issue repo:lu/greeter state:open assignee:@me" {
		t.Errorf("empty query = %q", (*queries)[1])
	}
	// A key looks the issue up directly; a bare number uses the one repo.
	byKey, err := s.Search(ctx, "#5", 10)
	if err != nil || len(byKey) != 1 || byKey[0].Key != "lu/greeter#5" {
		t.Errorf("search by key = %+v, %v", byKey, err)
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
	if _, err := s.Fetch(ctx, "lu/greeter#6"); err == nil || !strings.Contains(err.Error(), "pull request") {
		t.Errorf("a PR behind the number was not refused: %v", err)
	}
	if _, err := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"}).Fetch(ctx, "#5"); err == nil {
		t.Error("a bare number with no configured repo was accepted")
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
	var unreachable *ports.ErrStatusUnreachable
	if err := s.Transition(ctx, "lu/greeter#5", "In Progress"); !errors.As(err, &unreachable) || strings.Join(unreachable.Reachable, ",") != "open,closed" {
		t.Errorf("err = %v", err)
	}
}

// Canonical statuses on an issue: done closes, todo reopens, and the rest
// have no GitHub word — unsupported, which the engine skips.
func TestIssuesTransitionToCanonical(t *testing.T) {
	srv, writes, _ := issuesStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL, Token: "tok"})
	ctx := context.Background()
	if err := s.TransitionTo(ctx, "lu/greeter#5", ports.StatusDone); err != nil {
		t.Fatal(err)
	}
	patches := writes["PATCH /repos/lu/greeter/issues/5"]
	if len(patches) != 1 || patches[0]["state"] != "closed" || patches[0]["state_reason"] != "completed" {
		t.Errorf("patches = %+v", patches)
	}
	// The stub's issue reads open, so todo is already there: no call.
	if err := s.TransitionTo(ctx, "lu/greeter#5", ports.StatusTodo); err != nil {
		t.Fatal(err)
	}
	if len(writes["PATCH /repos/lu/greeter/issues/5"]) != 1 {
		t.Error("an already-open issue was patched")
	}
	var unsupported *ports.ErrStatusUnsupported
	err := s.TransitionTo(ctx, "lu/greeter#5", ports.StatusInReview)
	if !errors.As(err, &unsupported) || ports.StatusNames(unsupported.Supported) != "todo, done" || !strings.Contains(err.Error(), "GitHub Issues has no status") {
		t.Errorf("err = %v", err)
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
	var unsupported *ports.ErrFieldUnsupported
	if err := s.SetFields(ctx, "lu/greeter#5", map[string]string{"Feature flags": "x"}); !errors.As(err, &unsupported) || len(unsupported.Supported) != 3 {
		t.Errorf("custom field: err = %v", err)
	}
	var unknown *ports.ErrFieldUnknown
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
	if err := s.AddLink(ctx, "lu/greeter#5", ports.TicketLink{Title: "Merge request: feat/x", URL: "https://github.com/lu/greeter/pull/7"}); err != nil {
		t.Fatal(err)
	}
	if len(writes["POST /repos/lu/greeter/issues/5/comments"]) != 0 {
		t.Error("an already-linked merge request was posted again")
	}
	if err := s.AddLink(ctx, "lu/greeter#5", ports.TicketLink{Title: "Merge request: feat/y", URL: "https://github.com/lu/greeter/pull/8"}); err != nil {
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
