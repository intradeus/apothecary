package egressgw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"apothecary/internal/ports"
)

// DockerHost runs each project's gateway as a container on the engine host's
// docker daemon (apo-gw-<project key>, CAP_NET_ADMIN, not privileged). Each run
// gets its own --internal network (apo-egress-<run>) that the gateway is
// connected to: the run's sandbox has that network as its ONLY interface and
// default-routes to the gateway, so it cannot bypass it. The control API is
// published on the engine host's loopback only.
type DockerHost struct {
	runsDir     string
	run         HostRun
	ensureImage func(context.Context) (string, error)
	client      *http.Client

	mu  sync.Mutex
	ctl map[string]cachedControl
}

type cachedControl struct {
	c  Control
	at time.Time
}

// gwShape versions the gateway CONTAINER's shape (flags, mounts, published
// ports). A gateway of an older shape is recreated even while runs use it: the
// engine could not drive it at all otherwise.
const gwShape = "2"

const (
	controlPort   = "9041"
	buildCacheDir = "/var/lib/buildcache"
	// controlTTL bounds how long a looked-up control address is trusted: a
	// gateway restarted by the daemon may come back on another host port.
	controlTTL = 10 * time.Second
)

func NewDockerHost(runsDir string, run HostRun, ensureImage func(context.Context) (string, error)) *DockerHost {
	return &DockerHost{
		runsDir: runsDir, run: run, ensureImage: ensureImage,
		client: &http.Client{Timeout: 3 * time.Second},
		ctl:    map[string]cachedControl{},
	}
}

// NewDockerManager is the docker deployment's gateway manager.
func NewDockerManager(runsDir string, run HostRun, ensureImage func(context.Context) (string, error)) *Manager {
	return NewManager(NewDockerHost(runsDir, run, ensureImage))
}

func netName(run string) string { return "apo-egress-" + run }

func (h *DockerHost) gwDir(project string) string {
	return filepath.Join(h.runsDir, "gw", ports.ProjectHostKey(project))
}
func (h *DockerHost) tokenPath(project string) string {
	return filepath.Join(h.gwDir(project), "token")
}

// The gateway's volumes survive recreation: the pull-through image cache, the
// writable build cache (the one a human deletes from the UI), and its state
// (policy, routes, last denials/audit), so a recreated gateway keeps enforcing
// before the engine next talks to it.
func cacheVolume(project string) string      { return gwName(project) + "-cache" }
func buildCacheVolume(project string) string { return gwName(project) + "-buildcache" }
func stateVolume(project string) string      { return gwName(project) + "-state" }

// projectLabels are the docker labels every per-project resource carries: the
// id is what filters key on; the name is for a human reading `docker ps`.
func projectLabels(project, projectName string) []string {
	ls := []string{"--label", "apothecary=true", "--label", "apothecary.project=" + project}
	if projectName != "" {
		ls = append(ls, "--label", "apothecary.project.name="+projectName)
	}
	return ls
}

func (h *DockerHost) docker(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, code, err := h.run(ctx, args)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("docker %s: exit %d: %s", strings.Join(args, " "), code, strings.TrimSpace(string(stderr)))
	}
	return strings.TrimSpace(string(stdout)), nil
}

// token is the project gateway's control token, generated once and kept next
// to the engine's other per-project gateway state.
func (h *DockerHost) token(project string, create bool) (string, error) {
	if b, err := os.ReadFile(h.tokenPath(project)); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	} else if !create {
		return "", fmt.Errorf("no gateway token")
	}
	if err := os.MkdirAll(h.gwDir(project), 0o700); err != nil {
		return "", err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b[:])
	return tok, os.WriteFile(h.tokenPath(project), []byte(tok+"\n"), 0o600)
}

