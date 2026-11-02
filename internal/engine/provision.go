package engine

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"apothecary/internal/platform/logs"
	"apothecary/internal/platform/telemetry"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/bootstrap"
	"apothecary/internal/workspace/wsname"
)

// workspaceLink is what run.workspace hands a person: the workspace's first
// app link (a port exposed at provision), "" when the environment declared
// none — the ping still renders, just without a link. The workspace itself
// is not behind a URL the log records: the run page has its terminal and
// editor, the SSH door its ssh route.
func workspaceLink(links []event.Link) string {
	for _, l := range links {
		if l.URL != "" {
			return l.URL
		}
	}
	return ""
}

func (x *exec) ensureEnv() (err error) {
	// The span covers re-attach and provisioning alike: how long the
	// workspace took to be usable. Its own trace, linked to the run's marker:
	// it runs before (and between) the attempts that each have theirs. The
	// run_id attribute finds it beside them ({span.apothecary.run_id=…}).
	pctx, span := tracer.Start(x.ctx, "workspace.provision", trace.WithNewRoot(),
		trace.WithLinks(trace.Link{SpanContext: telemetry.RunSpanContext(x.rs.View.RunID)}),
		trace.WithAttributes(attribute.String("apothecary.run_id", x.rs.View.RunID)))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	rs := x.rs
	// Egress policy is delivered to the per-project gateway by the provisioner
	// at Provision time (from the EnvSpec egress fields), so there is nothing to
	// prime here.
	if rs.EnvID != "" {
		status, err := x.res.Envp.Status(x.ctx, rs.EnvID)
		// A container caught mid-restart reads Starting/Stopped for a moment;
		// re-probe once after a short pause before deciding to re-provision, so
		// a transient blip doesn't nuke a workspace that's just coming back up.
		// EnvAbsent is skipped — a genuinely gone container won't reappear.
		if err == nil && status != envport.EnvRunning && status != envport.EnvAbsent {
			select {
			case <-time.After(recoveryReprobeDelay):
			case <-x.ctx.Done():
				return context.Cause(x.ctx)
			}
			status, err = x.res.Envp.Status(x.ctx, rs.EnvID)
		}
		if err != nil {
			if x.ctx.Err() != nil {
				return context.Cause(x.ctx)
			}
			// Transient (host daemon blip, cluster unreachable): re-provisioning a
			// healthy env and re-running bootstrap on top of it would be worse
			// than waiting for the next boot.
			return fmt.Errorf("recovery: env status: %w", err)
		}
		if status == envport.EnvRunning {
			// Re-establish the reverse tunnel — it died with the previous
			// engine process, so the workspace's engine/proxy loopback
			// forwards are gone (found live: git push to 127.0.0.1:<proxy>
			// refused after an engine restart + resume).
			if r, ok := x.res.Envp.(envport.WorkspaceReattacher); ok {
				if err := r.Reattach(x.ctx, rs.EnvID); err != nil {
					if x.ctx.Err() != nil {
						return context.Cause(x.ctx)
					}
					return fmt.Errorf("recovery: re-establish workspace tunnel: %w", err)
				}
			}
			// Per-boot workspace state changes across restarts: the run token.
			// Refresh it so the shim re-authenticates. Egress needs no refresh —
			// the per-project gateway kept filtering across the restart.
			if err := x.writeRunEnv(rs.EnvID); err != nil {
				x.e.log.WarnContext(x.bg, "refreshing run.env on recovery failed", logs.RunID, rs.RunID, "err", err)
			}
			// Re-seed the credentials. A resumed run re-enters a workspace the
			// terminal scrub emptied (secrets.go), so without this "try again"
			// would run with no model auth and no git token. Idempotent, so the
			// paths that never scrubbed (play, crash recovery) just rewrite the
			// same bytes.
			if err := x.reseedSecrets(rs.EnvID); err != nil {
				return fmt.Errorf("recovery: re-seed workspace credentials: %w", err)
			}
			// Exposures also died with the previous process; re-create them
			// (fresh ports — the live registry, not the old env_ready event,
			// is what the UI trusts).
			x.e.runPorts.AutoExpose(x.ctx, rs.RunID, x.res.Envp, rs.EnvID)
			// Re-materialize goal + answer artifacts into the recovered
			// workspace (idempotent; the associations are durable).
			x.deliverRunAttachments()
			return nil
		}
		x.e.log.WarnContext(x.bg, "environment not running; re-provisioning — whatever was changed by hand in the old container is gone with it",
			logs.RunID, rs.RunID, "env", rs.EnvID, "status", status)
	}
	// The first provisioning announces itself (the timeline's "provisioning
	// workspace…"); a re-provisioning after a reap does not. The run may
	// already be past pending — a remote action first in the flow runs
	// before any workspace — so the record of the request is what decides.
	if !rs.EnvRequested {
		if err := x.append(&event.EnvRequested{}); err != nil {
			return err
		}
	}
	// The container/workspace name derives from the branch STEM, not the
	// suffixed actual branch: base already ends where the run-id tail begins,
	// so wsname.For re-appending the tail would double it (apo-…-h1-h1).
	// Legacy runs have no base — fall back to the actual branch.
	nameBranch := rs.View.BranchBase
	if nameBranch == "" {
		nameBranch = rs.Branch
	}
	spec := envport.EnvSpec{
		RunID:   rs.RunID,
		RepoURL: x.res.RepoRef.URL,
		Branch:  rs.Branch,
		Name:    wsname.For(nameBranch, rs.RunID),
		Project: x.res.ProjectID,
		// The name is a docker label only; the id keys the gateway and caches.
		ProjectName:        x.res.ProjectName,
		EgressDomains:      x.res.EgressDomains,
		EgressMode:         x.res.EgressMode,
		EgressDenylist:     x.res.EgressDenylist,
		EgressBlocked:      x.res.EgressBlocked,
		EgressAllowedCIDRs: x.res.EgressAllowedCIDRs,
		EgressAllowedPorts: x.res.EgressAllowedPorts,
		// For clone-first provisioners (devcontainer): the engine-side
		// checkout needs the repository host credential before the env exists.
		GitUser: x.res.GitUser,
		GitPass: x.res.GitPass,
	}
	// Whatever provisioning + bootstrap print is captured on every exit past
	// this point — ready, env_failed, abort mid-build — so the log survives
	// the workspace itself (see publishProvisionLog).
	defer x.publishProvisionLog(spec.Name)
	id, err := x.res.Envp.Provision(pctx, spec)
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		id, err = x.res.Envp.Provision(pctx, spec) // §2: failed after 1 retry
	}
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		return x.append(&event.EnvFailed{Error: err.Error()})
	}
	rs.EnvID = id
	if err := x.bootstrap(id); err != nil {
		return err
	}
	if rs.Done {
		return nil
	}
	if err := probeEgress(x.ctx, x.res.Envp, id, x.res); err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		// Kept, like a failed bootstrap's: the workspace is the evidence.
		return x.append(&event.EnvFailed{Error: err.Error(), EnvID: string(id)})
	}
	// After bootstrap (repo cloned, .apothecary/ git-excluded): deliver the
	// human's goal + answer artifacts into the workspace for the agent.
	x.deliverRunAttachments()
	// The workspace's links are its app ports: auto-expose the declared +
	// previously-exposed ones; safe to fold into env_ready because this
	// append happens on the run's own writer goroutine BEFORE the event
	// exists (out-of-band appends would race the seq and clobber run state
	// — see portrelay). The live registry stays authoritative (GET
	// /runs/{id}/ports); persisted allocations make the re-exposed URLs
	// match the pre-restart ones (best-effort). Getting INTO the workspace
	// is not a link the log records: the run page has its terminal and
	// editor, the SSH door its ssh/VS Code route (GET /runs/{id}/ssh).
	evLinks := x.e.runPorts.AutoExpose(x.ctx, x.rs.RunID, x.res.Envp, id)
	return x.append(&event.EnvReady{EnvID: string(id), Links: evLinks})
}

