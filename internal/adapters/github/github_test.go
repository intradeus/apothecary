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

// ghStub fakes the GitHub v3 endpoints the adapter touches and records
// pull-request creations.
func ghStub(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var prs []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"login":"lu"}`))
	})
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"full_name":"lu/greeter","clone_url":"https://github.com/lu/greeter.git","default_branch":"main"}]`))
	})
	mux.HandleFunc("GET /repos/lu/greeter", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"permissions":{"push":true,"pull":true}}`))
	})
	mux.HandleFunc("GET /repos/lu/readonly", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"permissions":{"push":false,"pull":true}}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("path") {
		case "AGENTS.md":
			_, _ = w.Write([]byte(`{"name":"AGENTS.md","type":"file"}`))
		case ".apothecary/agents":
			_, _ = w.Write([]byte(`[{"name":"planner.yaml"},{"name":"builder.yaml"}]`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	})
	mux.HandleFunc("POST /repos/lu/greeter/pulls", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		prs = append(prs, body)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/lu/greeter/pull/7"}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/pulls/7", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"state":"closed","merged_at":"2026-07-20T12:00:00Z"}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/pulls/8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"state":"closed","merged_at":null}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/pulls/9", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"state":"open","merged_at":null}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &prs
}

func testAdapter(ts *httptest.Server) (*Adapter, ports.RepoRef) {
	return New(Config{BaseURL: ts.URL, Token: "tok", Locator: "lu/greeter"}),
		ports.RepoRef{Locator: "lu/greeter", DefaultBranch: "main"}
}

func TestCheckAccess(t *testing.T) {
	ts, _ := ghStub(t)
	a, repo := testAdapter(ts)
	if err := a.CheckAccess(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	ro := New(Config{BaseURL: ts.URL, Token: "tok", Locator: "lu/readonly"})
	err := ro.CheckAccess(context.Background(), ports.RepoRef{Locator: "lu/readonly"})
	if err == nil || !strings.Contains(err.Error(), "cannot push") {
		t.Errorf("read-only err = %v", err)
	}
}

func TestFileExists(t *testing.T) {
	ts, _ := ghStub(t)
	a, repo := testAdapter(ts)
	ctx := context.Background()
	if ok, err := a.FileExists(ctx, repo, "AGENTS.md"); err != nil || !ok {
		t.Errorf("file = %v, %v", ok, err)
	}
	if ok, err := a.FileExists(ctx, repo, ".apothecary/agents/"); err != nil || !ok {
		t.Errorf("dir = %v, %v", ok, err)
	}
	if ok, err := a.FileExists(ctx, repo, "nope.md"); err != nil || ok {
		t.Errorf("missing = %v, %v", ok, err)
	}
}

func TestCreateDraftMRIsNativeDraft(t *testing.T) {
	ts, prs := ghStub(t)
	a, repo := testAdapter(ts)
	ref, err := a.CreateDraftMR(context.Background(), repo, "apothecary/x", "", "Add greeting", "body text")
	if err != nil {
		t.Fatal(err)
	}
	if ref.IID != 7 || !strings.Contains(ref.URL, "/pull/7") {
		t.Errorf("ref = %+v", ref)
	}
	pr := (*prs)[0]
	if pr["draft"] != true {
		t.Error("PR not created as native draft")
	}
	if title := pr["title"].(string); strings.HasPrefix(title, "Draft:") {
		t.Errorf("title %q must NOT carry the GitLab-style prefix", title)
	}
	if pr["head"] != "apothecary/x" || pr["base"] != "main" {
		t.Errorf("branches = %v -> %v", pr["head"], pr["base"])
	}
}

func TestCreateDraftMRReturnsExistingOnConflict(t *testing.T) {
	var listQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/lu/greeter/pulls", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"A pull request already exists for lu:apothecary/x."}]}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/pulls", func(w http.ResponseWriter, r *http.Request) {
		listQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[{"number":42,"html_url":"https://github.com/lu/greeter/pull/42"}]`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	a, repo := testAdapter(ts)

	ref, err := a.CreateDraftMR(context.Background(), repo, "apothecary/x", "", "Add greeting", "body")
	if err != nil {
		t.Fatalf("conflict must not error: %v", err)
	}
	if ref.IID != 42 || !strings.Contains(ref.URL, "/pull/42") {
		t.Errorf("ref = %+v, want existing PR 42", ref)
	}
	for _, want := range []string{"state=open", "head=lu%3Aapothecary%2Fx", "base=main"} {
		if !strings.Contains(listQuery, want) {
			t.Errorf("lookup query %q missing %s", listQuery, want)
		}
	}
}

