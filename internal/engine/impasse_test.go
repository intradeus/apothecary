package engine

// The impasse (FLOWLANG §3): a reviewer's rejections run out into
// `then: impasse`, the run parks for a person's decision on the rejected
// stage, and the answer — keep going, skip, give up — says what follows.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
	"apothecary/internal/run/runinput"
)

// impasseDoc: a builder and a reviewer whose rejections reach the impasse
// after ON_REJECT, then a stage that runs once the review is past.
const impasseDoc = `name: imp
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build,  type: agent, harness: $harness, model: $model, agent: builder,
      input: { rejection: { from: edge.verdict, type: verdict, optional: true } } }
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer,
      output: { verdict: { to: artifacts/verdict.json, type: verdict } },
      on_reject: ON_REJECT }
  - { id: after,  type: command, run: "true", on_success: done }
`

func impasseFlow(t *testing.T, onReject string) *flow.Flow {
	t.Helper()
	return mustFlow(t, strings.Replace(impasseDoc, "ON_REJECT", onReject, 1))
}

// impasseScript makes the reviewer reject until its approveAt-th review
// (0: never), and records each builder prompt.
func impasseScript(t *testing.T, h *harness, approveAt int) (builds *[]string) {
	t.Helper()
	reviews := 0
	builds = &[]string{}
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"builder": func(c fake.RunCall) (agentport.AgentResult, error) {
			*builds = append(*builds, c.In.Prompt)
			return agentport.AgentResult{TurnsUsed: 1}, nil
		},
		"reviewer": func(c fake.RunCall) (agentport.AgentResult, error) {
			reviews++
			if approveAt > 0 && reviews >= approveAt {
				h.writeFile(t, c.Env, "artifacts/verdict.json", `{"verdict":"APPROVE","comments":[]}`)
			} else {
				h.writeFile(t, c.Env, "artifacts/verdict.json",
					fmt.Sprintf(`{"verdict":"REJECT","comments":["round %d: rename the cart service"]}`, reviews))
			}
			return agentport.AgentResult{TurnsUsed: 1}, nil
		},
	})
	return builds
}

// route lists where a stage's edges went, with their counts.
func route(evs []event.Event, from string) string {
	var r []string
	for _, e := range evs {
		if et, ok := e.Payload.(*event.EdgeTaken); ok && et.From == from {
			r = append(r, fmt.Sprintf("%s %d", et.To, et.Count))
		}
	}
	return strings.Join(r, ", ")
}

// settleRun waits for the run's loop to finish, failing fast — naming what was
// left open — when it parks again instead (at a second impasse, say).
func settleRun(t *testing.T, h *harness, runID string) {
	t.Helper()
	done := make(chan struct{})
	go func() { h.e.Wait(runID); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		var open []string
		for _, p := range h.e.PendingPings() {
			open = append(open, p.Type+" at "+p.StageID)
		}
		t.Fatalf("the run did not settle; pending %v; events %v", open, eventTypes(h.events(t, runID)))
	}
}

func startImpasseRun(t *testing.T, h *harness) string {
	t.Helper()
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "imp", testProject)
	if err != nil {
		t.Fatal(err)
	}
	return runID
}

