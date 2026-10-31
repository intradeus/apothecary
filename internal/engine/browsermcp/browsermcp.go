// Package browsermcp serves a run's agent its browser: the builtin "browser"
// MCP server runs in the workspace as the shim, which streams to the engine,
// which bridges it to a Chromium in the project's browser container — with
// the workspace's ports reachable from that browser. Sessions are live
// connections, never run events, so this lives beside the engine.
package browsermcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"apothecary/internal/ports/browserport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
)

// Command is the workspace command the builtin browser server runs as: the
// shim, bridging its stdio to the engine (Service.Serve), which bridges it on
// to a Chromium in the project's browser container.
var Command = []string{"/opt/apothecary/shim", "browser-mcp"}

// ErrOff marks a browser session where this apothecary has no browser.
var ErrOff = errors.New("this apothecary has no project browser")

// Service opens browser sessions for runs; a nil Browser turns them off.
type Service struct {
	Runs     *run.Reader
	Resolver resolution.Resolver
	Browser  browserport.Browser

	ports *portCache
}

func New(runs *run.Reader, resolver resolution.Resolver, browser browserport.Browser) *Service {
	return &Service{Runs: runs, Resolver: resolver, Browser: browser, ports: &portCache{conns: map[string]envport.PortConn{}}}
}

// Forget is the engine's teardown hook: the browser's exposures of a run's
// ports die with its workspace.
func (s *Service) Forget(runID string) { s.ports.forget(runID) }

// BeforeDestroy is part of the teardown hook; there is nothing to flush.
func (s *Service) BeforeDestroy(context.Context, run.View, *resolution.Resolution) {}

// Session is a run's browser session, checked and ready to serve.
type Session struct {
	browser browserport.Browser
	s       browserport.BrowserSession
	release func()
}

// Open checks that a run's agent may have a browser session —
// this engine has a browser, the run a live workspace whose ports it can
// reach, the project its browser on — and returns it ready to serve. What
// it refuses, it refuses before any stream is opened.
func (s *Service) Open(ctx context.Context, runID string) (*Session, error) {
	if s.Browser == nil {
		return nil, ErrOff
	}
	v, err := s.Runs.View(ctx, runID)
	if err != nil {
		return nil, err
	}
	if v.EnvID == "" || v.ProjectID == "" {
		return nil, fmt.Errorf("run %s has no environment", runID)
	}
	res, err := s.Resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, err
	}
	if !res.Browser {
		res.Close()
		return nil, fmt.Errorf("the project's browser is off — turn it on in Workshop → Settings")
	}
	exposer, ok := res.Envp.(envport.WorkspacePortExposer)
	if !ok {
		res.Close()
		return nil, fmt.Errorf("run %s: its provisioner cannot reach the workspace's ports, so the browser has nothing to show", runID)
	}
	env := envport.EnvID(v.EnvID)
	dial := func(ctx context.Context, port int) (net.Conn, error) {
		return s.ports.dial(ctx, runID, port, func(ctx context.Context) (envport.PortConn, error) {
			return exposer.ExposePort(ctx, env, port)
		})
	}
	return &Session{browser: s.Browser, s: browserport.BrowserSession{Project: res.ProjectID, ProjectName: res.ProjectName, Run: runID, Dial: dial}, release: res.Close}, nil
}

// Serve bridges the session's stdio — the workspace's `shim browser-mcp` —
// to a Chromium in the project's browser container, until either side
// closes.
func (b *Session) Serve(ctx context.Context, stdio io.ReadWriter) error {
	defer b.Close()
	return b.browser.Session(ctx, b.s, stdio)
}

// Close gives up a session that will not be served.
func (b *Session) Close() {
	if b.release != nil {
		b.release()
		b.release = nil
	}
}

// Serve opens and serves one browser session for a run's agent.
func (s *Service) Serve(ctx context.Context, runID string, stdio io.ReadWriter) error {
	sess, err := s.Open(ctx, runID)
	if err != nil {
		return err
	}
	return sess.Serve(ctx, stdio)
}

// Status is the project browser's state, for the settings page.
// project is the project's id.
func (s *Service) Status(ctx context.Context, project string) (browserport.BrowserStatus, error) {
	if s.Browser == nil {
		return browserport.BrowserStatus{}, ErrOff
	}
	return s.Browser.Status(ctx, project)
}

// portCache keeps one exposure per (run, port) for the browser: exposing a
// port starts a relay in the workspace, so doing it per connection would
// pile them up. A cached exposure that no longer dials (the container was
// rebuilt, taking its relays) is exposed again once.
type portCache struct {
	mu    sync.Mutex
	conns map[string]envport.PortConn
}

func (c *portCache) dial(ctx context.Context, runID string, port int, expose func(context.Context) (envport.PortConn, error)) (net.Conn, error) {
	key := fmt.Sprintf("%s\x00%d", runID, port)
	c.mu.Lock()
	pc := c.conns[key]
	c.mu.Unlock()
	if pc != nil {
		if conn, err := pc.Dial(ctx); err == nil {
			return conn, nil
		}
	}
	pc, err := expose(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if old := c.conns[key]; old != nil && old != pc {
		_ = old.Close()
	}
	c.conns[key] = pc
	c.mu.Unlock()
	return pc.Dial(ctx)
}

// forget drops a run's exposures (its environment is gone).
func (c *portCache) forget(runID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, pc := range c.conns {
		if strings.HasPrefix(key, runID+"\x00") {
			_ = pc.Close()
			delete(c.conns, key)
		}
	}
}
