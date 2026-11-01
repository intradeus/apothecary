package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
)

// secret is the token every failure test runs with: no error may carry it.
const secret = "glpat-s3cr3tvalue"

const proj = "/api/v4/projects/7"

type reply struct {
	status int
	body   string
}

// answer serves canned replies by mux pattern ("GET /api/v4/user"); anything
// not listed is a 404.
func answer(t *testing.T, routes map[string]reply) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for pattern, r := range routes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
			if r.status != 0 {
				w.WriteHeader(r.status)
			}
			_, _ = w.Write([]byte(r.body))
		})
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func adapterFor(ts *httptest.Server) *Adapter {
	return New(Config{BaseURL: ts.URL, Token: secret, ProjectID: 7})
}

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("error carries the token: %v", err)
	}
}

func TestAPIFailuresAreNamedAndNeverCarryTheToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply reply
		want  []string
	}{
		{"unauthorized", reply{401, `{"message":"401 Unauthorized"}`}, []string{"401", "Unauthorized"}},
		{"forbidden", reply{403, `{"message":"403 Forbidden"}`}, []string{"403", "Forbidden"}},
		{"rate limited", reply{429, `Retry later`}, []string{"429", "Retry later"}},
		{"not found", reply{404, `{"message":"404 Not Found"}`}, []string{"404"}},
		{"server error", reply{502, `<html>Bad gateway</html>`}, []string{"502", "Bad gateway"}},
		{"malformed body", reply{200, `{"username":`}, []string{"decode"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := answer(t, map[string]reply{"GET /api/v4/user": tc.reply})
			name, err := adapterFor(ts).Whoami(context.Background())
			if err == nil || name != "" {
				t.Fatalf("name %q, err %v", name, err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err %q does not say %q", err, w)
				}
			}
			noSecret(t, err)
		})
	}
	ok := answer(t, map[string]reply{"GET /api/v4/user": {body: `{"username":"lu"}`}})
	if name, err := adapterFor(ok).Whoami(context.Background()); err != nil || name != "lu" {
		t.Errorf("whoami = %q, %v", name, err)
	}
}

