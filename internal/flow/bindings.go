package flow

import (
	"fmt"
	"strings"

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
)

// ValidateBindings is the flow language's second pass (FLOWLANG §7): it checks
// each stage's bindings against the contract on the other side — an agent's
// declared inputs/outputs, or an action's signature — and checks that every
// path a stage consumes is produced by some stage.
//
// It is separate from Parse because it needs data Parse cannot see: agents are
// per-project, flows are project-independent. specs is the caller's agent view
// (the project overlay on the built-in library); a stage whose agent has no
// spec entry is skipped — an unknown agent is preflight's separate error.
//
// errs gate run creation (wired at engine.preflight). warns are advisory
// (surfaced by /api/flows/validate); they never block a save, because the
// agents a flow binds are mutable per-project state and the authoritative
// check is at run creation.
func ValidateBindings(f *Flow, specs map[string]*agentspec.Spec) (errs []*Error, warns []string) {
	produced := producedPaths(f, specs)

	for i := range f.Stages {
		st := &f.Stages[i]
		errf := func(format string, args ...any) {
			errs = append(errs, &Error{File: f.Name, Line: st.Line, Msg: fmt.Sprintf("stage %q: %s", st.ID, fmt.Sprintf(format, args...))})
		}
		warnf := func(format string, args ...any) {
			warns = append(warns, fmt.Sprintf("stage %q: %s", st.ID, fmt.Sprintf(format, args...)))
		}

		if st.Type == StageAgent {
			spec := specs[st.Agent]
			if spec == nil {
				continue // unknown agent: preflight reports it separately
			}
			validateStageInputs(st, spec, errf)
			validateStageOutputs(st, spec, errf, warnf)
			// An on_reject edge only ever fires on a REJECT verdict, and only a
			// verdict-typed output produces one. Warning, not error: the flow may
			// be pointing at an agent that is about to declare it.
			if st.Edges[EdgeReject] != nil && spec.VerdictOutput() == nil {
				warnf("on_reject will never fire: agent %q declares no %s output", st.Agent, agentspec.OutputTypeVerdict)
			}
			validateFeedBindings(f, st, spec, errf, warnf)
		}

		// Rendezvous: every path a stage consumes should be produced by some
		// stage. Restricted to artifacts/ — a stage may legitimately read
		// pre-existing repo files (docs/spec.md), which have no producer.
		for _, in := range consumedPaths(st) {
			if strings.HasPrefix(in.Value, "artifacts/") && !produced[in.Value] {
				if in.Name != "" {
					warnf("input %q binds %q, which no stage produces", in.Name, in.Value)
				} else {
					warnf("consumes %q, which no stage produces", in.Value)
				}
			}
		}
	}
	return errs, warns
}

// producedPaths collects every artifact path the flow's stages produce: agent
// effective outputs and command declared outputs.
func producedPaths(f *Flow, specs map[string]*agentspec.Spec) map[string]bool {
	produced := map[string]bool{}
	for i := range f.Stages {
		st := &f.Stages[i]
		switch st.Type {
		case StageAgent:
			for _, o := range EffectiveOutputs(st, specs[st.Agent]) {
				if o.Value != "" {
					produced[o.Value] = true
				}
			}
		case StageCommand:
			for _, o := range st.Outputs {
				if o.Value != "" {
					produced[o.Value] = true
				}
			}
		}
	}
	return produced
}

// consumedPaths lists the artifact paths a stage reads. Every stage type binds
// the same way, so this is one loop plus a per-type filter: an action's value
// inputs are not files, and sentinels are engine-provided rather than produced
// by another stage.
func consumedPaths(st *Stage) []IOBinding {
	act := actionspec.Get(st.Action)
	if st.Type == StageAction && act == nil {
		return nil
	}
	var out []IOBinding
	for _, in := range st.Inputs {
		if IsSentinel(in.Value) {
			continue
		}
		if act != nil {
			if sig := act.Input(in.Name); sig == nil || sig.Kind != actionspec.KindFile {
				continue
			}
		}
		out = append(out, in)
	}
	return out
}

