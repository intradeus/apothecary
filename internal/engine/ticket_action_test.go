package engine

// Tracker actions (ticket_*): what a flow does to its ticket's tracker — the
// issue it was imported from, through the fake remote, or the ticket itself,
// through the internal tracker (tracker_world_test.go).

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// linkedTicket is a ticket imported from the fake tracker: its run knows
// which integration to write through and which issue to write to.
var linkedTicket = runinput.Ticket{TicketID: "tk-1", Title: "T", SourceID: "int-1", SourceKind: "fake",
	SourceKey: "DEV-1", SourceURL: "https://tracker.local/browse/DEV-1"}

// trackerHarness is a harness whose linkedTicket lives on a fake remote
// tracker: its tracker actions write there.
func trackerHarness(t *testing.T, doc string) (*harness, *fake.TicketSource) {
	t.Helper()
	h := newHarness(t, mustFlow(t, doc))
	return h, h.tickets.remote
}

func newTracker() *fake.TicketSource {
	return &fake.TicketSource{
		Tickets:   []trackerport.SourceTicket{{Key: "DEV-1", Title: "T", Status: "To Do", URL: "https://tracker.local/browse/DEV-1"}},
		Reachable: map[string][]string{"To Do": {"In Progress"}, "In Progress": {"In Review", "Done"}, "In Review": {"Done"}},
	}
}

const transitionFirstDoc = `name: tk
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: kick, type: action, action: ticket_transition, with: { to: in_progress } }
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`

// The move lands before any workspace exists: a remote action with no file
// input needs none, so the issue reads "In Progress" seconds after the run
// starts, not after a build — and the run reads as working meanwhile.
func TestTicketTransitionRunsBeforeAnyWorkspace(t *testing.T) {
	h, tracker := trackerHarness(t, transitionFirstDoc)
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed || v.TicketKey != "DEV-1" || v.TicketURL != linkedTicket.SourceURL {
		t.Fatalf("state = %s key = %q url = %q; events: %v", v.State, v.TicketKey, v.TicketURL, eventTypes(evs))
	}
	if got := strings.Join(tracker.Calls, "; "); got != "transition DEV-1 In Progress" {
		t.Errorf("tracker calls = %q", got)
	}
	types := eventTypes(evs)
	done, requested := slices.Index(types, "stage_completed"), slices.Index(types, "env_requested")
	if done < 0 || requested < done {
		t.Errorf("env_requested must follow the tracker stage: %v", types)
	}
	if countType(evs, event.TypeEnvRequested) != 1 {
		t.Errorf("env_requested count = %d, want 1 (announced once, when the build needed it)", countType(evs, event.TypeEnvRequested))
	}
	started := slices.Index(types, "stage_started")
	if v := run.Reduce(evs[:started+1]); v.State != run.Running {
		t.Errorf("state after the first stage_started = %s, want running", v.State)
	}
}

// The model check still runs when the workspace arrives after a remote
// stage: a leading tracker action must not count as "a stage already ran".
func TestModelCheckSurvivesALeadingRemoteStage(t *testing.T) {
	doc := `name: tkm
version: 2
parameters: { harness: fake, model: acme/alpha-1 }
main:
  - { id: kick, type: action, action: ticket_transition, with: { to: in_progress } }
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`
	h, _ := trackerHarness(t, doc)
	h.res.Runner = &listingRunner{AgentRunner: h.runner, models: []string{"acme/alpha-2"}}
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "tkm", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if rf, ok := findPayload[*event.RunFailed](evs); !ok || !strings.Contains(rf.Reason, "acme/alpha-1") {
		t.Fatalf("run_failed = %+v; events %v", rf, eventTypes(evs))
	}
	if len(h.runner.SnapshotRunCalls()) != 0 {
		t.Error("the agent ran on a model it does not offer")
	}
}

func TestTicketTransitionAlreadyThereIsANoop(t *testing.T) {
	h, tracker := trackerHarness(t, transitionFirstDoc)
	tracker.Tickets[0].Status = "In Progress"
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s", v.State)
	}
	if got := strings.Join(tracker.Calls, "; "); got != "transition DEV-1 In Progress (already there)" {
		t.Errorf("tracker calls = %q", got)
	}
}

