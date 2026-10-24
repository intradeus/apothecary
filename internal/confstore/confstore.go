// Package confstore is the config plane's storage service (DESIGN2.md §2,
// §3): integrations, projects, flows and settings, CRUD'd only through the
// API/CPUI and persisted in Postgres. The Service seals/opens integration
// secrets with the master key and owns validation; the DB interface is
// implemented by pgstore (SQL) and Mem (tests, seeding).
package confstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Ports an integration can implement. The provider port was removed
// post-M8: the harness owns model auth (see IMPLEMENTATION_NOTES).
const (
	PortRepository  = "repository"
	PortProvisioner = "provisioner"
	PortHarness     = "harness"
	// PortTicketSource is the read-only issue tracker tickets are imported
	// FROM (Jira). Unlike the other three it is NOT referenced by a project:
	// it is picked per import, on the ticket form, so a tracker outage can
	// never block a project's runs.
	PortTicketSource = "ticketsource"
)

var ValidPorts = []string{PortRepository, PortProvisioner, PortHarness, PortTicketSource}

var (
	ErrNotFound = errors.New("not found")
	// ErrReferenced refuses deletes of rows something still points at; the
	// message names the referrers (§2: API maps to 409).
	ErrReferenced = errors.New("still referenced")
	// ErrConflict signals a unique-name collision (409).
	ErrConflict = errors.New("name already in use")
	// ErrInvalid marks validation failures (API maps to 400); errors not
	// wrapped in any sentinel are treated as internal (500).
	ErrInvalid = errors.New("invalid")
)

