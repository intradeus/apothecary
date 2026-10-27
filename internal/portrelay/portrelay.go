// Package portrelay publishes exposed workspace ports to the human: one
// engine-host TCP listener per (run, workspace port), each accepted
// browser connection pumped through the provisioner's ports.PortConn.
// Listeners bind a host port on the CPUI's own interface — root-level URLs
// (webapp asset paths work, unlike path-prefix proxying), any number of
// concurrent runs.
//
// The live listeners/PortConns are engine-process-scoped and are re-created
// on the recovery path, never written to the immutable event log (a
// replayed URL would point at a dead port). But the ALLOCATION — which host
// port a run's workspace port got — is best-effort persisted via an
// optional Store (Persist), so after an engine restart Expose prefers the
// same host port and a URL the user opened keeps working (falling back to a
// fresh port only if it is now taken). Persistence is pruned when the run's
// environment is torn down (CloseRun), NOT on shutdown (CloseAll) — the
// whole point is that active runs' allocations survive a reboot.
//
// Trust level: the listeners carry raw TCP to workspace apps with no auth
// — exactly the CPUI's own posture (the API has none either); both bind
// the same host. The default 127.0.0.1 keeps everything loopback.
//
// Host routing (RouteByHost) is the other mode, for an engine behind a
// Service and an Ingress (Kubernetes), where a listener per port would be
// unreachable: an exposure gets a hostname, <port>-<run>.<apps domain>, and
// the engine's own HTTP server routes requests by Host to it (Lookup), behind
// the user's session (internal/httpapi apps.go).
package portrelay

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"apothecary/internal/ports"
)

// Store persists the (run → workspace port → host port) allocation so
// exposures come back on the same host port after a restart. *pgstore.Store
// satisfies it structurally; nil = in-memory only.
type Store interface {
	SaveExposedPort(ctx context.Context, runID string, wsPort, hostPort int) error
	DeleteExposedPorts(ctx context.Context, runID string) error
	LoadExposedPorts(ctx context.Context) (map[string]map[int]int, error)
}

// storeTimeout bounds a best-effort persistence call so it can never hang an
// exposure or a teardown.
const storeTimeout = 5 * time.Second

// Exposure is one published port (the GET /runs/{id}/ports row).
type Exposure struct {
	Port int    `json:"port"` // workspace-side port
	URL  string `json:"url"`  // browser-facing engine-host URL
}

type exposure struct {
	ln  net.Listener // nil when host-routed
	pc  ports.PortConn
	url string
}

type Manager struct {
	host string // listen host = the CPUI interface ("" -> 127.0.0.1)

	mu    sync.Mutex
	byRun map[string]map[int]*exposure
	// remembered is the persisted allocation runID -> wsPort -> hostPort:
	// loaded from the Store at boot (preferred bind targets) and kept in
	// sync as exposures come and go.
	remembered map[string]map[int]int
	store      Store // nil = in-memory only

	// domain/scheme switch exposures to host routing (RouteByHost).
	domain, scheme string
}

// RouteByHost serves every exposure at <port>-<run label>.<domain> through
// the engine's HTTP server instead of a listener of its own. domain may carry
// a port ("apps.localhost:8090") that browsers need in the URL.
func (m *Manager) RouteByHost(domain, scheme string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.domain = strings.Trim(strings.ToLower(domain), ".")
	if scheme == "" {
		scheme = "https"
	}
	m.scheme = scheme
}

// HostRouted reports host-routing mode.
func (m *Manager) HostRouted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.domain != ""
}

// RunLabel is the run's part of an app hostname: the tail of its id (a
// ULID's random part), lowercased — DNS-label safe and unique enough among
// live runs.
func RunLabel(runID string) string {
	id := strings.ToLower(runID)
	if len(id) > 10 {
		id = id[len(id)-10:]
	}
	return id
}

