// Package asktest is the ask.Store contract, run against every implementation
// (MemStore in ask's own tests, Postgres in pgstore's). It states what
// Postgres does: production runs nothing else, so a twin that differs makes
// the unit tier prove the wrong thing.
package asktest

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"apothecary/internal/engine/ask"
)

// RunContract exercises one Store implementation. open must return a fresh,
// empty store.
func RunContract(t *testing.T, open func(t *testing.T) ask.Store) {
	t.Helper()
	runThreads(t, open)
	runMessages(t, open)
	runRefusals(t, open)
	runConcurrent(t, open)
}

func runThreads(t *testing.T, open func(t *testing.T) ask.Store) {
	t.Run("a run with no thread has none", func(t *testing.T) {
		s := open(t)
		th, ok, err := s.AskThread(context.Background(), "nope")
		if err != nil || ok || th != (ask.Thread{}) {
			t.Fatalf("thread = %+v, %v, %v", th, ok, err)
		}
	})

	t.Run("a thread round trips and saving replaces it", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		save(t, s, ask.Thread{RunID: "r1", SessionID: "s1", Model: "fake/one"})
		save(t, s, ask.Thread{RunID: "r2", Model: "fake/other"})
		check := func(runID, session, model string) {
			t.Helper()
			th, ok, err := s.AskThread(ctx, runID)
			if err != nil || !ok || th.RunID != runID || th.SessionID != session || th.Model != model || th.UpdatedAt.IsZero() {
				t.Errorf("%s = %+v, %v, %v", runID, th, ok, err)
			}
		}
		check("r1", "s1", "fake/one")
		save(t, s, ask.Thread{RunID: "r1", SessionID: "s2", Model: "fake/two"})
		check("r1", "s2", "fake/two")
		check("r2", "", "fake/other")
		// Saving is the whole row: an empty field empties it.
		save(t, s, ask.Thread{RunID: "r1"})
		check("r1", "", "")
	})

	t.Run("the store stamps a thread, whatever the caller says", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		const skew = time.Minute // the stamp is the database server's clock
		for _, given := range []time.Time{{}, time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC), time.Now().Add(24 * time.Hour)} {
			before := time.Now()
			save(t, s, ask.Thread{RunID: "r1", Model: "fake/one", UpdatedAt: given})
			th, ok, err := s.AskThread(ctx, "r1")
			if err != nil || !ok {
				t.Fatalf("thread = %+v, %v, %v", th, ok, err)
			}
			if th.UpdatedAt.Before(before.Add(-skew)) || th.UpdatedAt.After(time.Now().Add(skew)) {
				t.Errorf("given %v: updated_at = %v", given, th.UpdatedAt)
			}
			if th.UpdatedAt.Nanosecond()%1000 != 0 || th.UpdatedAt.Location() != time.Local {
				t.Errorf("updated_at is not a timestamptz: %v", th.UpdatedAt)
			}
		}
		first, _, _ := s.AskThread(ctx, "r1")
		time.Sleep(2 * time.Millisecond)
		save(t, s, ask.Thread{RunID: "r1", UpdatedAt: first.UpdatedAt})
		if second, _, _ := s.AskThread(ctx, "r1"); !second.UpdatedAt.After(first.UpdatedAt) {
			t.Errorf("save kept updated_at: %v, then %v", first.UpdatedAt, second.UpdatedAt)
		}
	})

	t.Run("threads and messages are separate rows", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		appendMsg(t, s, ask.Message{RunID: "r1", ID: "m1", Role: ask.RoleQuestion, Text: "q"})
		if th, ok, err := s.AskThread(ctx, "r1"); err != nil || ok {
			t.Errorf("a message made a thread: %+v, %v, %v", th, ok, err)
		}
		save(t, s, ask.Thread{RunID: "r2"})
		if m, err := s.AskMessages(ctx, "r2", 0); err != nil || m != nil {
			t.Errorf("a thread made messages: %+v, %v", m, err)
		}
	})
}

