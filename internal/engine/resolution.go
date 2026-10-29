package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/run"
	"apothecary/internal/signalspec"
)

// Resolution is everything a run needs from its project's configuration
// (DESIGN2 §6.1), produced per run start by the Resolver. Fields are
// port-typed on purpose: the engine never sees kinds — the resolver
// (internal/resolve) flattens harness capabilities into plain funcs, so the
// scenario tests keep running on bare fakes.
type Resolution struct {
	ProjectID   string
	ProjectName string
	// UserID is the project's owner: what a run created on this resolution
	// is owned by, and whose channels its notifications reach.
	UserID       string
	RepoRef      ports.RepoRef
	AgentsMDPath string
	DefaultFlow  string
	// Commands is project.commands: named scripts, run only by a flow's
	// run_ref — none is special to the engine.
	Commands map[string]string
	// Services are the project's services (its app, a database): what a
	// flow's service stages start and run.service.<name> addresses.
	Services []Service
	// GitUser/GitPass form the §11 step 3 credential-store entry; both
	// empty = public repo, no credential helper.
	GitUser string
	GitPass string
	// GitName/GitEmail are the commit author identity stamped in the
	// workspace's `git config user.*` (§11 step 3), configured per repository
	// host integration; either empty ⇒ bootstrap keeps its built-in default.
	GitName  string
	GitEmail string
	// ProviderEnv is the merged env-var map of the project's provider
	// integrations, written to /apothecary/provider.env at bootstrap step 2.
	ProviderEnv map[string]string
	// HarnessSecrets are credential values the harnesses seed into the
	// workspace outside provider.env (their native auth.json blobs). Collected
	// for ONE purpose — feeding the redactor, which can only scrub what the
	// engine knows it injected — never written anywhere from here.
	HarnessSecrets []string
	// EgressDomains is the derived egress allowlist (allow mode): repo host ∪
	// harness/repository host contributions ∪ registry defaults ∪ project
	// extras.
	EgressDomains []string
	// EgressMode ("allow"|"deny", default allow) and EgressDenylist drive the
	// per-project gateway posture; the escape-hatch CIDRs/ports permit specific
	// raw external egress in allow mode.
	EgressMode         string
	EgressDenylist     []string
	EgressBlocked      []string
	EgressAllowedCIDRs []string
	EgressAllowedPorts []int
	// CodeServerExtensions are the project-declared VS Code extension ids
	// (also carried by EditorSettings.Extensions, which is what reaches the
	// adapter): installed into the browser editor when it first opens in a
	// run, together with the definition's own list.
	CodeServerExtensions []string
	// EditorSettings are the project's code-server UI defaults (theme + common
	// prefs) and extensions, applied to the browser editor at OpenEditor time
	// (no rebuild).
	EditorSettings ports.EditorSettings
	// Secrets are the project's secrets, opened: what a flow's MCP servers
	// read where they say ${secrets.<name>}. Checked at run creation, fed to
	// the redactor, and re-read when an agent stage starts (Engine
	// SetProjectSecrets), so a rotated value needs no new run.
	Secrets map[string]string
	// Browser is the project's browser toggle: a flow whose agents use the
	// builtin browser MCP server is refused while it is off.
	Browser bool
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
	// Models lists the model ids (provider/model, as flows spell them) a
	// harness kind in the project's set can reach with its credentials —
	// the provider's live list where it has one. A nil list with no error
	// means the harness takes any model. A nil func: this resolution cannot
	// tell (older wiring), and model checks are skipped.
	Models func(ctx context.Context, harnessKind string) ([]string, error)
	// CheckAuth probes, without spending tokens, whether the providers behind
	// the given models (provider/model) accept the harness kind's
	// credentials. rejected: a run would fail on them; unverified: no probe,
	// or it said nothing either way. A nil func: auth checks are skipped.
	CheckAuth func(ctx context.Context, harnessKind string, models []string) (rejected, unverified []string)
	// HarnessBinary is the fallback harness executable (envcheck probes it
	// with `command -v`, §9); the tools bundle mounted into every workspace
	// provides it.
	HarnessBinary string
	// HarnessBinaries is every harness's executable — envcheck probes them all,
	// since any set member may run. Empty ⇒ just HarnessBinary.
	HarnessBinaries []string

	// ExtraRepos are the project's additional repositories (multi-repo tasks):
	// cloned beside the primary, branched like it, each published as its own
	// merge request when a run changes it. RepoHosts holds a RepoHost port per
	// extra repo that has a locator, keyed by name.
	ExtraRepos []ExtraRepo
	RepoHosts  map[string]ports.RepoHost

	Envp   ports.EnvProvisioner
	Runner ports.AgentRunner
	// Runners maps harness KIND → runner for the full set (per-step harness).
	// A stage pinning `harness: <kind>` runs on Runners[kind]; unpinned stages
	// use Runner (the default). nil in single-harness setups (fakes) — the
	// engine falls back to Runner.
	Runners  map[string]ports.AgentRunner
	RepoHost ports.RepoHost
	// TicketWriter resolves the write side of a ticketsource integration — a
	// tracker action's target — by integration id: the TICKET's (recorded at
	// import), not the project's. nil = this build cannot write back (fakes
	// that don't wire it); an adapter without the capability errors, naming
	// the integration, so run creation can say so.
	TicketWriter func(ctx context.Context, integrationID string) (ports.TicketWriter, error)

	// CredentialIDs are the provider credentials the project's harness
	// integrations reference: a refresh of one of them is pushed into this
	// resolution's live workspaces (PushCredential), and only them.
	CredentialIDs []string
	// AuthBroker serves the harness logins over omp's auth-broker protocol
	// for one run (register: the run's redactor); nil when no harness reads
	// its logins from the broker.
	AuthBroker func(register func(secret string)) http.Handler

	// AuthFiles lists where the harnesses' auth material lives in a workspace,
	// so the engine can delete it when the run ends (kinds.AuthLocator). nil
	// when no configured harness reports any.
	AuthFiles func(ctx context.Context, env ports.EnvID) []string
	// SeedAuth writes harness-native auth material at bootstrap step 2, and
	// again when a login it uses is refreshed; register is told each secret
	// before it is written. nil = nothing to seed.
	SeedAuth func(ctx context.Context, env ports.EnvID, register func(secret string)) error
	// Prepare generates harness-native config at bootstrap step 4 from the
	// agents the RUN's flow declares (FLOWLANG §1, v3) — the caller passes
	// them, because agents are a property of the flow and not of the project.
	// nil = nothing to generate (fakes).
	Prepare func(ctx context.Context, env ports.EnvID, specs []*agentspec.Spec) error
	// InstallSkills makes the run's skills (already at acpagent.SkillsDir)
	// visible to every harness in the project's set, each in its own
	// directory (bootstrap step 4, after Prepare). nil = nothing to install
	// (fakes).
	InstallSkills func(ctx context.Context, env ports.EnvID, names []string) error
	// Release decrements the resolver's provisioner refcount (§6.1 adapter
	// cache); nil is fine.
	Release func()
}