// Lookup resolves a request's Host to the exposure it names: the run, the
// workspace port and its dialer. ok=false for any host outside the apps
// domain or naming nothing exposed.
func (m *Manager) Lookup(host string) (runID string, port int, pc ports.PortConn, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.domain == "" {
		return "", 0, nil, false
	}
	label, found := strings.CutSuffix(stripPort(strings.ToLower(host)), "."+stripPort(m.domain))
	if !found || strings.Contains(label, ".") {
		return "", 0, nil, false
	}
	ps, run, found := strings.Cut(label, "-")
	if !found {
		return "", 0, nil, false
	}
	port, err := strconv.Atoi(ps)
	if err != nil {
		return "", 0, nil, false
	}
	for id, exs := range m.byRun {
		if RunLabel(id) != run {
			continue
		}
		if ex, ok := exs[port]; ok {
			return id, port, ex.pc, true
		}
	}
	return "", 0, nil, false
}

// InAppsDomain reports whether host is under the apps domain at all.
func (m *Manager) InAppsDomain(host string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.domain != "" && strings.HasSuffix(stripPort(strings.ToLower(host)), "."+stripPort(m.domain))
}

func stripPort(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func New(host string) *Manager {
	if host == "" {
		host = "127.0.0.1"
	}
	return &Manager{
		host:       host,
		byRun:      map[string]map[int]*exposure{},
		remembered: map[string]map[int]int{},
	}
}

// Persist attaches a durable Store and loads the saved allocations, so
// subsequent Expose calls prefer each run's previously-allocated host port.
// Call once at boot, before recovery. A load error is returned (fail loud
// at startup); per-exposure writes afterward are best-effort.
func (m *Manager) Persist(store Store) error {
	remembered, err := store.LoadExposedPorts(context.Background())
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
	if remembered != nil {
		m.remembered = remembered
	}
	return nil
}

// SavedPorts lists the workspace ports a run had exposed (from the persisted
// allocation), sorted — the recovery path re-exposes these so manual
// exposures survive a restart, not just declared forwardPorts.
func (m *Manager) SavedPorts(runID string) []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int, 0, len(m.remembered[runID]))
	for p := range m.remembered[runID] {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// urlHost is the address browsers should use: the configured host, except
// wildcard binds where we cannot know the reachable name.
func (m *Manager) urlHost() string {
	switch m.host {
	case "0.0.0.0", "::", "[::]":
		return "localhost"
	}
	return m.host
}

// Expose publishes env's port through pc and returns the browser URL. A
// port already exposed for the run short-circuits to the existing URL and
// closes the redundant pc. The host port is the one this run+port had
// before (persisted), falling back to a kernel-picked port if none is saved
// or it is now taken.
func (m *Manager) Expose(runID string, port int, pc ports.PortConn) (string, error) {
	m.mu.Lock()
	if ex, ok := m.byRun[runID][port]; ok {
		m.mu.Unlock()
		_ = pc.Close()
		return ex.url, nil
	}
	if m.domain != "" {
		url := fmt.Sprintf("%s://%d-%s.%s/", m.scheme, port, RunLabel(runID), m.domain)
		if m.byRun[runID] == nil {
			m.byRun[runID] = map[int]*exposure{}
		}
		m.byRun[runID][port] = &exposure{pc: pc, url: url}
		if m.remembered[runID] == nil {
			m.remembered[runID] = map[int]int{}
		}
		m.remembered[runID][port] = 0 // no host port; remembered so recovery re-exposes it
		store := m.store
		m.mu.Unlock()
		if store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
			defer cancel()
			if err := store.SaveExposedPort(ctx, runID, port, 0); err != nil {
				slog.Warn("portrelay: persist exposure failed", "run", runID, "port", port, "err", err)
			}
		}
		return url, nil
	}
	ln, hostPort, err := m.bind(m.remembered[runID][port])
	if err != nil {
		m.mu.Unlock()
		_ = pc.Close()
		return "", fmt.Errorf("expose port %d: %w", port, err)
	}
	url := fmt.Sprintf("http://%s/", net.JoinHostPort(m.urlHost(), strconv.Itoa(hostPort)))
	if m.byRun[runID] == nil {
		m.byRun[runID] = map[int]*exposure{}
	}
	m.byRun[runID][port] = &exposure{ln: ln, pc: pc, url: url}
	if m.remembered[runID] == nil {
		m.remembered[runID] = map[int]int{}
	}
	m.remembered[runID][port] = hostPort
	store := m.store
	m.mu.Unlock()

	go serve(ln, pc)
	// Best-effort persist OUTSIDE the lock: a slow DB must never block other
	// exposures, and a failure only costs port stability across the next
	// restart, never the exposure itself.
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		defer cancel()
		if err := store.SaveExposedPort(ctx, runID, port, hostPort); err != nil {
			slog.Warn("portrelay: persist exposure failed", "run", runID, "port", port, "err", err)
		}
	}
	return url, nil
}

