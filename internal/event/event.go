// Package event defines the factory's event catalog (DESIGN.md §6) and the
// append-only store. Events are the source of truth: every transition is an
// event; projections and analytics are derived, never authoritative.
package event

import (
	"encoding/json"
	"time"
)

type Type string

const (
	TypeRunCreated     Type = "run_created"
	TypeEnvRequested   Type = "env_requested"
	TypeImageBuilt     Type = "image_built" // DESIGN2 §6.3: pipeline ran between env_requested and env_ready
	TypeEnvReady       Type = "env_ready"
	TypeEnvFailed      Type = "env_failed"
	TypeStageStarted   Type = "stage_started"
	TypeStageCompleted Type = "stage_completed"
	TypeStageFailed    Type = "stage_failed"
	// TypeStagePaused / TypeStageResumed bracket an attempt that stopped
	// mid-flight and picked up again LATER IN THE SAME ATTEMPT: the iteration
	// does not advance, no stage_completed/stage_failed sits between them, and
	// everything the harness had already done still stands. Today's producer
	// is an agent that ended its turn on an unanswered ask_human — the engine
	// owns that wait, because a human is not something a harness's tool-call
	// timer can be asked to outlast. The pair is deliberately generic though:
	// any future "park this stage, continue it later" gets these two events
	// with its own Reason.
	TypeStagePaused     Type = "stage_paused"
	TypeStageResumed    Type = "stage_resumed"
	TypeEdgeTaken       Type = "edge_taken"
	TypePingRaised      Type = "ping_raised"
	TypePingAnswered    Type = "ping_answered"
	TypeVerdictReceived Type = "verdict_received"
	// TypeActionStarted records the INTENT to perform an action's external
	// side effect, appended before the call (PLAN5 M20). A crash between the
	// effect landing on the repository host and its completion event leaves
	// this on its own — which is exactly the signal recovery needs to
	// reconcile rather than blindly re-run and duplicate.
	TypeActionStarted Type = "action_started"
	// TypeSnapshotCaptured links one engine-captured snapshot — a stage
	// attempt's declared output, its log, or a ping file (what the agent
	// showed the human) — to the run's timeline (DESIGN-FILES §4). It is the
	// AUTHORITY the snapshots table is projected from, and it carries the
	// REFERENCE, never the bytes: events stay replay-cheap while the content
	// they point at can be any size — which is what makes "the plan the human
	// rejected at attempt 1" answerable after the workspace is long gone.
	TypeSnapshotCaptured Type = "snapshot_captured"
	TypeMRCreated        Type = "mr_created"
	// Phases (PLAN6): a phase run starting on one merge request (its
	// subject), ending, or a human declining to start one.
	TypePhaseStarted   Type = "phase_started"
	TypePhaseCompleted Type = "phase_completed"
	TypePhaseDeclined  Type = "phase_declined"
	// A phase run parking at a wait point mid-chain (a wait or a gate) and
	// picking up again: between the two the run is idle and other phases
	// may run.
	TypePhaseParked  Type = "phase_parked"
	TypePhaseResumed Type = "phase_resumed"
	// Review rounds and target syncs (PLAN6 M31/M32).
	TypeReviewCommentsLoaded Type = "review_comments_loaded"
	TypeReviewRepliesPosted  Type = "review_replies_posted"
	TypeReviewPosted         Type = "review_posted"
	TypeMRMerged             Type = "mr_merged"
	TypeTargetSynced         Type = "target_synced"
	TypeBranchPushed         Type = "branch_pushed"
	TypeRunCompleted         Type = "run_completed"
	TypeRunAbandoned         Type = "run_abandoned"
	TypeRunFailed            Type = "run_failed"
	TypeRunArchived          Type = "run_archived" // addition over §6 (see IMPLEMENTATION_NOTES): archived is a projected state, so it needs an event
	TypeRunRetried           Type = "run_retried"  // "try again": re-enter a failed stage in the same workspace
	TypeRunPaused            Type = "run_paused"   // user pressed pause: loop exited cleanly, run parked non-terminal
	TypeRunUnpaused          Type = "run_unpaused" // user pressed play: loop restarts and re-enters via the recovery path
	TypeEnvDestroyed         Type = "env_destroyed"
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
	ReasonExitNonzero  = "exit_nonzero"
	ReasonTimeout      = "timeout"
	ReasonHarnessError = "harness_error"
	// ReasonOutputContract: the stage exited fine but did not write what it
	// declared it produces. Since PLAN5 M18 the engine deletes a stage's
	// declared outputs before each fresh attempt, so this also catches "an
	// earlier attempt wrote it, this one did not".
	ReasonOutputContract = "output_contract"
	// ReasonOutputInvalid: the stage wrote a declared output, but its content
	// does not hold what its type promises (a malformed verdict). Distinct
	// from output_contract, which means the file is not there at all.
	ReasonOutputInvalid = "output_invalid"
	// ReasonInputMissing: a path the stage declared it consumes was not there
	// when the stage started. Distinct from output_contract because the fault
	// is upstream wiring, not this stage's work.
	ReasonInputMissing = "input_missing"
	// ReasonInputInvalid: a value the flow wrote for the stage does not fit
	// what it addresses — a status the tracker cannot reach, a field it does
	// not have or cannot write from text. Like input_missing the fault is
	// the flow's wiring, not this stage's work; unlike it the input is there,
	// it just means nothing where it lands.
	ReasonInputInvalid = "input_invalid"
	ReasonInfra        = "infra"
)