func TestMRStateMergedWinsOverClosed(t *testing.T) {
	ts, _ := ghStub(t)
	a, _ := testAdapter(ts)
	ctx := context.Background()
	for iid, want := range map[int]ports.MRState{7: ports.MRMerged, 8: ports.MRClosed, 9: ports.MROpen} {
		st, err := a.MRStatus(ctx, ports.MRRef{IID: iid})
		got := st.State
		if err != nil || got != want {
			t.Errorf("pr %d state = %v, %v (want %v)", iid, got, err, want)
		}
	}
}

func TestAccount(t *testing.T) {
	ts, _ := ghStub(t)
	acc := NewAccount(ts.URL, "tok")
	if err := acc.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := NewAccount(ts.URL, "wrong")
	if err := bad.Test(context.Background()); err == nil {
		t.Error("bad token accepted")
	}
	repos, err := acc.ListRepos(context.Background())
	if err != nil || len(repos) != 1 || repos[0].Locator != "lu/greeter" || repos[0].DefaultBranch != "main" {
		t.Errorf("repos = %+v, %v", repos, err)
	}
}

func TestAccountListBranches(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/greeter", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"default_branch":"main"}`))
	})
	mux.HandleFunc("GET /repos/acme/greeter/branches", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`[{"name":"develop"},{"name":"main"}]`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	acc := NewAccount(ts.URL, "tok")
	branches, err := acc.ListBranches(context.Background(), "acme/greeter")
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %+v", branches)
	}
	// Default flag is derived from the repo GET, not branch order.
	got := map[string]bool{}
	for _, b := range branches {
		got[b.Name] = b.Default
	}
	if !got["main"] || got["develop"] {
		t.Errorf("default flags wrong: %+v", branches)
	}
}

