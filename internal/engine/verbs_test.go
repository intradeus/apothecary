package engine

// The verbs a person has over a run, and what each refuses: a refusal is the
// only thing standing between a click and a log the run cannot explain.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
	"apothecary/internal/workitem/ticket"
)

// seed writes a run's history straight to the log: a run in a state with no
// loop, without driving one there.
func (h *harness) seed(t *testing.T, runID string, ps ...event.Payload) {
	t.Helper()
	if _, err := h.store.Append(context.Background(), runID, ps...); err != nil {
		t.Fatal(err)
	}
}

// created is the run_created of a run of the harness's "loop" flow.
func created(project string) *event.RunCreated {
	return &event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: project, Branch: "feat/x"}
}

// wantErrText fails unless err says want.
func wantErrText(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want it to say %q", err, want)
	}
}

// blockRun starts a run whose agent works until ctx ends or release closes.
func blockRun(t *testing.T, h *harness, flowName string) (string, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	h.runner.RunFunc = func(ctx context.Context, _ fake.RunCall) (agentport.AgentResult, error) {
		select {
		case <-release:
			return agentport.AgentResult{TurnsUsed: 1}, nil
		case <-ctx.Done():
			return agentport.AgentResult{}, ctx.Err()
		}
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, flowName, testProject)
	if err != nil {
		t.Fatal(err)
	}
	return id, release
}

func TestVerbsRefuseAnActiveRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	id, release := blockRun(t, h, "loop")
	defer func() { close(release); h.e.Wait(id) }()
	waitUntil(t, "the run to be active", func() bool { return h.e.active(id) })
	ctx := context.Background()
	wantErrText(t, h.e.Archive(ctx, id), "is still active; abort it first")
	_, err := h.e.Rerun(ctx, id, runinput.Input{})
	wantErrText(t, err, "is still active; abort it first")
	wantErrText(t, h.e.RetryRun(ctx, id), "is still active")
	wantErrText(t, h.e.Play(ctx, id), "is still active")
	if got := h.e.ActiveRunIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("active runs = %v", got)
	}
}

func TestVerbsOnAnUnknownRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	_, rerr := h.e.Rerun(ctx, "ghost", runinput.Input{})
	for name, err := range map[string]error{
		"archive": h.e.Archive(ctx, "ghost"),
		"rerun":   rerr,
		"retry":   h.e.RetryRun(ctx, "ghost"),
		"play":    h.e.Play(ctx, "ghost"),
		"abort":   h.e.Abort("ghost"),
	} {
		if err == nil || !strings.Contains(err.Error(), "run ghost not found") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := h.e.Pause("ghost"); !errors.Is(err, ErrRunNotActive) {
		t.Errorf("pause: err = %v", err)
	}
}

