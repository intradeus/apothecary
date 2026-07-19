// Package ports holds the engine-shaped vendor boundaries (DESIGN.md §4).
// Ports are shaped by what the engine needs, never by a vendor's API; no
// vendor types cross this boundary. The fakes in internal/adapters/fake are
// the test substrate for every §16 scenario.
package ports

import (
	"context"
	"io/fs"
	"time"
)

type EnvID string

// EnvSpec carries only non-secrets (§11: secrets never pass through Coder
// template parameters — they are written into the env after provisioning).
type EnvSpec struct {
	RunID   string
	RepoURL string
	Branch  string
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
	Kind string // ide | terminal | app
	URL  string
}

type EnvStatus string

const (
	EnvStarting EnvStatus = "starting"
	EnvRunning  EnvStatus = "running"
	EnvStopped  EnvStatus = "stopped"
	EnvAbsent   EnvStatus = "absent"
)

type EnvProvisioner interface {
	// Provision blocks until the environment is ready; 1 internal retry.
	Provision(ctx context.Context, spec EnvSpec) (EnvID, error)
	Exec(ctx context.Context, env EnvID, cmd Command) (ExecResult, error)
	// ReadFile/WriteFile paths: relative = repo-root-relative
	// (artifacts/plan.md), absolute = absolute (/factory/run.env).
	ReadFile(ctx context.Context, env EnvID, path string) ([]byte, error)
	WriteFile(ctx context.Context, env EnvID, path string, data []byte, mode fs.FileMode) error
	Links(ctx context.Context, env EnvID) ([]AccessLink, error)
	Status(ctx context.Context, env EnvID) (EnvStatus, error)
	Destroy(ctx context.Context, env EnvID) error
}

type SessionID string

// AgentSpec is what a harness needs to invoke one agent stage. The agent
// name resolves inside the workspace (harness-native config generated at
// bootstrap from .factory/agents/, §5.2).
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

// RepoRef identifies the one project repo at the forge.
type RepoRef struct {
	URL           string
	DefaultBranch string
	ProjectID     int // forge-side id (GitLab project id)
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
