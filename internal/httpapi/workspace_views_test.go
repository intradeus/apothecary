package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/engine/ask"
	"apothecary/internal/ports/egressport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/sidecar"
)

// call runs a handler directly as a signed-in user, for the branches the guard
// stands in front of: a run that vanishes after the ownership check, a store
// that fails past it.
func call(h http.HandlerFunc, method, target, body, userID string, path map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range path {
		req.SetPathValue(k, v)
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{User: auth.User{ID: userID}}))
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

// workspaceEnv is a provisioner with the optional capabilities the fake
// lacks: a sidecar (an httptest server on the other end of the dial) and a
// build cache.
type workspaceEnv struct {
	*fake.EnvProvisioner
	sidecarAddr string
	sidecarErr  error

	mu           sync.Mutex
	cache        envport.BuildCacheInfo
	cacheErr     error
	cacheProject []string
	policyErr    error
	allowErr     error
}

type dialTCP string

func (a dialTCP) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", string(a))
}
func (dialTCP) Close() error { return nil }

func (w *workspaceEnv) Sidecar(context.Context, envport.EnvID) (envport.PortConn, error) {
	if w.sidecarErr != nil {
		return nil, w.sidecarErr
	}
	return dialTCP(w.sidecarAddr), nil
}

func (w *workspaceEnv) BuildCache(_ context.Context, project string) (envport.BuildCacheInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cacheProject = append(w.cacheProject, project)
	return w.cache, w.cacheErr
}

func (w *workspaceEnv) ClearBuildCache(_ context.Context, project string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cacheProject = append(w.cacheProject, "clear:"+project)
	return w.cacheErr
}

func (w *workspaceEnv) EgressPolicy(ctx context.Context, env envport.EnvID) (egressport.EgressPolicy, error) {
	if w.policyErr != nil {
		return egressport.EgressPolicy{}, w.policyErr
	}
	return w.EnvProvisioner.EgressPolicy(ctx, env)
}

func (w *workspaceEnv) AllowEgress(ctx context.Context, env envport.EnvID, host string) error {
	if w.allowErr != nil {
		return w.allowErr
	}
	return w.EnvProvisioner.AllowEgress(ctx, env, host)
}

func (w *workspaceEnv) SetEgressRule(ctx context.Context, env envport.EnvID, host string, allow bool) error {
	if w.allowErr != nil {
		return w.allowErr
	}
	return w.EnvProvisioner.SetEgressRule(ctx, env, host, allow)
}

// seedLiveRun records a run that has a workspace and a primary checkout, with
// no loop behind it: what the read views work from.
func (x *env) seedLiveRun(t *testing.T, id string) {
	t.Helper()
	if _, err := x.store.Append(context.Background(), id,
		&event.RunCreated{ProjectID: testProject, UserID: testUser, FlowName: "loop", Checkouts: []event.Checkout{{Name: "repo", Dir: "repo", Source: "develop"}}},
		&event.EnvReady{EnvID: "env-" + id},
	); err != nil {
		t.Fatal(err)
	}
}

