package main

import (
	"bufio"
	"bytes"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerdictAllowMode(t *testing.T) {
	p := newPolicy(File{Mode: ModeAllow, Domains: []string{"github.com", "*.githubusercontent.com"}})
	ip := net.ParseIP("140.82.121.4")
	cases := []struct {
		host string
		want bool
	}{
		{"github.com", true},
		{"GitHub.com", true},                // case-insensitive
		{"raw.githubusercontent.com", true}, // wildcard
		{"githubusercontent.com", false},    // "*." does not match bare
		{"example.com", false},
		{"", false}, // no SNI in allow mode → deny
	}
	for _, c := range cases {
		if got := p.verdict(c.host, ip); got != c.want {
			t.Errorf("allow verdict(%q)=%v want %v", c.host, got, c.want)
		}
	}
}

func TestVerdictDenyMode(t *testing.T) {
	p := newPolicy(File{Mode: ModeDeny, Domains: []string{"example.com", "*.ads.net"}, DeniedCIDRs: []string{"10.0.0.0/8"}})
	pub := net.ParseIP("140.82.121.4")
	cases := []struct {
		host string
		ip   net.IP
		want bool
	}{
		{"github.com", pub, true},            // not on denylist → allow
		{"example.com", pub, false},          // denied host
		{"x.ads.net", pub, false},            // denied wildcard
		{"", pub, true},                      // no SNI, public IP → allow
		{"", net.ParseIP("10.1.2.3"), false}, // no SNI, denied CIDR → deny
	}
	for _, c := range cases {
		if got := p.verdict(c.host, c.ip); got != c.want {
			t.Errorf("deny verdict(%q,%v)=%v want %v", c.host, c.ip, got, c.want)
		}
	}
}

func TestForceBlockOverride(t *testing.T) {
	ip := net.ParseIP("140.82.121.4")
	// Allow mode: a base-allowed host is force-blocked by the override.
	pa := newPolicy(File{Mode: ModeAllow, Domains: []string{"github.com", "*.githubusercontent.com"},
		BlockedDomains: []string{"github.com", "*.evil.net"}})
	if pa.verdict("github.com", ip) {
		t.Error("blocked host must be denied even though it's on the allowlist")
	}
	if pa.verdict("x.evil.net", ip) {
		t.Error("blocked wildcard must be denied")
	}
	if !pa.verdict("raw.githubusercontent.com", ip) {
		t.Error("a non-blocked allowlisted host stays allowed")
	}
	// No-SNI to a resolved blocked name is denied.
	if pa.verdictNoSNI([]string{"github.com"}, ip) {
		t.Error("no-SNI to a blocked resolved name must be denied")
	}
	// Deny mode: block override wins there too (allow-all minus blocks).
	pd := newPolicy(File{Mode: ModeDeny, Domains: []string{"ads.com"}, BlockedDomains: []string{"tracker.com"}})
	if pd.verdict("tracker.com", ip) {
		t.Error("deny mode: force-blocked host must be denied")
	}
	if !pd.verdict("github.com", ip) {
		t.Error("deny mode: a non-denied, non-blocked host stays allowed")
	}
}

func TestAllowIPLiteral(t *testing.T) {
	// The user clicked "allow" on a bare-IP denial → the IP is in Domains.
	p := newPolicy(File{Mode: ModeAllow, Domains: []string{"github.com", "104.16.6.34"}})
	ip := net.ParseIP("104.16.6.34")
	if !p.ipAllowed(ip) {
		t.Error("literal IP in Domains must be allowed")
	}
	if !p.verdictNoSNI(nil, ip) {
		t.Error("no-SNI connection to an allowlisted IP must be allowed")
	}
	if !p.allows("104.16.6.34", nil) {
		t.Error("allows(IP) must be true so the denial gets pruned")
	}
	if p.allows("evil.com", net.ParseIP("9.9.9.9")) {
		t.Error("unrelated host/IP must stay denied")
	}
}

func TestReloadPrunesNowAllowedDenials(t *testing.T) {
	g := &gateway{denials: map[string]*Denial{}, denialsPath: t.TempDir() + "/d.json"}
	g.recordDenial("run1", "104.16.6.34")     // an IP denial
	g.recordDenial("run1", "blocked.example") // a host denial
	// Allow the IP: reload should drop that denial but keep the host one.
	g.pruneDenials(newPolicy(File{Mode: ModeAllow, Domains: []string{"104.16.6.34"}}))
	if _, ok := g.denials["run1\x00104.16.6.34"]; ok {
		t.Error("allowed IP denial was not pruned")
	}
	if _, ok := g.denials["run1\x00blocked.example"]; !ok {
		t.Error("unrelated denial must remain")
	}
}

func TestPeekSNI(t *testing.T) {
	hello := buildClientHello("test.example.com")
	br := bufio.NewReader(bytes.NewReader(hello))
	if got := peekSNI(br); got != "test.example.com" {
		t.Fatalf("peekSNI=%q want test.example.com", got)
	}
	// Peek must NOT consume: the bytes are still readable for the splice.
	if n := br.Buffered(); n < len(hello) {
		t.Fatalf("peek consumed bytes: buffered=%d want %d", n, len(hello))
	}
}

func TestPeekHTTPHost(t *testing.T) {
	req := []byte("GET / HTTP/1.1\r\nHost: api.example.com:80\r\n\r\n")
	br := bufio.NewReader(bytes.NewReader(req))
	if got := peekHTTPHost(br); got != "api.example.com" {
		t.Fatalf("peekHTTPHost=%q want api.example.com", got)
	}
}

// buildClientHello crafts a minimal TLS ClientHello with an SNI extension.
func buildClientHello(name string) []byte {
	sni := []byte(name)
	// server_name extension body: list len(2) + [type(1)=0 + name len(2) + name]
	ext := []byte{0x00, byte(len(sni) + 3), 0x00, byte(len(sni) >> 8), byte(len(sni))}
	ext = append(ext, sni...)
	// extension: type(2)=0x0000 + len(2) + body
	extBlock := []byte{0x00, 0x00, byte(len(ext) >> 8), byte(len(ext))}
	extBlock = append(extBlock, ext...)
	// handshake body: version(2)+random(32)+sid(1=0)+ciphers(2len+2)+comp(1len+1)+ext(2len+block)
	body := make([]byte, 2+32)
	body[0], body[1] = 0x03, 0x03
	body = append(body, 0x00)                   // session id len 0
	body = append(body, 0x00, 0x02, 0x00, 0x2f) // cipher suites: len 2 + one suite
	body = append(body, 0x01, 0x00)             // compression: len 1 + null
	body = append(body, byte(len(extBlock)>>8), byte(len(extBlock)))
	body = append(body, extBlock...)
	// handshake header: type(1)=0x01 + len(3)
	hs := []byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	hs = append(hs, body...)
	// record header: type(1)=0x16 + version(2) + len(2)
	rec := []byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}
	return append(rec, hs...)
}