// The rejections run out: the run parks on the reviewer (like a gate: the
// chain holds the cursor, nobody the checkout) with a choice ping carrying
// the verdict. Keep going sends that verdict to the builder with the
// reviewer's on_reject budget back — the same two sends to build as the
// first time, this one included — and the review passes on the fifth.
func TestImpasseKeepGoing(t *testing.T) {
	h := newHarness(t, impasseFlow(t, "{ to: build, max: 2, then: impasse }"))
	builds := impasseScript(t, h, 5)
	runID := startImpasseRun(t, h)

	p := h.waitPing(t, PingImpasse)
	if p.StageID != "review" || p.AnswerKind != event.AnswerChoice || p.Question != "review rejected 3 times" ||
		strings.Join(event.ChoiceIDs(p.Choices), ",") != "keep_going,skip,give_up" ||
		!strings.Contains(p.Context, "round 3: rename the cart service") || p.Phase != flow.MainPhase || p.EndsPhase {
		t.Fatalf("ping = %+v", p)
	}
	// Read from the log, as PendingPings serves a ping its registry does not
	// hold (a parked run's loop gone, or one logged after the registry was
	// read), the impasse keeps its choices.
	h.e.mu.Lock()
	live := h.e.pings[p.ID]
	delete(h.e.pings, p.ID)
	h.e.mu.Unlock()
	logged := ""
	for _, pp := range h.e.PendingPings() {
		if pp.ID == p.ID {
			logged = strings.Join(event.ChoiceIDs(pp.Choices), ",")
		}
	}
	h.e.mu.Lock()
	h.e.pings[p.ID] = live
	h.e.mu.Unlock()
	if logged != "keep_going,skip,give_up" {
		t.Errorf("the impasse's choices read from the log = %q", logged)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err == nil || !strings.Contains(err.Error(), "keep_going|skip|give_up") {
		t.Errorf("a yes/no answer to a choice: %v", err)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: event.ChoiceKeepGoing, Feedback: "the name is fine, fix the rest", By: "u1"}); err != nil {
		t.Fatal(err)
	}
	settleRun(t, h, runID)

	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(evs))
	}
	if got, want := route(evs, "review"), "build 1, build 2, impasse 1, build 1, build 2"; got != want {
		t.Errorf("review's rejections went %s, want %s", got, want)
	}
	if parked, _ := findPayload[*event.PhaseParked](evs); parked == nil || parked.StageID != "review" || parked.PingID != p.ID {
		t.Errorf("phase_parked = %+v, want the chain parked on review's impasse", parked)
	}
	if ans, _ := findPayload[*event.PingAnswered](evs); ans == nil || ans.By != "u1" || ans.Answer != event.ChoiceKeepGoing {
		t.Errorf("ping_answered = %+v, want keep_going by u1", ans)
	}
	// The verdict that reached the impasse went on to the builder.
	if len(*builds) != 5 || !strings.Contains((*builds)[3], "round 3: rename the cart service") {
		t.Errorf("builds = %d; the one after keep going must read the verdict that reached the impasse", len(*builds))
	}
	// Pass numbers kept counting through it.
	var iters []int
	for _, e := range evs {
		if s, ok := e.Payload.(*event.StageStarted); ok && s.StageID == "review" {
			iters = append(iters, s.Attempt)
		}
	}
	if fmt.Sprint(iters) != "[1 2 3 4 5]" {
		t.Errorf("review attempts = %v", iters)
	}
	if rc, _ := findPayload[*event.RunCompleted](evs); rc.UnwantedPings != 0 {
		t.Errorf("unwanted_pings = %d, want 0 (the impasse is asked on purpose)", rc.UnwantedPings)
	}
}

// Skip: the stale REJECT is cleared, the reviewer completes as skipped by
// whoever answered, with their words, and the chain goes on.
func TestImpasseSkip(t *testing.T) {
	h := newHarness(t, impasseFlow(t, "{ to: build, max: 1, then: impasse }"))
	impasseScript(t, h, 0)
	runID := startImpasseRun(t, h)

	p := h.waitPing(t, PingImpasse)
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: event.ChoiceSkip, Feedback: "out of scope for this ticket", By: "u1"}); err != nil {
		t.Fatal(err)
	}
	settleRun(t, h, runID)

	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(evs))
	}
	var skip *event.StageCompleted
	var reviewIters []int
	for _, e := range evs {
		switch p := e.Payload.(type) {
		case *event.StageCompleted:
			if p.StageID == "review" && p.Skipped {
				skip = p
			}
		case *event.StageStarted:
			if p.StageID == "review" {
				reviewIters = append(reviewIters, p.Attempt)
			}
		}
	}
	if skip == nil || skip.By != "u1" || !strings.Contains(skip.Note, "out of scope for this ticket") {
		t.Fatalf("review's skip = %+v, want skipped by u1 with the note", skip)
	}
	if fmt.Sprint(reviewIters) != "[1 2 3]" {
		t.Errorf("review attempts = %v, want the skip as attempt 3", reviewIters)
	}
	ready, _ := findPayload[*event.EnvReady](evs)
	if _, held := h.envp.Env(envport.EnvID(ready.EnvID)).Files["artifacts/verdict.json"]; held {
		t.Error("the stale REJECT verdict is still in the workspace after the skip")
	}
	afterRan := false
	for _, e := range evs {
		if s, ok := e.Payload.(*event.StageStarted); ok && s.StageID == "after" {
			afterRan = true
		}
	}
	if !afterRan {
		t.Error("the stage after the skipped review never ran")
	}
}

