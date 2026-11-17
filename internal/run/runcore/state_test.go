package runcore

// Replay of the per-phase-run state: a phase run parked at a wait point keeps
// its cursor while another phase runs, and gets it back when it resumes.

import (
	"testing"

	"apothecary/internal/flow"
	"apothecary/internal/run/event"
)

const parkFlowDoc = `name: park
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: command, run: "true" }
  - { id: mr, type: action, action: create_mr, input: { body: { from: repo:artifacts/mr.md, type: file } } }
  - { id: merged, type: wait, for: mr_merged }
  - { id: after, type: command, run: "true", on_success: done }
watchers:
  - id: conflicts
    on: mr_conflicts
    stages:
      - { id: sync, type: action, action: sync_target }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`

func TestReplayParksAndResumes(t *testing.T) {
	fl := mustFlow(t, parkFlowDoc)
	ev := func(p event.Payload) event.Event { return event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
	evs := []event.Event{
		ev(&event.RunCreated{TicketTitle: "t", FlowName: "park", By: "human"}),
		ev(&event.PhaseStarted{Phase: flow.MainPhase, By: "human"}),
		ev(&event.StageStarted{StageID: "build", Attempt: 1}),
		ev(&event.StageCompleted{StageID: "build"}),
		ev(&event.StageStarted{StageID: "mr", Attempt: 1}),
		ev(&event.MRCreated{Repo: "repo", Branch: "b", Target: "main", MRURL: "https://x/1", MRIID: 1}),
		ev(&event.StageCompleted{StageID: "mr"}),
		ev(&event.StageStarted{StageID: "merged", Attempt: 1}),
		ev(&event.PhaseParked{Phase: flow.MainPhase, StageID: "merged"}),
	}
	rs := Replay(evs, fl)
	if !rs.Idle || rs.Parked == nil || rs.Parked.Stage != "merged" || rs.Phase != flow.MainPhase || rs.Pos != fl.Index("merged") {
		t.Fatalf("after park: idle %v parked %+v", rs.Idle, rs.Parked)
	}

	evs = append(evs,
		ev(&event.PhaseResumed{Phase: flow.MainPhase, StageID: "merged"}),
		ev(&event.StageCompleted{StageID: "merged"}),
	)
	rs = Replay(evs, fl)
	if rs.Idle || rs.Phase != flow.MainPhase || rs.Pos != fl.Index("merged") || rs.Ph != PhAfterComplete || rs.Parked != nil {
		t.Fatalf("after resume: idle %v phase %q pos %d ph %v parked %+v", rs.Idle, rs.Phase, rs.Pos, rs.Ph, rs.Parked)
	}
}

// One cursor: an outcome taking it from a parked chain ends that chain, and
// nothing is left to resume.
func TestAnOutcomeTakesTheCursorFromAParkedChain(t *testing.T) {
	fl := mustFlow(t, parkFlowDoc)
	ev := func(p event.Payload) event.Event { return event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
	rs := Replay([]event.Event{
		ev(&event.RunCreated{TicketTitle: "t", FlowName: "park", By: "human"}),
		ev(&event.PhaseStarted{Phase: flow.MainPhase, By: "human"}),
		ev(&event.StageStarted{StageID: "merged", Attempt: 1}),
		ev(&event.PhaseParked{Phase: flow.MainPhase, StageID: "merged"}),
		ev(&event.PhaseStarted{Phase: "shipped", Subject: "repo", Key: "k", By: "signal"}),
	}, fl)
	if rs.Parked != nil || rs.Idle || rs.Phase != "shipped" {
		t.Fatalf("parked %+v idle %v phase %q", rs.Parked, rs.Idle, rs.Phase)
	}
}

// The head a merge or a review is pinned to is the newest one the phase run
// knows on its subject's branch: the tip it began on, then each tip it
// pushed there — kept across a park.
func TestReplayPinsThePhaseRunsNewestHead(t *testing.T) {
	fl := mustFlow(t, parkFlowDoc)
	ev := func(p event.Payload) event.Event { return event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
	evs := []event.Event{
		ev(&event.RunCreated{TicketTitle: "t", FlowName: "park", By: "human"}),
		ev(&event.PhaseStarted{Phase: flow.MainPhase, By: "human"}),
		ev(&event.BranchPushed{Repo: "repo", Branch: "b", Tip: "p1"}),
		ev(&event.MRCreated{Repo: "repo", Branch: "b", Target: "main", MRURL: "https://x/1", MRIID: 1, Head: "p1"}),
		ev(&event.PhaseCompleted{Phase: flow.MainPhase}),
		ev(&event.PhaseStarted{Phase: "conflicts", Subject: "repo", Key: "k", By: "signal", Tip: "t1"}),
	}
	if rs := Replay(evs, fl); rs.PhaseTip != "t1" || rs.PhaseHead != "t1" {
		t.Fatalf("a phase run begins at its tip: tip %q head %q", rs.PhaseTip, rs.PhaseHead)
	}
	evs = append(evs, ev(&event.BranchPushed{Repo: "repo", Branch: "b", Tip: "p2"}))
	if rs := Replay(evs, fl); rs.PhaseTip != "t1" || rs.PhaseHead != "p2" {
		t.Fatalf("after its push: tip %q head %q — run.git.diff_phase still counts from t1, a merge pins p2", rs.PhaseTip, rs.PhaseHead)
	}
	evs = append(evs,
		ev(&event.StageStarted{StageID: "sync", Attempt: 1}),
		ev(&event.PhaseParked{Phase: "conflicts", Subject: "repo", StageID: "sync"}),
		ev(&event.PhaseResumed{Phase: "conflicts", Subject: "repo", StageID: "sync"}),
	)
	if rs := Replay(evs, fl); rs.Phase != "conflicts" || rs.PhaseTip != "t1" || rs.PhaseHead != "p2" {
		t.Errorf("across a park: phase %q tip %q head %q", rs.Phase, rs.PhaseTip, rs.PhaseHead)
	}
}

// Replay knows the same heads: pushed tips, the merge request's opening
// head, a phase run's tip — per checkout.
func TestReplayKnowsTheRunsHeads(t *testing.T) {
	fl := mustFlow(t, parkFlowDoc)
	ev := func(p event.Payload) event.Event { return event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
	rs := Replay([]event.Event{
		ev(&event.RunCreated{TicketTitle: "t", FlowName: "park", By: "human"}),
		ev(&event.PhaseStarted{Phase: flow.MainPhase, By: "human"}),
		ev(&event.BranchPushed{Repo: "repo", Branch: "b", Tip: "p1"}),
		ev(&event.MRCreated{Repo: "repo", Branch: "b", MRURL: "https://x/1", MRIID: 1, Head: "m1"}),
		ev(&event.PhaseCompleted{Phase: flow.MainPhase}),
		ev(&event.PhaseStarted{Phase: "conflicts", Subject: "repo", Key: "k", By: "signal", Tip: "t1"}),
	}, fl)
	for _, sha := range []string{"p1", "m1", "t1"} {
		if !rs.KnownHead("repo", sha) {
			t.Errorf("%s unknown after replay", sha)
		}
	}
	if rs.KnownHead("repo", "other") || rs.KnownHead("api", "p1") {
		t.Error("a head is known per checkout, and only the run's own")
	}
}

func mustFlow(t *testing.T, doc string) *flow.Flow {
	t.Helper()
	f, err := flow.Parse("test.yaml", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A workspace a failed provisioning left behind is the view's to destroy,
// never the run's to work in: a retry provisions afresh. One that failed
// after it was provisioned (bootstrap, the egress probe) stays the run's.
func TestReplayIgnoresAPartialWorkspace(t *testing.T) {
	fl := mustFlow(t, parkFlowDoc)
	ev := func(p event.Payload) event.Event { return event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
	for _, tc := range []struct {
		failed *event.EnvFailed
		want   string
	}{
		{&event.EnvFailed{Error: "up failed", EnvID: "apo-x", Partial: true}, ""},
		{&event.EnvFailed{Error: "bootstrap failed", EnvID: "apo-x"}, "apo-x"},
		{&event.EnvFailed{Error: "no sandbox"}, ""},
	} {
		rs := Replay([]event.Event{
			ev(&event.RunCreated{TicketTitle: "t", FlowName: "park", By: "human"}),
			ev(&event.EnvRequested{}),
			ev(tc.failed),
		}, fl)
		if string(rs.EnvID) != tc.want || !rs.Done {
			t.Errorf("%s: env %q done %v, want env %q", tc.failed.Error, rs.EnvID, rs.Done, tc.want)
		}
	}
}

const debugBudgetFlowDoc = `name: dbg
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: command, run: "true", on_fail: { to: build, max: 2, then: debug } }
  - { id: test,  type: command, run: "true", on_success: done, on_fail: { to: build, max: 5, then: debug } }
watchers:
  - id: conflicts
    on: mr_conflicts
    stages:
      - { id: sync, type: command, run: "true", on_fail: { to: sync, max: 1, then: debug } }
`

// Continuing from the debug leaf gives every retry chain of the stage's
// phase its budget back — the person just changed the workspace, so a stage
// failing again past their fix retries before it asks again — and keeps the
// debug links' own counts, so the timeline numbers each visit. Another
// phase's chains are its own: they reset when it starts. The live decision
// (the engine calls ContinueFromDebug) and replay (stage_started while
// parked) land on the same state.
func TestContinueFromDebugRestoresThePhasesRetryBudgets(t *testing.T) {
	fl := mustFlow(t, debugBudgetFlowDoc)
	ev := func(p event.Payload) event.Event { return event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
	evs := []event.Event{
		ev(&event.RunCreated{TicketTitle: "t", FlowName: "dbg", By: "human"}),
		ev(&event.PhaseStarted{Phase: flow.MainPhase, By: "human"}),
		ev(&event.StageStarted{StageID: "build", Attempt: 1}),
		ev(&event.StageFailed{StageID: "build", Error: "x"}),
		ev(&event.EdgeTaken{From: "build", To: "build", Edge: flow.EdgeFail, Count: 1, Max: 2}),
		ev(&event.StageStarted{StageID: "build", Attempt: 2}),
		ev(&event.StageCompleted{StageID: "build"}),
		ev(&event.StageStarted{StageID: "test", Attempt: 1}),
		ev(&event.StageFailed{StageID: "test", Error: "red"}),
		ev(&event.EdgeTaken{From: "test", To: "build", Edge: flow.EdgeFail, Count: 5, Max: 5}),
		ev(&event.StageStarted{StageID: "build", Attempt: 3}),
		ev(&event.StageCompleted{StageID: "build"}),
		ev(&event.StageStarted{StageID: "test", Attempt: 2}),
		ev(&event.StageFailed{StageID: "test", Error: "red"}),
		ev(&event.EdgeTaken{From: "test", To: flow.ReservedDebug, Edge: flow.EdgeFail, Count: 3, Max: flow.UnlimitedMax, Depth: 1}),
	}
	parked := Replay(evs, fl)
	parked.EdgeCounts[ChainKey("sync", flow.EdgeFail, 0)] = 1 // a watcher's, untouched here
	test := &fl.Stages[fl.Index("test")]
	if ch := parked.ChooseEdge(test, flow.EdgeFail); ch.Kind != EdgeDebug {
		t.Fatalf("parked: a failing test goes %v, want straight to the debug leaf", ch.Kind)
	}

	check := func(name string, rs *State) {
		t.Helper()
		if rs.Ph == PhDebug || rs.Pos != fl.Index("test") || rs.Feed != "" {
			t.Errorf("%s: ph %v pos %d feed %q, want test re-entered fresh", name, rs.Ph, rs.Pos, rs.Feed)
		}
		for key, want := range map[string]int{
			ChainKey("test", flow.EdgeFail, 0):  0, // retries back
			ChainKey("build", flow.EdgeFail, 0): 0, // the whole phase's
			ChainKey("test", flow.EdgeFail, 1):  3, // the debug visits kept
		} {
			if got := rs.EdgeCounts[key]; got != want {
				t.Errorf("%s: %s = %d, want %d", name, key, got, want)
			}
		}
		if ch := rs.ChooseEdge(test, flow.EdgeFail); ch.Kind != EdgeLink || ch.Edge.To != "build" || ch.Count != 1 {
			t.Errorf("%s: a failing test goes %v to %v (count %d), want back to build, 1 of 5", name, ch.Kind, ch.Edge, ch.Count)
		}
	}

	live := Replay(evs, fl)
	live.EdgeCounts[ChainKey("sync", flow.EdgeFail, 0)] = 1
	live.ContinueFromDebug()
	if live.Ph != PhStart {
		t.Errorf("live: ph %v, want PhStart", live.Ph)
	}
	if live.EdgeCounts[ChainKey("sync", flow.EdgeFail, 0)] != 1 {
		t.Error("live: a watcher's chain was reset by the main chain's debug leaf")
	}
	check("live", live)
	check("replayed", Replay(append(evs, ev(&event.StageStarted{StageID: "test", Attempt: 3})), fl))
}

// A handback is owed from a release until the session is sent it — and no
// longer than the attempt it was owed to: a new attempt starts with its own
// prompt, not with the handback of a hold its predecessor parked under.
func TestAHandbackIsOwedUntilSentOrTheAttemptEnds(t *testing.T) {
	fl := mustFlow(t, parkFlowDoc)
	ev := func(p event.Payload) event.Event { return event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
	evs := []event.Event{
		ev(&event.RunCreated{TicketTitle: "t", FlowName: "park", By: "human"}),
		ev(&event.PhaseStarted{Phase: flow.MainPhase, By: "human"}),
		ev(&event.StageStarted{StageID: "build", Attempt: 1}),
		ev(&event.HoldTaken{StageID: "build", Attempt: 1, UserID: "u"}),
		ev(&event.HoldReleased{StageID: "build", Attempt: 1, UserID: "u", Reason: event.HoldReleasedByUser}),
	}
	if !Replay(evs, fl).HandbackOwed["build"] {
		t.Fatal("a release owes the session its handback")
	}
	sent := append(append([]event.Event(nil), evs...), ev(&event.AgentPrompted{StageID: "build", Attempt: 1, Kind: event.PromptHandback}))
	if Replay(sent, fl).HandbackOwed["build"] {
		t.Error("still owed once sent")
	}
	retried := append(append([]event.Event(nil), evs...),
		ev(&event.StageFailed{StageID: "build", Reason: event.ReasonHarnessError}),
		ev(&event.StageStarted{StageID: "build", Attempt: 2}))
	if Replay(retried, fl).HandbackOwed["build"] {
		t.Error("a new attempt inherited the handback")
	}
}
