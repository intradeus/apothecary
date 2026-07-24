// Package ports holds the engine-shaped vendor boundaries (DESIGN.md §4).
// Ports are shaped by what the engine needs, never by a vendor's API; no
// vendor types cross this boundary. The fakes in internal/adapters/fake are
// the test substrate for every §16 scenario.
package ports

import (
	"context"
	"io/fs"
	"net"
	"time"
)

type EnvID string

// HarnessInstall is one harness's in-workspace install. Binary is what the
// engine probes with `command -v` (envcheck, §9); Commands are the shell
// lines that install it. Kept vendor-neutral (no Dockerfile syntax) so an
// adapter can run them however its environment allows — a devcontainer
// feature, an exec, or a build layer.
type HarnessInstall struct {
	Binary string
	// Requires are the tools Commands need present first (`npm` for an
	// npm-installed harness, `curl` for a script-installed one). The
	// installer provides each missing one before running Commands, so
	// nothing assumes a particular runtime: a harness that needs no Node
	// never triggers a Node install.
	Requires []string
	Commands []string
}

// EnvSpec carries the §11 provisioning inputs. Everything the adapter may
// expose to the vendor (template parameters, labels, generated configs)
// must be non-secret; GitUser/GitPass are the one engine-held exception,
// provided for adapters that must materialize the source BEFORE the
// environment exists (clone-first models like devcontainer) — they run the
// clone engine-side and must never let the credential reach the vendor
// surface or the workspace (in-workspace git auth is bootstrap's job, §11
// step 3).
type EnvSpec struct {
	RunID   string
	RepoURL string
	Branch  string
	// Name is the human-friendly workspace/container name the engine derives
	// from the branch (engine.workspaceName: "apo-" + slugified branch + a
	// short run-id tail for uniqueness, ≤32 chars for coder). Adapters use it
	// verbatim as the coder workspace / devcontainer container name and the
	// env identity. Empty ⇒ the adapter falls back to WorkspaceName(RunID),
	// the legacy run-id scheme, so direct and test callers keep working.
	Name string
	// Project is the run's project name, stamped on the container as the
	// apothecary.project docker label so containers can be grouped by project
	// (`docker ps --filter label=apothecary.project=<name>`). Empty ⇒ the
	// label is omitted.
	Project string
	// HarnessInstall is how to install the project's harnesses INSIDE the
	// workspace — a repo-authored devcontainer runs the repo's own image,
	// which knows nothing about opencode/codex, so the harness is injected as
	// a feature. Empty means the image already carries them.
	HarnessInstall []HarnessInstall
	// CodeServerExtensions are project-declared VS Code extension ids baked
	// into the workspace's code-server (union with the repo devcontainer's own
	// customizations.vscode.extensions, which the adapter reads). Non-secret.
	CodeServerExtensions []string
	// EgressDomains is the fully derived firewall allowlist (deny-all + allowlist
	// mode): repo host ∪ adapter contributions ∪ registry defaults ∪ project
	// extras. Non-secret.
	EgressDomains []string
	// EgressMode selects the gateway posture: "allow" (deny-all + EgressDomains,
	// the default) or "deny" (allow-all + EgressDenylist). Empty ⇒ allow.
	EgressMode string
	// EgressDenylist is the blocked-host list used in "deny" mode.
	EgressDenylist []string
	// EgressAllowedCIDRs/EgressAllowedPorts are the "allow" mode escape hatch for
	// legitimate raw (non-HTTP/TLS) external egress that the default backstop
	// would otherwise drop (e.g. an external managed database).
	EgressAllowedCIDRs []string
	EgressAllowedPorts []int
	// GitUser/GitPass are the forge git credential (see type comment).
	// Empty for public repos; adapters without an engine-side source step
	// (coder) ignore them.
	GitUser string
	GitPass string
}

type Command struct {
	Script string // executed `bash -eo pipefail` by the adapter
	// Dir is the working directory relative to the workspace home
	// ("" = home, "repo" = the checkout). Command stages run in the repo
	// root (§3); bootstrap's clone step runs in home.
	Dir     string
	Timeout time.Duration
}

type ExecResult struct {
	ExitCode int
	Output   string // combined stdout+stderr; adapters also stream to the stage log
}

type AccessLink struct {
	Kind string // ide | terminal | app | vscode | ssh
	URL  string
	// Command is the copy-paste alternative for access methods that aren't
	// a browser URL (ssh). Links carry a URL, a Command, or both.
	Command string
}

type EnvStatus string

const (
	EnvStarting EnvStatus = "starting"
	EnvRunning  EnvStatus = "running"
	EnvStopped  EnvStatus = "stopped"
	EnvAbsent   EnvStatus = "absent"
	// EnvUnknown is NOT returned by any adapter's Status — a provisioner
	// either knows a workspace's state or errors. The engine synthesizes it
	// when a status probe fails (provisioner unreachable, expired session,
	// deleted project) so the UI can show "can't tell" instead of a stale
	// last-known value dressed up as live.
	EnvUnknown EnvStatus = "unknown"
)