const routedDoc = `name: routed
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: kick, type: action, action: ticket_transition, with: { to: %s }, on_success: build, on_fail: { to: build, max: 1 } }
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`

// A status the workflow cannot reach is the flow's wiring, not the tracker's
// fault: input_invalid, with the reachable statuses named, routed down
// on_fail — here straight to the next stage, which is how "best effort" is
// written.
func TestTicketTransitionUnreachableRoutesOnFail(t *testing.T) {
	h, tracker := trackerHarness(t, fmt.Sprintf(routedDoc, "done"))
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "routed", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s, want completed via on_fail: %v", v.State, eventTypes(evs))
	}
	sf, ok := findPayload[*event.StageFailed](evs)
	if !ok || sf.Reason != event.ReasonInputInvalid || !strings.Contains(sf.Error, "reachable: In Progress") {
		t.Errorf("stage_failed = %+v", sf)
	}
	if len(tracker.Calls) != 0 {
		t.Errorf("an unreachable move was recorded as a call: %v", tracker.Calls)
	}
	// With no on_fail the stage failure ends the run.
	h2, _ := trackerHarness(t, strings.Replace(transitionFirstDoc, "to: in_progress", "to: done", 1))
	runID2, err := h2.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h2.e.Wait(runID2)
	if v := h2.view(t, runID2); v.State != run.Abandoned {
		t.Errorf("state without on_fail = %s, want abandoned", v.State)
	}
}

// A tracker that is down is infra — but still routable, never terminal: the
// work must not depend on Jira being up.
func TestTrackerTransportErrorIsRoutable(t *testing.T) {
	h, tracker := trackerHarness(t, fmt.Sprintf(routedDoc, "in_progress"))
	tracker.WriteErr = errors.New("jira: 503 service unavailable")
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "routed", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	if sf, ok := findPayload[*event.StageFailed](evs); !ok || sf.Reason != event.ReasonInfra || !strings.Contains(sf.Error, "503") {
		t.Errorf("stage_failed = %+v", sf)
	}
}

// A flow step is the flow's word; the tracker's status is whichever of its
// statuses holds the step — here a workflow that calls in_progress "Doing" —
// so one flow fits workflows that differ.
func TestFlowStepMovesToTheStatusHoldingIt(t *testing.T) {
	h, tracker := trackerHarness(t, transitionFirstDoc)
	h.tickets.steps(map[string]trackerport.FlowStep{"To Do": trackerport.StepTodo, "Doing": trackerport.StepInProgress})
	tracker.Reachable = map[string][]string{"To Do": {"Doing"}}
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, runID)))
	}
	if got := strings.Join(tracker.Calls, "; "); got != "transition DEV-1 Doing" {
		t.Errorf("tracker calls = %q", got)
	}
}

// A step no status of the tracker holds — GitHub Issues knows open and
// closed — skips the stage with the reason: the flow is not wrong, this
// tracker just cannot say it. Nothing fails, nothing is called.
func TestStepWithNoStatusSkipsTheStage(t *testing.T) {
	h, tracker := trackerHarness(t, transitionFirstDoc)
	tracker.Tickets[0].Status = "open"
	h.tickets.steps(map[string]trackerport.FlowStep{"open": trackerport.StepTodo, "closed": trackerport.StepDone})
	tracker.Reachable = nil
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	sc, ok := findPayload[*event.StageCompleted](evs)
	if !ok || sc.StageID != "kick" || !sc.Skipped || !strings.Contains(sc.Note, `has no status for step "in_progress"`) || !strings.Contains(sc.Note, "todo, done") {
		t.Errorf("first completion = %+v, want the move skipped with the steps the tracker has in the note", sc)
	}
	if countType(evs, event.TypeStageFailed) != 0 || len(tracker.Calls) != 0 {
		t.Errorf("an unsupported step failed or called: %v / %v", eventTypes(evs), tracker.Calls)
	}
}

