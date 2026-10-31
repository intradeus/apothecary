package event

// The agent transcript (DESIGN: chat). An agent stage runs as a live session
// whose conversation the engine records as it happens: what the engine sent
// (agent_prompted), what the agent said and did (agent_message, agent_tool,
// agent_plan), what the human sent it (message_queued → message_delivered).
//
// These events are COALESCED by the engine before they are appended — one
// event per message segment and per finished tool call, never one per
// streamed token — so the log stays replay-cheap. Token-level progress is
// ephemeral and travels separately (the run stream's "delta" frames). Replay
// ignores the content: only sessions, cursors and the message queue feed the
// run state.

const (
	// TypeAgentSession records the harness session an attempt runs in, as
	// soon as it exists — so a crash mid-invocation can resume (or reattach
	// to) the same session instead of starting the agent over.
	TypeAgentSession  Type = "agent_session"
	TypeAgentPrompted Type = "agent_prompted"
	TypeAgentMessage  Type = "agent_message"
	TypeAgentTool     Type = "agent_tool"
	TypeAgentPlan     Type = "agent_plan"
	TypeAgentNotice   Type = "agent_notice"
	// TypeMessageQueued / TypeMessageDelivered are a holder's message to the
	// agent they took over: accepted, then handed to it at the end of its turn.
	TypeMessageQueued    Type = "message_queued"
	TypeMessageDelivered Type = "message_delivered"
	// TypeHoldTaken / TypeHoldReleased bracket a TAKE-OVER: a human holding
	// the running agent's session. While held, the end of a turn is not the
	// end of the stage — the agent waits for the holder's next message — and
	// the stage can only end once the hold is released, which hands the
	// task back with a fixed prompt. The pair is the timeline's record of the
	// hold: who, how long, how many turns, what it cost.
	TypeHoldTaken    Type = "hold_taken"
	TypeHoldReleased Type = "hold_released"
)

// AgentPrompted kinds: why the engine sent the agent this text.
const (
	PromptStage    = "stage"    // the stage's assembled prompt (a fresh attempt)
	PromptAnswer   = "answer"   // answers to the questions the attempt parked on
	PromptRestart  = "restart"  // re-entry after an engine restart
	PromptMessages = "messages" // human messages carried into the prompt
	PromptHandback = "handback" // the take-over ended: finish the task
)

type AgentSession struct {
	StageID   string `json:"stage_id"`
	Iteration int    `json:"iteration,omitempty"`
	SessionID string `json:"session_id"`
}

