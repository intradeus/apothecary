package flow

import (
	"fmt"
	"strings"

	"apothecary/internal/agentspec"
)

// ValidateBindings checks each agent stage's input/output bindings against its
// agent's declared I/O contract. specs is the caller's agent view (the project
// overlay on the built-in library); a stage whose agent has no spec entry is
// skipped — an unknown agent is preflight's separate error, not this pass's.
//
// errs gate run creation (wired at engine.preflight). warns are advisory
// (surfaced by /api/flows/validate); they never block a save, because flows
// are project-independent and the agents they bind are mutable per-project
// state — the authoritative check is at run creation.
//
// Severity is monotonic with the new map syntax: a stage that never adopts the
// map form can only ever produce warnings, so no pre-existing flow starts
// failing preflight from this pass alone.
func ValidateBindings(f *Flow, specs map[string]*agentspec.Spec) (errs []*Error, warns []string) {
	// Every agent stage's effective output paths, for the rendezvous check.
	produced := map[string]bool{}
	for i := range f.Stages {
		st := &f.Stages[i]
		if st.Type != StageAgent {
			continue
		}
		for _, o := range EffectiveOutputs(st, specs[st.Agent]) {
			if o.Value != "" {
				produced[o.Value] = true
			}
		}
	}

	for i := range f.Stages {
		st := &f.Stages[i]
		if st.Type != StageAgent {
			continue
		}
		spec := specs[st.Agent]
		if spec == nil {
			continue // unknown agent: preflight reports it separately
		}
		errf := func(format string, args ...any) {
			errs = append(errs, &Error{File: f.Name, Line: st.Line, Msg: fmt.Sprintf("stage %q: %s", st.ID, fmt.Sprintf(format, args...))})
		}
		warnf := func(format string, args ...any) {
			warns = append(warns, fmt.Sprintf("stage %q: %s", st.ID, fmt.Sprintf(format, args...)))
		}
		validateStageInputs(st, spec, errf, warnf)
		validateStageOutputs(st, spec, errf, warnf)

		// Rendezvous soft check: a bound input path under artifacts/ that no
		// agent stage produces is probably a typo or a missing producer.
		// Restricted to artifacts/ — inputs may legitimately name pre-existing
		// repo files (docs/spec.md), which have no producing stage.
		for _, in := range st.Inputs {
			if in.Name == "" || in.Value == InputChanged {
				continue
			}
			if strings.HasPrefix(in.Value, "artifacts/") && !produced[in.Value] {
				warnf("input %q binds %q, which no agent stage produces", in.Name, in.Value)
			}
		}
	}
	return errs, warns
}

func validateStageInputs(st *Stage, spec *agentspec.Spec, errf, warnf func(string, ...any)) {
	mapForm := stageUsesMapForm(st.Inputs)
	if mapForm {
		if len(spec.Inputs) == 0 {
			warnf("agent %q declares no inputs; named bindings render generically", st.Agent)
			return
		}
		bound := map[string]bool{}
		for _, in := range st.Inputs {
			if !specHasInput(spec, in.Name) {
				errf("input %q is not declared by agent %q", in.Name, st.Agent)
				continue
			}
			bound[in.Name] = true
		}
		for _, d := range spec.Inputs {
			if d.Optional || d.Name == agentspec.InputFeedback {
				continue
			}
			if !bound[d.Name] {
				errf("required input %q of agent %q is not bound", d.Name, st.Agent)
			}
		}
		return
	}
	// Legacy list or absent: warnings only, never errors.
	for _, d := range spec.Inputs {
		if d.Optional || d.Name == agentspec.InputFeedback {
			continue
		}
		warnf("agent %q declares required input %q, but the stage uses no named bindings", st.Agent, d.Name)
	}
}

func validateStageOutputs(st *Stage, spec *agentspec.Spec, errf, warnf func(string, ...any)) {
	mapForm := stageUsesMapForm(st.Outputs)
	hasLegacy := len(st.Outputs) == 1 && st.Outputs[0].Name == ""

	switch {
	case len(spec.Outputs) == 0:
		if mapForm {
			warnf("agent %q declares no outputs; bound paths are enforced literally", st.Agent)
		}
	case hasLegacy && len(spec.Outputs) > 1:
		errf("single output is ambiguous: agent %q declares %d outputs, use the {name: path} form", st.Agent, len(spec.Outputs))
	case mapForm:
		for _, o := range st.Outputs {
			if !specHasOutput(spec, o.Name) {
				errf("output %q is not declared by agent %q", o.Name, st.Agent)
			}
		}
	}

	// Duplicate effective output paths would make the MUST-write contract
	// self-contradictory; caught regardless of decls.
	seen := map[string]bool{}
	for _, o := range EffectiveOutputs(st, spec) {
		if o.Value == "" {
			continue
		}
		if seen[o.Value] {
			errf("output path %q is bound more than once", o.Value)
		}
		seen[o.Value] = true
	}
}

// EffectiveOutputs resolves the set of files an agent stage must produce: the
// agent's declared output default paths, overlaid with the stage's rebinds.
// A nil spec (or an agent with no output decls) means the stage's literal
// bindings stand alone — so the engine degrades gracefully if agent decls
// change after a run's preflight.
func EffectiveOutputs(st *Stage, spec *agentspec.Spec) []IOBinding {
	if spec == nil || len(spec.Outputs) == 0 {
		return st.Outputs
	}
	rebinds := map[string]string{} // decl name -> rebound path
	var legacy string              // the single unnamed output, if present
	var extra []IOBinding          // named rebinds not matching any decl
	for _, b := range st.Outputs {
		if b.Name == "" {
			legacy = b.Value
			continue
		}
		if specHasOutput(spec, b.Name) {
			rebinds[b.Name] = b.Value
		} else {
			extra = append(extra, b)
		}
	}
	out := make([]IOBinding, 0, len(spec.Outputs)+len(extra))
	for _, d := range spec.Outputs {
		path := d.Path
		if p, ok := rebinds[d.Name]; ok {
			path = p
		} else if legacy != "" && len(spec.Outputs) == 1 {
			// A legacy single output rebinds the agent's sole declared output.
			path = legacy
		}
		out = append(out, IOBinding{Name: d.Name, Value: path})
	}
	out = append(out, extra...)
	if legacy != "" && len(spec.Outputs) != 1 {
		// Degenerate (a validation error), but still enforce what was written.
		out = append(out, IOBinding{Value: legacy})
	}
	return out
}

// stageUsesMapForm reports whether bindings are the named map form. A stage's
// bindings are homogeneous (all named or all unnamed); an empty set is neither
// and is treated as absent by callers.
func stageUsesMapForm(bindings []IOBinding) bool {
	return len(bindings) > 0 && bindings[0].Name != ""
}

func specHasInput(spec *agentspec.Spec, name string) bool {
	for _, d := range spec.Inputs {
		if d.Name == name {
			return true
		}
	}
	return false
}

func specHasOutput(spec *agentspec.Spec, name string) bool {
	for _, d := range spec.Outputs {
		if d.Name == name {
			return true
		}
	}
	return false
}
