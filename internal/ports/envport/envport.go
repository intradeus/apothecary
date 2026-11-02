// Package envport is the workspace port: what the engine and its services
// ask of a run's environment — provision, exec, files, destroy — and the
// optional capabilities a provisioner may offer on top (port exposure, the
// editor, rebuild, the sidecar, SSH, resource samples, the build cache…),
// each its own small interface so a provisioner implements only what it can.
// It imports nothing internal: every other port and adapter builds on it.
package envport

import (
	"context"
	"io/fs"
	"net"
	"strings"
	"time"
)

type EnvID string

// ProjectHostKey is the ONE token host-side, per-project resources are named
// by: the egress gateway and browser containers, their volumes and folders
// (<RunsDir>/gw/<key>, <RunsDir>/browser/<key>) and the build-cache registry's
// repository path. It is the project's ID (projects.id, a ULID) lowercased —
// docker names and registry paths want lowercase, and a ULID is uppercase
// Crockford base32 — with anything else mapped to '-' for safety; an empty id
// (direct and test callers with no project) reads as "default". The project's
// NAME keys nothing: two users' "api" projects must never share a gateway, and
// renaming a project must not move it off its caches. The name survives only
// as the apothecary.project.name docker label, for `docker ps`.
func ProjectHostKey(projectID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(projectID) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "default"
	}
	return out
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
	// from the branch (wsname.For: "apo-" + slugified branch + a
	// short run-id tail for uniqueness, ≤32 chars). Adapters use it verbatim
	// as the container / pod name and the env identity. Empty ⇒ the adapter falls back to WorkspaceName(RunID),
	// the legacy run-id scheme, so direct and test callers keep working.
	Name string
	// Project is the run's project ID (projects.id): what every host-side
	// per-project resource is keyed by (ProjectHostKey — the egress gateway,
	// the build cache, the browser) and the apothecary.project docker label
	// containers are grouped by (`docker ps --filter
	// label=apothecary.project=<id>`). Empty ⇒ no label, the "default" key.
	Project string
	// ProjectName is the project's human name, stamped as the
	// apothecary.project.name label where a container is created with --label,
	// so `docker ps` reads well. It keys NOTHING: a rename changes the label on
	// the next create and nothing else. Empty ⇒ omitted.
	ProjectName string
	// NoCache builds the workspace image from scratch, ignoring the project's
	// build cache. The cache is keyed by what the build actually reads, so a
	// changed Dockerfile or context file already rebuilds on its own — this is
	// for the staleness a cache key cannot see: a floating base tag that moved,
	// an `apt-get update` layer from three months ago. The fresh layers still
	// REPLACE the cached ones, so one no-cache build fixes the next ten.
	NoCache bool
	// EgressDomains is the fully derived firewall allowlist (deny-all + allowlist
	// mode): repo host ∪ adapter contributions ∪ registry defaults ∪ project
	// extras. Non-secret.
	EgressDomains []string
	// EgressMode selects the gateway posture: "allow" (deny-all + EgressDomains,
	// the default) or "deny" (allow-all + EgressDenylist). Empty ⇒ allow.
	EgressMode string
	// EgressDenylist is the blocked-host list used in "deny" mode.
	EgressDenylist []string
	// EgressBlocked is the force-DENY override (wins in both modes) — the run
	// page's live "block domain" toggle, persisted per project.
	EgressBlocked []string
	// EgressAllowedCIDRs/EgressAllowedPorts are the "allow" mode escape hatch for
	// legitimate raw (non-HTTP/TLS) external egress that the default backstop
	// would otherwise drop (e.g. an external managed database).
	EgressAllowedCIDRs []string
	EgressAllowedPorts []int
	// GitUser/GitPass are the repository host git credential (see type
	// comment). Empty for public repos.
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
	// Marker tags the command's process tree (as APOTHECARY_CMD in its
	// environment) and, before it starts, kills whatever earlier command with
	// the same marker is still running in the environment — TERM, then KILL.
	// Command stages set it per stage: an attempt's exec client dies with its
	// timeout or with the engine process, but the tree inside the container
	// does not, so without this a retry or a recovery re-entry would race
	// the orphan. Empty: no tagging, no sweep.
	Marker string
	// Scrub, when set, is applied to every line of the command's output the
	// adapter streams into the workspace's exec log: a command that runs repo
	// or agent code can print the run's credentials. nil: lines go as they are.
	Scrub func(string) string
	// NoLog keeps the command's output out of the exec log: what it prints is
	// returned to the caller, not progress worth streaming.
	NoLog bool
}

