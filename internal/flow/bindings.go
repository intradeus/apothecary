package flow

import (
	"fmt"
	"slices"
	"strings"

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
	"apothecary/internal/signalspec"
)

// validateContracts checks each stage's bindings against the contract on the
// other side — the flow's own agent definitions (FLOWLANG §1) or an action's
// signature — and checks that every path a stage consumes is produced by some
// stage.
//
// Since v3 the agents live in the document, so everything here is a parse-time
// fact: a binding that does not satisfy its agent's contract is an error that
// refuses the document, not a per-project warning. What remains advisory is
// suspicion rather than breakage — a path nothing produces (it may be a
// pre-existing repo file), a sentinel no edge can fill — and rides on
// Flow.Warnings for the editor to surface.
func (p *parser) validateContracts(f *Flow) ([]string, error) {
	produced := producedPaths(f)
	var warns []string

	for i := range f.Stages {
		st := &f.Stages[i]
		fail := func(format string, args ...any) error {
			return p.errf(st.Line, "stage %q: %s", st.ID, fmt.Sprintf(format, args...))
		}
		warnf := func(format string, args ...any) {
			warns = append(warns, fmt.Sprintf("stage %q: %s", st.ID, fmt.Sprintf(format, args...)))
		}

		if st.Type == StageAgent {
			spec := f.Agent(st.Agent)
			if spec == nil {
				return warns, fail("agent %q is not declared in this flow (add it to the %q block; declared: %s)",
					st.Agent, "agents", agentNameList(f))
			}
			if err := validateStageInputs(st, spec, fail); err != nil {
				return warns, err
			}
			if err := validateStageOutputs(st, spec, fail, warnf); err != nil {
				return warns, err
			}
			// An on_reject edge only ever fires on a REJECT verdict, and only a
			// verdict-typed output produces one. The agent is in the document,
			// so this is now decidable here rather than guessed at.
			if st.Edges[EdgeReject] != nil && spec.VerdictOutput() == nil {
				return warns, fail("on_reject can never fire: agent %q declares no %s output",
					st.Agent, agentspec.OutputTypeVerdict)
			}
			if err := validateFeedBindings(f, st, spec, fail, warnf); err != nil {
				return warns, err
			}
		}

		// Rendezvous: every path a stage consumes should be produced by some
		// stage. Restricted to artifacts/ — a stage may legitimately read
		// pre-existing repo files (docs/spec.md), which have no producer.
		for _, in := range consumedPaths(st) {
			if !strings.HasPrefix(in.Value, "artifacts/") {
				continue
			}
			switch {
			case !produced[in.Value]:
				if in.Name != "" {
					warnf("input %q binds %q, which no stage produces", in.Name, in.Value)
				} else {
					warnf("consumes %q, which no stage produces", in.Value)
				}
			case !definitelyProduced(f, st, in.Value):
				// A producer exists, but not on every route here: some path
				// through the graph reaches this stage without it, and the
				// engine will report input_missing at run time (PLAN5 M24).
				warnf("input %q binds %q, which is not produced on every path that reaches this stage",
					in.Name, in.Value)
			}
		}

		// A signal's prerequisite must be established BEFORE it can fire.
		// Waiting on mr_merged ahead of the create_mr that opens the MR
		// cannot ever succeed; the engine fails it with input_missing, which
		// is a worse way to learn it than a warning while editing.
		for _, slug := range []string{st.WaitFor, st.AutoApprove, st.AutoReject} {
			if slug == "" {
				continue
			}
			sig := signalspec.Get(slug)
			if sig == nil || sig.Needs != signalspec.NeedsMR {
				continue
			}
			if !definitelyHasMR(f, st) {
				warnf("waits on %q, which needs a merge request that is not opened on every path reaching this stage",
					slug)
			}
		}
		// An action's prerequisite, the same way: ticket_link attaches the
		// merge request, so a route that never opens one reaches a stage
		// that can only fail with input_missing. Inside a reaction phase the
		// subject merge request exists by definition.
		if st.Type == StageAction {
			if act := actionspec.Get(st.Action); act != nil && act.Needs == actionspec.NeedsMR &&
				!inSubjectPhase(f, st) && !definitelyHasMR(f, st) {
				warnf("%q needs a merge request that is not opened on every path reaching this stage", st.Action)
			}
			// A subject action outside a phase opened for a merge request has
			// one only when the phase was started on one by hand.
			if act := actionspec.Get(st.Action); act != nil && act.Subject && !inSubjectPhase(f, st) {
				warnf("%q acts on the merge request a phase is opened for, and phase %q is not (it opens on %s) — it has one only when started on one by hand; guard the stage with `when: subject` or route on_fail",
					st.Action, st.Phase, f.Phase(st.Phase).On.Signal)
			}
		}
		if st.EachRepo && inSubjectPhase(f, st) {
			warnf("each_repo is ignored in phase %q: a phase opened for one merge request runs in its checkout only", st.Phase)
		}
	}
	warns = append(warns, phaseShapeWarnings(f)...)
	warns = append(warns, endingWarnings(f)...)
	return warns, nil
}

