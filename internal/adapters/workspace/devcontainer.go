// Package devcontainer adapts the EnvProvisioner port to the reference
// @devcontainers/cli (devcontainers/cli, Microsoft-backed) plus the local
// docker CLI: the workspace IS the target repo's own devcontainer. This is
// the "run whatever the repo defines" provisioner — the CLI discovers
// .devcontainer/devcontainer.json (or .devcontainer.json) in the checkout
// and builds/runs exactly that (image, Dockerfile or compose, features,
// lifecycle hooks). A repo without one is unsupported — there is no
// apothecary-built fallback image.
//
// The model inverts the §11 clone order: the workspace definition
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
package workspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"apothecary/internal/egressgw"
	"apothecary/internal/egressgw/policyfile"
	"apothecary/internal/ports"
	"apothecary/internal/ports/envport"
)

// idLabel is the docker label key that ties a container to its apothecary
// environment; the value is the workspace name.
const idLabel = "apothecary.env"

// Metadata labels stamped on every container at `up` (as --id-label, so they
// land on repo-authored containers too, which we can't inject runArgs into):
// a marker to find ALL apothecary containers regardless of provisioner
// (`docker ps --filter label=apothecary`) and the run's project ID. They
// ride the id-label set so they're set at create time, but every lookup keys
// off idLabel alone, so their presence never widens a match.
//
// projectNameLabel carries the project's human name for `docker ps`. It goes
// ONLY on containers created with --label (the sandbox; the gateway and the
// browser do the same): the CLI matches an existing container on EVERY
// id-label, so a renamed project would stop matching its own workspace
// container and `up` would create a second one beside it.
const (
	managedLabel     = "apothecary"
	projectLabel     = "apothecary.project"
	projectNameLabel = "apothecary.project.name"
)

type Config struct {
	// DevcontainerBin is the @devcontainers/cli executable (default
	// "devcontainer"; install with `npm install -g @devcontainers/cli`).
	DevcontainerBin string
	// DockerBin is the docker CLI (default "docker"). The daemon it points
	// at is where workspaces run; remote daemons work via the engine
	// process's DOCKER_HOST.
	DockerBin string
	// RunsDir receives per-env state: the checkout, shim/, the markers
	// (definition, gwproject, gwip, devcontainer.sum) and exec.log — and,
	// beside the env folders, the engine-wide caches (shim-cache/,
	// tools-cache/, builds/). Empty = system temp dir for state and no exec
	// log.
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
	// ToolsDir builds (cached) and returns the host directory of the tools
	// bundle (wsimage.EnsureTools): every harness CLI and code-server as
	// standalone builds, bind-mounted into every workspace at
	// /opt/apothecary/tools. Required on the docker host.
	ToolsDir func(ctx context.Context) (string, error)
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
	// Kube, when set, runs every sandbox as a pod on a Kubernetes cluster
	// (host_k8s.go) instead of a container on the engine's docker daemon.
	Kube *KubeConfig
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

	// host is where sandboxes run (docker daemon or Kubernetes); defs are the
	// workspace definitions a repo may carry, in detection order.
	host Host
	defs []Definition

	// defByEnv remembers each env's definition (see defFor).
	defMu    sync.Mutex
	defByEnv map[string]Definition

	// listen is swapped in tests.
	listen func(network, addr string) (net.Listener, error)
	// noSandboxGuard disables the DinD path guards (temp-dir + sun_path
	// length) so unit tests can drive the scripted sandbox flow from a
	// t.TempDir() RunsDir; the guards themselves have a dedicated test.
	noSandboxGuard bool

	bridgeMu sync.Mutex
	bridges  map[envport.EnvID][]net.Listener // engine-side unix listeners per env

	// gw is the per-project egress gateway manager (nil ⇒ gateway off). It owns
	// the gateway container + per-run --internal networks and the policy/denials
	// files; Provision/Destroy drive its lifecycle.
	gw *egressgw.Manager

	// sibPortsMu guards the last sibling-port scan per env (port -> compose
	// service). Populated by ScanSiblingPorts and consulted by relayTarget so
	// an EXPOSE of an undeclared sibling port routes to the right service.
	sibPortsMu sync.Mutex
	sibPorts   map[envport.EnvID]map[int]string

	// sidecarMu serializes sidecar starts, so two callers racing on a cold
	// workspace start one (the shim's own singleton guard backs this up).
	sidecarMu sync.Mutex

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
// ARRIVE (live exec log, envport.ExecLogTailer).
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
		bridges:    map[envport.EnvID][]net.Listener{},
		sibPorts:   map[envport.EnvID]map[int]string{},
		defByEnv:   map[string]Definition{},
	}
	a.host = dockerHost{a}
	a.defs = []Definition{devcontainerDef{bin: cfg.DevcontainerBin}}
	if cfg.Kube != nil {
		kh := newK8sHost(a, cfg.Kube)
		a.host = kh
		a.gw = kh.gatewayManager()
		return a
	}
	if cfg.GatewayImage != nil && !cfg.GatewayOff {
		a.gw = egressgw.NewDockerManager(cfg.RunsDir, a.hostRun, cfg.GatewayImage)
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
	return a.runWith(ctx, a.runner, bin, args, stdin, tee, env...)
}