func TestAPITransportFailures(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	a := adapterFor(dead)
	dead.Close()
	_, err := a.Whoami(context.Background())
	if err == nil {
		t.Error("a dead server is an error")
	}
	noSecret(t, err)

	live := answer(t, map[string]reply{"GET /api/v4/user": {body: `{"username":"lu"}`}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapterFor(live).Whoami(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled call: %v", err)
	}
	bad := New(Config{BaseURL: "http://bad host", Token: secret, ProjectID: 7})
	if _, err := bad.Whoami(context.Background()); err == nil {
		t.Error("an unparsable base URL is an error")
	}
	if _, err := adapterFor(live).api(context.Background(), http.MethodPost, "/user", make(chan int), nil); err == nil {
		t.Error("a body that cannot marshal is an error")
	}
}

func TestCheckAccessEdges(t *testing.T) {
	ctx := context.Background()
	if err := New(Config{ProjectID: 7}).CheckAccess(ctx, repoport.RepoRef{}); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("no token: %v", err)
	}
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"permissions":{"project_access":null,"group_access":{"access_level":40}}}`, true},
		{`{"permissions":{"project_access":{"access_level":20},"group_access":{"access_level":30}}}`, true},
		{`{"permissions":{"project_access":{"access_level":29},"group_access":null}}`, false},
		{`{"permissions":{"project_access":null,"group_access":null}}`, false},
	} {
		ts := answer(t, map[string]reply{"GET " + proj: {body: tc.body}})
		if err := adapterFor(ts).CheckAccess(ctx, repoport.RepoRef{}); (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.body, err)
		}
	}
	ts := answer(t, map[string]reply{"GET " + proj: {404, `{"message":"404 Project Not Found"}`}})
	err := adapterFor(ts).CheckAccess(ctx, repoport.RepoRef{})
	if err == nil || !strings.Contains(err.Error(), "project 7 unreachable") {
		t.Errorf("missing project: %v", err)
	}
	noSecret(t, err)
}

func TestFileExistsFailures(t *testing.T) {
	ts := answer(t, map[string]reply{
		"HEAD " + proj + "/repository/files/{path}": {status: 500},
		"GET " + proj + "/repository/tree":          {500, `boom`},
	})
	a := adapterFor(ts)
	if ok, err := a.FileExists(context.Background(), repoport.RepoRef{}, "a/b.md"); err == nil || ok {
		t.Errorf("file = %v, %v", ok, err)
	}
	if ok, err := a.FileExists(context.Background(), repoport.RepoRef{}, "a/"); err == nil || ok {
		t.Errorf("dir = %v, %v", ok, err)
	}

	missing := answer(t, map[string]reply{"GET " + proj + "/repository/tree": {404, `{"message":"404 Tree Not Found"}`}})
	if ok, err := adapterFor(missing).FileExists(context.Background(), repoport.RepoRef{}, "a/"); err != nil || ok {
		t.Errorf("no tree = %v, %v", ok, err)
	}
}

func TestFileExistsEscapesThePathAsOneSegment(t *testing.T) {
	var escaped, ref string
	mux := http.NewServeMux()
	mux.HandleFunc("HEAD "+proj+"/repository/files/{path}", func(w http.ResponseWriter, r *http.Request) {
		escaped, ref = r.URL.EscapedPath(), r.URL.Query().Get("ref")
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if ok, err := adapterFor(ts).FileExists(context.Background(), repoport.RepoRef{}, "docs/a b.md"); err != nil || !ok {
		t.Fatalf("exists = %v, %v", ok, err)
	}
	if escaped != proj+"/repository/files/docs%2Fa%20b.md" || ref != "main" {
		t.Errorf("path %q ref %q", escaped, ref)
	}
}

func TestTargetOr(t *testing.T) {
	for _, tc := range []struct{ target, def, want string }{
		{"stack/1", "dev", "stack/1"},
		{"", "dev", "dev"},
		{"", "", "main"},
	} {
		if got := targetOr(tc.target, repoport.RepoRef{DefaultBranch: tc.def}); got != tc.want {
			t.Errorf("targetOr(%q, %q) = %q", tc.target, tc.def, got)
		}
	}
}

func TestCreateDraftMREdges(t *testing.T) {
	var titles []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+proj+"/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		titles = append(titles, b["title"].(string))
		if b["remove_source_branch"] != true {
			t.Errorf("source branch stays: %v", b)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"iid":3,"web_url":"u"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if _, err := adapterFor(ts).CreateDraftMR(context.Background(), repoport.RepoRef{}, "b", "", "Draft: already", ""); err != nil {
		t.Fatal(err)
	}
	if titles[0] != "Draft: already" {
		t.Errorf("a marked title is not marked twice: %q", titles[0])
	}

	for _, tc := range []struct {
		name   string
		routes map[string]reply
		want   string
	}{
		{"refused", map[string]reply{"POST " + proj + "/merge_requests": {422, `{"message":"Branch missing"}`}}, "Branch missing"},
		// A conflict with nothing to return is the conflict, never a zero MRRef.
		{"conflict, nothing listed", map[string]reply{
			"POST " + proj + "/merge_requests": {409, `{"message":"Another open merge request exists"}`},
			"GET " + proj + "/merge_requests":  {body: `[]`},
		}, "Another open merge request"},
		{"conflict, lookup fails", map[string]reply{
			"POST " + proj + "/merge_requests": {409, `{"message":"Another open merge request exists"}`},
			"GET " + proj + "/merge_requests":  {500, `boom`},
		}, "Another open merge request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := adapterFor(answer(t, tc.routes)).CreateDraftMR(context.Background(), repoport.RepoRef{}, "b", "", "t", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) || ref != (repoport.MRRef{}) {
				t.Errorf("ref %+v, err %v", ref, err)
			}
		})
	}
}

func TestAddMRLabelsFailure(t *testing.T) {
	ts := answer(t, map[string]reply{"PUT " + proj + "/merge_requests/5": {403, `{"message":"403 Forbidden"}`}})
	if err := adapterFor(ts).AddMRLabels(context.Background(), repoport.MRRef{IID: 5}, []string{"a"}); err == nil {
		t.Error("a refused label is an error")
	}
	if err := adapterFor(ts).AddMRLabels(context.Background(), repoport.MRRef{IID: 5}, nil); err != nil {
		t.Errorf("no labels is no call: %v", err)
	}
}

func TestFindMR(t *testing.T) {
	var query string
	body, status := `[{"iid":4,"web_url":"u"}]`, 200
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+proj+"/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a := adapterFor(ts)
	ctx := context.Background()
	repo := repoport.RepoRef{DefaultBranch: "dev"}

	ref, ok, err := a.FindMR(ctx, repo, "feat/x", "")
	if err != nil || !ok || ref.IID != 4 {
		t.Fatalf("found = %+v, %v, %v", ref, ok, err)
	}
	if query != "state=opened&source_branch=feat%2Fx&target_branch=dev" {
		t.Errorf("query = %q", query)
	}
	body = `[]`
	if _, ok, err := a.FindMR(ctx, repo, "feat/x", "stack"); err != nil || ok {
		t.Errorf("none = %v, %v", ok, err)
	}
	status, body = 500, `boom`
	if _, ok, err := a.FindMR(ctx, repo, "feat/x", ""); err == nil || ok {
		t.Errorf("failure = %v, %v", ok, err)
	}
}

func TestBranchURLFailures(t *testing.T) {
	ts := answer(t, map[string]reply{
		"GET " + proj + "/repository/branches/down": {500, `boom`},
		"GET " + proj + "/repository/branches/bare": {body: `{"name":"bare"}`},
	})
	a := adapterFor(ts)
	if u, ok, err := a.BranchURL(context.Background(), repoport.RepoRef{}, "down"); err == nil || ok || u != "" {
		t.Errorf("500 = %q, %v, %v", u, ok, err)
	}
	if u, ok, err := a.BranchURL(context.Background(), repoport.RepoRef{}, "bare"); err != nil || ok || u != "" {
		t.Errorf("no url = %q, %v, %v", u, ok, err)
	}
}

func TestMRStatusFailuresAndClosed(t *testing.T) {
	ts := answer(t, map[string]reply{
		"GET " + proj + "/merge_requests/1":         {500, `boom`},
		"GET " + proj + "/merge_requests/2":         {body: `{"state":"opened","detailed_merge_status":"conflict","target_branch":"main","sha":"h"}`},
		"GET " + proj + "/repository/branches/main": {502, `bad`},
		"GET " + proj + "/merge_requests/3":         {body: `{"state":"closed","detailed_merge_status":"mergeable","sha":"h"}`},
		"GET " + proj + "/merge_requests/4":         {body: `{"state":"opened","has_conflicts":true,"detailed_merge_status":"broken_status"}`},
	})
	a := adapterFor(ts)
	ctx := context.Background()
	if st, err := a.MRStatus(ctx, repoport.MRRef{IID: 1}); err == nil || st != (repoport.MRStatus{}) {
		t.Errorf("read failure = %+v, %v", st, err)
	}
	if st, err := a.MRStatus(ctx, repoport.MRRef{IID: 2}); err == nil || st != (repoport.MRStatus{}) {
		t.Errorf("target failure = %+v, %v", st, err)
	}
	// A closed merge request is not merge-ready, whatever GitLab calculates.
	if st, err := a.MRStatus(ctx, repoport.MRRef{IID: 3}); err != nil || st.State != repoport.MRClosed || st.MergeableNow {
		t.Errorf("closed = %+v, %v", st, err)
	}
	// A conflict with no target branch named needs no target tip.
	if st, err := a.MRStatus(ctx, repoport.MRRef{IID: 4}); err != nil || st.Mergeable != repoport.MergeConflicts || st.TargetSHA != "" {
		t.Errorf("has_conflicts = %+v, %v", st, err)
	}
}

func TestMergeableOf(t *testing.T) {
	for _, tc := range []struct {
		detailed  string
		conflicts bool
		want      repoport.Mergeable
	}{
		{"checking", true, repoport.MergeUnknown},
		{"unchecked", false, repoport.MergeUnknown},
		{"preparing", true, repoport.MergeUnknown},
		{"approvals_syncing", false, repoport.MergeUnknown},
		{"conflict", false, repoport.MergeConflicts},
		{"need_rebase", true, repoport.MergeConflicts},
		{"mergeable", false, repoport.MergeClean},
	} {
		if got := mergeableOf(tc.detailed, tc.conflicts); got != tc.want {
			t.Errorf("mergeableOf(%q, %v) = %s", tc.detailed, tc.conflicts, got)
		}
	}
}

func TestPipelineState(t *testing.T) {
	for status, want := range map[string]repoport.CheckState{
		"success": repoport.CheckSuccess, "failed": repoport.CheckFailure,
		"canceled": repoport.CheckNeutral, "skipped": repoport.CheckNeutral, "manual": repoport.CheckNeutral,
		"created": repoport.CheckPending, "running": repoport.CheckPending, "scheduled": repoport.CheckPending, "new-word": repoport.CheckPending,
	} {
		if got := pipelineState(status); got != want {
			t.Errorf("%s = %s", status, got)
		}
	}
}

func TestMRChecksEdges(t *testing.T) {
	ctx := context.Background()
	mr := repoport.MRRef{IID: 5}

	down := answer(t, map[string]reply{"GET " + proj + "/merge_requests/5": {500, `boom`}})
	if c, err := adapterFor(down).MRChecks(ctx, mr, "h1"); err == nil || c.Head != "h1" || len(c.Items) != 0 {
		t.Errorf("read failure = %+v, %v", c, err)
	}

	none := answer(t, map[string]reply{"GET " + proj + "/merge_requests/5": {body: `{"head_pipeline":null}`}})
	if c, err := adapterFor(none).MRChecks(ctx, mr, "h1"); err != nil || len(c.Items) != 0 {
		t.Errorf("no pipeline = %+v, %v", c, err)
	}

	// The head moved past the pipeline: its green says nothing about the head.
	stale := answer(t, map[string]reply{"GET " + proj + "/merge_requests/5": {body: `{"head_pipeline":{"id":1,"sha":"old","status":"success"}}`}})
	if c, err := adapterFor(stale).MRChecks(ctx, mr, "h1"); err != nil || c.State != repoport.CheckPending {
		t.Errorf("stale = %+v, %v", c, err)
	}

	failed := `{"head_pipeline":{"id":1,"sha":"h1","status":"failed","web_url":"pipe"}}`
	jobs := answer(t, map[string]reply{
		"GET " + proj + "/merge_requests/5": {body: failed},
		"GET " + proj + "/pipelines/1/jobs": {500, `boom`},
	})
	if _, err := adapterFor(jobs).MRChecks(ctx, mr, "h1"); err == nil {
		t.Error("unreadable jobs are an error")
	}

	// Only allowed failures: the pipeline itself stays the failed check.
	allowed := answer(t, map[string]reply{
		"GET " + proj + "/merge_requests/5": {body: failed},
		"GET " + proj + "/pipelines/1/jobs": {body: `[{"name":"flaky","allow_failure":true}]`},
	})
	c, err := adapterFor(allowed).MRChecks(ctx, mr, "h1")
	if err != nil || len(c.Items) != 1 || c.Items[0].Name != "pipeline" || c.State != repoport.CheckFailure {
		t.Errorf("allowed failure = %+v, %v", c, err)
	}
}

func TestCIFailuresEdges(t *testing.T) {
	ctx := context.Background()
	mr := repoport.MRRef{IID: 5}
	failed := `{"head_pipeline":{"id":1,"sha":"h1","status":"failed","web_url":"pipe"}}`

	for _, tc := range []struct {
		name   string
		routes map[string]reply
		head   string
		want   []repoport.CIFailure
		isErr  bool
	}{
		{"read fails", map[string]reply{"GET " + proj + "/merge_requests/5": {500, `boom`}}, "h1", nil, true},
		{"no pipeline", map[string]reply{"GET " + proj + "/merge_requests/5": {body: `{}`}}, "h1", nil, false},
		{"older head", map[string]reply{"GET " + proj + "/merge_requests/5": {body: failed}}, "h2", nil, false},
		{"not failed", map[string]reply{"GET " + proj + "/merge_requests/5": {body: `{"head_pipeline":{"id":1,"sha":"h1","status":"running"}}`}}, "h1", nil, false},
		{"project fails", map[string]reply{"GET " + proj + "/merge_requests/5": {body: failed}, "GET " + proj: {500, `boom`}}, "h1", nil, true},
		{"jobs fail", map[string]reply{"GET " + proj + "/merge_requests/5": {body: failed}, "GET " + proj: {body: `{}`},
			"GET " + proj + "/pipelines/1/jobs": {500, `boom`}}, "h1", nil, true},
		{"no job of its own", map[string]reply{"GET " + proj + "/merge_requests/5": {body: failed}, "GET " + proj: {body: `{"ci_config_path":"ci/main.yml"}`},
			"GET " + proj + "/pipelines/1/jobs": {body: `[{"name":"flaky","allow_failure":true}]`}}, "h1",
			[]repoport.CIFailure{{Name: "pipeline", URL: "pipe", File: "ci/main.yml"}}, false},
		{"default file, any head", map[string]reply{"GET " + proj + "/merge_requests/5": {body: failed}, "GET " + proj: {body: `{}`},
			"GET " + proj + "/pipelines/1/jobs": {body: `[{"name":"t","stage":"test","web_url":"j","failure_reason":"script_failure"}]`}}, "",
			[]repoport.CIFailure{{Name: "t", URL: "j", File: ".gitlab-ci.yml", Job: "t", Stage: "test", Summary: "script failure"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := adapterFor(answer(t, tc.routes)).CIFailures(ctx, mr, tc.head)
			if (err != nil) != tc.isErr || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, %v\nwant %+v", got, err, tc.want)
			}
		})
	}
}

func TestMRApprovalFailure(t *testing.T) {
	ts := answer(t, map[string]reply{"GET " + proj + "/merge_requests/5/approvals": {500, `boom`}})
	if ap, err := adapterFor(ts).MRApproval(context.Background(), repoport.MRRef{IID: 5}); err == nil || ap.Approved {
		t.Errorf("failure = %+v, %v", ap, err)
	}
}

func TestMRThreadsEdges(t *testing.T) {
	ctx := context.Background()
	down := answer(t, map[string]reply{"GET " + proj + "/merge_requests/5/discussions": {500, `boom`}})
	if th, err := adapterFor(down).MRThreads(ctx, repoport.MRRef{IID: 5}); err == nil || th != nil {
		t.Errorf("failure = %v, %v", th, err)
	}

	first := "[" + strings.TrimSuffix(strings.Repeat(`{"id":"sys","notes":[{"id":1,"system":true,"body":"pushed"}]},`, 100), ",") + "]"
	second := `[
	  {"id":"deleted-file","notes":[{"id":2,"body":"why gone?","resolvable":true,"resolved":false,"author":{"username":"r"},
	    "position":{"new_path":"","old_path":"gone.go","new_line":0,"old_line":9}}]},
	  {"id":"half","notes":[{"id":3,"body":"a","resolvable":true,"resolved":true,"position":{"new_path":"a.go","new_line":4}},
	    {"id":4,"body":"b","resolvable":true,"resolved":false}]},
	  {"id":"all","notes":[{"id":5,"body":"c","resolvable":true,"resolved":true}]},
	  {"id":"general","notes":[{"id":6,"body":"d"}]}]`
	h, calls := paged(first, second)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+proj+"/merge_requests/5/discussions", h)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	th, err := adapterFor(ts).MRThreads(ctx, repoport.MRRef{IID: 5})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(th) != 4 {
		t.Fatalf("%d calls, threads %+v", calls.Load(), th)
	}
	if th[0].Path != "gone.go" || th[0].Line != 9 || th[0].Resolved {
		t.Errorf("a note on a deleted file anchors on the old side: %+v", th[0])
	}
	if th[1].Path != "a.go" || th[1].Line != 4 || th[1].Resolved {
		t.Errorf("one open note leaves the thread open: %+v", th[1])
	}
	if !th[2].Resolved || th[3].Resolved {
		t.Errorf("resolved %v, unresolvable %v", th[2], th[3])
	}
}

func paged(pages ...string) (http.HandlerFunc, *atomic.Int32) {
	var calls atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if n < 1 || n > len(pages) {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(pages[n-1]))
	}, &calls
}

func repeat(n int, item string) string {
	return "[" + strings.TrimSuffix(strings.Repeat(item+",", n), ",") + "]"
}

func TestReplyAndResolveEscapeTheThreadAndReportFailure(t *testing.T) {
	var paths []string
	fail := false
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		if fail {
			w.WriteHeader(500)
		}
		_, _ = w.Write([]byte(`{}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a := adapterFor(ts)
	ctx := context.Background()
	if err := a.ReplyThread(ctx, repoport.MRRef{IID: 5}, "a/b", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := a.ResolveThread(ctx, repoport.MRRef{IID: 5}, "a/b"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST " + proj + "/merge_requests/5/discussions/a%2Fb/notes?",
		"PUT " + proj + "/merge_requests/5/discussions/a%2Fb?resolved=true",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %q", paths)
	}
	fail = true
	if err := a.ReplyThread(ctx, repoport.MRRef{IID: 5}, "x", "hi"); err == nil {
		t.Error("a failed reply is an error")
	}
	if err := a.ResolveThread(ctx, repoport.MRRef{IID: 5}, "x"); err == nil {
		t.Error("a failed resolve is an error")
	}
}

func TestUploadAttachmentFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply reply
		want  string
	}{
		{"too large", reply{413, `{"message":"413 Payload Too Large"}`}, "413"},
		{"malformed", reply{201, `not json`}, "decode"},
		{"no url", reply{201, `{}`}, "has no url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := answer(t, map[string]reply{"POST " + proj + "/uploads": tc.reply})
			att, err := adapterFor(ts).UploadAttachment(context.Background(), repoport.RepoRef{}, "a.txt", "text/plain", []byte("x"))
			if err == nil || !strings.Contains(err.Error(), tc.want) || att != (repoport.Attachment{}) {
				t.Errorf("att %+v, err %v", att, err)
			}
			noSecret(t, err)
		})
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	a := adapterFor(dead)
	dead.Close()
	if _, err := a.UploadAttachment(context.Background(), repoport.RepoRef{}, "a.txt", "text/plain", nil); err == nil {
		t.Error("a dead server is an error")
	} else {
		noSecret(t, err)
	}
	bad := New(Config{BaseURL: "http://bad host", Token: secret, ProjectID: 7})
	if _, err := bad.UploadAttachment(context.Background(), repoport.RepoRef{}, "a.txt", "text/plain", nil); err == nil {
		t.Error("an unparsable base URL is an error")
	}
}

