package engine

// Continuing from the debug leaf gives the retry chains back their budget
// (runcore.State.ContinueFromDebug): checked on every stage of every
// built-in flow that can escalate, and across a restart at both points a
// crash can land.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
)

// links calls fn for every link of every edge chain st declares.
func links(st *flow.Stage, fn func(kind string, depth int, e *flow.Edge)) {
	for kind, edge := range st.Edges {
		depth := 0
		for e := edge; e != nil; e, depth = e.Then, depth+1 {
			fn(kind, depth, e)
		}
	}
}

// For every built-in flow and every stage that can reach the debug leaf:
// with every counter of the flow spent, continuing there — live, and as
// replay folds the stage_started that records it — restores every non-debug
// link of every stage of that stage's phase, keeps the debug links' counts,
// and leaves every other phase's counters alone. Each chain of the phase
// then routes exactly as on a fresh run.
func TestContinueFromDebugRestoresEveryChainOfThePhase(t *testing.T) {
	const spent = 99
	checked := 0
	for _, name := range builtin.FlowNames() {
		fl := builtin.Flow(name)
		for i := range fl.Stages {
			esc := &fl.Stages[i]
			escalates := false
			links(esc, func(_ string, _ int, e *flow.Edge) { escalates = escalates || e.To == flow.ReservedDebug })
			if !escalates {
				continue
			}
			checked++
			t.Run(name+"/"+esc.ID, func(t *testing.T) {
				parked := func() *runcore.State {
					rs := runcore.NewState(fl)
					for j := range fl.Stages {
						st := &fl.Stages[j]
						links(st, func(kind string, depth int, _ *flow.Edge) {
							rs.EdgeCounts[runcore.ChainKey(st.ID, kind, depth)] = spent
						})
					}
					rs.Pos, rs.Ph, rs.Feed = i, runcore.PhDebug, "what it printed"
					return rs
				}
				live := parked()
				live.ContinueFromDebug()
				replayed := parked()
				started := &event.StageStarted{StageID: esc.ID, Attempt: 3}
				replayed.Apply(&event.Event{RunID: "r", Type: started.EventType(), Payload: started})
				if fmt.Sprint(live.EdgeCounts) != fmt.Sprint(replayed.EdgeCounts) {
					t.Fatalf("live and replay disagree:\n live   %v\n replay %v", live.EdgeCounts, replayed.EdgeCounts)
				}
				if live.Ph != runcore.PhStart || live.Pos != i || live.Feed != "" {
					t.Errorf("live: ph %v pos %d feed %q, want %s re-entered fresh", live.Ph, live.Pos, live.Feed, esc.ID)
				}

				ph := fl.PhaseOf(esc)
				fresh := runcore.NewState(fl)
				for j := range fl.Stages {
					st := &fl.Stages[j]
					inPhase := j >= ph.First && j <= ph.Last
					links(st, func(kind string, depth int, e *flow.Edge) {
						key := runcore.ChainKey(st.ID, kind, depth)
						want := spent
						if inPhase && e.To != flow.ReservedDebug {
							want = 0
						}
						if got := live.EdgeCounts[key]; got != want {
							t.Errorf("%s (→ %s, %s) = %d, want %d", key, e.To, map[bool]string{true: "this phase", false: "another phase"}[inPhase], got, want)
						}
					})
					if !inPhase {
						continue
					}
					for kind := range st.Edges {
						got, want := live.ChooseEdge(st, kind), fresh.ChooseEdge(st, kind)
						if got.Kind != want.Kind || got.Edge != want.Edge || got.Depth != want.Depth ||
							(got.Kind == runcore.EdgeLink && got.Count != want.Count) {
							t.Errorf("%s %s routes %v → %v depth %d count %d, want as fresh: %v → %v depth %d count %d",
								st.ID, kind, got.Kind, got.Edge.To, got.Depth, got.Count, want.Kind, want.Edge.To, want.Depth, want.Count)
						}
					}
				}
			})
		}
	}
	if checked < 10 {
		t.Fatalf("only %d escalating stages across the built-in flows; the test is not looking where it should", checked)
	}
}