// endingWarnings: the run's end written where it does not belong. A phase
// opened by a firing signal may run any number of times; ending the whole run
// from it (done, abort) stops every one of them for good — almost always a
// slip for `end`, which goes back to waiting. And a phase that opens a merge
// request, then reaches done with nothing waiting for the merge, completes
// the run the moment the merge request exists: the phases that react to it,
// and the outcomes that would end it, never get to run.
func endingWarnings(f *Flow) []string {
	reacts := false
	for i := range f.Phases {
		reacts = reacts || f.Phases[i].Observed()
	}
	var warns []string
	for pi := range f.Phases {
		ph := &f.Phases[pi]
		opened, waits, doneAfterMR := false, false, false
		for i := ph.First; i <= ph.Last; i++ {
			st := &f.Stages[i]
			ends := runEndsOf(st)
			if ph.Fires() && len(ends) > 0 {
				warns = append(warns, fmt.Sprintf("stage %q: phase %q opens on %s and may run any number of times, but this stage ends the whole run (%s) — end the phase with %q to go back to waiting",
					st.ID, ph.ID, ph.On.Signal, strings.Join(ends, ", "), TerminalEnd))
			}
			if st.Type == StageAction {
				if act := actionspec.Get(st.Action); act != nil && act.Provides == actionspec.ProvidesMR {
					opened = true
				}
			}
			if waitsOnMerge(st) {
				waits = true
			}
			if opened && slices.Contains(ends, TerminalDone) {
				doneAfterMR = true
			}
		}
		if reacts && doneAfterMR && !waits {
			warns = append(warns, fmt.Sprintf("phase %q opens a merge request and ends the run (%s) without waiting for it: the run completes as soon as the merge request exists, and the phases watching it never run — end the phase (%s) and let an outcome (on: %s) end the run, or park on a %s stage for %s first",
				ph.ID, TerminalDone, TerminalEnd, signalspec.MRMerged, StageWait, signalspec.MRMerged))
		}
	}
	return warns
}

// runEndsOf lists the run-ending targets (done, abort) a stage routes to, in
// a stable order: on_success first, then each edge kind's chain.
func runEndsOf(st *Stage) []string {
	var ends []string
	add := func(to string) {
		if (to == TerminalDone || to == TerminalAbort) && !slices.Contains(ends, to) {
			ends = append(ends, to)
		}
	}
	add(st.OnSuccess)
	for _, kind := range []string{EdgeFail, EdgeReject, EdgeTimeout} {
		for link := st.Edges[kind]; link != nil; link = link.Then {
			add(link.To)
		}
	}
	return ends
}

// waitsOnMerge reports whether a stage waits for the merge request's fate: a
// wait on mr_merged/mr_closed, or a gate either of them answers.
func waitsOnMerge(st *Stage) bool {
	merge := func(s string) bool { return s == signalspec.MRMerged || s == signalspec.MRClosed }
	switch st.Type {
	case StageWait:
		return merge(st.WaitFor)
	case StageHumanGate:
		return merge(st.AutoApprove) || merge(st.AutoReject)
	}
	return false
}