type ExecResult struct {
	ExitCode int
	Output   string // combined stdout+stderr; adapters also stream to the stage log
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
	// WriteTree replaces the ABSOLUTE directory dir with exactly files, in
	// one round trip: whatever was there is removed and the tree is laid
	// down as the remote user. Engine-owned locations only
	// (/apothecary/...); a relative dir is refused, and so is any file path
	// that is not a clean relative path (see TreeFile).
	WriteTree(ctx context.Context, env EnvID, dir string, files []TreeFile) error
	// ListFiles lists the regular files under dir, recursively, sorted by
	// path — what a files port holds. Symlinks are never followed; a dir
	// that is not there is fs.ErrNotExist. At most ListFilesLimit entries.
	ListFiles(ctx context.Context, env EnvID, dir string) ([]FileEntry, error)
	Status(ctx context.Context, env EnvID) (EnvStatus, error)
	Destroy(ctx context.Context, env EnvID) error
}

// TreeFile is one file of a directory tree WriteTree lays down: a clean
// relative path (forward slashes, no empty, "." or ".." segments, no leading
// "/"), its permission bits (0 = 0644) and its bytes.
type TreeFile struct {
	Path string
	Mode fs.FileMode
	Data []byte
}

// FileEntry is one file ListFiles found: its path as ReadFile takes it (the
// directory's path joined with the file's own) and its size in bytes.
type FileEntry struct {
	Path string
	Size int64
}

// ListFilesLimit bounds one ListFiles: a directory holding more lists its
// first ListFilesLimit files. A files port is screenshots and reports, not a
// tree to walk.
const ListFilesLimit = 1000

// WorkspaceReattacher is an OPTIONAL EnvProvisioner capability: after an
// engine restart, re-establish any engine-side resources for a still-
// running workspace that died with the previous process — notably the
// reverse tunnel (workspace → engine/proxy loopback). Idempotent; called
// on the recovery/resume reuse path. Adapters with no such state (fake)
// don't implement it.
type WorkspaceReattacher interface {
	Reattach(ctx context.Context, env EnvID) error
}

// BuildCacheInfo is what a project's devcontainer build cache holds right now.
// Available is false when the provisioner has a cache but cannot be reached
// for it (no gateway up yet) — distinct from an empty cache, which is a real
// answer.
type BuildCacheInfo struct {
	Available bool  `json:"available"`
	Bytes     int64 `json:"bytes"`
}

