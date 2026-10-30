package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"

	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
)

// MCP servers (FLOWLANG §1 mcp_servers): the tools an agent may call beyond
// its harness's own. The flow declares them, an agent lists the ones it uses,
// and the engine resolves them when the agent's stage opens its session —
// secrets filled from the project then (not at run creation: a rotated value
// reaches the next stage), the builtin browser turned into the workspace
// command that bridges to the project browser. The resolved servers ride the
// session (ports.AgentInput.MCPServers); they are never logged, never put in
// an event, and every value they carry is known to the redactor.

// BrowserMCPCommand is the workspace command the builtin browser server runs
// as: the shim, bridging its stdio to the engine (BrowserMCP), which bridges
// it on to a Chromium in the project's browser container.
var BrowserMCPCommand = []string{"/opt/apothecary/shim", "browser-mcp"}

// ProjectSecretSource opens a project's secrets (confstore.Service).
type ProjectSecretSource interface {
	ProjectSecrets(ctx context.Context, projectID string) (map[string]string, error)
}

// SetProjectSecrets installs the source an agent stage re-reads the
// project's secrets from; nil keeps the ones the run was created with.
func (e *Engine) SetProjectSecrets(s ProjectSecretSource) { e.projectSecrets = s }

// ErrBrowserOff marks a browser session where this engine has no browser.
var ErrBrowserOff = errors.New("this apothecary has no project browser")

// SetBrowser installs the project browser (internal/browser); nil disables
// it, and a flow whose agents use it is refused at run creation.
func (e *Engine) SetBrowser(b ports.Browser) {
	e.browser = b
	e.browserPorts = &portCache{conns: map[string]ports.PortConn{}}
}

// warmBrowser starts bringing the project's browser up as a run that will
// use it is created: building its image the first time takes minutes, and
// an agent's MCP client does not wait that long for its server.
func (e *Engine) warmBrowser(res *resolution.Resolution, fl *flow.Flow) {
	if e.browser != nil && fl.UsesBuiltin(flow.MCPBrowser) {
		e.browser.Warm(res.ProjectID, res.ProjectName)
	}
}

// stageMCP resolves the MCP servers of the stage's agent. Secrets are
// re-read from the project when a source is set; each value is taught to
// the redactor before anything can echo it.
func (x *exec) stageMCP(st *flow.Stage) ([]ports.MCPServer, error) {
	sp := x.rs.fl.Agent(st.Agent)
	if sp == nil || len(sp.MCP) == 0 {
		return nil, nil
	}
	secrets := x.res.Secrets
	if x.e.projectSecrets != nil && x.res.ProjectID != "" {
		fresh, err := x.e.projectSecrets.ProjectSecrets(x.ctx, x.res.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("read the project's secrets: %w", err)
		}
		secrets = fresh
	}
	out := make([]ports.MCPServer, 0, len(sp.MCP))
	for _, name := range sp.MCP {
		m := x.rs.fl.MCPServer(name)
		if m == nil {
			return nil, fmt.Errorf("mcp server %q is not declared in the flow", name)
		}
		for _, s := range m.Secrets {
			if v, ok := secrets[s]; ok {
				x.redact.add(v, "secret:"+s)
			}
		}
		srv, err := resolveMCPServer(m, secrets)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, nil
}

// resolveMCPServer fills one server's secrets and gives a builtin its
// workspace command.
func resolveMCPServer(m *flow.MCPServer, secrets map[string]string) (ports.MCPServer, error) {
	srv := ports.MCPServer{Name: m.Name, Command: slices.Clone(m.Command), URL: m.URL}
	if m.Builtin == flow.MCPBrowser {
		srv.Command = slices.Clone(BrowserMCPCommand)
		return srv, nil
	}
	fill := func(field string, in map[string]string) (map[string]string, error) {
		if len(in) == 0 {
			return nil, nil
		}
		out := make(map[string]string, len(in))
		for _, k := range slices.Sorted(maps.Keys(in)) {
			v, err := flow.FillSecrets(in[k], secrets)
			if err != nil {
				return nil, fmt.Errorf("mcp server %q: %s %s: %w", m.Name, field, k, err)
			}
			out[k] = v
		}
		return out, nil
	}
	var err error
	if srv.Env, err = fill("env", m.Env); err != nil {
		return ports.MCPServer{}, err
	}
	if srv.Headers, err = fill("header", m.Headers); err != nil {
		return ports.MCPServer{}, err
	}
	return srv, nil
}

// failStageMCP fails an agent stage whose servers could not be resolved —
// a secret deleted since the run was created — before its session opens.
func (x *exec) failStageMCP(st *flow.Stage, err error) error {
	msg := x.redact.scrub(err.Error())
	if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonHarnessError, Error: msg}); aerr != nil {
		return aerr
	}
	return x.takeEdge(st, flow.EdgeFail, msg)
}

