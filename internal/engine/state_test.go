package engine

// Replay of the per-phase-run state: a phase run parked at a wait point keeps
// its cursor while another phase runs, and gets it back when it resumes.

import (
	"testing"

	"apothecary/internal/event"
)

const parkFlowDoc = `name: park
version: 4
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
phases:
  - id: deliver
    on: run_start
    stages:
      - { id: build, type: command, run: "true" }
      - { id: mr, type: action, action: create_mr, input: { body: artifacts/mr.md } }
      - { id: merged, type: wait, for: mr_merged }
      - { id: after, type: command, run: "true", on_success: done }
  - id: conflicts
    on: mr_conflicts
    stages:
      - { id: sync, type: action, action: sync_target }
`

func TestReplayParksAndResumes(t *testing.T) {
	fl := mustFlow(t, parkFlowDoc)
	ev := func(p event.Payload) event.Event { return event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
	evs := []event.Event{
		ev(&event.RunCreated{TicketTitle: "t", FlowName: "park", Phase: "deliver", By: "human"}),
		ev(&event.PhaseStarted{Phase: "deliver", By: "human"}),
		ev(&event.StageStarted{StageID: "build", Iteration: 1}),
		ev(&event.StageCompleted{StageID: "build"}),
		ev(&event.StageStarted{StageID: "mr", Iteration: 1}),
		ev(&event.MRCreated{Repo: "repo", Branch: "b", Target: "main", MRURL: "https://x/1", MRIID: 1}),
		ev(&event.StageCompleted{StageID: "mr"}),
		ev(&event.StageStarted{StageID: "merged", Iteration: 1}),
		ev(&event.PhaseParked{Phase: "deliver", StageID: "merged"}),
	}
	rs := replay(evs, fl)
	if !rs.idle || rs.parked["deliver"] == nil || rs.parked["deliver"].parkedAt != "merged" || rs.parked["deliver"].pos != fl.Index("merged") || rs.begin != "deliver" {
		t.Fatalf("after park: idle %v parked %+v begin %q", rs.idle, rs.parked["deliver"], rs.begin)
	}

	evs = append(evs,
		ev(&event.PhaseStarted{Phase: "conflicts", Subject: "repo", Key: "k", By: "signal"}),
		ev(&event.StageStarted{StageID: "sync", Iteration: 1}),
	)
	rs = replay(evs, fl)
	if rs.idle || rs.phase != "conflicts" || rs.pos != fl.Index("sync") || rs.ph != phIn || rs.parked["deliver"] == nil {
		t.Fatalf("mid-reaction: idle %v phase %q pos %d ph %v parked %v", rs.idle, rs.phase, rs.pos, rs.ph, rs.parked["deliver"] != nil)
	}

	evs = append(evs,
		ev(&event.StageCompleted{StageID: "sync"}),
		ev(&event.PhaseCompleted{Phase: "conflicts", Subject: "repo"}),
		ev(&event.PhaseResumed{Phase: "deliver", StageID: "merged"}),
		ev(&event.StageCompleted{StageID: "merged"}),
	)
	rs = replay(evs, fl)
	if rs.idle || rs.phase != "deliver" || rs.pos != fl.Index("merged") || rs.ph != phAfterComplete || len(rs.parked) != 0 {
		t.Fatalf("after resume: idle %v phase %q pos %d ph %v parked %d", rs.idle, rs.phase, rs.pos, rs.ph, len(rs.parked))
	}
	if rs.phaseRuns[phaseRunKey("deliver", "")] != 1 || rs.phaseRuns[phaseRunKey("conflicts", "repo")] != 1 || !rs.triggerSeen[triggerKey("conflicts", "repo", "k")] {
		t.Errorf("phase runs %v seen %v", rs.phaseRuns, rs.triggerSeen)
	}
	if rs.anyParked() || rs.unpark("ghost") {
		t.Error("unpark of an unknown phase must be a no-op")
	}
}
