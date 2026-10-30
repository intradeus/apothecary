package runcore

import (
	"testing"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/repoport"
)

const idleFlow = `name: idle
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: command, run: "true" }
  - { id: gate, type: human_gate, question: "Merge?", auto_approve: mr_merged, auto_reject: mr_closed, on_reject: abort }
  - { id: hold, type: wait, for: mr_mergeable, timeout: 1h, on_fail: end, on_timeout: end }
watchers:
  - id: conflicts
    on: { signal: mr_conflicts, start: confirm }
    stages:
      - { id: fix, type: command, run: "true", on_success: end }
  - id: heads
    on: { signal: mr_head_moved, start: manual }
    stages:
      - { id: note, type: command, run: "true", on_success: end }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`

func idleState(t *testing.T) *State {
	t.Helper()
	rs := NewState(mustFlow(t, idleFlow))
	rs.RunID = "r"
	rs.Idle = true
	rs.MRs = []MRRecord{{Repo: "repo", Branch: "b", Ref: repoport.MRRef{URL: "http://mr"}}}
	return rs
}

func open(rs *State, mergeable repoport.Mergeable) []MRObservation {
	return []MRObservation{{MR: &rs.MRs[0], Status: repoport.MRStatus{State: repoport.MROpen, HeadSHA: "h", TargetSHA: "t", Mergeable: mergeable}}}
}

func kinds(p IdlePlan) []IdleActionKind {
	out := []IdleActionKind{}
	for _, a := range p.Actions {
		out = append(out, a.Kind)
	}
	return out
}