// A restart does not lose the restored budget, wherever the crash lands:
// after the answer but before its routing (recovery routes it, live), or
// after the stage_started that records the continue (replay folds it). The
// failing test goes back to build before it asks again either way.
func TestDebugContinueRestoresRetriesAcrossRestart(t *testing.T) {
	fl := mustFlow(t, debugLeafDoc)
	for _, window := range []string{"answered", "recorded"} {
		t.Run(window, func(t *testing.T) {
			h := newHarness(t, fl)
			h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
				if strings.Contains(cmd.Script, "make lint") {
					return envport.ExecResult{ExitCode: 1, Output: "red"}, nil
				}
				return envport.ExecResult{ExitCode: 0}, nil
			}
			ctx := context.Background()
			runID := "r-" + window
			envID, err := h.envp.Provision(ctx, envport.EnvSpec{RunID: runID})
			if err != nil {
				t.Fatal(err)
			}
			pingID := "dbg-" + runID
			evs := []event.Payload{
				&event.RunCreated{TicketTitle: "T", FlowName: "dbg", ProjectID: testProject, FlowHash: fl.Hash, Branch: "apothecary/" + runID},
				&event.PhaseStarted{Phase: flow.MainPhase, By: "human"},
				&event.EnvRequested{},
				&event.EnvReady{EnvID: string(envID)},
				&event.StageStarted{StageID: "build", Attempt: 1},
				&event.StageCompleted{StageID: "build", SessionID: "sess-1"},
				&event.StageStarted{StageID: "test", Attempt: 1},
				&event.StageFailed{StageID: "test", Reason: event.ReasonExitNonzero, Error: "red"},
				&event.EdgeTaken{From: "test", To: "build", Edge: flow.EdgeFail, Count: 1, Max: 1, Feed: "red"},
				&event.StageStarted{StageID: "build", Attempt: 2},
				&event.StageCompleted{StageID: "build", SessionID: "sess-1"},
				&event.StageStarted{StageID: "test", Attempt: 2},
				&event.StageFailed{StageID: "test", Reason: event.ReasonExitNonzero, Error: "red"},
				&event.EdgeTaken{From: "test", To: flow.ReservedDebug, Edge: flow.EdgeFail, Count: 1, Max: flow.UnlimitedMax, Depth: 1, Feed: "red"},
				&event.PingRaised{PingID: pingID, Type: event.PingDebug, Question: "test failed", Context: "red",
					StageID: "test", AnswerKind: event.AnswerYesNo, Phase: flow.MainPhase},
				&event.RunPaused{StageID: "test", Reason: event.PauseWaiting},
				&event.PingAnswered{PingID: pingID, Answer: "approve"},
				&event.RunUnpaused{Reason: "answered"},
			}
			if window == "recorded" {
				evs = append(evs, &event.StageStarted{StageID: "test", Attempt: 3})
			}
			for _, p := range evs {
				if _, err := h.store.Append(ctx, runID, p); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.e.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			p := h.waitPing(t, PingDebug, pingID)
			if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "reject"}); err != nil {
				t.Fatal(err)
			}
			h.e.Wait(runID)
			if v := h.view(t, runID); v.State != run.Abandoned {
				t.Fatalf("state %s, events %v", v.State, eventTypes(h.events(t, runID)))
			}
			var route []string
			for _, e := range h.events(t, runID) {
				if et, ok := e.Payload.(*event.EdgeTaken); ok && et.From == "test" {
					route = append(route, fmt.Sprintf("%s %d", et.To, et.Count))
				}
			}
			if got, want := strings.Join(route, ", "), "build 1, debug 1, build 1, debug 2"; got != want {
				t.Errorf("test's failures went %s, want %s", got, want)
			}
		})
	}
}
