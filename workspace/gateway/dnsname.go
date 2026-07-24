package main

import (
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"time"
)

// The gateway forwards every inner-container DNS query (dns.go), so it can snoop
// the answers to learn which hostnames a destination IP was resolved from. That
// mapping is how a connection with NO readable SNI/Host (Encrypted Client Hello,
// IP-literal dials, oversized ClientHellos) is still judged by hostname instead
// of being blindly blocked by IP.

type dnsHit struct {
	name string
	exp  time.Time
}

type dnsCache struct {
	mu   sync.Mutex
	byIP map[string][]dnsHit // dst IP string -> resolved names (with expiry)
}

func newDNSCache() *dnsCache { return &dnsCache{byIP: map[string][]dnsHit{}} }

// remember records that name resolved to each ip, kept for ttl (bounded).
func (c *dnsCache) remember(name string, ips []net.IP, ttl time.Duration) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "" || len(ips) == 0 {
		return
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	if ttl > time.Hour {
		ttl = time.Hour
	}
	exp := time.Now().Add(ttl)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.byIP) > 8192 { // crude cap: drop everything rather than grow unbounded
		c.byIP = map[string][]dnsHit{}
	}
	for _, ip := range ips {
		k := ip.String()
		hits := c.byIP[k]
		found := false
		for i := range hits {
			if hits[i].name == name {
				hits[i].exp = exp
				found = true
				break
			}
		}
		if !found {
			hits = append(hits, dnsHit{name: name, exp: exp})
		}
		c.byIP[k] = hits
	}
}

// names returns the non-expired hostnames the ip was resolved from.
func (c *dnsCache) names(ip net.IP) []string {
	k := ip.String()
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	hits := c.byIP[k]
	if len(hits) == 0 {
		return nil
	}
	var out []string
	kept := hits[:0]
	for _, h := range hits {
		if h.exp.After(now) {
			out = append(out, h.name)
			kept = append(kept, h)
		}
	}
	if len(kept) == 0 {
		delete(c.byIP, k)
	} else {
		c.byIP[k] = kept
	}
	return out
}

// snoop parses a DNS response and records name→IP mappings. Best-effort: a parse
// failure just means no mapping is learned for that answer.
func (c *dnsCache) snoop(msg []byte) {
	name, ips, ttl := parseDNSAnswers(msg)
	c.remember(name, ips, ttl)
}

// parseDNSAnswers extracts the (first) question name and the A/AAAA answer IPs
// from a DNS message, with the smallest answer TTL. Hand-rolled (pure stdlib).
func parseDNSAnswers(m []byte) (qname string, ips []net.IP, ttl time.Duration) {
	if len(m) < 12 {
		return "", nil, 0
	}
	qd := int(binary.BigEndian.Uint16(m[4:6]))
	an := int(binary.BigEndian.Uint16(m[6:8]))
	off := 12
	for i := 0; i < qd; i++ {
		n, no, ok := readName(m, off)
		if !ok {
			return "", nil, 0
		}
		if i == 0 {
			qname = n
		}
		off = no + 4 // + QTYPE(2) + QCLASS(2)
	}
	var minTTL uint32
	for i := 0; i < an && off+10 <= len(m); i++ {
		_, no, ok := skipName(m, off)
		if !ok {
			break
		}
		off = no
		if off+10 > len(m) {
			break
		}
		typ := binary.BigEndian.Uint16(m[off:])
		rttl := binary.BigEndian.Uint32(m[off+4:])
		rdlen := int(binary.BigEndian.Uint16(m[off+8:]))
		off += 10
		if off+rdlen > len(m) {
			break
		}
		switch {
		case typ == 1 && rdlen == 4: // A
			ips = append(ips, net.IP(append([]byte(nil), m[off:off+4]...)))
		case typ == 28 && rdlen == 16: // AAAA
			ips = append(ips, net.IP(append([]byte(nil), m[off:off+16]...)))
		}
		if (typ == 1 || typ == 28) && (minTTL == 0 || rttl < minTTL) {
			minTTL = rttl
		}
		off += rdlen
	}
	if minTTL == 0 {
		minTTL = 300
	}
	return qname, ips, time.Duration(minTTL) * time.Second
}

// readName reads a (possibly compressed) DNS name starting at off, returning the
// dotted name and the offset just past the name in the CURRENT record (for
// pointers, that's off+2). Bounded against malformed/looping input.
func readName(m []byte, off int) (string, int, bool) {
	var labels []string
	next := -1
	jumps := 0
	for {
		if off < 0 || off >= len(m) {
			return "", 0, false
		}
		b := int(m[off])
		if b == 0 {
			off++
			break
		}
		if b&0xC0 == 0xC0 { // compression pointer
			if off+1 >= len(m) {
				return "", 0, false
			}
			if next < 0 {
				next = off + 2
			}
			off = int(b&0x3F)<<8 | int(m[off+1])
			if jumps++; jumps > 64 {
				return "", 0, false
			}
			continue
		}
		if off+1+b > len(m) {
			return "", 0, false
		}
		labels = append(labels, string(m[off+1:off+1+b]))
		off += 1 + b
	}
	if next < 0 {
		next = off
	}
	return strings.Join(labels, "."), next, true
}

func skipName(m []byte, off int) (string, int, bool) {
	return readName(m, off)
}
