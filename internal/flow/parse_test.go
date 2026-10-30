package flow

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports/trackerport"
)

// header is a minimal valid preamble; tests append stages to it.
const header = `name: t
version: 5
parameters: { harness: fake, model: fake/model }
`

// commonAgents is the agents block the stage-shape tests splice in: since v3
// a document declares the agents its stages name (FLOWLANG §1), and most
// parse tests are about stage syntax rather than contracts — so the
// contracts here are permissive on purpose.
const commonAgents = `agents:
  - name: builder
    prompt: build the thing
    inputs:
      - { name: plan, type: file, optional: true }
      - { name: changes, type: changeset, optional: true }
      - { name: rejection, type: verdict, optional: true }
      - { name: failure, type: failure, optional: true }
    outputs:
      - { name: patch, type: file }
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

// TestCanonicalV5 locks the fixture that exercises every stage construct: all
// stage types, their ports, every engine source, action settings.
func TestCanonicalV5(t *testing.T) {
	f, err := Load("testdata/v5.yaml")
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
		f.Defaults.Harness != "fake" || f.Defaults.Model != "fake/model" {
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
	if plan.Model != "fake/model" || plan.MaxTurns != 25 || plan.Timeout != 30*time.Minute {
		t.Errorf("plan defaults not resolved: %+v", plan)
	}
	// The agent says what it writes; the stage says where.
	if len(plan.Outputs) != 1 || plan.Outputs[0] != (Port{Name: "plan", Value: "artifacts/plan.md", Type: "file"}) ||
		plan.Description != "write the implementation plan" {
		t.Errorf("plan = %+v", plan)
	}
	if in := plan.Input("ticket"); in == nil || *in != (Port{Name: "ticket", Value: SourceTicket, Type: "ticket"}) {
		t.Errorf("plan.ticket = %+v", in)
	}

	// An agent stage states its ports by the agent's names, each typed.
	cm := f.Stage("commit_msg")
	if len(cm.Inputs) != 1 || cm.Inputs[0] != (Port{Name: "plan", Value: "artifacts/plan.md", Type: "file"}) {
		t.Errorf("commit_msg.Inputs = %+v", cm.Inputs)
	}
	if len(cm.Outputs) != 1 || cm.Outputs[0] != (Port{Name: "commit", Value: "artifacts/commit-subject.md", Type: "file"}) {
		t.Errorf("commit_msg.Outputs = %+v", cm.Outputs)
	}

	// A command names its files too — the label documents each one's role.
	commit := f.Stage("commit")
	if len(commit.Inputs) != 1 || commit.Inputs[0] != (Port{Name: "message", Value: "artifacts/commit-subject.md", Type: "file"}) {
		t.Errorf("commit.Inputs = %+v", commit.Inputs)
	}

	review := f.Stage("review")
	if review.Model != "fake/other" {
		t.Errorf("review model override = %q", review.Model)
	}
	// on_reject needs no stage flag: the agent's verdict output makes it fire,
	// and it declares nothing about what it carries.
	if review.Edge(EdgeReject) == nil || review.Edge(EdgeReject).To != "build" || review.Edge(EdgeReject).Max != 3 {
		t.Errorf("review.on_reject = %+v", review.Edge(EdgeReject))
	}
	if len(review.Inputs) != 3 || *review.Input("changes") != (Port{Name: "changes", Value: SourceDiffTarget, Type: "changeset"}) {
		t.Errorf("review.Inputs = %+v", review.Inputs)
	}
	if p := TypedPath(review, "verdict"); p != "artifacts/review/verdict.json" {
		t.Errorf("review verdict path = %q", p)
	}

	test := f.Stage("test")
	if test.RunRef != "verify" || test.Edge(EdgeFail) == nil || test.Edge(EdgeFail).To != "build" ||
		test.Edge(EdgeFail).Max != 5 {
		t.Errorf("test stage = %+v", test)
	}
	// The build stage reads both edge-carried values, so a rejection and a
	// failure land in different, named places.
	build := f.Stage("build")
	if in := build.Input("rejection"); in == nil || *in != (Port{Name: "rejection", Value: SourceVerdict, Type: "verdict", Optional: true}) {
		t.Errorf("build.rejection = %+v", in)
	}
	if in := build.Input("failure"); in == nil || in.Value != SourceFailure || !in.Optional {
		t.Errorf("build.failure = %+v", in)
	}

	mr := f.Stage("mr")
	if mr.Type != StageAction || mr.Action != "create_mr" ||
		len(mr.Inputs) != 1 || mr.Inputs[0] != (Port{Name: "body", Value: "artifacts/mr.md", Type: "file"}) || len(mr.Outputs) != 0 {
		t.Errorf("mr = %+v", mr)
	}

	gate := f.Stage("final_gate")
	if len(gate.Inputs) != 1 || gate.Inputs[0] != (Port{Name: "mr", Value: SourceMRURL, Type: "link"}) ||
		gate.AutoApprove != "mr_merged" || gate.Question == "" {
		t.Errorf("final_gate = %+v", gate)
	}
	if dbg := f.Stage("debug_push"); len(dbg.Inputs) != 1 || dbg.Inputs[0].Value != SourceWorkspace {
		t.Errorf("debug_push.Inputs = %+v, want %s", dbg.Inputs, SourceWorkspace)
	}

	if f.Index("in_progress") != 0 || f.Index("ticket_done") != len(ids)-1 || f.Index("nope") != -1 {
		t.Error("Index misbehaves")
	}
	// The tracker stages: a canonical status, the tracker's own word, and a
	// canonical one read from an enum parameter — settings, not ports.
	if st := f.Stage("in_progress"); st.Setting("to", "") != "in_progress" || len(st.Inputs) != 0 {
		t.Errorf("in_progress = %+v", st.Settings)
	}
	if v := f.Stage("in_review").Setting("status", ""); v != "In Review" {
		t.Errorf("in_review.status = %q", v)
	}
	if v := f.Stage("ticket_done").Setting("to", ""); v != "done" {
		t.Errorf("ticket_done.to = %q", v)
	}
	for _, w := range f.Warnings {
		t.Errorf("warning: %s", w)
	}
	if p := f.Param("ticket_status"); p == nil || p.Known || strings.Join(p.Enum, ",") != "done,in_review" {
		t.Errorf("ticket_status = %+v", p)
	}
}

// TestReferenceFlowParses locks the repo's shipped expanded-form example (what
// the graph editor emits): abort terminals, on_timeout, then-chains, unlimited
// links and the debug leaf as every stage's rescue.
func TestReferenceFlowParses(t *testing.T) {
	f, err := Load("../../examples/flows/v5.yaml")
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

func TestStagePorts(t *testing.T) {
	doc := header + commonAgents + `main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder,
      input: { plan: { from: repo:docs/plan.md, type: file }, changes: { from: run.git.diff_target, type: changeset } },
      output: { patch: { to: artifacts/patch.md, type: file } }, on_success: done }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	s := f.Stage("build")
	if len(s.Inputs) != 2 ||
		s.Inputs[0] != (Port{Name: "plan", Value: "repo:docs/plan.md", Type: "file"}) ||
		s.Inputs[1] != (Port{Name: "changes", Value: SourceDiffTarget, Type: "changeset"}) {
		t.Errorf("inputs = %+v", s.Inputs)
	}
	if FilePath(s.Inputs[0].Value) != "docs/plan.md" || FilePath(s.Inputs[1].Value) != "" {
		t.Errorf("file paths = %q, %q", FilePath(s.Inputs[0].Value), FilePath(s.Inputs[1].Value))
	}
	if len(s.Outputs) != 1 || s.Outputs[0] != (Port{Name: "patch", Value: "artifacts/patch.md", Type: "file"}) {
		t.Errorf("outputs = %+v", s.Outputs)
	}
}

