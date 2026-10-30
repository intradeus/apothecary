package engine

// The interpreter's pure decisions (routing.go), table by table: no harness,
// no store — a parsed flow and a state set by hand.

import (
	"strings"
	"testing"

	"apothecary/internal/event"
	"apothecary/internal/flow"
)

const routingFlowDoc = `name: routing
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: command, run: "true", on_fail: { to: fix, max: 2, then: { to: debug } } }
  - { id: fix,   type: command, run: "true", on_success: gate, on_fail: { to: build, max: 1 } }
  - { id: gate,  type: human_gate, question: "ok?", on_reject: abort, on_success: post }
  - { id: post,  type: action, action: ticket_comment, input: { body: { from: repo:artifacts/note.md, type: file } } }
  - { id: hot,   type: command, run: "true", run_if: run.ticket.change_type == hotfix, on_fail: end, on_success: done }
`

func routingState(t *testing.T) *runState {
	t.Helper()
	return newRunState(mustFlow(t, routingFlowDoc))
}

func TestRouteSuccess(t *testing.T) {
	cases := []struct {
		stage, verdict string
		pos            string // stage the cursor lands on; "" = past the chain
		endsRun        bool
		sentinel       string
	}{
		{stage: "fix", pos: "gate"},
		{stage: "gate", verdict: "approved: ship it", pos: "post", sentinel: flow.SourceVerdict},
		{stage: "hot", endsRun: true},
	}
	for _, c := range cases {
		t.Run(c.stage, func(t *testing.T) {
			rs := routingState(t)
			rs.ph, rs.feed, rs.feedAttempts = phAfterComplete, "stale", 3
			rs.routeSuccess(&rs.fl.Stages[rs.fl.Index(c.stage)], c.verdict)
			want := len(rs.fl.Stages)
			if c.pos != "" {
				want = rs.fl.Index(c.pos)
			}
			if rs.pos != want || rs.ph != phStart || rs.endsRun != c.endsRun || rs.abortsRun {
				t.Errorf("pos %d ph %v endsRun %v abortsRun %v, want pos %d endsRun %v", rs.pos, rs.ph, rs.endsRun, rs.abortsRun, want, c.endsRun)
			}
			if rs.feed != c.verdict || rs.feedSentinel != c.sentinel || rs.feedAttempts != 0 {
				t.Errorf("feed %q sentinel %q attempts %d, want %q %q 0", rs.feed, rs.feedSentinel, rs.feedAttempts, c.verdict, c.sentinel)
			}
		})
	}
}

func TestEndPhase(t *testing.T) {
	rs := routingState(t)
	rs.pos, rs.ph, rs.endsRun = 1, phIn, true
	rs.endPhase()
	if rs.pos != len(rs.fl.Stages) || rs.ph != phStart || rs.endsRun || rs.abortsRun {
		t.Errorf("pos %d ph %v endsRun %v abortsRun %v", rs.pos, rs.ph, rs.endsRun, rs.abortsRun)
	}
}

func TestBudgetExceeded(t *testing.T) {
	b := flow.Budget{MaxCostUSD: 2, MaxTotalTurns: 10}
	cases := []struct {
		cost  float64
		turns int
		over  bool
		says  string
	}{
		{cost: 1, turns: 5},
		{cost: 2, turns: 10}, // the ceiling itself is within budget
		{cost: 2.5, turns: 5, over: true, says: "cost 2.50 USD > max 2.00"},
		{cost: 1, turns: 11, over: true, says: "11 turns > max 10"},
		{cost: 3, turns: 11, over: true, says: "cost"}, // cost is reported first
	}
	for _, c := range cases {
		over, reason := budgetExceeded(b, c.cost, c.turns)
		if over != c.over || !strings.Contains(reason, c.says) || (!c.over && reason != "") {
			t.Errorf("budgetExceeded(%v, %d) = %v %q, want %v containing %q", c.cost, c.turns, over, reason, c.over, c.says)
		}
	}
}

func TestChooseEdge(t *testing.T) {
	cases := []struct {
		name   string
		stage  string
		kind   string
		counts map[string]int
		want   edgeChoiceKind
		to     string
		depth  int
		count  int
	}{
		{name: "no such edge", stage: "fix", kind: flow.EdgeReject, want: edgeNone},
		{name: "first retry", stage: "build", kind: flow.EdgeFail, want: edgeLink, to: "fix", count: 1},
		{name: "last retry", stage: "build", kind: flow.EdgeFail, counts: map[string]int{chainKey("build", flow.EdgeFail, 0): 1}, want: edgeLink, to: "fix", count: 2},
		{name: "full link falls through to debug", stage: "build", kind: flow.EdgeFail,
			counts: map[string]int{chainKey("build", flow.EdgeFail, 0): 2}, want: edgeDebug, to: flow.ReservedDebug, depth: 1, count: 1},
		{name: "debug never exhausts", stage: "build", kind: flow.EdgeFail,
			counts: map[string]int{chainKey("build", flow.EdgeFail, 0): 2, chainKey("build", flow.EdgeFail, 1): 40}, want: edgeDebug, to: flow.ReservedDebug, depth: 1, count: 41},
		{name: "exhausted", stage: "fix", kind: flow.EdgeFail, counts: map[string]int{chainKey("fix", flow.EdgeFail, 0): 1}, want: edgeExhausted, to: "build"},
		{name: "abort", stage: "gate", kind: flow.EdgeReject, want: edgeAbort, to: flow.TerminalAbort},
		{name: "end", stage: "hot", kind: flow.EdgeFail, want: edgeEnd, to: flow.TerminalEnd},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := routingState(t)
			for k, n := range c.counts {
				rs.edgeCounts[k] = n
			}
			got := rs.chooseEdge(&rs.fl.Stages[rs.fl.Index(c.stage)], c.kind)
			if got.kind != c.want || got.depth != c.depth || got.count != c.count {
				t.Fatalf("got kind %d depth %d count %d, want %d %d %d", got.kind, got.depth, got.count, c.want, c.depth, c.count)
			}
			if c.want != edgeNone && (got.edge == nil || got.edge.To != c.to) {
				t.Errorf("edge %+v, want to %q", got.edge, c.to)
			}
		})
	}
}

