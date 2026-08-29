// Command apothecary-gateway is the per-project egress firewall daemon. It runs
// in the apo-gw-<project> container (CAP_NET_ADMIN, not privileged), multi-homed
// onto every run's --internal network plus one external uplink. All egress from
// every run's DinD sandbox (app + siblings) routes through it; it transparently
// filters HTTPS by TLS SNI and HTTP by Host header (no MITM), hot-reloading a
// per-project policy file with two modes (deny-all+allowlist / allow-all+denylist).
//
// Self-contained (pure stdlib), like workspace/shim: it is built standalone by
// wsimage.EnsureGatewayImage and cannot import apothecary packages.
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type gateway struct {
	policyPath, denialsPath, routesPath, dnsUpstream string
	listenPort                                       int
	logConns, logDNS                                 bool

	ext        atomic.Pointer[string]
	pol        atomic.Pointer[policy]
	file       atomic.Pointer[File]
	routesAttr atomic.Pointer[[]subnetRun]
	dns        *dnsCache // IP→hostname learned from forwarded DNS, for no-SNI connections
	audit      *auditor  // aggregated egress trail flushed for the engine to ingest

	dmu          sync.Mutex
	denials      map[string]*Denial // key: run + "\x00" + host
	denialsDirty bool
}

func main() {
	g := &gateway{denials: map[string]*Denial{}, dns: newDNSCache()}
	flag.StringVar(&g.policyPath, "policy", "", "path to egress-policy.json")
	flag.StringVar(&g.denialsPath, "denials", "", "path to egress-denials.json (written)")
	flag.StringVar(&g.routesPath, "routes", "", "path to routes.json (run->subnet)")
	var auditPath string
	flag.StringVar(&auditPath, "audit", "", "path to egress-audit.json (aggregated audit trail, written)")
	flag.StringVar(&g.dnsUpstream, "dns-upstream", "1.1.1.1:53", "upstream DNS resolver")
	flag.IntVar(&g.listenPort, "listen-port", 9040, "transparent filter listen port")
	flag.BoolVar(&g.logConns, "log-conns", true, "log every connection verdict (run/dst/host/allow|deny)")
	flag.BoolVar(&g.logDNS, "log-dns", true, "log DNS resolutions (run/name/ips)")
	// The pull-through registry cache is opt-in: no config path, no child (the
	// filtering gateway must work identically without it).
	var mirrorConfig, buildCacheConfig string
	flag.StringVar(&mirrorConfig, "mirror-config", "", "path to the registry mirror config.yml (empty = no image mirror)")
	flag.StringVar(&buildCacheConfig, "buildcache-config", "", "path to the build-cache registry config.yml (empty = no build cache)")
	flag.Parse()
	g.audit = newAuditor(auditPath)

	ext := detectEXT()
	for i := 0; ext == "" && i < 60; i++ {
		time.Sleep(500 * time.Millisecond)
		ext = detectEXT()
	}
	if ext == "" {
		log.Fatal("gateway: no default-route interface found")
	}
	g.ext.Store(&ext)
	log.Printf("gateway: EXT=%s listen=:%d policy=%s", ext, g.listenPort, g.policyPath)

	g.loadRoutes()
	if err := g.loadPolicy(); err != nil {
		log.Fatalf("gateway: initial policy load/apply: %v", err)
	}

	g.serveDNS(g.dnsUpstream)
	go g.watch()
	if mirrorConfig != "" {
		log.Printf("gateway: image mirror on :5000 (config %s)", mirrorConfig)
		go superviseRegistry("mirror", mirrorConfig)
	}
	// The writable twin: buildkit pushes the devcontainer build cache here and
	// pulls it back on the project's next run, which is the whole reason a
	// throwaway per-run daemon does not mean a from-scratch build.
	if buildCacheConfig != "" {
		log.Printf("gateway: build cache on :5001 (config %s)", buildCacheConfig)
		go superviseRegistry("buildcache", buildCacheConfig)
	}

	ln, err := net.Listen("tcp", ":"+itoa(g.listenPort))
	if err != nil {
		log.Fatalf("gateway: listen: %v", err)
	}
	log.Printf("gateway: ready (REDIRECT + SO_ORIGINAL_DST)")
	g.serve(ln)
}