// `status:` is the tracker's own word, taken verbatim: it moves the issue
// when the workflow allows, and an unreachable one still fails the stage
// (input_invalid) — a literal is never skipped.
func TestLiteralStatusMovesOrFails(t *testing.T) {
	h, tracker := trackerHarness(t, strings.Replace(transitionFirstDoc, "to: in_progress", `status: "in progress"`, 1))
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, runID)))
	}
	if got := strings.Join(tracker.Calls, "; "); got != "transition DEV-1 In Progress" {
		t.Errorf("tracker calls = %q", got)
	}

	h2, tracker2 := trackerHarness(t, strings.Replace(transitionFirstDoc, "to: in_progress", `status: "Ready for prod"`, 1))
	runID2, err := h2.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h2.e.Wait(runID2)
	evs := h2.events(t, runID2)
	if v := h2.view(t, runID2); v.State != run.Abandoned {
		t.Fatalf("state = %s, want abandoned: %v", v.State, eventTypes(evs))
	}
	if sf, ok := findPayload[*event.StageFailed](evs); !ok || sf.Reason != event.ReasonInputInvalid || !strings.Contains(sf.Error, `"Ready for prod" is not reachable`) {
		t.Errorf("stage_failed = %+v", sf)
	}
	if len(tracker2.Calls) != 0 {
		t.Errorf("an unreachable literal was recorded as a call: %v", tracker2.Calls)
	}
}

