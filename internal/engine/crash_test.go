package engine

// A crash at every event: each scenario runs once clean, then once per event
// of its log with the process dying right after writing that event — its
// store refuses everything from there on, whatever it was doing — and a fresh
// engine recovering over the same log and the same outside world. Side
// effects the dead process had already performed survive it, as they would.
// Wherever the crash lands, the run must end the way the clean one did, with
// the same stages completed in the same order and nothing done twice.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

var errCrashed = errors.New("the process died")

// crashStore writes the first n events and refuses everything after them. A
// batch is one transaction: it lands whole or not at all.
type crashStore struct {
	event.Store
	mu   sync.Mutex
	left int
	dead chan struct{}
}

func newCrashStore(s event.Store, n int) *crashStore {
	return &crashStore{Store: s, left: n, dead: make(chan struct{})}
}

func (s *crashStore) Append(ctx context.Context, runID string, ps ...event.Payload) ([]event.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.left <= 0 || len(ps) > s.left {
		s.kill()
		return nil, errCrashed
	}
	evs, err := s.Store.Append(ctx, runID, ps...)
	if s.left -= len(ps); s.left == 0 {
		s.kill()
	}
	return evs, err
}

func (s *crashStore) isDead() bool {
	select {
	case <-s.dead:
		return true
	default:
		return false
	}
}

func (s *crashStore) kill() {
	select {
	case <-s.dead:
	default:
		close(s.dead)
		s.left = 0
	}
}

// crashScenario is one run through the world. world scripts the fakes: what
// they do must follow from the world's state, never from counting calls, so
// that a step repeated after a crash behaves the same. drive looks at the
// run's log and plays the humans and the repository host.
type crashScenario struct {
	name  string
	doc   string
	world func(h *harness) // scripts the harness's fakes
	drive func(h *harness, runID string, evs []event.Event)
	want  run.State
}

// approveAll answers every open question with a yes: a thread selection takes
// every thread.
func approveAll(h *harness, runID string) {
	for _, p := range h.e.PendingPings() {
		if p.RunID != runID {
			continue
		}
		ans := PingAnswer{Answer: "approve"}
		if p.Type == event.PingClarification {
			ans = textAnswer("two")
		}
		if p.Type == event.PingSelect {
			all := []any{}
			for _, o := range p.Form.Fields[0].Options {
				all = append(all, o.ID)
			}
			ans = PingAnswer{Answer: "accept", Content: map[string]any{event.SelectField: all}}
		}
		_ = h.e.AnswerPing(p.ID, ans)
	}
}

func logHas[T event.Payload](evs []event.Event, ok func(T) bool) bool {
	for _, ev := range evs {
		if p, is := ev.Payload.(T); is && ok(p) {
			return true
		}
	}
	return false
}

// scriptAgents runs act for every agent turn, new or resumed. Each session
// announces itself at once, as a harness does, so a crash inside a turn
// leaves a session to resume.
func scriptAgents(h *harness, act func(env envport.EnvID, agent string)) {
	var mu sync.Mutex
	agents := map[string]string{}
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		mu.Lock()
		id := fmt.Sprintf("sess-%d", len(agents)+1)
		agents[id] = call.Spec.Agent
		mu.Unlock()
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: id})
		act(call.Env, call.Spec.Agent)
		return agentport.AgentResult{TurnsUsed: 1, Session: agentport.SessionID(id)}, nil
	}
	h.runner.ResumeFunc = func(_ context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		mu.Lock()
		agent := agents[string(call.Session)]
		mu.Unlock()
		act(call.Env, agent)
		return agentport.AgentResult{TurnsUsed: 1, Session: call.Session}, nil
	}
}

const crashShipDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: planner
    prompt: plan it
    outputs:
      - { name: plan, type: file }
  - name: builder
    prompt: build it
    inputs:
      - { name: failure, type: failure, optional: true }
  - name: reviewer
    prompt: review it
    outputs:
      - { name: verdict, type: verdict }
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
main:
  - { id: plan, type: agent, harness: $harness, model: $model, agent: planner, output: { plan: { to: artifacts/plan.md, type: file } } }
  - { id: plan_gate, type: human_gate, question: "Approve the plan?", input: { doc: { from: artifacts/plan.md, type: file } }, on_reject: { to: plan, max: 3 } }
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder, input: { failure: { from: edge.failure, type: failure, optional: true } } }
  - { id: verify, type: command, run: "./verify.sh", on_fail: { to: build, max: 2 } }
  - { id: review, type: agent, harness: $harness, model: $model, agent: reviewer, output: { verdict: { to: artifacts/verdict.json, type: verdict } }, on_reject: { to: build, max: 2 } }
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
  - { id: merged, type: wait, for: mr_merged, on_fail: abort, on_success: done }
