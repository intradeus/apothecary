package flow

import (
	"fmt"
	"strings"
)

// Blocks (FLOWLANG §1 *Blocks*) are chains of stages written once under
// `blocks:` and included where they are needed. An include expands in place
// at parse time, so everything after the parser — validation, the engine,
// the events — sees ordinary stages:
//
//   - the block's stage ids get the include's id and a dot in front
//     (make.ship.test). A hand-written id cannot contain a dot, so an
//     expansion never collides with a stage;
//   - a route inside a block names the block's own stages and includes. It
//     leaves the block only through its last stage, whose success is the
//     include's on_success, or through `fail`, which is the include's
//     on_fail: that chain is spliced in where `fail` is said (an include
//     without one makes it a dead end, §3);
//   - outside, an include's id is a route target: its first stage. Nothing
//     routes into the middle of an include;
//   - blocks may include blocks, at most maxIncludeDepth levels deep, and a
//     block that includes itself is refused;
//   - the main chain is a block named `main` (MainPhase), so a watcher can
//     run it again (`run: main`).

// blockDef is one declared block and whether any include expanded it.
type blockDef struct {
	doc  *DocBlock
	line int
	used bool
}

// expandScope is where a list of stages was written: a chain (block "") or
// a block expanded for one include.
type expandScope struct {
	block  string // the block being expanded; "" for a phase's own stages
	prefix string // what the block's ids get in front: "make.ship."
	path   string // what its stages record as Stage.Include: "make.ship"
	// local maps the block's ids to their expanded ids (an include's: its
	// first stage's).
	local map[string]string
	// onSuccess and onFail are the include's routes, resolved where the
	// include is written: what the last stage's success and `fail` become.
	// onSuccess "" leaves the success to the sugar (the next stage).
	onSuccess string
	onFail    *DocEdge
}

// idProblem says what is wrong with a hand-written stage (or include) id,
// or "" when it is fine.
func idProblem(id string) string {
	switch {
	case id == "":
		return "id is required"
	case !slugRe.MatchString(id):
		return "id is not a slug (lowercase [a-z0-9_-], starting with a letter)"
	case id == TerminalEnd:
		return fmt.Sprintf("id %q is reserved (the end of a phase: the run goes back to waiting)", TerminalEnd)
	case id == TerminalDone:
		return fmt.Sprintf("id %q is reserved (the end of the run)", TerminalDone)
	case id == TerminalAbort:
		return fmt.Sprintf("id %q is reserved (the deliberate-stop edge terminal)", TerminalAbort)
	case id == ReservedDebug:
		return fmt.Sprintf("id %q is reserved (the debug leaf: `then: debug` parks the run for a human and re-enters the failing stage)", ReservedDebug)
	case id == ReservedStart:
		return fmt.Sprintf("id %q is reserved (the graph editor's start marker)", ReservedStart)
	case id == ReservedFail:
		return fmt.Sprintf("id %q is reserved (inside a block, the include's on_fail)", ReservedFail)
	}
	return ""
}

// includeProblem says what an include stage carries that it must not, or "".
// An include runs a block's stages: it has no type of its own, and it fails
// as a whole.
func includeProblem(st *DocStage) string {
	if msgs := st.retired(); len(msgs) > 0 {
		return msgs[0]
	}
	if st.Type != "" {
		return fmt.Sprintf("an include has no type — it runs block %q's stages", st.Include)
	}
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"when", st.When != ""}, {"agent", st.Agent != ""}, {"model", st.Model != ""}, {"harness", st.Harness != ""},
		{"max_turns", st.MaxTurns != nil}, {"timeout", st.Timeout != ""}, {"input", st.Input != nil}, {"output", st.Output != nil},
		{"run", st.Run != ""}, {"run_ref", st.RunRef != ""}, {"each_repo", st.EachRepo}, {"question", st.Question != ""},
		{"auto_approve", st.AutoApprove != ""}, {"auto_reject", st.AutoReject != ""}, {"action", st.Action != ""}, {"for", st.For != ""},
		{"on_reject", st.OnReject != nil}, {"on_timeout", st.OnTimeout != nil},
	} {
		if f.set {
			return fmt.Sprintf("field %q does not apply to an include (it takes id, include, description, on_success and on_fail: the block's stages say the rest, and it fails as a whole)", f.name)
		}
	}
	return ""
}

