// Package coder adapts the EnvProvisioner port to Coder (DESIGN.md §12):
// workspaces are created and driven through the coder CLI (exec over
// `coder ssh`, file transfer base64-wrapped), access links come from the
// REST API. All CLI calls go through one wrapper with 3× retry on transport
// failures. CLI flags were verified against Coder v2.35 — behavior, not
// flags, is normative (§5.2 note applies here too).
package coder

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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"apothecary/internal/ports"
)

type Config struct {
	URL      string // Coder server, e.g. http://localhost:7080
	Token    string // session token (engine-held, never enters workspaces §15)
	Template string // template name, e.g. "apothecary"
	Owner    string // workspace owner login; empty ⇒ resolved from the token (whoami)
	// RunsDir receives exec logs (<run>/... §12); empty disables.
	RunsDir string
	// ProvisionTimeout bounds create+build+agent-ready (default 15m).
	ProvisionTimeout time.Duration
	// EnginePort is reverse-forwarded into every workspace
	// (workspace 127.0.0.1:port -> engine host 127.0.0.1:port) over the
	// Coder agent connection, so workspaces reach the engine without any
	// host firewall exposure. Default 8080.
	EnginePort int
	// ProxyPort is the engine-side egress proxy (M18): reverse-forwarded
	// like EnginePort and passed to the template so the workspace firewall
	// runs in proxy mode. 0 = proxy mode off.
	ProxyPort int
	// ExtraForwards are additional loopback ports forwarded the same way
	// (dev convenience: local git daemon, mock services).
	ExtraForwards []int
}

type Adapter struct {
	cfg        Config
	runner     CmdRunner
	http       *http.Client
	retryDelay time.Duration // base backoff between transport retries

	// tunnelCmd builds the long-lived reverse-forward process; swapped in
	// tests.
	tunnelCmd func(ctx context.Context, ws string) *exec.Cmd
	// pfCmd builds one `coder port-forward` process (workspace port →
	// engine-host loopback, ports.WorkspacePortExposer); swapped in tests.
	pfCmd func(ctx context.Context, ws string, local, remote int) *exec.Cmd

	tunnelMu sync.Mutex
	tunnels  map[ports.EnvID]*tunnel
	forwards map[string]*tunnel // "env|port" -> supervised port-forward

	// ownerName caches the resolved workspace owner login (from whoami) when
	// no username is configured; resolved once, reused for every link.
	ownerMu   sync.Mutex
	ownerName string

	// Engine-pushed template state (template.go): checked/pushed at most
	// once per process.
	tmplMu      sync.Mutex
	tmplEnsured bool
}

type tunnel struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// CmdRunner executes one coder CLI invocation; swapped in tests. tee,
// when non-nil, receives stdout+stderr AS THEY ARRIVE (live exec log,
// ports.ExecLogTailer); the returned buffers are unchanged.
type CmdRunner interface {
	Run(ctx context.Context, args []string, stdin []byte, tee io.Writer) (stdout, stderr []byte, exitCode int, err error)
}

func New(cfg Config) *Adapter {
	// cfg.Owner stays as configured (possibly empty): an empty owner is
	// resolved once from the token via owner()/whoami — the workspaces are
	// owned by the token's user, so @me in browser URLs would 404 for anyone
	// not logged in as exactly that account.
	if cfg.ProvisionTimeout == 0 {
		cfg.ProvisionTimeout = 15 * time.Minute
	}
	if cfg.EnginePort == 0 {
		cfg.EnginePort = 8080
	}
	a := &Adapter{
		cfg:        cfg,
		runner:     &cliRunner{url: cfg.URL, token: cfg.Token},
		http:       &http.Client{Timeout: 30 * time.Second},
		retryDelay: time.Second,
		tunnels:    map[ports.EnvID]*tunnel{},
		forwards:   map[string]*tunnel{},
	}
	a.tunnelCmd = a.defaultTunnelCmd
	a.pfCmd = a.defaultPfCmd
	return a
}

