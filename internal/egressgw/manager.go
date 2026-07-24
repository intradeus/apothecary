// Package egressgw is the engine-side lifecycle manager for the per-PROJECT
// egress firewall gateway. One gateway container (apo-gw-<project>) serves all
// of a project's runs: each run gets its own --internal docker network
// (apo-egress-<run>), and the gateway is multi-homed onto every one of them plus
// one external uplink. All egress from a run's DinD sandbox (app + siblings)
// routes through the gateway, which transparently filters by TLS SNI / HTTP Host
// (workspace/gateway). The gateway can't be bypassed: a sandbox's only interface
// is on an internal network with no external route, and enforcement lives
// OUTSIDE the (privileged) sandbox.
//
// The Manager performs only HOST-daemon docker calls and gw-folder file I/O; it
// never touches a run's inner daemon. Policy in / denials out ride JSON files on
// a per-project bind mount (<RunsDir>/gw/<project>), reusing the same
// bind-mount-over-run-folder seam the sandbox relies on (container→host-IP is
// blocked on hardened hosts, so a shared file is the right control plane).
package egressgw

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"apothecary/internal/egressgw/policyfile"
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

// sanitize reduces an arbitrary project name to a docker-safe token.
func sanitize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune('-')
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "default"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func (m *Manager) netName(run string) string    { return "apo-egress-" + run }
func (m *Manager) gwName(project string) string { return "apo-gw-" + sanitize(project) }
func (m *Manager) gwDir(project string) string {
	return filepath.Join(m.runsDir, "gw", sanitize(project))
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
func (m *Manager) EnsureRunEgress(ctx context.Context, project, run string, f policyfile.File) (gwIP string, err error) {
	if err := os.MkdirAll(m.gwDir(project), 0o755); err != nil {
		return "", err
	}
	// Policy is per-project; write it (idempotent) so the gateway has a policy
	// the moment it starts.
	if err := m.WritePolicy(project, f); err != nil {
		return "", err
	}

	pm := m.projectMu(project)
	pm.Lock()
	defer pm.Unlock()

	// Per-run internal network (no external route → sandbox can't bypass).
	if err := m.ensureNetwork(ctx, project, run); err != nil {
		return "", err
	}
	if err := m.ensureGateway(ctx, project); err != nil {
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

func (m *Manager) ensureNetwork(ctx context.Context, project, run string) error {
	if _, err := m.docker(ctx, "network", "create", "--driver", "bridge", "--internal",
		"--label", "apothecary=true",
		"--label", "apothecary.env="+run,
		"--label", "apothecary.project="+project,
		m.netName(run)); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return err
	}
	return nil
}

func (m *Manager) ensureGateway(ctx context.Context, project string) error {
	// Already present? start it (no-op if running) and return.
	ids, err := m.docker(ctx, "ps", "-aq", "--filter", "name=^"+m.gwName(project)+"$")
	if err != nil {
		return err
	}
	if ids != "" {
		_, err := m.docker(ctx, "start", m.gwName(project))
		return err
	}
	img, err := m.ensureImage(ctx)
	if err != nil {
		return fmt.Errorf("egressgw: build gateway image: %w", err)
	}
	dir := m.gwDir(project)
	_, err = m.docker(ctx, "run", "-d", "--name", m.gwName(project),
		"--label", "apothecary=true",
		"--label", "apothecary.project="+project,
		"--label", "apothecary.egressgw=true",
		"--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW",
		"--network", "bridge",
		"--sysctl", "net.ipv4.ip_forward=1",
		"--sysctl", "net.ipv4.conf.all.rp_filter=0",
		"--sysctl", "net.ipv4.conf.default.rp_filter=0",
		"--sysctl", "net.ipv6.conf.all.disable_ipv6=1",
		"-v", dir+":"+dir,
		img,
		"--policy", m.policyPath(project),
		"--denials", m.denialsPath(project),
		"--routes", m.routesPath(project),
		"--audit", m.auditPath(project),
		"--listen-port", "9040",
		"--dns-upstream", "1.1.1.1:53",
	)
	return err
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

// DestroyRunEgress disconnects and removes this run's network, drops it from the
// route map, and reaps the project gateway if it was the last run.
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

	// Refcount: reap the gateway when the project has no more sandboxes.
	left, err := m.docker(ctx, "ps", "-aq",
		"--filter", "label=apothecary.project="+project,
		"--filter", "label=apothecary.sandbox=true")
	if err == nil && left == "" {
		_, _ = m.docker(ctx, "rm", "-f", m.gwName(project))
		_ = os.RemoveAll(m.gwDir(project))
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
