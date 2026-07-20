// Package resolve implements engine.Resolver (DESIGN2 §6.1): project id →
// adapter set. It is the only place kinds and confstore meet the engine —
// harness capabilities are flattened into the port-typed Resolution.
//
// Provisioner instances are cached per (integration id, updated_at,
// template) and refcounted by active resolutions: a config edit makes new
// resolutions construct a fresh instance while in-flight runs keep theirs;
// the stale instance closes when its last run releases it. Forge, harness
// and provider adapters are stateless HTTP/exec wrappers and are simply
// constructed per resolution.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"sort"
	"sync"

	"factory/internal/confstore"
	"factory/internal/engine"
	"factory/internal/kinds"
	"factory/internal/ports"
)

type Resolver struct {
	conf       *confstore.Service
	reg        *kinds.Registry
	runsDir    string
	enginePort int

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

func New(conf *confstore.Service, reg *kinds.Registry, runsDir string, enginePort int) *Resolver {
	return &Resolver{
		conf:       conf,
		reg:        reg,
		runsDir:    runsDir,
		enginePort: enginePort,
		cache:      map[string]*provEntry{},
	}
}

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

	// Forge: account client (credentials + egress) and project-bound port.
	forgeInt, forgeKind, err := r.forgeKind(ctx, p.ForgeID)
	if err != nil {
		return nil, err
	}
	account, err := forgeKind.Account(kindConfig(forgeInt))
	if err != nil {
		return nil, fmt.Errorf("forge %s: %w", forgeInt.Name, err)
	}
	forge, err := forgeKind.Forge(kindConfig(forgeInt), repoRef)
	if err != nil {
		return nil, unresolvable(fmt.Errorf("forge %s: %v", forgeInt.Name, err))
	}

	// Provisioner: cached + refcounted (see package comment).
	provInt, err := r.integration(ctx, p.ProvisionerID, confstore.PortProvisioner)
	if err != nil {
		return nil, err
	}
	template := p.Workspace.Template
	prov, release, err := r.acquireProvisioner(provInt, template)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			release()
		}
	}()

	// Harness: wraps the provisioner for exec.
	harnessInt, err := r.integration(ctx, p.HarnessID, confstore.PortHarness)
	if err != nil {
		return nil, err
	}
	harnessKind, err := r.reg.Harness(harnessInt.Kind)
	if err != nil {
		return nil, unresolvable(err)
	}
	harness, err := harnessKind.New(kindConfig(harnessInt), prov)
	if err != nil {
		return nil, unresolvable(fmt.Errorf("harness %s: %v", harnessInt.Name, err))
	}

	// Providers: env vars + creds for SeedAuth + egress.
	var creds []kinds.ProviderCreds
	providerEnv := map[string]string{}
	var providerDomains []string
	for _, id := range p.ProviderIDs {
		provInt, err := r.integration(ctx, id, confstore.PortProvider)
		if err != nil {
			return nil, err
		}
		pk, err := r.reg.Provider(provInt.Kind)
		if err != nil {
			return nil, unresolvable(err)
		}
		provider, err := pk.New(kindConfig(provInt))
		if err != nil {
			return nil, unresolvable(fmt.Errorf("provider %s: %v", provInt.Name, err))
		}
		maps.Copy(providerEnv, provider.EnvVars())
		providerDomains = append(providerDomains, provider.EgressDomains()...)
		creds = append(creds, kinds.ProviderCreds{
			Kind:    provInt.Kind,
			EnvVars: provider.EnvVars(),
			Secrets: provInt.Secrets,
		})
	}

	gitUser, gitPass := account.GitCredential()

	res := &engine.Resolution{
		ProjectID:    p.ID,
		ProjectName:  p.Name,
		RepoRef:      repoRef,
		AgentsMDPath: p.Repo.AgentsMDPath,
		DefaultFlow:  p.DefaultFlow,
		Commands:     p.Commands,
		GitUser:      gitUser,
		GitPass:      gitPass,
		ProviderEnv:  providerEnv,
		EgressDomains: egressDomains(p, account.EgressDomains(),
			harness.EgressDomains(), providerDomains),
		Image:         "", // M7: custom-template path only; the §8 pipeline lands in M9
		HarnessKind:   harnessInt.Kind,
		HarnessBinary: harness.Binary(),
		Envp:          prov,
		Runner:        harness,
		Forge:         forge,
		SeedAuth: func(ctx context.Context, env ports.EnvID) error {
			return harness.SeedAuth(ctx, env, creds)
		},
		Prepare: harness.Prepare,
		Release: release,
	}
	ok = true
	return res, nil
}