// buildBlocks validates the blocks themselves — ids, what they include, no
// cycle, the nesting depth — whether or not anything includes them, and
// records each stage's source line for the expansion.
func (p *parser) buildBlocks(raw []DocBlock) error {
	p.blocks, p.blockOrder = map[string]*blockDef{}, nil
	nodes := p.seqNodes("blocks")
	for i := range raw {
		b := &raw[i]
		line := p.seqLine("blocks", i)
		fail := func(format string, args ...any) error {
			id := b.ID
			if id == "" {
				id = fmt.Sprintf("#%d", i+1)
			}
			return p.errf(line, "block %q: %s", id, fmt.Sprintf(format, args...))
		}
		if b.ID == "" {
			return fail("id is required")
		}
		if !slugRe.MatchString(b.ID) {
			return fail("id is not a slug (lowercase [a-z0-9_-], starting with a letter)")
		}
		if prev, dup := p.blocks[b.ID]; dup {
			return fail("duplicate block id (first declared at line %d)", prev.line)
		}
		if len(b.Stages) == 0 {
			return fail("a block needs at least one stage")
		}
		if b.ID == MainPhase {
			// The main chain, appended by build: its stages carry their lines.
			line = p.keyLine("main")
		} else {
			var lines []int
			if i < len(nodes) {
				lines = phaseStageLines(nodes[i])
			}
			for j := range b.Stages {
				b.Stages[j].line = line
				if j < len(lines) {
					b.Stages[j].line = lines[j]
				}
			}
		}
		p.blocks[b.ID] = &blockDef{doc: b, line: line}
		p.blockOrder = append(p.blockOrder, b.ID)
	}
	for i := range raw {
		b := &raw[i]
		seen := map[string]int{}
		for j := range b.Stages {
			st := &b.Stages[j]
			fail := func(format string, args ...any) error {
				id := st.ID
				if id == "" {
					id = fmt.Sprintf("#%d", j+1)
				}
				return p.errf(st.line, "block %q, stage %q: %s", b.ID, id, fmt.Sprintf(format, args...))
			}
			if msg := idProblem(st.ID); msg != "" {
				return fail("%s", msg)
			}
			if prev, dup := seen[st.ID]; dup {
				return fail("duplicate id (first declared at line %d)", prev)
			}
			seen[st.ID] = st.line
			if st.Include == "" {
				continue
			}
			if p.blocks[st.Include] == nil {
				return fail("include %q: no block of that name (blocks: %s)", st.Include, p.blockNames())
			}
			if msg := includeProblem(st); msg != "" {
				return fail("%s", msg)
			}
		}
	}
	depths := map[string]int{}
	for i := range raw {
		d, err := p.blockDepth(raw[i].ID, nil, depths)
		if err != nil {
			return err
		}
		if d > maxIncludeDepth {
			return p.errf(p.blocks[raw[i].ID].line, "block %q: includes nest %d levels deep; the limit is %d", raw[i].ID, d, maxIncludeDepth)
		}
	}
	return nil
}

// blockDepth is how many levels of blocks an include of name expands into:
// 1 for a block that includes none. A block reached again while expanding
// itself is a cycle.
func (p *parser) blockDepth(name string, stack []string, memo map[string]int) (int, error) {
	if d, ok := memo[name]; ok {
		return d, nil
	}
	for i, s := range stack {
		if s == name {
			cycle := append(append([]string(nil), stack[i:]...), name)
			return 0, p.errf(p.blocks[name].line, "block %q includes itself (%s)", name, strings.Join(cycle, " → "))
		}
	}
	d := 1
	for _, st := range p.blocks[name].doc.Stages {
		if st.Include == "" {
			continue
		}
		cd, err := p.blockDepth(st.Include, append(stack, name), memo)
		if err != nil {
			return 0, err
		}
		d = max(d, cd+1)
	}
	memo[name] = d
	return d, nil
}

func (p *parser) blockNames() string {
	if len(p.blockOrder) == 0 {
		return "none declared"
	}
	return strings.Join(p.blockOrder, ", ")
}

// indexIncludes checks the includes written in chains and records where
// each one begins, so a route to an include's id lands on its first stage.
// An include's id is in the stages' one namespace.
func (p *parser) indexIncludes(phases []phaseSrc) error {
	p.topIncludes = map[string]string{}
	ids := map[string]int{}
	for i := range phases {
		for j := range phases[i].stages {
			st := &phases[i].stages[j]
			if st.Include == "" {
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
				return fail("duplicate id (first declared at line %d) — stage ids are one namespace across phases, includes too", prev)
			}
			ids[st.ID] = st.line
			if p.blocks[st.Include] == nil {
				return fail("include %q: no block of that name (blocks: %s)", st.Include, p.blockNames())
			}
			if msg := includeProblem(st); msg != "" {
				return fail("%s", msg)
			}
			p.topIncludes[st.ID] = p.entryOf(st.Include, st.ID+".")
		}
	}
	// A plain stage written after an include of the same id.
	for i := range phases {
		for j := range phases[i].stages {
			st := &phases[i].stages[j]
			if _, inc := p.topIncludes[st.ID]; inc && st.Include == "" {
				return p.errf(st.line, "stage %q: duplicate id (an include is named %q) — stage ids are one namespace across phases, includes too", st.ID, st.ID)
			}
		}
	}
	return nil
}

