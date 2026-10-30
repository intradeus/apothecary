package resolution

import (
	"fmt"

	"apothecary/internal/flow"
)

// MCPAvailable is run creation's check of what the flow's MCP servers need
// from the project: every secret they read, and the browser on when an agent
// uses it. A command server's program cannot be checked before the
// workspace exists; a missing one fails its stage.
func (r *Resolution) MCPAvailable(fl *flow.Flow, haveBrowser bool) error {
	for _, m := range fl.MCPServers {
		for _, s := range m.Secrets {
			if _, ok := r.Secrets[s]; !ok {
				return fmt.Errorf("mcp server %q reads project secret %q, which the project does not have — add it in Workshop → Settings → Secrets", m.Name, s)
			}
		}
	}
	if fl.UsesBuiltin(flow.MCPBrowser) {
		if !r.Browser {
			return fmt.Errorf("an agent uses the builtin %q mcp server, and the project's browser is off — turn on the project's browser in Workshop → Settings", flow.MCPBrowser)
		}
		if !haveBrowser {
			return fmt.Errorf("an agent uses the builtin %q mcp server, and this apothecary runs no project browser", flow.MCPBrowser)
		}
	}
	return nil
}
