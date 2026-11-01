package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/ports/trackerport"
)

// secret is the token every failure test runs with: no error may carry it, in
// the clear or in the basic-auth encoding.
const secret = "ATATT-s3cr3tvalue"

type reply struct {
	status int
	body   string
}

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

func clientFor(ts *httptest.Server) *Client {
	return New(Config{BaseURL: ts.URL, Email: "you@acme.com", Token: secret})
}

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	basic := base64.StdEncoding.EncodeToString([]byte("you@acme.com:" + secret))
	if s := err.Error(); strings.Contains(s, secret) || strings.Contains(s, basic) {
		t.Errorf("error carries the credential: %v", err)
	}
}

func TestAPIFailuresAreNamedAndNeverCarryTheToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply reply
		want  string
	}{
		{"error messages", reply{403, `{"errorMessages":["You do not have permission"],"errors":{}}`}, "You do not have permission"},
		{"field errors", reply{400, `{"errorMessages":[],"errors":{"summary":"required"}}`}, "summary: required"},
		{"message", reply{429, `{"message":"Rate limit exceeded"}`}, "Rate limit exceeded"},
		{"html", reply{502, `  <html>Bad gateway</html>  `}, "<html>Bad gateway</html>"},
		{"json with nothing to say", reply{500, `{"status":"down"}`}, `{"status":"down"}`},
		{"not found", reply{404, `{"errorMessages":["Issue does not exist"]}`}, "Issue does not exist"},
		{"malformed body", reply{200, `{"key":`}, "decode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := answer(t, map[string]reply{"GET /rest/api/3/issue/PROJ-1": tc.reply})
			got, err := clientFor(ts).Fetch(context.Background(), "PROJ-1")
			if err == nil || got.Key != "" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %+v, err %v", got, err)
			}
			noSecret(t, err)
		})
	}
}

func TestErrTextBoundsAHugeBody(t *testing.T) {
	long := strings.Repeat("x", 1000)
	got := errText([]byte(long))
	if len(got) != 300+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("truncated to %d: %q", len(got), got[len(got)-10:])
	}
	if got := truncate("short", 300); got != "short" {
		t.Errorf("short = %q", got)
	}
}

func TestAPITransportFailures(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	c := clientFor(dead)
	dead.Close()
	_, err := c.Fetch(context.Background(), "PROJ-1")
	if err == nil {
		t.Error("a dead server is an error")
	}
	noSecret(t, err)

	live := answer(t, map[string]reply{"GET /rest/api/3/issue/PROJ-1": {body: `{}`}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := clientFor(live).Fetch(ctx, "PROJ-1"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled call: %v", err)
	}
	if _, err := New(Config{BaseURL: "http://bad host", Token: secret}).Fetch(context.Background(), "PROJ-1"); err == nil {
		t.Error("an unparsable base URL is an error")
	}
	if _, err := clientFor(live).api(context.Background(), http.MethodPost, "/x", make(chan int), nil); err == nil {
		t.Error("a body that cannot marshal is an error")
	}
}

func TestAuthorizeChoosesTheScheme(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"cloud", Config{Email: " you@acme.com ", Token: secret}, "Basic " + base64.StdEncoding.EncodeToString([]byte("you@acme.com:"+secret))},
		{"data center", Config{Token: secret}, "Bearer " + secret},
		{"anonymous", Config{Email: "you@acme.com"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			New(tc.cfg).authorize(req)
			if got := req.Header.Get("Authorization"); got != tc.want {
				t.Errorf("Authorization = %q", got)
			}
		})
	}
}

func TestTestFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{Token: secret}, "no base URL"},
		{Config{BaseURL: "https://acme.example"}, "no API token"},
		{Config{BaseURL: "https://acme.atlassian.net", Token: secret}, "ACCOUNT EMAIL"},
		{Config{BaseURL: "https://acme.jira.com", Token: secret}, "ACCOUNT EMAIL"},
	} {
		if err := New(tc.cfg).Test(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc.cfg, err)
		}
	}

	// A failure that is not a 401 gets no credential checklist.
	down := answer(t, map[string]reply{"GET /rest/api/3/myself": {503, `maintenance`}})
	err := clientFor(down).Test(ctx)
	if err == nil || strings.Contains(err.Error(), "check:") || !strings.Contains(err.Error(), "maintenance") {
		t.Errorf("503 = %v", err)
	}
	ok := answer(t, map[string]reply{"GET /rest/api/3/myself": {body: `{"displayName":"Lu"}`}})
	if err := clientFor(ok).Test(ctx); err != nil {
		t.Errorf("good credential: %v", err)
	}
}

