// Package harnessspec is the registry of harness options: the settings only
// one harness understands that an agent stage gives under `harness_options:`,
// keyed by harness kind — omp's advisor, its smol and slow models.
//
// Like actionspec it is compiled in, so a flow naming an unknown kind or
// option is a parse error. Only the block of the kind a stage resolves to
// applies: a stage on `harness: $harness` may carry blocks for several kinds,
// and the run picks one when the parameter is bound. Values may read flow
// $parameters exactly as a stage's model does.
//
// Adding an option to a harness: one Option row here, one case in that kind's
// renderer (omp: omp.RenderOptions), and one readback assertion in that
// kind's conformance test. The parser, JSON Schema, parameter roles, unbound
// checks, model checks, API and editor all read this registry.
//
// Adding a harness kind: one Harness entry here and a Profile.OptionArgs in
// its adapter. TestHarnessOptionsMatchTheirHarnesses (internal/project/kinds)
// fails until both exist.
//
// Adding an option kind (bool, int, duration…): a Kind constant here, its
// value rule in the parser's buildHarnessOptions, its schema in
// harnessOptionsSchema, and its row in the editor's AgentStage.svelte.
// Scalars already decode into DocStage.HarnessOptions (yaml.v3 sets any
// scalar's text into a string); a list- or map-valued option needs that
// field's type changed.
//
// Options a harness takes per session rather than per process (an ACP
// session/set_config_option) are not rendered by anything yet: they need a
// second hook beside acpagent.Profile.OptionArgs and a field on
// sidecar.SessionRequest. Every option registered today is per process.
//
// This package is a leaf: stdlib only.
package harnessspec

import (
	"slices"
	"strings"
)

// Kind is what a harness option is.
type Kind string

const (
	// KindModel is a provider/model the harness lists. Like a stage's model
	// it may read a $parameter with no value yet; the run is refused until
	// it has one, when the stage runs on that harness.
	KindModel Kind = "model"
	// KindEnum is one of Values, or an enum $parameter within them.
	KindEnum Kind = "enum"
)

// Option is one setting a harness takes.
type Option struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Kind        Kind   `json:"kind"`
	// Values are the literals a KindEnum option accepts.
	Values []string `json:"values,omitempty"`
	// Needs names another option of the same block this one means nothing
	// without (advisor_review without an advisor).
	Needs string `json:"needs,omitempty"`
}

// Harness is the options one harness kind takes.
type Harness struct {
	Kind    string   `json:"kind"`
	Options []Option `json:"options"`
}

// Harness kinds and their option names.
const (
	Omp              = "omp"
	OmpAdvisor       = "advisor"
	OmpAdvisorReview = "advisor_review"
	OmpSmol          = "smol"
	OmpSlow          = "slow"
)

var registry = []Harness{{
	Kind: Omp,
	Options: []Option{{
		Name:        OmpAdvisor,
		Description: "a second model that reviews the agent's work as it goes and injects notes into its session (omp's advisor); its spend is part of the session's",
		Kind:        KindModel,
	}, {
		Name:        OmpAdvisorReview,
		Description: "when the advisor reviews: every turn (turn, omp's default), or only when the agent ends (agent-end, far cheaper)",
		Kind:        KindEnum,
		Values:      []string{"turn", "agent-end"},
		Needs:       OmpAdvisor,
	}, {
		Name:        OmpSmol,
		Description: "omp's smol role: the fast model omp hands lightweight side work to",
		Kind:        KindModel,
	}, {
		Name:        OmpSlow,
		Description: "omp's slow role: the model omp hands thorough reasoning work to",
		Kind:        KindModel,
	}},
}}

// Registry returns every harness that takes options (a copy: callers may sort it).
func Registry() []Harness { return slices.Clone(registry) }

// Get returns the options of the given harness kind, or nil when it takes none.
func Get(kind string) *Harness {
	for i := range registry {
		if registry[i].Kind == kind {
			return &registry[i]
		}
	}
	return nil
}

// Option returns the named option, or nil (also on a nil Harness).
func (h *Harness) Option(name string) *Option {
	if h == nil {
		return nil
	}
	for i := range h.Options {
		if h.Options[i].Name == name {
			return &h.Options[i]
		}
	}
	return nil
}

// Names lists the harness's option names for error messages.
func (h *Harness) Names() string {
	names := make([]string, 0, len(h.Options))
	for _, o := range h.Options {
		names = append(names, o.Name)
	}
	return strings.Join(names, ", ")
}

// Kinds lists the harness kinds that take options for error messages
// ("none" when empty).
func Kinds() string {
	if len(registry) == 0 {
		return "none"
	}
	kinds := make([]string, 0, len(registry))
	for _, h := range registry {
		kinds = append(kinds, h.Kind)
	}
	return strings.Join(kinds, ", ")
}
