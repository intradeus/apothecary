package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
)

// secret is the token every failure test runs with: no error may carry it.
const secret = "ghp_s3cr3tvalue"

type reply struct {
	status int
	body   string
	header map[string]string
}

// answer serves canned replies by mux pattern ("GET /user"); anything not
// listed is GitHub's JSON 404.
func answer(t *testing.T, routes map[string]reply) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for pattern, r := range routes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range r.header {
				w.Header().Set(k, v)
			}
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
	return New(Config{BaseURL: ts.URL, Token: secret, Locator: "lu/greeter"})
}

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("error carries the token: %v", err)
	}
}

// Every way a REST call fails reads as what it is, and none carries the token.
func TestAPIFailuresAreNamedAndNeverCarryTheToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply reply
		want  []string
	}{
		{"unauthorized", reply{status: 401, body: `{"message":"Bad credentials"}`}, []string{"401", "Bad credentials"}},
		{"forbidden", reply{status: 403, body: `{"message":"Must have admin rights"}`}, []string{"403", "Must have admin rights"}},
		{"rate limited", reply{status: 403, body: `{"message":"API rate limit exceeded"}`, header: map[string]string{"X-Ratelimit-Remaining": "0", "X-Ratelimit-Reset": "1793000000"}},
			[]string{"rate limit exhausted", "1793000000", "not a permissions problem"}},
		{"forbidden with quota left", reply{status: 403, body: `{"message":"nope"}`, header: map[string]string{"X-Ratelimit-Remaining": "12"}}, []string{"403", "nope"}},
		{"not found", reply{status: 404, body: `{"message":"Not Found"}`}, []string{"404", "Not Found"}},
		{"server error", reply{status: 502, body: `<html>Bad gateway</html>`}, []string{"502", "Bad gateway"}},
		{"malformed body", reply{status: 200, body: `{"login":`}, []string{"decode"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := answer(t, map[string]reply{"GET /user": tc.reply})
			login, err := adapterFor(ts).Whoami(context.Background())
			if err == nil || login != "" {
				t.Fatalf("login %q, err %v", login, err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err %q does not say %q", err, w)
				}
			}
			noSecret(t, err)
		})
	}
}

