// Package egressgw is the engine-side lifecycle manager for the per-PROJECT
// egress firewall gateway. One gateway container (apo-gw-<project id>) serves
// all of a project's runs: each run gets its own --internal docker network
// (apo-egress-<run>), and the gateway is multi-homed onto every one of them plus
// one external uplink. All egress from a run's DinD sandbox (app + siblings)
// routes through the gateway, which transparently filters by TLS SNI / HTTP Host
// (workspace/gateway). The gateway can't be bypassed: a sandbox's only interface
// is on an internal network with no external route, and enforcement lives
// OUTSIDE the (privileged) sandbox.
//
// The Manager performs only HOST-daemon docker calls and gw-folder file I/O; it
// never touches a run's inner daemon. Policy in / denials out ride JSON files on
// a per-project bind mount (<RunsDir>/gw/<project id>), reusing the same
// bind-mount-over-run-folder seam the sandbox relies on (container→host-IP is
// blocked on hardened hosts, so a shared file is the right control plane).
//
// Every `project` argument in this package is the project's ID (projects.id).
// ports.ProjectHostKey turns it into the token docker names and paths use; the
// project's NAME is a docker label and nothing more.
package egressgw

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"apothecary/internal/egressgw/policyfile"
	"apothecary/internal/ports"
)

// HostRun runs one docker CLI invocation against the HOST daemon (never a run's
// inner daemon). It matches the devcontainer adapter's hostRun.
type HostRun func(ctx context.Context, args []string) (stdout, stderr []byte, code int, err error)

type Manager struct {
	runsDir     string
	run         HostRun
	ensureImage func(context.Context) (string, error)

	pmu   map[string]*sync.Mutex // per-project mutexes for network/routes edits
	pmuMu sync.Mutex
}

func NewManager(runsDir string, run HostRun, ensureImage func(context.Context) (string, error)) *Manager {
	return &Manager{runsDir: runsDir, run: run, ensureImage: ensureImage, pmu: map[string]*sync.Mutex{}}
}

func (m *Manager) projectMu(project string) *sync.Mutex {
	m.pmuMu.Lock()
	defer m.pmuMu.Unlock()
	mu, ok := m.pmu[project]
	if !ok {
		mu = &sync.Mutex{}
		m.pmu[project] = mu
	}
	return mu
}

// --- naming ---------------------------------------------------------------

// Everything per project is named by ports.ProjectHostKey(project): the
// project's ID, lowercased. Never its name — a rename must not move a project
// off its gateway and caches, and two projects with the same name must not
// share them.

func (m *Manager) netName(run string) string    { return "apo-egress-" + run }
func (m *Manager) gwName(project string) string { return "apo-gw-" + ports.ProjectHostKey(project) }
func (m *Manager) gwDir(project string) string {
	return filepath.Join(m.runsDir, "gw", ports.ProjectHostKey(project))
}

// projectLabels are the docker labels every per-project resource carries: the
// id is what filters key on (`--filter label=apothecary.project=<id>`); the
// name is for a human reading `docker ps`, omitted when unknown.
func projectLabels(project, projectName string) []string {
	ls := []string{"--label", "apothecary=true", "--label", "apothecary.project=" + project}
	if projectName != "" {
		ls = append(ls, "--label", "apothecary.project.name="+projectName)
	}
	return ls
}
func (m *Manager) policyPath(project string) string {
	return filepath.Join(m.gwDir(project), "egress-policy.json")
}
func (m *Manager) denialsPath(project string) string {
	return filepath.Join(m.gwDir(project), "egress-denials.json")
}
func (m *Manager) routesPath(project string) string {
	return filepath.Join(m.gwDir(project), "routes.json")
}
func (m *Manager) auditPath(project string) string {
	return filepath.Join(m.gwDir(project), "egress-audit.json")
}
func (m *Manager) mirrorConfigPath(project string) string {
	return filepath.Join(m.gwDir(project), "registry-config.yml")
}
func (m *Manager) buildCacheConfigPath(project string) string {
	return filepath.Join(m.gwDir(project), "buildcache-config.yml")
}

