package flow

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
	"apothecary/internal/signalspec"
)

// validateContracts checks each stage's ports against the contract on the
// other side — the flow's own agent definitions (FLOWLANG §1); an action's
// was checked as its stage was read — and every input against what feeds it:
// a path some stage writes, with a shape that satisfies the reader, on every
// route that reaches it; an edge source some edge of its kind carries in.
//
// Since v3 the agents live in the document, so everything here is a parse-time
// fact: a port that does not hold is an error that refuses the document. What
// remains advisory is suspicion rather than breakage — a merge request not
// opened on every route to what needs one — and rides on Flow.Warnings for
// the editor to surface.
func (p *parser) validateContracts(f *Flow) ([]string, error) {
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
			if err := agentPorts(st, spec); err != nil {
				return warns, fail("%v", err)
			}
			// An on_reject edge only ever fires on a REJECT verdict, and only a
			// verdict-typed output produces one.
			if st.Edges[EdgeReject] != nil && st.TypedOutput(agentspec.TypeVerdict) == nil {
				return warns, fail("on_reject can never fire: agent %q writes no %s", st.Agent, agentspec.TypeVerdict)
			}
		}
		for _, in := range st.Inputs {
			if err := sourceHolds(f, st, in, warnf); err != nil {
				return warns, fail("input %q: %v", in.Name, err)
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
		// that can only fail with input_missing. Inside a watcher on a merge
		// request signal the subject merge request exists by definition.
		if st.Type == StageAction {
			if act := actionspec.Get(st.Action); act != nil && act.Needs == actionspec.NeedsMR &&
				!inSubjectPhase(f, st) && !definitelyHasMR(f, st) {
				warnf("%q needs a merge request that is not opened on every path reaching this stage", st.Action)
			}
			// A subject action outside a phase opened for a merge request has
			// one only when the phase was started on one by hand.
			if act := actionspec.Get(st.Action); act != nil && act.Subject && !inSubjectPhase(f, st) {
				warnf("%q acts on the merge request a phase is opened for, and phase %q is not (it opens on %s) — it has one only when started on one by hand; guard the stage with `run_if: subject` or route on_fail",
					st.Action, st.Phase, f.Phase(st.Phase).OpensOn())
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

// mentionRe finds what reads like a parameter inside text: $name or ${name}.
var mentionRe = regexp.MustCompile(`\$\{?([a-z][a-z0-9_]*)\}?`)

// untemplatedWarnings: a declared parameter named inside a command's script
// or an agent's prompt. Neither is templated — the shell reads $name as its
// own variable, the agent reads the text — so it looks like it passes the
// value and does not. (A whole field that is $name is refused outright.)
func untemplatedWarnings(f *Flow) []string {
	declared := map[string]bool{}
	for _, pr := range f.Params {
		declared[pr.Name] = true
	}
	mentioned := func(text string) []string {
		var out []string
		for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
			if declared[m[1]] && !slices.Contains(out, "$"+m[1]) {
				out = append(out, "$"+m[1])
			}
		}
		return out
	}
	var warns []string
	for i := range f.Stages {
		st := &f.Stages[i]
		if names := mentioned(st.Run); len(names) > 0 {
			warns = append(warns, fmt.Sprintf("stage %q: its script names %s, a parameter — a script is not templated: the shell reads %s as its own variable, not the parameter (a command reads one with run_ref)",
				st.ID, strings.Join(names, ", "), names[0]))
		}
	}
	for i := range f.Agents {
		a := &f.Agents[i]
		if names := mentioned(a.Prompt); len(names) > 0 {
			warns = append(warns, fmt.Sprintf("agent %q: its prompt names %s, a parameter — a prompt is not templated: the agent reads the text %q, not the parameter's value",
				a.Name, strings.Join(names, ", "), names[0]))
		}
	}
	return warns
}

// endingWarnings: the run's end written where it does not belong. A watcher
// may run any number of times; ending the whole run from it (done, abort)
// stops every one of them for good — almost always a slip for `end`, which
// goes back to waiting. And a chain that opens a merge request, then reaches
// done with nothing waiting for the merge, completes the run the moment the
// merge request exists: the watchers on it, and the outcomes that would end
// it, never get to run.
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
				warns = append(warns, fmt.Sprintf("stage %q: watcher %q runs on each %s and may run any number of times, but this stage ends the whole run (%s) — end its chain with %q to go back to waiting",
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
			warns = append(warns, fmt.Sprintf("%s opens a merge request and ends the run (%s) without waiting for it: the run completes as soon as the merge request exists, and the watchers on it never run — end the chain (%s) and let an outcome (on: %s) end the run, or park on a %s stage for %s first",
				ph.Label(), TerminalDone, TerminalEnd, signalspec.MRMerged, StageWait, signalspec.MRMerged))
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

// phaseShapeWarnings: what a watcher or an outcome opens on versus what the
// flow does — a signal that needs a merge request no stage of the flow
// opens.
func phaseShapeWarnings(f *Flow) []string {
	opensMR := false
	for i := range f.Phases {
		opensMR = opensMR || f.Phases[i].OnMR() // its runs are created on one
	}
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
			warns = append(warns, fmt.Sprintf("%s opens on %s, which needs a merge request no stage opens (add a %s action)",
				ph.Label(), ph.OpensOn(), actionspec.CreateMR))
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
		if st.RunIf != nil {
			return false // a conditional producer may be skipped
		}
		for _, o := range st.Outputs {
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
	// A run created on a merge request has it from the start.
	if ph := f.Phase(st.Phase); ph != nil && ph.OnMR() {
		return true
	}
	return everyRouteSatisfies(f, st, func(s *Stage) bool {
		if s.Type != StageAction || s.RunIf != nil {
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

// agentPorts holds an agent stage's ports to the agent's contract: every
// port is one the agent declares, a required input is stated, every output
// is stated (the agent writes them all; the stage says where), and each
// port's shape satisfies the declaration's — an output may narrow it (a
// schema beside its type), never change it.
func agentPorts(st *Stage, spec *agentspec.Spec) error {
	stated := map[string]bool{}
	for _, in := range st.Inputs {
		d := spec.Input(in.Name)
		if d == nil {
			return fmt.Errorf("input %q is not an input of agent %q (inputs: %s)", in.Name, spec.Name, declNames(spec.Inputs))
		}
		stated[in.Name] = true
		if need := (shape{d.Type, d.Schema}); !shapeOf(in).satisfies(need) {
			return fmt.Errorf("input %q: agent %q reads %s, the port says %s", in.Name, spec.Name, need, shapeOf(in))
		}
	}
	for _, d := range spec.Inputs {
		if !stated[d.Name] && !d.Optional {
			return fmt.Errorf("agent %q requires input %q: input: { %s: { from: …, type: %s } }", spec.Name, d.Name, d.Name, shape{d.Type, d.Schema})
		}
	}
	stated = map[string]bool{}
	for _, out := range st.Outputs {
		d := spec.Output(out.Name)
		if d == nil {
			return fmt.Errorf("output %q is not an output of agent %q (outputs: %s)", out.Name, spec.Name, declNames(spec.Outputs))
		}
		stated[out.Name] = true
		if need := (shape{d.Type, d.Schema}); !shapeOf(out).satisfies(need) {
			return fmt.Errorf("output %q: agent %q writes %s, the port says %s", out.Name, spec.Name, need, shapeOf(out))
		}
	}
	for _, d := range spec.Outputs {
		if !stated[d.Name] {
			typ := d.Type
			if typ == "" {
				typ = "…, schema: " + d.Schema
			}
			return fmt.Errorf("agent %q writes %q — state where: output: { %s: { to: …, type: %s } }", spec.Name, d.Name, d.Name, typ)
		}
	}
	return nil
}

func declNames[T agentspec.InputDecl | agentspec.OutputDecl](ds []T) string {
	if len(ds) == 0 {
		return "none"
	}
	names := make([]string, 0, len(ds))
	for _, d := range ds {
		switch v := any(d).(type) {
		case agentspec.InputDecl:
			names = append(names, v.Name)
		case agentspec.OutputDecl:
			names = append(names, v.Name)
		}
	}
	return strings.Join(names, ", ")
}

// sourceHolds checks one input against what feeds it:
//
//   - an edge source: some edge of its kind enters the stage — a port no
//     edge can fill is a mistake, not a precaution;
//   - run.mr.url: a merge request is opened on every route (a warning — a
//     run started on one has it anyway);
//   - repo:path: nothing in the flow writes it (else it is a flow file);
//   - a path: some stage writes it; every writer states the same shape, and
//     that shape satisfies the port; and unless the port is optional, a
//     writer runs on every route that reaches the stage.
func sourceHolds(f *Flow, st *Stage, in Port, warnf func(string, ...any)) error {
	switch {
	case IsEdgeSource(in.Value):
		kinds := map[string]bool{}
		for i := range f.Stages {
			for kind, e := range f.Stages[i].Edges {
				for link := e; link != nil; link = link.Then {
					if link.To == st.ID {
						kinds[FeedSource(kind)] = true
					}
				}
			}
			// An approval carries its verdict on to its success target.
			if GivesVerdict(&f.Stages[i]) && f.Stages[i].OnSuccess == st.ID {
				kinds[SourceVerdict] = true
			}
		}
		if !kinds[in.Value] {
			if in.Value == SourceFailure {
				return fmt.Errorf("no %s or %s edge enters stage %q, so %s is never filled", EdgeFail, EdgeTimeout, st.ID, in.Value)
			}
			return fmt.Errorf("no %s edge enters stage %q, nor the success of a gate or a verdict, so %s is never filled", EdgeReject, st.ID, in.Value)
		}
		return nil
	case in.Value == SourceCIFailure:
		// What failed is the failure that opened the chain: a watcher on
		// ci_failed has one; any other chain would read nothing.
		if ph := f.PhaseOf(st); ph == nil || ph.Kind != PhaseWatcher || ph.On.Signal != signalspec.CIFailed {
			return fmt.Errorf("%s is what failed when a %s watcher opened — stage %q is not in one", in.Value, signalspec.CIFailed, st.ID)
		}
		return nil
	case in.Value == SourceMRURL:
		if !definitelyHasMR(f, st) {
			warnf("input %q reads %s, but a merge request is not opened on every path reaching this stage", in.Name, in.Value)
		}
		return nil
	case IsEngineSource(in.Value):
		return nil
	case strings.HasPrefix(in.Value, RepoPrefix):
		path := FilePath(in.Value)
		if ws := writersOf(f, path); len(ws) > 0 {
			return fmt.Errorf("stage %q writes %s — read it as from: %s (repo: is a file nothing in the flow writes)", ws[0].stage.ID, path, path)
		}
		return nil
	}
	ws := writersOf(f, in.Value)
	if len(ws) == 0 {
		hint := fmt.Sprintf(" — a file of the repository is from: %s%s", RepoPrefix, in.Value)
		if near := nearestSource(in.Value); near != "" {
			hint = fmt.Sprintf(" — did you mean %s?", near)
		}
		return fmt.Errorf("nothing in this flow writes %q%s", in.Value, hint)
	}
	for _, w := range ws[1:] {
		if w.port.Type != ws[0].port.Type || w.port.Schema != ws[0].port.Schema {
			return fmt.Errorf("stages %q and %q both write %q, as %s and as %s — one path holds one thing", ws[0].stage.ID, w.stage.ID, in.Value, shapeOf(ws[0].port), shapeOf(w.port))
		}
	}
	if !shapeOf(ws[0].port).satisfies(shapeOf(in)) {
		return fmt.Errorf("stage %q writes %q as %s; the port reads %s", ws[0].stage.ID, in.Value, shapeOf(ws[0].port), shapeOf(in))
	}
	if !in.Optional && !definitelyProduced(f, st, in.Value) {
		return fmt.Errorf("%q is not written on every route reaching this stage (written by %s) — mark the port optional: true if the stage does without it", in.Value, writerList(ws))
	}
	return nil
}

// writer is one stage output that writes a path.
type writer struct {
	stage *Stage
	port  Port
}

// writersOf lists the stage outputs that write path, in stage order.
func writersOf(f *Flow, path string) []writer {
	var out []writer
	for i := range f.Stages {
		for _, o := range f.Stages[i].Outputs {
			if o.Value == path {
				out = append(out, writer{&f.Stages[i], o})
			}
		}
	}
	return out
}

func writerList(ws []writer) string {
	ids := make([]string, 0, len(ws))
	for _, w := range ws {
		ids = append(ids, fmt.Sprintf("%q", w.stage.ID))
	}
	return strings.Join(ids, ", ")
}

// nearestSource is the engine source a mistyped run.* or edge.* most likely
// meant, or "".
func nearestSource(v string) string {
	if !strings.HasPrefix(v, "run.") && !strings.HasPrefix(v, "edge.") {
		return ""
	}
	best, bestD := "", 4
	for _, src := range EngineSources {
		if d := editDistance(v, src); d < bestD {
			best, bestD = src, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// TypedPath is where a stage writes its output of a structured type, or ""
// when it writes none: the single resolution used by prompt assembly and
// the engine's checks.
func TypedPath(st *Stage, typ string) string {
	if o := st.TypedOutput(typ); o != nil {
		return o.Value
	}
	return ""
}
