// Package flow defines the versioned flow document: its schema, parser and
// validator (DESIGN.md §3). Flows are loaded at run creation; the run stores
// the flow name plus the SHA-256 hash of the raw file.
package flow

import "time"

type StageType string

const (
	StageAgent     StageType = "agent"
	StageCommand   StageType = "command"
	StageHumanGate StageType = "human_gate"
	StageForgeMR   StageType = "forge_mr"
)

// Feed selects what content an edge carries back to its target stage.
type Feed string

const (
	FeedOutput   Feed = "output"   // last 8 KB of command output
	FeedComments Feed = "comments" // verdict comments
	FeedFeedback Feed = "feedback" // human text
)

// PingType for human gates. The full ping taxonomy (clarification, blocker,
// permission) belongs to ask_human; gates may only raise these two.
type PingType string

const (
	PingPlanApproval PingType = "plan_approval"
	PingFinalReview  PingType = "final_review"
)

const (
	HarnessOpencode = "opencode"

	AutoCompleteMRMerged = "mr_merged"

	// InputTicket and InputChanged are the two non-path input entries.
	InputTicket  = "ticket"
	InputChanged = "$CHANGED"

	// PayloadMRURL is the one non-path human_gate payload.
	PayloadMRURL = "mr_url"
)

type Defaults struct {
	Timeout  time.Duration // per-stage wall clock; suspended while a ping is pending
	Harness  string
	Model    string // provider/model
	MaxTurns int
}

type Budget struct {
	MaxCostUSD    float64 // sum of Usage.CostUSD across the run; exceeding => abandoned
	MaxTotalTurns int
}

// Edge is an on_fail / on_reject redirect. An exhausted edge abandons the run.
type Edge struct {
	To   string
	Max  int
	Feed Feed // empty = nothing fed
}

// Stage is one validated flow stage. Model, Harness, MaxTurns and Timeout are
// resolved: for agent stages they carry the effective value (stage override or
// flow default); for other types they are zero.
type Stage struct {
	ID   string
	Type StageType

	// agent
	Agent    string
	Model    string
	Harness  string
	MaxTurns int
	Timeout  time.Duration
	Input    []string // ordered: ticket | artifact path | $CHANGED
	Output   string   // artifact the agent MUST produce; "" = none
	Verdict  bool     // agent MUST write artifacts/verdict.json; REJECT triggers OnReject

	// command
	Run    string // literal bash
	RunRef string // key into project.commands

	// human_gate
	Ping         PingType
	Payload      string // artifact path or mr_url
	AutoComplete string // "" or mr_merged

	// forge_mr
	Body string // artifact path used as MR description

	OnFail   *Edge
	OnReject *Edge

	// Line is the stage's line in the source YAML, for error reporting.
	Line int
}

type Flow struct {
	Name        string
	Description string
	Defaults    Defaults
	Budget      Budget
	Stages      []Stage

	// Hash is the SHA-256 (hex) of the raw flow file.
	Hash string
}

// Stage returns the stage with the given id, or nil.
func (f *Flow) Stage(id string) *Stage {
	for i := range f.Stages {
		if f.Stages[i].ID == id {
			return &f.Stages[i]
		}
	}
	return nil
}

// Index returns the position of the stage id in execution order, or -1.
func (f *Flow) Index(id string) int {
	for i := range f.Stages {
		if f.Stages[i].ID == id {
			return i
		}
	}
	return -1
}