func runMessages(t *testing.T, open func(t *testing.T) ask.Store) {
	t.Run("a message round trips", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		at := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)
		in := ask.Message{RunID: "r1", ID: "m1", Role: ask.RoleAnswer, Text: "because é\n\"quoted\"", UserID: "u1",
			Model: "fake/one", CostUSD: 0.25, Turns: 3, Error: "partial", At: at}
		out, err := s.AppendAskMessage(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		in.Seq = 1
		if !same(out, in) {
			t.Errorf("appended = %+v, want %+v", out, in)
		}
		got, err := s.AskMessages(ctx, "r1", 0)
		if err != nil || len(got) != 1 {
			t.Fatalf("messages = %+v, %v", got, err)
		}
		if !same(got[0], in) {
			t.Errorf("read = %+v, want %+v", got[0], in)
		}
	})

	t.Run("a question needs no more than its role", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		appendMsg(t, s, ask.Message{RunID: "r1", Role: ask.RoleQuestion})
		got, err := s.AskMessages(ctx, "r1", 0)
		if err != nil || len(got) != 1 {
			t.Fatalf("messages = %+v, %v", got, err)
		}
		m := got[0]
		if m.ID != "" || m.Text != "" || m.UserID != "" || m.Model != "" || m.CostUSD != 0 || m.Turns != 0 || m.Error != "" {
			t.Errorf("row = %+v", m)
		}
	})

	t.Run("seq counts each run from 1, whatever the caller says", func(t *testing.T) {
		s := open(t)
		for i, want := range []int64{1, 2, 3} {
			m := appendMsg(t, s, ask.Message{RunID: "r1", ID: fmt.Sprintf("m%d", i), Role: ask.RoleQuestion, Seq: 40})
			if m.Seq != want {
				t.Errorf("r1 seq = %d, want %d", m.Seq, want)
			}
		}
		if m := appendMsg(t, s, ask.Message{RunID: "r2", ID: "o", Role: ask.RoleAnswer}); m.Seq != 1 {
			t.Errorf("r2 seq = %d", m.Seq)
		}
	})

	t.Run("ids are the caller's", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		for _, id := range []string{"same", "same", ""} {
			if m := appendMsg(t, s, ask.Message{RunID: "r1", ID: id, Role: ask.RoleQuestion}); m.ID != id {
				t.Errorf("appended id = %q, want %q", m.ID, id)
			}
		}
		got, err := s.AskMessages(ctx, "r1", 0)
		if err != nil || len(got) != 3 || got[0].ID != "same" || got[1].ID != "same" || got[2].ID != "" {
			t.Errorf("messages = %+v, %v", got, err)
		}
	})

	t.Run("at is kept, or stamped when zero", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		before := time.Now()
		m := appendMsg(t, s, ask.Message{RunID: "r1", Role: ask.RoleQuestion})
		if m.At.Before(before) || m.At.After(time.Now()) || m.At.Location() != time.UTC {
			t.Errorf("stamped at = %v", m.At)
		}
		// Below the microsecond the column has no room: the answer keeps
		// what it was given, a read what was stored.
		at := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.FixedZone("x", 3600))
		given := appendMsg(t, s, ask.Message{RunID: "r1", Role: ask.RoleQuestion, At: at})
		if !given.At.Equal(at) {
			t.Errorf("appended at = %v", given.At)
		}
		got, err := s.AskMessages(ctx, "r1", 0)
		if err != nil || len(got) != 2 {
			t.Fatalf("messages = %+v, %v", got, err)
		}
		if !got[0].At.Equal(m.At.Truncate(time.Microsecond)) {
			t.Errorf("stamped read = %v, appended %v", got[0].At, m.At)
		}
		if !got[1].At.Equal(at.Truncate(time.Microsecond)) || got[1].At.Location() != time.Local {
			t.Errorf("given read = %v", got[1].At)
		}
	})

	t.Run("messages read in seq order, whatever their times", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
		for i, at := range []time.Time{base.Add(time.Hour), base, base.Add(-time.Hour)} {
			appendMsg(t, s, ask.Message{RunID: "r1", ID: fmt.Sprintf("m%d", i), Role: ask.RoleQuestion, At: at})
		}
		appendMsg(t, s, ask.Message{RunID: "r2", ID: "other", Role: ask.RoleQuestion})
		for after, want := range map[int64][]string{-1: {"m0", "m1", "m2"}, 0: {"m0", "m1", "m2"}, 1: {"m1", "m2"}, 2: {"m2"}} {
			got, err := s.AskMessages(ctx, "r1", after)
			if err != nil {
				t.Fatal(err)
			}
			if ids := ids(got); !slices.Equal(ids, want) {
				t.Errorf("after %d = %v, want %v", after, ids, want)
			}
		}
		for _, after := range []int64{3, 100} {
			if got, err := s.AskMessages(ctx, "r1", after); err != nil || got != nil {
				t.Errorf("after %d = %#v, %v", after, got, err)
			}
		}
	})

	t.Run("an unknown run has no messages", func(t *testing.T) {
		s := open(t)
		if got, err := s.AskMessages(context.Background(), "nope", 0); err != nil || got != nil {
			t.Errorf("messages = %#v, %v", got, err)
		}
	})
}

