// Package kinds is the adapter-kind registry (DESIGN2.md §4): every
// integration kind declares a Descriptor (drives the generic CPUI form) and
// constructs adapters from stored config+secrets. The engine itself stays
// ports-only — the resolver (internal/resolve) is the sole consumer that
// turns kinds into engine.Resolution fields.
package kinds

import (
	"context"
	"fmt"
	"sort"

	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
)

// Config is an integration's stored configuration: plain values plus
// decrypted secrets (confstore opens them; they never travel further than
// adapter construction).
type Config struct {
	Values  map[string]any
	Secrets map[string]string
}

// Str reads a string value with a default.
func (c Config) Str(key, def string) string {
	if v, ok := c.Values[key].(string); ok && v != "" {
		return v
	}
	return def
}

// Ints reads an int list (JSON numbers arrive as float64).
func (c Config) Ints(key string) []int {
	raw, ok := c.Values[key].([]any)
	if !ok {
		return nil
	}
	var out []int
	for _, v := range raw {
		switch n := v.(type) {
		case float64:
			out = append(out, int(n))
		case int:
			out = append(out, n)
		}
	}
	return out
}

// Field describes one config/secret input (§4: descriptor-driven forms).
type Field struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Default     string `json:"default,omitempty"`
	Secret      bool   `json:"secret"`
	Required    bool   `json:"required"`
}

// Descriptor identifies a kind and its form schema.
type Descriptor struct {
	Port   string  `json:"port"`
	Kind   string  `json:"kind"`
	Label  string  `json:"label"`
	Fields []Field `json:"fields"`
}

// RepoInfo is one pickable repository (project wizard, §4).
type RepoInfo struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	Locator       string `json:"locator"`
	DefaultBranch string `json:"default_branch"`
}

// ModelInfo is one model a harness can route to: ID is the exact
// `provider/model` string flows use (`defaults.model`, stage `model:`).
type ModelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BranchInfo is one branch a run can fork off from (the source-branch picker).
// Default marks the repo's default branch so the UI can pre-select it.
type BranchInfo struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// ForgeAccount is the account-level forge client: token probe, repo
// browsing, git credentials and egress contribution.
type ForgeAccount interface {
	Test(ctx context.Context) error
	ListRepos(ctx context.Context) ([]RepoInfo, error)
	// ListBranches returns the branches of the repo named by locator (the
	// forge-kind identifier from RepoInfo.Locator), for the source-branch
	// picker. Newest-activity first where the forge supports it.
	ListBranches(ctx context.Context, locator string) ([]BranchInfo, error)
	// GitCredential is the (user, password) pair for the §11 step 3
	// credential-store entry; empty user+pass = public repos only.
	GitCredential() (user, pass string)
	// GitIdentity is the author name+email stamped on the agent's commits
	// (§11 step 3 `git config user.*`). Configured per forge integration so
	// commits carry the human operator's identity, not a generic bot. Either
	// empty ⇒ bootstrap keeps its built-in default for that field.
	GitIdentity() (name, email string)
	EgressDomains() []string
}

// ForgeKind constructs the account client and project-bound Forge port.
type ForgeKind interface {
	Descriptor() Descriptor
	Account(cfg Config) (ForgeAccount, error)
	Forge(cfg Config, repo ports.RepoRef) (ports.Forge, error)
}

// Provisioner is the EnvProvisioner port plus lifecycle/probe surface.
type Provisioner interface {
	ports.EnvProvisioner
	Test(ctx context.Context) error
	Close()
}

// ProvisionerDeps carry engine-side wiring a provisioner needs.
type ProvisionerDeps struct {
	RunsDir    string
	EnginePort int
	// ShimBin builds (cached) and returns the host path of the static
	// in-workspace shim binary (wsimage.EnsureShim); the devcontainer
	// provisioner bind-mounts it at provision time.
	ShimBin func(ctx context.Context) (string, error)
	// GatewayImage builds (cached) and returns the tag of the per-project egress
	// gateway image (wsimage.EnsureGatewayImage); the devcontainer provisioner
	// runs one gateway container per project to filter all workspace egress.
	GatewayImage func(ctx context.Context) (string, error)
}

