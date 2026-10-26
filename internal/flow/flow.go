// Package flow defines the versioned flow document: its schema, parser and
// validator — the flow language, specified in docs/flow-language.md and cited
// as FLOWLANG §n. Flows are loaded at run creation; the run stores the flow
// name plus the SHA-256 hash of the raw file.
package flow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/agentspec"
	"apothecary/internal/signalspec"
)

// CurrentVersion is the grammar this build speaks. Every document declares
// its version, so a break gets a clean refusal ("this build speaks version
// 5") instead of mis-parsing an unfamiliar shape.
//
// v5 gives a flow one way in (`operates_on:` — what a person or an automation
// starts its runs on), one `main` chain that runs when the run starts,
// `watchers` that run their chain on each new occurrence of a signal while
// the run is open, and `outcomes` that fire once and end it. Underneath
// they are v4's phases, each opened by a wait point. There is no legacy
// mapping: a document of any other version is refused.
const CurrentVersion = 5

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
	//
	// The git ones are named after the git they run, so each means one
	// range wherever it is bound: DIFF_TARGET what the branch changes
	// against its target (what the merge request merges), DIFF_PHASE what
	// this phase run committed since the commit it began on, STATUS what a
	// commit would record now.
	SentinelDiffTarget = "$DIFF_TARGET" // git diff --name-status <target>...HEAD
	SentinelDiffPhase  = "$DIFF_PHASE"  // git diff --name-status <phase start>...HEAD
	SentinelStatus     = "$STATUS"      // git status --short: staged, unstaged, untracked
	SentinelMRURL      = "$MR_URL"      // the run's merge-request link (create_mr's output)
	SentinelWorkspace  = "$WORKSPACE"   // the workspace's access link (terminal/IDE)
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
// repo-relative artifact path: $UPPER_CASE. A $lower_case token is a
// parameter reference (IsParamRef), never a sentinel.
func IsSentinel(v string) bool { return sentinelRe.MatchString(v) }

// IsParamRef reports whether v reads a parameter: $name, lowercase.
func IsParamRef(v string) bool { return paramRefRe.MatchString(v) }

var (
	sentinelRe = regexp.MustCompile(`^\$[A-Z][A-Z0-9_]*$`)
	paramRefRe = regexp.MustCompile(`^\$[a-z][a-z0-9_]*$`)
	// ParamNameRe is the shape of a parameter's name.
	ParamNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
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

// The reserved routing targets (FLOWLANG §3). Stage ids may shadow none.
//
// TerminalEnd ends the PHASE: its chain is over, and the run goes back to
// waiting — a parked phase resumes when its event comes, another phase opens
// on its signal, or the run completes once nothing is parked or armed. It is
// on_success's natural end (a phase's last stage), and an edge's for a stage
// whose failure is not worth routing anywhere ("best effort"). A phase that
// can run many times — a watcher — ends this way.
//
// TerminalDone ends the RUN: it completes now, whatever else is parked or
// armed (their open questions are withdrawn). It is said where the work is
// truly finished — the default flow says it once the merge request merged.
//
// TerminalAbort deliberately abandons the run (mirroring the frontend's Abort
// verb).
//
// ReservedStart is the graph editor's marker key ("start" is never a routing
// target). ReservedDebug is the debug leaf: NOT a terminal — the phase parks
// for a human who has the workspace, continuing re-enters the stage that took
// the link, and giving up ends the run in the main chain or an outcome, and
// a watcher's round in a watcher.
//
// ReservedFail exists only inside a sequence: an edge to it is the sequence
// stage's on_fail, spliced in by the parser. No parsed flow carries it.
const (
	TerminalEnd   = "end"
	TerminalDone  = "done"
	TerminalAbort = "abort"
	ReservedStart = "start"
	ReservedDebug = "debug"
	ReservedFail  = "fail"
)

// routeTerminal reports whether a route target is reserved rather than a
// stage: the two ends, the deliberate stop, or the debug leaf.
func routeTerminal(to string) bool {
	switch to {
	case TerminalEnd, TerminalDone, TerminalAbort, ReservedDebug:
		return true
	}
	return false
}

// MainPhase is the id of a flow's main chain, the phase every run begins
// with. It is also the name other chains run it by (`sequence: main`).
const MainPhase = "main"

// What a run is started on (FLOWLANG §1 operates_on): a ticket — the run
// makes its own branch and opens its merge request — or a merge request
// someone opened, which the run works on from the start.
const (
	OperatesOnTicket = "ticket"
	OperatesOnMR     = "merge_request"
)

// PhaseKind is which section of the document a phase comes from.
type PhaseKind string

const (
	// PhaseMain is the main chain: it runs once, when the run starts.
	PhaseMain PhaseKind = "main"
	// PhaseWatcher runs its chain on each new occurrence of its signal while
	// the run is open (or when a person starts it, on manual); it never ends
	// the run.
	PhaseWatcher PhaseKind = "watcher"
	// PhaseOutcome fires once, when its signal holds, and ends the run.
	PhaseOutcome PhaseKind = "outcome"
)

// UnlimitedMax marks a link that never exhausts (max: -1). It is only
// valid when a human_gate sits on the link — as its TARGET (each firing
// parks the run on a human decision; reject aborts) or as its SOURCE (the
// link only fires when a human rejects the gate). Either way the loop is
// bounded by human attention. Links between agents/commands must carry a
// finite max — flow budgets cannot bound cost-free retries (a failing
// command consumes no turns and no dollars; a broken harness can fail in
// milliseconds). The debug leaf is unlimited by nature and takes no max.
// On a phase's wait point it means "no bound on the phase's runs" — the
// default: a watcher runs as often as its signal brings a new fact.
const UnlimitedMax = -1

// Edge is a recoverable redirect (on_fail / on_reject / on_timeout). Each
// link carries its own cumulative max; when a link is exhausted the NEXT
// failure falls through to Then (retry-then-escalate chains). An exhausted
// chain is a dead end: it abandons the run in the main chain or an outcome,
// and ends that round in a watcher. To may be TerminalEnd: the phase ends there,
// TerminalDone: the run completes, TerminalAbort: the run is abandoned, or
// ReservedDebug: the phase parks for a human and comes back to this stage on
// continue (none of the four takes max/then — there is no target stage).
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
	// Sequence is the path of sequence stages a stage was expanded through
	// ("make.ship" for make.ship.test); empty for a stage written in its
	// chain (FLOWLANG §1 Sequences).
	Sequence string

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

	// OnSuccess is the explicit success target: a stage id, TerminalEnd or
	// TerminalDone. Parse always fills it (YAML sugar: absent = next stage in
	// document order, last stage of the phase = end), so the runtime model is
	// fully explicit.
	OnSuccess string

	// Edges are the recoverable redirects by kind (EdgeFail, EdgeReject,
	// future kinds — DESIGN3 §2.1). Never nil after Parse.
	Edges map[string]*Edge

	// When is the stage's condition (FLOWLANG §2 when), nil = always. A
	// stage whose condition is false completes as skipped and the chain
	// follows on_success.
	When *When

	// Phase is the id of the phase the stage belongs to — always set: the
	// main chain's stages are MainPhase's.
	Phase string

	// Line is the stage's line in the source YAML, for error reporting.
	Line int
}