// TestCommandPorts: a command states its ports like everything else. It has
// no contract to check the names against, so they are free labels — which is
// the point: they say what each file is for.
func TestCommandPorts(t *testing.T) {
	doc := header + `main:
  - { id: plan, type: command, run: "make plan", output: { plan: { to: artifacts/plan.md, type: file } } }
  - { id: verify, type: command, run: "make check",
      input: { plan: { from: artifacts/plan.md, type: file }, spec: { from: repo:docs/spec.md, type: file } },
      output: { report: { to: artifacts/report.txt, type: file } }, on_success: done }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	s := f.Stage("verify")
	want := []Port{{Name: "plan", Value: "artifacts/plan.md", Type: "file"}, {Name: "spec", Value: "repo:docs/spec.md", Type: "file"}}
	if len(s.Inputs) != len(want) {
		t.Fatalf("inputs = %+v", s.Inputs)
	}
	for i, w := range want {
		if s.Inputs[i] != w {
			t.Errorf("input[%d] = %+v, want %+v", i, s.Inputs[i], w)
		}
	}
	if len(s.Outputs) != 1 || s.Outputs[0] != (Port{Name: "report", Value: "artifacts/report.txt", Type: "file"}) {
		t.Errorf("outputs = %+v", s.Outputs)
	}
}

func TestPortShapeErrors(t *testing.T) {
	agent := func(ports string) string {
		return header + "main:\n  - { id: a, type: agent, harness: $harness, model: $model, agent: x, " + ports + ", on_success: done }\n"
	}
	cases := []struct{ name, doc, want string }{
		{"dup key", agent("input: { plan: { from: repo:a.md, type: file }, plan: { from: repo:b.md, type: file } }"), `port "plan" stated twice`},
		{"ticket value", agent("input: { plan: ticket }"), "(ticket is now run.ticket)"},
		{"bad input path", agent("input: { plan: { from: /abs, type: file } }"), "must be a path some stage writes, repo:<path>"},
		{"non-scalar value", agent("input: { plan: { from: [a, b], type: file } }"), "from must be a single value"},
		{"bad output path", agent("output: { p: { to: /abs, type: file } }"), "must be a repo-relative path"},
		{"bad port name", agent("input: { Bad Name: { from: repo:a.md, type: file } }"), "lowercase slug"},
		// One form everywhere: a bare value or a list is a shape error whose
		// message says what to do instead.
		{"agent list input", agent("input: [artifacts/plan.md]"), "not a list"},
		{"agent scalar output", agent("output: artifacts/out.md"), "must be a mapping of ports"},
		{"command list input", header + "main:\n  - { id: c, type: command, run: x, input: [a.md] }\n",
			"not a list"},
		{"gate scalar input", header + "main:\n  - { id: g, type: human_gate, input: artifacts/a.md }\n",
			"must be a mapping of ports"},
		{"gate two links", header + "main:\n  - { id: g, type: human_gate, input: { a: { from: run.workspace, type: link }, b: { from: run.mr.url, type: link } } }\n",
			"opens at most one link; got 2"},
		{"gate bad sentinel", header + "main:\n  - { id: g, type: human_gate, input: { x: { from: $NOPE, type: link } } }\n",
			"$NOPE is not a source"},
		// Engine sources are scoped: what a gate shows is a file or a link,
		// what an action reads is a file.
		{"gate gets prompt content", header + "main:\n  - { id: g, type: human_gate, input: { t: { from: run.ticket, type: ticket } } }\n",
			"a gate shows a file or a link; run.ticket is a ticket"},
		{"action gets a link", header + "main:\n  - { id: m, type: action, action: create_mr, input: { body: { from: run.mr.url, type: link } } }\n",
			"reads files; run.mr.url is a value the engine provides"},
		// The two git sentinels that meant different ranges by context are
		// gone; the error names what replaced them.
		{"retired $CHANGED", agent("input: { changes: { from: $CHANGED, type: changeset } }"), "$CHANGED is now run.git.diff_target"},
		{"retired $UNCOMMITTED", agent("input: { changes: { from: $UNCOMMITTED, type: changeset } }"), "$UNCOMMITTED is now run.git.status"},
		{"retired on a gate", header + "main:\n  - { id: g, type: human_gate, input: { c: $CHANGED } }\n", "($CHANGED is now run.git.diff_target)"},
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
		{"verdict", header + "main:\n  - { id: a, type: agent, harness: $harness, model: $model, agent: x, verdict: true }\n",
			"declare a verdict-typed output on the agent"},
		{"ping", header + "main:\n  - { id: g, type: human_gate, ping: plan_approval }\n",
			`say what you are asking in "question"`},
		{"payload", header + "main:\n  - { id: g, type: human_gate, payload: artifacts/plan.md }\n",
			"a gate shows an input port"},
		{"body", header + "main:\n  - { id: m, type: action, action: create_mr, body: artifacts/mr.md }\n",
			"with input: { body: { from: <path>, type: file } }"},
		{"feed", header + "main:\n  - { id: a, type: agent, harness: $harness, model: $model, agent: x, on_reject: { to: a, max: 1, feed: decision } }\n",
			"read edge.verdict from an input port"},
		{"auto_complete", header + "main:\n  - { id: g, type: human_gate, auto_complete: mr_merged }\n",
			`use "auto_approve"`},
		{"bare mr_url", header + "main:\n  - { id: g, type: human_gate, input: { mr: mr_url } }\n",
			"(mr_url is now run.mr.url)"},
		{"bare workspace", header + "main:\n  - { id: g, type: human_gate, input: { w: workspace } }\n",
			"(workspace is now run.workspace)"},
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
	stages := "main:\n  - { id: s, type: command, run: x }\n"
	noVersion := "name: t\nparameters: { harness: fake, model: a/b }\n" + stages
	if _, err := Parse("t.yaml", []byte(noVersion)); err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Errorf("missing version: err = %v", err)
	}
	future := "name: t\nversion: 99\nparameters: { harness: fake, model: a/b }\n" + stages
	if _, err := Parse("t.yaml", []byte(future)); err == nil || !strings.Contains(err.Error(), "this build speaks version 5") {
		t.Errorf("future version: err = %v", err)
	}
}

// TestDocIOJSONForms pins an action's settings' JSON shape. It must marshal in
// pair order, not sorted: reordering would churn the flow hash on every editor
// round-trip.
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

// TestParseDocMapRoundTrip parses a flow to the editor Doc and back to JSON,
// asserting the port objects survive with their keys in order.
func TestParseDocMapRoundTrip(t *testing.T) {
	doc := header + commonAgents + `main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder, input: { plan: { from: repo:docs/plan.md, type: file }, changes: { from: run.git.diff_target, type: changeset } },
      output: { patch: { to: artifacts/patch.md, type: file } }, on_success: done }
`
	d, err := ParseDoc("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"input":{"plan":{"from":"repo:docs/plan.md","type":"file"},"changes":{"from":"run.git.diff_target","type":"changeset"}},"output":{"patch":{"to":"artifacts/patch.md","type":"file"}}`) {
		t.Errorf("round-trip lost ordered map input: %s", b)
	}
}

func TestHashChangesWithContent(t *testing.T) {
	a, err := Parse("a.yaml", []byte(header+"main:\n  - { id: s, type: command, run: \"true\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse("b.yaml", []byte(header+"main:\n  - { id: s, type: command, run: \"false\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == b.Hash {
		t.Error("different content, same hash")
	}
}

func TestUnknownFieldRejectedWithLine(t *testing.T) {
	doc := header + `main:
  - { id: s, type: command, run: "true" }
bogus_field: 1
`
	fe := parseErr(t, doc)
	if !strings.Contains(fe.Error(), "bogus_field") || fe.Line != 6 {
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
		{"missing name", "version: 2\nparameters: { harness: fake, model: a/b }\nmain:\n  - { id: s, type: command, run: x }\n",
			"flow name is required", 0},
		// An agent stage states its harness and its model (a literal or a
		// $parameter); the parameters block owes neither a value.
		{"missing harness", header + "main:\n  - { id: a, type: agent, agent: x, model: $model }\n",
			"harness is required", 5},
		{"missing model", header + "main:\n  - { id: a, type: agent, agent: x, harness: $harness }\n",
			"model is required", 5},
		{"bad model format", "name: t\nversion: 5\nparameters: { harness: fake, model: nomodel }\nmain:\n  - { id: s, type: command, run: x }\n",
			"provider/model", 3},
		{"bad timeout", "name: t\nversion: 5\nparameters: { model: a/b, timeout: soon }\nmain:\n  - { id: s, type: command, run: x }\n",
			"takes a duration", 3},
		{"no main", header, "main is required", 0},
		{"duplicate id", header + "main:\n  - { id: s, type: command, run: x }\n  - { id: s, type: command, run: y }\n",
			"duplicate id", 6},
		{"non-slug id", header + "main:\n  - { id: Bad_ID, type: command, run: x }\n",
			"not a slug", 5},
		{"missing type", header + "main:\n  - { id: s, run: x }\n",
			"type is required", 5},
		{"unknown type", header + "main:\n  - { id: s, type: robot }\n",
			`unknown type "robot"`, 5},
		{"model on command", header + "main:\n  - { id: s, type: command, run: x, model: a/b }\n",
			`field "model" does not apply to type command`, 5},
		{"run and run_ref", header + "main:\n  - { id: s, type: command, run: x, run_ref: y }\n",
			"exactly one of run / run_ref", 5},
		{"neither run nor run_ref", header + "main:\n  - { id: s, type: command }\n",
			"exactly one of run / run_ref", 5},
		{"agent missing", header + "main:\n  - { id: s, type: agent, harness: $harness, model: $model }\n",
			"agent is required", 5},
		{"bad input path", header + "main:\n  - { id: s, type: command, run: x, input: { p: { from: /etc/passwd, type: file } } }\n",
			"must be a path some stage writes", 5},
		{"dotdot output", header + "main:\n  - { id: s, type: command, run: x, output: { o: { to: ../out.md, type: file } } }\n",
			"must be a repo-relative path", 5},
		{"unknown auto_approve", header + "main:\n  - { id: s, type: human_gate, auto_approve: ci_green }\n",
			"unknown auto_approve signal", 5},
		{"action missing action", header + "main:\n  - { id: s, type: action }\n",
			"action is required", 5},
		{"unknown action", header + "main:\n  - { id: s, type: action, action: send_carrier_pigeon }\n",
			`unknown action "send_carrier_pigeon"`, 5},
		{"action missing required input", header + "main:\n  - { id: s, type: action, action: create_mr }\n",
			`requires input "body"`, 5},
		{"action unknown input", header + "main:\n  - { id: s, type: action, action: create_mr, input: { body: { from: repo:a.md, type: file }, subject: { from: repo:b.md, type: file } } }\n",
			`input "subject" is not an input of action`, 5},
		{"action unknown output", header + "main:\n  - { id: s, type: action, action: create_mr, input: { body: { from: repo:a.md, type: file } }, output: { x: { to: x.md, type: file } } }\n",
			`output "x" is not an output of action "create_mr"`, 5},
		{"output on a gate", header + "main:\n  - { id: s, type: human_gate, output: { x: { to: x.md, type: file } } }\n",
			`field "output" does not apply to type human_gate`, 5},
		{"edge target missing", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: ghost, max: 2 } }\n",
			`on_fail target "ghost" does not exist`, 5},
		{"edge max missing", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: s } }\n",
			"max is required", 5},
		{"edge max zero", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 0 } }\n",
			"max must be >= 1", 5},

		{"on_fail on human_gate", header + "main:\n  - { id: g, type: human_gate, on_fail: { to: g, max: 1 } }\n",
			`field "on_fail" does not apply`, 5},
		{"two create_mr actions", header + "main:\n  - { id: a, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n  - { id: b, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n",
			`at most one "create_mr" action`, 6},

		{"reserved done id", header + "main:\n  - { id: done, type: command, run: x }\n",
			"reserved", 5},
		{"reserved abort id", header + "main:\n  - { id: abort, type: command, run: x }\n",
			"reserved", 5},
		{"reserved start id", header + "main:\n  - { id: start, type: command, run: x }\n",
			"reserved", 5},
		{"edge to done with max", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: done, max: 1 } }\n",
			"max does not apply", 5},
		{"abort edge with max", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: abort, max: 2 } }\n",
			"max does not apply", 5},

		{"abort edge with then", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: abort, then: { to: s, max: 1 } } }\n",
			"then does not apply", 5},
		{"no route ends the run", header + "main:\n  - { id: s, type: command, run: x, on_success: abort }\n",
			"no route ends the run", 4},
		{"reserved debug id", header + "main:\n  - { id: debug, type: command, run: x }\n",
			"reserved", 5},
		{"debug edge with max", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: debug, max: 2 } }\n",
			"max does not apply", 5},
		{"debug edge with then", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: debug, then: { to: s, max: 1 } } }\n",
			"then does not apply", 5},
		{"on_success debug", header + "main:\n  - { id: s, type: command, run: x, on_success: debug }\n",
			`on_success cannot be "debug"`, 5},
		{"on_timeout on gate", header + "main:\n  - { id: g, type: human_gate, on_timeout: { to: g, max: 1 } }\n",
			`field "on_timeout" does not apply`, 5},
		{"each_repo on an agent", header + "main:\n  - { id: a, type: agent, harness: $harness, model: $model, agent: x, each_repo: true }\n",
			"each_repo", 5},
		{"then target missing", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 1, then: { to: ghost, max: 1 } } }\n",
			`on_fail target "ghost" does not exist`, 5},
		{"then max missing", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 1, then: { to: s } } }\n",
			"then #1", 5},
		{"feed on a then link", header + "main:\n  - { id: a, type: agent, harness: $harness, model: $model, agent: x, on_reject: { to: a, max: 1, then: { to: a, max: 1, feed: output } } }\n",
			"is retired: read edge.verdict from an input port", 5},
		{"unknown edge field", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 1, bogus: 1 } }\n",
			"bogus", 0},
		{"unlimited without a gate on the link", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: -1 } }\n",
			"requires a human_gate", 5},
		{"max below -1", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: -2 } }\n",
			"max must be >= 1", 5},
		{"then after unlimited", header + "main:\n  - { id: s, type: command, run: x, on_fail: { to: g, max: -1, then: { to: s, max: 1 } } }\n  - { id: g, type: human_gate, on_success: done }\n",
			"unreachable", 5},
		// A wait stage cannot be rejected: there is no human to reject it.
		{"on_reject on a wait", header + "main:\n  - { id: w, type: wait, for: mr_merged, on_reject: { to: w, max: 1 } }\n",
			`field "on_reject" does not apply`, 5},
		{"on_success bad target", header + "main:\n  - { id: a, type: command, run: x, on_success: nope }\n",
			`on_success target "nope" does not exist`, 5},
		{"success cycle", header + "main:\n  - { id: a, type: command, run: x, on_success: b }\n  - { id: b, type: command, run: x, on_success: a }\n",
			"success cycle", 5},
		{"self success cycle", header + "main:\n  - { id: a, type: command, run: x, on_success: a }\n",
			"success cycle", 5},
		{"unreachable stage", header + "main:\n  - { id: a, type: command, run: x, on_success: done }\n  - { id: b, type: command, run: x, on_success: done }\n",
			`stage "b" is unreachable`, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, tc.doc, tc.substr, tc.line)
		})
	}
}