// StagePaused / StageResumed reasons.
const (
	// PauseAskHuman: the attempt is parked on unanswered ask_human questions
	// — the human took longer than the live hold, so the engine ended the turn
	// (or the agent ended it itself). It waits, untimed, until every open ping
	// is answered, then resumes the same session with the answers.
	PauseAskHuman = "ask_human"
	// ResumeAnswered: the questions that paused the attempt came back
	// answered and are being delivered into the harness session.
	ResumeAnswered = "answered"
	// ResumeRestart: the engine restarted while the attempt was in flight and
	// re-entered its session (recovery, not a human's doing).
	ResumeRestart = "engine_restart"
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
	// Checkouts are the repositories this run works in: the primary (the
	// devcontainer's) first, then the project's extra repos, each with the
	// branch the run owns there and the branch it forked from. Absent on runs
	// older than multi-repo support, which had exactly the primary (Branch /
	// SourceBranch / RepoURL above).
	Checkouts []Checkout `json:"checkouts,omitempty"`
	// TicketAttachmentIDs snapshots the parent ticket's attachments at run
	// start, and pins which ones this attempt saw even if the ticket changes
	// later.
	//
	// There is deliberately NO run-side equivalent: the run's OWN attachments
	// are rows claimed for this run (attachments.run_id) and found via
	// ForRun, so listing their ids here would be duplicate state that can
	// drift. A TICKET's attachments are owned by the ticket (ticket_id set)
	// so they can reach EVERY run — which is exactly why ForRun cannot see
	// them, and why their ids must ride on the event.
	TicketAttachmentIDs []string `json:"ticket_attachment_ids,omitempty"`
	// TicketSource* snapshot where the ticket was imported from — the
	// ticketsource integration, its vendor, the issue key and its page — so
	// a tracker action (FLOWLANG §2) knows which issue to write to without a
	// lookup the ticket's later deletion could break. Absent on a ticket
	// written by hand: tracker actions then skip.
	TicketSourceID   string `json:"ticket_source_id,omitempty"`
	TicketSourceKind string `json:"ticket_source_kind,omitempty"`
	TicketSourceKey  string `json:"ticket_source_key,omitempty"`
	TicketSourceURL  string `json:"ticket_source_url,omitempty"`
	// Params are the parameter values the run runs with — every declared
	// one, resolved at creation from the flow's defaults, the project's
	// bindings and this attempt's overrides — so recovery resolves the
	// pinned document to the same flow whatever changed since. By is who
	// created it: human, or automation.
	Params map[string]string `json:"params,omitempty"`
	By     string            `json:"by,omitempty"`
	// MR is the merge request someone opened that the run was started on (a
	// flow with run_start: merge_request): the run works on its branch, and
	// it is the run's merge request from the start. Nil for a run that opens
	// its own.
	MR *AdoptedMR `json:"mr,omitempty"`
}

