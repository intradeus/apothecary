// Package event defines the factory's event catalog (DESIGN.md §6) and the
// append-only store. Events are the source of truth: every transition is an
// event; projections and analytics are derived, never authoritative.
package event

import (
	"time"
)

type Type string

const (
	TypeRunCreated      Type = "run_created"
	TypeEnvRequested    Type = "env_requested"
	TypeImageBuilt      Type = "image_built" // DESIGN2 §6.3: pipeline ran between env_requested and env_ready
	TypeEnvReady        Type = "env_ready"
	TypeEnvFailed       Type = "env_failed"
	TypeStageStarted    Type = "stage_started"
	TypeStageCompleted  Type = "stage_completed"
	TypeStageFailed     Type = "stage_failed"
	TypeEdgeTaken       Type = "edge_taken"
	TypePingRaised      Type = "ping_raised"
	TypePingAnswered    Type = "ping_answered"
	TypeVerdictReceived Type = "verdict_received"
	TypeMRCreated       Type = "mr_created"
	TypeRunCompleted    Type = "run_completed"
	TypeRunAbandoned    Type = "run_abandoned"
	TypeRunFailed       Type = "run_failed"
	TypeRunArchived     Type = "run_archived" // addition over §6 (see IMPLEMENTATION_NOTES): archived is a projected state, so it needs an event
	TypeRunResumed      Type = "run_resumed"  // "try again": re-enter a failed stage in the same workspace
	TypeRunPaused       Type = "run_paused"   // user pressed pause: loop exited cleanly, run parked non-terminal
	TypeRunUnpaused     Type = "run_unpaused" // user pressed play: loop restarts and re-enters via the recovery path
	TypeEnvDestroyed    Type = "env_destroyed"
	// Rebuild recreates a run's container in place (keeps the run + checkout);
	// orthogonal to the run's lifecycle State — these never change it.
	TypeEnvRebuildStarted   Type = "env_rebuild_started"
	TypeEnvRebuildCompleted Type = "env_rebuild_completed"
	TypeEnvRebuildFailed    Type = "env_rebuild_failed"
)

// Payload is implemented by every event payload struct.
type Payload interface {
	EventType() Type
}

// Event is the envelope persisted per row: (run_id, seq) unique, seq
// monotonic per run, appended only by the run's single writer goroutine.
type Event struct {
	RunID   string    `json:"run_id"`
	Seq     int64     `json:"seq"`
	TS      time.Time `json:"ts"`
	Type    Type      `json:"type"`
	Payload Payload   `json:"payload"`
}

// StageFailed reasons (§6).
const (
	ReasonExitNonzero    = "exit_nonzero"
	ReasonTimeout        = "timeout"
	ReasonHarnessError   = "harness_error"
	ReasonOutputContract = "output_contract"
	ReasonInfra          = "infra"
)

type Link struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
	// Command is the copy-paste form for non-browser access (ssh).
	Command string `json:"command,omitempty"`
}

type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type RunCreated struct {
	TicketTitle string `json:"ticket_title"`
	TicketGoal  string `json:"ticket_goal"`
	FlowName    string `json:"flow_name"`
	FlowHash    string `json:"flow_hash"`
	RepoURL     string `json:"repo_url"`
	Branch      string `json:"branch"`
	// SourceBranch is the branch this run forks off from — the base of the
	// `git switch -c <Branch> origin/<SourceBranch>` at bootstrap. Per-run
	// (each attempt can start from a different source); empty on pre-source
	// runs, where bootstrap falls back to the project's default branch.
	SourceBranch string `json:"source_branch,omitempty"`
	// ProjectID scopes the run to its project (DESIGN2 §6.3); absent in
	// pre-v2 events, projected as "".
	ProjectID string `json:"project_id,omitempty"`
	// TicketID links the run to its parent ticket (a ticket owns many runs);
	// absent on pre-ticket runs and on direct/test runs, projected as "".
	TicketID string `json:"ticket_id,omitempty"`
	// BranchBase is the ticket's branch stem before the per-run suffix
	// (Branch = BranchBase + "-" + run-id tail). Kept so restart and the
	// workspace name can derive from the stem without re-parsing Branch.
	BranchBase string `json:"branch_base,omitempty"`
	// RunGoal is extra direction for THIS attempt ("last try broke the
	// router — use the existing middleware"), rendered as its own prompt
	// section. Additive: it never replaces the ticket's goal.
	RunGoal string `json:"run_goal,omitempty"`
	// TicketArtifactIDs snapshots the parent ticket's attachments at run
	// start, and pins which ones this attempt saw even if the ticket changes
	// later.
	//
	// There is deliberately NO run-side equivalent: the run's OWN attachments
	// are linked in the artifacts table (run_id=<run>, scope=goal) and found
	// via ForRun, so listing their ids here would be duplicate state that can
	// drift. A TICKET's attachments are owned by the ticket (run_id empty,
	// scope=ticket_goal) so they can reach EVERY run — which is exactly why
	// ForRun cannot see them, and why their ids must ride on the event.
	TicketArtifactIDs []string `json:"ticket_artifact_ids,omitempty"`
}

