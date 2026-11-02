package flow

import (
	"fmt"
	"strings"
)

// Sequences (FLOWLANG §1 *Sequences*) are chains of stages written once
// under `sequences:` and run wherever a sequence stage names them. A
// sequence stage expands in place at parse time, so everything after the
// parser — validation, the engine, the events — sees ordinary stages:
//
//   - the sequence's stage ids get the sequence stage's id and a dot in
//     front (make.ship.test). A hand-written id cannot contain a dot, so an
//     expansion never collides with a stage;
//   - a route inside a sequence names the sequence's own stages. It leaves
//     the sequence only through its last stage, whose success is the
//     sequence stage's on_success, or through `fail`, which is the sequence
//     stage's on_fail: that chain is spliced in where `fail` is said (a
//     sequence stage without one makes it a dead end, §3);
//   - outside, a sequence stage's id is a route target: its first stage.
//     Nothing routes into the middle of a sequence;
//   - sequences may run sequences, at most maxSequenceDepth levels deep, and
//     a sequence that runs itself is refused;
//   - the main chain is a sequence named `main` (MainPhase), so a watcher can
//     run it again (`sequence: main`).

// sequenceDef is one declared sequence and whether any stage expanded it.
type sequenceDef struct {
	doc  *DocSequence
	line int
	used bool
}

// expandScope is where a list of stages was written: a chain (sequence "")
// or a sequence expanded for one sequence stage.
type expandScope struct {
	sequence string // the sequence being expanded; "" for a chain's own stages
	prefix   string // what the sequence's ids get in front: "make.ship."
	path     string // what its stages record as Stage.Sequence: "make.ship"
	// local maps the sequence's ids to their expanded ids (a sequence
	// stage's: its first stage's).
	local map[string]string
	// onSuccess and onFail are the sequence stage's routes, resolved where
	// it is written: what the last stage's success and `fail` become.
	// onSuccess "" leaves the success to the sugar (the next stage).
	onSuccess string
	onFail    *DocEdge
}

// idProblem says what is wrong with a hand-written stage id, or "" when it
// is fine.
func idProblem(id string) string {
	switch {
	case id == "":
		return "id is required"
	case !slugRe.MatchString(id):
		return "id is not a slug (lowercase [a-z0-9_-], starting with a letter)"
	case id == TerminalEnd:
		return fmt.Sprintf("id %q is reserved (the end of a chain: the run goes back to waiting)", TerminalEnd)
	case id == TerminalDone:
		return fmt.Sprintf("id %q is reserved (the end of the run)", TerminalDone)
	case id == TerminalAbort:
		return fmt.Sprintf("id %q is reserved (the deliberate-stop edge terminal)", TerminalAbort)
	case id == ReservedDebug:
		return fmt.Sprintf("id %q is reserved (the debug leaf: `then: debug` parks the run for a human and re-enters the failing stage)", ReservedDebug)
	case id == ReservedStart:
		return fmt.Sprintf("id %q is reserved (the graph editor's start marker)", ReservedStart)
	case id == ReservedFail:
		return fmt.Sprintf("id %q is reserved (inside a sequence, the sequence stage's on_fail)", ReservedFail)
	}
	return ""
}

// sequenceStageProblem says what a sequence stage carries that it must not,
// or "". A sequence stage runs a sequence's stages: it has no type of its
// own, and it fails as a whole.
func sequenceStageProblem(st *DocStage) string {
	if msgs := st.retired(); len(msgs) > 0 {
		return msgs[0]
	}
	if err := notAParam(st.Description); err != nil {
		return "description: " + err.Error()
	}
	if st.Type != "" {
		return fmt.Sprintf("a sequence stage has no type — it runs sequence %q's stages", st.Sequence)
	}
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"run_if", st.RunIf != ""}, {"agent", st.Agent != ""}, {"model", st.Model != ""}, {"harness", st.Harness != ""}, {"harness_options", len(st.HarnessOptions) > 0},
		{"max_turns", st.MaxTurns != nil}, {"timeout", st.Timeout != ""}, {"input", st.Input != nil}, {"output", st.Output != nil},
		{"run", st.Run != ""}, {"run_ref", st.RunRef != ""}, {"each_repo", st.EachRepo}, {"reuse_session", st.ReuseSession != nil}, {"question", st.Question != ""},
		{"action", st.Action != ""}, {"for", st.For != ""},
		{"on_reject", st.OnReject != nil}, {"on_timeout", st.OnTimeout != nil},
	} {
		if f.set {
			return fmt.Sprintf("field %q does not apply to a sequence stage (it takes id, sequence, description, on_success and on_fail: the sequence's stages say the rest, and it fails as a whole)", f.name)
		}
	}
	return ""
}

