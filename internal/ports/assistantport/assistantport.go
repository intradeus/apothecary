// Package assistantport is how the assistant (internal/assistant) reaches a
// person's assistant pod (docs/assistant-plan.md): bring it up, reach the
// agent host in it, start a confined session, and bring that person's
// repositories up to date. The pod's host is an adapter's
// (internal/adapters/assistant); this is all the conversation sees of it.
package assistantport

import (
	"context"
	"errors"
	"net"
	"time"
)

// ErrNoHost: this deployment runs no assistant pods (not on Kubernetes, or
// turned off).
var ErrNoHost = errors.New("the assistant needs a Kubernetes deployment with it turned on")

// ErrNoLandlock: the node's kernel cannot confine a session, so none starts
// (decision 8: never an unconfined one).
var ErrNoLandlock = errors.New("the assistant's node cannot confine a session (Landlock is off or missing in its kernel)")

// Dialer opens one connection to the pod's agent host.
type Dialer interface {
	Dial(ctx context.Context) (net.Conn, error)
}

// Repo is one repository a session may read: a workshop's, by its name in
// the workshop. URL is what the pod's git container clones.
type Repo struct {
	Workshop     string // the workshop's id
	WorkshopName string // its name, for the folder
	Name         string // the repository's name in the workshop, the folder's last part
	URL          string
}

// Credential reaches the repository host during a fetch, and nothing else.
type Credential struct {
	User, Pass string
}

// FetchItem is one repository to bring up to date, with the credential its
// host takes (zero: a public repository). Each credential goes to its own
// repository's host only.
type FetchItem struct {
	Repo Repo
	Cred Credential
}

// AgentHost is how the engine reaches a pod's agent host: a dialer, and the
// token every request carries (the sessions in the pod share its user; the
// token is what keeps them off its API).
type AgentHost struct {
	Dialer Dialer
	Token  string
}

// AgentHostTokenHeader is the header the agent host's token rides.
const AgentHostTokenHeader = "X-Apothecary-Agent-Host"

// RepoState is one repository's folder in the pod, as the last fetch left it.
type RepoState struct {
	Workshop string    `json:"workshop"`
	Name     string    `json:"name"`
	Folder   string    `json:"folder"`
	OK       bool      `json:"ok"`
	Head     string    `json:"head,omitempty"`
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at"`
}

// PodState is where a person's pod is in its life.
type PodState string

const (
	PodAbsent   PodState = "absent"
	PodStarting PodState = "starting"
	PodRunning  PodState = "running"
)

// Status is a person's pod as the panel shows it.
type Status struct {
	State PodState `json:"state"`
	// Error is why the last start failed, when it did.
	Error string `json:"error,omitempty"`
}

// Session is what a confined agent process is started with: its id (its
// state folder's name), the repositories it reads, the harness's command
// line and environment, and its working directory.
type Session struct {
	ID    string
	Repos []Repo
	// Argv is the harness's ACP command, absolute; Exec the other programs
	// it runs (its own helpers). The pod adds git, ripgrep, the shim and the
	// loader.
	Argv []string
	Exec []string
	Env  map[string]string
}

// Rules are a confined process's Landlock rules (the shim's confine spec):
// what it reads, writes, runs, and which devices it opens.
type Rules struct {
	Read    []string `json:"read,omitempty"`
	Write   []string `json:"write,omitempty"`
	Exec    []string `json:"exec,omitempty"`
	Devices []string `json:"devices,omitempty"`
}

// Start is the agent host's start request (POST /agent/start) for one
// confined session.
type Start struct {
	Argv    []string          `json:"argv"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	Confine *Rules            `json:"confine,omitempty"`
}

// Pods is a person's assistant pod.
type Pods interface {
	// Ensure brings the person's pod up, its gateway's standing allow list
	// set to hosts (the model providers'), and returns once its agent host
	// answers. Idempotent; a pod on a stale image is recreated when nobody
	// uses it.
	Ensure(ctx context.Context, person string, hosts []string) error
	// Status is the pod's state, without starting it.
	Status(ctx context.Context, person string) (Status, error)
	// AgentHost reaches the pod's agent host (the shim sidecar's API).
	AgentHost(ctx context.Context, person string) (AgentHost, error)
	// StartRequest is the agent host's start request for a confined
	// session: the harness under the session's rules, its home the
	// session's state folder.
	StartRequest(person string, s Session) (Start, error)
	// Home is a session's home: its state folder in the pod.
	Home(sessionID string) string
	// Folder is where a repository lives in the pod.
	Folder(r Repo) string
	// Fetch brings the repositories up to date in the pod's git container,
	// the repository hosts reachable through the gateway for its length and
	// each credential in memory for its repository alone.
	Fetch(ctx context.Context, person string, items []FetchItem) ([]RepoState, error)
	// WriteFile writes a file into a session's state folder (the harness's
	// configuration and auth), as the agent host's user.
	WriteFile(ctx context.Context, person, path string, data []byte, mode uint32) error
	// ReadFile reads a file in the pod, as the agent host's user.
	ReadFile(ctx context.Context, person, path string) ([]byte, error)
}