// AdoptedMR is a merge request a run was created on: which checkout it is
// in (its Checkout says the branch and the ref its head is fetched from),
// and what the repository host said of it then.
type AdoptedMR struct {
	Repo   string `json:"repo"`
	IID    int    `json:"iid"`
	URL    string `json:"url"`
	Head   string `json:"head"`
	Author string `json:"author,omitempty"`
	Title  string `json:"title,omitempty"`
}

// Checkout is one repository a run checks out.
type Checkout struct {
	// Name is how flows, agents and branches refer to it: "repo" for the
	// primary, the project's repo name for extras.
	Name string `json:"name"`
	// Dir is the checkout's path relative to the workspace home: "repo" for
	// the primary, "repos/<name>" for extras.
	Dir     string `json:"dir"`
	URL     string `json:"url"`
	Locator string `json:"locator,omitempty"`
	// Branch is the run's own branch in this repo; Source is what it forked
	// from (and what its merge request targets).
	Branch  string `json:"branch"`
	Source  string `json:"source"`
	Primary bool   `json:"primary,omitempty"`
	// Fetch is the ref the branch starts from instead of forking Source: the
	// head of a merge request the run was created on, from the ref its
	// repository host keeps for it (a fork's included). Fork marks one from
	// another repository: readable, with no branch here to push to.
	Fetch string `json:"fetch,omitempty"`
	Fork  bool   `json:"fork,omitempty"`
}

// PrimaryCheckoutName / PrimaryCheckoutDir name the devcontainer's own repo.
const (
	PrimaryCheckoutName = "repo"
	PrimaryCheckoutDir  = "repo"
)

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
	// Skipped marks a completion that did no work because the stage had
	// nothing to act on — a tracker action on a ticket with no tracker
	// issue. Routing is unchanged (on_success follows); Note says why, for
	// the timeline. Engine-authored text, never workspace output.
	Skipped bool   `json:"skipped,omitempty"`
	Note    string `json:"note,omitempty"`
}