// Integration is a credentialed connection (§2) with decrypted secrets —
// service-layer only; at rest and over the API secrets are sealed/masked.
type Integration struct {
	ID        string            `json:"id"`
	Port      string            `json:"port"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Config    map[string]any    `json:"config"`
	Secrets   map[string]string `json:"-"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// StoredIntegration is the at-rest shape: secrets sealed (§3).
type StoredIntegration struct {
	ID, Port, Kind, Name string
	Config               map[string]any
	Sealed               []byte
	CreatedAt, UpdatedAt time.Time
}

// RepoConfig is projects.repo (§2).
type RepoConfig struct {
	URL           string `json:"url"`
	Locator       string `json:"locator"`
	DefaultBranch string `json:"default_branch"`
	AgentsMDPath  string `json:"agents_md_path"`
}

// ExtraRepo is an additional repository a project's runs check out beside the
// primary one (projects.repos). The primary repo supplies the devcontainer;
// extras are cloned as siblings (~/repos/<name>) on the same repository host
// account, branched like the primary, and get their own merge request when a
// run changes them. A locator is what lets the repository host open that MR;
// without one the repo is context only (cloned, never published).
type ExtraRepo struct {
	// Name is the checkout directory under ~/repos and how flows, agents and
	// the run's branches refer to it. A slug; defaults to the URL's last path
	// segment.
	Name          string `json:"name"`
	URL           string `json:"url"`
	Locator       string `json:"locator,omitempty"`
	DefaultBranch string `json:"default_branch"`
}

// WorkspaceConfig is projects.workspace (§2, §8).
type WorkspaceConfig struct {
	ExtraDomains []string `json:"extra_domains,omitempty"`
	// EgressMode selects the per-project egress gateway posture: "allow"
	// (deny-all + ExtraDomains/derived allowlist, the default) or "deny"
	// (allow-all + DeniedDomains). Empty ⇒ allow.
	EgressMode string `json:"egress_mode,omitempty"`
	// DeniedDomains is the blocked-host list used in "deny" mode.
	DeniedDomains []string `json:"denied_domains,omitempty"`
	// BlockedDomains is a force-DENY override that wins in BOTH modes — the
	// persisted backing for the run page's live "block this domain" toggle, so
	// future runs inherit the block.
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	// EgressAllowedCIDRs/EgressAllowedPorts are the "allow" mode escape hatch for
	// legitimate raw (non-HTTP/TLS) external egress the default backstop drops.
	EgressAllowedCIDRs []string `json:"egress_allowed_cidrs,omitempty"`
	EgressAllowedPorts []int    `json:"egress_allowed_ports,omitempty"`
	// CodeServerExtensions are VS Code extension ids pre-installed into the
	// workspace's code-server at build time (union with the repo devcontainer's
	// own customizations.vscode.extensions). Baked into the image, so they are
	// not reinstalled every run.
	CodeServerExtensions []string `json:"code_server_extensions,omitempty"`
	// Editor holds the workspace's code-server UI defaults (color theme + common
	// editor prefs). Unlike the extensions above, these are applied as VS Code
	// user settings when the browser editor is opened — so changing them needs
	// no image rebuild.
	Editor EditorConfig `json:"editor,omitzero"`
}

// EditorConfig is the per-project code-server UI defaults (§ code-server
// integration). Neutral, curated fields the devcontainer adapter maps to VS
// Code user settings; applied at editor-start, so changing them needs no
// rebuild. Kept separate from CodeServerExtensions, which must install at
// build time.
type EditorConfig struct {
	Theme        string `json:"theme,omitempty"`          // workbench.colorTheme
	FontSize     int    `json:"font_size,omitempty"`      // editor.fontSize
	TabSize      int    `json:"tab_size,omitempty"`       // editor.tabSize
	FormatOnSave bool   `json:"format_on_save,omitempty"` // editor.formatOnSave
	WordWrap     bool   `json:"word_wrap,omitempty"`      // editor.wordWrap ("on")
	// TrustWorkspace disables VS Code's Workspace Trust prompts (the
	// @tag:workspaceTrust settings) so the ephemeral sandbox opens as trusted.
	TrustWorkspace bool `json:"trust_workspace,omitempty"`
}

// Project picks one integration per port and adds repo/commands/workspace
// specifics (§2).
type Project struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	RepoAccountID string `json:"repo_account_id"`
	ProvisionerID string `json:"provisioner_id"`
	// HarnessIDs is the project's set of AVAILABLE harness integrations (for
	// auth + image layers) — the project has NO default harness. The FLOW
	// owns the default (defaults.harness) and a stage may override it; the
	// named kind resolves to the set member of that kind. At most one
	// integration per kind, so kind→integration is unambiguous. A flow whose
	// harness kind is not in this set is refused at run creation (preflight).
	HarnessIDs []string   `json:"harness_ids"`
	Repo       RepoConfig `json:"repo"`
	// Repos are the additional repositories (multi-repo tasks); see ExtraRepo.
	Repos       []ExtraRepo       `json:"repos,omitempty"`
	Commands    map[string]string `json:"commands"`
	Workspace   WorkspaceConfig   `json:"workspace"`
	DefaultFlow string            `json:"default_flow"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// FlowRow is the current version of a stored flow (§7). Layout is the
// graph editor's node-position metadata (DESIGN3 §2.2) — display-only,
// excluded from Hash, absent from versions.
//
// A flow belongs to ONE project: its stages name that project's commands and
// its agents know that repository, so (ProjectID, Name) is the identity. The
// compiled-in built-ins are the exception that proves it — they are templates
// with no project, resolved for anyone who has not saved their own copy.
type FlowRow struct {
	ProjectID string          `json:"project_id"`
	Name      string          `json:"name"`
	YAML      string          `json:"yaml"`
	Hash      string          `json:"hash"`
	Layout    json.RawMessage `json:"layout,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// FlowVersion is one immutable content-addressed version (§7), within one
// project's history: the same document saved in two projects is two versions
// with one hash, because they are two histories.
type FlowVersion struct {
	ProjectID string    `json:"project_id"`
	Hash      string    `json:"hash"`
	Name      string    `json:"name"`
	YAML      string    `json:"yaml"`
	CreatedAt time.Time `json:"created_at"`
}

// FlowBinding is a project's values for a flow's parameters (flow v4
// `parameters:`): what the project bound in Workshop → Flows → Configure,
// laid over the document's defaults at run creation and under a run's own
// overrides. Keyed by (project, flow name) — a built-in template has no
// FlowRow, and binding one is the common case — and never part of the
// flow's hash: a binding changes nothing in the document.
type FlowBinding struct {
	ProjectID string            `json:"project_id"`
	FlowName  string            `json:"flow_name"`
	Params    map[string]string `json:"params"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// DB is the persistence boundary. Implementations enforce the §2 delete
// rules that need adjacent tables (project→runs, integration→projects,
// flow→projects/runs) and return ErrNotFound/ErrReferenced/ErrConflict.
type DB interface {
	UpsertIntegration(ctx Ctx, r StoredIntegration) error
	GetIntegration(ctx Ctx, id string) (StoredIntegration, error)
	ListIntegrations(ctx Ctx) ([]StoredIntegration, error)
	DeleteIntegration(ctx Ctx, id string) error

	UpsertProject(ctx Ctx, p Project) error
	GetProject(ctx Ctx, id string) (Project, error)
	GetProjectByName(ctx Ctx, name string) (Project, error)
	ListProjects(ctx Ctx) ([]Project, error)
	DeleteProject(ctx Ctx, id string) error

	UpsertTicket(ctx Ctx, t Ticket) error
	GetTicket(ctx Ctx, id string) (Ticket, error)
	ListTickets(ctx Ctx) ([]Ticket, error)
	DeleteTicket(ctx Ctx, id string) error

	UpsertFlow(ctx Ctx, row FlowRow) error // also inserts the immutable version (ON CONFLICT DO NOTHING)
	GetFlow(ctx Ctx, projectID, name string) (FlowRow, error)
	// ListFlows returns one project's flows, or every project's when
	// projectID is empty (the cross-project overview, which is also where
	// copying starts).
	ListFlows(ctx Ctx, projectID string) ([]FlowRow, error)
	DeleteFlow(ctx Ctx, projectID, name string) error
	GetFlowVersion(ctx Ctx, projectID, hash string) (FlowVersion, error)
	ListFlowVersions(ctx Ctx, projectID, name string) ([]FlowVersion, error)

	// Flow bindings go with their project (deleted with it), not with the
	// flow row: a template has none, and the Service decides what a flow's
	// deletion means for its binding.
	PutFlowBinding(ctx Ctx, b FlowBinding) error
	GetFlowBinding(ctx Ctx, projectID, flowName string) (FlowBinding, error)
	ListFlowBindings(ctx Ctx, projectID string) ([]FlowBinding, error)
	DeleteFlowBinding(ctx Ctx, projectID, flowName string) error

	// Automations go with their project (deleted with it).
	UpsertAutomation(ctx Ctx, a Automation) error
	GetAutomation(ctx Ctx, id string) (Automation, error)
	// ListAutomations returns one project's, or every project's when
	// projectID is empty (the watcher's pass), by creation time.
	ListAutomations(ctx Ctx, projectID string) ([]Automation, error)
	DeleteAutomation(ctx Ctx, id string) error

	GetSettings(ctx Ctx) (map[string]string, error) // key -> raw JSON value
	PutSetting(ctx Ctx, key, value string) error
}

// Ctx aliases context.Context (brevity in the wide DB interface).
type Ctx = context.Context

func notFound(what, id string) error {
	return fmt.Errorf("%s %s: %w", what, id, ErrNotFound)
}