// A ticket written by hand is on the internal tracker: `to: done` moves the
// ticket's own status to the one holding done, and the remote is untouched —
// so one flow serves every ticket.
func TestTicketTransitionMovesAHandWrittenTicket(t *testing.T) {
	h, tracker := trackerHarness(t, strings.Replace(transitionFirstDoc, "to: in_progress", "to: done", 1))
	h.tickets.handWritten("tk-2")
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{TicketID: "tk-2", Title: "T"}, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	if sc, ok := findPayload[*event.StageCompleted](evs); !ok || sc.StageID != "kick" || sc.Skipped {
		t.Errorf("first completion = %+v, want the move made", sc)
	}
	in, err := h.tickets.conf.InternalTracker(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	done, err := h.tickets.status(in.ID, trackerport.StepDone)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.tickets.ticket("tk-2"); got.StatusID != done.ID {
		t.Errorf("status = %q (%s), want %q", got.StatusID, h.tickets.statusName("tk-2"), done.Name)
	}
	if len(tracker.Calls) != 0 {
		t.Errorf("the remote was called for a hand-written ticket: %v", tracker.Calls)
	}
}

// Run creation refuses a flow with tracker actions on a ticket whose
// tracker cannot write — naming the stage — and accepts the same flow on a
// ticket whose tracker can.
func TestCreateRunRefusesATrackerThatCannotWrite(t *testing.T) {
	h, _ := trackerHarness(t, transitionFirstDoc)
	world := h.res.TicketTracker
	h.res.TicketTracker = func(ctx context.Context, id string) (trackerport.TicketWriter, string, error) {
		if id == linkedTicket.TicketID {
			return nil, "", fmt.Errorf("integration %s (jira) can only read its tracker, not write to it", linkedTicket.SourceID)
		}
		return world(ctx, id)
	}
	_, err := h.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
	if err == nil || !strings.Contains(err.Error(), `stage "kick"`) || !strings.Contains(err.Error(), "can only read") {
		t.Errorf("err = %v, want the stage and the integration named", err)
	}
	if _, err := h.e.CreateRun(context.Background(), runinput.Ticket{TicketID: "tk-2", Title: "T"}, "tk", testProject); err != nil {
		t.Errorf("hand-written ticket refused: %v", err)
	}
	// A build with no tracker at all cannot run the flow for any ticket.
	h.res.TicketTracker = nil
	if _, err := h.e.CreateRun(context.Background(), runinput.Ticket{TicketID: "tk-3", Title: "T"}, "tk", testProject); err == nil || !strings.Contains(err.Error(), "cannot") {
		t.Errorf("no-tracker build: err = %v", err)
	}
}

// A rerun is a fresh attempt under the same ticket, tracker issue included.
func TestRerunKeepsTheTicketSource(t *testing.T) {
	h, tracker := trackerHarness(t, transitionFirstDoc)
	ctx := context.Background()
	runID, err := h.e.CreateRun(ctx, linkedTicket, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	tracker.Tickets[0].Status = "To Do"
	id2, err := h.e.Rerun(ctx, runID, runinput.Input{})
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id2)
	if v := h.view(t, id2); v.State != run.Completed || v.TicketKey != "DEV-1" || v.TicketSourceID != "int-1" {
		t.Errorf("rerun view = state %s key %q source %q", v.State, v.TicketKey, v.TicketSourceID)
	}
	if n := len(tracker.Calls); n != 2 {
		t.Errorf("tracker calls = %v, want one move per run", tracker.Calls)
	}
}

const commentDoc = `name: cm
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: write, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/ticket_comment.md, type: file } } }
  - { id: post,  type: action, action: ticket_comment, input: { body: { from: artifacts/ticket_comment.md, type: file } } }
`

func TestTicketCommentPostsTheArtifactWithATrailer(t *testing.T) {
	h, tracker := trackerHarness(t, commentDoc)
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/ticket_comment.md", "Plan approved; building.\n")
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "cm", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	posted := tracker.Posted["DEV-1"]
	want := "Plan approved; building.\n\n— apothecary · run " + runID + "/main-1 · stage post"
	if len(posted) != 1 || posted[0] != want {
		t.Errorf("posted = %q, want %q", posted, want)
	}
	if countType(evs, event.TypeActionStarted) != 1 {
		t.Error("the comment's intent was not recorded before posting")
	}
}

// A crash between posting the comment and recording the stage leaves the
// intent pending; recovery looks for the trailer on the issue and adopts the
// comment instead of posting it twice. The same path covers a stage that
// failed after its post landed and is retried: the trailer names no attempt.
func TestTicketCommentReconcilesAfterCrashOrFailedAttempt(t *testing.T) {
	for _, tc := range []struct {
		name string
		tail []event.Payload
	}{
		{"crash in the record window", nil},
		{"failed attempt retried", []event.Payload{&event.StageFailed{StageID: "post", Reason: event.ReasonInfra, Error: "jira: 503"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fl := mustFlow(t, commentDoc)
			h := newHarness(t, fl)
			tracker := h.tickets.remote
			ctx := context.Background()
			if err := tracker.Comment(ctx, "DEV-1", "hello\n\n— apothecary · run r-cm/main-1 · stage post"); err != nil {
				t.Fatal(err)
			}
			tracker.Calls = nil
			history := []event.Payload{
				&event.RunCreated{TicketTitle: "T", FlowName: "cm", ProjectID: testProject, FlowHash: fl.Hash, Branch: "apothecary/r-cm",
					TicketID: linkedTicket.TicketID, TicketSourceID: "int-1", TicketSourceKind: "fake", TicketSourceKey: "DEV-1"},
				&event.EnvRequested{},
				&event.EnvReady{EnvID: "env-cm"},
				&event.StageStarted{StageID: "write", Iteration: 1},
				&event.StageCompleted{StageID: "write"},
				&event.StageStarted{StageID: "post", Iteration: 1},
				&event.ActionStarted{StageID: "post", Action: "ticket_comment"},
			}
			for _, p := range append(history, tc.tail...) {
				if _, err := h.store.Append(ctx, "r-cm", p); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.e.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			h.e.Wait("r-cm")
			if v := h.view(t, "r-cm"); v.State != run.Completed {
				sf, _ := findPayload[*event.StageFailed](h.events(t, "r-cm"))
				t.Fatalf("state = %s (%+v): %v", v.State, sf, eventTypes(h.events(t, "r-cm")))
			}
			if n := len(tracker.Posted["DEV-1"]); n != 1 {
				t.Errorf("issue holds %d comments, want the one already there — recovery posted a duplicate", n)
			}
			if slices.Contains(tracker.Calls, "comment DEV-1") {
				t.Errorf("recovery posted: %v", tracker.Calls)
			}
		})
	}
}

const setFieldDoc = `name: sf
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: write, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/feature_flags.txt, type: file } } }
  - { id: flags, type: action, action: ticket_set_field, with: { field: "Feature flags", mode: append }, input: { value: { from: artifacts/feature_flags.txt, type: file } } }
  - { id: team,  type: action, action: ticket_set_field, with: { field: Team, text: Platform } }
`

func TestTicketSetFieldFromArtifactAndLiteral(t *testing.T) {
	h, tracker := trackerHarness(t, setFieldDoc)
	tracker.FieldValues = map[string]map[string]string{"DEV-1": {"Feature flags": "ff_a, ff_b"}}
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/feature_flags.txt", "ff_b, ff_c\n")
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "sf", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, runID)))
	}
	got := tracker.FieldValues["DEV-1"]
	if got["Feature flags"] != "ff_a, ff_b, ff_c" {
		t.Errorf("append merged to %q", got["Feature flags"])
	}
	if got["Team"] != "Platform" {
		t.Errorf("literal set %q", got["Team"])
	}
	// Append is a union: the same run again changes nothing.
	if m := mergeItems("ff_a, ff_b, ff_c", "ff_b, ff_c"); m != "ff_a, ff_b, ff_c" {
		t.Errorf("mergeItems is not idempotent: %q", m)
	}
}

