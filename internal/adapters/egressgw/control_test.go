package egressgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestLinkSandboxNeedsALinkerAndAPeer(t *testing.T) {
	ctx := context.Background()
	// A host that links itself: nothing to do.
	if err := NewManager(&scriptedHost{}).LinkSandbox(ctx, Attachment{Peer: &policyfile.Peer{}}, "s"); err != nil {
		t.Errorf("non-linker = %v", err)
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

// --- DockerHost failure paths ---------------------------------------------

func TestDockerErrorsNeverCarrySecrets(t *testing.T) {
	fd := &fakeDocker{fail: "run -d"}
	h, _ := newDockerFixture(t, fd)
	_, err := h.EnsureGateway(context.Background(), "acme", "")
	if err == nil {
		t.Fatal("failed run reported success")
	}
	tok, _ := h.token("acme", false)
	wg, _, _ := h.gatewayKey("acme")
	if strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), wg) {
		t.Errorf("secret in an error that reaches logs and run events: %v", err)
	}
	if !strings.Contains(err.Error(), "APO_GW_TOKEN=…") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestRedactEnvLeavesOtherArgsAlone(t *testing.T) {
	in := []string{"-e", "A=secret", "-e", "NOVALUE", "--label", "b=c", "-e"}
	got := redactEnv(in)
	want := []string{"-e", "A=…", "-e", "NOVALUE", "--label", "b=c", "-e"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("redactEnv = %v", got)
	}
	if in[1] != "A=secret" {
		t.Error("redactEnv wrote into its input")
	}
}

func TestDockerRunnerErrorPropagates(t *testing.T) {
	boom := errors.New("no docker")
	h := NewDockerHost(t.TempDir(), func(context.Context, []string) ([]byte, []byte, int, error) {
		return nil, nil, 0, boom
	}, testImageFn)
	if _, err := h.docker(context.Background(), "ps"); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestEnsureGatewayFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("image build", func(t *testing.T) {
		boom := errors.New("build failed")
		h := NewDockerHost(t.TempDir(), (&fakeDocker{}).run, func(context.Context) (string, error) { return "", boom })
		if _, err := h.EnsureGateway(ctx, "acme", ""); !errors.Is(err, boom) || !strings.Contains(err.Error(), "build gateway image") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("token dir", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "plain")
		os.WriteFile(file, nil, 0o600)
		h := NewDockerHost(file, (&fakeDocker{}).run, testImageFn)
		if _, err := h.EnsureGateway(ctx, "acme", ""); err == nil {
			t.Error("a runs dir that cannot hold the token was accepted")
		}
	})
	t.Run("corrupt wireguard key", func(t *testing.T) {
		fd := &fakeDocker{}
		h, _ := newDockerFixture(t, fd)
		if err := os.WriteFile(h.gwKeyPath("acme"), []byte("not base64!\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := h.EnsureGateway(ctx, "acme", ""); err == nil || !strings.Contains(err.Error(), "wireguard key") {
			t.Errorf("err = %v", err)
		}
		if fd.count("run", "-d") != 0 {
			t.Error("a gateway was started with no usable identity")
		}
	})
	stages := []struct {
		name string
		fd   *fakeDocker
		fail string
	}{
		{"listing the gateway", &fakeDocker{}, "ps -aq"},
		{"starting a kept gateway", &fakeDocker{gatewayHas: true}, "start apo-gw-acme"},
		{"removing a stale gateway", &fakeDocker{gatewayHas: true, gatewayImg: "old"}, "rm -f"},
		{"creating a volume", &fakeDocker{}, "volume create"},
		{"re-attaching a run", &fakeDocker{gatewayHas: true, gatewayImg: "old", netIPs: "apo-egress-r1 172.20.0.2\n"}, "connect --ip"},
	}
	for _, c := range stages {
		t.Run(c.name, func(t *testing.T) {
			c.fd.fail = c.fail
			h, _ := newDockerFixture(t, c.fd)
			if _, err := h.EnsureGateway(ctx, "acme", ""); err == nil {
				t.Errorf("failure at %q went unreported", c.fail)
			}
		})
	}
}

// A volume that already exists is the normal case of a replaced gateway.
func TestEnsureGatewayToleratesExistingVolumes(t *testing.T) {
	fd := &volumeExists{}
	h, _ := newDockerFixture(t, &fd.fakeDocker)
	h.run = fd.run
	if _, err := h.EnsureGateway(context.Background(), "acme", ""); err != nil {
		t.Fatalf("err = %v", err)
	}
}

type volumeExists struct{ fakeDocker }

func (f *volumeExists) run(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	if args[0] == "volume" {
		return nil, []byte("volume already exists"), 1, nil
	}
	return f.fakeDocker.run(ctx, args)
}

// A gateway not answering /healthz fails its ensure; the address is not
// cached, so the next call looks again.
func TestControlReadyFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("no loopback port", func(t *testing.T) {
		fd := &fakeDocker{gatewayHas: true}
		h, _ := newDockerFixture(t, fd)
		fd.ctlAddr = "0.0.0.0:9"
		cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		_, err := h.controlReady(cctx, "acme", "tok")
		if err == nil {
			t.Fatal("no address reported ready")
		}
	})
	t.Run("port never published", func(t *testing.T) {
		fd := &fakeDocker{}
		h, _ := newDockerFixture(t, fd)
		_, err := h.publishedControl(ctx, "acme")
		if err == nil {
			t.Fatal("a missing gateway has a control port")
		}
	})
	t.Run("publishes only public addresses", func(t *testing.T) {
		fd := &fakeDocker{gatewayHas: true}
		h, _ := newDockerFixture(t, fd)
		fd.ctlAddr = "0.0.0.0:49153"
		if _, err := h.publishedControl(ctx, "acme"); err == nil || !strings.Contains(err.Error(), "no loopback control port") {
			t.Fatalf("a non-loopback control port was accepted: %v", err)
		}
	})
	t.Run("unhealthy", func(t *testing.T) {
		fd := &fakeDocker{gatewayHas: true}
		h, gw := newDockerFixture(t, fd)
		gw.srv.Close()
		cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		if _, err := h.controlReady(cctx, "acme", "tok"); err == nil {
			t.Fatal("a dead gateway reported ready")
		}
		h.mu.Lock()
		_, cached := h.ctl["acme"]
		h.mu.Unlock()
		if cached {
			t.Error("a dead gateway's address was cached")
		}
	})
}

func TestControlCacheHasATTL(t *testing.T) {
	fd := &fakeDocker{gatewayHas: true}
	h, _ := newDockerFixture(t, fd)
	ctx := context.Background()
	if _, ok := h.Control(ctx, "acme"); !ok {
		t.Fatal("no control")
	}
	before := fd.count("port")
	if _, ok := h.Control(ctx, "acme"); !ok || fd.count("port") != before {
		t.Errorf("fresh entry not reused: %d lookups", fd.count("port")-before)
	}
	h.mu.Lock()
	e := h.ctl["acme"]
	e.at = time.Now().Add(-controlTTL - time.Second)
	h.ctl["acme"] = e
	h.mu.Unlock()
	if _, ok := h.Control(ctx, "acme"); !ok || fd.count("port") != before+1 {
		t.Errorf("stale entry trusted: %d lookups", fd.count("port")-before)
	}
}

func TestTokenIsPrivateAndStable(t *testing.T) {
	h := NewDockerHost(filepath.Join(t.TempDir(), "runs"), (&fakeDocker{}).run, testImageFn)
	if _, err := h.token("acme", false); err == nil {
		t.Error("no token yet, but one was found")
	}
	a, err := h.token("acme", true)
	if err != nil || len(a) != 64 {
		t.Fatalf("token = %q, %v", a, err)
	}
	if b, _ := h.token("acme", true); b != a {
		t.Error("the token changed between calls")
	}
	if info, _ := os.Stat(h.tokenPath("acme")); info.Mode().Perm() != 0o600 {
		t.Errorf("token mode = %v", info.Mode())
	}
	if info, _ := os.Stat(h.gwDir("acme")); info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", info.Mode())
	}
	// A blank file is no token.
	os.WriteFile(h.tokenPath("acme"), []byte(" \n"), 0o600)
	if _, err := h.token("acme", false); err == nil {
		t.Error("a blank token file read as a token")
	}
	if b, err := h.token("acme", true); err != nil || b == "" || b == a {
		t.Errorf("regenerated = %q, %v", b, err)
	}
	if other, _ := h.token("beta", true); other == a {
		t.Error("two projects share a token")
	}
}