// validateFeedBindings checks the edge-carried inputs ($DECISION, $FAILURE):
// they are empty on a stage's first attempt, so the decl must be optional, and
// they only ever fill if some edge of the matching kind actually targets this
// stage — a binding no edge can fill is dead weight worth flagging.
func validateFeedBindings(f *Flow, st *Stage, spec *agentspec.Spec, errf, warnf func(string, ...any)) {
	// Which feed sentinels can reach this stage, from any edge in the flow.
	reachable := map[string]bool{}
	for i := range f.Stages {
		for kind, e := range f.Stages[i].Edges {
			for link := e; link != nil; link = link.Then {
				if link.To == st.ID {
					reachable[FeedSentinel(kind)] = true
				}
			}
		}
	}
	for _, in := range st.Inputs {
		if !IsFeedSentinel(in.Value) {
			continue
		}
		for _, d := range spec.Inputs {
			if d.Name == in.Name && !d.Optional {
				errf("input %q binds %s, which is empty on the first attempt: declare it optional on agent %q",
					in.Name, in.Value, st.Agent)
			}
		}
		if !reachable[in.Value] {
			kind := EdgeReject
			if in.Value == SentinelFailure {
				kind = "on_fail/on_timeout"
			}
			warnf("input %q binds %s, but no %s edge targets this stage — nothing will ever fill it",
				in.Name, in.Value, kind)
		}
	}
}

func validateStageInputs(st *Stage, spec *agentspec.Spec, errf func(string, ...any)) {
	bound := map[string]bool{}
	for _, in := range st.Inputs {
		if !specHasInput(spec, in.Name) {
			errf("input %q is not declared by agent %q", in.Name, st.Agent)
			continue
		}
		bound[in.Name] = true
	}
	for _, d := range spec.Inputs {
		if d.Optional {
			continue
		}
		if !bound[d.Name] {
			errf("required input %q of agent %q is not bound", d.Name, st.Agent)
		}
	}
}

func validateStageOutputs(st *Stage, spec *agentspec.Spec, errf, warnf func(string, ...any)) {
	for _, o := range st.Outputs {
		if !specHasOutput(spec, o.Name) {
			errf("output %q is not declared by agent %q (a stage may rebind a declared output's path, not add one)", o.Name, st.Agent)
		}
	}
	if len(st.Outputs) > 0 && len(spec.Outputs) == 0 {
		warnf("agent %q declares no outputs", st.Agent)
	}

	// Duplicate effective output paths would make the MUST-write contract
	// self-contradictory.
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

// EffectiveOutputs resolves the set of files a stage must produce: for an agent,
// its declared output default paths overlaid with the stage's rebinds; for any
// other stage (or a nil spec) the stage's literal bindings. A nil spec means the
// engine degrades gracefully if an agent's decls change after a run's preflight.
func EffectiveOutputs(st *Stage, spec *agentspec.Spec) []IOBinding {
	if spec == nil || len(spec.Outputs) == 0 {
		return st.Outputs
	}
	rebinds := map[string]string{} // decl name -> rebound path
	var extra []IOBinding          // bindings matching no decl (a validation error)
	for _, b := range st.Outputs {
		if b.Name != "" && specHasOutput(spec, b.Name) {
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
		}
		out = append(out, IOBinding{Name: d.Name, Value: path})
	}
	return append(out, extra...)
}

// VerdictPath returns where a stage's verdict must be written: the path of the
// agent's verdict-typed output, honouring a stage rebind. Empty when the agent
// renders no verdict — the single resolution used by prompt assembly, the
// post-run read and crash recovery, so they can never disagree.
func VerdictPath(st *Stage, spec *agentspec.Spec) string {
	d := spec.VerdictOutput()
	if d == nil {
		return ""
	}
	for _, o := range EffectiveOutputs(st, spec) {
		if o.Name == d.Name {
			return o.Value
		}
	}
	return d.Path
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
