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

	"apothecary/internal/ports/trackerport"
)

// Ports an integration can implement. The provider port was removed
// post-M8: the harness owns model auth (see docs/notes).
const (
	PortRepository  = "repository"
	PortProvisioner = "provisioner"
	PortHarness     = "harness"
	// PortTicketSource is an issue tracker: a remote one (Jira, GitHub or
	// GitLab issues), or the owner's built-in internal tracker. Each workshop
	// reads its tickets live from exactly one (Project.TicketSourceID).
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
	ID string `json:"id"`
	// UserID is the owner: integrations are a person's credentials, seen and
	// used by no one else.
	UserID    string            `json:"user_id"`
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
	ID, UserID, Port, Kind, Name string
	Config                       map[string]any
	Sealed                       []byte
	CreatedAt, UpdatedAt         time.Time
}

// RepoConfig is projects.repo (§2).
type RepoConfig struct {
	URL           string `json:"url"`
	Locator       string `json:"locator"`
	DefaultBranch string `json:"default_branch"`
	AgentsMDPath  string `json:"agents_md_path"`
	// BranchFormat names a new ticket's base branch from {ticket_type},
	// {ticket_number} and {ticket_title} (package branchname); saved as
	// branchname.Default when left empty.
	BranchFormat string `json:"branch_format,omitempty"`
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
	// CodeServerExtensions are VS Code extension ids (Open VSX) installed into
	// the workspace's code-server the first time the browser editor opens in
	// a run (union with the repo devcontainer's own
	// customizations.vscode.extensions), through the project gateway's
	// extension cache — so a changed list needs no rebuild.
	CodeServerExtensions []string `json:"code_server_extensions,omitempty"`
	// Editor holds the workspace's code-server UI defaults (color theme + common
	// editor prefs), applied as VS Code user settings each time the browser
	// editor is opened.
	Editor EditorConfig `json:"editor,omitzero"`
	// Services are the project's long-running processes — its app, a
	// database — named by a flow's service stages (usually through a
	// parameter: `service: $app`) and started from a run's page.
	Services []ServiceConfig `json:"services,omitempty"`
	// Browser is the project's browser: one container its runs share, a
	// Chromium per agent session reaching the run's services at localhost —
	// what a flow's builtin `browser` MCP server drives.
	Browser BrowserConfig `json:"browser,omitzero"`
	// Docker gives this project's workspaces their run's own docker daemon
	// (testcontainers, `docker compose`, `docker run` from the agent) without
	// the repo declaring the docker-outside-of-docker feature itself: the
	// engine injects that feature into the devcontainer build. Subject to the
	// deployment's daemon door (APOTHECARY_WORKSPACE_DOCKER): a closed door
	// refuses the project's runs at provision, naming the fix.
	Docker bool `json:"docker,omitempty"`
}

// ServiceConfig is one project service. Start runs in the run's primary
// checkout. A start that keeps running is the service (stopped by signalling
// it, or by Stop); one that exits 0 started something in the background —
// `docker compose up -d` — and then Stop is what brings it down. Either way
// Health says when it is up.
type ServiceConfig struct {
	// Name is a slug: what a flow's `service:` and `run.service.<name>`
	// name. A project usually calls its main one `default`, the builder
	// template's default.
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Start       string        `json:"start"`
	Stop        string        `json:"stop,omitempty"`
	Health      ServiceHealth `json:"health,omitzero"`
	// Ports are where it listens in the workspace; the first is its
	// address (run.service.<name>) and each one gets a preview link.
	Ports []ServicePort `json:"ports,omitempty"`
}

// ServiceHealth says when a service is up: at most one probe (none = up as
// soon as it started), probed until Timeout while starting, then every
// Interval.
type ServiceHealth struct {
	// HTTP is a URL probed in the workspace (http://localhost:3000/health):
	// any 2xx or 3xx answer is healthy.
	HTTP string `json:"http,omitempty"`
	// TCP is a port that accepts connections.
	TCP int `json:"tcp,omitempty"`
	// Command is a script that exits 0.
	Command  string `json:"command,omitempty"`
	Timeout  string `json:"timeout,omitempty"`  // a duration; default 5m
	Interval string `json:"interval,omitempty"` // a duration; default 10s
}

// ServicePort is one port a service listens on.
type ServicePort struct {
	Port  int    `json:"port"`
	Label string `json:"label,omitempty"`
	// Path is what its link opens: "/" by default.
	Path string `json:"path,omitempty"`
	// Protocol is "http" (a web app, the default when empty) or "tcp"
	// (anything else: a database, a cache), which gets no browser link.
	Protocol string `json:"protocol,omitempty"`
}