// bind returns a listener on the preferred host port when free, else on a
// kernel-picked one; the second return is the chosen host port.
func (m *Manager) bind(preferred int) (net.Listener, int, error) {
	if preferred != 0 {
		if ln, err := net.Listen("tcp", net.JoinHostPort(m.host, strconv.Itoa(preferred))); err == nil {
			return ln, preferred, nil
		}
		// Taken (EADDRINUSE) or otherwise unavailable — fall back below.
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(m.host, "0"))
	if err != nil {
		return nil, 0, err
	}
	_, lport, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(lport)
	return ln, p, nil
}

// Existing returns the URL of an already-exposed port ("" if none).
func (m *Manager) Existing(runID string, port int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ex, ok := m.byRun[runID][port]; ok {
		return ex.url
	}
	return ""
}

// List returns a run's exposures sorted by workspace port.
func (m *Manager) List(runID string) []Exposure {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Exposure, 0, len(m.byRun[runID]))
	for port, ex := range m.byRun[runID] {
		out = append(out, Exposure{Port: port, URL: ex.url})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// teardown closes a run's listeners + PortConns and clears its live state,
// WITHOUT touching the persisted allocation.
func (m *Manager) teardown(runID string) {
	m.mu.Lock()
	exs := m.byRun[runID]
	delete(m.byRun, runID)
	m.mu.Unlock()
	for _, ex := range exs {
		if ex.ln != nil {
			_ = ex.ln.Close()
		}
		_ = ex.pc.Close()
	}
}

// CloseRun tears down a run's exposures AND prunes its saved allocation —
// the environment is gone (destroy/archive/reap), so the ports must not
// come back on the next restart.
func (m *Manager) CloseRun(runID string) {
	m.teardown(runID)
	m.mu.Lock()
	delete(m.remembered, runID)
	store := m.store
	m.mu.Unlock()
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
		defer cancel()
		if err := store.DeleteExposedPorts(ctx, runID); err != nil {
			slog.Warn("portrelay: prune exposures failed", "run", runID, "err", err)
		}
	}
}

// CloseAll tears down every listener (engine shutdown). The persisted
// allocation is deliberately KEPT so active runs' ports survive the restart.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	runs := make([]string, 0, len(m.byRun))
	for id := range m.byRun {
		runs = append(runs, id)
	}
	m.mu.Unlock()
	for _, id := range runs {
		m.teardown(id)
	}
}

func serve(ln net.Listener, pc ports.PortConn) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			d, err := pc.Dial(context.Background())
			if err != nil {
				return
			}
			defer d.Close()
			go func() {
				_, _ = io.Copy(d, c)
				halfClose(d)
			}()
			_, _ = io.Copy(c, d)
			halfClose(c)
		}(c)
	}
}

// halfClose signals write-EOF without tearing down the read side, so
// request/response protocols drain fully.
func halfClose(c net.Conn) {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}