// runWith is run through another runner (a pod exec on Kubernetes), with the
// same spawn-failure retries.
func (a *Adapter) runWith(ctx context.Context, r CmdRunner, bin string, args []string, stdin []byte, tee io.Writer, env ...string) ([]byte, []byte, int, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		stdout, stderr, code, err := r.Run(ctx, bin, args, stdin, tee, env...)
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
// name. It derives a name from the run id: "apothecary-" + the ULID's random
// tail.
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
	return a.host.runInner(ctx, name, bin, args, stdin, tee, extraEnv...)
}

func isCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// --- Provision --------------------------------------------------------------

// Provision is a single idempotent create-or-start: `devcontainer up` with
// our id-label creates the container on first call and restarts/reuses it
// on every later one (crash recovery), re-running the repo's lifecycle
// hooks exactly as the devcontainer spec prescribes.
func (a *Adapter) Provision(ctx context.Context, spec envport.EnvSpec) (envport.EnvID, error) {
	return a.provision(ctx, spec, false, nil)
}

// provision is the shared create-or-recreate core behind Provision and
// Rebuild. removeExisting adds `--remove-existing-container` so the CLI
// `docker rm -f`s any container matching the id-labels first (a from-scratch
// recreate — the Rebuild path). tee, when non-nil, receives the `up` build
// output live (Rebuild streams it into exec.log so the run page shows it).
func (a *Adapter) provision(ctx context.Context, spec envport.EnvSpec, removeExisting bool, tee io.Writer) (envport.EnvID, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.ProvisionTimeout)
	defer cancel()
	// The engine supplies a human-friendly, slug-of-branch name (unique per
	// run via its run-id tail); fall back to the run-id scheme for direct
	// callers. It is the env identity (id-label, checkout dir) and, after the
	// definition builds the container, the container's docker name.
	name := spec.Name
	if name == "" {
		name = WorkspaceName(spec.RunID)
	}

	// Capture provisioning output (clone + build) into the same exec.log the
	// UI tails, so init/provision streams live instead of only post-ready
	// commands. Rebuild supplies its own tee; Provision opens the log.
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

	// The host brings up everything the definition builds on: the checkout,
	// the shim, the egress attachment and the sandbox (its own docker daemon).
	p, err := a.host.prepare(ctx, name, spec, plog, func(ctx context.Context, src string) (Definition, error) {
		return a.detect(ctx, a.host.fs(name), src)
	})
	if err != nil {
		return "", err
	}
	a.recordDefinition(name, p.def)

	ws, err := p.def.Up(ctx, a.defEnv(name, p.src), spec, upOptions{
		shimDir:        p.shimDir,
		toolsDir:       p.toolsDir,
		gwIP:           p.gwIP,
		project:        spec.Project,
		removeExisting: removeExisting,
		noCache:        spec.NoCache,
		log:            logW,
		plog:           plog,
	})
	if err != nil {
		return "", err
	}
	if err := a.rootInit(ctx, name, ws); err != nil {
		return "", err
	}
	// What this container was built from, for the "config changed" hint.
	a.recordConfigDigest(name, p.src, plog)
	// Engine bridge + in-container relay: the shim must reach the engine
	// before anything runs in the workspace.
	if err := a.host.ensureBridge(envport.EnvID(name)); err != nil {
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
	a.pushBuildCache(ctx, name, p.gwIP, spec.Project, ws.ContainerID, logW)
	// Last, once the container is up and wired: give it the human-friendly
	// docker name. Nothing above depends on the name (all lookups key off the
	// container ID or the apothecary.env id-label), so it's safe to do last.
	a.renameContainer(ctx, ws.ContainerID, name)
	return envport.EnvID(name), nil
}