// Parks reports whether the stage is a wait point mid-chain: a wait or a
// gate, which stops the phase for something outside the run.
func (s *Stage) Parks() bool { return s.Type == StageWait || s.Type == StageHumanGate }

// When is a stage's condition: one term, optionally negated. A $bool
// parameter folds at parse time into nil (true) or WhenNever (false), so the
// engine only ever sees the two run facts.
type When struct {
	Term string // WhenTracker | WhenSubject | WhenNever
	Not  bool
}

const (
	WhenTracker = "tracker" // the run's ticket has a tracker issue to write to
	WhenSubject = "subject" // the phase run acts on one merge request
	WhenNever   = "never"   // a parameter said no
)

// Holds evaluates the condition against the run's facts; a nil When holds.
func (w *When) Holds(tracker, subject bool) bool {
	if w == nil {
		return true
	}
	var v bool
	switch w.Term {
	case WhenTracker:
		v = tracker
	case WhenSubject:
		v = subject
	}
	return v != w.Not
}

func (w *When) String() string {
	if w == nil {
		return ""
	}
	if w.Not {
		return "not " + w.Term
	}
	return w.Term
}

// ParamType is what a parameter's default was written as, and so what its
// bound values must parse as.
type ParamType string

const (
	ParamBool   ParamType = "bool"
	ParamInt    ParamType = "int"
	ParamFloat  ParamType = "float"
	ParamString ParamType = "string"
)

// Param is one entry of the flow's `parameters:` block (FLOWLANG §1): a knob
// declared with its default in the document and bound from outside it — by
// the project, then by the run. Known marks the six the engine reads itself
// (harness, model, timeout, max_turns, max_cost_usd, max_total_turns); every
// other one is read where $name appears.
type Param struct {
	Name        string    `json:"name"`
	Type        ParamType `json:"type"`
	Default     string    `json:"default"`
	Description string    `json:"description,omitempty"`
	Enum        []string  `json:"enum,omitempty"`
	Known       bool      `json:"known,omitempty"`
	Line        int       `json:"-"`
}

// KnownParams are the engine-read parameters and their types.
var KnownParams = map[string]ParamType{
	"harness": ParamString, "model": ParamString, "timeout": ParamString,
	"max_turns": ParamInt, "max_cost_usd": ParamFloat, "max_total_turns": ParamInt,
}