type EnvRequested struct{}

// ImageBuilt records the §8 image pipeline outcome for a run's workspace
// image; cached=true means the content-addressed tag already existed.
type ImageBuilt struct {
	Tag    string `json:"tag"`
	Cached bool   `json:"cached"`
}

type EnvReady struct {
	EnvID string `json:"env_id"`
	Links []Link `json:"links"`
}

type EnvFailed struct {
	Error string `json:"error"`
	// EnvID is set when a workspace was provisioned before the failure
	// (bootstrap errors): capacity counting, archive and the reaper need it
	// to see — and destroy — the orphan.
	EnvID string `json:"env_id,omitempty"`
}

type StageStarted struct {
	StageID   string `json:"stage_id"`
	Iteration int    `json:"iteration"`
	Harness   string `json:"harness,omitempty"`
	Model     string `json:"model,omitempty"`
	// Agent/AgentHash pin the agent-library entry and its exact content
	// version (DESIGN3 §1): outcome events become joinable to the prompt
	// version that produced them.
	Agent     string `json:"agent,omitempty"`
	AgentHash string `json:"agent_hash,omitempty"`
}

type StageCompleted struct {
	StageID    string `json:"stage_id"`
	DurationMS int64  `json:"duration_ms"`
	TurnsUsed  int    `json:"turns_used,omitempty"`
	Usage      *Usage `json:"usage,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
}

type StageFailed struct {
	StageID  string `json:"stage_id"`
	Reason   string `json:"reason"` // exit_nonzero|timeout|harness_error|output_contract|infra
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

type EdgeTaken struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Edge  string `json:"edge"` // on_fail|on_reject|on_timeout
	Count int    `json:"count"`
	Max   int    `json:"max"`
	// Depth is the chain link taken (0 = the edge itself, 1+ = its then
	// fallbacks); each link keeps its own counter across replay. Absent in
	// pre-chain events, which were all depth 0.
	Depth int `json:"depth,omitempty"`
	// Feed is the content carried to the target stage (≤8 KB tail),
	// persisted so crash recovery never loses feedback. Extension over the
	// §6 catalog — see IMPLEMENTATION_NOTES.
	Feed string `json:"feed,omitempty"`
}

type PingRaised struct {
	PingID     string `json:"ping_id"`
	Type       string `json:"type"` // plan_approval|final_review|clarification|blocker|permission
	Question   string `json:"question"`
	Context    string `json:"context,omitempty"` // §9 pings.context; persisted so recovery keeps it
	PayloadRef string `json:"payload_ref,omitempty"`
	// PayloadRefs is the FULL set of workspace files the agent attached
	// (ask_human multi-artifact). PayloadRef stays the PRIMARY ref (first, or
	// a URL like mr_url) for back-compat and the review-vs-link decision;
	// PayloadRefs is nil on pre-multi pings.
	PayloadRefs []string `json:"payload_refs,omitempty"`
	// StageID is the flow stage in flight when the ping was raised
	// (PLAN4 M15): the attribution key joining reviews to stages and,
	// via stage_started.agent_hash, to prompt versions.
	StageID string `json:"stage_id,omitempty"`
	// Options are the agent-declared answer choices (PLAN4 M16): when
	// non-empty, the answer MUST be one of them — the agent asked a closed
	// question so it can branch deterministically — or the reserved "other"
	// escape hatch (system-provided, never declared; the feedback then
	// carries the human's actual answer).
	Options []string `json:"options,omitempty"`
}

type PingAnswered struct {
	PingID   string `json:"ping_id"`
	Answer   string `json:"answer"` // approve|reject|text
	Feedback string `json:"feedback,omitempty"`
}

type VerdictReceived struct {
	StageID   string `json:"stage_id"`
	Approve   bool   `json:"approve"`
	NComments int    `json:"n_comments"`
}

type MRCreated struct {
	MRURL  string `json:"mr_url"`
	MRIID  int    `json:"mr_iid"`
	Branch string `json:"branch"`
}

type RunCompleted struct {
	DurationMS    int64   `json:"duration_ms"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	TotalTurns    int     `json:"total_turns"`
	UnwantedPings int     `json:"unwanted_pings"`
}