// Give up is a dead end: the main chain's abandons the run, with the words.
func TestImpasseGiveUp(t *testing.T) {
	h := newHarness(t, impasseFlow(t, "{ to: build, max: 1, then: impasse }"))
	impasseScript(t, h, 0)
	runID := startImpasseRun(t, h)

	p := h.waitPing(t, PingImpasse)
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: event.ChoiceGiveUp, Feedback: "wrong approach"}); err != nil {
		t.Fatal(err)
	}
	settleRun(t, h, runID)
	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s", v.State)
	}
	ra, _ := findPayload[*event.RunAbandoned](h.events(t, runID))
	if ra.StalledAt != "review" || !strings.Contains(ra.Reason, "gave up at the impasse") || !strings.Contains(ra.Reason, "wrong approach") {
		t.Errorf("run_abandoned = %+v", ra)
	}
}

// A gate's rejections reach the impasse too: the person who keeps saying no
// is asked whether to keep going — the gate sends the work back once more —
// or to skip the gate. The gate's own reject is routed before the impasse
// asks, so it is never taken for the impasse's answer.
func TestImpasseAtAGate(t *testing.T) {
	const doc = `name: imp
version: 2
parameters: {}
main:
  - { id: build, type: command, run: "true" }
  - { id: gate,  type: human_gate, question: "Ship it?", on_reject: { to: build, max: 1, then: impasse } }
  - { id: after, type: command, run: "true", on_success: done }
`
	for _, c := range []struct {
		choice, route string
		skipped       bool
	}{
		{event.ChoiceKeepGoing, "build 1, impasse 1, build 1", false},
		{event.ChoiceSkip, "build 1, impasse 1", true},
	} {
		t.Run(c.choice, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, doc))
			runID := startImpasseRun(t, h)
			first := h.waitPing(t, PingGate)
			if err := h.e.AnswerPing(first.ID, PingAnswer{Answer: "reject", Feedback: "not yet"}); err != nil {
				t.Fatal(err)
			}
			second := h.waitPing(t, PingGate, first.ID)
			if err := h.e.AnswerPing(second.ID, PingAnswer{Answer: "reject", Feedback: "still not"}); err != nil {
				t.Fatal(err)
			}
			p := h.waitPing(t, PingImpasse)
			if p.StageID != "gate" || p.Question != "gate rejected 2 times" || !strings.Contains(p.Context, "still not") {
				t.Fatalf("ping = %+v", p)
			}
			if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: c.choice, By: "u1"}); err != nil {
				t.Fatal(err)
			}
			if !c.skipped {
				third := h.waitPing(t, PingGate, first.ID, second.ID)
				if err := h.e.AnswerPing(third.ID, PingAnswer{Answer: "approve"}); err != nil {
					t.Fatal(err)
				}
			}
			settleRun(t, h, runID)

			evs := h.events(t, runID)
			if v := h.view(t, runID); v.State != run.Completed {
				t.Fatalf("state = %s, events %v", v.State, eventTypes(evs))
			}
			if got := route(evs, "gate"); got != c.route {
				t.Errorf("the gate's rejections went %s, want %s", got, c.route)
			}
			skipped := logHas(evs, func(sc *event.StageCompleted) bool { return sc.StageID == "gate" && sc.Skipped && sc.By == "u1" })
			if skipped != c.skipped {
				t.Errorf("gate skipped by u1 = %v, want %v", skipped, c.skipped)
			}
		})
	}
}