// buildSequences validates the sequences themselves — ids, what they run,
// no cycle, the nesting depth — whether or not anything runs them, and
// records each stage's source line for the expansion.
func (p *parser) buildSequences(raw []DocSequence) error {
	p.sequences, p.sequenceOrder = map[string]*sequenceDef{}, nil
	nodes := p.seqNodes("sequences")
	for i := range raw {
		s := &raw[i]
		line := p.seqLine("sequences", i)
		fail := func(format string, args ...any) error {
			id := s.ID
			if id == "" {
				id = fmt.Sprintf("#%d", i+1)
			}
			return p.errf(line, "sequence %q: %s", id, fmt.Sprintf(format, args...))
		}
		if s.ID == "" {
			return fail("id is required")
		}
		if !slugRe.MatchString(s.ID) {
			return fail("id is not a slug (lowercase [a-z0-9_-], starting with a letter)")
		}
		if err := notAParam(s.Description); err != nil {
			return fail("description: %v", err)
		}
		if prev, dup := p.sequences[s.ID]; dup {
			return fail("duplicate sequence id (first declared at line %d)", prev.line)
		}
		if len(s.Stages) == 0 {
			return fail("a sequence needs at least one stage")
		}
		if s.ID == MainPhase {
			// The main chain, appended by build: its stages carry their lines.
			line = p.keyLine("main")
		} else {
			var lines []int
			if i < len(nodes) {
				lines = phaseStageLines(nodes[i])
			}
			for j := range s.Stages {
				s.Stages[j].line = line
				if j < len(lines) {
					s.Stages[j].line = lines[j]
				}
			}
		}
		p.sequences[s.ID] = &sequenceDef{doc: s, line: line}
		p.sequenceOrder = append(p.sequenceOrder, s.ID)
	}
	for i := range raw {
		s := &raw[i]
		seen := map[string]int{}
		for j := range s.Stages {
			st := &s.Stages[j]
			fail := func(format string, args ...any) error {
				id := st.ID
				if id == "" {
					id = fmt.Sprintf("#%d", j+1)
				}
				return p.errf(st.line, "sequence %q, stage %q: %s", s.ID, id, fmt.Sprintf(format, args...))
			}
			if msg := idProblem(st.ID); msg != "" {
				return fail("%s", msg)
			}
			if prev, dup := seen[st.ID]; dup {
				return fail("duplicate id (first declared at line %d)", prev)
			}
			seen[st.ID] = st.line
			if st.Sequence == "" {
				continue
			}
			if p.sequences[st.Sequence] == nil {
				return fail("sequence %q: no sequence of that name (sequences: %s)", st.Sequence, p.sequenceNames())
			}
			if msg := sequenceStageProblem(st); msg != "" {
				return fail("%s", msg)
			}
		}
	}
	depths := map[string]int{}
	for i := range raw {
		d, err := p.sequenceDepth(raw[i].ID, nil, depths)
		if err != nil {
			return err
		}
		if d > maxSequenceDepth {
			return p.errf(p.sequences[raw[i].ID].line, "sequence %q: sequences nest %d levels deep; the limit is %d", raw[i].ID, d, maxSequenceDepth)
		}
	}
	return nil
}

