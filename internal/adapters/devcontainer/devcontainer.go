// Package devcontainer adapts the EnvProvisioner port to the reference
// @devcontainers/cli (devcontainers/cli, Microsoft-backed) plus the local
// docker CLI: the workspace IS the target repo's own devcontainer. This is
// the "run whatever the repo defines" provisioner — the CLI discovers
// .devcontainer/devcontainer.json (or .devcontainer.json) in the checkout
// and builds/runs exactly that (image, Dockerfile or compose, features,
// lifecycle hooks); repos without one fall back to a generated config
// around the §8 pipeline image.
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
// SECURITY POSTURE: the §11 egress firewall is brought to repo-authored
// containers by an injected devcontainer feature (workspace/feature/) that
// contributes capAdd NET_ADMIN + a container-start entrypoint running the
// shared init-firewall.sh, driven per run by /opt/apothecary/firewall.env.
// Repos with no devcontainer use the §8 pipeline image, which carries the
// firewall natively (capAdd + a blocking postStartCommand). Two cases stay
// egress-open (reported truthfully via ports.EgressReporter, envcheck
// skips the firewall invariants): COMPOSE devcontainers, whose primary
// service legitimately reaches sibling services by name that a default-
// deny firewall would break; and the explicit per-project firewall=off
// escape hatch (or a base image with no supported package manager, which
// fails the feature build closed). A non-root container user makes the
// feature entrypoint's iptables fail closed — a documented limitation.
package devcontainer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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

	"apothecary/internal/ports"
	"apothecary/workspace"
)

// idLabel is the docker label key that ties a container to its apothecary
// environment; the value is the workspace name.
const idLabel = "apothecary.env"

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
	// ProxyPort is the engine-side egress proxy (M18), bridged the same
	// way. 0 = proxy mode off. NOTE: with no workspace firewall the proxy
	// filters only tools that honor proxy env — see the security posture
	// note above.
	ProxyPort int
	// ShimBin builds (cached) and returns the host path of the static
	// in-workspace shim binary (wsimage.EnsureShim), bind-mounted into
	// every workspace at /opt/apothecary/shim. Required.
	ShimBin func(ctx context.Context) (string, error)
	// FirewallOff disables the §11 egress firewall for repo-authored dev
	// containers. Default (false) = firewall ON — the safe default. Off =
	// no NET_ADMIN, no init-firewall, egress open apart from engine-side
	// proxy filtering: the escape hatch for base images the firewall
	// toolchain cannot install on. (Compose configs are always left open
	// regardless — their primary service reaches sibling services by name.)
	FirewallOff bool
}

type Adapter struct {
	cfg        Config
	runner     CmdRunner
	retryDelay time.Duration

	// listen is swapped in tests.
	listen func(network, addr string) (net.Listener, error)

	bridgeMu sync.Mutex
	bridges  map[ports.EnvID][]net.Listener // engine-side unix listeners per env

	// fwMu guards the per-env firewall fact (whether THIS workspace runs
	// firewalled). Recorded at Provision, read by EgressOpen and rebuilt
	// from the on-disk marker after an engine restart.
	fwMu       sync.Mutex
	firewalled map[ports.EnvID]bool
}

// CmdRunner executes one CLI invocation (devcontainer, docker or git);
// swapped in tests. tee, when non-nil, receives stdout+stderr AS THEY
// ARRIVE (live exec log, ports.ExecLogTailer).
type CmdRunner interface {
	Run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer) (stdout, stderr []byte, exitCode int, err error)
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
	return &Adapter{
		cfg:        cfg,
		runner:     &cliRunner{},
		retryDelay: time.Second,
		listen:     net.Listen,
		bridges:    map[ports.EnvID][]net.Listener{},
		firewalled: map[ports.EnvID]bool{},
	}
}

// NewWithRunner is the test constructor.
func NewWithRunner(cfg Config, r CmdRunner) *Adapter {
	a := New(cfg)
	a.runner = r
	return a
}

type cliRunner struct{}

func (c *cliRunner) Run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	// GIT_TERMINAL_PROMPT=0: the engine-side clone must fail, not hang,
	// on missing/invalid credentials. Inert for docker/devcontainer.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