// cacheVolume backs the gateway's pull-through image cache. A named volume (not
// gwDir) because it is the one piece of gateway state worth keeping when the
// container is recreated for an image upgrade — and it must NOT be swept with
// the rest of the folder.
func (m *Manager) cacheVolume(project string) string { return m.gwName(project) + "-cache" }

// buildCacheVolume backs the writable registry buildkit exports the
// devcontainer build cache to. Separate from the mirror's volume because they
// have different lifetimes: the mirror is pure derived data nobody asks about,
// while THIS one is the thing a human deletes from the UI when a build starts
// producing something stale.
func (m *Manager) buildCacheVolume(project string) string { return m.gwName(project) + "-buildcache" }

// buildCacheDir is where the build-cache registry stores blobs inside the
// gateway container (the volume's mount point).
const buildCacheDir = "/var/lib/buildcache"

// BuildCachePort is the gateway-local port of the writable build-cache
// registry — what a run's buildkit pushes to and pulls from.
const BuildCachePort = "5001"

// --- docker helper --------------------------------------------------------

func (m *Manager) docker(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, code, err := m.run(ctx, args)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("docker %s: exit %d: %s", strings.Join(args, " "), code, strings.TrimSpace(string(stderr)))
	}
	return strings.TrimSpace(string(stdout)), nil
}

// --- lifecycle ------------------------------------------------------------

// EnsureRunEgress guarantees the project gateway exists, creates this run's
// --internal network, connects it to the gateway, records the run→subnet
// attribution, and returns the gateway's IP ON THIS run's network (for the
// sandbox default route + --dns). Idempotent across the Provision reuse path.
// project is the project's id; projectName is a label only (projectLabels).
func (m *Manager) EnsureRunEgress(ctx context.Context, project, projectName, run string, f policyfile.File) (gwIP string, err error) {
	if err := os.MkdirAll(m.gwDir(project), 0o755); err != nil {
		return "", err
	}
	// Policy is per-project; write it (idempotent) so the gateway has a policy
	// the moment it starts.
	if err := m.WritePolicy(project, f); err != nil {
		return "", err
	}
	// Same for the image-mirror config: present before the container starts, and
	// re-checked every pass so an engine upgrade that changes it propagates.
	if err := m.writeMirrorConfig(project); err != nil {
		return "", err
	}
	if err := m.writeBuildCacheConfig(project); err != nil {
		return "", err
	}

	pm := m.projectMu(project)
	pm.Lock()
	defer pm.Unlock()

	// Per-run internal network (no external route → sandbox can't bypass).
	if err := m.ensureNetwork(ctx, project, projectName, run); err != nil {
		return "", err
	}
	if err := m.ensureGateway(ctx, project, projectName); err != nil {
		return "", err
	}
	// Connect this run's network to the gateway (idempotent).
	if _, err := m.docker(ctx, "network", "connect", m.netName(run), m.gwName(project)); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		return "", err
	}
	if err := m.recordRoute(ctx, project, run); err != nil {
		return "", err
	}
	gwIP, err = m.docker(ctx, "inspect", m.gwName(project), "--format",
		fmt.Sprintf(`{{ (index .NetworkSettings.Networks "%s").IPAddress }}`, m.netName(run)))
	if err != nil {
		return "", err
	}
	if gwIP == "" {
		return "", fmt.Errorf("egressgw: gateway has no IP on %s", m.netName(run))
	}
	return gwIP, nil
}

func (m *Manager) ensureNetwork(ctx context.Context, project, projectName, run string) error {
	args := append([]string{"network", "create", "--driver", "bridge", "--internal",
		"--label", "apothecary.env=" + run},
		projectLabels(project, projectName)...)
	if _, err := m.docker(ctx, append(args, m.netName(run))...); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return err
	}
	return nil
}

