package workspace

import (
	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/envport"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// sandboxLabel marks the per-run Docker-in-Docker container on the HOST
// daemon, distinguishing it from a legacy (pre-DinD) workspace container that
// also carries the apothecary.env label there.
const sandboxLabel = "apothecary.sandbox"

// sandboxVolume is the named docker volume backing the inner daemon's
// /var/lib/docker. Kept as a real named volume (not anonymous) so Destroy can
// remove it deterministically and it survives a sandbox restart.
func (a *Adapter) sandboxVolume(name string) string { return name + "-docker" }

// sandboxReadyTimeout bounds the inner-daemon startup poll independently of
// the (much larger) ProvisionTimeout, so a wedged daemon fails fast-ish.
const sandboxReadyTimeout = 90 * time.Second

// hostRun runs a command against the HOST daemon (never the inner one) — the
// sandbox lifecycle must never carry the inner DOCKER_HOST (Invariant C:
// sandbox and workspace share the name string on different daemons, so a
// mis-targeted `docker rm` would delete the wrong one).
func (a *Adapter) hostRun(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	return a.run(ctx, a.cfg.DockerBin, args, nil, nil)
}

// sandboxShapeLabel records how a sandbox was created (its mounts and
// flags), so a sandbox of an older shape is recreated on first use instead of
// failing later on what it lacks — the same mechanism as the gateway's
// apothecary.gw.shape. Shape 2 added the tools-cache parity mount.
const (
	sandboxShapeLabel = "apothecary.sandbox.shape"
	sandboxShape      = "2"
)

// sandboxIDs lists the run's sandbox container ids on the host daemon (0 or 1).
func (a *Adapter) sandboxIDs(ctx context.Context, name string) ([]string, error) {
	return a.sandboxIDsWith(ctx, name)
}

// sandboxIDsWith is sandboxIDs narrowed by extra label filters.
func (a *Adapter) sandboxIDsWith(ctx context.Context, name string, labels ...string) ([]string, error) {
	args := []string{"ps", "-aq", "--filter", "label=" + label(name), "--filter", "label=" + sandboxLabel + "=true"}
	for _, l := range labels {
		args = append(args, "--filter", "label="+l)
	}
	out, stderr, code, err := a.hostRun(ctx, args)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("docker ps: exit %d: %s", code, shell.Tail(string(stderr), 512))
	}
	return strings.Fields(string(out)), nil
}

// sandboxState is the host-daemon state of the run's sandbox container
// ("running"/"created"/"exited"/…), or "" when the daemon answered and there
// is no sandbox (a legacy pre-DinD env, or a fully-destroyed run). A host
// docker failure returns an ERROR — NOT "" — so a transient daemon blip is
// reported as EnvUnknown upstream instead of masquerading as "absent", which
// used to trigger a nuke-and-reprovision of a perfectly live workspace.
func (a *Adapter) sandboxState(ctx context.Context, name string) (string, error) {
	out, _, code, err := a.hostRun(ctx, []string{"ps", "-a",
		"--filter", "label=" + label(name), "--filter", "label=" + sandboxLabel + "=true",
		"--format", "{{.State}}"})
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("docker ps sandbox %s: exit %d", name, code)
	}
	state, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return state, nil
}

// egressNet is the run's --internal docker network (owned by egressgw.Manager);
// the sandbox joins it as its ONLY NIC so all egress must traverse the gateway.
func egressNet(name string) string { return "apo-egress-" + name }

// ensureRuntime verifies the configured sandbox runtime is actually
// registered with the host daemon, once per adapter (successes only — a
// transient docker failure must stay retryable). Without it a typo'd or
// uninstalled APOTHECARY_SANDBOX_RUNTIME surfaces minutes into a run as
// docker's bare "Unknown runtime specified", with nothing pointing at the
// deployment variable that caused it.
func (a *Adapter) ensureRuntime(ctx context.Context) error {
	if a.cfg.SandboxRuntime == "" {
		return nil
	}
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	if a.runtimeOK {
		return nil
	}
	if err := a.checkRuntime(ctx); err != nil {
		return err
	}
	a.runtimeOK = true
	return nil
}

func (a *Adapter) checkRuntime(ctx context.Context) error {
	rt := a.cfg.SandboxRuntime
	out, stderr, code, err := a.hostRun(ctx, []string{"info", "--format", "{{json .Runtimes}}"})
	if err != nil {
		return fmt.Errorf("checking sandbox runtime %q: docker info: %w", rt, err)
	}
	if code != 0 {
		return fmt.Errorf("checking sandbox runtime %q: docker info: exit %d: %s", rt, code, shell.Tail(string(stderr), 256))
	}
	var runtimes map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(out), &runtimes); err != nil {
		return fmt.Errorf("checking sandbox runtime %q: parsing docker info runtimes: %w", rt, err)
	}
	if _, ok := runtimes[rt]; ok {
		return nil
	}
	have := make([]string, 0, len(runtimes))
	for k := range runtimes {
		have = append(have, k)
	}
	sort.Strings(have)
	return fmt.Errorf("APOTHECARY_SANDBOX_RUNTIME=%s: the docker daemon has no runtime by that name (it has: %s) — install it on the HOST (sysbox: https://github.com/nestybox/sysbox/blob/master/docs/user-guide/install.md), or unset the variable to fall back to a --privileged sandbox",
		rt, strings.Join(have, ", "))
}