// sequenceDepth is how many levels of sequences a sequence stage running
// name expands into: 1 for a sequence that runs none. A sequence reached
// again while expanding itself is a cycle.
func (p *parser) sequenceDepth(name string, stack []string, memo map[string]int) (int, error) {
	if d, ok := memo[name]; ok {
		return d, nil
	}
	for i, s := range stack {
		if s == name {
			cycle := append(append([]string(nil), stack[i:]...), name)
			return 0, p.errf(p.sequences[name].line, "sequence %q runs itself (%s)", name, strings.Join(cycle, " → "))
		}
	}
	d := 1
	for _, st := range p.sequences[name].doc.Stages {
		if st.Sequence == "" {
			continue
		}
		cd, err := p.sequenceDepth(st.Sequence, append(stack, name), memo)
		if err != nil {
			return 0, err
		}
		d = max(d, cd+1)
	}
	memo[name] = d
	return d, nil
}

func (p *parser) sequenceNames() string {
	if len(p.sequenceOrder) == 0 {
		return "none declared"
	}
	return strings.Join(p.sequenceOrder, ", ")
}

// indexSequenceStages checks the sequence stages written in chains and
// records where each one begins, so a route to a sequence stage's id lands
// on its first stage. A sequence stage's id is in the stages' one
// namespace.
func (p *parser) indexSequenceStages(phases []phaseSrc) error {
	p.topSequences = map[string]string{}
	ids := map[string]int{}
	for i := range phases {
		for j := range phases[i].stages {
			st := &phases[i].stages[j]
			if st.Sequence == "" {
				if st.ID != "" {
					if _, dup := ids[st.ID]; !dup {
						ids[st.ID] = st.line
					}
				}
				continue
			}
			fail := func(format string, args ...any) error {
				id := st.ID
				if id == "" {
					id = fmt.Sprintf("#%d", j+1)
				}
				return p.errf(st.line, "stage %q: %s", id, fmt.Sprintf(format, args...))
			}
			if msg := idProblem(st.ID); msg != "" {
				return fail("%s", msg)
			}
			if prev, dup := ids[st.ID]; dup {
				return fail("duplicate id (first declared at line %d) — stage ids are one namespace across chains, sequence stages too", prev)
			}
			ids[st.ID] = st.line
			if p.sequences[st.Sequence] == nil {
				return fail("sequence %q: no sequence of that name (sequences: %s)", st.Sequence, p.sequenceNames())
			}
			if msg := sequenceStageProblem(st); msg != "" {
				return fail("%s", msg)
			}
			p.topSequences[st.ID] = p.entryOf(st.Sequence, st.ID+".")
		}
	}
	// A plain stage written after a sequence stage of the same id.
	for i := range phases {
		for j := range phases[i].stages {
			st := &phases[i].stages[j]
			if _, seq := p.topSequences[st.ID]; seq && st.Sequence == "" {
				return p.errf(st.line, "stage %q: duplicate id (a sequence stage is named %q) — stage ids are one namespace across chains, sequence stages too", st.ID, st.ID)
			}
		}
	}
	return nil
}

// entryOf is the expanded id of the first stage a sequence stage running
// name runs, its stages' ids starting with prefix.
func (p *parser) entryOf(name, prefix string) string {
	first := &p.sequences[name].doc.Stages[0]
	if first.Sequence != "" {
		return p.entryOf(first.Sequence, prefix+first.ID+".")
	}
	return prefix + first.ID
}

