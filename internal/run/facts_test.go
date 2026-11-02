package run

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"apothecary/internal/run/event"
)

var t0 = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// factsLog is a run that queued, provisioned, asked a human, failed its
// first attempt, retried it under a take-over and completed: every time
// bucket but paused, every counter but a few. Offsets are minutes from t0.
func factsLog() []event.Event {
	steps := []struct {
		min int
		p   event.Payload
	}{
		{0, &event.RunCreated{UserID: "u1", ProjectID: "p1", TicketID: "t1", FlowName: "f", FlowHash: "h", TicketSourceKind: "jira"}},
		{1, &event.EnvRequested{}},
		{3, &event.EnvReady{EnvID: "env-1"}},
		{3, &event.StageStarted{StageID: "build", Iteration: 1, Harness: "claude", Model: "m1"}},
		{5, &event.PingRaised{PingID: "p1", Type: event.PingClarification, StageID: "build"}},
		{9, &event.PingAnswered{PingID: "p1", Answer: "accept"}},
		{10, &event.StageFailed{StageID: "build", Reason: event.ReasonExitNonzero, TurnsUsed: 2, Usage: &event.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.2}}},
		{10, &event.EdgeTaken{From: "build", To: "build", Edge: "on_fail", Count: 1, Max: 3}},
		{10, &event.StageStarted{StageID: "build", Iteration: 2, Harness: "claude", Model: "m1"}},
		{11, &event.HoldTaken{StageID: "build", Iteration: 2, UserID: "u1"}},
		{13, &event.HoldReleased{StageID: "build", Iteration: 2}},
		{15, &event.StageCompleted{StageID: "build", TurnsUsed: 3, Usage: &event.Usage{InputTokens: 20, OutputTokens: 7, CostUSD: 0.3}}},
		{15, &event.RunCompleted{}},
	}
	out := make([]event.Event, len(steps))
	for i, s := range steps {
		out[i] = event.Event{RunID: "r1", Seq: int64(i + 1), TS: t0.Add(time.Duration(s.min) * time.Minute), Type: s.p.EventType(), Payload: s.p}
	}
	return out
}

func TestFactsOfARun(t *testing.T) {
	v := Reduce(factsLog())
	f := v.Facts
	m := int64(time.Minute / time.Millisecond)
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"queued", f.QueuedMS, 1 * m},
		{"provisioning", f.ProvisioningMS, 2 * m},
		{"waiting human", f.WaitingHumanMS, 4 * m},
		{"held", f.HeldMS, 2 * m},
		{"paused", f.PausedMS, int64(0)},
		{"active", f.ActiveMS, 6 * m},
		{"wall", f.WallMS(), 15 * m},
		{"cost", f.CostUSD, 0.5},
		{"rework", f.ReworkCostUSD, 0.3},
		{"input", f.InputTokens, 30},
		{"output", f.OutputTokens, 12},
		{"turns", f.Turns, 5},
		{"attempts", f.Attempts, 2},
		{"failed", f.FailedAttempts, 1},
		{"retries", f.Retries, 1},
		{"try agains", f.TryAgains, 0},
		{"pings", f.Pings, 1},
		{"unwanted", f.PingsUnwanted, 1},
		{"answered", f.PingsAnswered, 1},
		{"response", f.HumanResponseMS, 4 * m},
		{"holds", f.Holds, 1},
		{"tracker", f.TrackerKind, "jira"},
		{"ended", f.EndedAt != nil && f.EndedAt.Equal(t0.Add(15*time.Minute)), true},
		{"stages", f.Stages, map[string]int{"build": 2}},
		{"open", len(f.Open), 0},
		{"ping clock", len(f.PingClock), 0},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if len(f.Ended) != 2 {
		t.Fatalf("ended attempts = %+v", f.Ended)
	}
	a, b := f.Ended[0], f.Ended[1]
	if a.Seq != 4 || a.Outcome != OutcomeFailed || a.FailureReason != event.ReasonExitNonzero || a.Iteration != 1 ||
		a.ActiveMS != 3*m || a.WaitingMS != 4*m || a.CostUSD != 0.2 || a.Turns != 2 || a.Pings != 1 || a.Model != "m1" {
		t.Errorf("first attempt = %+v", a)
	}
	if b.Seq != 9 || b.Outcome != OutcomeCompleted || b.Iteration != 2 || b.ActiveMS != 3*m || b.WaitingMS != 2*m ||
		b.CostUSD != 0.3 || b.InputTokens != 20 || b.EndedAt == nil || !b.EndedAt.Equal(t0.Add(15*time.Minute)) {
		t.Errorf("second attempt = %+v", b)
	}
}

