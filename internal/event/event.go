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
	TypeEnvDestroyed    Type = "env_destroyed"
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
	// ProjectID scopes the run to its project (DESIGN2 §6.3); absent in
	// pre-v2 events, projected as "".
	ProjectID string `json:"project_id,omitempty"`
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
	Edge  string `json:"edge"` // on_fail|on_reject
	Count int    `json:"count"`
	Max   int    `json:"max"`
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
	// StageID is the flow stage in flight when the ping was raised
	// (PLAN4 M15): the attribution key joining reviews to stages and,
	// via stage_started.agent_hash, to prompt versions.
	StageID string `json:"stage_id,omitempty"`
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

type EnvDestroyed struct {
	EnvID string `json:"env_id"`
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
func (*EnvDestroyed) EventType() Type    { return TypeEnvDestroyed }
