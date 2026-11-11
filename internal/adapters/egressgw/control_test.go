package egressgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/egressgw/policyfile"
)

// scriptedHost is a Host whose answers are the test's.
type scriptedHost struct {
	ctl       Control
	up        bool
	ensureErr error
	attachErr error
	detachErr error
	att       Attachment
}

func (h *scriptedHost) EnsureGateway(context.Context, string, string) (Control, error) {
	return h.ctl, h.ensureErr
}
func (h *scriptedHost) Control(context.Context, string) (Control, bool) { return h.ctl, h.up }
func (h *scriptedHost) AttachRun(context.Context, string, string, string) (Attachment, error) {
	return h.att, h.attachErr
}
func (h *scriptedHost) DetachRun(context.Context, string, string) error { return h.detachErr }

// gatewayAnswering is a control API answering every request the same way.
func gatewayAnswering(t *testing.T, status int, body string) (Control, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return Control{BaseURL: srv.URL + "/", Token: "tok"}, &hits
}

func TestEnsureRunEgressStopsAtTheFirstFailure(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")

	if _, err := NewManager(&scriptedHost{ensureErr: boom}).EnsureRunEgress(ctx, "p", "", "r", policyfile.File{}); !errors.Is(err, boom) {
		t.Errorf("gateway failure = %v", err)
	}

	ctl, hits := gatewayAnswering(t, http.StatusOK, "{}")
	if _, err := NewManager(&scriptedHost{ctl: ctl, attachErr: boom}).EnsureRunEgress(ctx, "p", "", "r", policyfile.File{}); !errors.Is(err, boom) {
		t.Errorf("attach failure = %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("a run that never attached reached the gateway %d times", hits.Load())
	}

	// A sandbox must not start on a gateway that has no policy for it: both
	// control calls are fatal, and they say which one failed.
	refused, _ := gatewayAnswering(t, http.StatusInternalServerError, "disk full")
	_, err := NewManager(&scriptedHost{ctl: refused}).EnsureRunEgress(ctx, "p", "", "r", policyfile.File{})
	if err == nil || !strings.Contains(err.Error(), "register run route") || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("route failure = %v", err)
	}

	policyDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/policy" {
			http.Error(w, "no", http.StatusForbidden)
		}
	}))
	defer policyDown.Close()
	_, err = NewManager(&scriptedHost{ctl: Control{BaseURL: policyDown.URL}}).EnsureRunEgress(ctx, "p", "", "r", policyfile.File{})
	if err == nil || !strings.Contains(err.Error(), "install policy") {
		t.Errorf("policy failure = %v", err)
	}
}

