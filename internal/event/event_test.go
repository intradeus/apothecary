package event

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestAppendAssignsMonotonicSeqPerRun(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()

	ev1, err := s.Append(ctx, "r1", &RunCreated{TicketTitle: "a"}, &EnvRequested{})
	if err != nil {
		t.Fatal(err)
	}
	ev2, _ := s.Append(ctx, "r1", &EnvReady{EnvID: "env-1"})
	ev3, _ := s.Append(ctx, "r2", &RunCreated{TicketTitle: "b"})

	if ev1[0].Seq != 1 || ev1[1].Seq != 2 || ev2[0].Seq != 3 {
		t.Errorf("r1 seqs = %d,%d,%d, want 1,2,3", ev1[0].Seq, ev1[1].Seq, ev2[0].Seq)
	}
	if ev3[0].Seq != 1 {
		t.Errorf("r2 starts at seq %d, want 1", ev3[0].Seq)
	}

	got, _ := s.Events(ctx, "r1", 1)
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 {
		t.Errorf("Events(after=1) = %+v", got)
	}

	ids, _ := s.RunIDs(ctx)
	if len(ids) != 2 || ids[0] != "r1" || ids[1] != "r2" {
		t.Errorf("RunIDs = %v", ids)
	}
}

func TestAppendRejectsEmptyRunID(t *testing.T) {
	s := NewMemStore()
	if _, err := s.Append(context.Background(), "", &EnvRequested{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestWatchReplaysThenStreams(t *testing.T) {
	s := NewMemStore()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.Append(ctx, "r1", &RunCreated{}, &EnvRequested{})
	ch, err := s.Watch(ctx, "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if e := <-ch; e.Type != TypeRunCreated || e.Seq != 1 {
		t.Errorf("replay[0] = %s seq %d", e.Type, e.Seq)
	}
	if e := <-ch; e.Type != TypeEnvRequested {
		t.Errorf("replay[1] = %s", e.Type)
	}

	s.Append(ctx, "r1", &EnvReady{EnvID: "e"})
	if e := <-ch; e.Type != TypeEnvReady || e.Seq != 3 {
		t.Errorf("live = %s seq %d", e.Type, e.Seq)
	}

	cancel()
	for range ch { // must drain and close, not hang
	}
}

// TestJSONKeysMatchSpec locks the §6 payload keys — the payload jsonb column
// and every analytics query depend on them.
func TestJSONKeysMatchSpec(t *testing.T) {
	exit := 2
	cases := []struct {
		p    Payload
		want string
	}{
		{&RunCreated{TicketTitle: "t", TicketGoal: "g", FlowName: "v1", FlowHash: "h", RepoURL: "u", Branch: "b"},
			`{"ticket_title":"t","ticket_goal":"g","flow_name":"v1","flow_hash":"h","repo_url":"u","branch":"b"}`},
		{&EnvReady{EnvID: "e", Links: []Link{{Kind: "ide", URL: "u"}}},
			`{"env_id":"e","links":[{"kind":"ide","url":"u"}]}`},
		{&StageStarted{StageID: "build", Iteration: 1, Harness: "opencode", Model: "a/b"},
			`{"stage_id":"build","iteration":1,"harness":"opencode","model":"a/b"}`},
		{&StageCompleted{StageID: "build", DurationMS: 5, TurnsUsed: 3, Usage: &Usage{InputTokens: 1, OutputTokens: 2, CostUSD: 0.5}, SessionID: "s"},
			`{"stage_id":"build","duration_ms":5,"turns_used":3,"usage":{"input_tokens":1,"output_tokens":2,"cost_usd":0.5},"session_id":"s"}`},
		{&StageFailed{StageID: "test", Reason: ReasonExitNonzero, ExitCode: &exit},
			`{"stage_id":"test","reason":"exit_nonzero","exit_code":2}`},
		{&EdgeTaken{From: "test", To: "build", Edge: "on_fail", Count: 1, Max: 5, Feed: "FAIL: x"},
			`{"from":"test","to":"build","edge":"on_fail","count":1,"max":5,"feed":"FAIL: x"}`},
		{&EdgeTaken{From: "test", To: "build", Edge: "on_fail", Count: 1, Max: 3, Depth: 1},
			`{"from":"test","to":"build","edge":"on_fail","count":1,"max":3,"depth":1}`},
		{&PingRaised{PingID: "p", Type: "clarification", Question: "q", Context: "ctx", Files: []string{"artifacts/plan.md"}},
			`{"ping_id":"p","type":"clarification","question":"q","context":"ctx","files":["artifacts/plan.md"]}`},
		{&PingRaised{PingID: "g", Type: "gate", Question: "q", Link: "https://git.local/mr/1"},
			`{"ping_id":"g","type":"gate","question":"q","link":"https://git.local/mr/1"}`},
		{&PingAnswered{PingID: "p", Answer: "approve", AttachmentIDs: []string{"a1"},
			Review: []ReviewComment{{Path: "artifacts/plan.md", StartLine: 2, EndLine: 3, Quote: "q", Body: "b", AttachmentIDs: []string{"a1"}}}},
			`{"ping_id":"p","answer":"approve","attachment_ids":["a1"],"review":[{"path":"artifacts/plan.md","start_line":2,"end_line":3,"quote":"q","body":"b","attachment_ids":["a1"]}]}`},
		{&SnapshotCaptured{StageID: "plan", Iteration: 2, Kind: "output", Name: "plan", Path: "artifacts/plan.md",
			SnapshotID: "sn1", Filename: "plan.md", Mime: "text/markdown", SHA256: "aa", Size: 5},
			`{"stage_id":"plan","iteration":2,"kind":"output","name":"plan","path":"artifacts/plan.md","snapshot_id":"sn1","filename":"plan.md","mime":"text/markdown","sha256":"aa","size":5}`},
		{&VerdictReceived{StageID: "review", Approve: false, NComments: 2},
			`{"stage_id":"review","approve":false,"n_comments":2}`},
		{&MRCreated{MRURL: "u", MRIID: 7, Branch: "b"},
			`{"mr_url":"u","mr_iid":7,"branch":"b"}`},
		{&RunCompleted{DurationMS: 9, TotalCostUSD: 1.5, TotalTurns: 40, UnwantedPings: 1},
			`{"duration_ms":9,"total_cost_usd":1.5,"total_turns":40,"unwanted_pings":1}`},
		{&RunAbandoned{StalledAt: "test", Edge: "on_fail", Reason: "edge exhausted"},
			`{"stalled_at":"test","edge":"on_fail","reason":"edge exhausted"}`},
		{&RunFailed{Reason: "aborted"}, `{"reason":"aborted"}`},
		{&RunPaused{StageID: "build"}, `{"stage_id":"build"}`},
		{&RunUnpaused{}, `{}`},
		{&EnvDestroyed{EnvID: "e"}, `{"env_id":"e"}`},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.p.EventType(), got, tc.want)
		}
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	e := Event{
		RunID: "r1", Seq: 4, TS: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
		Type: TypeStageFailed, Payload: &StageFailed{StageID: "test", Reason: ReasonTimeout},
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var back Event
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	sf, ok := back.Payload.(*StageFailed)
	if !ok || sf.Reason != ReasonTimeout || back.Seq != 4 || !back.TS.Equal(e.TS) {
		t.Errorf("round trip = %+v (payload %+v)", back, back.Payload)
	}
}

// The contract deliberately changed here (DESIGN-FILES follow-up): an unknown
// type used to error, which meant one retired event type bricked recovery and
// rebuild-projections for every database that contained it.
func TestUnmarshalPayloadUnknownType(t *testing.T) {
	p, err := UnmarshalPayload("nope", []byte(`{}`))
	if err != nil {
		t.Fatalf("unknown event type must decode as inert history, got: %v", err)
	}
	if _, ok := p.(*Unknown); !ok {
		t.Fatalf("payload = %#v, want *Unknown", p)
	}
}

// A retired event type (artifact_published) or one from a newer binary must
// decode as inert history, not brick recovery/rebuild: the log is append-only
// and outlives any codec. The raw payload round-trips verbatim so SSE replay
// shows old events untouched.
func TestUnknownEventTypeDecodesInert(t *testing.T) {
	raw := []byte(`{"stage_id":"plan","iteration":1,"scope":"stage_output","artifact_id":"cap1"}`)
	p, err := UnmarshalPayload("artifact_published", raw)
	if err != nil {
		t.Fatalf("retired type errored: %v", err)
	}
	u, ok := p.(*Unknown)
	if !ok || u.EventType() != "artifact_published" {
		t.Fatalf("payload = %#v, want *Unknown", p)
	}
	out, err := json.Marshal(p)
	if err != nil || string(out) != string(raw) {
		t.Errorf("re-marshal = %s (%v), want the original payload verbatim", out, err)
	}
	// A KNOWN type with a garbage payload still errors loudly — that is
	// corruption, not version skew.
	if _, err := UnmarshalPayload(TypeRunCreated, []byte(`{`)); err == nil {
		t.Error("corrupt known payload decoded without error")
	}
}
