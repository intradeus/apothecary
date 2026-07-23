package engine

import (
	"context"
	"errors"
	"fmt"

	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// Resolution is everything a run needs from its project's configuration
// (DESIGN2 §6.1), produced per run start by the Resolver. Fields are
// port-typed on purpose: the engine never sees kinds — the resolver
// (internal/resolve) flattens harness capabilities into plain funcs, so the
// scenario tests keep running on bare fakes.
type Resolution struct {
	ProjectID    string
	ProjectName  string
	RepoRef      ports.RepoRef
	AgentsMDPath string
	DefaultFlow  string
	// Commands is project.commands; "setup" is OPTIONAL in v2 (absent =
	// bootstrap step 6 skipped).
	Commands map[string]string
	// GitUser/GitPass form the §11 step 3 credential-store entry; both
	// empty = public repo, no credential helper.
	GitUser string
	GitPass string
	// GitName/GitEmail are the commit author identity stamped in the
	// workspace's `git config user.*` (§11 step 3), configured per forge
	// integration; either empty ⇒ bootstrap keeps its built-in default.
	GitName  string
	GitEmail string
	// ProviderEnv is the merged env-var map of the project's provider
	// integrations, written to /apothecary/provider.env at bootstrap step 2.
	ProviderEnv map[string]string
	// EgressDomains is the derived firewall allowlist (DESIGN2 §8.4).
	EgressDomains []string
	// Image is the built workspace image tag (§8 pipeline); empty on the
	// custom-template path, where the template bakes its own image.
	Image string
	// EnsureImage makes Image present on the docker daemon, returning
	// whether it was already there (§8.3 image_built{cached}). nil on the
	// custom-template path.
	EnsureImage func(ctx context.Context) (cached bool, err error)
	// HarnessKind is a deterministic FALLBACK harness kind (the first
	// available in the project's set) — NOT a project-chosen default. The
	// real default is the flow's defaults.harness, applied per stage; this
	// is reached only by a stage with no harness kind, which the required
	// flow default prevents. Kept so single-harness callers (fakes, envcheck)
	// have one concrete kind.
	HarnessKind string
	// HarnessKinds are every harness kind in the project's available set
	// (per-step harness). A stage/flow may only name one of these; preflight
	// refuses others. Empty ⇒ just HarnessKind.
	HarnessKinds []string
	// HarnessBinary is the fallback harness executable (envcheck probes it
	// with `command -v`, §9).
	HarnessBinary string
	// HarnessInstall is how to install each harness INSIDE the workspace,
	// for provisioners that don't get the §8 image (a repo-authored
	// devcontainer runs the repo's own image, which has no harness). Handed
	// to the adapter via EnvSpec; ignored by provisioners whose image
	// already carries them.
	HarnessInstall []ports.HarnessInstall
	// HarnessBinaries is every harness's executable — envcheck probes them all,
	// since any set member may run. Empty ⇒ just HarnessBinary.
	HarnessBinaries []string

	Envp   ports.EnvProvisioner
	Runner ports.AgentRunner
	// Runners maps harness KIND → runner for the full set (per-step harness).
	// A stage pinning `harness: <kind>` runs on Runners[kind]; unpinned stages
	// use Runner (the default). nil in single-harness setups (fakes) — the
	// engine falls back to Runner.
	Runners map[string]ports.AgentRunner
	Forge   ports.Forge

	// SeedAuth writes harness-native auth material at bootstrap step 2;
	// nil = nothing to seed.
	SeedAuth func(ctx context.Context, env ports.EnvID) error
	// Prepare generates harness-native config at bootstrap step 4 — the
	// resolver closes over the project's agent library specs (DESIGN3 §1);
	// nil = nothing to generate (fakes).
	Prepare func(ctx context.Context, env ports.EnvID) error
	// AgentNames lists the project's agent library entries (preflight:
	// every agent stage must reference one).
	AgentNames []string
	// AgentHashes maps agent name -> current content version; stage_started
	// events record the hash so analytics correlate outcome with prompt
	// version.
	AgentHashes map[string]string
	// Release decrements the resolver's provisioner refcount (§6.1 adapter
	// cache); nil is fine.
	Release func()
}

func (r *Resolution) release() {
	if r != nil && r.Release != nil {
		r.Release()
	}
}

// runnerFor returns the runner for a stage's pinned harness kind, falling
// back to the default Runner when the stage pins nothing or the set isn't
// populated (single-harness setups). Preflight guarantees a pinned kind is
// in the set, so a miss here can only be the fallback.
func (r *Resolution) runnerFor(harnessKind string) ports.AgentRunner {
	if harnessKind != "" && r.Runners != nil {
		if run, ok := r.Runners[harnessKind]; ok {
			return run
		}
	}
	return r.Runner
}

// harnessSet is the list of pinnable harness kinds, for error messages.
func (r *Resolution) harnessSet() []string {
	if len(r.HarnessKinds) > 0 {
		return r.HarnessKinds
	}
	return []string{r.HarnessKind}
}

// harnessInSet reports whether a stage may pin the given harness kind: it
// must be the default or a member of the resolved set.
func (r *Resolution) harnessInSet(harnessKind string) bool {
	if harnessKind == r.HarnessKind {
		return true
	}
	for _, k := range r.HarnessKinds {
		if k == harnessKind {
			return true
		}
	}
	return false
}

// Resolver turns a project id into a Resolution. Implementations cache
// adapter instances per integration; config edits apply to new resolutions
// only (§6.1).
type Resolver interface {
	Resolve(ctx context.Context, projectID string) (*Resolution, error)
}

// ErrUnresolvable marks a resolution failure that will never heal
// (project/integration deleted, pre-v2 run with no project): the run is
// failed terminally. Any other resolver error is treated as transient —
// the run stays non-terminal and recovery retries next boot.
var ErrUnresolvable = errors.New("run configuration unresolvable")

// resolveForRun resolves a run's project, mapping the pre-v2 no-project
// case to ErrUnresolvable (terminal, honest — the run can never proceed).
func (e *Engine) resolveForRun(ctx context.Context, v run.View) (*Resolution, error) {
	if v.ProjectID == "" {
		return nil, fmt.Errorf("%w: run %s has no project (created pre-v2)", ErrUnresolvable, v.RunID)
	}
	return e.resolver.Resolve(ctx, v.ProjectID)
}

// SettingsSource reads the §6.4 policy settings — on every admission
// decision, no cache, no watch.
type SettingsSource interface {
	Limits(ctx context.Context) (Limits, error)
}

// StaticResolver serves one fixed Resolution (tests, envcheck).
type StaticResolver struct{ R *Resolution }

func (s StaticResolver) Resolve(context.Context, string) (*Resolution, error) {
	if s.R == nil {
		return nil, ErrUnresolvable
	}
	return s.R, nil
}

// StaticSettings serves fixed limits (tests, envcheck).
type StaticSettings struct{ L Limits }

func (s StaticSettings) Limits(context.Context) (Limits, error) { return s.L, nil }
