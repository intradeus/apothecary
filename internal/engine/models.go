package engine

import (
	"strings"

	"apothecary/internal/modelcheck"
)

// checkAgentModels is modelcheck.AgentModels for the run's flow and workspace.
// Harnesses that cannot be asked are logged, not failed: their stage
// surfaces any problem itself.
func (x *exec) checkAgentModels() []string {
	missing, unasked := modelcheck.AgentModels(x.ctx, x.res, x.rs.EnvID, x.rs.Flow.Stages)
	if len(unasked) > 0 {
		x.e.log.Warn("could not ask the workspace agent for its models",
			"run", x.rs.RunID, "harnesses", strings.Join(unasked, "; "))
	}
	return missing
}