// phaseShapeWarnings: what a phase opens on versus what the flow does — a
// trigger that needs a merge request no stage of the flow opens.
func phaseShapeWarnings(f *Flow) []string {
	opensMR := false
	for i := range f.Stages {
		if st := &f.Stages[i]; st.Type == StageAction {
			if act := actionspec.Get(st.Action); act != nil && act.Provides == actionspec.ProvidesMR {
				opensMR = true
			}
		}
	}
	var warns []string
	for i := range f.Phases {
		if ph := &f.Phases[i]; ph.HasSubject() && !opensMR {
			warns = append(warns, fmt.Sprintf("phase %q opens on %s, which needs a merge request no stage opens (add a %s action)",
				ph.ID, ph.On.Signal, actionspec.CreateMR))
		}
	}
	return warns
}

// inSubjectPhase reports whether st belongs to a phase opened for one merge
// request (its signal needs one), where that merge request exists by
// definition.
func inSubjectPhase(f *Flow, st *Stage) bool {
	ph := f.Phase(st.Phase)
	return ph != nil && ph.HasSubject()
}

// definitelyProduced reports whether every route from the entry to consumer
// passes through a stage that writes path. Producers are checked against the
// SUCCESS chain only: an edge is a retry, so a stage reached by one has
// already run its producer at least once — or never will.
func definitelyProduced(f *Flow, consumer *Stage, path string) bool {
	return everyRouteSatisfies(f, consumer, func(st *Stage) bool {
		if st.When != nil {
			return false // a conditional producer may be skipped
		}
		for _, o := range effectiveOutputsOf(f, st) {
			if o.Value == path {
				return true
			}
		}
		return false
	})
}

// definitelyHasMR reports whether every route to st passes through the
// action that opens the run's merge request.
func definitelyHasMR(f *Flow, st *Stage) bool {
	return everyRouteSatisfies(f, st, func(s *Stage) bool {
		if s.Type != StageAction || s.When != nil {
			return false
		}
		act := actionspec.Get(s.Action)
		return act != nil && act.Provides == actionspec.ProvidesMR
	})
}

// everyRouteSatisfies walks every acyclic route from the entry stage to
// target and reports whether each one passes through a stage satisfying ok.
// Cycles (retry edges) are cut at the repeat: a loop adds no new route, and
// following it would not terminate.
func everyRouteSatisfies(f *Flow, target *Stage, ok func(*Stage) bool) bool {
	if len(f.Stages) == 0 {
		return false
	}
	var walk func(st *Stage, seen map[string]bool, satisfied bool) bool
	walk = func(st *Stage, seen map[string]bool, satisfied bool) bool {
		if st == nil || seen[st.ID] {
			return true // cycle: contributes no new route
		}
		if ok(st) {
			satisfied = true
		}
		if st.ID == target.ID {
			return satisfied
		}
		seen[st.ID] = true
		defer delete(seen, st.ID)

		next := func(id string) bool {
			if routeTerminal(id) {
				// A terminal never reaches the target; the debug leaf only
				// re-enters st, which is already on this walk.
				return true
			}
			return walk(f.Stage(id), seen, satisfied)
		}
		if !next(st.OnSuccess) {
			return false
		}
		for _, e := range st.Edges {
			for link := e; link != nil; link = link.Then {
				if !next(link.To) {
					return false
				}
			}
		}
		return true
	}
	// Phases run in any order, any number of times, so what every OTHER
	// phase does counts as done for the routes inside the target's (an
	// approximation for warnings: any stage of another phase satisfying ok).
	ph := f.Phase(target.Phase)
	if ph == nil {
		return walk(&f.Stages[0], map[string]bool{}, false)
	}
	before := false
	for _, other := range f.Phases {
		if other.ID == ph.ID {
			continue
		}
		for i := other.First; i <= other.Last; i++ {
			if ok(&f.Stages[i]) {
				before = true
			}
		}
	}
	return walk(&f.Stages[ph.First], map[string]bool{}, before)
}