func TestAttachRunFailures(t *testing.T) {
	ctx := context.Background()
	for _, fail := range []string{"network create", "network connect", "network inspect", "NetworkSettings"} {
		fd := &fakeDocker{gatewayHas: true, fail: fail}
		h, _ := newDockerFixture(t, fd)
		if _, err := h.AttachRun(ctx, "acme", "", "r1"); err == nil {
			t.Errorf("failure at %q went unreported", fail)
		}
	}
	// Re-attaching is idempotent: "already exists" is not a failure.
	fd := &existingNet{}
	h, _ := newDockerFixture(t, &fd.fakeDocker)
	h.run = fd.run
	att, err := h.AttachRun(ctx, "acme", "", "r1")
	if err != nil || att.GatewayIP != "172.20.0.2" {
		t.Errorf("re-attach = %+v, %v", att, err)
	}
}

type existingNet struct{ fakeDocker }

func (f *existingNet) run(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	if args[0] == "network" && (args[1] == "create" || args[1] == "connect") {
		return nil, []byte("network with name already exists"), 1, nil
	}
	return f.fakeDocker.run(ctx, args)
}

// A gateway with no address on the run's network would leave the sandbox
// routing into nothing: it is an error.
func TestAttachRunNeedsAGatewayAddress(t *testing.T) {
	fd := &noAddr{}
	h, _ := newDockerFixture(t, &fd.fakeDocker)
	h.run = fd.run
	_, err := h.AttachRun(context.Background(), "acme", "", "r1")
	if err == nil || !strings.Contains(err.Error(), "no IP on apo-egress-r1") {
		t.Errorf("err = %v", err)
	}
}