type StageFailed struct {
	StageID  string `json:"stage_id"`
	Reason   string `json:"reason"` // exit_nonzero|timeout|harness_error|output_contract|infra
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// StagePaused records an attempt parking mid-flight: the harness invocation
// returned, but the attempt is NOT over. TurnsUsed/Usage carry what that
// invocation spent — a paused attempt's cost is real even if the run never
// resumes — and SessionID is what the resume re-enters, persisted HERE so a
// pause survives an engine restart (a held-open pipe never could).
type StagePaused struct {
	StageID   string `json:"stage_id"`
	Iteration int    `json:"iteration,omitempty"`
	Reason    string `json:"reason"` // ask_human
	// PingIDs are the open questions the attempt is waiting on: what has to
	// be answered before a stage_resumed can follow.
	PingIDs   []string `json:"ping_ids,omitempty"`
	TurnsUsed int      `json:"turns_used,omitempty"`
	Usage     *Usage   `json:"usage,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
}

// StageResumed is the other half: the same stage and the same iteration
// picking up where StagePaused left it. PingIDs name the answers being
// delivered to the agent.
type StageResumed struct {
	StageID   string   `json:"stage_id"`
	Iteration int      `json:"iteration,omitempty"`
	Reason    string   `json:"reason"` // answered|engine_restart
	PingIDs   []string `json:"ping_ids,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
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

// Ping types. PingGate is every human_gate stage: gates are one machine
// (approve / reject-with-feedback), so what a gate is asking lives in its
// free-text question, not in a type the engine has to interpret. The rest are
// raised by agents through ask_human.
const (
	PingGate = "gate"
	// PingDebug is the debug leaf (FLOWLANG §3): a stage took `then: debug`
	// and the run is parked for a human who has the workspace. StageID is the
	// stage that failed, Context what it printed, Link the workspace; the
	// question is a bare label — the card is the UI's. Approve re-enters that
	// stage, reject abandons the run. Engine-raised and wanted, like a gate.
	PingDebug = "debug"
	// PingPhaseConfirm asks a human whether a phase may start (a phase with
	// `confirm: true`). Answered like a gate — approve | reject — but it
	// belongs to no stage: it is raised while the run is between phases.
	PingPhaseConfirm = "phase_confirm"
	// PingSelect asks a human to pick any number of its Options (which
	// review threads a round should fix). The answer is the chosen options,
	// one per line, or SelectNone.
	PingSelect = "select"
	SelectNone = "none"

	// Answer kinds: the shape of the answer a ping takes, declared on the
	// ping when it is raised (PingRaised.AnswerKind). One field, one table
	// of rules — the UI renders it, the engine validates it, no caller
	// infers it from the ping's type or its options.
	//   decision  approve | reject (+ feedback)        gates, phase confirmations
	//   choice    one option id | "other" (+ note)     ask_human closed questions
	//   multi     option ids, one per line | "none"    which review threads to fix
	//   text      free text                            ask_human open questions
	AnswerDecision    = "decision"
	AnswerChoice      = "choice"
	AnswerMulti       = "multi"
	AnswerText        = "text"
	PingClarification = "clarification"
	PingBlocker       = "blocker"
	PingPermission    = "permission"
)

type PingRaised struct {
	PingID   string `json:"ping_id"`
	Type     string `json:"type"` // gate|clarification|blocker|permission
	Question string `json:"question"`
	Context  string `json:"context,omitempty"` // §9 pings.context; persisted so recovery keeps it
	// Files are the workspace files attached to the ping (repo-relative
	// paths; the first is the primary reviewed document). Link is a resolved
	// URL payload (a gate's $MR_URL/$WORKSPACE sentinel). TYPED at the
	// source (DESIGN-FILES §4): a value is a file or a link by field, never
	// by sniffing the string for "http".
	Files []string `json:"files,omitempty"`
	Link  string   `json:"link,omitempty"`
	// StageID is the flow stage in flight when the ping was raised
	// (PLAN4 M15): the attribution key joining reviews to stages and,
	// via stage_started.agent_hash, to prompt versions.
	StageID string `json:"stage_id,omitempty"`
	// Key is the raiser's idempotency key: a retried raise (its response
	// was lost) carries the same key and gets the same ping back instead of
	// asking the human twice.
	Key string `json:"key,omitempty"`
	// Options are the answer choices (PLAN4 M16): for a choice the answer
	// is one of their ids — the agent asked a closed question so it can
	// branch deterministically — or the reserved "other" escape hatch
	// (system-provided, never declared; the feedback then carries the
	// human's actual answer); for a multi, any subset of their ids.
	Options []Option `json:"options,omitempty"`
	// AnswerKind is the shape of the answer (the Answer* constants). Empty
	// on pings raised before it existed; Kind() infers those.
	AnswerKind string `json:"answer_kind,omitempty"`
	// Phase, Subject and TriggerKey identify what a phase_confirm ping asks
	// to start: which phase, on which checkout's merge request, for which
	// occurrence of its trigger. A debug ping carries Phase too: the phase
	// it parks.
	Phase      string `json:"phase,omitempty"`
	Subject    string `json:"subject,omitempty"`
	TriggerKey string `json:"trigger_key,omitempty"`
	// EndsPhase, on a debug ping: giving up ends that phase run and the run
	// goes back to waiting — the phase is a watcher (FLOWLANG §3). False:
	// giving up abandons the run.
	EndsPhase bool `json:"ends_phase,omitempty"`
}

// PhaseStarted: a phase run begins. Subject is the checkout whose merge
// request it acts on ("" for a main chain started on a ticket, which acts on
// all of them, and an outcome that waits for every one);
// Key is the trigger occurrence it answers (R6: never started twice for
// one); By is signal (its trigger held) or human (a person started the run,
// or the phase from the run page); Tip is the subject branch's head at the
// start — what $DIFF_PHASE is measured from inside the phase.
type PhaseStarted struct {
	Phase   string `json:"phase"`
	Subject string `json:"subject,omitempty"`
	Key     string `json:"key,omitempty"`
	By      string `json:"by"`
	Tip     string `json:"tip,omitempty"`
}

// PhaseCompleted: the phase run reached done; the run is idle again (or,
// for a main chain that opened no merge request, about to complete).
type PhaseCompleted struct {
	Phase   string `json:"phase"`
	Subject string `json:"subject,omitempty"`
	// Reason is why the phase ended early, when it did: a dead end in a
	// watcher — a failure with nowhere to go, a person giving up at the
	// debug leaf (FLOWLANG §3). Empty: its chain reached its end.
	Reason string `json:"reason,omitempty"`
}

// PhaseDeclined: a human said no to starting a confirm phase for this
// trigger occurrence; it is not offered again until the key changes.
// PhaseParked: the phase run stopped at a wait point mid-chain — StageID,
// a wait or a gate — and gave the workspace back; the run is idle until it
// resumes, and other phases may run meanwhile. PhaseResumed: the wait point
// settled (its signal held, a human answered, it timed out) and the phase
// run goes on from that stage.
type PhaseParked struct {
	Phase   string `json:"phase"`
	Subject string `json:"subject,omitempty"`
	StageID string `json:"stage_id"`
	// PingID is the decision a parked gate waits on ("" for a wait stage).
	PingID string `json:"ping_id,omitempty"`
}

type PhaseResumed struct {
	Phase   string `json:"phase"`
	Subject string `json:"subject,omitempty"`
	StageID string `json:"stage_id"`
}

type PhaseDeclined struct {
	Phase   string `json:"phase"`
	Subject string `json:"subject,omitempty"`
	Key     string `json:"key"`
}

// ReviewCommentsLoaded: a review round took these threads on; Newest is the
// newest reviewer comment among them.
type ReviewCommentsLoaded struct {
	StageID   string   `json:"stage_id,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	ThreadIDs []string `json:"thread_ids"`
	Newest    string   `json:"newest,omitempty"`
	// Skipped are the pending threads a human chose not to fix, each with
	// its last comment's id: left alone until someone writes there again.
	Skipped map[string]string `json:"skipped,omitempty"`
}

// ReviewPosted: post_review put a review on the subject merge request —
// Inline notes on their lines, Folded ones (lines outside the change) in
// the summary; Already when a retry found it all posted.
type ReviewPosted struct {
	StageID string `json:"stage_id,omitempty"`
	Subject string `json:"subject"`
	Inline  int    `json:"inline"`
	Folded  int    `json:"folded,omitempty"`
	Already bool   `json:"already,omitempty"`
}

// MRMerged: mr_merge merged the subject merge request at Head with Method. A
// merge by a person is not an event — the repository host observation sees it.
type MRMerged struct {
	StageID       string `json:"stage_id,omitempty"`
	Repo          string `json:"repo"`
	Method        string `json:"method"`
	Head          string `json:"head,omitempty"`
	BranchDeleted bool   `json:"branch_deleted,omitempty"`
}

// ReviewRepliesPosted: the round's replies are on the repository host.
type ReviewRepliesPosted struct {
	StageID   string   `json:"stage_id,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	ThreadIDs []string `json:"thread_ids"`
	Resolved  []string `json:"resolved,omitempty"`
}

// BranchPushed: push_branch pushed Branch of checkout Repo at Tip — what the
// next push of it is leased against.
type BranchPushed struct {
	StageID string `json:"stage_id,omitempty"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Tip     string `json:"tip"`
}

// TargetSynced: the subject branch now contains its target at TargetTip.
type TargetSynced struct {
	StageID   string `json:"stage_id,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Strategy  string `json:"strategy"`
	TargetTip string `json:"target_tip"`
	ResultTip string `json:"result_tip"`
}

// Option is one answer choice: ID is what the answer names (and what an
// agent branches on), Label what the human reads, Detail an optional second
// line. Decodes from a bare string too — the form every option had before
// options were structured — as {ID: s, Label: s}.
type Option struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

func (o *Option) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*o = Option{ID: s, Label: s}
		return nil
	}
	type plain Option
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*o = Option(p)
	if o.Label == "" {
		o.Label = o.ID
	}
	return nil
}