func (a *Adapter) defaultTunnelCmd(ctx context.Context, ws string) *exec.Cmd {
	args := []string{"ssh", ws}
	forwards := append([]int{a.cfg.EnginePort}, a.cfg.ExtraForwards...)
	if a.cfg.ProxyPort > 0 {
		forwards = append(forwards, a.cfg.ProxyPort)
	}
	for _, port := range forwards {
		args = append(args, "--remote-forward", fmt.Sprintf("%d:127.0.0.1:%d", port, port))
	}
	args = append(args, "--", "sleep", "infinity")
	cmd := exec.CommandContext(ctx, "coder", args...)
	cmd.Env = append(os.Environ(),
		"CODER_URL="+a.cfg.URL,
		"CODER_SESSION_TOKEN="+a.cfg.Token,
	)
	return cmd
}

// ensureTunnel starts (once) the supervised reverse-forward for a workspace.
func (a *Adapter) ensureTunnel(env ports.EnvID) {
	a.tunnelMu.Lock()
	defer a.tunnelMu.Unlock()
	if _, ok := a.tunnels[env]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &tunnel{cancel: cancel, done: make(chan struct{})}
	a.tunnels[env] = t
	go a.superviseTunnel(ctx, env, t)
}

func (a *Adapter) superviseTunnel(ctx context.Context, env ports.EnvID, t *tunnel) {
	defer close(t.done)
	for {
		cmd := a.tunnelCmd(ctx, string(env))
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		// Tunnel dropped (agent restart, network blip): the shim retries
		// with backoff meanwhile (§10); we just re-establish.
		_ = err
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (a *Adapter) stopTunnel(env ports.EnvID) {
	a.tunnelMu.Lock()
	t := a.tunnels[env]
	delete(a.tunnels, env)
	a.tunnelMu.Unlock()
	if t != nil {
		t.cancel()
		<-t.done
	}
}

// Close stops all tunnel and port-forward processes (engine shutdown;
// workspaces keep running and tunnels re-establish on the next boot's
// Provision; exposures re-create through the engine's port registry).
func (a *Adapter) Close() {
	a.tunnelMu.Lock()
	envs := make([]ports.EnvID, 0, len(a.tunnels))
	for env := range a.tunnels {
		envs = append(envs, env)
	}
	fwd := make([]*tunnel, 0, len(a.forwards))
	for key, t := range a.forwards {
		fwd = append(fwd, t)
		delete(a.forwards, key)
	}
	a.tunnelMu.Unlock()
	for _, env := range envs {
		a.stopTunnel(env)
	}
	for _, t := range fwd {
		t.cancel()
		<-t.done
	}
}

// --- ports.WorkspacePortExposer ---------------------------------------------

// defaultPfCmd: `coder port-forward <ws> --tcp <local>:<remote>` (binds
// the engine host's loopback; flags verified against Coder v2.35).
func (a *Adapter) defaultPfCmd(ctx context.Context, ws string, local, remote int) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "coder", "port-forward", ws,
		"--tcp", fmt.Sprintf("%d:%d", local, remote))
	cmd.Env = append(os.Environ(),
		"CODER_URL="+a.cfg.URL,
		"CODER_SESSION_TOKEN="+a.cfg.Token,
	)
	return cmd
}

// AppPorts: none to declare — the engine-pushed template's only app is
// code-server, which already routes through Coder's own proxy (Links).
func (a *Adapter) AppPorts(context.Context, ports.EnvID) []int { return nil }

// ExposePort starts (idempotently per env+port) a supervised
// `coder port-forward` subprocess onto a kernel-allocated engine-loopback
// port and hands back a dialer for it. The returned PortConn owns the
// subprocess: closing it (the engine's port registry, at env teardown)
// stops the forward even if this adapter instance was refcount-closed by
// a config edit meanwhile.
func (a *Adapter) ExposePort(_ context.Context, env ports.EnvID, port int) (ports.PortConn, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	local := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // benign race: coder rebinds the freed port

	key := fmt.Sprintf("%s|%d", env, port)
	ctx, cancel := context.WithCancel(context.Background())
	t := &tunnel{cancel: cancel, done: make(chan struct{})}
	a.tunnelMu.Lock()
	if old := a.forwards[key]; old != nil {
		// Replace a stale forward (its local port is unknown to the new
		// PortConn anyway).
		a.tunnelMu.Unlock()
		old.cancel()
		<-old.done
		a.tunnelMu.Lock()
	}
	a.forwards[key] = t
	a.tunnelMu.Unlock()

	go func() {
		defer close(t.done)
		for {
			cmd := a.pfCmd(ctx, string(env), local, port)
			_ = cmd.Run()
			if ctx.Err() != nil {
				return
			}
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}()
	return &pfConn{a: a, key: key, t: t, local: local}, nil
}

type pfConn struct {
	a     *Adapter
	key   string
	t     *tunnel
	local int
}

func (p *pfConn) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", p.local))
}

