package run

import (
	"context"
	"reflect"
	"testing"

	"apothecary/internal/event"
)

// appendAll pushes payloads through a MemStore so events carry real seq/ts.
func appendAll(t *testing.T, ps ...event.Payload) []event.Event {
	t.Helper()
	s := event.NewMemStore()
	evs, err := s.Append(context.Background(), "r1", ps...)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestReduceFullLifecycle(t *testing.T) {
	steps := []struct {
		p    event.Payload
		want State
	}{
		{&event.RunCreated{TicketTitle: "Add login", TicketGoal: "goal", FlowName: "v1", FlowHash: "abc"}, Pending},
		{&event.EnvRequested{}, Provisioning},
		{&event.EnvReady{EnvID: "env-1"}, Running},
		{&event.StageStarted{StageID: "plan", Iteration: 1}, Running},
		{&event.StageCompleted{StageID: "plan"}, Running},
		{&event.PingRaised{PingID: "p1", Type: event.PingGate}, WaitingOnHuman},
		{&event.PingAnswered{PingID: "p1", Answer: "approve"}, Running},
		{&event.StageStarted{StageID: "build", Iteration: 1}, Running},
		{&event.MRCreated{MRURL: "https://gitlab.local/mr/1", MRIID: 1}, Running},
		{&event.RunCompleted{}, Completed},
		{&event.RunArchived{}, Archived},
		{&event.EnvDestroyed{EnvID: "env-1"}, Archived},
	}

	var ps []event.Payload
	for _, st := range steps {
		ps = append(ps, st.p)
	}
	evs := appendAll(t, ps...)

	var v View
	for i := range evs {
		Apply(&v, &evs[i])
		if v.State != steps[i].want {
			t.Errorf("after %s: state = %s, want %s", evs[i].Type, v.State, steps[i].want)
		}
	}

	if v.RunID != "r1" || v.TicketTitle != "Add login" || v.FlowName != "v1" || v.FlowHash != "abc" {
		t.Errorf("identity fields: %+v", v)
	}
	if v.CurrentStage != "build" || v.MRURL != "https://gitlab.local/mr/1" {
		t.Errorf("stage/mr: %+v", v)
	}
	if v.EnvID != "" {
		t.Errorf("env_id should be cleared after destroy, got %q", v.EnvID)
	}
	if v.CreatedAt.IsZero() || v.UpdatedAt.Before(v.CreatedAt) {
		t.Errorf("timestamps: created %v updated %v", v.CreatedAt, v.UpdatedAt)
	}

	// Reduce must equal the incremental fold (View holds slices now, so this
	// is a deep comparison).
	if got := Reduce(evs); !reflect.DeepEqual(got, v) {
		t.Errorf("Reduce != fold:\n got %+v\nwant %+v", got, v)
	}
}

func TestReduceFailurePaths(t *testing.T) {
	cases := []struct {
		name string
		ps   []event.Payload
		want State
	}{
		{"env failed", []event.Payload{&event.RunCreated{}, &event.EnvRequested{}, &event.EnvFailed{Error: "boom"}}, Failed},
		{"abandoned", []event.Payload{&event.RunCreated{}, &event.EnvRequested{}, &event.EnvReady{}, &event.RunAbandoned{StalledAt: "test", Reason: "edge exhausted"}}, Abandoned},
		{"aborted", []event.Payload{&event.RunCreated{}, &event.EnvRequested{}, &event.EnvReady{}, &event.RunFailed{Reason: "aborted"}}, Failed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Reduce(appendAll(t, tc.ps...))
			if v.State != tc.want {
				t.Errorf("state = %s, want %s", v.State, tc.want)
			}
			if !v.State.Terminal() {
				t.Error("state should be terminal")
			}
		})
	}
}

func TestTerminal(t *testing.T) {
	for st, want := range map[State]bool{
		Pending: false, Provisioning: false, Running: false, WaitingOnHuman: false,
		Completed: true, Abandoned: true, Failed: true, Archived: true,
	} {
		if st.Terminal() != want {
			t.Errorf("%s.Terminal() = %v", st, !want)
		}
	}
}

// Several merge requests (a stack, a multi-repo change) are all on the view,
// in creation order, and mr_url stays the first; old runs with no recorded
// checkouts still project their one primary.
func TestViewCollectsMergeRequestsAndCheckouts(t *testing.T) {
	v := Reduce([]event.Event{
		{RunID: "r", Seq: 1, Type: event.TypeRunCreated, Payload: &event.RunCreated{Branch: "b", SourceBranch: "main", RepoURL: "u"}},
		{RunID: "r", Seq: 2, Type: event.TypeMRCreated, Payload: &event.MRCreated{MRURL: "u1", MRIID: 1, Branch: "b-1", Target: "main"}},
		{RunID: "r", Seq: 3, Type: event.TypeMRCreated, Payload: &event.MRCreated{MRURL: "u2", MRIID: 2, Branch: "b-2", Target: "b-1", Repo: "repo"}},
	})
	if len(v.MRs) != 2 || v.MRURL != "u1" || v.MRs[1].Target != "b-1" || v.MRs[0].Repo != "repo" {
		t.Errorf("MRs = %+v, mr_url %q", v.MRs, v.MRURL)
	}
	if len(v.Checkouts) != 1 || !v.Checkouts[0].Primary || v.Checkouts[0].Source != "main" || v.Checkouts[0].URL != "u" {
		t.Errorf("checkouts = %+v", v.Checkouts)
	}
}

// A stage may start before any workspace exists (a remote tracker action
// first in the flow): the run is then working, not pending — pending means
// "queued, never started", and recovery would re-queue it as such.
func TestStageStartedLeavesPending(t *testing.T) {
	v := Reduce([]event.Event{
		{RunID: "r", Seq: 1, Type: event.TypeRunCreated, Payload: &event.RunCreated{TicketTitle: "T",
			TicketSourceID: "int-1", TicketSourceKind: "jira", TicketSourceKey: "PROJ-1", TicketSourceURL: "https://j/browse/PROJ-1"}},
		{RunID: "r", Seq: 2, Type: event.TypeStageStarted, Payload: &event.StageStarted{StageID: "start", Iteration: 1}},
	})
	if v.State != Running {
		t.Errorf("state = %s, want running", v.State)
	}
	if v.TicketKey != "PROJ-1" || v.TicketURL != "https://j/browse/PROJ-1" || v.TicketSourceID != "int-1" || v.TicketSourceKind != "jira" {
		t.Errorf("ticket source not projected: %+v", v)
	}
}