// TestGateInputForms: a gate takes one input — a file to review or a link the
// engine provides — and says what it is asking in free text. There is no gate
// kind: what it shows IS what kind it is.
func TestGateInputForms(t *testing.T) {
	cases := []struct{ name, value, typ string }{
		{"file", "repo:docs/plan.md", "file"},
		{"workspace", SourceWorkspace, "link"},
		{"mr url", SourceMRURL, "link"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := header + "main:\n  - { id: g, type: human_gate, question: \"Ready?\", input: { shown: { from: " + tc.value + ", type: " + tc.typ + " } } }\n"
			f, err := Parse("t.yaml", []byte(doc))
			if err != nil {
				t.Fatal(err)
			}
			g := f.Stages[0]
			if len(g.Inputs) != 1 || g.Inputs[0] != (Port{Name: "shown", Value: tc.value, Type: tc.typ}) {
				t.Errorf("Inputs = %+v, want shown -> %q", g.Inputs, tc.value)
			}
			if g.Question != "Ready?" {
				t.Errorf("Question = %q", g.Question)
			}
		})
	}
	// Both are optional: a gate with neither still parses (it asks about the
	// run itself, and the engine falls back to naming the stage).
	f, err := Parse("t.yaml", []byte(header+"main:\n  - { id: g, type: human_gate }\n"))
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
	f, err := Parse("t.yaml", []byte(header+`main:
  - { id: mr,     type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } }, on_success: merged }
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
	g, err := Parse("t.yaml", []byte(header+`main:
  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } }, on_success: gate }
  - { id: gate, type: human_gate, question: "Merge?", input: { mr: { from: run.mr.url, type: link } },
      auto_approve: mr_merged, on_success: done }
`))
	if err != nil {
		t.Fatal(err)
	}
	if g.Stage("gate").AutoApprove != "mr_merged" {
		t.Errorf("auto_approve = %q", g.Stage("gate").AutoApprove)
	}
	for _, tc := range []struct{ name, doc, want string }{
		{"missing for", header + "main:\n  - { id: w, type: wait }\n", "for is required"},
		{"unknown signal", header + "main:\n  - { id: w, type: wait, for: pigs_fly }\n", `unknown signal "pigs_fly"`},
		{"unknown auto_approve", header + "main:\n  - { id: g, type: human_gate, auto_approve: pigs_fly }\n",
			"unknown auto_approve signal"},
		{"for on a gate", header + "main:\n  - { id: g, type: human_gate, for: mr_merged }\n",
			`field "for" does not apply`},
		{"auto_approve on a wait", header + "main:\n  - { id: w, type: wait, for: mr_merged, auto_approve: mr_merged }\n",
			`field "auto_approve" does not apply`},
		// A wait has no start policy: a person in the loop after a signal
		// is a gate with auto_approve.
		{"start on a wait", header + "main:\n  - { id: w, type: wait, for: mr_merged, start: confirm }\n",
			`unknown key "start"`},
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
	doc := header + commonAgents + `main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: b,
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
	// Inside a watcher: its routes stay in its chain, and the leaf (like
	// done/abort) is in every chain.
	phased := header + commonAgents + `main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: b, on_fail: debug }
  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }
watchers:
  - id: fixes
    on: { signal: mr_review_settled }
    stages:
      - { id: fix, type: agent, harness: $harness, model: $model, agent: b, on_fail: { to: fix, max: 1, then: debug } }
outcomes:
  - id: shipped
    on: mr_merged
    then: done
`
	if _, err := Parse("t.yaml", []byte(phased)); err != nil {
		t.Fatalf("debug inside chains: %v", err)
	}
}