func TestAPIStateTheHeadersGitHubAsksFor(t *testing.T) {
	var got http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"login":"lu"}`))
	}))
	defer ts.Close()
	login, err := adapterFor(ts).Whoami(context.Background())
	if err != nil || login != "lu" {
		t.Fatalf("whoami = %q, %v", login, err)
	}
	if got.Get("Authorization") != "Bearer "+secret || got.Get("Accept") != "application/vnd.github+json" || got.Get("X-GitHub-Api-Version") == "" {
		t.Errorf("headers = %v", got)
	}
	if got.Get("Content-Type") != "" {
		t.Errorf("a GET carries no content type: %v", got.Get("Content-Type"))
	}
}

func TestAPITransportFailures(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	a := adapterFor(ts)
	ts.Close()
	if _, err := a.Whoami(context.Background()); err == nil {
		t.Error("a dead server is an error")
	} else {
		noSecret(t, err)
	}

	live := answer(t, map[string]reply{"GET /user": {body: `{"login":"lu"}`}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapterFor(live).Whoami(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled call: %v", err)
	}

	bad := New(Config{BaseURL: "http://bad host", Token: secret, Locator: "lu/greeter"})
	if _, err := bad.Whoami(context.Background()); err == nil {
		t.Error("an unparsable base URL is an error")
	} else {
		noSecret(t, err)
	}
	if _, err := bad.api(context.Background(), http.MethodPost, "/x", map[string]any{"a": 1}, nil); err == nil {
		t.Error("an unparsable base URL fails a POST too")
	}
	if _, err := adapterFor(live).api(context.Background(), http.MethodPost, "/user", make(chan int), nil); err == nil {
		t.Error("a body that cannot marshal is an error, not an empty POST")
	}
	if err := bad.graphql(context.Background(), "q", nil, nil); err == nil {
		t.Error("graphql over an unparsable base URL is an error")
	}
	if err := adapterFor(live).graphql(context.Background(), "q", map[string]any{"x": make(chan int)}, nil); err == nil {
		t.Error("graphql variables that cannot marshal are an error")
	}
}

func TestCheckAccessFailures(t *testing.T) {
	ctx := context.Background()
	if err := New(Config{Locator: "lu/greeter"}).CheckAccess(ctx, repoport.RepoRef{}); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("no token: %v", err)
	}
	ts := answer(t, map[string]reply{"GET /repos/lu/greeter": {status: 404, body: `{"message":"Not Found"}`}})
	err := adapterFor(ts).CheckAccess(ctx, repoport.RepoRef{})
	if err == nil || !strings.Contains(err.Error(), "repo lu/greeter unreachable") {
		t.Errorf("missing repo: %v", err)
	}
	noSecret(t, err)
}

func TestFileExistsEdges(t *testing.T) {
	var escaped, ref string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/greeter/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		escaped, ref = r.URL.EscapedPath(), r.URL.Query().Get("ref")
		switch r.PathValue("path") {
		case "a file":
			_, _ = w.Write([]byte(`{"name":"a file","type":"file"}`))
		case "empty":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`boom`))
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a := adapterFor(ts)
	ctx := context.Background()

	// A file where a directory was expected is not a directory.
	if ok, err := a.FileExists(ctx, repoport.RepoRef{}, "a file/"); err != nil || ok {
		t.Errorf("file as dir = %v, %v", ok, err)
	}
	if ok, err := a.FileExists(ctx, repoport.RepoRef{DefaultBranch: "dev/x"}, "empty/"); err != nil || ok {
		t.Errorf("empty dir = %v, %v", ok, err)
	}
	if ref != "dev/x" {
		t.Errorf("ref = %q", ref)
	}
	ok, err := a.FileExists(ctx, repoport.RepoRef{}, "sub dir/boom")
	if err == nil || ok {
		t.Errorf("a 500 is neither there nor not: %v, %v", ok, err)
	}
	// Slashes stay real, everything else is escaped per segment; the branch
	// falls back to main.
	if escaped != "/repos/lu/greeter/contents/sub%20dir/boom" || ref != "main" {
		t.Errorf("path %q ref %q", escaped, ref)
	}
}

func TestBaseOr(t *testing.T) {
	for _, tc := range []struct {
		target, def, want string
	}{
		{"stack/1", "dev", "stack/1"},
		{"", "dev", "dev"},
		{"", "", "main"},
	} {
		if got := baseOr(tc.target, repoport.RepoRef{DefaultBranch: tc.def}); got != tc.want {
			t.Errorf("baseOr(%q, %q) = %q", tc.target, tc.def, got)
		}
	}
}

// A plan without draft pull requests still gets its pull request, marked in
// the title the way GitLab's are.
func TestCreateDraftMRDegradesWithoutDraftSupport(t *testing.T) {
	var bodies []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/lu/greeter/pulls", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if b["draft"] == true {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"message":"Draft pull requests are not supported in this repository."}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"number":3,"html_url":"https://github.com/lu/greeter/pull/3"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ref, err := adapterFor(ts).CreateDraftMR(context.Background(), repoport.RepoRef{DefaultBranch: "dev"}, "b", "stack/1", "Add x", "body")
	if err != nil || ref.IID != 3 {
		t.Fatalf("ref %+v, err %v", ref, err)
	}
	if len(bodies) != 2 {
		t.Fatalf("attempts = %v", bodies)
	}
	second := bodies[1]
	if second["title"] != "Draft: Add x" || second["base"] != "stack/1" || second["head"] != "b" {
		t.Errorf("fallback = %v", second)
	}
	if _, has := second["draft"]; has {
		t.Errorf("the fallback must not ask for a draft: %v", second)
	}
}

func TestCreateDraftMRFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		routes map[string]reply
		want   string
	}{
		{"validation", map[string]reply{"POST /repos/lu/greeter/pulls": {status: 422, body: `{"message":"No commits between main and b"}`}}, "No commits between"},
		{"server error", map[string]reply{"POST /repos/lu/greeter/pulls": {status: 500, body: `oops`}}, "500"},
		// "Already exists" with no open pull request to return is the original
		// failure, never a zero MRRef reported as success.
		{"exists but nothing listed", map[string]reply{
			"POST /repos/lu/greeter/pulls": {status: 422, body: `{"message":"A pull request already exists"}`},
			"GET /repos/lu/greeter/pulls":  {body: `[]`},
		}, "already exists"},
		{"exists and the lookup fails", map[string]reply{
			"POST /repos/lu/greeter/pulls": {status: 422, body: `{"message":"A pull request already exists"}`},
			"GET /repos/lu/greeter/pulls":  {status: 500, body: `oops`},
		}, "already exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := answer(t, tc.routes)
			ref, err := adapterFor(ts).CreateDraftMR(context.Background(), repoport.RepoRef{}, "b", "", "t", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) || ref != (repoport.MRRef{}) {
				t.Errorf("ref %+v, err %v", ref, err)
			}
			noSecret(t, err)
		})
	}
}

func TestFindMR(t *testing.T) {
	var query string
	body := `[{"number":4,"html_url":"https://github.com/lu/greeter/pull/4"}]`
	status := 200
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/greeter/pulls", func(w http.ResponseWriter, r *http.Request) {
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
	if err != nil || !ok || ref.IID != 4 || ref.URL == "" {
		t.Fatalf("found = %+v, %v, %v", ref, ok, err)
	}
	if query != "state=open&head=lu%3Afeat%2Fx&base=dev" {
		t.Errorf("query = %q", query)
	}
	body = `[]`
	if ref, ok, err := a.FindMR(ctx, repo, "feat/x", "stack"); err != nil || ok || ref.IID != 0 {
		t.Errorf("none = %+v, %v, %v", ref, ok, err)
	}
	if !strings.Contains(query, "base=stack") {
		t.Errorf("query = %q", query)
	}
	status, body = 500, `boom`
	if _, ok, err := a.FindMR(ctx, repo, "feat/x", ""); err == nil || ok {
		t.Errorf("failure = %v, %v", ok, err)
	}

	// With no owner in the locator the head goes bare.
	var bareHead string
	mux.HandleFunc("GET /repos/greeter/pulls", func(w http.ResponseWriter, r *http.Request) {
		bareHead = r.URL.Query().Get("head")
		_, _ = w.Write([]byte(`[]`))
	})
	bare := New(Config{BaseURL: ts.URL, Token: secret, Locator: "greeter"})
	if _, err := bare.findOpenPR(ctx, "b", "main"); err != nil || bareHead != "b" {
		t.Errorf("bare head = %q, %v", bareHead, err)
	}
}

func TestBranchURLFailures(t *testing.T) {
	ts := answer(t, map[string]reply{
		"GET /repos/lu/greeter/branches/down":    {status: 500, body: `boom`},
		"GET /repos/lu/greeter/branches/nolinks": {body: `{"name":"nolinks"}`},
	})
	a := adapterFor(ts)
	if u, ok, err := a.BranchURL(context.Background(), repoport.RepoRef{}, "down"); err == nil || ok || u != "" {
		t.Errorf("500 = %q, %v, %v", u, ok, err)
	}
	// A branch with no page to show is not a branch to link to.
	if u, ok, err := a.BranchURL(context.Background(), repoport.RepoRef{}, "nolinks"); err != nil || ok || u != "" {
		t.Errorf("no links = %q, %v, %v", u, ok, err)
	}
}

func TestMRStatusFailures(t *testing.T) {
	ts := answer(t, map[string]reply{
		"GET /repos/lu/greeter/pulls/1":       {status: 500, body: `boom`},
		"GET /repos/lu/greeter/pulls/2":       {body: `{"state":"open","mergeable":false,"mergeable_state":"dirty","head":{"sha":"h"},"base":{"ref":"main"}}`},
		"GET /repos/lu/greeter/branches/main": {status: 502, body: `bad`},
		"GET /repos/lu/greeter/pulls/3":       {body: `{"state":"closed","mergeable":false,"mergeable_state":"dirty","head":{"sha":"h"}}`},
	})
	a := adapterFor(ts)
	if st, err := a.MRStatus(context.Background(), repoport.MRRef{IID: 1}); err == nil || st != (repoport.MRStatus{}) {
		t.Errorf("read failure = %+v, %v", st, err)
	}
	// A conflict's target tip is part of the answer: without it the status fails.
	if st, err := a.MRStatus(context.Background(), repoport.MRRef{IID: 2}); err == nil || st != (repoport.MRStatus{}) {
		t.Errorf("target failure = %+v, %v", st, err)
	}
	// No base ref, no target lookup.
	st, err := a.MRStatus(context.Background(), repoport.MRRef{IID: 3})
	if err != nil || st.State != repoport.MRClosed || st.Mergeable != repoport.MergeConflicts || st.TargetSHA != "" || st.MergeableNow {
		t.Errorf("closed conflict = %+v, %v", st, err)
	}
}

func TestMRChecksFailuresAndStates(t *testing.T) {
	a := New(Config{BaseURL: "http://127.0.0.1:1", Token: secret, Locator: "lu/greeter"})
	if c, err := a.MRChecks(context.Background(), repoport.MRRef{IID: 5}, ""); err != nil || len(c.Items) != 0 || c.Head != "" {
		t.Errorf("no head means no call: %+v, %v", c, err)
	}

	ts := answer(t, map[string]reply{"GET /repos/lu/greeter/commits/h1/check-runs": {status: 500, body: `boom`}})
	if _, err := adapterFor(ts).MRChecks(context.Background(), repoport.MRRef{IID: 5}, "h1"); err == nil {
		t.Error("check runs failing is an error")
	}

	ts = answer(t, map[string]reply{
		"GET /repos/lu/greeter/commits/h1/check-runs": {body: `{"check_runs":[{"id":1,"name":"t","status":"completed","conclusion":"success"}]}`},
		"GET /repos/lu/greeter/commits/h1/status":     {status: 500, body: `boom`},
	})
	if _, err := adapterFor(ts).MRChecks(context.Background(), repoport.MRRef{IID: 5}, "h1"); err == nil {
		t.Error("a half-read result is not a green one")
	}

	ts = answer(t, map[string]reply{
		"GET /repos/lu/greeter/commits/h1/check-runs": {body: `{"check_runs":[
		  {"id":1,"name":"slow","status":"completed","conclusion":"timed_out"},
		  {"id":2,"name":"gate","status":"completed","conclusion":"action_required"},
		  {"id":3,"name":"off","status":"completed","conclusion":"cancelled"}]}`},
		"GET /repos/lu/greeter/commits/h1/status": {body: `{"statuses":[
		  {"context":"a","state":"success"},{"context":"b","state":"pending"},{"context":"c","state":"failure"}]}`},
	})
	c, err := adapterFor(ts).MRChecks(context.Background(), repoport.MRRef{IID: 5}, "h1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]repoport.CheckState{
		"slow": repoport.CheckFailure, "gate": repoport.CheckFailure, "off": repoport.CheckNeutral,
		"a": repoport.CheckSuccess, "b": repoport.CheckPending, "c": repoport.CheckFailure,
	}
	if len(c.Items) != len(want) {
		t.Fatalf("items = %+v", c.Items)
	}
	for _, it := range c.Items {
		if it.State != want[it.Name] {
			t.Errorf("%s = %s", it.Name, it.State)
		}
	}
}

// The newest run of each name counts whatever order GitHub lists them in, and
// whichever name comes first.
func TestMRChecksNewestRunWinsAcrossNames(t *testing.T) {
	ts := answer(t, map[string]reply{
		"GET /repos/lu/greeter/commits/h1/check-runs": {body: `{"check_runs":[
		  {"id":10,"name":"deploy","status":"completed","conclusion":"success"},
		  {"id":1,"name":"deploy","status":"completed","conclusion":"success"},
		  {"id":5,"name":"test","status":"completed","conclusion":"success"},
		  {"id":3,"name":"test","status":"completed","conclusion":"failure"}]}`},
		"GET /repos/lu/greeter/commits/h1/status": {body: `{"statuses":[]}`},
	})
	c, err := adapterFor(ts).MRChecks(context.Background(), repoport.MRRef{IID: 5}, "h1")
	if err != nil {
		t.Fatal(err)
	}
	if c.State != repoport.CheckSuccess {
		t.Errorf("state = %s; the failed run is superseded by id 5: %+v", c.State, c.Items)
	}
}

func TestCIFailuresEdges(t *testing.T) {
	a := New(Config{BaseURL: "http://127.0.0.1:1", Token: secret, Locator: "lu/greeter"})
	if got, err := a.CIFailures(context.Background(), repoport.MRRef{IID: 5}, ""); err != nil || got != nil {
		t.Errorf("no head: %v, %v", got, err)
	}

	ts := answer(t, map[string]reply{"GET /repos/lu/greeter/commits/h1/check-runs": {status: 500, body: `boom`}})
	if _, err := adapterFor(ts).CIFailures(context.Background(), repoport.MRRef{IID: 5}, "h1"); err == nil {
		t.Error("check runs failing is an error")
	}

	// What was read survives the statuses failing; a job that cannot be read
	// stays as its name says, a run that cannot be read has no file.
	ts = answer(t, map[string]reply{
		"GET /repos/lu/greeter/commits/h1/check-runs": {body: `{"check_runs":[
		  {"id":1,"name":"nojob","status":"completed","conclusion":"failure","app":{"slug":"github-actions"}},
		  {"id":2,"name":"norun","status":"completed","conclusion":"timed_out","app":{"slug":"github-actions"}},
		  {"id":3,"name":"running","status":"in_progress","app":{"slug":"github-actions"}}]}`},
		"GET /repos/lu/greeter/actions/jobs/1":    {status: 403, body: `{"message":"Resource not accessible"}`},
		"GET /repos/lu/greeter/actions/jobs/2":    {body: `{"name":"norun job","run_id":77,"steps":[{"name":"slow step","conclusion":"timed_out"}]}`},
		"GET /repos/lu/greeter/actions/runs/77":   {status: 500, body: `boom`},
		"GET /repos/lu/greeter/commits/h1/status": {status: 500, body: `boom`},
	})
	got, err := adapterFor(ts).CIFailures(context.Background(), repoport.MRRef{IID: 5}, "h1")
	if err == nil {
		t.Error("the statuses failing is reported")
	}
	want := []repoport.CIFailure{{Name: "nojob"}, {Name: "norun", Job: "norun job", Steps: []string{"slow step"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// Past ciDetailMax the failed checks are named, not detailed: two calls each
// must not grow with the size of the matrix.
func TestCIFailuresBoundsDetail(t *testing.T) {
	var runs []string
	for i := 1; i <= ciDetailMax+3; i++ {
		runs = append(runs, fmt.Sprintf(`{"id":%d,"name":"job-%d","status":"completed","conclusion":"failure","app":{"slug":"github-actions"}}`, i, i))
	}
	var jobReads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/greeter/commits/h1/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"check_runs":[` + strings.Join(runs, ",") + `]}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/actions/jobs/{id}", func(w http.ResponseWriter, _ *http.Request) {
		jobReads.Add(1)
		_, _ = w.Write([]byte(`{"name":"j","run_id":1}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/actions/runs/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"path":".github/workflows/ci.yml"}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/commits/h1/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"statuses":[]}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	got, err := adapterFor(ts).CIFailures(context.Background(), repoport.MRRef{IID: 5}, "h1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != ciDetailMax+3 || int(jobReads.Load()) != ciDetailMax {
		t.Errorf("%d failures, %d job reads", len(got), jobReads.Load())
	}
	if last := got[len(got)-1]; last.Job != "" || last.File != "" {
		t.Errorf("past the bound a failure is its name: %+v", last)
	}
}