// fakeSidecar answers the workspace routes and records what they asked for.
func fakeSidecar(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		q := r.URL.Query()
		switch {
		case q.Get("path") == "denied":
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "outside the checkout"})
		case q.Get("path") == "broken":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "git exploded"})
		case q.Get("path") == "empty":
			_, _ = io.WriteString(w, "null")
		case r.URL.Path == "/fs/tree":
			_ = json.NewEncoder(w).Encode([]sidecar.TreeEntry{{Name: "main.go", Path: "main.go"}})
		case r.URL.Path == "/fs/file":
			_ = json.NewEncoder(w).Encode(sidecar.File{Path: q.Get("path"), Content: "package main"})
		case r.URL.Path == "/git/changes":
			if q.Get("repo") == "broken" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.Changes{Branch: "feat/x", Base: q.Get("base")})
		case r.URL.Path == "/git/diff":
			_ = json.NewEncoder(w).Encode(sidecar.FileDiff{Path: q.Get("path"), Diff: "+x"})
		case r.URL.Path == "/term":
			_, _ = io.WriteString(w, "terminal for "+q.Get("repo"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The workspace views ask the run's sidecar about the named checkout, against
// the branch it forked from, and pass the sidecar's own refusals through.
func TestWorkspaceViewsThroughTheSidecar(t *testing.T) {
	x := newEnv(t, engine.Config{})
	var seen []string
	side := fakeSidecar(t, &seen)
	x.res.Envp = &workspaceEnv{EnvProvisioner: x.envp, sidecarAddr: strings.TrimPrefix(side.URL, "http://")}
	x.seedLiveRun(t, "r1")
	base := "/api/runs/r1"

	res, body := x.do(t, "GET", base+"/workspace/tree?path=src", nil, nil)
	if res.StatusCode != http.StatusOK || decode[[]sidecar.TreeEntry](t, body)[0].Name != "main.go" {
		t.Errorf("tree = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "GET", base+"/workspace/tree?path=empty", nil, nil)
	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("empty tree = %d %s, want a list", res.StatusCode, body)
	}
	res, body = x.do(t, "GET", base+"/workspace/file?path=main.go", nil, nil)
	if res.StatusCode != http.StatusOK || decode[sidecar.File](t, body).Content != "package main" {
		t.Errorf("file = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "GET", base+"/workspace/changes", nil, nil)
	if res.StatusCode != http.StatusOK || decode[sidecar.Changes](t, body).Base != "origin/develop" {
		t.Errorf("changes = %d %s, want them against the fork point", res.StatusCode, body)
	}
	res, body = x.do(t, "GET", base+"/workspace/diff?path=main.go", nil, nil)
	if res.StatusCode != http.StatusOK || decode[sidecar.FileDiff](t, body).Diff != "+x" {
		t.Errorf("diff = %d %s", res.StatusCode, body)
	}
	found := false
	for _, s := range seen {
		found = found || strings.HasPrefix(s, "/git/diff") && strings.Contains(s, "repo=repo") && strings.Contains(s, "base=origin%2Fdevelop")
	}
	if !found {
		t.Errorf("the sidecar was never asked for the primary against its source: %v", seen)
	}

	// Its 4xx are the caller's fault and keep their status; anything else is
	// the gateway failing.
	for _, view := range []string{"tree", "file", "diff"} {
		res, body := x.do(t, "GET", base+"/workspace/"+view+"?path=denied", nil, nil)
		if res.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "outside the checkout") {
			t.Errorf("%s denied = %d %s", view, res.StatusCode, body)
		}
		res, body = x.do(t, "GET", base+"/workspace/"+view+"?path=broken", nil, nil)
		if res.StatusCode != http.StatusBadGateway {
			t.Errorf("%s broken = %d %s", view, res.StatusCode, body)
		}
	}
	// A checkout the run does not have.
	if res, body := x.do(t, "GET", base+"/workspace/changes?repo=nope", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown checkout = %d %s", res.StatusCode, body)
	}

	// The terminal proxies to the sidecar for the checkout.
	res, body = x.do(t, "GET", base+"/terminal", nil, map[string]string{"Origin": x.ts.URL})
	if res.StatusCode != http.StatusOK || string(body) != "terminal for repo" {
		t.Errorf("terminal = %d %s", res.StatusCode, body)
	}
}

func TestWorkspaceWhenTheSidecarCannotBeReached(t *testing.T) {
	x := newEnv(t, engine.Config{})
	side := &workspaceEnv{EnvProvisioner: x.envp, sidecarErr: errors.New("no socket")}
	x.res.Envp = side
	x.seedLiveRun(t, "r1")
	res, body := x.do(t, "GET", "/api/runs/r1/workspace/tree", nil, nil)
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "no socket") {
		t.Errorf("tree = %d %s", res.StatusCode, body)
	}
	// The sidecar's socket answers nothing at all: a gateway failure too.
	side.sidecarErr = nil
	side.sidecarAddr = "127.0.0.1:1"
	for _, view := range []string{"changes", "diff?path=a"} {
		if res, body := x.do(t, "GET", "/api/runs/r1/workspace/"+view, nil, nil); res.StatusCode != http.StatusBadGateway {
			t.Errorf("%s = %d %s", view, res.StatusCode, body)
		}
	}
	// A run with no workspace yet has nothing to ask.
	if _, err := x.store.Append(context.Background(), "r2", &event.RunCreated{ProjectID: testProject, UserID: testUser, FlowName: "loop"}); err != nil {
		t.Fatal(err)
	}
	if res, body := x.do(t, "GET", "/api/runs/r2/workspace/tree", nil, nil); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("no workspace = %d %s", res.StatusCode, body)
	}
}

// The handlers answer for what fails after the guard let the request by.
func TestWorkspaceReadsForAVanishedRun(t *testing.T) {
	x := newEnv(t, engine.Config{})
	s := x.srv
	id := map[string]string{"id": "gone"}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"repos":    call(s.getWorkspaceRepos, "GET", "/", "", testUser, id),
		"mrstatus": call(s.getRunMRStatus, "GET", "/", "", testUser, id),
		"phases":   call(s.getRunPhases, "GET", "/", "", testUser, id),
	} {
		if w.Code != http.StatusNotFound {
			t.Errorf("%s = %d %s", name, w.Code, w.Body)
		}
	}
	if w := call(s.getWorkspaceLog, "GET", "/", "", testUser, id); w.Code != http.StatusNotFound {
		t.Errorf("log = %d %s", w.Code, w.Body)
	}
}

func TestRunMRStatus(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	res, body := x.do(t, "GET", "/api/runs/r1/mrs/status", nil, nil)
	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "null" && strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("no merge requests = %d %s", res.StatusCode, body)
	}
}

