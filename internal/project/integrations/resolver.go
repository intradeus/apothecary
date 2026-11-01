// Package integrations turns a project's stored integrations into live
// adapters. Its Resolver implements resolution.Resolver (DESIGN2 §6.1), project
// id → adapter set, flattening harness capabilities into the port-typed
// Resolution; it also tests an integration and asks one what it offers
// (repositories, branches, models, tickets). It is the only place kinds and
// confstore meet the engine.
//
// Provisioner instances are cached per (integration id, updated_at, template)
// and refcounted by active resolutions: a config edit makes new resolutions
// construct a fresh instance while in-flight runs keep theirs; the stale
// instance closes when its last run releases it. The repository host, harness
// and provider adapters are stateless HTTP/exec wrappers and are simply
// constructed per resolution.
package integrations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"apothecary/internal/account/providers"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
	"apothecary/internal/project/resolution"
	"apothecary/internal/project/tickets"
	"apothecary/internal/project/tickets/internaltracker"
	"apothecary/internal/workspace/wsimage"
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
	// logins opens provider credentials refreshing them when due (the
	// keeper); nil opens them as stored (tests).
	logins CredentialOpener
}

// CredentialOpener opens a provider credential with an access token that
// will outlive the keeper's margin (logins.Keeper).
type CredentialOpener interface {
	Fresh(ctx context.Context, id string) (providers.Credential, error)
}

// SetLogins wires the keeper.
func (r *Resolver) SetLogins(o CredentialOpener) { r.logins = o }

// openCredential is one credential, fresh when the keeper is wired.
func (r *Resolver) openCredential(ctx context.Context, id string) (providers.Credential, error) {
	if r.logins != nil {
		return r.logins.Fresh(ctx, id)
	}
	c, _, err := r.conf.OpenCredential(ctx, id)
	return c, err
}

// credentials opens a harness integration's referenced credentials: the
// snapshot its static parts read, the ones that could not be used (a login
// the keeper disabled, a credential deleted since), and Fresh for seeding —
// which skips an unusable one rather than failing the whole workspace.
func (r *Resolver) credentials(ctx context.Context, in confstore.Integration) kinds.Credentials {
	ids := confstore.CredentialRefIDs(in.Config)
	out := kinds.Credentials{Problems: map[string]string{}}
	for _, id := range ids {
		c, err := r.openCredential(ctx, id)
		if err != nil {
			out.Problems[id] = err.Error()
			continue
		}
		out.Snapshot = append(out.Snapshot, c)
	}
	out.Fresh = func(ctx context.Context) ([]providers.Credential, error) {
		var creds []providers.Credential
		for _, id := range ids {
			c, err := r.openCredential(ctx, id)
			if err != nil {
				slog.Warn("a harness credential is unusable; seeding without it", "integration", in.Name, "credential", id, "err", err)
				continue
			}
			creds = append(creds, c)
		}
		return creds, nil
	}
	return out
}

type provEntry struct {
	key  string // updatedAt|template — instance generation
	prov kinds.Provisioner
	refs int
	// stale marks a superseded generation: closed when refs hits 0.
	stale bool
}

// New builds the resolver. The builder compiles + caches the apothecary-owned
// images a docker host needs (the shim, the tools bundle, the gateway and
// browser images → ProvisionerDeps).
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

// WarmImages builds, in the background, what every docker-host provision
// needs and would otherwise pay for on its first run after an upgrade — the
// tools bundle (~1 GB of downloads and a codex-acp compile). Same Builder as
// the provisioners', so its lock serialises the warm-up with a concurrent
// provision instead of racing it. Failures are logged; the provision that
// needs the bundle retries the build and reports its own error.
func (r *Resolver) WarmImages(ctx context.Context) {
	go func() {
		if _, err := r.builder.EnsureTools(ctx); err != nil {
			slog.Warn("tools bundle warm-up failed (the first provision will retry it)", "err", err)
		}
	}()
}

