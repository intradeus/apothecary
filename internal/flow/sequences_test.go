package flow

import (
	"strings"
	"testing"
)

// sequencesDoc runs sequences from the main chain and a watcher, and a
// sequence from a sequence: make runs build, ship (a sequence of its own),
// review and publish.
const sequencesDoc = header + `sequences:
  - id: ship
    description: commit what was left and run the tests
    stages:
      - { id: commit, type: command, run: x, on_fail: debug }
      - { id: test, type: command, run: x, on_fail: fail,
          on_timeout: { to: test, max: 1, then: fail } }
  - id: publish
    stages:
      - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
  - id: make
    stages:
      - { id: build, type: command, run: x, output: { mr: { to: artifacts/mr.md, type: file } },
          on_fail: { to: build, max: 2, then: debug } }
      - { id: ship, sequence: ship, on_fail: { to: build, max: 5, then: debug } }
      - { id: review, type: command, run: x, on_fail: { to: build, max: 3, then: debug } }
      - { id: publish, sequence: publish }
main:
  - { id: plan, type: command, run: x, on_fail: { to: make, max: 1 } }
  - { id: make, sequence: make }
watchers:
  - id: fixes
    on: mr_conflicts
    stages:
      - { id: patch, type: command, run: x }
      - { id: again, sequence: ship, on_fail: { to: patch, max: 2 }, on_success: wrap }
      - { id: wrap, type: command, run: x, on_success: end }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`

// A sequence stage expands in place: ids get its path, routes inside a
// sequence name its own stages, the last stage's success is the sequence
// stage's, and `fail` is the sequence stage's on_fail spliced in where it is
// said.
func TestSequencesExpandInPlace(t *testing.T) {
	f, err := Parse("t.yaml", []byte(sequencesDoc))
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
	if d := f.Phase(MainPhase); d.First != 0 || d.Last != 5 {
		t.Errorf("main spans %d..%d", d.First, d.Last)
	}
	st := func(id string) *Stage {
		t.Helper()
		s := f.Stage(id)
		if s == nil {
			t.Fatalf("no stage %s", id)
		}
		return s
	}
	// A sequence stage's id is a route target: its first stage.
	if e := st("plan").Edge(EdgeFail); e.To != "make.build" || e.Max != 1 {
		t.Errorf("plan on_fail = %+v", e)
	}
	for id, next := range map[string]string{
		"plan": "make.build", "make.build": "make.ship.commit", "make.ship.commit": "make.ship.test",
		// ship's last stage leaves by the sequence stage's success: the
		// sugar, here the next stage of make.
		"make.ship.test": "make.review", "make.review": "make.publish.mr", "make.publish.mr": TerminalEnd,
		// a sequence stage's explicit on_success is its sequence's last
		// success.
		"again.test": "wrap",
	} {
		if got := st(id).OnSuccess; got != next {
			t.Errorf("%s on_success = %q, want %q", id, got, next)
		}
	}
	// fail → the sequence stage's on_fail, resolved where it is written.
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
	for id, path := range map[string]string{"plan": "", "make.build": "make", "make.ship.test": "make.ship", "make.publish.mr": "make.publish", "again.commit": "again"} {
		if got := st(id).Sequence; got != path {
			t.Errorf("%s sequence path = %q, want %q", id, got, path)
		}
	}
	if st("make.ship.test").Phase != MainPhase || st("again.test").Phase != "fixes" {
		t.Error("expanded stages belong to the chain that runs them")
	}
	if st("make.ship.test").Line != 9 {
		t.Errorf("make.ship.test line = %d, want its line in the sequence (9)", st("make.ship.test").Line)
	}
	for _, w := range f.Warnings {
		t.Errorf("warning: %s", w)
	}
}