// paged replies by ?page=, with empty arrays past the last page, and counts
// the pages read.
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

// repeat renders n copies of a JSON object as an array.
func repeat(n int, item string) string {
	return "[" + strings.TrimSuffix(strings.Repeat(item+",", n), ",") + "]"
}

func TestMRApprovalPagesAndFailures(t *testing.T) {
	// a approves on page 1 and is overruled by their own request on page 2.
	first := repeat(100, `{"state":"APPROVED","user":{"login":"a"}}`)
	second := `[{"state":"CHANGES_REQUESTED","user":{"login":"a"}},{"state":"APPROVED","user":{"login":"b"}}]`
	h, calls := paged(first, second)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/greeter/pulls/5/reviews", h)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ap, err := adapterFor(ts).MRApproval(context.Background(), repoport.MRRef{IID: 5})
	if err != nil || ap.Approved || !reflect.DeepEqual(ap.By, []string{"b"}) {
		t.Errorf("approval = %+v, %v", ap, err)
	}
	if calls.Load() != 2 {
		t.Errorf("pages read = %d", calls.Load())
	}

	down := answer(t, map[string]reply{"GET /repos/lu/greeter/pulls/5/reviews": {status: 500, body: `boom`}})
	if ap, err := adapterFor(down).MRApproval(context.Background(), repoport.MRRef{IID: 5}); err == nil || ap.Approved {
		t.Errorf("a failed read approves nothing: %+v, %v", ap, err)
	}
}