// ensureGateway brings the project's gateway up, creating it when absent and
// RECREATING it when it runs a stale image. The recreate path exists because
// gateways are no longer reaped when a project's last run goes away (they are
// kept warm for their image cache), so nothing else would ever pick up a new
// gateway build. Only the image tag is compared: argv changes here do not
// trigger a recreate on their own — they always ride a gateway-source or
// Dockerfile change, which bumps the content-addressed tag.
func (m *Manager) ensureGateway(ctx context.Context, project, projectName string) error {
	// Resolved on EVERY path (unlike before, when the warm path skipped it): the
	// tag is the upgrade signal, and a cache hit is just one image inspect.
	img, err := m.ensureImage(ctx)
	if err != nil {
		return fmt.Errorf("egressgw: build gateway image: %w", err)
	}
	ids, err := m.docker(ctx, "ps", "-aq", "--filter", "name=^"+m.gwName(project)+"$")
	if err != nil {
		return err
	}
	// netIPs carries the gateway's per-run addresses across a recreate.
	var netIPs map[string]string
	if ids != "" {
		cur, _ := m.docker(ctx, "inspect", m.gwName(project), "--format", "{{.Config.Image}}")
		if cur == img {
			_, err := m.docker(ctx, "start", m.gwName(project)) // no-op when running
			return err
		}
		// Stale image. A `rm -f` would sever egress for every attached run, so
		// upgrade only while the project is idle (no running sandbox carries its
		// id label); a busy project keeps its old gateway until its runs are done.
		if running, err := m.docker(ctx, "ps", "-q",
			"--filter", "label=apothecary.project="+project,
			"--filter", "label=apothecary.sandbox=true"); err == nil && running != "" {
			_, err := m.docker(ctx, "start", m.gwName(project))
			return err
		}
		// Stopped sandboxes baked --dns/--registry-mirror at their gwIP and
		// cannot be re-flagged by `docker start`, so the new gateway must land
		// on the same addresses.
		netIPs = m.gatewayNetIPs(ctx, project)
		if _, err := m.docker(ctx, "rm", "-f", m.gwName(project)); err != nil {
			return err
		}
	}
	// Both caches survive everything (idempotent create; never reaped).
	for _, vol := range []string{m.cacheVolume(project), m.buildCacheVolume(project)} {
		args := append([]string{"volume", "create"}, projectLabels(project, projectName)...)
		if _, err := m.docker(ctx, append(args, vol)...); err != nil && !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}
	dir := m.gwDir(project)
	args := append([]string{"run", "-d", "--name", m.gwName(project)}, projectLabels(project, projectName)...)
	args = append(args,
		"--label", "apothecary.egressgw=true",
		// Kept warm across host/daemon restarts: nothing reaps a gateway now, and
		// a stopped one would leave its runs with no egress until the next
		// provision. A fresh netns also means stale nft rules are impossible.
		"--restart", "unless-stopped",
		"--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW",
		"--network", "bridge",
		"--sysctl", "net.ipv4.ip_forward=1",
		"--sysctl", "net.ipv4.conf.all.rp_filter=0",
		"--sysctl", "net.ipv4.conf.default.rp_filter=0",
		"--sysctl", "net.ipv6.conf.all.disable_ipv6=1",
		"-v", dir+":"+dir,
		"-v", m.cacheVolume(project)+":/var/lib/registry",
		"-v", m.buildCacheVolume(project)+":"+buildCacheDir,
		img,
		"--policy", m.policyPath(project),
		"--denials", m.denialsPath(project),
		"--routes", m.routesPath(project),
		"--audit", m.auditPath(project),
		"--listen-port", "9040",
		"--dns-upstream", "1.1.1.1:53",
		"--mirror-config", m.mirrorConfigPath(project),
		"--buildcache-config", m.buildCacheConfigPath(project),
	)
	if _, err = m.docker(ctx, args...); err != nil {
		return err
	}
	// Re-attach the runs the replaced gateway served, at their old addresses.
	for netName, ip := range netIPs {
		if _, err := m.docker(ctx, "network", "connect", "--ip", ip, netName, m.gwName(project)); err != nil {
			// Not fatal: the run being provisioned right now gets its own
			// connect next, and a reused sandbox at worst loses DNS until it is
			// recreated — but it must be visible when it happens.
			return fmt.Errorf("egressgw: gateway upgraded but %s could not be re-attached at %s (its sandbox needs a rebuild): %w", netName, ip, err)
		}
	}
	return nil
}