func TestStageCondition(t *testing.T) {
	cases := []struct {
		name    string
		stage   string
		tracker bool
		change  string
		skip    bool
		says    string
	}{
		{name: "no condition", stage: "build"},
		{name: "run_if holds", stage: "hot", change: "hotfix"},
		{name: "run_if does not hold", stage: "hot", change: "feat", skip: true, says: "run_if"},
		{name: "tracker action with an issue", stage: "post", tracker: true},
		{name: "tracker action on a ticket written by hand", stage: "post", skip: true, says: "no tracker issue"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := routingState(t)
			rs.view.TicketChangeType = c.change
			if c.tracker {
				rs.view.TicketSourceID, rs.view.TicketKey = "jira", "APO-1"
			}
			skip, note := rs.stageCondition(&rs.fl.Stages[rs.fl.Index(c.stage)])
			if skip != c.skip || !strings.Contains(note, c.says) {
				t.Errorf("skip %v note %q, want %v containing %q", skip, note, c.skip, c.says)
			}
		})
	}
}

func TestNextStep(t *testing.T) {
	cases := []struct {
		name  string
		set   func(rs *runState)
		want  step
		stage string
	}{
		{name: "idle", set: func(rs *runState) { rs.idle = true }, want: stepIdle},
		{name: "phase gone", set: func(rs *runState) { rs.pos = -1 }, want: stepGone},
		{name: "chain over", set: func(rs *runState) { rs.pos = len(rs.fl.Stages) }, want: stepChainOver},
		{name: "debug leaf", set: func(rs *runState) { rs.ph = phDebug }, want: stepDebug, stage: "build"},
		{name: "completed, unrouted", set: func(rs *runState) { rs.pos, rs.ph = rs.fl.Index("gate"), phAfterComplete }, want: stepRouteComplete, stage: "gate"},
		{name: "failed, unrouted", set: func(rs *runState) { rs.ph = phAfterFail }, want: stepRouteFail, stage: "build"},
		{name: "failed action re-enters its executor", set: func(rs *runState) {
			rs.pos, rs.ph = rs.fl.Index("post"), phAfterFail
			rs.view.TicketSourceID, rs.view.TicketKey = "jira", "APO-1"
		}, want: stepRun, stage: "post"},
		{name: "condition does not hold", set: func(rs *runState) { rs.pos = rs.fl.Index("hot") }, want: stepSkip, stage: "hot"},
		{name: "condition is not re-read mid-stage", set: func(rs *runState) { rs.pos, rs.ph = rs.fl.Index("hot"), phIn }, want: stepRun, stage: "hot"},
		{name: "run", want: stepRun, stage: "build"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := routingState(t)
			if c.set != nil {
				c.set(rs)
			}
			got, st, _ := rs.nextStep()
			if got != c.want {
				t.Fatalf("step %d, want %d", got, c.want)
			}
			if id := stageID(st); id != c.stage {
				t.Errorf("stage %q, want %q", id, c.stage)
			}
		})
	}
}

func stageID(st *flow.Stage) string {
	if st == nil {
		return ""
	}
	return st.ID
}

// The subject's merge request is what a condition on run.subject reads.
func TestSubjectMR(t *testing.T) {
	rs := routingState(t)
	if rs.subjectCheckout() != nil || rs.subjectMR() != nil {
		t.Fatal("a ticket run's main chain has no subject")
	}
	rs.apply(&event.Event{Type: event.TypeRunCreated, Payload: &event.RunCreated{FlowName: "routing", Branch: "feat/x"}})
	rs.apply(&event.Event{Type: event.TypeMRCreated, Payload: &event.MRCreated{Repo: event.PrimaryCheckoutName, Branch: "feat/x", Target: "main", MRURL: "https://x/1", MRIID: 1}})
	rs.subject = event.PrimaryCheckoutName
	if mr := rs.subjectMR(); mr == nil || mr.Ref.IID != 1 {
		t.Errorf("subject MR = %+v, want the one created on the run's branch", mr)
	}
}