// EnsureGateway brings the project's gateway up, creating it when absent and
// RECREATING it when it runs a stale image or an older container shape. Stale
// image while the project has running sandboxes: the old gateway is kept (an
// `rm -f` would sever their egress) until the project is idle. Older shape:
// recreated regardless, its runs re-attached at their old addresses.
func (h *DockerHost) EnsureGateway(ctx context.Context, project, projectName string) (Control, error) {
	img, err := h.ensureImage(ctx)
	if err != nil {
		return Control{}, fmt.Errorf("egressgw: build gateway image: %w", err)
	}
	name := gwName(project)
	tok, err := h.token(project, true)
	if err != nil {
		return Control{}, err
	}
	ids, err := h.docker(ctx, "ps", "-aq", "--filter", "name=^"+name+"$")
	if err != nil {
		return Control{}, err
	}
	var netIPs map[string]string
	if ids != "" {
		cur, _ := h.docker(ctx, "inspect", name, "--format", `{{.Config.Image}} {{index .Config.Labels "apothecary.gw.shape"}}`)
		curImg, curShape, _ := strings.Cut(cur, " ")
		keep := curImg == img && curShape == gwShape
		if !keep && curShape == gwShape {
			// Stale image only: a busy project keeps its old gateway.
			if running, err := h.docker(ctx, "ps", "-q",
				"--filter", "label=apothecary.project="+project,
				"--filter", "label=apothecary.sandbox=true"); err == nil && running != "" {
				keep = true
			}
		}
		if keep {
			if _, err := h.docker(ctx, "start", name); err != nil { // no-op when running
				return Control{}, err
			}
			return h.controlReady(ctx, project, tok)
		}
		// Stopped sandboxes baked --dns/--registry-mirror at their gateway
		// address and cannot be re-flagged by `docker start`, so the new
		// gateway must land on the same addresses.
		netIPs = h.gatewayNetIPs(ctx, project)
		if _, err := h.docker(ctx, "rm", "-f", name); err != nil {
			return Control{}, err
		}
	}
	for _, vol := range []string{cacheVolume(project), buildCacheVolume(project), stateVolume(project)} {
		args := append([]string{"volume", "create"}, projectLabels(project, projectName)...)
		if _, err := h.docker(ctx, append(args, vol)...); err != nil && !strings.Contains(err.Error(), "already exists") {
			return Control{}, err
		}
	}
	args := append([]string{"run", "-d", "--name", name}, projectLabels(project, projectName)...)
	args = append(args,
		"--label", "apothecary.egressgw=true",
		"--label", "apothecary.gw.shape="+gwShape,
		// Kept warm across host/daemon restarts: nothing reaps a gateway, and
		// a stopped one would leave its runs with no egress until the next
		// provision. A fresh netns also means stale nft rules are impossible.
		"--restart", "unless-stopped",
		"--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW",
		"--network", "bridge",
		"--sysctl", "net.ipv4.ip_forward=1",
		"--sysctl", "net.ipv4.conf.all.rp_filter=0",
		"--sysctl", "net.ipv4.conf.default.rp_filter=0",
		"--sysctl", "net.ipv6.conf.all.disable_ipv6=1",
		// The control API, on the engine host's loopback only (host→container
		// published ports work even where container→host traffic is dropped).
		"-p", "127.0.0.1::"+controlPort,
		"-e", "APO_GW_TOKEN="+tok,
		"-v", stateVolume(project)+":/state",
		"-v", cacheVolume(project)+":/var/lib/registry",
		"-v", buildCacheVolume(project)+":"+buildCacheDir,
		img,
		"--state-dir", "/state",
		"--mirror", "--buildcache", "--buildcache-dir", buildCacheDir,
		"--control-listen", ":"+controlPort,
		"--listen-port", "9040",
		"--dns-upstream", "1.1.1.1:53",
	)
	if _, err = h.docker(ctx, args...); err != nil {
		return Control{}, err
	}
	// Re-attach the runs the replaced gateway served, at their old addresses.
	for nn, ip := range netIPs {
		if _, err := h.docker(ctx, "network", "connect", "--ip", ip, nn, name); err != nil {
			return Control{}, fmt.Errorf("egressgw: gateway upgraded but %s could not be re-attached at %s (its sandbox needs a rebuild): %w", nn, ip, err)
		}
	}
	h.forget(project)
	return h.controlReady(ctx, project, tok)
}