func TestGraphQLEndpointAndFailures(t *testing.T) {
	if got := New(Config{BaseURL: "https://ghe.example/api/v3/"}).graphqlURL(); got != "https://ghe.example/api/graphql" {
		t.Errorf("GHES = %q", got)
	}
	if got := New(Config{}).graphqlURL(); got != "https://api.github.com/graphql" {
		t.Errorf("github.com = %q", got)
	}

	for _, tc := range []struct {
		name  string
		reply reply
		want  string
	}{
		{"status", reply{status: 502, body: `bad gateway`}, "502"},
		{"malformed", reply{body: `{"data":`}, "decode"},
		{"graphql errors", reply{body: `{"errors":[{"message":"Could not resolve to a node"}]}`}, "Could not resolve to a node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := answer(t, map[string]reply{"POST /graphql": tc.reply})
			a := adapterFor(ts)
			err := a.ResolveThread(context.Background(), repoport.MRRef{IID: 5}, "T1")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
			noSecret(t, err)
			if _, err := a.MRThreads(context.Background(), repoport.MRRef{IID: 5}); err == nil {
				t.Error("threads must not read as empty when GraphQL fails")
			}
			if err := a.ReplyThread(context.Background(), repoport.MRRef{IID: 5}, "T1", "hi"); err == nil {
				t.Error("a reply that failed is reported")
			}
		})
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	a := adapterFor(dead)
	dead.Close()
	if err := a.ResolveThread(context.Background(), repoport.MRRef{IID: 5}, "T1"); err == nil {
		t.Error("a dead server is an error")
	} else {
		noSecret(t, err)
	}
}

func TestMRThreadsPagesAndFiltersDown(t *testing.T) {
	var cursors []any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		cursors = append(cursors, b.Variables["after"])
		if b.Variables["owner"] != "lu" || b.Variables["name"] != "greeter" || b.Variables["n"] != float64(5) {
			t.Errorf("variables = %v", b.Variables)
		}
		if b.Variables["after"] == nil {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[
			  {"id":"T1","path":"a.go","line":1,"comments":{"nodes":[{"databaseId":1,"body":"gone author","author":null}]}},
			  {"id":"empty","path":"b.go","comments":{"nodes":[]}}]}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
		  {"id":"T2","path":"c.go","comments":{"nodes":[{"databaseId":2,"body":"no line","diffHunk":"@@ first","author":{"login":"r"}},
		    {"databaseId":3,"body":"later","diffHunk":"@@ second","author":{"login":"r"}}]}}]}}}}}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/pulls/5/reviews", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1,"body":"bot says","state":"COMMENTED","user":{"login":"b","type":"Bot"}},
		  {"id":2,"body":"   ","state":"COMMENTED","user":{"login":"r"}},
		  {"id":3,"body":"lgtm","state":"APPROVED","user":{"login":"r"}},
		  {"id":4,"body":"thoughts","state":"COMMENTED","user":{"login":"r"}}]`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/issues/5/comments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1,"body":"   ","user":{"login":"r"}}]`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	th, err := adapterFor(ts).MRThreads(context.Background(), repoport.MRRef{IID: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cursors, []any{nil, "c1"}) {
		t.Errorf("cursors = %v", cursors)
	}
	var ids []string
	for _, x := range th {
		ids = append(ids, x.ID)
	}
	if !reflect.DeepEqual(ids, []string{"T1", "T2", "review:4"}) {
		t.Fatalf("threads = %v", ids)
	}
	if th[0].Comments[0].Author != "" {
		t.Errorf("a deleted author is empty, not a panic: %+v", th[0])
	}
	if th[1].DiffHunk != "@@ first" || th[1].Line != 0 || len(th[1].Comments) != 2 {
		t.Errorf("the first comment's hunk is the thread's: %+v", th[1])
	}
}

// Threads the reviewer must answer are all of them, however long the
// conversation: a comment past the first page is still a comment.
func TestMRThreadsReadsEveryConversationComment(t *testing.T) {
	var first []string
	for i := 1; i <= 100; i++ {
		first = append(first, fmt.Sprintf(`{"id":%d,"body":"c%d","user":{"login":"r"}}`, i, i))
	}
	h, _ := paged("["+strings.Join(first, ",")+"]", `[{"id":101,"body":"one more","user":{"login":"r"}}]`)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/pulls/5/reviews", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /repos/lu/greeter/issues/5/comments", h)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	th, err := adapterFor(ts).MRThreads(context.Background(), repoport.MRRef{IID: 5})
	if err != nil || len(th) != 101 {
		t.Errorf("%d threads, %v", len(th), err)
	}
}

// A host that never reaches a short page must not hold a run forever.
func TestEveryPageStops(t *testing.T) {
	var calls atomic.Int32
	full := repeat(100, `{"id":1}`)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(full))
	}))
	defer ts.Close()
	type row struct {
		ID int `json:"id"`
	}
	rows, err := everyPage[row](context.Background(), adapterFor(ts), "/anything")
	if err != nil || calls.Load() != 100 || len(rows) != 10000 {
		t.Errorf("%d rows in %d calls, %v", len(rows), calls.Load(), err)
	}
}

func TestMRThreadsRESTFailures(t *testing.T) {
	gql := reply{body: `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}}`}
	for name, routes := range map[string]map[string]reply{
		"reviews": {"POST /graphql": gql, "GET /repos/lu/greeter/pulls/5/reviews": {status: 500, body: `boom`}},
		"comments": {"POST /graphql": gql, "GET /repos/lu/greeter/pulls/5/reviews": {body: `[]`},
			"GET /repos/lu/greeter/issues/5/comments": {status: 500, body: `boom`}},
	} {
		t.Run(name, func(t *testing.T) {
			if th, err := adapterFor(answer(t, routes)).MRThreads(context.Background(), repoport.MRRef{IID: 5}); err == nil || th != nil {
				t.Errorf("threads %v, err %v", th, err)
			}
		})
	}
}

func TestUploadAttachmentFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		routes map[string]reply
		want   string
	}{
		{"repository unreadable", map[string]reply{"GET /repos/lu/greeter": {status: 500, body: `boom`}}, "500"},
		{"repository without id", map[string]reply{"GET /repos/lu/greeter": {body: `{}`}}, "reports no id"},
		{"unauthorized", map[string]reply{"GET /repos/lu/greeter": {body: `{"id":1}`},
			"POST /user-attachments/assets": {status: 401, body: `no`}}, "classic personal access token"},
		{"forbidden", map[string]reply{"GET /repos/lu/greeter": {body: `{"id":1}`},
			"POST /user-attachments/assets": {status: 403, body: `no`}}, "classic personal access token"},
		{"server error", map[string]reply{"GET /repos/lu/greeter": {body: `{"id":1}`},
			"POST /user-attachments/assets": {status: 500, body: `storage down`}}, "storage down"},
		{"malformed answer", map[string]reply{"GET /repos/lu/greeter": {body: `{"id":1}`},
			"POST /user-attachments/assets": {status: 201, body: `not json`}}, "carries no url"},
		{"answer without url", map[string]reply{"GET /repos/lu/greeter": {body: `{"id":1}`},
			"POST /user-attachments/assets": {status: 201, body: `{}`}}, "carries no url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := answer(t, tc.routes)
			a := adapterFor(ts)
			a.uploads = ts.URL
			att, err := a.UploadAttachment(context.Background(), repoport.RepoRef{}, "a.txt", "text/plain", []byte("x"))
			if err == nil || !strings.Contains(err.Error(), tc.want) || att != (repoport.Attachment{}) {
				t.Errorf("att %+v, err %v", att, err)
			}
			noSecret(t, err)
		})
	}

	ts := answer(t, map[string]reply{"GET /repos/lu/greeter": {body: `{"id":1}`}})
	a := adapterFor(ts)
	a.uploads = "http://bad host"
	if _, err := a.UploadAttachment(context.Background(), repoport.RepoRef{}, "a.txt", "text/plain", nil); err == nil {
		t.Error("an unparsable upload host is an error")
	}
	a.uploads = "http://127.0.0.1:1"
	if _, err := a.UploadAttachment(context.Background(), repoport.RepoRef{}, "a.txt", "text/plain", nil); err == nil {
		t.Error("an unreachable upload host is an error")
	} else {
		noSecret(t, err)
	}
}

// A file that is not an image is a link; only an image embeds.
func TestUploadAttachmentMarkdownForFiles(t *testing.T) {
	ts := answer(t, map[string]reply{"GET /repos/lu/greeter": {body: `{"id":1}`},
		"POST /user-attachments/assets": {status: 201, body: `{"url":"https://github.com/f"}`}})
	a := adapterFor(ts)
	a.uploads = ts.URL
	att, err := a.UploadAttachment(context.Background(), repoport.RepoRef{}, "notes.txt", "text/plain", []byte("x"))
	if err != nil || att.Markdown != "[notes.txt](https://github.com/f)" {
		t.Errorf("att = %+v, %v", att, err)
	}
}

func TestGetMRMapsThePull(t *testing.T) {
	merged := `"2026-07-20T12:00:00Z"`
	for _, tc := range []struct {
		name string
		body string
		want repoport.MRInfo
	}{
		{"same repository",
			`{"number":5,"html_url":"u","title":"t","body":"b","state":"open","draft":true,"user":{"login":"lu"},
			  "head":{"ref":"feat","sha":"h","repo":{"full_name":"lu/greeter"}},"base":{"ref":"main","repo":{"full_name":"lu/greeter"}}}`,
			repoport.MRInfo{Ref: repoport.MRRef{IID: 5, URL: "u"}, Title: "t", Body: "b", Author: "lu", State: repoport.MROpen, Draft: true,
				Source: "feat", Target: "main", Head: "h", HeadRef: "refs/pull/5/head"}},
		{"fork",
			`{"number":5,"state":"closed","head":{"repo":{"full_name":"x/greeter"}},"base":{"repo":{"full_name":"lu/greeter"}}}`,
			repoport.MRInfo{Ref: repoport.MRRef{IID: 5}, State: repoport.MRClosed, HeadRef: "refs/pull/5/head", Fork: true}},
		{"deleted fork",
			`{"number":5,"state":"closed","merged_at":` + merged + `,"head":{"repo":null},"base":{"repo":{"full_name":"lu/greeter"}}}`,
			repoport.MRInfo{Ref: repoport.MRRef{IID: 5}, State: repoport.MRMerged, HeadRef: "refs/pull/5/head", Fork: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := answer(t, map[string]reply{"GET /repos/lu/greeter/pulls/5": {body: tc.body}})
			got, err := adapterFor(ts).GetMR(context.Background(), 5)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, %v\nwant %+v", got, err, tc.want)
			}
		})
	}
	ts := answer(t, map[string]reply{"GET /repos/lu/greeter/pulls/5": {status: 404, body: `{"message":"Not Found"}`}})
	if got, err := adapterFor(ts).GetMR(context.Background(), 5); err == nil || !reflect.DeepEqual(got, repoport.MRInfo{}) {
		t.Errorf("missing = %+v, %v", got, err)
	}
}

func TestListMRsPagesToThreeHundred(t *testing.T) {
	var calls atomic.Int32
	var query string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/greeter/pulls", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(repeat(100, `{"number":1,"state":"open"}`)))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	got, err := adapterFor(ts).ListMRs(context.Background())
	if err != nil || len(got) != 300 || calls.Load() != 3 {
		t.Errorf("%d pulls in %d calls, %v", len(got), calls.Load(), err)
	}
	if !strings.Contains(query, "state=open") || !strings.Contains(query, "sort=created") || !strings.Contains(query, "direction=desc") {
		t.Errorf("query = %q", query)
	}

	short := answer(t, map[string]reply{"GET /repos/lu/greeter/pulls": {body: `[{"number":1,"state":"open"}]`}})
	if got, err := adapterFor(short).ListMRs(context.Background()); err != nil || len(got) != 1 {
		t.Errorf("short = %+v, %v", got, err)
	}
	down := answer(t, map[string]reply{"GET /repos/lu/greeter/pulls": {status: 500, body: `boom`}})
	if got, err := adapterFor(down).ListMRs(context.Background()); err == nil || got != nil {
		t.Errorf("failure = %+v, %v", got, err)
	}
}