// gatewayNetIPs maps the gateway's run networks to its address on each, so an
// upgrade can restore them. Best-effort: nil when the inspect fails.
func (m *Manager) gatewayNetIPs(ctx context.Context, project string) map[string]string {
	out, err := m.docker(ctx, "inspect", m.gwName(project), "--format",
		`{{range $n, $v := .NetworkSettings.Networks}}{{$n}} {{$v.IPAddress}}{{"\n"}}{{end}}`)
	if err != nil {
		return nil
	}
	ips := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, ip, ok := strings.Cut(strings.TrimSpace(line), " ")
		// Only run networks: the bridge uplink is re-created by docker run.
		if ok && ip != "" && strings.HasPrefix(name, "apo-egress-") {
			ips[name] = ip
		}
	}
	return ips
}

// registryConfig configures the gateway's pull-through Docker Hub cache
// (workspace/gateway/registry.go supervises `registry serve` with it).
// proxy.remoteurl is what makes it a read-through mirror rather than a
// registry; delete.enabled lets the proxy scheduler's TTL actually evict.
const registryConfig = `version: 0.1
log:
  level: warn
storage:
  filesystem:
    rootdirectory: /var/lib/registry
  delete:
    enabled: true
  cache:
    blobdescriptor: inmemory
http:
  addr: :5000
  secret: apothecary-mirror
proxy:
  remoteurl: https://registry-1.docker.io
`

// buildCacheConfig is the WRITABLE registry: no proxy section (a registry in
// proxy mode refuses pushes, which is exactly what a build cache needs to do),
// its own storage root, and delete enabled so a wipe can be surgical if it
// ever needs to be.
const buildCacheConfig = `version: 0.1
log:
  level: warn
storage:
  filesystem:
    rootdirectory: ` + buildCacheDir + `
  delete:
    enabled: true
  cache:
    blobdescriptor: inmemory
http:
  addr: :` + BuildCachePort + `
  secret: apothecary-buildcache
`

// writeBuildCacheConfig materializes the build-cache registry config, on the
// same only-when-different rule as the mirror: the gateway restarts the child
// on any mtime bump, and that restart is also how ClearBuildCache makes the
// registry forget what it had open.
func (m *Manager) writeBuildCacheConfig(project string) error {
	path := m.buildCacheConfigPath(project)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == buildCacheConfig {
		return nil
	}
	return os.WriteFile(path, []byte(buildCacheConfig), 0o644)
}

// writeMirrorConfig materializes the mirror config in gwDir, ONLY when the
// content differs: the gateway restarts its registry child on any mtime bump
// (the registry has no config hot-reload), so a blind rewrite on every
// provision would restart it needlessly.
func (m *Manager) writeMirrorConfig(project string) error {
	path := m.mirrorConfigPath(project)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == registryConfig {
		return nil
	}
	return os.WriteFile(path, []byte(registryConfig), 0o644)
}

// recordRoute updates routes.json with this run's subnet so the gateway can
// attribute denials to the run by source address.
func (m *Manager) recordRoute(ctx context.Context, project, run string) error {
	subnet, err := m.docker(ctx, "network", "inspect", m.netName(run), "--format",
		"{{ (index .IPAM.Config 0).Subnet }}")
	if err != nil {
		return err
	}
	var r policyfile.Routes
	_ = policyfile.Read(m.routesPath(project), &r)
	if r.RunSubnets == nil {
		r.RunSubnets = map[string]string{}
	}
	r.RunSubnets[run] = subnet
	return policyfile.WriteAtomic(m.routesPath(project), r)
}