// What to check depends on what was sent.
func TestHint401(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		has  []string
		not  []string
	}{
		{"bearer on a custom domain", Config{BaseURL: "https://jira.acme.example", Token: secret},
			[]string{"sent as a BEARER token", "API version to 2"}, []string{"login of the account"}},
		{"basic on cloud", Config{BaseURL: "https://acme.atlassian.net", Email: "a@b", Token: secret},
			[]string{"login of the account"}, []string{"BEARER", "API version to 2"}},
		{"self-hosted already on v2", Config{BaseURL: "https://jira.acme.example", Email: "a@b", Token: secret, APIVersion: "2"},
			[]string{"login of the account"}, []string{"API version to 2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := New(tc.cfg).hint401()
			for _, w := range tc.has {
				if !strings.Contains(got, w) {
					t.Errorf("hint lacks %q: %s", w, got)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(got, w) {
					t.Errorf("hint says %q: %s", w, got)
				}
			}
			if strings.Contains(got, secret) {
				t.Errorf("hint carries the token: %s", got)
			}
		})
	}
}

func TestIsCloud(t *testing.T) {
	for url, want := range map[string]bool{
		"https://acme.atlassian.net":         true,
		"https://ACME.Atlassian.NET/":        true,
		"https://acme.jira.com":              true,
		"acme.atlassian.net":                 true, // no scheme parses to a path
		"https://atlassian.net.evil.example": false,
		"https://jira.acme.example":          false,
	} {
		if got := New(Config{BaseURL: url}).isCloud(); got != want {
			t.Errorf("%s = %v", url, got)
		}
	}
}

func TestToTicketLooseShapes(t *testing.T) {
	c := New(Config{BaseURL: "https://j", TypeMap: map[string]trackerport.ChangeType{"bug": trackerport.ChangeFix}})
	var in issueJSON
	raw := `{"key":"P-1","fields":{"summary":"s","updated":"not a time","issuetype":{"name":" Bug "},
	  "attachment":[{"id":"10","filename":"a"},{"id":11,"filename":"b"},{"id":null,"filename":"c"}]}}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	got := c.toTicket(in)
	if got.URL != "https://j/browse/P-1" || got.ChangeType != trackerport.ChangeFix || !got.Updated.IsZero() {
		t.Errorf("ticket = %+v", got)
	}
	var ids []string
	for _, a := range got.Attachments {
		ids = append(ids, a.ID)
	}
	if !reflect.DeepEqual(ids, []string{"10", "11", ""}) {
		t.Errorf("ids = %q", ids)
	}
}

func TestSearchFallbacksAndFailures(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{404, 405, 410} {
		ts := answer(t, map[string]reply{
			"POST /rest/api/3/search/jql": {status, `{"errorMessages":["gone"]}`},
			"GET /rest/api/3/search":      {body: `{"issues":[{"key":"P-1","fields":{"summary":"s"}}]}`},
		})
		got, err := clientFor(ts).Search(ctx, "text", 5)
		if err != nil || len(got) != 1 || got[0].Key != "P-1" {
			t.Errorf("%d: %+v, %v", status, got, err)
		}
	}

	// The classic route failing too is a failure, and a server error on the
	// current route does not try the old one.
	both := answer(t, map[string]reply{
		"POST /rest/api/3/search/jql": {404, `{"errorMessages":["gone"]}`},
		"GET /rest/api/3/search":      {500, `{"errorMessages":["classic down"]}`},
	})
	if got, err := clientFor(both).Search(ctx, "text", 5); err == nil || got != nil || !strings.Contains(err.Error(), "classic down") {
		t.Errorf("both down = %+v, %v", got, err)
	}
	var classic atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rest/api/3/search/jql", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"errorMessages":["Error in the JQL Query"]}`))
	})
	mux.HandleFunc("GET /rest/api/3/search", func(w http.ResponseWriter, _ *http.Request) { classic.Add(1) })
	ts := httptest.NewServer(mux)
	defer ts.Close()
	got, err := clientFor(ts).Search(ctx, "jql: bad (", 0)
	if err == nil || got != nil || !strings.Contains(err.Error(), "Error in the JQL Query") {
		t.Errorf("bad JQL = %+v, %v", got, err)
	}
	if classic.Load() != 0 {
		t.Error("a rejected query must not be retried on the classic route")
	}
}