func TestHostMatchingEdges(t *testing.T) {
	p := newPolicy(File{Mode: ModeAllow,
		Domains:        []string{" Good.Example ", "", "*.Wild.Example", "   "},
		BlockedDomains: []string{" Bad.Example ", "", "*.Blocked.Example"}})
	for _, c := range []struct {
		host string
		want bool
	}{
		{"good.example", true},
		{"GOOD.EXAMPLE", true},
		{"good.example.", true},
		{"sub.good.example", false}, // exact entries do not cover subdomains
		{"a.b.wild.example", true},
		{"wild.example", false},
		{"notwild.example", false},
		{"", false}, // a blank entry must not match "no host"
	} {
		if got := p.hostMatches(c.host); got != c.want {
			t.Errorf("hostMatches(%q) = %v, want %v", c.host, got, c.want)
		}
	}
	for _, c := range []struct {
		host string
		want bool
	}{
		{"bad.example", true},
		{"BAD.EXAMPLE.", true},
		{"x.blocked.example", true},
		{"blocked.example", false},
		{"", false},
		{".", false},
	} {
		if got := p.blockedHost(c.host); got != c.want {
			t.Errorf("blockedHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestVerdictWithoutAHost(t *testing.T) {
	listed, other := net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")
	allow := newPolicy(File{Mode: ModeAllow, Domains: []string{listed.String()}})
	if !allow.verdict("", listed) || allow.verdict("", other) {
		t.Error("allow mode: no host must pass only an allowlisted IP")
	}
	if !allow.verdict("unlisted.example", listed) {
		t.Error("allow mode: an allowlisted IP passes whatever name it is reached by")
	}
	if allow.verdict("", nil) {
		t.Error("allow mode: no host and no IP must be refused")
	}
	blocked := newPolicy(File{Mode: ModeDeny, BlockedDomains: []string{listed.String()}})
	if blocked.verdict("fine.example", listed) || blocked.verdict("", listed) {
		t.Error("a blocked IP is refused in deny mode too, named or not")
	}
	if !blocked.verdict("fine.example", other) {
		t.Error("deny mode must pass anything not listed")
	}
}

func TestVerdictNoSNI(t *testing.T) {
	ip := net.ParseIP("192.0.2.1")
	deniedIP := net.ParseIP("10.1.2.3")
	cases := []struct {
		name  string
		pol   File
		names []string
		ip    net.IP
		want  bool
	}{
		{"allow: listed name", File{Mode: ModeAllow, Domains: []string{"a.example"}}, []string{"x.example", "a.example"}, ip, true},
		{"allow: unlisted names", File{Mode: ModeAllow, Domains: []string{"a.example"}}, []string{"x.example"}, ip, false},
		{"allow: no names", File{Mode: ModeAllow, Domains: []string{"a.example"}}, nil, ip, false},
		{"allow: listed IP", File{Mode: ModeAllow, Domains: []string{ip.String()}}, nil, ip, true},
		{"allow: any blocked name wins", File{Mode: ModeAllow, Domains: []string{"a.example"}, BlockedDomains: []string{"x.example"}}, []string{"a.example", "x.example"}, ip, false},
		{"allow: blocked IP wins", File{Mode: ModeAllow, Domains: []string{"a.example"}, BlockedDomains: []string{ip.String()}}, []string{"a.example"}, ip, false},
		{"deny: no names", File{Mode: ModeDeny}, nil, ip, true},
		{"deny: unlisted name", File{Mode: ModeDeny, Domains: []string{"a.example"}}, []string{"x.example"}, ip, true},
		{"deny: any listed name", File{Mode: ModeDeny, Domains: []string{"a.example"}}, []string{"x.example", "a.example"}, ip, false},
		{"deny: denied CIDR", File{Mode: ModeDeny, DeniedCIDRs: []string{" 10.0.0.0/8 ", "not-a-cidr"}}, nil, deniedIP, false},
		{"deny: outside the denied CIDR", File{Mode: ModeDeny, DeniedCIDRs: []string{"10.0.0.0/8"}}, nil, ip, true},
		{"deny: blocked name", File{Mode: ModeDeny, BlockedDomains: []string{"*.x.example"}}, []string{"a.x.example"}, ip, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := newPolicy(c.pol).verdictNoSNI(c.names, c.ip); got != c.want {
				t.Errorf("verdictNoSNI(%v, %v) = %v, want %v", c.names, c.ip, got, c.want)
			}
		})
	}
}

func TestAllowsDecidesRecordedDenials(t *testing.T) {
	ip := net.ParseIP("192.0.2.1")
	p := newPolicy(File{Mode: ModeAllow, Domains: []string{"a.example", ip.String()}})
	for _, c := range []struct {
		host string
		dst  net.IP
		want bool
	}{
		{"a.example", nil, true},
		{"b.example", nil, false},
		{ip.String(), nil, true},  // a recorded IP denial, now allowed
		{"192.0.2.2", nil, false}, // another IP
		{"", ip, true},            // no name: judged by the destination
		{"", net.ParseIP("192.0.2.3"), false},
	} {
		if got := p.allows(c.host, c.dst); got != c.want {
			t.Errorf("allows(%q, %v) = %v, want %v", c.host, c.dst, got, c.want)
		}
	}
}

// "Force-block overrides win in both modes", and allows is what prunes a
// denial: an IP on both lists is refused on the wire, so its denial must stay.
func TestAllowsHonoursABlockedIP(t *testing.T) {
	ip := "192.0.2.1"
	p := newPolicy(File{Mode: ModeAllow, Domains: []string{ip}, BlockedDomains: []string{ip}})
	if p.allows(ip, nil) {
		t.Fatal("allows must agree with the verdict: a blocked IP is refused")
	}
}

func TestLoadPolicy(t *testing.T) {
	t.Run("a missing file is deny-all", func(t *testing.T) {
		g := newTestGateway(t)
		if err := g.loadPolicy(); err != nil {
			t.Fatal(err)
		}
		if g.pol.Load().verdict("any.example", net.ParseIP("192.0.2.1")) {
			t.Fatal("no policy file must fail closed")
		}
	})
	t.Run("no path at all is deny-all", func(t *testing.T) {
		g := newTestGateway(t)
		g.policyPath = ""
		if err := g.loadPolicy(); err != nil {
			t.Fatal(err)
		}
		if g.pol.Load().verdict("any.example", nil) {
			t.Fatal("no policy path must fail closed")
		}
	})
	t.Run("a corrupt file is an error and the old policy stays", func(t *testing.T) {
		g := newTestGateway(t)
		if err := g.installPolicy(File{Mode: ModeAllow, Domains: []string{"a.example"}}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(g.policyPath, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := g.loadPolicy(); err == nil {
			t.Fatal("a corrupt policy must not load")
		}
		if !g.pol.Load().verdict("a.example", nil) || g.pol.Load().verdict("b.example", nil) {
			t.Fatal("the previous policy must stay in force")
		}
	})
	t.Run("an unknown mode is allow mode, the closed one", func(t *testing.T) {
		g := newTestGateway(t)
		if err := os.WriteFile(g.policyPath, []byte(`{"mode":"open","domains":["a.example"]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := g.loadPolicy(); err != nil {
			t.Fatal(err)
		}
		if g.pol.Load().verdict("b.example", nil) {
			t.Fatal("an unknown mode must not open the gateway")
		}
	})
}

// A policy that nft refuses is reported, so the engine knows.
func TestInstallPolicyReportsAnNFTFailure(t *testing.T) {
	g := newTestGateway(t)
	applyNFT = func(string) error { return errors.New("nft: syntax error") }
	if err := g.installPolicy(File{Mode: ModeAllow}); err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("installPolicy = %v", err)
	}
}

func TestInstallPolicyLogsTheDiffAndPrunes(t *testing.T) {
	g := newTestGateway(t)
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	if err := g.installPolicy(File{Mode: ModeAllow, Domains: []string{"Old.example", "keep.example"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "loaded mode=allow domains=2") {
		t.Errorf("first load log = %q", buf.String())
	}
	g.recordDenial("run-1", "new.example")
	g.recordDenial("run-1", "other.example")
	buf.Reset()
	if err := g.installPolicy(File{Mode: ModeAllow, Domains: []string{"KEEP.example", "new.example"}}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{"+allowed [new.example]", "-removed [Old.example]", "cleared denials now allowed: [new.example]"} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q lacks %q", got, want)
		}
	}
	// The prune is durable: the engine reads this file.
	var df DenialsFile
	if err := readJSON(g.denialsPath, &df); err != nil {
		t.Fatal(err)
	}
	if len(df.Denials) != 1 || df.Denials[0].Host != "other.example" {
		t.Errorf("denials file = %+v, want only other.example", df.Denials)
	}
}

func TestDiffDomains(t *testing.T) {
	if got := diffDomains([]string{"A.example"}, []string{"a.example", "B.example"}); len(got) != 1 || got[0] != "B.example" {
		t.Errorf("diffDomains = %v", got)
	}
	if got := diffDomains([]string{"a.example"}, nil); got != nil {
		t.Errorf("diffDomains = %v", got)
	}
}

func TestRecordDenialCapsDistinctPairs(t *testing.T) {
	g := newTestGateway(t)
	for i := range 500 {
		g.recordDenial("run-1", "h"+itoa(i)+".example")
	}
	g.recordDenial("run-1", "one-too-many.example")
	if len(g.denials) != 500 {
		t.Fatalf("denials = %d, want the cap of 500", len(g.denials))
	}
	if _, ok := g.denials["run-1\x00one-too-many.example"]; ok {
		t.Fatal("a pair past the cap was recorded")
	}
	// A pair already tracked keeps counting at the cap.
	g.recordDenial("run-1", "h0.example")
	if d := g.denials["run-1\x00h0.example"]; d.Count != 2 {
		t.Fatalf("count = %d, want 2", d.Count)
	}
}

func TestRetainDenialsDropsDeadRuns(t *testing.T) {
	g := newTestGateway(t)
	g.recordDenial("live", "a.example")
	g.recordDenial("dead", "a.example")
	g.denialsDirty = false
	g.retainDenials(map[string]bool{"live": true})
	if _, ok := g.denials["dead\x00a.example"]; ok || len(g.denials) != 1 {
		t.Fatalf("denials = %+v", g.denials)
	}
	if !g.denialsDirty {
		t.Fatal("an eviction must be flushed")
	}
}

func TestFlushDenials(t *testing.T) {
	t.Run("nothing dirty, nothing written", func(t *testing.T) {
		g := newTestGateway(t)
		g.flushDenials()
		if _, err := os.Stat(g.denialsPath); !os.IsNotExist(err) {
			t.Fatalf("stat = %v, want no file", err)
		}
	})
	t.Run("no path keeps it in memory", func(t *testing.T) {
		g := newTestGateway(t)
		g.denialsPath = ""
		g.recordDenial("run-1", "a.example")
		g.flushDenials()
		if g.denialsDirty || len(g.denials) != 1 {
			t.Fatalf("dirty=%v denials=%+v", g.denialsDirty, g.denials)
		}
	})
	t.Run("an unwritable path is survivable", func(t *testing.T) {
		g := newTestGateway(t)
		g.denialsPath = filepath.Join(g.denialsPath, "no", "such", "dir")
		g.recordDenial("run-1", "a.example")
		g.flushDenials()
		if len(g.denials) != 1 {
			t.Fatal("the in-memory record must survive")
		}
	})
}

func TestItoa(t *testing.T) {
	for in, want := range map[int]string{0: "0", 7: "7", 9040: "9040", 65535: "65535"} {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q", in, got)
		}
	}
}
