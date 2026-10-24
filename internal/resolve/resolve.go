// Package resolve implements engine.Resolver (DESIGN2 §6.1): project id →
// adapter set. It is the only place kinds and confstore meet the engine —
// harness capabilities are flattened into the port-typed Resolution.
//
// Provisioner instances are cached per (integration id, updated_at, template)
// and refcounted by active resolutions: a config edit makes new resolutions
// construct a fresh instance while in-flight runs keep theirs; the stale
// instance closes when its last run releases it. The repository host, harness
// and provider adapters are stateless HTTP/exec wrappers and are simply
// constructed per resolution.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"apothecary/internal/agentspec"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/kinds"
	"apothecary/internal/ports"
	"apothecary/internal/wsimage"
)

type Resolver struct {
	conf       *confstore.Service
	reg        *kinds.Registry
	runsDir    string
	enginePort int
	// sandboxRuntime is deployment-wide (APOTHECARY_SANDBOX_RUNTIME), not
	// per-integration: it names a runtime the HOST daemon must have.
	sandboxRuntime string
	builder        *wsimage.Builder

	mu    sync.Mutex
	cache map[string]*provEntry // provisioner integration id -> entry
}

type provEntry struct {
	key  string // updatedAt|template — instance generation
	prov kinds.Provisioner
	refs int
	// stale marks a superseded generation: closed when refs hits 0.
	stale bool
}

// New builds the resolver. The builder exists solely to compile + cache the
// static in-workspace shim (wsimage.EnsureShim → ProvisionerDeps.ShimBin).
// sandboxRuntime is APOTHECARY_SANDBOX_RUNTIME (empty ⇒ privileged sandbox).
func New(conf *confstore.Service, reg *kinds.Registry, runsDir string, enginePort int, sandboxRuntime string) *Resolver {
	return &Resolver{
		conf:           conf,
		reg:            reg,
		runsDir:        runsDir,
		enginePort:     enginePort,
		sandboxRuntime: sandboxRuntime,
		builder:        wsimage.New(runsDir),
		cache:          map[string]*provEntry{},
	}
}

// SetImageBuilder swaps the image builder (tests).
func (r *Resolver) SetImageBuilder(b *wsimage.Builder) { r.builder = b }

// unresolvable wraps errors that no retry will heal (§6.1): missing
// project/integration rows and unregistered kinds.
func unresolvable(err error) error {
	return fmt.Errorf("%w: %v", engine.ErrUnresolvable, err)
}

