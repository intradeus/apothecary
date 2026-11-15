package egressgw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"apothecary/internal/adapters/egressgw/policyfile"
)

// Every per-project name derives from the project's ID, lowercased (a ULID is
// uppercase Crockford base32; Kubernetes names and registry paths are not),
// and never from its name: a rename must not move a project off its gateway
// and caches, and two projects called "api" must not share them.
func TestNamesKeyOnTheProjectID(t *testing.T) {
	const id = "01HZX4V8Q2M9N7P3R5T1W6Y0AB"
	const key = "01hzx4v8q2m9n7p3r5t1w6y0ab"
	if got := gwName(id); got != "apo-gw-"+key {
		t.Errorf("gwName = %q", got)
	}
	if got := gwLabels(id)[labelGateway]; got != key {
		t.Errorf("gateway label = %q", got)
	}
	if got, want := buildCacheRepo("172.20.0.2", id), "172.20.0.2:5001/"+key+"/devcontainer"; got != want {
		t.Errorf("buildCacheRepo = %q, want %q", got, want)
	}
	// No project at all (direct and test callers) still gets a gateway.
	if got := gwName(""); got != "apo-gw-default" {
		t.Errorf("gwName(\"\") = %q", got)
	}
}

func TestBuildCacheRefIsStablePerProject(t *testing.T) {
	if BuildCacheRef("10.90.0.1", "P") != "10.90.0.1:5001/p/devcontainer:cache" {
		t.Errorf("cache ref = %q", BuildCacheRef("10.90.0.1", "P"))
	}
	if BuildCacheImageRef("10.90.0.1", "P") != "10.90.0.1:5001/p/devcontainer:image" {
		t.Errorf("image ref = %q", BuildCacheImageRef("10.90.0.1", "P"))
	}
}

func TestApplyRuleToggles(t *testing.T) {
	allow := policyfile.File{Mode: policyfile.ModeAllow, Domains: []string{"github.com"}}
	f := applyRule(allow, "example.com", true)
	if !contains(f.Domains, "example.com") || !contains(f.Domains, "github.com") {
		t.Fatalf("allow-mode allow = %+v", f)
	}
	f = applyRule(f, "github.com", false)
	if contains(f.Domains, "github.com") || !contains(f.BlockedDomains, "github.com") {
		t.Fatalf("allow-mode block = %+v", f)
	}
	f = applyRule(f, "github.com", true)
	if contains(f.BlockedDomains, "github.com") || !contains(f.Domains, "github.com") {
		t.Fatalf("unblock = %+v", f)
	}
	deny := policyfile.File{Mode: policyfile.ModeDeny, Domains: []string{"evil.example"}}
	f = applyRule(deny, "evil.example", true)
	if contains(f.Domains, "evil.example") {
		t.Fatalf("deny-mode allow must drop from the denylist: %+v", f)
	}
	if f := applyRule(policyfile.File{}, "x.io", true); f.Mode != policyfile.ModeAllow {
		t.Fatalf("empty mode not defaulted: %+v", f)
	}
}

// fakeGateway is an in-memory control API (the real one is package main in
// workspace/gateway) that records what the engine asked.
type fakeGateway struct {
	mu       sync.Mutex
	srv      *httptest.Server
	token    string
	policy   policyfile.File
	routes   policyfile.Routes
	denials  []policyfile.Denial
	audit    []policyfile.AuditRecord
	bytes    int64
	cleared  int
	requests []string
}