// A hostile filename cannot break out of its multipart header; the file and
// its type arrive whole, and a full_path wins over the built address.
func TestUploadAttachmentFilenameAndFullPath(t *testing.T) {
	var filename, partType, data, token string
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+proj+"/uploads", func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("PRIVATE-TOKEN")
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Error(err)
			return
		}
		part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		b, _ := io.ReadAll(part)
		filename, partType, data = part.FileName(), part.Header.Get("Content-Type"), string(b)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"url":"/uploads/abc/a.png","full_path":"/-/project/7/uploads/abc/a.png","markdown":"![a](/uploads/abc/a.png)"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	att, err := adapterFor(ts).UploadAttachment(context.Background(), repoport.RepoRef{}, `we"ird\.png`, "image/png", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if filename != `we"ird\.png` || partType != "image/png" || data != "payload" || token != secret {
		t.Errorf("name %q type %q data %q token %q", filename, partType, data, token)
	}
	want := repoport.Attachment{URL: ts.URL + "/-/project/7/uploads/abc/a.png", Link: "/uploads/abc/a.png", Markdown: "![a](/uploads/abc/a.png)"}
	if att != want {
		t.Errorf("att = %+v", att)
	}
}

func TestGetMRMapsTheMergeRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want repoport.MRInfo
	}{
		{"own branch",
			`{"iid":5,"web_url":"u","title":"t","description":"d","state":"opened","draft":true,"source_branch":"feat","target_branch":"main","sha":"h",
			  "source_project_id":7,"target_project_id":7,"author":{"username":"lu"}}`,
			repoport.MRInfo{Ref: repoport.MRRef{IID: 5, URL: "u"}, Title: "t", Body: "d", Author: "lu", State: repoport.MROpen, Draft: true,
				Source: "feat", Target: "main", Head: "h", HeadRef: "refs/merge-requests/5/head"}},
		{"fork", `{"iid":5,"state":"closed","source_project_id":8,"target_project_id":7}`,
			repoport.MRInfo{Ref: repoport.MRRef{IID: 5}, State: repoport.MRClosed, HeadRef: "refs/merge-requests/5/head", Fork: true}},
		{"merged, source unknown", `{"iid":5,"state":"merged","target_project_id":7}`,
			repoport.MRInfo{Ref: repoport.MRRef{IID: 5}, State: repoport.MRMerged, HeadRef: "refs/merge-requests/5/head"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := answer(t, map[string]reply{"GET " + proj + "/merge_requests/5": {body: tc.body}})
			got, err := adapterFor(ts).GetMR(context.Background(), 5)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, %v\nwant %+v", got, err, tc.want)
			}
		})
	}
	ts := answer(t, map[string]reply{"GET " + proj + "/merge_requests/5": {404, `{"message":"404 Not found"}`}})
	if got, err := adapterFor(ts).GetMR(context.Background(), 5); err == nil || !reflect.DeepEqual(got, repoport.MRInfo{}) {
		t.Errorf("missing = %+v, %v", got, err)
	}
}