// The store folds a run incrementally, the view round-tripping through JSON
// between appends: wherever the log is cut, the facts must come out as one
// Reduce over the whole log.
func TestFactsFoldIncrementallyThroughJSON(t *testing.T) {
	evs := factsLog()
	want := Reduce(evs).Facts
	want.Ended = nil
	for cut := range len(evs) + 1 {
		v := Reduce(evs[:cut])
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var back View
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		for i := cut; i < len(evs); i++ {
			Apply(&back, &evs[i])
		}
		got := back.Facts
		got.Ended = nil
		if !reflect.DeepEqual(got, want) {
			t.Errorf("cut at %d: facts = %+v\nwant %+v", cut, got, want)
		}
	}
}

func at(min int, seq int64, p event.Payload) event.Event {
	return event.Event{RunID: "r1", Seq: seq, TS: t0.Add(time.Duration(min) * time.Minute), Type: p.EventType(), Payload: p}
}

func TestFactsEdges(t *testing.T) {
	m := int64(time.Minute / time.Millisecond)
	t.Run("pause, skip, gate rejection, abandon and try again", func(t *testing.T) {
		evs := []event.Event{
			at(0, 1, &event.RunCreated{}),
			at(0, 2, &event.EnvRequested{}),
			at(0, 3, &event.EnvReady{}),
			at(0, 4, &event.StageStarted{StageID: "a", Iteration: 1}),
			at(1, 5, &event.StagePaused{StageID: "a", Reason: event.PauseAskHuman, TurnsUsed: 1, Usage: &event.Usage{CostUSD: 0.1}}),
			at(2, 6, &event.StageResumed{StageID: "a", Reason: event.ResumeMessage}),
			at(3, 7, &event.StageCompleted{StageID: "a", Skipped: true}),
			at(3, 8, &event.StageStarted{StageID: "g", Iteration: 1}),
			at(3, 9, &event.PingRaised{PingID: "g1", Type: event.PingGate, StageID: "g"}),
			at(5, 10, &event.PingAnswered{PingID: "g1", Answer: "reject"}),
			at(5, 11, &event.PingAnswered{PingID: "nope", Answer: "approve"}), // no clock: ignored
			at(5, 12, &event.StageStarted{StageID: "g", Iteration: 2}),        // restarts: the first is abandoned
			at(5, 13, &event.MRCreated{MRURL: "u"}),
			at(6, 14, &event.RunPaused{Reason: "user"}),
			at(8, 15, &event.RunUnpaused{}),
			at(9, 16, &event.StageFailed{StageID: "zzz", Reason: "infra", TurnsUsed: 1}), // no open attempt: banks only
			at(9, 17, &event.RunAbandoned{StalledAt: "g", Reason: "budget exceeded"}),
			at(20, 18, &event.RunRetried{StageID: "g"}),
		}
		v := Reduce(evs)
		f := v.Facts
		if f.PausedMS != 2*m || f.ActiveMS != 5*m || f.WaitingHumanMS != 2*m || f.GatesRejected != 1 || f.PingsAnswered != 1 || f.PingsUnwanted != 0 ||
			f.MRsCreated != 1 || f.TryAgains != 1 || f.EndedAt != nil || f.EndReason != "" || f.StalledAt != "" ||
			f.FailedAttempts != 1 || f.Turns != 2 || f.CostUSD != 0.1 || f.Attempts != 3 {
			t.Errorf("facts = %+v", f)
		}
		outcomes := map[int64]string{}
		for _, a := range f.Ended {
			outcomes[a.Seq] = a.Outcome
		}
		if !reflect.DeepEqual(outcomes, map[int64]string{4: OutcomeSkipped, 8: OutcomeAbandoned, 12: OutcomeAbandoned}) {
			t.Errorf("outcomes = %v", outcomes)
		}
		// Abandoned: wall time stops at the end and does not count while
		// the run sat terminal.
		if f.WallMS() != 9*m {
			t.Errorf("wall = %d", f.WallMS())
		}
	})
	t.Run("paused attempt and failure end", func(t *testing.T) {
		evs := []event.Event{
			at(0, 1, &event.RunCreated{}),
			at(0, 2, &event.StageStarted{StageID: "a", Iteration: 1}),
			at(1, 3, &event.StagePaused{StageID: "a", Reason: event.PauseAskHuman}),
		}
		v := Reduce(evs)
		if a := v.Facts.Open["a"]; a == nil || a.Outcome != OutcomePaused {
			t.Fatalf("open = %+v", v.Facts.Open)
		}
		Apply(&v, &[]event.Event{at(2, 4, &event.RunFailed{Reason: "boom"})}[0])
		if v.Facts.EndReason != "boom" || v.Facts.EndedAt == nil || len(v.Facts.Open) != 0 || len(v.Facts.Ended) != 1 {
			t.Errorf("facts = %+v", v.Facts)
		}
	})
	t.Run("archive closes what is open", func(t *testing.T) {
		v := Reduce([]event.Event{
			at(0, 1, &event.RunCreated{}),
			at(0, 2, &event.StageStarted{StageID: "a", Iteration: 1}),
			at(1, 3, &event.RunArchived{}),
		})
		if v.Facts.EndedAt == nil || len(v.Facts.Open) != 0 || v.Facts.Ended[0].Outcome != OutcomeAbandoned {
			t.Errorf("facts = %+v", v.Facts)
		}
		ended := *v.Facts.EndedAt
		Apply(&v, &[]event.Event{at(5, 4, &event.RunArchived{})}[0])
		if !v.Facts.EndedAt.Equal(ended) {
			t.Errorf("archived again moved the end to %v", v.Facts.EndedAt)
		}
	})
}

