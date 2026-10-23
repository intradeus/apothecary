// Package flow defines the versioned flow document: its schema, parser and
// validator — the flow language, specified in docs/flow-language.md and cited
// as FLOWLANG §n. Flows are loaded at run creation; the run stores the flow
// name plus the SHA-256 hash of the raw file.
package flow

import (
	"strings"
	"time"

	"apothecary/internal/agentspec"
	"apothecary/internal/signalspec"
)

// CurrentVersion is the grammar this build speaks. Every document declares
// its version, so a break gets a clean refusal ("this build speaks version
// 4") instead of mis-parsing an unfamiliar shape.
//
// v4 made every phase an equal chain opened by a wait point (`on:`), with
// explicit entrypoints (`on: run_start`) in place of a positional entry, and
// moved the flow's settings into one `parameters:` block a project binds.
// There is no legacy mapping: a document of any other version is refused.
const CurrentVersion = 4

type StageType string

const (
	StageAgent     StageType = "agent"
	StageCommand   StageType = "command"
	StageHumanGate StageType = "human_gate"
	// StageAction invokes one of apothecary's built-in integrations
	// (actionspec): create the merge request, and whatever follows.
	StageAction StageType = "action"
	// StageWait parks the phase until an external signal holds (signalspec).
	StageWait StageType = "wait"
)

const (
	// Sentinels are the engine-provided input values (FLOWLANG §4). The rule
	// is lexical: a $NAME input is resolved by the engine at stage time,
	// anything else is a repo-relative artifact path. Adding one costs a
	// const here plus one resolution case in the engine.
	SentinelChanged     = "$CHANGED"     // changed-file list of the branch vs its base
	SentinelUncommitted = "$UNCOMMITTED" // the working tree's pending changes (git status)
	SentinelMRURL       = "$MR_URL"      // the run's merge-request link (create_mr's output)
	SentinelWorkspace   = "$WORKSPACE"   // the workspace's access link (terminal/IDE)
	// SentinelDecision and SentinelFailure carry what the edge that re-entered
	// this stage brought with it: a rejection's rationale (verdict comments or
	// a human's gate feedback) and a failing stage's output tail. They are
	// bound like any other input, so the retry channel is declared rather than
	// arriving invisibly — and an agent can tell "you were rejected" apart
	// from "the tests broke". Empty on a stage's first attempt, so a binding
	// to one must be an optional input.
	SentinelDecision = "$DECISION"
	SentinelFailure  = "$FAILURE"
)

// FeedSentinel is which sentinel an edge kind's carried content fills. It is
// derived, not declared: on_reject can only carry a decision, on_fail and
// on_timeout can only carry the failure's output — so an edge says where to go
// and the target says where the content lands, each exactly once.
func FeedSentinel(edgeKind string) string {
	if edgeKind == EdgeReject {
		return SentinelDecision
	}
	return SentinelFailure
}

// IsFeedSentinel reports whether v is one of the edge-carried values.
func IsFeedSentinel(v string) bool { return v == SentinelDecision || v == SentinelFailure }

// IsSentinel reports whether v is an engine-provided value rather than a
// repo-relative artifact path.
func IsSentinel(v string) bool { return strings.HasPrefix(v, "$") }

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

// Edge kinds (DESIGN3 §2.1). on_success is not an Edge — it is a plain
// target (Stage.OnSuccess) with no max/feed; loops are only expressible
// through kinds that carry max. on_timeout is raised instead of on_fail
// when a stage times out AND the stage declares it; absent, timeouts fall
// back to on_fail (purely additive).
const (
	EdgeFail    = "on_fail"
	EdgeReject  = "on_reject"
	EdgeTimeout = "on_timeout"
)