func TestListMRsPagesToThreeHundred(t *testing.T) {
	var calls atomic.Int32
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+proj+"/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(repeat(100, `{"iid":1,"state":"opened"}`)))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	got, err := adapterFor(ts).ListMRs(context.Background())
	if err != nil || len(got) != 300 || calls.Load() != 3 {
		t.Errorf("%d in %d calls, %v", len(got), calls.Load(), err)
	}
	if !strings.Contains(query, "state=opened") || !strings.Contains(query, "order_by=created_at") {
		t.Errorf("query = %q", query)
	}
	short := answer(t, map[string]reply{"GET " + proj + "/merge_requests": {body: `[{"iid":1}]`}})
	if got, err := adapterFor(short).ListMRs(context.Background()); err != nil || len(got) != 1 {
		t.Errorf("short = %+v, %v", got, err)
	}
	down := answer(t, map[string]reply{"GET " + proj + "/merge_requests": {500, `boom`}})
	if got, err := adapterFor(down).ListMRs(context.Background()); err == nil || got != nil {
		t.Errorf("failure = %+v, %v", got, err)
	}
}

func TestMergeMRBodyAndRefusals(t *testing.T) {
	var body map[string]any
	status := 200
	mux := http.NewServeMux()
	mux.HandleFunc("PUT "+proj+"/merge_requests/5/merge", func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"no"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a := adapterFor(ts)
	ctx := context.Background()
	mr := repoport.MRRef{IID: 5}

	// Rebase is refused before anything reaches GitLab.
	var refused *repoport.ErrMergeRefused
	if err := a.MergeMR(ctx, mr, repoport.MergeOptions{Method: repoport.MergeMethodRebase}); !errors.As(err, &refused) || !strings.Contains(refused.Reason, "fast-forward") {
		t.Errorf("rebase = %v", err)
	}
	if body != nil {
		t.Errorf("rebase reached GitLab: %v", body)
	}
	if err := a.MergeMR(ctx, mr, repoport.MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"squash": false, "should_remove_source_branch": false}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %v", body)
	}
	for _, code := range []int{405, 406, 409, 422} {
		status = code
		if err := a.MergeMR(ctx, mr, repoport.MergeOptions{}); !errors.As(err, &refused) {
			t.Errorf("%d = %v", code, err)
		}
	}
	status = 503
	if err := a.MergeMR(ctx, mr, repoport.MergeOptions{}); err == nil || errors.As(err, &refused) {
		t.Errorf("503 = %v", err)
	}
}

// postReviewStub is a merge request with one file: lines 1-2 of a.go in the diff.
type postReviewStub struct {
	discussions string
	discussion  func(w http.ResponseWriter)
	note        func(w http.ResponseWriter)
	posted      []map[string]any
	notes       []string
}