// ensureSandbox creates (or restarts) the run's nested-daemon container and
// blocks until the inner daemon answers on its unix socket. All docker calls
// here target the HOST daemon (hostRun), never the inner one. When gwIP is set
// (a gateway is active), the sandbox joins the run's --internal network and is
// default-routed to the gateway, so no inner container can bypass egress
// filtering; gwIP=="" keeps the legacy default-bridge behaviour.
func (a *Adapter) ensureSandbox(ctx context.Context, name string, spec envport.EnvSpec, att egressgw.Attachment, toolsCache string) error {
	gwIP := att.GatewayIP
	runRoot := a.folderFor(name)
	sock := a.sockPath(name)
	// Guard the two externally-imposed limits the spike surfaced:
	// (1) dind remounts /tmp as tmpfs and hides bind mounts under it, so a
	//     socket there never appears; (2) a unix socket path must fit sun_path.
	if !a.noSandboxGuard {
		// Cleaned: $TMPDIR (os.TempDir's source) may end in a slash.
		if strings.HasPrefix(runRoot, filepath.Clean(os.TempDir())+string(os.PathSeparator)) || strings.HasPrefix(runRoot, "/tmp/") {
			return fmt.Errorf("devcontainer DinD: RunsDir %q is under the temp dir — dind masks /tmp, so the inner socket can't surface; configure a persistent RunsDir (e.g. /var/lib/apothecary/runs)", runRoot)
		}
		if len(sock) > 104 { // unix sun_path is 108 incl. NUL; keep margin
			return fmt.Errorf("devcontainer DinD: inner socket path %q is too long (%d bytes; unix limit ~108) — shorten RunsDir", sock, len(sock))
		}
	}

	if err := a.ensureRuntime(ctx); err != nil {
		return err
	}

	ids, err := a.sandboxIDs(ctx, name)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		// A sandbox of an older shape lacks a mount `up` needs (docker refuses
		// a bind whose source is missing), and both Rebuild and recovery
		// re-enter here on an existing sandbox: recreate it. The inner
		// /var/lib/docker volume stays, so the image cache and the stopped
		// workspace container survive; `up` restarts the latter.
		cur, err := a.sandboxIDsWith(ctx, name, sandboxShapeLabel+"="+sandboxShape)
		if err != nil {
			return err
		}
		if len(cur) == 0 {
			if _, stderr, code, err := a.hostRun(ctx, append([]string{"rm", "-f"}, ids...)); err != nil {
				return err
			} else if code != 0 {
				return fmt.Errorf("sandbox %s: remove the previous-shape sandbox: %s", name, shell.Tail(string(stderr), 512))
			}
			ids = nil
		}
	}
	switch {
	case len(ids) > 0:
		// Exists (running or stopped): a start is a cheap no-op when already up.
		if _, _, _, err := a.hostRun(ctx, []string{"start", name}); err != nil {
			return err
		}
	default:
		if err := a.createSandbox(ctx, name, spec, gwIP, toolsCache); err != nil {
			return err
		}
	}
	if err := a.waitSandboxReady(ctx, name); err != nil {
		return err
	}
	if gwIP != "" {
		return a.wireSandboxEgress(ctx, name, att)
	}
	return nil
}

// wireSandboxEgress makes the run's WireGuard tunnel to the gateway the
// sandbox's default route and disables IPv6, so every inner container's
// egress exits via the gateway (the --internal network gives the sandbox no
// other route). The tunnel, not a next hop across the bridge: a host with
// bridge netfilter on drops bridged traffic bound outside the subnet
// (egressgw/wg_docker.go). Best-effort ipv6 off.
func (a *Adapter) wireSandboxEgress(ctx context.Context, name string, att egressgw.Attachment) error {
	if err := a.gw.LinkSandbox(ctx, att, name); err != nil {
		return err
	}
	_, _, _, _ = a.hostRun(ctx, []string{"exec", name, "sysctl", "-w", "net.ipv6.conf.all.disable_ipv6=1"})
	return nil
}