// unresolvable wraps errors that no retry will heal (§6.1): missing
// project/integration rows and unregistered kinds.
func unresolvable(err error) error {
	return fmt.Errorf("%w: %v", resolution.ErrUnresolvable, err)
}

func (r *Resolver) Resolve(ctx context.Context, projectID string) (*resolution.Resolution, error) {
	p, err := r.conf.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, confstore.ErrNotFound) {
			return nil, unresolvable(err)
		}
		return nil, err
	}

	repoRef := repoport.RepoRef{
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
	var extras []resolution.ExtraRepo
	repoHosts := map[string]repoport.RepoHost{}
	for _, x := range p.Repos {
		extras = append(extras, resolution.ExtraRepo{Name: x.Name, URL: x.URL, Locator: x.Locator, DefaultBranch: x.DefaultBranch})
		if x.Locator == "" {
			continue
		}
		f, err := repoHostKind.RepoHost(kindConfig(repoHostInt), repoport.RepoRef{URL: x.URL, DefaultBranch: x.DefaultBranch, Locator: x.Locator})
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
	var credentialIDs []string
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
		cfg := kindConfig(hi)
		cfg.Credentials = r.credentials(ctx, hi)
		credentialIDs = append(credentialIDs, confstore.CredentialRefIDs(hi.Config)...)
		h, err := hk.New(cfg, prov)
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
	// Deterministic unions by sorted kind: HarnessBinaries and the egress
	// allowlist must not depend on set/map ordering.
	kindList := make([]string, 0, len(byKind))
	for k := range byKind {
		kindList = append(kindList, k)
	}
	sort.Strings(kindList)
	// The project has no default; each agent stage names its harness (a
	// kind, or the parameter the project binds) and drives selection
	// (RunnerFor(stage.Harness)). The Resolution's default fields
	// (Runner/HarnessKind/HarnessBinary) are a FALLBACK — the first-configured
	// harness — reached only by a stage with no harness kind, which run
	// creation prevents (flow.Unbound); envcheck also probes it. Preflight
	// refuses a flow harness kind that is not in this set.
	defaultKind := firstKind
	runners := make(map[string]agentport.AgentRunner, len(byKind))
	providerEnv := map[string]string{}
	var harnessSecrets []string
	var harnessBinaries []string
	// The editor installs the project's extensions plus every harness's own
	// (kinds.EditorExtender), so each harness the project can run is one
	// click away in the browser editor, signed in as its agent is.
	editorExtensions := append([]string(nil), p.Workspace.CodeServerExtensions...)
	egressContribs := [][]string{account.EgressDomains()}
	for _, k := range kindList {
		h := byKind[k]
		runners[k] = h
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
		if ee, ok := h.(kinds.EditorExtender); ok {
			editorExtensions = append(editorExtensions, ee.EditorExtensions()...)
		}
	}
	harness := byKind[defaultKind] // default: Runner, HarnessBinary, provider.env base

	gitUser, gitPass := account.GitCredential()
	gitName, gitEmail := account.GitIdentity()
	projectSecrets, err := r.conf.ProjectSecrets(ctx, p.ID)
	if err != nil {
		return nil, err
	}

	res := &resolution.Resolution{
		ProjectID:            p.ID,
		ProjectName:          p.Name,
		UserID:               p.UserID,
		RepoRef:              repoRef,
		AgentsMDPath:         p.Repo.AgentsMDPath,
		DefaultFlow:          p.DefaultFlow,
		Commands:             p.Commands,
		Services:             services(p.Workspace.Services),
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
		EditorSettings: envport.EditorSettings{
			Extensions:     editorExtensions,
			Theme:          p.Workspace.Editor.Theme,
			FontSize:       p.Workspace.Editor.FontSize,
			TabSize:        p.Workspace.Editor.TabSize,
			FormatOnSave:   p.Workspace.Editor.FormatOnSave,
			WordWrap:       p.Workspace.Editor.WordWrap,
			TrustWorkspace: p.Workspace.Editor.TrustWorkspace,
		},
		Secrets:      projectSecrets,
		Browser:      p.Workspace.Browser.Enabled,
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
		Envp:            prov,
		Runner:          harness,
		Runners:         runners,
		RepoHost:        repoHost,
		// The tracker a ticket's tracker actions write to, from the ticket's
		// current row (the ticket's, not the project's).
		TicketTracker: r.TicketTracker,
		TicketStep:    r.TicketStep,
		// Seed and prepare EVERY harness in the set: a stage may run on any of
		// them, so all their auth + native configs must be in the workspace.
		SeedAuth: func(ctx context.Context, env envport.EnvID, register func(string)) error {
			for _, k := range kindList {
				if err := byKind[k].SeedAuth(ctx, env, register); err != nil {
					return fmt.Errorf("seed %s auth: %w", k, err)
				}
			}
			return nil
		},
		CredentialIDs: credentialIDs,
		AuthBroker:    authBroker(byKind),
		// The mirror image of SeedAuth: every harness that can say where its
		// auth lands, so the engine can wipe it when the run ends.
		AuthFiles: func(ctx context.Context, env envport.EnvID) []string {
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
		Prepare: func(ctx context.Context, env envport.EnvID, specs []*agentspec.Spec) error {
			for _, k := range kindList {
				if err := byKind[k].Prepare(ctx, env, specs); err != nil {
					return fmt.Errorf("prepare %s config: %w", k, err)
				}
			}
			return nil
		},
		InstallSkills: func(ctx context.Context, env envport.EnvID, names []string) error {
			for _, k := range kindList {
				if err := byKind[k].InstallSkills(ctx, env, names); err != nil {
					return fmt.Errorf("install %s skills: %w", k, err)
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

// authBroker is the run's auth broker: the harness that reads its logins
// from one (omp), or nil.
func authBroker(byKind map[string]kinds.Harness) func(register func(string)) http.Handler {
	for _, h := range byKind {
		if ab, ok := h.(kinds.AuthBrokered); ok && ab.HasLogins() {
			return ab.Broker
		}
	}
	return nil
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
			ToolsDir:       r.builder.EnsureTools,
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
	cfg := kindConfig(in)
	cfg.Credentials = r.credentials(ctx, in)
	h, err := hk.New(cfg, nil) // probes run from the engine: the envp is never touched
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
			ShimBin: r.builder.EnsureShim, ToolsDir: r.builder.EnsureTools,
			GatewayImage: r.builder.EnsureGatewayImage, SandboxRuntime: r.sandboxRuntime,
		})
		if err != nil {
			return err
		}
		defer prov.Close()
		return prov.Test(ctx)
	case confstore.PortTicketSource:
		if in.Kind == confstore.KindInternal {
			return nil // built in: nothing to reach
		}
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
func (r *Resolver) TicketSource(ctx context.Context, id string) (trackerport.TicketSource, error) {
	in, tk, err := r.ticketSourceKind(ctx, id)
	if err != nil {
		return nil, err
	}
	return tk.New(kindConfig(in))
}

// TicketWriter constructs the write side of a remote ticketsource integration
// — the same adapter the import uses, asserted for its optional
// trackerport.TicketWriter capability. An adapter without it is refused by
// name, so run creation can say which integration cannot write back.
func (r *Resolver) TicketWriter(ctx context.Context, id string) (trackerport.TicketWriter, error) {
	in, tk, err := r.ticketSourceKind(ctx, id)
	if err != nil {
		return nil, err
	}
	src, err := tk.New(kindConfig(in))
	if err != nil {
		return nil, err
	}
	w, ok := src.(trackerport.TicketWriter)
	if !ok {
		return nil, fmt.Errorf("integration %s (%s) can only read its tracker, not write to it", in.Name, in.Kind)
	}
	return w, nil
}

// TicketTracker is resolution.Resolution.TicketTracker: the tracker of the
// ticket as its row stands NOW. With a source, the source integration's
// writer and the issue's key — a source whose integration is gone is an
// error naming it (the ticket must be unlinked to move to the internal
// tracker); without one, the internal tracker and the ticket's id. A deleted
// ticket is *trackerport.ErrNotApplicable.
func (r *Resolver) TicketTracker(ctx context.Context, ticketID string) (trackerport.TicketWriter, string, error) {
	t, err := r.conf.GetTicket(ctx, ticketID)
	if errors.Is(err, confstore.ErrNotFound) {
		return nil, "", &trackerport.ErrNotApplicable{Key: ticketID, Reason: "the ticket was deleted"}
	}
	if err != nil {
		return nil, "", err
	}
	if t.SourceID == "" {
		return internaltracker.New(r.conf), t.ID, nil
	}
	w, err := r.TicketWriter(ctx, t.SourceID)
	if errors.Is(err, confstore.ErrNotFound) || errors.Is(err, resolution.ErrUnresolvable) {
		return nil, "", fmt.Errorf("ticket %s mirrors %s of %s integration %s, which no longer resolves (%w) — unlink the ticket to put it on the internal tracker",
			t.ID, t.SourceKey, t.SourceKind, t.SourceID, err)
	}
	if err != nil {
		return nil, "", err
	}
	return w, t.SourceKey, nil
}

// TicketStep is resolution.Resolution.TicketStep: the name of the status
// holding the step on the ticket's tracker. A remote tracker whose lists were
// never fetched is fetched first, so an issue imported before the lists
// existed resolves too.
func (r *Resolver) TicketStep(ctx context.Context, ticketID string, step trackerport.FlowStep) (string, error) {
	st, err := r.conf.StepStatus(ctx, ticketID, step)
	var unsupported *trackerport.ErrStatusUnsupported
	if errors.As(err, &unsupported) && len(unsupported.Supported) == 0 {
		if t, terr := r.conf.GetTicket(ctx, ticketID); terr == nil && t.SourceID != "" {
			if src, serr := r.TicketSource(ctx, t.SourceID); serr == nil {
				if _, lerr := tickets.SyncLists(ctx, r.conf, t.SourceID, src); lerr != nil {
					return "", fmt.Errorf("fetching the statuses of ticket %s's tracker: %w", t.SourceKey, lerr)
				}
				st, err = r.conf.StepStatus(ctx, ticketID, step)
			}
		}
	}
	if err != nil {
		return "", err
	}
	return st.Name, nil
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
	if in.Kind == confstore.KindInternal {
		return confstore.Integration{}, nil, fmt.Errorf("integration %s is the internal tracker: nothing is imported from it", in.Name)
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
	cfg := kindConfig(in)
	cfg.Credentials = r.credentials(ctx, in)
	h, err := hk.New(cfg, nil)
	if err != nil {
		return nil, err
	}
	return h.Models(ctx)
}

var _ resolution.Resolver = (*Resolver)(nil)

// services is the project's services as the engine runs them, health
// durations defaulted.
func services(in []confstore.ServiceConfig) []resolution.Service {
	out := make([]resolution.Service, 0, len(in))
	for _, s := range in {
		sv := resolution.Service{Name: s.Name, Description: s.Description, Start: s.Start, Stop: s.Stop,
			Health: resolution.ServiceHealth{HTTP: s.Health.HTTP, TCP: s.Health.TCP, Command: s.Health.Command,
				Timeout: s.Health.TimeoutOr(), Interval: s.Health.IntervalOr()}}
		for _, p := range s.Ports {
			sv.Ports = append(sv.Ports, resolution.ServicePort{Port: p.Port, Label: p.Label, Path: p.Path})
		}
		out = append(out, sv)
	}
	return out
}