// On a hand-written ticket the internal tracker writes the row: labels are
// its one field (append merges into them), a branch link lands on the
// ticket, and a field it does not have skips the stage with the reason
// instead of failing.
func TestTrackerActionsOnAHandWrittenTicket(t *testing.T) {
	doc := `name: hw
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: tag,   type: action, action: ticket_set_field, with: { field: Labels, mode: append, text: "ui, backend" } }
  - { id: team,  type: action, action: ticket_set_field, with: { field: Team, text: Platform } }
  - { id: link,  type: action, action: ticket_link_branch }
`
	h, tracker := trackerHarness(t, doc)
	h.tickets.handWritten("tk-2")
	if _, err := h.tickets.conf.SetTicketLabels(context.Background(), "tk-2", []string{"backend"}, false); err != nil {
		t.Fatal(err)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{TicketID: "tk-2", Title: "T"}, "hw", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	if got := h.tickets.ticket("tk-2").Labels; !slices.Equal(got, []string{"backend", "ui"}) {
		t.Errorf("labels = %v, want backend and ui merged", got)
	}
	skipped := map[string]string{}
	for _, e := range evs {
		if sc, ok := e.Payload.(*event.StageCompleted); ok && sc.Skipped {
			skipped[sc.StageID] = sc.Note
		}
	}
	if len(skipped) != 1 || !strings.Contains(skipped["team"], `no field "Team"`) {
		t.Errorf("skipped = %v, want only team, naming the field", skipped)
	}
	links, err := h.tickets.conf.TicketLinks(context.Background(), "tk-2")
	if err != nil || len(links) != 1 || !strings.HasPrefix(links[0].Title, "Branch") {
		t.Errorf("links = %+v (%v)", links, err)
	}
	if len(tracker.Calls) != 0 {
		t.Errorf("the remote was called: %v", tracker.Calls)
	}
}

func TestTicketSetFieldUnknownFieldIsInputInvalid(t *testing.T) {
	doc := `name: sfu
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: team, type: action, action: ticket_set_field, with: { field: Squad, text: Platform } }
`
	h, tracker := trackerHarness(t, doc)
	tracker.KnownFields = []string{"Feature flags", "Team"}
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "sfu", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Abandoned {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	if sf, ok := findPayload[*event.StageFailed](evs); !ok || sf.Reason != event.ReasonInputInvalid || !strings.Contains(sf.Error, `"Squad"`) {
		t.Errorf("stage_failed = %+v", sf)
	}
}

func TestTicketLinkAttachesTheMergeRequest(t *testing.T) {
	doc := `name: lk
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr,     type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
  - { id: link,   type: action, action: ticket_link }
`
	fl := mustFlow(t, doc)
	if len(fl.Warnings) != 0 {
		t.Errorf("a link after create_mr warned: %v", fl.Warnings)
	}
	h, tracker := trackerHarness(t, doc)
	h.agentScript(map[string]func(fake.RunCall) (agentport.AgentResult, error){
		"redactor": func(c fake.RunCall) (agentport.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Add the thing\n\nbody")
			return agentport.AgentResult{}, nil
		},
	})
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "lk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, runID)))
	}
	links := tracker.Linked["DEV-1"]
	if len(links) != 1 || links[0].URL != h.repoHost.MRs[0].Ref.URL || !strings.HasPrefix(links[0].Title, "Merge request: ") {
		t.Errorf("links = %+v", links)
	}

	// No merge request yet: input_missing, and the parser already warned.
	first := `name: lk1
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: link, type: action, action: ticket_link }
`
	fl1 := mustFlow(t, first)
	if len(fl1.Warnings) != 1 || !strings.Contains(fl1.Warnings[0], "needs a merge request") {
		t.Errorf("warnings = %v", fl1.Warnings)
	}
	h1, _ := trackerHarness(t, first)
	runID1, err := h1.e.CreateRun(context.Background(), linkedTicket, "lk1", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h1.e.Wait(runID1)
	if sf, ok := findPayload[*event.StageFailed](h1.events(t, runID1)); !ok || sf.Reason != event.ReasonInputMissing {
		t.Errorf("stage_failed = %+v", sf)
	}
}

// The branch link needs no merge request — only the branch on the
// repository host, whose own page for it is what the issue gets.
func TestTicketLinkBranchAttachesTheBranch(t *testing.T) {
	doc := `name: lb
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: link, type: action, action: ticket_link_branch }
`
	if fl := mustFlow(t, doc); len(fl.Warnings) != 0 {
		t.Errorf("a branch link warned: %v", fl.Warnings)
	}
	h, tracker := trackerHarness(t, doc)
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "lb", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	v := h.view(t, runID)
	if v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, runID)))
	}
	links := tracker.Linked["DEV-1"]
	if len(links) != 1 || links[0].URL != "https://gitlab.local/tree/"+v.Branch || links[0].Title != "Branch: "+v.Branch {
		t.Errorf("links = %+v (branch %s)", links, v.Branch)
	}

	// Not pushed yet: input_missing, and nothing is linked.
	h1, tracker1 := trackerHarness(t, doc)
	h1.repoHost.Unpushed = true
	runID1, err := h1.e.CreateRun(context.Background(), linkedTicket, "lb", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h1.e.Wait(runID1)
	if sf, ok := findPayload[*event.StageFailed](h1.events(t, runID1)); !ok || sf.Reason != event.ReasonInputMissing || !strings.Contains(sf.Error, "push_branch") {
		t.Errorf("stage_failed = %+v", sf)
	}
	if n := len(tracker1.Linked["DEV-1"]); n != 0 {
		t.Errorf("linked %d before the push", n)
	}
}