// createSandbox runs the sandbox container. toolsCache is the engine-wide
// tools-cache directory (every bundle version), mounted read-only at its own
// path so a workspace can bind-mount the bundle its engine expects.
func (a *Adapter) createSandbox(ctx context.Context, name string, spec envport.EnvSpec, gwIP, toolsCache string) error {
	runRoot := a.folderFor(name)
	// Named volume for the inner /var/lib/docker (idempotent create).
	if _, stderr, code, err := a.hostRun(ctx, []string{"volume", "create",
		"--label", managedLabel + "=true", "--label", label(name), a.sandboxVolume(name)}); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("docker volume create %s: exit %d: %s", a.sandboxVolume(name), code, shell.Tail(string(stderr), 512))
	}

	// A nested dockerd has to mount overlayfs, manage cgroups and program
	// netfilter. Under the default runtime that means --privileged, which
	// hands the sandbox the host kernel: all devices, unmasked /proc + /sys,
	// no seccomp — sandbox root IS host root. A user-namespace runtime
	// (sysbox-runc) provides the same abilities inside a userns, so the flag
	// comes off and the two are mutually exclusive — sysbox rejects
	// --privileged outright.
	args := []string{"run", "-d", "--name", name}
	if a.cfg.SandboxRuntime != "" {
		args = append(args, "--runtime", a.cfg.SandboxRuntime)
	} else {
		args = append(args, "--privileged")
	}
	args = append(args,
		"--label", managedLabel+"=true",
		"--label", label(name),
		"--label", sandboxLabel+"=true",
		"--label", sandboxShapeLabel+"="+sandboxShape,
	)
	if spec.Project != "" {
		args = append(args, "--label", projectLabel+"="+spec.Project)
	}
	if spec.ProjectName != "" {
		args = append(args, "--label", projectNameLabel+"="+spec.ProjectName)
	}
	// When a gateway is active the sandbox's ONLY NIC is the run's --internal
	// network (no external route → no bypass) and its resolver is the gateway.
	if gwIP != "" {
		args = append(args, "--network", egressNet(name), "--dns", gwIP)
	}
	args = append(args,
		"-v", a.sandboxVolume(name)+":/var/lib/docker",
		"-v", runRoot+":"+runRoot, // Invariant A: identical path in host + sandbox
		// The tools bundles, same invariant, read-only: the PARENT of the
		// bundle dirs, so an engine upgrade's new bundle is visible to a
		// Rebuild without recreating the sandbox — and why no bundle dir is
		// ever swept while a sandbox may be running from it.
		"-v", toolsCache+":"+toolsCache+":ro",
		a.cfg.DinDImage,
		// Explicit `dockerd` arg1 makes the entrypoint SKIP its default host
		// injection (which would add an insecure tcp://0.0.0.0:2375 listener,
		// slowing startup and breaking on future docker) while still running
		// its iptables/cgroup setup. We listen only on the parity-mount socket.
		"dockerd", "--host=unix://"+a.sockPath(name),
	)
	// Inner containers resolve external names through the gateway too, so their
	// DNS queries arrive where egress is filtered (compose service-name lookups
	// stay local to the embedded 127.0.0.11 resolver — unaffected). The gateway
	// also hosts the project's pull-through Docker Hub cache, so pulls warm each
	// other across the project's runs instead of every run cold-pulling into its
	// own /var/lib/docker volume; plain HTTP on an --internal network, hence
	// insecure-registry. A down mirror is not a failure mode: dockerd falls back
	// to upstream on its own.
	if gwIP != "" {
		args = append(args, "--dns", gwIP,
			"--registry-mirror=http://"+gwIP+":5000",
			"--insecure-registry="+gwIP+":5000",
			// ...and the writable twin on :5001, where buildkit exports this
			// build's cache so the project's next run does not rebuild from
			// scratch into its own empty daemon.
			"--insecure-registry="+gwIP+":"+egressgw.BuildCachePort)
	}
	if _, stderr, code, err := a.hostRun(ctx, args); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("sandbox run %s: exit %d: %s", name, code, shell.Tail(string(stderr), 512))
	}
	return nil
}

// waitSandboxReady polls until the inner daemon's socket exists and answers.
// The version probe uses an EXPLICIT DOCKER_HOST (not dockerEnv, which would
// race the socket file into/out of existence).
func (a *Adapter) waitSandboxReady(ctx context.Context, name string) error {
	sock := a.sockPath(name)
	deadline := time.Now().Add(sandboxReadyTimeout)
	for {
		// The version probe (not the file's type) is the authoritative gate:
		// a stale non-socket file just makes it keep failing until timeout.
		if _, err := os.Stat(sock); err == nil {
			_, _, code, err := a.run(ctx, a.cfg.DockerBin,
				[]string{"version", "--format", "{{.Server.Version}}"}, nil, nil,
				"DOCKER_HOST=unix://"+sock)
			if err == nil && code == 0 {
				return nil
			}
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandbox daemon for %s never came up within %s (inspect: docker logs %s)", name, sandboxReadyTimeout, name)
		}
	}
}