func TestArchive(t *testing.T) {
	ctx := context.Background()
	t.Run("only a terminal run", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seed(t, "r1", created(testProject), &event.RunPaused{Reason: event.PauseUser})
		wantErrText(t, h.e.Archive(ctx, "r1"), "only terminal runs can be archived")
		if countType(h.events(t, "r1"), event.TypeRunArchived) != 0 {
			t.Error("a paused run was archived")
		}
	})
	t.Run("once", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seed(t, "r1", created(testProject), &event.RunCompleted{})
		if err := h.e.Archive(ctx, "r1"); err != nil {
			t.Fatal(err)
		}
		wantErrText(t, h.e.Archive(ctx, "r1"), "is already archived")
		if evs := h.events(t, "r1"); countType(evs, event.TypeRunArchived) != 1 || countType(evs, event.TypeEnvDestroyed) != 0 {
			t.Errorf("events = %v", eventTypes(evs))
		}
	})
	t.Run("a workspace that won't go keeps its record", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seed(t, "r1", created(""), &event.EnvReady{EnvID: "env-1"}, &event.RunCompleted{})
		if err := h.e.Archive(ctx, "r1"); err != nil {
			t.Fatal(err)
		}
		v := h.view(t, "r1")
		if v.State != run.Archived || v.EnvID != "env-1" {
			t.Errorf("view = %s env %q: an env not destroyed must stay on record", v.State, v.EnvID)
		}
	})
	t.Run("destroys the workspace and its uploads", func(t *testing.T) {
		atts, _, _ := newFiles(t)
		h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Attachments = failingAttachments{atts} }, mustFlow(t, loopFlowDoc))
		id := completedWithEnv(t, h)
		env := h.view(t, id).EnvID
		if err := h.e.Archive(ctx, id); err != nil {
			t.Fatalf("a failed cleanup must not fail the archive: %v", err)
		}
		v := h.view(t, id)
		if v.State != run.Archived || v.EnvID != "" {
			t.Errorf("view = %s env %q", v.State, v.EnvID)
		}
		if len(h.envp.Destroyed) != 1 || string(h.envp.Destroyed[0]) != env {
			t.Errorf("destroyed = %v", h.envp.Destroyed)
		}
	})
	t.Run("a resolver failure leaves the env on record", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		id := completedWithEnv(t, h)
		h.e.resolver = failingResolver{errors.New("vault sealed")}
		if err := h.e.Archive(ctx, id); err != nil {
			t.Fatal(err)
		}
		if v := h.view(t, id); v.State != run.Archived || v.EnvID == "" {
			t.Errorf("view = %+v", v)
		}
	})
}

type failingAttachments struct{ *filestore.Attachments }

func (failingAttachments) DeleteForRun(context.Context, string) error {
	return errors.New("disk full")
}

type failingResolver struct{ err error }

func (r failingResolver) Resolve(context.Context, string) (*resolution.Resolution, error) {
	return nil, r.err
}

func TestRerun(t *testing.T) {
	ctx := context.Background()
	t.Run("only a terminal run", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seed(t, "r1", created(testProject), &event.RunPaused{Reason: event.PauseUser})
		_, err := h.e.Rerun(ctx, "r1", runinput.Input{})
		wantErrText(t, err, "only terminal runs can be restarted")
	})
	t.Run("a run with no project", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seed(t, "r1", created(""), &event.RunFailed{Reason: "x"})
		_, err := h.e.Rerun(ctx, "r1", runinput.Input{})
		wantErrText(t, err, "has no project; cannot restart")
	})
	t.Run("a fresh attempt: its own id, branch and direction", func(t *testing.T) {
		const paramsDoc = `name: loop
version: 2
parameters:
  harness: fake
  model: fake/model
  region: { type: string, default: eu }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder, input: { ticket: { from: run.ticket, type: ticket } } }
`
		h := newHarness(t, mustFlow(t, paramsDoc))
		first, err := h.e.CreateRunWith(ctx, runinput.Ticket{Title: "T", TicketID: "tk-1", Branch: "feat/t", SourceKey: "DEV-1"},
			"loop", testProject, runinput.Input{Params: map[string]string{"region": "us"}})
		if err != nil {
			t.Fatal(err)
		}
		h.e.Wait(first)
		second, err := h.e.Rerun(ctx, first, runinput.Input{SourceBranch: "release", Goal: "try the other way", By: "automation:nightly"})
		if err != nil {
			t.Fatal(err)
		}
		h.e.Wait(second)
		a, b := h.view(t, first), h.view(t, second)
		if second == first || a.Branch == b.Branch || !strings.HasPrefix(b.Branch, "feat/t-") {
			t.Errorf("branches %q, %q", a.Branch, b.Branch)
		}
		if b.SourceBranch != "release" || b.RunGoal != "try the other way" {
			t.Errorf("rerun = source %q goal %q", b.SourceBranch, b.RunGoal)
		}
		if b.Params["region"] != "us" {
			t.Errorf("the rerun dropped the original's parameter: %v", b.Params)
		}
		if b.TicketID != "tk-1" || b.TicketKey != "DEV-1" {
			t.Errorf("the rerun lost its ticket: %+v", b)
		}
		// Overridden, and history unchanged.
		third, err := h.e.Rerun(ctx, second, runinput.Input{Params: map[string]string{"region": "ap"}})
		if err != nil {
			t.Fatal(err)
		}
		h.e.Wait(third)
		if got := h.view(t, third).Params["region"]; got != "ap" {
			t.Errorf("override = %q", got)
		}
		if h.view(t, first).Params["region"] != "us" {
			t.Error("a rerun rewrote its original")
		}
		// The ticket's runs, oldest to newest attempt, all found.
		runs, err := h.e.RunsOf(ctx, ticket.Ref(testProject, "tk-1"))
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 3 {
			t.Errorf("runs of the ticket = %d", len(runs))
		}
		if none, err := h.e.RunsOf(ctx, ticket.Ref(testProject, "tk-2")); err != nil || len(none) != 0 {
			t.Errorf("runs of another ticket = %v, %v", none, err)
		}
	})
	t.Run("a flow override drops the old flow's parameters", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, `name: loop
version: 2
parameters:
  harness: fake
  model: fake/model
  region: { type: string, default: eu }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`), mustFlow(t, `name: other
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`))
		first, err := h.e.CreateRunWith(ctx, runinput.Ticket{Title: "T"}, "loop", testProject, runinput.Input{Params: map[string]string{"region": "us"}})
		if err != nil {
			t.Fatal(err)
		}
		h.e.Wait(first)
		second, err := h.e.Rerun(ctx, first, runinput.Input{Flow: "other"})
		if err != nil {
			t.Fatal(err)
		}
		h.e.Wait(second)
		if v := h.view(t, second); v.FlowName != "other" || v.Params["region"] != "" {
			t.Errorf("rerun = flow %q params %v", v.FlowName, v.Params)
		}
	})
}

