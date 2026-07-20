package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"factory/internal/ports"
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
		if r.URL.Query().Get("path") == ".factory/agents" {
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
	ok, err = a.FileExists(ctx, repo, ".factory/agents/")
	if err != nil || !ok {
		t.Errorf(".factory/agents/ = %v, %v", ok, err)
	}
	ok, _ = a.FileExists(ctx, repo, "empty-dir/")
	if ok {
		t.Error("empty dir must report false")
	}
	joined := strings.Join(*calls, " ")
	if !strings.Contains(joined, "tree:.factory/agents") {
		t.Errorf("directory check should use tree endpoint: %v", *calls)
	}
	if !strings.Contains(joined, "ref=main") {
		t.Errorf("file check must pin the default branch ref: %v", *calls)
	}
}

func TestCreateDraftMRPrefixesTitle(t *testing.T) {
	ts, calls := glStub(t)
	a, repo := testAdapter(ts)
	ref, err := a.CreateDraftMR(context.Background(), repo, "factory/x-add-login", "Add login", "body text")
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
	for _, want := range []string{`"title":"Draft: Add login"`, `"source_branch":"factory/x-add-login"`, `"target_branch":"main"`, `"description":"body text"`} {
		if !strings.Contains(mrCall, want) {
			t.Errorf("MR payload missing %s: %s", want, mrCall)
		}
	}
}

func TestMRStateMapping(t *testing.T) {
	ts, _ := glStub(t)
	a, _ := testAdapter(ts)
	st, err := a.MRState(context.Background(), ports.MRRef{IID: 3})
	if err != nil || st != ports.MRMerged {
		t.Errorf("state = %v, %v", st, err)
	}
}
