package engine

// More worlds for TestCrashAtEveryEvent: what a stage does outside the run's
// log — a tracker write, a pushed branch, a posted review, a started service
// — must land exactly once whichever event the process died after, and a run
// that fails must fail the same way crashed or not.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// Every tracker action in one chain, the merge request between them.
const crashTrackerDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: writer
    prompt: write it
    outputs:
      - { name: note, type: file }
      - { name: value, type: file }
      - { name: mr, type: file }
main:
  - { id: kick, type: action, action: ticket_transition, with: { to: in_progress } }
  - { id: write, type: agent, harness: $harness, model: $model, agent: writer,
      output: { note: { to: artifacts/note.md, type: file }, value: { to: artifacts/value.txt, type: file }, mr: { to: artifacts/mr.md, type: file } } }
  - { id: comment, type: action, action: ticket_comment, input: { body: { from: artifacts/note.md, type: file } } }
  - { id: field, type: action, action: ticket_set_field, with: { field: Notes }, input: { value: { from: artifacts/value.txt, type: file } } }
  - { id: branch, type: action, action: ticket_link_branch }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
  - { id: link, type: action, action: ticket_link }
  - { id: finish, type: action, action: ticket_transition, with: { to: done }, on_success: done }
`

func trackerWorld(tracker **fake.TicketSource) func(h *harness) {
	return func(h *harness) {
		*tracker = h.tickets.remote
		scriptAgents(h, func(env envport.EnvID, _ string) {
			for path, body := range map[string]string{"artifacts/note.md": "the note\n", "artifacts/value.txt": "the value\n", "artifacts/mr.md": "# Crash\n"} {
				h.envp.WriteFile(context.Background(), env, path, []byte(body), 0o644) //nolint:errcheck
			}
		})
	}
}

// Committing and publishing: the push must not be repeated into a lease it
// already moved.
const crashPublishDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: committer
    prompt: commit it
    outputs:
      - { name: commit, type: commit_message }
main:
  - { id: write, type: agent, harness: $harness, model: $model, agent: committer, output: { commit: { to: artifacts/commit.yaml, type: commit_message } } }
  - { id: commit, type: action, action: commit, input: { message: { from: artifacts/commit.yaml, type: commit_message } } }
  - { id: push, type: action, action: push_branch, on_success: done }
`

// A review posted on someone's merge request, from a run started on it.
const crashPostDoc = `name: crash
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
agents:
  - name: pr_reviewer
    prompt: review it
    inputs:
      - { name: threads, type: mr_threads }
    outputs:
      - { name: review, type: review_threads }
main:
  - { id: poll, type: action, action: load_review_comments, with: { scope: all }, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } } }
  - { id: read, type: agent, harness: $harness, model: $model, agent: pr_reviewer,
      input: { threads: { from: artifacts/threads.yaml, type: mr_threads } }, output: { review: { to: artifacts/review.yaml, type: review_threads } } }
  - { id: post, type: action, action: post_review, input: { review: { from: artifacts/review.yaml, type: review_threads } }, on_success: done }
`

// A service brought up, used, and stopped by the flow.
const crashServiceDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: looker
    prompt: look at it
    inputs:
      - { name: app, type: link }
main:
  - { id: up, type: service, service: default, timeout: 2m }
  - { id: look, type: agent, harness: $harness, model: $model, agent: looker, input: { app: { from: run.service.default, type: link } } }
  - { id: down, type: service, service: default, do: stop, on_success: done }
`

// An agent whose every turn streams a transcript, and a stage's retries
// running out into the abort.
const crashFailDoc = `name: crash
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build it
    inputs:
      - { name: failure, type: failure, optional: true }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder, input: { failure: { from: edge.failure, type: failure, optional: true } } }
  - { id: verify, type: command, run: "./verify.sh", on_fail: { to: build, max: 1, then: { to: abort } } }
  - { id: never, type: command, run: "./never.sh", on_success: done }