func TestMergeMRBranchDeletion(t *testing.T) {
	same := `{"head":{"ref":"apo/x","repo":{"full_name":"lu/greeter"}},"base":{"repo":{"full_name":"lu/greeter"}}}`
	for _, tc := range []struct {
		name      string
		pull      reply
		deletion  reply
		wantErr   bool
		wantCalls int32
	}{
		{"deleted", reply{body: same}, reply{status: 204}, false, 1},
		{"already gone", reply{body: same}, reply{status: 404, body: `{"message":"Reference does not exist"}`}, false, 1},
		{"not a ref any more", reply{body: same}, reply{status: 422, body: `{"message":"Reference does not exist"}`}, false, 1},
		{"deletion refused", reply{body: same}, reply{status: 500, body: `boom`}, true, 1},
		{"a fork's branch is left", reply{body: `{"head":{"ref":"apo/x","repo":{"full_name":"x/greeter"}},"base":{"repo":{"full_name":"lu/greeter"}}}`}, reply{status: 204}, false, 0},
		{"a deleted fork's branch is left", reply{body: `{"head":{"ref":"apo/x","repo":null},"base":{"repo":{"full_name":"lu/greeter"}}}`}, reply{status: 204}, false, 0},
		{"no ref to delete", reply{body: `{"head":{"ref":"","repo":{"full_name":"lu/greeter"}},"base":{"repo":{"full_name":"lu/greeter"}}}`}, reply{status: 204}, false, 0},
		{"pull unreadable", reply{status: 500, body: `boom`}, reply{status: 204}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deletes atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("PUT /repos/lu/greeter/pulls/5/merge", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"merged":true}`)) })
			mux.HandleFunc("GET /repos/lu/greeter/pulls/5", func(w http.ResponseWriter, _ *http.Request) {
				if tc.pull.status != 0 {
					w.WriteHeader(tc.pull.status)
				}
				_, _ = w.Write([]byte(tc.pull.body))
			})
			mux.HandleFunc("DELETE /repos/lu/greeter/git/refs/heads/apo/x", func(w http.ResponseWriter, _ *http.Request) {
				deletes.Add(1)
				w.WriteHeader(tc.deletion.status)
				_, _ = w.Write([]byte(tc.deletion.body))
			})
			ts := httptest.NewServer(mux)
			defer ts.Close()
			err := adapterFor(ts).MergeMR(context.Background(), repoport.MRRef{IID: 5}, repoport.MergeOptions{DeleteBranch: true})
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v", err)
			}
			if deletes.Load() != tc.wantCalls {
				t.Errorf("deletes = %d", deletes.Load())
			}
		})
	}
}

// Nothing asked, nothing pinned: the method defaults, no sha travels, and the
// branch stays.
func TestMergeMRDefaults(t *testing.T) {
	var body map[string]any
	var reads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /repos/lu/greeter/pulls/5/merge", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /repos/lu/greeter/pulls/5", func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_, _ = w.Write([]byte(`{}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if err := adapterFor(ts).MergeMR(context.Background(), repoport.MRRef{IID: 5}, repoport.MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	if body["merge_method"] != string(repoport.MergeMethodMerge) {
		t.Errorf("method = %v", body["merge_method"])
	}
	if _, has := body["sha"]; has {
		t.Errorf("an unpinned merge sends no sha: %v", body)
	}
	if reads.Load() != 0 {
		t.Error("a kept branch needs no second read")
	}
}

// A branch name is one ref whatever it holds: a # in it is not a URL fragment.
func TestMergeMRDeletesTheExactRef(t *testing.T) {
	var deleted string
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /repos/lu/greeter/pulls/5/merge", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	mux.HandleFunc("GET /repos/lu/greeter/pulls/5", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"head":{"ref":"fix/#12","repo":{"full_name":"lu/greeter"}},"base":{"repo":{"full_name":"lu/greeter"}}}`))
	})
	mux.HandleFunc("DELETE /", func(w http.ResponseWriter, r *http.Request) {
		deleted = r.URL.Path
		w.WriteHeader(204)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if err := adapterFor(ts).MergeMR(context.Background(), repoport.MRRef{IID: 5}, repoport.MergeOptions{DeleteBranch: true}); err != nil {
		t.Fatal(err)
	}
	if deleted != "/repos/lu/greeter/git/refs/heads/fix/#12" {
		t.Errorf("deleted %q", deleted)
	}
}

func TestPostReviewPagesAndFailures(t *testing.T) {
	rev := repoport.Review{ID: "run/p-1/post", Notes: []repoport.ReviewNote{{Path: "z.go", Line: 2, Body: " spaced "}}}
	marker := strings.ReplaceAll(repoport.ReviewMarker(rev.ID, "review"), `"`, `\"`)

	t.Run("the marker on a later page is still the review", func(t *testing.T) {
		h, calls := paged(repeat(100, `{"body":"other"}`), `[{"body":"x `+marker+`"}]`)
		mux := http.NewServeMux()
		mux.HandleFunc("GET /repos/lu/greeter/pulls/5/reviews", h)
		ts := httptest.NewServer(mux)
		defer ts.Close()
		res, err := adapterFor(ts).PostReview(context.Background(), repoport.MRRef{IID: 5}, rev)
		if err != nil || !res.Already || calls.Load() != 2 {
			t.Errorf("res %+v err %v calls %d", res, err, calls.Load())
		}
	})

	t.Run("a note on a later page of files goes on its line", func(t *testing.T) {
		var posted map[string]any
		h, calls := paged(repeat(100, `{"filename":"a.go","patch":""}`), `[{"filename":"z.go","patch":"@@ -1,1 +1,2 @@\n package z\n+func x() {}"}]`)
		mux := http.NewServeMux()
		mux.HandleFunc("GET /repos/lu/greeter/pulls/5/reviews", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
		mux.HandleFunc("GET /repos/lu/greeter/pulls/5/files", h)
		mux.HandleFunc("POST /repos/lu/greeter/pulls/5/reviews", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&posted)
			_, _ = w.Write([]byte(`{}`))
		})
		ts := httptest.NewServer(mux)
		defer ts.Close()
		res, err := adapterFor(ts).PostReview(context.Background(), repoport.MRRef{IID: 5}, rev)
		if err != nil || res.Inline != 1 || res.Folded != 0 || calls.Load() != 2 {
			t.Fatalf("res %+v err %v calls %d", res, err, calls.Load())
		}
		if _, has := posted["commit_id"]; has {
			t.Errorf("no head, no commit_id: %v", posted)
		}
		if got := posted["body"]; got != repoport.ReviewMarker(rev.ID, "review") {
			t.Errorf("an empty summary leaves only the marker: %q", got)
		}
		c := posted["comments"].([]any)[0].(map[string]any)
		if !strings.HasPrefix(c["body"].(string), "spaced\n\n") {
			t.Errorf("note body = %q", c["body"])
		}
	})

	for name, routes := range map[string]map[string]reply{
		"reviews unreadable": {"GET /repos/lu/greeter/pulls/5/reviews": {status: 500, body: `boom`}},
		"files unreadable": {"GET /repos/lu/greeter/pulls/5/reviews": {body: `[]`},
			"GET /repos/lu/greeter/pulls/5/files": {status: 500, body: `boom`}},
		"post refused": {"GET /repos/lu/greeter/pulls/5/reviews": {body: `[]`}, "GET /repos/lu/greeter/pulls/5/files": {body: `[]`},
			"POST /repos/lu/greeter/pulls/5/reviews": {status: 422, body: `{"message":"Unprocessable"}`}},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := adapterFor(answer(t, routes)).PostReview(context.Background(), repoport.MRRef{IID: 5}, rev)
			if err == nil || res != (repoport.ReviewPosted{}) {
				t.Errorf("res %+v, err %v", res, err)
			}
		})
	}
}

