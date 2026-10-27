// Package browser is the project browser: ONE container per project
// (apo-browser-<project id>) that its runs share, a Chromium launched per agent
// session inside it, driven through Playwright's MCP server — what a flow's
// builtin `browser` MCP server is (FLOWLANG §1 mcp_servers).
//
// Lifecycle follows the egress gateway's (internal/egressgw): the container
// is created on first need from a content-addressed image (wsimage
// EnsureBrowserImage), reused while it runs, recreated when it runs a stale
// image and nobody is using it, and stopped after a while with no session.
//
// A session is one `docker exec -i` of `apothecary-shim browser-session`,
// which starts @playwright/mcp (one MCP server, one Chromium) on that exec's
// stdio — the engine bridges it to the agent's `shim browser-mcp` in the
// workspace. The container has NO network (--network none): the session's
// Chromium proxies everything through a loopback relay in the container, to a
// unix socket on the per-project bind mount, to this package's routing proxy
// (proxy.go), which sends the run's localhost to the run's workspace and
// refuses everything else. Container→host IP traffic is blocked on hardened
// docker hosts anyway; a bind-mounted unix socket is the sanctioned channel.
package browser

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"apothecary/internal/ports"
)

// HostRun runs one docker CLI invocation against the host daemon.
type HostRun func(ctx context.Context, args []string) (stdout, stderr []byte, code int, err error)

// StreamRun runs one docker CLI invocation wired to a stream — the exec a
// session is.
type StreamRun func(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error

// Config wires a Manager.
type Config struct {
	RunsDir string
	// Image builds (once) the browser image and returns its tag; Tag is the
	// tag this build would run, without building (Status).
	Image func(ctx context.Context) (string, error)
	Tag   func() string
	// Run / Stream default to the docker CLI.
	Run    HostRun
	Stream StreamRun
	// IdleAfter is how long a container with no session keeps running
	// (default 10 minutes). Chromium is per session, so an idle container
	// holds only node — but there is no reason to hold even that.
	IdleAfter time.Duration
	Log       *slog.Logger
}

// Manager runs every project's browser.
type Manager struct {
	cfg Config

	mu       sync.Mutex
	projects map[string]*project
}

type project struct {
	mu       sync.Mutex // serializes ensure (create/recreate/start)
	sessions int
	idle     *time.Timer
}

func New(cfg Config) *Manager {
	if cfg.Run == nil {
		cfg.Run = dockerRun
	}
	if cfg.Stream == nil {
		cfg.Stream = dockerStream
	}
	if cfg.IdleAfter <= 0 {
		cfg.IdleAfter = 10 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Manager{cfg: cfg, projects: map[string]*project{}}
}

var _ ports.Browser = (*Manager)(nil)

// --- naming ---------------------------------------------------------------

// Everything per project is named by ports.ProjectHostKey(project): the
// project's ID, lowercased — never its name (a rename must not strand a
// browser, and two projects called "api" must not share one). Every
// `project`/`name` argument in this file is that id.

func containerName(project string) string { return "apo-browser-" + ports.ProjectHostKey(project) }

// dir is the project's bind mount: the sessions' proxy sockets, at the same
// path in the daemon and the container.
func (m *Manager) dir(project string) string {
	return filepath.Join(m.cfg.RunsDir, "browser", ports.ProjectHostKey(project))
}

// maxSockPath is sun_path's limit, less the terminating NUL.
const maxSockPath = 107

// playwrightArgs start one MCP server with its own in-memory Chromium. The
// session adds --proxy-server (its relay's port, known only inside).
var playwrightArgs = []string{"playwright-mcp", "--headless", "--isolated", "--browser", "chromium",
	"--no-sandbox", "--image-responses", "allow"}

// shimInImage is where the browser image carries the shim.
const shimInImage = "/usr/local/bin/apothecary-shim"

// --- docker -----------------------------------------------------------------

func dockerRun(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), errb.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

func dockerStream(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	errb := &tailBuffer{max: 4 << 10}
	cmd.Stderr = errb
	// The stdin copier blocks on the agent's side of the stream, which only
	// the caller closes: do not wait on it once the exec is gone.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func (m *Manager) docker(ctx context.Context, args ...string) (string, error) {
	out, errb, code, err := m.cfg.Run(ctx, args)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("docker %s: exit %d: %s", args[0], code, strings.TrimSpace(string(errb)))
	}
	return strings.TrimSpace(string(out)), nil
}

// --- lifecycle ----------------------------------------------------------------

func (m *Manager) project(name string) *project {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[name]
	if !ok {
		p = &project{}
		m.projects[name] = p
	}
	return p
}

// acquire counts a session in and cancels a pending idle stop.
func (m *Manager) acquire(name string) *project {
	p := m.project(name)
	m.mu.Lock()
	p.sessions++
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	m.mu.Unlock()
	return p
}

// release counts a session out; the last one arms the idle stop.
func (m *Manager) release(name string, p *project) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p.sessions--
	if p.sessions == 0 {
		m.armIdleLocked(name, p)
	}
}