func TestRetryRefusals(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.seed(t, "done", created(testProject), &event.RunCompleted{})
	wantErrText(t, h.e.RetryRun(ctx, "done"), "only abandoned or failed runs can be retried")

	h.seed(t, "nostage", created(testProject), &event.RunFailed{Reason: "x"})
	wantErrText(t, h.e.RetryRun(ctx, "nostage"), "has no stage to retry")

	h.seed(t, "noenv", created(testProject), &event.StageStarted{StageID: "build", Iteration: 1}, &event.RunFailed{Reason: "x"})
	wantErrText(t, h.e.RetryRun(ctx, "noenv"), "has no workspace to retry in")

	// The flow moved on and no longer has the stage it stalled at.
	h.seed(t, "gone", created(testProject), &event.EnvReady{EnvID: "e"}, &event.StageStarted{StageID: "vanished", Iteration: 1},
		&event.RunAbandoned{StalledAt: "vanished", Reason: "x"})
	wantErrText(t, h.e.RetryRun(ctx, "gone"), `flow "loop" no longer has a stage "vanished"`)
	for _, id := range []string{"done", "nostage", "noenv", "gone"} {
		if countType(h.events(t, id), event.TypeRunRetried) != 0 {
			t.Errorf("%s: a refused retry reached the log", id)
		}
	}
}

// A retry is where an edited flow reaches a run: unless the edit no longer
// resolves with the run's parameters, which keeps the pin rather than block
// the retry.
func TestRetryPinsTheCurrentFlowOrKeepsTheOldOne(t *testing.T) {
	const doc = `name: loop
version: 2
parameters:
  harness: fake
  model: fake/model
  turns: { type: int, default: 3 }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder, max_turns: $turns, on_fail: { to: abort } }
`
	h := newHarness(t, mustFlow(t, doc))
	ctx := context.Background()
	h.runner.RunFunc = func(context.Context, fake.RunCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{}, errors.New("provider down")
	}
	id, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if s := h.view(t, id).State; s != run.Abandoned && s != run.Failed {
		t.Fatalf("setup: %s", s)
	}
	h.runner.RunFunc = nil
	if err := h.e.RetryRun(ctx, id); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	rt, ok := findPayload[*event.RunRetried](h.events(t, id))
	if !ok || rt.FlowHash != h.flows["loop"].Hash || rt.StageID != "build" {
		t.Fatalf("retried = %+v", rt)
	}
	if s := h.view(t, id).State; s != run.Completed {
		t.Errorf("state after retry = %s", s)
	}
}

