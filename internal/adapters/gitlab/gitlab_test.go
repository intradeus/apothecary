package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apothecary/internal/ports"
)

// glStub fakes the few v4 endpoints the adapter touches.
func glStub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/7", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "project")
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"id":7,"permissions":{"project_access":{"access_level":40},"group_access":null}}`))
	})
	mux.HandleFunc("HEAD /api/v4/projects/7/repository/files/{path}", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "file:"+r.PathValue("path")+"?"+r.URL.RawQuery)
		if strings.Contains(r.PathValue("path"), "AGENTS.md") {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	})
	mux.HandleFunc("GET /api/v4/projects/7/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "tree:"+r.URL.Query().Get("path"))
		if r.URL.Query().Get("path") == ".apothecary/agents" {
			_, _ = w.Write([]byte(`[{"name":"planner.yaml"}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /api/v4/projects/7/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		data, _ := json.Marshal(body)
		calls = append(calls, "mr:"+string(data))
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"iid":3,"web_url":"http://gl.local/root/greeter/-/merge_requests/3"}`))
	})
	mux.HandleFunc("GET /api/v4/projects/7/merge_requests/3", func(w http.ResponseWriter, _ *http.Request) {
		calls = append(calls, "mrstate")
		_, _ = w.Write([]byte(`{"iid":3,"state":"merged"}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &calls
}

func testAdapter(ts *httptest.Server) (*Adapter, ports.RepoRef) {
	return New(Config{BaseURL: ts.URL, Token: "tok", ProjectID: 7}),
		ports.RepoRef{Locator: "7", DefaultBranch: "main"}
}

func TestCheckAccess(t *testing.T) {
	ts, _ := glStub(t)
	a, repo := testAdapter(ts)
	if err := a.CheckAccess(context.Background(), repo); err != nil {
		t.Fatal(err)
	}

	bad := New(Config{BaseURL: ts.URL, Token: "wrong", ProjectID: 7})
	if err := bad.CheckAccess(context.Background(), repo); err == nil {
		t.Error("bad token must fail access check")
	}
	none := New(Config{BaseURL: ts.URL, ProjectID: 7})
	if err := none.CheckAccess(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("missing token err = %v", err)
	}
}

func TestCheckAccessInsufficientLevel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/7", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":7,"permissions":{"project_access":{"access_level":20}}}`)) // Reporter
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a := New(Config{BaseURL: ts.URL, Token: "tok", ProjectID: 7})
	err := a.CheckAccess(context.Background(), ports.RepoRef{Locator: "7"})
	if err == nil || !strings.Contains(err.Error(), "Developer") {
		t.Errorf("err = %v, want access-level refusal", err)
	}
}

func TestFileExistsFileAndDirectory(t *testing.T) {
	ts, calls := glStub(t)
	a, repo := testAdapter(ts)
	ctx := context.Background()

	ok, err := a.FileExists(ctx, repo, "AGENTS.md")
	if err != nil || !ok {
		t.Errorf("AGENTS.md = %v, %v", ok, err)
	}
	ok, err = a.FileExists(ctx, repo, "MISSING.md")
	if err != nil || ok {
		t.Errorf("MISSING.md = %v, %v", ok, err)
	}
	// Trailing slash routes to the tree endpoint (directories).
	ok, err = a.FileExists(ctx, repo, ".apothecary/agents/")
	if err != nil || !ok {
		t.Errorf(".apothecary/agents/ = %v, %v", ok, err)
	}
	ok, _ = a.FileExists(ctx, repo, "empty-dir/")
	if ok {
		t.Error("empty dir must report false")
	}
	joined := strings.Join(*calls, " ")
	if !strings.Contains(joined, "tree:.apothecary/agents") {
		t.Errorf("directory check should use tree endpoint: %v", *calls)
	}
	if !strings.Contains(joined, "ref=main") {
		t.Errorf("file check must pin the default branch ref: %v", *calls)
	}
}

func TestCreateDraftMRPrefixesTitle(t *testing.T) {
	ts, calls := glStub(t)
	a, repo := testAdapter(ts)
	ref, err := a.CreateDraftMR(context.Background(), repo, "apothecary/x-add-login", "", "Add login", "body text")
	if err != nil {
		t.Fatal(err)
	}
	if ref.IID != 3 || !strings.Contains(ref.URL, "/merge_requests/3") {
		t.Errorf("ref = %+v", ref)
	}
	var mrCall string
	for _, c := range *calls {
		if strings.HasPrefix(c, "mr:") {
			mrCall = c
		}
	}
	for _, want := range []string{`"title":"Draft: Add login"`, `"source_branch":"apothecary/x-add-login"`, `"target_branch":"main"`, `"description":"body text"`} {
		if !strings.Contains(mrCall, want) {
			t.Errorf("MR payload missing %s: %s", want, mrCall)
		}
	}
}

func TestCreateDraftMRReturnsExistingOnConflict(t *testing.T) {
	var listQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v4/projects/7/merge_requests", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":["Another open merge request already exists for this source branch: !9"]}`))
	})
	mux.HandleFunc("GET /api/v4/projects/7/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		listQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[{"iid":9,"web_url":"http://gl.local/root/greeter/-/merge_requests/9"}]`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	a, repo := testAdapter(ts)

	ref, err := a.CreateDraftMR(context.Background(), repo, "apothecary/x-add-login", "", "Add login", "body")
	if err != nil {
		t.Fatalf("conflict must not error: %v", err)
	}
	if ref.IID != 9 || !strings.Contains(ref.URL, "/merge_requests/9") {
		t.Errorf("ref = %+v, want existing MR 9", ref)
	}
	for _, want := range []string{"state=opened", "source_branch=apothecary%2Fx-add-login", "target_branch=main"} {
		if !strings.Contains(listQuery, want) {
			t.Errorf("lookup query %q missing %s", listQuery, want)
		}
	}
}