func (m *Manager) armIdleLocked(name string, p *project) {
	if p.idle != nil {
		p.idle.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(m.cfg.IdleAfter, func() {
		m.mu.Lock()
		stale := p.idle != t || p.sessions > 0
		if !stale {
			p.idle = nil
		}
		m.mu.Unlock()
		if stale {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		p.mu.Lock() // not while an ensure is starting it
		defer p.mu.Unlock()
		m.mu.Lock()
		busy := p.sessions > 0
		m.mu.Unlock()
		if busy {
			return
		}
		if _, err := m.docker(ctx, "stop", "-t", "5", containerName(name)); err != nil {
			m.cfg.Log.Warn("browser: idle stop failed", "project", name, "err", err)
			return
		}
		m.cfg.Log.Info("browser: stopped the idle project browser", "project", name)
	})
	p.idle = t
}

// Start arms the idle stop for every project browser already running — one
// a previous engine process left behind has no session to count it out. The
// label it reads back is the project id.
func (m *Manager) Start(ctx context.Context) {
	out, err := m.docker(ctx, "ps", "--filter", "label=apothecary.browser=true",
		"--format", `{{.Label "apothecary.project"}}`)
	if err != nil {
		m.cfg.Log.Warn("browser: listing running browsers failed", "err", err)
		return
	}
	for _, name := range strings.Fields(out) {
		p := m.project(name)
		m.mu.Lock()
		if p.sessions == 0 {
			m.armIdleLocked(name, p)
		}
		m.mu.Unlock()
	}
}

// Close stops the idle timers (the containers stay; the next process
// re-arms them in Start).
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.projects {
		if p.idle != nil {
			p.idle.Stop()
			p.idle = nil
		}
	}
}

// Warm brings the project's browser up in the background. project is the
// id; projectName is a docker label only.
func (m *Manager) Warm(project, projectName string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		p := m.acquire(project)
		defer m.release(project, p)
		if err := m.ensure(ctx, project, projectName, p); err != nil {
			m.cfg.Log.Warn("browser: warming the project browser failed", "project", project, "err", err)
		}
	}()
}

// ensure brings the project's container up: created when absent, started
// when stopped, recreated when it runs a stale image and only the caller
// is using it (a busy browser keeps its image until it is idle).
func (m *Manager) ensure(ctx context.Context, name, projectName string, p *project) error {
	img, err := m.cfg.Image(ctx)
	if err != nil {
		return fmt.Errorf("build the browser image: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	dir := m.dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	c := containerName(name)
	cur, err := m.docker(ctx, "ps", "-a", "--filter", "name=^"+c+"$", "--format", "{{.Image}}")
	if err != nil {
		return err
	}
	if cur != "" {
		m.mu.Lock()
		alone := p.sessions <= 1
		m.mu.Unlock()
		if cur == img || !alone {
			_, err := m.docker(ctx, "start", c) // a no-op when running
			return err
		}
		if _, err := m.docker(ctx, "rm", "-f", c); err != nil {
			return err
		}
	}
	args := []string{"run", "-d", "--name", c,
		"--label", "apothecary=true",
		"--label", "apothecary.project=" + name, // the id: what Start finds it by
		"--label", "apothecary.browser=true",
	}
	if projectName != "" {
		args = append(args, "--label", "apothecary.project.name="+projectName) // for a human's docker ps
	}
	_, err = m.docker(ctx, append(args,
		// No network at all: the routing proxy is the only way out.
		"--network", "none",
		"--init",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--shm-size", "1g",
		"-v", dir+":"+dir,
		img)...)
	return err
}

// --- sessions ---------------------------------------------------------------

func newSessionID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Session serves one MCP session: the project's browser brought up, a
// routing proxy for the run on a fresh socket, one exec of the session
// runner — until either side closes.
func (m *Manager) Session(ctx context.Context, s ports.BrowserSession, stdio io.ReadWriter) error {
	p := m.acquire(s.Project)
	defer m.release(s.Project, p)
	if err := m.ensure(ctx, s.Project, s.ProjectName, p); err != nil {
		return err
	}
	sid := newSessionID()
	sock := filepath.Join(m.dir(s.Project), "s-"+sid+".sock")
	if len(sock) > maxSockPath {
		return fmt.Errorf("browser: socket path %s is longer than a unix socket allows (%d) — use a shorter runs dir", sock, maxSockPath)
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("browser: proxy socket: %w", err)
	}
	defer os.Remove(sock)
	// The container's user is whoever its image says; open the socket to it.
	_ = os.Chmod(sock, 0o666)
	log := m.cfg.Log.With("project", s.Project, "run", s.Run, "session", sid)
	srv := &http.Server{Handler: newProxy(Dial(s.Dial), log), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	log.Info("browser: session started")
	args := append([]string{"exec", "-i", containerName(s.Project),
		shimInImage, "browser-session", "--sock", sock, "--"}, playwrightArgs...)
	err = m.cfg.Stream(ctx, args, stdio, stdio)
	log.Info("browser: session ended", "err", err)
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("browser session: %w", err)
	}
	return nil
}

// Status is the project's browser as the settings page shows it (name is
// the project id).
func (m *Manager) Status(ctx context.Context, name string) (ports.BrowserStatus, error) {
	st := ports.BrowserStatus{Container: containerName(name), State: "absent"}
	if m.cfg.Tag != nil {
		st.Current = m.cfg.Tag()
	}
	m.mu.Lock()
	if p := m.projects[name]; p != nil {
		st.Sessions = p.sessions
	}
	m.mu.Unlock()
	out, err := m.docker(ctx, "ps", "-a", "--filter", "name=^"+st.Container+"$", "--format", "{{.Image}}\t{{.State}}")
	if err != nil {
		return st, err
	}
	if out == "" {
		return st, nil
	}
	img, state, _ := strings.Cut(out, "\t")
	st.Image = img
	if state == "running" {
		st.State = "running"
	} else {
		st.State = "stopped"
	}
	return st, nil
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
