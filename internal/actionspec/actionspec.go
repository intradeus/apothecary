// Package actionspec is the registry of apothecary's built-in actions: the
// integrations a flow can invoke as a stage without an agent (FLOWLANG §6) —
// "create the merge request", and in future "pull the MR comments", "post to
// the channel", "transition the ticket".
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
	// Single caps the action at one stage per flow (a run has one MR).
	Single bool `json:"single,omitempty"`
	// Provides names the run fact this action establishes, mirroring a
	// signal's Needs (signalspec): the validator pairs them to catch a wait
	// whose prerequisite no path creates.
	Provides string `json:"provides,omitempty"`
	// Subject marks an action that acts on one merge request — the phase's
	// subject — and so only makes sense in a reaction phase (PLAN6 R12).
	Subject bool        `json:"subject,omitempty"`
	Inputs  []InputSig  `json:"inputs,omitempty"`
	Outputs []OutputSig `json:"outputs,omitempty"`
}

// Action slugs and the port kinds they require.
const (
	CreateMR            = "create_mr"
	LoadReviewComments  = "load_review_comments"
	ReplyReviewComments = "reply_review_comments"
	SyncTarget          = "sync_target"
	PushBranch          = "push_branch"

	// Who picks a review round's threads (load_review_comments' select).
	SelectHuman = "human"
	SelectAll   = "all"

	// Sync strategies (sync_target's strategy input).
	StrategyMerge  = "merge"
	StrategyRebase = "rebase"

	RequiresForge = "forge"

	// ProvidesMR pairs with signalspec.NeedsMR: create_mr is what makes an
	// mr_merged / mr_closed wait reachable.
	ProvidesMR = "merge_request"
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