func (r *Resolver) Resolve(ctx context.Context, projectID string) (*engine.Resolution, error) {
	p, err := r.conf.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, confstore.ErrNotFound) {
			return nil, unresolvable(err)
		}
		return nil, err
	}

	repoRef := ports.RepoRef{
		URL:           p.Repo.URL,
		DefaultBranch: p.Repo.DefaultBranch,
		Locator:       p.Repo.Locator,
	}

	// RepoHost: account client (credentials + egress) and project-bound port.
	repoHostInt, repoHostKind, err := r.repoHostKind(ctx, p.RepoAccountID)
	if err != nil {
		return nil, err
	}
	account, err := repoHostKind.Account(kindConfig(repoHostInt))
	if err != nil {
		return nil, fmt.Errorf("repository integration %s: %w", repoHostInt.Name, err)
	}
	repoHost, err := repoHostKind.RepoHost(kindConfig(repoHostInt), repoRef)
	if err != nil {
		return nil, unresolvable(fmt.Errorf("repository integration %s: %v", repoHostInt.Name, err))
	}
	// Extra repos ride the same repository host account; each one with a
	// locator gets its own bound port (that is what opens its merge requests).
	// One without is context only: cloned, never published.
	var extras []engine.ExtraRepo
	repoHosts := map[string]ports.RepoHost{}
	for _, x := range p.Repos {
		extras = append(extras, engine.ExtraRepo{Name: x.Name, URL: x.URL, Locator: x.Locator, DefaultBranch: x.DefaultBranch})
		if x.Locator == "" {
			continue
		}
		f, err := repoHostKind.RepoHost(kindConfig(repoHostInt), ports.RepoRef{URL: x.URL, DefaultBranch: x.DefaultBranch, Locator: x.Locator})
		if err != nil {
			return nil, unresolvable(fmt.Errorf("repository integration %s, repo %s: %v", repoHostInt.Name, x.Name, err))
		}
		repoHosts[x.Name] = f
	}

	// Provisioner: cached + refcounted (see package comment).
	provInt, err := r.integration(ctx, p.ProvisionerID, confstore.PortProvisioner)
	if err != nil {
		return nil, err
	}
	prov, release, err := r.acquireProvisioner(provInt)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			release()
		}
	}()

	// Harness set (per-step harness): the project holds the AVAILABLE harness
	// integrations, one per kind, with NO default — the flow owns the default
	// harness kind (defaults.harness) and a stage may override it. Each
	// harness wraps the provisioner for exec.
	byKind := map[string]kinds.Harness{}
	firstKind := "" // the first-configured kind: the fallback default (below)
	for _, hid := range p.HarnessIDs {
		hi, err := r.integration(ctx, hid, confstore.PortHarness)
		if err != nil {
			return nil, err
		}
		if _, dup := byKind[hi.Kind]; dup {
			return nil, unresolvable(fmt.Errorf("project has two %q harness integrations; the set allows one per kind", hi.Kind))
		}
		hk, err := r.reg.Harness(hi.Kind)
		if err != nil {
			return nil, unresolvable(err)
		}
		h, err := hk.New(kindConfig(hi), prov)
		if err != nil {
			return nil, unresolvable(fmt.Errorf("harness %s: %v", hi.Name, err))
		}
		byKind[hi.Kind] = h
		if firstKind == "" {
			firstKind = hi.Kind
		}
	}
	if len(byKind) == 0 {
		return nil, unresolvable(fmt.Errorf("project %s has no harness integrations", p.Name))
	}
	// Deterministic unions by sorted kind: the image tag is content-addressed,
	// so the install-layer order must not depend on set/map ordering.
	kindList := make([]string, 0, len(byKind))
	for k := range byKind {
		kindList = append(kindList, k)
	}
	sort.Strings(kindList)
	// The project has no default; the flow's defaults.harness drives selection
	// per stage (runnerFor(stage.Harness)). The Resolution's default fields
	// (Runner/HarnessKind/HarnessBinary) are a FALLBACK — the first-configured
	// harness — reached only by a stage with no harness kind, which the
	// required flow default (parse.go) prevents; envcheck also probes it.
	// Preflight refuses a flow harness kind that is not in this set.
	defaultKind := firstKind
	runners := make(map[string]ports.AgentRunner, len(byKind))
	providerEnv := map[string]string{}
	var harnessSecrets []string
	var harnessBinaries []string
	var harnessInstall []ports.HarnessInstall
	egressContribs := [][]string{account.EgressDomains()}
	for _, k := range kindList {
		h := byKind[k]
		runners[k] = h
		layer := h.InstallLayer()
		// A repo-authored devcontainer runs the repo's own image, which knows
		// nothing about opencode/codex — the harness is installed in-workspace
		// via an injected feature, and it needs shell, not Dockerfile syntax.
		if cmds := shellLines(layer); len(cmds) > 0 {
			harnessInstall = append(harnessInstall, ports.HarnessInstall{
				Binary: h.Binary(), Requires: requiredTools(cmds), Commands: cmds,
			})
		}
		egressContribs = append(egressContribs, h.EgressDomains())
		for ek, ev := range h.EnvVars() {
			providerEnv[ek] = ev
		}
		// Auth material that never passes through provider.env, so the
		// redactor can scrub it (kinds.SecretHolder).
		if sh, ok := h.(kinds.SecretHolder); ok {
			harnessSecrets = append(harnessSecrets, sh.SecretValues()...)
		}
		harnessBinaries = append(harnessBinaries, h.Binary())
	}
	harness := byKind[defaultKind] // default: Runner, HarnessBinary, provider.env base

	gitUser, gitPass := account.GitCredential()
	gitName, gitEmail := account.GitIdentity()

	res := &engine.Resolution{
		ProjectID:            p.ID,
		ProjectName:          p.Name,
		RepoRef:              repoRef,
		AgentsMDPath:         p.Repo.AgentsMDPath,
		DefaultFlow:          p.DefaultFlow,
		Commands:             p.Commands,
		GitUser:              gitUser,
		GitPass:              gitPass,
		GitName:              gitName,
		GitEmail:             gitEmail,
		ProviderEnv:          providerEnv, // union of every harness's own auth env
		HarnessSecrets:       harnessSecrets,
		EgressDomains:        egressDomains(p, egressContribs...),
		EgressMode:           p.Workspace.EgressMode,
		EgressDenylist:       p.Workspace.DeniedDomains,
		EgressBlocked:        p.Workspace.BlockedDomains,
		EgressAllowedCIDRs:   p.Workspace.EgressAllowedCIDRs,
		EgressAllowedPorts:   p.Workspace.EgressAllowedPorts,
		CodeServerExtensions: p.Workspace.CodeServerExtensions,
		EditorSettings: ports.EditorSettings{
			Theme:          p.Workspace.Editor.Theme,
			FontSize:       p.Workspace.Editor.FontSize,
			TabSize:        p.Workspace.Editor.TabSize,
			FormatOnSave:   p.Workspace.Editor.FormatOnSave,
			WordWrap:       p.Workspace.Editor.WordWrap,
			TrustWorkspace: p.Workspace.Editor.TrustWorkspace,
		},
		ExtraRepos:   extras,
		RepoHosts:    repoHosts,
		HarnessKind:  defaultKind,
		HarnessKinds: kindList,
		Models: func(ctx context.Context, kind string) ([]string, error) {
			h, ok := byKind[kind]
			if !ok {
				return nil, fmt.Errorf("harness %q is not in the project's set", kind)
			}
			if am, ok := h.(kinds.AnyModel); ok && am.AcceptsAnyModel() {
				return nil, nil // no limit to check against
			}
			infos, err := h.Models(ctx)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(infos))
			for _, m := range infos {
				ids = append(ids, m.ID)
			}
			return ids, nil
		},
		CheckAuth: func(ctx context.Context, kind string, models []string) ([]string, []string) {
			ac, ok := byKind[kind].(kinds.AuthChecker)
			if !ok {
				return nil, nil // nothing to probe (a fake), or no probe yet
			}
			r := ac.CheckAuth(ctx, models)
			return r.Rejected, r.Unverified
		},
		HarnessBinary:   harness.Binary(),
		HarnessBinaries: harnessBinaries,
		HarnessInstall:  harnessInstall,
		Envp:            prov,
		Runner:          harness,
		Runners:         runners,
		RepoHost:        repoHost,
		// The write side of a ticket's tracker, by integration id (the
		// ticket's, not the project's): what a tracker action calls.
		TicketWriter: r.TicketWriter,
		// Seed and prepare EVERY harness in the set: a stage may run on any of
		// them, so all their auth + native configs must be in the workspace.
		SeedAuth: func(ctx context.Context, env ports.EnvID) error {
			for _, k := range kindList {
				if err := byKind[k].SeedAuth(ctx, env); err != nil {
					return fmt.Errorf("seed %s auth: %w", k, err)
				}
			}
			return nil
		},
		// The mirror image of SeedAuth: every harness that can say where its
		// auth lands, so the engine can wipe it when the run ends.
		AuthFiles: func(ctx context.Context, env ports.EnvID) []string {
			var out []string
			for _, k := range kindList {
				if loc, ok := byKind[k].(kinds.AuthLocator); ok {
					out = append(out, loc.AuthFiles(ctx, env)...)
				}
			}
			return out
		},
		// The agents come from the RUN's flow (FLOWLANG §1, v3), so they are
		// an argument here rather than something the project resolves.
		Prepare: func(ctx context.Context, env ports.EnvID, specs []*agentspec.Spec) error {
			for _, k := range kindList {
				if err := byKind[k].Prepare(ctx, env, specs); err != nil {
					return fmt.Errorf("prepare %s config: %w", k, err)
				}
			}
			return nil
		},
		Release: release,
	}
	ok = true
	return res, nil
}