// TerminalDone is the reserved target that finishes the PHASE: on_success's
// natural end, and an edge's for a stage whose failure is not worth routing
// anywhere ("best effort"). TerminalAbort is the reserved EDGE target that
// deliberately stops the run (projected as abandoned, mirroring the
// frontend's Abort verb) — the explicit form of "no edge / edge exhausted".
// ReservedStart is the graph editor's marker key ("start" is never a routing
// target). ReservedDebug is the debug leaf: a reserved EDGE target that is
// NOT a terminal — the run parks for a human who has the workspace, and
// continuing re-enters the stage that took the link (abandoning ends the
// run). Stage ids may shadow none of them.
const (
	TerminalDone  = "done"
	TerminalAbort = "abort"
	ReservedStart = "start"
	ReservedDebug = "debug"
)

// ShorthandPhase is the phase a document written with top-level stages
// desugars to: one chain opened by run_start.
const ShorthandPhase = "main"

// UnlimitedMax marks a link that never exhausts (max: -1). It is only
// valid when a human_gate sits on the link — as its TARGET (each firing
// parks the run on a human decision; reject aborts) or as its SOURCE (the
// link only fires when a human rejects the gate). Either way the loop is
// bounded by human attention. Links between agents/commands must carry a
// finite max — flow budgets cannot bound cost-free retries (a failing
// command consumes no turns and no dollars; a broken harness can fail in
// milliseconds). The debug leaf is unlimited by nature and takes no max.
// On a phase's wait point it means "no bound on the phase's runs".
const UnlimitedMax = -1

// Edge is a recoverable redirect (on_fail / on_reject / on_timeout). Each
// link carries its own cumulative max; when a link is exhausted the NEXT
// failure falls through to Then (retry-then-escalate chains). An exhausted
// chain abandons the run. To may be TerminalDone: the phase ends there,
// TerminalAbort: taking that link stops the run deliberately, or
// ReservedDebug: the run parks for a human and comes back to this stage on
// continue (none of the three takes max/then — there is no target stage).
//
// An edge does not say what it carries: the content it brings is always
// available to its target as $DECISION or $FAILURE (FeedSentinel), and the
// target decides whether to bind it.
type Edge struct {
	To   string
	Max  int   // >= 1, or UnlimitedMax on human_gate targets
	Then *Edge // fallback link once this one is exhausted; nil = abandon
}

// IOBinding is one input or output binding on a stage (FLOWLANG §4). Every
// stage type uses the same form — a name and a value — so there is one thing to
// learn and one thing to render. The value is a repo-relative artifact path or
// a sentinel; the name is checked against a contract where one exists (an
// agent's declared I/O, an action's signature) and is otherwise a free label
// that documents the file's role.
type IOBinding struct {
	Name  string
	Value string
}

// Stage is one validated flow stage. Model, Harness, MaxTurns and Timeout are
// resolved: for agent stages they carry the effective value (stage override or
// flow default); for other types they are zero.
type Stage struct {
	ID   string
	Type StageType
	// Description is an optional human note on what this stage is for.
	// Documentation only — it reaches no prompt and no decision.
	Description string

	// agent
	Agent    string
	Model    string
	Harness  string
	MaxTurns int
	Timeout  time.Duration

	// Inputs and Outputs are the stage's declared consumption and production
	// (agent, command and action stages). Every path here is visible to the
	// validator, the editor's dataflow view and — for inputs the engine can
	// check — enforced before or after the stage runs.
	Inputs  []IOBinding
	Outputs []IOBinding

	// command
	Run    string // literal bash
	RunRef string // key into project.commands
	// EachRepo runs the script once per checkout of a multi-repo run (in
	// each checkout's root, stopping at the first failure) instead of once
	// in the primary. What `git commit` and `git push` want.
	EachRepo bool

	// human_gate
	//
	// Question is the free text the human is asked; empty falls back to the
	// stage id. Gates are one machine — approve follows OnSuccess, reject
	// follows the on_reject edge — so intent lives in this text rather than in
	// an enum the engine would have to interpret. What the human is shown is
	// the stage's single Inputs entry.
	Question string
	// AutoApprove and AutoReject are signalspec slugs that answer this gate on
	// the human's behalf when they fire: "they merged it instead of clicking
	// approve", "they closed it, which is a no". A gate is a decision point
	// whose default answerer is a human; these name the other things that may
	// supply the same two answers. The human can still answer at any time —
	// whichever lands first wins, which is why these live on the gate rather
	// than being separate wait stages (the engine runs one stage at a time).
	AutoApprove string
	AutoReject  string

	// action
	Action string // a slug registered in actionspec

	// wait: the mid-chain wait point (its signal, policy and timeout).
	// WaitFor mirrors Wait.Signal for the engine's current readers.
	Wait    *Wait
	WaitFor string

	// OnSuccess is the explicit success target: a stage id or TerminalDone.
	// Parse always fills it (YAML sugar: absent = next stage in document
	// order, last stage of the phase = done), so the runtime model is fully
	// explicit.
	OnSuccess string

	// Edges are the recoverable redirects by kind (EdgeFail, EdgeReject,
	// future kinds — DESIGN3 §2.1). Never nil after Parse.
	Edges map[string]*Edge

	// Phase is the id of the phase the stage belongs to — always set, since
	// every flow is phases (the shorthand's is ShorthandPhase).
	Phase string

	// Line is the stage's line in the source YAML, for error reporting.
	Line int
}