// BrowserConfig turns the project's browser on.
type BrowserConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// EditorConfig is the per-project code-server UI defaults (§ code-server
// integration). Neutral, curated fields the devcontainer adapter maps to VS
// Code user settings; applied at editor-start, so changing them needs no
// rebuild.
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
	ID string `json:"id"`
	// UserID is the owner. Everything under the project — flows, bindings,
	// automations, secrets, tickets, runs — is theirs through it.
	UserID        string `json:"user_id"`
	Name          string `json:"name"`
	RepoAccountID string `json:"repo_account_id"`
	ProvisionerID string `json:"provisioner_id"`
	// TicketSourceID is the workshop's ticket source: a ticketsource
	// integration of the owner — a remote tracker, or their internal one
	// (the default).
	TicketSourceID string `json:"ticket_source_id"`
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
//
// Lists take the owner: a userID scopes them to one person's rows, and ""
// is every row — for the daemon's own passes (the automation watcher, the
// reaper), never for a request.
type DB interface {
	UpsertIntegration(ctx Ctx, r StoredIntegration) error
	GetIntegration(ctx Ctx, id string) (StoredIntegration, error)
	ListIntegrations(ctx Ctx, userID string) ([]StoredIntegration, error)
	// DeleteIntegration refuses one a project references — its ticket source
	// included — naming the projects (ErrReferenced). Its tracker lists go
	// with it, its tickets stay: their type_id is set NULL in the same
	// transaction (tickets.source_id has no FK).
	DeleteIntegration(ctx Ctx, id string) error

	UpsertProject(ctx Ctx, p Project) error
	GetProject(ctx Ctx, id string) (Project, error)
	// GetProjectByName finds a user's project by name ("" matches any
	// owner: the CLI's envcheck and dev seeding).
	GetProjectByName(ctx Ctx, userID, name string) (Project, error)
	ListProjects(ctx Ctx, userID string) ([]Project, error)
	// ListVisibleProjects is userID's projects ("" matches no owner, unlike
	// ListProjects) and those whose id is in projectIDs, oldest first.
	ListVisibleProjects(ctx Ctx, userID string, projectIDs []string) ([]Project, error)
	// DeleteProject takes its members with it.
	DeleteProject(ctx Ctx, id string) error

	// Members: a workshop shared with other accounts. Both writes are a
	// compare-and-swap on the account's role there, from ("" = not yet a
	// member): a role changed meanwhile is ErrConflict and nothing is
	// written, which holds the owner-only rule on maintainers against a
	// stale read. PutMember inserts or changes the role (created_at kept; a
	// missing project is ErrNotFound, a role outside MemberRoles
	// ErrInvalid); DeleteMember of one that is not there is ErrNotFound.
	PutMember(ctx Ctx, m Member, from string) error
	GetMember(ctx Ctx, projectID, userID string) (Member, error)
	ListMembers(ctx Ctx, projectID string) ([]Member, error) // oldest first
	DeleteMember(ctx Ctx, projectID, userID, from string) error
	// ProjectAccess is a project's owner and userID's role on it (RoleOwner,
	// a member role, or ""); a missing project is ErrNotFound.
	ProjectAccess(ctx Ctx, projectID, userID string) (owner, role string, err error)
	// ProjectsOf is every project userID owns (RoleOwner) or is a member of,
	// by project id.
	ProjectsOf(ctx Ctx, userID string) ([]Reach, error)

	// Ticket writes check the ticket's status and type rows exist
	// (ErrInvalid), not that they are its tracker's — that is the Service's.
	// A second row with the same (source_id, remote_id) is ErrConflict.
	// UpsertTicket, SetTicketSeen and SetTicketLabels append a version made
	// by c, in the same write, when the stored title, goal, base branch,
	// change type, type or labels differ from the latest version, and keep
	// the newest MaxTicketVersions; two appending at once is ErrConflict.
	UpsertTicket(ctx Ctx, t Ticket, c Change) error
	GetTicket(ctx Ctx, id string) (Ticket, error)
	DeleteTicket(ctx Ctx, id string) error // its comments, links and versions go with it
	// ListTicketVersions is a ticket's versions, newest first (nil: none);
	// a missing ticket is ErrNotFound. GetTicketVersion is one, by seq.
	ListTicketVersions(ctx Ctx, ticketID string) ([]TicketVersion, error)
	GetTicketVersion(ctx Ctx, ticketID string, seq int) (TicketVersion, error)
	// GetTicketByRemote is the record of a source's issue by the tracker's
	// stable id; ErrNotFound when there is none.
	GetTicketByRemote(ctx Ctx, sourceID, remoteID string) (Ticket, error)
	// GetTicketBySourceKey is the record of a source's issue by its
	// last-seen key (exact): a real issue's record before a placeholder's
	// (a remote_id of "dup:" or "legacy:"), then the newest. ErrNotFound
	// when there is none.
	GetTicketBySourceKey(ctx Ctx, sourceID, key string) (Ticket, error)
	// ListTicketsByRemote is the records of those issues that have one, in
	// any order.
	ListTicketsByRemote(ctx Ctx, sourceID string, remoteIDs []string) ([]Ticket, error)
	// ListInternalTickets is an internal tracker's tickets matching q, in
	// (created_at DESC, id DESC) order, at most q.Limit (<= 0 = 50).
	ListInternalTickets(ctx Ctx, sourceID string, q InternalTicketQuery) ([]Ticket, error)
	// SetTicketSeen writes what was last seen of a remote issue: title, goal,
	// source key/url/type, type_id ("" = NULL), change type, labels and
	// updated_at = at. remote_id is unchanged.
	SetTicketSeen(ctx Ctx, id string, seen TicketSeen, typeID string, changeType trackerport.ChangeType, at time.Time, c Change) error
	// SetTicketStatus sets the status in one statement, stamping
	// status_changed_at/by and updated_at only when it changes.
	SetTicketStatus(ctx Ctx, id, statusID, by string, at time.Time) error
	// SetTicketLabels replaces the labels, or adds to them in order of first
	// appearance, in one write (with its version); returns the result.
	SetTicketLabels(ctx Ctx, id string, labels []string, union bool, at time.Time, c Change) ([]string, error)

	// A ticket's comments and links go with it.
	ListTicketComments(ctx Ctx, ticketID string) ([]TicketComment, error)
	// InsertTicketComment adds c unless one with its (run, stage) is already
	// there (posted=false). A missing ticket is ErrNotFound.
	InsertTicketComment(ctx Ctx, c TicketComment) (posted bool, err error)
	UpsertTicketLink(ctx Ctx, l TicketLink) error // a missing ticket is ErrNotFound
	ListTicketLinks(ctx Ctx, ticketID string) ([]TicketLink, error)

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

	// A ticketsource integration's status and type lists go with it
	// (deleted with the integration). Only the internal tracker has
	// statuses: a remote one's are read from it. Lists are in position order.
	ListTrackerStatuses(ctx Ctx, integrationID string) ([]TrackerStatus, error)
	ListTrackerTypes(ctx Ctx, integrationID string) ([]TrackerType, error)
	// ReplaceTrackerStatuses makes rows the integration's whole list, in one
	// transaction: tickets on a row being dropped move to reassign[id], the
	// dropped rows go, and rows are written by id. A dropped row a ticket
	// still uses with no reassignment is ErrReferenced; a remote id held
	// twice is ErrConflict.
	ReplaceTrackerStatuses(ctx Ctx, integrationID string, rows []TrackerStatus, reassign map[string]string) error
	// ReplaceTrackerTypes is ReplaceTrackerStatuses for types; every ticket of
	// a type also takes the type's change type.
	ReplaceTrackerTypes(ctx Ctx, integrationID string, rows []TrackerType, reassign map[string]string) error
	// EnsureTrackerStatus inserts s unless the integration already lists its
	// remote id; then the listed row takes s's name and keeps everything
	// else. Returns the stored row. A missing integration is ErrInvalid.
	EnsureTrackerStatus(ctx Ctx, s TrackerStatus) (TrackerStatus, error)
	// EnsureTrackerType is EnsureTrackerStatus for types: a listed row takes
	// the name only.
	EnsureTrackerType(ctx Ctx, t TrackerType) (TrackerType, error)

	// Project secrets go with their project (deleted with it); each value
	// is sealed by the Service before it gets here.
	PutProjectSecret(ctx Ctx, projectID string, sec StoredProjectSecret) error
	ListProjectSecrets(ctx Ctx, projectID string) ([]StoredProjectSecret, error)
	DeleteProjectSecret(ctx Ctx, projectID, name string) error

	// Automations go with their project (deleted with it).
	UpsertAutomation(ctx Ctx, a Automation) error
	GetAutomation(ctx Ctx, id string) (Automation, error)
	// ListAutomations returns one project's, or every project's when
	// projectID is empty (the watcher's pass), by creation time.
	ListAutomations(ctx Ctx, projectID string) ([]Automation, error)
	DeleteAutomation(ctx Ctx, id string) error

	// Settings are per user: key -> raw JSON value.
	GetSettings(ctx Ctx, userID string) (map[string]string, error)
	PutSetting(ctx Ctx, userID, key, value string) error

	// Provider credentials are a user's; the Service seals them and refuses
	// to delete one a harness integration still references.
	UpsertCredential(ctx Ctx, c ProviderCredential) error
	GetCredential(ctx Ctx, id string) (ProviderCredential, error)
	// ListCredentials returns one user's ("" = everyone's).
	ListCredentials(ctx Ctx, userID string) ([]ProviderCredential, error)
	DeleteCredential(ctx Ctx, id string) error
}

// Ctx aliases context.Context (brevity in the wide DB interface).
type Ctx = context.Context

func notFound(what, id string) error {
	return fmt.Errorf("%s %s: %w", what, id, ErrNotFound)
}