func (r *Resolver) repoHostKind(ctx context.Context, id string) (confstore.Integration, kinds.RepoHostKind, error) {
	in, err := r.integration(ctx, id, confstore.PortRepository)
	if err != nil {
		return confstore.Integration{}, nil, err
	}
	fk, err := r.reg.RepoHost(in.Kind)
	if err != nil {
		return confstore.Integration{}, nil, unresolvable(err)
	}
	return in, fk, nil
}

func (r *Resolver) integration(ctx context.Context, id, wantPort string) (confstore.Integration, error) {
	in, err := r.conf.GetIntegration(ctx, id)
	if err != nil {
		if errors.Is(err, confstore.ErrNotFound) {
			return in, unresolvable(err)
		}
		return in, err
	}
	if in.Port != wantPort {
		return in, unresolvable(fmt.Errorf("integration %s is port %s, expected %s", in.Name, in.Port, wantPort))
	}
	return in, nil
}

func kindConfig(in confstore.Integration) kinds.Config {
	return kinds.Config{Values: in.Config, Secrets: in.Secrets}
}

// acquireProvisioner returns the cached instance for the integration's
// current generation, constructing (and staling the old one) on change.
func (r *Resolver) acquireProvisioner(in confstore.Integration) (kinds.Provisioner, func(), error) {
	pk, err := r.reg.Provisioner(in.Kind)
	if err != nil {
		return nil, nil, unresolvable(err)
	}
	// One cache entry per provisioner integration; the generation is the
	// integration's updated_at (config edits invalidate).
	cacheKey := in.ID
	gen := in.UpdatedAt.UTC().String()

	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.cache[cacheKey]
	if e != nil && e.key != gen {
		e.stale = true
		if e.refs == 0 {
			e.prov.Close()
		}
		// Remove immediately: leaving a closed entry in the map would make
		// the next acquire close it a second time.
		delete(r.cache, cacheKey)
		e = nil
	}
	if e == nil {
		prov, err := pk.New(kindConfig(in), kinds.ProvisionerDeps{
			RunsDir:        r.runsDir,
			EnginePort:     r.enginePort,
			ShimBin:        r.builder.EnsureShim,
			GatewayImage:   r.builder.EnsureGatewayImage,
			SandboxRuntime: r.sandboxRuntime,
		})
		if err != nil {
			return nil, nil, unresolvable(fmt.Errorf("provisioner %s: %v", in.Name, err))
		}
		e = &provEntry{key: gen, prov: prov}
		r.cache[cacheKey] = e
	}
	e.refs++
	entry := e
	var once sync.Once
	release := func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			entry.refs--
			if entry.stale && entry.refs == 0 {
				entry.prov.Close()
			}
		})
	}
	return entry.prov, release, nil
}

