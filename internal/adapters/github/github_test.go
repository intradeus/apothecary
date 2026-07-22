package github

import (
	"context"
	"encoding/json"
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
	ref, err := a.CreateDraftMR(context.Background(), repo, "apothecary/x", "Add greeting", "body text")
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

	ref, err := a.CreateDraftMR(context.Background(), repo, "apothecary/x", "Add greeting", "body")
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
		got, err := a.MRState(ctx, ports.MRRef{IID: iid})
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