func runRefusals(t *testing.T, open func(t *testing.T) ask.Store) {
	t.Run("a refused message stores nothing and takes no seq", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		appendMsg(t, s, ask.Message{RunID: "r1", ID: "m1", Role: ask.RoleQuestion, Text: "q"})
		good := ask.Message{RunID: "r1", ID: "bad", Role: ask.RoleAnswer}
		for name, bad := range map[string]func(*ask.Message){
			"unknown role":        func(m *ask.Message) { m.Role = "shout" },
			"no role":             func(m *ask.Message) { m.Role = "" },
			"NUL in the text":     func(m *ask.Message) { m.Text = "a\x00b" },
			"NUL in the error":    func(m *ask.Message) { m.Error = "a\x00b" },
			"NUL in the id":       func(m *ask.Message) { m.ID = "a\x00b" },
			"NUL in the user":     func(m *ask.Message) { m.UserID = "a\x00b" },
			"NUL in the model":    func(m *ask.Message) { m.Model = "a\x00b" },
			"invalid UTF-8 text":  func(m *ask.Message) { m.Text = "a\xffb" },
			"invalid UTF-8 error": func(m *ask.Message) { m.Error = "a\xffb" },
			"too many turns":      func(m *ask.Message) { m.Turns = math.MaxInt32 + 1 },
			"too few turns":       func(m *ask.Message) { m.Turns = math.MinInt32 - 1 },
		} {
			m := good
			bad(&m)
			if _, err := s.AppendAskMessage(ctx, m); err == nil {
				t.Errorf("%s: stored", name)
			}
		}
		if got, err := s.AskMessages(ctx, "r1", 0); err != nil || len(got) != 1 {
			t.Fatalf("refusals left %+v, %v", got, err)
		}
		if m := appendMsg(t, s, good); m.Seq != 2 {
			t.Errorf("next seq = %d, want 2", m.Seq)
		}
		if m := appendMsg(t, s, ask.Message{RunID: "r2", Role: ask.RoleAnswer, Turns: math.MaxInt32}); m.Turns != math.MaxInt32 {
			t.Errorf("turns = %d", m.Turns)
		}
	})

	t.Run("a refused thread stores nothing", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		save(t, s, ask.Thread{RunID: "r1", SessionID: "s1", Model: "fake/one"})
		for name, th := range map[string]ask.Thread{
			"NUL in the session":   {RunID: "r1", SessionID: "a\x00b"},
			"NUL in the model":     {RunID: "r1", Model: "a\x00b"},
			"invalid UTF-8 model":  {RunID: "r1", Model: "a\xffb"},
			"NUL in the run":       {RunID: "r\x00"},
			"invalid UTF-8 in run": {RunID: "r\xff"},
		} {
			if err := s.SaveAskThread(ctx, th); err == nil {
				t.Errorf("%s: stored", name)
			}
		}
		if th, ok, err := s.AskThread(ctx, "r1"); err != nil || !ok || th.SessionID != "s1" || th.Model != "fake/one" {
			t.Errorf("thread = %+v, %v, %v", th, ok, err)
		}
	})

	t.Run("a run id must be text to be looked up", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		for _, id := range []string{"a\x00b", "a\xffb"} {
			if _, _, err := s.AskThread(ctx, id); err == nil {
				t.Errorf("AskThread(%q) succeeded", id)
			}
			if _, err := s.AskMessages(ctx, id, 0); err == nil {
				t.Errorf("AskMessages(%q) succeeded", id)
			}
		}
	})

	t.Run("a dead context does nothing", func(t *testing.T) {
		s := open(t)
		live := context.Background()
		save(t, s, ask.Thread{RunID: "r1", SessionID: "s1"})
		appendMsg(t, s, ask.Message{RunID: "r1", ID: "m1", Role: ask.RoleQuestion})
		ctx, cancel := context.WithCancel(live)
		cancel()
		if err := s.SaveAskThread(ctx, ask.Thread{RunID: "r1", SessionID: "s2"}); err == nil {
			t.Error("SaveAskThread succeeded")
		}
		if _, err := s.AppendAskMessage(ctx, ask.Message{RunID: "r1", ID: "m2", Role: ask.RoleQuestion}); err == nil {
			t.Error("AppendAskMessage succeeded")
		}
		if th, ok, err := s.AskThread(ctx, "r1"); err == nil || ok {
			t.Errorf("AskThread = %+v, %v, %v", th, ok, err)
		}
		if m, err := s.AskMessages(ctx, "r1", 0); err == nil {
			t.Errorf("AskMessages = %+v", m)
		}
		if th, _, _ := s.AskThread(live, "r1"); th.SessionID != "s1" {
			t.Errorf("thread = %+v", th)
		}
		if m, _ := s.AskMessages(live, "r1", 0); len(m) != 1 {
			t.Errorf("messages = %+v", m)
		}
	})
}

