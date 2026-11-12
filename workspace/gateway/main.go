// Command apothecary-gateway is the per-project egress firewall daemon. It runs
// as the apo-gw-<project> container (CAP_NET_ADMIN, not privileged) and every
// run's sandbox egresses through it over its own WireGuard peer on wg0
// (--wg-key). On docker the gateway is also multi-homed onto each run's
// --internal network, which carries the tunnel, DNS and the caches — the
// tunnel keeps the egress itself off the host's bridge, where bridge
// netfilter would judge (and drop) it. Either way the run links are "not the
// uplink", and the same transparent filter applies: HTTPS by TLS SNI, HTTP
// by Host header (no MITM), from a hot-reloaded per-project policy with two
// modes (deny-all+allowlist / allow-all+denylist).
//
// The engine drives it over a small HTTP control API (--control-listen, see
// control.go): policy and run routes in, denials, audit and build-cache facts
// out. The same state also lives as files under --state-dir, so a restarted
// gateway comes back with its policy, its routes and its WireGuard peers.
//
// It also hosts the project's caches (one gateway, every run reaches it):
// the Docker Hub mirror and the build cache (registry.go) and the VS Code
// extension gallery (gallery.go).
//
// Self-contained (pure stdlib), like workspace/shim: it is built standalone
// into the egress-gw image (workspace/images/gateway) and cannot import
// apothecary packages.
package main