// TestAbortAndThenChains covers the abort terminal (map + scalar sugar) and
// retry-then-escalate chains.
func TestAbortAndThenChains(t *testing.T) {
	doc := header + commonAgents + `main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: b }
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
	unl := header + `main:
  - { id: work,   type: command, run: w, on_success: done,
      on_fail: { to: rescue, max: -1 } }
  - { id: rescue, type: human_gate, input: { workspace: { from: run.workspace, type: link } },
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
	gsrc := header + "agents:\n  - { name: a, prompt: do a }\n" + `main:
  - { id: work, type: agent, harness: $harness, model: $model, agent: a, on_success: gate }
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
	if d.Main[2].OnReject == nil || d.Main[2].OnReject.To != TerminalAbort || d.Main[2].OnReject.Max != nil {
		t.Fatalf("doc on_reject = %+v", d.Main[2].OnReject)
	}
}

// TestExplicitSuccessGraph covers DESIGN3 §2.1: sugar defaults, explicit
// overrides, and mid-flow termination.
func TestExplicitSuccessGraph(t *testing.T) {
	// Sugar: absent on_success chains document order; the last stage of the
	// shorthand's one phase ends the run (done) — nothing could follow it.
	f, err := Parse("t.yaml", []byte(header+"main:\n  - { id: a, type: command, run: x }\n  - { id: b, type: command, run: x }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Stages[0].OnSuccess != "b" || f.Stages[1].OnSuccess != TerminalDone {
		t.Errorf("sugar chain = %q, %q", f.Stages[0].OnSuccess, f.Stages[1].OnSuccess)
	}

	// Explicit branching: a skips b on success; b is reached via a's
	// on_fail and terminates the run early.
	doc := header + `main:
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
	stages := "main:\n  - { id: s, type: command, run: x }\n"
	cases := []struct{ name, doc, substr string }{
		{
			name:   "anchors are refused",
			doc:    header + "agents:\n  - &base { name: a, prompt: p }\n" + stages,
			substr: "anchors",
		},
		{
			name:   "aliases are refused",
			doc:    header + commonAgents + "main:\n  - &s { id: a, type: command, run: x, on_success: done }\n",
			substr: "anchors",
		},
		{
			name:   "a second document is refused",
			doc:    header + stages + "---\nname: other\nversion: 5\n",
			substr: "one document",
		},
		{
			name: "duplicate edge keys are refused",
			doc: header + "main:\n  - { id: s, type: command, run: x, " +
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
	_, err := Parse("t.yaml", []byte(header+"main:\n  - { id: s, type: command, run: x, on_fail: "+edge+" }\n"))
	if err == nil || !strings.Contains(err.Error(), "then chain is deeper") {
		t.Errorf("err = %v, want a chain-depth refusal", err)
	}
}

// A flow with all three sections: the main chain, two watchers and an
// outcome — stage ids one namespace, routes kept inside their chain.
const phasedDoc = header + `main:
  - { id: build, type: command, run: x }
  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }
watchers:
  - id: conflicts
    on: { signal: mr_conflicts, start: confirm }
    stages:
      - { id: sync, type: action, action: sync_target, with: { strategy: merge }, on_success: sync_test }
      - { id: sync_test, type: command, run: x }
  - id: review_fixes
    on: { signal: mr_review_settled, quiet_for: 1h, max: 3 }
    stages:
      - { id: load, type: action, action: load_review_comments, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } } }
      - { id: fix, type: command, run: x, input: { comments: { from: artifacts/threads.yaml, type: file } } }
outcomes:
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
	if d.ID != MainPhase || d.Kind != PhaseMain || c.Kind != PhaseWatcher || o.Kind != PhaseOutcome || f.OperatesOn != OperatesOnTicket {
		t.Errorf("kinds: %s/%s %s %s, starts on %s", d.ID, d.Kind, c.Kind, o.Kind, f.OperatesOn)
	}
	if !d.IsMain() || d.First != 0 || d.Last != 1 || c.First != 2 || c.Last != 3 || r.First != 4 || r.Last != 5 {
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
		t.Errorf("fires/subject: main %v/%v conflicts %v/%v", d.Fires(), d.HasSubject(), c.Fires(), c.HasSubject())
	}
	// Absent on_success: next stage in the chain; a chain's last stage ends
	// the PHASE (end) when anything watches — never the run.
	if f.Stage("build").OnSuccess != "mr" || f.Stage("mr").OnSuccess != TerminalEnd || f.Stage("sync_test").OnSuccess != TerminalEnd {
		t.Errorf("on_success: build→%s mr→%s sync_test→%s", f.Stage("build").OnSuccess, f.Stage("mr").OnSuccess, f.Stage("sync_test").OnSuccess)
	}
	if f.Stage("load").Phase != "review_fixes" || len(f.Stage("load").Outputs) != 1 || f.Stage("load").Outputs[0].Value != "artifacts/threads.yaml" {
		t.Errorf("load stage = %+v", f.Stage("load"))
	}
	for _, w := range f.Warnings {
		if strings.Contains(w, "threads.yaml") {
			t.Errorf("warning %q: threads.yaml is produced by load", w)
		}
	}
	if f.Stage("build").Line != 5 || f.Stage("load").Line != 16 {
		t.Errorf("lines: build %d load %d", f.Stage("build").Line, f.Stage("load").Line)
	}
}

func TestPhaseValidationErrors(t *testing.T) {
	entry := header + "main:\n  - { id: build, type: command, run: x }\n  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n" +
		"outcomes:\n  - { id: shipped, on: mr_merged, then: done }\n"
	w := func(item string) string { return entry + "watchers:\n" + item }
	o := func(item string) string { return entry + item } // one more outcome
	cmd := "stages: [{ id: s, type: command, run: x }]"
	cases := []struct{ name, doc, substr string }{
		{"a v4 document", strings.Replace(entry, "version: 5", "version: 4", 1), "this build speaks version 5) — v5 writes the chain a run starts with under `main:`"},
		{"v4 phases", header + "phases:\n  - { id: d, on: run_start, " + cmd + " }\n", "phases: is v4"},
		{"v4 stages", entry + "stages:\n  - { id: s, type: command, run: x }\n", "stages: is v4's shorthand"},
		{"no main", header + "watchers:\n  - { id: c, on: mr_conflicts, " + cmd + " }\n", "main is required"},
		{"no main and nothing watched", header + "operates_on: merge_request\noutcomes:\n  - { id: m, on: mr_merged, then: done }\n", "declare at least one"},
		{"bad operates_on", header + "operates_on: repository\nmain:\n  - { id: s, type: command, run: x }\n", `operates_on "repository" must be ticket`},
		{"retired run_start", header + "run_start: merge_request\nmain:\n  - { id: s, type: command, run: x }\n", "run_start: is now operates_on:"},
		{"missing on", w("  - { id: d, " + cmd + " }\n"), "signal is required"},
		{"start on manual", w("  - { id: m, on: { signal: manual, start: confirm }, " + cmd + " }\n"), "start does not apply"},
		{"run_start on a watcher", w("  - { id: m, on: run_start, " + cmd + " }\n"), `unknown signal "run_start"`},
		{"manual outcome", o("  - { id: m, on: manual, then: done }\n"), "an outcome fires on something the engine observes"},
		{"bad start", w("  - { id: m, on: { signal: mr_conflicts, start: later }, " + cmd + " }\n"), `start "later" must be`},
		{"then on a watcher", w("  - { id: m, on: mr_conflicts, then: done }\n"), "then is an outcome's"},
		{"bad then", o("  - { id: m, on: mr_closed, then: finish }\n"), `then "finish" must be`},
		{"max on an outcome", o("  - { id: m, on: { signal: mr_closed, max: 2 }, then: abort }\n"), "max does not apply to an outcome"},
		{"quiet_for on an outcome", o("  - { id: m, on: { signal: mr_closed, quiet_for: 1h }, then: abort }\n"), "quiet_for applies only"},
		{"end inside an outcome", o("  - { id: m, on: mr_closed, stages: [{ id: s, type: command, run: x, on_success: end }] }\n"), `outcome "m" ends the run`},
		{"end on an outcome's edge", o("  - { id: m, on: mr_closed, stages: [{ id: s, type: command, run: x, on_fail: end }] }\n"), `on_fail: outcome "m" ends the run`},
		{"no route ends the run", header + "main:\n  - { id: s, type: command, run: x }\nwatchers:\n  - { id: c, on: mr_conflicts, stages: [{ id: t, type: command, run: x }] }\n", "no route ends the run"},
		{"unknown signal", w("  - { id: m, on: mr_ghost, " + cmd + " }\n"), "unknown signal"},
		{"quiet on conflicts", w("  - { id: m, on: { signal: mr_conflicts, quiet_for: 1h }, " + cmd + " }\n"), "quiet_for applies only"},
		{"quiet too short", w("  - { id: m, on: { signal: mr_review_settled, quiet_for: 10s }, " + cmd + " }\n"), "at least 1m"},
		{"max zero", w("  - { id: m, on: { signal: mr_conflicts, max: 0 }, " + cmd + " }\n"), "max must be >= 1"},
		{"confirm is not grammar", w("  - { id: m, on: { signal: mr_conflicts, confirm: true }, " + cmd + " }\n"), `unknown key "confirm"`},
		{"unknown trigger key", w("  - { id: m, on: { signal: mr_conflicts, when: now }, " + cmd + " }\n"), `unknown key "when"`},
		{"empty watcher", w("  - { id: m, on: mr_conflicts, stages: [] }\n"), "a watcher needs a chain"},
		{"empty outcome", o("  - { id: m, on: mr_closed }\n"), "an outcome needs a chain"},
		{"sequence and stages", w("  - { id: m, on: mr_conflicts, sequence: main, " + cmd + " }\n"), "sequence or stages, not both"},
		{"sequence of nothing", w("  - { id: m, on: mr_conflicts, sequence: nope }\n"), `sequence "nope": no sequence of that name`},
		{"a watcher's run is retired", w("  - { id: m, on: mr_conflicts, run: main }\n"), "a watcher runs a sequence with sequence: main"},
		{"duplicate watcher", w("  - { id: shipped, on: mr_conflicts, " + cmd + " }\n"), "watchers and outcomes are one namespace"},
		{"watcher named main", w("  - { id: main, on: mr_conflicts, " + cmd + " }\n"), `id "main" is the main chain's`},
		{"sequence named main", header + "sequences:\n  - { id: main, " + cmd + " }\nmain:\n  - { id: s, type: command, run: x }\n", "the name is the flow's main chain"},
		{"main runs itself", header + "main:\n  - { id: s, type: command, run: x }\n  - { id: again, sequence: main }\n", "runs itself"},
		{"blocks is retired", header + "blocks:\n  - { id: b, " + cmd + " }\nmain:\n  - { id: s, type: command, run: x }\n", "blocks: is now sequences:"},
		{"include is retired", header + "sequences:\n  - { id: b, " + cmd + " }\nmain:\n  - { id: s, include: b }\n", `"include" is retired: a stage runs a sequence with "sequence" (sequence: b)`},
		{"when is run_if", header + "main:\n  - { id: s, type: command, run: x, when: run.ticket.tracker }\n", `"when" is now "run_if" (run_if: run.ticket.tracker)`},
		{"duplicate stage across chains", w("  - { id: m, on: mr_conflicts, stages: [{ id: build, type: command, run: x }] }\n"), "one namespace"},
		{"route leaves its chain", w("  - { id: m, on: mr_conflicts, stages: [{ id: s, type: command, run: x, on_success: build }] }\n"), "stay inside it"},
		{"edge leaves its chain", w("  - { id: m, on: mr_conflicts, stages: [{ id: s, type: command, run: x, on_fail: { to: build, max: 1 } }] }\n"), "stay inside it"},
		{"two create_mr in one chain", header + "main:\n  - { id: a, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n  - { id: b, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n", "per phase"},
		{"rebase without a leased push", w("  - { id: m, on: mr_conflicts, stages: [{ id: s, type: action, action: sync_target, with: { strategy: rebase } }, { id: p, type: command, run: \"git push\" }] }\n"), "push_branch"},
		{"bad strategy", w("  - { id: m, on: mr_conflicts, stages: [{ id: s, type: action, action: sync_target, with: { strategy: squash } }] }\n"), "not one of merge | rebase"},
		{"run_start on a gate", header + "main:\n  - { id: g, type: human_gate, auto_approve: run_start }\n", `unknown auto_approve signal "run_start"`},
		{"manual on a wait", header + "main:\n  - { id: w, type: wait, for: manual }\n", "is not something the engine observes"},
		{"run_start on a wait", header + "main:\n  - { id: w, type: wait, for: run_start }\n", "unknown signal"},
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

// Any observed signal may be watched, waited on or answer a gate — what it
// does is the flow's call, not the registry's.
func TestObservedSignalsGoAnywhere(t *testing.T) {
	doc := header + "main:\n  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n" +
		"  - { id: ready, type: wait, for: mr_mergeable, timeout: 1h, on_fail: end }\n" +
		"  - { id: g, type: human_gate, auto_approve: mr_approved, auto_reject: mr_conflicts }\n" +
		"watchers:\n  - { id: ship, on: { signal: mr_approved, start: confirm }, stages: [{ id: merge, type: action, action: mr_merge }] }\n" +
		"  - { id: gone, on: mr_closed, stages: [{ id: note, type: command, run: x }] }\n" +
		"outcomes:\n  - { id: shipped, on: mr_merged, then: done }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ship, gone := f.Phase("ship"), f.Phase("gone")
	if !ship.Fires() || !ship.HasSubject() || ship.On.Start != StartConfirm || !gone.Fires() || !gone.HasSubject() || gone.IsOutcome() {
		t.Errorf("ship %+v gone %+v", ship, gone)
	}
	if st := f.Stage("ready"); st.WaitFor != "mr_mergeable" || st.Wait.Timeout != time.Hour {
		t.Errorf("ready = %+v", st)
	}
}

// The main chain is a phase, MainPhase, opened when the run starts; with
// nothing watching, its last stage ends the run.
func TestMainChain(t *testing.T) {
	f, err := Parse("t.yaml", []byte(header+"main:\n  - { id: s, type: command, run: x }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Phases) != 1 || f.Phases[0].ID != MainPhase || !f.Phases[0].IsMain() || f.Stages[0].Phase != MainPhase || f.OperatesOnMR() {
		t.Fatalf("phases = %+v, stage phase %q", f.Phases, f.Stages[0].Phase)
	}
	if m := f.Main(); m == nil || f.Stages[0].Line != 5 || m.On.Max != UnlimitedMax || m.On.Signal != "" || f.Stages[0].OnSuccess != TerminalDone {
		t.Errorf("main %+v, line %d, on_success %q", m, f.Stages[0].Line, f.Stages[0].OnSuccess)
	}
}

// A watcher or an outcome may run a sequence — or the main chain — in place:
// `sequence: X` is one sequence stage running X, named after the watcher.
func TestRunASequence(t *testing.T) {
	doc := header + "operates_on: merge_request\n" +
		"sequences:\n  - { id: tidy, stages: [{ id: fmt, type: command, run: x }] }\n" +
		"main:\n  - { id: sync, type: action, action: sync_target }\n  - { id: push, type: action, action: push_branch }\n" +
		"watchers:\n  - { id: again, on: mr_conflicts, sequence: main }\n  - { id: neat, on: manual, sequence: tidy }\n" +
		"outcomes:\n  - { id: merged, on: mr_merged, then: done }\n  - { id: closed, on: mr_closed, sequence: tidy, then: abort }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, st := range f.Stages {
		ids = append(ids, st.Phase+":"+st.ID+"→"+st.OnSuccess)
	}
	want := "main:sync→push main:push→end again:again.sync→again.push again:again.push→end neat:neat.fmt→end closed:closed.fmt→abort"
	if got := strings.Join(ids, " "); got != want {
		t.Errorf("stages = %s\n want %s", got, want)
	}
	if !f.OperatesOnMR() || !f.Main().OnMR() || f.Stage("again.sync").Sequence != "again" {
		t.Errorf("starts on %s, main %+v", f.OperatesOn, f.Main())
	}
	for _, w := range f.Warnings {
		t.Errorf("warning: %s", w)
	}
}

// A flow whose runs start on a merge request may leave the main chain out
// and only watch it: a caretaker, started idle.
func TestCaretakerHasNoMain(t *testing.T) {
	doc := header + "operates_on: merge_request\n" +
		"watchers:\n  - { id: conflicts, on: mr_conflicts, stages: [{ id: sync, type: action, action: sync_target }, { id: push, type: action, action: push_branch }] }\n" +
		"outcomes:\n  - { id: merged, on: mr_merged, then: done }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if f.Main() != nil || len(f.Phases) != 2 || f.Phases[0].Kind != PhaseWatcher {
		t.Errorf("phases = %+v", f.Phases)
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
	doc := header + "main:\n  - { id: s, type: action, action: sync_target }\n  - { id: p, type: action, action: push_branch }\n" +
		"watchers:\n  - { id: m, on: mr_conflicts, stages: [{ id: t, type: command, run: x, each_repo: true }, { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }] }\n" +
		"outcomes:\n  - { id: shipped, on: mr_merged, then: done }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !warned(f, `"sync_target" acts on the merge request`) || !warned(f, "each_repo is ignored") || warned(f, "needs a merge request no stage opens") {
		t.Errorf("warnings = %v", f.Warnings)
	}
	noMR := header + "main:\n  - { id: s, type: command, run: x }\nwatchers:\n  - { id: m, on: mr_conflicts, stages: [{ id: t, type: command, run: x }] }\noutcomes:\n  - { id: shipped, on: mr_merged, then: done }\n"
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
	f, err := Parse("t.yaml", []byte(header+"main:\n"+
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
	g, err := Parse("t.yaml", []byte(header+"main:\n  - { id: a, type: command, run: x, on_success: end, on_fail: done }\n"))
	if err != nil || g.Stage("a").OnSuccess != TerminalEnd {
		t.Errorf("on_success: end = %v, %v", g, err)
	}
	wantErr(t, header+"main:\n  - { id: a, type: command, run: x, on_fail: { to: done, then: { to: a, max: 1 } } }\n", "then does not apply", 5)
	wantErr(t, header+"main:\n  - { id: a, type: command, run: x, on_fail: { to: end, max: 2 } }\n", `max does not apply when to is "end" (the phase ends)`, 5)
	wantErr(t, header+"main:\n  - { id: end, type: command, run: x }\n", `id "end" is reserved`, 5)
	wantErr(t, header+"main:\n  - { id: a, type: command, run: x, on_success: finish }\n", `on_success target "finish" does not exist`, 5)
}

// The run's end written where it does not belong is warned about, never
// refused: a watcher opened by a firing signal may run any number of times,
// so ending the whole run from it (done, abort) is almost always a slip for
// end; and a main chain that opens a merge request and says done without
// waiting for it leaves the watchers nothing to watch.
func TestEndingWarnings(t *testing.T) {
	mainChain := func(tail string) string {
		return header + "main:\n" +
			"  - { id: build, type: command, run: x, output: { mr: { to: artifacts/mr.md, type: file } } }\n" +
			"  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }\n" + tail
	}
	conflicts := func(push string) string {
		return "watchers:\n  - id: conflicts\n    on: mr_conflicts\n    stages:\n" +
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
	wait := "  - { id: wait_merge, type: wait, for: mr_merged, on_fail: abort }\n" +
		"  - { id: ticket_done, type: command, run: x, on_success: done }\n"

	// The main chain parks on the merge and says done after it; the watcher
	// ends with end. All there is to say is that the two take turns.
	w := warnings(mainChain(wait) + conflicts("      - { id: push, type: action, action: push_branch, on_success: end }\n"))
	if !strings.Contains(w, `stage "wait_merge": a wait keeps the run's one cursor`) || strings.Count(w, "\n") != 0 {
		t.Errorf("clean flow warned: %s", w)
	}
	// With no watcher a wait costs nothing.
	if w := warnings(mainChain(wait)); strings.Contains(w, "cursor") {
		t.Errorf("a wait with no watchers warned: %s", w)
	}
	// A watcher that ends the run, on success or on failure.
	w = warnings(mainChain(wait) + conflicts("      - { id: push, type: action, action: push_branch, on_success: done, on_fail: abort }\n"))
	if !strings.Contains(w, `stage "push": watcher "conflicts" runs on each mr_conflicts and may run any number of times, but this stage ends the whole run (done, abort)`) {
		t.Errorf("watcher ending the run: %s", w)
	}
	// A main chain that is done as soon as the merge request exists.
	w = warnings(mainChain("  - { id: note, type: command, run: x, on_success: done }\n") + conflicts(""))
	if !strings.Contains(w, `the main chain opens a merge request and ends the run (done) without waiting for it`) {
		t.Errorf("done without a wait: %s", w)
	}
	// A gate the merge answers is a wait too; and without watchers there is
	// nothing to miss.
	gate := "  - { id: final, type: human_gate, auto_approve: mr_merged, on_success: done, on_reject: abort }\n"
	if w := warnings(mainChain(gate) + conflicts("")); strings.Contains(w, "without waiting") {
		t.Errorf("a merge-answered gate is a wait: %s", w)
	}
	if w := warnings(header + "main:\n  - { id: b, type: command, run: x, output: { mr: { to: artifacts/mr.md, type: file } } }\n  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } }, on_success: done }\n"); strings.Contains(w, "without waiting") {
		t.Errorf("no watcher, nothing to miss: %s", w)
	}
}

// A watcher runs as often as its signal brings a new fact: unbounded unless
// the flow caps it, whatever its start policy.
func TestPhaseRunsAreUnboundedByDefault(t *testing.T) {
	entry := header + "main:\n  - { id: build, type: command, run: x }\n  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n" +
		"outcomes:\n  - { id: shipped, on: mr_merged, then: done }\nwatchers:\n"
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
	f, err := Parse("t.yaml", []byte(header+"main:\n  - { id: s, type: command, run: x, timeout: 5m }\n"))
	if err != nil || f.Stages[0].Timeout != 5*time.Minute {
		t.Fatalf("err %v timeout %v", err, f.Stages[0].Timeout)
	}
	wantErr(t, header+"main:\n  - { id: s, type: command, run: x, timeout: soon }\n", "invalid timeout", 5)
}

// Tracker actions: ticket_transition names a canonical status (`to`, an enum
// the tracker translates — readable from an enum parameter within it) OR
// the tracker's own word (`status`, a text setting: only blankness is a parse
// error, and quoting is the author's choice), exactly one; ticket_set_field
// takes its value from a file OR the flow; ticket_link warns when no merge
// request precedes it.
func TestTrackerActionSettings(t *testing.T) {
	head := "name: tk\nversion: 5\nparameters: { harness: fake, model: fake/m }\nagents: []\nmain:\n"
	plain := head + "  - { id: s, type: action, action: ticket_transition, with: { status: In Progress } }\n"
	quoted := head + "  - { id: s, type: action, action: ticket_transition, with: { status: \"In Progress\" } }\n"
	fp, err := Parse("t.yaml", []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	fq, err := Parse("t.yaml", []byte(quoted))
	if err != nil {
		t.Fatal(err)
	}
	if v := fp.Stages[0].Setting("status", ""); v != "In Progress" || v != fq.Stages[0].Setting("status", "") {
		t.Errorf("plain/quoted text settings differ: %+v vs %+v", fp.Stages[0].Settings, fq.Stages[0].Settings)
	}
	canon, err := Parse("t.yaml", []byte(head+"  - { id: s, type: action, action: ticket_transition, with: { to: in_review } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if st := canon.Stages[0]; st.Setting("to", "") != "in_review" || st.Setting("status", "") != "" {
		t.Errorf("canonical setting = %+v", st.Settings)
	}
	// The canonical status may come from an enum parameter within the
	// statuses — a project decides where its issues go once merged.
	withParam := strings.Replace(head, "model: fake/m", "model: fake/m, final: { type: string, default: done, enum: [done, in_review] }", 1)
	viaParam, err := Parse("t.yaml", []byte(withParam+"  - { id: s, type: action, action: ticket_transition, with: { to: $final } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v := viaParam.Stages[0].Setting("to", ""); v != "done" {
		t.Errorf("to via $final = %+v", viaParam.Stages[0].Settings)
	}
	if bound, err := viaParam.Resolve(map[string]string{"final": "in_review"}); err != nil || bound.Stages[0].Setting("to", "") != "in_review" {
		t.Errorf("to via $final bound = %+v, %v", bound, err)
	}
	for _, tc := range []struct{ doc, substr string }{
		{head + "  - { id: s, type: action, action: ticket_transition, with: { to: \"In Progress\" } }\n", `with "to": "In Progress" is not one of todo | in_progress | in_review | done | blocked`},
		{head + "  - { id: s, type: action, action: ticket_transition, with: { status: \"  \" } }\n", `with "status": a value is required`},
		{head + "  - { id: s, type: action, action: ticket_transition }\n", `takes exactly one of to / status`},
		{head + "  - { id: s, type: action, action: ticket_transition, with: { to: done, status: Done } }\n", `takes exactly one of to / status`},
		{strings.Replace(head, "model: fake/m", "model: fake/m, wide: { type: string, default: done }", 1) +
			"  - { id: s, type: action, action: ticket_transition, with: { to: $wide } }\n", `parameter "wide" needs an enum within todo | in_progress`},
		{strings.Replace(head, "model: fake/m", "model: fake/m, wide: { type: string, default: done, enum: [done, closed] }", 1) +
			"  - { id: s, type: action, action: ticket_transition, with: { to: $wide } }\n", `parameter "wide" allows "closed", which this field does not`},
		{head + "  - { id: s, type: action, action: ticket_set_field, with: { field: Team } }\n", "exactly one of value"},
		{head + "  - { id: s, type: action, action: ticket_set_field, input: { value: { from: repo:t.txt, type: file } }, with: { field: Team, text: Platform } }\n", "exactly one of value"},
		{head + "  - { id: s, type: action, action: ticket_set_field, with: { field: Team, text: Platform, mode: merge } }\n", "not one of replace | append"},
		{head + "  - { id: s, type: action, action: ticket_set_field, input: { value: { from: /abs, type: file } }, with: { field: Team } }\n", "must be a path some stage writes"},
	} {
		if _, err := Parse("t.yaml", []byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.substr) {
			t.Errorf("doc %q: err = %v, want %q", tc.doc[len(head):], err, tc.substr)
		}
	}
	ok := head + "  - { id: s, type: action, action: ticket_set_field, input: { value: { from: repo:flags.txt, type: file } }, with: { field: \"Feature flags\", mode: append } }\n"
	if _, err := Parse("t.yaml", []byte(ok)); err != nil {
		t.Errorf("valid set_field refused: %v", err)
	}

	// ticket_link's prerequisite: a warning in a main chain with no create_mr
	// before it, none inside a watcher (the subject exists).
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
	after := head + "  - { id: m, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n  - { id: l, type: action, action: ticket_link }\n"
	if f, err := Parse("t.yaml", []byte(after)); err != nil || needsMR(f) != 0 {
		t.Errorf("link after create_mr: err %v warnings %v", err, f.Warnings)
	}
	phased := "name: tk\nversion: 5\nparameters: { harness: fake, model: fake/m }\nagents: []\n" +
		"main:\n  - { id: m, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n" +
		"watchers:\n  - { id: react, on: mr_conflicts, stages: [ { id: l, type: action, action: ticket_link } ] }\n" +
		"outcomes:\n  - { id: shipped, on: mr_merged, then: done }\n"
	if f, err := Parse("t.yaml", []byte(phased)); err != nil || needsMR(f) != 0 {
		t.Errorf("link in a watcher: err %v warnings %v", err, f.Warnings)
	}
}

// Outcomes (FLOWLANG §1) fire once and end the run. An outcome takes a start
// policy like a watcher, no max or quiet_for; its last stage's implicit
// success is its then (done by default); with no stages, then says how the
// run ends. on_success may say abort anywhere; the outcome signals stay
// waitable; and a flow needs some route to done.
func TestOutcomes(t *testing.T) {
	doc := header + "main:\n  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n  - { id: w, type: wait, for: mr_closed, on_fail: end }\n" +
		"watchers:\n  - { id: giveup, on: manual, stages: [{ id: g, type: command, run: x, on_success: abort, on_fail: done }] }\n" +
		"outcomes:\n  - id: merged\n    on: { signal: mr_merged, start: confirm }\n    stages:\n      - { id: tidy, type: command, run: x }\n      - { id: last, type: command, run: x, on_fail: abort }\n" +
		"  - { id: closed, on: mr_closed, then: abort }\n" +
		"  - { id: approved, on: mr_approved, then: abort, stages: [{ id: back, type: command, run: x }] }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	merged, closed := f.Phase("merged"), f.Phase("closed")
	if !merged.IsOutcome() || !merged.Observed() || merged.Fires() || merged.HasSubject() || merged.On.Start != StartConfirm || merged.On.Max != UnlimitedMax || merged.Then != TerminalDone {
		t.Errorf("merged = %+v", merged)
	}
	if f.Stage("tidy").OnSuccess != "last" || f.Stage("last").OnSuccess != TerminalDone {
		t.Errorf("an outcome's chain: tidy→%s last→%s", f.Stage("tidy").OnSuccess, f.Stage("last").OnSuccess)
	}
	if !closed.IsOutcome() || !closed.HasSubject() || closed.Then != TerminalAbort || closed.On.Start != StartAuto || closed.First != 5 || closed.Last != 4 { // no stages of its own; back follows at 5
		t.Errorf("closed = %+v", closed)
	}
	if f.Stage("back").OnSuccess != TerminalAbort {
		t.Errorf("an outcome's then is its chain's end: back→%s", f.Stage("back").OnSuccess)
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
	base := header + "main:\n  - { id: b, type: command, run: x }\nwatchers:\n  - { id: c, on: mr_conflicts, stages: [{ id: c1, type: command, run: x }] }\n"
	for _, tc := range []struct{ name, tail string }{
		{"then", "outcomes:\n  - { id: o, on: mr_merged, then: done }\n"},
		{"on_success", "  - { id: o, on: manual, stages: [{ id: s, type: command, run: x, on_success: done }] }\n"},
		{"edge", "  - { id: o, on: manual, stages: [{ id: s, type: command, run: x, on_fail: done }] }\n"},
	} {
		if f, err := Parse("t.yaml", []byte(base+tc.tail)); err != nil || !f.EndsRun() {
			t.Errorf("%s: err %v", tc.name, err)
		}
	}
}

// One way in: a run starts with the main chain (a person's or an
// automation's act, so no start policy), and a watcher `on: manual` is
// started by hand from the run page only.
func TestMainAndManual(t *testing.T) {
	doc := header + "main:\n  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n" +
		"watchers:\n  - { id: polish, on: { signal: manual, max: 3 }, stages: [{ id: shine, type: command, run: x }] }\n" +
		"outcomes:\n  - { id: shipped, on: mr_merged, then: done }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	polish := f.Phase("polish")
	if polish.IsMain() || polish.Fires() || polish.HasSubject() || polish.On.Max != 3 || polish.On.Start != StartManual {
		t.Errorf("polish = %+v", polish.On)
	}
	main := f.Main()
	if main == nil || main.ID != MainPhase || !main.IsMain() || main.Fires() || main.On.Max != UnlimitedMax || main.On.Start != StartManual {
		t.Errorf("main = %+v", main)
	}
}

// Parameters: the flow's one settings block. The engine-known six land in
// Defaults and Budget (typed, validated by the rules their old homes had,
// present even when omitted); the flow's own are typed by their defaults and
// read where $name appears.
func TestParametersDeclare(t *testing.T) {
	doc := `name: t
version: 5
parameters:
  harness: fake
  model: { default: a/b, description: "the default model" }
  max_cost_usd: 20
  verify: { type: string, default: verify, description: "the test command" }
  review_model: { type: string, default: a/c }
  plan_gate: { type: bool, default: true }
  turns: { type: int, default: 3 }
  rate: { type: float, default: 1.5 }
  mode: { type: string, default: confirm, enum: [auto, confirm, manual] }
agents:
  - name: b
    prompt: do b
main:
  - { id: build, type: agent, harness: $harness, agent: b, model: $review_model, max_turns: $turns, run_if: $plan_gate }
  - { id: test, type: command, run_ref: $verify }
  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }
watchers:
  - id: conflicts
    on: { signal: mr_conflicts, start: $mode }
    stages:
      - { id: sync, type: action, action: sync_target }
outcomes:
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
	types := map[string]ParamType{"verify": ParamString, "plan_gate": ParamBool, "turns": ParamInt, "rate": ParamFloat, "mode": ParamString, "max_cost_usd": ParamFloat, "timeout": ParamDuration}
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
	if build.Model != "a/c" || build.MaxTurns != 3 || build.RunIf != nil || test.RunRef != "verify" || f.Phase("conflicts").On.Start != StartConfirm {
		t.Errorf("resolved: build %+v test %+v conflicts %+v", build, test, f.Phase("conflicts").On)
	}
	refs := map[string]string{}
	for _, r := range f.Refs {
		refs[r.Param] = r.Phase + "/" + r.Stage + "/" + r.Field
	}
	if len(f.Refs) != 6 || refs["review_model"] != "main/build/model" || refs["plan_gate"] != "main/build/run_if" || refs["harness"] != "main/build/harness" ||
		refs["verify"] != "main/test/run_ref" || refs["mode"] != "conflicts//start" || refs["turns"] != "main/build/max_turns" {
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

	stages := "main:\n  - { id: s, type: command, run: x }\n"
	for _, tc := range []struct{ name, params, substr string }{
		{"bad name", "{ harness: fake, model: a/b, Bad: 1 }", "is not a parameter name"},
		{"no default, no type", "{ harness: fake, model: a/b, x: { description: hi } }", "x: type is required (bool | int | float | string | duration)"},
		{"known, no default", "{ harness: fake, model: a/b, timeout: { type: duration } }", "timeout: default is required"},
		{"enum on an int", "{ harness: fake, model: a/b, x: { type: int, default: 1, enum: [1, 2] } }", "enum applies to a string"},
		{"default not in enum", "{ harness: fake, model: a/b, x: { type: string, default: c, enum: [a, b] } }", "is not one of its enum"},
		{"list default not in enum", "{ harness: fake, model: a/b, x: { type: string, list: true, default: [a, c], enum: [a, b] } }", `default "c" is not one of its enum`},
		{"type required", "{ harness: fake, model: a/b, x: { default: true } }", "x: type is required — type: bool"},
		{"type required, list", "{ harness: fake, model: a/b, x: { default: [a] } }", "type is required — type: string, list: true"},
		{"a bare custom default", "{ harness: fake, model: a/b, x: 1 }", "x: type is required — type: int"},
		{"unknown type", "{ harness: fake, model: a/b, x: { type: text, default: a } }", `type "text" must be one of bool | int | float | string | duration`},
		{"default not of its type", "{ harness: fake, model: a/b, x: { type: int, default: many } }", `takes an integer, got "many"`},
		{"a duration that is none", "{ harness: fake, model: a/b, x: { type: duration, default: soon } }", "takes a duration"},
		{"list without a list", "{ harness: fake, model: a/b, x: { type: string, list: true, default: a } }", "x is a list: its default is a list too"},
		{"a list default undeclared", "{ harness: fake, model: a/b, x: { type: string, default: [a] } }", "the default is a list — declare list: true"},
		{"known, another type", "{ harness: fake, model: a/b, max_turns: { type: string, default: x } }", "max_turns is the engine's: a int, not a string"},
		{"known, a list", "{ harness: fake, model: a/b, timeout: { type: duration, list: true, default: [1m] } }", "timeout is the engine's: one duration, not a list"},
		{"known, bad value", "{ harness: 1, model: a/b }", `"1" is not a slug`},
		{"float as int", "{ harness: fake, model: a/b, max_turns: 1.5 }", `max_turns: default: parameter "max_turns" takes an integer, got "1.5"`},
		{"null default", "{ harness: fake, model: a/b, x: }", "needs a default value"},
		{"unknown long-form key", "{ harness: fake, model: a/b, x: { type: int, default: 1, kind: int } }", `unknown key "kind" in a parameter`},
		{"bad timeout", "{ harness: fake, model: a/b, timeout: soon }", `"timeout" takes a duration (30m, 2h), got "soon"`},
		{"max_cost zero", "{ harness: fake, model: a/b, max_cost_usd: 0 }", "max_cost_usd must be > 0"},
		{"declared twice", "{ harness: fake, model: a/b, x: 1, x: 2 }", "declared twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte("name: t\nversion: 5\nparameters: "+tc.params+"\n"+stages))
			if err == nil || !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("err = %v, want %q", err, tc.substr)
			}
		})
	}
	// defaults: and budget: are not grammar.
	if _, err := Parse("t.yaml", []byte("name: t\nversion: 5\ndefaults: { harness: fake, model: a/b }\n"+stages)); err == nil || !strings.Contains(err.Error(), `unknown key "defaults"`) {
		t.Errorf("defaults: err = %v", err)
	}
}

// A field that reads no parameter refuses one: written there, $name would
// stay the text "$name", never the value a workshop binds.
func TestNoParamWhereNoneIsRead(t *testing.T) {
	head := "name: t\nversion: 5\nparameters: { harness: fake, model: a/b, s: { type: string, default: text } }\n"
	stage := "main:\n  - { id: a, type: command, run: x }\n"
	agent := func(fields string) string {
		return "agents:\n  - { name: b, prompt: do b" + fields + " }\n" + stage
	}
	const refused = "$s is a parameter, and this field reads none"
	for _, tc := range []struct{ name, doc string }{
		{"flow description", "name: t\nversion: 5\ndescription: $s\nparameters: { harness: fake, model: a/b, s: { type: string, default: text } }\n" + stage},
		{"parameter description", "name: t\nversion: 5\nparameters: { harness: fake, model: a/b, s: { type: string, default: text, description: $s } }\n" + stage},
		{"stage description", head + "main:\n  - { id: a, type: command, run: x, description: $s }\n"},
		{"gate question", head + "main:\n  - { id: a, type: human_gate, question: $s }\n"},
		{"command script", head + "main:\n  - { id: a, type: command, run: $s }\n"},
		{"command reads", head + "main:\n  - { id: a, type: command, run: x, input: { f: { from: $s, type: file } } }\n"},
		{"command writes", head + "main:\n  - { id: a, type: command, run: x, output: { f: { to: $s, type: file } } }\n"},
		{"gate shows", head + "main:\n  - { id: a, type: human_gate, question: ok, input: { show: { from: $s, type: file } } }\n"},
		{"port description", head + "main:\n  - { id: a, type: command, run: x, input: { f: { from: repo:f.md, type: file, description: $s } } }\n"},
		{"agent description", head + agent(", description: $s")},
		{"agent prompt", strings.Replace(head+agent(""), "prompt: do b", "prompt: $s", 1)},
		{"agent input description", head + agent(", inputs: [ { name: i, type: file, description: $s, optional: true } ]")},
		{"watcher description", head + "operates_on: merge_request\n" + stage + "watchers:\n  - { id: w, description: $s, on: manual, stages: [ { id: t, type: command, run: y } ] }\n"},
		{"sequence description", head + "sequences:\n  - { id: q, description: $s, stages: [ { id: z, type: command, run: z } ] }\nmain:\n  - { id: a, sequence: q }\n"},
		{"sequence stage description", head + "sequences:\n  - { id: q, stages: [ { id: z, type: command, run: z } ] }\nmain:\n  - { id: a, sequence: q, description: $s }\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), refused) {
				t.Errorf("err = %v, want %q", err, refused)
			}
		})
	}
	// A declared parameter named inside a script or a prompt is not read
	// either: a warning says so.
	mention := strings.Replace(head+agent(""), "prompt: do b", "prompt: \"use ${s} here\"", 1)
	mention = strings.Replace(mention, "run: x", "run: \"echo $s\"", 1)
	f, err := Parse("t.yaml", []byte(mention))
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(f.Warnings, "\n"); !strings.Contains(w, `stage "a": its script names $s`) || !strings.Contains(w, `agent "b": its prompt names $s`) {
		t.Errorf("warnings = %q", f.Warnings)
	}
	// Text that only mentions a $ — a shell variable, a sentinel in prose —
	// is text.
	ok := head + "main:\n  - { id: a, type: command, run: \"echo $HOME\", description: \"costs $5\" }\n"
	if _, err := Parse("t.yaml", []byte(ok)); err != nil {
		t.Errorf("text with a $ in it: %v", err)
	}
}

// Where a $parameter may be read, and the checks at each site: declared, of
// the site's type, an enum within an enum site's values, and then the site's
// own rule on the value it yields.
func TestParamSites(t *testing.T) {
	head := "name: t\nversion: 5\nparameters: { harness: fake, model: a/b, s: { type: string, default: text }, d: { type: duration, default: 5m }, n: { type: int, default: 2 }, b: { type: bool, default: true }, e: { type: string, default: auto, enum: [auto, confirm] }, big: { type: string, default: auto, enum: [auto, confirm, manual] } }\n" + commonAgents
	for _, tc := range []struct{ name, doc, substr string }{
		{"sentinel at a site", head + "main:\n  - { id: a, type: agent, harness: $harness, agent: b, model: $DIFF_TARGET }\n", "$DIFF_TARGET is not a parameter"},
		{"undeclared", head + "main:\n  - { id: a, type: agent, harness: $harness, agent: b, model: $nope }\n", `parameter "nope" is not declared`},
		{"wrong type", head + "main:\n  - { id: a, type: agent, harness: $harness, model: $model, agent: b, max_turns: $s }\n", `parameter "s" is a string, and this field takes a int`},
		{"bool at a string site", head + "main:\n  - { id: a, type: agent, model: $model, agent: b, harness: $b }\n", "is a bool"},
		{"param in a path", head + "main:\n  - { id: a, type: agent, harness: $harness, model: $model, agent: b, input: { plan: { from: $s, type: file } } }\n", "$s is a parameter, and this field reads none"},
		{"enum site needs an enum", head + "main:\n  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\nwatchers:\n  - { id: c, on: { signal: mr_conflicts, start: $s }, stages: [{ id: x, type: command, run: x }] }\n", "needs an enum within"},
		{"enum wider than the site", head + "main:\n  - { id: s, type: action, action: sync_target, with: { strategy: $big } }\n", `allows "auto", which this field does not`},
		{"mixed case ref", head + "main:\n  - { id: a, type: agent, harness: $harness, agent: b, model: $Mixed }\n", "is not a parameter reference"},
		{"value fails the site rule", head + "main:\n  - { id: a, type: agent, harness: $harness, agent: b, model: $s }\n", `model: "text" (parameter "s") is not of the form provider/model`},
		{"duration via param", head + "main:\n  - { id: a, type: agent, harness: $harness, model: $model, agent: b, timeout: $s }\n", `parameter "s" is a string, and this field takes a duration`},
		{"max_turns via param zero", "name: t\nversion: 5\nparameters: { harness: fake, model: a/b, z: { type: int, default: 0 } }\n" + commonAgents + "main:\n  - { id: a, type: agent, harness: $harness, model: $model, agent: b, max_turns: $z }\n", `max_turns must be > 0, got 0 (parameter "z")`},
		{"on.max via string", head + "main:\n  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\nwatchers:\n  - { id: c, on: { signal: mr_conflicts, max: $s }, stages: [{ id: x, type: command, run: x }] }\n", "takes a int"},
		{"action enum via plain string", head + "main:\n  - { id: s, type: action, action: sync_target, with: { strategy: $s } }\n", "needs an enum within"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.yaml", []byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("err = %v, want %q", err, tc.substr)
			}
		})
	}
	ok := head + "main:\n" +
		"  - { id: a, type: agent, agent: b, max_turns: $n, timeout: $d, harness: $s, model: a/c }\n" +
		"  - { id: c, type: command, run: \"echo $HOME\", run_ref: $s, timeout: $d }\n"
	ok = strings.Replace(ok, "run: \"echo $HOME\", run_ref: $s", "run: \"echo $HOME\"", 1)
	ok += "  - { id: w, type: wait, for: mr_merged }\n  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }\n" +
		"watchers:\n  - id: cf\n    on: { signal: mr_conflicts, start: $big, max: $n, quiet_for: $d }\n    stages: [{ id: x, type: command, run: x }]\n" +
		"outcomes:\n  - { id: shipped, on: mr_merged, then: done }\n"
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
version: 5
parameters:
  harness: fake
  model: a/b
  max_cost_usd: 5
  verify: { type: string, default: verify }
  gate: { type: bool, default: true }
  mode: { type: string, default: confirm, enum: [auto, confirm, manual] }
  turns: { type: int, default: 2 }
` + commonAgents + `main:
  - { id: a, type: agent, harness: $harness, model: $model, agent: b, max_turns: $turns, run_if: $gate }
  - { id: t, type: command, run_ref: $verify }
  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:mr.md, type: file } } }
watchers:
  - id: c
    on: { signal: mr_conflicts, start: $mode, max: -1 }
    stages:
      - { id: s, type: action, action: sync_target }
outcomes:
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
		r.Stage("a").RunIf == nil || r.Stage("a").RunIf.Fact != CondNever || r.Phase("c").On.Start != StartManual || r.Hash != f.Hash || r.Bindings["mode"] != "manual" {
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

// run_if: a run fact by its path or a bool parameter (which folds), optionally
// negated as `not x`, or the change type compared with == / !=; a
// conditional writer is no longer a definite one.
func TestWhen(t *testing.T) {
	head := "name: t\nversion: 5\nparameters: { harness: fake, model: a/b, yes: { type: bool, default: true }, no: { type: bool, default: false }, s: { type: string, default: x } }\n"
	f, err := Parse("t.yaml", []byte(head+"main:\n"+
		"  - { id: a, type: command, run: x, run_if: run.ticket.tracker }\n"+
		"  - { id: b, type: command, run: x, run_if: not run.subject }\n"+
		"  - { id: c, type: command, run: x, run_if: $yes }\n"+
		"  - { id: d, type: command, run: x, run_if: $no }\n"+
		"  - { id: e, type: command, run: x, run_if: not $no }\n"+
		"  - { id: g, type: command, run: x, run_if: not $yes }\n"+
		"  - { id: h, type: command, run: x, run_if: run.ticket.change_type == fix }\n"+
		"  - { id: i, type: command, run: x, run_if: 'run.ticket.change_type!=chore' }\n"))
	if err != nil {
		t.Fatal(err)
	}
	tracker, subject := CondFacts{Tracker: true, Change: "feat"}, CondFacts{Subject: true, Change: "feat"}
	a, b := f.Stage("a").RunIf, f.Stage("b").RunIf
	if a == nil || a.Fact != CondTracker || a.Not || !a.Holds(tracker) || a.Holds(subject) || a.String() != "run.ticket.tracker" {
		t.Errorf("a = %+v", a)
	}
	if b == nil || b.Fact != CondSubject || !b.Not || b.Holds(subject) || !b.Holds(tracker) || b.String() != "not run.subject" {
		t.Errorf("b = %+v", b)
	}
	// The ticket's change type.
	h, i := f.Stage("h").RunIf, f.Stage("i").RunIf
	if h == nil || !h.Holds(CondFacts{Change: "fix"}) || h.Holds(CondFacts{Tracker: true, Subject: true, Change: "feat"}) || h.Fact != CondChangeType || h.Value != "fix" || h.Not || h.String() != "run.ticket.change_type == fix" {
		t.Errorf("h = %+v", h)
	}
	if i == nil || i.Holds(CondFacts{Change: "chore"}) || !i.Holds(CondFacts{Change: "fix"}) || !i.Not || i.String() != "run.ticket.change_type != chore" {
		t.Errorf("i = %+v", i)
	}
	if f.Stage("c").RunIf != nil || f.Stage("e").RunIf != nil {
		t.Errorf("true folds to no condition: c %+v e %+v", f.Stage("c").RunIf, f.Stage("e").RunIf)
	}
	for _, id := range []string{"d", "g"} {
		if w := f.Stage(id).RunIf; w == nil || w.Fact != CondNever || w.Holds(CondFacts{Tracker: true, Subject: true, Change: "never"}) {
			t.Errorf("%s = %+v, want never", id, w)
		}
	}
	for _, tc := range []struct{ doc, substr string }{
		{head + "main:\n  - { id: a, type: command, run: x, run_if: maybe }\n", "is not a condition"},
		{head + "main:\n  - { id: a, type: command, run: x, run_if: tracker }\n", "is not a condition"},
		{head + "main:\n  - { id: a, type: command, run: x, run_if: fix }\n", "is not a condition"},
		{head + "main:\n  - { id: a, type: command, run: x, run_if: run.ticket.change_type }\n", "compares, as"},
		{head + "main:\n  - { id: a, type: command, run: x, run_if: run.ticket.change_type == bug }\n", `"bug" is not a change type`},
		{head + "main:\n  - { id: a, type: command, run: x, run_if: not run.ticket.change_type == fix }\n", "only run.ticket.change_type compares"},
		{head + "main:\n  - { id: a, type: command, run: x, run_if: run.subject == fix }\n", "only run.ticket.change_type compares"},
		{head + "main:\n  - { id: a, type: command, run: x, run_if: $s }\n", "is a string, and this field takes a bool"},
	} {
		if _, err := Parse("t.yaml", []byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.substr) {
			t.Errorf("err = %v, want %q", err, tc.substr)
		}
	}
	cond := header + commonAgents + "main:\n  - { id: p, type: agent, harness: $harness, model: $model, agent: builder, run_if: run.ticket.tracker, output: { patch: { to: artifacts/patch.md, type: file } } }\n" +
		"  - { id: c, type: command, run: x, input: { patch: { from: artifacts/patch.md, type: file } } }\n"
	if _, err := Parse("t.yaml", []byte(cond)); err == nil || !strings.Contains(err.Error(), `"artifacts/patch.md" is not written on every route reaching this stage`) {
		t.Errorf("a conditional writer should not count: %v", err)
	}
	// A reader that does without it says so.
	if _, err := Parse("t.yaml", []byte(strings.Replace(cond, "{ from: artifacts/patch.md, type: file }", "{ from: artifacts/patch.md, type: file, optional: true }", 1))); err != nil {
		t.Errorf("optional reader of a conditional writer: %v", err)
	}
}

// The editor's document shape: parameters as an ordered object of long
// forms with typed defaults, int fields as a number or a "$name" string.
func TestDocJSONShapes(t *testing.T) {
	doc := "name: t\nversion: 5\nparameters:\n  harness: fake\n  model: a/b\n  turns: { type: int, default: 3 }\n  cost: { type: float, default: 1.5 }\n  gate: { type: bool, default: true }\n  mode: { type: string, default: a, enum: [a, b], description: pick }\n  labels: { type: string, list: true, default: [x, y] }\n" + commonAgents +
		"main:\n  - { id: s, type: agent, harness: $harness, model: $model, agent: b, max_turns: $turns, run_if: $gate }\n" +
		"watchers:\n  - id: c\n    on: { signal: mr_conflicts, max: $turns }\n    stages: [{ id: x, type: command, run: x }]\n" +
		"outcomes:\n  - { id: o, on: mr_merged, then: done }\n"
	d, err := ParseDoc("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"parameters":{"harness":{"default":"fake"},"model":{"default":"a/b"},"turns":{"type":"int","default":3},"cost":{"type":"float","default":1.5},"gate":{"type":"bool","default":true},"mode":{"type":"string","default":"a","description":"pick","enum":["a","b"]},"labels":{"type":"string","list":true,"default":["x","y"]}}`,
		`"max_turns":"$turns"`, `"run_if":"$gate"`, `"main":[{"id":"s"`, `"on":{"signal":"mr_conflicts","max":"$turns"}`,
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
	if err := json.Unmarshal([]byte(`{"name":"t","version":5,"parameters":{"harness":"fake","n":2}}`), &short); err != nil || len(short.Parameters.Entries) != 2 || short.Parameters.Entries[1].Param.Default.Hint != ParamInt {
		t.Errorf("short form: %v %+v", err, short.Parameters.Entries)
	}
}

// The Ask pane (FLOWLANG §1 ask): a harness kind, or a $parameter; its
// agent name is reserved.
func TestAskSection(t *testing.T) {
	cmd := "main:\n  - { id: s, type: command, run: x }\n"
	f, err := Parse("t.yaml", []byte(header+"ask: { harness: opencode }\n"+cmd))
	if err != nil {
		t.Fatal(err)
	}
	if f.Ask == nil || f.Ask.Harness != "opencode" {
		t.Fatalf("ask = %+v", f.Ask)
	}
	f, err = Parse("t.yaml", []byte(header+"ask: { harness: $harness }\n"+cmd))
	if err != nil {
		t.Fatal(err)
	}
	if f.Ask == nil || f.Ask.Harness != "fake" {
		t.Fatalf("ask via parameter = %+v", f.Ask)
	}
	if f, err := Parse("t.yaml", []byte(header+cmd)); err != nil || f.Ask != nil {
		t.Fatalf("no ask section: %+v, %v", f.Ask, err)
	}
	for _, tc := range []struct{ name, doc, want string }{
		{"no harness", header + "ask: {}\n" + cmd, "ask: harness is required"},
		{"not a slug", header + "ask: { harness: 'not a kind' }\n" + cmd, "is not a slug"},
		{"undeclared parameter", header + "ask: { harness: $nope }\n" + cmd, "not declared"},
		{"reserved agent name", header + "ask: { harness: fake }\nagents:\n  - { name: ask, description: d, prompt: p }\n" + cmd, "the name is the Ask pane's"},
	} {
		_, err := Parse("t.yaml", []byte(tc.doc))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	// Without ask:, an agent may be called ask.
	if _, err := Parse("t.yaml", []byte(header+"agents:\n  - { name: ask, description: d, prompt: p }\n"+cmd)); err != nil {
		t.Errorf("agent named ask without an ask section: %v", err)
	}
}

// The values run.ticket.change_type compares with are the canonical change types.
func TestCondChangeTypesAreCanonical(t *testing.T) {
	var want []string
	for _, c := range trackerport.ChangeTypes() {
		want = append(want, string(c))
	}
	if !slices.Equal(CondChangeTypes, want) {
		t.Errorf("CondChangeTypes = %v, want trackerport.ChangeTypes %v", CondChangeTypes, want)
	}
}
