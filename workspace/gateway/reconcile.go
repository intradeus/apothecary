package main

import (
	"net"
	"os"
	"sort"
	"strings"
)

// detectEXT returns the external-uplink interface = the one carrying the default
// route (read from /proc/net/route, so no iproute2 dependency). Run networks are
// everything else and are matched "not EXT" in nft, so they need no enumeration.
func detectEXT() string {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) >= 4 && f[1] == "00000000" { // destination 0.0.0.0 == default route
			return f[0]
		}
	}
	return ""
}

type subnetRun struct {
	net *net.IPNet
	run string
}

// loadRoutes reads routes.json (run -> apo-egress-<run> subnet CIDR) and rebuilds
// the src-subnet -> run attribution table used to tag denials per run.
func (g *gateway) loadRoutes() {
	var r Routes
	if err := readJSON(g.routesPath, &r); err != nil {
		return
	}
	var attr []subnetRun
	active := make(map[string]bool, len(r.RunSubnets))
	for run, cidr := range r.RunSubnets {
		active[run] = true
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			attr = append(attr, subnetRun{net: n, run: run})
		}
	}
	// longest-prefix first, so a more specific subnet wins.
	sort.Slice(attr, func(i, j int) bool {
		oi, _ := attr[i].net.Mask.Size()
		oj, _ := attr[j].net.Mask.Size()
		return oi > oj
	})
	g.routesAttr.Store(&attr)

	// Bound the in-memory aggregate (and the files it flushes) to ACTIVE runs:
	// a run removed from routes.json is torn down, and the engine has already
	// persisted its audit to the DB — so its records can be evicted. This keeps
	// the long-lived per-project gateway from accumulating dead runs forever.
	// Skip when routes is empty at startup (nothing connected yet) to avoid a
	// spurious wipe before the first run registers.
	if len(active) > 0 {
		g.audit.retain(active)
		g.retainDenials(active)
	}
}

// attributeIP maps a source IP to the run whose sandbox subnet contains it.
func (g *gateway) attributeIP(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if p := g.routesAttr.Load(); p != nil {
		for _, sr := range *p {
			if sr.net.Contains(ip) {
				return sr.run
			}
		}
	}
	return ""
}
