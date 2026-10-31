// Package signalspec is the registry of things a flow can react to: "the
// merge request conflicts", "review comments settled", "it merged". A signal
// is something that happens *to* the run from outside it.
//
// A signal says what it is and nothing about what it does (FLOWLANG §1): the
// flow decides that by where it names it —
//
//	watchers: [{ on: mr_conflicts, … }]   — a watcher runs its chain on each new occurrence
//	outcomes: [{ on: mr_merged, … }]      — an outcome fires once and ends the run
//	type: wait, for: mr_mergeable          — a wait stage parks its chain until it holds
//
// One entry is not observed: manual, a watcher a person opens from the run
// page. Every other signal is observed on the run's merge requests, and any
// of them may be watched, be an outcome or be waited on. A
// run itself begins by someone's decision — a person or an automation starts
// it, and its main chain runs — never on a signal.
//
// The gate's auto-answer exists beside `wait` because a final gate is a race
// (a human clicks approve, OR they merge and apothecary notices), and the
// engine runs one stage at a time, so the race has to live inside the gate.
//
// Signals are apothecary-defined, never project data: the registry is compiled
// in, so a flow naming an unknown signal is a parse error. Adding one is an
// entry here plus a poller in the engine.
//
// This package is a leaf: stdlib only. Requires is a port-kind string the
// engine matches, keeping the registry free of adapter types.
package signalspec

import (
	"slices"
	"strings"
)

// Signal is one thing a flow can react to.
type Signal struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// Kind is what the signal is: KindHand (manual, a person's act) or
	// KindObserved (a fact about the run's merge requests the engine watches
	// for).
	Kind string `json:"kind"`
	// Requires names an integration port the project must have configured for
	// a flow using this signal to start; empty = none needed.
	Requires string `json:"requires,omitempty"`
	// Needs names a run fact the signal depends on, so a flow that waits for
	// something that can never happen is caught rather than hanging.
	Needs string `json:"needs,omitempty"`
	// Debounced marks a signal that fires on activity and so accepts a
	// quiet_for: the watcher starts once the activity has stopped.
	Debounced bool `json:"debounced,omitempty"`
	// All marks a signal that, as an outcome or a wait on the whole run,
	// holds once it holds on every merge request of the run — a merged one
	// counts as approved and as mergeable. Any other holds as soon as one
	// merge request has it, and that one is the outcome's subject.
	All bool `json:"all,omitempty"`
}

// Signal kinds.
const (
	KindHand     = "hand"
	KindObserved = "observed"
)

// Observed reports whether the engine watches for the signal on the run's
// merge requests — anything but manual.
func (s *Signal) Observed() bool { return s != nil && s.Kind == KindObserved }

// Signal slugs, the port kinds they require, and the run facts they need.
const (
	// Manual is the start a person gives from the run page: a watcher on it
	// never fires on its own.
	Manual = "manual"

	MRMerged   = "mr_merged"
	MRClosed   = "mr_closed"
	MRApproved = "mr_approved"

	MRConflicts     = "mr_conflicts"
	MRReviewSettled = "mr_review_settled"
	CIFailed        = "ci_failed"
	MRMergeable     = "mr_mergeable"
	MRHeadMoved     = "mr_head_moved"

	RequiresRepository = "repository"

	// NeedsMR: the run must have a merge request (one it opened with
	// create_mr, or the one it was started on) before this signal can hold.
	NeedsMR = "merge_request"
)

var registry = []Signal{{
	Slug:        Manual,
	Title:       "Started by hand",
	Description: "Never fires on its own: a person starts the watcher from the run page while the run is idle.",
	Kind:        KindHand,
}, {
	Slug:        MRMerged,
	Title:       "Merge request merged",
	Description: "As an outcome or a wait: every merge request of the run is merged. As a watcher: one of them merged — once per merge request.",
	Kind:        KindObserved,
	Requires:    RequiresRepository,
	Needs:       NeedsMR,
	All:         true,
}, {
	Slug:        MRClosed,
	Title:       "Merge request closed unmerged",
	Description: "One of the run's merge requests was closed without being merged — someone decided against it. Once per merge request; that one is the chain's subject.",
	Kind:        KindObserved,
	Requires:    RequiresRepository,
	Needs:       NeedsMR,
}, {
	Slug:        MRApproved,
	Title:       "Merge request approved",
	Description: "Approved by at least one reviewer with no request for changes outstanding (GitHub), or its approval rules met (GitLab). As an outcome or a wait: every merge request of the run (or merged). As a watcher: one of them, once per head it was approved at — a push after the approval is a new occurrence.",
	Kind:        KindObserved,
	Requires:    RequiresRepository,
	Needs:       NeedsMR,
	All:         true,
}, {
	Slug:        MRConflicts,
	Title:       "Merge request conflicts with its target",
	Description: "The repository reports the merge request cannot merge cleanly into its target. Once per (target tip, head) pair: a declined or finished fix is not offered again until either moves.",
	Kind:        KindObserved,
	Requires:    RequiresRepository,
	Needs:       NeedsMR,
}, {
	Slug:        MRReviewSettled,
	Title:       "Review comments settled",
	Description: "The merge request has unresolved review threads whose last word is a reviewer's, and the newest of those comments is older than quiet_for: someone reviewed, then stopped.",
	Kind:        KindObserved,
	Requires:    RequiresRepository,
	Needs:       NeedsMR,
	Debounced:   true,
}, {
	Slug:        CIFailed,
	Title:       "CI failed",
	Description: "A CI check on the merge request's head commit has failed (GitHub check runs and commit statuses, GitLab's head pipeline). Once per head and set of failed checks: a push, or another check failing, is a new occurrence.",
	Kind:        KindObserved,
	Requires:    RequiresRepository,
	Needs:       NeedsMR,
}, {
	Slug:        MRMergeable,
	Title:       "Merge request can be merged",
	Description: "The repository says it would merge the merge request now: no conflict, its required approvals and checks in, not a draft. The repository's own verdict, so the project's rules decide. As an outcome or a wait: every merge request of the run (or merged). As a watcher: once per head.",
	Kind:        KindObserved,
	Requires:    RequiresRepository,
	Needs:       NeedsMR,
	All:         true,
}, {
	Slug:        MRHeadMoved,
	Title:       "Someone pushed to the merge request",
	Description: "The merge request's head is a commit the run did not push, open the merge request at, or already act on — someone else pushed. With quiet_for, only once the head has stood still that long. Once per head.",
	Kind:        KindObserved,
	Requires:    RequiresRepository,
	Needs:       NeedsMR,
	Debounced:   true,
}}

// Registry returns every registered signal (a copy: callers may sort it).
func Registry() []Signal { return slices.Clone(registry) }

// Get returns the signal with the given slug, or nil.
func Get(slug string) *Signal {
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
