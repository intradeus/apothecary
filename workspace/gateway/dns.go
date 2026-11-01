package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

// Swapped in tests: :53 is privileged, and these let a test close what it opened.
var (
	listenPacket = net.ListenPacket
	listenStream = net.Listen
)

// resolvConfUpstream is the first nameserver of a resolv.conf, as host:53. On
// Kubernetes that is the cluster DNS, so a run's lookups of cluster names (the
// engine's Service) resolve too — reaching them is still the filter's call.
func resolvConfUpstream(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil {
			return net.JoinHostPort(f[1], "53"), nil
		}
	}
	return "", fmt.Errorf("%s has no nameserver", path)
}

// serveDNS runs a minimal UDP+TCP :53 forwarder to upstream. DNS is deliberately
// NOT a filtering point (a resolver/enforcer parser-differential is the classic
// bypass) — egress is filtered at connect time by SNI/Host. Inner containers and
// the sandbox point their resolver at the gateway (--dns gwIP), so this just has
// to resolve names so those connections can then be filtered.
func (g *gateway) serveDNS(upstream string) {
	go g.serveDNSUDP(upstream)
	go g.serveDNSTCP(upstream)
}

func (g *gateway) serveDNSUDP(upstream string) {
	pc, err := listenPacket("udp", ":53")
	if err != nil {
		log.Printf("dns udp listen: %v", err)
		return
	}
	buf := make([]byte, 4096)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		req := make([]byte, n)
		copy(req, buf[:n])
		go func() {
			uc, err := net.DialTimeout("udp", upstream, 5*time.Second)
			if err != nil {
				return
			}
			defer uc.Close()
			_ = uc.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := uc.Write(req); err != nil {
				return
			}
			resp := make([]byte, 4096)
			m, err := uc.Read(resp)
			if err != nil {
				return
			}
			// Learn IP→hostname for no-SNI connections, and log the resolution.
			name, ips, ttl := parseDNSAnswers(resp[:m])
			g.dns.remember(name, ips, ttl)
			if g.logDNS && name != "" && len(ips) > 0 {
				var cip net.IP
				if ua, ok := addr.(*net.UDPAddr); ok {
					cip = ua.IP
				}
				log.Printf("run=%s dns %s -> %v", runOr(g.attributeIP(cip)), name, ips)
			}
			_, _ = pc.WriteTo(resp[:m], addr)
		}()
	}
}

func (g *gateway) serveDNSTCP(upstream string) {
	ln, err := listenStream("tcp", ":53")
	if err != nil {
		log.Printf("dns tcp listen: %v", err)
		return
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			uc, err := net.DialTimeout("tcp", upstream, 5*time.Second)
			if err != nil {
				return
			}
			defer uc.Close()
			deadline := time.Now().Add(10 * time.Second)
			_ = c.SetDeadline(deadline)
			_ = uc.SetDeadline(deadline)
			go io.Copy(uc, c)
			_, _ = io.Copy(c, uc)
		}()
	}
}