`

// An agent that stops on a question and is resumed with the answer.
const crashAskDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build it
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: verify, type: command, run: "./verify.sh", on_success: done }
`

// askWorld has the agent ask once, whatever a crash repeats; a turn that
// finds the question on the log has its answer coming and finishes.
func askWorld(h *harness) {
	scriptAgents(h, func(envport.EnvID, string) {
		ids, _ := h.store.RunIDs(context.Background())
		evs, _ := h.store.Events(context.Background(), ids[0], 0)
		if logHas(evs, func(*event.PingRaised) bool { return true }) {
			return
		}
		_, _ = h.live.Load().RaisePing(ids[0], event.PingClarification, "one endpoint or two?", "", nil, nil)
	})
}

// A confirmed watcher round on the merge request, then the outcome.
const crashWatcherDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
watchers:
  - id: conflicts
    on: { signal: mr_conflicts, start: confirm }
    stages:
      - { id: sync, type: command, run: "./sync.sh" }
outcomes:
  - id: shipped
    on: mr_merged
    then: done
`

func redactWorld(h *harness) {
	scriptAgents(h, func(env envport.EnvID, agent string) {
		if agent == "redactor" {
			h.envp.WriteFile(context.Background(), env, "artifacts/mr.md", []byte("# Crash\n"), 0o644) //nolint:errcheck
		}
	})
}

// The engine merging, then the merge ending the run.
const crashMergeDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
watchers:
  - id: automerge
    on: mr_mergeable
    stages:
      - { id: merge, type: action, action: mr_merge, with: { method: squash, branch: delete }, on_fail: end }
outcomes:
  - id: shipped
    on: mr_merged
    then: done
`

// A review round: threads loaded, fixed, answered on the merge request.
// crashOutcomeDoc: an outcome takes the cursor from a chain parked at a gate
// nobody answers.
const crashOutcomeDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
  - { id: final, type: human_gate, question: "Keep going?" }
  - { id: never, type: command, run: "./never.sh" }
outcomes:
  - id: merged
    on: mr_merged
    then: done
    stages:
      - { id: wrap, type: command, run: "./wrap.sh" }
`

// crashOverrideDoc: a merge stops the chain, whose agent is on a question.
const crashOverrideDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
  - name: asker
    prompt: ask something
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
watchers:
  - id: curious
    on: mr_head_moved
    stages:
      - { id: asking, type: agent, harness: $harness, model: $model, agent: asker }
      - { id: never, type: command, run: "./never.sh" }
outcomes:
  - id: merged
    on: mr_merged
    then: done
    stages:
      - { id: wrap, type: command, run: "./wrap.sh" }
`

func overrideWorld(h *harness) {
	scriptAgents(h, func(env envport.EnvID, agent string) {
		if agent == "redactor" {
			h.envp.WriteFile(context.Background(), env, "artifacts/mr.md", []byte("# Crash\n"), 0o644) //nolint:errcheck
			return
		}
		ids, _ := h.store.RunIDs(context.Background())
		evs, _ := h.store.Events(context.Background(), ids[0], 0)
		if !logHas(evs, func(*event.PingRaised) bool { return true }) {
			_, _ = h.live.Load().RaisePing(ids[0], event.PingClarification, "which one?", "", nil, nil)
		}
	})
}

const crashReviewDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
  - name: builder
    prompt: fix it
    inputs:
      - { name: comments, type: mr_threads }
  - name: replier
    prompt: answer them
    inputs:
      - { name: comments, type: mr_threads }
    outputs:
      - { name: replies, type: review_replies }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
watchers:
  - id: review_fixes
    on: { signal: mr_review_settled, max: 1 }
    stages:
      - { id: load, type: action, action: load_review_comments, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } } }
      - { id: fix, type: agent, harness: $harness, model: $model, agent: builder, input: { comments: { from: artifacts/threads.yaml, type: mr_threads } } }
      - { id: reply, type: agent, harness: $harness, model: $model, agent: replier, input: { comments: { from: artifacts/threads.yaml, type: mr_threads } }, output: { replies: { to: artifacts/replies.yaml, type: review_replies } } }
      - { id: post, type: action, action: reply_review_comments, input: { replies: { from: artifacts/replies.yaml, type: review_replies } } }
outcomes:
  - id: shipped
    on: mr_merged
    then: done
