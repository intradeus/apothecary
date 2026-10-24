package flow

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// header is a minimal valid preamble; tests append stages to it.
const header = `name: t
version: 4
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
`

// commonAgents is the agents block the stage-shape tests splice in: since v3
// a document declares the agents its stages name (FLOWLANG §1), and most
// parse tests are about stage syntax rather than contracts — so the
// contracts here are permissive on purpose.
const commonAgents = `agents:
  - name: builder
    prompt: build the thing
    inputs:
      - { name: plan, optional: true }
      - { name: changes, optional: true }
      - { name: rejection, optional: true }
      - { name: failure, optional: true }
    outputs:
      - { name: patch, path: artifacts/patch.md }
  - name: b
    prompt: do b
`

func parseErr(t *testing.T, doc string) *Error {
	t.Helper()
	_, err := Parse("t.yaml", []byte(doc))
	if err == nil {
		t.Fatalf("expected error, got none\n---\n%s", doc)
	}
	var fe *Error
	if !errors.As(err, &fe) {
		t.Fatalf("expected *flow.Error, got %T: %v", err, err)
	}
	return fe
}

func wantErr(t *testing.T, doc, substr string, line int) {
	t.Helper()
	fe := parseErr(t, doc)
	if !strings.Contains(fe.Error(), substr) {
		t.Errorf("error %q does not contain %q", fe.Error(), substr)
	}
	if line > 0 && fe.Line != line {
		t.Errorf("error at line %d, want %d: %s", fe.Line, line, fe.Error())
	}
}

// TestCanonicalV4 locks the fixture that exercises every stage construct: all
// stage types, the binding forms, every sentinel, output rebinding.
func TestCanonicalV4(t *testing.T) {
	f, err := Load("testdata/v4.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "canonical" {
		t.Errorf("name = %q", f.Name)
	}
	if len(f.Hash) != 64 {
		t.Errorf("hash = %q, want 64 hex chars", f.Hash)
	}
	if f.Defaults.Timeout != 30*time.Minute || f.Defaults.MaxTurns != 50 ||
		f.Defaults.Harness != "fake" || f.Defaults.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("defaults = %+v", f.Defaults)
	}
	if f.Budget.MaxCostUSD != 10.0 || f.Budget.MaxTotalTurns != 300 {
		t.Errorf("budget = %+v", f.Budget)
	}

	ids := []string{"in_progress", "plan", "plan_gate", "build", "commit_msg", "commit", "test", "review",
		"redact", "push", "debug_push", "mr", "in_review", "final_gate", "ticket_done"}
	if len(f.Stages) != len(ids) {
		t.Fatalf("got %d stages, want %d", len(f.Stages), len(ids))
	}
	for i, id := range ids {
		if f.Stages[i].ID != id {
			t.Errorf("stage[%d] = %q, want %q", i, f.Stages[i].ID, id)
		}
	}

	plan := f.Stage("plan")
	if plan.Model != "anthropic/claude-sonnet-4-6" || plan.MaxTurns != 25 || plan.Timeout != 30*time.Minute {
		t.Errorf("plan defaults not resolved: %+v", plan)
	}
	// No output on the stage: the planner's contract carries the path.
	if len(plan.Outputs) != 0 || plan.Description != "write the implementation plan" {
		t.Errorf("plan = %+v", plan)
	}

	// An agent stage binds inputs by name and may rebind a declared output.
	cm := f.Stage("commit_msg")
	if len(cm.Inputs) != 1 || cm.Inputs[0] != (IOBinding{Name: "plan", Value: "artifacts/plan.md"}) {
		t.Errorf("commit_msg.Inputs = %+v", cm.Inputs)
	}
	if len(cm.Outputs) != 1 || cm.Outputs[0] != (IOBinding{Name: "commit", Value: "artifacts/commit-subject.md"}) {
		t.Errorf("commit_msg.Outputs = %+v", cm.Outputs)
	}

	// A command names its files too — the label documents each one's role.
	commit := f.Stage("commit")
	if len(commit.Inputs) != 1 || commit.Inputs[0] != (IOBinding{Name: "message", Value: "artifacts/commit-subject.md"}) {
		t.Errorf("commit.Inputs = %+v", commit.Inputs)
	}

	review := f.Stage("review")
	if review.Model != "openai/gpt-5" {
		t.Errorf("review model override = %q", review.Model)
	}
	// on_reject needs no stage flag: the agent's verdict output makes it fire,
	// and it declares nothing about what it carries.
	if review.Edge(EdgeReject) == nil || review.Edge(EdgeReject).To != "build" || review.Edge(EdgeReject).Max != 3 {
		t.Errorf("review.on_reject = %+v", review.Edge(EdgeReject))
	}
	if len(review.Inputs) != 2 || review.Inputs[1] != (IOBinding{Name: "changes", Value: SentinelDiffTarget}) {
		t.Errorf("review.Inputs = %+v", review.Inputs)
	}

	test := f.Stage("test")
	if test.RunRef != "verify" || test.Edge(EdgeFail) == nil || test.Edge(EdgeFail).To != "build" ||
		test.Edge(EdgeFail).Max != 5 {
		t.Errorf("test stage = %+v", test)
	}
	// The build stage binds both edge-carried values, so a rejection and a
	// failure land in different, named places.
	build := f.Stage("build")
	if build.Input("rejection") == nil || build.Input("rejection").Value != SentinelDecision ||
		build.Input("failure") == nil || build.Input("failure").Value != SentinelFailure {
		t.Errorf("build.Inputs = %+v", build.Inputs)
	}

	mr := f.Stage("mr")
	if mr.Type != StageAction || mr.Action != "create_mr" ||
		len(mr.Inputs) != 1 || mr.Inputs[0] != (IOBinding{Name: "body", Value: "artifacts/mr.md"}) {
		t.Errorf("mr = %+v", mr)
	}

	gate := f.Stage("final_gate")
	if len(gate.Inputs) != 1 || gate.Inputs[0].Value != SentinelMRURL ||
		gate.AutoApprove != "mr_merged" || gate.Question == "" {
		t.Errorf("final_gate = %+v", gate)
	}
	if dbg := f.Stage("debug_push"); len(dbg.Inputs) != 1 || dbg.Inputs[0].Value != SentinelWorkspace {
		t.Errorf("debug_push.Inputs = %+v, want %s", dbg.Inputs, SentinelWorkspace)
	}

	if f.Index("in_progress") != 0 || f.Index("ticket_done") != len(ids)-1 || f.Index("nope") != -1 {
		t.Error("Index misbehaves")
	}
	// The tracker stages: a canonical status, the tracker's own word, and a
	// canonical one read from an enum parameter.
	if in := f.Stage("in_progress").Input("to"); in == nil || in.Value != "in_progress" {
		t.Errorf("in_progress.to = %+v", in)
	}
	if in := f.Stage("in_review").Input("status"); in == nil || in.Value != "In Review" {
		t.Errorf("in_review.status = %+v", in)
	}
	if in := f.Stage("ticket_done").Input("to"); in == nil || in.Value != "done" {
		t.Errorf("ticket_done.to = %+v", in)
	}
	if p := f.Param("ticket_status"); p == nil || p.Known || strings.Join(p.Enum, ",") != "done,in_review" {
		t.Errorf("ticket_status = %+v", p)
	}
}

// TestReferenceFlowParses locks the repo's shipped expanded-form example (what
// the graph editor emits): abort terminals, on_timeout, then-chains, unlimited
// links and the debug leaf as every stage's rescue.
func TestReferenceFlowParses(t *testing.T) {
	f, err := Load("../../examples/flows/v4.yaml")
	if err != nil {
		t.Fatal(err)
	}
	test := f.Stage("test")
	if test.Edge(EdgeFail).Then == nil || test.Edge(EdgeFail).Then.To != ReservedDebug {
		t.Errorf("test.on_fail.then = %+v, want the debug leaf", test.Edge(EdgeFail).Then)
	}
	if test.Edge(EdgeTimeout) == nil || test.Edge(EdgeTimeout).To != "test" {
		t.Errorf("test.on_timeout = %+v, want self-retry", test.Edge(EdgeTimeout))
	}
	if f.Stage("debug_test") != nil {
		t.Error("the example still carries a debug_test gate; the leaf replaced it")
	}
	// Approval gates reject into a feedback loop — rejecting the final MR
	// review sends the run back to build to address comments, not abort. The
	// per-stage rescues are the debug leaf (asserted above via test).
	if fg := f.Stage("final_gate").Edge(EdgeReject); fg == nil || fg.To != "build" {
		t.Errorf("final_gate.on_reject = %+v, want a loop back to build", f.Stage("final_gate").Edge(EdgeReject))
	}

	// The flow's failure philosophy: every agent/command stage declares
	// on_fail, and every on_fail chain ends at the debug leaf — a human with
	// the workspace, then the same stage again, so the success chain replays
	// from the failure point and never skips a stage.
	for i := range f.Stages {
		s := &f.Stages[i]
		if s.Type != StageAgent && s.Type != StageCommand {
			continue
		}
		e := s.Edge(EdgeFail)
		if e == nil {
			t.Errorf("stage %s: no on_fail", s.ID)
			continue
		}
		for e.Then != nil {
			e = e.Then
		}
		if e.To != ReservedDebug {
			t.Errorf("stage %s: on_fail chain ends at %q, want the debug leaf", s.ID, e.To)
		}
	}
}

func TestStageMapFormBindings(t *testing.T) {
	doc := header + commonAgents + `stages:
  - { id: build, type: agent, agent: builder,
      input: { plan: artifacts/plan.md, changes: $DIFF_TARGET },
      output: { patch: artifacts/patch.md }, on_success: done }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	s := f.Stage("build")
	if len(s.Inputs) != 2 ||
		s.Inputs[0] != (IOBinding{Name: "plan", Value: "artifacts/plan.md"}) ||
		s.Inputs[1] != (IOBinding{Name: "changes", Value: SentinelDiffTarget}) {
		t.Errorf("inputs = %+v", s.Inputs)
	}
	if len(s.Outputs) != 1 || s.Outputs[0] != (IOBinding{Name: "patch", Value: "artifacts/patch.md"}) {
		t.Errorf("outputs = %+v", s.Outputs)
	}
}

// TestCommandBindings: a command binds like everything else. It has no contract
// to check the names against, so they are free labels — which is the point:
// they say what each file is for.
func TestCommandBindings(t *testing.T) {
	doc := header + `stages:
  - { id: verify, type: command, run: "make check",
      input: { plan: artifacts/plan.md, spec: docs/spec.md },
      output: { report: artifacts/report.txt }, on_success: done }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	s := f.Stage("verify")
	want := []IOBinding{{Name: "plan", Value: "artifacts/plan.md"}, {Name: "spec", Value: "docs/spec.md"}}
	if len(s.Inputs) != len(want) {
		t.Fatalf("inputs = %+v", s.Inputs)
	}
	for i, w := range want {
		if s.Inputs[i] != w {
			t.Errorf("input[%d] = %+v, want %+v", i, s.Inputs[i], w)
		}
	}
	if len(s.Outputs) != 1 || s.Outputs[0] != (IOBinding{Name: "report", Value: "artifacts/report.txt"}) {
		t.Errorf("outputs = %+v", s.Outputs)
	}
}

