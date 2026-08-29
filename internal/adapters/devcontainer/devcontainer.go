// Package devcontainer adapts the EnvProvisioner port to the reference
// @devcontainers/cli (devcontainers/cli, Microsoft-backed) plus the local
// docker CLI: the workspace IS the target repo's own devcontainer. This is
// the "run whatever the repo defines" provisioner — the CLI discovers
// .devcontainer/devcontainer.json (or .devcontainer.json) in the checkout
// and builds/runs exactly that (image, Dockerfile or compose, features,
// lifecycle hooks). A repo without one is unsupported — there is no
// apothecary-built fallback image.
//
// The model inverts the coder/§11 clone order: the workspace definition
// lives in the repo, so the adapter materializes an engine-side checkout
// FIRST (EnvSpec.GitUser/GitPass; the credential rides a temp store file,
// never argv, and never enters the container) and `devcontainer up`
// bind-mounts it as the workspace folder. The §11 contract that the repo
// is at $HOME/repo is preserved with a symlink created by a one-time
// root init, so bootstrap's idempotent clone step no-ops and everything
// downstream (branch, agent-configs, commands, ReadFile paths) is
// unchanged. In-workspace git auth still comes from bootstrap step 3.
//
// The CLI has no stop/list/delete and holds no state beyond docker labels
// (`--id-label`): status, destroy, links and IP/gateway discovery go
// through the docker CLI on the same label. Exec rides `devcontainer exec`
// (script on stdin), which faithfully propagates exit codes and keeps
// stdout/stderr separated — no sentinel tricks needed (verified live
// against @devcontainers/cli 0.86.0 / docker 29).
//
// There are no reverse tunnels (exec is docker-exec transport, not ssh),
// and hardened docker hosts drop container→host IP traffic entirely
// (verified live), so workspace→engine traffic rides the engine bridge
// (bridge.go): unix sockets in the bind-mounted shim dir, pumped to the
// engine's loopback engine-side and re-exposed on the workspace's own
// 127.0.0.1 by a tiny in-container node relay. Loopback parity holds, so
// run.env/proxy.env keep the same addresses as every other provisioner.
// Ports declared by the repo's devcontainer (forwardPorts/appPort) are
// surfaced as Links against the container's own bridge IP: per-instance
// unique, so concurrent runs never contend for host ports (the headless
// CLI does not publish forwardPorts; only appPort / compose `ports:`
// publish, and a fixed-port collision fails the second `devcontainer up`
// loudly).
//
// SECURITY POSTURE: egress is filtered OUTSIDE the workspace by the per-project
// egress gateway (internal/egressgw). The run's DinD sandbox joins a per-run
// --internal docker network with no external route and is default-routed to the
// gateway, so ALL egress — the workspace container AND every compose sibling
// (a database, etc.) — traverses the gateway, which allows/denies by TLS SNI /
// HTTP Host (no MITM). A privileged sandbox cannot bypass it (enforcement lives
// in a separate container + the host bridge). This finally covers compose
// devcontainers, which the old in-container firewall left wide open. The
// per-project firewall=off switch (GatewayOff) is the escape hatch: the sandbox
// then egresses directly on the host bridge, unfiltered. See ports.EgressController.
package devcontainer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"apothecary/internal/egressgw"
	"apothecary/internal/egressgw/policyfile"
	"apothecary/internal/ports"
	"apothecary/workspace"
)

// idLabel is the docker label key that ties a container to its apothecary
// environment; the value is the workspace name.
const idLabel = "apothecary.env"

// Metadata labels stamped on every container at `up` (as --id-label, so they
// land on repo-authored containers too, which we can't inject runArgs into):
// a marker to find ALL apothecary containers regardless of provisioner
// (`docker ps --filter label=apothecary`) and the run's project name. They
// ride the id-label set so they're set at create time, but every lookup keys
// off idLabel alone, so their presence never widens a match.
const (
	managedLabel = "apothecary"
	projectLabel = "apothecary.project"
)

type Config struct {
	// DevcontainerBin is the @devcontainers/cli executable (default
	// "devcontainer"; install with `npm install -g @devcontainers/cli`).
	DevcontainerBin string
	// DockerBin is the docker CLI (default "docker"). The daemon it points
	// at is where workspaces run; remote daemons work via the engine
	// process's DOCKER_HOST.
	DockerBin string
	// RunsDir receives per-env state: src/ (the checkout), shim/, the
	// generated fallback config and exec.log. Empty = system temp dir for
	// state and no exec log.
	RunsDir string
	// ProvisionTimeout bounds clone+build+up+ready (default 15m; first
	// builds of heavy devcontainers can be slow).
	ProvisionTimeout time.Duration
	// EnginePort is the engine's loopback port, re-exposed on every
	// workspace's own 127.0.0.1 through the engine bridge (bridge.go).
	// Default 8080.
	EnginePort int
	// ShimBin builds (cached) and returns the host path of the static
	// in-workspace shim binary (wsimage.EnsureShim), bind-mounted into
	// every workspace at /opt/apothecary/shim. Required.
	ShimBin func(ctx context.Context) (string, error)
	// GatewayImage builds (cached) and returns the tag of the per-project
	// egress gateway image (wsimage.EnsureGatewayImage). Required for egress
	// filtering; nil ⇒ no gateway (the sandbox runs on the default bridge,
	// unfiltered — used by tests and the GatewayOff escape hatch).
	GatewayImage func(ctx context.Context) (string, error)
	// GatewayOff disables the egress gateway for this provisioner (the escape
	// hatch). Default (false) = gateway ON. Off = the sandbox egresses directly
	// via the host bridge with no filtering.
	GatewayOff bool
	// DinDImage is the sandbox image providing the per-run nested docker
	// daemon (default "docker:29-dind"). Each run gets its own daemon so repo
	// compose files run verbatim — container_name/ports collide with nothing
	// — and Destroy is total (the whole inner daemon dies). Override for a
	// mirror or a pinned digest.
	DinDImage string
	// SandboxRuntime is the OCI runtime the per-run sandbox is created with
	// (APOTHECARY_SANDBOX_RUNTIME). Empty (default) = the daemon's default
	// runtime plus --privileged, which is what a nested dockerd needs under
	// plain runc — and which makes sandbox root equivalent to host root.
	// "sysbox-runc" (sysbox installed on the HOST) runs the sandbox
	// unprivileged in a user namespace instead, so a compromised run reaches
	// neither the host, nor its docker socket, nor any other run's state.
	// Verified against the daemon once before first use.
	SandboxRuntime string
}

type Adapter struct {
	cfg        Config
	runner     CmdRunner
	retryDelay time.Duration

	// listen is swapped in tests.
	listen func(network, addr string) (net.Listener, error)
	// noSandboxGuard disables the DinD path guards (temp-dir + sun_path
	// length) so unit tests can drive the scripted sandbox flow from a
	// t.TempDir() RunsDir; the guards themselves have a dedicated test.
	noSandboxGuard bool

	bridgeMu sync.Mutex
	bridges  map[ports.EnvID][]net.Listener // engine-side unix listeners per env

	// gw is the per-project egress gateway manager (nil ⇒ gateway off). It owns
	// the gateway container + per-run --internal networks and the policy/denials
	// files; Provision/Destroy drive its lifecycle.
	gw *egressgw.Manager

	// sibPortsMu guards the last sibling-port scan per env (port -> compose
	// service). Populated by ScanSiblingPorts and consulted by relayTarget so
	// an EXPOSE of an undeclared sibling port routes to the right service.
	sibPortsMu sync.Mutex
	sibPorts   map[ports.EnvID]map[int]string

	// runtimeMu guards runtimeOK, the "cfg.SandboxRuntime is registered with
	// the daemon" check. Only SUCCESS is cached: a transient docker failure
	// must not wedge the provisioner for the process's lifetime.
	runtimeMu sync.Mutex
	runtimeOK bool

	// cpusMu guards the cached host CPU count (see hostCPUs — the docker
	// stats CPUPerc denominator, fetched once per adapter lifetime).
	cpusMu sync.Mutex
	cpus   int
}