// The build cache is a provisioner capability, keyed by the project.
func TestBuildCacheAPI(t *testing.T) {
	x := newEnv(t, engine.Config{})
	path := "/api/projects/" + testProject + "/build-cache"

	// The fixture's fake keeps none, and says so.
	if res, body := x.do(t, "GET", path, nil, nil); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("no cache = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "DELETE", path, nil, nil); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("clear no cache = %d %s", res.StatusCode, body)
	}

	w := &workspaceEnv{EnvProvisioner: x.envp, cache: envport.BuildCacheInfo{Available: true, Bytes: 4096}}
	x.res.Envp = w
	res, body := x.do(t, "GET", path, nil, nil)
	if got := decode[BuildCacheInfo](t, body); res.StatusCode != http.StatusOK || !got.Available || got.Bytes != 4096 {
		t.Errorf("cache = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "DELETE", path, nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("clear = %d %s", res.StatusCode, body)
	}
	if len(w.cacheProject) != 2 || w.cacheProject[0] != testProject || w.cacheProject[1] != "clear:"+testProject {
		t.Errorf("cache calls = %v", w.cacheProject)
	}

	w.cacheErr = errors.New("daemon down")
	for _, method := range []string{"GET", "DELETE"} {
		if res, body := x.do(t, method, path, nil, nil); res.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "daemon down") {
			t.Errorf("%s during an outage = %d %s", method, res.StatusCode, body)
		}
	}
}

// memAudit is a durable audit store for the audit routes.
type memAudit struct {
	rows   []egressport.EgressAuditRow
	err    error
	filter egressport.EgressAuditFilter
}

func (m *memAudit) UpsertEgressAudit(context.Context, []egressport.EgressAuditRow) error { return nil }
func (m *memAudit) QueryEgressAudit(_ context.Context, f egressport.EgressAuditFilter) ([]egressport.EgressAuditRow, error) {
	m.filter = f
	return m.rows, m.err
}

