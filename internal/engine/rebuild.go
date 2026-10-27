package engine

import (
	"context"
	"errors"
	"fmt"

	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// ErrRunActive refuses a workspace op while the run's loop is live (mid-stage
// or waiting-on-human). Rebuild in v1 requires an idle run: Pause → Rebuild →
// Play (rebuilding under the loop would race ensureEnv's un-gated reattach).
var ErrRunActive = errors.New("run is still active; pause or abort it first")

// RebuildEnv recreates a run's workspace container IN PLACE, keeping the run
// and its engine-side checkout (agent commits survive), then re-runs the
// idempotent bootstrap (a fresh container has no /apothecary state). Distinct
// from restart (a new run) and resume (re-enter a stalled stage). The op-gate
// serializes it against the reaper/reconciler/destroy; a live run refuses.
func (e *Engine) RebuildEnv(ctx context.Context, runID string, noCache bool) (err error) {
	// Fail-fast pre-checks BEFORE claiming the gate, so a refused call never
	// touches op-state. A live loop → refuse (see ErrRunActive).
	e.mu.Lock()
	_, active := e.handles[runID]
	e.mu.Unlock()
	if active {
		return fmt.Errorf("run %s: %w", runID, ErrRunActive)
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		return fmt.Errorf("run %s not found", runID)
	}
	v := run.Reduce(evs)
	if v.EnvID == "" {
		return fmt.Errorf("run %s has no workspace to rebuild", runID)
	}
	if v.ProjectID == "" {
		return fmt.Errorf("run %s has no project (pre-v2); cannot rebuild", runID)
	}

	rel, err := e.beginEnvOp(runID, "rebuilding")
	if err != nil {
		return err
	}
	defer func() { rel(err) }()

	res, err := e.resolveForRun(ctx, v)
	if err != nil {
		return err
	}
	defer res.release()
	rebuilder, ok := res.Envp.(ports.WorkspaceRebuilder)
	if !ok {
		return fmt.Errorf("run %s: provisioner does not support rebuild", runID)
	}
	fl, err := e.runFlow(ctx, v)
	if err != nil {
		return fmt.Errorf("run %s: load flow: %w", runID, err)
	}

	if err = e.appendAndEmit(ctx, runID, &event.EnvRebuildStarted{EnvID: v.EnvID}); err != nil {
		return err
	}

	// Same spec ensureEnv builds (runloop.go). The name derives from the
	// branch STEM (BranchBase), not the suffixed branch, or the CLI doubles
	// the run-id tail.
	nameBranch := v.BranchBase
	if nameBranch == "" {
		nameBranch = v.Branch
	}
	spec := ports.EnvSpec{
		RunID:                runID,
		NoCache:              noCache,
		RepoURL:              res.RepoRef.URL,
		Branch:               v.Branch,
		Name:                 workspaceName(nameBranch, runID),
		Project:              res.ProjectID,
		ProjectName:          res.ProjectName,
		HarnessInstall:       res.HarnessInstall,
		EgressDomains:        res.EgressDomains,
		EgressMode:           res.EgressMode,
		EgressDenylist:       res.EgressDenylist,
		EgressBlocked:        res.EgressBlocked,
		EgressAllowedCIDRs:   res.EgressAllowedCIDRs,
		EgressAllowedPorts:   res.EgressAllowedPorts,
		CodeServerExtensions: res.CodeServerExtensions,
		GitUser:              res.GitUser,
		GitPass:              res.GitPass,
	}
	// Egress policy is delivered to the per-project gateway by the provisioner's
	// Rebuild (from the EnvSpec egress fields) — nothing to prime here.

	id := ports.EnvID(v.EnvID)
	if rerr := rebuilder.Rebuild(ctx, spec); rerr != nil {
		err = fmt.Errorf("rebuild %s: %w", runID, rerr)
		return e.finishRebuild(ctx, runID, v.EnvID, err)
	}

	// A fresh container lost /apothecary/* (run.env, provider.env, agent
	// configs, git creds — they live in the container FS, not the bind mount).
	// Re-run the idempotent bootstrap: the surviving checkout means the clone
	// no-ops and agent commits are preserved.
	if berr := RunBootstrap(ctx, res.Envp, id, BootstrapSpec{
		RunID:        runID,
		Token:        e.RunToken(runID),
		EngineURL:    e.engineURLFor(res.Envp, id),
		RepoURL:      res.RepoRef.URL,
		SourceBranch: sourceBranchOr(v.SourceBranch, res.RepoRef.DefaultBranch),
		Branch:       v.Branch,
		AgentsMDPath: res.AgentsMDPath,
		ProviderEnv:  res.ProviderEnv,
		GitUser:      res.GitUser,
		GitPass:      res.GitPass,
		GitName:      res.GitName,
		GitEmail:     res.GitEmail,
		SeedAuth:     res.SeedAuth,
		Prepare:      res.Prepare,
		Agents:       fl.AgentSpecs(),
		Timeout:      fl.Defaults.Timeout,
		Extras:       viewExtras(v),
	}); berr != nil {
		err = fmt.Errorf("rebuild %s: bootstrap: %w", runID, berr)
		return e.finishRebuild(ctx, runID, v.EnvID, err)
	}

	// Re-deliver artifacts + re-expose ports into the fresh container, reusing
	// the run-loop helpers via a minimal exec (these append no events).
	x := &exec{e: e, rs: &runState{runID: runID, view: v, fl: fl, envID: id}, res: res, ctx: ctx, bg: ctx}
	x.deliverRunAttachments()
	x.autoExpose(id)

	// One targeted probe so the badge returns to live immediately instead of
	// waiting up to 30s for the next reconcile pass — and the "config changed"
	// hint goes with it: the container was just built from the current inputs.
	if st, serr := res.Envp.Status(ctx, id); serr == nil {
		e.envStatusMu.Lock()
		e.envStatus[runID] = st
		delete(e.envDirty, runID)
		e.envStatusMu.Unlock()
	}
	return e.finishRebuild(ctx, runID, v.EnvID, nil)
}

// finishRebuild emits the terminal rebuild event and returns opErr (which the
// deferred release reads to set/clear the op badge). A completed event bumps
// UpdatedAt, resetting the reaper window (see run.Apply).
func (e *Engine) finishRebuild(ctx context.Context, runID, envID string, opErr error) error {
	if opErr != nil {
		if aerr := e.appendAndEmit(ctx, runID, &event.EnvRebuildFailed{EnvID: envID, Error: opErr.Error()}); aerr != nil {
			e.log.Warn("record env_rebuild_failed", "run", runID, "err", aerr)
		}
		return opErr
	}
	return e.appendAndEmit(ctx, runID, &event.EnvRebuildCompleted{EnvID: envID})
}

// viewExtras is a run's extra checkouts as the view records them.
func viewExtras(v run.View) []ExtraCheckout {
	var out []ExtraCheckout
	for _, co := range v.Checkouts {
		if co.Primary {
			continue
		}
		out = append(out, ExtraCheckout{Name: co.Name, Dir: co.Dir, URL: co.URL, Branch: co.Branch, Source: co.Source})
	}
	return out
}
