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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"factory/internal/ports"
)

type Config struct {
	URL      string // Coder server, e.g. http://localhost:7080
	Token    string // session token (engine-held, never enters workspaces §15)
	Template string // template name, e.g. "factory"
	Owner    string // workspace owner; "me" works for CLI, REST needs the login name
	// RunsDir receives exec logs (<run>/... §12); empty disables.
	RunsDir string
	// ProvisionTimeout bounds create+build+agent-ready (default 15m).
	ProvisionTimeout time.Duration
	// EnginePort is reverse-forwarded into every workspace
	// (workspace 127.0.0.1:port -> engine host 127.0.0.1:port) over the
	// Coder agent connection, so workspaces reach the engine without any
	// host firewall exposure. Default 8080.
	EnginePort int
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

	tunnelMu sync.Mutex
	tunnels  map[ports.EnvID]*tunnel

	// Engine-pushed template state (template.go): checked/pushed at most
	// once per process.
	tmplMu      sync.Mutex
	tmplEnsured bool
}

type tunnel struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// CmdRunner executes one coder CLI invocation; swapped in tests.
type CmdRunner interface {
	Run(ctx context.Context, args []string, stdin []byte) (stdout, stderr []byte, exitCode int, err error)
}

func New(cfg Config) *Adapter {
	if cfg.Owner == "" {
		cfg.Owner = "me"
	}
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
	}
	a.tunnelCmd = a.defaultTunnelCmd
	return a
}

func (a *Adapter) defaultTunnelCmd(ctx context.Context, ws string) *exec.Cmd {
	args := []string{"ssh", ws}
	for _, port := range append([]int{a.cfg.EnginePort}, a.cfg.ExtraForwards...) {
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

// Close stops all tunnel processes (engine shutdown; workspaces keep
// running and tunnels re-establish on the next boot's Provision).
func (a *Adapter) Close() {
	a.tunnelMu.Lock()
	envs := make([]ports.EnvID, 0, len(a.tunnels))
	for env := range a.tunnels {
		envs = append(envs, env)
	}
	a.tunnelMu.Unlock()
	for _, env := range envs {
		a.stopTunnel(env)
	}
}

// NewWithRunner is the test constructor.
func NewWithRunner(cfg Config, r CmdRunner) *Adapter {
	a := New(cfg)
	a.runner = r
	return a
}

type cliRunner struct{ url, token string }

func (c *cliRunner) Run(ctx context.Context, args []string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "coder", args...)
	cmd.Env = append(os.Environ(),
		"CODER_URL="+c.url,
		"CODER_SESSION_TOKEN="+c.token,
	)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
		err = nil // command ran; nonzero exit is a result, not a failure
	}
	return out.Bytes(), errb.Bytes(), code, err
}

// run is the §12 single wrapper: 3 attempts on transport-level failures
// (process failed to run, or ssh transport exit 255). Command-level nonzero
// exits are results and are never retried.
func (a *Adapter) run(ctx context.Context, args []string, stdin []byte) ([]byte, []byte, int, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		stdout, stderr, code, err := a.runner.Run(ctx, args, stdin)
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

// --- EnvProvisioner ---------------------------------------------------------

// WorkspaceName derives the workspace name from the run id (§12). Coder
// caps names at 32 chars, so a full 26-char ULID doesn't fit behind the
// prefix — use its random tail (last 16 chars, the full 80 bits of ULID
// entropy; the dropped prefix is just the timestamp).
func WorkspaceName(runID string) string {
	id := strings.ToLower(runID)
	if len(id) > 16 {
		id = id[len(id)-16:]
	}
	return "factory-" + id
}

func (a *Adapter) Provision(ctx context.Context, spec ports.EnvSpec) (ports.EnvID, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.ProvisionTimeout)
	defer cancel()
	name := WorkspaceName(spec.RunID)

	// Idempotent for crash recovery: reuse an existing workspace.
	if st, err := a.status(ctx, name); err == nil && st != ports.EnvAbsent {
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
		)
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
	stdout, stderr, code, err := a.run(ctx,
		[]string{"ssh", string(env), "--", "bash", "-eo", "pipefail", "-s"}, []byte(script))
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
	a.writeLog(env, cmd, output)
	return ports.ExecResult{ExitCode: code, Output: output}, nil
}

// writeLog streams exec output to the runs dir (§12), keyed by workspace —
// best-effort; the engine's stage logs are the primary copy.
func (a *Adapter) writeLog(env ports.EnvID, cmd ports.Command, output string) {
	if a.cfg.RunsDir == "" {
		return
	}
	dir := filepath.Join(a.cfg.RunsDir, string(env))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "exec.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "--- %s [dir=%s]\n%s\n%s\n", time.Now().UTC().Format(time.RFC3339), cmd.Dir, cmd.Script, output)
}

// resolvePath applies the port's path convention: relative = repo-root.
func resolvePath(path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "$HOME/repo/" + path
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

// Links queries the REST API for workspace apps (§12): IDE (code-server app),
// web terminal, and any exposed app port.
func (a *Adapter) Links(ctx context.Context, env ports.EnvID) ([]ports.AccessLink, error) {
	owner := a.cfg.Owner
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
	}
	for _, res := range ws.LatestBuild.Resources {
		for _, ag := range res.Agents {
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