type EnvProvisioner interface {
	// Provision blocks until the environment is ready; 1 internal retry.
	Provision(ctx context.Context, spec EnvSpec) (EnvID, error)
	Exec(ctx context.Context, env EnvID, cmd Command) (ExecResult, error)
	// ReadFile/WriteFile paths: relative = repo-root-relative
	// (artifacts/plan.md), absolute = absolute (/apothecary/run.env).
	ReadFile(ctx context.Context, env EnvID, path string) ([]byte, error)
	WriteFile(ctx context.Context, env EnvID, path string, data []byte, mode fs.FileMode) error
	Links(ctx context.Context, env EnvID) ([]AccessLink, error)
	Status(ctx context.Context, env EnvID) (EnvStatus, error)
	Destroy(ctx context.Context, env EnvID) error
}

type SessionID string

// AgentSpec is what a harness needs to invoke one agent stage. The agent
// name resolves inside the workspace (harness-native config generated at
// bootstrap from .apothecary/agents/, §5.2).
type AgentSpec struct {
	Agent    string
	Harness  string
	Model    string // provider/model
	MaxTurns int
}

type AgentInput struct {
	Prompt string // assembled per §3.2
}

type Verdict struct {
	Approve  bool
	Comments []string
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

type AgentResult struct {
	Session   SessionID
	Verdict   *Verdict // parsed artifacts/verdict.json, when the stage demands one
	Output    string
	TurnsUsed int
	Usage     Usage
}

type AgentRunner interface {
	Run(ctx context.Context, env EnvID, spec AgentSpec, in AgentInput) (AgentResult, error)
	Resume(ctx context.Context, env EnvID, s SessionID, reply string) (AgentResult, error)
}

// RepoRef identifies a project repo at its forge.
type RepoRef struct {
	URL           string
	DefaultBranch string
	// Locator is the forge-kind-specific repo identifier (DESIGN2 §5):
	// GitLab's numeric project id as a string, GitHub's "owner/repo".
	// Filled by the repo picker; adapters parse their own locator.
	Locator string
}

type MRRef struct {
	IID int
	URL string
}

type MRState string

const (
	MROpen   MRState = "open"
	MRMerged MRState = "merged"
	MRClosed MRState = "closed"
)

// WorkspaceReattacher is an OPTIONAL EnvProvisioner capability: after an
// engine restart, re-establish any engine-side resources for a still-
// running workspace that died with the previous process — notably the
// reverse tunnel (workspace → engine/proxy loopback). Idempotent; called
// on the recovery/resume reuse path. Adapters with no such state (fake)
// don't implement it.
type WorkspaceReattacher interface {
	Reattach(ctx context.Context, env EnvID) error
}

// EgressDenial is one blocked host as the run page consumes it (the gateway
// tracks these per run; the engine surfaces them at GET /runs/{id}/egress-denials).
type EgressDenial struct {
	Host  string    `json:"host"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

// EgressAuditRecord is one aggregated egress fact for a run (a (host, port,
// verdict) tuple) with counts, byte totals and first/last timestamps.
type EgressAuditRecord struct {
	Host     string    `json:"host"`
	DstIP    string    `json:"dst_ip"`
	Port     int       `json:"port"`
	Proto    string    `json:"proto"`
	Verdict  string    `json:"verdict"`
	Count    int64     `json:"count"`
	BytesOut int64     `json:"bytes_out"`
	BytesIn  int64     `json:"bytes_in"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
}

// EgressAuditRow is a durable audit record (an EgressAuditRecord tagged with its
// project + engine run id) as stored and queried in the audit trail.
type EgressAuditRow struct {
	Project  string    `json:"project"`
	Run      string    `json:"run"`
	Host     string    `json:"host"`
	DstIP    string    `json:"dst_ip"`
	Port     int       `json:"port"`
	Proto    string    `json:"proto"`
	Verdict  string    `json:"verdict"`
	Count    int64     `json:"count"`
	BytesOut int64     `json:"bytes_out"`
	BytesIn  int64     `json:"bytes_in"`
	First    time.Time `json:"first_seen"`
	Last     time.Time `json:"last_seen"`
}

// EgressAuditFilter narrows an audit query; empty fields are ignored.
type EgressAuditFilter struct {
	Run     string
	Project string
	Verdict string
	Host    string
	Limit   int
}

// EgressAuditor is an OPTIONAL EnvProvisioner capability: the aggregated egress
// audit trail for an environment (every host the workspace reached or was
// blocked from, with counts + bytes + timestamps), read from the per-project
// gateway. The engine ingests these into durable storage so the trail survives
// the ephemeral gateway.
type EgressAuditor interface {
	EgressAudit(ctx context.Context, env EnvID) []EgressAuditRecord
}

// EgressController is an OPTIONAL EnvProvisioner capability: inspect and live-
// edit a run's egress policy, backed by the per-project egress gateway
// (internal/egressgw). AllowEgress is the "add to egress" fast path — it rewrites
// the project policy file and the gateway hot-reloads it, so the next request
// succeeds with no restart. Because the policy is per-project, an allow applies
// to every run of the project; denials stay per-run. Provisioners without a
// gateway simply don't implement it and the engine degrades honestly.
type EgressController interface {
	EgressDenials(ctx context.Context, env EnvID) []EgressDenial
	AllowEgress(ctx context.Context, env EnvID, host string) error
}

// PortConn is one exposed workspace port as the engine consumes it: a
// dialer for per-browser-connection streams plus teardown. The ENGINE owns
// the handle (provisioner adapter instances are refcount-closed on config
// edits; a subprocess or listener parented there would die with them).
type PortConn interface {
	Dial(ctx context.Context) (net.Conn, error)
	Close() error
}

// WorkspacePortExposer is an OPTIONAL EnvProvisioner capability: reach a
// TCP port inside the environment so the engine can publish it to the
// human (the "browse the app under development" path). Adapters implement
// whatever transport reaches their workspaces (coder: a supervised
// port-forward subprocess; devcontainer: a reverse relay over the
// bind-mounted unix sockets).
type WorkspacePortExposer interface {
	// ExposePort prepares access to the port AS SERVED BY the environment and
	// returns the dial handle. Which container actually serves it is the
	// adapter's business: a compose devcontainer's port may belong to a
	// sibling service rather than the workspace's own loopback. Idempotency
	// across repeated exposures of the same port is the caller's job (the
	// engine's port registry).
	ExposePort(ctx context.Context, env EnvID, port int) (PortConn, error)
	// AppPorts lists ports the environment declares it will serve (e.g.
	// devcontainer forwardPorts) — auto-exposed at env_ready. Best-effort;
	// empty is normal.
	AppPorts(ctx context.Context, env EnvID) []int
}

// CodeServerPort is the fixed workspace-loopback port code-server binds
// (avoids the engine 8080 / egress-proxy 8079 loopback ports the bridge uses).
const CodeServerPort = 8471

// EditorStarter is an OPTIONAL EnvProvisioner capability: start an in-workspace
// code-server (VS Code in the browser) listening on the given loopback port, so
// the engine can expose it (the "Open editor" path). Idempotent — a no-op when
// code-server is already listening; returns when the port accepts connections.
// The binary is installed by the injected code-server devcontainer feature.
type EditorStarter interface {
	StartEditor(ctx context.Context, env EnvID, port int) error
}

// WorkspaceRebuilder is an OPTIONAL EnvProvisioner capability: recreate the
// environment's container(s) IN PLACE, keeping the run and the engine-side
// checkout (agent commits survive). Distinct from a fresh Provision (same
// env identity) and from the engine's re-run/resume actions. The engine
// re-runs bootstrap afterwards (the fresh container has no /apothecary state).
type WorkspaceRebuilder interface {
	Rebuild(ctx context.Context, spec EnvSpec) error
}

// ObservedPort is a listening TCP port discovered at runtime inside an
// environment (as opposed to declared in devcontainer.json). Process is a
// best-effort name; Service names the compose sibling that serves it (empty
// for ports on the workspace container itself).
type ObservedPort struct {
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"`
	Service string `json:"service,omitempty"`
}

// SiblingPortScanner is an OPTIONAL EnvProvisioner capability: discover
// listening ports in the environment's SIBLING containers (compose
// services), which the in-workspace scanner cannot see (separate network +
// pid namespaces). The workspace's own ports are discovered in-container and
// reported directly to the engine; this covers the rest. Implementations
// also prime their expose routing so a discovered sibling port can be
// reached by ExposePort. Best-effort; empty is normal (non-compose, or a
// sibling image without a shell/procfs).
type SiblingPortScanner interface {
	ScanSiblingPorts(ctx context.Context, env EnvID) []ObservedPort
}

// ExecLogTailer is an OPTIONAL EnvProvisioner capability: a live tail of
// an environment's combined exec output (bootstrap, commands, agent
// invocations) for run-page feedback. How output is captured is the
// adapter's business; implementations that cannot stream simply don't
// implement the interface and the API degrades honestly.
type ExecLogTailer interface {
	TailExecLog(ctx context.Context, env EnvID, n int) ([]byte, error)
}

type Forge interface {
	// CheckAccess verifies token validity, project reachability and scopes (§1 preflight).
	CheckAccess(ctx context.Context, repo RepoRef) error
	FileExists(ctx context.Context, repo RepoRef, path string) (bool, error)
	CreateDraftMR(ctx context.Context, repo RepoRef, branch, title, body string) (MRRef, error)
	MRState(ctx context.Context, mr MRRef) (MRState, error)
}

// Notification is the one-way fan-out payload; the CPUI inbox is canonical,
// channels are best-effort mirrors.
type Notification struct {
	RunID  string
	PingID string
	Type   string // ping type
	Title  string
	Body   string
	URL    string // deep link into the CPUI
}

type NotificationChannel interface {
	// Send errors are logged, never fail the run.
	Send(ctx context.Context, n Notification) error
}