func newFakeGateway(t *testing.T) *fakeGateway {
	g := &fakeGateway{token: "tok", routes: policyfile.Routes{RunSubnets: map[string]string{}, Peers: map[string]policyfile.Peer{}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /policy", func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(g.policy) })
	mux.HandleFunc("PUT /policy", func(w http.ResponseWriter, r *http.Request) { json.NewDecoder(r.Body).Decode(&g.policy) })
	mux.HandleFunc("PUT /runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		var rr policyfile.RunRoute
		json.NewDecoder(r.Body).Decode(&rr)
		g.routes.RunSubnets[r.PathValue("run")] = rr.Subnet
		if rr.Peer != nil {
			g.routes.Peers[r.PathValue("run")] = *rr.Peer
		}
	})
	mux.HandleFunc("DELETE /runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		delete(g.routes.RunSubnets, r.PathValue("run"))
		delete(g.routes.Peers, r.PathValue("run"))
	})
	mux.HandleFunc("GET /routes", func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(g.routes) })
	mux.HandleFunc("GET /denials", func(w http.ResponseWriter, r *http.Request) {
		var out []policyfile.Denial
		for _, d := range g.denials {
			if d.Run == r.URL.Query().Get("run") {
				out = append(out, d)
			}
		}
		json.NewEncoder(w).Encode(policyfile.DenialsFile{Denials: out})
	})
	mux.HandleFunc("GET /audit", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(policyfile.AuditFile{Records: g.audit})
	})
	mux.HandleFunc("GET /buildcache", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(policyfile.BuildCacheInfo{Bytes: g.bytes})
	})
	mux.HandleFunc("DELETE /buildcache", func(http.ResponseWriter, *http.Request) { g.cleared++ })
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.requests = append(g.requests, r.Method+" "+r.URL.RequestURI())
		if r.URL.Path != "/healthz" && r.Header.Get("Authorization") != "Bearer "+g.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGateway) control() Control { return Control{BaseURL: g.srv.URL, Token: g.token} }

// fakeHost is a Host whose gateway is the fake one.
type fakeHost struct {
	gw       *fakeGateway
	up       bool
	att      Attachment
	attached []string
	detached []string
	held     map[string]string
}

func (h *fakeHost) EnsureGateway(context.Context, string, string) (Control, error) {
	h.up = true
	return h.gw.control(), nil
}
func (h *fakeHost) Control(context.Context, string) (Control, bool, error) {
	return h.gw.control(), h.up, nil
}
func (h *fakeHost) AttachRun(_ context.Context, _, _, run string, held map[string]string) (Attachment, error) {
	h.attached = append(h.attached, run)
	h.held = held
	return h.att, nil
}
func (h *fakeHost) DetachRun(_ context.Context, _, run string) error {
	h.detached = append(h.detached, run)
	return nil
}
func (h *fakeHost) RunProject(context.Context, string) (string, bool, error) { return "", false, nil }

