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
		ev(&event.StageStarted{StageID: "build", Iteration: 1}),
		ev(&event.StageCompleted{StageID: "build"}),
		ev(&event.StageStarted{StageID: "mr", Iteration: 1}),
		ev(&event.MRCreated{Repo: "repo", Branch: "b", Target: "main", MRURL: "https://x/1", MRIID: 1}),
		ev(&event.StageCompleted{StageID: "mr"}),
		ev(&event.StageStarted{StageID: "merged", Iteration: 1}),
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
		ev(&event.StageStarted{StageID: "merged", Iteration: 1}),
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
		ev(&event.StageStarted{StageID: "sync", Iteration: 1}),
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
