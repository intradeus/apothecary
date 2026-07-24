package main

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// buildDNSResponse crafts a minimal DNS response for name with one A answer.
func buildDNSResponse(name string, ip net.IP, ttl uint32) []byte {
	var b []byte
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint16(hdr[4:], 1) // QDCOUNT
	binary.BigEndian.PutUint16(hdr[6:], 1) // ANCOUNT
	b = append(b, hdr...)
	// question name
	qstart := len(b)
	for _, lbl := range splitDots(name) {
		b = append(b, byte(len(lbl)))
		b = append(b, lbl...)
	}
	b = append(b, 0)
	b = append(b, 0, 1, 0, 1) // QTYPE=A, QCLASS=IN
	// answer: name = compression pointer to the question name
	b = append(b, 0xC0, byte(qstart))
	b = append(b, 0, 1, 0, 1) // TYPE=A CLASS=IN
	tb := make([]byte, 4)
	binary.BigEndian.PutUint32(tb, ttl)
	b = append(b, tb...)
	b = append(b, 0, 4)        // RDLENGTH=4
	b = append(b, ip.To4()...) // RDATA
	return b
}

func splitDots(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '.' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func TestParseDNSAnswers(t *testing.T) {
	ip := net.IPv4(104, 16, 6, 34)
	msg := buildDNSResponse("openvsx.eclipsecontent.org", ip, 120)
	name, ips, ttl := parseDNSAnswers(msg)
	if name != "openvsx.eclipsecontent.org" {
		t.Errorf("name = %q", name)
	}
	if len(ips) != 1 || !ips[0].Equal(ip) {
		t.Errorf("ips = %v", ips)
	}
	if ttl != 120*time.Second {
		t.Errorf("ttl = %v", ttl)
	}
}

func TestDNSCacheAndNoSNIVerdict(t *testing.T) {
	c := newDNSCache()
	ip := net.IPv4(104, 16, 6, 34)
	c.snoop(buildDNSResponse("raw.githubusercontent.com", ip, 300))

	got := c.names(ip)
	if len(got) != 1 || got[0] != "raw.githubusercontent.com" {
		t.Fatalf("names = %v", got)
	}

	// A no-SNI connection to that IP is allowed because a resolved name matches
	// the allowlist wildcard.
	p := newPolicy(File{Mode: ModeAllow, Domains: []string{"*.githubusercontent.com"}})
	if !p.verdictNoSNI(c.names(ip), ip) {
		t.Error("no-SNI connection to a DNS-resolved allowlisted host must be allowed")
	}
	// An IP with no known resolution stays denied in allow mode.
	if p.verdictNoSNI(nil, net.IPv4(1, 2, 3, 4)) {
		t.Error("no-SNI connection to an unknown IP must be denied in allow mode")
	}
	// Deny mode: unknown IP allowed; denied name blocks it.
	pd := newPolicy(File{Mode: ModeDeny, Domains: []string{"bad.com"}})
	if !pd.verdictNoSNI(nil, net.IPv4(1, 2, 3, 4)) {
		t.Error("deny mode: unknown no-SNI IP should be allowed")
	}
	c.snoop(buildDNSResponse("bad.com", net.IPv4(9, 9, 9, 9), 300))
	if pd.verdictNoSNI(c.names(net.IPv4(9, 9, 9, 9)), net.IPv4(9, 9, 9, 9)) {
		t.Error("deny mode: no-SNI IP resolved from a denied name must be blocked")
	}
}

func TestDNSCacheExpiry(t *testing.T) {
	c := newDNSCache()
	ip := net.IPv4(10, 0, 0, 1)
	c.mu.Lock()
	c.byIP[ip.String()] = []dnsHit{{name: "stale.com", exp: time.Now().Add(-time.Minute)}}
	c.mu.Unlock()
	if got := c.names(ip); got != nil {
		t.Errorf("expired entry returned: %v", got)
	}
}