func (p *pfConn) Close() error {
	p.a.tunnelMu.Lock()
	if p.a.forwards[p.key] == p.t {
		delete(p.a.forwards, p.key)
	}
	p.a.tunnelMu.Unlock()
	p.t.cancel()
	<-p.t.done
	return nil
}

// NewWithRunner is the test constructor.
func NewWithRunner(cfg Config, r CmdRunner) *Adapter {
	a := New(cfg)
	a.runner = r
	return a
}

type cliRunner struct{ url, token string }

func (c *cliRunner) Run(ctx context.Context, args []string, stdin []byte, tee io.Writer) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "coder", args...)
	cmd.Env = append(os.Environ(),
		"CODER_URL="+c.url,
		"CODER_SESSION_TOKEN="+c.token,
	)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	if tee != nil {
		// stdout and stderr write from separate goroutines; serialize the
		// shared tee so interleaved lines stay intact.
		safe := &syncWriter{w: tee}
		cmd.Stdout = io.MultiWriter(&out, safe)
		cmd.Stderr = io.MultiWriter(&errb, safe)
	} else {
		cmd.Stdout, cmd.Stderr = &out, &errb
	}
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
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

// run is the §12 single wrapper: 3 attempts on transport-level failures
// (process failed to run, or ssh transport exit 255). Command-level nonzero
// exits are results and are never retried.
func (a *Adapter) run(ctx context.Context, args []string, stdin []byte) ([]byte, []byte, int, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		stdout, stderr, code, err := a.runner.Run(ctx, args, stdin, nil)
		if err == nil && code != 255 {
			return stdout, stderr, code, nil
		}
		if err == nil {
			lastErr = fmt.Errorf("ssh transport failure (exit 255): %s", tail(string(stderr), 512))
		} else {
			lastErr = err
		}
		if ctx.Err() != nil {
			return nil, nil, 0, ctx.Err()
		}
		select {
		case <-time.After(time.Duration(attempt) * a.retryDelay):
		case <-ctx.Done():
			return nil, nil, 0, ctx.Err()
		}
	}
	return nil, nil, 0, fmt.Errorf("coder %s: %w", strings.Join(args[:min(2, len(args))], " "), lastErr)
}

// execRun is run() without the exit-255 retry: a remote command's own exit
// 255 is indistinguishable from an ssh transport failure, and re-running a
// side-effectful command (a full agent invocation) is worse than reporting
// the ambiguous exit honestly. Spawn failures (nothing ran remotely) still
// retry.
func (a *Adapter) execRun(ctx context.Context, args []string, stdin []byte, tee io.Writer) ([]byte, []byte, int, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		// Retries only happen when the process never spawned, so a shared
		// tee sees no duplicate output.
		stdout, stderr, code, err := a.runner.Run(ctx, args, stdin, tee)
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
	return nil, nil, 0, fmt.Errorf("coder %s: %w", strings.Join(args[:min(2, len(args))], " "), lastErr)
}

// --- EnvProvisioner ---------------------------------------------------------

// WorkspaceName is the FALLBACK env name, used only when EnvSpec.Name is
// empty (direct/test callers); real runs get the engine's slug-of-branch
// name (engine.workspaceName). It derives a name from the run id (§12): coder
// caps names at 32 chars, so a full 26-char ULID doesn't fit behind the
// prefix — use its random tail (last 16 chars, the full 80 bits of ULID
// entropy; the dropped prefix is just the timestamp).
func WorkspaceName(runID string) string {
	id := strings.ToLower(runID)
	if len(id) > 16 {
		id = id[len(id)-16:]
	}
	return "apothecary-" + id
}

// Reattach re-establishes the reverse tunnel for a running workspace after
// an engine restart (ports.WorkspaceReattacher): the tunnel is a
// subprocess that died with the previous engine, so the workspace's
// engine/proxy loopback forwards are gone until re-created. Idempotent —
// ensureTunnel is a no-op if a tunnel already exists.
func (a *Adapter) Reattach(ctx context.Context, env ports.EnvID) error {
	if err := a.waitReady(ctx, string(env)); err != nil {
		return err
	}
	a.ensureTunnel(env)
	return nil
}