type noAddr struct{ fakeDocker }

func (f *noAddr) run(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	if args[0] == "inspect" {
		return []byte("\n"), nil, 0, nil
	}
	return f.fakeDocker.run(ctx, args)
}

func TestGatewayNetIPsIsBestEffort(t *testing.T) {
	fd := &fakeDocker{fail: "$v.IPAddress"}
	h, _ := newDockerFixture(t, fd)
	if ips := h.gatewayNetIPs(context.Background(), "acme"); ips != nil {
		t.Errorf("failed inspect = %v", ips)
	}
	fd = &fakeDocker{netIPs: "bridge 172.17.0.2\napo-egress-a 10.0.0.2\napo-egress-b \n\nnoip\n"}
	h, _ = newDockerFixture(t, fd)
	ips := h.gatewayNetIPs(context.Background(), "acme")
	if len(ips) != 1 || ips["apo-egress-a"] != "10.0.0.2" {
		t.Errorf("ips = %v", ips)
	}
}

func TestPublicKeyRejectsBadKeys(t *testing.T) {
	for name, k := range map[string]string{"not base64": "%%%", "short": b64("short")} {
		if _, err := publicKey(k); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	priv, pub, err := newKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := publicKey(priv); err != nil || got != pub {
		t.Errorf("publicKey = %q, %v; want %q", got, err, pub)
	}
}

func TestGatewayKeyPersists(t *testing.T) {
	h := NewDockerHost(t.TempDir(), (&fakeDocker{}).run, testImageFn)
	p1, pub1, err := h.gatewayKey("acme")
	if err != nil {
		t.Fatal(err)
	}
	p2, pub2, err := h.gatewayKey("acme")
	if err != nil || p1 != p2 || pub1 != pub2 {
		t.Errorf("key changed: %v", err)
	}
	if info, _ := os.Stat(h.gwKeyPath("acme")); info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v", info.Mode())
	}
	file := filepath.Join(t.TempDir(), "plain")
	os.WriteFile(file, nil, 0o600)
	if _, _, err := NewDockerHost(file, nil, nil).gatewayKey("acme"); err == nil {
		t.Error("a key that cannot be stored was returned")
	}
}

// A run name cannot climb out of the peers directory.
func TestPeerPathStaysInThePeersDir(t *testing.T) {
	h := NewDockerHost(t.TempDir(), (&fakeDocker{}).run, testImageFn)
	if got := h.peerPath("acme", "../../evil"); filepath.Dir(got) != h.peersDir("acme") {
		t.Errorf("peer path = %q", got)
	}
}

func TestRunPeerEdges(t *testing.T) {
	h := NewDockerHost(t.TempDir(), (&fakeDocker{}).run, testImageFn)
	a, err := h.runPeer("acme", "r1")
	if err != nil || a.IP != "10.90.0.2" {
		t.Fatalf("first = %+v, %v", a, err)
	}
	if info, _ := os.Stat(h.peerPath("acme", "r1")); info.Mode().Perm() != 0o600 {
		t.Errorf("a file holding a private key is %v", info.Mode())
	}
	// A corrupt identity is replaced, never used.
	os.WriteFile(h.peerPath("acme", "r1"), []byte("{nope"), 0o600)
	b, err := h.runPeer("acme", "r1")
	if err != nil || b.PrivateKey == "" || b.PrivateKey == a.PrivateKey {
		t.Fatalf("after corruption = %+v, %v", b, err)
	}
	// Peers of another project do not take addresses here.
	if c, _ := h.runPeer("beta", "r9"); c.IP != "10.90.0.2" {
		t.Errorf("other project = %+v", c)
	}
	// Storing the identity failing is an error, not a key nobody can find again.
	file := filepath.Join(t.TempDir(), "plain")
	os.WriteFile(file, nil, 0o600)
	if _, err := NewDockerHost(file, nil, nil).runPeer("acme", "r1"); err == nil {
		t.Error("an unstored identity was returned")
	}
}