type AgentPrompted struct {
	StageID   string `json:"stage_id"`
	Iteration int    `json:"iteration,omitempty"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// AgentMessage is one contiguous segment of the agent's reply (or, Thought,
// of its visible reasoning). Segment is stable across the live deltas that
// preceded it, so a viewer can replace its streamed draft with this.
type AgentMessage struct {
	StageID   string `json:"stage_id"`
	Iteration int    `json:"iteration,omitempty"`
	Turn      string `json:"turn,omitempty"`
	Segment   string `json:"segment"`
	Text      string `json:"text"`
	Thought   bool   `json:"thought,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

// AgentTool is one tool call in its final state (or its last known state,
// when the turn ended before it finished).
type AgentTool struct {
	StageID    string      `json:"stage_id"`
	Iteration  int         `json:"iteration,omitempty"`
	Turn       string      `json:"turn,omitempty"`
	ToolCallID string      `json:"tool_call_id"`
	Title      string      `json:"title,omitempty"`
	Kind       string      `json:"kind,omitempty"`
	Status     string      `json:"status,omitempty"`
	Input      string      `json:"input,omitempty"`
	Output     string      `json:"output,omitempty"`
	Locations  []string    `json:"locations,omitempty"`
	Diffs      []AgentDiff `json:"diffs,omitempty"`
	Cursor     string      `json:"cursor,omitempty"`
}

type AgentDiff struct {
	Path    string `json:"path"`
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text"`
}

type AgentPlan struct {
	StageID   string      `json:"stage_id"`
	Iteration int         `json:"iteration,omitempty"`
	Turn      string      `json:"turn,omitempty"`
	Entries   []PlanEntry `json:"entries"`
	Cursor    string      `json:"cursor,omitempty"`
}

type PlanEntry struct {
	Content  string `json:"content"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// AgentNotice is something about the agent session itself rather than its
// work: the process exited, a permission was auto-approved, a turn errored.
type AgentNotice struct {
	StageID   string `json:"stage_id"`
	Iteration int    `json:"iteration,omitempty"`
	Text      string `json:"text"`
	Cursor    string `json:"cursor,omitempty"`
}

type MessageQueued struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
	StageID   string `json:"stage_id,omitempty"`
}

type MessageDelivered struct {
	MessageIDs []string `json:"message_ids"`
	StageID    string   `json:"stage_id,omitempty"`
	Iteration  int      `json:"iteration,omitempty"`
}

// HoldTaken: a human took over the agent stage's live session. From here
// the agent's turns end into a wait for the holder, not into the stage's
// output check.
type HoldTaken struct {
	StageID   string `json:"stage_id"`
	Iteration int    `json:"iteration,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

// HoldReleased closes a hold with its account: how long it lasted (from
// hold_taken to here), and what the holder's turns spent — every leg of it,
// across any park. Reason says how it ended: the holder handing the task
// back (the handback prompt follows), or the attempt ending underneath it
// (a harness error, a timeout, an abort).
type HoldReleased struct {
	StageID    string `json:"stage_id"`
	Iteration  int    `json:"iteration,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	Reason     string `json:"reason"` // released|stage_ended
	DurationMS int64  `json:"duration_ms"`
	HoldSpend
}

// HoldSpend is what a hold's turns cost: the turns the holder's messages
// prompted, the messages themselves, and their token/cost usage.
type HoldSpend struct {
	Turns    int    `json:"turns,omitempty"`
	Messages int    `json:"messages,omitempty"`
	Usage    *Usage `json:"usage,omitempty"`
}

// Add folds another leg's spend in.
func (s *HoldSpend) Add(o HoldSpend) {
	s.Turns += o.Turns
	s.Messages += o.Messages
	if o.Usage != nil {
		if s.Usage == nil {
			s.Usage = &Usage{}
		}
		s.Usage.InputTokens += o.Usage.InputTokens
		s.Usage.OutputTokens += o.Usage.OutputTokens
		s.Usage.CostUSD += o.Usage.CostUSD
	}
}

// HoldReleased reasons.
const (
	HoldReleasedByUser = "released"
	HoldEndedWithStage = "stage_ended"
)

func (*AgentSession) EventType() Type     { return TypeAgentSession }
func (*AgentPrompted) EventType() Type    { return TypeAgentPrompted }
func (*AgentMessage) EventType() Type     { return TypeAgentMessage }
func (*AgentTool) EventType() Type        { return TypeAgentTool }
func (*AgentPlan) EventType() Type        { return TypeAgentPlan }
func (*AgentNotice) EventType() Type      { return TypeAgentNotice }
func (*MessageQueued) EventType() Type    { return TypeMessageQueued }
func (*MessageDelivered) EventType() Type { return TypeMessageDelivered }
func (*HoldTaken) EventType() Type        { return TypeHoldTaken }
func (*HoldReleased) EventType() Type     { return TypeHoldReleased }

func init() {
	registry[TypeAgentSession] = func() Payload { return &AgentSession{} }
	registry[TypeAgentPrompted] = func() Payload { return &AgentPrompted{} }
	registry[TypeAgentMessage] = func() Payload { return &AgentMessage{} }
	registry[TypeAgentTool] = func() Payload { return &AgentTool{} }
	registry[TypeAgentPlan] = func() Payload { return &AgentPlan{} }
	registry[TypeAgentNotice] = func() Payload { return &AgentNotice{} }
	registry[TypeMessageQueued] = func() Payload { return &MessageQueued{} }
	registry[TypeMessageDelivered] = func() Payload { return &MessageDelivered{} }
	registry[TypeHoldTaken] = func() Payload { return &HoldTaken{} }
	registry[TypeHoldReleased] = func() Payload { return &HoldReleased{} }
}
