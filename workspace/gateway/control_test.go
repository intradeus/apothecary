package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestGateway is a gateway with state on disk and nft stubbed out.
func newTestGateway(t *testing.T) *gateway {
	t.Helper()
	dir := t.TempDir()
	ext := "eth0"
	g := &gateway{
		denials:       map[string]*Denial{},
		dns:           newDNSCache(),
		audit:         newAuditor(filepath.Join(dir, "egress-audit.json")),
		policyPath:    filepath.Join(dir, "egress-policy.json"),
		routesPath:    filepath.Join(dir, "routes.json"),
		denialsPath:   filepath.Join(dir, "egress-denials.json"),
		buildCacheDir: filepath.Join(dir, "buildcache"),
	}
	g.ext.Store(&ext)
	orig := applyNFT
	applyNFT = func(string) error { return nil }
	t.Cleanup(func() { applyNFT = orig })
	return g
}

func do(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestControlRequiresToken(t *testing.T) {
	g := newTestGateway(t)
	h := g.controlHandler("s3cret")
	if rec := do(t, h, "GET", "/healthz", "", ""); rec.Code != 200 {
		t.Fatalf("healthz must be open: %d", rec.Code)
	}
	for _, tok := range []string{"", "wrong"} {
		if rec := do(t, h, "GET", "/policy", tok, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: got %d, want 401", tok, rec.Code)
		}
	}
	if rec := do(t, h, "GET", "/policy", "s3cret", ""); rec.Code != 200 {
		t.Fatalf("authorized GET /policy: %d", rec.Code)
	}
}

func TestControlPolicyAppliesAndPersists(t *testing.T) {
	g := newTestGateway(t)
	h := g.controlHandler("t")
	rec := do(t, h, "PUT", "/policy", "t", `{"mode":"allow","domains":["github.com"]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT /policy: %d %s", rec.Code, rec.Body)
	}
	if !g.pol.Load().allows("github.com", nil) || g.pol.Load().allows("evil.example", nil) {
		t.Fatal("policy not live after PUT")
	}
	var onDisk File
	if err := readJSON(g.policyPath, &onDisk); err != nil || len(onDisk.Domains) != 1 {
		t.Fatalf("policy not persisted: %+v %v", onDisk, err)
	}
	var got File
	_ = json.Unmarshal(do(t, h, "GET", "/policy", "t", "").Body.Bytes(), &got)
	if got.Mode != ModeAllow || len(got.Domains) != 1 || got.Domains[0] != "github.com" {
		t.Fatalf("GET /policy = %+v", got)
	}
}

func TestControlRunsAttachDetach(t *testing.T) {
	g := newTestGateway(t)
	h := g.controlHandler("t")
	if rec := do(t, h, "PUT", "/runs/r1", "t", `{"subnet":"10.90.0.2/32","peer":{"public_key":"K1","ip":"10.90.0.2"}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("attach: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "PUT", "/runs/r2", "t", `{"subnet":"172.20.0.0/16"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("attach r2: %d", rec.Code)
	}
	if got := g.attributeIP(net.ParseIP("10.90.0.2")); got != "r1" {
		t.Fatalf("attribution r1 = %q", got)
	}
	if got := g.attributeIP(net.ParseIP("172.20.0.5")); got != "r2" {
		t.Fatalf("attribution r2 = %q", got)
	}
	// A docker run: its network's subnet AND its tunnel address are its own.
	if rec := do(t, h, "PUT", "/runs/r4", "t", `{"subnet":"172.21.0.0/16","peer":{"public_key":"K4","ip":"10.90.0.3"}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("attach r4: %d", rec.Code)
	}
	if a, b := g.attributeIP(net.ParseIP("172.21.0.3")), g.attributeIP(net.ParseIP("10.90.0.3")); a != "r4" || b != "r4" {
		t.Fatalf("docker run attribution = %q (network), %q (tunnel)", a, b)
	}
	do(t, h, "DELETE", "/runs/r4", "t", "")
	var rt Routes
	_ = json.Unmarshal(do(t, h, "GET", "/routes", "t", "").Body.Bytes(), &rt)
	if rt.Peers["r1"].PublicKey != "K1" || len(rt.RunSubnets) != 2 {
		t.Fatalf("routes = %+v", rt)
	}
	if rec := do(t, h, "DELETE", "/runs/r1", "t", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("detach: %d", rec.Code)
	}
	if got := g.attributeIP(net.ParseIP("10.90.0.2")); got != "" {
		t.Fatalf("detached run still attributed: %q", got)
	}
	if rec := do(t, h, "PUT", "/runs/r3", "t", `{"subnet":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad subnet accepted: %d", rec.Code)
	}
}

func TestControlDenialsAndAuditFilterByRun(t *testing.T) {
	g := newTestGateway(t)
	h := g.controlHandler("t")
	g.recordDenial("r1", "a.example")
	g.recordDenial("r2", "b.example")
	g.audit.record("r1", "a.example", "1.2.3.4", 443, "tls", "deny", 0, 0)
	g.audit.record("r2", "b.example", "1.2.3.5", 443, "tls", "allow", 10, 20)
	var df DenialsFile
	_ = json.Unmarshal(do(t, h, "GET", "/denials?run=r1", "t", "").Body.Bytes(), &df)
	if len(df.Denials) != 1 || df.Denials[0].Host != "a.example" {
		t.Fatalf("denials = %+v", df)
	}
	var af AuditFile
	_ = json.Unmarshal(do(t, h, "GET", "/audit?run=r2", "t", "").Body.Bytes(), &af)
	if len(af.Records) != 1 || af.Records[0].BytesIn != 20 {
		t.Fatalf("audit = %+v", af)
	}
	_ = json.Unmarshal(do(t, h, "GET", "/audit", "t", "").Body.Bytes(), &af)
	if len(af.Records) != 2 {
		t.Fatalf("unfiltered audit = %d records", len(af.Records))
	}
}

func TestControlBuildCacheSizeAndClear(t *testing.T) {
	g := newTestGateway(t)
	h := g.controlHandler("t")
	blob := filepath.Join(g.buildCacheDir, "docker", "blob")
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	var bc BuildCacheInfo
	_ = json.Unmarshal(do(t, h, "GET", "/buildcache", "t", "").Body.Bytes(), &bc)
	if bc.Bytes != 1000 {
		t.Fatalf("size = %d", bc.Bytes)
	}
	if rec := do(t, h, "DELETE", "/buildcache", "t", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(blob); !os.IsNotExist(err) {
		t.Fatalf("blob survived the wipe: %v", err)
	}
}

func TestReconcilePeers(t *testing.T) {
	var calls []string
	l := &wgLink{run: func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if strings.Join(args, " ") == "show wg0 peers" {
			return "KEEP\nSTALE\n", nil
		}
		return "", nil
	}}
	l.reconcilePeers(map[string]Peer{
		"r1": {PublicKey: "KEEP", IP: "10.90.0.2"},
		"r2": {PublicKey: "NEW", IP: "10.90.0.3"},
	})
	want := []string{
		"wg show wg0 peers",
		"wg set wg0 peer KEEP allowed-ips 10.90.0.2/32",
		"wg set wg0 peer NEW allowed-ips 10.90.0.3/32",
		"wg set wg0 peer STALE remove",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestRenderNFTProtectsCIDRsBeforeModeAccepts(t *testing.T) {
	nets, err := parseCIDRs("10.42.0.0/16, 169.254.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{ModeAllow, ModeDeny} {
		out := renderNFT("eth0", 9040, File{Mode: mode, AllowedCIDRs: []string{"10.0.0.0/8"}}, nets, 0, nil)
		drop := strings.Index(out, `ip daddr { 10.42.0.0/16, 169.254.0.0/16 } drop`)
		if drop < 0 {
			t.Fatalf("%s: protect rule missing:\n%s", mode, out)
		}
		if acc := strings.Index(out, `iifname != "eth0" accept`); acc >= 0 && acc < drop {
			t.Fatalf("%s: a mode accept precedes the protect drop:\n%s", mode, out)
		}
		if acc := strings.Index(out, `{ 10.0.0.0/8 } accept`); acc >= 0 && acc < drop {
			t.Fatalf("%s: allowed CIDR precedes the protect drop:\n%s", mode, out)
		}
	}
	if out := renderNFT("eth0", 9040, File{}, nil, 0, nil); strings.Contains(out, "daddr {  }") {
		t.Fatal("empty protect set rendered")
	}
}

func TestProtected(t *testing.T) {
	g := &gateway{}
	g.protect, _ = parseCIDRs("10.43.0.0/16,169.254.169.254/32")
	if !g.protected(net.ParseIP("10.43.0.1")) || !g.protected(net.ParseIP("169.254.169.254")) {
		t.Fatal("protected ranges not matched")
	}
	if g.protected(net.ParseIP("140.82.112.3")) {
		t.Fatal("public address treated as protected")
	}
	if _, err := parseCIDRs("10.0.0.0/33"); err == nil {
		t.Fatal("bad CIDR accepted")
	}
}

func TestRenderNFTClosesTheControlPortToRuns(t *testing.T) {
	out := renderNFT("eth0", 9040, File{}, nil, 9041, nil)
	if !strings.Contains(out, `add rule ip apo input iifname != { "eth0", "lo" } tcp dport 9041 drop`) {
		t.Fatalf("control port open to run links:\n%s", out)
	}
	if strings.Contains(renderNFT("eth0", 9040, File{}, nil, 0, nil), "input") {
		t.Fatal("input chain rendered with no control API")
	}
}

func TestResolvConfUpstream(t *testing.T) {
	p := filepath.Join(t.TempDir(), "resolv.conf")
	os.WriteFile(p, []byte("search ws.svc.cluster.local\nnameserver 10.43.0.10\noptions ndots:5\n"), 0o644)
	if got, err := resolvConfUpstream(p); err != nil || got != "10.43.0.10:53" {
		t.Fatalf("got %q %v", got, err)
	}
	os.WriteFile(p, []byte("search x\n"), 0o644)
	if _, err := resolvConfUpstream(p); err == nil {
		t.Fatal("no nameserver accepted")
	}
}