func TestEgressAuditRoutes(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	for _, path := range []string{"/api/runs/r1/egress-audit", "/api/projects/" + testProject + "/egress-audit"} {
		// No durable store wired: an empty trail, not an error.
		res, body := x.do(t, "GET", path, nil, nil)
		if res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
			t.Errorf("%s with no audit = %d %s", path, res.StatusCode, body)
		}
	}
	audit := &memAudit{rows: []egressport.EgressAuditRow{{Run: "r1", Host: "example.com", Verdict: "deny", Count: 3}}}
	x.srv.Egress.Audit = audit

	res, body := x.do(t, "GET", "/api/runs/r1/egress-audit?verdict=deny&host=exam&limit=7", nil, nil)
	if got := decode[[]egressport.EgressAuditRow](t, body); res.StatusCode != http.StatusOK || len(got) != 1 || got[0].Count != 3 {
		t.Fatalf("audit = %d %s", res.StatusCode, body)
	}
	if f := audit.filter; f.Run != "r1" || f.Verdict != "deny" || f.Host != "exam" || f.Limit != 7 || f.Project != "" {
		t.Errorf("run filter = %+v", f)
	}
	if res, body := x.do(t, "GET", "/api/projects/"+testProject+"/egress-audit?verdict=allow", nil, nil); res.StatusCode != http.StatusOK {
		t.Errorf("project audit = %d %s", res.StatusCode, body)
	}
	if f := audit.filter; f.Project != testProject || f.Run != "" || f.Verdict != "allow" {
		t.Errorf("project filter = %+v", f)
	}

	audit.err = errors.New("audit down")
	for _, path := range []string{"/api/runs/r1/egress-audit", "/api/projects/" + testProject + "/egress-audit"} {
		if res, body := x.do(t, "GET", path, nil, nil); res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "audit down") {
			t.Errorf("%s = %d %s", path, res.StatusCode, body)
		}
	}
}

func TestEgressPolicyAndDenials(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	if _, err := x.envp.Provision(context.Background(), envport.EnvSpec{}); err != nil {
		t.Fatal(err)
	}

	res, body := x.do(t, "GET", "/api/runs/r1/egress-denials", nil, nil)
	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("no denials = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "GET", "/api/runs/r1/egress-policy", nil, nil)
	got := decode[EgressPolicy](t, body)
	if res.StatusCode != http.StatusOK || got.Domains == nil || got.BlockedDomains == nil {
		t.Errorf("policy = %d %s, want lists not nulls", res.StatusCode, body)
	}

	// Without a gateway, or with one that fails, the page is told which.
	side := &workspaceEnv{EnvProvisioner: x.envp, policyErr: errors.New("gateway down")}
	x.res.Envp = side
	if res, body := x.do(t, "GET", "/api/runs/r1/egress-policy", nil, nil); res.StatusCode != http.StatusBadGateway {
		t.Errorf("gateway failure = %d %s", res.StatusCode, body)
	}
	x.res.Envp = bareEnv{x.envp}
	if res, body := x.do(t, "GET", "/api/runs/r1/egress-policy", nil, nil); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("no gateway = %d %s", res.StatusCode, body)
	}
}

// bareEnv is a provisioner with no optional capabilities at all.
type bareEnv struct{ envport.EnvProvisioner }

