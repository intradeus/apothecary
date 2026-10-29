package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"apothecary/internal/ports"
)

// issueBody is a v3 issue response with an ADF description and one attachment.
const issueBody = `{
  "key": "PROJ-7",
  "fields": {
    "summary": "Add a Farewell function",
    "description": {"type":"doc","version":1,"content":[
      {"type":"paragraph","content":[{"type":"text","text":"The greeter only says hello."}]}
    ]},
    "status": {"name": "To Do"},
    "issuetype": {"name": "Story"},
    "assignee": {"displayName": "Dev User"},
    "updated": "2026-07-30T11:22:33.000+0200",
    "attachment": [
      {"id": 10001, "filename": "spec.md", "mimeType": "text/markdown", "size": 12,
       "content": "%s/rest/api/3/attachment/content/10001"}
    ]
  }
}`

func TestFetchMapsIssueAndAttachments(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-7":
			if got := r.URL.Query().Get("fields"); !strings.Contains(got, "attachment") {
				t.Errorf("fetch did not request attachments: fields=%q", got)
			}
			w.Write([]byte(strings.Replace(issueBody, "%s", srv.URL, 1)))
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/attachment/content/"):
			if r.Header.Get("Authorization") == "" {
				t.Error("attachment download sent no credential")
			}
			w.Write([]byte("# Farewell"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "you@acme.com", Token: "tok"})
	tk, err := c.Fetch(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Title != "Add a Farewell function" || tk.Status != "To Do" || tk.Type != "Story" {
		t.Errorf("mapped ticket = %+v", tk)
	}
	if tk.ChangeType != "" {
		t.Errorf("change type = %q with no type map, want it left to the importer", tk.ChangeType)
	}
	mapped, err := New(Config{BaseURL: srv.URL, Email: "you@acme.com", Token: "tok",
		TypeMap: map[string]ports.ChangeType{"story": ports.ChangeChore}}).Fetch(context.Background(), "PROJ-7")
	if err != nil || mapped.ChangeType != ports.ChangeChore {
		t.Errorf("mapped change type = %q (%v), want the type map's chore", mapped.ChangeType, err)
	}
	if tk.Body != "The greeter only says hello." {
		t.Errorf("body = %q, want the ADF rendered to markdown", tk.Body)
	}
	if tk.URL != srv.URL+"/browse/PROJ-7" {
		t.Errorf("url = %q", tk.URL)
	}
	if tk.Updated.IsZero() {
		t.Error("updated timestamp did not parse (Jira's offset has no colon)")
	}
	if len(tk.Attachments) != 1 || tk.Attachments[0].Filename != "spec.md" || tk.Attachments[0].ID != "10001" {
		t.Fatalf("attachments = %+v", tk.Attachments)
	}

	rc, err := c.OpenAttachment(context.Background(), tk.Attachments[0])
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "# Farewell" {
		t.Errorf("attachment bytes = %q", data)
	}
}

// A tracker must not be able to aim an authenticated engine request at a host
// of its choosing: an attachment URL outside the configured site is refused.
func TestOpenAttachmentRefusesForeignHost(t *testing.T) {
	c := New(Config{BaseURL: "https://acme.atlassian.net", Email: "a@b.c", Token: "tok"})
	_, err := c.OpenAttachment(context.Background(),
		ports.SourceAttachment{Filename: "evil.md", URL: "https://evil.example/steal"})
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("err = %v, want a refusal naming the foreign host", err)
	}
}