func runConcurrent(t *testing.T, open func(t *testing.T) ask.Store) {
	t.Run("concurrent appends on different runs all land", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		const runs, each = 8, 5
		var wg sync.WaitGroup
		for r := range runs {
			wg.Go(func() {
				id := fmt.Sprintf("r%d", r)
				for range each {
					if _, err := s.AppendAskMessage(ctx, ask.Message{RunID: id, Role: ask.RoleQuestion}); err != nil {
						t.Errorf("%s: %v", id, err)
					}
				}
			})
		}
		wg.Wait()
		for r := range runs {
			got, err := s.AskMessages(ctx, fmt.Sprintf("r%d", r), 0)
			if err != nil || len(got) != each {
				t.Fatalf("r%d: %d messages, %v", r, len(got), err)
			}
			checkGapless(t, got)
		}
	})

	// The engine keeps one answer in flight per run. Racing that may lose,
	// the second insert meeting the first's key; it may never number two
	// messages alike, leave a gap, or keep a message it refused.
	t.Run("concurrent appends on one run never corrupt the log", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		const writers, each = 8, 5
		var (
			mu       sync.Mutex
			accepted = map[int64]string{}
			wg       sync.WaitGroup
		)
		for w := range writers {
			wg.Go(func() {
				for i := range each {
					id := fmt.Sprintf("%d-%d", w, i)
					m, err := s.AppendAskMessage(ctx, ask.Message{RunID: "r1", ID: id, Role: ask.RoleQuestion})
					if err != nil {
						continue
					}
					mu.Lock()
					if prev, dup := accepted[m.Seq]; dup {
						t.Errorf("seq %d given to %s and %s", m.Seq, prev, id)
					}
					accepted[m.Seq] = id
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if len(accepted) == 0 {
			t.Fatal("no append landed")
		}
		got, err := s.AskMessages(ctx, "r1", 0)
		if err != nil || len(got) != len(accepted) {
			t.Fatalf("stored %d of %d accepted, %v", len(got), len(accepted), err)
		}
		checkGapless(t, got)
		for _, m := range got {
			if accepted[m.Seq] != m.ID {
				t.Errorf("seq %d holds %q, given to %q", m.Seq, m.ID, accepted[m.Seq])
			}
		}
	})
}

func save(t *testing.T, s ask.Store, th ask.Thread) {
	t.Helper()
	if err := s.SaveAskThread(context.Background(), th); err != nil {
		t.Fatal(err)
	}
}

func appendMsg(t *testing.T, s ask.Store, m ask.Message) ask.Message {
	t.Helper()
	out, err := s.AppendAskMessage(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// same compares messages by instant: a read's zone is the connection's.
func same(a, b ask.Message) bool {
	if !a.At.Equal(b.At) {
		return false
	}
	a.At, b.At = time.Time{}, time.Time{}
	return a == b
}

func ids(ms []ask.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func checkGapless(t *testing.T, ms []ask.Message) {
	t.Helper()
	for i, m := range ms {
		if m.Seq != int64(i+1) {
			t.Fatalf("position %d holds seq %d", i, m.Seq)
		}
	}
}
