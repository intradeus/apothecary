// Package portrelay publishes exposed workspace ports to the human: one
// engine-host TCP listener per (run, workspace port), each accepted
// browser connection pumped through the provisioner's ports.PortConn.
// Listeners bind kernel-allocated ports on the CPUI's own interface —
// root-level URLs (webapp asset paths work, unlike path-prefix proxying),
// zero fixed ports, any number of concurrent runs. Exposures are
// deliberately in-memory only: they are engine-process-scoped (subprocess
// forwards, unix sockets), so persisting them — e.g. into run events —
// would advertise dead ports after a restart; auto-exposures are simply
// re-created on the recovery path.
//
// Trust level: the listeners carry raw TCP to workspace apps with no auth
// — exactly the CPUI's own posture (the API has none either); both bind
// the same host. The default 127.0.0.1 keeps everything loopback.
package portrelay

import (
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"

	"apothecary/internal/ports"
)

// Exposure is one published port (the GET /runs/{id}/ports row).
type Exposure struct {
	Port int    `json:"port"` // workspace-side port
	URL  string `json:"url"`  // browser-facing engine-host URL
}

type exposure struct {
	ln  net.Listener
	pc  ports.PortConn
	url string
}

type Manager struct {
	host string // listen host = the CPUI interface ("" -> 127.0.0.1)

	mu    sync.Mutex
	byRun map[string]map[int]*exposure
}

func New(host string) *Manager {
	if host == "" {
		host = "127.0.0.1"
	}
	return &Manager{host: host, byRun: map[string]map[int]*exposure{}}
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
// closes the redundant pc.
func (m *Manager) Expose(runID string, port int, pc ports.PortConn) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ex, ok := m.byRun[runID][port]; ok {
		_ = pc.Close()
		return ex.url, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(m.host, "0"))
	if err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("expose port %d: %w", port, err)
	}
	_, lport, _ := net.SplitHostPort(ln.Addr().String())
	ex := &exposure{ln: ln, pc: pc, url: fmt.Sprintf("http://%s/", net.JoinHostPort(m.urlHost(), lport))}
	if m.byRun[runID] == nil {
		m.byRun[runID] = map[int]*exposure{}
	}
	m.byRun[runID][port] = ex
	go serve(ln, pc)
	return ex.url, nil
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

// CloseRun tears down every exposure of a run (env destroy/archive/reap).
func (m *Manager) CloseRun(runID string) {
	m.mu.Lock()
	exs := m.byRun[runID]
	delete(m.byRun, runID)
	m.mu.Unlock()
	for _, ex := range exs {
		_ = ex.ln.Close()
		_ = ex.pc.Close()
	}
}

// CloseAll tears down everything (engine shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	runs := make([]string, 0, len(m.byRun))
	for id := range m.byRun {
		runs = append(runs, id)
	}
	m.mu.Unlock()
	for _, id := range runs {
		m.CloseRun(id)
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