// KnownParamOrder lists the engine-read parameters in display order.
var KnownParamOrder = []string{"harness", "model", "timeout", "max_turns", "max_cost_usd", "max_total_turns"}

// ParamRef is one place the document reads a parameter (derived by the
// parser; the editor marks bound fields with it).
type ParamRef struct {
	Param string `json:"param"`
	Phase string `json:"phase,omitempty"`
	Stage string `json:"stage,omitempty"`
	Field string `json:"field"`
	Input string `json:"input,omitempty"`
}

// ParseParamValue checks a value against a parameter's type and enum and
// returns its canonical text.
func ParseParamValue(pr *Param, v string) (string, error) {
	switch pr.Type {
	case ParamBool:
		b, err := strconv.ParseBool(v)
		if err != nil {
			return "", fmt.Errorf("parameter %q takes true or false, got %q", pr.Name, v)
		}
		return strconv.FormatBool(b), nil
	case ParamInt:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return "", fmt.Errorf("parameter %q takes an integer, got %q", pr.Name, v)
		}
		return strconv.Itoa(n), nil
	case ParamFloat:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return "", fmt.Errorf("parameter %q takes a number, got %q", pr.Name, v)
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	if len(pr.Enum) > 0 && !contains(pr.Enum, v) {
		return "", fmt.Errorf("parameter %q takes one of %s, got %q", pr.Name, strings.Join(pr.Enum, " | "), v)
	}
	return v, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ValidateBindings checks a set of bound values against the declared
// parameters: every key declared, every value of its parameter's type.
func ValidateBindings(params []Param, bindings map[string]string) error {
	for name, v := range bindings {
		var pr *Param
		for i := range params {
			if params[i].Name == name {
				pr = &params[i]
			}
		}
		if pr == nil {
			return fmt.Errorf("parameter %q is not declared by this flow", name)
		}
		if _, err := ParseParamValue(pr, v); err != nil {
			return err
		}
	}
	return nil
}

// Start is a wait point's policy: what happens when its signal holds.
type Start string

const (
	StartAuto    Start = "auto"    // proceed as soon as the signal holds
	StartConfirm Start = "confirm" // raise a decision ping first; proceed on yes
	StartManual  Start = "manual"  // never proceed on its own: a person does, the signal is advice
)

// Wait is a wait point: where a chain stops for something outside the run
// (FLOWLANG §1). A phase's `on:` is its opening wait point; a wait stage is
// one mid-chain. Signal is a registry slug — manual (a person starts it) or
// one the engine observes (the repository host); empty for the main chain,
// which nothing opens: it runs when the run is started. Start is the policy
// applied when the signal holds (a person's act for the main chain and
// manual; always auto on a wait stage). QuietFor debounces a signal that fires on activity. Max bounds a
// phase's runs per subject (UnlimitedMax, the default, = no bound; 1 on a wait
// stage, which fires once per pass). Timeout bounds a wait stage (zero =
// none).
type Wait struct {
	Signal   string
	Start    Start
	QuietFor time.Duration
	Max      int
	Timeout  time.Duration
}

// Phase is one chain of stages and the wait point that opens it — the main
// chain, a watcher or an outcome (Kind). Its stages are
// Flow.Stages[First..Last], contiguous in document order. The main chain
// opens when the run starts, a watcher each time its signal brings something
// new (or when a person starts it, on manual), an outcome once — one phase
// run at a time, in one workspace.
type Phase struct {
	ID          string
	Description string
	Kind        PhaseKind
	On          Wait
	// BringsMR marks the main chain of a flow whose runs are started on a
	// merge request: it acts on that merge request from the start.
	BringsMR bool
	// Then is how an OUTCOME ends the run once its chain, if it has one, is
	// through: TerminalDone (the default) or TerminalAbort — its last
	// stage's success goes there. Empty for the main chain and watchers.
	Then        string
	First, Last int // the phase's stages; Last < First for an outcome with no chain
	Line        int
}

// IsMain reports whether this phase is the main chain, the one a run begins
// with.
func (p *Phase) IsMain() bool { return p.Kind == PhaseMain }

// OnMR reports whether a run begun at this phase is started on an existing
// merge request (operates_on: merge_request) — and so needs one named.
func (p *Phase) OnMR() bool { return p.Kind == PhaseMain && p.BringsMR }

// IsOutcome reports whether the phase ends the run: it fires once, and every
// route in it ends the run.
func (p *Phase) IsOutcome() bool { return p.Kind == PhaseOutcome }

// Observed reports whether the engine watches a signal to open this phase:
// a watcher's or an outcome's, as opposed to a person's act.
func (p *Phase) Observed() bool { return p.Fires() || p.IsOutcome() }

// Fires reports whether the phase is a watcher on a signal the engine
// observes (any but manual).
func (p *Phase) Fires() bool { return p.Kind == PhaseWatcher && p.On.Signal != signalspec.Manual }

// OpensOn says what opens the phase, for a message: its signal, or for the
// main chain the run's start.
func (p *Phase) OpensOn() string {
	if p.Kind == PhaseMain {
		return "the run's start"
	}
	return p.On.Signal
}

// Label names the phase for a person: "the main chain", `watcher "x"`,
// `outcome "x"`.
func (p *Phase) Label() string {
	if p.Kind == PhaseMain {
		return "the main chain"
	}
	return fmt.Sprintf("%s %q", p.Kind, p.ID)
}

// HasSubject reports whether a run of this phase acts on one merge request:
// a watcher on a merge-request signal (each occurrence is one merge
// request's), the main chain of a run started on one, and an outcome that
// holds as soon as one merge request has its signal (closed: the one someone
// closed). An outcome that waits for every merge request (merged, approved —
// signalspec.All) acts on none in particular.
func (p *Phase) HasSubject() bool {
	if p.Kind == PhaseMain {
		return p.BringsMR
	}
	sig := signalspec.Get(p.On.Signal)
	if sig == nil || sig.Needs != signalspec.NeedsMR {
		return false
	}
	return p.Kind == PhaseWatcher || !sig.All
}

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
	// OperatesOn is what its runs are started on: OperatesOnTicket or
	// OperatesOnMR (FLOWLANG §1 operates_on).
	OperatesOn string
	// Phases are the main chain (MainPhase, absent in a flow that only
	// watches), then the watchers, then the outcomes, in document order.
	Phases []Phase

	// Hash is the SHA-256 (hex) of the raw flow file.
	Hash string

	// Warnings are advisory findings from parsing: legal constructs that look
	// like mistakes (a path no stage produces, a sentinel no edge can fill).
	// Never block a save or a run — everything that breaks is a parse error
	// now that agents live in the document.
	Warnings []string

	// Params are the flow's parameters in document order, the six known ones
	// always present (synthesized with the engine's defaults when the
	// document omits them). Refs are where the document reads them. Bindings
	// are the values this Flow was resolved with (Resolve); nil = defaults.
	Params   []Param
	Refs     []ParamRef
	Bindings map[string]string

	// file and src are what Parse was given, kept so Resolve can re-parse
	// the same bytes with bound values.
	file string
	src  []byte
}

