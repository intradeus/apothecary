package engine

// Replay of the per-phase-run state: a phase run parked at a wait point keeps
// its cursor while another phase runs, and gets it back when it resumes.

import (
	"testing"

	"apothecary/internal/event"
	"apothecary/internal/flow"
)

const parkFlowDoc = `name: park
version: 5
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
main:
  - { id: build, type: command, run: "true" }
  - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }
  - { id: merged, type: wait, for: mr_merged }
  - { id: after, type: command, run: "true", on_success: done }
watchers:
  - id: conflicts
    on: mr_conflicts
    stages:
      - { id: sync, type: action, action: sync_target }
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
	rs := replay(evs, fl)
	if !rs.idle || rs.parked[flow.MainPhase] == nil || rs.parked[flow.MainPhase].parkedAt != "merged" || rs.parked[flow.MainPhase].pos != fl.Index("merged") {
		t.Fatalf("after park: idle %v parked %+v", rs.idle, rs.parked[flow.MainPhase])
	}

	evs = append(evs,
		ev(&event.PhaseStarted{Phase: "conflicts", Subject: "repo", Key: "k", By: "signal"}),
		ev(&event.StageStarted{StageID: "sync", Iteration: 1}),
	)
	rs = replay(evs, fl)
	if rs.idle || rs.phase != "conflicts" || rs.pos != fl.Index("sync") || rs.ph != phIn || rs.parked[flow.MainPhase] == nil {
		t.Fatalf("mid-watcher: idle %v phase %q pos %d ph %v parked %v", rs.idle, rs.phase, rs.pos, rs.ph, rs.parked[flow.MainPhase] != nil)
	}

	evs = append(evs,
		ev(&event.StageCompleted{StageID: "sync"}),
		ev(&event.PhaseCompleted{Phase: "conflicts", Subject: "repo"}),
		ev(&event.PhaseResumed{Phase: flow.MainPhase, StageID: "merged"}),
		ev(&event.StageCompleted{StageID: "merged"}),
	)
	rs = replay(evs, fl)
	if rs.idle || rs.phase != flow.MainPhase || rs.pos != fl.Index("merged") || rs.ph != phAfterComplete || len(rs.parked) != 0 {
		t.Fatalf("after resume: idle %v phase %q pos %d ph %v parked %d", rs.idle, rs.phase, rs.pos, rs.ph, len(rs.parked))
	}
	if rs.phaseRuns[phaseRunKey(flow.MainPhase, "")] != 1 || rs.phaseRuns[phaseRunKey("conflicts", "repo")] != 1 || !rs.triggerSeen[triggerKey("conflicts", "repo", "k")] {
		t.Errorf("phase runs %v seen %v", rs.phaseRuns, rs.triggerSeen)
	}
	if rs.anyParked() || rs.unpark("ghost") {
		t.Error("unpark of an unknown phase must be a no-op")
	}
}

// The head a merge or a review is pinned to is the newest one the phase run
// knows on its subject's branch: the tip it began on, then each tip it
// pushed there — kept across a park, its own in every phase run.
func TestReplayPinsThePhaseRunsNewestHead(t *testing.T) {
	fl := mustFlow(t, parkFlowDoc)
	ev := func(p event.Payload) event.Event { return event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
	evs := []event.Event{
		ev(&event.RunCreated{TicketTitle: "t", FlowName: "park", By: "human"}),
		ev(&event.PhaseStarted{Phase: flow.MainPhase, By: "human"}),
		ev(&event.BranchPushed{Repo: "repo", Branch: "b", Tip: "p1"}),
		ev(&event.MRCreated{Repo: "repo", Branch: "b", Target: "main", MRURL: "https://x/1", MRIID: 1, Head: "p1"}),
		ev(&event.StageStarted{StageID: "merged", Iteration: 1}),
		ev(&event.PhaseParked{Phase: flow.MainPhase, StageID: "merged"}),
		ev(&event.PhaseStarted{Phase: "conflicts", Subject: "repo", Key: "k", By: "signal", Tip: "t1"}),
	}
	if rs := replay(evs, fl); rs.phaseTip != "t1" || rs.phaseHead != "t1" {
		t.Fatalf("a phase run begins at its tip: tip %q head %q", rs.phaseTip, rs.phaseHead)
	}
	evs = append(evs, ev(&event.BranchPushed{Repo: "repo", Branch: "b", Tip: "p2"}))
	if rs := replay(evs, fl); rs.phaseTip != "t1" || rs.phaseHead != "p2" {
		t.Fatalf("after its push: tip %q head %q — $DIFF_PHASE still counts from t1, a merge pins p2", rs.phaseTip, rs.phaseHead)
	}
	evs = append(evs,
		ev(&event.PhaseCompleted{Phase: "conflicts", Subject: "repo"}),
		ev(&event.PhaseResumed{Phase: flow.MainPhase, StageID: "merged"}),
	)
	if rs := replay(evs, fl); rs.phase != flow.MainPhase || rs.phaseHead != "" {
		t.Errorf("the main chain resumed with head %q: it has no subject to pin", rs.phaseHead)
	}
}
