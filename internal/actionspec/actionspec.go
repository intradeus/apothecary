// Package actionspec is the registry of apothecary's built-in actions: the
// integrations a flow can invoke as a stage without an agent (FLOWLANG §6) —
// "create the merge request", "pull the MR comments", "transition the
// ticket", and in future "post to the channel".
//
// An action is apothecary-defined, never project data: the registry is
// compiled in, so a flow naming an unknown action is a parse error (unlike
// agents, which are per-project and resolved at run creation). Each action
// declares a signature — named inputs and outputs, exactly like an agent's
// I/O contract — so a stage binds it the same way and the editor can render
// binding rows and dataflow dots from the same shape.
//
// Adding an action: one Action entry here, one executor case in the engine,
// and (if it needs an integration) a Requires port kind that preflight
// asserts. Nothing else in the language changes.
//
// Tracker actions (ticket_*) are the one family with a per-TICKET
// prerequisite rather than a per-project one: the tracker is the
// integration the ticket was imported from. The engine skips them, visibly,
// on a ticket that has none.
//
// This package is a leaf: stdlib only, no ports. Requires is a port-kind
// string that the engine matches, keeping the registry free of adapter types.
package actionspec

import (
	"slices"
	"strings"
)

// Kind is what an action input or output carries.
type Kind string

const (
	// KindFile is a repo-relative workspace artifact path.
	KindFile Kind = "file"
	// KindValue is a value the engine holds rather than a file; a value
	// output is surfaced to later stages as its Sentinel (e.g. $MR_URL).
	KindValue Kind = "value"
	// KindEnum is a literal the stage writes in the flow, one of Values —
	// a choice the author makes (sync_target's strategy), visible in the
	// graph rather than hidden in project settings.
	KindEnum Kind = "enum"
	// KindText is a free literal the stage writes in the flow — a status
	// name, a field's display name. Like an enum it is the author's choice,
	// visible in the graph; unlike one its vocabulary belongs to the
	// tracker, so the parser checks only that it is not blank. Not part of
	// the dataflow: no io dot, no produced-path check.
	KindText Kind = "text"
)

// InputSig is one input a stage must bind when it invokes the action.
type InputSig struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Kind        Kind   `json:"kind"`
	Required    bool   `json:"required,omitempty"`
	// DefaultPath is the conventional path the editor prefills for a file
	// input. Unlike an agent output decl it is not a fallback: an action
	// input is bound by the stage or it is unbound.
	DefaultPath string `json:"default_path,omitempty"`
	// Values are the literals a KindEnum input accepts; Default is used when
	// the stage does not bind it.
	Values  []string `json:"values,omitempty"`
	Default string   `json:"default,omitempty"`
}

// OutputSig is one thing the action produces.
type OutputSig struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Kind        Kind   `json:"kind"`
	// Sentinel is the flow-language token later stages use to consume a
	// value output (KindValue only).
	Sentinel string `json:"sentinel,omitempty"`
	// Path is where a KindFile output is written.
	Path string `json:"path,omitempty"`
}

// Action is one registered action.
type Action struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// Requires names an integration port the project must have configured for
	// a flow using this action to start; empty = no integration needed.
	Requires string `json:"requires,omitempty"`
	// Single caps the action at one stage per phase (a phase run opens one
	// merge request).
	Single bool `json:"single,omitempty"`
	// ExactlyOne names inputs of which a stage binds exactly one: the value
	// comes from an artifact OR from the flow, the way a command takes run
	// OR run_ref.
	ExactlyOne []string `json:"exactly_one,omitempty"`
	// Provides names the run fact this action establishes, mirroring a
	// signal's Needs (signalspec): the validator pairs them to catch a wait
	// whose prerequisite no path creates.
	Provides string `json:"provides,omitempty"`
	// Subject marks an action that acts on one merge request — the phase's
	// subject — and so only makes sense in a reaction phase (PLAN6 R12).
	Subject bool `json:"subject,omitempty"`
	// Needs names a run fact the action depends on, mirroring a signal's
	// Needs: ticket_link needs a merge request to link. The validator warns
	// when no route establishes it; the engine fails the stage with
	// input_missing when it is absent at run time.
	Needs string `json:"needs,omitempty"`
	// Remote marks an action whose effect lands on an external service only
	// (the ticket tracker, a merge on the forge): it needs no workspace
	// unless one of its FILE inputs is bound, so the engine runs it without
	// provisioning one — a "move to In Progress" lands seconds after the run
	// starts, and a "Done" after the final gate never re-provisions a reaped
	// workspace.
	Remote  bool        `json:"remote,omitempty"`
	Inputs  []InputSig  `json:"inputs,omitempty"`
	Outputs []OutputSig `json:"outputs,omitempty"`
}