// CmdRunner executes one CLI invocation (devcontainer, docker or git);
// swapped in tests. tee, when non-nil, receives stdout+stderr AS THEY
// ARRIVE (live exec log, ports.ExecLogTailer).
type CmdRunner interface {
	Run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) (stdout, stderr []byte, exitCode int, err error)
}

func New(cfg Config) *Adapter {
	if cfg.DevcontainerBin == "" {
		cfg.DevcontainerBin = "devcontainer"
	}
	if cfg.DockerBin == "" {
		cfg.DockerBin = "docker"
	}
	if cfg.ProvisionTimeout == 0 {
		cfg.ProvisionTimeout = 15 * time.Minute
	}
	if cfg.EnginePort == 0 {
		cfg.EnginePort = 8080
	}
	if cfg.DinDImage == "" {
		cfg.DinDImage = "docker:29-dind"
	}
	a := &Adapter{
		cfg:        cfg,
		runner:     &cliRunner{},
		retryDelay: time.Second,
		listen:     net.Listen,
		bridges:    map[ports.EnvID][]net.Listener{},
		sibPorts:   map[ports.EnvID]map[int]string{},
	}
	if cfg.GatewayImage != nil && !cfg.GatewayOff {
		a.gw = egressgw.NewManager(cfg.RunsDir, a.hostRun, cfg.GatewayImage)
	}
	return a
}

// NewWithRunner is the test constructor.
func NewWithRunner(cfg Config, r CmdRunner) *Adapter {
	a := New(cfg)
	a.runner = r
	return a
}

type cliRunner struct{}

func (c *cliRunner) Run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	// GIT_TERMINAL_PROMPT=0: the engine-side clone must fail, not hang,
	// on missing/invalid credentials. Inert for docker/devcontainer. env
	// carries per-call additions (see the Provision `up` call).
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	if tee != nil {
		safe := &syncWriter{w: tee}
		cmd.Stdout = io.MultiWriter(&out, safe)
		cmd.Stderr = io.MultiWriter(&errb, safe)
	} else {
		cmd.Stdout, cmd.Stderr = &out, &errb
	}
	err := cmd.Run()
	code := 0
	if ee, ok := err.(interface{ ExitCode() int }); ok {
		code = ee.ExitCode()
		err = nil // command ran; nonzero exit is a result, not a failure
	}
	return out.Bytes(), errb.Bytes(), code, err
}

// syncWriter serializes writes to a shared sink.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// run executes one CLI call with 3 attempts on spawn failures (the process
// never ran). Nonzero exits are results, never retried: docker reserves
// 125–127 for its own errors and `devcontainer exec` propagates the remote
// command's code verbatim, so a clean exit is authoritative.
func (a *Adapter) run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		stdout, stderr, code, err := a.runner.Run(ctx, bin, args, stdin, tee, env...)
		if err == nil {
			return stdout, stderr, code, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, nil, 0, ctx.Err()
		}
		select {
		case <-time.After(time.Duration(attempt) * a.retryDelay):
		case <-ctx.Done():
			return nil, nil, 0, ctx.Err()
		}
	}
	return nil, nil, 0, fmt.Errorf("%s %s: %w", bin, strings.Join(args[:min(2, len(args))], " "), lastErr)
}

func label(name string) string { return idLabel + "=" + name }

// WorkspaceName is the FALLBACK env name, used only when EnvSpec.Name is
// empty (direct/test callers); real runs get the engine's slug-of-branch
// name. It derives a name from the run id, same scheme as the coder adapter:
// "apothecary-" + the ULID's random tail.
func WorkspaceName(runID string) string {
	id := strings.ToLower(runID)
	if len(id) > 16 {
		id = id[len(id)-16:]
	}
	return "apothecary-" + id
}

func (a *Adapter) folderFor(name string) string {
	base := a.cfg.RunsDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "apothecary-devcontainer")
	}
	return filepath.Join(base, name)
}

// srcDir is the run's checkout — and the devcontainer CLI's "workspace
// folder". Its BASENAME is load-bearing: for COMPOSE-based devcontainers the
// CLI derives the docker-compose project name from it (`<basename>_devcontainer`)
// and there is no flag to override it. When every run checked out to a folder
// literally named "src", all runs collapsed onto one compose project — compose
// reused the FIRST run's containers, our per-run `--id-label` never landed on
// anything, and the next run failed with "no running container". Naming the
// checkout after the workspace keeps each run's compose project distinct.
//
// The legacy <folder>/src layout wins when it is already a checkout, so
// workspaces created before this fix keep resolving to their existing clone.
func (a *Adapter) srcDir(name string) string {
	if legacy := filepath.Join(a.folderFor(name), "src"); isCheckout(legacy) {
		return legacy
	}
	return filepath.Join(a.folderFor(name), name)
}

// --- daemon split (Docker-in-Docker) ----------------------------------------
//
// Each run gets its own nested docker daemon inside a --privileged sandbox
// container (sandbox.go). The workspace's compose then runs verbatim in an
// isolated daemon: container_name/ports collide with nothing, and Destroy is
// total. The engine keeps driving the devcontainer/docker CLIs ENGINE-SIDE;
// they target the inner daemon via DOCKER_HOST pointing at a unix socket the
// inner dockerd creates on the run folder (mounted into the sandbox at the
// IDENTICAL path, so the devcontainer CLI's host-resolved bind sources line
// up in the sandbox fs by construction).

// sockPath is the inner daemon's unix socket, on the run folder (== the
// sandbox's parity mount). The daemon must not live under /tmp (dind masks it)
// and the path must fit a unix sun_path (108) — both guarded in ensureSandbox.
func (a *Adapter) sockPath(name string) string {
	return filepath.Join(a.folderFor(name), "docker.sock")
}

// dockerEnv points the docker/devcontainer CLIs at the run's INNER daemon —
// or returns nil when the run predates the sandbox (no socket file yet), which
// transparently keeps legacy envs and pre-provision probes on the host daemon.
func (a *Adapter) dockerEnv(name string) []string {
	if _, err := os.Stat(a.sockPath(name)); err != nil {
		return nil
	}
	return []string{"DOCKER_HOST=unix://" + a.sockPath(name)}
}

// runInner is a.run against the run's inner daemon (host daemon for legacy
// envs — dockerEnv nil). extraEnv is appended after the daemon selector. Every
// call that acts on the WORKSPACE or its inner containers goes through this;
// sandbox-lifecycle calls (create/start/rm the sandbox itself) use a.run so
// they never carry the inner DOCKER_HOST — see sandbox.go's Invariant C.
func (a *Adapter) runInner(ctx context.Context, name, bin string, args []string, stdin []byte, tee io.Writer, extraEnv ...string) ([]byte, []byte, int, error) {
	return a.run(ctx, bin, args, stdin, tee, append(a.dockerEnv(name), extraEnv...)...)
}

func isCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// --- Provision --------------------------------------------------------------

// upResult is `devcontainer up`'s machine-readable last stdout line.
type upResult struct {
	Outcome               string `json:"outcome"`
	ContainerID           string `json:"containerId"`
	RemoteUser            string `json:"remoteUser"`
	RemoteWorkspaceFolder string `json:"remoteWorkspaceFolder"`
	Message               string `json:"message"`
	Description           string `json:"description"`
}

