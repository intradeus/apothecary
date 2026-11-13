package pgstore

import (
	"context"
	"testing"
	"time"

	"apothecary/internal/engine/ask"
)

func TestLimits(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if runs, envs, ok, err := s.LoadLimits(ctx); err != nil || ok || runs != 0 || envs != 0 {
		t.Fatalf("no row = %d, %d, %v, %v", runs, envs, ok, err)
	}
	if err := s.SaveLimits(ctx, 4, -1); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLimits(ctx, -1, 8); err != nil {
		t.Fatal(err)
	}
	runs, envs, ok, err := s.LoadLimits(ctx)
	if err != nil || !ok || runs != -1 || envs != 8 {
		t.Fatalf("after two saves = %d, %d, %v, %v (one row, last wins)", runs, envs, ok, err)
	}
	// The schema is the last word on what a cap can be.
	for _, c := range [][2]int{{0, 1}, {1, 0}, {-2, 1}} {
		if err := s.SaveLimits(ctx, c[0], c[1]); err == nil {
			t.Errorf("SaveLimits(%d, %d) accepted", c[0], c[1])
		}
	}
	if runs, envs, _, _ := s.LoadLimits(ctx); runs != -1 || envs != 8 {
		t.Errorf("refused save wrote: %d, %d", runs, envs)
	}
}

func TestAskThread(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if th, ok, err := s.AskThread(ctx, "r1", "t1"); err != nil || ok || th.RunID != "" {
		t.Fatalf("no thread = %+v, %v, %v", th, ok, err)
	}
	if err := s.SaveAskThread(ctx, ask.Thread{RunID: "r1", ID: "t1", SessionID: "s1", Model: "fake/one"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAskThread(ctx, ask.Thread{RunID: "r2", ID: "t1", Model: "fake/other"}); err != nil {
		t.Fatal(err)
	}
	// Saving again moves the session and the model on; other runs stay.
	if err := s.SaveAskThread(ctx, ask.Thread{RunID: "r1", ID: "t1", SessionID: "s2", Model: "fake/two"}); err != nil {
		t.Fatal(err)
	}
	th, ok, err := s.AskThread(ctx, "r1", "t1")
	if err != nil || !ok || th.RunID != "r1" || th.ID != "t1" || th.SessionID != "s2" || th.Model != "fake/two" || th.UpdatedAt.IsZero() {
		t.Fatalf("thread = %+v, %v, %v", th, ok, err)
	}
	if th, _, _ := s.AskThread(ctx, "r2", "t1"); th.Model != "fake/other" {
		t.Errorf("r2 = %+v", th)
	}
}

func TestAskMessagesAreOrderedPerRun(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	at := testNow.Add(time.Hour)
	q, err := s.AppendAskMessage(ctx, ask.Message{RunID: "r1", ThreadID: "t1", ID: "m1", Role: ask.RoleQuestion, Text: "why?", UserID: "u1", At: at})
	if err != nil || q.Seq != 1 || !q.At.Equal(at) {
		t.Fatalf("question = %+v, %v", q, err)
	}
	// A zero time is stamped on the way in, not stored as year 1.
	a, err := s.AppendAskMessage(ctx, ask.Message{RunID: "r1", ThreadID: "t2", ID: "m2", Role: ask.RoleAnswer, Text: "because", Model: "fake/one", CostUSD: 0.25, Turns: 3, Error: "partial"})
	if err != nil || a.Seq != 2 || a.At.IsZero() {
		t.Fatalf("answer = %+v, %v", a, err)
	}
	// seq is per run.
	if o, err := s.AppendAskMessage(ctx, ask.Message{RunID: "r2", ThreadID: "t1", ID: "m3", Role: ask.RoleQuestion, Text: "x"}); err != nil || o.Seq != 1 {
		t.Fatalf("other run = %+v, %v", o, err)
	}
	if _, err := s.AppendAskMessage(ctx, ask.Message{RunID: "r1", ThreadID: "t1", ID: "m4", Role: "shout", Text: "x"}); err == nil {
		t.Error("unknown role stored")
	}

	all, err := s.AskMessages(ctx, "r1", 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("messages = %+v, %v", all, err)
	}
	if all[0].RunID != "r1" || all[0].ThreadID != "t1" || all[0].ID != "m1" || all[0].UserID != "u1" || !all[0].At.Equal(at) {
		t.Errorf("question row = %+v", all[0])
	}
	if m := all[1]; m.Seq != 2 || m.ThreadID != "t2" || m.Model != "fake/one" || m.CostUSD != 0.25 || m.Turns != 3 || m.Error != "partial" || m.Text != "because" {
		t.Errorf("answer row = %+v", m)
	}
	after, err := s.AskMessages(ctx, "r1", 1)
	if err != nil || len(after) != 1 || after[0].ID != "m2" {
		t.Errorf("after seq 1 = %+v, %v", after, err)
	}
	if none, err := s.AskMessages(ctx, "r9", 0); err != nil || len(none) != 0 {
		t.Errorf("unknown run = %+v, %v", none, err)
	}
}
