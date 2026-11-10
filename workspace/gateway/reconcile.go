package main

import (
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

// routeTable is the kernel's IPv4 routing table (swapped in tests).
var routeTable = "/proc/net/route"

// detectEXT returns the external-uplink interface = the one carrying the default
// route (read from /proc/net/route, so no iproute2 dependency). Run networks are
// everything else and are matched "not EXT" in nft, so they need no enumeration.
func detectEXT() string {
	b, err := os.ReadFile(routeTable)
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
	g.applyRoutes(r)
}

// applyRoutes installs a routes table: attribution, WireGuard peers, and the
// eviction of torn-down runs' audit and denials.
func (g *gateway) applyRoutes(r Routes) {
	g.routesMem.Store(&r)
	if g.wg != nil {
		g.wg.reconcilePeers(r.Peers)
	}
	var attr []subnetRun
	active := make(map[string]bool, len(r.RunSubnets))
	for run, cidr := range r.RunSubnets {
		active[run] = true
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			attr = append(attr, subnetRun{net: n, run: run})
		}
	}
	// A docker run reaches the gateway two ways: its tunnel (egress) and its
	// network (DNS, mirror, build cache) — its subnet is the network's, so
	// its tunnel address is attributed here. On Kubernetes the two coincide.
	for run, p := range r.Peers {
		if ip := net.ParseIP(p.IP).To4(); ip != nil {
			attr = append(attr, subnetRun{net: &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}, run: run})
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
	g.reapplyIfOpenChanged()
}

// isOpen reports whether the run's egress is open right now (Routes.Open).
func (g *gateway) isOpen(run string) bool {
	if run == "" {
		return false
	}
	r := g.routesMem.Load()
	if r == nil {
		return false
	}
	until, ok := r.Open[run]
	return ok && time.Now().Before(until)
}

// openSources are the source ranges of the runs whose egress is open at now:
// each one's subnet and, on a tunnel, its address. Sorted, so an unchanged
// set renders the same rules.
func (g *gateway) openSources(now time.Time) []string {
	r := g.routesMem.Load()
	if r == nil {
		return nil
	}
	var out []string
	for run, until := range r.Open {
		if !now.Before(until) {
			continue
		}
		if c := r.RunSubnets[run]; c != "" {
			out = append(out, c)
		}
		if p, ok := r.Peers[run]; ok && p.IP != "" {
			out = append(out, p.IP)
		}
	}
	sort.Strings(out)
	return out
}

// reapplyIfOpenChanged re-renders the nft rules when the set of open runs
// moved — the raw-egress accept for them lives there. A no-op before the
// policy and the uplink are known (startup applies the rules itself).
func (g *gateway) reapplyIfOpenChanged() {
	if g.ext.Load() == nil || g.file.Load() == nil {
		return
	}
	key := strings.Join(g.openSources(time.Now()), ",")
	if prev := g.openKey.Load(); prev != nil && *prev == key {
		return
	}
	if err := g.applyRules(); err != nil {
		slog.Warn("gateway: reapply nft for open runs", "err", err)
	}
}

// expireOpen closes the windows whose deadline passed: the engine closes a
// run's window when its provisioning ends, this is for when it never does.
func (g *gateway) expireOpen(now time.Time) {
	r := g.routesMem.Load()
	if r == nil {
		return
	}
	expired := false
	for _, until := range r.Open {
		if !now.Before(until) {
			expired = true
			break
		}
	}
	if !expired {
		return
	}
	g.cmu.Lock()
	defer g.cmu.Unlock()
	rt := g.currentRoutes()
	for run, until := range rt.Open {
		if !now.Before(until) {
			delete(rt.Open, run)
			slog.Info("gateway: provisioning egress window expired", "run_id", run)
		}
	}
	if err := g.persist(g.routesPath, rt); err != nil {
		slog.Warn("gateway: persist routes", "err", err)
	}
	g.applyRoutes(rt)
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

// protected reports a destination no run may reach (--protect-cidrs).
func (g *gateway) protected(ip net.IP) bool {
	for _, n := range g.protect {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// parseCIDRs parses a comma-separated CIDR list ("" → none).
func parseCIDRs(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}