// --- the browser session ------------------------------------------------------

// BrowserMCPSession is a run's browser session, checked and ready to serve.
type BrowserMCPSession struct {
	e       *Engine
	s       ports.BrowserSession
	release func()
}

// OpenBrowserMCP checks that a run's agent may have a browser session —
// this engine has a browser, the run a live workspace whose ports it can
// reach, the project its browser on — and returns it ready to serve. What
// it refuses, it refuses before any stream is opened.
func (e *Engine) OpenBrowserMCP(ctx context.Context, runID string) (*BrowserMCPSession, error) {
	if e.browser == nil {
		return nil, ErrBrowserOff
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.EnvID == "" || v.ProjectID == "" {
		return nil, fmt.Errorf("run %s has no environment", runID)
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		return nil, err
	}
	if !res.Browser {
		res.Close()
		return nil, fmt.Errorf("the project's browser is off — turn it on in Workshop → Settings")
	}
	exposer, ok := res.Envp.(ports.WorkspacePortExposer)
	if !ok {
		res.Close()
		return nil, fmt.Errorf("run %s: its provisioner cannot reach the workspace's ports, so the browser has nothing to show", runID)
	}
	env := ports.EnvID(v.EnvID)
	dial := func(ctx context.Context, port int) (net.Conn, error) {
		return e.browserPorts.dial(ctx, runID, port, func(ctx context.Context) (ports.PortConn, error) {
			return exposer.ExposePort(ctx, env, port)
		})
	}
	return &BrowserMCPSession{e: e, s: ports.BrowserSession{Project: res.ProjectID, ProjectName: res.ProjectName, Run: runID, Dial: dial}, release: res.Close}, nil
}

// Serve bridges the session's stdio — the workspace's `shim browser-mcp` —
// to a Chromium in the project's browser container, until either side
// closes.
func (b *BrowserMCPSession) Serve(ctx context.Context, stdio io.ReadWriter) error {
	defer b.Close()
	return b.e.browser.Session(ctx, b.s, stdio)
}

// Close gives up a session that will not be served.
func (b *BrowserMCPSession) Close() {
	if b.release != nil {
		b.release()
		b.release = nil
	}
}

// BrowserMCP opens and serves one browser session for a run's agent.
func (e *Engine) BrowserMCP(ctx context.Context, runID string, stdio io.ReadWriter) error {
	s, err := e.OpenBrowserMCP(ctx, runID)
	if err != nil {
		return err
	}
	return s.Serve(ctx, stdio)
}

// BrowserStatus is the project browser's state, for the settings page.
// project is the project's id.
func (e *Engine) BrowserStatus(ctx context.Context, project string) (ports.BrowserStatus, error) {
	if e.browser == nil {
		return ports.BrowserStatus{}, ErrBrowserOff
	}
	return e.browser.Status(ctx, project)
}

// portCache keeps one exposure per (run, port) for the browser: exposing a
// port starts a relay in the workspace, so doing it per connection would
// pile them up. A cached exposure that no longer dials (the container was
// rebuilt, taking its relays) is exposed again once.
type portCache struct {
	mu    sync.Mutex
	conns map[string]ports.PortConn
}

func (c *portCache) dial(ctx context.Context, runID string, port int, expose func(context.Context) (ports.PortConn, error)) (net.Conn, error) {
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