func TestBindingShapeErrors(t *testing.T) {
	agent := func(binding string) string {
		return header + "stages:\n  - { id: a, type: agent, agent: x, " + binding + ", on_success: done }\n"
	}
	cases := []struct{ name, doc, want string }{
		{"dup key", agent("input: { plan: a.md, plan: b.md }"), "bound twice"},
		{"ticket value", agent("input: { plan: ticket }"), "always the TASK section"},
		{"bad input path", agent("input: { plan: /abs }"), "artifact path"},
		{"non-scalar value", agent("input: { plan: [a, b] }"), "value must be a single value"},
		{"bad output path", agent("output: { p: /abs }"), "artifact path"},
		{"bad binding name", agent("input: { Bad Name: a.md }"), "lowercase slug"},
		// One form everywhere: a bare value or a list is a shape error whose
		// message says what to do instead.
		{"agent list input", agent("input: [artifacts/plan.md]"), "name each entry"},
		{"agent scalar output", agent("output: artifacts/out.md"), "name what"},
		{"command list input", header + "stages:\n  - { id: c, type: command, run: x, input: [a.md] }\n",
			"name each entry"},
		{"command sentinel input", header + "stages:\n  - { id: c, type: command, run: x, input: { c: $DIFF_TARGET } }\n",
			"must be a relative artifact path"},
		{"gate scalar input", header + "stages:\n  - { id: g, type: human_gate, input: artifacts/a.md }\n",
			"name what"},
		{"gate two inputs", header + "stages:\n  - { id: g, type: human_gate, input: { a: x.md, b: y.md } }\n",
			"shows one thing"},
		{"gate bad sentinel", header + "stages:\n  - { id: g, type: human_gate, input: { x: $NOPE } }\n",
			"must be a relative artifact path"},
		// Sentinels are scoped: a link is for a human, prompt content is not.
		{"agent gets a link", agent("input: { mr: $MR_URL }"), "is a link for a human_gate"},
		{"gate gets prompt content", header + "stages:\n  - { id: g, type: human_gate, input: { c: $DIFF_TARGET } }\n",
			"not something to show a human"},
		{"gate gets uncommitted", header + "stages:\n  - { id: g, type: human_gate, input: { c: $STATUS } }\n",
			"not something to show a human"},
		{"command uncommitted input", header + "stages:\n  - { id: c, type: command, run: x, input: { c: $STATUS } }\n",
			"must be a relative artifact path"},
		// The two git sentinels that meant different ranges by context are
		// gone; the error names what replaced them.
		{"retired $CHANGED", agent("input: { changes: $CHANGED }"), "use $DIFF_TARGET (what the branch changes against its target) or $DIFF_PHASE"},
		{"retired $UNCOMMITTED", agent("input: { changes: $UNCOMMITTED }"), "use $STATUS"},
		{"retired on a gate", header + "stages:\n  - { id: g, type: human_gate, input: { c: $CHANGED } }\n", "$CHANGED is retired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse("t.yaml", []byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

// TestRetiredSpellings: every v1 spelling answers with the v2 upgrade rather
// than a bare "unknown field", so an old document tells you how to fix itself.
func TestRetiredSpellings(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"verdict", header + "stages:\n  - { id: a, type: agent, agent: x, verdict: true }\n",
			"declare a verdict-typed output on the agent"},
		{"ping", header + "stages:\n  - { id: g, type: human_gate, ping: plan_approval }\n",
			`say what you are asking in "question"`},
		{"payload", header + "stages:\n  - { id: g, type: human_gate, payload: artifacts/plan.md }\n",
			`renamed "input"`},
		{"body", header + "stages:\n  - { id: m, type: action, action: create_mr, body: artifacts/mr.md }\n",
			"with input: {body: <path>}"},
		{"feed", header + "stages:\n  - { id: a, type: agent, agent: x, on_reject: { to: a, max: 1, feed: decision } }\n",
			"bind $DECISION"},
		{"auto_complete", header + "stages:\n  - { id: g, type: human_gate, auto_complete: mr_merged }\n",
			`use "auto_approve"`},
		{"bare mr_url", header + "stages:\n  - { id: g, type: human_gate, input: { mr: mr_url } }\n",
			"use $MR_URL"},
		{"bare workspace", header + "stages:\n  - { id: g, type: human_gate, input: { w: workspace } }\n",
			"use $WORKSPACE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse("t.yaml", []byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

// TestVersionRequired: a document states its grammar, so an unfamiliar one is
// refused rather than half-understood.
func TestVersionRequired(t *testing.T) {
	stages := "stages:\n  - { id: s, type: command, run: x }\n"
	noVersion := "name: t\nparameters: { harness: fake, model: a/b }\n" + stages
	if _, err := Parse("t.yaml", []byte(noVersion)); err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Errorf("missing version: err = %v", err)
	}
	future := "name: t\nversion: 99\nparameters: { harness: fake, model: a/b }\n" + stages
	if _, err := Parse("t.yaml", []byte(future)); err == nil || !strings.Contains(err.Error(), "this build speaks version 4") {
		t.Errorf("future version: err = %v", err)
	}
}

// TestDocIOJSONForms pins the three document forms' JSON shapes. The map form
// must marshal in pair order, not sorted: reordering would churn the flow hash
// on every editor round-trip.
func TestDocIOJSONForms(t *testing.T) {
	d := DocIO{Pairs: []IOPair{{Name: "z", Value: "1"}, {Name: "a", Value: "2"}, {Name: "m", Value: "3"}}}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	// Pair order, not sorted: reordering would churn the flow hash on every
	// editor round-trip.
	if string(b) != `{"z":"1","a":"2","m":"3"}` {
		t.Errorf("json = %s, want pair order z,a,m", b)
	}
	var got DocIO
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if out, _ := json.Marshal(got); string(out) != string(b) {
		t.Errorf("round-trip %s -> %s", b, out)
	}
}

// TestParseDocMapRoundTrip parses a map-form flow to the editor Doc and back to
// JSON, asserting the binding object survives with its keys in order.
func TestParseDocMapRoundTrip(t *testing.T) {
	doc := header + commonAgents + `stages:
  - { id: build, type: agent, agent: builder, input: { plan: artifacts/plan.md, changes: $DIFF_TARGET }, on_success: done }
`
	d, err := ParseDoc("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"input":{"plan":"artifacts/plan.md","changes":"$DIFF_TARGET"}`) {
		t.Errorf("round-trip lost ordered map input: %s", b)
	}
}

func TestHashChangesWithContent(t *testing.T) {
	a, err := Parse("a.yaml", []byte(header+"stages:\n  - { id: s, type: command, run: \"true\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse("b.yaml", []byte(header+"stages:\n  - { id: s, type: command, run: \"false\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == b.Hash {
		t.Error("different content, same hash")
	}
}

func TestUnknownFieldRejectedWithLine(t *testing.T) {
	doc := header + `stages:
  - { id: s, type: command, run: "true" }
bogus_field: 1
`
	fe := parseErr(t, doc)
	if !strings.Contains(fe.Error(), "bogus_field") || !strings.Contains(fe.Error(), "line 6") {
		t.Errorf("unknown-field error not line-precise: %s", fe.Error())
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name   string
		doc    string
		substr string
		line   int
	}{
		{"missing name", "version: 2\nparameters: { harness: fake, model: a/b }\nstages:\n  - { id: s, type: command, run: x }\n",
			"flow name is required", 0},
		{"missing harness", "name: t\nversion: 4\nparameters: { model: a/b }\nstages:\n  - { id: s, type: command, run: x }\n",
			"harness is required", 3},
		{"missing model", "name: t\nversion: 4\nparameters: { harness: fake }\nstages:\n  - { id: s, type: command, run: x }\n",
			"model is required", 0},
		{"bad model format", "name: t\nversion: 4\nparameters: { harness: fake, model: nomodel }\nstages:\n  - { id: s, type: command, run: x }\n",
			"provider/model", 3},
		{"bad timeout", "name: t\nversion: 4\nparameters: { model: a/b, timeout: soon }\nstages:\n  - { id: s, type: command, run: x }\n",
			"invalid duration", 3},
		{"no stages", header, "at least one stage", 0},
		{"duplicate id", header + "stages:\n  - { id: s, type: command, run: x }\n  - { id: s, type: command, run: y }\n",
			"duplicate id", 6},
		{"non-slug id", header + "stages:\n  - { id: Bad_ID, type: command, run: x }\n",
			"not a slug", 5},
		{"missing type", header + "stages:\n  - { id: s, run: x }\n",
			"type is required", 5},
		{"unknown type", header + "stages:\n  - { id: s, type: robot }\n",
			`unknown type "robot"`, 5},
		{"model on command", header + "stages:\n  - { id: s, type: command, run: x, model: a/b }\n",
			`field "model" does not apply to type command`, 5},
		{"run and run_ref", header + "stages:\n  - { id: s, type: command, run: x, run_ref: y }\n",
			"exactly one of run / run_ref", 5},
		{"neither run nor run_ref", header + "stages:\n  - { id: s, type: command }\n",
			"exactly one of run / run_ref", 5},
		{"agent missing", header + "stages:\n  - { id: s, type: agent }\n",
			"agent is required", 5},
		{"bad input path", header + "stages:\n  - { id: s, type: command, run: x, input: { p: /etc/passwd } }\n",
			"must be a relative artifact path", 5},
		{"dotdot output", header + "stages:\n  - { id: s, type: command, run: x, output: { o: ../out.md } }\n",
			"relative artifact path", 5},
		{"unknown auto_approve", header + "stages:\n  - { id: s, type: human_gate, auto_approve: ci_green }\n",
			"unknown auto_approve signal", 5},
		{"action missing action", header + "stages:\n  - { id: s, type: action }\n",
			"action is required", 5},
		{"unknown action", header + "stages:\n  - { id: s, type: action, action: send_carrier_pigeon }\n",
			`unknown action "send_carrier_pigeon"`, 5},
		{"action missing required input", header + "stages:\n  - { id: s, type: action, action: create_mr }\n",
			`requires input "body"`, 5},
		{"action unknown input", header + "stages:\n  - { id: s, type: action, action: create_mr, input: { body: a.md, subject: b.md } }\n",
			`input "subject" is not an input of action`, 5},
		{"output on action", header + "stages:\n  - { id: s, type: action, action: create_mr, input: { body: a.md }, output: { x: x.md } }\n",
			`field "output" does not apply to type action`, 5},
		{"edge target missing", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: ghost, max: 2 } }\n",
			`on_fail target "ghost" does not exist`, 5},
		{"edge max missing", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s } }\n",
			"max is required", 5},
		{"edge max zero", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 0 } }\n",
			"max must be >= 1", 5},

		{"on_fail on human_gate", header + "stages:\n  - { id: g, type: human_gate, on_fail: { to: g, max: 1 } }\n",
			`field "on_fail" does not apply`, 5},
		{"two create_mr actions", header + "stages:\n  - { id: a, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n  - { id: b, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n",
			`at most one "create_mr" action`, 6},

		{"reserved done id", header + "stages:\n  - { id: done, type: command, run: x }\n",
			"reserved", 5},
		{"reserved abort id", header + "stages:\n  - { id: abort, type: command, run: x }\n",
			"reserved", 5},
		{"reserved start id", header + "stages:\n  - { id: start, type: command, run: x }\n",
			"reserved", 5},
		{"edge to done with max", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: done, max: 1 } }\n",
			"max does not apply", 5},
		{"abort edge with max", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: abort, max: 2 } }\n",
			"max does not apply", 5},

		{"abort edge with then", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: abort, then: { to: s, max: 1 } } }\n",
			"then does not apply", 5},
		{"no route ends the run", header + "stages:\n  - { id: s, type: command, run: x, on_success: abort }\n",
			"no route ends the run", 4},
		{"reserved debug id", header + "stages:\n  - { id: debug, type: command, run: x }\n",
			"reserved", 5},
		{"debug edge with max", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: debug, max: 2 } }\n",
			"max does not apply", 5},
		{"debug edge with then", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: debug, then: { to: s, max: 1 } } }\n",
			"then does not apply", 5},
		{"on_success debug", header + "stages:\n  - { id: s, type: command, run: x, on_success: debug }\n",
			`on_success cannot be "debug"`, 5},
		{"on_timeout on gate", header + "stages:\n  - { id: g, type: human_gate, on_timeout: { to: g, max: 1 } }\n",
			`field "on_timeout" does not apply`, 5},
		{"each_repo on an agent", header + "stages:\n  - { id: a, type: agent, agent: x, each_repo: true }\n",
			"each_repo", 5},
		{"then target missing", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 1, then: { to: ghost, max: 1 } } }\n",
			`on_fail target "ghost" does not exist`, 5},
		{"then max missing", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 1, then: { to: s } } }\n",
			"then #1", 5},
		{"feed on a then link", header + "stages:\n  - { id: a, type: agent, agent: x, on_reject: { to: a, max: 1, then: { to: a, max: 1, feed: output } } }\n",
			"is retired: bind $DECISION", 5},
		{"unknown edge field", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 1, bogus: 1 } }\n",
			"bogus", 0},
		{"unlimited without a gate on the link", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: -1 } }\n",
			"requires a human_gate", 5},
		{"max below -1", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: -2 } }\n",
			"max must be >= 1", 5},
		{"then after unlimited", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: g, max: -1, then: { to: s, max: 1 } } }\n  - { id: g, type: human_gate, on_success: done }\n",
			"unreachable", 5},
		// A wait stage cannot be rejected: there is no human to reject it.
		{"on_reject on a wait", header + "stages:\n  - { id: w, type: wait, for: mr_merged, on_reject: { to: w, max: 1 } }\n",
			`field "on_reject" does not apply`, 5},
		{"on_success bad target", header + "stages:\n  - { id: a, type: command, run: x, on_success: nope }\n",
			`on_success target "nope" does not exist`, 5},
		{"success cycle", header + "stages:\n  - { id: a, type: command, run: x, on_success: b }\n  - { id: b, type: command, run: x, on_success: a }\n",
			"success cycle", 5},
		{"self success cycle", header + "stages:\n  - { id: a, type: command, run: x, on_success: a }\n",
			"success cycle", 5},
		{"unreachable stage", header + "stages:\n  - { id: a, type: command, run: x, on_success: done }\n  - { id: b, type: command, run: x, on_success: done }\n",
			`stage "b" is unreachable`, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, tc.doc, tc.substr, tc.line)
		})
	}
}