func (r *Resolver) forgeKind(ctx context.Context, id string) (confstore.Integration, kinds.ForgeKind, error) {
	in, err := r.integration(ctx, id, confstore.PortForge)
	if err != nil {
		return confstore.Integration{}, nil, err
	}
	fk, err := r.reg.Forge(in.Kind)
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
func (r *Resolver) acquireProvisioner(in confstore.Integration, template string) (kinds.Provisioner, func(), error) {
	pk, err := r.reg.Provisioner(in.Kind)
	if err != nil {
		return nil, nil, unresolvable(err)
	}
	key := in.UpdatedAt.UTC().String() + "|" + template

	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.cache[in.ID]
	if e != nil && e.key != key {
		e.stale = true
		if e.refs == 0 {
			e.prov.Close()
		}
		e = nil
	}
	if e == nil {
		prov, err := pk.New(kindConfig(in), kinds.ProvisionerDeps{
			RunsDir:    r.runsDir,
			EnginePort: r.enginePort,
			Template:   template,
		})
		if err != nil {
			return nil, nil, unresolvable(fmt.Errorf("provisioner %s: %v", in.Name, err))
		}
		e = &provEntry{key: key, prov: prov}
		r.cache[in.ID] = e
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

// egressDomains derives the firewall allowlist (DESIGN2 §8.4): repo host ∪
// adapter contributions ∪ project extras, deduped and sorted.
func egressDomains(p confstore.Project, contributions ...[]string) []string {
	set := map[string]bool{}
	if u, err := url.Parse(p.Repo.URL); err == nil && u.Hostname() != "" {
		set[u.Hostname()] = true
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
// probe (the UI "Test connection" button).
func (r *Resolver) TestIntegration(ctx context.Context, id string) error {
	in, err := r.conf.GetIntegration(ctx, id)
	if err != nil {
		return err
	}
	switch in.Port {
	case confstore.PortForge:
		fk, err := r.reg.Forge(in.Kind)
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
		prov, err := pk.New(kindConfig(in), kinds.ProvisionerDeps{RunsDir: r.runsDir, EnginePort: r.enginePort})
		if err != nil {
			return err
		}
		defer prov.Close()
		return prov.Test(ctx)
	case confstore.PortHarness:
		hk, err := r.reg.Harness(in.Kind)
		if err != nil {
			return err
		}
		h, err := hk.New(kindConfig(in), nil) // Test is static: the envp is never touched
		if err != nil {
			return err
		}
		return h.Test(ctx)
	case confstore.PortProvider:
		pk, err := r.reg.Provider(in.Kind)
		if err != nil {
			return err
		}
		p, err := pk.New(kindConfig(in))
		if err != nil {
			return err
		}
		return p.Test(ctx)
	}
	return fmt.Errorf("integration %s: unknown port %q", id, in.Port)
}

// ListRepos serves the project wizard's repo picker for a forge
// integration (§10).
func (r *Resolver) ListRepos(ctx context.Context, id string) ([]kinds.RepoInfo, error) {
	in, err := r.conf.GetIntegration(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Port != confstore.PortForge {
		return nil, fmt.Errorf("integration %s is port %s; repo listing needs a forge", in.Name, in.Port)
	}
	fk, err := r.reg.Forge(in.Kind)
	if err != nil {
		return nil, err
	}
	acc, err := fk.Account(kindConfig(in))
	if err != nil {
		return nil, err
	}
	return acc.ListRepos(ctx)
}

var _ engine.Resolver = (*Resolver)(nil)