type RunAbandoned struct {
	StalledAt string `json:"stalled_at"`
	Edge      string `json:"edge,omitempty"`
	Reason    string `json:"reason"`
}

type RunFailed struct {
	Reason string `json:"reason"`
}

type RunArchived struct{}

// RunResumed un-terminals an abandoned/failed run and re-enters StageID
// (the "try again" path): the workspace and prior stages are reused, only
// the failed stage re-runs, with its retry budgets reset. History stays
// immutable — this is a forward event, not a rewind.
type RunResumed struct {
	StageID string `json:"stage_id"`
}

// RunPaused parks a run: the loop goroutine exits cleanly (a controlled
// crash) and the run stays non-terminal but is NOT recovered on boot.
// StageID is the stage in flight when pause hit, informational only —
// replay keeps the stage events' own record of where to re-enter.
type RunPaused struct {
	StageID string `json:"stage_id,omitempty"`
}

// RunUnpaused (play) restarts a paused run's loop; it re-enters mid-stage
// exactly like crash recovery (recorded harness sessions resume, commands
// re-execute, gates re-arm). Unlike RunResumed it resets NO retry budgets —
// the run continues, it does not start the stage's accounting over.
// Waiting is set when the run still has an open ping at play time (a
// paused gate): re-arming appends no new ping_raised, so the projection
// needs it to land back on waiting_on_human instead of running.
type RunUnpaused struct {
	Waiting bool `json:"waiting,omitempty"`
}

type EnvDestroyed struct {
	EnvID string `json:"env_id"`
}

type EnvRebuildStarted struct {
	EnvID string `json:"env_id"`
}

type EnvRebuildCompleted struct {
	EnvID string `json:"env_id"`
}

type EnvRebuildFailed struct {
	EnvID string `json:"env_id"`
	Error string `json:"error"`
}

func (*RunCreated) EventType() Type      { return TypeRunCreated }
func (*EnvRequested) EventType() Type    { return TypeEnvRequested }
func (*ImageBuilt) EventType() Type      { return TypeImageBuilt }
func (*EnvReady) EventType() Type        { return TypeEnvReady }
func (*EnvFailed) EventType() Type       { return TypeEnvFailed }
func (*StageStarted) EventType() Type    { return TypeStageStarted }
func (*StageCompleted) EventType() Type  { return TypeStageCompleted }
func (*StageFailed) EventType() Type     { return TypeStageFailed }
func (*EdgeTaken) EventType() Type       { return TypeEdgeTaken }
func (*PingRaised) EventType() Type      { return TypePingRaised }
func (*PingAnswered) EventType() Type    { return TypePingAnswered }
func (*VerdictReceived) EventType() Type { return TypeVerdictReceived }
func (*MRCreated) EventType() Type       { return TypeMRCreated }
func (*RunCompleted) EventType() Type    { return TypeRunCompleted }
func (*RunAbandoned) EventType() Type    { return TypeRunAbandoned }
func (*RunFailed) EventType() Type       { return TypeRunFailed }
func (*RunArchived) EventType() Type     { return TypeRunArchived }
func (*RunResumed) EventType() Type      { return TypeRunResumed }
func (*RunPaused) EventType() Type       { return TypeRunPaused }
func (*RunUnpaused) EventType() Type     { return TypeRunUnpaused }
func (*EnvDestroyed) EventType() Type    { return TypeEnvDestroyed }

func (*EnvRebuildStarted) EventType() Type   { return TypeEnvRebuildStarted }
func (*EnvRebuildCompleted) EventType() Type { return TypeEnvRebuildCompleted }
func (*EnvRebuildFailed) EventType() Type    { return TypeEnvRebuildFailed }
