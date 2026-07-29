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
}

// OutputSig is one thing the action produces.
type OutputSig struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Kind        Kind   `json:"kind"`
	// Sentinel is the flow-language token later stages use to consume a
	// value output (KindValue only).
	Sentinel string `json:"sentinel,omitempty"`
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
	Provides string      `json:"provides,omitempty"`
	Inputs   []InputSig  `json:"inputs,omitempty"`
	Outputs  []OutputSig `json:"outputs,omitempty"`
}

// Action slugs and the port kinds they require.
const (
	CreateMR = "create_mr"

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
