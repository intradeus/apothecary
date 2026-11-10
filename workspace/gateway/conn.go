package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"
)

// runOr renders the run attribution for logs, "-" when unknown.
func runOr(run string) string {
	if run == "" {
		return "-"
	}
	return run
}

// Swapped in tests: a real SO_ORIGINAL_DST needs an nft REDIRECT, and the
// destinations it yields are :80/:443.
var (
	originalDstOf = originalDst
	dialUpstream  = net.DialTimeout
)

// serve accepts REDIRECT-intercepted connections and filters them by SNI/Host.
func (g *gateway) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go g.handle(c.(*net.TCPConn))
	}
}

func (g *gateway) handle(c *net.TCPConn) {
	defer c.Close()
	orig, err := originalDstOf(c)
	if err != nil {
		return
	}
	var srcIP net.IP
	if src, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		srcIP = src.IP
	}
	run := g.attributeIP(srcIP)

	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	// 16 KiB: post-quantum / ECH ClientHellos can exceed the 4 KiB default, and a
	// truncated peek loses the SNI.
	br := bufio.NewReaderSize(c, 16<<10)
	var host string
	switch orig.Port {
	case 443:
		host = peekSNI(br)
	case 80:
		host = peekHTTPHost(br)
	}
	_ = c.SetReadDeadline(time.Time{})

	pol := g.pol.Load()
	if pol == nil {
		return
	}
	allow := false
	if g.protected(orig.IP) {
		// Never reachable through the gateway, whatever the policy says.
		if host == "" {
			host = orig.IP.String()
		}
	} else if g.isOpen(run) {
		// Provisioning: open but for what the project blocks.
		var names []string
		if host == "" {
			names = g.dns.names(orig.IP)
		}
		allow = pol.verdictOpen(host, names, orig.IP)
		if host == "" {
			host = orig.IP.String()
			if len(names) > 0 {
				host = names[0]
			}
		}
	} else if host != "" {
		allow = pol.verdict(host, orig.IP)
	} else {
		// No readable SNI/Host (ECH, IP-literal, oversized ClientHello): judge by
		// the hostname(s) this IP was DNS-resolved from (snooped by dns.go).
		names := g.dns.names(orig.IP)
		allow = pol.verdictNoSNI(names, orig.IP)
		if len(names) > 0 {
			host = names[0] // for an actionable denial line, not a bare IP
		} else {
			host = orig.IP.String()
		}
	}
	proto := protoFor(orig.Port)
	if g.logConns {
		verd := "ALLOW"
		if !allow {
			verd = "DENY"
		}
		slog.Info("conn", "run_id", runOr(run), "verdict", verd, "dst", fmt.Sprintf("%s:%d", orig.IP, orig.Port), "host", host)
	}
	if !allow {
		g.recordDenial(run, host)
		g.audit.record(run, host, orig.IP.String(), orig.Port, proto, "deny", 0, 0)
		return
	}

	up, err := dialUpstream("tcp", orig.String(), 15*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	// br still holds the peeked ClientHello/request (Peek does not consume), so
	// io.Copy(up, br) forwards it first — do NOT re-write it separately.
	var bytesOut, bytesIn int64
	done := make(chan struct{}, 2)
	go func() { bytesOut = pipe(up, br); done <- struct{}{} }() // client → upstream
	go func() { bytesIn = pipe(c, up); done <- struct{}{} }()   // upstream → client
	<-done
	<-done
	g.audit.record(run, host, orig.IP.String(), orig.Port, proto, "allow", bytesOut, bytesIn)
}

// pipe copies src→dst then half-closes dst's write side, so the peer sees a
// clean EOF without truncating the reverse direction (lifted from egressproxy).
// Returns the bytes copied (for the audit trail).
func pipe(dst io.Writer, src io.Reader) int64 {
	n, _ := io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	return n
}