// Search uses Cloud's /search/jql; a deployment that 404s it (Data Center)
// must fall back to the classic /search rather than reporting a search error.
func TestSearchFallsBackToClassicEndpoint(t *testing.T) {
	var hitNew, hitOld bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/search/jql":
			hitNew = true
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errorMessages":["gone"]}`))
		case "/rest/api/3/search":
			hitOld = true
			if jql := r.URL.Query().Get("jql"); !strings.Contains(jql, `text ~ "router*"`) {
				t.Errorf("jql = %q", jql)
			}
			w.Write([]byte(`{"issues":[{"key":"PROJ-9","fields":{"summary":"Router","description":"plain text"}}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "a@b.c", Token: "tok"})
	got, err := c.Search(context.Background(), "router", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !hitNew || !hitOld {
		t.Fatalf("endpoints tried: new=%v old=%v", hitNew, hitOld)
	}
	if len(got) != 1 || got[0].Key != "PROJ-9" || got[0].Body != "plain text" {
		t.Fatalf("results = %+v", got)
	}
}

func TestSearchPostsFieldsAndLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JQL        string   `json:"jql"`
			MaxResults int      `json:"maxResults"`
			Fields     []string `json:"fields"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.MaxResults != 5 {
			t.Errorf("maxResults = %d", body.MaxResults)
		}
		if len(body.Fields) == 0 {
			t.Error("no fields requested — /search/jql answers with ids only")
		}
		if !strings.Contains(body.JQL, `key = "PROJ-7"`) {
			t.Errorf("jql = %q, want a direct key lookup", body.JQL)
		}
		w.Write([]byte(`{"issues":[]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "a@b.c", Token: "tok"})
	if _, err := c.Search(context.Background(), "proj-7", 5); err != nil {
		t.Fatal(err)
	}
}

// A bare number is what people actually type ("2624"), and Jira's own quick
// search resolves it. JQL cannot match a key by its number half, so the
// candidate key is built from the configured project filter and fetched
// directly — with no project-listing request needed.
func TestSearchByBareNumberUsesConfiguredProject(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/rest/api/3/issue/PROJ-2624":
			w.Write([]byte(`{"key":"PROJ-2624","fields":{"summary":"The one you meant"}}`))
		case "/rest/api/3/search/jql":
			w.Write([]byte(`{"issues":[{"key":"PROJ-9","fields":{"summary":"mentions 2624 in passing"}}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "a@b.c", Token: "tok", JQL: "project = PROJ"})
	got, err := c.Search(context.Background(), "2624", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "PROJ-2624" {
		t.Fatalf("results = %+v, want the exact key first then text hits", got)
	}
	for _, p := range paths {
		if strings.Contains(p, "/project") {
			t.Errorf("listed projects despite a configured filter (%s)", p)
		}
	}
}

// With no filter configured the candidates come from the projects the account
// can see, cached after the first lookup; a project without that issue 404s
// and is simply not a hit.
func TestSearchByBareNumberProbesVisibleProjects(t *testing.T) {
	var listings int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/project/search"):
			listings++
			w.Write([]byte(`{"values":[{"key":"AAA"},{"key":"BBB"}]}`))
		case r.URL.Path == "/rest/api/3/issue/BBB-2624":
			w.Write([]byte(`{"key":"BBB-2624","fields":{"summary":"found"}}`))
		case r.URL.Path == "/rest/api/3/issue/AAA-2624":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errorMessages":["Issue does not exist"]}`))
		case r.URL.Path == "/rest/api/3/search/jql":
			w.Write([]byte(`{"issues":[]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "a@b.c", Token: "tok"})
	got, err := c.Search(context.Background(), "#2624", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "BBB-2624" {
		t.Fatalf("results = %+v, want only the project that has it", got)
	}
	if _, err := c.Search(context.Background(), "2624", 10); err != nil {
		t.Fatal(err)
	}
	if listings != 1 {
		t.Errorf("listed projects %d times, want a cached single lookup", listings)
	}
}

// A number that resolved must survive the text half failing — half an answer
// beats an error page.
func TestBareNumberSurvivesTextSearchFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/issue/PROJ-2624" {
			w.Write([]byte(`{"key":"PROJ-2624","fields":{"summary":"found"}}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"errorMessages":["bad JQL"]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Email: "a@b.c", Token: "tok", JQL: "project = PROJ"})
	got, err := c.Search(context.Background(), "2624", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestProjectKeysFromJQL(t *testing.T) {
	for _, tc := range []struct {
		jql  string
		want []string
	}{
		{`project = PROJ`, []string{"PROJ"}},
		{`project="proj" AND resolution = Unresolved`, []string{"PROJ"}},
		{`project in (AAA, "BBB") ORDER BY updated`, []string{"AAA", "BBB"}},
		{`assignee = currentUser()`, nil},
	} {
		got := projectKeysFromJQL(tc.jql)
		if len(got) != len(tc.want) {
			t.Errorf("projectKeysFromJQL(%q) = %v, want %v", tc.jql, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("projectKeysFromJQL(%q) = %v, want %v", tc.jql, got, tc.want)
				break
			}
		}
	}
}

func TestJQL(t *testing.T) {
	c := New(Config{JQL: "project = PROJ"})
	for _, tc := range []struct{ query, want string }{
		{"", `(project = PROJ) AND (assignee = currentUser() AND resolution = Unresolved) ORDER BY updated DESC`},
		{"PROJ-12", `(project = PROJ) AND (key = "PROJ-12") ORDER BY updated DESC`},
		{"flaky router", `(project = PROJ) AND (text ~ "flaky router*") ORDER BY updated DESC`},
		{`jql:status = Done ORDER BY created DESC`, `(project = PROJ) AND (status = Done ORDER BY created DESC)`},
		{`say "hi"`, `(project = PROJ) AND (text ~ "say \"hi\"*") ORDER BY updated DESC`},
		// Typed loosely, still an exact key lookup.
		{"proj 2624", `(project = PROJ) AND (key = "PROJ-2624") ORDER BY updated DESC`},
		{"PROJ_2624", `(project = PROJ) AND (key = "PROJ-2624") ORDER BY updated DESC`},
		{"#PROJ-2624", `(project = PROJ) AND (key = "PROJ-2624") ORDER BY updated DESC`},
	} {
		if got := c.JQL(tc.query); got != tc.want {
			t.Errorf("JQL(%q) =\n %q\nwant %q", tc.query, got, tc.want)
		}
	}
	// With no scope filter the clause stands alone.
	if got := (New(Config{})).JQL("PROJ-1"); got != `key = "PROJ-1" ORDER BY updated DESC` {
		t.Errorf("unscoped JQL = %q", got)
	}
}

// A Cloud site with no email cannot authenticate — Cloud rejects bearer API
// tokens with the same generic 401 body a wrong password gives. Name it
// before spending a request, so "Test" explains the fix instead of echoing
// Jira's unhelpful sentence.
func TestCloudWithoutEmailFailsFast(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	// The guard keys off the host, so point at a Cloud-looking URL.
	c := New(Config{BaseURL: "https://acme.atlassian.net", Token: "tok"})
	err := c.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ACCOUNT EMAIL") {
		t.Fatalf("err = %v, want the missing-email diagnosis", err)
	}
	if called {
		t.Error("a request was sent for a credential that cannot work")
	}
	_ = srv
}

// A 401 that survives the guard carries the checklist: Jira's own body is the
// same sentence for every cause, so alone it diagnoses nothing.
func TestUnauthorizedCarriesChecklist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errorMessages":["Client must be authenticated to access this resource"]}`))
	}))
	defer srv.Close()

	err := New(Config{BaseURL: srv.URL, Email: "a@b.c", Token: "tok"}).Test(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"Client must be authenticated", "check:", "SCOPES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("401 error %q is missing %q", err, want)
		}
	}
}

// A token pasted from a browser often carries a trailing newline; it must not
// become a mystery 401.
func TestTokenIsTrimmed(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"displayName":"Dev"}`))
	}))
	defer srv.Close()

	if err := New(Config{BaseURL: srv.URL, Email: " a@b.c ", Token: "tok\n"}).Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("a@b.c:tok"))
	if auth != want {
		t.Errorf("Authorization = %q, want the trimmed pair", auth)
	}
}

func TestErrorsCarryJiraMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errorMessages":["Client must be authenticated"]}`))
	}))
	defer srv.Close()

	err := New(Config{BaseURL: srv.URL, Email: "a@b.c", Token: "tok"}).Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Client must be authenticated") {
		t.Fatalf("err = %v, want Jira's own message", err)
	}
}

func TestParseTypeMap(t *testing.T) {
	m, err := ParseTypeMap("Bug=fix, Task = chore\nSpike=CHORE")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ports.ChangeType{"bug": ports.ChangeFix, "task": ports.ChangeChore, "spike": ports.ChangeChore}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("type map = %v, want %v", m, want)
	}
	if m, err := ParseTypeMap(""); err != nil || len(m) != 0 {
		t.Errorf("empty = %v, %v", m, err)
	}
	for _, bad := range []string{"Bug", "Bug=bugfix", "=fix"} {
		if _, err := ParseTypeMap(bad); err == nil {
			t.Errorf("ParseTypeMap(%q) accepted it", bad)
		}
	}
}
