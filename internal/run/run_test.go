package run

import (
	"context"
	"reflect"
	"testing"
	"time"

	"apothecary/internal/run/event"
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
		{&event.StageStarted{StageID: "plan", Attempt: 1}, Running},
		{&event.StageCompleted{StageID: "plan"}, Running},
		{&event.PingRaised{PingID: "p1", Type: event.PingGate}, WaitingOnHuman},
		{&event.PingAnswered{PingID: "p1", Answer: "approve"}, Running},
		{&event.StageStarted{StageID: "build", Attempt: 1}, Running},
		{&event.MRCreated{MRURL: "https://gitlab.local/mr/1", MRIID: 1}, Running},
		{&event.RunCompleted{}, Completed},
		{&event.RunArchived{}, Completed},
		{&event.EnvDestroyed{EnvID: "env-1"}, Completed},
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
	if v.ArchivedAt == nil || !v.ArchivedAt.Equal(evs[len(evs)-2].TS) {
		t.Errorf("archived_at = %v, want the run_archived timestamp", v.ArchivedAt)
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
		{"env failed, leftovers", []event.Payload{&event.RunCreated{}, &event.EnvRequested{}, &event.EnvFailed{Error: "boom", EnvID: "apo-x", Partial: true}}, Failed},
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
			// What a failed provisioning left behind is the view's: archive
			// and the capacity count read it from here.
			if ef, ok := tc.ps[len(tc.ps)-1].(*event.EnvFailed); ok && v.EnvID != ef.EnvID {
				t.Errorf("env_id = %q, want %q", v.EnvID, ef.EnvID)
			}
		})
	}
}

// Archiving is cleanup, not an outcome: whatever the run ended as, it still
// reads that after run_archived, with the flag set at the archive's time. A
// second run_archived (the destroy's retry) keeps the first time.
func TestArchiveKeepsTheOutcome(t *testing.T) {
	t0 := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	ev := func(min int, p event.Payload) event.Event {
		return event.Event{RunID: "r1", Seq: int64(min + 1), Type: p.EventType(), TS: t0.Add(time.Duration(min) * time.Minute), Payload: p}
	}
	for name, end := range map[string]struct {
		p    event.Payload
		want State
	}{
		"completed": {&event.RunCompleted{}, Completed},
		"abandoned": {&event.RunAbandoned{Reason: "edge exhausted"}, Abandoned},
		"failed":    {&event.RunFailed{Reason: "aborted"}, Failed},
	} {
		t.Run(name, func(t *testing.T) {
			v := Reduce([]event.Event{
				ev(0, &event.RunCreated{}), ev(1, &event.EnvReady{EnvID: "env-1"}), ev(3, end.p), ev(4, &event.RunArchived{}),
			})
			if v.State != end.want || v.ArchivedAt == nil || !v.ArchivedAt.Equal(t0.Add(4*time.Minute)) {
				t.Fatalf("state %s archived_at %v", v.State, v.ArchivedAt)
			}
			again := ev(9, &event.RunArchived{})
			Apply(&v, &again)
			if v.State != end.want || !v.ArchivedAt.Equal(t0.Add(4*time.Minute)) {
				t.Errorf("archived again: state %s archived_at %v", v.State, v.ArchivedAt)
			}
		})
	}
	if v := Reduce([]event.Event{ev(0, &event.RunCreated{}), ev(1, &event.RunCompleted{})}); v.ArchivedAt != nil {
		t.Errorf("a run never archived reads archived at %v", v.ArchivedAt)
	}
}

// A failed destroy leaves the environment the run's (it still counts, the
// reaper retries it) and says why; a destroy that then succeeds clears it.
func TestEnvDestroyFailureKeepsTheEnv(t *testing.T) {
	v := Reduce(appendAll(t, &event.RunCreated{}, &event.EnvRequested{}, &event.EnvReady{EnvID: "apo-x"},
		&event.RunCompleted{}, &event.RunArchived{}, &event.EnvDestroyFailed{EnvID: "apo-x", Error: "pod stuck"}))
	if v.State != Completed || v.ArchivedAt == nil || v.EnvID != "apo-x" || v.EnvDestroyError != "pod stuck" {
		t.Fatalf("after a failed destroy: state %s env %q error %q", v.State, v.EnvID, v.EnvDestroyError)
	}
	v = Reduce(appendAll(t, &event.RunCreated{}, &event.EnvReady{EnvID: "apo-x"}, &event.RunArchived{},
		&event.EnvDestroyFailed{EnvID: "apo-x", Error: "pod stuck"}, &event.EnvDestroyed{EnvID: "apo-x"}))
	if v.EnvID != "" || v.EnvDestroyError != "" {
		t.Fatalf("after the retry: env %q error %q", v.EnvID, v.EnvDestroyError)
	}
	v = Reduce(appendAll(t, &event.RunCreated{}, &event.EnvReady{EnvID: "apo-x"},
		&event.EnvDestroyFailed{EnvID: "apo-x", Error: "pod stuck"}, &event.EnvReady{EnvID: "apo-x"}))
	if v.EnvDestroyError != "" {
		t.Fatalf("a workspace back up still reads as failing to go: %q", v.EnvDestroyError)
	}
}

func TestTerminal(t *testing.T) {
	for st, want := range map[State]bool{
		Pending: false, Provisioning: false, Running: false, WaitingOnHuman: false,
		Completed: true, Abandoned: true, Failed: true,
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
		{RunID: "r", Seq: 2, Type: event.TypeStageStarted, Payload: &event.StageStarted{StageID: "start", Attempt: 1}},
	})
	if v.State != Running {
		t.Errorf("state = %s, want running", v.State)
	}
	if v.TicketKey != "PROJ-1" || v.TicketURL != "https://j/browse/PROJ-1" || v.TicketSourceID != "int-1" || v.TicketSourceKind != "jira" {
		t.Errorf("ticket source not projected: %+v", v)
	}
}