// Close shuts down every cached provisioner (serve shutdown).
func (r *Resolver) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.cache {
		e.prov.Close()
	}
	r.cache = map[string]*provEntry{}
}

// shellLines converts a harness's Dockerfile InstallLayer into plain shell.
// The kinds layer speaks Dockerfile because the §8 image build is its usual
// consumer; a provisioner that installs the harness IN the workspace (a
// repo-authored devcontainer runs the repo's own image) needs the command
// itself. Directives with no shell equivalent (ENV, COPY…) are dropped.
func shellLines(layer []string) []string {
	var out []string
	for _, l := range layer {
		if cmd, ok := strings.CutPrefix(strings.TrimSpace(l), "RUN "); ok {
			if cmd = strings.TrimSpace(cmd); cmd != "" {
				out = append(out, cmd)
			}
		}
	}
	return out
}

// requiredTools names the tools an install command needs present before it
// can run. An install command's FIRST token is exactly its prerequisite —
// `npm install -g x` needs npm, `pip install x` needs pip, `curl … | sh`
// needs curl — so nothing here assumes a particular runtime: a harness that
// installs without Node never asks for Node. Deduped, order preserved.
func requiredTools(cmds []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range cmds {
		tool, _, _ := strings.Cut(strings.TrimSpace(c), " ")
		// Skip shell noise that is always present anyway.
		switch tool {
		case "", "sh", "bash", "env", "set", "cd", "true":
			continue
		}
		if !seen[tool] {
			seen[tool] = true
			out = append(out, tool)
		}
	}
	return out
}