// Statuses lists the canonical ticket statuses in lifecycle order.
var Statuses = []string{StatusTodo, StatusInProgress, StatusInReview, StatusDone, StatusBlocked}

// Action slugs and the port kinds they require.
const (
	CreateMR            = "create_mr"
	LoadReviewComments  = "load_review_comments"
	ReplyReviewComments = "reply_review_comments"
	SyncTarget          = "sync_target"
	PushBranch          = "push_branch"
	MRMerge             = "mr_merge"
	PostReview          = "post_review"

	// Tracker actions: write back to the ticket's issue tracker (the
	// ticketsource integration the ticket was imported from). Skipped on a
	// ticket written by hand — there is nothing to write to.
	TicketTransition = "ticket_transition"
	TicketComment    = "ticket_comment"
	TicketSetField   = "ticket_set_field"
	TicketLink       = "ticket_link"

	// Field write modes (ticket_set_field's mode input).
	ModeReplace = "replace"
	ModeAppend  = "append"

	// Who picks a review round's threads (load_review_comments' select).
	SelectHuman = "human"
	SelectAll   = "all"

	// Sync strategies (sync_target's strategy input).
	StrategyMerge  = "merge"
	StrategyRebase = "rebase"

	// Merge methods (mr_merge's method input; they mirror the ports
	// package's, a test holds them equal) and what becomes of the branch.
	MethodMerge  = "merge"
	MethodSquash = "squash"
	MethodRebase = "rebase"
	BranchKeep   = "keep"
	BranchDelete = "delete"

	// Canonical ticket statuses (ticket_transition's `to`): the flow's words
	// for where the work is, which the ticket's tracker translates into its
	// own. They mirror ports.CanonicalStatuses — the registry stays free of
	// the ports package, and a test holds the two lists equal.
	StatusTodo       = "todo"
	StatusInProgress = "in_progress"
	StatusInReview   = "in_review"
	StatusDone       = "done"
	StatusBlocked    = "blocked"

	RequiresForge = "forge"
	// RequiresTracker: the ticket's tracker must be writable — a ticketsource
	// integration whose adapter implements ports.TicketWriter. Checked per
	// ticket at run creation (the integration is the ticket's, not the
	// project's); a ticket with no tracker passes and the stage skips.
	RequiresTracker = "tracker"

	// ProvidesMR pairs with signalspec.NeedsMR: create_mr is what makes an
	// mr_merged / mr_closed wait reachable.
	ProvidesMR = "merge_request"
	// NeedsMR is the same fact from the consumer's side (ticket_link).
	NeedsMR = ProvidesMR
)