// effectiveOutputsOf is EffectiveOutputs with the flow's own agent lookup.
func effectiveOutputsOf(f *Flow, st *Stage) []IOBinding {
	if st.Type == StageAgent {
		return EffectiveOutputs(st, f.Agent(st.Agent))
	}
	return st.Outputs
}

func agentNameList(f *Flow) string {
	if len(f.Agents) == 0 {
		return "(none)"
	}
	names := make([]string, len(f.Agents))
	for i := range f.Agents {
		names[i] = f.Agents[i].Name
	}
	return strings.Join(names, ", ")
}

// producedPaths collects every artifact path the flow's stages produce: agent
// effective outputs and command declared outputs.
func producedPaths(f *Flow) map[string]bool {
	produced := map[string]bool{}
	for i := range f.Stages {
		st := &f.Stages[i]
		switch st.Type {
		case StageAgent:
			for _, o := range EffectiveOutputs(st, f.Agent(st.Agent)) {
				if o.Value != "" {
					produced[o.Value] = true
				}
			}
		case StageCommand, StageAction:
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
func validateFeedBindings(f *Flow, st *Stage, spec *agentspec.Spec, fail func(string, ...any) error, warnf func(string, ...any)) error {
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
				return fail("input %q binds %s, which is empty on the first attempt: declare it optional on agent %q",
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
	return nil
}

func validateStageInputs(st *Stage, spec *agentspec.Spec, fail func(string, ...any) error) error {
	bound := map[string]bool{}
	for _, in := range st.Inputs {
		if !specHasInput(spec, in.Name) {
			return fail("input %q is not declared by agent %q (declares: %s)",
				in.Name, st.Agent, inputNameList(spec))
		}
		bound[in.Name] = true
	}
	for _, d := range spec.Inputs {
		if d.Optional {
			continue
		}
		if !bound[d.Name] {
			return fail("required input %q of agent %q is not bound", d.Name, st.Agent)
		}
	}
	return nil
}

func validateStageOutputs(st *Stage, spec *agentspec.Spec, fail func(string, ...any) error, warnf func(string, ...any)) error {
	for _, o := range st.Outputs {
		if !specHasOutput(spec, o.Name) {
			return fail("output %q is not declared by agent %q (a stage may rebind a declared output's path, not add one)", o.Name, st.Agent)
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
			return fail("output path %q is bound more than once", o.Value)
		}
		seen[o.Value] = true
	}
	return nil
}

func inputNameList(spec *agentspec.Spec) string {
	if len(spec.Inputs) == 0 {
		return "(none)"
	}
	names := make([]string, len(spec.Inputs))
	for i, d := range spec.Inputs {
		names[i] = d.Name
	}
	return strings.Join(names, ", ")
}

// EffectiveOutputs resolves the set of files a stage must produce: for an agent,
// its declared output default paths overlaid with the stage's rebinds; for any
// other stage (or a nil spec) the stage's literal bindings. A nil spec means the
// engine degrades gracefully if a stage names an agent the flow does not declare.
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
	return typedOutputPath(st, spec, spec.VerdictOutput())
}

// ReviewRepliesPath is the effective path of the agent's review_replies
// output, or "" when it declares none.
func ReviewRepliesPath(st *Stage, spec *agentspec.Spec) string {
	return typedOutputPath(st, spec, spec.ReviewRepliesOutput())
}

// ReviewThreadsPath is the effective path of the agent's review_threads
// output, or "" when it declares none.
func ReviewThreadsPath(st *Stage, spec *agentspec.Spec) string {
	return typedOutputPath(st, spec, spec.ReviewThreadsOutput())
}

func typedOutputPath(st *Stage, spec *agentspec.Spec, d *agentspec.OutputDecl) string {
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