func TestWithPeerSurfacesKeyFailures(t *testing.T) {
	h := NewDockerHost(t.TempDir(), (&fakeDocker{}).run, testImageFn)
	if _, _, err := h.gatewayKey("acme"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(h.gwKeyPath("acme"), []byte("%%%\n"), 0o600)
	if _, err := h.withPeer("acme", "r1", Attachment{GatewayIP: "1.2.3.4"}); err == nil {
		t.Error("a bad gateway key was attached")
	}
	file := filepath.Join(t.TempDir(), "plain")
	os.WriteFile(file, nil, 0o600)
	blocked := NewDockerHost(t.TempDir(), nil, nil)
	if _, _, err := blocked.gatewayKey("acme"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(blocked.peersDir("acme"), nil, 0o600) // a file where the dir goes
	if _, err := blocked.withPeer("acme", "r1", Attachment{GatewayIP: "1.2.3.4"}); err == nil {
		t.Error("an unstored peer was attached")
	}
}

func TestLinkSandboxFailures(t *testing.T) {
	ctx := context.Background()
	fd := &fakeDocker{}
	h, _ := newDockerFixture(t, fd)
	if err := h.LinkSandbox(ctx, Attachment{}, "s"); err == nil || !strings.Contains(err.Error(), "no WireGuard identity") {
		t.Errorf("no identity = %v", err)
	}
	if fd.count("run") != 0 {
		t.Error("a link ran with no identity")
	}
	att := Attachment{Peer: &policyfile.Peer{IP: "10.90.0.2"}, WGPrivateKey: "k"}
	if err := h.LinkSandbox(ctx, Attachment{Peer: att.Peer}, "s"); err == nil {
		t.Error("a peer with no private key linked")
	}
	boom := errors.New("build failed")
	broken := NewDockerHost(t.TempDir(), fd.run, func(context.Context) (string, error) { return "", boom })
	if err := broken.LinkSandbox(ctx, att, "s"); !errors.Is(err, boom) {
		t.Errorf("image failure = %v", err)
	}
}

func TestDetachForgetsTheIdentity(t *testing.T) {
	h := NewDockerHost(t.TempDir(), (&fakeDocker{}).run, testImageFn)
	if _, err := h.runPeer("acme", "r1"); err != nil {
		t.Fatal(err)
	}
	if err := h.DetachRun(context.Background(), "acme", "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.peerPath("acme", "r1")); !os.IsNotExist(err) {
		t.Errorf("a detached run's private key stayed on disk: %v", err)
	}
}

func TestProjectLabels(t *testing.T) {
	if got := strings.Join(projectLabels("id", ""), " "); got != "--label apothecary=true --label apothecary.project=id" {
		t.Errorf("no name = %q", got)
	}
	if got := strings.Join(projectLabels("id", "My App"), " "); !strings.HasSuffix(got, "--label apothecary.project.name=My App") {
		t.Errorf("named = %q", got)
	}
}

func TestNewDockerManagerDrivesADockerHost(t *testing.T) {
	m := NewDockerManager(t.TempDir(), (&fakeDocker{}).run, testImageFn)
	if _, ok := m.host.(*DockerHost); !ok {
		t.Fatalf("host = %T", m.host)
	}
	if _, ok := m.host.(SandboxLinker); !ok {
		t.Error("a docker host must link its sandboxes")
	}
}

func TestControlNeedsItsToken(t *testing.T) {
	h := NewDockerHost(t.TempDir(), (&fakeDocker{gatewayHas: true, ctlAddr: "127.0.0.1:9"}).run, testImageFn)
	if _, ok := h.Control(context.Background(), "acme"); ok {
		t.Error("control found with no token on disk")
	}
}

// Not knowing what a gateway runs is not evidence that it is stale. The
// comment at EnsureGateway is the rule: an `rm -f` severs every attached
// run's egress, so a failed inspect must not lead to one.
func TestEnsureGatewayFailedInspectKeepsABusyGateway(t *testing.T) {
	fd := &fakeDocker{gatewayHas: true, running: "sbx1\n", fail: ".Config.Image"}
	h, _ := newDockerFixture(t, fd)
	_, _ = h.EnsureGateway(context.Background(), "acme", "")
	if fd.count("rm") != 0 {
		t.Errorf("a gateway serving live runs was removed: %v", fd.calls)
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
