// Package kinds is the adapter-kind registry (DESIGN2.md §4): every
// integration kind declares a Descriptor (drives the generic CPUI form) and
// constructs adapters from stored config+secrets. The engine itself stays
// ports-only — the resolver (internal/project/integrations) is the sole consumer that
// turns kinds into resolution.Resolution fields.
package kinds

import (
	"context"
	"fmt"
	"sort"

	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
)

// Config is an integration's stored configuration: plain values plus
// decrypted secrets (confstore opens them; they never travel further than
// adapter construction). A harness's model credentials arrive in
// Credentials instead: it holds no secret of its own.
type Config struct {
	Values      map[string]any
	Secrets     map[string]string
	Credentials Credentials
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
	// Multiline renders a multi-line input (a `*_json` secret always is).
	Multiline bool `json:"multiline,omitempty"`
	// Help is a one-line hint rendered under the input. Plain text; bare URLs
	// in it are linkified by the form.
	Help string `json:"help,omitempty"`
	// Type is the input's shape when it is not a text: "credentials" is a
	// pick of the owner's provider credentials this harness takes (the value
	// is their ids).
	Type string `json:"type,omitempty"`
}

// Descriptor identifies a kind and its form schema. Help is prose shown above
// the form — where to get the credential, which flavor of the vendor this is —
// so the operator never has to leave the page to find out what to paste.
type Descriptor struct {
	Port   string  `json:"port"`
	Kind   string  `json:"kind"`
	Label  string  `json:"label"`
	Help   string  `json:"help,omitempty"`
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

// RepoAccount is the account-level repository host client: token probe, repo
// browsing, git credentials and egress contribution.
type RepoAccount interface {
	Test(ctx context.Context) error
	ListRepos(ctx context.Context) ([]RepoInfo, error)
	// ListBranches returns the branches of the repo named by locator (the
	// host-specific identifier from RepoInfo.Locator), for the source-branch
	// picker. Newest-activity first where the repository host supports it.
	ListBranches(ctx context.Context, locator string) ([]BranchInfo, error)
	// GitCredential is the (user, password) pair for the §11 step 3
	// credential-store entry; empty user+pass = public repos only.
	GitCredential() (user, pass string)
	// GitIdentity is the author name+email stamped on the agent's commits (§11
	// step 3 `git config user.*`). Configured per repository host integration
	// so commits carry the human operator's identity, not a generic bot.
	// Either empty ⇒ bootstrap keeps its built-in default for that field.
	GitIdentity() (name, email string)
	EgressDomains() []string
}

// RepoHostKind constructs the account client and project-bound RepoHost port.
type RepoHostKind interface {
	Descriptor() Descriptor
	Account(cfg Config) (RepoAccount, error)
	RepoHost(cfg Config, repo repoport.RepoRef) (repoport.RepoHost, error)
}

// Provisioner is the EnvProvisioner port plus lifecycle/probe surface.
type Provisioner interface {
	envport.EnvProvisioner
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
	// ToolsDir builds (cached) and returns the host directory of the tools
	// bundle (wsimage.EnsureTools): every harness CLI and code-server as
	// standalone builds, which the docker provisioner bind-mounts into every
	// workspace at /opt/apothecary/tools. The parent of that directory is
	// what each sandbox mounts (every bundle version, read-only).
	ToolsDir func(ctx context.Context) (string, error)
	// GatewayImage builds (cached) and returns the tag of the per-project egress
	// gateway image (wsimage.EnsureGatewayImage); the devcontainer provisioner
	// runs one gateway container per project to filter all workspace egress.
	GatewayImage func(ctx context.Context) (string, error)
	// SandboxRuntime is the OCI runtime for the per-run DinD sandbox
	// (APOTHECARY_SANDBOX_RUNTIME). Empty ⇒ the daemon default + --privileged.
	SandboxRuntime string
	// Drift hears whether a workspace's seeded shim/tools match this
	// engine (kubernetes only); nil = log only.
	Drift func(ctx context.Context, part string, err error)
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
	agentport.AgentRunner
	// Test statically validates config/auth material (no remote call — a
	// harness has no server to probe).
	Test(ctx context.Context) error
	// Binary is the executable envcheck probes with `command -v`. Every
	// harness kind ships in the tools bundle mounted into each workspace
	// (workspace/images/tools), at the version its kind pins.
	Binary() string
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
	// bootstrap step 2, and again whenever the keeper refreshes a login it
	// uses. It renders from current credentials (Credentials.Fresh) and
	// calls register with every secret value it is about to write, BEFORE
	// writing it: the run's redactor must know a token before the token can
	// reach any output.
	SeedAuth(ctx context.Context, env envport.EnvID, register func(secret string)) error
	// Prepare generates harness-native config at bootstrap step 4 from the
	// project's agent library specs (DESIGN3 §1: agents live in the DB, not
	// the target repo). Implementations must write their base config (e.g.
	// the ask_human MCP registration) even for zero specs.
	Prepare(ctx context.Context, env envport.EnvID, specs []*agentspec.Spec) error
	// InstallSkills makes the run's skills — laid down by the engine at
	// acpagent.SkillsDir/<name>, one directory per skill — visible to this
	// harness, by copying them into the directory IT discovers skills in
	// (bootstrap step 4, after Prepare). Each harness owns that location.
	// Zero names = nothing to do.
	InstallSkills(ctx context.Context, env envport.EnvID, names []string) error
}

// AnyModel is an OPTIONAL Harness capability: the harness runs whatever
// model string it is given (a scripted fake), so Models is a picker
// suggestion, not a limit, and model checks skip it.
type AnyModel interface {
	AcceptsAnyModel() bool
}

// SecretHolder is an OPTIONAL Harness capability: the literal credential
// values it puts into a workspace by any route the engine's own injection
// bookkeeping doesn't already cover — i.e. everything except EnvVars, which
// the engine writes itself and already tracks. Today that means the
// harness-native auth blobs (auth.json), whose contents would otherwise be
// invisible to the redactor and could surface verbatim in a stage log.
//
// Returning a value here does NOT put it anywhere; it only declares "the
// engine caused this string to exist in the workspace", which is exactly the
// redactor's contract (see internal/engine/redact.go).
type SecretHolder interface {
	SecretValues() []string
}

// AuthLocator is an OPTIONAL Harness capability: where in the workspace its
// SeedAuth material lands. The engine deletes those files when a run reaches a
// terminal state — the workspace is deliberately kept for inspection, and a
// dead run has no business still holding live model credentials. Paths are
// resolved per env because they hang off the workspace user's home.
type AuthLocator interface {
	AuthFiles(ctx context.Context, env envport.EnvID) []string
}

// EditorExtender is an OPTIONAL Harness capability: the VS Code extensions
// (Open VSX ids) that front this harness in the browser editor, installed
// with the project's own list when the editor first opens in a run. Only an
// extension that reads the auth SeedAuth writes — same user, same home, so
// the editor is signed in the way the agent is — belongs here; a harness
// with no such extension does not implement it.
type EditorExtender interface {
	EditorExtensions() []string
}

type HarnessKind interface {
	Descriptor() Descriptor
	New(cfg Config, envp envport.EnvProvisioner) (Harness, error)
}

// TicketSourceKind constructs the ticket-tracker port (Jira, GitHub/GitLab
// issues). It takes no engine deps: a ticket source is a config-plane vendor
// — the ticket factory imports from it, the sync re-reads it, and a run only
// reaches it through a tracker action's write (trackerport.TicketWriter).
type TicketSourceKind interface {
	Descriptor() Descriptor
	New(cfg Config) (trackerport.TicketSource, error)
}

// --- registry ---------------------------------------------------------------

type Registry struct {
	repoHosts     map[string]RepoHostKind
	provisioners  map[string]ProvisionerKind
	harnesses     map[string]HarnessKind
	ticketSources map[string]TicketSourceKind
}

func NewRegistry() *Registry {
	return &Registry{
		repoHosts:     map[string]RepoHostKind{},
		provisioners:  map[string]ProvisionerKind{},
		harnesses:     map[string]HarnessKind{},
		ticketSources: map[string]TicketSourceKind{},
	}
}

func (r *Registry) RegisterRepoHost(k RepoHostKind)       { r.repoHosts[k.Descriptor().Kind] = k }
func (r *Registry) RegisterProvisioner(k ProvisionerKind) { r.provisioners[k.Descriptor().Kind] = k }
func (r *Registry) RegisterHarness(k HarnessKind)         { r.harnesses[k.Descriptor().Kind] = k }
func (r *Registry) RegisterTicketSource(k TicketSourceKind) {
	r.ticketSources[k.Descriptor().Kind] = k
}

func (r *Registry) RepoHost(kind string) (RepoHostKind, error) {
	k, ok := r.repoHosts[kind]
	if !ok {
		return nil, fmt.Errorf("unknown repository kind %q", kind)
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

func (r *Registry) TicketSource(kind string) (TicketSourceKind, error) {
	k, ok := r.ticketSources[kind]
	if !ok {
		return nil, fmt.Errorf("unknown ticket source kind %q", kind)
	}
	return k, nil
}

// Valid reports whether (port, kind) is registered — wired into
// confstore.Service.KindValid.
func (r *Registry) Valid(port, kind string) bool {
	switch port {
	case "repository":
		_, ok := r.repoHosts[kind]
		return ok
	case "provisioner":
		_, ok := r.provisioners[kind]
		return ok
	case "harness":
		_, ok := r.harnesses[kind]
		return ok
	case "ticketsource":
		_, ok := r.ticketSources[kind]
		return ok
	}
	return false
}

// Descriptors lists every registered kind, stably ordered (GET /api/kinds).
func (r *Registry) Descriptors() []Descriptor {
	var out []Descriptor
	for _, k := range r.repoHosts {
		out = append(out, k.Descriptor())
	}
	for _, k := range r.provisioners {
		out = append(out, k.Descriptor())
	}
	for _, k := range r.harnesses {
		out = append(out, k.Descriptor())
	}
	for _, k := range r.ticketSources {
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