// Parks reports whether the stage is a wait point mid-chain: a wait or a
// gate, which stops the phase for something outside the run.
func (s *Stage) Parks() bool { return s.Type == StageWait || s.Type == StageHumanGate }

// TriggerManual is the beginning a person gives from the run page
// (signalspec.Manual): a phase on it has no signal and starts by hand only.
const TriggerManual = signalspec.Manual

// DefaultPhaseMax bounds how many times a phase opened by a firing signal may
// run for one subject when `max` does not say.
const DefaultPhaseMax = 10

// Start is a wait point's policy: what happens when its signal holds.
type Start string

const (
	StartAuto    Start = "auto"    // proceed as soon as the signal holds
	StartConfirm Start = "confirm" // raise a decision ping first; proceed on yes
	StartManual  Start = "manual"  // never proceed on its own: a person does, the signal is advice
)

// Wait is a wait point: where a chain stops for something outside the run
// (FLOWLANG §1). A phase's `on:` is its opening wait point; a wait stage is
// one mid-chain. Signal is a registry slug — run_start (a run is created for
// the phase), manual (a person starts it), a trigger (the forge) — or, on a
// wait stage, a waitable one. Start is the policy applied when the signal
// holds (a person's act for run_start and manual). QuietFor debounces a
// signal that fires on activity. Max bounds a phase's runs per subject
// (UnlimitedMax = no bound; 1 on a wait stage, which fires once per pass).
// Timeout bounds a wait stage (zero = none).
type Wait struct {
	Signal   string
	Start    Start
	QuietFor time.Duration
	Max      int
	Timeout  time.Duration
}

// Trigger is the pre-v4 name of Wait.
//
// Deprecated: use Wait.
type Trigger = Wait

// Phase is one chain of stages and the wait point that opens it. Its stages
// are Flow.Stages[First..Last], contiguous in document order. Every phase is
// equal: an entrypoint (on: run_start) opens when a run is created for it, a
// manual phase when a person starts it from the run page, a firing phase when
// its signal holds for one of the run's merge requests — one phase run at a
// time, in one workspace.
type Phase struct {
	ID          string
	Description string
	On          Wait
	First, Last int
	Line        int
}

// IsEntrypoint reports whether a run may begin at this phase (on: run_start).
func (p *Phase) IsEntrypoint() bool { return p.On.Signal == signalspec.RunStart }

// Fires reports whether the phase opens on a signal the engine observes (a
// trigger other than manual).
func (p *Phase) Fires() bool {
	sig := signalspec.Get(p.On.Signal)
	return sig != nil && sig.Trigger && p.On.Signal != signalspec.Manual
}

// HasSubject reports whether a run of this phase acts on one merge request:
// its signal needs one.
func (p *Phase) HasSubject() bool {
	sig := signalspec.Get(p.On.Signal)
	return sig != nil && sig.Needs == signalspec.NeedsMR
}

