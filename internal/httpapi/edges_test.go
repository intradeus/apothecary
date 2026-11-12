package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth/authtest"
	"apothecary/internal/account/providers"
	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/engine/ask"
	"apothecary/internal/engine/browsermcp"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/browserport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/portrelay"
	"apothecary/internal/workspace/services"
	"apothecary/internal/workspace/wsinfo"
)

// --- the UI bundle ------------------------------------------------------------

func TestCPUIServesFilesAndFallsBackToTheApp(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ui")
	if err := os.MkdirAll(filepath.Join(dir, "_app"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index.html": "<app>", "_app/x.js": "js"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	x := newEnv(t, engine.Config{})
	x.srv.CPUIDir = dir
	x.reserve(t)

	for path, want := range map[string]string{
		"/_app/x.js": "js",    // a file
		"/runs/abc":  "<app>", // a client route
		"/":          "<app>", // the root
		"/_app":      "<app>", // a directory is no file
	} {
		res, body := x.bare(t, "GET", path, "", nil)
		if res.StatusCode != http.StatusOK || body != want {
			t.Errorf("%s = %d %q, want %q", path, res.StatusCode, body, want)
		}
	}
	// The API still answers for itself, and an unknown API path is the UI's to
	// render rather than an open door.
	if res, _ := x.bare(t, "GET", "/api/me", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("api under the UI = %d", res.StatusCode)
	}
	// Nothing outside the bundle, however the path is spelled.
	for _, path := range []string{"/../secret.txt", "/..%2fsecret.txt", "/_app/../../secret.txt", "//../secret.txt"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.URL.Path, _ = url.PathUnescape(path)
		w := httptest.NewRecorder()
		x.srv.serveCPUI(w, req)
		if strings.Contains(w.Body.String(), "outside") {
			t.Errorf("%s escaped the bundle: %d %s", path, w.Code, w.Body)
		}
	}
}

func TestDocsPages(t *testing.T) {
	x := newEnv(t, engine.Config{})
	for path, ct := range map[string]string{
		"/docs":                      "text/html",
		"/docs/":                     "text/html",
		"/docs/swagger-ui.css":       "text/css",
		"/docs/swagger-ui-bundle.js": "application/javascript",
		"/docs/LICENSE":              "text/plain",
	} {
		res, body := x.bare(t, "GET", path, "", nil)
		if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), ct) || body == "" {
			t.Errorf("%s = %d %s", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
	if res, _ := x.bare(t, "GET", "/docs/secrets.env", "", nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("anything else under /docs = %d", res.StatusCode)
	}
	for path, ct := range map[string]string{"/api/openapi.json": "application/json", "/api/openapi.yaml": "application/yaml"} {
		res, body := x.bare(t, "GET", path, "", nil)
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != ct || !strings.Contains(body, "openapi") {
			t.Errorf("%s = %d %s", path, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
}

// --- body limits ---------------------------------------------------------------

// A JSON body has a ceiling; a route never reads past it.
func TestJSONBodiesAreBounded(t *testing.T) {
	x := newEnv(t, engine.Config{})
	huge := `{"name":"` + strings.Repeat("x", 1<<20+1) + `"}`
	res, body := x.bare(t, "POST", "/api/me/tokens", huge, map[string]string{"Authorization": "Bearer " + x.token})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid JSON") {
		t.Errorf("oversized body = %d %s", res.StatusCode, body[:min(len(body), 120)])
	}
}

func TestSmallHelpers(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("ab", 3); got != "ab" {
		t.Errorf("truncate short = %q", got)
	}
	if got := statusText(599); got != "status 599" {
		t.Errorf("statusText = %q", got)
	}
}

// The spec generator refuses two types landing on one name rather than
// quietly merging their schemas.
func TestTypeNamesMustNotCollide(t *testing.T) {
	n := &typeNamer{seen: map[string]reflect.Type{}}
	type Row struct{ A int }
	first := n.name(reflect.TypeOf(&Row{}))
	if first != "Row" {
		t.Errorf("name through a pointer = %q", first)
	}
	if n.name(reflect.TypeOf(Row{})) != "Row" || n.conflicts() != nil {
		t.Errorf("one type twice is no collision: %v", n.conflicts())
	}
	other := func() reflect.Type {
		type Row struct{ B int }
		return reflect.TypeOf(Row{})
	}()
	n.name(other)
	err := n.conflicts()
	if err == nil || !strings.Contains(err.Error(), `"Row"`) {
		t.Errorf("collision = %v", err)
	}
	if got := n.name(reflect.TypeOf([]int{})); got != "[]int" {
		t.Errorf("an unnamed type = %q", got)
	}
}

// --- live frames ---------------------------------------------------------------

// What an agent is writing reaches the stream as delta frames, with no id so a
// reconnect cannot resume from one.
func TestEventStreamCarriesLiveDeltas(t *testing.T) {
	x := newEnv(t, engine.Config{})
	release, connected := make(chan struct{}), make(chan struct{})
	x.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		<-connected
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "t1"})
		select {
		case <-release:
		case <-ctx.Done():
		}
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	id := x.createTicket(t, "T")
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", x.ts.URL+"/api/runs/"+id+"/events", nil)
	res, err := http.DefaultClient.Do(x.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	close(connected)
	lines := readSSE(t, res.Body, func(lines []string) bool { return lines[len(lines)-1] == "event: delta" })
	if len(lines) == 0 || lines[len(lines)-1] != "event: delta" {
		t.Fatalf("no delta frame: %v", lines)
	}
}

// askFlow is a flow with a pane to ask.
const askFlow = `name: asky
version: 5
parameters: { harness: fake, model: fake/model }
ask: { harness: fake }
agents:
  - name: builder
    prompt: build the thing
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`

// The pane end to end: a question is recorded at once, frames reach the
// stream, the answer lands, and the model can be picked.
func TestAskPane(t *testing.T) {
	x := newEnv(t, engine.Config{}, askFlow)
	id := x.createTicketFlow(t, "T", "asky")
	x.e.Wait(id)
	path := "/api/runs/" + id + "/ask"

	res, body := x.do(t, "GET", path, nil, nil)
	st := decode[ask.AskState](t, body)
	if res.StatusCode != http.StatusOK || !st.Enabled {
		t.Fatalf("state = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "PUT", path+"/model", AskModelRequest{Model: "fake/other"}, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("pick model = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "PUT", path+"/model", AskModelRequest{Model: " "}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("no model = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", path, AskRequest{Text: "  "}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("empty question = %d %s", res.StatusCode, body)
	}

	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(sctx, "GET", x.ts.URL+path+"/stream", nil)
	stream, err := http.DefaultClient.Do(x.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()

	res, body = x.do(t, "POST", path, AskRequest{Text: "why?"}, nil)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("ask = %d %s", res.StatusCode, body)
	}
	q := decode[ask.Message](t, body)
	if q.Role != ask.RoleQuestion || q.Text != "why?" || q.UserID != testUser {
		t.Errorf("question = %+v", q)
	}
	lines := readSSE(t, stream.Body, func(lines []string) bool { return lines[len(lines)-1] == "event: state" })
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"text":"why?"`) || !strings.Contains(joined, "id: 1") {
		t.Errorf("stream:\n%s", joined)
	}
	waitFor(t, "the answer", func() bool {
		_, b := x.do(t, "GET", path, nil, nil)
		return !decode[ask.AskState](t, b).Busy
	})
}

// streamAsk over a store that cannot be read says so instead of opening.
func TestAskStreamStoreFailure(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	x.srv.Ask = ask.New(&run.Reader{Store: x.store}, x.srv.Workspaces.Resolver, x.e, askStoreFails{ask.NewMemStore()})
	res, body := x.do(t, "GET", "/api/runs/r1/ask/stream", nil, nil)
	if res.StatusCode == http.StatusOK || !strings.Contains(string(body), "messages down") {
		t.Errorf("stream = %d %s", res.StatusCode, body)
	}
}

type askStoreFails struct{ *ask.MemStore }

func (askStoreFails) AskMessages(context.Context, string, int64) ([]ask.Message, error) {
	return nil, errors.New("messages down")
}

// noFlush hides http.Flusher: a transport that cannot stream is told so.
type noFlush struct{ http.ResponseWriter }

func TestStreamsNeedAFlusher(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	for name, h := range map[string]http.HandlerFunc{"events": x.srv.streamEvents, "ask": x.srv.streamAsk} {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetPathValue("id", "r1")
		rec := httptest.NewRecorder()
		h(noFlush{rec}, req)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "streaming") {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
}

// --- app hosts --------------------------------------------------------------------

// appWorld is a run's port exposed at an app host, and a client that resolves
// every host to the test server.
type appWorld struct {
	*env
	runID, appURL string
	relay         *portrelay.Manager
	ts            *httptest.Server
	client        *http.Client
}

func newAppWorld(t *testing.T, publicURL string, conn envport.PortConn) *appWorld {
	t.Helper()
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	relay := portrelay.New("")
	relay.RouteByHost("apps.test", "http")
	appURL, err := relay.Expose("r1", 3000, conn)
	if err != nil {
		t.Fatal(err)
	}
	x.srv.Apps = NewApps(relay, publicURL)
	ts := httptest.NewServer(x.srv.Handler())
	t.Cleanup(ts.Close)
	return &appWorld{env: x, runID: "r1", appURL: appURL, relay: relay, ts: ts, client: &http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", strings.TrimPrefix(ts.URL, "http://"))
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (a *appWorld) get(t *testing.T, u string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := a.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res, readBody(res)
}

func authURL(to string) string { return "http://ui.test/api/apps/auth?to=" + url.QueryEscape(to) }

func TestAppAuthorizeRefusals(t *testing.T) {
	a := newAppWorld(t, "http://ui.test", tcpConn{"127.0.0.1:1"})
	cookie := a.session(t, testUser)

	for name, to := range map[string]string{
		"not an address":    "%zz",
		"no host":           "/just/a/path",
		"another domain":    "http://evil.example/",
		"the domain itself": "http://apps.test/",
	} {
		res, body := a.get(t, authURL(to), cookie)
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "not an app address") {
			t.Errorf("%s = %d %s", name, res.StatusCode, body)
		}
	}
	nothing := strings.Replace(a.appURL, "3000-", "3001-", 1)
	if res, body := a.get(t, authURL(nothing), cookie); res.StatusCode != http.StatusNotFound || !strings.Contains(body, "nothing is exposed") {
		t.Errorf("nothing exposed = %d %s", res.StatusCode, body)
	}

	// Someone else's run: they are signed in, and it is not theirs.
	a.addUser(t, "bob", false)
	if res, body := a.get(t, authURL(a.appURL), a.session(t, "bob")); res.StatusCode != http.StatusNotFound || !strings.Contains(body, "not your run") {
		t.Errorf("someone else's run = %d %s", res.StatusCode, body)
	}
	// A dead session gets the sign-in page, the address escaped.
	res, body := a.get(t, authURL(a.appURL+"?x=<script>"), map[string]string{"Cookie": sessionCookie + "=dead"})
	if res.StatusCode != http.StatusUnauthorized || strings.Contains(body, "<script>") {
		t.Errorf("signed out = %d %s", res.StatusCode, body)
	}
	// A server without accounts lets nobody through.
	a.srv.Auth = nil
	if res, _ := a.get(t, authURL(a.appURL), cookie); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no accounts = %d", res.StatusCode)
	}
}

// The sign-in page echoes the address into a link: an app address with a
// script scheme must not become a live javascript: link on the UI's origin.
func TestAppAuthorizeRefusesNonWebSchemes(t *testing.T) {
	a := newAppWorld(t, "http://ui.test", tcpConn{"127.0.0.1:1"})
	host := strings.TrimPrefix(a.appURL, "http://")
	res, body := a.get(t, authURL("javascript://"+strings.TrimSuffix(host, "/")+"/%0aalert(document.domain)"), nil)
	if res.StatusCode != http.StatusBadRequest || strings.Contains(body, "javascript:") {
		t.Errorf("script scheme = %d %s", res.StatusCode, body)
	}
}

func TestAppHostEdges(t *testing.T) {
	a := newAppWorld(t, "", tcpConn{"127.0.0.1:1"})
	// No UI address to send a browser to: said, rather than a broken redirect.
	if res, body := a.get(t, a.appURL, nil); res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "APOTHECARY_PUBLIC_URL") {
		t.Errorf("no public url = %d %s", res.StatusCode, body)
	}
	// Nothing exposed at that app host.
	if res, _ := a.get(t, strings.Replace(a.appURL, "3000-", "3001-", 1), nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("nothing exposed = %d", res.StatusCode)
	}
	// Behind TLS the sign-in hand-off keeps the scheme.
	a.srv.Apps.PublicURL = "https://ui.test"
	res, _ := a.get(t, strings.TrimSuffix(a.appURL, "/")+"/x?y=1", map[string]string{"X-Forwarded-Proto": "https"})
	if res.StatusCode != http.StatusFound || !strings.Contains(res.Header.Get("Location"), url.QueryEscape("https://")) {
		t.Errorf("behind TLS = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

// grant walks the sign-in hand-off and returns the app host's cookie header.
func (a *appWorld) grant(t *testing.T, to string) (map[string]string, *http.Response) {
	t.Helper()
	res, body := a.get(t, authURL(to), a.session(t, testUser))
	if res.StatusCode != http.StatusFound {
		t.Fatalf("authorize = %d %s", res.StatusCode, body)
	}
	res, _ = a.get(t, res.Header.Get("Location"), nil)
	if res.StatusCode != http.StatusFound || len(res.Cookies()) != 1 {
		t.Fatalf("finish = %d %v", res.StatusCode, res.Header)
	}
	return map[string]string{"Cookie": appCookie + "=" + res.Cookies()[0].Value + "; theirs=kept"}, res
}

func TestAppCodesAreOneUseAndHostBound(t *testing.T) {
	seen := make(chan string, 1)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Cookie")
	}))
	defer app.Close()
	a := newAppWorld(t, "http://ui.test", tcpConn{strings.TrimPrefix(app.URL, "http://")})

	deep := strings.TrimSuffix(a.appURL, "/") + "/deep?x=1"
	res, _ := a.get(t, authURL(deep), a.session(t, testUser))
	loc := res.Header.Get("Location")

	// A code for this host is no good on another of the run's.
	other, err := a.relay.Expose("r1", 3001, tcpConn{strings.TrimPrefix(app.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(loc)
	o, _ := url.Parse(other)
	wrong := *u
	wrong.Host = o.Host
	if res, _ := a.get(t, wrong.String(), nil); res.StatusCode != http.StatusForbidden {
		t.Errorf("code on another host = %d", res.StatusCode)
	}
	// That spent it: a code is one attempt.
	if res, _ := a.get(t, loc, nil); res.StatusCode != http.StatusForbidden {
		t.Errorf("a code after a failed use = %d", res.StatusCode)
	}

	// A good one lands on the page asked for, and the workspace sees the app's
	// own cookies, never apothecary's.
	cookie, res := a.grant(t, deep)
	if got := res.Header.Get("Location"); got != "/deep?x=1" {
		t.Errorf("landing = %q", got)
	}
	if res, body := a.get(t, a.appURL, cookie); res.StatusCode != http.StatusOK {
		t.Fatalf("app = %d %s", res.StatusCode, body)
	}
	if got := <-seen; got != "theirs=kept" {
		t.Errorf("cookies at the app = %q", got)
	}
	// The cookie is this host's alone.
	if res, _ := a.get(t, other, cookie); res.StatusCode != http.StatusFound {
		t.Errorf("cookie on another app host = %d, want a sign-in redirect", res.StatusCode)
	}
}

// A `to` that points off the app host goes home instead.
func TestAppLandingStaysOnTheHost(t *testing.T) {
	a := newAppWorld(t, "http://ui.test", tcpConn{"127.0.0.1:1"})
	for _, to := range []string{"https://evil.example/", "//evil.example/", ""} {
		res, _ := a.get(t, authURL(a.appURL), a.session(t, testUser))
		u, _ := url.Parse(res.Header.Get("Location"))
		q := u.Query()
		q.Set("to", to)
		u.RawQuery = q.Encode()
		res, _ = a.get(t, u.String(), nil)
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/" {
			t.Errorf("to=%q landed on %q (%d)", to, res.Header.Get("Location"), res.StatusCode)
		}
	}
}

func TestAppWorkspaceNotAnswering(t *testing.T) {
	a := newAppWorld(t, "http://ui.test", tcpConn{"127.0.0.1:1"})
	cookie, _ := a.grant(t, a.appURL)
	if res, body := a.get(t, a.appURL, cookie); res.StatusCode != http.StatusBadGateway || !strings.Contains(body, "did not answer") {
		t.Errorf("dead workspace = %d %s", res.StatusCode, body)
	}
}

func TestExpiredGrantsGoAsNewOnesArrive(t *testing.T) {
	a := NewApps(portrelay.New(""), "http://ui.test/")
	if a.PublicURL != "http://ui.test" {
		t.Errorf("public url = %q", a.PublicURL)
	}
	old := a.issue(a.codes, appGrant{run: "r", until: time.Now().Add(-time.Minute)})
	live := a.issue(a.codes, appGrant{run: "r", until: time.Now().Add(time.Minute)})
	if _, ok := a.codes[old]; ok {
		t.Error("an expired code stayed")
	}
	if _, ok := a.codes[live]; !ok {
		t.Error("a live code went")
	}
	// An expired code that survived is refused all the same.
	a.codes["stale"] = appGrant{run: "r", host: "h", until: time.Now().Add(-time.Minute)}
	w := httptest.NewRecorder()
	a.finish(w, httptest.NewRequest("GET", "http://h/?code=stale", nil), "r")
	if w.Code != http.StatusForbidden {
		t.Errorf("stale code = %d", w.Code)
	}
}

// --- ports -----------------------------------------------------------------------

// exposer can publish a port and start an editor.
type exposer struct {
	*fake.EnvProvisioner
	exposeErr error
}

func (e exposer) ExposePort(context.Context, envport.EnvID, int) (envport.PortConn, error) {
	return tcpConn{"127.0.0.1:1"}, e.exposeErr
}
func (exposer) AppPorts(context.Context, envport.EnvID) []int { return nil }

func TestExposeThroughARelay(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	relay := portrelay.New("127.0.0.1")
	t.Cleanup(relay.CloseAll)
	x.srv.Ports.Relay = relay
	x.res.Envp = exposer{EnvProvisioner: x.envp}

	res, body := x.do(t, "POST", "/api/runs/r1/expose-port", ExposePortRequest{Port: 3000}, nil)
	got := decode[ExposedPort](t, body)
	if res.StatusCode != http.StatusOK || got.Port != 3000 || got.URL == "" {
		t.Fatalf("expose = %d %s", res.StatusCode, body)
	}
	x.srv.Ports.ReportObservedPorts("r1", []envport.ObservedPort{{Port: 3000}, {Port: 4000}})
	_, body = x.do(t, "GET", "/api/runs/r1/ports", nil, nil)
	ports := decode[RunPorts](t, body)
	if len(ports.Ports) != 1 || len(ports.Observed) != 1 || ports.Observed[0].Port != 4000 {
		t.Errorf("an exposed port stayed in the discovered list: %s", body)
	}
	if !ports.Links {
		t.Error("a relay hands out browser links")
	}

	x.res.Envp = exposer{EnvProvisioner: x.envp, exposeErr: errors.New("no such listener")}
	if res, body := x.do(t, "POST", "/api/runs/r1/expose-port", ExposePortRequest{Port: 3001}, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("failed expose = %d %s", res.StatusCode, body)
	}
}

// --- the browser -----------------------------------------------------------------

type failingBrowser struct {
	echoBrowser
	status error
}

func (b *failingBrowser) Status(context.Context, string) (browserport.BrowserStatus, error) {
	return browserport.BrowserStatus{}, b.status
}

func TestBrowserRefusals(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id, tok := x.holdRun(t)
	path := "/api/runs/" + id + "/browser-mcp"

	// A plain request on the upgrade route.
	if res, body := x.do(t, "GET", path, nil, tok); res.StatusCode != http.StatusUpgradeRequired || !strings.Contains(string(body), browserMCPProtocol) {
		t.Errorf("no upgrade = %d %s", res.StatusCode, body)
	}
	// This apothecary runs no browsers.
	if _, _, res := upgrade(t, x, id, x.e.RunToken(id)); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("no browser = %d", res.StatusCode)
	}

	// The project's status fails: still a page to render.
	x.srv.Browser = browsermcp.New(&run.Reader{Store: x.store}, x.srv.Workspaces.Resolver, &failingBrowser{status: errors.New("docker down")})
	res2, body := x.do(t, "GET", "/api/projects/"+testProject+"/browser", nil, nil)
	info := decode[BrowserInfo](t, body)
	if res2.StatusCode != http.StatusOK || !info.Available || info.Error != "docker down" {
		t.Errorf("status failure = %d %s", res2.StatusCode, body)
	}
}

// --- ssh ---------------------------------------------------------------------------

func TestRunSSHFailures(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.srv.SSH = &stubDoor{}
	if _, err := x.store.Append(context.Background(), "r1", &event.RunCreated{ProjectID: testProject, UserID: testUser, FlowName: "loop"}); err != nil {
		t.Fatal(err)
	}
	if res, body := x.do(t, "GET", "/api/runs/r1/ssh", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("no workspace = %d %s", res.StatusCode, body)
	}
	x.seedLiveRun(t, "r2")
	x.srv.Auth = authtest.New(failAfterGuard{DB: x.users, err: errors.New("db down")})
	if res, body := x.do(t, "GET", "/api/runs/r2/ssh", nil, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("keys outage = %d %s", res.StatusCode, body)
	}
}

// --- services ------------------------------------------------------------------------

func TestServiceErrMapping(t *testing.T) {
	for err, want := range map[error]int{
		wsinfo.ErrNoWorkspace:       http.StatusConflict,
		resolution.ErrUnresolvable:  http.StatusConflict,
		errors.New("run not found"): http.StatusNotFound,
		errors.New("exec failed"):   http.StatusBadGateway,
	} {
		w := httptest.NewRecorder()
		serviceErr(w, err)
		if w.Code != want {
			t.Errorf("%v = %d, want %d", err, w.Code, want)
		}
	}
}

func TestRaiseOnAParkedRun(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id, tok := x.holdRun(t)
	// The token lives as long as the loop does: pause, and it is gone.
	if res, body := x.do(t, "POST", "/api/runs/"+id+"/pause", nil, nil); res.StatusCode != http.StatusAccepted {
		t.Fatalf("pause = %d %s", res.StatusCode, body)
	}
	x.e.Wait(id)
	if res, _ := x.do(t, "POST", "/api/runs/"+id+"/pings", RaisePingRequest{Type: "clarification", Question: "q"}, tok); res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusConflict {
		t.Errorf("raise on a parked run = %d", res.StatusCode)
	}
}

// --- merge requests ----------------------------------------------------------------

func TestMergeRequestStartRefusals(t *testing.T) {
	x := newConfEnv(t)
	pid := x.wizard(t)
	repoHost := kinds.FakeRepoHostOf("1")
	repoHost.Reset()
	t.Cleanup(repoHost.Reset)
	mr := repoHost.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	if res, data := x.do(t, "POST", "/api/projects/"+pid+"/flows", map[string]any{"name": "on-mr", "yaml": mrFlowYAML}, nil); res.StatusCode != http.StatusCreated {
		t.Fatalf("flow = %d %s", res.StatusCode, data)
	}
	start := "/api/projects/" + pid + "/mrs/" + itoa(int64(mr.IID)) + "/runs"

	// A flow that does not start on merge requests is refused, and the ticket
	// made for the attempt goes with the refusal.
	before := len(listTickets(t, x, pid))
	res, data := x.do(t, "POST", start, MRRunRequest{Flow: "v1"}, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong flow = %d %s", res.StatusCode, data)
	}
	if after := len(listTickets(t, x, pid)); after != before {
		t.Errorf("a refused run left a ticket behind: %d -> %d", before, after)
	}
	// The project's default flow when the body names none: here one that does
	// not take merge requests either.
	if res, data := x.do(t, "POST", start, nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("default flow = %d %s", res.StatusCode, data)
	}

	// A closed merge request is no place to start.
	if err := repoHost.MergeMR(context.Background(), mr, repoport.MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	if res, data := x.do(t, "POST", start, MRRunRequest{Flow: "on-mr"}, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "open merge request") {
		t.Errorf("closed = %d %s", res.StatusCode, data)
	}
}

func listTickets(t *testing.T, x *confEnv, project string) []confstore.Ticket {
	t.Helper()
	_, data := x.do(t, "GET", "/api/tickets?project="+project, nil, nil)
	return decode[[]confstore.Ticket](t, data)
}

// A project with no repository host to ask has nothing to list.
func TestMergeRequestListFailure(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.res.RepoHost = nil
	res, body := x.do(t, "GET", "/api/projects/"+testProject+"/mrs", nil, nil)
	if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusOK {
		t.Errorf("mrs = %d %s", res.StatusCode, body)
	}
}

// Credentials: a login's test passes without spending anything, and says how
// long it lasts.
func TestCredentialTestOfAFreshLogin(t *testing.T) {
	x := newConfEnv(t)
	ctx := context.Background()
	soon := time.Now().Add(6 * time.Hour)
	pc, err := x.conf.CreateCredential(ctx, testUser, providers.Credential{
		Provider: "anthropic", Kind: providers.KindOAuth, Access: "a", Refresh: "r", Expires: soon,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	res, data := x.do(t, "POST", "/api/credentials/"+pc.ID+"/test", nil, nil)
	got := decode[TestResult](t, data)
	if res.StatusCode != http.StatusOK || !got.OK || len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "valid until") {
		t.Errorf("test = %d %s", res.StatusCode, data)
	}
}

// An engine bound to every interface cannot name the host people reach it
// at, so its relay URLs say "localhost" — the API hands each browser the
// host it opened the UI at instead: the same machine, so the same ports.
func TestRelayURLsNameTheHostTheUIWasReachedAt(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	relay := portrelay.New("0.0.0.0")
	t.Cleanup(relay.CloseAll)
	x.srv.Ports.Relay = relay
	x.res.Envp = exposer{EnvProvisioner: x.envp}
	// The browser reached the UI at 100.1.2.3:8080 (the client dials the
	// test server; the Host it sends is what the engine reads).
	at := func(method, path string, in any) []byte {
		t.Helper()
		var rd io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, x.ts.URL+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "100.1.2.3:8080"
		res, err := http.DefaultClient.Do(x.authed(req))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return out
	}

	body := at("POST", "/api/runs/r1/expose-port", ExposePortRequest{Port: 3000})
	got := decode[ExposedPort](t, body)
	if !strings.HasPrefix(got.URL, "http://100.1.2.3:") || strings.HasPrefix(got.URL, "http://100.1.2.3:8080/") {
		t.Fatalf("exposed url = %q, want the UI's host on the relay's own port", got.URL)
	}
	body = at("GET", "/api/runs/r1/ports", nil)
	if ports := decode[RunPorts](t, body); len(ports.Ports) != 1 || ports.Ports[0].URL != got.URL {
		t.Errorf("ports = %s, want %s", body, got.URL)
	}
	// From the machine itself, localhost stays localhost.
	local := httptest.NewRequest("GET", "/", nil)
	local.Host = "localhost:8080"
	if got := x.srv.reach(local, relay.List("r1")[0].URL); !strings.HasPrefix(got, "http://localhost:") {
		t.Errorf("local url = %q", got)
	}
	// A preview link of a service goes the same way.
	info := services.ServiceInfo{Ports: []services.ServicePortInfo{{Port: 3000, URL: relay.List("r1")[0].URL + "health"}, {Port: 9}}}
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "box.tail1234.ts.net"
	x.srv.reachService(req, &info)
	if !strings.HasPrefix(info.Ports[0].URL, "http://box.tail1234.ts.net:") || !strings.HasSuffix(info.Ports[0].URL, "/health") || info.Ports[1].URL != "" {
		t.Errorf("service links = %+v", info.Ports)
	}
	// No relay: nothing to rewrite.
	x.srv.Ports.Relay = nil
	if got := x.srv.reach(req, "http://localhost:1/"); got != "http://localhost:1/" {
		t.Errorf("reach without a relay = %q", got)
	}
}

// A door on every interface with nothing configured to advertise is named
// by the host the page was reached at.
func TestRunSSHNamesTheRequestHostWhenTheDoorHasNone(t *testing.T) {
	for host, want := range map[string]string{"100.1.2.3:8080": "100.1.2.3", "[fd7a::1]:8080": "fd7a::1", "box.ts.net": "box.ts.net"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		if got := requestHostname(req); got != want {
			t.Errorf("requestHostname(%q) = %q, want %q", host, got, want)
		}
	}
}