// bootstrap runs the §11 ordered steps. A setup failure is a deterministic
// env_failed with the command's output tail (§1) — agents never repair the
// environment.
func (x *exec) writeRunEnv(id envport.EnvID) error {
	runEnv := fmt.Sprintf("APOTHECARY_RUN_ID=%s\nAPOTHECARY_RUN_TOKEN=%s\nAPOTHECARY_ENGINE_URL=%s\n",
		x.rs.RunID, x.e.RunToken(x.rs.RunID), x.e.engineURLFor(x.res.Envp, id))
	return x.res.Envp.WriteFile(x.ctx, id, "/apothecary/run.env", []byte(runEnv), 0o600)
}

// reseedSecrets restores the credential files in a workspace the loop is
// re-entering, undoing the terminal scrub (secrets.go) for a resumed run. It
// writes exactly what bootstrap step 2 writes — same function, so the two can
// never drift — and touches nothing else: the checkout, the branch and the
// agent's commits are already there.
func (x *exec) reseedSecrets(id envport.EnvID) error {
	res := x.res
	x.e.authSink.Set(x.rs.RunID, res, id, x.redact)
	return bootstrap.WriteWorkspaceSecrets(x.ctx, res.Envp, id, bootstrap.BootstrapSpec{
		RepoURL:     res.RepoRef.URL,
		ProviderEnv: x.e.authSink.ProviderEnv(res, x.rs.RunID, x.e.engineURLFor(res.Envp, id)),
		GitUser:     res.GitUser,
		GitPass:     res.GitPass,
		SeedAuth:    res.SeedAuth,
		Register:    x.registerSecret,
		Extras:      x.extraCheckouts(),
	})
}