func TestMRStatusMergeability(t *testing.T) {
	for _, tc := range []struct {
		body string
		want ports.Mergeable
	}{
		{`{"state":"open","mergeable":true,"mergeable_state":"clean","head":{"sha":"h"},"base":{"ref":"main"}}`, ports.MergeClean},
		{`{"state":"open","mergeable":false,"mergeable_state":"dirty","head":{"sha":"h"},"base":{"ref":"main"}}`, ports.MergeConflicts},
		{`{"state":"open","mergeable":null,"mergeable_state":"unknown","head":{"sha":"h"},"base":{"ref":"main"}}`, ports.MergeUnknown},
		{`{"state":"open","mergeable":false,"mergeable_state":"blocked","head":{"sha":"h"},"base":{"ref":"main"}}`, ports.MergeClean}, // blocked by checks, not conflicts
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /repos/lu/greeter/pulls/5", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) })
		mux.HandleFunc("GET /repos/lu/greeter/branches/main", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"commit":{"sha":"t1"}}`)) })
		ts := httptest.NewServer(mux)
		a, _ := testAdapter(ts)
		st, err := a.MRStatus(context.Background(), ports.MRRef{IID: 5})
		ts.Close()
		if err != nil || st.Mergeable != tc.want || st.HeadSHA != "h" || st.Target != "main" {
			t.Errorf("%s: %+v, %v; want %s", tc.body, st, err, tc.want)
		}
		if (tc.want == ports.MergeConflicts) != (st.TargetSHA == "t1") {
			t.Errorf("target sha %q is fetched only on conflicts", st.TargetSHA)
		}
	}
}

func TestMRThreadsRepliesAndResolve(t *testing.T) {
	var gql []string
	var issueComment string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		gql = append(gql, b.Query)
		switch {
		case strings.Contains(b.Query, "reviewThreads"):
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
			  {"id":"T1","isResolved":false,"path":"a.go","line":7,"comments":{"nodes":[
			    {"databaseId":1,"body":"rename","createdAt":"2026-10-18T10:00:00Z","diffHunk":"@@ -1 +1 @@","author":{"login":"rev"}}]}},
			  {"id":"T2","isResolved":true,"path":"b.go","line":null,"originalLine":3,"comments":{"nodes":[
			    {"databaseId":2,"body":"ok","createdAt":"2026-10-18T10:00:00Z","author":{"login":"rev"}}]}}]}}}}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	})
	mux.HandleFunc("GET /repos/lu/greeter/pulls/5/reviews", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":9,"body":"please add tests","state":"CHANGES_REQUESTED","user":{"login":"rev"},"submitted_at":"2026-10-18T09:00:00Z"},
		  {"id":10,"body":"","state":"APPROVED","user":{"login":"rev"}}]`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/issues/5/comments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":50,"body":"added <!-- apothecary run=r thread=review:9 -->","user":{"login":"bot"},"created_at":"2026-10-18T11:00:00Z"},
		  {"id":51,"body":"ci passed","user":{"login":"ci[bot]","type":"Bot"},"created_at":"2026-10-18T11:00:00Z"},
		  {"id":52,"body":"also update the README","user":{"login":"rev","type":"User"},"created_at":"2026-10-18T11:30:00Z"},
		  {"id":53,"body":"done <!-- apothecary run=r thread=issue:52 -->","user":{"login":"bot"},"created_at":"2026-10-18T12:00:00Z"}]`))
	})
	mux.HandleFunc("POST /repos/lu/greeter/issues/5/comments", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		issueComment = b["body"]
		_, _ = w.Write([]byte(`{}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a, _ := testAdapter(ts)
	ctx := context.Background()
	th, err := a.MRThreads(ctx, ports.MRRef{IID: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(th) != 4 || th[0].ID != "T1" || th[0].Line != 7 || th[0].DiffHunk == "" || th[1].Line != 3 || !th[1].Resolved {
		t.Fatalf("threads = %+v", th)
	}
	// A conversation comment is a thread; the bot's is not; our reply to it
	// is attached, not a thread of its own.
	if th[3].ID != "issue:52" || len(th[3].Comments) != 2 || !ports.IsFactoryReply(th[3].Comments[1].Body) {
		t.Errorf("conversation thread = %+v", th[3])
	}
	if err := a.ReplyThread(ctx, ports.MRRef{IID: 5}, "issue:52", "readme updated"); err != nil || issueComment != "readme updated" {
		t.Errorf("conversation reply: %q, %v", issueComment, err)
	}
	if th[2].ID != "review:9" || len(th[2].Comments) != 2 || !ports.IsFactoryReply(th[2].Comments[1].Body) {
		t.Errorf("review-body thread = %+v (approved empty review dropped, our reply attached)", th[2])
	}
	if err := a.ReplyThread(ctx, ports.MRRef{IID: 5}, "T1", "done"); err != nil || !strings.Contains(gql[len(gql)-1], "addPullRequestReviewThreadReply") {
		t.Errorf("thread reply: %v, %v", err, gql)
	}
	if err := a.ReplyThread(ctx, ports.MRRef{IID: 5}, "review:9", "added"); err != nil || issueComment != "added" {
		t.Errorf("review reply: %q, %v", issueComment, err)
	}
	n := len(gql)
	if err := a.ResolveThread(ctx, ports.MRRef{IID: 5}, "T1"); err != nil || !strings.Contains(gql[len(gql)-1], "resolveReviewThread") {
		t.Errorf("resolve: %v", err)
	}
	if err := a.ResolveThread(ctx, ports.MRRef{IID: 5}, "review:9"); err != nil || len(gql) != n+1 {
		t.Errorf("a review body has nothing to resolve: %v", err)
	}
}

func TestMRStatusMergeableNowAndDraft(t *testing.T) {
	for _, tc := range []struct {
		body       string
		now, draft bool
	}{
		{`{"state":"open","mergeable":true,"mergeable_state":"clean","draft":false,"head":{"sha":"h"},"base":{"ref":"main"}}`, true, false},
		{`{"state":"open","mergeable":true,"mergeable_state":"has_hooks","head":{"sha":"h"},"base":{"ref":"main"}}`, true, false},
		{`{"state":"open","mergeable":true,"mergeable_state":"unstable","head":{"sha":"h"},"base":{"ref":"main"}}`, false, false},
		{`{"state":"open","mergeable":false,"mergeable_state":"blocked","head":{"sha":"h"},"base":{"ref":"main"}}`, false, false},
		{`{"state":"open","mergeable":true,"mergeable_state":"draft","draft":true,"head":{"sha":"h"},"base":{"ref":"main"}}`, false, true},
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /repos/lu/greeter/pulls/5", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) })
		ts := httptest.NewServer(mux)
		a, _ := testAdapter(ts)
		st, err := a.MRStatus(context.Background(), ports.MRRef{IID: 5})
		ts.Close()
		if err != nil || st.MergeableNow != tc.now || st.Draft != tc.draft {
			t.Errorf("%s: now %v draft %v, %v", tc.body, st.MergeableNow, st.Draft, err)
		}
	}
}

// Check runs and commit statuses both count; a re-run's newest result is
// the one that does.
func TestMRChecks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/greeter/commits/h1/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"check_runs":[
		  {"id":2,"name":"test","status":"completed","conclusion":"success","html_url":"https://gh/run/2"},
		  {"id":1,"name":"test","status":"completed","conclusion":"failure","html_url":"https://gh/run/1"},
		  {"id":3,"name":"lint","status":"in_progress","conclusion":null},
		  {"id":4,"name":"docs","status":"completed","conclusion":"skipped"}]}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/commits/h1/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"statuses":[{"context":"ci/legacy","state":"error","target_url":"https://ci/9"}]}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a, _ := testAdapter(ts)
	c, err := a.MRChecks(context.Background(), ports.MRRef{IID: 5}, "h1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ports.CheckState{"test": ports.CheckSuccess, "lint": ports.CheckPending, "docs": ports.CheckNeutral, "ci/legacy": ports.CheckFailure}
	if len(c.Items) != len(want) || c.State != ports.CheckFailure || c.Head != "h1" {
		t.Fatalf("checks = %+v", c)
	}
	for _, it := range c.Items {
		if want[it.Name] != it.State {
			t.Errorf("%s = %s, want %s", it.Name, it.State, want[it.Name])
		}
	}
	if f := c.Failed(); len(f) != 1 || f[0] != "ci/legacy" {
		t.Errorf("failed = %v", f)
	}
}

