package runcore

import (
	"strings"
	"testing"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/run/event"
)

const idleFlow = `name: idle
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: command, run: "true" }
  - { id: gate, type: human_gate, question: "Merge?", on_reject: abort }
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

const planOutcomeFlow = `name: outcome
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: command, run: "true" }
  - { id: act, type: action, action: mr_merge, run_if: run.subject }
  - { id: hold, type: wait, for: mr_merged, on_fail: end }
watchers:
  - id: conflicts
    on: mr_conflicts
    stages:
      - { id: fix, type: command, run: "true" }
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
	// A parked chain keeps the run alive with nothing to observe.
	rs.Pos, rs.Parked = rs.Flow.Index("fix"), &ParkPoint{Stage: "fix"}
	if plan := rs.PlanIdle(nil, now); len(plan.Actions) != 0 {
		t.Fatalf("actions = %v, want none", kinds(plan))
	}
}

// One cursor: a parked chain holds it. No watcher opens, by its signal or by
// an answered confirmation, and none is asked about; what holds is still
// shown, and the answer waits for the chain to end.
func TestPlanIdleAParkedChainHoldsTheCursor(t *testing.T) {
	now := time.Unix(1000, 0)
	rs := idleState(t)
	rs.Phase, rs.Pos, rs.Parked = flow.MainPhase, rs.Flow.Index("gate"), &ParkPoint{Stage: "gate", Ping: "gate"}
	plan := rs.PlanIdle(open(rs, repoport.MergeConflicts), now)
	if len(plan.Actions) != 0 {
		t.Fatalf("actions = %v, want none: the signal must not ask while a chain holds the cursor", kinds(plan))
	}
	if hs := plan.Holds["conflicts"]; len(hs) != 1 || hs[0].Seen {
		t.Fatalf("holds = %+v, want the signal shown", hs)
	}

	rs.Confirms = []*event.PingRaised{{PingID: "yes", Phase: "conflicts", Subject: "repo", TriggerKey: "t..h"}}
	rs.AnsweredPings["yes"] = &event.PingAnswered{PingID: "yes", Answer: "approve"}
	if plan := rs.PlanIdle(open(rs, repoport.MergeConflicts), now); len(plan.Actions) != 0 {
		t.Fatalf("actions = %v, want the approved watcher to wait for the cursor", kinds(plan))
	}
	rs.Parked = nil
	if plan := rs.PlanIdle(open(rs, repoport.MergeConflicts), now); !sameKinds(kinds(plan), []IdleActionKind{IdleBegin}) {
		t.Fatalf("actions = %v, want it to begin once the cursor is back", kinds(plan))
	}
}

// An outcome is the run's end, so it may take the cursor from a parked chain,
// but a wait point its signal settles comes first.
func TestPlanIdleAnOutcomeTakesTheCursor(t *testing.T) {
	now := time.Unix(1000, 0)
	rs := idleState(t)
	merged := []MRObservation{{MR: &rs.MRs[0], Status: repoport.MRStatus{State: repoport.MRMerged}}}

	rs.Phase, rs.Pos, rs.Parked = "conflicts", rs.Flow.Index("fix"), &ParkPoint{Stage: "fix"}
	plan := rs.PlanIdle(merged, now)
	if !sameKinds(kinds(plan), []IdleActionKind{IdleBegin}) || plan.Actions[0].Phase.ID != "shipped" {
		t.Fatalf("actions = %+v, want the outcome", plan.Actions)
	}

	rs.Phase, rs.Pos, rs.Parked = flow.MainPhase, rs.Flow.Index("hold"), &ParkPoint{Stage: "hold"}
	if plan := rs.PlanIdle(merged, now); !sameKinds(kinds(plan), []IdleActionKind{IdleResume}) {
		t.Fatalf("actions = %v, want the wait settled first", kinds(plan))
	}
}