// A run name is user-derived and lands in a URL path: it must stay one segment.
func TestRunNamesAreEscapedInControlPaths(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"denials":[]}`)
		}
	}))
	defer srv.Close()
	m := NewManager(&scriptedHost{ctl: Control{BaseURL: srv.URL}, up: true})
	ctx := context.Background()
	if _, err := m.EnsureRunEgress(ctx, "p", "", "a/../b c", policyfile.File{}); err != nil {
		t.Fatal(err)
	}
	if err := m.DestroyRunEgress(ctx, "p", "a/../b c"); err != nil {
		t.Fatal(err)
	}
	if paths[0] != "PUT /runs/a%2F..%2Fb%20c" || paths[len(paths)-1] != "DELETE /runs/a%2F..%2Fb%20c" {
		t.Errorf("paths = %v", paths)
	}
}

// Detaching goes on even when the gateway is unreachable: the run's network
// must not leak because the gateway is down, and the host's own failure is
// the one reported.
func TestDestroyRunEgressSurvivesTheGateway(t *testing.T) {
	ctx := context.Background()
	down, _ := gatewayAnswering(t, http.StatusBadGateway, "")
	if err := NewManager(&scriptedHost{ctl: down, up: true}).DestroyRunEgress(ctx, "p", "r"); err != nil {
		t.Errorf("gateway down = %v", err)
	}
	if err := NewManager(&scriptedHost{}).DestroyRunEgress(ctx, "p", "r"); err != nil {
		t.Errorf("no gateway = %v", err)
	}
	boom := errors.New("boom")
	if err := NewManager(&scriptedHost{detachErr: boom}).DestroyRunEgress(ctx, "p", "r"); !errors.Is(err, boom) {
		t.Errorf("detach failure = %v", err)
	}
}

// A failure reading the policy must not read as "nothing is allowed", and a
// policy from a gateway that says nothing about its mode is the secure one.
func TestPolicyReads(t *testing.T) {
	ctx := context.Background()
	bad, _ := gatewayAnswering(t, http.StatusUnauthorized, "bad token")
	if f, err := NewManager(&scriptedHost{ctl: bad, up: true}).Policy(ctx, "p"); err == nil || f.Mode != "" {
		t.Errorf("refused read = %+v, %v", f, err)
	}
	garbled, _ := gatewayAnswering(t, http.StatusOK, "{nope")
	if _, err := NewManager(&scriptedHost{ctl: garbled, up: true}).Policy(ctx, "p"); err == nil {
		t.Error("a garbled policy read as a policy")
	}
	modeless, _ := gatewayAnswering(t, http.StatusOK, `{"domains":["x.io"]}`)
	f, err := NewManager(&scriptedHost{ctl: modeless, up: true}).Policy(ctx, "p")
	if err != nil || f.Mode != policyfile.ModeAllow || f.Domains[0] != "x.io" {
		t.Errorf("modeless = %+v, %v", f, err)
	}
}

func TestSetRuleFailuresLeaveThePolicyAlone(t *testing.T) {
	ctx := context.Background()
	// Unreadable policy: nothing is written back over it.
	var puts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
		}
		http.Error(w, "no", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	m := NewManager(&scriptedHost{ctl: Control{BaseURL: srv.URL}, up: true})
	if err := m.SetRule(ctx, "p", "x.io", true); err == nil {
		t.Error("SetRule over an unreadable policy succeeded")
	}
	if puts.Load() != 0 {
		t.Errorf("wrote a policy built from nothing (%d PUTs)", puts.Load())
	}
	// The write failing is the caller's to hear.
	wr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		io.WriteString(w, `{"mode":"allow"}`)
	}))
	defer wr.Close()
	if err := NewManager(&scriptedHost{ctl: Control{BaseURL: wr.URL}, up: true}).Allow(ctx, "p", "x.io"); err == nil {
		t.Error("a refused PUT reported success")
	}
}

// Block wins in both modes; the edits are idempotent and case-blind.
func TestApplyRuleEdges(t *testing.T) {
	f := policyfile.File{Mode: policyfile.ModeDeny, Domains: []string{"Evil.Example"}}
	f = applyRule(f, "evil.example", false)
	if len(f.Domains) != 0 || !contains(f.BlockedDomains, "evil.example") {
		t.Fatalf("deny-mode block = %+v", f)
	}
	f = applyRule(f, "evil.example", false)
	if len(f.BlockedDomains) != 1 {
		t.Fatalf("second block duplicated: %+v", f)
	}
	f = applyRule(f, "EVIL.example", true)
	if len(f.BlockedDomains) != 0 || len(f.Domains) != 0 {
		t.Fatalf("deny-mode allow = %+v", f)
	}
	a := policyfile.File{Mode: policyfile.ModeAllow, Domains: []string{"x.io"}}
	a = applyRule(a, "X.IO", true)
	if len(a.Domains) != 1 {
		t.Fatalf("allow duplicated: %+v", a)
	}
	// remove never writes into the caller's slice: the policy may be shared.
	orig := []string{"a", "b", "c"}
	if got := remove(orig, "b"); len(got) != 2 || orig[1] != "b" {
		t.Errorf("remove = %v, orig = %v", got, orig)
	}
}

func TestDenialsSwallowFailuresByDesign(t *testing.T) {
	bad, _ := gatewayAnswering(t, http.StatusInternalServerError, "")
	if d := NewManager(&scriptedHost{ctl: bad, up: true}).Denials(context.Background(), "p", "r"); d != nil {
		t.Errorf("Denials = %+v", d)
	}
}

func TestDenialsEscapeTheRunQuery(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("run")
		json.NewEncoder(w).Encode(policyfile.DenialsFile{})
	}))
	defer srv.Close()
	NewManager(&scriptedHost{ctl: Control{BaseURL: srv.URL}, up: true}).Denials(context.Background(), "p", "a&run=b")
	if q != "a&run=b" {
		t.Errorf("run query = %q: a name must not smuggle a second parameter", q)
	}
}

func TestAuditMissesAreNotCached(t *testing.T) {
	ctx := context.Background()
	if r := NewManager(&scriptedHost{}).Audit(ctx, "p"); r != nil {
		t.Errorf("no gateway = %+v", r)
	}
	bad, hits := gatewayAnswering(t, http.StatusInternalServerError, "")
	m := NewManager(&scriptedHost{ctl: bad, up: true})
	if r := m.Audit(ctx, "p"); r != nil {
		t.Errorf("failed audit = %+v", r)
	}
	m.Audit(ctx, "p")
	if hits.Load() != 2 {
		t.Errorf("a failure was cached: %d requests", hits.Load())
	}
}

// The cache is per project: one project's trail is never another's.
func TestAuditCacheIsPerProjectAndExpires(t *testing.T) {
	ctx := context.Background()
	body := `{"records":[{"run":"r","host":"h"}]}`
	good, hits := gatewayAnswering(t, http.StatusOK, body)
	m := NewManager(&scriptedHost{ctl: good, up: true})
	if r := m.Audit(ctx, "a"); len(r) != 1 {
		t.Fatalf("audit = %+v", r)
	}
	m.Audit(ctx, "a")
	if hits.Load() != 1 {
		t.Errorf("within the second: %d requests", hits.Load())
	}
	m.Audit(ctx, "b")
	if hits.Load() != 2 {
		t.Errorf("project b served from a's snapshot: %d requests", hits.Load())
	}
	m.auditMu.Lock()
	s := m.auditCache["a"]
	s.at = time.Now().Add(-2 * time.Second)
	m.auditCache["a"] = s
	m.auditMu.Unlock()
	m.Audit(ctx, "a")
	if hits.Load() != 3 {
		t.Errorf("a stale snapshot was reused: %d requests", hits.Load())
	}
}

func TestBuildCacheFailures(t *testing.T) {
	ctx := context.Background()
	bad, _ := gatewayAnswering(t, http.StatusInternalServerError, "no")
	m := NewManager(&scriptedHost{ctl: bad, up: true})
	if n, err := m.BuildCacheSize(ctx, "p"); err == nil || n != 0 {
		t.Errorf("BuildCacheSize = %d, %v", n, err)
	}
	if err := m.ClearBuildCache(ctx, "p"); err == nil {
		t.Error("a refused clear reported success")
	}
	if err := NewManager(&scriptedHost{}).ClearBuildCache(ctx, "p"); !errors.Is(err, ErrNoGateway) {
		t.Errorf("no gateway = %v", err)
	}
}

func TestCallRequestShape(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer srv.Close()
	m := NewManager(&scriptedHost{})
	ctl := Control{BaseURL: srv.URL + "/", Token: "s3cret"}
	if err := m.call(context.Background(), ctl, http.MethodPut, "/policy", map[string]string{"a": "b"}, nil); err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/policy" || got.Header.Get("Authorization") != "Bearer s3cret" ||
		got.Header.Get("Content-Type") != "application/json" || body != `{"a":"b"}` {
		t.Errorf("request = %s %v %q", got.URL.Path, got.Header, body)
	}
	// No body, no content type; nothing to decode, nothing decoded.
	if err := m.call(context.Background(), ctl, http.MethodDelete, "/buildcache", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("Content-Type") != "" {
		t.Errorf("content type without a body: %v", got.Header)
	}
}

func TestCallFailures(t *testing.T) {
	m := NewManager(&scriptedHost{})
	ctx := context.Background()
	if err := m.call(ctx, Control{BaseURL: "http://127.0.0.1:1"}, http.MethodPut, "/x", make(chan int), nil); err == nil {
		t.Error("unmarshalable body sent")
	}
	if err := m.call(ctx, Control{BaseURL: "http://bad host"}, http.MethodGet, "/x", nil, nil); err == nil {
		t.Error("bad base URL accepted")
	}
	if err := m.call(ctx, Control{BaseURL: "http://127.0.0.1:1", Token: "s3cret"}, http.MethodGet, "/x", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "egress gateway GET /x") || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("connection failure = %v (must name the call, never the token)", err)
	}
	// A long error body is cut.
	long, _ := gatewayAnswering(t, http.StatusBadRequest, strings.Repeat("x", 4096))
	err := m.call(ctx, long, http.MethodGet, "/x", nil, nil)
	if err == nil || len(err.Error()) > 1200 {
		t.Errorf("error length = %d", len(err.Error()))
	}
}

func TestWaitHealthy(t *testing.T) {
	ctx := context.Background()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("probed %s", r.URL.Path)
		}
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	if err := WaitHealthy(ctx, nil, srv.URL+"/", 10*time.Second); err != nil {
		t.Fatalf("eventually healthy = %v", err)
	}
	if n.Load() != 3 {
		t.Errorf("probes = %d", n.Load())
	}

	// Never healthy: the limit ends it, naming the address and the last reason.
	sick, _ := gatewayAnswering(t, http.StatusTeapot, "")
	err := WaitHealthy(ctx, &http.Client{Timeout: time.Second}, sick.BaseURL, 0)
	if err == nil || !strings.Contains(err.Error(), "did not become healthy") || !strings.Contains(err.Error(), "418") {
		t.Errorf("sick = %v", err)
	}

	// The caller's context ends the wait.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := WaitHealthy(cctx, nil, sick.BaseURL, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled = %v", err)
	}

	if err := WaitHealthy(ctx, nil, "http://bad host", time.Second); err == nil {
		t.Error("bad URL waited on")
	}
}

func TestGalleryURL(t *testing.T) {
	if GalleryURL("10.0.0.1") != "http://10.0.0.1:5002" {
		t.Errorf("GalleryURL = %q", GalleryURL("10.0.0.1"))
	}
}

// A provisioning run's window opens and closes on the gateway, the run's name
// escaped like every other control path; a project without a gateway has
// nothing to open (an error the provisioner logs) and nothing to close.
func TestOpenAndCloseRunEgress(t *testing.T) {
	var got []string
	var body policyfile.RunOpen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.EscapedPath())
		if r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	ctx := context.Background()
	m := NewManager(&scriptedHost{ctl: Control{BaseURL: srv.URL}, up: true})
	until := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := m.OpenRunEgress(ctx, "p", "a b", until); err != nil {
		t.Fatal(err)
	}
	if err := m.CloseRunEgress(ctx, "p", "a b"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "PUT /runs/a%20b/open" || got[1] != "DELETE /runs/a%20b/open" || !body.Until.Equal(until) {
		t.Errorf("calls = %v body = %+v", got, body)
	}

	none := NewManager(&scriptedHost{})
	if err := none.OpenRunEgress(ctx, "p", "r", until); !errors.Is(err, ErrNoGateway) {
		t.Errorf("open without a gateway = %v", err)
	}
	if err := none.CloseRunEgress(ctx, "p", "r"); err != nil {
		t.Errorf("close without a gateway = %v", err)
	}
	down, _ := gatewayAnswering(t, http.StatusBadGateway, "")
	failing := NewManager(&scriptedHost{ctl: down, up: true})
	if err := failing.OpenRunEgress(ctx, "p", "r", until); err == nil || !strings.Contains(err.Error(), "open run egress") {
		t.Errorf("open on a failing gateway = %v", err)
	}
	if err := failing.CloseRunEgress(ctx, "p", "r"); err == nil || !strings.Contains(err.Error(), "close run egress") {
		t.Errorf("close on a failing gateway = %v", err)
	}
}
