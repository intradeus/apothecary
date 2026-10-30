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
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"apothecary/internal/ports"
	"apothecary/internal/ports/envport"
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
	// Host is where browsers run; nil = containers on the docker daemon (Run,
	// Stream, RunsDir).
	Host Host
}

// Host is where project browsers run: a container on the engine's docker
// daemon (host_docker.go) or a pod on Kubernetes (host_k8s.go). The manager
// keeps the policy — when to start, recreate, stop — and the host does it.
type Host interface {
	// Ensure brings the project's browser up from img: created when absent,
	// started when stopped, recreated on another image only when replace.
	Ensure(ctx context.Context, project, projectName, img string, replace bool) error
	// Stop stops it (idle); it may be removed — Ensure brings it back.
	Stop(ctx context.Context, project string) error
	// Running lists the projects whose browser is running.
	Running(ctx context.Context) ([]string, error)
	// Inspect reports the browser's image and whether it runs ("" = absent).
	Inspect(ctx context.Context, project string) (image string, running bool, err error)
	// Listen opens one session's routing-proxy listener and returns the
	// browser-session arguments that reach it from inside the browser.
	Listen(ctx context.Context, project, sid string) (net.Listener, []string, error)
	// Exec runs argv in the project's browser wired to the stream.
	Exec(ctx context.Context, project string, argv []string, stdin io.Reader, stdout io.Writer) error
	// Name is the browser's name, for the settings page.
	Name(project string) string
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
	if cfg.Host == nil {
		cfg.Host = &dockerHost{runsDir: cfg.RunsDir, run: cfg.Run, stream: cfg.Stream}
	}
	return &Manager{cfg: cfg, projects: map[string]*project{}}
}

var _ ports.Browser = (*Manager)(nil)

// --- naming ---------------------------------------------------------------

// Everything per project is named by envport.ProjectHostKey(project): the
// project's ID, lowercased — never its name (a rename must not strand a
// browser, and two projects called "api" must not share one). Every
// `project`/`name` argument in this file is that id.

func containerName(project string) string { return "apo-browser-" + envport.ProjectHostKey(project) }

// playwrightArgs start one MCP server with its own in-memory Chromium. The
// session adds --proxy-server (its relay's port, known only inside).
var playwrightArgs = []string{"playwright-mcp", "--headless", "--isolated", "--browser", "chromium",
	"--no-sandbox", "--image-responses", "allow"}

// shimInImage is where the browser image carries the shim.
const shimInImage = "/usr/local/bin/apothecary-shim"

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
		if err := m.cfg.Host.Stop(ctx, name); err != nil {
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
	names, err := m.cfg.Host.Running(ctx)
	if err != nil {
		m.cfg.Log.Warn("browser: listing running browsers failed", "err", err)
		return
	}
	for _, name := range names {
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
	m.mu.Lock()
	alone := p.sessions <= 1
	m.mu.Unlock()
	return m.cfg.Host.Ensure(ctx, name, projectName, img, alone)
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
	ln, reach, err := m.cfg.Host.Listen(ctx, s.Project, sid)
	if err != nil {
		return err
	}
	defer ln.Close()
	log := m.cfg.Log.With("project", s.Project, "run", s.Run, "session", sid)
	srv := &http.Server{Handler: newProxy(Dial(s.Dial), log), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	log.Info("browser: session started")
	argv := append(append([]string{shimInImage, "browser-session"}, reach...), "--")
	argv = append(argv, playwrightArgs...)
	err = m.cfg.Host.Exec(ctx, s.Project, argv, stdio, stdio)
	log.Info("browser: session ended", "err", err)
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("browser session: %w", err)
	}
	return nil
}

// Status is the project's browser as the settings page shows it (name is
// the project id).
func (m *Manager) Status(ctx context.Context, name string) (ports.BrowserStatus, error) {
	st := ports.BrowserStatus{Container: m.cfg.Host.Name(name), State: "absent"}
	if m.cfg.Tag != nil {
		st.Current = m.cfg.Tag()
	}
	m.mu.Lock()
	if p := m.projects[name]; p != nil {
		st.Sessions = p.sessions
	}
	m.mu.Unlock()
	img, running, err := m.cfg.Host.Inspect(ctx, name)
	if err != nil || img == "" {
		return st, err
	}
	st.Image = img
	if running {
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