type ProvisionerKind interface {
	Descriptor() Descriptor
	New(cfg Config, deps ProvisionerDeps) (Provisioner, error)
}

// Harness is the AgentRunner port plus the §4 capability surface. The
// harness owns model auth entirely (post-M8 amendment): its integration
// config carries API keys and/or subscription logins in native format —
// there is no separate provider port.
type Harness interface {
	ports.AgentRunner
	// Test statically validates config/auth material (no remote call — a
	// harness has no server to probe).
	Test(ctx context.Context) error
	// Binary is the executable envcheck probes with `command -v`.
	Binary() string
	// InstallLayer returns Dockerfile lines installing the harness (§8.2).
	InstallLayer() []string
	EgressDomains() []string
	// Models lists the models this harness can route to given its
	// configured auth — the exact strings flows may use. Engine-side (no
	// workspace involved); implementations should be cheap or cached.
	Models(ctx context.Context) ([]ModelInfo, error)
	// EnvVars are env-var-style credentials written to provider.env at
	// bootstrap step 2 (harness-owned auth: e.g. codex's OPENAI_API_KEY).
	// nil/empty = nothing to write.
	EnvVars() map[string]string
	// SeedAuth writes the harness's auth material into the workspace at
	// bootstrap step 2.
	SeedAuth(ctx context.Context, env ports.EnvID) error
	// Prepare generates harness-native config at bootstrap step 4 from the
	// project's agent library specs (DESIGN3 §1: agents live in the DB, not
	// the target repo). Implementations must write their base config (e.g.
	// the ask_human MCP registration) even for zero specs.
	Prepare(ctx context.Context, env ports.EnvID, specs []*agentspec.Spec) error
}

type HarnessKind interface {
	Descriptor() Descriptor
	New(cfg Config, envp ports.EnvProvisioner) (Harness, error)
}

// --- registry ---------------------------------------------------------------

type Registry struct {
	forges       map[string]ForgeKind
	provisioners map[string]ProvisionerKind
	harnesses    map[string]HarnessKind
}

func NewRegistry() *Registry {
	return &Registry{
		forges:       map[string]ForgeKind{},
		provisioners: map[string]ProvisionerKind{},
		harnesses:    map[string]HarnessKind{},
	}
}

func (r *Registry) RegisterForge(k ForgeKind)             { r.forges[k.Descriptor().Kind] = k }
func (r *Registry) RegisterProvisioner(k ProvisionerKind) { r.provisioners[k.Descriptor().Kind] = k }
func (r *Registry) RegisterHarness(k HarnessKind)         { r.harnesses[k.Descriptor().Kind] = k }

func (r *Registry) Forge(kind string) (ForgeKind, error) {
	k, ok := r.forges[kind]
	if !ok {
		return nil, fmt.Errorf("unknown forge kind %q", kind)
	}
	return k, nil
}

func (r *Registry) Provisioner(kind string) (ProvisionerKind, error) {
	k, ok := r.provisioners[kind]
	if !ok {
		return nil, fmt.Errorf("unknown provisioner kind %q", kind)
	}
	return k, nil
}

func (r *Registry) Harness(kind string) (HarnessKind, error) {
	k, ok := r.harnesses[kind]
	if !ok {
		return nil, fmt.Errorf("unknown harness kind %q", kind)
	}
	return k, nil
}

// Valid reports whether (port, kind) is registered — wired into
// confstore.Service.KindValid.
func (r *Registry) Valid(port, kind string) bool {
	switch port {
	case "forge":
		_, ok := r.forges[kind]
		return ok
	case "provisioner":
		_, ok := r.provisioners[kind]
		return ok
	case "harness":
		_, ok := r.harnesses[kind]
		return ok
	}
	return false
}

// Descriptors lists every registered kind, stably ordered (GET /api/kinds).
func (r *Registry) Descriptors() []Descriptor {
	var out []Descriptor
	for _, k := range r.forges {
		out = append(out, k.Descriptor())
	}
	for _, k := range r.provisioners {
		out = append(out, k.Descriptor())
	}
	for _, k := range r.harnesses {
		out = append(out, k.Descriptor())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}