// entryOf is the expanded id of the first stage an include of name runs,
// its stages' ids starting with prefix.
func (p *parser) entryOf(name, prefix string) string {
	first := &p.blocks[name].doc.Stages[0]
	if first.Include != "" {
		return p.entryOf(first.Include, prefix+first.ID+".")
	}
	return prefix + first.ID
}

// expand inlines every include in stages, recursively: what a phase's
// stages are once its blocks are written out. Each stage keeps its source
// line; a block's stages also record the include path they came from.
func (p *parser) expand(stages []DocStage, sc *expandScope) ([]DocStage, error) {
	var out []DocStage
	for i := range stages {
		st := stages[i]
		fail := func(format string, args ...any) error {
			if sc.block != "" {
				return p.errf(st.line, "block %q, stage %q: %s", sc.block, st.ID, fmt.Sprintf(format, args...))
			}
			return p.errf(st.line, "stage %q: %s", st.ID, fmt.Sprintf(format, args...))
		}
		if st.OnSuccess == ReservedFail {
			return nil, fail("on_success cannot be %q — a block is left on success through its last stage; %q is for an edge", ReservedFail, ReservedFail)
		}
		succ, err := p.target(sc, st.OnSuccess)
		if err != nil {
			return nil, fail("on_success: %v", err)
		}
		if succ == "" && i == len(stages)-1 {
			// The block's last stage: its success is the include's.
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
		if st.Include == "" {
			st.OnSuccess = succ
			if sc.block != "" {
				st.ID, st.scope = sc.prefix+st.ID, sc.path
			}
			out = append(out, st)
			continue
		}
		blk := p.blocks[st.Include]
		blk.used = true
		child := &expandScope{
			block: blk.doc.ID, prefix: sc.prefix + st.ID + ".", path: st.ID,
			local: map[string]string{}, onSuccess: succ, onFail: st.OnFail,
		}
		if sc.path != "" {
			child.path = sc.path + "." + st.ID
		}
		for _, bs := range blk.doc.Stages {
			child.local[bs.ID] = child.prefix + bs.ID
			if bs.Include != "" {
				child.local[bs.ID] = p.entryOf(bs.Include, child.prefix+bs.ID+".")
			}
		}
		inner, err := p.expand(blk.doc.Stages, child)
		if err != nil {
			return nil, err
		}
		out = append(out, inner...)
	}
	return out, nil
}

// target resolves a route target written in sc: a terminal stays itself, a
// block's own id becomes its expanded id, and in a phase an include's id
// becomes its first stage.
func (p *parser) target(sc *expandScope, to string) (string, error) {
	if to == "" || routeTerminal(to) {
		return to, nil
	}
	if sc.block != "" {
		if id, ok := sc.local[to]; ok {
			return id, nil
		}
		return "", fmt.Errorf("%q is not a stage of block %q — a block's routes stay inside it: it is left through its last stage, or by %q (the include's on_fail)", to, sc.block, ReservedFail)
	}
	if strings.Contains(to, ".") {
		return "", fmt.Errorf("%q is inside an include — route to the include itself (its first stage)", to)
	}
	if id, ok := p.topIncludes[to]; ok {
		return id, nil
	}
	return to, nil
}

// edgeIn resolves an edge chain written in sc. `fail` is the include's
// on_fail, spliced in where it is said; nothing may follow it.
func (p *parser) edgeIn(sc *expandScope, e *DocEdge) (*DocEdge, error) {
	if e == nil {
		return nil, nil
	}
	if e.To == ReservedFail {
		if sc.block == "" {
			return nil, fmt.Errorf("%q is the include's on_fail — it only exists inside a block", ReservedFail)
		}
		if e.Max != nil || e.Then != nil {
			return nil, fmt.Errorf("max and then do not apply when to is %q — the include's on_fail says what follows", ReservedFail)
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

// unusedBlocks lists the blocks nothing includes, in document order.
func (p *parser) unusedBlocks(raw []DocBlock) []string {
	var out []string
	for i := range raw {
		if b := p.blocks[raw[i].ID]; b != nil && !b.used && raw[i].ID != MainPhase {
			out = append(out, raw[i].ID)
		}
	}
	return out
}