// errNoDefinition is what a repo without any workspace definition gets.
var errNoDefinition = fmt.Errorf("repo has no .devcontainer/devcontainer.json — apothecary requires a devcontainer")

// detect picks the repo's definition (first match wins). A repo without one
// is unsupported — the engine preflight already refuses it; this is
// defense-in-depth.
func (a *Adapter) detect(ctx context.Context, fsys sandboxFS, src string) (Definition, error) {
	for _, d := range a.defs {
		if d.Detect(ctx, fsys, src) {
			return d, nil
		}
	}
	return nil, errNoDefinition
}

// definitionFile records which definition built an env, so an engine restart
// keeps using it without re-detecting (which would cost a sandbox round trip
// on remote hosts, and could flip if the agent added a second definition).
const definitionFile = "definition"

func (a *Adapter) recordDefinition(name string, d Definition) {
	a.defMu.Lock()
	a.defByEnv[name] = d
	a.defMu.Unlock()
	_ = os.WriteFile(filepath.Join(a.folderFor(name), definitionFile), []byte(d.Name()+"\n"), 0o644)
}

// defFor is the definition an env was built with: remembered, recorded, or —
// for envs built before definitions were recorded — the first one.
func (a *Adapter) defFor(name string) Definition {
	a.defMu.Lock()
	defer a.defMu.Unlock()
	if d, ok := a.defByEnv[name]; ok {
		return d
	}
	d := a.defs[0]
	if b, err := os.ReadFile(filepath.Join(a.folderFor(name), definitionFile)); err == nil {
		want := strings.TrimSpace(string(b))
		for _, c := range a.defs {
			if c.Name() == want {
				d = c
				break
			}
		}
	}
	a.defByEnv[name] = d
	return d
}

// defEnv binds a definition to an env: its checkout, the run folder's
// filesystem and a runner aimed at the env's sandbox.
func (a *Adapter) defEnv(name, src string) defEnv {
	return defEnv{
		name: name,
		src:  src,
		fs:   a.host.fs(name),
		run: func(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, extraEnv ...string) ([]byte, []byte, int, error) {
			return a.runInner(ctx, name, bin, args, stdin, tee, extraEnv...)
		},
	}
}