// Provision is a single idempotent create-or-start: `devcontainer up` with
// our id-label creates the container on first call and restarts/reuses it
// on every later one (crash recovery), re-running the repo's lifecycle
// hooks exactly as the devcontainer spec prescribes.
func (a *Adapter) Provision(ctx context.Context, spec ports.EnvSpec) (ports.EnvID, error) {
	return a.provision(ctx, spec, false, nil)
}

// provision is the shared create-or-recreate core behind Provision and
// Rebuild. removeExisting adds `--remove-existing-container` so the CLI
// `docker rm -f`s any container matching the id-labels first (a from-scratch
// recreate — the Rebuild path). tee, when non-nil, receives the `up` build
// output live (Rebuild streams it into exec.log so the run page shows it).
func (a *Adapter) provision(ctx context.Context, spec ports.EnvSpec, removeExisting bool, tee io.Writer) (ports.EnvID, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.ProvisionTimeout)
	defer cancel()
	// The engine supplies a human-friendly, slug-of-branch name (unique per
	// run via its run-id tail); fall back to the run-id scheme for direct
	// callers. It is the env identity (id-label, checkout dir) and, after the
	// CLI builds the container, the container's docker name (renamed below).
	name := spec.Name
	if name == "" {
		name = WorkspaceName(spec.RunID)
	}

	// Capture provisioning output (clone + devcontainer build) into the same
	// exec.log the UI tails, so init/provision streams live instead of only
	// post-ready commands. Rebuild supplies its own tee; Provision opens the log.
	logW := tee
	if logW == nil && a.cfg.RunsDir != "" {
		if lf := a.openProvisionLog(name); lf != nil {
			defer lf.Close()
			logW = lf
		}
	}
	plog := func(format string, args ...any) {
		if logW != nil {
			fmt.Fprintf(logW, format, args...)
		}
	}
	plog("\n=== apothecary: provisioning %s @ %s ===\n→ preparing source checkout\n", name, time.Now().UTC().Format(time.RFC3339))

	src, err := a.ensureSrc(ctx, name, spec)
	if err != nil {
		return "", err
	}
	// Every workspace runs the repo's OWN devcontainer — apothecary injects its
	// firewall + harness as features. A repo without one is unsupported (the
	// engine preflight already refuses it; this is defense-in-depth).
	if !hasRepoConfig(src) {
		return "", fmt.Errorf("repo has no .devcontainer/devcontainer.json — apothecary requires a devcontainer")
	}
	shimDir, err := a.ensureShim(ctx, name)
	if err != nil {
		return "", err
	}
	// Egress firewall: the run's DinD sandbox routes ALL egress (the workspace
	// AND every compose sibling) through the per-project gateway
	// (internal/egressgw). Bring it up BEFORE the sandbox so the sandbox can join
	// the run's --internal network and default-route to the gateway — which is
	// also why a privileged sandbox can't bypass it (its only NIC has no external
	// route). This finally covers compose devcontainers, which the old in-
	// container firewall left wide open.
	plog("→ bringing up per-run sandbox + egress gateway\n")
	var gwIP string
	if a.gw != nil {
		ip, gerr := a.gw.EnsureRunEgress(ctx, spec.Project, name, policyFor(spec))
		if gerr != nil {
			return "", gerr
		}
		gwIP = ip
		// Record the owning project NOW (not after the build): if the build is
		// blocked on a denied domain, "add to egress" must be able to find this
		// run's gateway mid-provision to unblock it.
		a.recordProject(name, spec.Project)
	}

	// Each run gets its own nested docker daemon (sandbox.go). Everything from
	// here — devcontainer up, exec, docker ps/inspect/rename — targets that
	// inner daemon (via runInner), so the repo's compose runs verbatim with no
	// host-daemon collisions and Destroy is total.
	if err := a.ensureSandbox(ctx, name, spec, gwIP); err != nil {
		return "", err
	}

	args := []string{"up",
		"--workspace-folder", src,
		"--id-label", label(name),
		"--id-label", managedLabel + "=true",
		"--mount", "type=bind,source=" + shimDir + ",target=/opt/apothecary",
		"--log-format", "json",
	}
	if spec.Project != "" {
		args = append(args, "--id-label", projectLabel+"="+spec.Project)
	}
	if removeExisting {
		// Force a from-scratch recreate: the CLI removes any container matching
		// the id-labels before (re)creating. The bind-mounted checkout (with
		// agent commits) is untouched; only the container is rebuilt.
		args = append(args, "--remove-existing-container")
	}
	// The repo is honored untouched; anything apothecary needs rides in as an
	// injected devcontainer feature (harness + code-server). Egress is no longer
	// a feature — it is enforced externally by the per-project gateway, so the
	// workspace container needs no NET_ADMIN and compose siblings are covered too.
	features := map[string]any{}
	// The repo's image has no harness — install it as a feature.
	if len(spec.HarnessInstall) > 0 {
		if err := a.materializeHarnessFeature(name, src, spec.HarnessInstall); err != nil {
			return "", err
		}
		features["./apothecary-harness"] = map[string]any{}
	}
	// code-server (VS Code in the browser) is ALWAYS injected — installed at
	// build time (egress open), started lazily on demand. Extensions = the
	// repo devcontainer's own customizations.vscode.extensions ∪ the project
	// list, baked into the image (cached per list).
	if err := a.materializeCodeServerFeature(name, src, spec.CodeServerExtensions); err != nil {
		return "", err
	}
	features["./apothecary-codeserver"] = map[string]any{}
	if len(features) > 0 {
		blob, err := json.Marshal(features)
		if err != nil {
			return "", err
		}
		args = append(args, "--additional-features", string(blob))
	}
	// The build cache. A run's inner daemon is thrown away with the run, so
	// without this every run rebuilds the devcontainer image from nothing —
	// measured at 77s of a 90s provision on a small project. buildkit exports
	// the cache to the project gateway's writable registry and imports it on
	// the next run, which turns that into seconds.
	//
	// Three things make it safe rather than clever:
	//   * ignore-error: a wiped, restarting or wedged cache registry must cost
	//     a cache miss, never a failed build (verified: the export error is
	//     swallowed and the build exits 0).
	//   * mode=max: intermediate stages too, which is where devcontainer
	//     features and the code-server install live.
	//   * only with a gateway: no gateway, no registry to talk to, so the
	//     flags are simply absent and the build behaves as it always did.
	//
	// Cache EXPORT needs a buildkit driver that supports it; the sandbox's
	// docker:29-dind runs the containerd image store, which is exactly what
	// makes the default docker driver able to (docker's own error message
	// names it as the alternative to switching drivers).
	if gwIP != "" && spec.Project != "" {
		cacheRef := egressgw.BuildCacheRef(gwIP, spec.Project)
		imageRef := egressgw.BuildCacheImageRef(gwIP, spec.Project)
		args = append(args,
			// Both sources, every time: whichever exists restores, and a
			// missing one is a 404 buildkit shrugs at.
			"--cache-from", "type=registry,ref="+imageRef,
			"--cache-from", "type=registry,ref="+cacheRef,
			// Only a single-container devcontainer honors this; a compose one
			// ignores it, and pushBuildCache below is what covers that case.
			"--cache-to", "type=registry,ref="+cacheRef+",mode=max,ignore-error=true")
	}
	// BUILDX_BAKE_ENTITLEMENTS_FS=0: compose-based devcontainers build via
	// `docker buildx bake`, which (buildx >=0.17) gates any fs.read outside
	// the compose dir behind an interactive entitlement prompt. `devcontainer
	// up` runs non-interactively, so the prompt can't be answered and the
	// build aborts with "additional privileges requested". Disabling the
	// filesystem entitlement check restores the pre-0.17 behavior. Scoped to
	// this call so it never affects the other docker/git subprocesses.
	plog("→ building devcontainer (image + features; can take several minutes)\n")
	stdout, stderr, code, err := a.runInner(ctx, name, a.cfg.DevcontainerBin, args, nil, logW, "BUILDX_BAKE_ENTITLEMENTS_FS=0")
	if err != nil {
		return "", err
	}
	res, perr := parseUpResult(stdout)
	if code != 0 || perr != nil || res.Outcome != "success" {
		return "", fmt.Errorf("devcontainer up %s: exit %d: %s%s", name, code,
			strings.TrimSpace(res.Message+" "+res.Description), tail("\n"+string(stderr), 2048))
	}
	if err := a.rootInit(ctx, name, res); err != nil {
		return "", err
	}
	// Engine bridge + in-container relay: loopback parity for the shim before
	// anything runs in the workspace.
	if err := a.ensureBridge(ports.EnvID(name)); err != nil {
		return "", err
	}
	if err := a.startRelay(ctx, name); err != nil {
		return "", err
	}
	if err := a.waitReady(ctx, name); err != nil {
		return "", err
	}
	// The other half of the cache. The CLI builds with BUILDKIT_INLINE_CACHE=1,
	// so the image it just built carries the metadata --cache-from needs — but
	// only if the image outlives this run's daemon, which is what the push is
	// for. It is the ONLY export path a compose devcontainer has.
	a.pushBuildCache(ctx, name, gwIP, spec.Project, res.ContainerID, logW)
	// Last, once the container is up and wired: give it the human-friendly
	// docker name. Nothing above depends on the name (all lookups key off the
	// container ID or the apothecary.env id-label), so it's safe to do last.
	a.renameContainer(ctx, res.ContainerID, name)
	return ports.EnvID(name), nil
}