// ExtraRepo is an additional repository of a multi-repo project, as the
// engine needs it (mirrors the config plane's ExtraRepo without the import).
type ExtraRepo struct {
	Name          string
	URL           string
	Locator       string
	DefaultBranch string
}

// repoHostFor returns the repository host port bound to a checkout: the
// project's for the primary, the per-repo one for an extra that has a locator,
// nil for an extra that is context only.
func (r *Resolution) repoHostFor(repo string) ports.RepoHost {
	if repo == "" || repo == event.PrimaryCheckoutName {
		return r.RepoHost
	}
	return r.RepoHosts[repo]
}

func (r *Resolution) release() {
	if r != nil && r.Release != nil {
		r.Release()
	}
}

// actionAvailable reports whether this project can run the stage's action:
// the registry says which integration port the action needs, and the
// resolution says whether the project configured one. Checked at run creation
// (like agent names and run_refs) rather than at parse time, because flows are
// portable data and integrations are per-project.
func (r *Resolution) actionAvailable(st *flow.Stage) error {
	act := actionspec.Get(st.Action)
	if act == nil {
		return fmt.Errorf("action %q is not registered in this build", st.Action)
	}
	switch act.Requires {
	case "":
		return nil
	case actionspec.RequiresRepository:
		if r.RepoHost == nil {
			return fmt.Errorf("action %q needs a repository integration (GitLab, GitHub), which this project has not configured", act.Slug)
		}
	case actionspec.RequiresTracker:
		// Per ticket, not per project: whether THIS ticket's tracker can be
		// written is preflightTicket's question. Here only "can this build
		// write back at all".
		if r.TicketWriter == nil {
			return fmt.Errorf("action %q writes to the ticket's tracker, which this build cannot", act.Slug)
		}
	default:
		return fmt.Errorf("action %q requires %q, which this build cannot verify", act.Slug, act.Requires)
	}
	return nil
}