func (a *Adapter) Provision(ctx context.Context, spec ports.EnvSpec) (ports.EnvID, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.ProvisionTimeout)
	defer cancel()
	// The engine supplies a human-friendly, slug-of-branch name (unique per
	// run via its run-id tail); fall back to the run-id scheme for direct
	// callers. This becomes the coder workspace name AND — via the template's
	// docker_container.name = the workspace name — the container name.
	name := spec.Name
	if name == "" {
		name = WorkspaceName(spec.RunID)
	}

	// Idempotent for crash recovery: reuse an existing workspace.
	if st, err := a.status(ctx, name); err == nil && st != ports.EnvAbsent {
		if st != ports.EnvRunning && st != ports.EnvStarting {
			// Autostop or a failed build leaves the workspace stopped, and
			// waitReady only polls — issue the start ourselves. Errors are
			// deliberately not fatal here: waitReady is the arbiter (a
			// concurrent auto-start makes this a harmless no-op failure).
			if _, stderr, code, serr := a.run(ctx, []string{"start", name, "--yes"}, nil); serr != nil || code != 0 {
				slog.Warn("coder start failed; continuing to wait",
					"workspace", name, "err", serr, "exit", code, "stderr", tail(string(stderr), 512))
			}
		}
		if err := a.waitReady(ctx, name); err != nil {
			return "", err
		}
		a.ensureTunnel(ports.EnvID(name))
		return ports.EnvID(name), nil
	}

	args := []string{"create", name,
		"--template", a.cfg.Template,
		"--parameter", "run_id=" + spec.RunID,
		"--parameter", "repo_url=" + spec.RepoURL,
		"--parameter", "branch=" + spec.Branch,
	}
	// Pipeline path (DESIGN2 §8.4): the engine-pushed template takes the
	// built image and the derived egress list as per-workspace parameters.
	// The custom-template path (spec.Image == "") passes v1 params only.
	if spec.Image != "" {
		if err := a.EnsureTemplate(ctx); err != nil {
			return "", err
		}
		args = append(args,
			"--parameter", "workspace_image="+spec.Image,
			"--parameter", "extra_allowed_domains="+strings.Join(spec.EgressDomains, " "),
			// Non-secret; stamped onto the container as the apothecary.project
			// label by the template so containers can be grouped by project.
			"--parameter", "project="+spec.Project,
		)
		// Proxy mode (M18): a non-empty egress_proxy switches the workspace
		// firewall to loopback+gateway-only; all HTTP(S) goes through the
		// reverse-tunneled proxy on the workspace's own loopback.
		if a.cfg.ProxyPort > 0 {
			args = append(args, "--parameter",
				fmt.Sprintf("egress_proxy=127.0.0.1:%d", a.cfg.ProxyPort))
		}
	}
	args = append(args, "--yes")
	_, stderr, code, err := a.run(ctx, args, nil)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("coder create %s: exit %d: %s", name, code, tail(string(stderr), 2048))
	}
	if err := a.waitReady(ctx, name); err != nil {
		return "", err
	}
	a.ensureTunnel(ports.EnvID(name))
	return ports.EnvID(name), nil
}

// waitReady polls workspace status until running, then probes agent
// connectivity with `coder ssh -- true`.
func (a *Adapter) waitReady(ctx context.Context, name string) error {
	for {
		st, err := a.status(ctx, name)
		if err != nil {
			return err
		}
		if st == ports.EnvRunning {
			break
		}
		if st == ports.EnvAbsent {
			return fmt.Errorf("workspace %s disappeared while waiting", name)
		}
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return fmt.Errorf("workspace %s: not running before deadline", name)
		}
	}
	for {
		_, _, code, err := a.run(ctx, []string{"ssh", name, "--", "true"}, nil)
		if err == nil && code == 0 {
			return nil
		}
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return fmt.Errorf("workspace %s: agent not reachable before deadline", name)
		}
	}
}

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
	// Source the egress proxy env (M18) for EVERY workspace command — not
	// just bootstrap/agent runs, but flow command stages too (e.g. `git
	// push`), which otherwise go direct and hit the firewall DROP. No-op
	// when proxy mode is off (empty file).
	script = "[ -s /apothecary/proxy.env ] && { set -a; . /apothecary/proxy.env; set +a; }\n" + script
	// The live exec log (ports.ExecLogTailer): header + output stream into
	// <RunsDir>/<env>/exec.log AS THE COMMAND RUNS, so the run page can
	// tail work in progress. Best-effort — a log failure never fails Exec.
	tee := a.openExecLog(env, cmd)
	if tee != nil {
		defer tee.Close()
	}
	var teeW io.Writer
	if tee != nil {
		teeW = tee
	}
	// execRun, not run: exec'd commands can have side effects (agent
	// invocations, user scripts), so an ambiguous exit 255 must surface as
	// a result instead of triggering a blind re-execution.
	stdout, stderr, code, err := a.execRun(ctx,
		[]string{"ssh", string(env), "--", "bash", "-eo", "pipefail", "-s"}, []byte(script), teeW)
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