func TestEnsureRunEgressRegistersRouteAndPolicy(t *testing.T) {
	gw := newFakeGateway(t)
	peer := &policyfile.Peer{PublicKey: "K", IP: "10.90.0.2"}
	h := &fakeHost{gw: gw, att: Attachment{GatewayIP: "10.90.0.1", Subnet: "10.90.0.2/32", Peer: peer}}
	m := NewManager(h)
	att, err := m.EnsureRunEgress(context.Background(), "acme", "Acme", "run1",
		policyfile.File{Domains: []string{"github.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if att.GatewayIP != "10.90.0.1" {
		t.Errorf("gwIP = %q", att.GatewayIP)
	}
	if gw.routes.RunSubnets["run1"] != "10.90.0.2/32" || gw.routes.Peers["run1"] != *peer {
		t.Errorf("routes = %+v", gw.routes)
	}
	if gw.policy.Mode != policyfile.ModeAllow || !contains(gw.policy.Domains, "github.com") {
		t.Errorf("policy = %+v (mode must default to allow)", gw.policy)
	}
	// Route before policy: the policy PUT is what a starting sandbox waits on.
	if i, j := slices.Index(gw.requests, "PUT /runs/run1"), slices.Index(gw.requests, "PUT /policy"); i < 0 || j < i {
		t.Errorf("request order = %v", gw.requests)
	}
}

func TestSetRuleRoundTripsThroughTheGateway(t *testing.T) {
	gw := newFakeGateway(t)
	gw.policy = policyfile.File{Mode: policyfile.ModeAllow, Domains: []string{"github.com"}}
	m := NewManager(&fakeHost{gw: gw, up: true})
	ctx := context.Background()
	if err := m.Allow(ctx, "acme", " Example.COM "); err != nil {
		t.Fatal(err)
	}
	if !contains(gw.policy.Domains, "example.com") {
		t.Fatalf("allow not applied: %+v", gw.policy)
	}
	if err := m.SetRule(ctx, "acme", "github.com", false); err != nil {
		t.Fatal(err)
	}
	f, err := m.Policy(ctx, "acme")
	if err != nil || !contains(f.BlockedDomains, "github.com") {
		t.Fatalf("Policy = %+v, %v", f, err)
	}
	if err := m.SetRule(ctx, "acme", "  ", true); err == nil {
		t.Fatal("empty host accepted")
	}
}

func TestReadsFilterAndCache(t *testing.T) {
	gw := newFakeGateway(t)
	gw.denials = []policyfile.Denial{{Run: "run1", Host: "a.com"}, {Run: "run2", Host: "b.com"}}
	gw.audit = []policyfile.AuditRecord{{Run: "run1", Host: "a.com"}}
	gw.bytes = 4096
	m := NewManager(&fakeHost{gw: gw, up: true})
	ctx := context.Background()
	if got := m.Denials(ctx, "acme", "run1"); len(got) != 1 || got[0].Host != "a.com" {
		t.Fatalf("Denials = %+v", got)
	}
	m.Audit(ctx, "acme")
	m.Audit(ctx, "acme")
	n := 0
	for _, r := range gw.requests {
		if r == "GET /audit" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("audit fetched %d times within a second, want 1 (cached)", n)
	}
	if b, err := m.BuildCacheSize(ctx, "acme"); err != nil || b != 4096 {
		t.Errorf("BuildCacheSize = %d, %v", b, err)
	}
	if err := m.ClearBuildCache(ctx, "acme"); err != nil || gw.cleared != 1 {
		t.Errorf("ClearBuildCache: %v, cleared=%d", err, gw.cleared)
	}
}

func TestNoGatewayIsAnAnswerNotACrash(t *testing.T) {
	m := NewManager(&fakeHost{gw: newFakeGateway(t)}) // never brought up
	ctx := context.Background()
	if f, err := m.Policy(ctx, "acme"); err != nil || f.Mode != policyfile.ModeAllow {
		t.Errorf("Policy = %+v, %v", f, err)
	}
	if d := m.Denials(ctx, "acme", "run1"); d != nil {
		t.Errorf("Denials = %+v", d)
	}
	if err := m.SetRule(ctx, "acme", "x.io", true); !errors.Is(err, ErrNoGateway) {
		t.Errorf("SetRule err = %v", err)
	}
	if _, err := m.BuildCacheSize(ctx, "acme"); !errors.Is(err, ErrNoGateway) {
		t.Errorf("BuildCacheSize err = %v", err)
	}
}

// Destroying the project's last run must NOT reap the gateway: it holds the
// warm caches. Only the run's route and link go.
func TestDestroyRunEgressKeepsGateway(t *testing.T) {
	gw := newFakeGateway(t)
	h := &fakeHost{gw: gw, att: Attachment{GatewayIP: "172.20.0.2", Subnet: "172.20.0.0/16"}}
	m := NewManager(h)
	ctx := context.Background()
	if _, err := m.EnsureRunEgress(ctx, "acme", "Acme", "run1", policyfile.File{}); err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRunEgress(ctx, "acme", "run1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := gw.routes.RunSubnets["run1"]; ok {
		t.Error("destroyed run still routed")
	}
	if !slices.Equal(h.detached, []string{"run1"}) {
		t.Errorf("detached = %v", h.detached)
	}
	if !h.up {
		t.Error("gateway taken down")
	}
}

// The host allocates against the gateway's own peers (other runs' only): a
// peer whose link was deleted without a detach still holds its address.
func TestEnsureRunEgressHandsTheHostTheGatewaysPeers(t *testing.T) {
	gw := newFakeGateway(t)
	gw.routes.Peers["orphan"] = policyfile.Peer{PublicKey: "O", IP: "10.90.0.3"}
	gw.routes.Peers["run1"] = policyfile.Peer{PublicKey: "K", IP: "10.90.0.2"}
	h := &fakeHost{gw: gw, att: Attachment{Subnet: "10.90.0.4/32", Peer: &policyfile.Peer{PublicKey: "K", IP: "10.90.0.4"}}}
	if _, err := NewManager(h).EnsureRunEgress(context.Background(), "acme", "", "run1", policyfile.File{}); err != nil {
		t.Fatal(err)
	}
	if len(h.held) != 1 || h.held["10.90.0.3"] != "orphan" {
		t.Errorf("held = %v (the run's own peer is not someone else's)", h.held)
	}
}

// The prune detaches what the gateway routes for runs that are not live, and
// asks which runs are live only after reading the table, under the project's
// lock, so a run that attaches meanwhile is never pruned.
func TestPruneRunsDetachesWhatIsNotLive(t *testing.T) {
	gw := newFakeGateway(t)
	gw.routes.RunSubnets["live"] = "10.90.0.2/32"
	gw.routes.Peers["live"] = policyfile.Peer{IP: "10.90.0.2"}
	gw.routes.RunSubnets["gone"] = "10.90.0.3/32"
	gw.routes.Peers["gone"] = policyfile.Peer{IP: "10.90.0.3"}
	gw.routes.Peers["peer-only"] = policyfile.Peer{IP: "10.90.0.4"}
	h := &fakeHost{gw: gw, up: true}
	m := NewManager(h)
	ctx := context.Background()
	var askedAfterRead bool
	pruned, err := m.PruneRuns(ctx, "acme", func(context.Context) (map[string]bool, error) {
		askedAfterRead = slices.Contains(gw.requests, "GET /routes")
		return map[string]bool{"live": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !askedAfterRead {
		t.Error("live was asked before the gateway's table was read")
	}
	if !slices.Equal(pruned, []string{"gone", "peer-only"}) || !slices.Equal(h.detached, pruned) {
		t.Errorf("pruned %v, detached %v", pruned, h.detached)
	}
	if _, ok := gw.routes.Peers["live"]; !ok || len(gw.routes.Peers) != 1 || len(gw.routes.RunSubnets) != 1 {
		t.Errorf("routes = %+v", gw.routes)
	}

	// A failing liveness read prunes nothing.
	gw.routes.Peers["gone"] = policyfile.Peer{IP: "10.90.0.3"}
	if p, err := m.PruneRuns(ctx, "acme", func(context.Context) (map[string]bool, error) {
		return nil, errors.New("store down")
	}); err == nil || len(p) != 0 {
		t.Errorf("prune on a failed liveness read = %v, %v", p, err)
	}
	// An empty table does not even ask; no gateway has nothing to prune.
	if p, err := NewManager(&fakeHost{gw: newFakeGateway(t), up: true}).PruneRuns(ctx, "acme", func(context.Context) (map[string]bool, error) {
		t.Error("asked for an empty table")
		return nil, nil
	}); err != nil || p != nil {
		t.Errorf("empty table = %v, %v", p, err)
	}
	if p, err := NewManager(&fakeHost{gw: gw}).PruneRuns(ctx, "acme", nil); err != nil || p != nil {
		t.Errorf("no gateway = %v, %v", p, err)
	}
}

func TestPruneRunsReportsGatewayFailures(t *testing.T) {
	ctx := context.Background()
	live := func(context.Context) (map[string]bool, error) { return nil, nil }
	if _, err := NewManager(&scriptedHost{controlErr: errors.New("boom")}).PruneRuns(ctx, "p", live); err == nil {
		t.Error("lookup failure swallowed")
	}
	refused, _ := gatewayAnswering(t, http.StatusInternalServerError, "no")
	if _, err := NewManager(&scriptedHost{ctl: refused, up: true}).PruneRuns(ctx, "p", live); err == nil {
		t.Error("table read failure swallowed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(policyfile.Routes{RunSubnets: map[string]string{"a": "x", "b": "y"}})
			return
		}
		if r.URL.Path == "/runs/a" {
			http.Error(w, "no", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	h := &scriptedHost{ctl: Control{BaseURL: srv.URL}, up: true, detachErr: errors.New("secret stuck")}
	pruned, err := NewManager(h).PruneRuns(ctx, "p", live)
	if err == nil || !slices.Equal(pruned, []string{"b"}) || h.detaches != 1 {
		t.Errorf("pruned %v, err %v, detaches %d (a refused detach keeps its link)", pruned, err, h.detaches)
	}
}