// renameContainer gives the CLI-built container a human-friendly docker name
// (the run's slug). Cosmetic and best-effort: every lookup keys off the
// apothecary.env id-label, and on reuse the container is already named `name`
// so docker rejects the no-op rename — a non-zero exit is logged, never fatal.
func (a *Adapter) renameContainer(ctx context.Context, containerID, name string) {
	if containerID == "" {
		return
	}
	if _, stderr, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"rename", containerID, name}, nil, nil); err != nil || code != 0 {
		slog.Debug("devcontainer container rename skipped",
			"container", containerID, "name", name, "exit", code, "err", err,
			"stderr", tail(string(stderr), 256))
	}
}

// parseUpResult extracts the result object from up's stdout (last
// non-empty line; --log-format json keeps log lines on stderr).
func parseUpResult(stdout []byte) (upResult, error) {
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	var res upResult
	if len(lines) == 0 {
		return res, fmt.Errorf("empty up output")
	}
	err := json.Unmarshal([]byte(lines[len(lines)-1]), &res)
	return res, err
}

// ensureSrc materializes the engine-side checkout the workspace folder is
// mounted from. An existing checkout is kept (crash recovery; it may hold
// agent commits via the bind mount).
func (a *Adapter) ensureSrc(ctx context.Context, name string, spec ports.EnvSpec) (string, error) {
	src := a.srcDir(name)
	if isCheckout(src) {
		return src, nil
	}
	if err := os.MkdirAll(a.folderFor(name), 0o755); err != nil {
		return "", err
	}
	args := []string{"clone", "--", spec.RepoURL, src}
	if spec.GitPass != "" {
		// The credential rides a mode-0600 store file consumed by git's
		// credential machinery — never argv (ps-visible), never the checkout
		// config, and removed as soon as the clone returns.
		cred, err := credentialLine(spec.RepoURL, spec.GitUser, spec.GitPass)
		if err != nil {
			return "", err
		}
		credPath := filepath.Join(a.folderFor(name), "git-credentials")
		if err := os.WriteFile(credPath, []byte(cred+"\n"), 0o600); err != nil {
			return "", err
		}
		defer os.Remove(credPath)
		args = append([]string{"-c", "credential.helper=store --file=" + credPath}, args...)
	}
	_, stderr, code, err := a.run(ctx, "git", args, nil, nil)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("clone %s: exit %d: %s", spec.RepoURL, code, tail(string(stderr), 2048))
	}
	return src, nil
}

// credentialLine mirrors bootstrap's credential-store entry format.
func credentialLine(repoURL, user, pass string) (string, error) {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("cannot parse repo url %q", repoURL)
	}
	if user == "" {
		user = "oauth2"
	}
	return fmt.Sprintf("%s://%s:%s@%s", u.Scheme, url.QueryEscape(user), url.QueryEscape(pass), u.Host), nil
}

