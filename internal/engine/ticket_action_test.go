package engine

// Tracker actions (ticket_*): what a flow does to the issue its ticket was
// imported from, through the fake tracker.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// linkedTicket is a ticket imported from the fake tracker: its run knows
// which integration to write through and which issue to write to.
var linkedTicket = Ticket{TicketID: "tk-1", Title: "T", SourceID: "int-1", SourceKind: "fake",
	SourceKey: "DEV-1", SourceURL: "https://tracker.local/browse/DEV-1"}

// trackerHarness is a harness whose resolution writes to one fake tracker,
// whatever the integration id.
func trackerHarness(t *testing.T, doc string) (*harness, *fake.TicketSource) {
	t.Helper()
	h := newHarness(t, mustFlow(t, doc))
	tracker := newTracker()
	h.res.TicketWriter = func(context.Context, string) (ports.TicketWriter, error) { return tracker, nil }
	return h, tracker
}

func newTracker() *fake.TicketSource {
	return &fake.TicketSource{
		Tickets:   []ports.SourceTicket{{Key: "DEV-1", Title: "T", Status: "To Do", URL: "https://tracker.local/browse/DEV-1"}},
		Reachable: map[string][]string{"To Do": {"In Progress"}, "In Progress": {"In Review", "Done"}, "In Review": {"Done"}},
	}
}