// TestGateInputForms: a gate takes one input — an artifact to review or a
// sentinel the engine resolves to a link — and says what it is asking in free
// text. There is no gate kind: what it shows IS what kind it is.
func TestGateInputForms(t *testing.T) {
	cases := []struct{ name, value string }{
		{"artifact", "artifacts/plan.md"},
		{"workspace", SentinelWorkspace},
		{"mr url", SentinelMRURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := header + "stages:\n  - { id: g, type: human_gate, question: \"Ready?\", input: { shown: " + tc.value + " } }\n"
			f, err := Parse("t.yaml", []byte(doc))
			if err != nil {
				t.Fatal(err)
			}
			g := f.Stages[0]
			if len(g.Inputs) != 1 || g.Inputs[0] != (IOBinding{Name: "shown", Value: tc.value}) {
				t.Errorf("Inputs = %+v, want shown -> %q", g.Inputs, tc.value)
			}
			if g.Question != "Ready?" {
				t.Errorf("Question = %q", g.Question)
			}
		})
	}
	// Both are optional: a gate with neither still parses (it asks about the
	// run itself, and the engine falls back to naming the stage).
	f, err := Parse("t.yaml", []byte(header+"stages:\n  - { id: g, type: human_gate }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Stages[0].Inputs) != 0 || f.Stages[0].Question != "" {
		t.Errorf("bare gate = %+v", f.Stages[0])
	}
}