var registry = []Action{{
	Slug:        CreateMR,
	Title:       "Create merge request",
	Description: "Opens a draft merge request for the run's branch. The body artifact's first `# ` heading becomes the title (the ticket title when it has none).",
	Requires:    RequiresForge,
	Single:      true,
	Provides:    ProvidesMR,
	Inputs: []InputSig{{
		Name:        "body",
		Description: "markdown description of the change; its first heading is the MR title",
		Kind:        KindFile,
		Required:    true,
		DefaultPath: "artifacts/mr.md",
	}},
	Outputs: []OutputSig{{
		Name:        "mr_url",
		Description: "link to the created merge request",
		Kind:        KindValue,
		Sentinel:    "$MR_URL",
	}},
}, {
	Slug:        LoadReviewComments,
	Title:       "Load review comments",
	Description: "Writes the merge request's pending review threads — unresolved, last word a reviewer's — to a file the fixing agent reads: file, line, diff hunk, the comments in order, and the thread id a reply names. With select: human (the default) a person first picks which threads to fix in the inbox; the others are skipped until someone comments on them again, and picking none ends the round. Re-entry rewrites the same set, so one phase run fixes what it started with.",
	Requires:    RequiresForge,
	Subject:     true,
	Inputs: []InputSig{{
		Name:        "select",
		Description: "human: a person picks the threads to fix (some may be wrong, or already handled); all: every pending thread",
		Kind:        KindEnum,
		Values:      []string{SelectHuman, SelectAll},
		Default:     SelectHuman,
	}},
	Outputs: []OutputSig{{
		Name:        "review",
		Description: "the pending review threads, as markdown",
		Kind:        KindFile,
		Path:        "artifacts/review.md",
	}},
}, {
	Slug:        ReplyReviewComments,
	Title:       "Reply to review comments",
	Description: "Posts the replies file — one reply per loaded thread, optionally resolving it — on the merge request. Each reply carries an invisible marker, so a crash between two posts is reconciled from the forge and nothing is posted twice.",
	Requires:    RequiresForge,
	Subject:     true,
	Inputs: []InputSig{{
		Name:        "replies",
		Description: "the replies (a review_replies-typed agent output)",
		Kind:        KindFile,
		Required:    true,
		DefaultPath: "artifacts/replies.yaml",
	}},
}, {
	Slug:        SyncTarget,
	Title:       "Take the target branch in",
	Description: "Brings the merge request's target branch into its branch, in the engine: nothing to do when it is already contained; a conflict pauses the merge (or rebase) in the working tree and fails the stage with the files named, for an agent or a human to resolve by editing them — the retried stage continues it.",
	Subject:     true,
	Inputs: []InputSig{{
		Name:        "strategy",
		Description: "merge (no history rewrite) or rebase (linear history; the branch is force-pushed, leased)",
		Kind:        KindEnum,
		Values:      []string{StrategyMerge, StrategyRebase},
		Default:     StrategyMerge,
	}},
}, {
	Slug:        PushBranch,
	Title:       "Push the branch",
	Description: "Pushes the run's branch — every checkout's in the entry phase, the subject's in a reaction phase. A branch the run pushed before is force-pushed leased against the tip it recorded (or, in a reaction phase, the head when the phase started), never blind; a first push is a plain one. What a rebase needs.",
}, {
	Slug:        TicketTransition,
	Title:       "Move the ticket",
	Description: "Moves the ticket's tracker issue. `to` names a canonical status (todo, in_progress, in_review, done, blocked) that the ticket's tracker translates into its own — Jira through the integration's status map, GitHub Issues to open/closed — so one flow fits every tracker; a status this tracker has no word for skips the stage with a note. `status` names the tracker's own status or transition verbatim (matched case-insensitively). Exactly one of the two. Already there: nothing happens. Not reachable from the current status: the stage fails with the statuses that are, for on_fail to route. A ticket written by hand (no tracker issue) skips the stage.",
	Requires:    RequiresTracker,
	Remote:      true,
	ExactlyOne:  []string{"to", "status"},
	Inputs: []InputSig{{
		Name:        "to",
		Description: "a canonical status, translated by the ticket's tracker; one it has no word for skips the stage. Exactly one of to / status",
		Kind:        KindEnum,
		Values:      Statuses,
	}, {
		Name:        "status",
		Description: "the tracker's own status (or transition) name, verbatim. Exactly one of to / status",
		Kind:        KindText,
	}},
}, {
	Slug:        TicketComment,
	Title:       "Comment on the ticket",
	Description: "Posts a markdown artifact as a comment on the ticket's tracker issue, with a short apothecary trailer naming the run and stage — which is also how a crash between posting and recording is reconciled, so nothing is posted twice. A ticket written by hand skips the stage.",
	Requires:    RequiresTracker,
	Remote:      true,
	Inputs: []InputSig{{
		Name:        "body",
		Description: "markdown to post as the comment",
		Kind:        KindFile,
		Required:    true,
		DefaultPath: "artifacts/ticket_comment.md",
	}},
}, {
	Slug:        TicketSetField,
	Title:       "Set a ticket field",
	Description: "Writes one field of the ticket's tracker issue, named as the tracker shows it (\"Feature flags\", \"labels\"). The value comes from an artifact (value) or is written in the flow (text) — exactly one. mode: replace overwrites; append merges comma-separated items into what is there (a list field grows, nothing is duplicated). A field the tracker cannot write from text fails the stage for on_fail. A ticket written by hand skips the stage.",
	Requires:    RequiresTracker,
	Remote:      true,
	ExactlyOne:  []string{"value", "text"},
	Inputs: []InputSig{{
		Name:        "field",
		Description: "the field's display name (or its raw id), as the tracker shows it",
		Kind:        KindText,
		Required:    true,
	}, {
		Name:        "value",
		Description: "an artifact whose text is the value (comma-separated for list fields); exactly one of value / text",
		Kind:        KindFile,
		DefaultPath: "artifacts/ticket_field.txt",
	}, {
		Name:        "text",
		Description: "the value written in the flow itself; exactly one of value / text",
		Kind:        KindText,
	}, {
		Name:        "mode",
		Description: "replace: overwrite the field; append: merge comma-separated items into the current value",
		Kind:        KindEnum,
		Values:      []string{ModeReplace, ModeAppend},
		Default:     ModeReplace,
	}},
}, {
	Slug:        TicketLink,
	Title:       "Link the merge request on the ticket",
	Description: "Attaches every merge request the run opened to the ticket's tracker issue (a remote link on Jira, a comment on GitHub), titled with the branch. Idempotent: re-entering never attaches twice. Needs a create_mr earlier on the route; with no merge request the stage fails with input_missing. A ticket written by hand skips the stage.",
	Requires:    RequiresTracker,
	Remote:      true,
	Needs:       NeedsMR,
}, {
	Slug:        MRMerge,
	Title:       "Merge the merge request",
	Description: "Merges the phase's merge request on the forge — only at the head the phase began on, so nothing pushed after the decision is merged unseen. The forge's own rules apply: a merge it declines (a draft, checks or approvals missing, the head moved, a method it does not offer) fails the stage with its reason, for on_fail to route. After a crash the merge request's state says whether it already happened. Needs no workspace.",
	Requires:    RequiresForge,
	Subject:     true,
	Remote:      true,
	Inputs: []InputSig{{
		Name:        "method",
		Description: "merge (a merge commit), squash (one commit) or rebase (the branch's commits on top of the target; GitHub only — GitLab merges by the project's merge method)",
		Kind:        KindEnum,
		Values:      []string{MethodMerge, MethodSquash, MethodRebase},
		Default:     MethodMerge,
	}, {
		Name:        "branch",
		Description: "keep the merge request's branch, or delete it once merged",
		Kind:        KindEnum,
		Values:      []string{BranchKeep, BranchDelete},
		Default:     BranchKeep,
	}},
}, {
	Slug:        PostReview,
	Title:       "Post a review",
	Description: "Posts a review on the phase's merge request from a review file (a review_threads-typed agent output): a note on each line it names, and its summary. A note on a line outside the change joins the summary, quoted with its place. Never approves nor requests changes — that decision stays a person's. Every part is marked, so a retry after a crash posts only what is missing.",
	Requires:    RequiresForge,
	Subject:     true,
	Inputs: []InputSig{{
		Name:        "review",
		Description: "the review (a review_threads-typed agent output)",
		Kind:        KindFile,
		Required:    true,
		DefaultPath: "artifacts/review.yaml",
	}},
}}

// Registry returns every registered action (a copy: callers may sort it).
func Registry() []Action { return slices.Clone(registry) }

// Get returns the action with the given slug, or nil.
func Get(slug string) *Action {
	for i := range registry {
		if registry[i].Slug == slug {
			return &registry[i]
		}
	}
	return nil
}

// Slugs lists the registered slugs for error messages.
func Slugs() string {
	names := make([]string, 0, len(registry))
	for i := range registry {
		names = append(names, registry[i].Slug)
	}
	return strings.Join(names, "|")
}

// Input returns the named input signature, or nil.
func (a *Action) Input(name string) *InputSig {
	for i := range a.Inputs {
		if a.Inputs[i].Name == name {
			return &a.Inputs[i]
		}
	}
	return nil
}

// Output returns the named output signature, or nil.
func (a *Action) Output(name string) *OutputSig {
	for i := range a.Outputs {
		if a.Outputs[i].Name == name {
			return &a.Outputs[i]
		}
	}
	return nil
}

// InputNames lists the action's input names for error messages.
func (a *Action) InputNames() string {
	names := make([]string, 0, len(a.Inputs))
	for i := range a.Inputs {
		names = append(names, a.Inputs[i].Name)
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, "|")
}