func TestEgressHostShapes(t *testing.T) {
	for host, want := range map[string]bool{
		"example.com": true, "*.example.com": true, "a-b.example.co": true, "A.Example.COM": true,
		"": false, "*.": false, "localhost": false, "http://x.y": false, "x.y:80": false, "x.y/z": false,
		"me@x.y": false, "a b.c": false, "ex_ample.com": false, "exämple.com": false, strings.Repeat("a", 250) + ".com": false,
	} {
		if got := validEgressHost(host); got != want {
			t.Errorf("validEgressHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// The rule toggle changes the live gateway and keeps the project's two lists
// exclusive, so future runs inherit the same answer.
func TestEgressRule(t *testing.T) {
	x := newConfEnv(t)
	projectID := x.wizard(t)
	runID := x.runOnProject(t, projectID, "T")
	path := "/api/runs/" + runID + "/egress-rule"

	set := func(host string, allow bool) (*http.Response, []byte) {
		return x.do(t, "POST", path, EgressRuleRequest{Host: host, Allow: allow}, nil)
	}
	res, body := set(" Example.COM ", false)
	if got := decode[EgressRuleResult](t, body); res.StatusCode != http.StatusOK || got.Host != "example.com" || got.Allow || !got.Persisted {
		t.Fatalf("block = %d %s", res.StatusCode, body)
	}
	p, _ := x.conf.GetProject(t.Context(), projectID)
	if !containsStr(p.Workspace.BlockedDomains, "example.com") || containsStr(p.Workspace.ExtraDomains, "example.com") {
		t.Errorf("after block: %+v", p.Workspace)
	}
	// Twice is once.
	set("example.com", false)
	p, _ = x.conf.GetProject(t.Context(), projectID)
	if n := len(p.Workspace.BlockedDomains); n != 1 {
		t.Errorf("blocked twice: %v", p.Workspace.BlockedDomains)
	}

	if res, body := set("example.com", true); res.StatusCode != http.StatusOK {
		t.Fatalf("allow = %d %s", res.StatusCode, body)
	}
	p, _ = x.conf.GetProject(t.Context(), projectID)
	if containsStr(p.Workspace.BlockedDomains, "example.com") || !containsStr(p.Workspace.ExtraDomains, "example.com") {
		t.Errorf("after allow: %+v", p.Workspace)
	}
	set("example.com", true)
	p, _ = x.conf.GetProject(t.Context(), projectID)
	if n := len(p.Workspace.ExtraDomains); n != 1 {
		t.Errorf("allowed twice: %v", p.Workspace.ExtraDomains)
	}
	// Allowing again via the button reports it as already persisted.
	res, body = x.do(t, "POST", "/api/runs/"+runID+"/egress-allow", EgressHostRequest{Host: "example.com"}, nil)
	if got := decode[EgressAllowResult](t, body); res.StatusCode != http.StatusOK || !got.Persisted {
		t.Errorf("re-allow = %d %s", res.StatusCode, body)
	}
	p, _ = x.conf.GetProject(t.Context(), projectID)
	if n := len(p.Workspace.ExtraDomains); n != 1 {
		t.Errorf("re-allow duplicated the host: %v", p.Workspace.ExtraDomains)
	}

	if res, _ := set("http://x.y", true); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad host = %d", res.StatusCode)
	}
	if res, _ := x.bare(t, "POST", path, "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
	if res, _ := x.bare(t, "POST", "/api/runs/"+runID+"/egress-allow", "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed allow = %d", res.StatusCode)
	}
}

// A live change that fails is refused and nothing is written to the project;
// with no gateway the answer says it is off.
func TestEgressChangesWhenTheGatewayRefuses(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	side := &workspaceEnv{EnvProvisioner: x.envp, allowErr: errors.New("gateway refused")}
	x.res.Envp = side
	for _, route := range []string{"egress-allow", "egress-rule"} {
		path := "/api/runs/r1/" + route
		res, body := x.do(t, "POST", path, map[string]any{"host": "example.com"}, nil)
		if res.StatusCode != http.StatusConflict || !strings.Contains(string(body), "gateway refused") {
			t.Errorf("%s refused = %d %s", route, res.StatusCode, body)
		}
	}
	p, err := x.srv.Conf.GetProject(context.Background(), testProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Workspace.ExtraDomains)+len(p.Workspace.BlockedDomains) != 0 {
		t.Errorf("a refused change was persisted: %+v", p.Workspace)
	}
	x.res.Envp = bareEnv{x.envp}
	for _, route := range []string{"egress-allow", "egress-rule"} {
		res, body := x.do(t, "POST", "/api/runs/r1/"+route, map[string]any{"host": "example.com"}, nil)
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s with no gateway = %d %s", route, res.StatusCode, body)
		}
	}
}

// Without the config plane the live change still happens and says it was not
// kept.
func TestEgressChangeWithNothingToPersistIn(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	x.srv.Conf = nil
	res, body := x.do(t, "POST", "/api/runs/r1/egress-allow", EgressHostRequest{Host: "example.com"}, nil)
	if got := decode[EgressAllowResult](t, body); res.StatusCode != http.StatusOK || got.Persisted {
		t.Errorf("allow = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "POST", "/api/runs/r1/egress-rule", EgressRuleRequest{Host: "example.com", Allow: true}, nil)
	if got := decode[EgressRuleResult](t, body); res.StatusCode != http.StatusOK || got.Persisted {
		t.Errorf("rule = %d %s", res.StatusCode, body)
	}
}

func TestEgressDefaultsArePublicToUsers(t *testing.T) {
	x := newEnv(t, engine.Config{})
	res, body := x.do(t, "GET", "/api/egress-defaults", nil, nil)
	if got := decode[EgressDefaults](t, body); res.StatusCode != http.StatusOK || len(got.Domains) == 0 {
		t.Errorf("defaults = %d %s", res.StatusCode, body)
	}
}

// readSSE reads frames until the stream closes, or stop says enough.
func readSSE(t *testing.T, r io.Reader, stop func(lines []string) bool) []string {
	t.Helper()
	var lines []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if stop != nil && stop(lines) {
			break
		}
	}
	return lines
}

// The Ask stream replays after its cursor, a reconnect's Last-Event-ID wins
// when it is further on, and a shutdown ends it.
func TestAskStream(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	store := ask.NewMemStore()
	x.srv.Ask = ask.New(&run.Reader{Store: x.store}, x.srv.Workspaces.Resolver, x.e, store)
	ctx := context.Background()
	for _, text := range []string{"one", "two", "three"} {
		if _, err := store.AppendAskMessage(ctx, ask.Message{RunID: "r1", Role: ask.RoleQuestion, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/runs/r1/ask/stream"

	if res, body := x.do(t, "GET", path+"?after_seq=x", nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad cursor = %d %s", res.StatusCode, body)
	}

	stream := func(query string, hdr map[string]string, want int) []string {
		t.Helper()
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(sctx, "GET", x.ts.URL+path+query, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(x.authed(req))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Fatalf("content-type = %q", ct)
		}
		got := 0
		return readSSE(t, res.Body, func(lines []string) bool {
			if strings.HasPrefix(lines[len(lines)-1], "data: ") {
				got++
			}
			return got == want
		})
	}
	lines := stream("?after_seq=1", nil, 2)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, `"text":"one"`) || !strings.Contains(joined, "id: 2") || !strings.Contains(joined, `"text":"three"`) {
		t.Errorf("after_seq=1 replay:\n%s", joined)
	}
	lines = stream("?after_seq=1", map[string]string{"Last-Event-ID": "2"}, 1)
	if joined := strings.Join(lines, "\n"); strings.Contains(joined, `"text":"two"`) || !strings.Contains(joined, `"text":"three"`) {
		t.Errorf("reconnect replay:\n%s", joined)
	}
	// A cursor that goes backwards does not rewind past the query's.
	lines = stream("?after_seq=2", map[string]string{"Last-Event-ID": "1"}, 1)
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, `"text":"three"`) || strings.Contains(joined, `"text":"two"`) {
		t.Errorf("backward id replay:\n%s", joined)
	}

	// Past the replay the stream stays open until the server drains.
	req, _ := http.NewRequest("GET", x.ts.URL+path+"?after_seq=3", nil)
	res, err := http.DefaultClient.Do(x.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, res.Body); close(done) }()
	select {
	case <-done:
		t.Fatal("the stream ended by itself")
	case <-time.After(20 * time.Millisecond):
	}
	x.srv.Drain()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not end the stream")
	}
}

func TestAskStatusMapping(t *testing.T) {
	for err, want := range map[error]int{
		ask.ErrOff:                     http.StatusConflict,
		ask.ErrBusy:                    http.StatusConflict,
		errors.New("run r not found"):  http.StatusNotFound,
		errors.New("question is long"): http.StatusBadRequest,
	} {
		if got := askStatus(err); got != want {
			t.Errorf("askStatus(%v) = %d, want %d", err, got, want)
		}
	}
}

// The Ask routes refuse a bad body, and say when the run is not there.
func TestAskHandlers(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	tok := map[string]string{"Authorization": "Bearer " + x.token}
	if res, _ := x.bare(t, "POST", "/api/runs/r1/ask", "{", tok); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed ask = %d", res.StatusCode)
	}
	if res, _ := x.bare(t, "PUT", "/api/runs/r1/ask/model", "{", tok); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed model = %d", res.StatusCode)
	}
	id := map[string]string{"id": "gone"}
	if w := call(x.srv.getAsk, "GET", "/", "", testUser, id); w.Code != http.StatusNotFound {
		t.Errorf("state of nothing = %d %s", w.Code, w.Body)
	}
}