// TestWaitStage: blocking on an external signal, and the errors that keep a
// flow from waiting on something that cannot happen.
func TestWaitStage(t *testing.T) {
	f, err := Parse("t.yaml", []byte(header+`stages:
  - { id: mr,     type: action, action: create_mr, input: { body: artifacts/mr.md }, on_success: merged }
  - { id: merged, type: wait, for: mr_merged, timeout: 24h, on_success: done,
      on_timeout: { to: merged, max: 2 } }
`))
	if err != nil {
		t.Fatal(err)
	}
	w := f.Stage("merged")
	if w.Type != StageWait || w.WaitFor != "mr_merged" || w.Timeout != 24*time.Hour ||
		w.Wait == nil || w.Wait.Signal != "mr_merged" || w.Wait.Start != StartAuto || w.Wait.Timeout != 24*time.Hour || !w.Parks() {
		t.Errorf("wait stage = %+v wait %+v", w, w.Wait)
	}
	// A gate may be auto-answered by the same registry.
	g, err := Parse("t.yaml", []byte(header+`stages:
  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md }, on_success: gate }
  - { id: gate, type: human_gate, question: "Merge?", input: { mr: $MR_URL },
      auto_approve: mr_merged, on_success: done }
`))
	if err != nil {
		t.Fatal(err)
	}
	if g.Stage("gate").AutoApprove != "mr_merged" {
		t.Errorf("auto_approve = %q", g.Stage("gate").AutoApprove)
	}
	for _, tc := range []struct{ name, doc, want string }{
		{"missing for", header + "stages:\n  - { id: w, type: wait }\n", "for is required"},
		{"unknown signal", header + "stages:\n  - { id: w, type: wait, for: pigs_fly }\n", `unknown signal "pigs_fly"`},
		{"unknown auto_approve", header + "stages:\n  - { id: g, type: human_gate, auto_approve: pigs_fly }\n",
			"unknown auto_approve signal"},
		{"for on a gate", header + "stages:\n  - { id: g, type: human_gate, for: mr_merged }\n",
			`field "for" does not apply`},
		{"auto_approve on a wait", header + "stages:\n  - { id: w, type: wait, for: mr_merged, auto_approve: mr_merged }\n",
			`field "auto_approve" does not apply`},
		// A wait has no start policy: a person in the loop after a signal
		// is a gate with auto_approve.
		{"start on a wait", header + "stages:\n  - { id: w, type: wait, for: mr_merged, start: confirm }\n",
			"field start not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse("t.yaml", []byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

// TestDebugLeaf covers the debug leaf: the scalar on an edge, as a chain's
// escalation, no max/then, and no stage of its own — it is a reserved
// target the run comes back from, not a terminal.
func TestDebugLeaf(t *testing.T) {
	doc := header + commonAgents + `stages:
  - { id: build, type: agent, agent: b,
      on_fail: { to: build, max: 2, then: debug } }
  - { id: test,  type: command, run: t, on_fail: debug,
      on_timeout: { to: test, max: 1, then: debug } }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	be := f.Stage("build").Edge(EdgeFail)
	if be == nil || be.To != "build" || be.Max != 2 || be.Then == nil ||
		be.Then.To != ReservedDebug || be.Then.Max != 0 || be.Then.Then != nil {
		t.Fatalf("build on_fail chain = %+v / %+v, want retry twice then the debug leaf", be, be.Then)
	}
	if te := f.Stage("test").Edge(EdgeFail); te == nil || te.To != ReservedDebug || te.Max != 0 || te.Then != nil {
		t.Fatalf("test on_fail = %+v, want the bare debug leaf", te)
	}
	if to := f.Stage("test").Edge(EdgeTimeout); to == nil || to.Then == nil || to.Then.To != ReservedDebug {
		t.Fatalf("test on_timeout = %+v, want a chain ending at debug", to)
	}
	if len(f.Stages) != 2 || f.Stage(ReservedDebug) != nil {
		t.Fatalf("stages = %d; the debug leaf must not become a stage", len(f.Stages))
	}
	// Inside a phase: a reaction's routes stay in the phase, and the leaf
	// (like done/abort) is in every phase.
	phased := header + commonAgents + `phases:
  - id: deliver
    on: run_start
    stages:
      - { id: build, type: agent, agent: b, on_fail: debug }
      - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }
  - id: fixes
    on: { signal: mr_review_settled }
    stages:
      - { id: fix, type: agent, agent: b, on_fail: { to: fix, max: 1, then: debug } }
  - id: shipped
    on: mr_merged
    then: done
`
	if _, err := Parse("t.yaml", []byte(phased)); err != nil {
		t.Fatalf("debug inside phases: %v", err)
	}
}

// TestAbortAndThenChains covers the abort terminal (map + scalar sugar) and
// retry-then-escalate chains.
func TestAbortAndThenChains(t *testing.T) {
	doc := header + commonAgents + `stages:
  - { id: build, type: agent, agent: b }
  - { id: test,  type: command, run: t,
      on_fail: { to: test, max: 2, then: { to: build, max: 3 } },
      on_timeout: { to: test, max: 1, then: abort } }
  - { id: gate,  type: human_gate, on_reject: abort }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	test := f.Stage("test")
	fe := test.Edge(EdgeFail)
	if fe == nil || fe.To != "test" || fe.Max != 2 {
		t.Fatalf("on_fail link 0 = %+v", fe)
	}
	if fe.Then == nil || fe.Then.To != "build" || fe.Then.Max != 3 || fe.Then.Then != nil {
		t.Fatalf("on_fail link 1 = %+v", fe.Then)
	}
	te := test.Edge(EdgeTimeout)
	if te == nil || te.To != "test" || te.Max != 1 || te.Then == nil || te.Then.To != TerminalAbort {
		t.Fatalf("on_timeout chain = %+v", te)
	}
	ge := f.Stage("gate").Edge(EdgeReject)
	if ge == nil || ge.To != TerminalAbort || ge.Max != 0 || ge.Then != nil {
		t.Fatalf("gate on_reject = %+v", ge)
	}

	// Unlimited links (max -1) parse on human_gate targets.
	unl := header + `stages:
  - { id: work,   type: command, run: w, on_success: done,
      on_fail: { to: rescue, max: -1 } }
  - { id: rescue, type: human_gate, input: { workspace: $WORKSPACE },
      on_success: work, on_reject: abort }
`
	uf, err := Parse("t.yaml", []byte(unl))
	if err != nil {
		t.Fatal(err)
	}
	if e := uf.Stage("work").Edge(EdgeFail); e.Max != UnlimitedMax || e.Then != nil {
		t.Fatalf("unlimited link = %+v", e)
	}

	// Unlimited is also legal when the SOURCE is a human_gate (the link
	// only fires when a human rejects it), regardless of the target type.
	gsrc := header + "agents:\n  - { name: a, prompt: do a }\n" + `stages:
  - { id: work, type: agent, agent: a, on_success: gate }
  - { id: gate, type: human_gate, on_success: done,
      on_reject: { to: work, max: -1 } }
`
	gf, err := Parse("t.yaml", []byte(gsrc))
	if err != nil {
		t.Fatal(err)
	}
	if e := gf.Stage("gate").Edge(EdgeReject); e.Max != UnlimitedMax {
		t.Fatalf("gate-source unlimited link = %+v", e)
	}

	// The scalar sugar round-trips through ParseDoc for the editor.
	d, err := ParseDoc("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if d.Stages[2].OnReject == nil || d.Stages[2].OnReject.To != TerminalAbort || d.Stages[2].OnReject.Max != nil {
		t.Fatalf("doc on_reject = %+v", d.Stages[2].OnReject)
	}
}

// TestExplicitSuccessGraph covers DESIGN3 §2.1: sugar defaults, explicit
// overrides, and mid-flow termination.
func TestExplicitSuccessGraph(t *testing.T) {
	// Sugar: absent on_success chains document order; the last stage of the
	// shorthand's one phase ends the run (done) — nothing could follow it.
	f, err := Parse("t.yaml", []byte(header+"stages:\n  - { id: a, type: command, run: x }\n  - { id: b, type: command, run: x }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Stages[0].OnSuccess != "b" || f.Stages[1].OnSuccess != TerminalDone {
		t.Errorf("sugar chain = %q, %q", f.Stages[0].OnSuccess, f.Stages[1].OnSuccess)
	}

	// Explicit branching: a skips b on success; b is reached via a's
	// on_fail and terminates the run early.
	doc := header + `stages:
  - { id: a, type: command, run: x, on_success: c, on_fail: { to: b, max: 1 } }
  - { id: b, type: command, run: x, on_success: done }
  - { id: c, type: command, run: x }
`
	f, err = Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if f.Stages[0].OnSuccess != "c" || f.Stages[1].OnSuccess != TerminalDone || f.Stages[2].OnSuccess != TerminalDone {
		t.Errorf("targets = %q %q %q", f.Stages[0].OnSuccess, f.Stages[1].OnSuccess, f.Stages[2].OnSuccess)
	}
	if f.Stages[0].Edge(EdgeFail) == nil || f.Stages[0].Edge(EdgeFail).To != "b" {
		t.Errorf("edges = %+v", f.Stages[0].Edges)
	}
}

// TestYAMLHardening covers the structural rules a flow document must obey
// (FLOWLANG §6, PLAN5 M22). Each of these is legal YAML that would either
// break the editor's canonical round-trip or hide structure from a reader.
func TestYAMLHardening(t *testing.T) {
	stages := "stages:\n  - { id: s, type: command, run: x }\n"
	cases := []struct{ name, doc, substr string }{
		{
			name:   "anchors are refused",
			doc:    header + "agents:\n  - &base { name: a, prompt: p }\n" + stages,
			substr: "anchors",
		},
		{
			name:   "aliases are refused",
			doc:    header + commonAgents + "stages:\n  - &s { id: a, type: command, run: x, on_success: done }\n",
			substr: "anchors",
		},
		{
			name:   "a second document is refused",
			doc:    header + stages + "---\nname: other\nversion: 4\n",
			substr: "one document",
		},
		{
			name: "duplicate edge keys are refused",
			doc: header + "stages:\n  - { id: s, type: command, run: x, " +
				"on_fail: { to: s, max: 1, to: s } }\n",
			substr: `"to" set twice`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte(tc.doc))
			if err == nil {
				t.Fatalf("parsed cleanly, want an error mentioning %q", tc.substr)
			}
			if !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("error = %q, want mention of %q", err, tc.substr)
			}
		})
	}
}

// A then-chain deeper than the cap is refused rather than recursed into.
func TestThenChainDepthCapped(t *testing.T) {
	edge := "{ to: s, max: 1 }"
	for range maxChainDepth + 2 {
		edge = "{ to: s, max: 1, then: " + edge + " }"
	}
	_, err := Parse("t.yaml", []byte(header+"stages:\n  - { id: s, type: command, run: x, on_fail: "+edge+" }\n"))
	if err == nil || !strings.Contains(err.Error(), "then chain is deeper") {
		t.Errorf("err = %v, want a chain-depth refusal", err)
	}
}

// A phased flow (PLAN6): an entrypoint, two firing phases, stage ids one
// namespace, routes kept inside their phase.
const phasedDoc = header + `phases:
  - id: deliver
    on: run_start
    stages:
      - { id: build, type: command, run: x }
      - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }
  - id: conflicts
    on: { signal: mr_conflicts, start: confirm }
    stages:
      - { id: sync, type: action, action: sync_target, input: { strategy: merge }, on_success: sync_test }
      - { id: sync_test, type: command, run: x }
  - id: review_fixes
    on: { signal: mr_review_settled, quiet_for: 1h, max: 3 }
    stages:
      - { id: load, type: action, action: load_review_comments }
      - { id: fix, type: command, run: x, input: { comments: artifacts/review.md } }
  - id: shipped
    on: mr_merged
    then: done
`

func TestPhasedFlowParses(t *testing.T) {
	f, err := Parse("t.yaml", []byte(phasedDoc))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Phases) != 4 || len(f.Stages) != 6 {
		t.Fatalf("phases %d stages %d", len(f.Phases), len(f.Stages))
	}
	d, c, r, o := f.Phases[0], f.Phases[1], f.Phases[2], f.Phases[3]
	if !d.IsEntrypoint() || d.First != 0 || d.Last != 1 || c.First != 2 || c.Last != 3 || r.First != 4 || r.Last != 5 {
		t.Errorf("spans: %+v %+v %+v", d, c, r)
	}
	// A then-phase spans no stages: its start is the run's end.
	if !o.IsOutcome() || o.Then != TerminalDone || o.First != 6 || o.Last != 5 || o.On.Start != StartAuto || o.Fires() || o.HasSubject() || !o.Observed() {
		t.Errorf("outcome: %+v", o)
	}
	if c.On.Start != StartConfirm || c.On.Max != UnlimitedMax || r.On.Start != StartAuto || r.On.QuietFor != time.Hour || r.On.Max != 3 {
		t.Errorf("wait points: %+v %+v", c.On, r.On)
	}
	if !c.Fires() || !c.HasSubject() || d.Fires() || d.HasSubject() {
		t.Errorf("fires/subject: deliver %v/%v conflicts %v/%v", d.Fires(), d.HasSubject(), c.Fires(), c.HasSubject())
	}
	// Absent on_success: next stage in the phase; the phase's last stage ends
	// the PHASE (end), never the run.
	if f.Stage("build").OnSuccess != "mr" || f.Stage("mr").OnSuccess != TerminalEnd || f.Stage("sync_test").OnSuccess != TerminalEnd {
		t.Errorf("on_success: build→%s mr→%s sync_test→%s", f.Stage("build").OnSuccess, f.Stage("mr").OnSuccess, f.Stage("sync_test").OnSuccess)
	}
	if f.Stage("load").Phase != "review_fixes" || len(f.Stage("load").Outputs) != 1 || f.Stage("load").Outputs[0].Value != "artifacts/review.md" {
		t.Errorf("load stage = %+v", f.Stage("load"))
	}
	for _, w := range f.Warnings {
		if strings.Contains(w, "review.md") {
			t.Errorf("warning %q: review.md is produced by load", w)
		}
	}
	if f.Stage("build").Line != 8 || f.Stage("load").Line != 18 {
		t.Errorf("lines: build %d load %d", f.Stage("build").Line, f.Stage("load").Line)
	}
}