// Each reviewer's latest decision counts; a comment decides nothing, a
// dismissal undoes an approval, one request for changes blocks.
func TestMRApproval(t *testing.T) {
	for _, tc := range []struct {
		reviews string
		ok      bool
		by      int
	}{
		{`[]`, false, 0},
		{`[{"state":"APPROVED","user":{"login":"a"}},{"state":"COMMENTED","user":{"login":"a"}}]`, true, 1},
		{`[{"state":"APPROVED","user":{"login":"a"}},{"state":"CHANGES_REQUESTED","user":{"login":"b"}}]`, false, 1},
		{`[{"state":"CHANGES_REQUESTED","user":{"login":"b"}},{"state":"APPROVED","user":{"login":"b"}}]`, true, 1},
		{`[{"state":"APPROVED","user":{"login":"a"}},{"state":"DISMISSED","user":{"login":"a"}}]`, false, 0},
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /repos/lu/greeter/pulls/5/reviews", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.reviews)) })
		ts := httptest.NewServer(mux)
		a, _ := testAdapter(ts)
		ap, err := a.MRApproval(context.Background(), ports.MRRef{IID: 5})
		ts.Close()
		if err != nil || ap.Approved != tc.ok || len(ap.By) != tc.by {
			t.Errorf("%s: %+v, %v", tc.reviews, ap, err)
		}
	}
}