func TestAccountFailuresAndPaging(t *testing.T) {
	ctx := context.Background()
	if err := NewAccount("", "").Test(ctx); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("no token: %v", err)
	}

	h, calls := paged(repeat(100, `{"full_name":"lu/a","clone_url":"u","default_branch":"main"}`), `[{"full_name":"lu/b"}]`)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/repos", h)
	mux.HandleFunc("GET /repos/lu/a", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"default_branch":"main"}`)) })
	bh, bcalls := paged(repeat(100, `{"name":"x"}`), `[{"name":"main"}]`)
	mux.HandleFunc("GET /repos/lu/a/branches", bh)
	mux.HandleFunc("GET /repos/lu/gone/branches", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	mux.HandleFunc("GET /repos/lu/nobranches", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	mux.HandleFunc("GET /repos/lu/nobranches/branches", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ac := NewAccount(ts.URL, secret)

	repos, err := ac.ListRepos(ctx)
	if err != nil || len(repos) != 101 || repos[100].Locator != "lu/b" || calls.Load() != 2 {
		t.Errorf("%d repos, %d calls, %v", len(repos), calls.Load(), err)
	}
	branches, err := ac.ListBranches(ctx, "lu/a")
	if err != nil || len(branches) != 101 || bcalls.Load() != 2 {
		t.Fatalf("%d branches, %d calls, %v", len(branches), bcalls.Load(), err)
	}
	if !branches[100].Default || branches[0].Default {
		t.Errorf("default flags: first %+v last %+v", branches[0], branches[100])
	}
	if _, err := ac.ListBranches(ctx, "lu/missing"); err == nil {
		t.Error("an unreadable repository is an error")
	}
	if _, err := ac.ListBranches(ctx, "lu/nobranches"); err == nil {
		t.Error("unreadable branches are an error")
	}

	down := answer(t, map[string]reply{"GET /user/repos": {status: 500, body: `boom`}, "GET /user": {status: 401, body: `{"message":"Bad credentials"}`}})
	bad := NewAccount(down.URL, secret)
	if got, err := bad.ListRepos(ctx); err == nil || got != nil {
		t.Errorf("repos failure: %v, %v", got, err)
	}
	err = bad.Test(ctx)
	if err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("bad token: %v", err)
	}
	noSecret(t, err)
}

func issuesFor(ts *httptest.Server, repos ...string) *Issues {
	return NewIssues(IssuesConfig{BaseURL: ts.URL, Token: secret, Repos: repos})
}

func TestIssuesTest(t *testing.T) {
	ctx := context.Background()
	if err := NewIssues(IssuesConfig{}).Test(ctx); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("no token: %v", err)
	}
	ok := answer(t, map[string]reply{"GET /user": {body: `{"login":"lu"}`}})
	if err := issuesFor(ok).Test(ctx); err != nil {
		t.Errorf("good token: %v", err)
	}
	bad := answer(t, map[string]reply{"GET /user": {status: 401, body: `{"message":"Bad credentials"}`}})
	err := issuesFor(bad).Test(ctx)
	if err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("bad token: %v", err)
	}
	noSecret(t, err)
}

func TestIssuesParseKey(t *testing.T) {
	one := NewIssues(IssuesConfig{Repos: []string{" lu/greeter.git ", "  "}})
	two := NewIssues(IssuesConfig{Repos: []string{"lu/a", "lu/b"}})
	for _, tc := range []struct {
		s       *Issues
		key     string
		repo    string
		n       int
		wantErr string
	}{
		{one, "lu/other#7", "lu/other", 7, ""},
		{one, "#7", "lu/greeter", 7, ""},
		{one, " 7 ", "lu/greeter", 7, ""},
		{two, "lu/b#7", "lu/b", 7, ""},
		{two, "#7", "", 0, "names no repository"},
		{one, "PROJ-7", "", 0, "is not owner/repo#number"},
		{one, "lu/other#", "", 0, "is not owner/repo#number"},
		{one, "", "", 0, "is not owner/repo#number"},
	} {
		repo, n, err := tc.s.parseKey(tc.key)
		if repo != tc.repo || n != tc.n {
			t.Errorf("%q = %q, %d", tc.key, repo, n)
		}
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%q err = %v", tc.key, err)
		}
	}
}

func TestIssuesSearchFailuresAndScope(t *testing.T) {
	ctx := context.Background()
	var q, perPage string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search/issues", func(w http.ResponseWriter, r *http.Request) {
		q, perPage = r.URL.Query().Get("q"), r.URL.Query().Get("per_page")
		_, _ = w.Write([]byte(`{"items":[{"number":9,"title":"no repo url","updated_at":"not a time"}]}`))
	})
	mux.HandleFunc("GET /repos/lu/a/issues/404", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	ts := httptest.NewServer(mux)
	defer ts.Close()

	for _, limit := range []int{0, -1, 101} {
		if _, err := issuesFor(ts, "lu/a").Search(ctx, "  ", limit); err != nil {
			t.Fatal(err)
		}
		if perPage != "25" {
			t.Errorf("limit %d = per_page %s", limit, perPage)
		}
	}
	if q != "is:issue repo:lu/a state:open assignee:@me" {
		t.Errorf("empty query = %q", q)
	}

	// A key that fetches nothing is still text to search for.
	got, err := issuesFor(ts, "lu/a").Search(ctx, "#404", 10)
	if err != nil || len(got) != 1 || perPage != "10" || q != "is:issue repo:lu/a #404" {
		t.Fatalf("got %+v, %v, q %q per_page %s", got, err, q, perPage)
	}
	if got[0].Key != "#9" || !got[0].Updated.IsZero() || got[0].Assignee != "" {
		t.Errorf("sparse issue = %+v", got[0])
	}

	down := answer(t, map[string]reply{"GET /search/issues": {status: 422, body: `{"message":"Validation Failed"}`}})
	if got, err := issuesFor(down).Search(ctx, "x", 5); err == nil || got != nil {
		t.Errorf("failure = %+v, %v", got, err)
	}
}

func TestIssuesFetchFailures(t *testing.T) {
	ctx := context.Background()
	ts := answer(t, map[string]reply{
		"GET /repos/lu/a/issues/1": {status: 404, body: `{"message":"Not Found"}`},
		"GET /repos/lu/a/issues/2": {body: `{"number":2,"title":"PR","pull_request":{"url":"x"}}`},
		"GET /repos/lu/a/issues/3": {body: `{"number":3,"title":"bare"}`},
	})
	s := issuesFor(ts, "lu/a")
	if _, err := s.Fetch(ctx, "nonsense"); err == nil {
		t.Error("a bad key is an error")
	}
	if _, err := s.Fetch(ctx, "#1"); err == nil {
		t.Error("a missing issue is an error")
	}
	if _, err := s.Fetch(ctx, "#2"); err == nil || !strings.Contains(err.Error(), "is a pull request") {
		t.Errorf("pull request = %v", err)
	}
	// Without repository_url the key still names the repository asked for.
	if got, err := s.Fetch(ctx, "#3"); err != nil || got.Key != "lu/a#3" {
		t.Errorf("fallback = %+v, %v", got, err)
	}
	if rc, err := s.OpenAttachment(ctx, trackerport.SourceAttachment{}); err == nil || rc != nil {
		t.Errorf("issues have no attachments: %v, %v", rc, err)
	}
}

func TestIssuesWriteFailures(t *testing.T) {
	ctx := context.Background()
	s := issuesFor(answer(t, nil), "lu/a")
	bad := "not a key"
	calls := map[string]error{
		"transition": s.Transition(ctx, bad, "open"),
		"comment":    s.Comment(ctx, bad, "x"),
		"set fields": s.SetFields(ctx, bad, map[string]string{"labels": "a"}),
		"add link":   s.AddLink(ctx, bad, trackerport.TicketLink{URL: "https://x"}),
	}
	_, calls["comments"] = s.ListComments(ctx, bad)
	_, calls["fields"] = s.Fields(ctx, bad, []string{"labels"})
	_, calls["attach"] = s.Attach(ctx, bad, "a", "text/plain", nil)
	for name, err := range calls {
		if err == nil || !strings.Contains(err.Error(), "not owner/repo#number") {
			t.Errorf("%s: %v", name, err)
		}
	}

	// The stub knows nothing: each call reports the host's 404.
	if err := s.Transition(ctx, "#1", "closed"); err == nil {
		t.Error("transition: a missing issue is an error")
	}
	if _, err := s.ListComments(ctx, "#1"); err == nil {
		t.Error("comments: a missing issue is an error")
	}
	if _, err := s.Fields(ctx, "#1", nil); err == nil {
		t.Error("fields: a missing issue is an error")
	}
	if err := s.AddLink(ctx, "#1", trackerport.TicketLink{URL: "https://x"}); err == nil {
		t.Error("add link: unreadable comments are an error, not a second link")
	}
	if err := s.AddLink(ctx, "#1", trackerport.TicketLink{URL: "   "}); err == nil || !strings.Contains(err.Error(), "needs a URL") {
		t.Errorf("empty link = %v", err)
	}
	if err := s.SetFields(ctx, "#1", map[string]string{"milestone": "v1"}); err == nil {
		t.Error("set fields: an unreadable milestone list is an error")
	}
	if err := s.SetFields(ctx, "#1", nil); err != nil {
		t.Errorf("no fields is no call: %v", err)
	}
}

func TestIssuesListCommentsPagesToTheEnd(t *testing.T) {
	h, calls := paged(repeat(100, `{"id":1,"body":"x"}`), `[{"id":2,"body":"last"}]`)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/a/issues/1/comments", h)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	got, err := issuesFor(ts, "lu/a").ListComments(context.Background(), "#1")
	if err != nil || len(got) != 101 || got[100].Body != "last" || got[100].ID != "2" || calls.Load() != 2 {
		t.Errorf("%d comments, %d calls, %v", len(got), calls.Load(), err)
	}
}

// A link is posted once; a link with no title says Link.
func TestIssuesAddLinkDefaultsTitleAndDeduplicates(t *testing.T) {
	var posted string
	marker := linkMarker("https://mr/1")
	comments := `[]`
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/lu/a/issues/1/comments", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(comments)) })
	mux.HandleFunc("POST /repos/lu/a/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var b map[string]string
		_ = json.Unmarshal(data, &b)
		posted = b["body"]
		_, _ = w.Write([]byte(`{}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s := issuesFor(ts, "lu/a")
	if err := s.AddLink(context.Background(), "#1", trackerport.TicketLink{URL: "https://mr/1"}); err != nil {
		t.Fatal(err)
	}
	if want := "Link: https://mr/1\n\n" + marker; posted != want {
		t.Errorf("posted %q, want %q", posted, want)
	}
	posted = ""
	enc, _ := json.Marshal(marker)
	comments = `[{"body":` + string(enc) + `}]`
	if err := s.AddLink(context.Background(), "#1", trackerport.TicketLink{URL: "https://mr/1"}); err != nil || posted != "" {
		t.Errorf("second link posted %q, %v", posted, err)
	}
}