import (
	"flag"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type gateway struct {
	policyPath, denialsPath, routesPath, dnsUpstream string
	listenPort                                       int
	logConns, logDNS                                 bool

	// buildCacheDir / buildCacheConfig: the writable registry's storage and
	// config (the control API sizes and wipes it).
	buildCacheDir, buildCacheConfig string
	// gallery is the extension gallery proxy (gallery.go); nil when off. The
	// build-cache wipe clears its cache too.
	gallery *gallery
	// protect are destinations no run may reach, whatever the policy says —
	// the cluster's pod and service ranges and link-local metadata, so the
	// gateway is never a pivot into the network it runs on.
	protect []*net.IPNet
	// wg is the WireGuard run link (Kubernetes); nil on docker.
	wg *wgLink
	// controlPort is the control API's port (0 = none), closed to run links.
	controlPort int
	// cmu serializes control-API writes (policy, routes) against each other.
	cmu sync.Mutex

	ext        atomic.Pointer[string]
	pol        atomic.Pointer[policy]
	file       atomic.Pointer[File]
	routesAttr atomic.Pointer[[]subnetRun]
	routesMem  atomic.Pointer[Routes] // the routes as last applied (no --state-dir)
	openKey    atomic.Pointer[string] // the open sources the nft rules were last rendered with
	dns        *dnsCache              // IP→hostname learned from forwarded DNS, for no-SNI connections
	audit      *auditor               // aggregated egress trail flushed for the engine to ingest

	dmu          sync.Mutex
	denials      map[string]*Denial // key: run + "\x00" + host
	denialsDirty bool
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	g := &gateway{denials: map[string]*Denial{}, dns: newDNSCache()}
	flag.StringVar(&g.policyPath, "policy", "", "path to egress-policy.json")
	flag.StringVar(&g.denialsPath, "denials", "", "path to egress-denials.json (written)")
	flag.StringVar(&g.routesPath, "routes", "", "path to routes.json (run->subnet)")
	var auditPath string
	flag.StringVar(&auditPath, "audit", "", "path to egress-audit.json (aggregated audit trail, written)")
	flag.StringVar(&g.dnsUpstream, "dns-upstream", "1.1.1.1:53", `upstream DNS resolver, or "resolv.conf" for the container's own (a cluster's DNS)`)
	flag.IntVar(&g.listenPort, "listen-port", 9040, "transparent filter listen port")
	flag.BoolVar(&g.logConns, "log-conns", true, "log every connection verdict (run/dst/host/allow|deny)")
	flag.BoolVar(&g.logDNS, "log-dns", true, "log DNS resolutions (run/name/ips)")
	// The pull-through registry cache is opt-in: no config path, no child (the
	// filtering gateway must work identically without it).
	var mirrorConfig, buildCacheConfig string
	flag.StringVar(&mirrorConfig, "mirror-config", "", "path to the registry mirror config.yml (empty = no image mirror)")
	flag.StringVar(&buildCacheConfig, "buildcache-config", "", "path to the build-cache registry config.yml (empty = no build cache)")
	var stateDir, controlListen, controlTokenFile, wgKey, wgAddr, protect string
	var wgListen, wgMTU int
	var mirror, buildCache bool
	flag.StringVar(&stateDir, "state-dir", "", "directory for policy/routes/denials/audit and generated registry configs (defaults every path flag left empty)")
	flag.BoolVar(&mirror, "mirror", false, "run the Docker Hub pull-through mirror on :5000, config generated into --state-dir")
	flag.BoolVar(&buildCache, "buildcache", false, "run the writable build-cache registry on :5001, config generated into --state-dir")
	flag.StringVar(&g.buildCacheDir, "buildcache-dir", "/var/lib/buildcache", "build-cache registry storage")
	var galleryOn bool
	var galleryUpstream string
	flag.BoolVar(&galleryOn, "gallery", false, "serve the VS Code extension gallery (an Open VSX proxy + cache) on :"+galleryPort+", cache under --state-dir")
	flag.StringVar(&galleryUpstream, "gallery-upstream", "https://open-vsx.org", "the marketplace the gallery proxies")
	flag.StringVar(&controlListen, "control-listen", "", "control API address, e.g. :9041 (empty = no API)")
	flag.StringVar(&controlTokenFile, "control-token-file", "", "file holding the control API bearer token (else $APO_GW_TOKEN)")
	flag.StringVar(&wgKey, "wg-key", "", "WireGuard private key file (else $APO_GW_WG_KEY): enables the wg0 run link")
	flag.IntVar(&wgListen, "wg-listen", 51820, "WireGuard UDP port")
	flag.StringVar(&wgAddr, "wg-addr", "10.90.0.1/16", "gateway address on wg0 (runs get /32s inside it)")
	flag.IntVar(&wgMTU, "wg-mtu", 0, "wg0 MTU (0 = uplink MTU - 60)")
	flag.StringVar(&protect, "protect-cidrs", "", "comma-separated destinations no run may reach (cluster pod/service ranges, metadata)")
	flag.Parse()
	// The engine's secrets leave the environment at once: every child (wg, ip,
	// nft, the registry) would otherwise inherit them.
	envWGKey, envToken := os.Getenv("APO_GW_WG_KEY"), os.Getenv("APO_GW_TOKEN")
	os.Unsetenv("APO_GW_WG_KEY")
	os.Unsetenv("APO_GW_TOKEN")
	if stateDir != "" {
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			slog.Error("gateway: state dir", "err", err)
			os.Exit(1)
		}
		def := func(p *string, name string) {
			if *p == "" {
				*p = filepath.Join(stateDir, name)
			}
		}
		def(&g.policyPath, "egress-policy.json")
		def(&g.denialsPath, "egress-denials.json")
		def(&g.routesPath, "routes.json")
		def(&auditPath, "egress-audit.json")
		if mirror {
			def(&mirrorConfig, "registry-config.yml")
			if err := writeIfChanged(mirrorConfig, mirrorRegistryConfig); err != nil {
				slog.Error("gateway: mirror config", "err", err)
				os.Exit(1)
			}
		}
		if buildCache {
			def(&buildCacheConfig, "buildcache-config.yml")
			if err := writeIfChanged(buildCacheConfig, buildCacheRegistryConfig(g.buildCacheDir)); err != nil {
				slog.Error("gateway: build-cache config", "err", err)
				os.Exit(1)
			}
		}
	}
	g.buildCacheConfig = buildCacheConfig
	if galleryOn {
		if err := galleryUpstreamOK(galleryUpstream); err != nil {
			slog.Error("gateway: --gallery-upstream", "err", err)
			os.Exit(1)
		}
		dir := ""
		if stateDir != "" {
			dir = filepath.Join(stateDir, "gallery")
		}
		g.gallery = newGallery(galleryUpstream, dir)
	}
	if controlListen != "" {
		_, p, err := net.SplitHostPort(controlListen)
		if err != nil {
			slog.Error("gateway: --control-listen", "err", err)
			os.Exit(1)
		}
		g.controlPort, _ = strconv.Atoi(p)
	}
	nets, err := parseCIDRs(protect)
	if err != nil {
		slog.Error("gateway: --protect-cidrs", "err", err)
		os.Exit(1)
	}
	g.protect = nets
	g.audit = newAuditor(auditPath)

	ext := detectEXT()
	for i := 0; ext == "" && i < 60; i++ {
		time.Sleep(500 * time.Millisecond)
		ext = detectEXT()
	}
	if ext == "" {
		slog.Error("gateway: no default-route interface found")
		os.Exit(1)
	}
	g.ext.Store(&ext)
	slog.Info("gateway", "ext", ext, "listen", g.listenPort, "policy", g.policyPath)

	if wgKey != "" || envWGKey != "" {
		key, err := stageWGKey(wgKey, envWGKey)
		if err != nil {
			slog.Error("gateway: wireguard key", "err", err)
			os.Exit(1)
		}
		link, err := setupWG(ext, key, wgListen, wgAddr, wgMTU)
		if err != nil {
			slog.Error("gateway: wireguard", "err", err)
			os.Exit(1)
		}
		g.wg = link
		slog.Info("gateway: wg0 listening", "addr", wgAddr, "udp", wgListen, "mtu", link.mtu)
	}
	g.loadRoutes()
	if err := g.loadPolicy(); err != nil {
		slog.Error("gateway: initial policy load/apply", "err", err)
		os.Exit(1)
	}

	if g.dnsUpstream == "resolv.conf" {
		up, err := resolvConfUpstream("/etc/resolv.conf")
		if err != nil {
			slog.Error("gateway: --dns-upstream resolv.conf", "err", err)
			os.Exit(1)
		}
		g.dnsUpstream = up
	}
	slog.Info("gateway: DNS upstream", "addr", g.dnsUpstream)
	g.serveDNS(g.dnsUpstream)
	go g.watch()
	if mirrorConfig != "" {
		slog.Info("gateway: image mirror on :5000", "config", mirrorConfig)
		go superviseRegistry("mirror", mirrorConfig)
	}
	// The writable twin: buildkit pushes the devcontainer build cache here and
	// pulls it back on the project's next run, which is the whole reason a
	// throwaway per-run daemon does not mean a from-scratch build.
	if buildCacheConfig != "" {
		slog.Info("gateway: build cache on :5001", "config", buildCacheConfig)
		go superviseRegistry("buildcache", buildCacheConfig)
	}
	// The extension gallery: Open VSX, proxied and cached per project, so a
	// run's code-server installs extensions inside allow-mode projects and a
	// project's runs warm each other's — the same shape as the image mirror.
	if g.gallery != nil {
		go serveGallery(g.gallery)
	}

	if controlListen != "" {
		token, err := controlToken(controlTokenFile, envToken)
		if err != nil {
			slog.Error("gateway: control token", "err", err)
			os.Exit(1)
		}
		go g.serveControl(controlListen, token)
	}

	ln, err := listenStream("tcp", ":"+itoa(g.listenPort))
	if err != nil {
		slog.Error("gateway: listen", "err", err)
		os.Exit(1)
	}
	slog.Info("gateway: ready (REDIRECT + SO_ORIGINAL_DST)")
	g.serve(ln)
}