// `on_reject: impasse` alone: every rejection asks, and keep going — which
// would only ask again — is not offered.
func TestImpasseAloneOffersNoKeepGoing(t *testing.T) {
	doc := strings.Replace(impasseDoc, "\n      input: { rejection: { from: edge.verdict, type: verdict, optional: true } }", "", 1)
	h := newHarness(t, mustFlow(t, strings.Replace(doc, "ON_REJECT", "impasse", 1)))
	impasseScript(t, h, 0)
	runID := startImpasseRun(t, h)

	p := h.waitPing(t, PingImpasse)
	if got := strings.Join(event.ChoiceIDs(p.Choices), ","); got != "skip,give_up" || p.Question != "review rejected once" {
		t.Fatalf("choices %q question %q", got, p.Question)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: event.ChoiceKeepGoing}); err == nil {
		t.Error("keep going was accepted where it is not offered")
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: event.ChoiceGiveUp}); err != nil {
		t.Fatal(err)
	}
	settleRun(t, h, runID)
}

// A restart does not lose the decision or the budget it restores: the log
// ends parked at the impasse (unanswered, then answered before its routing),
// recovery routes the answer, and the rejection goes back to build with the
// chain's budget back — never straight back to the impasse.
func TestImpasseAcrossRestart(t *testing.T) {
	fl := impasseFlow(t, "{ to: build, max: 1, then: impasse }")
	for _, answered := range []bool{false, true} {
		t.Run(fmt.Sprintf("answered=%v", answered), func(t *testing.T) {
			h := newHarness(t, fl)
			impasseScript(t, h, 1) // the review after keep going approves
			ctx := context.Background()
			runID := fmt.Sprintf("r-imp-%v", answered)
			envID, err := h.envp.Provision(ctx, envport.EnvSpec{RunID: runID})
			if err != nil {
				t.Fatal(err)
			}
			pingID := "imp-" + runID
			evs := []event.Payload{
				&event.RunCreated{TicketTitle: "T", FlowName: "imp", ProjectID: testProject, FlowHash: fl.Hash, Branch: "apothecary/" + runID},
				&event.PhaseStarted{Phase: flow.MainPhase, By: "human"},
				&event.EnvRequested{},
				&event.EnvReady{EnvID: string(envID)},
				&event.StageStarted{StageID: "build", Attempt: 1},
				&event.StageCompleted{StageID: "build", SessionID: "s-b"},
				&event.StageStarted{StageID: "review", Attempt: 1},
				&event.StageCompleted{StageID: "review", SessionID: "s-r"},
				&event.VerdictReceived{StageID: "review", NComments: 1},
				&event.EdgeTaken{From: "review", To: "build", Edge: flow.EdgeReject, Count: 1, Max: 1, Feed: "round 1"},
				&event.StageStarted{StageID: "build", Attempt: 2},
				&event.StageCompleted{StageID: "build", SessionID: "s-b"},
				&event.StageStarted{StageID: "review", Attempt: 2},
				&event.StageCompleted{StageID: "review", SessionID: "s-r"},
				&event.VerdictReceived{StageID: "review", NComments: 1},
				&event.EdgeTaken{From: "review", To: flow.ReservedImpasse, Edge: flow.EdgeReject, Count: 1, Max: flow.UnlimitedMax, Depth: 1, Feed: "round 2"},
				&event.PingRaised{PingID: pingID, Type: event.PingImpasse, Question: "review rejected 2 times", Context: "round 2",
					StageID: "review", AnswerKind: event.AnswerChoice, Choices: event.ImpasseChoices(), Phase: flow.MainPhase},
				&event.PhaseParked{Phase: flow.MainPhase, StageID: "review", PingID: pingID},
			}
			if answered {
				evs = append(evs, &event.PingAnswered{PingID: pingID, Answer: event.ChoiceKeepGoing, By: "u1"})
			}
			for _, p := range evs {
				if _, err := h.store.Append(ctx, runID, p); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.e.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			if !answered {
				// Parked like a gate: the run idles on its question (no Wait
				// returns until it moves), which recovery re-registered.
				if v := h.view(t, runID); v.State.Terminal() {
					t.Fatalf("an unanswered impasse ended the run: %s", v.State)
				}
				if err := h.e.AnswerPing(pingID, PingAnswer{Answer: event.ChoiceKeepGoing}); err != nil {
					t.Fatal(err)
				}
			}
			settleRun(t, h, runID)
			out := h.events(t, runID)
			if v := h.view(t, runID); v.State != run.Completed {
				t.Fatalf("state %s, events %v", v.State, eventTypes(out))
			}
			if got, want := route(out, "review"), "build 1, impasse 1, build 1"; got != want {
				t.Errorf("review's rejections went %s, want %s", got, want)
			}
			if n := countType(out, event.TypePingRaised); n != 1 {
				t.Errorf("ping_raised ×%d, want the one the log had", n)
			}
		})
	}
}

// For every built-in flow and every stage whose rejections can reach the
// impasse: with every counter of the flow spent, keep going restores that
// stage's on_reject chain and nothing else — not its other chains, not
// another stage's, not another phase's — keeps the impasse's own count, and
// the rejection then goes its chain's first way, 1 of its max. Skip and give
// up restore nothing.
func TestImpasseKeepGoingRestoresOnlyThatChain(t *testing.T) {
	const spent = 99
	checked := 0
	for _, name := range builtin.FlowNames() {
		fl := builtin.Flow(name)
		for i := range fl.Stages {
			st := &fl.Stages[i]
			if !st.Skippable() {
				continue
			}
			checked++
			for _, choice := range event.ChoiceIDs(event.ImpasseChoices()) {
				t.Run(name+"/"+st.ID+"/"+choice, func(t *testing.T) {
					rs := runcore.NewState(fl)
					for j := range fl.Stages {
						s := &fl.Stages[j]
						links(s, func(kind string, depth int, _ *flow.Edge) {
							rs.EdgeCounts[runcore.ChainKey(s.ID, kind, depth)] = spent
						})
					}
					rs.Pos, rs.Ph = i, runcore.PhImpasse
					ev := func(p event.Payload) *event.Event { return &event.Event{RunID: "r", Type: p.EventType(), Payload: p} }
					rs.Apply(ev(&event.PingRaised{PingID: "p", Type: event.PingImpasse, StageID: st.ID,
						AnswerKind: event.AnswerChoice, Choices: event.ImpasseChoices()}))
					rs.Apply(ev(&event.PingAnswered{PingID: "p", Answer: choice}))
					if rs.GateAnswer == nil {
						t.Fatal("the answer is not the impasse's decision")
					}
					for j := range fl.Stages {
						s := &fl.Stages[j]
						links(s, func(kind string, depth int, e *flow.Edge) {
							key := runcore.ChainKey(s.ID, kind, depth)
							want := spent
							if choice == event.ChoiceKeepGoing && s.ID == st.ID && kind == flow.EdgeReject && e.To != flow.ReservedImpasse {
								want = 0
							}
							if got := rs.EdgeCounts[key]; got != want {
								t.Errorf("%s (→ %s) = %d, want %d", key, e.To, got, want)
							}
						})
					}
					if choice == event.ChoiceKeepGoing {
						first := st.Edge(flow.EdgeReject)
						if ch := rs.ChooseEdge(st, flow.EdgeReject); ch.Kind != runcore.EdgeLink || ch.Edge != first || ch.Count != 1 {
							t.Errorf("after keep going the rejection goes %v → %v (count %d), want %s 1 of %d", ch.Kind, ch.Edge, ch.Count, first.To, first.Max)
						}
					}
				})
			}
		}
	}
	if checked == 0 {
		t.Fatal("no built-in stage reaches the impasse; the test is not looking where it should")
	}
}
