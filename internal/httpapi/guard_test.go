package httpapi

// The guard, walked over the whole route table: whatever a route is, a
// signed-out caller, someone else and a non-admin are refused the way it
// declares, and a browser on another site gets nowhere with a cookie. The
// table is the input, so an endpoint added tomorrow is covered the day it is
// added.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/auth/authtest"
	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/project/kinds"
	"apothecary/internal/run/event"
	"apothecary/internal/skills/skillstore"
)

// world is one owner's resources of every scope on a server, plus a second
// account to try them as.
type world struct {
	*env
	files *testFiles

	run, ticket, ping, integration, credential string
	skill, skillset, channel                   string
	attachment, snapshot, envcheck, token      string

	// otherID/other are a second, non-admin account and its bearer header.
	otherID string
	other   map[string]string
}

// addUser makes an account with a personal token and returns its bearer
// header.
func (x *env) addUser(t *testing.T, id string, admin bool) map[string]string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := x.users.CreateUser(ctx, auth.User{
		ID: id, Handle: id, Email: id + "@example.test", Name: id, Admin: admin, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	secret, _, err := x.srv.Auth.NewToken(ctx, id, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + secret}
}

// reserve rebuilds the test server's mux, for fields that gate registration.
func (x *env) reserve(t *testing.T) {
	t.Helper()
	x.ts.Close()
	x.ts = httptest.NewServer(x.srv.Handler())
	t.Cleanup(x.ts.Close)
}

// raisePing appends a pending question to a run's log, the record the inbox
// and the ping routes read.
func (x *env) raisePing(t *testing.T, runID, pingID string) {
	t.Helper()
	if _, err := x.store.Append(context.Background(), runID, &event.PingRaised{
		PingID: pingID, Type: "clarification", Question: "which db?", StageID: "build",
	}); err != nil {
		t.Fatal(err)
	}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	files := newTestFiles(t, 1<<20, []string{"text/*"})
	skl := newTestSkills(t, nil)
	x := newEnvWith(t, engine.Config{}, func(d *engine.Deps) { files.deps(d); skl.deps(d) })
	files.serve(x)
	reg := kinds.NewRegistry()
	kinds.RegisterFakes(reg)
	x.srv.Kinds = reg
	skl.serve(t, x)
	ctx := context.Background()
	w := &world{env: x, files: files}

	w.run = x.createTicket(t, "owned")
	x.e.Wait(w.run)
	w.ticket = x.newTicket(t, "a ticket", "")
	w.ping = "ping-owned"
	x.raisePing(t, w.run, w.ping)

	x.seedIntegration(t, "int-owned", "ticketsource")
	w.integration = "int-owned"

	res, body := x.do(t, "POST", "/api/credentials", map[string]any{"provider": "openrouter", "key": "sk-or-x"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("credential = %d %s", res.StatusCode, body)
	}
	w.credential = decode[struct {
		ID string `json:"id"`
	}](t, body).ID

	_, body = x.do(t, "POST", "/api/skills", skillBody("pdf", "Reads PDFs."), nil)
	w.skill = decode[skillstore.Skill](t, body).ID
	_, body = x.do(t, "POST", "/api/skillsets", SkillsetRequest{Name: "Docs", SkillIDs: []string{w.skill}}, nil)
	w.skillset = decode[skillstore.Skillset](t, body).ID

	ch, err := x.reg.Add(testUser, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	w.channel = ch.ID

	a, err := files.att.Save(ctx, testUser, "note.txt", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	w.attachment = a.ID
	sn, err := files.snaps.Capture(ctx, filestore.Snapshot{RunID: w.run, StageID: "build", Kind: filestore.KindLog, Filename: "build.log"},
		func(s string) string { return s }, []byte("log line\n"))
	if err != nil {
		t.Fatal(err)
	}
	w.snapshot = sn.ID

	if w.envcheck, err = x.srv.EnvCheck.Start(ctx, testProject, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the envcheck to finish", func() bool {
		cr, ok := x.srv.EnvCheck.Get(w.envcheck)
		return ok && cr.Status != "running"
	})

	secret, tok, err := x.srv.Auth.NewToken(ctx, testUser, "another", nil)
	if err != nil || secret == "" {
		t.Fatal(err)
	}
	w.token = tok.ID

	w.otherID = "usr-other"
	w.other = x.addUser(t, w.otherID, false)
	return w
}

// waitFor polls cond until it holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var pathParam = regexp.MustCompile(`\{([a-z]+)(\.\.\.)?\}`)

// owned is this world's resource of a scope.
func (w *world) owned(sc Scope) string {
	return map[Scope]string{
		ScopeRun: w.run, ScopeProject: testProject, ScopeTicket: w.ticket, ScopePing: w.ping,
		ScopeIntegration: w.integration, ScopeChannel: w.channel, ScopeAttachment: w.attachment,
		ScopeSnapshot: w.snapshot, ScopeEnvCheck: w.envcheck, ScopeCredential: w.credential,
		ScopeSkill: w.skill, ScopeSkillset: w.skillset,
	}[sc]
}

// address fills a route's pattern with this world's ids, by the resource the
// route declares.
func (w *world) address(rt Route) string {
	id := w.owned(rt.Scope)
	if id == "" {
		id = w.run // the run-token routes
	}
	if rt.Path == "/api/me/tokens/{id}" {
		id = w.token
	}
	return pathParam.ReplaceAllStringFunc(rt.Path, func(m string) string {
		switch name := pathParam.FindStringSubmatch(m)[1]; name {
		case "id":
			return id
		case "pid":
			return w.ping
		case "iid", "seq":
			return "1"
		case "rest":
			return "snapshot"
		default:
			return "x"
		}
	})
}

// blockUntil is an agent turn that lasts until release closes (or the run is
// stopped), so a test holds a live run.
func blockUntil(release <-chan struct{}) func(context.Context, fake.RunCall) (agentport.AgentResult, error) {
	return func(ctx context.Context, _ fake.RunCall) (agentport.AgentResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
}

func (w *world) enabled(t *testing.T) []Route {
	t.Helper()
	var out []Route
	for _, rt := range w.srv.routes() {
		if rt.Enabled == nil || rt.Enabled(w.srv) {
			out = append(out, rt)
		}
	}
	if len(out) < 100 {
		t.Fatalf("only %d routes enabled: the walk would prove little", len(out))
	}
	return out
}

// bare sends a request with exactly the given headers (x.do stamps the owner's
// token on anything without an Authorization).
func (x *env) bare(t *testing.T, method, path, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, x.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res, readBody(res)
}

// Nobody reaches anything without saying who they are: the walk covers every
// route the table enables, and the two streams that live outside it.
func TestEveryRouteRefusesTheSignedOut(t *testing.T) {
	w := newWorld(t)
	paths := map[string]Access{}
	for _, rt := range w.enabled(t) {
		if rt.Auth == AccessPublic {
			continue
		}
		paths[rt.Method+" "+w.address(rt)] = rt.Auth
	}
	paths["GET /api/runs/"+w.run+"/terminal"] = AccessUser
	paths["GET /api/runs/"+w.run+"/browser-mcp"] = AccessRunToken

	for where := range paths {
		method, path, _ := strings.Cut(where, " ")
		for _, hdr := range []map[string]string{
			nil,
			{"Authorization": "Bearer nonsense"},
			{"Authorization": "Bearer " + auth.TokenPrefix + "unknown"},
			{"Cookie": sessionCookie + "=unknown"},
		} {
			res, body := w.bare(t, method, path, "{}", hdr)
			if res.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s with %v = %d %s, want 401", where, hdr, res.StatusCode, body)
			}
		}
	}
}

// An admin route is an admin's: a signed-in user is told so, and the check
// comes before anything happens.
func TestAdminRoutesRefuseEveryoneElse(t *testing.T) {
	w := newWorld(t)
	n := 0
	for _, rt := range w.enabled(t) {
		if rt.Auth != AccessAdmin {
			continue
		}
		n++
		path := pathParam.ReplaceAllString(rt.Path, testUser)
		res, body := w.do(t, rt.Method, path, map[string]any{"password": "a new password", "handle": "hacked", "admin": true, "disabled": true}, w.other)
		if res.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "instance admin") {
			t.Errorf("%s %s as a user = %d %s, want 403", rt.Method, path, res.StatusCode, body)
		}
	}
	if n < 5 {
		t.Fatalf("walked %d admin routes", n)
	}
	// Nothing changed: the owner is still who they were.
	res, body := w.do(t, "GET", "/api/me", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"handle":"tester"`) || !strings.Contains(string(body), `"admin":true`) {
		t.Errorf("me after the attempts = %d %s", res.StatusCode, body)
	}
}

// Not theirs reads exactly like not there, on every route that names a
// resource, and the walk leaves the owner's resources standing.
func TestEveryScopedRouteHidesOthersResources(t *testing.T) {
	w := newWorld(t)
	n := 0
	for _, rt := range w.enabled(t) {
		if rt.Scope == ScopeNone {
			continue
		}
		n++
		path, id := w.address(rt), w.owned(rt.Scope)
		res, body := w.do(t, rt.Method, path, map[string]any{}, w.other)
		if res.StatusCode != http.StatusNotFound || !strings.Contains(string(body), scopeName(rt.Scope)+" "+id+" not found") {
			t.Errorf("%s %s as other = %d %s, want the guard's 404", rt.Method, path, res.StatusCode, body)
		}
		// The same refusal as an id that does not exist: nothing tells the two
		// apart but the id itself.
		res2, body2 := w.do(t, rt.Method, strings.Replace(path, id, "missing", 1), map[string]any{}, w.other)
		if res2.StatusCode != http.StatusNotFound {
			t.Errorf("%s of a missing %s = %d %s", rt.Method, scopeName(rt.Scope), res2.StatusCode, body2)
		}
		if a, b := strings.ReplaceAll(string(body), id, "ID"), strings.ReplaceAll(string(body2), "missing", "ID"); a != b {
			t.Errorf("%s: a hidden resource reads differently from a missing one: %q vs %q", rt.Path, a, b)
		}
	}
	if n < 80 {
		t.Fatalf("walked only %d scoped routes", n)
	}
	for _, path := range []string{
		"/api/runs/" + w.run, "/api/tickets/" + w.ticket, "/api/projects/" + testProject,
		"/api/integrations/" + w.integration, "/api/credentials/" + w.credential,
		"/api/skills/" + w.skill, "/api/skillsets/" + w.skillset,
		"/api/attachments/" + w.attachment, "/api/snapshots/" + w.snapshot, "/api/envchecks/" + w.envcheck,
	} {
		if res, body := w.do(t, "GET", path, nil, nil); res.StatusCode != http.StatusOK {
			t.Errorf("owner's %s after the walk = %d %s", path, res.StatusCode, body)
		}
	}
	if _, ok := w.reg.Get(w.channel); !ok {
		t.Error("someone else deleted the owner's channel")
	}
}

// An admin manages accounts. It grants no view into anyone's work.
func TestAdminSeesNoOneElsesWork(t *testing.T) {
	w := newWorld(t)
	root := w.addUser(t, "root", true)
	n := 0
	for _, rt := range w.enabled(t) {
		if rt.Scope == ScopeNone || rt.Method != "GET" {
			continue
		}
		n++
		if res, body := w.do(t, "GET", w.address(rt), nil, root); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s as an admin = %d %s, want 404", rt.Path, res.StatusCode, body)
		}
	}
	if n < 30 {
		t.Fatalf("walked %d routes", n)
	}
	if res, body := w.do(t, "GET", "/api/runs", nil, root); res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("an admin's run list = %d %s", res.StatusCode, body)
	}
}

// The terminal's stream sits outside the table with the same rule.
func TestTerminalIsTheRunOwners(t *testing.T) {
	w := newWorld(t)
	path := "/api/runs/" + w.run + "/terminal"
	if res, body := w.do(t, "GET", path, nil, w.other); res.StatusCode != http.StatusNotFound {
		t.Errorf("terminal as other = %d %s", res.StatusCode, body)
	}
	// The owner, from another site: a browser always says where it came from.
	if res, body := w.do(t, "GET", path, nil, map[string]string{"Origin": "https://evil.example"}); res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site upgrade = %d %s", res.StatusCode, body)
	}
	if res, body := w.do(t, "GET", path, nil, map[string]string{"Origin": "://bad"}); res.StatusCode != http.StatusForbidden {
		t.Errorf("unparseable origin = %d %s", res.StatusCode, body)
	}
	// From the UI's own host it gets past the guards to the workspace, which
	// the fake provisioner cannot host a sidecar for.
	res, body := w.do(t, "GET", path, nil, map[string]string{"Origin": w.ts.URL})
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("same-origin upgrade = %d %s, want the no-sidecar 503", res.StatusCode, body)
	}
}

// A bearer of one kind does not open a door of another: the shim's run token
// is not a user, a person's token is not the shim, and one run's token is
// worthless for the next run.
func TestTokensOpenOnlyTheirOwnDoors(t *testing.T) {
	x := newEnv(t, engine.Config{})
	block := make(chan struct{})
	defer close(block)
	x.runner.RunFunc = blockUntil(block)
	first, second := x.createTicket(t, "one"), x.createTicket(t, "two")
	var a, b string
	waitFor(t, "the run tokens", func() bool {
		a, b = x.e.RunToken(first), x.e.RunToken(second)
		return a != "" && b != ""
	})
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

	// The run token on a user route: not a user.
	if res, body := x.do(t, "GET", "/api/runs/"+first, nil, bearer(a)); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("run token on a user route = %d %s", res.StatusCode, body)
	}
	raise := func(run, tok string) (*http.Response, []byte) {
		return x.do(t, "POST", "/api/runs/"+run+"/observed-ports", map[string]any{"ports": []any{}}, bearer(tok))
	}
	// The owner's personal token on a run-token route: not the shim.
	if res, body := raise(first, x.token); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("personal token on a run route = %d %s", res.StatusCode, body)
	}
	// Each run's token is its own.
	if res, body := raise(first, b); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("run %s with run %s's token = %d %s", first, second, res.StatusCode, body)
	}
	if res, body := raise(first, a); res.StatusCode != http.StatusNoContent {
		t.Errorf("run with its own token = %d %s", res.StatusCode, body)
	}
	// Without the Bearer scheme the same secret is not a credential.
	if res, body := x.do(t, "POST", "/api/runs/"+first+"/observed-ports", map[string]any{"ports": []any{}}, map[string]string{"Authorization": a}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("run token without its scheme = %d %s", res.StatusCode, body)
	}
	// A ping belongs to the run that raised it, whatever token asks.
	p, err := x.e.RaisePing(first, "clarification", "q", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res, body := x.do(t, "GET", "/api/runs/"+second+"/pings/"+p.ID, nil, bearer(b)); res.StatusCode != http.StatusNotFound {
		t.Errorf("another run's ping = %d %s", res.StatusCode, body)
	}
	// A run with no live token (never existed) takes none.
	if res, _ := x.do(t, "GET", "/api/runs/nope/pings/x", nil, bearer("")); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown run = %d", res.StatusCode)
	}
}

// A personal token keeps working until its owner or its clock says otherwise.
func TestPersonalTokenLifecycle(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	other := x.addUser(t, "usr-other", false)

	// Expired: as good as none.
	past := time.Now().Add(-time.Minute)
	expired, _, err := x.srv.Auth.NewToken(ctx, testUser, "old", &past)
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := x.do(t, "GET", "/api/me", nil, map[string]string{"Authorization": "Bearer " + expired}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("expired token = %d", res.StatusCode)
	}

	// Minted over the API, the secret is shown once and the list never has it.
	soon := time.Now().Add(time.Hour)
	res, body := x.do(t, "POST", "/api/me/tokens", TokenRequest{Name: "ci", ExpiresAt: &soon}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create token = %d %s", res.StatusCode, body)
	}
	created := decode[TokenCreated](t, body)
	if !strings.HasPrefix(created.Secret, auth.TokenPrefix) || created.Token.ExpiresAt == nil {
		t.Errorf("created = %+v", created)
	}
	mine := map[string]string{"Authorization": "Bearer " + created.Secret}
	if res, body := x.do(t, "GET", "/api/me", nil, mine); res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"id":"`+testUser+`"`) {
		t.Errorf("me with the new token = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "GET", "/api/me/tokens", nil, nil)
	if res.StatusCode != http.StatusOK || strings.Contains(string(body), created.Secret) || strings.Contains(string(body), `"hash"`) {
		t.Errorf("token list = %d %s", res.StatusCode, body)
	}
	if list := decode[[]auth.Token](t, body); len(list) != 3 { // tests, old, ci
		t.Errorf("token list = %+v", list)
	}

	// Someone else's list is theirs, and it starts with their one token.
	if _, body := x.do(t, "GET", "/api/me/tokens", nil, other); len(decode[[]auth.Token](t, body)) != 1 {
		t.Errorf("other's token list = %s", body)
	}
	// They cannot revoke mine; it reads as a token that is not there.
	if res, body := x.do(t, "DELETE", "/api/me/tokens/"+created.Token.ID, nil, other); res.StatusCode != http.StatusNotFound {
		t.Errorf("revoking someone else's token = %d %s", res.StatusCode, body)
	}
	if res, _ := x.do(t, "GET", "/api/me", nil, mine); res.StatusCode != http.StatusOK {
		t.Errorf("token after the failed revoke = %d", res.StatusCode)
	}
	// Mine to revoke, and then it is gone.
	if res, body := x.do(t, "DELETE", "/api/me/tokens/"+created.Token.ID, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("revoke = %d %s", res.StatusCode, body)
	}
	if res, _ := x.do(t, "GET", "/api/me", nil, mine); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked token = %d", res.StatusCode)
	}
	if res, _ := x.do(t, "DELETE", "/api/me/tokens/"+created.Token.ID, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("double revoke = %d", res.StatusCode)
	}

	// A token needs a name, and the body must be one.
	if res, _ := x.do(t, "POST", "/api/me/tokens", TokenRequest{Name: " "}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("nameless token = %d", res.StatusCode)
	}
	if res, _ := x.bare(t, "POST", "/api/me/tokens", "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body = %d", res.StatusCode)
	}
}

// session starts a browser session for a user and returns its cookie header.
func (x *env) session(t *testing.T, userID string) map[string]string {
	t.Helper()
	raw, _, err := x.srv.Auth.NewSession(context.Background(), userID, "test")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Cookie": sessionCookie + "=" + raw}
}

func merged(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// A cookie rides along on a cross-site request, so every write that a session
// can reach must come from the UI's own origin; a bearer header cannot be
// sent by another site, so those are trusted as is.
func TestCookieWritesMustComeFromTheOrigin(t *testing.T) {
	w := newWorld(t)
	cookie := w.session(t, testUser)
	n := 0
	for _, rt := range w.enabled(t) {
		if rt.Method == "GET" || rt.Auth == AccessRunToken || rt.Auth == AccessBrokerToken {
			continue
		}
		n++
		path := w.address(rt)
		for name, site := range map[string]map[string]string{
			"cross-site":  {"Sec-Fetch-Site": "cross-site"},
			"same-site":   {"Sec-Fetch-Site": "same-site"},
			"origin":      {"Origin": "https://evil.example"},
			"bad-origin":  {"Origin": "://nonsense"},
			"origin-port": {"Origin": strings.Replace(w.ts.URL, "127.0.0.1", "localhost", 1)},
		} {
			res, body := w.bare(t, rt.Method, path, "{}", merged(cookie, site))
			if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "cross-site") {
				t.Errorf("%s %s (%s) = %d %s, want 403", rt.Method, path, name, res.StatusCode, body)
			}
		}
	}
	if n < 50 {
		t.Fatalf("walked %d writes", n)
	}

	// The UI's own requests pass, by either signal; reads are not writes.
	path := "/api/me/tokens"
	for _, site := range []map[string]string{
		{"Sec-Fetch-Site": "same-origin"},
		{"Sec-Fetch-Site": "none"}, // typed into the address bar
		{"Origin": w.ts.URL},
		{}, // no browser at all
	} {
		res, body := w.bare(t, "POST", path, `{"name":"from-ui"}`, merged(cookie, site))
		if res.StatusCode != http.StatusCreated {
			t.Errorf("%v = %d %s", site, res.StatusCode, body)
		}
	}
	if res, _ := w.bare(t, "GET", "/api/me", "", merged(cookie, map[string]string{"Sec-Fetch-Site": "cross-site"})); res.StatusCode != http.StatusOK {
		t.Errorf("cross-site read = %d, want 200", res.StatusCode)
	}
	// A bearer write is not a browser's, wherever it says it came from.
	res, body := w.bare(t, "POST", path, `{"name":"from-script"}`, merged(
		map[string]string{"Authorization": "Bearer " + w.token2(t)}, map[string]string{"Sec-Fetch-Site": "cross-site"}))
	if res.StatusCode != http.StatusCreated {
		t.Errorf("bearer write = %d %s", res.StatusCode, body)
	}
}

func (w *world) token2(t *testing.T) string {
	t.Helper()
	secret, _, err := w.srv.Auth.NewToken(context.Background(), testUser, "script", nil)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// A server wired without accounts is misconfigured, never open.
func TestServerWithoutAuthIsClosed(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.srv.Auth = nil
	x.reserve(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/auth/status"}, {"POST", "/api/auth/setup"}, {"POST", "/api/auth/login"},
		{"GET", "/api/me"}, {"GET", "/api/runs"}, {"GET", "/api/users"},
	} {
		res, body := x.bare(t, c.method, c.path, "{}", nil)
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d %s, want 503", c.method, c.path, res.StatusCode, body)
		}
	}
}

// failingDB makes one account-store call fail: the outage reads as a 500, not
// as a verdict on the credential.
type failingDB struct {
	auth.DB
	err error
}

func (f failingDB) GetTokenByHash(context.Context, string) (auth.Token, error) {
	return auth.Token{}, f.err
}
func (f failingDB) GetSession(context.Context, string) (auth.Session, error) {
	return auth.Session{}, f.err
}
func (f failingDB) CountUsers(context.Context) (int, error) { return 0, f.err }
func (f failingDB) GetUserByLogin(context.Context, string) (auth.User, error) {
	return auth.User{}, f.err
}
func (f failingDB) ListMethods(context.Context, string) ([]auth.Method, error) { return nil, f.err }
func (f failingDB) ListUsers(context.Context) ([]auth.User, error)             { return nil, f.err }
func (f failingDB) ListTokens(context.Context, string) ([]auth.Token, error)   { return nil, f.err }
func (f failingDB) ListSSHKeys(context.Context, string) ([]auth.SSHKey, error) { return nil, f.err }
func (f failingDB) DeleteSession(context.Context, string) error                { return f.err }

func TestAccountStoreOutageIs500(t *testing.T) {
	x := newEnv(t, engine.Config{})
	boom := context.DeadlineExceeded
	cookie := x.session(t, testUser)
	x.srv.Auth = authtest.New(failingDB{DB: x.users, err: boom})

	for name, hdr := range map[string]map[string]string{
		"bearer": {"Authorization": "Bearer " + auth.TokenPrefix + "x"},
		"cookie": cookie,
	} {
		if res, body := x.bare(t, "GET", "/api/me", "", hdr); res.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s during an outage = %d %s", name, res.StatusCode, body)
		}
	}
	if res, body := x.bare(t, "GET", "/api/auth/status", "", nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d %s", res.StatusCode, body)
	}
	if res, body := x.bare(t, "POST", "/api/auth/login", `{"login":"lu","proof":"x"}`, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("login = %d %s", res.StatusCode, body)
	}
}

// A handler past the guard hits the same outage: one store that works for the
// guard and fails for the handler.
type failAfterGuard struct {
	auth.DB
	err error
}

func (f failAfterGuard) ListMethods(context.Context, string) ([]auth.Method, error) {
	return nil, f.err
}
func (f failAfterGuard) ListUsers(context.Context) ([]auth.User, error)           { return nil, f.err }
func (f failAfterGuard) ListTokens(context.Context, string) ([]auth.Token, error) { return nil, f.err }
func (f failAfterGuard) ListSSHKeys(context.Context, string) ([]auth.SSHKey, error) {
	return nil, f.err
}
func (f failAfterGuard) DeleteSession(context.Context, string) error { return f.err }

func TestHandlersReportTheAccountStoreFailing(t *testing.T) {
	x := newEnv(t, engine.Config{})
	cookie := x.session(t, testUser)
	x.srv.Auth = authtest.New(failAfterGuard{DB: x.users, err: context.DeadlineExceeded})
	for _, c := range []struct {
		method, path string
		hdr          map[string]string
	}{
		{"GET", "/api/me", nil},
		{"PUT", "/api/me", nil},
		{"GET", "/api/users", nil},
		{"GET", "/api/me/tokens", nil},
		{"GET", "/api/me/ssh-keys", nil},
		{"POST", "/api/auth/logout", cookie},
	} {
		res, body := x.do(t, c.method, c.path, map[string]string{}, c.hdr)
		if res.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "deadline") {
			t.Errorf("%s %s = %d %s", c.method, c.path, res.StatusCode, body)
		}
	}
}

// Whoever owns a project owns what hangs off it; a principal's id on the
// context is what every list reads.
func TestListsAreEachPersonsOwn(t *testing.T) {
	w := newWorld(t)
	for _, path := range []string{
		"/api/runs", "/api/tickets", "/api/projects", "/api/integrations", "/api/credentials",
		"/api/skills", "/api/skillsets", "/api/channels", "/api/flows", "/api/pings?status=all",
	} {
		res, body := w.do(t, "GET", path, nil, w.other)
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s = %d %s", path, res.StatusCode, body)
			continue
		}
		var list []json.RawMessage
		if err := json.Unmarshal(body, &list); err != nil || len(list) != 0 {
			t.Errorf("%s as other = %s, want an empty list", path, body)
		}
		// The owner's holds something (flows aside: this fixture saved none),
		// so the empty list above is a filter and not an empty server.
		if strings.HasPrefix(path, "/api/flows") {
			continue
		}
		res, body = w.do(t, "GET", path, nil, nil)
		if err := json.Unmarshal(body, &list); err != nil || len(list) == 0 {
			t.Errorf("%s as the owner = %d %s", path, res.StatusCode, body)
		}
	}
}

func secretsKey(t *testing.T) secrets.Key {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	return key
}