// expand inlines every sequence stage in stages, recursively: what a
// chain's stages are once its sequences are written out. Each stage keeps
// its source line; a sequence's stages also record the path of sequence
// stages they came from.
func (p *parser) expand(stages []DocStage, sc *expandScope) ([]DocStage, error) {
	var out []DocStage
	for i := range stages {
		st := stages[i]
		fail := func(format string, args ...any) error {
			if sc.sequence != "" {
				return p.errf(st.line, "sequence %q, stage %q: %s", sc.sequence, st.ID, fmt.Sprintf(format, args...))
			}
			return p.errf(st.line, "stage %q: %s", st.ID, fmt.Sprintf(format, args...))
		}
		if st.OnSuccess == ReservedFail {
			return nil, fail("on_success cannot be %q — a sequence is left on success through its last stage; %q is for an edge", ReservedFail, ReservedFail)
		}
		succ, err := p.target(sc, st.OnSuccess)
		if err != nil {
			return nil, fail("on_success: %v", err)
		}
		if succ == "" && i == len(stages)-1 {
			// The sequence's last stage: its success is the sequence stage's.
			succ = sc.onSuccess
		}
		edges := []struct {
			kind string
			e    **DocEdge
		}{{EdgeFail, &st.OnFail}, {EdgeReject, &st.OnReject}, {EdgeTimeout, &st.OnTimeout}}
		for _, ed := range edges {
			resolved, err := p.edgeIn(sc, *ed.e)
			if err != nil {
				return nil, fail("%s: %v", ed.kind, err)
			}
			*ed.e = resolved
		}
		if st.Sequence == "" {
			st.OnSuccess = succ
			if sc.sequence != "" {
				st.ID, st.scope = sc.prefix+st.ID, sc.path
			}
			out = append(out, st)
			continue
		}
		seq := p.sequences[st.Sequence]
		seq.used = true
		child := &expandScope{
			sequence: seq.doc.ID, prefix: sc.prefix + st.ID + ".", path: st.ID,
			local: map[string]string{}, onSuccess: succ, onFail: st.OnFail,
		}
		if sc.path != "" {
			child.path = sc.path + "." + st.ID
		}
		for _, ss := range seq.doc.Stages {
			child.local[ss.ID] = child.prefix + ss.ID
			if ss.Sequence != "" {
				child.local[ss.ID] = p.entryOf(ss.Sequence, child.prefix+ss.ID+".")
			}
		}
		inner, err := p.expand(seq.doc.Stages, child)
		if err != nil {
			return nil, err
		}
		out = append(out, inner...)
	}
	return out, nil
}

// target resolves a route target written in sc: a terminal stays itself, a
// sequence's own id becomes its expanded id, and in a chain a sequence
// stage's id becomes its first stage.
func (p *parser) target(sc *expandScope, to string) (string, error) {
	if to == "" || routeTerminal(to) {
		return to, nil
	}
	if sc.sequence != "" {
		if id, ok := sc.local[to]; ok {
			return id, nil
		}
		return "", fmt.Errorf("%q is not a stage of sequence %q — a sequence's routes stay inside it: it is left through its last stage, or by %q (the sequence stage's on_fail)", to, sc.sequence, ReservedFail)
	}
	if strings.Contains(to, ".") {
		return "", fmt.Errorf("%q is inside a sequence — route to the sequence stage itself (its first stage)", to)
	}
	if id, ok := p.topSequences[to]; ok {
		return id, nil
	}
	return to, nil
}

// edgeIn resolves an edge chain written in sc. `fail` is the sequence
// stage's on_fail, spliced in where it is said; nothing may follow it.
func (p *parser) edgeIn(sc *expandScope, e *DocEdge) (*DocEdge, error) {
	if e == nil {
		return nil, nil
	}
	if e.To == ReservedFail {
		if sc.sequence == "" {
			return nil, fmt.Errorf("%q is the sequence stage's on_fail — it only exists inside a sequence", ReservedFail)
		}
		if e.Max != nil || e.Then != nil {
			return nil, fmt.Errorf("max and then do not apply when to is %q — the sequence stage's on_fail says what follows", ReservedFail)
		}
		return cloneEdge(sc.onFail), nil
	}
	to, err := p.target(sc, e.To)
	if err != nil {
		return nil, err
	}
	out := &DocEdge{To: to, Max: e.Max, Feed: e.Feed}
	if out.Then, err = p.edgeIn(sc, e.Then); err != nil {
		return nil, err
	}
	return out, nil
}

func cloneEdge(e *DocEdge) *DocEdge {
	if e == nil {
		return nil
	}
	c := *e
	if e.Max != nil {
		m := *e.Max
		c.Max = &m
	}
	c.Then = cloneEdge(e.Then)
	return &c
}

// unusedSequences lists the sequences nothing runs, in document order.
func (p *parser) unusedSequences(raw []DocSequence) []string {
	var out []string
	for i := range raw {
		if s := p.sequences[raw[i].ID]; s != nil && !s.used && raw[i].ID != MainPhase {
			out = append(out, raw[i].ID)
		}
	}
	return out
}