// Side events land between the ones that move a run; they must not split
// its intervals, each truncated to the millisecond, or a log with more of
// them would read as less time.
func TestSideEventsDoNotSplitIntervals(t *testing.T) {
	ms := func(n float64) time.Time { return t0.Add(time.Duration(n * float64(time.Millisecond))) }
	ev := func(ts time.Time, seq int64, p event.Payload) event.Event {
		return event.Event{RunID: "r1", Seq: seq, TS: ts, Type: p.EventType(), Payload: p}
	}
	plain := Reduce([]event.Event{
		ev(ms(0), 1, &event.RunCreated{}),
		ev(ms(0), 2, &event.StageStarted{StageID: "a", Iteration: 1}),
		ev(ms(3.2), 3, &event.StageCompleted{StageID: "a"}),
	})
	sided := Reduce([]event.Event{
		ev(ms(0), 1, &event.RunCreated{}),
		ev(ms(0), 2, &event.StageStarted{StageID: "a", Iteration: 1}),
		ev(ms(1.6), 3, &event.ServiceReady{Service: "db"}),
		ev(ms(3.2), 4, &event.StageCompleted{StageID: "a"}),
	})
	if plain.Facts.ActiveMS != 3 || sided.Facts.ActiveMS != 3 || sided.Facts.Ended[0].ActiveMS != 3 {
		t.Errorf("active = %d plain, %d with a side event", plain.Facts.ActiveMS, sided.Facts.ActiveMS)
	}
	if !sided.Facts.LastTS.Equal(ms(3.2)) {
		t.Errorf("last ts = %v", sided.Facts.LastTS)
	}
}

func TestFactBearing(t *testing.T) {
	for _, ty := range []event.Type{event.TypeRunCreated, event.TypeStageFailed, event.TypeHoldReleased, event.TypeRunUnpaused} {
		if !FactBearing(ty) {
			t.Errorf("%s is fact-bearing", ty)
		}
	}
	for _, ty := range []event.Type{event.TypeSnapshotCaptured, event.TypeVerdictReceived, event.TypeEnvDestroyed} {
		if FactBearing(ty) {
			t.Errorf("%s is not fact-bearing", ty)
		}
	}
}
