package flow

import (
	"strings"
	"testing"
)

// blocksDoc includes blocks from two phases and a block from a block: make
// runs build, ship (a block of its own), review and publish.
const blocksDoc = header + `blocks:
  - id: ship
    description: commit what was left and run the tests
    stages:
      - { id: commit, type: command, run: x, on_fail: debug }
      - { id: test, type: command, run: x, on_fail: fail,
          on_timeout: { to: test, max: 1, then: fail } }
  - id: publish
    stages:
      - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }
  - id: make
    stages:
      - { id: build, type: command, run: x, output: { mr: artifacts/mr.md },
          on_fail: { to: build, max: 2, then: debug } }
      - { id: ship, include: ship, on_fail: { to: build, max: 5, then: debug } }
      - { id: review, type: command, run: x, on_fail: { to: build, max: 3, then: debug } }
      - { id: publish, include: publish }
phases:
  - id: deliver
    on: run_start
    stages:
      - { id: plan, type: command, run: x, on_fail: { to: make, max: 1 } }
      - { id: make, include: make }
  - id: fixes
    on: mr_conflicts
    stages:
      - { id: patch, type: command, run: x }
      - { id: again, include: ship, on_fail: { to: patch, max: 2 }, on_success: wrap }
      - { id: wrap, type: command, run: x, on_success: end }
  - { id: shipped, on: mr_merged, then: done }
`

// An include expands in place: ids get the include's path, routes inside a
// block name its own stages, the last stage's success is the include's, and
// `fail` is the include's on_fail spliced in where it is said.
func TestBlocksExpandInPlace(t *testing.T) {
	f, err := Parse("t.yaml", []byte(blocksDoc))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range f.Stages {
		ids = append(ids, s.ID)
	}
	want := "plan make.build make.ship.commit make.ship.test make.review make.publish.mr patch again.commit again.test wrap"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("stages = %s\n want %s", got, want)
	}
	if d := f.Phase("deliver"); d.First != 0 || d.Last != 5 {
		t.Errorf("deliver spans %d..%d", d.First, d.Last)
	}
	st := func(id string) *Stage {
		t.Helper()
		s := f.Stage(id)
		if s == nil {
			t.Fatalf("no stage %s", id)
		}
		return s
	}
	// An include's id is a route target: its first stage.
	if e := st("plan").Edge(EdgeFail); e.To != "make.build" || e.Max != 1 {
		t.Errorf("plan on_fail = %+v", e)
	}
	for id, next := range map[string]string{
		"plan": "make.build", "make.build": "make.ship.commit", "make.ship.commit": "make.ship.test",
		// ship's last stage leaves by the include's success: the sugar, here
		// the next stage of make.
		"make.ship.test": "make.review", "make.review": "make.publish.mr", "make.publish.mr": TerminalEnd,
		// an include's explicit on_success is its block's last success.
		"again.test": "wrap",
	} {
		if got := st(id).OnSuccess; got != next {
			t.Errorf("%s on_success = %q, want %q", id, got, next)
		}
	}
	// fail → the include's on_fail, resolved where the include is written.
	if e := st("make.ship.test").Edge(EdgeFail); e == nil || e.To != "make.build" || e.Max != 5 || e.Then == nil || e.Then.To != ReservedDebug {
		t.Errorf("make.ship.test on_fail = %+v", e)
	}
	if e := st("make.ship.test").Edge(EdgeTimeout); e == nil || e.To != "make.ship.test" || e.Max != 1 || e.Then == nil || e.Then.To != "make.build" || e.Then.Max != 5 || e.Then.Then.To != ReservedDebug {
		t.Errorf("make.ship.test on_timeout = %+v", e)
	}
	if e := st("again.test").Edge(EdgeFail); e == nil || e.To != "patch" || e.Max != 2 || e.Then != nil {
		t.Errorf("again.test on_fail = %+v", e)
	}
	if e := st("make.review").Edge(EdgeFail); e.To != "make.build" {
		t.Errorf("make.review on_fail = %+v", e)
	}
	for id, inc := range map[string]string{"plan": "", "make.build": "make", "make.ship.test": "make.ship", "make.publish.mr": "make.publish", "again.commit": "again"} {
		if got := st(id).Include; got != inc {
			t.Errorf("%s include = %q, want %q", id, got, inc)
		}
	}
	if st("make.ship.test").Phase != "deliver" || st("again.test").Phase != "fixes" {
		t.Error("expanded stages belong to the phase that includes them")
	}
	if st("make.ship.test").Line != 9 {
		t.Errorf("make.ship.test line = %d, want its line in the block (9)", st("make.ship.test").Line)
	}
	for _, w := range f.Warnings {
		t.Errorf("warning: %s", w)
	}
}

