// Package egressgw is the engine-side manager of the per-PROJECT egress
// gateway (workspace/gateway). One gateway serves all of a project's runs and
// every run's sandbox egresses through it, so egress is filtered OUTSIDE the
// (privileged) sandbox: transparently, by TLS SNI / HTTP Host, no MITM.
//
// The package has two halves:
//
//   - a Host owns the gateway's lifetime and each run's link to it — docker
//     (host_docker.go: a container multi-homed onto per-run --internal
//     networks) or Kubernetes (host_k8s.go: a Deployment the run pods reach
//     over WireGuard peers);
//   - the Manager drives the gateway itself over its control API (policy and
//     run routes in; denials, audit and build-cache facts out), the same way
//     on every host.
//
// Every `project` argument in this package is the project's ID (projects.id).
// envport.ProjectHostKey turns it into the token names use; the project's NAME is
// a label and nothing more.
package egressgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"apothecary/internal/adapters/egressgw/policyfile"
	"apothecary/internal/platform/telemetry"
	"apothecary/internal/ports/envport"
)

// HostRun runs one CLI invocation (docker against the HOST daemon, or kubectl).
type HostRun func(ctx context.Context, args []string) (stdout, stderr []byte, code int, err error)

// Control is how to reach one project's gateway control API.
type Control struct {
	BaseURL string // e.g. http://127.0.0.1:49153
	Token   string
}

// Attachment is a run's link to its project gateway.
type Attachment struct {
	// GatewayIP is the gateway's address as the run's sandbox reaches it: its
	// DNS server, image mirror and build cache live there.
	GatewayIP string
	// Subnet is the source range the run's traffic arrives from (attribution).
	Subnet string
	// Peer is the run's WireGuard identity: its public key and tunnel address.
	Peer *policyfile.Peer
	// WGEndpoint (host:port) is where the run's sandbox sends its WireGuard
	// traffic. WGSecret is the Secret holding its WireGuard identity
	// (Kubernetes); on docker the engine links the sandbox itself (LinkSandbox)
	// with the private half and the gateway's public key.
	WGEndpoint   string
	WGSecret     string
	WGPrivateKey string
	WGGatewayKey string
}

// Host is where a project's gateway runs and how runs are linked to it.
type Host interface {
	// EnsureGateway brings the project's gateway up (creating or upgrading
	// it) and returns its control API once it answers.
	EnsureGateway(ctx context.Context, project, projectName string) (Control, error)
	// Control finds a running gateway's API without creating anything; ok is
	// false when the project has no gateway (never ran, or removed).
	Control(ctx context.Context, project string) (Control, bool)
	// AttachRun links a run to the (running) gateway.
	AttachRun(ctx context.Context, project, projectName, run string) (Attachment, error)
	// DetachRun undoes AttachRun. Best-effort, idempotent.
	DetachRun(ctx context.Context, project, run string) error
}

// SandboxLinker is a Host whose engine brings the run's tunnel up inside the
// sandbox (docker). A Kubernetes pod does that itself (its wg sidecar).
type SandboxLinker interface {
	LinkSandbox(ctx context.Context, att Attachment, sandbox string) error
}

// LinkSandbox makes the run's WireGuard tunnel the running sandbox's default
// route. A no-op on hosts whose sandboxes link themselves.
func (m *Manager) LinkSandbox(ctx context.Context, att Attachment, sandbox string) error {
	l, ok := m.host.(SandboxLinker)
	if !ok || att.Peer == nil {
		return nil
	}
	return l.LinkSandbox(ctx, att, sandbox)
}

// ErrNoGateway: the project has no gateway to ask.
var ErrNoGateway = errors.New("egress gateway is not running for this project")

type Manager struct {
	host   Host
	client *http.Client

	pmu   map[string]*sync.Mutex // per-project: attach/detach and policy edits
	pmuMu sync.Mutex

	auditMu    sync.Mutex
	auditCache map[string]auditSnap
}

type auditSnap struct {
	at   time.Time
	recs []policyfile.AuditRecord
}