func sameKinds(a, b []IdleActionKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPlanIdleAsksOnceForAnOccurrence(t *testing.T) {
	now := time.Unix(1000, 0)
	rs := idleState(t)
	plan := rs.PlanIdle(open(rs, repoport.MergeConflicts), now)
	if !sameKinds(kinds(plan), []IdleActionKind{IdleAsk}) {
		t.Fatalf("actions = %v, want one ask", kinds(plan))
	}
	a := plan.Actions[0]
	if a.Phase.ID != "conflicts" || a.Subject != "repo" || a.Key != "t..h" {
		t.Fatalf("ask = %+v", a)
	}
	hs := plan.Holds["conflicts"]
	if len(hs) != 1 || hs[0].Seen || hs[0].Subject != "repo" || !hs[0].At.Equal(now) {
		t.Fatalf("holds = %+v", hs)
	}

	// The question open, the next tick does not ask again — and the run is
	// not over: its answer is still owed.
	rs.Confirms = []*event.PingRaised{{PingID: "p", Phase: "conflicts", Subject: "repo", TriggerKey: "t..h"}}
	if plan := rs.PlanIdle(open(rs, repoport.MergeConflicts), now); len(plan.Actions) != 0 {
		t.Fatalf("actions = %v, want none", kinds(plan))
	}
}

func TestPlanIdleConfirmationAnswers(t *testing.T) {
	now := time.Unix(1000, 0)
	rs := idleState(t)
	rs.Confirms = []*event.PingRaised{
		{PingID: "no", Phase: "conflicts", Subject: "repo", TriggerKey: "old"},
		{PingID: "dup", Phase: "conflicts", Subject: "repo", TriggerKey: "old"},
		{PingID: "yes", Phase: "conflicts", Subject: "repo", TriggerKey: "t..h"},
		{PingID: "open", Phase: "conflicts", Subject: "other", TriggerKey: "k"},
	}
	rs.AnsweredPings["no"] = &event.PingAnswered{PingID: "no", Answer: "reject"}
	rs.AnsweredPings["dup"] = &event.PingAnswered{PingID: "dup", Answer: "approve"}
	rs.AnsweredPings["yes"] = &event.PingAnswered{PingID: "yes", Answer: "approve"}

	// The first answer declines the occurrence, so the second one about it
	// is spent; the third starts, and the tick ends before any hold is read.
	plan := rs.PlanIdle(open(rs, repoport.MergeConflicts), now)
	if !sameKinds(kinds(plan), []IdleActionKind{IdleDecline, IdleBegin}) {
		t.Fatalf("actions = %v, want decline then begin", kinds(plan))
	}
	if b := plan.Actions[1]; b.Key != "t..h" || b.Tip != "h" || b.Phase.ID != "conflicts" {
		t.Fatalf("begin = %+v", b)
	}
	if plan.Holds != nil {
		t.Fatalf("holds = %v, want the last tick's to stand", plan.Holds)
	}
}

func TestPlanIdleADeclineThisTickSilencesItsOccurrence(t *testing.T) {
	now := time.Unix(1000, 0)
	rs := idleState(t)
	rs.Confirms = []*event.PingRaised{{PingID: "no", Phase: "conflicts", Subject: "repo", TriggerKey: "t..h"}}
	rs.AnsweredPings["no"] = &event.PingAnswered{PingID: "no", Answer: "reject"}
	plan := rs.PlanIdle(open(rs, repoport.MergeConflicts), now)
	if !sameKinds(kinds(plan), []IdleActionKind{IdleDecline}) {
		t.Fatalf("actions = %v, want the decline alone", kinds(plan))
	}
	if hs := plan.Holds["conflicts"]; len(hs) != 1 || !hs[0].Seen {
		t.Fatalf("holds = %+v, want the occurrence seen", hs)
	}
}

func TestPlanIdleEndsARunWithNothingLeft(t *testing.T) {
	now := time.Unix(1000, 0)
	rs := idleState(t)
	if plan := rs.PlanIdle(nil, now); !sameKinds(kinds(plan), []IdleActionKind{IdleComplete}) {
		t.Fatalf("actions = %v, want the run's end", kinds(plan))
	}
	// A merge request still open keeps the watchers armed.
	if plan := rs.PlanIdle(open(rs, repoport.MergeClean), now); len(plan.Actions) != 0 {
		t.Fatalf("actions = %v, want none", kinds(plan))
	}
	// A parked phase keeps the run alive with nothing to observe.
	rs.Parked = map[string]*PhaseRun{"conflicts": {Phase: "conflicts", Pos: rs.Flow.Index("fix"), ParkedAt: "fix"}}
	if plan := rs.PlanIdle(nil, now); len(plan.Actions) != 0 {
		t.Fatalf("actions = %v, want none", kinds(plan))
	}
}

func TestPlanResume(t *testing.T) {
	t0 := time.Unix(1000, 0)
	merged := func(rs *State) []MRObservation {
		return []MRObservation{{MR: &rs.MRs[0], Status: repoport.MRStatus{State: repoport.MRMerged}}}
	}
	closed := func(rs *State) []MRObservation {
		return []MRObservation{{MR: &rs.MRs[0], Status: repoport.MRStatus{State: repoport.MRClosed}}}
	}
	mergeable := func(rs *State) []MRObservation {
		return []MRObservation{{MR: &rs.MRs[0], Status: repoport.MRStatus{State: repoport.MROpen, MergeableNow: true}}}
	}
	tests := []struct {
		name   string
		stage  string
		obs    func(*State) []MRObservation
		now    time.Time
		answer string
		want   ResumeKind
		auto   string
	}{
		{name: "wait not yet", stage: "hold", obs: func(rs *State) []MRObservation { return open(rs, repoport.MergeClean) }, now: t0, want: ResumeNone},
		{name: "wait holds", stage: "hold", obs: mergeable, now: t0, want: ResumeWaitHolds},
		{name: "wait past it", stage: "hold", obs: merged, now: t0, want: ResumeWaitHolds},
		{name: "wait impossible", stage: "hold", obs: closed, now: t0, want: ResumeWaitImpossible},
		{name: "wait timed out", stage: "hold", obs: func(rs *State) []MRObservation { return open(rs, repoport.MergeClean) }, now: t0.Add(time.Hour), want: ResumeWaitTimedOut},
		{name: "gate open", stage: "gate", obs: func(rs *State) []MRObservation { return open(rs, repoport.MergeClean) }, now: t0, want: ResumeNone},
		{name: "gate answered", stage: "gate", obs: merged, now: t0, answer: "reject", want: ResumeGateAnswered},
		{name: "gate merged instead", stage: "gate", obs: merged, now: t0, want: ResumeGateAuto, auto: "approve"},
		{name: "gate closed instead", stage: "gate", obs: closed, now: t0, want: ResumeGateAuto, auto: "reject"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := idleState(t)
			pr := &PhaseRun{Phase: flow.MainPhase, Pos: rs.Flow.Index(tt.stage), Ping: "p", Since: t0}
			if tt.answer != "" {
				rs.AnsweredPings["p"] = &event.PingAnswered{PingID: "p", Answer: tt.answer}
			}
			got := rs.PlanResume(pr, tt.obs(rs), tt.now)
			if got.Kind != tt.want || got.AutoAnswer != tt.auto {
				t.Fatalf("got %+v, want kind %v auto %q", got, tt.want, tt.auto)
			}
		})
	}
}