func hasRepoConfig(src string) bool {
	for _, p := range []string{
		filepath.Join(src, ".devcontainer", "devcontainer.json"),
		filepath.Join(src, ".devcontainer.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// policyFor maps an EnvSpec's egress inputs to the gateway policy file. Allow
// mode (the default) uses the derived allowlist; deny mode uses the denylist.
func policyFor(spec ports.EnvSpec) policyfile.File {
	if spec.EgressMode == string(policyfile.ModeDeny) {
		return policyfile.File{
			Mode:           policyfile.ModeDeny,
			Domains:        spec.EgressDenylist,
			BlockedDomains: spec.EgressBlocked,
		}
	}
	return policyfile.File{
		Mode:           policyfile.ModeAllow,
		Domains:        spec.EgressDomains,
		BlockedDomains: spec.EgressBlocked,
		AllowedCIDRs:   spec.EgressAllowedCIDRs,
		AllowedPorts:   spec.EgressAllowedPorts,
	}
}

// recordProject persists which project owns an env (a tiny marker in the run
// folder), so EgressDenials/AllowEgress can locate the per-project gateway after
// an engine restart without a docker call.
func (a *Adapter) recordProject(name, project string) {
	_ = os.WriteFile(filepath.Join(a.folderFor(name), "gwproject"), []byte(project), 0o644)
}

// projectFor reads the env's owning project (from the marker); "" when unknown.
func (a *Adapter) projectFor(name string) string {
	b, err := os.ReadFile(filepath.Join(a.folderFor(name), "gwproject"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// isCompose reports whether the repo's devcontainer is compose-based (its
// merged config carries dockerComposeFile). Best-effort: a read failure is
// treated as non-compose (the common image/Dockerfile case).
func (a *Adapter) isCompose(ctx context.Context, src string) bool {
	stdout, _, code, err := a.run(ctx, a.cfg.DevcontainerBin,
		[]string{"read-configuration", "--workspace-folder", src, "--include-merged-configuration", "--log-format", "json"}, nil, nil)
	if err != nil || code != 0 {
		return false
	}
	var rc struct {
		Configuration       map[string]json.RawMessage `json:"configuration"`
		MergedConfiguration map[string]json.RawMessage `json:"mergedConfiguration"`
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	if len(lines) == 0 || json.Unmarshal([]byte(lines[len(lines)-1]), &rc) != nil {
		return false
	}
	_, a1 := rc.Configuration["dockerComposeFile"]
	_, a2 := rc.MergedConfiguration["dockerComposeFile"]
	return a1 || a2
}

// materializeHarnessFeature writes the harness feature into the repo's
// .devcontainer so `up --additional-features` can install it. A repo-authored
// dev container runs the REPO's image, which knows nothing about
// opencode/codex — without this the first agent stage exits 127.
//
// The static feature (install.sh) owns the Node bootstrap; the per-project
// install commands are generated here into two files it reads:
// harness-binaries (what to probe) and harness-install.sh (guarded installs,
// so a harness already in the image is left alone).
func (a *Adapter) materializeHarnessFeature(name, src string, steps []ports.HarnessInstall) error {
	dir := filepath.Join(src, ".devcontainer", "apothecary-harness")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := fs.WalkDir(workspace.HarnessFeature, "harness-feature", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := fs.ReadFile(workspace.HarnessFeature, p)
		if rerr != nil {
			return rerr
		}
		mode := fs.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(filepath.Join(dir, filepath.Base(p)), data, mode)
	}); err != nil {
		return err
	}

	var bins, reqs, script strings.Builder
	seenReq := map[string]bool{}
	script.WriteString("#!/bin/sh\n# Generated by apothecary — per-project harness installs.\nset -e\n")
	for _, s := range steps {
		if s.Binary != "" {
			bins.WriteString(s.Binary + "\n")
		}
		// Prerequisites are per-harness, so the feature installs only what
		// this project's harnesses actually need — no runtime is assumed.
		for _, r := range s.Requires {
			if r != "" && !seenReq[r] {
				seenReq[r] = true
				reqs.WriteString(r + "\n")
			}
		}
		for _, cmd := range s.Commands {
			if s.Binary == "" {
				fmt.Fprintf(&script, "%s\n", cmd)
				continue
			}
			// Guarded: an image that already ships the harness is untouched.
			fmt.Fprintf(&script, "if command -v %s >/dev/null 2>&1; then\n"+
				"  echo \"apothecary-harness: %s already present\"\nelse\n  %s\nfi\n",
				s.Binary, s.Binary, cmd)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "harness-binaries"), []byte(bins.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "harness-requires"), []byte(reqs.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "harness-install.sh"), []byte(script.String()), 0o755); err != nil {
		return err
	}
	return gitExclude(src, ".devcontainer/apothecary-harness/")
}

// codeServerExtDir is the image-baked, shared extensions dir the code-server
// feature installs into at build time and the engine starts code-server with.
// MUST match workspace/codeserver-feature/install.sh's EXT_DIR.
const codeServerExtDir = "/usr/local/share/apothecary-code-server/extensions"

// materializeCodeServerFeature writes the code-server feature into the repo's
// .devcontainer so `up --additional-features` installs it at build time. The
// extensions the feature pre-installs are the UNION of the repo devcontainer's
// own customizations.vscode.extensions and the project list — sorted+deduped
// into a generated `extensions` file, so an unchanged list is a build-cache hit.
func (a *Adapter) materializeCodeServerFeature(name, src string, projectExts []string) error {
	dir := filepath.Join(src, ".devcontainer", "apothecary-codeserver")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := fs.WalkDir(workspace.CodeServer, "codeserver-feature", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := fs.ReadFile(workspace.CodeServer, p)
		if rerr != nil {
			return rerr
		}
		mode := fs.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(filepath.Join(dir, filepath.Base(p)), data, mode)
	}); err != nil {
		return err
	}

	set := map[string]bool{}
	for _, e := range append(repoExtensions(src), projectExts...) {
		if e = strings.TrimSpace(e); e != "" {
			set[e] = true
		}
	}
	exts := make([]string, 0, len(set))
	for e := range set {
		exts = append(exts, e)
	}
	sort.Strings(exts) // deterministic → stable feature content → stable build cache
	var b strings.Builder
	for _, e := range exts {
		b.WriteString(e + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "extensions"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	return gitExclude(src, ".devcontainer/apothecary-codeserver/")
}

// repoExtensions reads customizations.vscode.extensions straight from the
// repo's devcontainer.json (the author's declared list — not extensions other
// features pull in). Reads the raw file (no CLI call) so it never disturbs the
// provision command sequence; devcontainer.json is JSONC, so comments +
// trailing commas are stripped first. Best-effort: empty on any miss.
func repoExtensions(src string) []string {
	for _, p := range []string{
		filepath.Join(src, ".devcontainer", "devcontainer.json"),
		filepath.Join(src, ".devcontainer.json"),
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var rc struct {
			Customizations struct {
				VSCode struct {
					Extensions []string `json:"extensions"`
				} `json:"vscode"`
			} `json:"customizations"`
		}
		if json.Unmarshal(stripJSONC(data), &rc) == nil {
			return rc.Customizations.VSCode.Extensions
		}
	}
	return nil
}

// stripJSONC turns JSONC into JSON stdlib encoding/json accepts: it removes
// // line and /* */ block comments (respecting string literals) and trailing
// commas before } or ]. Enough for devcontainer.json.
func stripJSONC(data []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++ // land on '/', loop's i++ steps past it
		default:
			out = append(out, c)
		}
	}
	// Trailing commas: ,<ws>} or ,<ws>] → drop the comma.
	return trailingCommaRE.ReplaceAll(out, []byte("$1"))
}

var trailingCommaRE = regexp.MustCompile(`,(\s*[}\]])`)

// gitExclude appends an entry to .git/info/exclude (idempotent) so the
// injected feature is invisible to `git status` and uncommittable.
func gitExclude(src, entry string) error {
	p := filepath.Join(src, ".git", "info", "exclude")
	existing, _ := os.ReadFile(p)
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(entry + "\n")
	return err
}

// ensureShim places the static shim binary (ask_human MCP server + engine
// relay, docker-built via cfg.ShimBin) into the per-env dir bind-mounted
// at /opt/apothecary — the path harness configs (and startRelay) hardcode.
// Written as tmp+rename: overwriting a binary a live relay is executing
// would fail with ETXTBSY, while rename just swaps the directory entry.
func (a *Adapter) ensureShim(ctx context.Context, name string) (string, error) {
	dir := filepath.Join(a.folderFor(name), "shim")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if a.cfg.ShimBin == nil {
		return "", fmt.Errorf("devcontainer: no shim builder configured")
	}
	src, err := a.cfg.ShimBin(ctx)
	if err != nil {
		return "", fmt.Errorf("build shim binary: %w", err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, "shim.tmp")
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return "", err
	}
	return dir, os.Rename(tmp, filepath.Join(dir, "shim"))
}

// rootInit performs the one-time (idempotent) root-level setup the §11
// contract needs inside an arbitrary user image: the /apothecary state dir
// owned by the remote user, and the $HOME/repo symlink onto the mounted
// workspace folder. `devcontainer exec` has no --user, so this one call
// goes through docker directly.
func (a *Adapter) rootInit(ctx context.Context, name string, res upResult) error {
	user := res.RemoteUser
	if user == "" {
		user = "root"
	}
	script := fmt.Sprintf(`set -e
mkdir -p /apothecary
chown %[1]s /apothecary
H="$(getent passwd %[1]s | cut -d: -f6)"
[ -n "$H" ] || H=/root
ln -sfn %[2]s "$H/repo"`, shellQuote(user), shellQuote(res.RemoteWorkspaceFolder))
	_, stderr, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"exec", "-u", "root", res.ContainerID, "sh", "-c", script}, nil, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("workspace init: exit %d: %s", code, tail(string(stderr), 1024))
	}
	return nil
}

// waitReady probes until exec answers AND the relay serves the engine
// port on the workspace's loopback — ready means the shim can reach the
// engine (a TCP connect suffices: the relay accepts, engine reachability
// end-to-end is envcheck's tunnel step). When firewalled, it ALSO waits
// for /run/apothecary-firewall.done: the feature entrypoint brings the
// firewall up asynchronously at container start, and fail-closed writes no
// lock — so a missing done-file must time out into env_failed rather than
// run agents under half-applied (or open) egress. (Loopback is ACCEPTed
// first, so the relay probe keeps working under a DROP policy.)
func (a *Adapter) waitReady(ctx context.Context, name string) error {
	script := fmt.Sprintf("timeout 3 bash -c 'exec 3<>/dev/tcp/127.0.0.1/%d'", a.cfg.EnginePort)
	for {
		_, _, code, err := a.runInner(ctx, name, a.cfg.DevcontainerBin,
			[]string{"exec", "--id-label", label(name), "bash", "-c", script}, nil, nil)
		if err == nil && code == 0 {
			return nil
		}
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return fmt.Errorf("workspace %s: exec/relay not ready before deadline", name)
		}
	}
}

// --- Exec / files -----------------------------------------------------------

func (a *Adapter) Exec(ctx context.Context, env ports.EnvID, cmd ports.Command) (ports.ExecResult, error) {
	if cmd.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cmd.Timeout)
		defer cancel()
	}
	// The script travels on stdin (bash -s): no CLI quoting pitfalls. Egress is
	// filtered externally by the gateway, so commands need no proxy-env sourcing.
	script := cmd.Script
	if cmd.Dir != "" {
		script = fmt.Sprintf("cd \"$HOME/%s\" || exit 90\n%s", cmd.Dir, cmd.Script)
	}
	tee := a.openExecLog(env, cmd)
	if tee != nil {
		defer tee.Close()
	}
	var teeW io.Writer
	if tee != nil {
		teeW = tee
	}
	stdout, stderr, code, err := a.runInner(ctx, string(env), a.cfg.DevcontainerBin,
		[]string{"exec", "--id-label", label(string(env)), "bash", "-eo", "pipefail", "-s"},
		[]byte(script), teeW)
	if err != nil {
		if ctx.Err() != nil {
			return ports.ExecResult{}, ctx.Err()
		}
		return ports.ExecResult{}, err
	}
	output := string(stdout)
	if len(stderr) > 0 {
		output += string(stderr)
	}
	return ports.ExecResult{ExitCode: code, Output: output}, nil
}

