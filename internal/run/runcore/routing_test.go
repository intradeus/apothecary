package runcore

// The interpreter's pure decisions (routing.go), table by table: no harness,
// no store — a parsed flow and a state set by hand.

import (
	"strings"
	"testing"

	"apothecary/internal/flow"
	"apothecary/internal/run/event"
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

func routingState(t *testing.T) *State {
	t.Helper()
	return NewState(mustFlow(t, routingFlowDoc))
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
			rs.Ph, rs.Feed, rs.FeedAttempts = PhAfterComplete, "stale", 3
			rs.RouteSuccess(&rs.Flow.Stages[rs.Flow.Index(c.stage)], c.verdict)
			want := len(rs.Flow.Stages)
			if c.pos != "" {
				want = rs.Flow.Index(c.pos)
			}
			if rs.Pos != want || rs.Ph != PhStart || rs.EndsRun != c.endsRun || rs.AbortsRun {
				t.Errorf("pos %d ph %v endsRun %v abortsRun %v, want pos %d endsRun %v", rs.Pos, rs.Ph, rs.EndsRun, rs.AbortsRun, want, c.endsRun)
			}
			if rs.Feed != c.verdict || rs.FeedSentinel != c.sentinel || rs.FeedAttempts != 0 {
				t.Errorf("feed %q sentinel %q attempts %d, want %q %q 0", rs.Feed, rs.FeedSentinel, rs.FeedAttempts, c.verdict, c.sentinel)
			}
		})
	}
}

func TestEndPhase(t *testing.T) {
	rs := routingState(t)
	rs.Pos, rs.Ph, rs.EndsRun = 1, PhIn, true
	rs.EndPhase()
	if rs.Pos != len(rs.Flow.Stages) || rs.Ph != PhStart || rs.EndsRun || rs.AbortsRun {
		t.Errorf("pos %d ph %v endsRun %v abortsRun %v", rs.Pos, rs.Ph, rs.EndsRun, rs.AbortsRun)
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
		over, reason := BudgetExceeded(b, c.cost, c.turns)
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
		want   EdgeChoiceKind
		to     string
		depth  int
		count  int
	}{
		{name: "no such edge", stage: "fix", kind: flow.EdgeReject, want: EdgeNone},
		{name: "first retry", stage: "build", kind: flow.EdgeFail, want: EdgeLink, to: "fix", count: 1},
		{name: "last retry", stage: "build", kind: flow.EdgeFail, counts: map[string]int{ChainKey("build", flow.EdgeFail, 0): 1}, want: EdgeLink, to: "fix", count: 2},
		{name: "full link falls through to debug", stage: "build", kind: flow.EdgeFail,
			counts: map[string]int{ChainKey("build", flow.EdgeFail, 0): 2}, want: EdgeDebug, to: flow.ReservedDebug, depth: 1, count: 1},
		{name: "debug never exhausts", stage: "build", kind: flow.EdgeFail,
			counts: map[string]int{ChainKey("build", flow.EdgeFail, 0): 2, ChainKey("build", flow.EdgeFail, 1): 40}, want: EdgeDebug, to: flow.ReservedDebug, depth: 1, count: 41},
		{name: "exhausted", stage: "fix", kind: flow.EdgeFail, counts: map[string]int{ChainKey("fix", flow.EdgeFail, 0): 1}, want: EdgeExhausted, to: "build"},
		{name: "abort", stage: "gate", kind: flow.EdgeReject, want: EdgeAbort, to: flow.TerminalAbort},
		{name: "end", stage: "hot", kind: flow.EdgeFail, want: EdgeEnd, to: flow.TerminalEnd},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := routingState(t)
			for k, n := range c.counts {
				rs.EdgeCounts[k] = n
			}
			got := rs.ChooseEdge(&rs.Flow.Stages[rs.Flow.Index(c.stage)], c.kind)
			if got.Kind != c.want || got.Depth != c.depth || got.Count != c.count {
				t.Fatalf("got kind %d depth %d count %d, want %d %d %d", got.Kind, got.Depth, got.Count, c.want, c.depth, c.count)
			}
			if c.want != EdgeNone && (got.Edge == nil || got.Edge.To != c.to) {
				t.Errorf("edge %+v, want to %q", got.Edge, c.to)
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
			rs.View.TicketChangeType = c.change
			if c.tracker {
				rs.View.TicketSourceID, rs.View.TicketKey = "jira", "APO-1"
			}
			skip, note := rs.StageCondition(&rs.Flow.Stages[rs.Flow.Index(c.stage)])
			if skip != c.skip || !strings.Contains(note, c.says) {
				t.Errorf("skip %v note %q, want %v containing %q", skip, note, c.skip, c.says)
			}
		})
	}
}

func TestNextStep(t *testing.T) {
	cases := []struct {
		name  string
		set   func(rs *State)
		want  Step
		stage string
	}{
		{name: "idle", set: func(rs *State) { rs.Idle = true }, want: StepIdle},
		{name: "phase gone", set: func(rs *State) { rs.Pos = -1 }, want: StepGone},
		{name: "chain over", set: func(rs *State) { rs.Pos = len(rs.Flow.Stages) }, want: StepChainOver},
		{name: "debug leaf", set: func(rs *State) { rs.Ph = PhDebug }, want: StepDebug, stage: "build"},
		{name: "completed, unrouted", set: func(rs *State) { rs.Pos, rs.Ph = rs.Flow.Index("gate"), PhAfterComplete }, want: StepRouteComplete, stage: "gate"},
		{name: "failed, unrouted", set: func(rs *State) { rs.Ph = PhAfterFail }, want: StepRouteFail, stage: "build"},
		{name: "failed action re-enters its executor", set: func(rs *State) {
			rs.Pos, rs.Ph = rs.Flow.Index("post"), PhAfterFail
			rs.View.TicketSourceID, rs.View.TicketKey = "jira", "APO-1"
		}, want: StepRun, stage: "post"},
		{name: "condition does not hold", set: func(rs *State) { rs.Pos = rs.Flow.Index("hot") }, want: StepSkip, stage: "hot"},
		{name: "condition is not re-read mid-stage", set: func(rs *State) { rs.Pos, rs.Ph = rs.Flow.Index("hot"), PhIn }, want: StepRun, stage: "hot"},
		{name: "run", want: StepRun, stage: "build"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := routingState(t)
			if c.set != nil {
				c.set(rs)
			}
			got, st, _ := rs.NextStep()
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
	if rs.SubjectCheckout() != nil || rs.SubjectMR() != nil {
		t.Fatal("a ticket run's main chain has no subject")
	}
	rs.Apply(&event.Event{Type: event.TypeRunCreated, Payload: &event.RunCreated{FlowName: "routing", Branch: "feat/x"}})
	rs.Apply(&event.Event{Type: event.TypeMRCreated, Payload: &event.MRCreated{Repo: event.PrimaryCheckoutName, Branch: "feat/x", Target: "main", MRURL: "https://x/1", MRIID: 1}})
	rs.Subject = event.PrimaryCheckoutName
	if mr := rs.SubjectMR(); mr == nil || mr.Ref.IID != 1 {
		t.Errorf("subject MR = %+v, want the one created on the run's branch", mr)
	}
}