// preflightTicket is the per-TICKET half of preflight: a tracker action
// writes through the integration the ticket was imported from, so whether
// that integration can write is checked here, where the ticket is known,
// rather than in preflight (per project, also run without a ticket by
// PreflightProject). A ticket with no tracker passes — the engine skips
// those stages, visibly — and so does a flow with no tracker action.
func (e *Engine) preflightTicket(ctx context.Context, res *Resolution, fl *flow.Flow, t Ticket) error {
	if t.SourceID == "" {
		return nil
	}
	for i := range fl.Stages {
		st := &fl.Stages[i]
		if st.Type != flow.StageAction {
			continue
		}
		act := actionspec.Get(st.Action)
		if act == nil || act.Requires != actionspec.RequiresTracker {
			continue
		}
		if res.TicketWriter == nil {
			return fmt.Errorf("preflight: stage %q: action %q writes to the ticket's tracker, which this build cannot", st.ID, st.Action)
		}
		if _, err := res.TicketWriter(ctx, t.SourceID); err != nil {
			return fmt.Errorf("preflight: stage %q: action %q writes to ticket %s's tracker, but %w", st.ID, st.Action, t.SourceKey, err)
		}
		return nil // every tracker stage writes through the same integration
	}
	return nil
}

// signalAvailable reports whether this project can observe the signal a stage
// waits for (or a gate auto-answers on): the registry says which integration
// port it needs, the resolution says whether the project configured one.
// phaseAvailable checks what opening a phase needs from the project: a
// watcher or an outcome on an observed signal needs whoever raises it, like
// a wait stage does — the run would otherwise sit armed on an event nothing
// can observe — and a main chain started on a merge request needs the
// repository that holds it. A main chain started on a ticket, and a watcher
// a person starts by hand, need nothing.
func (r *Resolution) phaseAvailable(ph *flow.Phase) error {
	switch {
	case ph.Observed():
		return r.signalAvailable(ph.On.Signal)
	case ph.OnMR() && r.RepoHost == nil:
		return fmt.Errorf("its runs are started on a merge request (operates_on: %s), which needs a repository integration (GitLab, GitHub) this project has not configured", flow.OperatesOnMR)
	}
	return nil
}

func (r *Resolution) signalAvailable(slug string) error {
	sig := signalspec.Get(slug)
	if sig == nil {
		return fmt.Errorf("signal %q is not registered in this build", slug)
	}
	switch sig.Requires {
	case "":
		return nil
	case signalspec.RequiresRepository:
		if r.RepoHost == nil {
			return fmt.Errorf("signal %q needs a repository integration (GitLab, GitHub), which this project has not configured", sig.Slug)
		}
	default:
		return fmt.Errorf("signal %q requires %q, which this build cannot verify", sig.Slug, sig.Requires)
	}
	return nil
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

// pinnedFlow loads the exact document a run started with (PLAN5 M19). A run
// records its flow's content hash at creation, and since v3 the document
// carries its agents — so pinning by hash means an edit to a flow (or to a
// prompt inside it) never changes a run already in flight. Recovery after a
// restart re-reads the same pinned version.
//
// Falls back to the current document by name when the pinned version cannot
// be resolved: runs created before pinning carry no usable hash, and a
// pruned version should not strand a live run.
func (e *Engine) pinnedFlow(ctx context.Context, projectID, name, hash string) (*flow.Flow, error) {
	ps, ok := e.flows.(flow.PinnedSource)
	if !ok || hash == "" {
		return e.flows.Get(ctx, projectID, name)
	}
	f, err := ps.GetVersion(ctx, projectID, name, hash)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, flow.ErrNotFound) {
		return nil, err
	}
	e.log.Warn("pinned flow version is gone; falling back to the current document",
		"flow", name, "pinned", hash)
	return e.flows.Get(ctx, projectID, name)
}

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