// resolvePath applies the port's path convention: relative = repo-root.
// Shell-quoted — repo-controlled names must never be interpretable.
func resolvePath(path string) string {
	if strings.HasPrefix(path, "/") {
		return shellQuote(path)
	}
	return `"$HOME"/repo/` + shellQuote(path)
}

// escapeExit is the exit code the containment guard uses for a repo-relative
// path that resolves outside the checkout.
const escapeExit = 45

// containmentGuard emits a shell prelude that refuses a repo-relative path
// whose RESOLVED location escapes the checkout (PLAN5 M22). The flow parser's
// path rules are lexical — they stop "../" but not a symlink, and an agent
// writes into the repo, so `artifacts/plan.md -> /apothecary/run.env` would
// otherwise hand the run token to a prompt or a gate.
//
// Absolute paths are engine-controlled (/apothecary/...) and pass through.
func containmentGuard(path string) string {
	if strings.HasPrefix(path, "/") {
		return ""
	}
	p := resolvePath(path)
	// readlink -f resolves every symlink in the path and does not require the
	// leaf to exist, so this guards writes as well as reads.
	return fmt.Sprintf(
		`__root=$(cd "$HOME"/repo 2>/dev/null && pwd -P) || exit %[1]d; `+
			`__real=$(readlink -f -- %[2]s) || exit %[1]d; `+
			`case "$__real" in "$__root"/*) ;; *) exit %[1]d ;; esac; `,
		escapeExit, p)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (a *Adapter) ReadFile(ctx context.Context, env ports.EnvID, path string) ([]byte, error) {
	p := resolvePath(path)
	script := containmentGuard(path) + fmt.Sprintf(`if [ ! -f %s ]; then exit 44; fi; base64 < %s`, p, p)
	stdout, stderr, code, err := a.runInner(ctx, string(env), a.cfg.DevcontainerBin,
		[]string{"exec", "--id-label", label(string(env)), "bash", "-s"}, []byte(script), nil)
	if err != nil {
		return nil, err
	}
	if code == 44 {
		return nil, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
	}
	if code == escapeExit {
		return nil, fmt.Errorf("read %s: resolves outside the repository checkout (symlink escape)", path)
	}
	if code != 0 {
		return nil, fmt.Errorf("read %s: exit %d: %s", path, code, tail(string(stderr), 512))
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(stdout), "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("read %s: decode: %w", path, err)
	}
	return data, nil
}

func (a *Adapter) WriteFile(ctx context.Context, env ports.EnvID, path string, data []byte, mode fs.FileMode) error {
	p := resolvePath(path)
	// One-line script + payload on the same stdin: bash consumes the first
	// line, base64 -d consumes the rest.
	script := containmentGuard(path) +
		fmt.Sprintf(`mkdir -p "$(dirname %s)" && base64 -d > %s && chmod %o %s`, p, p, mode.Perm(), p)
	stdin := script + "\n" + base64.StdEncoding.EncodeToString(data) + "\n"
	_, stderr, code, err := a.runInner(ctx, string(env), a.cfg.DevcontainerBin,
		[]string{"exec", "--id-label", label(string(env)), "bash", "-eo", "pipefail", "-s"}, []byte(stdin), nil)
	if err != nil {
		return err
	}
	if code == escapeExit {
		return fmt.Errorf("write %s: resolves outside the repository checkout (symlink escape)", path)
	}
	if code != 0 {
		return fmt.Errorf("write %s: exit %d: %s", path, code, tail(string(stderr), 512))
	}
	return nil
}

// --- Status / Destroy / Links -----------------------------------------------

func (a *Adapter) Status(ctx context.Context, env ports.EnvID) (ports.EnvStatus, error) {
	return a.status(ctx, string(env))
}

// status reports the env's liveness in two stages: the SANDBOX on the host
// daemon gates the WORKSPACE on the inner daemon. A run with no sandbox is a
// legacy (pre-DinD) env — it keeps the original host-label query — or absent.
func (a *Adapter) status(ctx context.Context, name string) (ports.EnvStatus, error) {
	sbx, err := a.sandboxState(ctx, name)
	if err != nil {
		// Host daemon didn't answer — return the error so the reconciler
		// records EnvUnknown and recovery WAITS, rather than reading "" as
		// absent and re-provisioning a live env from scratch.
		return "", err
	}
	switch sbx {
	case "":
		// No sandbox. Pre-DinD envs (no inner socket) resolve on the host
		// daemon as before; a DinD env whose sandbox is gone is absent.
		if a.dockerEnv(name) == nil {
			return a.workspaceState(ctx, name, false)
		}
		return ports.EnvAbsent, nil
	case "running":
		return a.workspaceState(ctx, name, true) // sandbox up → query the inner workspace
	case "created", "restarting":
		return ports.EnvStarting, nil
	default: // exited, paused, dead, removing
		return ports.EnvStopped, nil
	}
}

// workspaceState maps the workspace container's `docker ps` state for our
// id-label. inner selects the daemon: the inner one when the sandbox is up,
// the host one for legacy envs.
func (a *Adapter) workspaceState(ctx context.Context, name string, inner bool) (ports.EnvStatus, error) {
	q := a.run
	if inner {
		q = func(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
			return a.runInner(ctx, name, bin, args, stdin, tee, env...)
		}
	}
	stdout, stderr, code, err := q(ctx, a.cfg.DockerBin,
		[]string{"ps", "-a", "--filter", "label=" + label(name), "--format", "{{.State}}"}, nil, nil)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("docker ps: exit %d: %s", code, tail(string(stderr), 512))
	}
	state, _, _ := strings.Cut(strings.TrimSpace(string(stdout)), "\n")
	switch state {
	case "":
		return ports.EnvAbsent, nil
	case "running":
		return ports.EnvRunning, nil
	case "created", "restarting":
		return ports.EnvStarting, nil
	default: // exited, paused, dead, removing
		return ports.EnvStopped, nil
	}
}