// "Done once the merge request is accepted" is a stage after the final
// gate. By then the workspace may be gone — and the move needs none, so
// nothing is re-provisioned for it.
func TestTicketDoneAfterTheFinalGateNeedsNoWorkspace(t *testing.T) {
	doc := `name: dn
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: build,  type: agent, harness: $harness, model: $model, agent: builder }
  - { id: gate,   type: human_gate, question: "Merge?", on_success: finish, on_reject: abort }
  - { id: finish, type: action, action: ticket_transition, with: { to: done } }
`
	h, tracker := trackerHarness(t, doc)
	tracker.Tickets[0].Status = "In Review"
	ctx := context.Background()
	runID, err := h.e.CreateRun(ctx, linkedTicket, "dn", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p := h.waitPing(t, PingGate)
	// The workspace was reaped while the gate waited.
	v := h.view(t, runID)
	if err := h.envp.Destroy(ctx, envport.EnvID(v.EnvID)); err != nil {
		t.Fatal(err)
	}
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	if got := strings.Join(tracker.Calls, "; "); got != "transition DEV-1 Done" {
		t.Errorf("tracker calls = %q", got)
	}
	if n := countType(evs, event.TypeEnvReady); n != 1 {
		t.Errorf("env_ready count = %d: the Done move re-provisioned a workspace it does not need", n)
	}
}