// inWorkspace runs cmd inside the env's workspace container as its remote
// user (the definition's exec), with remoteEnv set.
func (a *Adapter) inWorkspace(ctx context.Context, name string, remoteEnv []string, cmd []string, stdin []byte, tee io.Writer) ([]byte, []byte, int, error) {
	bin, args := a.defFor(name).ExecArgs(name, remoteEnv...)
	return a.runInner(ctx, name, bin, append(args, cmd...), stdin, tee)
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

// ensureSrc materializes the engine-side checkout the workspace folder is
// mounted from. An existing checkout is kept (crash recovery; it may hold
// agent commits via the bind mount).
func (a *Adapter) ensureSrc(ctx context.Context, name string, spec envport.EnvSpec) (string, error) {
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

// policyFor maps an EnvSpec's egress inputs to the gateway policy file. Allow
// mode (the default) uses the derived allowlist; deny mode uses the denylist.
func policyFor(spec envport.EnvSpec) policyfile.File {
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

// recordGateway persists which project (by id) owns an env and the address
// its gateway has on the run's link — two tiny markers in the run folder — so
// EgressDenials/AllowEgress can locate the per-project gateway after an
// engine restart without a docker call, and StartEditor can point
// code-server at the gateway's extension gallery. gwIP is "" when the run
// has no gateway (the marker is then absent).
func (a *Adapter) recordGateway(name, project, gwIP string) {
	folder := a.folderFor(name)
	_ = os.MkdirAll(folder, 0o755)
	_ = os.WriteFile(filepath.Join(folder, "gwproject"), []byte(project), 0o644)
	if gwIP == "" {
		_ = os.Remove(filepath.Join(folder, "gwip"))
		return
	}
	_ = os.WriteFile(filepath.Join(folder, "gwip"), []byte(gwIP), 0o644)
}

// gatewayIPFor reads the gateway's address on the env's link (from the
// marker); "" when the env has no gateway.
func (a *Adapter) gatewayIPFor(name string) string {
	b, err := os.ReadFile(filepath.Join(a.folderFor(name), "gwip"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// projectFor reads the env's owning project id (from the marker); "" when
// unknown.
func (a *Adapter) projectFor(name string) string {
	b, err := os.ReadFile(filepath.Join(a.folderFor(name), "gwproject"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ensureShim places the static shim binary (ask_human MCP server + engine
// relay, docker-built via cfg.ShimBin) into the per-env dir bind-mounted
// at /opt/apothecary — the path harness configs (and startRelay) hardcode.
// Written as tmp+rename: overwriting a binary a live relay is executing
// would fail with ETXTBSY, while rename just swaps the directory entry.
// Alongside it, the relative symlink apothecary-shim -> shim is the name
// scripts call it by (shimPathPrelude puts the dir on their PATH); relative,
// so it resolves inside the container too, and rename never breaks it.
// Reattach runs this as well, so older workspaces gain the alias.
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
	if err := os.Rename(tmp, filepath.Join(dir, "shim")); err != nil {
		return "", err
	}
	alias := filepath.Join(dir, shimAlias)
	if target, err := os.Readlink(alias); err == nil && target == "shim" {
		return dir, nil
	}
	_ = os.Remove(alias)
	return dir, os.Symlink("shim", alias)
}

// shimAlias is the shim's callable name in workspaces (see ensureShim).
const shimAlias = "apothecary-shim"

// ensureTools builds (cached) the tools bundle on the engine host and returns
// its directory (cfg.ToolsDir); its PARENT is what the sandbox mounts, the
// dir itself is what the workspace mounts at /opt/apothecary/tools.
func (a *Adapter) ensureTools(ctx context.Context) (string, error) {
	if a.cfg.ToolsDir == nil {
		return "", fmt.Errorf("devcontainer: no tools builder configured")
	}
	dir, err := a.cfg.ToolsDir(ctx)
	if err != nil {
		return "", fmt.Errorf("build tools bundle: %w", err)
	}
	return dir, nil
}

// rootInit performs the one-time (idempotent) root-level setup the §11
// contract needs inside an arbitrary user image: the /apothecary state dir
// (and the editor's state under it) owned by the remote user, the $HOME/repo
// symlink onto the mounted workspace folder, and the tools bundle's entries
// linked into /opt/apothecary — the libc variant the image can run, picked by
// probing for musl's loader; the links are absolute container paths into the
// mount, so they stay valid across engine upgrades (the mount target never
// changes) and Rebuild simply relinks. Each harness entry is a wrapper that
// loads provider.env at launch, so a human running omp/codex/opencode in a
// terminal has the run's API keys and omp's broker, as the agent does, for as
// long as the run holds them (the end-of-run scrub deletes the file). The
// editor stays a plain link: code-server lives on past that scrub, and keys
// in its environment would too. /opt/apothecary is APPENDED to every
// Exec'd PATH, so an image that ships its own harness still wins. A musl
// image gets no code-server link (there is no musl build). `devcontainer
// exec` has no --user, so this one call goes through docker directly.
func (a *Adapter) rootInit(ctx context.Context, name string, res Workspace) error {
	user := res.RemoteUser
	if user == "" {
		user = "root"
	}
	script := fmt.Sprintf(`set -e
mkdir -p /apothecary %[3]s
chown %[1]s /apothecary %[3]s
H="$(getent passwd %[1]s | cut -d: -f6)"
[ -n "$H" ] || H=/root
ln -sfn %[2]s "$H/repo"
libc=gnu
if [ -e /lib/ld-musl-x86_64.so.1 ] || [ -e /lib/ld-musl-aarch64.so.1 ]; then libc=musl; fi
for f in /opt/apothecary/tools/$libc/bin/*; do
  [ -e "$f" ] || continue
  b="/opt/apothecary/$(basename "$f")"
  rm -f "$b"
  if [ "$(basename "$f")" = code-server ]; then
    ln -s "$f" "$b"
    continue
  fi
  printf '#!/bin/sh\nset -a\n[ -r /apothecary/provider.env ] && . /apothecary/provider.env\nset +a\nexec %%s "$@"\n' "$f" > "$b"
  chmod 0755 "$b"
done
true`, shellQuote(user), shellQuote(res.WorkspaceFolder), codeServerState)
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

// waitReady probes until exec answers AND the engine answers at the URL the
// workspace will be given — ready means the shim can reach the engine (a TCP
// connect suffices; end-to-end reachability is envcheck's tunnel step). On
// docker that is the in-container relay on the workspace's loopback; on
// Kubernetes it is the engine Service through the run's network policy.
func (a *Adapter) waitReady(ctx context.Context, name string) error {
	host, port := engineHostPort(a.host.engineURL())
	script := fmt.Sprintf("timeout 3 bash -c 'exec 3<>/dev/tcp/%s/%s'", host, port)
	for {
		_, _, code, err := a.inWorkspace(ctx, name, nil, []string{"bash", "-c", script}, nil, nil)
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

// shimPathPrelude puts the bind-mounted shim dir on every Exec'd script's
// PATH, so command stages can call `apothecary-shim jq ...` by name (the
// ensureShim alias). APPENDED, never prepended: the image's own PATH — as
// the CLI's userEnvProbe resolved it — still wins every lookup. The sidecar
// is started through Exec, so its children (harness, terminals) inherit it.
const shimPathPrelude = `export PATH="$PATH:/opt/apothecary"` + "\n"

func (a *Adapter) Exec(ctx context.Context, env envport.EnvID, cmd envport.Command) (envport.ExecResult, error) {
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
	script = shimPathPrelude + script
	tee := a.openExecLog(env, cmd)
	if tee != nil {
		defer tee.Close()
	}
	var teeW io.Writer
	if tee != nil {
		teeW = tee
	}
	var remoteEnv []string
	if cmd.Marker != "" {
		// The marker rides in the environment (the shell's own included, which
		// an export in the script could never do) and the sweep runs first.
		remoteEnv = append(remoteEnv, markerVar+"="+cmd.Marker)
		script = sweepPrelude(cmd.Marker) + script
	}
	stdout, stderr, code, err := a.inWorkspace(ctx, string(env), remoteEnv,
		[]string{"bash", "-eo", "pipefail", "-s"}, []byte(script), teeW)
	if err != nil {
		if ctx.Err() != nil {
			return envport.ExecResult{}, ctx.Err()
		}
		return envport.ExecResult{}, err
	}
	output := string(stdout)
	if len(stderr) > 0 {
		output += string(stderr)
	}
	return envport.ExecResult{ExitCode: code, Output: output}, nil
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

func (a *Adapter) ReadFile(ctx context.Context, env envport.EnvID, path string) ([]byte, error) {
	p := resolvePath(path)
	script := containmentGuard(path) + fmt.Sprintf(`if [ ! -f %s ]; then exit 44; fi; base64 < %s`, p, p)
	stdout, stderr, code, err := a.inWorkspace(ctx, string(env), nil, []string{"bash", "-s"}, []byte(script), nil)
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

// ListFiles lists the regular files under dir, each as "<size> <path>" from
// a POSIX loop rather than GNU find's -printf, so it works on a busybox
// image too. find never follows a symlink, and the directory itself is
// held inside the checkout like a read; a name with a newline in it cannot
// be told apart and is dropped.
func (a *Adapter) ListFiles(ctx context.Context, env envport.EnvID, dir string) ([]envport.FileEntry, error) {
	dir = strings.TrimSuffix(dir, "/")
	p := resolvePath(dir)
	script := containmentGuard(dir) + fmt.Sprintf(
		`if [ ! -d %[1]s ]; then exit 44; fi; cd %[1]s && `+
			`find . -type f -exec sh -c 'for f; do printf "%%s %%s\n" "$(wc -c < "$f" | tr -d " ")" "$f"; done' sh {} + | head -n %[2]d`,
		p, envport.ListFilesLimit)
	stdout, stderr, code, err := a.inWorkspace(ctx, string(env), nil, []string{"bash", "-s"}, []byte(script), nil)
	if err != nil {
		return nil, err
	}
	if code == 44 {
		return nil, fmt.Errorf("%s: %w", dir, fs.ErrNotExist)
	}
	if code == escapeExit {
		return nil, fmt.Errorf("list %s: resolves outside the repository checkout (symlink escape)", dir)
	}
	if code != 0 {
		return nil, fmt.Errorf("list %s: exit %d: %s", dir, code, tail(string(stderr), 512))
	}
	return parseFileList(dir, string(stdout)), nil
}

// parseFileList reads ListFiles' "<size> ./<name>" lines into entries under
// dir, sorted by path.
func parseFileList(dir, out string) []envport.FileEntry {
	var files []envport.FileEntry
	for line := range strings.SplitSeq(out, "\n") {
		size, name, ok := strings.Cut(line, " ")
		n, err := strconv.ParseInt(size, 10, 64)
		if !ok || err != nil || !strings.HasPrefix(name, "./") {
			continue
		}
		files = append(files, envport.FileEntry{Path: dir + "/" + strings.TrimPrefix(name, "./"), Size: n})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func (a *Adapter) WriteFile(ctx context.Context, env envport.EnvID, path string, data []byte, mode fs.FileMode) error {
	p := resolvePath(path)
	// One-line script + payload on the same stdin: bash consumes the first
	// line, base64 -d consumes the rest.
	script := containmentGuard(path) +
		fmt.Sprintf(`mkdir -p "$(dirname %s)" && base64 -d > %s && chmod %o %s`, p, p, mode.Perm(), p)
	stdin := script + "\n" + base64.StdEncoding.EncodeToString(data) + "\n"
	_, stderr, code, err := a.inWorkspace(ctx, string(env), nil, []string{"bash", "-eo", "pipefail", "-s"}, []byte(stdin), nil)
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

func (a *Adapter) Status(ctx context.Context, env envport.EnvID) (envport.EnvStatus, error) {
	return a.status(ctx, string(env))
}

// status reports the env's liveness in two stages: the SANDBOX on the host
// daemon gates the WORKSPACE on the inner daemon. A run with no sandbox is a
// legacy (pre-DinD) env — it keeps the original host-label query — or absent.
func (a *Adapter) status(ctx context.Context, name string) (envport.EnvStatus, error) {
	sbx, legacy, err := a.host.sandboxState(ctx, name)
	if err != nil {
		// Host didn't answer — return the error so the reconciler
		// records EnvUnknown and recovery WAITS, rather than reading "" as
		// absent and re-provisioning a live env from scratch.
		return "", err
	}
	switch sbx {
	case "":
		// No sandbox. Pre-DinD envs (no inner socket) resolve on the host
		// daemon as before; a DinD env whose sandbox is gone is absent.
		if legacy {
			return a.workspaceState(ctx, name, false)
		}
		return envport.EnvAbsent, nil
	case "running":
		return a.workspaceState(ctx, name, true) // sandbox up → query the inner workspace
	case "created", "restarting":
		return envport.EnvStarting, nil
	default: // exited, paused, dead, removing
		return envport.EnvStopped, nil
	}
}

// workspaceState maps the workspace container's `docker ps` state for our
// id-label. inner selects the daemon: the inner one when the sandbox is up,
// the host one for legacy envs.
func (a *Adapter) workspaceState(ctx context.Context, name string, inner bool) (envport.EnvStatus, error) {
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
		return envport.EnvAbsent, nil
	case "running":
		return envport.EnvRunning, nil
	case "created", "restarting":
		return envport.EnvStarting, nil
	default: // exited, paused, dead, removing
		return envport.EnvStopped, nil
	}
}

func (a *Adapter) Destroy(ctx context.Context, env envport.EnvID) error {
	name := string(env)
	if err := a.host.destroySandbox(ctx, name); err != nil {
		return err
	}
	// Tear down this run's egress network (reads the project marker before
	// RemoveAll). The project gateway itself stays up — it keeps the warm image
	// cache for the project's next run.
	if a.gw != nil {
		_ = a.gw.DestroyRunEgress(ctx, a.projectFor(name), name)
	}
	a.host.closeBridge(env)
	return os.RemoveAll(a.folderFor(name))
}

// destroyDockerSandbox removes a docker-host env's sandbox (and with it the
// inner daemon and everything in it), or a legacy env's host-daemon workspace.
func (a *Adapter) destroyDockerSandbox(ctx context.Context, name string) error {
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
	return nil
}

// --- optional capabilities --------------------------------------------------

// Reattach (envport.WorkspaceReattacher): the bridge listeners and the
// in-container relay both died with the previous engine process — rebuild
// them for the still-running workspace. Idempotent (ensureBridge is a
// no-op for a live env; the relay exits quietly if one already serves).
func (a *Adapter) Reattach(ctx context.Context, env envport.EnvID) error {
	st, err := a.status(ctx, string(env))
	if err != nil {
		return err
	}
	if st != envport.EnvRunning {
		return fmt.Errorf("workspace %s is %s, not running", env, st)
	}
	// Refresh the mounted shim too: a workspace provisioned by an older
	// engine build must get the current binary before the relay starts.
	if err := a.host.refreshShim(ctx, string(env)); err != nil {
		return err
	}
	if err := a.host.ensureBridge(env); err != nil {
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
func (a *Adapter) EgressDenials(ctx context.Context, env envport.EnvID) []ports.EgressDenial {
	if a.gw == nil {
		return nil
	}
	project := a.projectFor(string(env))
	out := []ports.EgressDenial{}
	for _, d := range a.gw.Denials(ctx, project, string(env)) {
		out = append(out, ports.EgressDenial{Host: d.Host, Count: d.Count, Last: d.Last})
	}
	return out
}

// EgressAudit (ports.EgressAuditor) returns this run's aggregated egress trail
// from the per-project gateway (filtered to the run).
func (a *Adapter) EgressAudit(ctx context.Context, env envport.EnvID) []ports.EgressAuditRecord {
	if a.gw == nil {
		return nil
	}
	out := []ports.EgressAuditRecord{}
	for _, r := range a.gw.Audit(ctx, a.projectFor(string(env))) {
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
func (a *Adapter) AllowEgress(ctx context.Context, env envport.EnvID, host string) error {
	if a.gw == nil {
		return fmt.Errorf("egress gateway is off for this provisioner")
	}
	return a.gw.Allow(ctx, a.projectFor(string(env)), host)
}

// SetEgressRule (ports.EgressController) live-toggles a host allow/deny.
func (a *Adapter) SetEgressRule(ctx context.Context, env envport.EnvID, host string, allow bool) error {
	if a.gw == nil {
		return fmt.Errorf("egress gateway is off for this provisioner")
	}
	return a.gw.SetRule(ctx, a.projectFor(string(env)), host, allow)
}

// EgressPolicy (ports.EgressController) reads back the project policy the
// gateway is enforcing right now — the "current rule" side of the run page's
// firewall UI.
func (a *Adapter) EgressPolicy(ctx context.Context, env envport.EnvID) (ports.EgressPolicy, error) {
	if a.gw == nil {
		return ports.EgressPolicy{}, fmt.Errorf("egress gateway is off for this provisioner")
	}
	f, err := a.gw.Policy(ctx, a.projectFor(string(env)))
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
func (a *Adapter) Close() { a.host.close() }

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
func (a *Adapter) openExecLog(env envport.EnvID, cmd envport.Command) *os.File {
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

// TailExecLog implements the optional envport.ExecLogTailer capability.
func (a *Adapter) TailExecLog(_ context.Context, env envport.EnvID, n int) ([]byte, error) {
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

// Test is the cheap probe (DESIGN2 §4): the host can run sandboxes.
func (a *Adapter) Test(ctx context.Context) error { return a.host.test(ctx) }

// dockerTest: both CLIs answer and the daemon is up. All local.
func (a *Adapter) dockerTest(ctx context.Context) error {
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

// Rebuild (envport.WorkspaceRebuilder) recreates the workspace container in
// place, keeping the engine-side checkout (agent commits survive — it's a
// bind mount that Destroy never touches). Non-compose: `up
// --remove-existing-container` recreates the single container while keeping
// the inner daemon + image cache warm. Compose: the only total recreate is a
// DinD sandbox teardown + fresh provision (the flag's single-container scope
// can't cleanly recreate a multi-service project). Build output streams into
// exec.log so the run page's workspace-log poll shows it live.
func (a *Adapter) Rebuild(ctx context.Context, spec envport.EnvSpec) error {
	name := spec.Name
	if name == "" {
		name = WorkspaceName(spec.RunID)
	}
	tee := a.openExecLog(envport.EnvID(name), envport.Command{Script: "# devcontainer rebuild"})
	if tee != nil {
		defer tee.Close()
	}
	if a.defFor(name).IsCompose(ctx, a.defEnv(name, a.srcDir(name))) {
		if err := a.Destroy(ctx, envport.EnvID(name)); err != nil {
			return fmt.Errorf("rebuild %s: teardown: %w", name, err)
		}
		_, err := a.provision(ctx, spec, false, tee)
		return err
	}
	_, err := a.provision(ctx, spec, true, tee)
	return err
}

var _ envport.EnvProvisioner = (*Adapter)(nil)
var _ envport.WorkspaceReattacher = (*Adapter)(nil)
var _ envport.ExecLogTailer = (*Adapter)(nil)
var _ ports.EgressController = (*Adapter)(nil)
var _ ports.EgressAuditor = (*Adapter)(nil)
var _ envport.WorkspacePortExposer = (*Adapter)(nil)
var _ envport.WorkspaceRebuilder = (*Adapter)(nil)
var _ envport.EditorStarter = (*Adapter)(nil)

// --- build cache (envport.BuildCacheController) --------------------------------

// BuildCache reports the size of the project's devcontainer build cache, held
// by its gateway's writable registry.
func (a *Adapter) BuildCache(ctx context.Context, project string) (envport.BuildCacheInfo, error) {
	if a.gw == nil || project == "" {
		return envport.BuildCacheInfo{}, nil
	}
	size, err := a.gw.BuildCacheSize(ctx, project)
	if err != nil {
		// No gateway up yet (a project that has never run) is not an error to
		// show: there is simply nothing cached.
		return envport.BuildCacheInfo{}, nil
	}
	return envport.BuildCacheInfo{Available: true, Bytes: size}, nil
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

// engineHostPort splits the workspace-visible engine URL for a /dev/tcp probe.
func engineHostPort(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "127.0.0.1", "8080"
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return u.Hostname(), port
}

// EngineURL implements envport.EngineURLProvider: the address this env's
// workspace reaches the engine at.
func (a *Adapter) EngineURL(envport.EnvID) string { return a.host.engineURL() }

var _ envport.EngineURLProvider = (*Adapter)(nil)