func TestIssuesSetFieldsAndFieldsShapes(t *testing.T) {
	var patch map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /repos/lu/a/issues/1", func(w http.ResponseWriter, r *http.Request) {
		patch = nil
		_ = json.NewDecoder(r.Body).Decode(&patch)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /repos/lu/a/milestones", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"number":4,"title":"Later"},{"number":2,"title":"V1"}]`))
	})
	mux.HandleFunc("GET /repos/lu/a/issues/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"number":1,"milestone":null,"labels":[{"name":"a"},{"name":"b"}],"assignees":[{"login":"x"},{"login":"y"}]}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s := issuesFor(ts, "lu/a")
	ctx := context.Background()

	// Emptying a list sends an empty list, and a blank milestone clears it.
	if err := s.SetFields(ctx, "#1", map[string]string{"Labels": " , ", "assignees": "", "milestone": " "}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"labels": []any{}, "assignees": []any{}, "milestone": nil}
	if !reflect.DeepEqual(patch, want) {
		t.Errorf("patch = %#v", patch)
	}
	if err := s.SetFields(ctx, "#1", map[string]string{"milestone": "v1", "labels": "x, y"}); err != nil {
		t.Fatal(err)
	}
	if patch["milestone"] != float64(2) || !reflect.DeepEqual(patch["labels"], []any{"x", "y"}) {
		t.Errorf("patch = %#v", patch)
	}

	var unknown *trackerport.ErrFieldUnknown
	if err := s.SetFields(ctx, "#1", map[string]string{"milestone": "v9"}); !errors.As(err, &unknown) || !reflect.DeepEqual(unknown.Candidates, []string{"Later", "V1"}) {
		t.Errorf("unknown milestone = %v", err)
	}
	var unsupported *trackerport.ErrFieldUnsupported
	if err := s.SetFields(ctx, "#1", map[string]string{"priority": "1"}); !errors.As(err, &unsupported) {
		t.Errorf("unsupported field = %v", err)
	}
	if _, err := s.Fields(ctx, "#1", []string{"priority"}); !errors.As(err, &unsupported) {
		t.Errorf("unsupported read = %v", err)
	}
	got, err := s.Fields(ctx, "#1", []string{"labels", "assignees", "Milestone"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]string{"labels": "a, b", "assignees": "x, y", "Milestone": ""}) {
		t.Errorf("fields = %v", got)
	}
}