// TailExecLog implements the optional ports.ExecLogTailer capability: the
// last n bytes of the env's live exec log.
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
	off := info.Size() - int64(n)
	if off < 0 {
		off = 0
	}
	buf := make([]byte, info.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil, err
	}
	return buf, nil
}

// resolvePath applies the port's path convention: relative = repo-root.
// The result is shell-quoted: paths can carry repo-controlled names (agent
// spec stems), which must never be interpretable — only the deliberate
// $HOME prefix expands.
func resolvePath(path string) string {
	if strings.HasPrefix(path, "/") {
		return shellQuote(path)
	}
	return `"$HOME"/repo/` + shellQuote(path)
}

// shellQuote single-quotes s for POSIX sh (inert $, backticks, spaces).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (a *Adapter) ReadFile(ctx context.Context, env ports.EnvID, path string) ([]byte, error) {
	p := resolvePath(path)
	script := fmt.Sprintf(`if [ ! -f %s ]; then echo MISSING >&2; exit 44; fi; base64 < %s`, p, p)
	stdout, stderr, code, err := a.run(ctx, []string{"ssh", string(env), "--", "bash", "-s"}, []byte(script))
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
	// line as the script, base64 -d consumes the rest.
	script := fmt.Sprintf(`mkdir -p "$(dirname %s)" && base64 -d > %s && chmod %o %s`, p, p, mode.Perm(), p)
	stdin := script + "\n" + base64.StdEncoding.EncodeToString(data) + "\n"
	_, stderr, code, err := a.run(ctx, []string{"ssh", string(env), "--", "bash", "-eo", "pipefail", "-s"},
		[]byte(stdin))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("write %s: exit %d: %s", path, code, tail(string(stderr), 512))
	}
	return nil
}

func (a *Adapter) Status(ctx context.Context, env ports.EnvID) (ports.EnvStatus, error) {
	return a.status(ctx, string(env))
}

// status shells out to `coder list --output json` (single code path with the
// rest of the CLI usage; §12 lists REST here — recorded deviation).
func (a *Adapter) status(ctx context.Context, name string) (ports.EnvStatus, error) {
	stdout, stderr, code, err := a.run(ctx,
		[]string{"list", "--search", "name:" + name, "--output", "json"}, nil)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("coder list: exit %d: %s", code, tail(string(stderr), 512))
	}
	var rows []struct {
		Name        string `json:"name"`
		LatestBuild struct {
			Status string `json:"status"`
		} `json:"latest_build"`
	}
	if err := json.Unmarshal(stdout, &rows); err != nil {
		return "", fmt.Errorf("coder list: %w", err)
	}
	for _, r := range rows {
		if r.Name != name {
			continue
		}
		switch r.LatestBuild.Status {
		case "running":
			return ports.EnvRunning, nil
		case "pending", "starting":
			return ports.EnvStarting, nil
		case "stopped", "stopping", "canceled", "failed":
			return ports.EnvStopped, nil
		case "deleted", "deleting":
			return ports.EnvAbsent, nil
		default:
			return ports.EnvStarting, nil
		}
	}
	return ports.EnvAbsent, nil
}

func (a *Adapter) Destroy(ctx context.Context, env ports.EnvID) error {
	a.stopTunnel(env)
	_, stderr, code, err := a.run(ctx, []string{"delete", string(env), "--yes"}, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		if strings.Contains(string(stderr), "not found") {
			return nil // already gone
		}
		return fmt.Errorf("coder delete %s: exit %d: %s", env, code, tail(string(stderr), 512))
	}
	return nil
}