// fail in a sequence whose sequence stage has no on_fail is a dead end;
// fail said by a sequence stage inside a sequence is the enclosing sequence
// stage's on_fail.
func TestFailWithoutAndThroughSequenceStages(t *testing.T) {
	doc := header + `sequences:
  - id: inner
    stages:
      - { id: t, type: command, run: x, on_fail: fail }
  - id: outer
    stages:
      - { id: in, sequence: inner, on_fail: fail }
      - { id: u, type: command, run: x, on_fail: { to: u, max: 1, then: fail } }
main:
  - { id: a, type: command, run: x }
  - { id: o, sequence: outer, on_fail: { to: a, max: 2 } }
  - { id: bare, sequence: inner, on_success: done }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if e := f.Stage("o.in.t").Edge(EdgeFail); e == nil || e.To != "a" || e.Max != 2 {
		t.Errorf("o.in.t on_fail = %+v, want the outer sequence stage's", e)
	}
	if e := f.Stage("o.u").Edge(EdgeFail); e == nil || e.To != "o.u" || e.Then == nil || e.Then.To != "a" {
		t.Errorf("o.u on_fail = %+v", e)
	}
	if e := f.Stage("bare.t").Edge(EdgeFail); e != nil {
		t.Errorf("bare.t on_fail = %+v, want none: its sequence stage has no on_fail", e)
	}
	if f.Stage("bare.t").OnSuccess != TerminalDone {
		t.Errorf("bare.t on_success = %q", f.Stage("bare.t").OnSuccess)
	}
}

func TestSequenceErrors(t *testing.T) {
	seq := func(stages string) string { return "sequences:\n  - id: b\n    stages:\n" + stages }
	use := "main:\n  - { id: a, type: command, run: x }\n  - { id: inc, sequence: b, on_success: done }\n"
	for _, tc := range []struct{ name, doc, substr string }{
		{"unknown sequence", header + "main:\n  - { id: a, sequence: nope, on_success: done }\n", `sequence "nope": no sequence of that name`},
		{"cycle", header + "sequences:\n  - { id: x, stages: [{ id: s, sequence: y }] }\n  - { id: y, stages: [{ id: s, sequence: x }] }\n" + "main:\n  - { id: a, sequence: x, on_success: done }\n", "runs itself (x → y → x)"},
		{"too deep", header + "sequences:\n  - { id: b1, stages: [{ id: s, sequence: b2 }] }\n  - { id: b2, stages: [{ id: s, sequence: b3 }] }\n  - { id: b3, stages: [{ id: s, sequence: b4 }] }\n  - { id: b4, stages: [{ id: s, type: command, run: x }] }\n" + "main:\n  - { id: a, sequence: b1, on_success: done }\n", "sequences nest 4 levels deep; the limit is 3"},
		{"route out of a sequence", header + seq("      - { id: s, type: command, run: x, on_success: a }\n") + use, `"a" is not a stage of sequence "b"`},
		{"edge out of a sequence", header + seq("      - { id: s, type: command, run: x, on_fail: { to: a, max: 1 } }\n") + use, `on_fail: "a" is not a stage of sequence "b"`},
		{"fail outside a sequence", header + "main:\n  - { id: a, type: command, run: x, on_fail: fail, on_success: done }\n", "only exists inside a sequence"},
		{"fail on a sequence stage in a chain", header + seq("      - { id: s, type: command, run: x }\n") + "main:\n  - { id: inc, sequence: b, on_fail: fail, on_success: done }\n", "only exists inside a sequence"},
		{"fail with max", header + seq("      - { id: s, type: command, run: x, on_fail: { to: fail, max: 1 } }\n") + use, `max and then do not apply when to is "fail"`},
		{"on_success fail", header + seq("      - { id: s, type: command, run: x, on_success: fail }\n") + use, `on_success cannot be "fail"`},
		{"sequence stage with a type", header + seq("      - { id: s, type: command, run: x }\n") + "main:\n  - { id: inc, sequence: b, type: command, on_success: done }\n", "a sequence stage has no type"},
		{"sequence stage with run_if", header + seq("      - { id: s, type: command, run: x }\n") + "main:\n  - { id: inc, sequence: b, run_if: run.ticket.tracker, on_success: done }\n", `field "run_if" does not apply to a sequence stage`},
		{"sequence stage with on_reject", header + seq("      - { id: s, type: command, run: x }\n") + "main:\n  - { id: inc, sequence: b, on_reject: abort, on_success: done }\n", `field "on_reject" does not apply to a sequence stage`},
		{"route into a sequence stage", header + seq("      - { id: s, type: command, run: x }\n      - { id: t, type: command, run: x }\n") + "main:\n  - { id: a, type: command, run: x, on_success: inc.t }\n  - { id: inc, sequence: b, on_success: done }\n", `"inc.t" is inside a sequence`},
		{"sequence stage id taken", header + seq("      - { id: s, type: command, run: x }\n") + "main:\n  - { id: inc, type: command, run: x }\n  - { id: inc, sequence: b, on_success: done }\n", "duplicate id"},
		{"stage after a sequence stage of its id", header + seq("      - { id: s, type: command, run: x }\n") + "main:\n  - { id: inc, sequence: b }\n  - { id: inc, type: command, run: x, on_success: done }\n", "duplicate id"},
		{"reserved id in a sequence", header + seq("      - { id: fail, type: command, run: x }\n") + use, `id "fail" is reserved`},
		{"reserved fail in a chain", header + "main:\n  - { id: fail, type: command, run: x, on_success: done }\n", `id "fail" is reserved`},
		{"dotted id", header + "main:\n  - { id: a.b, type: command, run: x, on_success: done }\n", "not a slug"},
		{"empty sequence", header + "sequences:\n  - { id: b, stages: [] }\n" + use, "a sequence needs at least one stage"},
		{"duplicate sequence", header + "sequences:\n  - { id: b, stages: [{ id: s, type: command, run: x }] }\n  - { id: b, stages: [{ id: s, type: command, run: x }] }\n" + use, "duplicate sequence id"},
		{"duplicate stage in a sequence", header + seq("      - { id: s, type: command, run: x }\n      - { id: s, type: command, run: x }\n") + use, `sequence "b", stage "s": duplicate id`},
		{"unknown field in a sequence's stage", header + seq("      - { id: s, type: command, run: x, bogus: 1 }\n") + use, "bogus"},
		{"stage errors name the expansion", header + seq("      - { id: s, type: command }\n") + use, `stage "inc.s": exactly one of run / run_ref`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("err = %v, want %q", err, tc.substr)
			}
		})
	}
	// The error points at the sequence's line, not the sequence stage's.
	wantErr(t, header+seq("      - { id: s, type: command, run: x, on_success: a }\n")+use, "not a stage of sequence", 7)
}

// A sequence nothing runs is not checked, and the parser says so.
func TestUnusedSequenceWarns(t *testing.T) {
	f, err := Parse("t.yaml", []byte(header+"sequences:\n  - { id: spare, stages: [{ id: s, type: command, run: x }] }\nmain:\n  - { id: a, type: command, run: x }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Warnings) != 1 || !strings.Contains(f.Warnings[0], `sequence "spare" is never run`) {
		t.Errorf("warnings = %v", f.Warnings)
	}
}

// The editor's document keeps sequences and sequence stages as written.
func TestSequencesRoundTripTheDocument(t *testing.T) {
	d, err := ParseDoc("t.yaml", []byte(sequencesDoc))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Sequences) != 3 || d.Sequences[0].ID != "ship" || d.Sequences[0].Description == "" || len(d.Sequences[2].Stages) != 4 || d.Sequences[2].Stages[1].Sequence != "ship" {
		t.Errorf("sequences = %+v", d.Sequences)
	}
	if st := d.Main[1]; st.ID != "make" || st.Sequence != "make" || st.Type != "" {
		t.Errorf("sequence stage = %+v", st)
	}
}

// A flow whose runs are started on a merge request someone opened acts on
// it from the start: its main chain's subject is that merge request, so a
// subject action needs no guard and nothing has to open one.
func TestOperatesOnAMergeRequest(t *testing.T) {
	doc := header + `operates_on: merge_request
main:
  - { id: read, type: command, run: x, output: { review: { to: artifacts/review.yaml, type: review_threads } } }
  - { id: post, type: action, action: post_review, input: { review: { from: artifacts/review.yaml, type: review_threads } } }
  - { id: wait_merge, type: wait, for: mr_merged, on_fail: done }
outcomes:
  - { id: merged, on: mr_merged, then: done }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ph := f.Phase(MainPhase)
	if !f.OperatesOnMR() || !ph.IsMain() || !ph.OnMR() || !ph.HasSubject() || ph.Fires() || ph.On.Start != StartManual {
		t.Errorf("main = %+v", ph)
	}
	if b := f.Main(); b == nil || b.ID != MainPhase {
		t.Errorf("main = %+v", b)
	}
	for _, w := range f.Warnings {
		t.Errorf("warning: %s", w)
	}
	if f.Phase("merged").HasSubject() {
		t.Error("mr_merged holds for every merge request: no subject")
	}
}