// fail in a block whose include has no on_fail is a dead end; fail said by
// an include inside a block is the enclosing include's on_fail.
func TestFailWithoutAndThroughIncludes(t *testing.T) {
	doc := header + `blocks:
  - id: inner
    stages:
      - { id: t, type: command, run: x, on_fail: fail }
  - id: outer
    stages:
      - { id: in, include: inner, on_fail: fail }
      - { id: u, type: command, run: x, on_fail: { to: u, max: 1, then: fail } }
stages:
  - { id: a, type: command, run: x }
  - { id: o, include: outer, on_fail: { to: a, max: 2 } }
  - { id: bare, include: inner, on_success: done }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if e := f.Stage("o.in.t").Edge(EdgeFail); e == nil || e.To != "a" || e.Max != 2 {
		t.Errorf("o.in.t on_fail = %+v, want the outer include's", e)
	}
	if e := f.Stage("o.u").Edge(EdgeFail); e == nil || e.To != "o.u" || e.Then == nil || e.Then.To != "a" {
		t.Errorf("o.u on_fail = %+v", e)
	}
	if e := f.Stage("bare.t").Edge(EdgeFail); e != nil {
		t.Errorf("bare.t on_fail = %+v, want none: its include has no on_fail", e)
	}
	if f.Stage("bare.t").OnSuccess != TerminalDone {
		t.Errorf("bare.t on_success = %q", f.Stage("bare.t").OnSuccess)
	}
}

func TestBlockErrors(t *testing.T) {
	blk := func(stages string) string { return "blocks:\n  - id: b\n    stages:\n" + stages }
	use := "stages:\n  - { id: a, type: command, run: x }\n  - { id: inc, include: b, on_success: done }\n"
	for _, tc := range []struct{ name, doc, substr string }{
		{"unknown block", header + "stages:\n  - { id: a, include: nope, on_success: done }\n", `include "nope": no block of that name`},
		{"cycle", header + "blocks:\n  - { id: x, stages: [{ id: s, include: y }] }\n  - { id: y, stages: [{ id: s, include: x }] }\n" + "stages:\n  - { id: a, include: x, on_success: done }\n", "includes itself (x → y → x)"},
		{"too deep", header + "blocks:\n  - { id: b1, stages: [{ id: s, include: b2 }] }\n  - { id: b2, stages: [{ id: s, include: b3 }] }\n  - { id: b3, stages: [{ id: s, include: b4 }] }\n  - { id: b4, stages: [{ id: s, type: command, run: x }] }\n" + "stages:\n  - { id: a, include: b1, on_success: done }\n", "includes nest 4 levels deep; the limit is 3"},
		{"route out of a block", header + blk("      - { id: s, type: command, run: x, on_success: a }\n") + use, `"a" is not a stage of block "b"`},
		{"edge out of a block", header + blk("      - { id: s, type: command, run: x, on_fail: { to: a, max: 1 } }\n") + use, `on_fail: "a" is not a stage of block "b"`},
		{"fail outside a block", header + "stages:\n  - { id: a, type: command, run: x, on_fail: fail, on_success: done }\n", "only exists inside a block"},
		{"fail on an include in a phase", header + blk("      - { id: s, type: command, run: x }\n") + "stages:\n  - { id: inc, include: b, on_fail: fail, on_success: done }\n", "only exists inside a block"},
		{"fail with max", header + blk("      - { id: s, type: command, run: x, on_fail: { to: fail, max: 1 } }\n") + use, `max and then do not apply when to is "fail"`},
		{"on_success fail", header + blk("      - { id: s, type: command, run: x, on_success: fail }\n") + use, `on_success cannot be "fail"`},
		{"include with a type", header + blk("      - { id: s, type: command, run: x }\n") + "stages:\n  - { id: inc, include: b, type: command, on_success: done }\n", "an include has no type"},
		{"include with when", header + blk("      - { id: s, type: command, run: x }\n") + "stages:\n  - { id: inc, include: b, when: tracker, on_success: done }\n", `field "when" does not apply to an include`},
		{"include with on_reject", header + blk("      - { id: s, type: command, run: x }\n") + "stages:\n  - { id: inc, include: b, on_reject: abort, on_success: done }\n", `field "on_reject" does not apply to an include`},
		{"route into an include", header + blk("      - { id: s, type: command, run: x }\n      - { id: t, type: command, run: x }\n") + "stages:\n  - { id: a, type: command, run: x, on_success: inc.t }\n  - { id: inc, include: b, on_success: done }\n", `"inc.t" is inside an include`},
		{"include id taken", header + blk("      - { id: s, type: command, run: x }\n") + "stages:\n  - { id: inc, type: command, run: x }\n  - { id: inc, include: b, on_success: done }\n", "duplicate id"},
		{"stage after an include of its id", header + blk("      - { id: s, type: command, run: x }\n") + "stages:\n  - { id: inc, include: b }\n  - { id: inc, type: command, run: x, on_success: done }\n", "duplicate id"},
		{"reserved id in a block", header + blk("      - { id: fail, type: command, run: x }\n") + use, `id "fail" is reserved`},
		{"reserved fail in a phase", header + "stages:\n  - { id: fail, type: command, run: x, on_success: done }\n", `id "fail" is reserved`},
		{"dotted id", header + "stages:\n  - { id: a.b, type: command, run: x, on_success: done }\n", "not a slug"},
		{"empty block", header + "blocks:\n  - { id: b, stages: [] }\n" + use, "a block needs at least one stage"},
		{"duplicate block", header + "blocks:\n  - { id: b, stages: [{ id: s, type: command, run: x }] }\n  - { id: b, stages: [{ id: s, type: command, run: x }] }\n" + use, "duplicate block id"},
		{"duplicate stage in a block", header + blk("      - { id: s, type: command, run: x }\n      - { id: s, type: command, run: x }\n") + use, `block "b", stage "s": duplicate id`},
		{"unknown field in a block stage", header + blk("      - { id: s, type: command, run: x, bogus: 1 }\n") + use, "bogus"},
		{"stage errors name the expansion", header + blk("      - { id: s, type: command }\n") + use, `stage "inc.s": exactly one of run / run_ref`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("err = %v, want %q", err, tc.substr)
			}
		})
	}
	// The error points at the block's line, not the include's.
	wantErr(t, header+blk("      - { id: s, type: command, run: x, on_success: a }\n")+use, "not a stage of block", 7)
}

// A block nothing includes is not checked, and the parser says so.
func TestUnusedBlockWarns(t *testing.T) {
	f, err := Parse("t.yaml", []byte(header+"blocks:\n  - { id: spare, stages: [{ id: s, type: command, run: x }] }\nstages:\n  - { id: a, type: command, run: x }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Warnings) != 1 || !strings.Contains(f.Warnings[0], `block "spare" is never included`) {
		t.Errorf("warnings = %v", f.Warnings)
	}
}

// The editor's document keeps blocks and includes as written.
func TestBlocksRoundTripTheDocument(t *testing.T) {
	d, err := ParseDoc("t.yaml", []byte(blocksDoc))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Blocks) != 3 || d.Blocks[0].ID != "ship" || d.Blocks[0].Description == "" || len(d.Blocks[2].Stages) != 4 || d.Blocks[2].Stages[1].Include != "ship" {
		t.Errorf("blocks = %+v", d.Blocks)
	}
	if inc := d.Phases[0].Stages[1]; inc.ID != "make" || inc.Include != "make" || inc.Type != "" {
		t.Errorf("include stage = %+v", inc)
	}
}
