package main

import (
	"net"
	"strings"
)

// policy is an immutable snapshot of the matching rules, built from a File and
// swapped atomically on reload. Domain matching (exact + "*.suffix") is lifted
// verbatim from internal/egressproxy; the two-mode verdict is the new part.
type policy struct {
	mode        Mode
	exact       map[string]bool
	wildcards   []string        // ".suffix" forms
	allowedIPs  map[string]bool // literal IPs in Domains (so "allow this IP" works)
	blockExact  map[string]bool // force-deny overrides (win in both modes)
	blockWild   []string
	blockedIPs  map[string]bool
	deniedCIDRs []*net.IPNet
}

func newPolicy(f File) *policy {
	p := &policy{mode: f.normMode(), exact: map[string]bool{}, allowedIPs: map[string]bool{},
		blockExact: map[string]bool{}, blockedIPs: map[string]bool{}}
	for _, h := range f.Domains {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(h, "*."); ok {
			p.wildcards = append(p.wildcards, "."+rest)
			continue
		}
		if ip := net.ParseIP(h); ip != nil { // a bare IP (user allowlisted an IP denial)
			p.allowedIPs[ip.String()] = true
		}
		p.exact[h] = true
	}
	for _, h := range f.BlockedDomains {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(h, "*."); ok {
			p.blockWild = append(p.blockWild, "."+rest)
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			p.blockedIPs[ip.String()] = true
		}
		p.blockExact[h] = true
	}
	for _, c := range f.DeniedCIDRs {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			p.deniedCIDRs = append(p.deniedCIDRs, n)
		}
	}
	return p
}

// blockedHost reports whether the host is force-denied by an override.
func (p *policy) blockedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	if p.blockExact[host] {
		return true
	}
	for _, w := range p.blockWild {
		if strings.HasSuffix(host, w) {
			return true
		}
	}
	return false
}

func (p *policy) blockedIP(ip net.IP) bool { return ip != nil && p.blockedIPs[ip.String()] }

// hostMatches reports whether host (no port) matches an exact or wildcard entry.
func (p *policy) hostMatches(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if p.exact[host] {
		return true
	}
	for _, w := range p.wildcards {
		if strings.HasSuffix(host, w) {
			return true
		}
	}
	return false
}

func (p *policy) ipAllowed(ip net.IP) bool { return ip != nil && p.allowedIPs[ip.String()] }

// allows reports whether the policy permits a connection to (host, dstIP) —
// used both to decide traffic and to prune denials the policy now covers.
func (p *policy) allows(host string, dstIP net.IP) bool {
	if host != "" && !isIPString(host) {
		return p.verdict(host, dstIP)
	}
	if ip := net.ParseIP(host); ip != nil {
		return p.verdictNoSNI(nil, ip)
	}
	return p.verdictNoSNI(nil, dstIP)
}

func isIPString(s string) bool { return net.ParseIP(s) != nil }

func (p *policy) ipDenied(ip net.IP) bool {
	for _, n := range p.deniedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// verdict decides an intercepted :80/:443 connection. host is the SNI/Host (may
// be "" when unparseable); dstIP is the original destination IP.
func (p *policy) verdict(host string, dstIP net.IP) bool {
	// Force-block overrides win in both modes (the live "block this domain" toggle).
	if p.blockedHost(host) || p.blockedIP(dstIP) {
		return false
	}
	match := p.hostMatches(host)
	switch p.mode {
	case ModeDeny: // allow-all + denylist
		if host == "" {
			return !p.ipDenied(dstIP)
		}
		return !match
	default: // ModeAllow: deny-all + allowlist
		if host == "" {
			return p.ipAllowed(dstIP)
		}
		return match || p.ipAllowed(dstIP)
	}
}

// verdictOpen decides a connection from a run whose egress is open (it is
// being provisioned): anything goes but what the project blocks — its
// force-blocked domains, and in deny mode its denylist. names are the
// hostnames the destination was resolved from, for a connection without a
// readable SNI/Host.
func (p *policy) verdictOpen(host string, names []string, dstIP net.IP) bool {
	if p.mode == ModeDeny {
		if host != "" {
			return p.verdict(host, dstIP)
		}
		return p.verdictNoSNI(names, dstIP)
	}
	if p.blockedHost(host) || p.blockedIP(dstIP) {
		return false
	}
	for _, n := range names {
		if p.blockedHost(n) {
			return false
		}
	}
	return true
}

// verdictNoSNI decides a connection with NO readable SNI/Host (ECH, IP-literal,
// oversized ClientHello) using the hostnames the destination IP was DNS-resolved
// from (the gateway snoops its own DNS forwarder). A no-SNI TLS connection to a
// shared CDN can't reach a specific site anyway (the CDN routes by SNI), so
// allowing it when a resolved name is allowlisted is safe.
func (p *policy) verdictNoSNI(names []string, dstIP net.IP) bool {
	// Force-block overrides win: block if the IP or any resolved name is blocked.
	if p.blockedIP(dstIP) {
		return false
	}
	for _, n := range names {
		if p.blockedHost(n) {
			return false
		}
	}
	switch p.mode {
	case ModeDeny: // allow unless a resolved name is on the denylist or the IP is denied
		for _, n := range names {
			if p.hostMatches(n) {
				return false
			}
		}
		return !p.ipDenied(dstIP)
	default: // ModeAllow: allow if a resolved name — or the IP itself — is allowlisted
		for _, n := range names {
			if p.hostMatches(n) {
				return true
			}
		}
		return p.ipAllowed(dstIP)
	}
}