func (s *postReviewStub) serve(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+proj+"/merge_requests/5/discussions", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(s.discussions)) })
	mux.HandleFunc("GET "+proj+"/merge_requests/5", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"diff_refs":{"base_sha":"b","head_sha":"h","start_sha":"s"}}`))
	})
	mux.HandleFunc("GET "+proj+"/merge_requests/5/diffs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"old_path":"old.go","new_path":"a.go","diff":"@@ -1,1 +1,2 @@\n package a\n+func x() {}"}]`))
	})
	mux.HandleFunc("POST "+proj+"/merge_requests/5/discussions", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		s.posted = append(s.posted, b)
		if s.discussion != nil {
			s.discussion(w)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST "+proj+"/merge_requests/5/notes", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		s.notes = append(s.notes, b["body"])
		if s.note != nil {
			s.note(w)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestPostReviewAnchors(t *testing.T) {
	rev := repoport.Review{ID: "run/p-1/post", Summary: "ok", Notes: []repoport.ReviewNote{{Path: "a.go", Line: 1, Body: " context "}, {Path: "a.go", Line: 2, Body: "added"}}}
	s := &postReviewStub{discussions: `[]`}
	res, err := adapterFor(s.serve(t)).PostReview(context.Background(), repoport.MRRef{IID: 5}, rev)
	if err != nil || res.Inline != 2 || res.Folded != 0 || res.Already {
		t.Fatalf("res %+v, %v", res, err)
	}
	ctxLine := s.posted[0]["position"].(map[string]any)
	if ctxLine["old_line"] != float64(1) || ctxLine["old_path"] != "old.go" || ctxLine["new_path"] != "a.go" ||
		ctxLine["base_sha"] != "b" || ctxLine["head_sha"] != "h" || ctxLine["start_sha"] != "s" {
		t.Errorf("context line position = %v", ctxLine)
	}
	if _, has := s.posted[1]["position"].(map[string]any)["old_line"]; has {
		t.Errorf("an added line has no old side: %v", s.posted[1])
	}
	if b, _ := s.posted[0]["body"].(string); !strings.HasPrefix(b, "context\n\n") {
		t.Errorf("note body = %q", b)
	}
}

// GitLab declining to anchor a note folds it into the summary; any other
// failure stops the review.
func TestPostReviewFoldsWhatGitLabWillNotAnchor(t *testing.T) {
	rev := repoport.Review{ID: "run/p-1/post", Notes: []repoport.ReviewNote{{Path: "a.go", Line: 2, Body: "here"}}}
	for _, code := range []int{400, 422} {
		s := &postReviewStub{discussions: `[]`, discussion: func(w http.ResponseWriter) { w.WriteHeader(code) }}
		res, err := adapterFor(s.serve(t)).PostReview(context.Background(), repoport.MRRef{IID: 5}, rev)
		if err != nil || res.Inline != 0 || res.Folded != 1 || len(s.notes) != 1 || !strings.Contains(s.notes[0], "`a.go:2` — here") {
			t.Errorf("%d: res %+v, %v, notes %q", code, res, err, s.notes)
		}
	}
	s := &postReviewStub{discussions: `[]`, discussion: func(w http.ResponseWriter) { w.WriteHeader(500) }}
	res, err := adapterFor(s.serve(t)).PostReview(context.Background(), repoport.MRRef{IID: 5}, rev)
	if err == nil || res != (repoport.ReviewPosted{}) || len(s.notes) != 0 {
		t.Errorf("500: res %+v, %v, notes %q", res, err, s.notes)
	}
}

// A retry posts only what is missing: here the note landed and the summary
// did not.
func TestPostReviewRetryPostsTheRest(t *testing.T) {
	rev := repoport.Review{ID: "run/p-1/post", Summary: "ok", Notes: []repoport.ReviewNote{{Path: "a.go", Line: 2, Body: "n"}}}
	enc, _ := json.Marshal("n\n\n" + repoport.NoteMarker(rev.ID, 0, ""))
	s := &postReviewStub{discussions: `[{"id":"d","notes":[{"id":1,"body":` + string(enc) + `}]}]`}
	res, err := adapterFor(s.serve(t)).PostReview(context.Background(), repoport.MRRef{IID: 5}, rev)
	if err != nil || res.Inline != 1 || res.Already || len(s.posted) != 0 || len(s.notes) != 1 {
		t.Fatalf("res %+v, %v, posted %v notes %v", res, err, s.posted, s.notes)
	}

	// Everything there: nothing posted, and the review says so.
	sum, _ := json.Marshal("ok\n\n" + repoport.ReviewMarker(rev.ID, "summary"))
	s = &postReviewStub{discussions: `[{"id":"d","notes":[{"id":1,"body":` + string(enc) + `},{"id":2,"body":` + string(sum) + `}]}]`}
	res, err = adapterFor(s.serve(t)).PostReview(context.Background(), repoport.MRRef{IID: 5}, rev)
	if err != nil || !res.Already || res.Inline != 1 || len(s.posted) != 0 || len(s.notes) != 0 {
		t.Errorf("res %+v, %v, posted %v notes %v", res, err, s.posted, s.notes)
	}

	// Nothing to say at all posts nothing.
	s = &postReviewStub{discussions: `[]`}
	res, err = adapterFor(s.serve(t)).PostReview(context.Background(), repoport.MRRef{IID: 5}, repoport.Review{ID: "r"})
	if err != nil || !res.Already || len(s.notes) != 0 {
		t.Errorf("empty review: %+v, %v, %v", res, err, s.notes)
	}
}

func TestPostReviewFailures(t *testing.T) {
	rev := repoport.Review{ID: "r", Summary: "ok"}
	s := &postReviewStub{discussions: `[]`, note: func(w http.ResponseWriter) { w.WriteHeader(500) }}
	if res, err := adapterFor(s.serve(t)).PostReview(context.Background(), repoport.MRRef{IID: 5}, rev); err == nil || res != (repoport.ReviewPosted{}) {
		t.Errorf("summary failure = %+v, %v", res, err)
	}
	for name, routes := range map[string]map[string]reply{
		"threads": {"GET " + proj + "/merge_requests/5/discussions": {500, `boom`}},
		"diff refs": {"GET " + proj + "/merge_requests/5/discussions": {body: `[]`},
			"GET " + proj + "/merge_requests/5": {500, `boom`}},
		"diffs": {"GET " + proj + "/merge_requests/5/discussions": {body: `[]`}, "GET " + proj + "/merge_requests/5": {body: `{}`},
			"GET " + proj + "/merge_requests/5/diffs": {500, `boom`}},
	} {
		t.Run(name, func(t *testing.T) {
			if res, err := adapterFor(answer(t, routes)).PostReview(context.Background(), repoport.MRRef{IID: 5}, rev); err == nil || res != (repoport.ReviewPosted{}) {
				t.Errorf("res %+v, %v", res, err)
			}
		})
	}

	// Diffs past the first page still anchor.
	h, calls := paged(repeat(100, `{"new_path":"x.go","diff":""}`), `[{"new_path":"z.go","old_path":"z.go","diff":"@@ -1,1 +1,2 @@\n a\n+b"}]`)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+proj+"/merge_requests/5/discussions", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET "+proj+"/merge_requests/5", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	mux.HandleFunc("GET "+proj+"/merge_requests/5/diffs", h)
	mux.HandleFunc("POST "+proj+"/merge_requests/5/discussions", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	ts := httptest.NewServer(mux)
	defer ts.Close()
	res, err := adapterFor(ts).PostReview(context.Background(), repoport.MRRef{IID: 5},
		repoport.Review{ID: "r", Notes: []repoport.ReviewNote{{Path: "z.go", Line: 2, Body: "n"}}})
	if err != nil || res.Inline != 1 || calls.Load() != 2 {
		t.Errorf("res %+v, %v, %d", res, err, calls.Load())
	}
}

func TestAccount(t *testing.T) {
	ctx := context.Background()
	if err := NewAccount("", "").Test(ctx); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("no token: %v", err)
	}

	h, calls := paged(repeat(100, `{"id":7,"path_with_namespace":"g/a","http_url_to_repo":"u","default_branch":"main"}`), `[{"id":8,"path_with_namespace":"g/b"}]`)
	bh, bcalls := paged(repeat(100, `{"name":"x"}`), `[{"name":"main","default":true}]`)
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"username":"lu"}`)) })
	mux.HandleFunc("GET /api/v4/projects", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		h(w, r)
	})
	mux.HandleFunc("GET /api/v4/projects/7/repository/branches", bh)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ac := NewAccount(ts.URL, secret)
	if err := ac.Test(ctx); err != nil {
		t.Errorf("good token: %v", err)
	}
	repos, err := ac.ListRepos(ctx)
	if err != nil || len(repos) != 101 || calls.Load() != 2 {
		t.Fatalf("%d repos, %d calls, %v", len(repos), calls.Load(), err)
	}
	// The locator is the project id: the rest of the adapter is bound by it.
	if want := (Repo{Name: "g/a", URL: "u", Locator: "7", DefaultBranch: "main"}); repos[0] != want {
		t.Errorf("repo = %+v", repos[0])
	}
	if repos[100].Locator != "8" {
		t.Errorf("last repo = %+v", repos[100])
	}
	if !strings.Contains(query, "min_access_level=30") || !strings.Contains(query, "membership=true") {
		t.Errorf("only pushable repositories are pickable: %q", query)
	}
	branches, err := ac.ListBranches(ctx, "7")
	if err != nil || len(branches) != 101 || bcalls.Load() != 2 || !branches[100].Default || branches[0].Default {
		t.Errorf("%d branches, %d calls, %v", len(branches), bcalls.Load(), err)
	}

	down := answer(t, map[string]reply{
		"GET /api/v4/user":                           {401, `{"message":"401 Unauthorized"}`},
		"GET /api/v4/projects":                       {500, `boom`},
		"GET /api/v4/projects/7/repository/branches": {500, `boom`},
	})
	bad := NewAccount(down.URL, secret)
	err = bad.Test(ctx)
	if err == nil {
		t.Error("a bad token is an error")
	}
	noSecret(t, err)
	if got, err := bad.ListRepos(ctx); err == nil || got != nil {
		t.Errorf("repos failure = %v, %v", got, err)
	}
	if got, err := bad.ListBranches(ctx, "7"); err == nil || got != nil {
		t.Errorf("branches failure = %v, %v", got, err)
	}
}

func issuesFor(ts *httptest.Server, projects ...string) *Issues {
	return NewIssues(IssuesConfig{BaseURL: ts.URL, Token: secret, Projects: projects})
}

func TestIssuesTest(t *testing.T) {
	ctx := context.Background()
	if err := NewIssues(IssuesConfig{}).Test(ctx); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("no token: %v", err)
	}
	bad := answer(t, map[string]reply{"GET /api/v4/user": {401, `{"message":"401 Unauthorized"}`}})
	err := issuesFor(bad).Test(ctx)
	if err == nil {
		t.Error("a bad token is an error")
	}
	noSecret(t, err)
}

func TestIssuesParseKey(t *testing.T) {
	one := NewIssues(IssuesConfig{Projects: []string{" /g/p.git ", "  "}})
	two := NewIssues(IssuesConfig{Projects: []string{"g/a", "g/b"}})
	for _, tc := range []struct {
		s       *Issues
		key     string
		project string
		iid     int
		wantErr string
	}{
		{one, "g/sub/p#7", "g/sub/p", 7, ""},
		{one, "#7", "g/p", 7, ""},
		{one, " 7 ", "g/p", 7, ""},
		{two, "#7", "", 0, "names no project"},
		{one, "bare#7", "", 0, "is not group/project#number"},
		{one, "", "", 0, "is not group/project#number"},
	} {
		project, iid, err := tc.s.parseKey(tc.key)
		if project != tc.project || iid != tc.iid {
			t.Errorf("%q = %q, %d", tc.key, project, iid)
		}
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%q err = %v", tc.key, err)
		}
	}
}

func TestProjectOf(t *testing.T) {
	s := NewIssues(IssuesConfig{})
	for _, tc := range []struct {
		in   issueJSON
		want string
	}{
		{issueJSON{WebURL: "https://gl/g/sub/p/-/issues/5"}, "g/sub/p"},
		{issueJSON{WebURL: "https://gl/no/marker"}, "fallback"},
		{issueJSON{WebURL: "://bad"}, "fallback"},
		{issueJSON{}, "fallback"},
	} {
		if got := s.projectOf(tc.in, "fallback"); got != tc.want {
			t.Errorf("%+v = %q", tc.in, got)
		}
	}
	var withRef issueJSON
	withRef.References.Full = "g/p#5"
	withRef.WebURL = "https://gl/other/-/issues/5"
	if got := s.projectOf(withRef, "fallback"); got != "g/p" {
		t.Errorf("the full reference wins: %q", got)
	}
}

func TestSearchParams(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  map[string]string
	}{
		{"", map[string]string{"scope": "assigned_to_me", "state": "opened"}},
		{"flaky test", map[string]string{"scope": "all", "search": "flaky test"}},
		{"~bug label:~p1 labels:x", map[string]string{"scope": "all", "labels": "bug,p1,x"}},
		{"state:open", map[string]string{"scope": "all", "state": "opened"}},
		{"is:OPENED", map[string]string{"scope": "all", "state": "opened"}},
		{"state:closed", map[string]string{"scope": "all", "state": "closed"}},
		{"state:closed state:all", map[string]string{"scope": "all"}},
		{"assignee:@me", map[string]string{"scope": "assigned_to_me"}},
		{"assignee:@lu", map[string]string{"scope": "all", "assignee_username": "lu"}},
		{"author:@me", map[string]string{"scope": "created_by_me"}},
		{"author:lu", map[string]string{"scope": "all", "author_username": "lu"}},
		{"milestone:%v1", map[string]string{"scope": "all", "milestone": "v1"}},
		{"type:INCIDENT", map[string]string{"scope": "all", "issue_type": "incident"}},
		// Words that only look like filters stay text.
		{"~ state: http://x note:y", map[string]string{"scope": "all", "search": "~ state: http://x note:y"}},
	} {
		got := searchParams(tc.query)
		if got.Get("order_by") != "updated_at" || got.Get("sort") != "desc" {
			t.Errorf("%q order = %v", tc.query, got)
		}
		got.Del("order_by")
		got.Del("sort")
		flat := map[string]string{}
		for k := range got {
			flat[k] = got.Get(k)
		}
		if !reflect.DeepEqual(flat, tc.want) {
			t.Errorf("%q = %v, want %v", tc.query, flat, tc.want)
		}
	}
}

func TestIssuesSearchScopesAndLimits(t *testing.T) {
	ctx := context.Background()
	var paths []string
	mux := http.NewServeMux()
	row := func(iid int, updated string) string {
		return fmt.Sprintf(`{"iid":%d,"title":"t","updated_at":%q,"references":{"full":"g/p#%d"}}`, iid, updated, iid)
	}
	mux.HandleFunc("GET /api/v4/projects/g%2Fp/issues", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath()+" "+r.URL.Query().Get("per_page"))
		_, _ = w.Write([]byte("[" + row(1, "2026-01-01T00:00:00Z") + "," + row(2, "2026-03-01T00:00:00Z") + "]"))
	})
	mux.HandleFunc("GET /api/v4/groups/g%2Fsub/issues", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		_, _ = w.Write([]byte("[" + row(2, "2026-03-01T00:00:00Z") + "," + row(3, "2026-02-01T00:00:00Z") + "]"))
	})
	mux.HandleFunc("GET /api/v4/issues", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		_, _ = w.Write([]byte(`[]`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	s := NewIssues(IssuesConfig{BaseURL: ts.URL, Token: secret, Projects: []string{"g/p"}, Groups: []string{"g/sub/"}})
	got, err := s.Search(ctx, "text", 500)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, x := range got {
		keys = append(keys, x.Key)
	}
	// Both scopes searched, the duplicate dropped, newest first.
	if !reflect.DeepEqual(keys, []string{"g/p#2", "g/p#3", "g/p#1"}) {
		t.Errorf("keys = %v", keys)
	}
	if paths[0] != "/api/v4/projects/g%2Fp/issues 25" || paths[1] != "/api/v4/groups/g%2Fsub/issues" {
		t.Errorf("paths = %q", paths)
	}
	// Merging the scopes must not outgrow the limit.
	got, err = s.Search(ctx, "text", 2)
	if err != nil || len(got) != 2 || got[0].Key != "g/p#2" {
		t.Errorf("limited = %+v, %v", got, err)
	}

	paths = nil
	if got, err := NewIssues(IssuesConfig{BaseURL: ts.URL, Token: secret}).Search(ctx, "", 5); err != nil || len(got) != 0 {
		t.Errorf("unscoped = %+v, %v", got, err)
	}
	if !reflect.DeepEqual(paths, []string{"/api/v4/issues"}) {
		t.Errorf("no scope searches everything: %q", paths)
	}

	down := answer(t, map[string]reply{"GET /api/v4/issues": {500, `boom`}})
	if got, err := issuesFor(down).Search(ctx, "x", 5); err == nil || got != nil {
		t.Errorf("failure = %+v, %v", got, err)
	}
}

func TestToTicketSparseIssue(t *testing.T) {
	s := NewIssues(IssuesConfig{BaseURL: "https://gl"})
	got := s.toTicket(issueJSON{IID: 5, UpdatedAt: "not a time", Description: "  body  "}, "g/p")
	if got.Key != "g/p#5" || got.Type != "Issue" || got.TypeID != "issue" || got.Body != "body" || !got.Updated.IsZero() || got.Assignee != "" || got.Attachments != nil || got.RemoteID != "" {
		t.Errorf("sparse = %+v", got)
	}
	got = s.toTicket(issueJSON{IID: 5, IssueType: "incident"}, "g/p")
	if got.Type != "Incident" || got.TypeID != "incident" {
		t.Errorf("type = %q (%q)", got.Type, got.TypeID)
	}
}

func TestUploadsListsEachFileOnceAndMakesLinksAbsolute(t *testing.T) {
	s := NewIssues(IssuesConfig{BaseURL: "https://gl"})
	sec := strings.Repeat("a", 32)
	body := "see ![x](/uploads/" + sec + "/my%20shot.png) and [again](/uploads/" + sec + "/my%20shot.png) and [bad](/uploads/" + strings.Repeat("b", 32) + "/100%.txt) [other](/uploads/short/x.png)"
	got, atts := s.uploads(body, 7)
	if len(atts) != 2 {
		t.Fatalf("attachments = %+v", atts)
	}
	if want := (trackerport.SourceAttachment{ID: sec, Filename: "my shot.png", URL: "https://gl/api/v4/projects/7/uploads/" + sec + "/my%20shot.png"}); atts[0] != want {
		t.Errorf("attachment = %+v", atts[0])
	}
	if atts[1].Filename != "100%.txt" {
		t.Errorf("an undecodable name stays as written: %q", atts[1].Filename)
	}
	if !strings.Contains(got, "](https://gl/-/project/7/uploads/"+sec+"/my%20shot.png)") || strings.Contains(got, "](/uploads/"+sec) {
		t.Errorf("body = %q", got)
	}
	if !strings.Contains(got, "[other](/uploads/short/x.png)") {
		t.Errorf("not an upload, not rewritten: %q", got)
	}
}

func TestOpenAttachmentFailures(t *testing.T) {
	ctx := context.Background()
	var got http.Header
	status, body := 200, "file bytes"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/7/uploads/abc/a.txt", func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s := issuesFor(ts)
	u := ts.URL + "/api/v4/projects/7/uploads/abc/a.txt"

	rc, err := s.OpenAttachment(ctx, trackerport.SourceAttachment{Filename: "a.txt", URL: u})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "file bytes" || got.Get("PRIVATE-TOKEN") != secret {
		t.Errorf("data %q headers %v", data, got)
	}

	status, body = 404, `{"message":"404 File Not Found"}`
	rc, err = s.OpenAttachment(ctx, trackerport.SourceAttachment{Filename: "a.txt", URL: u})
	if err == nil || rc != nil || !strings.Contains(err.Error(), "a.txt") || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing = %v, %v", rc, err)
	}
	noSecret(t, err)

	got = nil
	_, err = s.OpenAttachment(ctx, trackerport.SourceAttachment{Filename: "a.txt", URL: "http://evil.example/x"})
	if err == nil || !strings.Contains(err.Error(), "refusing to send credentials") || got != nil {
		t.Errorf("foreign host = %v, request %v", err, got)
	}
	if _, err := s.OpenAttachment(ctx, trackerport.SourceAttachment{Filename: "a.txt", URL: "http://[bad"}); err == nil {
		t.Error("an unparsable attachment URL is an error")
	}
	if _, err := NewIssues(IssuesConfig{BaseURL: "://bad"}).OpenAttachment(ctx, trackerport.SourceAttachment{URL: "http://x/y"}); err == nil {
		t.Error("an unparsable base URL is an error")
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	dead := issuesFor(gone)
	deadURL := gone.URL + "/x"
	gone.Close()
	if _, err := dead.OpenAttachment(ctx, trackerport.SourceAttachment{URL: deadURL}); err == nil {
		t.Error("a dead server is an error")
	} else {
		noSecret(t, err)
	}
}

// The host check is what keeps the token at home, so it must hold past a
// redirect too: GitLab's object storage answers uploads with one.
func TestOpenAttachmentKeepsTheTokenOffARedirect(t *testing.T) {
	var leaked string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("PRIVATE-TOKEN")
		_, _ = w.Write([]byte("object bytes"))
	}))
	defer elsewhere.Close()
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/bucket/a.txt", http.StatusFound)
	}))
	defer home.Close()
	rc, err := issuesFor(home).OpenAttachment(context.Background(), trackerport.SourceAttachment{Filename: "a.txt", URL: home.URL + "/x"})
	if err == nil {
		rc.Close()
	}
	if leaked != "" {
		t.Errorf("the token left for %s", elsewhere.URL)
	}
}

// Dropping the token must not drop the file: object storage takes its own
// signature in the URL, and a redirect on the same host still needs the token.
func TestOpenAttachmentFollowsItsRedirects(t *testing.T) {
	var tokens []string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("PRIVATE-TOKEN"))
		_, _ = w.Write([]byte("object bytes"))
	}))
	defer storage.Close()
	var home *httptest.Server
	home = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("PRIVATE-TOKEN"))
		if r.URL.Path == "/x" {
			http.Redirect(w, r, "/y", http.StatusFound)
			return
		}
		http.Redirect(w, r, storage.URL+"/bucket/a.txt", http.StatusFound)
	}))
	defer home.Close()
	rc, err := issuesFor(home).OpenAttachment(context.Background(), trackerport.SourceAttachment{Filename: "a.txt", URL: home.URL + "/x"})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if data, _ := io.ReadAll(rc); string(data) != "object bytes" {
		t.Errorf("file = %q", data)
	}
	if want := []string{secret, secret, ""}; !slices.Equal(tokens, want) {
		t.Errorf("tokens by hop = %q, want %q", tokens, want)
	}
}

func TestRedirectLoopStops(t *testing.T) {
	var home *httptest.Server
	home = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, home.URL+"/again", http.StatusFound)
	}))
	defer home.Close()
	_, err := issuesFor(home).OpenAttachment(context.Background(), trackerport.SourceAttachment{Filename: "a.txt", URL: home.URL + "/x"})
	if err == nil || !strings.Contains(err.Error(), "10 redirects") {
		t.Errorf("err = %v", err)
	}
}

func TestIssuesTransitionEdges(t *testing.T) {
	ctx := context.Background()
	var events []any
	state := "closed"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/g%2Fp/issues/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"iid":1,"state":"` + state + `"}`))
	})
	mux.HandleFunc("PUT /api/v4/projects/g%2Fp/issues/1", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		events = append(events, b["state_event"])
		_, _ = w.Write([]byte(`{}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s := issuesFor(ts, "g/p")

	// "open" reads as GitHub's word for it does.
	if err := s.Transition(ctx, "#1", " Open "); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(ctx, "#1", "closed"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []any{"reopen"}) {
		t.Errorf("events = %v (already closed is no event)", events)
	}
	var unreachable *trackerport.ErrStatusUnreachable
	if err := s.Transition(ctx, "#1", "In Review"); !errors.As(err, &unreachable) || unreachable.From != "closed" || !reflect.DeepEqual(unreachable.Reachable, issueStates) {
		t.Errorf("unreachable = %v", err)
	}
	if err := s.Transition(ctx, "#404", "closed"); !errors.Is(err, trackerport.ErrTicketNotFound) {
		t.Errorf("a missing issue = %v, want ErrTicketNotFound", err)
	}
}

func TestIssuesWriteFailures(t *testing.T) {
	ctx := context.Background()
	s := issuesFor(answer(t, nil), "g/p")
	bad := "not a key"
	calls := map[string]error{
		"transition": s.Transition(ctx, bad, "closed"),
		"comment":    s.Comment(ctx, bad, "x"),
		"set fields": s.SetFields(ctx, bad, map[string]string{"labels": "a"}),
		"add link":   s.AddLink(ctx, bad, trackerport.TicketLink{URL: "https://x"}),
	}
	_, calls["comments"] = s.ListComments(ctx, bad)
	_, calls["fields"] = s.Fields(ctx, bad, []string{"labels"})
	_, calls["attach"] = s.Attach(ctx, bad, "a", "text/plain", nil)
	_, calls["fetch"] = s.Fetch(ctx, bad)
	for name, err := range calls {
		if err == nil || !strings.Contains(err.Error(), "not group/project#number") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The stub knows nothing: every read underneath is a 404.
	if _, err := s.Fetch(ctx, "#1"); err == nil {
		t.Error("fetch: a missing issue is an error")
	}
	if _, err := s.ListComments(ctx, "#1"); err == nil {
		t.Error("comments: a missing issue is an error")
	}
	if _, err := s.Fields(ctx, "#1", nil); err == nil {
		t.Error("fields: a missing issue is an error")
	}
	if _, err := s.Attach(ctx, "#1", "a", "text/plain", nil); err == nil {
		t.Error("attach: a missing issue is an error")
	}
	if err := s.AddLink(ctx, "#1", trackerport.TicketLink{URL: "https://x"}); err == nil {
		t.Error("add link: unreadable notes are an error, not a second link")
	}
	if err := s.AddLink(ctx, "#1", trackerport.TicketLink{URL: " "}); err == nil || !strings.Contains(err.Error(), "needs a URL") {
		t.Errorf("empty link = %v", err)
	}
	if err := s.SetFields(ctx, "#1", map[string]string{"milestone": "v1"}); err == nil {
		t.Error("set fields: an unreadable milestone list is an error")
	}
	if err := s.SetFields(ctx, "#1", map[string]string{"assignees": "lu"}); err == nil {
		t.Error("set fields: an unreadable user list is an error")
	}
	if err := s.SetFields(ctx, "#1", nil); err != nil {
		t.Errorf("no fields is no call: %v", err)
	}
}

func TestIssuesListCommentsSkipSystemNotesAcrossPages(t *testing.T) {
	h, calls := paged(repeat(100, `{"id":1,"body":"pushed","system":true}`), `[{"id":2,"author":{"username":"ann"},"body":"human","created_at":"2026-07-30T10:00:00Z"}]`)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/g%2Fp/issues/1/notes", h)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	got, err := issuesFor(ts, "g/p").ListComments(context.Background(), "#1")
	if err != nil || len(got) != 1 || calls.Load() != 2 {
		t.Fatalf("comments %+v, %d calls, %v", got, calls.Load(), err)
	}
	if c := got[0]; c.ID != "2" || c.Author != "ann" || c.Body != "human" || c.Created.IsZero() || !c.Updated.Equal(c.Created) {
		t.Errorf("comment = %+v", c)
	}
}

func TestIssuesAddLinkDefaultsTitleAndDeduplicates(t *testing.T) {
	var posted string
	marker := linkMarker("https://mr/1")
	notes := `[]`
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/g%2Fp/issues/1/notes", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(notes)) })
	mux.HandleFunc("POST /api/v4/projects/g%2Fp/issues/1/notes", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		posted = b["body"]
		_, _ = w.Write([]byte(`{}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s := issuesFor(ts, "g/p")
	if err := s.AddLink(context.Background(), "#1", trackerport.TicketLink{URL: "https://mr/1"}); err != nil {
		t.Fatal(err)
	}
	if want := "Link: https://mr/1\n\n" + marker; posted != want {
		t.Errorf("posted %q, want %q", posted, want)
	}
	posted = ""
	enc, _ := json.Marshal(marker)
	notes = `[{"body":` + string(enc) + `}]`
	if err := s.AddLink(context.Background(), "#1", trackerport.TicketLink{URL: "https://mr/1"}); err != nil || posted != "" {
		t.Errorf("second link posted %q, %v", posted, err)
	}
}

func TestIssuesSetFieldsAndFieldsShapes(t *testing.T) {
	var update map[string]any
	var usernames []string
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v4/projects/g%2Fp/issues/1", func(w http.ResponseWriter, r *http.Request) {
		update = nil
		_ = json.NewDecoder(r.Body).Decode(&update)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /api/v4/projects/g%2Fp/milestones", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":40,"title":"Later"},{"id":20,"title":"V1"}]`))
	})
	mux.HandleFunc("GET /api/v4/users", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("username")
		usernames = append(usernames, name)
		if name == "ghost" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":9}]`))
	})
	mux.HandleFunc("GET /api/v4/projects/g%2Fp/issues/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"iid":1,"labels":["a","b"],"assignees":[{"username":"x"},{"username":"y"}],"milestone":{"id":1,"title":"v1"}}`))
	})
	mux.HandleFunc("GET /api/v4/projects/g%2Fp/issues/2", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"iid":2}`)) })
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s := issuesFor(ts, "g/p")
	ctx := context.Background()

	if err := s.SetFields(ctx, "#1", map[string]string{"labels": " x, y ", "assignees": "@lu", "milestone": "v1"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"labels": "x,y", "assignee_ids": []any{float64(9)}, "milestone_id": float64(20)}
	if !reflect.DeepEqual(update, want) {
		t.Errorf("update = %#v", update)
	}
	if !reflect.DeepEqual(usernames, []string{"lu"}) {
		t.Errorf("the @ is not part of a username: %v", usernames)
	}
	// Emptying everything still says so.
	if err := s.SetFields(ctx, "#1", map[string]string{"labels": "", "assignees": ",", "milestone": " "}); err != nil {
		t.Fatal(err)
	}
	want = map[string]any{"labels": "", "assignee_ids": []any{}, "milestone_id": float64(0)}
	if !reflect.DeepEqual(update, want) {
		t.Errorf("clearing update = %#v", update)
	}

	var unknown *trackerport.ErrFieldUnknown
	if err := s.SetFields(ctx, "#1", map[string]string{"milestone": "v9"}); !errors.As(err, &unknown) || !reflect.DeepEqual(unknown.Candidates, []string{"Later", "V1"}) {
		t.Errorf("unknown milestone = %v", err)
	}
	if err := s.SetFields(ctx, "#1", map[string]string{"assignees": "ghost"}); !errors.As(err, &unknown) || !strings.Contains(unknown.Field, "ghost") {
		t.Errorf("unknown user = %v", err)
	}
	var unsupported *trackerport.ErrFieldUnsupported
	if err := s.SetFields(ctx, "#1", map[string]string{"priority": "1"}); !errors.As(err, &unsupported) {
		t.Errorf("unsupported field = %v", err)
	}
	if _, err := s.Fields(ctx, "#1", []string{"priority"}); !errors.As(err, &unsupported) {
		t.Errorf("unsupported read = %v", err)
	}
	got, err := s.Fields(ctx, "#1", []string{"Labels", "assignees", "milestone"})
	if err != nil || !reflect.DeepEqual(got, map[string]string{"Labels": "a, b", "assignees": "x, y", "milestone": "v1"}) {
		t.Errorf("fields = %v, %v", got, err)
	}
	got, err = s.Fields(ctx, "#2", []string{"milestone"})
	if err != nil || got["milestone"] != "" {
		t.Errorf("no milestone = %v, %v", got, err)
	}
}