// A merge pins the head and names the method; GitHub declining is a refusal
// the flow routes; the branch goes only when asked, and only the repo's own.
func TestMergeMR(t *testing.T) {
	var merged map[string]any
	var deleted []string
	refuse := 0
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /repos/lu/greeter/pulls/5/merge", func(w http.ResponseWriter, r *http.Request) {
		if refuse != 0 {
			w.WriteHeader(refuse)
			_, _ = w.Write([]byte(`{"message":"Pull Request is not mergeable"}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&merged)
		_, _ = w.Write([]byte(`{"merged":true}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/pulls/5", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"head":{"ref":"apo/x","repo":{"full_name":"lu/greeter"}},"base":{"repo":{"full_name":"lu/greeter"}}}`))
	})
	mux.HandleFunc("DELETE /repos/lu/greeter/git/refs/heads/apo/x", func(w http.ResponseWriter, r *http.Request) {
		deleted = append(deleted, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a, _ := testAdapter(ts)
	ctx := context.Background()
	if err := a.MergeMR(ctx, ports.MRRef{IID: 5}, ports.MergeOptions{Method: ports.MergeMethodSquash, Head: "h1", DeleteBranch: true}); err != nil {
		t.Fatal(err)
	}
	if merged["merge_method"] != "squash" || merged["sha"] != "h1" || len(deleted) != 1 {
		t.Errorf("merge body %v, deleted %v", merged, deleted)
	}
	for _, code := range []int{405, 409, 422} {
		refuse = code
		var refused *ports.ErrMergeRefused
		if err := a.MergeMR(ctx, ports.MRRef{IID: 5}, ports.MergeOptions{}); !errors.As(err, &refused) {
			t.Errorf("%d: err = %v, want a refusal", code, err)
		}
	}
	refuse = 500
	var refused *ports.ErrMergeRefused
	if err := a.MergeMR(ctx, ports.MRRef{IID: 5}, ports.MergeOptions{}); err == nil || errors.As(err, &refused) {
		t.Errorf("500: err = %v, want the repository host failing, not a refusal", err)
	}
}

// One COMMENT review: notes in the diff go on their lines, marked; the rest
// join the body; a review already carrying the marker is not posted again.
func TestPostReview(t *testing.T) {
	var posted []map[string]any
	existing := `[]`
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/greeter/pulls/5/reviews", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(existing)) })
	mux.HandleFunc("GET /repos/lu/greeter/pulls/5/files", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"filename":"a.go","patch":"@@ -1,2 +1,3 @@\n package a\n+func x() {}\n ok"}]`))
	})
	mux.HandleFunc("POST /repos/lu/greeter/pulls/5/reviews", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		posted = append(posted, b)
		_, _ = w.Write([]byte(`{"id":1}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a, _ := testAdapter(ts)
	rev := ports.Review{ID: "run/p-1/post", Head: "h1", Summary: "Looks reasonable.", Notes: []ports.ReviewNote{
		{Path: "a.go", Line: 2, Body: "name it"}, {Path: "a.go", Line: 40, Body: "far away"}, {Path: "b.go", Line: 1, Body: "untouched file"}}}
	res, err := a.PostReview(context.Background(), ports.MRRef{IID: 5}, rev)
	if err != nil || res.Inline != 1 || res.Folded != 2 || len(posted) != 1 {
		t.Fatalf("res %+v err %v posted %d", res, err, len(posted))
	}
	b := posted[0]
	body, _ := b["body"].(string)
	comments, _ := b["comments"].([]any)
	if b["event"] != "COMMENT" || b["commit_id"] != "h1" || len(comments) != 1 ||
		!strings.Contains(body, "Looks reasonable.") || !strings.Contains(body, "`a.go:40` — far away") ||
		!strings.Contains(body, ports.ReviewMarker(rev.ID, "review")) {
		t.Errorf("review = %v", b)
	}
	if c := comments[0].(map[string]any); c["line"] != float64(2) || c["side"] != "RIGHT" || !strings.Contains(c["body"].(string), ports.ReviewMarker(rev.ID, "note-0")) {
		t.Errorf("comment = %v", c)
	}
	existing = `[{"body":"Looks reasonable.\n\n` + strings.ReplaceAll(ports.ReviewMarker(rev.ID, "review"), `"`, `\"`) + `"}]`
	res, err = a.PostReview(context.Background(), ports.MRRef{IID: 5}, rev)
	if err != nil || !res.Already || len(posted) != 1 {
		t.Errorf("retry: res %+v err %v posted %d", res, err, len(posted))
	}
}
