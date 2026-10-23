// Package signalspec is the registry of external things a flow can wait for:
// "the merge request merged", and in future "CI went green", "the ticket moved
// to Done". A signal is something that happens *to* the run from outside it.
//
// Two stages use this registry (FLOWLANG §2):
//
//	type: wait, for: mr_merged                — park the phase until the signal holds
//	type: human_gate, auto_approve/auto_reject — the signal answers the gate for you
//
// A phase's `on:` (FLOWLANG §1) names a signal too: the wait point that
// opens it. Those carry Trigger (may open a REACTION while the merge request
// is open, any number of times), Outcome (may open an OUTCOME, once: the
// phase ends the run) or Entry (may begin the run itself). `manual` is the
// one Trigger that never fires on its own — a person gives it from the run
// page — and `run_start` the one Entry: a run is created for the phase, by a
// person or an automation. A signal that is not a Trigger or an Entry is
// waitable: a wait stage parks on it, a gate's auto-answer takes it — the
// outcome signals among them, so a flow may park on the merge instead of
// ending on it.
//
// The second is why both exist rather than only `wait`: a final gate is a race
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

// Signal is one waitable external condition.
type Signal struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// Requires names an integration port the project must have configured for
	// a flow using this signal to start; empty = none needed.
	Requires string `json:"requires,omitempty"`
	// Needs names a run fact the signal depends on, so a flow that waits for
	// something that can never happen is caught rather than hanging.
	Needs string `json:"needs,omitempty"`
	// Trigger marks a signal that opens a phase (PLAN6) rather than
	// answering a gate or ending a wait: it describes something to react to
	// while the merge request is open, not an outcome. Manual carries it
	// too, as the trigger that never fires on its own.
	Trigger bool `json:"trigger,omitempty"`
	// Entry marks a signal that begins the run itself: a phase on it is an
	// entrypoint a run is created at. RunStart is the one today.
	Entry bool `json:"entry,omitempty"`
	// Outcome marks a signal that ends the run: a phase on it is an outcome
	// — it fires once, with the start policies a trigger takes, and must end
	// the run (done or abort). Still waitable: a wait stage may park on it.
	Outcome bool `json:"outcome,omitempty"`
	// Debounced marks a trigger that fires on activity and so accepts a
	// quiet_for: the phase starts once the activity has stopped.
	Debounced bool `json:"debounced,omitempty"`
}

// Signal slugs, the port kinds they require, and the run facts they need.
const (
	// RunStart is the entrypoint's beginning: a run is created for the phase
	// (a person starts one from a ticket, or an automation does). A flow
	// opens at least one phase on it; several are allowed, and the creator
	// picks.
	RunStart = "run_start"
	// Manual is the beginning a person gives from the run page; it never
	// fires on its own, and a phase on it starts no other way.
	Manual = "manual"

	MRMerged = "mr_merged"
	MRClosed = "mr_closed"

	// Phase triggers.
	MRConflicts     = "mr_conflicts"
	MRReviewSettled = "mr_review_settled"

	RequiresForge = "forge"

	// NeedsMR: the run must have opened a merge request (a create_mr action)
	// before this signal can ever fire.
	NeedsMR = "merge_request"
)

var registry = []Signal{{
	Slug:        RunStart,
	Title:       "Run started",
	Description: "A run is created for this phase — a person starts one from a ticket, or an automation does. Marks the phase as an entrypoint; a flow has at least one, and the start-run form picks among several.",
	Entry:       true,
}, {
	Slug:        Manual,
	Title:       "Started by hand",
	Description: "Never fires on its own: a person starts the phase from the run page while the run is idle, and it starts no other way.",
	Trigger:     true,
}, {
	Slug:        MRMerged,
	Title:       "Merge request merged",
	Description: "Holds once every merge request the run opened is merged on the forge. A phase on it is an outcome: it fires once and ends the run; a wait stage may park on it instead.",
	Requires:    RequiresForge,
	Needs:       NeedsMR,
	Outcome:     true,
}, {
	Slug:        MRClosed,
	Title:       "Merge request closed unmerged",
	Description: "Holds as soon as one of the run's merge requests is closed without being merged — someone decided against it on the forge. A phase on it is an outcome, acting on the closed merge request.",
	Requires:    RequiresForge,
	Needs:       NeedsMR,
	Outcome:     true,
}, {
	Slug:        MRConflicts,
	Title:       "Merge request conflicts with its target",
	Description: "Holds while the forge reports the merge request cannot merge cleanly into its target. Once per (target tip, head) pair: a declined or finished fix is not offered again until either moves.",
	Requires:    RequiresForge,
	Needs:       NeedsMR,
	Trigger:     true,
}, {
	Slug:        MRReviewSettled,
	Title:       "Review comments settled",
	Description: "Holds when the merge request has unresolved review threads whose last word is a reviewer's, and the newest of those comments is older than quiet_for: someone reviewed, then stopped.",
	Requires:    RequiresForge,
	Needs:       NeedsMR,
	Trigger:     true,
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