// The events route's own edges: a bad cursor, a reconnect, a store that fails.
func TestEventStreamCursors(t *testing.T) {
	x := newEnv(t, engine.Config{})
	runID := x.createTicket(t, "T")
	x.e.Wait(runID)
	path := "/api/runs/" + runID + "/events"
	if res, body := x.do(t, "GET", path+"?after_seq=x", nil, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad cursor = %d %s", res.StatusCode, body)
	}
	evs, err := x.store.Events(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1].Seq

	// firstID is the id of the first frame a connection is sent.
	firstID := func(query string, hdr map[string]string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", x.ts.URL+path+query, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(x.authed(req))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		lines := readSSE(t, res.Body, func(lines []string) bool { return strings.HasPrefix(lines[len(lines)-1], "id: ") })
		if len(lines) == 0 {
			t.Fatalf("no frame for %s", query)
		}
		return lines[len(lines)-1]
	}
	want := "id: " + itoa(last)
	if got := firstID("", nil); got != "id: 1" {
		t.Errorf("from the start = %q", got)
	}
	// A reconnect resumes from Last-Event-ID when it is further on, and ignores
	// a rewind or garbage.
	if got := firstID("?after_seq=1", map[string]string{"Last-Event-ID": itoa(last - 1)}); got != want {
		t.Errorf("reconnect = %q, want %q", got, want)
	}
	if got := firstID("?after_seq="+itoa(last-1), map[string]string{"Last-Event-ID": "1"}); got != want {
		t.Errorf("rewinding header = %q, want %q", got, want)
	}
	if got := firstID("?after_seq="+itoa(last-1), map[string]string{"Last-Event-ID": "nonsense"}); got != want {
		t.Errorf("garbage header = %q, want %q", got, want)
	}
}