// BuildCacheController is an OPTIONAL EnvProvisioner capability: report and
// wipe the project's devcontainer build cache.
//
// The cache is per PROJECT and outlives every run, which is the point — a run's
// daemon is thrown away, so without a cache that outlives it each run rebuilds
// the image from scratch. It is also the one piece of derived state a human
// occasionally has to delete: a base image that moved under a floating tag, a
// feature install that cached a bad layer. Provisioners without one simply
// don't implement this and the engine says so honestly. project is the
// project's id — the same value EnvSpec.Project carried at Provision.
type BuildCacheController interface {
	BuildCache(ctx context.Context, project string) (BuildCacheInfo, error)
	ClearBuildCache(ctx context.Context, project string) error
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
// whatever transport reaches their workspaces (devcontainer: a reverse
// relay over the bind-mounted unix sockets).
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

// EditorSettings are neutral, editor-agnostic UI preferences and extensions
// applied to the in-workspace editor when it starts — written as the editor's
// user settings and installed into its per-run extensions dir, so changing
// them needs NO image rebuild. The adapter maps these to its editor's native
// config (code-server → VS Code settings.json keys, Open VSX ids); a future
// editor adapter maps the same neutral fields to its own. Zero value ⇒ leave
// the editor's own defaults untouched.
type EditorSettings struct {
	// Extensions are the VS Code extension ids the project declares plus its
	// harnesses' own; the adapter unions them with the ones the workspace
	// definition itself declares
	// (a devcontainer's customizations.vscode.extensions) and installs the
	// union when the editor first opens in a run. Non-secret.
	Extensions []string
	// Theme is the color theme name (code-server: workbench.colorTheme); must be
	// a built-in theme or one carried by an installed extension.
	Theme string
	// FontSize / TabSize apply only when > 0.
	FontSize int
	TabSize  int
	// FormatOnSave / WordWrap toggle the matching editor prefs when true.
	FormatOnSave bool
	WordWrap     bool
	// TrustWorkspace, when true, disables VS Code's Workspace Trust feature
	// (the @tag:workspaceTrust settings) so the editor never prompts to trust
	// the folder — the workspace is an ephemeral sandbox where the agent already
	// runs arbitrary code, so the prompts are pure friction.
	TrustWorkspace bool
}

// EditorStarter is an OPTIONAL EnvProvisioner capability: start an in-workspace
// code-server (VS Code in the browser) listening on the given loopback port, so
// the engine can expose it (the "Open editor" path). Idempotent — a no-op when
// code-server is already listening; returns when the port accepts connections.
// The binary comes from the tools bundle mounted into every workspace (glibc
// images only). settings are the project's editor UI defaults and extensions,
// (re)applied on each start.
type EditorStarter interface {
	StartEditor(ctx context.Context, env EnvID, port int, settings EditorSettings) error
	// EditorExtensions is the list StartEditor installs for env: the
	// definition's own ids (a devcontainer's customizations.vscode.extensions)
	// ∪ settings.Extensions, deduplicated and sorted. The run page hands it
	// to a desktop editor connecting over SSH, so the same extensions can
	// follow a person there. Best effort: what the definition cannot say is
	// left out, never an error.
	EditorExtensions(ctx context.Context, env EnvID, settings EditorSettings) []string
}

// WorkspaceRebuilder is an OPTIONAL EnvProvisioner capability: recreate the
// environment's container(s) IN PLACE, keeping the run and the engine-side
// checkout (agent commits survive). Distinct from a fresh Provision (same
// env identity) and from the engine's re-run/resume actions. The engine
// re-runs bootstrap afterwards (the fresh container has no /apothecary state).
type WorkspaceRebuilder interface {
	Rebuild(ctx context.Context, spec EnvSpec) error
}

// WorkspaceDirtyChecker is an OPTIONAL EnvProvisioner capability: whether the
// environment's build inputs (its devcontainer configuration) have changed
// since the container was built, so the UI can suggest a rebuild — a
// "dirty" workspace. External state like EnvStatus: polled by the engine's
// reconciler, kept in memory, never in the event log. False when the adapter
// cannot tell (an environment built before it recorded the inputs).
type WorkspaceDirtyChecker interface {
	ConfigDirty(ctx context.Context, env EnvID) (bool, error)
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

// ResourceSample is one point-in-time reading of an environment's resource
// usage — for the devcontainer adapter, the whole DinD sandbox (inner dockerd
// + workspace + compose siblings), which is exactly "what this run costs the
// host". At is stamped by the caller (the engine's clock), not the adapter.
// Net counters are cumulative since container start; the UI derives rates
// from deltas.
type ResourceSample struct {
	At       time.Time `json:"at"`
	CPUPerc  float64   `json:"cpu_perc"`  // % of one core; ceiling is CPUs×100
	CPUs     int       `json:"cpus"`      // host logical cores (the % denominator); 0 = unknown
	MemUsed  int64     `json:"mem_used"`  // bytes
	MemLimit int64     `json:"mem_limit"` // bytes (host total when uncapped)
	NetRx    int64     `json:"net_rx"`    // cumulative bytes received
	NetTx    int64     `json:"net_tx"`    // cumulative bytes sent
	PIDs     int       `json:"pids"`
}

// ResourceSampler is an OPTIONAL EnvProvisioner capability: one resource
// reading of a live environment (the run-page cpu/ram graphs). Samples
// are external state like EnvStatus — the engine keeps a rolling in-memory
// window, never the event log. An error means "no sample" (a gap in the
// series), which callers must prefer over carrying a stale value forward.
type ResourceSampler interface {
	SampleResources(ctx context.Context, env EnvID) (ResourceSample, error)
}

// ExecLogTailer is an OPTIONAL EnvProvisioner capability: a live tail of
// an environment's combined exec output (bootstrap, commands, agent
// invocations) for run-page feedback. How output is captured is the
// adapter's business; implementations that cannot stream simply don't
// implement the interface and the API degrades honestly.
type ExecLogTailer interface {
	TailExecLog(ctx context.Context, env EnvID, n int) ([]byte, error)
}

// Sidecar is an OPTIONAL EnvProvisioner capability: the per-workspace
// runtime the engine drives over HTTP — the long-lived coding agent (ACP),
// the workspace's files and git state, and terminals. Sidecar starts it if it
// is not already serving (idempotent) and returns a dialer for its socket;
// the HTTP API is internal/workspace/sidecar's. Provisioners whose workspaces cannot
// host it don't implement this, and the features it backs degrade honestly.
type Sidecar interface {
	Sidecar(ctx context.Context, env EnvID) (PortConn, error)
}

// WorkspaceSSH is an OPTIONAL EnvProvisioner capability: a dialer for the
// sidecar's SSH server (ssh.sock beside sidecar.sock in the shim dir), the
// far end of the engine's SSH door — a person's ssh or editor session into
// the workspace. Starting the sidecar first is the adapter's business, as
// for Sidecar.
type WorkspaceSSH interface {
	SSH(ctx context.Context, env EnvID) (PortConn, error)
}

// EngineURLProvider is an OPTIONAL EnvProvisioner capability: the URL this
// env's workspace reaches the engine at (written into run.env as
// APOTHECARY_ENGINE_URL). A provisioner whose workspaces are not on the
// engine's machine supplies its own — a Kubernetes pod reaches the engine's
// Service, not a loopback relay. Absent ⇒ the engine's configured URL.
type EngineURLProvider interface {
	EngineURL(env EnvID) string
}
