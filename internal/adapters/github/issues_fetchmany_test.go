package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"testing"
	"time"

	"apothecary/internal/ports/trackerport"
)

var aliasRe = regexp.MustCompile(`i(\d+):issue\(number:(\d+)\)`)

// graphqlStub serves a GitHub Enterprise layout (REST under /api/v3, GraphQL
// at /api/graphql): issue 5 of lu/greeter as REST and GraphQL both, every
// odd number of lu/greeter and lu/other as GraphQL, every even one (but 5's
// repo's 6 — a pull request) NOT_FOUND. It records each query's repository
// and alias count.
func graphqlStub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var queries []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/lu/greeter/issues/5", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(issueJSONBody))
	})
	mux.HandleFunc("POST /api/graphql", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		var req struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		repo := req.Variables["owner"] + "/" + req.Variables["name"]
		aliases := aliasRe.FindAllStringSubmatch(req.Query, -1)
		queries = append(queries, fmt.Sprintf("%s ×%d", repo, len(aliases)))
		data := map[string]any{}
		var errs []map[string]any
		for _, m := range aliases {
			n, _ := strconv.Atoi(m[2])
			switch {
			case repo == "lu/greeter" && n == 5:
				data["i5"] = map[string]any{"databaseId": 1005, "number": 5, "title": "Flaky router test",
					"body": "It fails **sometimes**.\n", "url": "https://github.com/lu/greeter/issues/5", "state": "OPEN",
					"updatedAt": "2026-07-30T11:22:33Z", "repository": map[string]any{"nameWithOwner": "lu/greeter"},
					"labels":    map[string]any{"nodes": []any{map[string]any{"name": "bug"}}},
					"assignees": map[string]any{"nodes": []any{map[string]any{"login": "lu"}}}}
			case n%2 == 1:
				data["i"+m[1]] = map[string]any{"databaseId": 9000 + n, "number": n, "title": "t", "url": "u", "state": "CLOSED",
					"updatedAt": "2026-07-30T11:22:33Z", "repository": map[string]any{"nameWithOwner": repo},
					"labels": map[string]any{"nodes": []any{}}, "assignees": map[string]any{"nodes": []any{}}}
			default:
				data["i"+m[1]] = nil
				errs = append(errs, map[string]any{"type": "NOT_FOUND", "path": []any{"repository", "i" + m[1]},
					"message": "Could not resolve to an Issue with the number of " + m[2] + "."})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": data}, "errors": errs})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &queries
}

// FetchMany reads a repository's issues a hundred per GraphQL query (on GHE
// at /api/graphql), leaves out the ones GitHub does not resolve, and maps an
// issue exactly as Fetch does.
func TestIssuesFetchManyBatchesGraphQL(t *testing.T) {
	srv, queries := graphqlStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL + "/api/v3", Token: "tok", Repos: []string{"lu/greeter"}})
	ctx := context.Background()

	keys := []string{"#5", "lu/greeter#6", "lu/other#1", "lu/greeter#5"}
	for n := 7; n < 7+250; n++ {
		keys = append(keys, fmt.Sprintf("lu/greeter#%d", n))
	}
	got, err := s.FetchMany(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	// lu/greeter: 5, 6 and 7…256 (252 numbers, 5 given twice) → 100+100+52;
	// lu/other: one.
	want := []string{"lu/greeter ×100", "lu/greeter ×100", "lu/greeter ×52", "lu/other ×1"}
	if !reflect.DeepEqual(*queries, want) {
		t.Errorf("queries = %v, want %v", *queries, want)
	}
	byKey := map[string]trackerport.SourceTicket{}
	for _, tk := range got {
		byKey[tk.Key] = tk
	}
	// 5, the 125 odd numbers in 7…256, and lu/other#1.
	if len(got) != 127 || len(byKey) != 127 {
		t.Errorf("got %d tickets (%d keys), want 127", len(got), len(byKey))
	}
	if _, ok := byKey["lu/greeter#6"]; ok {
		t.Error("an unresolved number (a pull request, a deleted issue) was not left out")
	}
	if tk := byKey["lu/other#1"]; tk.RemoteID != "9001" || tk.StatusID != "closed" || tk.Labels != nil {
		t.Errorf("lu/other#1 = %+v", tk)
	}

	rest, err := s.Fetch(ctx, "lu/greeter#5")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(byKey["lu/greeter#5"], rest) {
		t.Errorf("batched = %+v\n   REST = %+v", byKey["lu/greeter#5"], rest)
	}
}

// A malformed key fails the batch, as Fetch would; an empty list asks nothing.
func TestIssuesFetchManyRefusesBadKeys(t *testing.T) {
	srv, queries := graphqlStub(t)
	s := NewIssues(IssuesConfig{BaseURL: srv.URL + "/api/v3", Token: "tok"})
	if _, err := s.FetchMany(context.Background(), []string{"#5"}); err == nil {
		t.Error("a bare number with no configured repo was accepted")
	}
	if got, err := s.FetchMany(context.Background(), nil); err != nil || len(got) != 0 || len(*queries) != 0 {
		t.Errorf("empty = %v, %v, queries %v", got, err, *queries)
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
	if _, err := s.FetchMany(ctx, []string{"lu/greeter#3"}); !errors.As(err, &rl) || rl.RetryAfter < 80*time.Second || rl.RetryAfter > 90*time.Second {
		t.Errorf("RATE_LIMITED = %v (%+v)", err, rl)
	}
}