`

func reviewWorld(h *harness) {
	scriptAgents(h, func(env envport.EnvID, agent string) {
		switch agent {
		case "redactor":
			h.envp.WriteFile(context.Background(), env, "artifacts/mr.md", []byte("# Crash\n"), 0o644) //nolint:errcheck
		case "replier":
			h.envp.WriteFile(context.Background(), env, "artifacts/replies.yaml", []byte("- { thread: d1, did: fixed, reply: renamed }\n"), 0o644) //nolint:errcheck
		}
	})
}

// shipWorld fails verify until a build ran after a failure, and rejects the
// review until a build ran after a rejection, whatever a crash repeats: a
// fault is the state of the work, not the nth call.
func shipWorld(h *harness) {
	var mu sync.Mutex
	failed, fixed, rejected, addressed := false, false, false, false
	act := func(env envport.EnvID, agent string) {
		mu.Lock()
		defer mu.Unlock()
		switch agent {
		case "planner":
			h.envp.WriteFile(context.Background(), env, "artifacts/plan.md", []byte("the plan\n"), 0o644) //nolint:errcheck
		case "builder":
			fixed = failed
			addressed = rejected
		case "reviewer":
			v := `{"verdict":"APPROVE"}`
			if !addressed {
				rejected = true
				v = `{"verdict":"REJECT","summary":"not yet"}`
			}
			h.envp.WriteFile(context.Background(), env, "artifacts/verdict.json", []byte(v), 0o644) //nolint:errcheck
		case "redactor":
			h.envp.WriteFile(context.Background(), env, "artifacts/mr.md", []byte("# Crash\n"), 0o644) //nolint:errcheck
		}
	}
	scriptAgents(h, act)
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if !strings.Contains(cmd.Script, "./verify.sh") {
			return envport.ExecResult{}, nil
		}
		mu.Lock()
		defer mu.Unlock()
		if !fixed {
			failed = true
			return envport.ExecResult{ExitCode: 1, Output: "verify failed\n"}, nil
		}
		return envport.ExecResult{Output: "ok\n"}, nil
	}
}

func crashScenarios() []crashScenario {
	mrOpen := func(evs []event.Event) bool {
		return logHas(evs, func(p *event.PhaseCompleted) bool { return p.Phase == flow.MainPhase })
	}
	return []crashScenario{
		{
			name: "ship", doc: crashShipDoc, world: shipWorld, want: run.Completed,
			drive: func(h *harness, runID string, evs []event.Event) {
				approveAll(h, runID)
				if logHas(evs, func(p *event.PhaseParked) bool { return p.StageID == "merged" }) {
					h.repoHost.SetMRState(1, repoport.MRMerged)
				}
			},
		},
		{
			name: "ask", doc: crashAskDoc, world: askWorld, want: run.Completed,
			drive: func(h *harness, runID string, _ []event.Event) { approveAll(h, runID) },
		},
		{
			name: "watcher", doc: crashWatcherDoc, world: redactWorld, want: run.Completed,
			drive: func(h *harness, runID string, evs []event.Event) {
				switch {
				case logHas(evs, func(p *event.PhaseCompleted) bool { return p.Phase == "conflicts" }):
					h.repoHost.SetMRState(1, repoport.MRMerged)
				case mrOpen(evs):
					h.repoHost.SetTargetSHA(1, "t1")
					h.repoHost.SetMergeable(1, repoport.MergeConflicts, "h1")
					approveAll(h, runID)
				}
			},
		},
		{
			name: "merge", doc: crashMergeDoc, world: redactWorld, want: run.Completed,
			drive: func(h *harness, _ string, evs []event.Event) {
				if mrOpen(evs) {
					h.repoHost.SetHead(1, "h1")
					h.repoHost.SetMergeableNow(1, true)
				}
			},
		},
		{
			name: "outcome", doc: crashOutcomeDoc, world: redactWorld, want: run.Completed,
			drive: func(h *harness, _ string, evs []event.Event) {
				if logHas(evs, func(p *event.PhaseParked) bool { return p.StageID == "final" }) {
					h.repoHost.SetMRState(1, repoport.MRMerged)
				}
			},
		},
		{
			name: "override", doc: crashOverrideDoc, world: overrideWorld, want: run.Completed,
			drive: func(h *harness, _ string, evs []event.Event) {
				switch {
				case logHas(evs, func(*event.PingRaised) bool { return true }):
					h.repoHost.SetMRState(1, repoport.MRMerged)
					_ = h.live.Load().OutcomesOnce(context.Background())
				case mrOpen(evs):
					h.repoHost.SetHead(1, "h1")
				}
			},
		},
		{
			name: "review", doc: crashReviewDoc, world: reviewWorld, want: run.Completed,
			drive: func(h *harness, runID string, evs []event.Event) {
				approveAll(h, runID)
				switch {
				case logHas(evs, func(p *event.PhaseCompleted) bool { return p.Phase == "review_fixes" }):
					h.repoHost.SetMRState(1, repoport.MRMerged)
				case mrOpen(evs) && len(h.repoHost.SnapshotThreads(1)) == 0:
					h.repoHost.AddComment(1, "d1", "a.go", "rev", "rename this", time.Now().Add(-time.Hour))
				}
			},
		},
	}
}

// crashOutcome is what a run left, for comparing.
type crashOutcome struct {
	state     run.State
	completed []string // stages, in completion order
	mrs       int
	replies   int // what the host holds beyond the reviewer's comments
	evs       []event.Event
}

// playCrash runs one scenario: n events, then the crash (n == 0: no crash).
func playCrash(t *testing.T, sc crashScenario, n int) crashOutcome {
	t.Helper()
	var cs *crashStore
	store := func(s *event.MemStore) event.Store {
		if n == 0 {
			return s
		}
		cs = newCrashStore(s, n)
		return cs
	}
	fl := mustFlow(t, sc.doc)
	h := newHarnessOnWith(t, store, nil, fl)
	sc.world(h)
	h.live.Store(h.e)
	ctx := context.Background()

	runID, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "Crash"}, fl.Name, testProject)
	if err != nil && !errors.Is(err, errCrashed) {
		t.Fatal(err)
	}
	var alive atomic.Bool
	alive.Store(true)
	deadline := time.Now().Add(10 * time.Second)
	settle := func(stop func() bool) []event.Event {
		for {
			evs := h.events(t, runID)
			if len(evs) > 0 && run.Reduce(evs).State.Terminal() || stop() {
				return evs
			}
			if time.Now().After(deadline) {
				t.Fatalf("no end: state %s after %v", run.Reduce(evs).State, eventTypes(evs))
			}
			if alive.Load() {
				sc.drive(h, runID, evs)
			}
			time.Sleep(time.Millisecond)
		}
	}
	dead := func() bool { return cs != nil && cs.isDead() }
	evs := settle(dead)
	if dead() {
		alive.Store(false)
		h.e.Shutdown()
		alive.Store(true)
		e2 := New(Deps{Config: Config{MRPollInterval: 5 * time.Millisecond}, Store: h.store,
			Resolver: resolution.StaticResolver{R: h.res}, Flows: h.flows, Settings: h.settings})
		t.Cleanup(e2.Shutdown)
		h.e = e2
		h.live.Store(e2)
		if err := e2.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		evs = settle(func() bool { return false })
	}
	h.e.Wait(runID)
	h.e.Shutdown()
	evs = h.events(t, runID)
	out := crashOutcome{state: run.Reduce(evs).State, mrs: len(h.repoHost.SnapshotMRs()), evs: evs}
	if out.mrs > 0 {
		for _, th := range h.repoHost.SnapshotThreads(1) {
			out.replies += len(th.Comments) - 1
		}
	}
	for _, ev := range evs {
		if p, ok := ev.Payload.(*event.StageCompleted); ok {
			out.completed = append(out.completed, p.StageID)
		}
	}
	return out
}

func TestCrashAtEveryEvent(t *testing.T) {
	for _, sc := range crashScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			clean := playCrash(t, sc, 0)
			if clean.state != sc.want {
				t.Fatalf("clean run ended %s: %v", clean.state, eventTypes(clean.evs))
			}
			for n := 1; n < len(clean.evs); n++ {
				name := fmt.Sprintf("%02d-%s", n, clean.evs[n-1].Type)
				t.Run(name, func(t *testing.T) {
					got := playCrash(t, sc, n)
					if got.state != clean.state {
						t.Errorf("ended %s, want %s: %v", got.state, clean.state, eventTypes(got.evs))
					}
					if !slices.Equal(got.completed, clean.completed) {
						t.Errorf("stages completed %v, want %v", got.completed, clean.completed)
					}
					if got.mrs != clean.mrs {
						t.Errorf("%d merge requests, want %d", got.mrs, clean.mrs)
					}
					if got.replies != clean.replies {
						t.Errorf("%d replies on the host, want %d", got.replies, clean.replies)
					}
					checkOnce(t, got.evs)
				})
			}
		})
	}
}

// checkOnce holds what no crash may repeat: an attempt's completion, a gate's
// question, a merge request's creation.
func checkOnce(t *testing.T, evs []event.Event) {
	t.Helper()
	seen := map[string]bool{}
	once := func(k string) {
		if seen[k] {
			t.Errorf("%s twice: %v", k, eventTypes(evs))
		}
		seen[k] = true
	}
	iter := map[string]int{}
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case *event.StageStarted:
			iter[p.StageID] = p.Iteration
		case *event.StageCompleted:
			once(fmt.Sprintf("stage_completed %s#%d", p.StageID, iter[p.StageID]))
		case *event.PingRaised:
			if p.Type == event.PingGate {
				once(fmt.Sprintf("gate ping %s#%d", p.StageID, iter[p.StageID]))
			}
		case *event.MRCreated:
			once("mr_created " + p.Repo)
		}
	}
}