`

// streamTurn plays a turn with a transcript in it, so a crash lands between
// the session, the text and the tool it reports.
func streamTurn(in agentport.AgentInput, id string) {
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: id})
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "looking", Cursor: "m1"})
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTool, Cursor: "t1",
		Tool: &agentport.AgentTool{ID: "t1", Title: "Read main.go", Kind: "read", Status: "in_progress"}})
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvTool, Cursor: "t2",
		Tool: &agentport.AgentTool{ID: "t1", Status: "completed", Output: "ok"}})
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvPlan, Cursor: "p1",
		Plan: []agentport.AgentPlanEntry{{Content: "look", Status: "done"}}})
	in.Events(agentport.AgentEvent{Kind: agentport.AgentEvMessage, Text: "done", Cursor: "m2"})
}

func failWorld(h *harness) {
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		streamTurn(call.In, "sess-b")
		return agentport.AgentResult{TurnsUsed: 1, Session: "sess-b"}, nil
	}
	h.runner.ResumeFunc = func(_ context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		streamTurn(call.In, string(call.Session))
		return agentport.AgentResult{TurnsUsed: 1, Session: call.Session}, nil
	}
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "./verify.sh") {
			return envport.ExecResult{ExitCode: 1, Output: "verify failed\n"}, nil
		}
		return envport.ExecResult{}, nil
	}
}

func moreCrashScenarios() []crashScenario {
	var tracker *fake.TicketSource
	var pushes atomic.Int32
	return []crashScenario{
		{
			name: "tracker", doc: crashTrackerDoc, world: trackerWorld(&tracker), want: run.Completed, ticket: linkedTicket,
			drive: func(*harness, string, []event.Event) {},
			check: func(t *testing.T, _ *harness) {
				if got := len(tracker.Posted["DEV-1"]); got != 1 {
					t.Errorf("%d comments on the issue, want 1: %v", got, tracker.Calls)
				}
				if got := tracker.FieldValues["DEV-1"]["Notes"]; !strings.Contains(got, "the value") {
					t.Errorf("field = %q", got)
				}
				if got := len(tracker.Linked["DEV-1"]); got != 2 {
					t.Errorf("%d links on the issue, want the branch and the merge request: %+v", got, tracker.Linked)
				}
				if st := tracker.Tickets[0].Status; st != "Done" {
					t.Errorf("status = %q", st)
				}
			},
		},
		{
			name: "publish", doc: crashPublishDoc, want: run.Completed,
			world: func(h *harness) {
				pushes.Store(0)
				h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
					switch {
					case strings.Contains(cmd.Script, "git rev-parse HEAD"):
						return envport.ExecResult{Output: "tip-1\n"}, nil
					case strings.HasPrefix(cmd.Script, "git push"):
						pushes.Add(1)
					}
					return envport.ExecResult{}, nil
				}
				scriptAgents(h, func(env envport.EnvID, _ string) {
					h.envp.WriteFile(context.Background(), env, "artifacts/commit.yaml", []byte("subject: fix the thing\n"), 0o644) //nolint:errcheck
				})
			},
			drive: func(*harness, string, []event.Event) {},
			check: func(t *testing.T, h *harness) {
				if pushes.Load() == 0 {
					t.Error("nothing was pushed")
				}
				ids, _ := h.store.RunIDs(context.Background())
				if evs := h.events(t, ids[0]); countType(evs, event.TypeBranchPushed) != 1 {
					t.Errorf("branch_pushed %d times: %v", countType(evs, event.TypeBranchPushed), eventTypes(evs))
				}
			},
		},
		{
			name: "post-review", doc: crashPostDoc, want: run.Completed,
			world: func(h *harness) {
				h.repoHost.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
				scriptAgents(h, func(env envport.EnvID, _ string) {
					h.envp.WriteFile(context.Background(), env, "artifacts/review.yaml", []byte(noteA), 0o644) //nolint:errcheck
				})
			},
			input: func(*harness) runinput.Input { return runinput.Input{MR: &runinput.MR{IID: 1}} },
			drive: func(*harness, string, []event.Event) {},
			check: func(t *testing.T, h *harness) {
				mr := h.repoHost.SnapshotMRs()[0]
				if len(mr.Reviews) != 1 || len(mr.Threads) != 1 {
					t.Errorf("%d reviews, %d threads: want the one, once", len(mr.Reviews), len(mr.Threads))
				}
			},
		},
		{
			name: "service", doc: crashServiceDoc, want: run.Completed,
			world: func(h *harness) {
				h.res.Services = []resolution.Service{{Name: "default", Start: "npm run dev",
					Health: resolution.ServiceHealth{TCP: 3000}, Ports: []resolution.ServicePort{{Port: 3000, Label: "app"}}}}
				svcs := fake.NewServices()
				h.envp.ExecFunc = svcs.Exec(nil)
				scriptAgents(h, func(envport.EnvID, string) {})
			},
			drive: func(*harness, string, []event.Event) {},
		},
		{
			name: "retries-run-out", doc: crashFailDoc, world: failWorld, want: run.Abandoned,
			drive: func(*harness, string, []event.Event) {},
		},
	}
}