func (a *Adapter) Destroy(ctx context.Context, env ports.EnvID) error {
	name := string(env)
	ids, err := a.sandboxIDs(ctx, name)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		// DinD env: removing the sandbox tears down the inner daemon and with
		// it EVERY workspace + compose side service + network inside — the
		// side-service leak that plagued the shared-daemon design is gone by
		// construction. The named /var/lib/docker volume goes next.
		if _, stderr, code, err := a.hostRun(ctx, append([]string{"rm", "-f"}, ids...)); err != nil {
			return err
		} else if code != 0 {
			return fmt.Errorf("docker rm %s sandbox: exit %d: %s", name, code, tail(string(stderr), 512))
		}
		if _, stderr, code, err := a.hostRun(ctx, []string{"volume", "rm", "-f", a.sandboxVolume(name)}); err != nil {
			return err
		} else if code != 0 {
			return fmt.Errorf("docker volume rm %s: exit %d: %s", a.sandboxVolume(name), code, tail(string(stderr), 512))
		}
	} else {
		// Legacy (pre-DinD) env: the workspace container lives on the host
		// daemon; remove it by id-label (its compose side services were never
		// labeled and are not reaped — the old, documented limitation).
		stdout, stderr, code, err := a.run(ctx, a.cfg.DockerBin,
			[]string{"ps", "-aq", "--filter", "label=" + label(name)}, nil, nil)
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("docker ps: exit %d: %s", code, tail(string(stderr), 512))
		}
		if wids := strings.Fields(string(stdout)); len(wids) > 0 {
			if _, stderr, code, err = a.run(ctx, a.cfg.DockerBin, append([]string{"rm", "-f"}, wids...), nil, nil); err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("docker rm %s: exit %d: %s", name, code, tail(string(stderr), 512))
			}
		}
	}
	// Tear down this run's egress network (reads the project marker before
	// RemoveAll). The project gateway itself stays up — it keeps the warm image
	// cache for the project's next run.
	if a.gw != nil {
		_ = a.gw.DestroyRunEgress(ctx, a.projectFor(name), name)
	}
	a.closeBridge(env)
	return os.RemoveAll(a.folderFor(name))
}

// Links surfaces the terminal command; browser-facing app links are the
// engine's business now (ports.WorkspacePortExposer + the port relay —
// container-IP URLs were dead weight for any browser not on the docker
// host itself).
func (a *Adapter) Links(ctx context.Context, env ports.EnvID) ([]ports.AccessLink, error) {
	name := string(env)
	stdout, _, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"ps", "--filter", "label=" + label(name), "--format", "{{.Names}}"}, nil, nil)
	if err != nil {
		return nil, err
	}
	cname, _, _ := strings.Cut(strings.TrimSpace(string(stdout)), "\n")
	if code != 0 || cname == "" {
		return nil, fmt.Errorf("workspace %s: no running container", name)
	}
	// The workspace lives on the run's inner daemon, so the human's copy-paste
	// must point docker there — a plain `docker exec` would hit the host
	// daemon and not find it.
	prefix := ""
	if a.dockerEnv(name) != nil {
		prefix = "DOCKER_HOST=unix://" + a.sockPath(name) + " "
	}
	return []ports.AccessLink{
		{Kind: "terminal", Command: fmt.Sprintf("%sdocker exec -it %s bash", prefix, cname)},
	}, nil
}

