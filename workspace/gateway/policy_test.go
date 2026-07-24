package main

import (
	"bufio"
	"bytes"
	"net"
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