func TestAbortOfARunWithNoLoop(t *testing.T) {
	ctx := context.Background()
	t.Run("a parked run is abandoned and scrubbed", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		env, err := h.envp.Provision(ctx, envport.EnvSpec{})
		if err != nil {
			t.Fatal(err)
		}
		h.seed(t, "r1", created(testProject), &event.EnvReady{EnvID: string(env)},
			&event.StageStarted{StageID: "build", Iteration: 1}, &event.RunPaused{StageID: "build", Reason: event.PauseUser})
		if err := h.e.Abort("r1"); err != nil {
			t.Fatal(err)
		}
		v := h.view(t, "r1")
		if v.State != run.Abandoned {
			t.Fatalf("state = %s", v.State)
		}
		ab, _ := findPayload[*event.RunAbandoned](h.events(t, "r1"))
		if ab == nil || ab.StalledAt != "build" || ab.Reason != "aborted by user" {
			t.Errorf("abandoned = %+v", ab)
		}
		// The terminal scrub ran: a paused workspace still holds credentials.
		scrubbed := false
		for _, c := range h.envp.Execs() {
			if strings.HasPrefix(strings.TrimSpace(c.Cmd.Script), "rm -f") {
				scrubbed = true
			}
		}
		if !scrubbed {
			t.Errorf("no credential scrub among %v", h.execScripts())
		}
	})
	t.Run("twice is refused", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.seed(t, "r1", created(testProject), &event.RunCompleted{})
		if err := h.e.Abort("r1"); !errors.Is(err, ErrRunNotActive) || !strings.Contains(err.Error(), "already completed") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a queued run leaves the queue", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.settings.L.MaxConcurrentRuns = 1
		first, release := blockRun(t, h, "loop")
		second, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "U"}, "loop", testProject)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.e.Abort(second); err != nil {
			t.Fatal(err)
		}
		close(release)
		h.e.Wait(first)
		h.e.Wait(second)
		if v := h.view(t, second); v.State != run.Abandoned {
			t.Errorf("state = %s", v.State)
		}
		if countType(h.events(t, second), event.TypeStageStarted) != 0 {
			t.Errorf("the aborted run started: %v", eventTypes(h.events(t, second)))
		}
	})
}

func TestPauseRefusals(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.seed(t, "finished", created(testProject), &event.RunCompleted{})
	if err := h.e.Pause("finished"); !errors.Is(err, ErrRunNotActive) {
		t.Errorf("finished: %v", err)
	}
	h.seed(t, "paused", created(testProject), &event.RunPaused{Reason: event.PauseUser})
	err := h.e.Pause("paused")
	if !errors.Is(err, ErrRunNotActive) || !strings.Contains(err.Error(), "already paused") {
		t.Errorf("paused: %v", err)
	}
}

func TestPlay(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.seed(t, "done", created(testProject), &event.RunCompleted{})
	wantErrText(t, h.e.Play(ctx, "done"), "only paused runs can be played")

	h.seed(t, "waiting", created(testProject),
		&event.PingRaised{PingID: "p1", Type: "clarification", Question: "?"},
		&event.RunPaused{Reason: event.PauseWaiting})
	wantErrText(t, h.e.Play(ctx, "waiting"), "is waiting for an answer, not paused")
}