// loadPolicy reads the policy file, swaps the in-memory policy, re-applies nft,
// logs what changed, and prunes any recorded denial the new policy now allows
// (so an "add to egress" click makes the UI's denial disappear).
func (g *gateway) loadPolicy() error {
	var f File
	if err := readJSON(g.policyPath, &f); err != nil {
		if os.IsNotExist(err) {
			f = File{Mode: ModeAllow} // fail closed: deny-all until a policy arrives
		} else {
			return err
		}
	}
	var prev *File
	if p := g.file.Load(); p != nil {
		prev = p
	}
	pol := newPolicy(f)
	g.file.Store(&f)
	g.pol.Store(pol)
	if prev != nil {
		if added := diffDomains(prev.Domains, f.Domains); len(added) > 0 {
			log.Printf("policy: mode=%s +allowed %v", f.normMode(), added)
		}
		if removed := diffDomains(f.Domains, prev.Domains); len(removed) > 0 {
			log.Printf("policy: mode=%s -removed %v", f.normMode(), removed)
		}
	} else {
		log.Printf("policy: loaded mode=%s domains=%d", f.normMode(), len(f.Domains))
	}
	g.pruneDenials(pol)
	return g.applyRules()
}

// pruneDenials drops denial records the new policy now allows, so the run page
// stops showing a host the user just permitted.
func (g *gateway) pruneDenials(pol *policy) {
	g.dmu.Lock()
	var pruned []string
	for key, d := range g.denials {
		if pol.allows(d.Host, nil) {
			delete(g.denials, key)
			pruned = append(pruned, d.Host)
			g.denialsDirty = true
		}
	}
	g.dmu.Unlock()
	if len(pruned) > 0 {
		log.Printf("policy: cleared denials now allowed: %v", pruned)
		g.flushDenials()
	}
}

// diffDomains returns entries in b not in a (case-insensitive).
func diffDomains(a, b []string) []string {
	have := map[string]bool{}
	for _, x := range a {
		have[strings.ToLower(x)] = true
	}
	var out []string
	for _, x := range b {
		if !have[strings.ToLower(x)] {
			out = append(out, x)
		}
	}
	return out
}

func (g *gateway) applyRules() error {
	ext := *g.ext.Load()
	f := *g.file.Load()
	return applyNFT(renderNFT(ext, g.listenPort, f))
}

// watch polls the policy/routes files and the EXT interface, hot-reloading on
// change, and flushes recorded denials.
func (g *gateway) watch() {
	var polMod, routesMod time.Time
	for range time.Tick(time.Second) {
		if mt, ok := mtime(g.policyPath); ok && mt.After(polMod) {
			polMod = mt
			if err := g.loadPolicy(); err != nil {
				log.Printf("gateway: reload policy: %v", err)
			}
		}
		if mt, ok := mtime(g.routesPath); ok && mt.After(routesMod) {
			routesMod = mt
			g.loadRoutes()
		}
		if ext := detectEXT(); ext != "" && ext != *g.ext.Load() {
			g.ext.Store(&ext)
			log.Printf("gateway: EXT changed to %s", ext)
			if err := g.applyRules(); err != nil {
				log.Printf("gateway: reapply nft: %v", err)
			}
		}
		g.flushDenials()
		g.audit.flush()
	}
}

func (g *gateway) recordDenial(run, host string) {
	key := run + "\x00" + host
	g.dmu.Lock()
	defer g.dmu.Unlock()
	d, ok := g.denials[key]
	if !ok {
		if len(g.denials) >= 500 { // cap distinct (run,host) pairs
			return
		}
		d = &Denial{Run: run, Host: host}
		g.denials[key] = d
	}
	d.Count++
	d.Last = time.Now().UTC()
	g.denialsDirty = true
}

// retainDenials drops denial records for runs no longer active (torn down),
// keeping the denials file bounded on a long-lived per-project gateway.
func (g *gateway) retainDenials(active map[string]bool) {
	g.dmu.Lock()
	defer g.dmu.Unlock()
	for key, d := range g.denials {
		if !active[d.Run] {
			delete(g.denials, key)
			g.denialsDirty = true
		}
	}
}

func (g *gateway) flushDenials() {
	g.dmu.Lock()
	if !g.denialsDirty {
		g.dmu.Unlock()
		return
	}
	out := make([]Denial, 0, len(g.denials))
	for _, d := range g.denials {
		out = append(out, *d)
	}
	g.denialsDirty = false
	g.dmu.Unlock()
	if err := writeAtomic(g.denialsPath, DenialsFile{Denials: out}); err != nil {
		log.Printf("gateway: write denials: %v", err)
	}
}

func mtime(path string) (time.Time, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