const transitionFirstDoc = `name: tk
version: 2
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: kick, type: action, action: ticket_transition, input: { to: in_progress } }
  - { id: build, type: agent, agent: builder }
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
parameters: { harness: fake, model: zen/glm-5.1 }
stages:
  - { id: kick, type: action, action: ticket_transition, input: { to: in_progress } }
  - { id: build, type: agent, agent: builder }
`
	h, _ := trackerHarness(t, doc)
	h.res.Runner = &listingRunner{AgentRunner: h.runner, models: []string{"zen/glm-5.2"}}
	runID, err := h.e.CreateRun(context.Background(), linkedTicket, "tkm", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if rf, ok := findPayload[*event.RunFailed](evs); !ok || !strings.Contains(rf.Reason, "zen/glm-5.1") {
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
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: kick, type: action, action: ticket_transition, input: { to: %s }, on_success: build, on_fail: { to: build, max: 1 } }
  - { id: build, type: agent, agent: builder }
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

// A canonical status is the flow's word; the tracker's is the adapter's.
// The fake translates in_progress to whatever its map says — here a
// workflow that calls it "Doing" — so one flow fits workflows that differ.
func TestCanonicalStatusIsMappedForTheTracker(t *testing.T) {
	h, tracker := trackerHarness(t, transitionFirstDoc)
	tracker.StatusMap = map[ports.TicketStatus]string{ports.StatusTodo: "To Do", ports.StatusInProgress: "Doing"}
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

// A tracker with no word for the status — GitHub Issues knows open and
// closed — skips the stage with the reason, like a ticket with no tracker:
// the flow is not wrong, this tracker just cannot say it. Nothing fails,
// nothing is called.
func TestUnsupportedCanonicalStatusSkipsTheStage(t *testing.T) {
	h, tracker := trackerHarness(t, transitionFirstDoc)
	tracker.Tickets[0].Status = "open"
	tracker.StatusMap = map[ports.TicketStatus]string{ports.StatusTodo: "open", ports.StatusDone: "closed"}
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
	if !ok || sc.StageID != "kick" || !sc.Skipped || !strings.Contains(sc.Note, `has no status for "in_progress"`) || !strings.Contains(sc.Note, "todo, done") {
		t.Errorf("first completion = %+v, want the move skipped with the tracker's vocabulary in the note", sc)
	}
	if countType(evs, event.TypeStageFailed) != 0 || len(tracker.Calls) != 0 {
		t.Errorf("an unsupported status failed or called: %v / %v", eventTypes(evs), tracker.Calls)
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

// A ticket written by hand has no tracker issue: the stage completes as
// skipped — visibly — and the flow goes on, so one flow serves every ticket.
func TestTrackerActionSkipsATicketWrittenByHand(t *testing.T) {
	h, tracker := trackerHarness(t, transitionFirstDoc)
	runID, err := h.e.CreateRun(context.Background(), Ticket{TicketID: "tk-2", Title: "T"}, "tk", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := h.view(t, runID); v.State != run.Completed {
		t.Fatalf("state = %s: %v", v.State, eventTypes(evs))
	}
	sc, ok := findPayload[*event.StageCompleted](evs)
	if !ok || sc.StageID != "kick" || !sc.Skipped || !strings.Contains(sc.Note, "written by hand") {
		t.Errorf("first completion = %+v, want the tracker stage skipped with a note", sc)
	}
	if len(tracker.Calls) != 0 {
		t.Errorf("the tracker was called for an unlinked ticket: %v", tracker.Calls)
	}
}

// Run creation refuses a flow with tracker actions on a ticket whose
// integration cannot write — naming the stage — and accepts the same flow on
// a ticket with no tracker at all (those stages will skip).
func TestCreateRunRefusesATrackerThatCannotWrite(t *testing.T) {
	h, _ := trackerHarness(t, transitionFirstDoc)
	h.res.TicketWriter = func(_ context.Context, id string) (ports.TicketWriter, error) {
		return nil, fmt.Errorf("integration %s (jira) can only read its tracker, not write to it", id)
	}
	_, err := h.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
	if err == nil || !strings.Contains(err.Error(), `stage "kick"`) || !strings.Contains(err.Error(), "can only read") {
		t.Errorf("err = %v, want the stage and the integration named", err)
	}
	if _, err := h.e.CreateRun(context.Background(), Ticket{TicketID: "tk-2", Title: "T"}, "tk", testProject); err != nil {
		t.Errorf("unlinked ticket refused: %v", err)
	}
	// A build with no writer at all cannot run the flow for any ticket.
	h.res.TicketWriter = nil
	if _, err := h.e.CreateRun(context.Background(), Ticket{TicketID: "tk-2", Title: "T"}, "tk", testProject); err == nil || !strings.Contains(err.Error(), "cannot") {
		t.Errorf("no-writer build: err = %v", err)
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
	id2, err := h.e.Rerun(ctx, runID, RunInput{})
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
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: write, type: agent, agent: redactor, output: { mr: artifacts/ticket_comment.md } }
  - { id: post,  type: action, action: ticket_comment, input: { body: artifacts/ticket_comment.md } }
`

func TestTicketCommentPostsTheArtifactWithATrailer(t *testing.T) {
	h, tracker := trackerHarness(t, commentDoc)
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"redactor": func(c fake.RunCall) (ports.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/ticket_comment.md", "Plan approved; building.\n")
			return ports.AgentResult{}, nil
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
			tracker := newTracker()
			tracker.Posted = map[string][]string{"DEV-1": {"hello\n\n— apothecary · run r-cm/main-1 · stage post"}}
			h.res.TicketWriter = func(context.Context, string) (ports.TicketWriter, error) { return tracker, nil }
			ctx := context.Background()
			history := []event.Payload{
				&event.RunCreated{TicketTitle: "T", FlowName: "cm", ProjectID: testProject, FlowHash: fl.Hash, Branch: "apothecary/r-cm",
					TicketSourceID: "int-1", TicketSourceKind: "fake", TicketSourceKey: "DEV-1"},
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
				t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, "r-cm")))
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
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: write, type: agent, agent: redactor, output: { mr: artifacts/feature_flags.txt } }
  - { id: flags, type: action, action: ticket_set_field, input: { field: "Feature flags", value: artifacts/feature_flags.txt, mode: append } }
  - { id: team,  type: action, action: ticket_set_field, input: { field: Team, text: Platform } }
`

func TestTicketSetFieldFromArtifactAndLiteral(t *testing.T) {
	h, tracker := trackerHarness(t, setFieldDoc)
	tracker.FieldValues = map[string]map[string]string{"DEV-1": {"Feature flags": "ff_a, ff_b"}}
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"redactor": func(c fake.RunCall) (ports.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/feature_flags.txt", "ff_b, ff_c\n")
			return ports.AgentResult{}, nil
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

func TestTicketSetFieldUnknownFieldIsInputInvalid(t *testing.T) {
	doc := `name: sfu
version: 2
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: team, type: action, action: ticket_set_field, input: { field: Squad, text: Platform } }
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
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: redact, type: agent, agent: redactor }
  - { id: mr,     type: action, action: create_mr, input: { body: artifacts/mr.md } }
  - { id: link,   type: action, action: ticket_link }
`
	fl := mustFlow(t, doc)
	if len(fl.Warnings) != 0 {
		t.Errorf("a link after create_mr warned: %v", fl.Warnings)
	}
	h, tracker := trackerHarness(t, doc)
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"redactor": func(c fake.RunCall) (ports.AgentResult, error) {
			h.writeFile(t, c.Env, "artifacts/mr.md", "# Add the thing\n\nbody")
			return ports.AgentResult{}, nil
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
	if len(links) != 1 || links[0].URL != h.forge.MRs[0].Ref.URL || !strings.HasPrefix(links[0].Title, "Merge request: ") {
		t.Errorf("links = %+v", links)
	}

	// No merge request yet: input_missing, and the parser already warned.
	first := `name: lk1
version: 2
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
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

// "Done once the merge request is accepted" is a stage after the final
// gate. By then the workspace may be gone — and the move needs none, so
// nothing is re-provisioned for it.
func TestTicketDoneAfterTheFinalGateNeedsNoWorkspace(t *testing.T) {
	doc := `name: dn
version: 2
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build,  type: agent, agent: builder }
  - { id: gate,   type: human_gate, question: "Merge?", on_success: finish, on_reject: abort }
  - { id: finish, type: action, action: ticket_transition, input: { to: done } }
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
	if err := h.envp.Destroy(ctx, ports.EnvID(v.EnvID)); err != nil {
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