func TestPhaseValidationErrors(t *testing.T) {
	entry := header + "phases:\n  - id: deliver\n    on: run_start\n    stages:\n      - { id: build, type: command, run: x }\n      - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n" +
		"  - { id: shipped, on: mr_merged, then: done }\n"
	cases := []struct{ name, doc, substr string }{
		{"stages and phases", entry + "stages:\n  - { id: s, type: command, run: x }\n", "either top-level stages"},
		{"no entrypoint", header + "phases:\n  - { id: c, on: mr_conflicts, stages: [{ id: s, type: command, run: x }] }\n", "no phase begins a run"},
		{"missing on", header + "phases:\n  - { id: d, stages: [{ id: s, type: command, run: x }] }\n", "signal is required"},
		{"start on manual", entry + "  - { id: m, on: { signal: manual, start: confirm }, stages: [{ id: s, type: command, run: x }] }\n", "start does not apply"},
		{"start on run_start", header + "phases:\n  - { id: d, on: { signal: run_start, start: auto }, stages: [{ id: s, type: command, run: x }] }\n", "start does not apply"},
		{"bad start", entry + "  - { id: m, on: { signal: mr_conflicts, start: later }, stages: [{ id: s, type: command, run: x }] }\n", `start "later" must be`},
		{"then on a trigger", entry + "  - { id: m, on: mr_conflicts, then: done }\n", "then is for an outcome phase"},
		{"then and stages", entry + "  - { id: m, on: mr_closed, then: abort, stages: [{ id: s, type: command, run: x }] }\n", "stages or then, not both"},
		{"bad then", entry + "  - { id: m, on: mr_closed, then: finish }\n", `then "finish" must be`},
		{"max on an outcome", entry + "  - { id: m, on: { signal: mr_closed, max: 2 }, then: abort }\n", "max does not apply to an outcome"},
		{"quiet_for on an outcome", entry + "  - { id: m, on: { signal: mr_closed, quiet_for: 1h }, then: abort }\n", "quiet_for applies only"},
		{"end inside an outcome", entry + "  - { id: m, on: mr_closed, stages: [{ id: s, type: command, run: x, on_success: end }] }\n", `phase "m" is an outcome and ends the run`},
		{"end on an outcome's edge", entry + "  - { id: m, on: mr_closed, stages: [{ id: s, type: command, run: x, on_fail: end }] }\n", `on_fail: phase "m" is an outcome and ends the run`},
		{"no route ends the run", header + "phases:\n  - { id: d, on: run_start, stages: [{ id: s, type: command, run: x }] }\n", "no route ends the run"},
		{"unknown signal", entry + "  - { id: m, on: mr_ghost, stages: [{ id: s, type: command, run: x }] }\n", "unknown signal"},
		{"quiet on conflicts", entry + "  - { id: m, on: { signal: mr_conflicts, quiet_for: 1h }, stages: [{ id: s, type: command, run: x }] }\n", "quiet_for applies only"},
		{"quiet too short", entry + "  - { id: m, on: { signal: mr_review_settled, quiet_for: 10s }, stages: [{ id: s, type: command, run: x }] }\n", "at least 1m"},
		{"max zero", entry + "  - { id: m, on: { signal: mr_conflicts, max: 0 }, stages: [{ id: s, type: command, run: x }] }\n", "max must be >= 1"},
		{"confirm is not grammar", entry + "  - { id: m, on: { signal: mr_conflicts, confirm: true }, stages: [{ id: s, type: command, run: x }] }\n", "field confirm not found"},
		{"unknown trigger key", entry + "  - { id: m, on: { signal: mr_conflicts, when: now }, stages: [{ id: s, type: command, run: x }] }\n", "field when not found"},
		{"empty phase", entry + "  - { id: m, on: mr_conflicts, stages: [] }\n", "a phase needs stages"},
		{"duplicate phase", entry + "  - { id: deliver, on: mr_conflicts, stages: [{ id: s, type: command, run: x }] }\n", "duplicate phase id"},
		{"duplicate stage across phases", entry + "  - { id: m, on: mr_conflicts, stages: [{ id: build, type: command, run: x }] }\n", "one namespace"},
		{"route leaves phase", entry + "  - { id: m, on: mr_conflicts, stages: [{ id: s, type: command, run: x, on_success: build }] }\n", "stay inside it"},
		{"edge leaves phase", entry + "  - { id: m, on: mr_conflicts, stages: [{ id: s, type: command, run: x, on_fail: { to: build, max: 1 } }] }\n", "stay inside it"},
		{"two create_mr in one phase", header + "phases:\n  - { id: d, on: run_start, stages: [{ id: a, type: action, action: create_mr, input: { body: artifacts/mr.md } }, { id: b, type: action, action: create_mr, input: { body: artifacts/mr.md } }] }\n", "per phase"},
		{"rebase without a leased push", entry + "  - { id: m, on: mr_conflicts, stages: [{ id: s, type: action, action: sync_target, input: { strategy: rebase } }, { id: p, type: command, run: \"git push\" }] }\n", "push_branch"},
		{"bad strategy", entry + "  - { id: m, on: mr_conflicts, stages: [{ id: s, type: action, action: sync_target, input: { strategy: squash } }] }\n", "not one of merge | rebase"},
		{"trigger on a gate", header + "stages:\n  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n  - { id: g, type: human_gate, auto_approve: mr_conflicts }\n", "opens a phase"},
		{"trigger on a wait", header + "stages:\n  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n  - { id: w, type: wait, for: mr_review_settled }\n", "opens a phase"},
		{"run_start on a wait", header + "stages:\n  - { id: w, type: wait, for: run_start }\n", "opens a phase"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("err = %v, want %q", err, tc.substr)
			}
		})
	}
}

// The shorthand is one phase: top-level stages desugar to ShorthandPhase,
// opened by run_start, so the engine has one execution mode.
func TestShorthandDesugarsToMain(t *testing.T) {
	f, err := Parse("t.yaml", []byte(header+"stages:\n  - { id: s, type: command, run: x }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Phases) != 1 || f.Phases[0].ID != ShorthandPhase || !f.Phases[0].IsEntrypoint() || f.Stages[0].Phase != ShorthandPhase {
		t.Fatalf("phases = %+v, stage phase %q", f.Phases, f.Stages[0].Phase)
	}
	if b := f.Beginning(); b == nil || b.ID != ShorthandPhase || f.Stages[0].Line != 5 || f.Phases[0].On.Max != UnlimitedMax {
		t.Errorf("beginning %+v, line %d", b, f.Stages[0].Line)
	}
}

// Phase shape is advice, not a rule: a subject action outside a phase opened
// for a merge request, each_repo inside one, and a trigger that needs a merge
// request no stage opens all warn — and create_mr may live in any phase.
func TestPhaseShapeWarnings(t *testing.T) {
	warned := func(f *Flow, substr string) bool {
		for _, w := range f.Warnings {
			if strings.Contains(w, substr) {
				return true
			}
		}
		return false
	}
	doc := header + "phases:\n" +
		"  - { id: d, on: run_start, stages: [{ id: s, type: action, action: sync_target }, { id: p, type: action, action: push_branch }] }\n" +
		"  - { id: m, on: mr_conflicts, stages: [{ id: t, type: command, run: x, each_repo: true }, { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }] }\n" +
		"  - { id: shipped, on: mr_merged, then: done }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !warned(f, `"sync_target" acts on the merge request`) || !warned(f, "each_repo is ignored") || warned(f, "needs a merge request no stage opens") {
		t.Errorf("warnings = %v", f.Warnings)
	}
	noMR := header + "phases:\n  - { id: d, on: run_start, stages: [{ id: s, type: command, run: x }] }\n  - { id: m, on: mr_conflicts, stages: [{ id: t, type: command, run: x }] }\n  - { id: shipped, on: mr_merged, then: done }\n"
	if f, err := Parse("t.yaml", []byte(noMR)); err != nil || !warned(f, "needs a merge request no stage opens") {
		t.Errorf("err %v warnings %v", err, f.Warnings)
	}
}

// An edge may end the phase: `on_fail: done` is the best-effort stage whose
// failure is not worth routing anywhere.
// Two ends, said apart: `end` finishes the phase (the run goes back to
// waiting), `done` finishes the run. Either may close a success chain or an
// edge, in its scalar or mapping form, and neither takes max or then; both
// are reserved ids.
func TestEdgeToEndOrDone(t *testing.T) {
	f, err := Parse("t.yaml", []byte(header+"stages:\n"+
		"  - { id: a, type: command, run: x, on_fail: done }\n"+
		"  - { id: b, type: command, run: x, on_fail: { to: done } }\n"+
		"  - { id: c, type: command, run: x, on_fail: end }\n"+
		"  - { id: d, type: command, run: x, on_fail: { to: end } }\n"+
		"  - { id: e, type: command, run: x, on_success: done }\n"))
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"a": TerminalDone, "b": TerminalDone, "c": TerminalEnd, "d": TerminalEnd} {
		if e := f.Stage(id).Edges[EdgeFail]; e == nil || e.To != want || e.Max != 0 || e.Then != nil {
			t.Errorf("%s on_fail = %+v, want to %s", id, e, want)
		}
	}
	if f.Stage("d").OnSuccess != "e" || f.Stage("e").OnSuccess != TerminalDone {
		t.Errorf("on_success: d %q e %q", f.Stage("d").OnSuccess, f.Stage("e").OnSuccess)
	}
	g, err := Parse("t.yaml", []byte(header+"stages:\n  - { id: a, type: command, run: x, on_success: end, on_fail: done }\n"))
	if err != nil || g.Stage("a").OnSuccess != TerminalEnd {
		t.Errorf("on_success: end = %v, %v", g, err)
	}
	wantErr(t, header+"stages:\n  - { id: a, type: command, run: x, on_fail: { to: done, then: { to: a, max: 1 } } }\n", "then does not apply", 5)
	wantErr(t, header+"stages:\n  - { id: a, type: command, run: x, on_fail: { to: end, max: 2 } }\n", `max does not apply when to is "end" (the phase ends)`, 5)
	wantErr(t, header+"stages:\n  - { id: end, type: command, run: x }\n", `id "end" is reserved`, 5)
	wantErr(t, header+"stages:\n  - { id: a, type: command, run: x, on_success: finish }\n", `on_success target "finish" does not exist`, 5)
}