// egressDomains derives the allow-mode allowlist WITHOUT any convenience
// defaults: repo host ∪ adapter/harness contributions (functionally required to
// operate) ∪ the project's own ExtraDomains. The broad registry/OS-mirror list
// is no longer force-seeded here — it is offered as opt-in "sensible defaults"
// (builtin.EgressDefaults) that a project copies into ExtraDomains via the form,
// where the operator can see and curate every line.
func egressDomains(p confstore.Project, contributions ...[]string) []string {
	set := map[string]bool{}
	if u, err := url.Parse(p.Repo.URL); err == nil && u.Hostname() != "" {
		set[u.Hostname()] = true
	}
	for _, x := range p.Repos {
		if u, err := url.Parse(x.URL); err == nil && u.Hostname() != "" {
			set[u.Hostname()] = true
		}
	}
	for _, list := range contributions {
		for _, d := range list {
			if d != "" {
				set[d] = true
			}
		}
	}
	for _, d := range p.Workspace.ExtraDomains {
		if d != "" {
			set[d] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// --- integration-level operations for the API (§10) -------------------------

// TestIntegration constructs the integration's adapter and runs its Test
// probe (the UI "Test connection" button). A harness's credentials are then
// probed against their providers: a rejected one fails the test, one that
// could not be probed comes back as a warning.
func (r *Resolver) TestIntegration(ctx context.Context, id string) (warnings []string, err error) {
	in, err := r.conf.GetIntegration(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Port == confstore.PortHarness {
		return r.testHarness(ctx, in)
	}
	return nil, r.testIntegration(ctx, in)
}

func (r *Resolver) testHarness(ctx context.Context, in confstore.Integration) ([]string, error) {
	hk, err := r.reg.Harness(in.Kind)
	if err != nil {
		return nil, err
	}
	h, err := hk.New(kindConfig(in), nil) // probes run from the engine: the envp is never touched
	if err != nil {
		return nil, err
	}
	if err := h.Test(ctx); err != nil {
		return nil, err
	}
	ac, ok := h.(kinds.AuthChecker)
	if !ok {
		return nil, nil
	}
	res := ac.CheckCredentials(ctx)
	if len(res.Rejected) > 0 {
		return res.Unverified, errors.New(strings.Join(res.Rejected, "; "))
	}
	return res.Unverified, nil
}

func (r *Resolver) testIntegration(ctx context.Context, in confstore.Integration) error {
	id := in.ID
	switch in.Port {
	case confstore.PortRepository:
		fk, err := r.reg.RepoHost(in.Kind)
		if err != nil {
			return err
		}
		acc, err := fk.Account(kindConfig(in))
		if err != nil {
			return err
		}
		return acc.Test(ctx)
	case confstore.PortProvisioner:
		pk, err := r.reg.Provisioner(in.Kind)
		if err != nil {
			return err
		}
		prov, err := pk.New(kindConfig(in), kinds.ProvisionerDeps{
			RunsDir: r.runsDir, EnginePort: r.enginePort,
			ShimBin: r.builder.EnsureShim, GatewayImage: r.builder.EnsureGatewayImage,
			SandboxRuntime: r.sandboxRuntime,
		})
		if err != nil {
			return err
		}
		defer prov.Close()
		return prov.Test(ctx)
	case confstore.PortTicketSource:
		tk, err := r.reg.TicketSource(in.Kind)
		if err != nil {
			return err
		}
		src, err := tk.New(kindConfig(in))
		if err != nil {
			return err
		}
		return src.Test(ctx)
	}
	return fmt.Errorf("integration %s: unknown port %q", id, in.Port)
}

// ListRepos serves the project wizard's repo picker for a repository host
// integration (§10).
func (r *Resolver) ListRepos(ctx context.Context, id string) ([]kinds.RepoInfo, error) {
	in, err := r.conf.GetIntegration(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Port != confstore.PortRepository {
		return nil, fmt.Errorf("integration %s is port %s; repo listing needs a repository integration", in.Name, in.Port)
	}
	fk, err := r.reg.RepoHost(in.Kind)
	if err != nil {
		return nil, err
	}
	acc, err := fk.Account(kindConfig(in))
	if err != nil {
		return nil, err
	}
	return acc.ListRepos(ctx)
}

// ListBranches serves the source-branch picker for a project: the branches of
// the project's own repo, fetched through its repository host integration.
// Project- scoped (not integration-scoped) so the caller needs only the
// project id — the repository host id and repo locator are resolved here.
func (r *Resolver) ListBranches(ctx context.Context, projectID string) ([]kinds.BranchInfo, error) {
	p, err := r.conf.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	in, fk, err := r.repoHostKind(ctx, p.RepoAccountID)
	if err != nil {
		return nil, err
	}
	acc, err := fk.Account(kindConfig(in))
	if err != nil {
		return nil, err
	}
	return acc.ListBranches(ctx, p.Repo.Locator)
}

// TicketSource constructs the ticket-tracker adapter for a ticketsource
// integration (the ticket factory's import path). Unlike the ports a project
// binds, this one is addressed directly by integration id: the tracker is
// picked per import, not per project.
func (r *Resolver) TicketSource(ctx context.Context, id string) (ports.TicketSource, error) {
	in, tk, err := r.ticketSourceKind(ctx, id)
	if err != nil {
		return nil, err
	}
	return tk.New(kindConfig(in))
}

// TicketWriter constructs the write side of a ticketsource integration for a
// flow's tracker actions — the same adapter the import uses, asserted for its
// optional ports.TicketWriter capability. An adapter without it is refused by
// name, so run creation can say which integration cannot write back.
func (r *Resolver) TicketWriter(ctx context.Context, id string) (ports.TicketWriter, error) {
	in, tk, err := r.ticketSourceKind(ctx, id)
	if err != nil {
		return nil, err
	}
	src, err := tk.New(kindConfig(in))
	if err != nil {
		return nil, err
	}
	w, ok := src.(ports.TicketWriter)
	if !ok {
		return nil, fmt.Errorf("integration %s (%s) can only read its tracker, not write to it", in.Name, in.Kind)
	}
	return w, nil
}

// TicketSourceKind names an integration's vendor ("jira") for the provenance
// stamped on an imported ticket.
func (r *Resolver) TicketSourceKind(ctx context.Context, id string) (string, error) {
	in, _, err := r.ticketSourceKind(ctx, id)
	if err != nil {
		return "", err
	}
	return in.Kind, nil
}

func (r *Resolver) ticketSourceKind(ctx context.Context, id string) (confstore.Integration, kinds.TicketSourceKind, error) {
	in, err := r.integration(ctx, id, confstore.PortTicketSource)
	if err != nil {
		return confstore.Integration{}, nil, err
	}
	tk, err := r.reg.TicketSource(in.Kind)
	if err != nil {
		return confstore.Integration{}, nil, unresolvable(err)
	}
	return in, tk, nil
}

// ListModels serves the flow editor's model browser for a harness
// integration: the exact `provider/model` strings flows may use, given the
// harness's configured auth. Engine-side; Models must not touch the envp.
func (r *Resolver) ListModels(ctx context.Context, id string) ([]kinds.ModelInfo, error) {
	in, err := r.conf.GetIntegration(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Port != confstore.PortHarness {
		return nil, fmt.Errorf("integration %s is port %s; model listing needs a harness", in.Name, in.Port)
	}
	hk, err := r.reg.Harness(in.Kind)
	if err != nil {
		return nil, err
	}
	h, err := hk.New(kindConfig(in), nil)
	if err != nil {
		return nil, err
	}
	return h.Models(ctx)
}

var _ engine.Resolver = (*Resolver)(nil)