// controlReady resolves the published control port and waits for /healthz.
func (h *DockerHost) controlReady(ctx context.Context, project, tok string) (Control, error) {
	var addr string
	var err error
	for range 20 { // docker publishes the port a moment after `run` returns
		if addr, err = h.publishedControl(ctx, project); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return Control{}, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	if err != nil {
		return Control{}, err
	}
	c := Control{BaseURL: "http://" + addr, Token: tok}
	if err := WaitHealthy(ctx, h.client, c.BaseURL, 30*time.Second); err != nil {
		return Control{}, err
	}
	h.mu.Lock()
	h.ctl[project] = cachedControl{c: c, at: time.Now()}
	h.mu.Unlock()
	return c, nil
}

// publishedControl is the engine-host address docker published the control
// port on (IPv4 loopback).
func (h *DockerHost) publishedControl(ctx context.Context, project string) (string, error) {
	out, err := h.docker(ctx, "port", gwName(project), controlPort+"/tcp")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "127.0.0.1:") {
			return line, nil
		}
	}
	return "", fmt.Errorf("egressgw: %s publishes no loopback control port (%q)", gwName(project), out)
}

func (h *DockerHost) forget(project string) {
	h.mu.Lock()
	delete(h.ctl, project)
	h.mu.Unlock()
}

// Control finds a running gateway's API without creating anything.
func (h *DockerHost) Control(ctx context.Context, project string) (Control, bool) {
	h.mu.Lock()
	if c, ok := h.ctl[project]; ok && time.Since(c.at) < controlTTL {
		h.mu.Unlock()
		return c.c, true
	}
	h.mu.Unlock()
	tok, err := h.token(project, false)
	if err != nil {
		return Control{}, false
	}
	addr, err := h.publishedControl(ctx, project) // fails when not running
	if err != nil {
		return Control{}, false
	}
	c := Control{BaseURL: "http://" + addr, Token: tok}
	h.mu.Lock()
	h.ctl[project] = cachedControl{c: c, at: time.Now()}
	h.mu.Unlock()
	return c, true
}

// AttachRun creates the run's --internal network (no external route → the
// sandbox can't bypass the gateway), connects the gateway to it and reports
// the gateway's address on it and the network's subnet.
func (h *DockerHost) AttachRun(ctx context.Context, project, projectName, run string) (Attachment, error) {
	args := append([]string{"network", "create", "--driver", "bridge", "--internal",
		"--label", "apothecary.env=" + run},
		projectLabels(project, projectName)...)
	if _, err := h.docker(ctx, append(args, netName(run))...); err != nil && !strings.Contains(err.Error(), "already exists") {
		return Attachment{}, err
	}
	if _, err := h.docker(ctx, "network", "connect", netName(run), gwName(project)); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		return Attachment{}, err
	}
	subnet, err := h.docker(ctx, "network", "inspect", netName(run), "--format",
		"{{ (index .IPAM.Config 0).Subnet }}")
	if err != nil {
		return Attachment{}, err
	}
	gwIP, err := h.docker(ctx, "inspect", gwName(project), "--format",
		fmt.Sprintf(`{{ (index .NetworkSettings.Networks "%s").IPAddress }}`, netName(run)))
	if err != nil {
		return Attachment{}, err
	}
	if gwIP == "" {
		return Attachment{}, fmt.Errorf("egressgw: gateway has no IP on %s", netName(run))
	}
	return Attachment{GatewayIP: gwIP, Subnet: subnet}, nil
}

// DetachRun disconnects and removes the run's network.
func (h *DockerHost) DetachRun(ctx context.Context, project, run string) error {
	_, _ = h.docker(ctx, "network", "disconnect", "-f", netName(run), gwName(project))
	_, _ = h.docker(ctx, "network", "rm", netName(run))
	return nil
}

// gatewayNetIPs maps the gateway's run networks to its address on each, so an
// upgrade can restore them. Best-effort: nil when the inspect fails.
func (h *DockerHost) gatewayNetIPs(ctx context.Context, project string) map[string]string {
	out, err := h.docker(ctx, "inspect", gwName(project), "--format",
		`{{range $n, $v := .NetworkSettings.Networks}}{{$n}} {{$v.IPAddress}}{{"\n"}}{{end}}`)
	if err != nil {
		return nil
	}
	ips := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, ip, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && ip != "" && strings.HasPrefix(name, "apo-egress-") {
			ips[name] = ip
		}
	}
	return ips
}

var _ Host = (*DockerHost)(nil)