// Playing a paused gate lands back on waiting: no fresh ping_raised will say
// so, the event must.
func TestPlayLandsBackOnAPendingQuestion(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.settings.L.MaxConcurrentRuns = 1
	blocker, release := blockRun(t, h, "loop")
	defer func() { close(release); h.e.Wait(blocker) }()
	waitUntil(t, "a slot taken", func() bool { return h.e.active(blocker) })

	h.seed(t, "gated", created(testProject),
		&event.PingRaised{PingID: "p1", Type: "gate", Question: "ok?", AnswerKind: event.AnswerYesNo},
		&event.RunPaused{Reason: event.PauseUser})
	if err := h.e.Play(ctx, "gated"); err != nil {
		t.Fatal(err)
	}
	un, ok := findPayload[*event.RunUnpaused](h.events(t, "gated"))
	if !ok || !un.Waiting || un.Reason != event.UnpausePlay {
		t.Fatalf("unpaused = %+v", un)
	}
	if err := h.e.Abort("gated"); err != nil { // still queued: no loop to wait for
		t.Fatal(err)
	}
}

func TestCreateRunRefusals(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	for _, c := range []struct {
		name    string
		t       runinput.Ticket
		flow    string
		project string
		want    string
	}{
		{"blank title", runinput.Ticket{Title: "  "}, "loop", testProject, "ticket title is required"},
		{"no project", runinput.Ticket{Title: "T"}, "loop", "", "project_id is required"},
		{"unknown flow", runinput.Ticket{Title: "T"}, "nope", testProject, `flow "nope"`},
		{"bad branch", runinput.Ticket{Title: "T", Branch: "a..b"}, "loop", testProject, `branch "a..b"`},
		{"bad ticket branch", runinput.Ticket{Title: "T", TicketID: "t1", Branch: "/lead"}, "loop", testProject, "branch"},
		{"bad source branch", runinput.Ticket{Title: "T", SourceBranch: "x..y"}, "loop", testProject, `source branch "x..y"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := h.e.CreateRun(ctx, c.t, c.flow, c.project)
			wantErrText(t, err, c.want)
		})
	}
	ids, _ := h.store.RunIDs(ctx)
	if len(ids) != 0 {
		t.Errorf("a refused run reached the log: %v", ids)
	}

	// A resolver failure is a preflight refusal, and "" is the project's flow.
	id, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "", testProject)
	if err != nil {
		t.Fatalf("default flow: %v", err)
	}
	h.e.Wait(id)
	h.e.resolver = failingResolver{errors.New("vault sealed")}
	_, err = h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject)
	wantErrText(t, err, "preflight: vault sealed")
}

// At the cap a run is refused and the owner told which runs could free one;
// the refusal costs nothing.
func TestEnvironmentCapacity(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.settings.L.MaxEnvironments = 2
	h.res.UserID = "u1"
	h.seed(t, "finished", created(testProject), &event.EnvReady{EnvID: "e1"}, &event.RunCompleted{})
	h.seed(t, "live", created(testProject), &event.EnvReady{EnvID: "e2"}, &event.RunPaused{Reason: event.PauseUser})
	_, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject)
	if !errors.Is(err, ErrEnvCapacity) {
		t.Fatalf("err = %v", err)
	}
	wantErrText(t, err, "(2 live, max 2); archivable runs: finished")
	var sent []notifyport.Notification
	for _, n := range h.ch.Sent() {
		if n.Type == "blocker" {
			sent = append(sent, n)
		}
	}
	if len(sent) != 1 || sent[0].UserID != "u1" || !strings.Contains(sent[0].Body, "finished") {
		t.Errorf("notified = %+v", sent)
	}
	ids, _ := h.store.RunIDs(ctx)
	if len(ids) != 2 {
		t.Errorf("runs = %v", ids)
	}

	h.settings.L.MaxEnvironments = Unlimited
	id, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatalf("uncapped: %v", err)
	}
	h.e.Wait(id)
}

// A run's store failing while counting is not capacity: the caller hears it.
type failingEvents struct {
	event.Store
	err error
}

func (s failingEvents) RunIDs(context.Context) ([]string, error) { return nil, s.err }

func TestEnvCapacityReportsAStoreFailure(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.e.store = failingEvents{Store: h.store, err: errors.New("db down")}
	_, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	wantErrText(t, err, "db down")
}

// The merge request a run is created on: what can go wrong finding it, each
// refused before anything is written.
func TestCreatingOnAMergeRequest(t *testing.T) {
	ctx := context.Background()
	doc := `name: onmr
version: 2
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
main:
  - { id: fix, type: agent, harness: $harness, model: $model, agent: builder }
`
	start := func(h *harness, in *runinput.MR) (string, error) {
		return h.e.CreateRunWith(ctx, runinput.Ticket{Title: "T"}, "onmr", testProject, runinput.Input{MR: in})
	}
	h := newHarness(t, mustFlow(t, doc))
	_, err := start(h, &runinput.MR{Repo: "gone", IID: 1})
	wantErrText(t, err, `the project has no repository "gone"`)

	h.res.ExtraRepos = []resolution.ExtraRepo{{Name: "web", URL: "https://x/web.git", DefaultBranch: "main"}}
	_, err = start(h, &runinput.MR{Repo: "web", IID: 1})
	wantErrText(t, err, `no integration reaches repository "web"`)

	_, err = start(h, &runinput.MR{IID: 42})
	wantErrText(t, err, "merge request !42")

	ref := h.repoHost.AddOpenMR("alice", "t", "feat/a", "main", "abc")
	h.repoHost.SetMRState(ref.IID, repoport.MRMerged)
	_, err = start(h, &runinput.MR{IID: ref.IID})
	wantErrText(t, err, "a run starts on an open merge request")

	// A branch git would refuse is checked out under a local name.
	odd := h.repoHost.AddOpenMR("alice", "t", "weird..branch", "main", "abc")
	id, err := start(h, &runinput.MR{IID: odd.IID})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	v := h.view(t, id)
	if want := fmt.Sprintf("mr-%d", odd.IID); v.Branch != want {
		t.Errorf("branch = %q, want %q", v.Branch, want)
	}
}

// The extra repositories count in preflight, and every missing piece names
// itself.
func TestPreflightNamesWhatIsMissing(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("403")
	start := func(h *harness) error {
		_, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject)
		return err
	}
	cases := []struct {
		name string
		arm  func(h *harness)
		want string
		step string
	}{
		{"repository access", func(h *harness) { h.repoHost.AccessErr = boom }, "preflight: repository access: 403", "repo_access"},
		{"an extra repository", func(h *harness) {
			h.res.ExtraRepos = []resolution.ExtraRepo{{Name: "web"}}
			h.res.RepoHosts = map[string]repoport.RepoHost{"web": &fake.RepoHost{AccessErr: boom}}
		}, "preflight: repository access to repo web: 403", "repo_access:web"},
		{"AGENTS.md", func(h *harness) { h.repoHost.MissingFiles = map[string]bool{"AGENTS.md": true} }, "AGENTS.md not found in repo", "repo_file:AGENTS.md"},
		{"devcontainer", func(h *harness) {
			h.repoHost.MissingFiles = map[string]bool{".devcontainer/devcontainer.json": true, ".devcontainer.json": true}
		}, "repo has no .devcontainer/devcontainer.json", "devcontainer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, loopFlowDoc))
			c.arm(h)
			wantErrText(t, start(h), c.want)
			s := stepNamed(h.e.PreflightProject(ctx, testProject), c.step)
			if s == nil || s.OK || s.Error == "" {
				t.Errorf("step %s = %+v", c.step, s)
			}
		})
	}
	t.Run("a devcontainer at the other path passes", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.repoHost.MissingFiles = map[string]bool{".devcontainer/devcontainer.json": true}
		if err := start(h); err != nil {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no agent guide checks nothing", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.res.AgentsMDPath = ""
		h.repoHost.MissingFiles = map[string]bool{"AGENTS.md": true}
		if err := start(h); err != nil {
			t.Errorf("err = %v", err)
		}
		for _, s := range h.e.PreflightProject(ctx, testProject) {
			if strings.HasPrefix(s.ID, "repo_file:") {
				t.Errorf("an unset agent guide has no step: %+v", s)
			}
		}
	})
	t.Run("a project that does not resolve stops at the first step", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, loopFlowDoc))
		h.e.resolver = failingResolver{boom}
		steps := h.e.PreflightProject(ctx, testProject)
		if len(steps) != 1 || steps[0].ID != "resolve" || steps[0].OK {
			t.Errorf("steps = %+v", steps)
		}
	})
}

// fileProbeErr is a repository host whose file lookups fail.
type fileProbeErr struct {
	*fake.RepoHost
	err error
}

func (f fileProbeErr) FileExists(context.Context, repoport.RepoRef, string) (bool, error) {
	return false, f.err
}

func TestPreflightReportsAFailedLookup(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.res.RepoHost = fileProbeErr{h.repoHost, errors.New("rate limited")}
	_, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	wantErrText(t, err, "preflight: checking AGENTS.md: rate limited")
	if h.e.PreflightProject(context.Background(), testProject)[0].ID != "resolve" {
		t.Error("the steps start at resolve")
	}
}

// A stage's reference that no longer resolves fails the project's preflight
// the way it would fail a run.
func TestPreflightProjectChecksFlowRefs(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, doc string
		arm       func(h *harness)
		want      string
	}{
		{"a command the project lost", loopFlowDoc, func(h *harness) { h.res.Commands = nil }, `run_ref "verify" not found`},
		{"a harness outside the set", `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: b, type: agent, harness: other, model: $model, agent: builder }
`, nil, `stage "b" pins harness "other"`},
		{"an ask harness outside the set", `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
ask: { harness: other }
main:
  - { id: b, type: agent, harness: $harness, model: $model, agent: builder }
`, nil, `ask: harness "other"`},
		{"a service the project lost", `name: loop
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: s, type: service, service: app }
`, nil, `"app"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, c.doc))
			if c.arm != nil {
				c.arm(h)
			}
			s := stepNamed(h.e.PreflightProject(ctx, testProject), "flow_refs")
			if s == nil || s.OK || !strings.Contains(s.Error, c.want) {
				t.Errorf("flow_refs = %+v, want %q", s, c.want)
			}
			if _, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "T"}, "loop", testProject); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("create = %v, want %q", err, c.want)
			}
		})
	}
}