func TestSearchClampsTheLimit(t *testing.T) {
	var bodies []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		_, _ = w.Write([]byte(`{"issues":[]}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c := clientFor(ts)
	for _, limit := range []int{-1, 0, 101} {
		if _, err := c.Search(context.Background(), "x", limit); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range bodies {
		if b["maxResults"] != float64(25) {
			t.Errorf("maxResults = %v", b["maxResults"])
		}
	}
}

// A project's bare number: tried against the scope in order, a 404 is simply
// not that project, and the text search's copy is not listed twice.
func TestSearchBareNumberKeepsOrderAndDeduplicates(t *testing.T) {
	ts := answer(t, map[string]reply{
		"GET /rest/api/3/issue/A-7":   {404, `{"errorMessages":["no"]}`},
		"GET /rest/api/3/issue/B-7":   {body: `{"key":"B-7","fields":{"summary":"second"}}`},
		"GET /rest/api/3/issue/C-7":   {body: `{"key":"C-7","fields":{"summary":"third"}}`},
		"POST /rest/api/3/search/jql": {body: `{"issues":[{"key":"C-7","fields":{"summary":"dup"}},{"key":"D-1","fields":{"summary":"text hit"}}]}`},
	})
	c := New(Config{BaseURL: ts.URL, Email: "a@b", Token: secret, JQL: "project in (a, b, c)"})
	got, err := c.Search(context.Background(), "#7", 10)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, x := range got {
		keys = append(keys, x.Key)
	}
	if !reflect.DeepEqual(keys, []string{"B-7", "C-7", "D-1"}) {
		t.Errorf("keys = %v", keys)
	}
	if got[1].Title != "third" {
		t.Errorf("the direct fetch wins over the text hit: %+v", got[1])
	}
}

func TestScopeProjects(t *testing.T) {
	ctx := context.Background()

	t.Run("listed by the configured filter, no request", func(t *testing.T) {
		c := New(Config{BaseURL: "http://127.0.0.1:1", JQL: `project = "web" AND project in (API, "Ops", )`})
		if got := c.scopeProjects(ctx); !reflect.DeepEqual(got, []string{"WEB", "API", "OPS"}) {
			t.Errorf("projects = %v", got)
		}
	})

	t.Run("older Data Center serves a bare array", func(t *testing.T) {
		ts := answer(t, map[string]reply{
			"GET /rest/api/3/project/search": {404, `{}`},
			"GET /rest/api/3/project":        {body: `[{"key":"OLD"},{"key":""}]`},
		})
		if got := clientFor(ts).scopeProjects(ctx); !reflect.DeepEqual(got, []string{"OLD"}) {
			t.Errorf("projects = %v", got)
		}
	})

	t.Run("capped at the probe budget, cached", func(t *testing.T) {
		var values []string
		for i := range maxNumberProbes + 3 {
			values = append(values, fmt.Sprintf(`{"key":"P%d"}`, i))
		}
		var calls atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("GET /rest/api/3/project/search", func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte(`{"values":[` + strings.Join(values, ",") + `]}`))
		})
		ts := httptest.NewServer(mux)
		defer ts.Close()
		c := clientFor(ts)
		if got := c.scopeProjects(ctx); len(got) != maxNumberProbes {
			t.Errorf("projects = %v", got)
		}
		c.scopeProjects(ctx)
		if calls.Load() != 1 {
			t.Errorf("listed %d times", calls.Load())
		}
	})

	t.Run("nothing to read is remembered too", func(t *testing.T) {
		var calls atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("GET /rest/api/3/project/search", func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(500)
		})
		mux.HandleFunc("GET /rest/api/3/project", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
		ts := httptest.NewServer(mux)
		defer ts.Close()
		c := clientFor(ts)
		if got := c.scopeProjects(ctx); len(got) != 0 {
			t.Errorf("projects = %v", got)
		}
		c.scopeProjects(ctx)
		if calls.Load() != 1 {
			t.Errorf("probed %d times; the search box must not re-probe on every keystroke", calls.Load())
		}
	})
}

func TestFetchEdges(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://127.0.0.1:1"}).Fetch(context.Background(), "  "); err == nil || !strings.Contains(err.Error(), "no issue key") {
		t.Errorf("empty key = %v", err)
	}
	ts := answer(t, map[string]reply{"GET /rest/api/3/issue/P-1": {body: `{"fields":{"summary":"keyless"}}`}})
	got, err := clientFor(ts).Fetch(context.Background(), " P-1 ")
	if err != nil || got.Key != "P-1" {
		t.Errorf("a body with no key keeps the asked one: %+v, %v", got, err)
	}
}

func TestOpenAttachmentChecks(t *testing.T) {
	ctx := context.Background()
	var auth []string
	status, body := 200, "bytes"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /secure/a.txt", func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c := clientFor(ts)

	// A relative URL resolves against the site.
	rc, err := c.OpenAttachment(ctx, trackerport.SourceAttachment{Filename: "a.txt", URL: "/secure/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "bytes" || len(auth) != 1 || auth[0] == "" {
		t.Errorf("data %q auth %v", data, auth)
	}

	status, body = 403, `{"errorMessages":["No permission"]}`
	rc, err = c.OpenAttachment(ctx, trackerport.SourceAttachment{Filename: "a.txt", URL: ts.URL + "/secure/a.txt"})
	if err == nil || rc != nil || !strings.Contains(err.Error(), "download a.txt: 403: No permission") {
		t.Errorf("403 = %v, %v", rc, err)
	}
	noSecret(t, err)

	if _, err := c.OpenAttachment(ctx, trackerport.SourceAttachment{Filename: "a.txt"}); err == nil || !strings.Contains(err.Error(), "no content URL") {
		t.Errorf("no URL = %v", err)
	}
	if _, err := c.OpenAttachment(ctx, trackerport.SourceAttachment{Filename: "a.txt", URL: "http://[bad"}); err == nil {
		t.Error("an unparsable URL is an error")
	}
	if _, err := New(Config{BaseURL: "://bad"}).OpenAttachment(ctx, trackerport.SourceAttachment{URL: "http://x/y"}); err == nil {
		t.Error("an unparsable base URL is an error")
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	dead := clientFor(gone)
	u := gone.URL + "/x"
	gone.Close()
	if _, err := dead.OpenAttachment(ctx, trackerport.SourceAttachment{URL: u}); err == nil {
		t.Error("a dead server is an error")
	} else {
		noSecret(t, err)
	}
}

// Cloud redirects a download to a pre-signed media host. The credential stays
// behind: that is what makes the host check above worth having.
func TestOpenAttachmentDropsTheCredentialOnARedirect(t *testing.T) {
	var leaked atomic.Value
	leaked.Store("")
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("media bytes"))
	}))
	defer media.Close()
	// Same loopback, other name: net/http treats it as another host.
	mediaURL := strings.Replace(media.URL, "127.0.0.1", "localhost", 1)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, mediaURL+"/x", http.StatusFound)
	}))
	defer site.Close()
	rc, err := clientFor(site).OpenAttachment(context.Background(), trackerport.SourceAttachment{URL: site.URL + "/secure/a"})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "media bytes" || leaked.Load() != "" {
		t.Errorf("data %q, Authorization %q", data, leaked.Load())
	}
}

func TestJQLEdges(t *testing.T) {
	scoped := New(Config{JQL: "project = P"})
	plain := New(Config{})
	for _, tc := range []struct {
		c    *Client
		q    string
		want string
	}{
		{plain, "jql:", " ORDER BY updated DESC"},
		{scoped, "jql:", "project = P ORDER BY updated DESC"},
		{scoped, "jql: status = Done ORDER BY created", "(project = P) AND (status = Done ORDER BY created)"},
		{plain, "proj 12", `key = "PROJ-12" ORDER BY updated DESC`},
		{plain, "#PROJ_12", `key = "PROJ-12" ORDER BY updated DESC`},
		{plain, `say "hi"\`, `text ~ "say \"hi\"\\*" ORDER BY updated DESC`},
	} {
		if got := tc.c.JQL(tc.q); got != tc.want {
			t.Errorf("JQL(%q) = %q, want %q", tc.q, got, tc.want)
		}
	}
}

// Text a person typed is not a query's ORDER BY.
func TestJQLOrdersATextSearchForTheWordsOrderBy(t *testing.T) {
	if got, want := New(Config{}).JQL("order by date"), `text ~ "order by date*" ORDER BY updated DESC`; got != want {
		t.Errorf("JQL = %q, want %q", got, want)
	}
	// Written JQL keeps its own order, and a quoted one is not one.
	for q, want := range map[string]string{
		`jql:project = X order by created`:       `project = X order by created`,
		`jql:summary ~ "order by"`:               `summary ~ "order by" ORDER BY updated DESC`,
		`jql:summary ~ 'order by' ORDER BY rank`: `summary ~ 'order by' ORDER BY rank`,
		`jql:summary ~ "a \" order by b"`:        `summary ~ "a \" order by b" ORDER BY updated DESC`,
	} {
		if got := New(Config{}).JQL(q); got != want {
			t.Errorf("JQL(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestIssueNumber(t *testing.T) {
	for q, want := range map[string]string{"2624": "2624", " #2624 ": "2624", "": "", "PROJ-1": "", "12a": "", "#": ""} {
		if got := issueNumber(q); got != want {
			t.Errorf("issueNumber(%q) = %q", q, got)
		}
	}
}

func TestTransitionFailures(t *testing.T) {
	ctx := context.Background()
	c := clientFor(answer(t, nil))
	if err := c.Transition(ctx, " ", "Done"); err == nil || !strings.Contains(err.Error(), "needs an issue key") {
		t.Errorf("no key = %v", err)
	}
	if err := c.Transition(ctx, "P-1", " "); err == nil || !strings.Contains(err.Error(), "needs an issue key") {
		t.Errorf("no status = %v", err)
	}
	if err := c.Transition(ctx, "P-1", "Done"); err == nil {
		t.Error("an unreadable status is an error")
	}

	status := reply{body: `{"fields":{"status":{"name":"To Do"}}}`}
	noList := answer(t, map[string]reply{"GET /rest/api/3/issue/P-1": status, "GET /rest/api/3/issue/P-1/transitions": {500, `boom`}})
	if err := clientFor(noList).Transition(ctx, "P-1", "Done"); err == nil {
		t.Error("an unreadable transition list is an error")
	}
	refused := answer(t, map[string]reply{
		"GET /rest/api/3/issue/P-1":              status,
		"GET /rest/api/3/issue/P-1/transitions":  {body: `{"transitions":[{"id":"1","name":"Finish","to":{"name":"Done"}},{"id":"2","name":"Also","to":{"name":"done"}},{"id":"3","name":"Nameless","to":{"name":""}}]}`},
		"POST /rest/api/3/issue/P-1/transitions": {400, `{"errorMessages":["A required field is missing"]}`},
	})
	if err := clientFor(refused).Transition(ctx, "P-1", "finish"); err == nil || !strings.Contains(err.Error(), "A required field") {
		t.Errorf("refused = %v", err)
	}
	var unreachable *trackerport.ErrStatusUnreachable
	if err := clientFor(refused).Transition(ctx, "P-1", "Limbo"); !errors.As(err, &unreachable) || !reflect.DeepEqual(unreachable.Reachable, []string{"Done"}) {
		t.Errorf("reachable statuses are listed once each: %v", err)
	}
}

func TestSupportedStatuses(t *testing.T) {
	c := New(Config{StatusMap: map[trackerport.TicketStatus]string{trackerport.StatusDone: "Shipped", trackerport.StatusTodo: "Open"}})
	var unsupported *trackerport.ErrStatusUnsupported
	err := c.TransitionTo(context.Background(), " P-1 ", trackerport.StatusInReview)
	if !errors.As(err, &unsupported) || unsupported.Key != "P-1" || unsupported.Kind != "Jira" {
		t.Fatalf("err = %v", err)
	}
	if want := []trackerport.TicketStatus{trackerport.StatusTodo, trackerport.StatusDone}; !reflect.DeepEqual(unsupported.Supported, want) {
		t.Errorf("supported = %v", unsupported.Supported)
	}
}

func TestWriteFailuresReachTheCaller(t *testing.T) {
	ctx := context.Background()
	c := clientFor(answer(t, nil))
	if err := c.Comment(ctx, "P-1", "x"); err == nil {
		t.Error("comment: a 404 is an error")
	}
	if _, err := c.Comments(ctx, "P-1"); err == nil {
		t.Error("comments: a 404 is an error")
	}
	if err := c.AddLink(ctx, "P-1", trackerport.TicketLink{URL: "https://x"}); err == nil {
		t.Error("add link: a 404 is an error")
	}
	if err := c.AddLink(ctx, "P-1", trackerport.TicketLink{URL: "  "}); err == nil || !strings.Contains(err.Error(), "needs a URL") {
		t.Errorf("empty link = %v", err)
	}
}

func TestAddLinkTitleFallsBackToTheURL(t *testing.T) {
	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rest/api/3/issue/P-1/remotelink", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if err := clientFor(ts).AddLink(context.Background(), "P-1", trackerport.TicketLink{URL: "https://mr/1"}); err != nil {
		t.Fatal(err)
	}
	if obj := body["object"].(map[string]any); obj["title"] != "https://mr/1" || body["globalId"] != "apothecary:https://mr/1" {
		t.Errorf("body = %v", body)
	}
}

func TestCommentsPageToTheTotal(t *testing.T) {
	var starts []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/issue/P-1/comment", func(w http.ResponseWriter, r *http.Request) {
		start := r.URL.Query().Get("startAt")
		starts = append(starts, start)
		switch start {
		case "0":
			_, _ = w.Write([]byte(`{"total":3,"comments":[{"body":"a"},{"body":"b"}]}`))
		default:
			_, _ = w.Write([]byte(`{"total":3,"comments":[{"body":"c"}]}`))
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	got, err := clientFor(ts).Comments(context.Background(), "P-1")
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b", "c"}) || !reflect.DeepEqual(starts, []string{"0", "2"}) {
		t.Errorf("comments %v, starts %v, %v", got, starts, err)
	}
}

func TestAttachFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		reply reply
		want  string
	}{
		{"refused", reply{413, `{"errorMessages":["too big"]}`}, "too big"},
		{"malformed", reply{200, `oops`}, "decode"},
		{"empty", reply{200, `[]`}, "no content URL"},
		{"no content", reply{200, `[{"filename":"a"}]`}, "no content URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := answer(t, map[string]reply{"POST /rest/api/3/issue/P-1/attachments": tc.reply})
			_, err := clientFor(ts).Attach(ctx, "P-1", "a.txt", "text/plain", []byte("x"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
			noSecret(t, err)
		})
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	dead := clientFor(gone)
	gone.Close()
	if _, err := dead.Attach(ctx, "P-1", "a.txt", "text/plain", nil); err == nil {
		t.Error("a dead server is an error")
	} else {
		noSecret(t, err)
	}
	if _, err := New(Config{BaseURL: "http://bad host"}).Attach(ctx, "P-1", "a.txt", "text/plain", nil); err == nil {
		t.Error("an unparsable base URL is an error")
	}
}

func TestParseStatusMapErrorsAndOverrides(t *testing.T) {
	if _, err := ParseStatusMap("done"); err == nil || !strings.Contains(err.Error(), "is not canonical=status") {
		t.Errorf("no equals = %v", err)
	}
	if _, err := ParseStatusMap("shipped=Done"); err == nil || !strings.Contains(err.Error(), "canonical statuses are") {
		t.Errorf("unknown status = %v", err)
	}
	got, err := ParseStatusMap("done=Shipped\nin_review=\n , blocked=On hold")
	if err != nil {
		t.Fatal(err)
	}
	if got[trackerport.StatusDone] != "Shipped" || got[trackerport.StatusBlocked] != "On hold" {
		t.Errorf("map = %v", got)
	}
	if _, has := got[trackerport.StatusInReview]; has {
		t.Errorf("an empty value removes the default: %v", got)
	}
	if got[trackerport.StatusTodo] != "To Do" {
		t.Errorf("unlisted statuses keep the default: %v", got)
	}
}

func TestResolveFieldAndFieldsFailures(t *testing.T) {
	ctx := context.Background()
	var fieldReads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/field", func(w http.ResponseWriter, _ *http.Request) {
		fieldReads.Add(1)
		_, _ = w.Write([]byte(`[{"id":"cf_1","name":"Team name","schema":{"type":"user"}},{"id":"cf_2","name":"Team size","schema":{"type":"number"}}]`))
	})
	mux.HandleFunc("GET /rest/api/3/issue/P-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c := clientFor(ts)

	var unknown *trackerport.ErrFieldUnknown
	if _, err := c.resolveField(ctx, "P-1", "team"); !errors.As(err, &unknown) || !reflect.DeepEqual(unknown.Candidates, []string{"Team name", "Team size"}) {
		t.Errorf("near names = %v", err)
	}
	if f, err := c.resolveField(ctx, "P-1", " cf_2 "); err != nil || f.Name != "Team size" {
		t.Errorf("by id = %+v, %v", f, err)
	}
	if fieldReads.Load() != 1 {
		t.Errorf("the field list was read %d times", fieldReads.Load())
	}
	var unsupported *trackerport.ErrFieldUnsupported
	if err := c.SetFields(ctx, "P-1", map[string]string{"Team name": "x"}); !errors.As(err, &unsupported) || unsupported.Type != "user" {
		t.Errorf("a user field = %v", err)
	}
	if err := c.SetFields(ctx, "P-1", map[string]string{"Nope": "x"}); !errors.As(err, &unknown) {
		t.Errorf("unknown = %v", err)
	}
	if err := c.SetFields(ctx, "P-1", nil); err != nil {
		t.Errorf("no fields is no call: %v", err)
	}
	if got, err := c.Fields(ctx, "P-1", nil); err != nil || len(got) != 0 {
		t.Errorf("no names = %v, %v", got, err)
	}
	if _, err := c.Fields(ctx, "P-1", []string{"Nope"}); !errors.As(err, &unknown) {
		t.Errorf("unknown read = %v", err)
	}
	if _, err := c.Fields(ctx, "P-1", []string{"Team size"}); err == nil {
		t.Error("an unreadable issue is an error")
	}

	down := clientFor(answer(t, map[string]reply{"GET /rest/api/3/field": {500, `boom`}}))
	if _, err := down.resolveField(ctx, "P-1", "x"); err == nil {
		t.Error("an unreadable field list is an error")
	}
}

func TestCoerce(t *testing.T) {
	c := New(Config{})
	field := func(typ, items, custom string) fieldJSON {
		var f fieldJSON
		f.Name, f.Schema.Type, f.Schema.Items, f.Schema.Custom = "F", typ, items, custom
		return f
	}
	opt := func(k, v string) map[string]any { return map[string]any{k: v} }
	for _, tc := range []struct {
		name  string
		f     fieldJSON
		value string
		want  any
	}{
		{"number", field("number", "", ""), "1.5", 1.5},
		{"empty number clears", field("number", "", ""), "", nil},
		{"list of strings", field("array", "string", ""), "a, b", []string{"a", "b"}},
		{"empty list stays a list", field("array", "string", ""), " , ", []string{}},
		{"components", field("array", "component", ""), "a", []map[string]any{opt("name", "a")}},
		{"versions", field("array", "version", ""), "v1", []map[string]any{opt("name", "v1")}},
		{"options", field("array", "option", ""), "a", []map[string]any{opt("value", "a")}},
		{"option", field("option", "", ""), "x", opt("value", "x")},
		{"empty option clears", field("option", "", ""), "", nil},
		{"priority", field("priority", "", ""), "High", opt("name", "High")},
		{"empty priority clears", field("priority", "", ""), "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.coerce("P-1", tc.f, tc.value)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, %v", got, err)
			}
		})
	}

	var unsupported *trackerport.ErrFieldUnsupported
	if _, err := c.coerce("P-1", field("number", "", ""), "many"); !errors.As(err, &unsupported) || !strings.Contains(err.Error(), `"many" is not a number`) {
		t.Errorf("not a number = %v", err)
	}
	if _, err := c.coerce("P-1", field("array", "user", ""), "a"); !errors.As(err, &unsupported) || unsupported.Type != "array of user" {
		t.Errorf("array of users = %v", err)
	}
}

func TestRenderField(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{``, ""},
		{`null`, ""},
		{`  "text"  `, "text"},
		{`{broken`, ""},
		{`1.25`, "1.25"},
		{`100000000`, "100000000"},
		{`true`, "true"},
		{`["a",null,{"value":"b"},{"name":"c"},{"displayName":"d"},{"key":"e"},{"id":"skipped"}]`, "a, b, c, d, e"},
		{`{"value":"","name":"fallback"}`, "fallback"},
		{`{"other":1}`, ""},
		{`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"rich"}]}]}`, "rich"},
	} {
		if got := renderField(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("renderField(%s) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestToMarkdownDegenerateInput(t *testing.T) {
	for _, raw := range []string{``, `   `, `null`, `123`, `[1,2]`} {
		if got := ToMarkdown(json.RawMessage(raw)); got != "" {
			t.Errorf("ToMarkdown(%q) = %q", raw, got)
		}
	}
}

// doc wraps ADF block nodes in a document and renders it.
func doc(t *testing.T, blocks string) string {
	t.Helper()
	return ToMarkdown(json.RawMessage(`{"type":"doc","version":1,"content":[` + blocks + `]}`))
}

func txt(s string) string {
	enc, _ := json.Marshal(s)
	return `{"type":"text","text":` + string(enc) + `}`
}

func para(inline ...string) string {
	return `{"type":"paragraph","content":[` + strings.Join(inline, ",") + `]}`
}

func TestRenderBlocks(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"heading", `{"type":"heading","attrs":{"level":3},"content":[` + txt("T") + `]}`, "### T"},
		{"heading out of range", `{"type":"heading","attrs":{"level":9},"content":[` + txt("T") + `]}`, "# T"},
		{"heading without a level", `{"type":"heading","content":[` + txt("T") + `]}`, "# T"},
		{"code", `{"type":"codeBlock","attrs":{"language":"go"},"content":[` + txt("a") + `,{"type":"hardBreak"},{"type":"x","content":[` + txt("b") + `]}]}`, "```go\na\nb\n```"},
		{"code without a language", `{"type":"codeBlock","attrs":{"language":7},"content":[` + txt("a") + `]}`, "```\na\n```"},
		{"quote", `{"type":"blockquote","content":[` + para(txt("a")) + `,` + para(txt("b")) + `]}`, "> a\n> \n> b"},
		{"panel", `{"type":"panel","attrs":{"panelType":"warning"},"content":[` + para(txt("careful")) + `]}`, "> WARNING: careful"},
		{"panel without a kind", `{"type":"panel","content":[` + para(txt("plain")) + `]}`, "> plain"},
		{"rule", `{"type":"rule"}`, "---"},
		{"tasks", `{"type":"taskList","content":[{"type":"taskItem","attrs":{"state":"DONE"},"content":[` + txt("a") + `]},{"type":"taskItem","attrs":{"state":"TODO"},"content":[` + txt("b") + `]}]}`, "- [x] a\n- [ ] b"},
		{"expand", `{"type":"expand","attrs":{"title":"More"},"content":[` + para(txt("x")) + `]}`, "**More**\n\nx"},
		{"expand without a title", `{"type":"nestedExpand","content":[` + para(txt("x")) + `]}`, "x"},
		{"media by alt", `{"type":"mediaSingle","content":[{"type":"media","attrs":{"alt":"shot.png","id":"1"}}]}`, "[attachment: shot.png]"},
		{"media by id", `{"type":"mediaGroup","content":[{"type":"media","attrs":{"id":"abc"}}]}`, "[attachment: abc]"},
		{"inline where a block goes", txt("loose"), "loose"},
		{"unknown with children", `{"type":"custom","content":[` + para(txt("kept")) + `]}`, "kept"},
		{"unknown with text", `{"type":"custom","text":"kept"}`, "kept"},
		{"empty nodes leave no gap", para(txt("a")) + `,` + para() + `,` + para(txt("b")), "a\n\nb"},
		{"table of no rows", `{"type":"table","content":[{"type":"other"}]}`, ""},
		{"uneven table", `{"type":"table","content":[
		  {"type":"tableRow","content":[{"type":"tableHeader","content":[` + para(txt("a|b")) + `]},{"type":"tableHeader","content":[` + para(txt("c")) + `]}]},
		  {"type":"tableRow","content":[{"type":"tableCell","content":[` + para(txt("d")) + `]}]}]}`,
			"| a\\|b | c |\n| --- | --- |\n| d |  |"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := doc(t, tc.in); got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestRenderLists(t *testing.T) {
	item := func(blocks ...string) string {
		return `{"type":"listItem","content":[` + strings.Join(blocks, ",") + `]}`
	}
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"numbered from an order", `{"type":"orderedList","attrs":{"order":5},"content":[` + item(para(txt("a"))) + `,` + item(para(txt("b"))) + `]}`, "5. a\n6. b"},
		{"empty item", `{"type":"bulletList","content":[` + item() + `,` + item(para()) + `]}`, "- \n-"},
		{"second paragraph", `{"type":"bulletList","content":[` + item(para(txt("a")), para(txt("b"))) + `]}`, "- a\n  b"},
		{"nested", `{"type":"bulletList","content":[` + item(para(txt("a")), `{"type":"orderedList","content":[`+item(para(txt("b")))+`]}`) + `]}`, "- a\n  1. b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := doc(t, tc.in); got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestRenderInline(t *testing.T) {
	mark := func(text string, marks string) string {
		enc, _ := json.Marshal(text)
		return `{"type":"text","text":` + string(enc) + `,"marks":[` + marks + `]}`
	}
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"break", txt("a") + `,{"type":"hardBreak"},` + txt("b"), "a\nb"},
		{"mention", `{"type":"mention","attrs":{"text":"@Lu","id":"1"}}`, "@Lu"},
		{"mention by id", `{"type":"mention","attrs":{"id":"1"}}`, "@1"},
		{"emoji", `{"type":"emoji","attrs":{"text":"😀","shortName":":x:"}}`, "😀"},
		{"emoji by short name", `{"type":"emoji","attrs":{"shortName":":x:"}}`, ":x:"},
		{"card", `{"type":"inlineCard","attrs":{"url":"https://x"}}`, "https://x"},
		{"status", `{"type":"status","attrs":{"text":"DONE"}}`, "[DONE]"},
		{"date", `{"type":"date","attrs":{"timestamp":"1700000000"}}`, "1700000000"},
		{"unknown with children", `{"type":"x","content":[` + txt("kept") + `]}`, "kept"},
		{"unknown with text", `{"type":"x","text":"kept"}`, "kept"},
		{"code mark", mark("c", `{"type":"code"}`), "`c`"},
		{"strike mark", mark("s", `{"type":"strike"}`), "~~s~~"},
		{"em mark", mark("e", `{"type":"em"}`), "*e*"},
		{"link outside emphasis", mark("l", `{"type":"link","attrs":{"href":"https://x"}},{"type":"strong"}`), "[**l**](https://x)"},
		{"link without an address", mark("l", `{"type":"link","attrs":{}}`), "l"},
		{"unknown mark", mark("l", `{"type":"underline"}`), "l"},
		{"empty text", mark("", `{"type":"strong"}`) + `,` + txt("x"), "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := doc(t, para(tc.in)); got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestPrefixLinesLeavesEmptyAlone(t *testing.T) {
	if got := prefixLines("", "> "); got != "" {
		t.Errorf("empty = %q", got)
	}
	if got := prefixLines("a", ""); got != "a" {
		t.Errorf("no prefix = %q", got)
	}
}

// blocks renders markdown and returns the document's block nodes as JSON, so
// a comparison reads the way the wire does.
func blocks(t *testing.T, md string) string {
	t.Helper()
	data, err := json.Marshal(FromMarkdown(md)["content"])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFromMarkdownBlocks(t *testing.T) {
	for _, tc := range []struct {
		name string
		md   string
		want string
	}{
		{"nothing", "  \n\n", `[]`},
		{"windows line ends", "a\r\nb", `[{"content":[{"text":"a","type":"text"},{"type":"hardBreak"},{"text":"b","type":"text"}],"type":"paragraph"}]`},
		{"code with a language", "```go\nx := 1\n```", `[{"attrs":{"language":"go"},"content":[{"text":"x := 1","type":"text"}],"type":"codeBlock"}]`},
		{"code never closed", "```\nx", `[{"content":[{"text":"x","type":"text"}],"type":"codeBlock"}]`},
		{"rule", "***", `[{"type":"rule"}]`},
		{"ordered, either punctuation", "1. a\n2) b", `[{"content":[{"content":[{"content":[{"text":"a","type":"text"}],"type":"paragraph"}],"type":"listItem"},{"content":[{"content":[{"text":"b","type":"text"}],"type":"paragraph"}],"type":"listItem"}],"type":"orderedList"}]`},
		{"a paragraph stops at a heading", "a\n# b", `[{"content":[{"text":"a","type":"text"}],"type":"paragraph"},{"attrs":{"level":1},"content":[{"text":"b","type":"text"}],"type":"heading"}]`},
		{"quote lines join", "> a\n>b", `[{"content":[{"content":[{"text":"a","type":"text"},{"type":"hardBreak"},{"text":"b","type":"text"}],"type":"paragraph"}],"type":"blockquote"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := blocks(t, tc.md); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestInlineNodes(t *testing.T) {
	plain := func(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
	marked := func(s, mark string) map[string]any {
		return map[string]any{"type": "text", "text": s, "marks": []map[string]any{{"type": mark}}}
	}
	link := func(s, href string) map[string]any {
		return map[string]any{"type": "text", "text": s, "marks": []map[string]any{{"type": "link", "attrs": map[string]any{"href": href}}}}
	}
	for _, tc := range []struct {
		in   string
		want []map[string]any
	}{
		{"", []map[string]any{}},
		{"**b**", []map[string]any{marked("b", "strong")}},
		{"~~s~~", []map[string]any{marked("s", "strike")}},
		{"`c`", []map[string]any{marked("c", "code")}},
		{"*e*", []map[string]any{marked("e", "em")}},
		{"_e_", []map[string]any{marked("e", "em")}},
		{"[t](https://x)", []map[string]any{link("t", "https://x")}},
		{"![t](https://x)", []map[string]any{link("t", "https://x")}},
		// An unmatched opener stays literal instead of eating the line.
		{"**open", []map[string]any{plain("**open")}},
		{"~~open", []map[string]any{plain("~~open")}},
		{"`open", []map[string]any{plain("`open")}},
		{"[open", []map[string]any{plain("[open")}},
		{"[t](open", []map[string]any{plain("[t](open")}},
		{"![t](open", []map[string]any{plain("![t](open")}},
		{"2 * 3 * 4", []map[string]any{plain("2 * 3 * 4")}},
		{"snake_case", []map[string]any{plain("snake_case")}},
		{"**", []map[string]any{plain("**")}},
		{"a!b", []map[string]any{plain("a!b")}},
	} {
		got := inlineNodes(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("inlineNodes(%q) = %v", tc.in, got)
		}
	}
}