// The run's end written where it does not belong is warned about, never
// refused: a phase opened by a firing signal may run any number of times, so
// ending the whole run from it (done, abort) is almost always a slip for
// end; and a delivery that opens a merge request and says done without
// waiting for it leaves the reactions nothing to react to.
func TestEndingWarnings(t *testing.T) {
	deliver := func(tail string) string {
		return header + "phases:\n  - id: deliver\n    on: run_start\n    stages:\n" +
			"      - { id: build, type: command, run: x, output: { mr: artifacts/mr.md } }\n" +
			"      - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n" + tail
	}
	conflicts := func(push string) string {
		return "  - id: conflicts\n    on: mr_conflicts\n    stages:\n" +
			"      - { id: sync, type: action, action: sync_target }\n" + push
	}
	warnings := func(doc string) string {
		t.Helper()
		f, err := Parse("t.yaml", []byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(f.Warnings, "\n")
	}
	wait := "      - { id: wait_merge, type: wait, for: mr_merged, on_fail: abort }\n" +
		"      - { id: ticket_done, type: command, run: x, on_success: done }\n"

	// The default shape: deliver parks on the merge and says done after it;
	// the reaction ends with end. Nothing to warn about.
	if w := warnings(deliver(wait) + conflicts("      - { id: push, type: action, action: push_branch, on_success: end }\n")); w != "" {
		t.Errorf("clean flow warned: %s", w)
	}
	// A reaction that ends the run, on success or on failure.
	w := warnings(deliver(wait) + conflicts("      - { id: push, type: action, action: push_branch, on_success: done, on_fail: abort }\n"))
	if !strings.Contains(w, `stage "push": phase "conflicts" opens on mr_conflicts and may run any number of times, but this stage ends the whole run (done, abort)`) {
		t.Errorf("reaction ending the run: %s", w)
	}
	// A delivery that is done as soon as the merge request exists.
	w = warnings(deliver("      - { id: note, type: command, run: x, on_success: done }\n") + conflicts(""))
	if !strings.Contains(w, `phase "deliver" opens a merge request and ends the run (done) without waiting for it`) {
		t.Errorf("done without a wait: %s", w)
	}
	// A gate the merge answers is a wait too; and without reactions there is
	// nothing to miss.
	gate := "      - { id: final, type: human_gate, auto_approve: mr_merged, on_success: done, on_reject: abort }\n"
	if w := warnings(deliver(gate) + conflicts("")); strings.Contains(w, "without waiting") {
		t.Errorf("a merge-answered gate is a wait: %s", w)
	}
	if w := warnings(header + "stages:\n  - { id: b, type: command, run: x, output: { mr: artifacts/mr.md } }\n  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md }, on_success: done }\n"); strings.Contains(w, "without waiting") {
		t.Errorf("no reaction phase, nothing to miss: %s", w)
	}
}

// A phase runs as often as its signal brings a new fact: unbounded unless
// the flow caps it, whatever its start policy.
func TestPhaseRunsAreUnboundedByDefault(t *testing.T) {
	entry := header + "phases:\n  - id: deliver\n    on: run_start\n    stages:\n      - { id: build, type: command, run: x }\n      - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n" +
		"  - { id: shipped, on: mr_merged, then: done }\n"
	f, err := Parse("t.yaml", []byte(entry+
		"  - { id: a, on: mr_conflicts, stages: [{ id: s1, type: command, run: x }] }\n"+
		"  - { id: b, on: { signal: mr_review_settled, quiet_for: 1h, max: -1 }, stages: [{ id: s2, type: command, run: x }] }\n"+
		"  - { id: c, on: { signal: mr_review_settled, max: 3 }, stages: [{ id: s3, type: command, run: x }] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a, b, c := f.Phase("a"), f.Phase("b"), f.Phase("c"); a.On.Max != UnlimitedMax || b.On.Max != UnlimitedMax || b.On.Start != StartAuto || c.On.Max != 3 {
		t.Errorf("max: a %d b %d (start %s) c %d", a.On.Max, b.On.Max, b.On.Start, c.On.Max)
	}
}

// A command may bound its script.
func TestCommandTimeout(t *testing.T) {
	f, err := Parse("t.yaml", []byte(header+"stages:\n  - { id: s, type: command, run: x, timeout: 5m }\n"))
	if err != nil || f.Stages[0].Timeout != 5*time.Minute {
		t.Fatalf("err %v timeout %v", err, f.Stages[0].Timeout)
	}
	wantErr(t, header+"stages:\n  - { id: s, type: command, run: x, timeout: soon }\n", "invalid timeout", 5)
}

// Tracker actions: ticket_transition names a canonical status (`to`, an enum
// the tracker translates — readable from an enum parameter within it) OR
// the tracker's own word (`status`, a text input: only blankness is a parse
// error, and quoting is the author's choice), exactly one; ticket_set_field
// takes its value from an artifact OR the flow; ticket_link warns when no
// merge request precedes it.
func TestTrackerActionBindings(t *testing.T) {
	head := "name: tk\nversion: 4\nparameters: { harness: fake, model: fake/m }\nagents: []\nstages:\n"
	plain := head + "  - { id: s, type: action, action: ticket_transition, input: { status: In Progress } }\n"
	quoted := head + "  - { id: s, type: action, action: ticket_transition, input: { status: \"In Progress\" } }\n"
	fp, err := Parse("t.yaml", []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	fq, err := Parse("t.yaml", []byte(quoted))
	if err != nil {
		t.Fatal(err)
	}
	if in := fp.Stages[0].Input("status"); in == nil || in.Value != "In Progress" || in.Value != fq.Stages[0].Input("status").Value {
		t.Errorf("plain/quoted text bindings differ: %+v vs %+v", fp.Stages[0].Inputs, fq.Stages[0].Inputs)
	}
	canon, err := Parse("t.yaml", []byte(head+"  - { id: s, type: action, action: ticket_transition, input: { to: in_review } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if in := canon.Stages[0].Input("to"); in == nil || in.Value != "in_review" || canon.Stages[0].Input("status") != nil {
		t.Errorf("canonical binding = %+v", canon.Stages[0].Inputs)
	}
	// The canonical status may come from an enum parameter within the
	// statuses — a project decides where its issues go once merged.
	withParam := strings.Replace(head, "model: fake/m", "model: fake/m, final: { default: done, enum: [done, in_review] }", 1)
	viaParam, err := Parse("t.yaml", []byte(withParam+"  - { id: s, type: action, action: ticket_transition, input: { to: $final } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if in := viaParam.Stages[0].Input("to"); in == nil || in.Value != "done" {
		t.Errorf("to via $final = %+v", viaParam.Stages[0].Inputs)
	}
	if bound, err := viaParam.Resolve(map[string]string{"final": "in_review"}); err != nil || bound.Stages[0].Input("to").Value != "in_review" {
		t.Errorf("to via $final bound = %+v, %v", bound, err)
	}
	for _, tc := range []struct{ doc, substr string }{
		{head + "  - { id: s, type: action, action: ticket_transition, input: { to: \"In Progress\" } }\n", `input "to": "In Progress" is not one of todo | in_progress | in_review | done | blocked`},
		{head + "  - { id: s, type: action, action: ticket_transition, input: { status: \"  \" } }\n", `input "status": a value is required`},
		{head + "  - { id: s, type: action, action: ticket_transition }\n", `takes exactly one of to / status`},
		{head + "  - { id: s, type: action, action: ticket_transition, input: { to: done, status: Done } }\n", `takes exactly one of to / status`},
		{strings.Replace(head, "model: fake/m", "model: fake/m, wide: { default: done }", 1) +
			"  - { id: s, type: action, action: ticket_transition, input: { to: $wide } }\n", `parameter "wide" needs an enum within todo | in_progress`},
		{strings.Replace(head, "model: fake/m", "model: fake/m, wide: { default: done, enum: [done, closed] }", 1) +
			"  - { id: s, type: action, action: ticket_transition, input: { to: $wide } }\n", `parameter "wide" allows "closed", which this field does not`},
		{head + "  - { id: s, type: action, action: ticket_set_field, input: { field: Team } }\n", "exactly one of value"},
		{head + "  - { id: s, type: action, action: ticket_set_field, input: { field: Team, value: artifacts/t.txt, text: Platform } }\n", "exactly one of value"},
		{head + "  - { id: s, type: action, action: ticket_set_field, input: { field: Team, text: Platform, mode: merge } }\n", "not one of replace | append"},
		{head + "  - { id: s, type: action, action: ticket_set_field, input: { field: Team, value: /abs } }\n", "relative artifact path"},
	} {
		if _, err := Parse("t.yaml", []byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.substr) {
			t.Errorf("doc %q: err = %v, want %q", tc.doc[len(head):], err, tc.substr)
		}
	}
	ok := head + "  - { id: s, type: action, action: ticket_set_field, input: { field: \"Feature flags\", value: artifacts/flags.txt, mode: append } }\n"
	if _, err := Parse("t.yaml", []byte(ok)); err != nil {
		t.Errorf("valid set_field refused: %v", err)
	}

	// ticket_link's prerequisite: a warning in a stages-only flow with no
	// create_mr before it, none inside a reaction phase (the subject exists).
	alone := head + "  - { id: l, type: action, action: ticket_link }\n"
	f, err := Parse("t.yaml", []byte(alone))
	if err != nil {
		t.Fatal(err)
	}
	needsMR := func(f *Flow) int {
		n := 0
		for _, w := range f.Warnings {
			if strings.Contains(w, "needs a merge request") {
				n++
			}
		}
		return n
	}
	if needsMR(f) != 1 {
		t.Errorf("warnings = %v", f.Warnings)
	}
	after := head + "  - { id: m, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n  - { id: l, type: action, action: ticket_link }\n"
	if f, err := Parse("t.yaml", []byte(after)); err != nil || needsMR(f) != 0 {
		t.Errorf("link after create_mr: err %v warnings %v", err, f.Warnings)
	}
	phased := "name: tk\nversion: 4\nparameters: { harness: fake, model: fake/m }\nagents: []\nphases:\n" +
		"  - { id: deliver, on: run_start, stages: [ { id: m, type: action, action: create_mr, input: { body: artifacts/mr.md } } ] }\n" +
		"  - { id: react, on: mr_conflicts, stages: [ { id: l, type: action, action: ticket_link } ] }\n" +
		"  - { id: shipped, on: mr_merged, then: done }\n"
	if f, err := Parse("t.yaml", []byte(phased)); err != nil || needsMR(f) != 0 {
		t.Errorf("link in a reaction phase: err %v warnings %v", err, f.Warnings)
	}
}

// Outcomes (FLOWLANG §1): a phase on mr_merged or mr_closed ends the run. It
// takes a start policy like a trigger, no max or quiet_for, and fires once;
// with stages, its last stage's implicit success is done; with none, `then`
// says done or abort. on_success may say abort anywhere; the outcome signals
// stay waitable; and a flow needs some route to done.
func TestOutcomes(t *testing.T) {
	doc := header + "phases:\n" +
		"  - { id: d, on: run_start, stages: [{ id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }, { id: w, type: wait, for: mr_closed, on_fail: end }] }\n" +
		"  - id: merged\n    on: { signal: mr_merged, start: confirm }\n    stages:\n      - { id: tidy, type: command, run: x }\n      - { id: last, type: command, run: x, on_fail: abort }\n" +
		"  - { id: closed, on: mr_closed, then: abort }\n" +
		"  - { id: giveup, on: manual, stages: [{ id: g, type: command, run: x, on_success: abort, on_fail: done }] }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	merged, closed := f.Phase("merged"), f.Phase("closed")
	if !merged.IsOutcome() || !merged.Observed() || merged.Fires() || merged.HasSubject() || merged.On.Start != StartConfirm || merged.On.Max != UnlimitedMax || merged.Then != "" {
		t.Errorf("merged = %+v", merged)
	}
	if f.Stage("tidy").OnSuccess != "last" || f.Stage("last").OnSuccess != TerminalDone {
		t.Errorf("an outcome's chain: tidy→%s last→%s", f.Stage("tidy").OnSuccess, f.Stage("last").OnSuccess)
	}
	if !closed.IsOutcome() || !closed.HasSubject() || closed.Then != TerminalAbort || closed.On.Start != StartAuto || closed.First != 4 || closed.Last != 3 { // no stages of its own; g follows at 4
		t.Errorf("closed = %+v", closed)
	}
	if f.Stage("g").OnSuccess != TerminalAbort || f.Stage("w").WaitFor != "mr_closed" || !f.EndsRun() {
		t.Errorf("g→%s w waits on %s ends %v", f.Stage("g").OnSuccess, f.Stage("w").WaitFor, f.EndsRun())
	}
	for _, w := range f.Warnings {
		if strings.Contains(w, "ends the whole run") {
			t.Errorf("an outcome ending the run is not a slip: %s", w)
		}
	}
	// The done rule counts a then: done, an on_success: done and an edge to done.
	for _, tc := range []struct{ name, tail string }{
		{"then", "  - { id: o, on: mr_merged, then: done }\n"},
		{"on_success", "  - { id: o, on: manual, stages: [{ id: s, type: command, run: x, on_success: done }] }\n"},
		{"edge", "  - { id: o, on: manual, stages: [{ id: s, type: command, run: x, on_fail: done }] }\n"},
	} {
		base := header + "phases:\n  - { id: d, on: run_start, stages: [{ id: b, type: command, run: x }] }\n"
		if f, err := Parse("t.yaml", []byte(base+tc.tail)); err != nil || !f.EndsRun() {
			t.Errorf("%s: err %v", tc.name, err)
		}
	}
}

// Entrypoints are explicit: a run begins at a phase `on: run_start` (several
// allowed; the first is the default), a phase `on: manual` starts by hand
// from the run page only, and neither takes a start policy.
func TestEntrypoints(t *testing.T) {
	doc := header + "phases:\n" +
		"  - { id: polish, on: { signal: manual, max: 3 }, stages: [{ id: shine, type: command, run: x }] }\n" +
		"  - { id: deliver, on: run_start, stages: [{ id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }] }\n" +
		"  - { id: quick, on: run_start, stages: [{ id: mr2, type: action, action: create_mr, input: { body: artifacts/mr.md } }] }\n" +
		"  - { id: shipped, on: mr_merged, then: done }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Entrypoints(); len(got) != 2 || got[0].ID != "deliver" || got[1].ID != "quick" {
		t.Errorf("entrypoints = %+v", got)
	}
	if b := f.Beginning(); b == nil || b.ID != "deliver" {
		t.Errorf("beginning = %+v", b)
	}
	polish := f.Phase("polish")
	if polish.IsEntrypoint() || polish.Fires() || polish.HasSubject() || polish.On.Max != 3 || polish.On.Start != StartManual {
		t.Errorf("polish = %+v", polish.On)
	}
	deliver := f.Phase("deliver")
	if !deliver.IsEntrypoint() || deliver.Fires() || deliver.On.Max != UnlimitedMax || deliver.On.Start != StartManual {
		t.Errorf("deliver = %+v", deliver.On)
	}
}

// Parameters: the flow's one settings block. The engine-known six land in
// Defaults and Budget (typed, validated by the rules their old homes had,
// present even when omitted); the flow's own are typed by their defaults and
// read where $name appears.
func TestParametersDeclare(t *testing.T) {
	doc := `name: t
version: 4
parameters:
  harness: fake
  model: { default: a/b, description: "the default model" }
  max_cost_usd: 20
  verify: { default: verify, description: "the test command" }
  review_model: a/c
  plan_gate: true
  turns: 3
  rate: 1.5
  mode: { default: confirm, enum: [auto, confirm, manual] }
agents:
  - name: b
    prompt: do b
phases:
  - id: deliver
    on: run_start
    stages:
      - { id: build, type: agent, agent: b, model: $review_model, max_turns: $turns, when: $plan_gate }
      - { id: test, type: command, run_ref: $verify }
      - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }
  - id: conflicts
    on: { signal: mr_conflicts, start: $mode }
    stages:
      - { id: sync, type: action, action: sync_target }
  - id: shipped
    on: mr_merged
    then: done
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if f.Defaults.Harness != "fake" || f.Defaults.Model != "a/b" || f.Defaults.Timeout != 30*time.Minute || f.Defaults.MaxTurns != 50 ||
		f.Budget.MaxCostUSD != 20 || f.Budget.MaxTotalTurns != 300 {
		t.Errorf("defaults %+v budget %+v", f.Defaults, f.Budget)
	}
	var names []string
	for _, pr := range f.Params {
		names = append(names, pr.Name)
	}
	want := "harness model max_cost_usd verify review_model plan_gate turns rate mode timeout max_turns max_total_turns"
	if strings.Join(names, " ") != want {
		t.Errorf("params = %v", names)
	}
	types := map[string]ParamType{"verify": ParamString, "plan_gate": ParamBool, "turns": ParamInt, "rate": ParamFloat, "mode": ParamString, "max_cost_usd": ParamFloat, "timeout": ParamString}
	for name, typ := range types {
		if pr := f.Param(name); pr == nil || pr.Type != typ {
			t.Errorf("%s = %+v, want %s", name, pr, typ)
		}
	}
	if pr := f.Param("timeout"); !pr.Known || pr.Default != "30m" {
		t.Errorf("timeout = %+v", pr)
	}
	if pr := f.Param("mode"); len(pr.Enum) != 3 || pr.Known || f.Param("model").Description != "the default model" {
		t.Errorf("mode = %+v", pr)
	}
	build, test := f.Stage("build"), f.Stage("test")
	if build.Model != "a/c" || build.MaxTurns != 3 || build.When != nil || test.RunRef != "verify" || f.Phase("conflicts").On.Start != StartConfirm {
		t.Errorf("resolved: build %+v test %+v conflicts %+v", build, test, f.Phase("conflicts").On)
	}
	refs := map[string]string{}
	for _, r := range f.Refs {
		refs[r.Param] = r.Phase + "/" + r.Stage + "/" + r.Field
	}
	if len(f.Refs) != 5 || refs["review_model"] != "deliver/build/model" || refs["plan_gate"] != "deliver/build/when" ||
		refs["verify"] != "deliver/test/run_ref" || refs["mode"] != "conflicts//start" || refs["turns"] != "deliver/build/max_turns" {
		t.Errorf("refs = %+v", f.Refs)
	}
	unused := false
	for _, w := range f.Warnings {
		if strings.Contains(w, `parameter "rate" is declared but nothing reads it`) {
			unused = true
		}
	}
	if !unused {
		t.Errorf("warnings = %v", f.Warnings)
	}

	stages := "stages:\n  - { id: s, type: command, run: x }\n"
	for _, tc := range []struct{ name, params, substr string }{
		{"harness missing", "{ model: a/b }", "harness is required"},
		{"bad name", "{ harness: fake, model: a/b, Bad: 1 }", "is not a parameter name"},
		{"no default", "{ harness: fake, model: a/b, x: { description: hi } }", "default is required"},
		{"enum on an int", "{ harness: fake, model: a/b, x: { default: 1, enum: [1, 2] } }", "enum applies to a string"},
		{"default not in enum", "{ harness: fake, model: a/b, x: { default: c, enum: [a, b] } }", "is not one of its enum"},
		{"known type", "{ harness: 1, model: a/b }", "harness takes a string default"},
		{"float as int", "{ harness: fake, model: a/b, max_turns: 1.5 }", "max_turns takes a int default"},
		{"null default", "{ harness: fake, model: a/b, x: }", "needs a default value"},
		{"list default", "{ harness: fake, model: a/b, x: [1] }", "must be a default value or a mapping"},
		{"unknown long-form key", "{ harness: fake, model: a/b, x: { default: 1, type: int } }", "field type not found"},
		{"bad timeout", "{ harness: fake, model: a/b, timeout: soon }", "timeout: invalid duration"},
		{"max_cost zero", "{ harness: fake, model: a/b, max_cost_usd: 0 }", "max_cost_usd must be > 0"},
		{"declared twice", "{ harness: fake, model: a/b, x: 1, x: 2 }", "declared twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte("name: t\nversion: 4\nparameters: "+tc.params+"\n"+stages))
			if err == nil || !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("err = %v, want %q", err, tc.substr)
			}
		})
	}
	// defaults: and budget: are not grammar.
	if _, err := Parse("t.yaml", []byte("name: t\nversion: 4\ndefaults: { harness: fake, model: a/b }\n"+stages)); err == nil || !strings.Contains(err.Error(), "field defaults not found") {
		t.Errorf("defaults: err = %v", err)
	}
}

// Where a $parameter may be read, and the checks at each site: declared, of
// the site's type, an enum within an enum site's values, and then the site's
// own rule on the value it yields.
func TestParamSites(t *testing.T) {
	head := "name: t\nversion: 4\nparameters: { harness: fake, model: a/b, s: text, d: 5m, n: 2, b: true, e: { default: auto, enum: [auto, confirm] }, big: { default: auto, enum: [auto, confirm, manual] } }\n" + commonAgents
	for _, tc := range []struct{ name, doc, substr string }{
		{"sentinel at a site", head + "stages:\n  - { id: a, type: agent, agent: b, model: $DIFF_TARGET }\n", "is a sentinel, not a parameter"},
		{"undeclared", head + "stages:\n  - { id: a, type: agent, agent: b, model: $nope }\n", `parameter "nope" is not declared`},
		{"wrong type", head + "stages:\n  - { id: a, type: agent, agent: b, max_turns: $s }\n", `parameter "s" is a string, and this field takes a int`},
		{"bool at a string site", head + "stages:\n  - { id: a, type: agent, agent: b, harness: $b }\n", "is a bool"},
		{"param in a path", head + "stages:\n  - { id: a, type: agent, agent: b, input: { plan: $s } }\n", "must be a relative artifact path"},
		{"enum site needs an enum", head + "phases:\n  - { id: d, on: run_start, stages: [{ id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }] }\n  - { id: c, on: { signal: mr_conflicts, start: $s }, stages: [{ id: x, type: command, run: x }] }\n", "needs an enum within"},
		{"enum wider than the site", head + "stages:\n  - { id: s, type: action, action: sync_target, input: { strategy: $big } }\n", `allows "auto", which this field does not`},
		{"mixed case ref", head + "stages:\n  - { id: a, type: agent, agent: b, model: $Mixed }\n", "is not a parameter reference"},
		{"value fails the site rule", head + "stages:\n  - { id: a, type: agent, agent: b, model: $s }\n", `model: "text" (parameter "s") is not of the form provider/model`},
		{"duration via param", head + "stages:\n  - { id: a, type: agent, agent: b, timeout: $s }\n", `invalid timeout "text" (parameter "s")`},
		{"max_turns via param zero", "name: t\nversion: 4\nparameters: { harness: fake, model: a/b, z: 0 }\n" + commonAgents + "stages:\n  - { id: a, type: agent, agent: b, max_turns: $z }\n", `max_turns must be > 0, got 0 (parameter "z")`},
		{"on.max via string", head + "phases:\n  - { id: d, on: run_start, stages: [{ id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }] }\n  - { id: c, on: { signal: mr_conflicts, max: $s }, stages: [{ id: x, type: command, run: x }] }\n", "takes a int"},
		{"action enum via plain string", head + "stages:\n  - { id: s, type: action, action: sync_target, input: { strategy: $s } }\n", "needs an enum within"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("err = %v, want %q", err, tc.substr)
			}
		})
	}
	ok := head + "phases:\n  - id: d\n    on: run_start\n    stages:\n" +
		"      - { id: a, type: agent, agent: b, max_turns: $n, timeout: $d, harness: $s, model: a/c }\n" +
		"      - { id: c, type: command, run: \"echo $HOME\", run_ref: $s, timeout: $d }\n"
	ok = strings.Replace(ok, "run: \"echo $HOME\", run_ref: $s", "run: \"echo $HOME\"", 1)
	ok += "      - { id: w, type: wait, for: mr_merged }\n      - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }\n" +
		"  - id: cf\n    on: { signal: mr_conflicts, start: $big, max: $n, quiet_for: $d }\n    stages: [{ id: x, type: command, run: x }]\n" +
		"  - { id: shipped, on: mr_merged, then: done }\n"
	ok = strings.Replace(ok, "start: $big, max: $n, quiet_for: $d", "start: $big, max: $n", 1)
	f, err := Parse("t.yaml", []byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	a, c, w, cf := f.Stage("a"), f.Stage("c"), f.Stage("w"), f.Phase("cf")
	if a.MaxTurns != 2 || a.Timeout != 5*time.Minute || a.Harness != "text" || c.Timeout != 5*time.Minute || c.Run != "echo $HOME" ||
		w.Wait.Start != StartAuto || cf.On.Start != StartAuto || cf.On.Max != 2 {
		t.Errorf("a %+v c %+v w %+v cf %+v", a, c, w.Wait, cf.On)
	}
	unused := false
	for _, w := range f.Warnings {
		if strings.Contains(w, `parameter "e" is declared but nothing reads it`) {
			unused = true
		}
	}
	if !unused {
		t.Errorf("warnings = %v", f.Warnings)
	}
}

// Resolve re-parses the document with bound values in place of the defaults:
// every value-level rule runs on the binding, the hash is the document's, and
// the receiver is untouched.
func TestResolve(t *testing.T) {
	doc := `name: t
version: 4
parameters:
  harness: fake
  model: a/b
  max_cost_usd: 5
  verify: verify
  gate: true
  mode: { default: confirm, enum: [auto, confirm, manual] }
  turns: 2
` + commonAgents + `phases:
  - id: d
    on: run_start
    stages:
      - { id: a, type: agent, agent: b, max_turns: $turns, when: $gate }
      - { id: t, type: command, run_ref: $verify }
      - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }
  - id: c
    on: { signal: mr_conflicts, start: $mode, max: -1 }
    stages:
      - { id: s, type: action, action: sync_target }
  - id: shipped
    on: mr_merged
    then: done
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	b := map[string]string{"harness": "codex", "max_cost_usd": "9", "verify": "check", "gate": "false", "mode": "manual", "turns": "7"}
	r, err := f.Resolve(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Defaults.Harness != "codex" || r.Budget.MaxCostUSD != 9 || r.Stage("t").RunRef != "check" || r.Stage("a").MaxTurns != 7 ||
		r.Stage("a").When == nil || r.Stage("a").When.Term != WhenNever || r.Phase("c").On.Start != StartManual || r.Hash != f.Hash || r.Bindings["mode"] != "manual" {
		t.Errorf("resolved: defaults %+v budget %+v t %+v a %+v c %+v hash %v", r.Defaults, r.Budget, r.Stage("t"), r.Stage("a"), r.Phase("c").On, r.Hash == f.Hash)
	}
	if f.Stage("t").RunRef != "verify" || f.Bindings != nil || f.Defaults.Harness != "fake" {
		t.Errorf("receiver changed: %+v", f.Stage("t"))
	}
	if same, err := f.Resolve(nil); err != nil || same != f {
		t.Errorf("nothing bound should return the receiver: %v %v", same == f, err)
	}
	for _, tc := range []struct {
		b      map[string]string
		substr string
	}{
		{map[string]string{"nope": "1"}, `parameter "nope" is not declared`},
		{map[string]string{"turns": "x"}, `parameter "turns" takes an integer`},
		{map[string]string{"gate": "maybe"}, `parameter "gate" takes true or false`},
		{map[string]string{"mode": "later"}, `parameter "mode" takes one of auto | confirm | manual`},
		{map[string]string{"model": "nomodel"}, `model: "nomodel" (bound) is not of the form provider/model`},
		{map[string]string{"max_cost_usd": "-1"}, "max_cost_usd must be > 0"},
		{map[string]string{"turns": "0"}, `max_turns must be > 0, got 0 (parameter "turns")`},
	} {
		if _, err := f.Resolve(tc.b); err == nil || !strings.Contains(err.Error(), tc.substr) {
			t.Errorf("bindings %v: err = %v, want %q", tc.b, err, tc.substr)
		}
		if err := ValidateBindings(f.Params, tc.b); err == nil && !strings.Contains(tc.substr, "must be > 0") && !strings.Contains(tc.substr, "provider/model") {
			t.Errorf("ValidateBindings(%v) = nil, want an error", tc.b)
		}
	}
}

// when: a run fact or a bool parameter (which folds), optionally negated as
// `not x`; a conditional producer is no longer a definite one.
func TestWhen(t *testing.T) {
	head := "name: t\nversion: 4\nparameters: { harness: fake, model: a/b, yes: true, no: false, s: x }\n"
	f, err := Parse("t.yaml", []byte(head+"stages:\n"+
		"  - { id: a, type: command, run: x, when: tracker }\n"+
		"  - { id: b, type: command, run: x, when: not subject }\n"+
		"  - { id: c, type: command, run: x, when: $yes }\n"+
		"  - { id: d, type: command, run: x, when: $no }\n"+
		"  - { id: e, type: command, run: x, when: not $no }\n"+
		"  - { id: g, type: command, run: x, when: not $yes }\n"))
	if err != nil {
		t.Fatal(err)
	}
	a, b := f.Stage("a").When, f.Stage("b").When
	if a == nil || a.Term != WhenTracker || a.Not || !a.Holds(true, false) || a.Holds(false, true) || a.String() != "tracker" {
		t.Errorf("a = %+v", a)
	}
	if b == nil || b.Term != WhenSubject || !b.Not || b.Holds(false, true) || !b.Holds(true, false) || b.String() != "not subject" {
		t.Errorf("b = %+v", b)
	}
	if f.Stage("c").When != nil || f.Stage("e").When != nil {
		t.Errorf("true folds to no condition: c %+v e %+v", f.Stage("c").When, f.Stage("e").When)
	}
	for _, id := range []string{"d", "g"} {
		if w := f.Stage(id).When; w == nil || w.Term != WhenNever || w.Holds(true, true) {
			t.Errorf("%s = %+v, want never", id, w)
		}
	}
	for _, tc := range []struct{ doc, substr string }{
		{head + "stages:\n  - { id: a, type: command, run: x, when: maybe }\n", "is not a condition"},
		{head + "stages:\n  - { id: a, type: command, run: x, when: $s }\n", "is a string, and this field takes a bool"},
	} {
		if _, err := Parse("t.yaml", []byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.substr) {
			t.Errorf("err = %v, want %q", err, tc.substr)
		}
	}
	cond := header + commonAgents + "stages:\n  - { id: p, type: agent, agent: builder, when: tracker }\n  - { id: c, type: command, run: x, input: { patch: artifacts/patch.md } }\n"
	f, err = Parse("t.yaml", []byte(cond))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range f.Warnings {
		if strings.Contains(w, "not produced on every path") {
			found = true
		}
	}
	if !found {
		t.Errorf("a conditional producer should not count: %v", f.Warnings)
	}
}

// The editor's document shape: parameters as an ordered object of long
// forms with typed defaults, int fields as a number or a "$name" string.
func TestDocJSONShapes(t *testing.T) {
	doc := "name: t\nversion: 4\nparameters:\n  harness: fake\n  model: a/b\n  turns: 3\n  cost: 1.5\n  gate: true\n  mode: { default: a, enum: [a, b], description: pick }\n" + commonAgents +
		"phases:\n  - id: d\n    on: run_start\n    stages:\n      - { id: s, type: agent, agent: b, max_turns: $turns, when: $gate }\n" +
		"  - id: c\n    on: { signal: mr_conflicts, max: $turns }\n    stages: [{ id: x, type: command, run: x }]\n" +
		"  - { id: o, on: mr_merged, then: done }\n"
	d, err := ParseDoc("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"parameters":{"harness":{"default":"fake"},"model":{"default":"a/b"},"turns":{"default":3},"cost":{"default":1.5},"gate":{"default":true},"mode":{"default":"a","description":"pick","enum":["a","b"]}}`,
		`"max_turns":"$turns"`, `"when":"$gate"`, `"on":{"signal":"run_start"}`, `"on":{"signal":"mr_conflicts","max":"$turns"}`,
		`{"id":"o","on":{"signal":"mr_merged"},"then":"done"}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json missing %s:\n%s", want, b)
		}
	}
	var back Doc
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(&back)
	if err != nil || string(again) != string(b) {
		t.Errorf("round trip differs: %v\n%s\n%s", err, b, again)
	}
	// Short forms read back too.
	var short Doc
	if err := json.Unmarshal([]byte(`{"name":"t","version":4,"parameters":{"harness":"fake","n":2}}`), &short); err != nil || len(short.Parameters.Entries) != 2 || short.Parameters.Entries[1].Param.Default.Kind != ParamInt {
		t.Errorf("short form: %v %+v", err, short.Parameters.Entries)
	}
}