func NewManager(host Host) *Manager {
	return &Manager{
		host:       host,
		client:     &http.Client{Transport: telemetry.HTTPTransport(http.DefaultTransport), Timeout: 20 * time.Second},
		pmu:        map[string]*sync.Mutex{},
		auditCache: map[string]auditSnap{},
	}
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

// gwName is the gateway's name on every host: apo-gw-<project key>. Never the
// project's name — a rename must not move a project off its gateway and
// caches, and two projects with the same name must not share them.
func gwName(project string) string { return "apo-gw-" + envport.ProjectHostKey(project) }

// --- lifecycle ------------------------------------------------------------

// EnsureRunEgress guarantees the project gateway is up, links the run to it,
// registers the run's route (attribution, and its WireGuard peer on
// Kubernetes) and installs the project policy — all before it returns, so the
// sandbox about to start has a filter, a resolver and a policy waiting.
// Returns the run's attachment (the gateway's address as the run sees it, and
// on Kubernetes its WireGuard identity). Idempotent.
func (m *Manager) EnsureRunEgress(ctx context.Context, project, projectName, run string, f policyfile.File) (Attachment, error) {
	pm := m.projectMu(project)
	pm.Lock()
	defer pm.Unlock()

	ctl, err := m.host.EnsureGateway(ctx, project, projectName)
	if err != nil {
		return Attachment{}, err
	}
	att, err := m.host.AttachRun(ctx, project, projectName, run)
	if err != nil {
		return Attachment{}, err
	}
	if err := m.call(ctx, ctl, http.MethodPut, "/runs/"+url.PathEscape(run),
		policyfile.RunRoute{Subnet: att.Subnet, Peer: att.Peer}, nil); err != nil {
		return Attachment{}, fmt.Errorf("egressgw: register run route: %w", err)
	}
	if f.Mode == "" {
		f.Mode = policyfile.ModeAllow
	}
	if err := m.call(ctx, ctl, http.MethodPut, "/policy", f, nil); err != nil {
		return Attachment{}, fmt.Errorf("egressgw: install policy: %w", err)
	}
	return att, nil
}

// DestroyRunEgress drops the run's route and link. The GATEWAY ITSELF IS KEPT:
// it holds the project's warm image and build caches, far more expensive to
// rebuild than an idle gateway is to keep.
func (m *Manager) DestroyRunEgress(ctx context.Context, project, run string) error {
	pm := m.projectMu(project)
	pm.Lock()
	defer pm.Unlock()
	if ctl, ok := m.host.Control(ctx, project); ok {
		_ = m.call(ctx, ctl, http.MethodDelete, "/runs/"+url.PathEscape(run), nil, nil)
	}
	return m.host.DetachRun(ctx, project, run)
}

// --- policy / denials / audit --------------------------------------------

// Policy is what the gateway is enforcing right now. A project with no
// gateway reads as the default deny-all allowlist.
func (m *Manager) Policy(ctx context.Context, project string) (policyfile.File, error) {
	ctl, ok := m.host.Control(ctx, project)
	if !ok {
		return policyfile.File{Mode: policyfile.ModeAllow}, nil
	}
	var f policyfile.File
	if err := m.call(ctx, ctl, http.MethodGet, "/policy", nil, &f); err != nil {
		return policyfile.File{}, err
	}
	f.Mode = f.NormMode()
	return f, nil
}

// Denials returns the run's recorded denials (nil when unknown).
func (m *Manager) Denials(ctx context.Context, project, run string) []policyfile.Denial {
	ctl, ok := m.host.Control(ctx, project)
	if !ok {
		return nil
	}
	var df policyfile.DenialsFile
	if err := m.call(ctx, ctl, http.MethodGet, "/denials?run="+url.QueryEscape(run), nil, &df); err != nil {
		return nil
	}
	return df.Denials
}

// Audit returns the project's aggregated egress records (all runs). The
// engine's ingester asks once per active run every few seconds, so a project's
// snapshot is reused for a second instead of fetched per run.
func (m *Manager) Audit(ctx context.Context, project string) []policyfile.AuditRecord {
	m.auditMu.Lock()
	if s, ok := m.auditCache[project]; ok && time.Since(s.at) < time.Second {
		m.auditMu.Unlock()
		return s.recs
	}
	m.auditMu.Unlock()
	ctl, ok := m.host.Control(ctx, project)
	if !ok {
		return nil
	}
	var af policyfile.AuditFile
	if err := m.call(ctx, ctl, http.MethodGet, "/audit", nil, &af); err != nil {
		return nil
	}
	m.auditMu.Lock()
	m.auditCache[project] = auditSnap{at: time.Now(), recs: af.Records}
	m.auditMu.Unlock()
	return af.Records
}

// Allow is the "add to egress" fast path (SetRule with allow=true).
func (m *Manager) Allow(ctx context.Context, project, host string) error {
	return m.SetRule(ctx, project, host, true)
}

// SetRule live-toggles a host's egress rule, project-wide, no restart. allow=true
// permits it (clears any force-block, and for allow-mode adds it to the allowlist
// / for deny-mode drops it from the denylist); allow=false force-blocks it via
// the BlockedDomains override, which wins in either mode.
func (m *Manager) SetRule(ctx context.Context, project, host string, allow bool) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return fmt.Errorf("empty host")
	}
	pm := m.projectMu(project)
	pm.Lock()
	defer pm.Unlock()
	ctl, ok := m.host.Control(ctx, project)
	if !ok {
		return ErrNoGateway
	}
	var f policyfile.File
	if err := m.call(ctx, ctl, http.MethodGet, "/policy", nil, &f); err != nil {
		return err
	}
	f = applyRule(f, host, allow)
	return m.call(ctx, ctl, http.MethodPut, "/policy", f, nil)
}