// registerSecret teaches the run's redactor a credential the engine is about
// to write into the workspace.
func (x *exec) registerSecret(s string) { x.redact.Add(s, "harness-auth") }

func (x *exec) bootstrap(id envport.EnvID) error {
	res := x.res
	x.e.authSink.Set(x.rs.RunID, res, id, x.redact)
	trees, err := x.e.skillTrees(x.ctx, x.rs.Skillsets)
	if err != nil {
		err = fmt.Errorf("bootstrap skills: %w", err)
	} else {
		err = bootstrap.RunBootstrap(x.ctx, res.Envp, id, bootstrap.BootstrapSpec{
			RunID:         x.rs.RunID,
			Token:         x.e.RunToken(x.rs.RunID),
			EngineURL:     x.e.engineURLFor(res.Envp, id),
			RepoURL:       res.RepoRef.URL,
			SourceBranch:  bootstrap.SourceBranchOr(x.rs.SourceBranch, res.RepoRef.DefaultBranch),
			Branch:        x.rs.Branch,
			Fetch:         x.primaryFetch(),
			AgentsMDPath:  res.AgentsMDPath,
			ProviderEnv:   x.e.authSink.ProviderEnv(res, x.rs.RunID, x.e.engineURLFor(res.Envp, id)),
			GitUser:       res.GitUser,
			GitPass:       res.GitPass,
			GitName:       res.GitName,
			GitEmail:      res.GitEmail,
			SeedAuth:      res.SeedAuth,
			Register:      x.registerSecret,
			Prepare:       res.Prepare,
			Agents:        x.rs.Flow.WorkspaceAgents(),
			Timeout:       x.rs.Flow.Defaults.Timeout,
			Extras:        x.extraCheckouts(),
			Skills:        trees,
			SkillPins:     x.rs.Skillsets,
			InstallSkills: res.InstallSkills,
		})
	}
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		// The workspace exists (Provision succeeded) — record its id so
		// capacity counting, archive and the reaper can destroy it.
		return x.append(&event.EnvFailed{Error: err.Error(), EnvID: string(id)})
	}
	return nil
}
