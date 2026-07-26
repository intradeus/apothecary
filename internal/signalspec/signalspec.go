// Package signalspec is the registry of external things a flow can wait for:
// "the merge request merged", and in future "CI went green", "the ticket moved
// to Done". A signal is something that happens *to* the run from outside it.
//
// Two stages use this registry (FLOWLANG §3, §6):
//
//	type: wait, for: mr_merged      — block until the signal fires
//	type: human_gate, auto_answer:  — the signal approves the gate for you
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
}

// Signal slugs, the port kinds they require, and the run facts they need.
const (
	MRMerged = "mr_merged"
	MRClosed = "mr_closed"

	RequiresForge = "forge"

	// NeedsMR: the run must have opened a merge request (a create_mr action)
	// before this signal can ever fire.
	NeedsMR = "merge_request"
)

var registry = []Signal{{
	Slug:        MRMerged,
	Title:       "Merge request merged",
	Description: "Fires when the run's merge request is merged on the forge.",
	Requires:    RequiresForge,
	Needs:       NeedsMR,
}, {
	Slug:        MRClosed,
	Title:       "Merge request closed unmerged",
	Description: "Fires when the run's merge request is closed without being merged — someone decided against it on the forge.",
	Requires:    RequiresForge,
	Needs:       NeedsMR,
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