// applyRule is SetRule's pure edit.
func applyRule(f policyfile.File, host string, allow bool) policyfile.File {
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
	if f.Mode == "" {
		f.Mode = policyfile.ModeAllow
	}
	return f
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

// --- extension gallery -------------------------------------------------------

// GalleryPort is the gateway-local port of its Open VSX gallery proxy: the
// VS Code extension marketplace a run's code-server is pointed at, cached
// per project on the gateway's state volume (workspace/gateway/gallery.go).
const GalleryPort = "5002"

// GalleryURL is the gallery proxy's base URL as seen from inside a run's
// sandbox; gwIP is the gateway's address as THAT run reaches it.
func GalleryURL(gwIP string) string { return "http://" + gwIP + ":" + GalleryPort }

// --- build cache -------------------------------------------------------------

// BuildCachePort is the gateway-local port of the writable build-cache
// registry — what a run's buildkit pushes to and pulls from.
const BuildCachePort = "5001"

// BuildCacheRef / BuildCacheImageRef are the two registry references a
// project's devcontainer build reads, as seen from inside a run's sandbox.
// gwIP is the gateway's address as THAT run reaches it.
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

// buildCacheRepo keys the repository by envport.ProjectHostKey(project) — the
// lowercased project id, which is also all the distribution spec allows in a
// path component. Not the name: a rename must not orphan the cache.
func buildCacheRepo(gwIP, project string) string {
	return gwIP + ":" + BuildCachePort + "/" + envport.ProjectHostKey(project) + "/devcontainer"
}

// BuildCacheSize reports how many bytes the project's build cache occupies.
func (m *Manager) BuildCacheSize(ctx context.Context, project string) (int64, error) {
	ctl, ok := m.host.Control(ctx, project)
	if !ok {
		return 0, ErrNoGateway
	}
	var bc policyfile.BuildCacheInfo
	if err := m.call(ctx, ctl, http.MethodGet, "/buildcache", nil, &bc); err != nil {
		return 0, err
	}
	return bc.Bytes, nil
}

// ClearBuildCache empties the project's build cache: the next build starts
// from scratch and repopulates it. The gateway wipes the storage and restarts
// its registry child, so it stops serving manifests whose blobs are gone; a
// build racing the wipe loses its cache (ignore-error on export), never fails.
func (m *Manager) ClearBuildCache(ctx context.Context, project string) error {
	pm := m.projectMu(project)
	pm.Lock()
	defer pm.Unlock()
	ctl, ok := m.host.Control(ctx, project)
	if !ok {
		return ErrNoGateway
	}
	return m.call(ctx, ctl, http.MethodDelete, "/buildcache", nil, nil)
}

// --- control API client ----------------------------------------------------

// call does one control-API request: in (when non-nil) is the JSON body, out
// (when non-nil) receives the JSON answer.
func (m *Manager) call(ctx context.Context, ctl Control, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(ctl.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ctl.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("egress gateway %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("egress gateway %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// WaitHealthy polls the gateway's /healthz until it answers or ctx/limit ends.
func WaitHealthy(ctx context.Context, client *http.Client, baseURL string, limit time.Duration) error {
	if client == nil {
		client = &http.Client{Transport: telemetry.HTTPTransport(http.DefaultTransport), Timeout: 3 * time.Second}
	}
	deadline := time.Now().Add(limit)
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/healthz", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("healthz: %s", resp.Status)
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("egress gateway at %s did not become healthy within %s: %w", baseURL, limit, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
