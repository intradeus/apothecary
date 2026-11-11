package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

// A run being provisioned may reach anything but what the project blocks:
// the force-blocked domains always, the denylist in deny mode.
func TestVerdictOpen(t *testing.T) {
	ip := net.ParseIP("203.0.113.7").To4()
	allow := newPolicy(File{Mode: ModeAllow, Domains: []string{"good.example"}, BlockedDomains: []string{"blocked.example", "*.ads.example", "198.51.100.1"}})
	deny := newPolicy(File{Mode: ModeDeny, Domains: []string{"bad.example"}, BlockedDomains: []string{"blocked.example"}})
	cases := []struct {
		name  string
		pol   *policy
		host  string
		names []string
		ip    net.IP
		want  bool
	}{
		{"allow mode: an unlisted host passes", allow, "registry.npmjs.org", nil, ip, true},
		{"allow mode: a blocked host does not", allow, "blocked.example", nil, ip, false},
		{"allow mode: a blocked wildcard does not", allow, "x.ads.example", nil, ip, false},
		{"allow mode: a blocked IP does not", allow, "fine.example", nil, net.ParseIP("198.51.100.1"), false},
		{"allow mode: no name, never resolved, passes", allow, "", nil, ip, true},
		{"allow mode: no name, resolved from a blocked one", allow, "", []string{"blocked.example"}, ip, false},
		{"deny mode: the denylist still holds", deny, "bad.example", nil, ip, false},
		{"deny mode: a blocked host does not pass", deny, "blocked.example", nil, ip, false},
		{"deny mode: the rest passes", deny, "fine.example", nil, ip, true},
		{"deny mode: no name, resolved from a denied one", deny, "", []string{"bad.example"}, ip, false},
	}
	for _, c := range cases {
		if got := c.pol.verdictOpen(c.host, c.names, c.ip); got != c.want {
			t.Errorf("%s: verdictOpen = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHandleLetsAnOpenRunThroughButNotToWhatIsBlocked(t *testing.T) {
	dstIP := net.ParseIP("203.0.113.7").To4()
	pol := File{Mode: ModeAllow, Domains: []string{"good.example"}, BlockedDomains: []string{"blocked.example"}}
	for _, c := range []struct {
		host      string
		open      bool
		wantAllow bool
	}{
		{"registry.npmjs.org", true, true},
		{"blocked.example", true, false},
		{"registry.npmjs.org", false, false}, // the window closed: back to the allowlist
	} {
		g := newTestGateway(t)
		until := time.Now().Add(time.Hour)
		if !c.open {
			until = time.Now().Add(-time.Second)
		}
		g.applyRoutes(Routes{RunSubnets: map[string]string{"run-1": "127.0.0.0/8"}, Open: map[string]time.Time{"run-1": until}})
		if err := g.installPolicy(pol); err != nil {
			t.Fatal(err)
		}
		ix := newInterception(t, &net.TCPAddr{IP: dstIP, Port: 443})
		got := exchange(t, g, buildClientHello(c.host))
		allowed := ix.dialed.Load() == 1 && string(got) == upstreamReply
		if allowed != c.wantAllow {
			t.Errorf("%s (open %v): allowed = %v, want %v", c.host, c.open, allowed, c.wantAllow)
		}
	}
}

// What only the window let through is marked so on the trail — the engine
// allows those hosts on the project; what the policy allows anyway is not.
func TestHandleMarksWhatOnlyTheWindowLetThrough(t *testing.T) {
	dstIP := net.ParseIP("203.0.113.7").To4()
	for _, c := range []struct {
		host string
		mode Mode
		want bool
	}{
		{"registry.npmjs.org", ModeAllow, true},
		{"good.example", ModeAllow, false}, // on the allowlist already
		{"registry.npmjs.org", ModeDeny, false},
	} {
		g := newTestGateway(t)
		g.applyRoutes(Routes{RunSubnets: map[string]string{"run-1": "127.0.0.0/8"}, Open: map[string]time.Time{"run-1": time.Now().Add(time.Hour)}})
		if err := g.installPolicy(File{Mode: c.mode, Domains: []string{"good.example"}}); err != nil {
			t.Fatal(err)
		}
		newInterception(t, &net.TCPAddr{IP: dstIP, Port: 443})
		_ = exchange(t, g, buildClientHello(c.host))
		recs := g.audit.snapshot("run-1")
		if len(recs) != 1 || recs[0].Verdict != "allow" || recs[0].Provisioning != c.want {
			t.Errorf("%s (%s): records = %+v, want one allow marked %v", c.host, c.mode, recs, c.want)
		}
	}
}

func TestControlOpensAndClosesARunsWindow(t *testing.T) {
	g := newTestGateway(t)
	var rendered []string
	applyNFT = func(s string) error { rendered = append(rendered, s); return nil }
	h := g.controlHandler("t")
	if rec := do(t, h, "PUT", "/policy", "t", `{"mode":"allow","domains":["github.com"]}`); rec.Code != 204 {
		t.Fatalf("PUT /policy: %d", rec.Code)
	}
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if rec := do(t, h, "PUT", "/runs/run-1", "t", `{"subnet":"10.1.0.0/24","peer":{"public_key":"k","ip":"10.90.0.2"}}`); rec.Code != 204 {
		t.Fatalf("PUT /runs/run-1: %d %s", rec.Code, rec.Body)
	}
	if g.isOpen("run-1") {
		t.Fatal("attaching a run opened its window")
	}
	if rec := do(t, h, "PUT", "/runs/run-1/open", "t", `{"until":"`+until+`"}`); rec.Code != 204 {
		t.Fatalf("PUT /runs/run-1/open: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "PUT", "/runs/run-1/open", "t", `nope`); rec.Code != 400 {
		t.Errorf("a malformed window: %d, want 400", rec.Code)
	}
	if !g.isOpen("run-1") {
		t.Fatal("the run's window is not open after the route opened it")
	}
	if last := rendered[len(rendered)-1]; !strings.Contains(last, `ip saddr { 10.1.0.0/24, 10.90.0.2 } accept`) {
		t.Errorf("nft does not accept the open run's raw egress:\n%s", last)
	}
	var onDisk Routes
	if err := readJSON(g.routesPath, &onDisk); err != nil || onDisk.Open["run-1"].IsZero() {
		t.Errorf("the window is not persisted: %+v %v", onDisk, err)
	}

	if rec := do(t, h, "DELETE", "/runs/run-1/open", "t", ""); rec.Code != 204 {
		t.Fatalf("DELETE /runs/run-1/open: %d", rec.Code)
	}
	if g.isOpen("run-1") {
		t.Error("the window is still open after it was closed")
	}
	if last := rendered[len(rendered)-1]; strings.Contains(last, "saddr") {
		t.Errorf("nft still accepts the closed run's raw egress:\n%s", last)
	}
	if g.attributeIP(net.ParseIP("10.1.0.5")) != "run-1" {
		t.Error("closing the window detached the run")
	}

	// Re-registering a route keeps its window; detaching drops it.
	_ = do(t, h, "PUT", "/runs/run-2", "t", `{"subnet":"10.2.0.0/24"}`)
	_ = do(t, h, "PUT", "/runs/run-2/open", "t", `{"until":"`+until+`"}`)
	_ = do(t, h, "PUT", "/runs/run-2", "t", `{"subnet":"10.2.0.0/24"}`)
	if !g.isOpen("run-2") {
		t.Error("re-registering the route closed the window")
	}
	_ = do(t, h, "PUT", "/runs/run-3", "t", `{"subnet":"10.3.0.0/24"}`)
	_ = do(t, h, "PUT", "/runs/run-3/open", "t", `{"until":"`+until+`"}`)
	_ = do(t, h, "DELETE", "/runs/run-3", "t", "")
	if g.isOpen("run-3") || len(g.currentRoutes().Open) != 1 {
		t.Errorf("detaching kept the window: %+v", g.currentRoutes().Open)
	}
	// A deadline already past opens nothing.
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	_ = do(t, h, "PUT", "/runs/run-4", "t", `{"subnet":"10.4.0.0/24"}`)
	_ = do(t, h, "PUT", "/runs/run-4/open", "t", `{"until":"`+past+`"}`)
	if g.isOpen("run-4") {
		t.Error("a past deadline opened the window")
	}
	if g.isOpen("") {
		t.Error("an unattributed connection reads as open")
	}
}

func TestOpenWindowsExpire(t *testing.T) {
	g := newTestGateway(t)
	if err := g.installPolicy(File{Mode: ModeAllow}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	g.expireOpen(now) // no routes yet: nothing to do
	rt := Routes{RunSubnets: map[string]string{"a": "10.1.0.0/24", "b": "10.2.0.0/24"},
		Open: map[string]time.Time{"a": now.Add(time.Minute), "b": now.Add(time.Hour)}}
	if err := writeAtomic(g.routesPath, rt); err != nil {
		t.Fatal(err)
	}
	g.applyRoutes(rt)
	g.expireOpen(now) // nothing due
	if len(g.currentRoutes().Open) != 2 {
		t.Fatalf("a window closed early: %+v", g.currentRoutes().Open)
	}
	later := now.Add(2 * time.Minute)
	if got := g.openSources(later); len(got) != 1 || got[0] != "10.2.0.0/24" {
		t.Errorf("open sources past a's deadline = %v", got)
	}
	g.expireOpen(later)
	var onDisk Routes
	if err := readJSON(g.routesPath, &onDisk); err != nil {
		t.Fatal(err)
	}
	if _, ok := onDisk.Open["a"]; ok || len(onDisk.Open) != 1 {
		t.Errorf("expired window still persisted: %+v", onDisk.Open)
	}
	if g.attributeIP(net.ParseIP("10.1.0.9")) != "a" {
		t.Error("expiring the window detached the run")
	}
}

func TestOpenSourcesWithoutRoutes(t *testing.T) {
	g := newTestGateway(t)
	if g.openSources(time.Now()) != nil || g.isOpen("x") {
		t.Error("a gateway with no routes has open sources")
	}
	// Before a policy is known, an open-set change renders nothing.
	calls := 0
	applyNFT = func(string) error { calls++; return nil }
	g.applyRoutes(Routes{RunSubnets: map[string]string{"a": "10.1.0.0/24"}, Open: map[string]time.Time{"a": time.Now().Add(time.Hour)}})
	if calls != 0 {
		t.Errorf("nft applied %d times before a policy", calls)
	}
}

func TestRenderNFTOpenRunsInAllowModeOnly(t *testing.T) {
	open := []string{"10.1.0.0/24", "10.90.0.2"}
	allow := renderNFT("eth0", 9040, File{Mode: ModeAllow}, []*net.IPNet{{IP: net.IPv4(169, 254, 0, 0), Mask: net.CIDRMask(16, 32)}}, 0, open)
	acc := strings.Index(allow, `ip saddr { 10.1.0.0/24, 10.90.0.2 } accept`)
	prot := strings.Index(allow, `ip daddr { 169.254.0.0/16 } drop`)
	if acc < 0 || prot < 0 || prot > acc {
		t.Errorf("the open accept must come after the protected drop:\n%s", allow)
	}
	if deny := renderNFT("eth0", 9040, File{Mode: ModeDeny}, nil, 0, open); strings.Contains(deny, "saddr") {
		t.Errorf("deny mode accepts everything already; no source rule expected:\n%s", deny)
	}
}