// DestroyRunEgress disconnects and removes this run's network and drops it from
// the route map. The GATEWAY ITSELF IS KEPT: it holds the project's warm image
// cache, and rebuilding that on the next run is far more expensive than an idle
// alpine container (it also has --restart unless-stopped, so it survives daemon
// restarts). Nothing reaps it automatically; `docker rm -f apo-gw-<project id>`
// is the manual escape hatch, and the next provision recreates it.
func (m *Manager) DestroyRunEgress(ctx context.Context, project, run string) error {
	pm := m.projectMu(project)
	pm.Lock()
	defer pm.Unlock()

	_, _ = m.docker(ctx, "network", "disconnect", "-f", m.netName(run), m.gwName(project))
	_, _ = m.docker(ctx, "network", "rm", m.netName(run))

	var r policyfile.Routes
	if policyfile.Read(m.routesPath(project), &r) == nil && r.RunSubnets != nil {
		delete(r.RunSubnets, run)
		_ = policyfile.WriteAtomic(m.routesPath(project), r)
	}

	return nil
}

// --- policy / denials -----------------------------------------------------

// WritePolicy atomically writes the project's policy file; the gateway hot-swaps
// its in-memory policy within its poll interval (no restart).
func (m *Manager) WritePolicy(project string, f policyfile.File) error {
	if err := os.MkdirAll(m.gwDir(project), 0o755); err != nil {
		return err
	}
	if f.Mode == "" {
		f.Mode = policyfile.ModeAllow
	}
	return policyfile.WriteAtomic(m.policyPath(project), f)
}

// Audit returns the project's aggregated egress audit records (all runs). The
// engine ingests these into Postgres so the trail survives the gateway.
func (m *Manager) Audit(project string) []policyfile.AuditRecord {
	var af policyfile.AuditFile
	if err := policyfile.Read(m.auditPath(project), &af); err != nil {
		return nil
	}
	return af.Records
}

// Denials returns the project's recorded denials filtered to one run.
func (m *Manager) Denials(project, run string) []policyfile.Denial {
	var df policyfile.DenialsFile
	if err := policyfile.Read(m.denialsPath(project), &df); err != nil {
		return nil
	}
	out := df.Denials[:0:0]
	for _, d := range df.Denials {
		if d.Run == run {
			out = append(out, d)
		}
	}
	return out
}

// Policy returns the project's current policy file — what the gateway is
// enforcing (within its ~1s reload poll). A missing file reads as the
// default deny-all allowlist, matching WritePolicy's default.
func (m *Manager) Policy(project string) (policyfile.File, error) {
	var f policyfile.File
	if err := policyfile.Read(m.policyPath(project), &f); err != nil {
		if os.IsNotExist(err) {
			return policyfile.File{Mode: policyfile.ModeAllow}, nil
		}
		return policyfile.File{}, err
	}
	f.Mode = f.NormMode()
	return f, nil
}

// Allow is the "add to egress" fast path (SetRule with allow=true).
func (m *Manager) Allow(project, host string) error { return m.SetRule(project, host, true) }

// SetRule live-toggles a host's egress rule, project-wide, no restart. allow=true
// permits it (clears any force-block, and for allow-mode adds it to the allowlist
// / for deny-mode drops it from the denylist); allow=false force-blocks it via
// the BlockedDomains override, which wins in either mode.
func (m *Manager) SetRule(project, host string, allow bool) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return fmt.Errorf("empty host")
	}
	pm := m.projectMu(project)
	pm.Lock()
	defer pm.Unlock()
	var f policyfile.File
	if err := policyfile.Read(m.policyPath(project), &f); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		f = policyfile.File{Mode: policyfile.ModeAllow}
	}
	if allow {
		f.BlockedDomains = remove(f.BlockedDomains, host) // lift any block
		if f.NormMode() == policyfile.ModeDeny {
			f.Domains = remove(f.Domains, host) // stop denying it
		} else if !contains(f.Domains, host) {
			f.Domains = append(f.Domains, host) // add to the allowlist
		}
	} else {
		if !contains(f.BlockedDomains, host) {
			f.BlockedDomains = append(f.BlockedDomains, host) // force-deny (wins)
		}
		f.Domains = remove(f.Domains, host) // drop any explicit allow of it
	}
	return policyfile.WriteAtomic(m.policyPath(project), f)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if strings.EqualFold(v, x) {
			return true
		}
	}
	return false
}

