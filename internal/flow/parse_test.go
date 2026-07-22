package flow

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// header is a minimal valid preamble; tests append stages to it.
const header = `name: t
defaults: { harness: fake, model: anthropic/claude-sonnet-4-6 }
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

func TestCanonicalV1(t *testing.T) {
	// The DESIGN §3.1 document survives only as a test fixture — the
	// product ships no built-in flows (user-defined, post-M8).
	f, err := Load("testdata/v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "v1" {
		t.Errorf("name = %q", f.Name)
	}
	if len(f.Hash) != 64 {
		t.Errorf("hash = %q, want 64 hex chars", f.Hash)
	}
	// harness is deliberately unpinned in the canonical flow (DESIGN2 §6.3):
	// empty resolves to the project's harness kind at run creation.
	if f.Defaults.Timeout != 30*time.Minute || f.Defaults.MaxTurns != 50 ||
		f.Defaults.Harness != "fake" || f.Defaults.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("defaults = %+v", f.Defaults)
	}
	if f.Budget.MaxCostUSD != 10.0 || f.Budget.MaxTotalTurns != 300 {
		t.Errorf("budget = %+v", f.Budget)
	}

	ids := []string{"plan", "plan_gate", "build", "test", "review", "redact", "push", "mr", "final_gate"}
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
	if plan.Output != "artifacts/plan.md" {
		t.Errorf("plan.Output = %q", plan.Output)
	}

	review := f.Stage("review")
	if review.Model != "openai/gpt-5" {
		t.Errorf("review model override = %q", review.Model)
	}
	if !review.Verdict || review.Edge(EdgeReject) == nil || review.Edge(EdgeReject).To != "build" ||
		review.Edge(EdgeReject).Max != 3 || review.Edge(EdgeReject).Feed != FeedComments {
		t.Errorf("review verdict/edge = %+v %+v", review.Verdict, review.Edge(EdgeReject))
	}

	test := f.Stage("test")
	if test.RunRef != "verify" || test.Edge(EdgeFail) == nil || test.Edge(EdgeFail).To != "build" ||
		test.Edge(EdgeFail).Max != 5 || test.Edge(EdgeFail).Feed != FeedOutput {
		t.Errorf("test stage = %+v", test)
	}

	gate := f.Stage("final_gate")
	if gate.Ping != PingFinalReview || gate.Payload != "mr_url" || gate.AutoComplete != "mr_merged" {
		t.Errorf("final_gate = %+v", gate)
	}

	if f.Index("plan") != 0 || f.Index("final_gate") != 8 || f.Index("nope") != -1 {
		t.Error("Index misbehaves")
	}
}

// TestLiveV1FlowParses locks the repo's shipped example flow to the schema:
// it exercises abort terminals, on_timeout, then-chains and manual_debug.
func TestLiveV1FlowParses(t *testing.T) {
	f, err := Load("../../artifacts/flows/v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	test := f.Stage("test")
	if test.Edge(EdgeFail).Then == nil || test.Edge(EdgeFail).Then.To != "debug_test" {
		t.Errorf("test.on_fail.then = %+v, want debug_test escalation", test.Edge(EdgeFail).Then)
	}
	if test.Edge(EdgeTimeout) == nil || test.Edge(EdgeTimeout).To != "test" {
		t.Errorf("test.on_timeout = %+v, want self-retry", test.Edge(EdgeTimeout))
	}
	dbg := f.Stage("debug_test")
	if dbg.Ping != PingManualDebug || dbg.Payload != PayloadWorkspace || dbg.Edge(EdgeReject).To != TerminalAbort {
		t.Errorf("debug_test gate = %+v", dbg)
	}
	if f.Stage("final_gate").Edge(EdgeReject).To != TerminalAbort {
		t.Errorf("final_gate.on_reject = %+v, want abort", f.Stage("final_gate").Edge(EdgeReject))
	}

	// The flow's failure philosophy: every agent/command stage declares
	// on_fail, every on_fail chain ends at a manual_debug rescue gate, and
	// that gate resumes the stage that escalated to it — the success chain
	// replays from the failure point, never skipping stages.
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
		tgt := f.Stage(e.To)
		if tgt == nil || tgt.Ping != PingManualDebug {
			t.Errorf("stage %s: on_fail chain ends at %q, want a manual_debug gate", s.ID, e.To)
			continue
		}
		if tgt.OnSuccess != s.ID {
			t.Errorf("stage %s: rescue gate %s resumes %q, must resume the escalating stage %q",
				s.ID, tgt.ID, tgt.OnSuccess, s.ID)
		}
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
	if !strings.Contains(fe.Error(), "bogus_field") || !strings.Contains(fe.Error(), "line 5") {
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
		{"missing name", "defaults: { harness: fake, model: a/b }\nstages:\n  - { id: s, type: command, run: x }\n",
			"flow name is required", 0},
		{"missing harness", "name: t\ndefaults: { model: a/b }\nstages:\n  - { id: s, type: command, run: x }\n",
			"defaults.harness is required", 2},
		{"missing model", "name: t\ndefaults: { harness: fake }\nstages:\n  - { id: s, type: command, run: x }\n",
			"defaults.model is required", 0},
		{"bad model format", "name: t\ndefaults: { harness: fake, model: nomodel }\nstages:\n  - { id: s, type: command, run: x }\n",
			"provider/model", 2},
		{"bad timeout", "name: t\ndefaults: { model: a/b, timeout: soon }\nstages:\n  - { id: s, type: command, run: x }\n",
			"invalid duration", 2},
		{"no stages", header, "at least one stage", 0},
		{"duplicate id", header + "stages:\n  - { id: s, type: command, run: x }\n  - { id: s, type: command, run: y }\n",
			"duplicate id", 5},
		{"non-slug id", header + "stages:\n  - { id: Bad_ID, type: command, run: x }\n",
			"not a slug", 4},
		{"missing type", header + "stages:\n  - { id: s, run: x }\n",
			"type is required", 4},
		{"unknown type", header + "stages:\n  - { id: s, type: robot }\n",
			`unknown type "robot"`, 4},
		{"model on command", header + "stages:\n  - { id: s, type: command, run: x, model: a/b }\n",
			`field "model" does not apply to type command`, 4},
		{"run and run_ref", header + "stages:\n  - { id: s, type: command, run: x, run_ref: y }\n",
			"exactly one of run / run_ref", 4},
		{"neither run nor run_ref", header + "stages:\n  - { id: s, type: command }\n",
			"exactly one of run / run_ref", 4},
		{"agent missing", header + "stages:\n  - { id: s, type: agent }\n",
			"agent is required", 4},
		{"bad input entry", header + "stages:\n  - { id: s, type: agent, agent: a, input: [/etc/passwd] }\n",
			"input entry", 4},
		{"dotdot output", header + "stages:\n  - { id: s, type: agent, agent: a, output: ../out.md }\n",
			"relative artifact path", 4},
		{"missing ping", header + "stages:\n  - { id: s, type: human_gate }\n",
			"ping is required", 4},
		{"unknown ping", header + "stages:\n  - { id: s, type: human_gate, ping: nudge }\n",
			`unknown ping "nudge"`, 4},
		{"unknown auto_complete", header + "stages:\n  - { id: s, type: human_gate, ping: final_review, auto_complete: ci_green }\n",
			"unknown auto_complete", 4},
		{"forge_mr missing body", header + "stages:\n  - { id: s, type: forge_mr }\n",
			"body is required", 4},
		{"edge target missing", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: ghost, max: 2 } }\n",
			`on_fail target "ghost" does not exist`, 4},
		{"edge max missing", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s } }\n",
			"max is required", 4},
		{"edge max zero", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 0 } }\n",
			"max must be >= 1", 4},
		{"bad feed pairing", header + "stages:\n  - { id: a, type: agent, agent: x }\n  - { id: g, type: human_gate, ping: plan_approval, on_reject: { to: a, max: 1, feed: output } }\n",
			`feed "output" is not valid`, 5},
		{"on_reject without verdict", header + "stages:\n  - { id: a, type: agent, agent: x, on_reject: { to: a, max: 1 } }\n",
			"requires verdict: true", 4},
		{"on_fail on human_gate", header + "stages:\n  - { id: g, type: human_gate, ping: plan_approval, on_fail: { to: g, max: 1 } }\n",
			`field "on_fail" does not apply`, 4},
		{"two forge_mr", header + "stages:\n  - { id: a, type: forge_mr, body: artifacts/mr.md }\n  - { id: b, type: forge_mr, body: artifacts/mr.md }\n",
			"at most one forge_mr", 5},
		{"auto_complete not last", header + "stages:\n  - { id: g, type: human_gate, ping: final_review, auto_complete: mr_merged }\n  - { id: s, type: command, run: x }\n",
			"must complete the run", 4},
		{"reserved done id", header + "stages:\n  - { id: done, type: command, run: x }\n",
			"reserved", 4},
		{"reserved abort id", header + "stages:\n  - { id: abort, type: command, run: x }\n",
			"reserved", 4},
		{"reserved start id", header + "stages:\n  - { id: start, type: command, run: x }\n",
			"reserved", 4},
		{"edge to done", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: done, max: 1 } }\n",
			"only on_success may complete the run", 4},
		{"abort edge with max", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: abort, max: 2 } }\n",
			"max does not apply", 4},
		{"abort edge with feed", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: abort, feed: output } }\n",
			"feed does not apply", 4},
		{"abort edge with then", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: abort, then: { to: s, max: 1 } } }\n",
			"then does not apply", 4},
		{"on_success abort", header + "stages:\n  - { id: s, type: command, run: x, on_success: abort }\n",
			`on_success target "abort" does not exist`, 4},
		{"on_timeout on gate", header + "stages:\n  - { id: g, type: human_gate, ping: plan_approval, on_timeout: { to: g, max: 1 } }\n",
			`field "on_timeout" does not apply`, 4},
		{"then target missing", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 1, then: { to: ghost, max: 1 } } }\n",
			`on_fail target "ghost" does not exist`, 4},
		{"then max missing", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 1, then: { to: s } } }\n",
			"then #1", 4},
		{"then bad feed", header + "stages:\n  - { id: a, type: agent, agent: x, verdict: true, on_reject: { to: a, max: 1, then: { to: a, max: 1, feed: output } } }\n",
			`feed "output" is not valid`, 4},
		{"unknown edge field", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: 1, bogus: 1 } }\n",
			"bogus", 0},
		{"unlimited without a gate on the link", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: -1 } }\n",
			"requires a human_gate", 4},
		{"max below -1", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: s, max: -2 } }\n",
			"max must be >= 1", 4},
		{"then after unlimited", header + "stages:\n  - { id: s, type: command, run: x, on_fail: { to: g, max: -1, then: { to: s, max: 1 } } }\n  - { id: g, type: human_gate, ping: plan_approval, on_success: done }\n",
			"unreachable", 4},
		{"on_success bad target", header + "stages:\n  - { id: a, type: command, run: x, on_success: nope }\n",
			`on_success target "nope" does not exist`, 4},
		{"success cycle", header + "stages:\n  - { id: a, type: command, run: x, on_success: b }\n  - { id: b, type: command, run: x, on_success: a }\n",
			"success cycle", 4},
		{"self success cycle", header + "stages:\n  - { id: a, type: command, run: x, on_success: a }\n",
			"success cycle", 4},
		{"unreachable stage", header + "stages:\n  - { id: a, type: command, run: x, on_success: done }\n  - { id: b, type: command, run: x, on_success: done }\n",
			`stage "b" is unreachable`, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, tc.doc, tc.substr, tc.line)
		})
	}
}

func TestPingTypesOnGates(t *testing.T) {
	doc := header + "stages:\n  - { id: g, type: human_gate, ping: plan_approval }\n"
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if f.Stages[0].Ping != PingPlanApproval {
		t.Errorf("ping = %q", f.Stages[0].Ping)
	}

	// manual_debug gate with the workspace payload (SSH-and-inspect stop).
	doc = header + "stages:\n  - { id: g, type: human_gate, ping: manual_debug, payload: workspace }\n"
	f, err = Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if f.Stages[0].Ping != PingManualDebug || f.Stages[0].Payload != PayloadWorkspace {
		t.Errorf("manual_debug gate = %+v", f.Stages[0])
	}
}

// TestAbortAndThenChains covers the abort terminal (map + scalar sugar) and
// retry-then-escalate chains.
func TestAbortAndThenChains(t *testing.T) {
	doc := header + `stages:
  - { id: build, type: agent, agent: b }
  - { id: test,  type: command, run: t,
      on_fail: { to: test, max: 2, feed: output, then: { to: build, max: 3, feed: output } },
      on_timeout: { to: test, max: 1, then: abort } }
  - { id: gate,  type: human_gate, ping: final_review, on_reject: abort }
`
	f, err := Parse("t.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	test := f.Stage("test")
	fe := test.Edge(EdgeFail)
	if fe == nil || fe.To != "test" || fe.Max != 2 || fe.Feed != FeedOutput {
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
  - { id: rescue, type: human_gate, ping: manual_debug, payload: workspace,
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
	gsrc := header + `stages:
  - { id: work, type: agent, agent: a, on_success: gate }
  - { id: gate, type: human_gate, ping: plan_approval, on_success: done,
      on_reject: { to: work, max: -1, feed: feedback } }
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
	// Sugar: absent on_success chains document order; last -> done.
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