func (a *Adapter) run(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer) ([]byte, []byte, int, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		stdout, stderr, code, err := a.runner.Run(ctx, bin, args, stdin, tee)
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

// WorkspaceName derives the environment name from the run id (same scheme
// as the coder adapter: "apothecary-" + the ULID's random tail).
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
	ctx, cancel := context.WithTimeout(ctx, a.cfg.ProvisionTimeout)
	defer cancel()
	name := WorkspaceName(spec.RunID)

	src, err := a.ensureSrc(ctx, name, spec)
	if err != nil {
		return "", err
	}
	shimDir, err := a.ensureShim(ctx, name)
	if err != nil {
		return "", err
	}

	// Firewall decision (§11 for repo-authored containers): on by default,
	// but OFF for compose configs (the primary service legitimately reaches
	// sibling services by name, which a default-deny firewall would break)
	// and OFF when the project turned it off. Recorded per env so envcheck
	// and EgressOpen report the truth.
	hasConfig := hasRepoConfig(src)
	firewall := !a.cfg.FirewallOff
	if firewall && hasConfig && a.isCompose(ctx, src) {
		firewall = false
	}

	args := []string{"up",
		"--workspace-folder", src,
		"--id-label", label(name),
		"--mount", "type=bind,source=" + shimDir + ",target=/opt/apothecary",
		"--log-format", "json",
	}
	// Repos without a devcontainer run the §8 pipeline image via a
	// generated fallback config (which carries iptables + init-firewall.sh
	// already); repos WITH one are honored untouched, with the firewall
	// injected as a devcontainer feature when enabled.
	if !hasConfig {
		if spec.Image == "" {
			return "", fmt.Errorf("devcontainer: repo has no .devcontainer config and the spec carries no pipeline image (projects using this provisioner cannot set workspace.template)")
		}
		override, werr := a.writeFallbackConfig(name, spec.Image, firewall, spec)
		if werr != nil {
			return "", werr
		}
		args = append(args, "--override-config", override)
	} else if firewall {
		if err := a.materializeFeature(name, src); err != nil {
			return "", err
		}
		// Per-run firewall config rides firewall.env in the mounted shim dir
		// (NOT feature options — those would bust the build cache per run);
		// the feature entrypoint sources it at container start.
		if err := a.writeFirewallEnv(name, spec); err != nil {
			return "", err
		}
		args = append(args, "--additional-features", `{"./apothecary-firewall":{}}`)
	}
	stdout, stderr, code, err := a.run(ctx, a.cfg.DevcontainerBin, args, nil, nil)
	if err != nil {
		return "", err
	}
	res, perr := parseUpResult(stdout)
	if code != 0 || perr != nil || res.Outcome != "success" {
		return "", fmt.Errorf("devcontainer up %s: exit %d: %s%s", name, code,
			strings.TrimSpace(res.Message+" "+res.Description), tail("\n"+string(stderr), 2048))
	}
	a.recordFirewall(ports.EnvID(name), firewall)

	if err := a.rootInit(ctx, res); err != nil {
		return "", err
	}
	// Engine bridge + in-container relay: loopback parity for the shim
	// (and proxy) before anything runs in the workspace.
	if err := a.ensureBridge(ports.EnvID(name)); err != nil {
		return "", err
	}
	if err := a.startRelay(ctx, name); err != nil {
		return "", err
	}
	if err := a.waitReady(ctx, name, firewall); err != nil {
		return "", err
	}
	return ports.EnvID(name), nil
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
	src := filepath.Join(a.folderFor(name), "src")
	if _, err := os.Stat(filepath.Join(src, ".git")); err == nil {
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

// writeFallbackConfig generates the minimal config for repos without one:
// the §8 pipeline image, which already carries the apothecary user, shim
// binary, iptables and init-firewall.sh (with its sudoers grant). The
// firewall rides containerEnv + a BLOCKING postStartCommand — the pipeline
// image's own mechanism, no feature and (since up waits on lifecycle
// hooks) no start-race.
func (a *Adapter) writeFallbackConfig(name, image string, firewall bool, spec ports.EnvSpec) (string, error) {
	cfg := map[string]any{"image": image}
	if firewall {
		cfg["capAdd"] = []string{"NET_ADMIN"}
		cfg["containerEnv"] = firewallEnvVars(a.proxyAddr(), spec)
		cfg["postStartCommand"] = "sudo /usr/local/bin/init-firewall.sh"
	}
	path := filepath.Join(a.folderFor(name), "override.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o644)
}

// proxyAddr is the workspace-loopback egress proxy address (M18) or "".
func (a *Adapter) proxyAddr() string {
	if a.cfg.ProxyPort > 0 {
		return fmt.Sprintf("127.0.0.1:%d", a.cfg.ProxyPort)
	}
	return ""
}

// firewallEnvVars are the APOTHECARY_* inputs init-firewall.sh reads,
// shared by the fallback (containerEnv) and feature (firewall.env) paths.
// APOTHECARY_CODER_PORT=1 is a dead port: the script's gateway hole is a
// Coder concept, and the devcontainer bridge rides unix sockets, so point
// it at reserved port 1 rather than leaving Coder's 7080 default open.
func firewallEnvVars(proxyAddr string, spec ports.EnvSpec) map[string]string {
	return map[string]string{
		"APOTHECARY_REPO_URL":      spec.RepoURL,
		"APOTHECARY_EXTRA_DOMAINS": strings.Join(spec.EgressDomains, " "),
		"APOTHECARY_EGRESS_PROXY":  proxyAddr,
		"APOTHECARY_CODER_PORT":    "1",
	}
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

// materializeFeature writes the embedded firewall feature (plus the
// single-sourced init-firewall.sh from BaseContext) into the checkout at
// .devcontainer/apothecary-firewall/ and git-excludes it, so
// `--additional-features './apothecary-firewall'` resolves relative to the
// config dir without the agent ever seeing (or committing) it.
func (a *Adapter) materializeFeature(name, src string) error {
	dir := filepath.Join(src, ".devcontainer", "apothecary-firewall")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := fs.WalkDir(workspace.Feature, "feature", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := fs.ReadFile(workspace.Feature, p)
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
	// init-firewall.sh is single-sourced from the base context.
	fw, err := fs.ReadFile(workspace.BaseContext, "init-firewall.sh")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "init-firewall.sh"), fw, 0o755); err != nil {
		return err
	}
	return gitExclude(src, ".devcontainer/apothecary-firewall/")
}

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

// writeFirewallEnv writes the per-run APOTHECARY_* config the feature
// entrypoint sources at container start (the mounted shim dir surfaces it
// at /opt/apothecary/firewall.env).
func (a *Adapter) writeFirewallEnv(name string, spec ports.EnvSpec) error {
	dir := filepath.Join(a.folderFor(name), "shim")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	env := firewallEnvVars(a.proxyAddr(), spec)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, shellQuote(env[k]))
	}
	return os.WriteFile(filepath.Join(dir, "firewall.env"), []byte(b.String()), 0o644)
}

// recordFirewall persists the per-env firewall fact in memory and as an
// on-disk marker, so EgressOpen answers correctly after an engine restart
// (a fresh adapter instance, same RunsDir).
func (a *Adapter) recordFirewall(env ports.EnvID, firewall bool) {
	a.fwMu.Lock()
	a.firewalled[env] = firewall
	a.fwMu.Unlock()
	marker := filepath.Join(a.folderFor(string(env)), "firewalled")
	val := "0"
	if firewall {
		val = "1"
	}
	_ = os.WriteFile(marker, []byte(val), 0o644)
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
func (a *Adapter) rootInit(ctx context.Context, res upResult) error {
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
	_, stderr, code, err := a.run(ctx, a.cfg.DockerBin,
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
func (a *Adapter) waitReady(ctx context.Context, name string, firewall bool) error {
	script := fmt.Sprintf("timeout 3 bash -c 'exec 3<>/dev/tcp/127.0.0.1/%d'", a.cfg.EnginePort)
	if firewall {
		script += " && test -e /run/apothecary-firewall.done"
	}
	for {
		_, _, code, err := a.run(ctx, a.cfg.DevcontainerBin,
			[]string{"exec", "--id-label", label(name), "bash", "-c", script}, nil, nil)
		if err == nil && code == 0 {
			return nil
		}
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			if firewall {
				return fmt.Errorf("workspace %s: exec/relay/firewall not ready before deadline (firewall may have failed closed — check the container)", name)
			}
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
	// The script travels on stdin (bash -s): no CLI quoting pitfalls.
	script := cmd.Script
	if cmd.Dir != "" {
		script = fmt.Sprintf("cd \"$HOME/%s\" || exit 90\n%s", cmd.Dir, cmd.Script)
	}
	// Source the egress proxy env (M18) for EVERY workspace command; no-op
	// when proxy mode is off (empty file).
	script = "[ -s /apothecary/proxy.env ] && { set -a; . /apothecary/proxy.env; set +a; }\n" + script
	tee := a.openExecLog(env, cmd)
	if tee != nil {
		defer tee.Close()
	}
	var teeW io.Writer
	if tee != nil {
		teeW = tee
	}
	stdout, stderr, code, err := a.run(ctx, a.cfg.DevcontainerBin,
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

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (a *Adapter) ReadFile(ctx context.Context, env ports.EnvID, path string) ([]byte, error) {
	p := resolvePath(path)
	script := fmt.Sprintf(`if [ ! -f %s ]; then exit 44; fi; base64 < %s`, p, p)
	stdout, stderr, code, err := a.run(ctx, a.cfg.DevcontainerBin,
		[]string{"exec", "--id-label", label(string(env)), "bash", "-s"}, []byte(script), nil)
	if err != nil {
		return nil, err
	}
	if code == 44 {
		return nil, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
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
	script := fmt.Sprintf(`mkdir -p "$(dirname %s)" && base64 -d > %s && chmod %o %s`, p, p, mode.Perm(), p)
	stdin := script + "\n" + base64.StdEncoding.EncodeToString(data) + "\n"
	_, stderr, code, err := a.run(ctx, a.cfg.DevcontainerBin,
		[]string{"exec", "--id-label", label(string(env)), "bash", "-eo", "pipefail", "-s"}, []byte(stdin), nil)
	if err != nil {
		return err
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

// status maps `docker ps` container state for our label. No container =
// absent (docker is the only state store; the CLI keeps none).
func (a *Adapter) status(ctx context.Context, name string) (ports.EnvStatus, error) {
	stdout, stderr, code, err := a.run(ctx, a.cfg.DockerBin,
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
	stdout, stderr, code, err := a.run(ctx, a.cfg.DockerBin,
		[]string{"ps", "-aq", "--filter", "label=" + label(name)}, nil, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("docker ps: exit %d: %s", code, tail(string(stderr), 512))
	}
	if ids := strings.Fields(string(stdout)); len(ids) > 0 {
		args := append([]string{"rm", "-f"}, ids...)
		if _, stderr, code, err = a.run(ctx, a.cfg.DockerBin, args, nil, nil); err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("docker rm %s: exit %d: %s", name, code, tail(string(stderr), 512))
		}
	}
	// NOTE: side services of compose-based devcontainers are not labeled by
	// the CLI and are not reaped here (documented limitation).
	a.closeBridge(env)
	return os.RemoveAll(a.folderFor(name))
}

// Links surfaces the terminal command; browser-facing app links are the
// engine's business now (ports.WorkspacePortExposer + the port relay —
// container-IP URLs were dead weight for any browser not on the docker
// host itself).
func (a *Adapter) Links(ctx context.Context, env ports.EnvID) ([]ports.AccessLink, error) {
	name := string(env)
	stdout, _, code, err := a.run(ctx, a.cfg.DockerBin,
		[]string{"ps", "--filter", "label=" + label(name), "--format", "{{.Names}}"}, nil, nil)
	if err != nil {
		return nil, err
	}
	cname, _, _ := strings.Cut(strings.TrimSpace(string(stdout)), "\n")
	if code != 0 || cname == "" {
		return nil, fmt.Errorf("workspace %s: no running container", name)
	}
	return []ports.AccessLink{
		{Kind: "terminal", Command: fmt.Sprintf("docker exec -it %s bash", cname)},
	}, nil
}

// declaredPorts reads forwardPorts/appPort from the merged configuration
// (best-effort; repos without a config or with service-qualified entries
// yield nothing).
func (a *Adapter) declaredPorts(ctx context.Context, name string) []int {
	src := filepath.Join(a.folderFor(name), "src")
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
	// The firewall (if any) is already up in the still-running container;
	// only re-probe the engine relay we just rebuilt.
	return a.waitReady(ctx, string(env), false)
}

// EgressOpen (ports.EgressReporter) reports whether THIS workspace runs
// without the apothecary firewall — read from the in-memory fact, or the
// on-disk marker after an engine restart. Unknown env (no marker) defaults
// to firewalled/closed, the safe assumption.
func (a *Adapter) EgressOpen(_ context.Context, env ports.EnvID) (bool, error) {
	a.fwMu.Lock()
	if fw, ok := a.firewalled[env]; ok {
		a.fwMu.Unlock()
		return !fw, nil
	}
	a.fwMu.Unlock()
	data, err := os.ReadFile(filepath.Join(a.folderFor(string(env)), "firewalled"))
	if err != nil {
		return false, nil // no marker: assume firewalled (closed)
	}
	fw := strings.TrimSpace(string(data)) == "1"
	a.fwMu.Lock()
	a.firewalled[env] = fw
	a.fwMu.Unlock()
	return !fw, nil
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
	return nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

var _ ports.EnvProvisioner = (*Adapter)(nil)
var _ ports.WorkspaceReattacher = (*Adapter)(nil)
var _ ports.ExecLogTailer = (*Adapter)(nil)
var _ ports.EgressReporter = (*Adapter)(nil)
var _ ports.WorkspacePortExposer = (*Adapter)(nil)