// Entry is IsEntrypoint under its pre-v4 name.
//
// Deprecated: use IsEntrypoint.
func (p *Phase) Entry() bool { return p.IsEntrypoint() }

// Input returns the stage's named binding for name, or nil.
func (s *Stage) Input(name string) *IOBinding {
	for i := range s.Inputs {
		if s.Inputs[i].Name == name {
			return &s.Inputs[i]
		}
	}
	return nil
}

// Edge returns the stage's edge of the given kind, or nil.
func (s *Stage) Edge(kind string) *Edge { return s.Edges[kind] }

type Flow struct {
	Name        string
	Description string
	Defaults    Defaults
	Budget      Budget
	// Agents are the agent definitions this flow runs, in document order
	// (FLOWLANG §1, v3). The flow is the single source: every agent stage's
	// Agent names one of these, so contracts are parse-time facts and the
	// run pins prompts along with the rest of the document.
	Agents []agentspec.Spec
	Stages []Stage
	// Phases is never empty after Parse: a document written with top-level
	// stages is one phase, ShorthandPhase, opened by run_start.
	Phases []Phase

	// Hash is the SHA-256 (hex) of the raw flow file.
	Hash string

	// Warnings are advisory findings from parsing: legal constructs that look
	// like mistakes (a path no stage produces, a sentinel no edge can fill).
	// Never block a save or a run — everything that breaks is a parse error
	// now that agents live in the document.
	Warnings []string

	// shorthand marks a document written with top-level stages, desugared
	// into the one phase above.
	shorthand bool
}

// Agent returns the flow's definition of name, or nil.
func (f *Flow) Agent(name string) *agentspec.Spec {
	if f == nil {
		return nil
	}
	for i := range f.Agents {
		if f.Agents[i].Name == name {
			return &f.Agents[i]
		}
	}
	return nil
}

// AgentHash is the content address of the flow's agent, or "" when it
// declares no such agent. Recorded on every stage_started.
func (f *Flow) AgentHash(name string) string {
	if sp := f.Agent(name); sp != nil {
		return agentspec.Hash(sp)
	}
	return ""
}

// AgentSpecs lists every agent the flow declares, for the harness adapters
// that must materialize them in the workspace (Harness.Prepare).
func (f *Flow) AgentSpecs() []*agentspec.Spec {
	if f == nil {
		return nil
	}
	out := make([]*agentspec.Spec, 0, len(f.Agents))
	for i := range f.Agents {
		out = append(out, &f.Agents[i])
	}
	return out
}

// Entrypoints lists the phases a run may begin at, in document order.
func (f *Flow) Entrypoints() []*Phase {
	var out []*Phase
	for i := range f.Phases {
		if f.Phases[i].IsEntrypoint() {
			out = append(out, &f.Phases[i])
		}
	}
	return out
}

// Beginning is the phase a run begins at when its creation names none: the
// first entrypoint in document order (Parse guarantees one exists).
func (f *Flow) Beginning() *Phase {
	for i := range f.Phases {
		if f.Phases[i].IsEntrypoint() {
			return &f.Phases[i]
		}
	}
	return nil
}

// Phased reports whether the document declared its phases itself rather
// than writing the one-phase shorthand.
//
// Deprecated: every flow is phases; the engine's remaining uses go away with
// per-phase-run state.
func (f *Flow) Phased() bool { return f != nil && !f.shorthand }

// Phase returns the phase with the given id, or nil.
func (f *Flow) Phase(id string) *Phase {
	for i := range f.Phases {
		if f.Phases[i].ID == id {
			return &f.Phases[i]
		}
	}
	return nil
}

// Reactions lists the phases that are not entrypoints, in document order.
//
// Deprecated: walk Phases and ask IsEntrypoint / Fires.
func (f *Flow) Reactions() []Phase {
	var out []Phase
	for i := range f.Phases {
		if !f.Phases[i].IsEntrypoint() {
			out = append(out, f.Phases[i])
		}
	}
	return out
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