func remove(xs []string, x string) []string {
	out := xs[:0:0]
	for _, v := range xs {
		if !strings.EqualFold(v, x) {
			out = append(out, v)
		}
	}
	return out
}

// --- build cache -------------------------------------------------------------

// BuildCacheRef / BuildCacheImageRef are the two registry references a
// project's devcontainer build reads, as seen from inside a run's sandbox.
// gwIP is the gateway's address on THAT run's network.
//
// Two, because the devcontainer CLI supports two different mechanisms and the
// one that works depends on the repository's own devcontainer:
//
//	:cache  a buildkit cache manifest (--cache-to/--cache-from, mode=max, so
//	        intermediate stages too). Only single-container devcontainers get
//	        it: for a compose devcontainer the CLI refuses --cache-to outright
//	        ("--cache-to not supported", its words).
//	:image  the built image itself, pushed after the build. The CLI always
//	        builds with BUILDKIT_INLINE_CACHE=1 — including in the compose
//	        override file it generates — so the pushed image carries the cache
//	        metadata that makes --cache-from restore from it.
//
// Both are offered as --cache-from every time; a missing one is a 404 buildkit
// shrugs at. The host:port half is just how the sandbox reaches the registry —
// the manifests live under the repository path, so a gateway that comes back
// on a different address still finds what it wrote yesterday.
func BuildCacheRef(gwIP, project string) string {
	return buildCacheRepo(gwIP, project) + ":cache"
}

func BuildCacheImageRef(gwIP, project string) string {
	return buildCacheRepo(gwIP, project) + ":image"
}

// buildCacheRepo keys the repository by ports.ProjectHostKey(project) — the
// lowercased project id, which is also all the distribution spec allows in a
// path component. Not the name: a rename must not orphan the cache.
func buildCacheRepo(gwIP, project string) string {
	return gwIP + ":" + BuildCachePort + "/" + ports.ProjectHostKey(project) + "/devcontainer"
}

// BuildCacheSize reports how many bytes the project's build cache occupies.
// Best-effort by design: a gateway that is down has no cache to report, and
// that is a number (0), not an error the UI has to render.
func (m *Manager) BuildCacheSize(ctx context.Context, project string) (int64, error) {
	out, err := m.docker(ctx, "exec", m.gwName(project), "du", "-sk", buildCacheDir)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return 0, nil
	}
	kb, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("egressgw: build cache size %q: %w", out, err)
	}
	return kb * 1024, nil
}

// ClearBuildCache empties the project's build cache: the next build starts
// from scratch and repopulates it.
//
// Empty-then-restart, in that order and both parts required. The registry
// holds no lock we can take, so the wipe removes the storage under it and the
// config touch makes the gateway restart the child — which is what stops it
// serving a manifest whose blobs are gone. A build racing the wipe is covered
// by ignore-error on the cache export: it loses its cache, it does not fail.
func (m *Manager) ClearBuildCache(ctx context.Context, project string) error {
	pm := m.projectMu(project)
	pm.Lock()
	defer pm.Unlock()

	if _, err := m.docker(ctx, "exec", m.gwName(project), "sh", "-c",
		"rm -rf "+buildCacheDir+"/* "+buildCacheDir+"/.[!.]* 2>/dev/null; exit 0"); err != nil {
		return fmt.Errorf("egressgw: clear build cache: %w", err)
	}
	// Touch the config so the gateway's supervisor restarts the registry with
	// a clean view. Writing the same bytes is enough — it watches mtime.
	if err := os.WriteFile(m.buildCacheConfigPath(project), []byte(buildCacheConfig), 0o644); err != nil {
		return fmt.Errorf("egressgw: restart build-cache registry: %w", err)
	}
	return nil
}
