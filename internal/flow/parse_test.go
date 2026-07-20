package flow

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// header is a minimal valid preamble; tests append stages to it.
const header = `name: t
defaults: { model: anthropic/claude-sonnet-4-6 }
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
		f.Defaults.Harness != "" || f.Defaults.Model != "anthropic/claude-sonnet-4-6" {
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
	if !review.Verdict || review.OnReject == nil || review.OnReject.To != "build" ||
		review.OnReject.Max != 3 || review.OnReject.Feed != FeedComments {
		t.Errorf("review verdict/edge = %+v %+v", review.Verdict, review.OnReject)
	}

	test := f.Stage("test")
	if test.RunRef != "verify" || test.OnFail == nil || test.OnFail.To != "build" ||
		test.OnFail.Max != 5 || test.OnFail.Feed != FeedOutput {
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
		{"missing name", "defaults: { model: a/b }\nstages:\n  - { id: s, type: command, run: x }\n",
			"flow name is required", 0},
		{"missing model", "name: t\nstages:\n  - { id: s, type: command, run: x }\n",
			"defaults.model is required", 0},
		{"bad model format", "name: t\ndefaults: { model: nomodel }\nstages:\n  - { id: s, type: command, run: x }\n",
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
			`edge target "ghost" does not exist`, 4},
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
			"must be the last stage", 4},
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
}