// The route's documentation promises the stream ends with the run.
func TestEventStreamEndsWithTheRun(t *testing.T) {
	x := newEnv(t, engine.Config{})
	runID := x.createTicket(t, "T")
	x.e.Wait(runID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", x.ts.URL+"/api/runs/"+runID+"/events", nil)
	res, err := http.DefaultClient.Do(x.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("the stream outlived its run: %v after %q", err, body)
	}
	if !strings.Contains(string(body), "run_completed") {
		t.Errorf("stream = %s", body)
	}
}

// A failed run can be retried, so a replay does not stop at a failure the log
// has already moved past.
func TestEventStreamReplaysPastARetriedFailure(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	if _, err := x.store.Append(context.Background(), "r1",
		&event.RunFailed{Reason: "boom"}, &event.RunRetried{StageID: "s"}, &event.RunCompleted{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", x.ts.URL+"/api/runs/r1/events", nil)
	res, err := http.DefaultClient.Do(x.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || !strings.Contains(string(body), "run_retried") || !strings.Contains(string(body), "run_completed") {
		t.Errorf("stream = %v, %s", err, body)
	}
}

// The browser reconnects when a stream ends: resuming past the end stays open,
// or it would reconnect for as long as the page is up, and a run's page must
// still hear about a retry or its workspace going.
func TestEventStreamResumingPastTheEndStaysOpen(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	last, err := x.store.Append(context.Background(), "r1", &event.RunFailed{Reason: "boom"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", x.ts.URL+"/api/runs/r1/events?after_seq="+itoa(last[len(last)-1].Seq), nil)
	res, err := http.DefaultClient.Do(x.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err := x.store.Append(context.Background(), "r1", &event.RunRetried{StageID: "s"}); err != nil {
		t.Fatal(err)
	}
	lines := readSSE(t, res.Body, func(lines []string) bool { return strings.Contains(lines[len(lines)-1], "run_retried") })
	if len(lines) == 0 {
		t.Fatal("the stream ended with nothing to say")
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// brokenStore fails its reads, for what the routes do past the guard.
type brokenStore struct {
	event.Store
	events, watch, ids error
}

func (b brokenStore) Events(ctx context.Context, id string, after int64) ([]event.Event, error) {
	if b.events != nil {
		return nil, b.events
	}
	return b.Store.Events(ctx, id, after)
}

func (b brokenStore) Watch(ctx context.Context, id string, after int64) (<-chan event.Event, error) {
	if b.watch != nil {
		return nil, b.watch
	}
	return b.Store.Watch(ctx, id, after)
}

func (b brokenStore) RunIDs(ctx context.Context) ([]string, error) {
	if b.ids != nil {
		return nil, b.ids
	}
	return b.Store.RunIDs(ctx)
}

func TestStoreOutagesReadAs500(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	boom := errors.New("store down")
	s := x.srv
	s.Store = brokenStore{Store: x.store, watch: boom}
	if res, body := x.do(t, "GET", "/api/runs/r1/events", nil, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("watch = %d %s", res.StatusCode, body)
	}
	// The ownership lookup failing is a 404, never a pass.
	s.Store = brokenStore{Store: x.store, events: boom}
	if res, body := x.do(t, "GET", "/api/runs/r1", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("guard during an outage = %d %s", res.StatusCode, body)
	}
	id := map[string]string{"id": "r1"}
	if w := call(s.getRun, "GET", "/", "", testUser, id); w.Code != http.StatusInternalServerError {
		t.Errorf("getRun = %d %s", w.Code, w.Body)
	}
	if w := call(s.streamEvents, "GET", "/", "", testUser, id); w.Code != http.StatusInternalServerError {
		t.Errorf("streamEvents = %d %s", w.Code, w.Body)
	}
	s.Store = brokenStore{Store: x.store, ids: boom}
	if res, body := x.do(t, "GET", "/api/runs", nil, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("list = %d %s", res.StatusCode, body)
	}
}

// The live log is an optional capability: without it, and for what it cannot
// find, the page gets a 404 to fall back on; anything else is the workspace.
func TestWorkspaceLogFailures(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	x.res.Envp = bareEnv{x.envp}
	if res, body := x.do(t, "GET", "/api/runs/r1/workspace-log", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("no live log = %d %s", res.StatusCode, body)
	}
	x.res.Envp = tailEnv{bareEnv{x.envp}, []byte("0123456789"), nil}
	res, body := x.do(t, "GET", "/api/runs/r1/workspace-log?n=4", nil, nil)
	if res.StatusCode != http.StatusOK || string(body) != "6789" {
		t.Errorf("tail = %d %q", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("tail content type = %q", ct)
	}
	x.res.Envp = tailEnv{bareEnv{x.envp}, nil, errors.New("docker logs failed")}
	if res, body := x.do(t, "GET", "/api/runs/r1/workspace-log", nil, nil); res.StatusCode != http.StatusBadGateway {
		t.Errorf("tail failure = %d %s", res.StatusCode, body)
	}
}

type tailEnv struct {
	bareEnv
	data []byte
	err  error
}

func (e tailEnv) TailExecLog(_ context.Context, _ envport.EnvID, n int) ([]byte, error) {
	if e.err != nil {
		return nil, e.err
	}
	if n > 0 && n < len(e.data) {
		return e.data[len(e.data)-n:], nil
	}
	return e.data, nil
}

// The URL parser's refusal of a malformed origin is a refusal, not a pass.
func TestSameOriginUpgrade(t *testing.T) {
	s := &Server{}
	for origin, ok := range map[string]bool{
		"": true, "http://ui.test": true, "HTTP://UI.TEST": true,
		"http://evil.test": false, "http://ui.test.evil.test": false, ":": false,
	} {
		req := httptest.NewRequest("GET", "http://ui.test/", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		if got := s.sameOriginUpgrade(w, req); got != ok {
			t.Errorf("origin %q = %v, want %v", origin, got, ok)
		}
		if !ok && w.Code != http.StatusForbidden {
			t.Errorf("origin %q refused with %d", origin, w.Code)
		}
	}
}

// compile-time: the helpers above serve the capability interfaces they name.
var (
	_ envport.Sidecar              = (*workspaceEnv)(nil)
	_ envport.BuildCacheController = (*workspaceEnv)(nil)
	_ envport.ExecLogTailer        = tailEnv{}
)
