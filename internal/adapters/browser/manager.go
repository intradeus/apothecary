// Package browser is the project browser: ONE pod per project
// (apo-browser-<project id>) that its runs share, a Chromium launched per agent
// session inside it, driven through Playwright's MCP server — what a flow's
// builtin `browser` MCP server is (FLOWLANG §1 mcp_servers).
//
// Lifecycle follows the egress gateway's (internal/adapters/egressgw): the pod
// is created on first need from Microsoft's Playwright MCP image (DefaultImage,
// pinned by digest), reused while it runs, recreated when it runs a stale
// image or shim and nobody is using it, and stopped after a while with no
// session. The image is not ours: the engine gives it the shim (a volume an
// init container seeds) and keeps it idle on `sleep` instead of the image's
// own MCP server.
//
// A session is one `kubectl exec -i` of `apothecary-shim browser-session`,
// which starts @playwright/mcp (one MCP server, one Chromium) on that exec's
// stdio — the engine bridges it to the agent's `shim browser-mcp` in the
// workspace. The pod's network policy lets it reach the engine's session mux
// (mux.go) and nothing else: the session's Chromium proxies everything
// through a loopback relay in the pod, over that mux, to this package's
// routing proxy (proxy.go), which sends the run's localhost to the run's
// workspace and refuses everything else.
//
// Browsers run on Kubernetes only (K8sHost). A Manager with no Host is a
// deployment without a project browser: it starts nothing and its sessions
// fail with ErrNoHost.
package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"apothecary/internal/platform/logs"
	"apothecary/internal/ports/browserport"
)

// Config wires a Manager.
type Config struct {
	// Image returns the browser image's reference, ready to run (on
	// Kubernetes the kubelet pulls it); Tag is the reference it would run
	// (Status).
	Image func(ctx context.Context) (string, error)
	Tag   func() string
	// IdleAfter is how long a browser with no session keeps running
	// (default 10 minutes). Chromium is per session, so an idle browser
	// holds only node — but there is no reason to hold even that.
	IdleAfter time.Duration
	Log       *slog.Logger
	// Host is where browsers run (K8sHost); nil = this deployment has no
	// project browser (ErrNoHost).
	Host Host
}

// ErrNoHost is what a Manager with no Host answers: browsers run on
// Kubernetes only.
var ErrNoHost = errors.New("the project browser needs a Kubernetes deployment")

// Host is where project browsers run: a pod on Kubernetes (host_k8s.go). The
// manager keeps the policy — when to start, recreate, stop — and the host does
// it; the interface is also the seam the manager's unit tests fake.
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
	if cfg.IdleAfter <= 0 {
		cfg.IdleAfter = 10 * time.Minute
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Manager{cfg: cfg, projects: map[string]*project{}}
}

var _ browserport.Browser = (*Manager)(nil)

// --- naming ---------------------------------------------------------------

// Everything per project is named by envport.ProjectHostKey(project): the
// project's ID, lowercased — never its name (a rename must not strand a
// browser, and two projects called "api" must not share one). Every
// `project`/`name` argument in this file is that id.

// DefaultImage is the project browser: Microsoft's Playwright MCP server with
// the Chromium its Playwright drives, pinned by digest (an index: amd64 and
// arm64). APOTHECARY_BROWSER_IMAGE overrides it, e.g. with a mirror. A bump
// is re-checked with the live session test: playwrightArgs depend on the
// image's layout (/app/cli.js).
const DefaultImage = "mcr.microsoft.com/playwright/mcp:v0.0.83@sha256:690763cbb437ee7233f538a953bb5390bf03791eb5c965b471e59d17a6132fcb"

// playwrightArgs start one MCP server with its own in-memory Chromium. The
// session adds --proxy-server (its relay's port, known only inside).
var playwrightArgs = []string{"node", "/app/cli.js", "--headless", "--isolated", "--browser", "chromium",
	"--no-sandbox", "--image-responses", "allow"}

// idleCommand keeps the browser up between sessions in place of the image's
// own entrypoint (an MCP server on the container's stdio).
var idleCommand = []string{"sleep", "infinity"}

// shimDir is where the browser finds the shim, seeded by an init container
// (whose seed also writes the apothecary-shim alias); shimPath is the binary
// in it.
const (
	shimDir  = "/opt/apothecary/shim"
	shimPath = shimDir + "/apothecary-shim"
)

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
			m.cfg.Log.Warn("browser: idle stop failed", logs.ProjectID, name, "err", err)
			return
		}
		m.cfg.Log.Info("browser: stopped the idle project browser", logs.ProjectID, name)
	})
	p.idle = t
}

// Start arms the idle stop for every project browser already running — one
// a previous engine process left behind has no session to count it out. The
// label it reads back is the project id. With no Host there is nothing to
// look for.
func (m *Manager) Start(ctx context.Context) {
	if m.cfg.Host == nil {
		return
	}
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

// Close stops the idle timers (the browsers stay; the next process re-arms
// them in Start).
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
// id; projectName is an annotation only. With no Host it does nothing: the
// session that follows is the one to say why.
func (m *Manager) Warm(project, projectName string) {
	if m.cfg.Host == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		p := m.acquire(project)
		defer m.release(project, p)
		if err := m.ensure(ctx, project, projectName, p); err != nil {
			m.cfg.Log.Warn("browser: warming the project browser failed", logs.ProjectID, project, "err", err)
		}
	}()
}

// ensure brings the project's browser up: created when absent, started
// when stopped, recreated when it runs a stale image and only the caller
// is using it (a busy browser keeps its image until it is idle).
func (m *Manager) ensure(ctx context.Context, name, projectName string, p *project) error {
	img, err := m.cfg.Image(ctx)
	if err != nil {
		return fmt.Errorf("prepare the browser image: %w", err)
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
func (m *Manager) Session(ctx context.Context, s browserport.BrowserSession, stdio io.ReadWriter) error {
	if m.cfg.Host == nil {
		return ErrNoHost
	}
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
	log := m.cfg.Log.With(logs.ProjectID, s.Project, logs.RunID, s.Run, "session", sid)
	srv := &http.Server{Handler: newProxy(Dial(s.Dial), log), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	log.Info("browser: session started")
	argv := append(append([]string{shimPath, "browser-session"}, reach...), "--")
	argv = append(argv, playwrightArgs...)
	err = m.cfg.Host.Exec(ctx, s.Project, argv, stdio, stdio)
	log.Info("browser: session ended", "err", err)
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("browser session: %w", err)
	}
	return nil
}

// Status is the project's browser as the settings page shows it (name is
// the project id). With no Host it is ErrNoHost.
func (m *Manager) Status(ctx context.Context, name string) (browserport.BrowserStatus, error) {
	if m.cfg.Host == nil {
		return browserport.BrowserStatus{}, ErrNoHost
	}
	st := browserport.BrowserStatus{Container: m.cfg.Host.Name(name), State: "absent"}
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
