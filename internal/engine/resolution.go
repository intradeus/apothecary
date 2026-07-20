package engine

import (
	"context"
	"errors"
	"fmt"

	"factory/internal/ports"
	"factory/internal/run"
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
	// ProviderEnv is the merged env-var map of the project's provider
	// integrations, written to /factory/provider.env at bootstrap step 2.
	ProviderEnv map[string]string
	// EgressDomains is the derived firewall allowlist (DESIGN2 §8.4).
	EgressDomains []string
	// Image is the built workspace image tag; empty on the M7
	// custom-template compatibility path.
	Image string
	// HarnessKind names the project's harness; flows pinning a different
	// harness are refused at preflight (§6.3).
	HarnessKind string
	// HarnessBinary is the harness executable (envcheck probes it with
	// `command -v`, §9).
	HarnessBinary string

	Envp   ports.EnvProvisioner
	Runner ports.AgentRunner
	Forge  ports.Forge

	// SeedAuth writes harness-native auth material at bootstrap step 2;
	// nil = nothing to seed.
	SeedAuth func(ctx context.Context, env ports.EnvID) error
	// Prepare generates harness-native config at bootstrap step 4; nil =
	// nothing to generate (fakes).
	Prepare func(ctx context.Context, env ports.EnvID) error
	// Release decrements the resolver's provisioner refcount (§6.1 adapter
	// cache); nil is fine.
	Release func()
}

func (r *Resolution) release() {
	if r != nil && r.Release != nil {
		r.Release()
	}
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
