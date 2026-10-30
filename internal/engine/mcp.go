package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"apothecary/internal/browsermcp"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/resolution"
)

// MCP servers (FLOWLANG §1 mcp_servers): the tools an agent may call beyond
// its harness's own. The flow declares them, an agent lists the ones it uses,
// and the engine resolves them when the agent's stage opens its session —
// secrets filled from the project then (not at run creation: a rotated value
// reaches the next stage), the builtin browser turned into the workspace
// command that bridges to the project browser. The resolved servers ride the
// session (agentport.AgentInput.MCPServers); they are never logged, never put in
// an event, and every value they carry is known to the redactor.

// ProjectSecretSource opens a project's secrets (confstore.Service).
type ProjectSecretSource interface {
	ProjectSecrets(ctx context.Context, projectID string) (map[string]string, error)
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
func (x *exec) stageMCP(st *flow.Stage) ([]agentport.MCPServer, error) {
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
	out := make([]agentport.MCPServer, 0, len(sp.MCP))
	for _, name := range sp.MCP {
		m := x.rs.fl.MCPServer(name)
		if m == nil {
			return nil, fmt.Errorf("mcp server %q is not declared in the flow", name)
		}
		for _, s := range m.Secrets {
			if v, ok := secrets[s]; ok {
				x.redact.Add(v, "secret:"+s)
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
func resolveMCPServer(m *flow.MCPServer, secrets map[string]string) (agentport.MCPServer, error) {
	srv := agentport.MCPServer{Name: m.Name, Command: slices.Clone(m.Command), URL: m.URL}
	if m.Builtin == flow.MCPBrowser {
		srv.Command = slices.Clone(browsermcp.Command)
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
		return agentport.MCPServer{}, err
	}
	if srv.Headers, err = fill("header", m.Headers); err != nil {
		return agentport.MCPServer{}, err
	}
	return srv, nil
}

// failStageMCP fails an agent stage whose servers could not be resolved —
// a secret deleted since the run was created — before its session opens.
func (x *exec) failStageMCP(st *flow.Stage, err error) error {
	msg := x.redact.Scrub(err.Error())
	if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonHarnessError, Error: msg}); aerr != nil {
		return aerr
	}
	return x.takeEdge(st, flow.EdgeFail, msg)
}