// loadPolicy reads the policy file, swaps the in-memory policy, re-applies nft,
// logs what changed, and prunes any recorded denial the new policy now allows
// (so an "add to egress" click makes the UI's denial disappear).
func (g *gateway) loadPolicy() error {
	var f File
	if err := readJSON(g.policyPath, &f); err != nil {
		if os.IsNotExist(err) || g.policyPath == "" {
			f = File{Mode: ModeAllow} // fail closed: deny-all until a policy arrives
		} else {
			return err
		}
	}
	return g.installPolicy(f)
}

// installPolicy makes f the live policy: swaps it in, re-applies nft, logs
// what changed and prunes denials it now allows.
func (g *gateway) installPolicy(f File) error {
	var prev *File
	if p := g.file.Load(); p != nil {
		prev = p
	}
	pol := newPolicy(f)
	g.file.Store(&f)
	g.pol.Store(pol)
	if prev != nil {
		if added := diffDomains(prev.Domains, f.Domains); len(added) > 0 {
			slog.Info("policy: allowed", "mode", f.normMode(), "added", added)
		}
		if removed := diffDomains(f.Domains, prev.Domains); len(removed) > 0 {
			slog.Info("policy: removed", "mode", f.normMode(), "removed", removed)
		}
	} else {
		slog.Info("policy: loaded", "mode", f.normMode(), "domains", len(f.Domains))
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
		slog.Info("policy: cleared denials now allowed", "domains", pruned)
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
	open := g.openSources(time.Now())
	key := strings.Join(open, ",")
	g.openKey.Store(&key)
	return applyNFT(renderNFT(ext, g.listenPort, f, g.protect, g.controlPort, open))
}

// watchTick paces watch (swapped in tests, which close it to stop the loop).
var watchTick = func() <-chan time.Time { return time.Tick(time.Second) }

// watch polls the policy/routes files and the EXT interface, hot-reloading on
// change, and flushes recorded denials.
func (g *gateway) watch() {
	var polMod, routesMod time.Time
	for range watchTick() {
		if mt, ok := mtime(g.policyPath); ok && mt.After(polMod) {
			polMod = mt
			if err := g.loadPolicy(); err != nil {
				slog.Warn("gateway: reload policy", "err", err)
			}
		}
		if mt, ok := mtime(g.routesPath); ok && mt.After(routesMod) {
			routesMod = mt
			g.loadRoutes()
		}
		if ext := detectEXT(); ext != "" && ext != *g.ext.Load() {
			g.ext.Store(&ext)
			slog.Info("gateway: EXT changed", "ext", ext)
			if err := g.applyRules(); err != nil {
				slog.Warn("gateway: reapply nft", "err", err)
			}
		}
		g.expireOpen(time.Now())
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
	if g.denialsPath == "" {
		return // in memory only: the control API serves it
	}
	if err := writeAtomic(g.denialsPath, DenialsFile{Denials: out}); err != nil {
		slog.Warn("gateway: write denials", "err", err)
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