// owner returns the workspace owner login used in REST paths and, crucially,
// in the browser-facing app/terminal URLs. A configured username wins;
// otherwise it is resolved once from the token (whoami) and cached — the
// workspaces belong to the token's user, so a literal "@me" would 404 for a
// human viewer who isn't logged in as exactly that account. Falls back to
// "me" only if whoami fails (REST still accepts it for the token's own user).
func (a *Adapter) owner(ctx context.Context) string {
	if a.cfg.Owner != "" {
		return a.cfg.Owner
	}
	a.ownerMu.Lock()
	defer a.ownerMu.Unlock()
	if a.ownerName != "" {
		return a.ownerName
	}
	if name, err := a.whoami(ctx); err == nil && name != "" {
		a.ownerName = name
		return name
	}
	return "me"
}

// whoami resolves the token's Coder username via /api/v2/users/me.
func (a *Adapter) whoami(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.URL+"/api/v2/users/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Coder-Session-Token", a.cfg.Token)
	resp, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("coder whoami: %d: %s", resp.StatusCode, tail(string(body), 256))
	}
	var u struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return "", err
	}
	return u.Username, nil
}

// Links queries the REST API for workspace apps (§12): IDE (code-server app),
// web terminal, any exposed app port, plus the non-browser access methods —
// raw SSH (a `coder ssh` command; works for any Coder user with access to
// the workspace, no config-ssh needed) and VS Code Desktop over remote SSH
// (Coder's vscode://coder.coder-remote deep link, handled by the Coder VS
// Code extension, which prompts for install/auth on first use).
func (a *Adapter) Links(ctx context.Context, env ports.EnvID) ([]ports.AccessLink, error) {
	owner := a.owner(ctx)
	u := fmt.Sprintf("%s/api/v2/users/%s/workspace/%s", a.cfg.URL, url.PathEscape(owner), url.PathEscape(string(env)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Coder-Session-Token", a.cfg.Token)
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coder workspace API: %d: %s", resp.StatusCode, tail(string(body), 512))
	}
	var ws struct {
		LatestBuild struct {
			Resources []struct {
				Agents []struct {
					Name string `json:"name"`
					Apps []struct {
						Slug string `json:"slug"`
					} `json:"apps"`
				} `json:"agents"`
			} `json:"resources"`
		} `json:"latest_build"`
	}
	if err := json.Unmarshal(body, &ws); err != nil {
		return nil, err
	}
	links := []ports.AccessLink{
		{Kind: "terminal", URL: fmt.Sprintf("%s/@%s/%s/terminal", a.cfg.URL, owner, env)},
		{Kind: "ssh", Command: fmt.Sprintf("coder ssh %s/%s", owner, env)},
	}
	for _, res := range ws.LatestBuild.Resources {
		for _, ag := range res.Agents {
			links = append(links, ports.AccessLink{
				Kind: "vscode",
				URL: "vscode://coder.coder-remote/open?" + url.Values{
					"owner":     {owner},
					"workspace": {string(env)},
					"agent":     {ag.Name},
					"folder":    {"/home/apothecary/repo"},
					"url":       {a.cfg.URL},
				}.Encode(),
			})
			for _, app := range ag.Apps {
				kind := "app"
				if app.Slug == "code-server" {
					kind = "ide"
				}
				links = append(links, ports.AccessLink{
					Kind: kind,
					URL:  fmt.Sprintf("%s/@%s/%s.%s/apps/%s/", a.cfg.URL, owner, env, ag.Name, app.Slug),
				})
			}
		}
	}
	return links, nil
}

// Test is the cheap auth/reachability probe (DESIGN2 §4: the UI "Test
// connection" button): an authenticated REST call that fails on a bad URL
// or token without touching workspaces.
func (a *Adapter) Test(ctx context.Context) error {
	if a.cfg.URL == "" {
		return fmt.Errorf("coder: url is required")
	}
	if a.cfg.Token == "" {
		return fmt.Errorf("coder: no token configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.URL+"/api/v2/users/me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Coder-Session-Token", a.cfg.Token)
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("coder unreachable at %s: %w", a.cfg.URL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("coder auth check: %d: %s", resp.StatusCode, tail(string(body), 256))
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