// OptionIDs lists the options' ids.
func OptionIDs(opts []Option) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.ID
	}
	return out
}

// Kind is the ping's answer kind, inferred for pings recorded before the
// field existed: a gate or phase confirmation is a decision, a selection a
// multi, a question with options a choice, anything else free text.
func (p *PingRaised) Kind() string {
	if p.AnswerKind != "" {
		return p.AnswerKind
	}
	switch {
	case p.Type == PingGate || p.Type == PingPhaseConfirm || p.Type == PingDebug:
		return AnswerDecision
	case p.Type == PingSelect:
		return AnswerMulti
	case len(p.Options) > 0:
		return AnswerChoice
	}
	return AnswerText
}

type PingAnswered struct {
	PingID   string `json:"ping_id"`
	Answer   string `json:"answer"` // approve|reject|text
	Feedback string `json:"feedback,omitempty"`
	// AttachmentIDs are the files the human attached to this answer — the
	// association is a replayable fact of the run, not just a side-table
	// write (DESIGN-FILES §4).
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
}

type VerdictReceived struct {
	StageID   string `json:"stage_id"`
	Approve   bool   `json:"approve"`
	NComments int    `json:"n_comments"`
}

// SnapshotCaptured records one engine capture. Kind is a
// filestore.SnapshotKind value (output|log|ping_file); the per-kind fields
// mirror the snapshots projection exactly — the event carries everything a
// rebuild needs to reconstruct the row, minus the bytes.
type SnapshotCaptured struct {
	StageID    string `json:"stage_id"`
	Iteration  int    `json:"iteration,omitempty"`
	Kind       string `json:"kind"`
	Name       string `json:"name,omitempty"`    // output role ('plan'); outputs only
	Path       string `json:"path,omitempty"`    // workspace path; outputs + ping files
	PingID     string `json:"ping_id,omitempty"` // ping files only
	SnapshotID string `json:"snapshot_id"`
	Filename   string `json:"filename,omitempty"`
	Mime       string `json:"mime,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// ActionStarted is the pre-effect intent marker for an action stage.
type ActionStarted struct {
	StageID string `json:"stage_id"`
	Action  string `json:"action"`
}

type MRCreated struct {
	MRURL  string `json:"mr_url"`
	MRIID  int    `json:"mr_iid"`
	Branch string `json:"branch"`
	// Repo is the checkout the branch lives in ("" = the primary, on runs
	// older than multi-repo); Target is the branch the MR merges into ("" =
	// the repo's default, on older runs).
	Repo   string `json:"repo,omitempty"`
	Target string `json:"target,omitempty"`
	// Head is the merge request's head commit when the run opened (or
	// adopted) it: a commit the run knows, so finding it there later is not
	// someone else's push ("" when the repository host could not be asked).
	Head string `json:"head,omitempty"`
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

// RunRetried un-terminals an abandoned/failed run and re-enters StageID
// (the "try again" path): the workspace and prior stages are reused, only
// the failed stage re-runs, with its retry budgets reset. History stays
// immutable — this is a forward event, not a rewind.
type RunRetried struct {
	StageID string `json:"stage_id"`
	// FlowHash re-pins the run to the flow's CURRENT version (PLAN5 M19).
	// Runs execute the document they started with, so "try again" is the one
	// sanctioned moment to pick up an edit — fix a prompt or a binding, then
	// retry. Empty on pre-repin events, which keep their original pin.
	FlowHash string `json:"flow_hash,omitempty"`
}

// RunPaused parks a run: the loop goroutine exits cleanly (a controlled
// crash) and the run stays non-terminal but is NOT recovered on boot.
// StageID is the stage in flight when pause hit, informational only —
// replay keeps the stage events' own record of where to re-enter.
//
// Reason says WHO parked it, which decides both what the run reads as and
// what wakes it. PauseUser is the pause button: the run reads paused, and
// only play moves it. PauseWaiting is the engine parking a run that has
// nothing left to do but wait for a human — it reads waiting_on_human, since
// a run waiting for YOU is not a run someone pressed pause on, and the answer
// itself wakes it. Empty means user: every pause predating this field was one.
type RunPaused struct {
	StageID string `json:"stage_id,omitempty"`
	Reason  string `json:"reason,omitempty"` // user|waiting_on_human
}

// RunPaused reasons.
const (
	PauseUser    = "user"
	PauseWaiting = "waiting_on_human"
)

// RunUnpaused (play) restarts a paused run's loop; it re-enters mid-stage
// exactly like crash recovery (recorded harness sessions resume, commands
// re-execute, gates re-arm). Unlike RunRetried it resets NO retry budgets —
// the run continues, it does not start the stage's accounting over.
// Waiting is set when the run still has an open ping at play time (a
// paused gate): re-arming appends no new ping_raised, so the projection
// needs it to land back on waiting_on_human instead of running.
type RunUnpaused struct {
	Waiting bool `json:"waiting,omitempty"`
	// Reason says what woke it: a human pressing play, or the answer a parked
	// run was waiting for arriving. Empty means play — every unpause before
	// this field was one.
	Reason string `json:"reason,omitempty"` // play|answered
}

// RunUnpaused reasons.
const (
	UnpausePlay     = "play"
	UnpauseAnswered = "answered"
)

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
func (*StagePaused) EventType() Type     { return TypeStagePaused }
func (*StageResumed) EventType() Type    { return TypeStageResumed }
func (*EdgeTaken) EventType() Type       { return TypeEdgeTaken }
func (*PingRaised) EventType() Type      { return TypePingRaised }
func (*PingAnswered) EventType() Type    { return TypePingAnswered }
func (*VerdictReceived) EventType() Type { return TypeVerdictReceived }
func (*ActionStarted) EventType() Type   { return TypeActionStarted }

func (*SnapshotCaptured) EventType() Type { return TypeSnapshotCaptured }
func (*MRCreated) EventType() Type        { return TypeMRCreated }
func (*PhaseStarted) EventType() Type     { return TypePhaseStarted }
func (*PhaseCompleted) EventType() Type   { return TypePhaseCompleted }
func (*PhaseParked) EventType() Type      { return TypePhaseParked }
func (*PhaseResumed) EventType() Type     { return TypePhaseResumed }
func (*PhaseDeclined) EventType() Type    { return TypePhaseDeclined }
func (*ReviewCommentsLoaded) EventType() Type {
	return TypeReviewCommentsLoaded
}
func (*ReviewRepliesPosted) EventType() Type { return TypeReviewRepliesPosted }
func (*ReviewPosted) EventType() Type        { return TypeReviewPosted }
func (*MRMerged) EventType() Type            { return TypeMRMerged }
func (*TargetSynced) EventType() Type        { return TypeTargetSynced }
func (*BranchPushed) EventType() Type        { return TypeBranchPushed }
func (*RunCompleted) EventType() Type        { return TypeRunCompleted }
func (*RunAbandoned) EventType() Type        { return TypeRunAbandoned }
func (*RunFailed) EventType() Type           { return TypeRunFailed }
func (*RunArchived) EventType() Type         { return TypeRunArchived }
func (*RunRetried) EventType() Type          { return TypeRunRetried }
func (*RunPaused) EventType() Type           { return TypeRunPaused }
func (*RunUnpaused) EventType() Type         { return TypeRunUnpaused }
func (*EnvDestroyed) EventType() Type        { return TypeEnvDestroyed }

func (*EnvRebuildStarted) EventType() Type   { return TypeEnvRebuildStarted }
func (*EnvRebuildCompleted) EventType() Type { return TypeEnvRebuildCompleted }
func (*EnvRebuildFailed) EventType() Type    { return TypeEnvRebuildFailed }
