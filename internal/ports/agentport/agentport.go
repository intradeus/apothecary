// Package agentport is the harness port: running an agent in a workspace —
// a session started or resumed with a prompt, its normalized event stream,
// the messages a person sends mid-turn, and what it cost — plus the optional
// capabilities a harness may offer (reattaching after a restart, listing the
// models it can reach, stopping a turn, promising a read-only session).
package agentport

import (
	"context"

	"apothecary/internal/ports/envport"
)

type SessionID string

// AgentSpec is what a harness needs to invoke one agent stage. The agent
// name resolves inside the workspace: harness-native config is generated at
// bootstrap from the flow document's own agent declarations (FLOWLANG v3 —
// a flow carries its agents; there is no repo-side agents directory).
type AgentSpec struct {
	Agent    string
	Harness  string
	Model    string // provider/model
	MaxTurns int
}

type AgentInput struct {
	Prompt string // assembled per §3.2 (Run), or the reply to deliver (Resume)
	// Events receives the invocation's transcript AS IT HAPPENS — message
	// text, tool calls, plans, turn boundaries. nil = nobody is listening.
	// Called from the runner's goroutine; implementations must not block for
	// long (the engine buffers).
	Events func(AgentEvent)
	// MCPServers are the MCP servers the stage's agent may call (its `mcp:`
	// list, FLOWLANG §1), resolved for this run: secrets filled, the builtin
	// browser turned into its workspace command. A runner registers them on
	// the session it opens or loads — so they are the agent's, not the
	// workspace's — and must never log or persist them.
	MCPServers []MCPServer
	// Inbox holds the human's messages for this invocation. A runner that can
	// deliver them mid-invocation takes them at the end of each turn; one
	// that cannot simply never takes them, and the engine carries them into
	// the next prompt.
	// nil = no messages.
	Inbox Inbox
	// ReadOnly: the session must not change the workspace (the Ask pane's
	// session, beside a stage's). A runner that cannot promise it says so
	// (ReadOnlyCapable) and is never asked.
	ReadOnly bool
	// Model, on a Resume, switches the session to this model before the
	// prompt; "" keeps the session's.
	Model string
}

// ReadOnlyCapable is an OPTIONAL AgentRunner capability: whether it can run
// a session that cannot change the workspace (AgentInput.ReadOnly).
type ReadOnlyCapable interface {
	CanReadOnly() bool
}

// MCPServer is one MCP server an agent session can call: a local command
// speaking MCP on stdio in the workspace (Command), or a remote server over
// streamable HTTP (URL). Values are final — a harness passes them through.
type MCPServer struct {
	Name    string
	Command []string
	URL     string
	Env     map[string]string
	Headers map[string]string
}

// UserMessage is one message a human sent to a running agent.
type UserMessage struct {
	ID   string
	Text string
	// Verbatim marks text the engine composed (the handback prompt that
	// ends a take-over): delivered as it is, not framed as a human's words.
	Verbatim bool
}

// Inbox is the engine-owned queue of human messages for one invocation.
type Inbox interface {
	// Wake is signalled (buffered, coalescing) whenever a message is queued
	// — and when a hold ends.
	Wake() <-chan struct{}
	// Take removes and returns every queued message: taking IS delivering,
	// so a runner takes only what it is about to send.
	Take() []UserMessage
	// Held reports whether a human holds the session (a take-over). While
	// held, a turn that ends with nothing queued is not the invocation's
	// end: the runner waits for the holder's next message (Wake) and keeps
	// going in the same session. The hold ends with a Wake and Held false.
	Held() bool
}

// AgentEvent kinds.
const (
	AgentEvMessage     = "message"      // Text: a chunk of the agent's reply
	AgentEvThought     = "thought"      // Text: a chunk of visible reasoning
	AgentEvTool        = "tool"         // Tool: a tool call, created or updated
	AgentEvPlan        = "plan"         // Plan: the agent's current todo list
	AgentEvPermission  = "permission"   // Tool + Text: a permission request and its outcome
	AgentEvTurnStart   = "turn_start"   // Turn
	AgentEvTurnEnd     = "turn_end"     // Turn, StopReason, Error
	AgentEvUserMessage = "user_message" // Messages: what was delivered to the agent, Text: as sent
	AgentEvNotice      = "notice"       // Text: something about the agent process itself (it exited, a gap)
	AgentEvSession     = "session"      // Text: the session id, as soon as it exists
)

// AgentEvent is one normalized fact about a running agent. Cursor is an
// opaque position in the runner's own stream: handed back to Reattach after
// an engine restart, it resumes the stream right after this event.
type AgentEvent struct {
	Kind       string
	Turn       string
	Text       string
	Tool       *AgentTool
	Plan       []AgentPlanEntry
	StopReason string
	Error      string
	Messages   []UserMessage
	Cursor     string
	// Usage, on a turn_end, is what that one turn spent — so the engine can
	// account for the turns a take-over prompted.
	Usage *Usage
}

type AgentTool struct {
	ID        string
	Title     string
	Kind      string // read|edit|delete|move|search|execute|think|fetch|other
	Status    string // pending|in_progress|completed|failed
	Input     string // raw input, JSON (truncated)
	Output    string // text output (truncated)
	Locations []string
	Diffs     []AgentDiff
}

type AgentDiff struct {
	Path    string
	OldText string
	NewText string
}

type AgentPlanEntry struct {
	Content  string
	Status   string // pending|in_progress|completed
	Priority string
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// AgentResult is what a harness reports about one agent invocation. Note what
// is absent: a verdict. A verdict is a declared output file (FLOWLANG §4), and
// the engine reads it from the path the flow bound — one source of truth, so a
// harness cannot report APPROVE over a file that says REJECT.
type AgentResult struct {
	Session   SessionID
	Output    string
	TurnsUsed int
	Usage     Usage
}

type AgentRunner interface {
	Run(ctx context.Context, env envport.EnvID, spec AgentSpec, in AgentInput) (AgentResult, error)
	// Resume continues session s with in.Prompt as the next message.
	Resume(ctx context.Context, env envport.EnvID, s SessionID, in AgentInput) (AgentResult, error)
}

// AgentReattacher is an OPTIONAL AgentRunner capability: after an engine
// restart, pick up a turn that is STILL RUNNING in session s (the agent
// process outlived the engine) instead of resuming it with a new message.
// cursor is the last AgentEvent.Cursor the engine recorded ("" = none). ok is
// false when nothing is in flight for s — the caller then resumes normally.
type AgentReattacher interface {
	// Reattachable reports, quickly, whether Reattach has something to
	// follow: a turn of s still running, or one that finished after cursor
	// while nobody was listening.
	Reattachable(ctx context.Context, env envport.EnvID, s SessionID, cursor string) bool
	Reattach(ctx context.Context, env envport.EnvID, s SessionID, cursor string, in AgentInput) (res AgentResult, ok bool, err error)
}

// AgentModelLister is an OPTIONAL AgentRunner capability: ask the agent IN
// the environment which models it will accept — the authoritative list, as
// opposed to what the provider serves (the installed harness version may not
// know a model the provider still offers). Ids are the agent's own spelling.
type AgentModelLister interface {
	AgentModels(ctx context.Context, env envport.EnvID) ([]string, error)
}

// AgentStopper is an OPTIONAL AgentRunner capability: stop whatever agent
// process the runner keeps alive in the environment (a run that ended gives
// up its credentials, and a live agent holds them in memory).
type AgentStopper interface {
	StopAgent(ctx context.Context, env envport.EnvID) error
}