// The engine's preflight on a flow whose action or signal the project cannot
// serve, checked one level below: the host is what preflight dials first.
func TestFlowRefsNeedTheirIntegrations(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	res := *h.res
	res.RepoHost = nil
	action := mustFlow(t, `name: a
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: a, type: action, action: mr_merge }
`)
	wantErrText(t, h.e.preflightFlowRefs(&res, action), `stage "a"`)
	wait := mustFlow(t, `name: w
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: w, type: wait, for: mr_merged }
`)
	wantErrText(t, h.e.preflightFlowRefs(&res, wait), `stage "w"`)
}

func TestReapOnceReportsItsFailures(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.e.store = failingViews{Store: h.store, err: errors.New("db down")}
	h.e.runs = &run.Reader{Store: h.e.store}
	wantErrText(t, h.e.ReapOnce(ctx, Window(0)), "db down")
	if err := h.e.Recover(ctx); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("recover = %v", err)
	}
	if _, err := h.e.RunsOf(ctx, ticket.Ref(testProject, "t")); err == nil {
		t.Error("runs of a ticket swallowed the failure")
	}
	if _, err := h.e.ItemRuns(ctx, testProject, ticket.Name); err == nil {
		t.Error("runs of a kind swallowed the failure")
	}
}

type failingViews struct {
	event.Store
	err error
}

func (s failingViews) RunIDs(context.Context) ([]string, error) { return nil, s.err }

// A notification that fails is logged: a dead Slack webhook never fails a
// run.
func TestANotificationFailureDoesNotTouchTheRun(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.ch.Err = errors.New("webhook down")
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	if s := h.view(t, id).State; s != run.Completed {
		t.Errorf("state = %s", s)
	}
}