// declaredPorts reads forwardPorts/appPort from the merged configuration
// (best-effort; repos without a config or with service-qualified entries
// yield nothing).
func (a *Adapter) declaredPorts(ctx context.Context, name string) []int {
	src := a.srcDir(name)
	stdout, _, code, err := a.run(ctx, a.cfg.DevcontainerBin,
		[]string{"read-configuration", "--workspace-folder", src, "--include-merged-configuration", "--log-format", "json"}, nil, nil)
	if err != nil || code != 0 {
		return nil
	}
	var rc struct {
		Configuration       map[string]any `json:"configuration"`
		MergedConfiguration map[string]any `json:"mergedConfiguration"`
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	if len(lines) == 0 || json.Unmarshal([]byte(lines[len(lines)-1]), &rc) != nil {
		return nil
	}
	set := map[int]bool{}
	for _, cfg := range []map[string]any{rc.MergedConfiguration, rc.Configuration} {
		if cfg == nil {
			continue
		}
		collectPorts(cfg["forwardPorts"], set)
		collectPorts(cfg["appPort"], set)
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// collectPorts accepts the spec's port shapes: number, numeric string,
// or arrays of either ("host:port" service refs are skipped).
func collectPorts(v any, set map[int]bool) {
	switch t := v.(type) {
	case float64:
		set[int(t)] = true
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			set[n] = true
		}
	case []any:
		for _, e := range t {
			collectPorts(e, set)
		}
	}
}

// --- optional capabilities --------------------------------------------------

// Reattach (ports.WorkspaceReattacher): the bridge listeners and the
// in-container relay both died with the previous engine process — rebuild
// them for the still-running workspace. Idempotent (ensureBridge is a
// no-op for a live env; the relay exits quietly if one already serves).
func (a *Adapter) Reattach(ctx context.Context, env ports.EnvID) error {
	st, err := a.status(ctx, string(env))
	if err != nil {
		return err
	}
	if st != ports.EnvRunning {
		return fmt.Errorf("workspace %s is %s, not running", env, st)
	}
	// Refresh the mounted shim too: a workspace provisioned by an older
	// engine build must get the current binary before the relay starts.
	if _, err := a.ensureShim(ctx, string(env)); err != nil {
		return err
	}
	if err := a.ensureBridge(env); err != nil {
		return err
	}
	if err := a.startRelay(ctx, string(env)); err != nil {
		return err
	}
	// The gateway (a separate host container) kept filtering throughout the
	// engine restart; only re-probe the engine relay we just rebuilt.
	return a.waitReady(ctx, string(env))
}

// EgressDenials (ports.EgressController) returns this run's blocked hosts, read
// from the per-project gateway's denials file.
func (a *Adapter) EgressDenials(_ context.Context, env ports.EnvID) []ports.EgressDenial {
	if a.gw == nil {
		return nil
	}
	project := a.projectFor(string(env))
	out := []ports.EgressDenial{}
	for _, d := range a.gw.Denials(project, string(env)) {
		out = append(out, ports.EgressDenial{Host: d.Host, Count: d.Count, Last: d.Last})
	}
	return out
}

// EgressAudit (ports.EgressAuditor) returns this run's aggregated egress trail
// from the per-project gateway (filtered to the run).
func (a *Adapter) EgressAudit(_ context.Context, env ports.EnvID) []ports.EgressAuditRecord {
	if a.gw == nil {
		return nil
	}
	out := []ports.EgressAuditRecord{}
	for _, r := range a.gw.Audit(a.projectFor(string(env))) {
		if r.Run != string(env) {
			continue
		}
		out = append(out, ports.EgressAuditRecord{
			Host: r.Host, DstIP: r.DstIP, Port: r.Port, Proto: r.Proto, Verdict: r.Verdict,
			Count: r.Count, BytesOut: r.BytesOut, BytesIn: r.BytesIn, First: r.First, Last: r.Last,
		})
	}
	return out
}

// AllowEgress (ports.EgressController) adds a host to the run's project policy
// live — the gateway hot-reloads it, so the next request succeeds with no
// restart. Project-wide by design (the policy is per project).
func (a *Adapter) AllowEgress(_ context.Context, env ports.EnvID, host string) error {
	if a.gw == nil {
		return fmt.Errorf("egress gateway is off for this provisioner")
	}
	return a.gw.Allow(a.projectFor(string(env)), host)
}

// SetEgressRule (ports.EgressController) live-toggles a host allow/deny.
func (a *Adapter) SetEgressRule(_ context.Context, env ports.EnvID, host string, allow bool) error {
	if a.gw == nil {
		return fmt.Errorf("egress gateway is off for this provisioner")
	}
	return a.gw.SetRule(a.projectFor(string(env)), host, allow)
}

// EgressPolicy (ports.EgressController) reads back the project policy the
// gateway is enforcing right now — the "current rule" side of the run page's
// firewall UI.
func (a *Adapter) EgressPolicy(_ context.Context, env ports.EnvID) (ports.EgressPolicy, error) {
	if a.gw == nil {
		return ports.EgressPolicy{}, fmt.Errorf("egress gateway is off for this provisioner")
	}
	f, err := a.gw.Policy(a.projectFor(string(env)))
	if err != nil {
		return ports.EgressPolicy{}, err
	}
	return ports.EgressPolicy{
		Mode:           string(f.NormMode()),
		Domains:        f.Domains,
		BlockedDomains: f.BlockedDomains,
	}, nil
}

// Close stops every engine-side bridge listener (engine shutdown;
// workspaces keep running and Reattach rebuilds bridges on the next boot).
func (a *Adapter) Close() {
	a.bridgeMu.Lock()
	defer a.bridgeMu.Unlock()
	for env, lns := range a.bridges {
		for _, ln := range lns {
			_ = ln.Close()
		}
		delete(a.bridges, env)
	}
}

// openProvisionLog opens (append) the env's exec.log — the same file the UI
// tails via TailExecLog — so the clone + devcontainer build stream live during
// provisioning, before the env is ready. nil when RunsDir is unset or open fails.
func (a *Adapter) openProvisionLog(name string) *os.File {
	dir := a.folderFor(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "exec.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	return f
}

// openExecLog opens the env's live exec log and writes the entry header;
// nil when RunsDir is unset or the open fails.
func (a *Adapter) openExecLog(env ports.EnvID, cmd ports.Command) *os.File {
	if a.cfg.RunsDir == "" {
		return nil
	}
	dir := filepath.Join(a.cfg.RunsDir, string(env))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "exec.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	fmt.Fprintf(f, "--- %s [dir=%s]\n%s\n", time.Now().UTC().Format(time.RFC3339), cmd.Dir, cmd.Script)
	return f
}

// TailExecLog implements the optional ports.ExecLogTailer capability.
func (a *Adapter) TailExecLog(_ context.Context, env ports.EnvID, n int) ([]byte, error) {
	if a.cfg.RunsDir == "" {
		return nil, fmt.Errorf("live exec log: runs dir is not configured")
	}
	path := filepath.Join(a.cfg.RunsDir, string(env), "exec.log")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		n = 64 << 10
	}
	off := max(info.Size()-int64(n), 0)
	buf := make([]byte, info.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil, err
	}
	return buf, nil
}

// Test is the cheap probe (DESIGN2 §4): both CLIs answer. All local.
func (a *Adapter) Test(ctx context.Context) error {
	if _, _, code, err := a.run(ctx, a.cfg.DevcontainerBin, []string{"--version"}, nil, nil); err != nil || code != 0 {
		return fmt.Errorf("devcontainer CLI not runnable (%q): %v — install with `npm install -g @devcontainers/cli`", a.cfg.DevcontainerBin, err)
	}
	_, stderr, code, err := a.run(ctx, a.cfg.DockerBin, []string{"version", "--format", "{{.Server.Version}}"}, nil, nil)
	if err != nil {
		return fmt.Errorf("docker CLI not runnable (%q): %v", a.cfg.DockerBin, err)
	}
	if code != 0 {
		return fmt.Errorf("docker daemon unreachable: %s", tail(string(stderr), 512))
	}
	// An opted-in sandbox runtime is a host capability, so the "Test" button
	// is the right place to find out it isn't installed — not the first run.
	return a.ensureRuntime(ctx)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// Rebuild (ports.WorkspaceRebuilder) recreates the workspace container in
// place, keeping the engine-side checkout (agent commits survive — it's a
// bind mount that Destroy never touches). Non-compose: `up
// --remove-existing-container` recreates the single container while keeping
// the inner daemon + image cache warm. Compose: the only total recreate is a
// DinD sandbox teardown + fresh provision (the flag's single-container scope
// can't cleanly recreate a multi-service project). Build output streams into
// exec.log so the run page's workspace-log poll shows it live.
func (a *Adapter) Rebuild(ctx context.Context, spec ports.EnvSpec) error {
	name := spec.Name
	if name == "" {
		name = WorkspaceName(spec.RunID)
	}
	tee := a.openExecLog(ports.EnvID(name), ports.Command{Script: "# devcontainer rebuild"})
	if tee != nil {
		defer tee.Close()
	}
	src := a.srcDir(name)
	if a.isCompose(ctx, src) {
		if err := a.Destroy(ctx, ports.EnvID(name)); err != nil {
			return fmt.Errorf("rebuild %s: teardown: %w", name, err)
		}
		_, err := a.provision(ctx, spec, false, tee)
		return err
	}
	_, err := a.provision(ctx, spec, true, tee)
	return err
}

var _ ports.EnvProvisioner = (*Adapter)(nil)
var _ ports.WorkspaceReattacher = (*Adapter)(nil)
var _ ports.ExecLogTailer = (*Adapter)(nil)
var _ ports.EgressController = (*Adapter)(nil)
var _ ports.EgressAuditor = (*Adapter)(nil)
var _ ports.WorkspacePortExposer = (*Adapter)(nil)
var _ ports.WorkspaceRebuilder = (*Adapter)(nil)
var _ ports.EditorStarter = (*Adapter)(nil)

// --- build cache (ports.BuildCacheController) --------------------------------

// BuildCache reports the size of the project's devcontainer build cache, held
// by its gateway's writable registry.
func (a *Adapter) BuildCache(ctx context.Context, project string) (ports.BuildCacheInfo, error) {
	if a.gw == nil || project == "" {
		return ports.BuildCacheInfo{}, nil
	}
	size, err := a.gw.BuildCacheSize(ctx, project)
	if err != nil {
		// No gateway up yet (a project that has never run) is not an error to
		// show: there is simply nothing cached.
		return ports.BuildCacheInfo{}, nil
	}
	return ports.BuildCacheInfo{Available: true, Bytes: size}, nil
}

// ClearBuildCache empties it. The next build repopulates it, so the only cost
// of being wrong is one slow build.
func (a *Adapter) ClearBuildCache(ctx context.Context, project string) error {
	if a.gw == nil || project == "" {
		return fmt.Errorf("build cache: this project has no gateway to hold one")
	}
	return a.gw.ClearBuildCache(ctx, project)
}

// pushBuildCache publishes the freshly built devcontainer image into the
// project's build-cache registry, so the project's NEXT run restores it
// instead of rebuilding (measured: 77s of a 90s provision was the build).
//
// Best-effort throughout, and deliberately so: every failure here costs a
// slower next build and nothing else, while failing the provision over it
// would trade a real workspace for a cache. The push is cheap after the first
// one — the registry already has every unchanged layer.
func (a *Adapter) pushBuildCache(ctx context.Context, name, gwIP, project, containerID string, logW io.Writer) {
	if gwIP == "" || project == "" || containerID == "" {
		return
	}
	plog := func(format string, args ...any) {
		if logW != nil {
			fmt.Fprintf(logW, format, args...)
		}
	}
	img, _, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"inspect", containerID, "--format", "{{.Config.Image}}"}, nil, nil)
	image := strings.TrimSpace(string(img))
	if err != nil || code != 0 || image == "" {
		plog("→ build cache: could not resolve the built image; next build starts cold\n")
		return
	}
	ref := egressgw.BuildCacheImageRef(gwIP, project)
	if _, _, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"tag", image, ref}, nil, nil); err != nil || code != 0 {
		plog("→ build cache: could not tag %s; next build starts cold\n", image)
		return
	}
	plog("→ saving the build cache for this project's next run\n")
	if _, stderr, code, err := a.runInner(ctx, name, a.cfg.DockerBin,
		[]string{"push", ref}, nil, logW); err != nil || code != 0 {
		plog("→ build cache: push failed (%s); next build starts cold\n",
			strings.TrimSpace(tail(string(stderr), 200)))
	}
}