func TestMRStateMapping(t *testing.T) {
	ts, _ := glStub(t)
	a, _ := testAdapter(ts)
	st, err := a.MRStatus(context.Background(), ports.MRRef{IID: 3})
	if err != nil || st.State != ports.MRMerged {
		t.Errorf("state = %v, %v", st, err)
	}
}

func TestMRStatusMergeability(t *testing.T) {
	for _, tc := range []struct {
		detailed  string
		conflicts bool
		want      ports.Mergeable
	}{
		{"mergeable", false, ports.MergeClean},
		{"conflict", true, ports.MergeConflicts},
		{"broken_status", true, ports.MergeConflicts},
		{"checking", true, ports.MergeUnknown}, // stale until checked
		{"unchecked", false, ports.MergeUnknown},
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/v4/projects/7/merge_requests/4", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"state":"opened","has_conflicts":%v,"detailed_merge_status":%q,"sha":"abc","target_branch":"main"}`, tc.conflicts, tc.detailed)
		})
		mux.HandleFunc("GET /api/v4/projects/7/repository/branches/main", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"commit":{"id":"t1"}}`))
		})
		ts := httptest.NewServer(mux)
		a, _ := testAdapter(ts)
		st, err := a.MRStatus(context.Background(), ports.MRRef{IID: 4})
		ts.Close()
		if err != nil || st.Mergeable != tc.want || st.HeadSHA != "abc" || st.Target != "main" || st.State != ports.MROpen {
			t.Errorf("%s/%v: status = %+v, %v; want %s", tc.detailed, tc.conflicts, st, err, tc.want)
		}
		if (tc.want == ports.MergeConflicts) != (st.TargetSHA == "t1") {
			t.Errorf("%s: target sha %q is fetched only on conflicts", tc.detailed, st.TargetSHA)
		}
	}
}

func TestMRThreadsRepliesAndResolve(t *testing.T) {
	var replied, resolved string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/7/merge_requests/4/discussions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
		 {"id":"d1","notes":[
		   {"id":1,"body":"rename this","system":false,"author":{"username":"rev"},"created_at":"2026-10-18T10:00:00Z","resolvable":true,"resolved":false,
		    "position":{"new_path":"a.go","new_line":12}},
		   {"id":2,"body":"done <!-- apothecary run=r1 thread=d1 -->","author":{"username":"bot"},"created_at":"2026-10-18T11:00:00Z","resolvable":true,"resolved":false}]},
		 {"id":"d2","notes":[{"id":3,"body":"added 1 commit","system":true,"created_at":"2026-10-18T10:00:00Z"}]},
		 {"id":"d3","notes":[{"id":4,"body":"looks good overall","author":{"username":"rev"},"created_at":"2026-10-18T09:00:00Z","resolvable":false}]},
		 {"id":"d4","notes":[{"id":5,"body":"fixed","author":{"username":"rev"},"created_at":"2026-10-18T09:00:00Z","resolvable":true,"resolved":true}]}
		]`))
	})
	mux.HandleFunc("POST /api/v4/projects/7/merge_requests/4/discussions/d1/notes", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		replied = b["body"]
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("PUT /api/v4/projects/7/merge_requests/4/discussions/d1", func(w http.ResponseWriter, r *http.Request) {
		resolved = r.URL.Query().Get("resolved")
		_, _ = w.Write([]byte(`{}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a, _ := testAdapter(ts)
	ctx := context.Background()
	th, err := a.MRThreads(ctx, ports.MRRef{IID: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(th) != 3 {
		t.Fatalf("threads = %+v, want 3 (system-only discussion dropped)", th)
	}
	if th[0].ID != "d1" || th[0].Path != "a.go" || th[0].Line != 12 || th[0].Resolved || len(th[0].Comments) != 2 || th[0].Comments[0].Author != "rev" {
		t.Errorf("thread d1 = %+v", th[0])
	}
	if th[1].Resolved || th[2].ID != "d4" || !th[2].Resolved {
		t.Errorf("resolution: general comment %+v, resolved %+v", th[1], th[2])
	}
	if err := a.ReplyThread(ctx, ports.MRRef{IID: 4}, "d1", "ok"); err != nil || replied != "ok" {
		t.Errorf("reply = %q, %v", replied, err)
	}
	if err := a.ResolveThread(ctx, ports.MRRef{IID: 4}, "d1"); err != nil || resolved != "true" {
		t.Errorf("resolve = %q, %v", resolved, err)
	}
}

func TestAccountListBranches(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/7/repository/branches", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`[{"name":"main","default":true},{"name":"develop","default":false}]`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	acc := NewAccount(ts.URL, "tok")
	branches, err := acc.ListBranches(context.Background(), "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 || branches[0].Name != "main" || !branches[0].Default || branches[1].Name != "develop" {
		t.Errorf("branches = %+v", branches)
	}
}

func TestMRStatusMergeableNowAndDraft(t *testing.T) {
	for _, tc := range []struct {
		body       string
		now, draft bool
	}{
		{`{"state":"opened","detailed_merge_status":"mergeable","sha":"abc","target_branch":"main"}`, true, false},
		{`{"state":"opened","detailed_merge_status":"not_approved","sha":"abc","target_branch":"main"}`, false, false},
		{`{"state":"opened","detailed_merge_status":"draft_status","draft":true,"sha":"abc","target_branch":"main"}`, false, true},
		{`{"state":"merged","detailed_merge_status":"mergeable","sha":"abc","target_branch":"main"}`, false, false},
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/v4/projects/7/merge_requests/4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) })
		ts := httptest.NewServer(mux)
		a, _ := testAdapter(ts)
		st, err := a.MRStatus(context.Background(), ports.MRRef{IID: 4})
		ts.Close()
		if err != nil || st.MergeableNow != tc.now || st.Draft != tc.draft {
			t.Errorf("%s: now %v draft %v, %v", tc.body, st.MergeableNow, st.Draft, err)
		}
	}
}

// The head pipeline is the head's CI; a failed one names its failed jobs
// (allowed failures aside), an older commit's is not the head's yet.
func TestMRChecks(t *testing.T) {
	for _, tc := range []struct {
		name, mr string
		state    ports.CheckState
		items    []string
	}{
		{"none", `{"head_pipeline":null}`, ports.ChecksNone, nil},
		{"running", `{"head_pipeline":{"id":9,"sha":"h1","status":"running","web_url":"https://gl/p/9"}}`, ports.CheckPending, []string{"pipeline"}},
		{"passed", `{"head_pipeline":{"id":9,"sha":"h1","status":"success"}}`, ports.CheckSuccess, []string{"pipeline"}},
		{"stale", `{"head_pipeline":{"id":8,"sha":"h0","status":"success"}}`, ports.CheckPending, []string{"pipeline"}},
		{"failed", `{"head_pipeline":{"id":9,"sha":"h1","status":"failed"}}`, ports.CheckFailure, []string{"rspec"}},
		{"manual", `{"head_pipeline":{"id":9,"sha":"h1","status":"manual"}}`, ports.ChecksNone, []string{"pipeline"}},
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/v4/projects/7/merge_requests/4", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.mr)) })
		mux.HandleFunc("GET /api/v4/projects/7/pipelines/9/jobs", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[{"name":"rspec","web_url":"https://gl/j/1"},{"name":"flaky","allow_failure":true}]`))
		})
		ts := httptest.NewServer(mux)
		a, _ := testAdapter(ts)
		c, err := a.MRChecks(context.Background(), ports.MRRef{IID: 4}, "h1")
		ts.Close()
		var names []string
		for _, it := range c.Items {
			names = append(names, it.Name)
		}
		if err != nil || c.State != tc.state || strings.Join(names, ",") != strings.Join(tc.items, ",") {
			t.Errorf("%s: %+v, %v", tc.name, c, err)
		}
	}
}

// A project without approval rules reports approved with nobody's approval:
// that is not one.
func TestMRApproval(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"approved":true,"approved_by":[]}`, false},
		{`{"approved":true,"approved_by":[{"user":{"username":"rev"}}]}`, true},
		{`{"approved":false,"approved_by":[{"user":{"username":"rev"}}]}`, false},
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/v4/projects/7/merge_requests/4/approvals", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) })
		ts := httptest.NewServer(mux)
		a, _ := testAdapter(ts)
		ap, err := a.MRApproval(context.Background(), ports.MRRef{IID: 4})
		ts.Close()
		if err != nil || ap.Approved != tc.ok {
			t.Errorf("%s: %+v, %v", tc.body, ap, err)
		}
	}
}