// Param returns the declared parameter named name, or nil.
func (f *Flow) Param(name string) *Param {
	for i := range f.Params {
		if f.Params[i].Name == name {
			return &f.Params[i]
		}
	}
	return nil
}

// Resolve returns the flow with bindings applied — the document re-parsed
// with the bound values in place of the defaults, so every value-level rule
// is re-checked and the result carries no references. The receiver is
// unchanged; the hash is the document's. With nothing bound the receiver
// itself is returned.
func (f *Flow) Resolve(bindings map[string]string) (*Flow, error) {
	if len(bindings) == 0 && len(f.Bindings) == 0 {
		return f, nil
	}
	nf, err := parseBound(f.file, f.src, bindings)
	if err != nil {
		return nil, err
	}
	return nf, nil
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

// OperatesOnMR reports whether the flow's runs are started on a merge request
// someone opened.
func (f *Flow) OperatesOnMR() bool { return f.OperatesOn == OperatesOnMR }

// Main is the flow's main chain, the phase every run begins with; nil for a
// flow that only watches (a caretaker, started idle on a merge request).
func (f *Flow) Main() *Phase {
	for i := range f.Phases {
		if f.Phases[i].IsMain() {
			return &f.Phases[i]
		}
	}
	return nil
}

// Phase returns the phase with the given id, or nil.
func (f *Flow) Phase(id string) *Phase {
	for i := range f.Phases {
		if f.Phases[i].ID == id {
			return &f.Phases[i]
		}
	}
	return nil
}

func (f *Flow) Stage(id string) *Stage {
	for i := range f.Stages {
		if f.Stages[i].ID == id {
			return &f.Stages[i]
		}
	}
	return nil
}

// EndsRun reports whether some route of the flow ends the run: a stage
// routing to done, or an outcome with no chain whose then is done. A flow
// with none is refused (FLOWLANG §3): it would say nothing about when the
// work is finished.
func (f *Flow) EndsRun() bool {
	for i := range f.Phases {
		if ph := &f.Phases[i]; ph.Then == TerminalDone && ph.Last < ph.First {
			return true
		}
	}
	for i := range f.Stages {
		s := &f.Stages[i]
		if s.OnSuccess == TerminalDone {
			return true
		}
		for _, e := range s.Edges {
			for link := e; link != nil; link = link.Then {
				if link.To == TerminalDone {
					return true
				}
			}
		}
	}
	return false
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