// What may stop a run mid-chain, and what an outcome leaves alone.
func TestPlanOutcome(t *testing.T) {
	now := time.Unix(1000, 0)
	merged := func(rs *State) []MRObservation {
		return []MRObservation{{MR: &rs.MRs[0], Status: repoport.MRStatus{State: repoport.MRMerged}}}
	}
	tests := []struct {
		name    string
		stage   string
		phase   string
		dormant bool
		set     func(*State)
		obs     func(*State) []MRObservation
		fires   bool
	}{
		{name: "a running command", stage: "build", phase: flow.MainPhase, obs: merged, fires: true},
		{name: "no fate yet", stage: "build", phase: flow.MainPhase, obs: func(rs *State) []MRObservation { return open(rs, repoport.MergeClean) }},
		{name: "a take-over", stage: "build", phase: flow.MainPhase, obs: merged, set: func(rs *State) { rs.Holds["build"] = &HeldState{} }},
		{name: "the chain's own merge", stage: "fix", phase: "conflicts", obs: merged, set: func(rs *State) { rs.ChainMerged = true }},
		{name: "the run's end under way", stage: "fix", phase: "shipped", obs: merged},
		{name: "already fired", stage: "build", phase: flow.MainPhase, obs: merged, set: func(rs *State) { rs.TriggerSeen[TriggerKey("shipped", "", "mr_merged")] = true }},
		{name: "a live action", stage: "act", phase: flow.MainPhase, obs: merged},
		{name: "a dormant action", stage: "act", phase: flow.MainPhase, obs: merged, dormant: true, fires: true},
		{name: "a wait it settles", stage: "hold", phase: flow.MainPhase, obs: merged, set: func(rs *State) { rs.Parked = &ParkPoint{Stage: "hold"} }},
		{name: "done", stage: "build", phase: flow.MainPhase, obs: merged, set: func(rs *State) { rs.Done = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := NewState(mustFlow(t, planOutcomeFlow))
			rs.MRs = []MRRecord{{Repo: "repo", Branch: "b", Ref: repoport.MRRef{URL: "http://mr"}}}
			rs.Phase, rs.Pos, rs.Ph = tt.phase, rs.Flow.Index(tt.stage), PhIn
			if tt.set != nil {
				tt.set(rs)
			}
			got := rs.PlanOutcome(tt.obs(rs), now, tt.dormant)
			if (got != nil) != tt.fires {
				t.Fatalf("got %+v, want fires = %v", got, tt.fires)
			}
			if got != nil && got.Phase.ID != "shipped" {
				t.Fatalf("got %+v", got)
			}
		})
	}
	// An outcome that asks first waits for the hub, where it can.
	rs := NewState(mustFlow(t, strings.Replace(planOutcomeFlow, "{ id: shipped, on: mr_merged, then: done }", "{ id: shipped, on: { signal: mr_merged, start: confirm }, then: done }", 1)))
	rs.MRs = []MRRecord{{Repo: "repo", Branch: "b", Ref: repoport.MRRef{URL: "http://mr"}}}
	rs.Phase, rs.Pos, rs.Ph = flow.MainPhase, rs.Flow.Index("build"), PhIn
	if got := rs.PlanOutcome(merged(rs), now, false); got != nil {
		t.Fatalf("a confirmed outcome fired: %+v", got)
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
	}{
		{name: "wait not yet", stage: "hold", obs: func(rs *State) []MRObservation { return open(rs, repoport.MergeClean) }, now: t0, want: ResumeNone},
		{name: "wait holds", stage: "hold", obs: mergeable, now: t0, want: ResumeWaitHolds},
		{name: "wait past it", stage: "hold", obs: merged, now: t0, want: ResumeWaitHolds},
		{name: "wait impossible", stage: "hold", obs: closed, now: t0, want: ResumeWaitImpossible},
		{name: "wait timed out", stage: "hold", obs: func(rs *State) []MRObservation { return open(rs, repoport.MergeClean) }, now: t0.Add(time.Hour), want: ResumeWaitTimedOut},
		{name: "gate open", stage: "gate", obs: func(rs *State) []MRObservation { return open(rs, repoport.MergeClean) }, now: t0, want: ResumeNone},
		{name: "gate answered", stage: "gate", obs: merged, now: t0, answer: "reject", want: ResumeGateAnswered},
		{name: "gate merged instead", stage: "gate", obs: merged, now: t0, want: ResumeNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := idleState(t)
			rs.Phase, rs.Pos, rs.Parked = flow.MainPhase, rs.Flow.Index(tt.stage), &ParkPoint{Stage: tt.stage, Ping: "p", Since: t0}
			if tt.answer != "" {
				rs.AnsweredPings["p"] = &event.PingAnswered{PingID: "p", Answer: tt.answer}
			}
			got := rs.PlanResume(tt.obs(rs), tt.now)
			if got.Kind != tt.want {
				t.Fatalf("got %+v, want kind %v", got, tt.want)
			}
		})
	}
}
