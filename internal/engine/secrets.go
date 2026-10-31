package engine

import (
	"context"
	"strings"

	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
)

// Workspace credential lifecycle.
//
// Bootstrap seeds a workspace's credentials once (WriteWorkspaceSecrets) and
// they stay for as long as the run is live — the agent needs them while it
// works, so there is nothing to gain from re-seeding per stage.
//
// What the engine DOES own is the other end: a workspace deliberately outlives
// its run (completed envs wait for the reaper; abandoned/failed ones are kept
// indefinitely for inspection, and "Open editor" gives a human a terminal in
// them). A dead run has no business still holding a live repository host token
// and live model keys, so reaching a terminal state wipes them. A resumed run
// re-enters the same workspace, so ensureEnv puts them back.
//
// This bounds exposure; it is not a boundary. While a run is live the agent
// legitimately has the credentials and can read them — that is the case
// credential brokering would address, not this.

// scrubbedFiles are the engine-written credential files. The harnesses' own
// auth material is added per-run from Resolution.AuthFiles.
//
// run.env is deliberately NOT scrubbed: its bearer token is scoped to one run,
// is regenerated every boot, and identifies the workspace for inspection — it
// is the weak, run-scoped credential, not a standing one.
var scrubbedFiles = []string{
	"/apothecary/provider.env",
	"/apothecary/git-credentials",
}

// ScrubWorkspaceSecrets deletes a finished run's credentials from its
// workspace, leaving everything else (the checkout, the agent's commits, the
// logs) intact for inspection. Best-effort by design: the workspace may already
// be stopped or destroyed, and neither is a reason to fail the run that just
// ended — the loss is a longer exposure window, which is logged.
func (e *Engine) ScrubWorkspaceSecrets(ctx context.Context, v run.View) {
	if v.EnvID == "" || v.ProjectID == "" {
		return
	}
	res, err := e.resolver.Resolve(ctx, v.ProjectID)
	if err != nil {
		e.log.Warn("workspace secret scrub skipped: project unresolvable",
			"run", v.RunID, "env", v.EnvID, "err", err)
		return
	}
	defer res.Close()
	id := envport.EnvID(v.EnvID)

	// A live agent process holds the credentials in memory and would keep
	// using them: stop it before removing the files.
	for _, kind := range append([]string{res.HarnessKind}, res.HarnessKinds...) {
		if st, ok := res.RunnerFor(kind).(agentport.AgentStopper); ok {
			if err := st.StopAgent(ctx, id); err != nil {
				e.log.Warn("could not stop the workspace agent of a finished run",
					"run", v.RunID, "env", v.EnvID, "err", err)
			}
			break // one sidecar per workspace: one stop covers every harness
		}
	}

	paths := append([]string(nil), scrubbedFiles...)
	if res.AuthFiles != nil {
		// Harness auth hangs off the workspace user's home, so the paths are
		// resolved against the live env; a failure here yields none and the
		// engine-written files are still removed.
		paths = append(paths, res.AuthFiles(ctx, id)...)
	}
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" {
			quoted = append(quoted, shell.Quote(p))
		}
	}
	if len(quoted) == 0 {
		return
	}
	out, err := res.Envp.Exec(ctx, id, envport.Command{
		Script: "rm -f " + strings.Join(quoted, " "),
	})
	switch {
	case err != nil:
		e.log.Warn("workspace secret scrub failed; credentials remain in the workspace",
			"run", v.RunID, "env", v.EnvID, "err", err)
	case out.ExitCode != 0:
		e.log.Warn("workspace secret scrub failed; credentials remain in the workspace",
			"run", v.RunID, "env", v.EnvID, "exit", out.ExitCode, "output", shell.Tail(out.Output, 512))
	default:
		e.log.Info("scrubbed workspace credentials of finished run",
			"run", v.RunID, "env", v.EnvID, "files", len(quoted))
	}
}
