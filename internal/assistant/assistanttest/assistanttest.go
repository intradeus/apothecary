// Package assistanttest is the contract every assistant.Store keeps: the
// in-memory one and Postgres run the same suite.
package assistanttest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"apothecary/internal/assistant"
)

// RunContract runs the suite against stores open makes, one per test.
func RunContract(t *testing.T, open func(t *testing.T) assistant.Store) {
	ctx := context.Background()
	sess := func(id, user string, kind assistant.Kind, ws, subject string) assistant.Session {
		return assistant.Session{ID: id, UserID: user, Kind: kind, Workshop: ws, Subject: subject}
	}

	t.Run("sessions round-trip and are found by subject", func(t *testing.T) {
		s := open(t)
		a := sess("s1", "u1", assistant.KindFlow, "w1", "builder")
		a.HarnessSession, a.Integration, a.Model, a.TokenID = "h-1", "int-1", "acme/one", "tok-1"
		if err := s.SaveAssistantSession(ctx, a); err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.AssistantSession(ctx, "s1")
		if err != nil || !ok || got.HarnessSession != "h-1" || got.Integration != "int-1" || got.Model != "acme/one" ||
			got.TokenID != "tok-1" || got.Workshop != "w1" || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
			t.Fatalf("session = %+v, %v, %v", got, ok, err)
		}
		if _, ok, err := s.AssistantSession(ctx, "nope"); ok || err != nil {
			t.Errorf("a missing session = %v, %v", ok, err)
		}
		// Another subject, another person, another kind: not found as it.
		for _, other := range []assistant.Session{
			sess("s2", "u1", assistant.KindFlow, "w1", "reviewer"),
			sess("s3", "u2", assistant.KindFlow, "w1", "builder"),
			sess("s4", "u1", assistant.KindTicket, "w1", "builder"),
			sess("s5", "u1", assistant.KindFlow, "w2", "builder"),
		} {
			if err := s.SaveAssistantSession(ctx, other); err != nil {
				t.Fatal(err)
			}
		}
		found, ok, err := s.FindAssistantSession(ctx, "u1", assistant.KindFlow, "w1", "builder")
		if err != nil || !ok || found.ID != "s1" {
			t.Errorf("find = %+v, %v, %v", found, ok, err)
		}
		// A later session about the same subject is the one found.
		time.Sleep(2 * time.Millisecond)
		if err := s.SaveAssistantSession(ctx, sess("s6", "u1", assistant.KindFlow, "w1", "builder")); err != nil {
			t.Fatal(err)
		}
		if found, _, _ := s.FindAssistantSession(ctx, "u1", assistant.KindFlow, "w1", "builder"); found.ID != "s6" {
			t.Errorf("the latest = %s", found.ID)
		}
		if _, ok, _ := s.FindAssistantSession(ctx, "u1", assistant.KindSkill, "", "x"); ok {
			t.Error("found a session for nothing")
		}
		list, err := s.AssistantSessions(ctx, "u1")
		if err != nil || len(list) != 5 || list[0].ID != "s6" {
			t.Errorf("u1's sessions = %d %v (first %v)", len(list), err, list)
		}
		// Saving again keeps its creation, moves its update.
		created := got.CreatedAt
		time.Sleep(2 * time.Millisecond)
		got.Model = "acme/two"
		old := got.Subject
		got.Subject = "APO-12" // a draft saved as a ticket
		if err := s.SaveAssistantSession(ctx, got); err != nil {
			t.Fatal(err)
		}
		again, _, _ := s.AssistantSession(ctx, "s1")
		if !again.CreatedAt.Equal(created) || !again.UpdatedAt.After(created) || again.Model != "acme/two" || again.Subject != "APO-12" {
			t.Errorf("resaved = %+v (created %v)", again, created)
		}
		if f, ok, _ := s.FindAssistantSession(ctx, again.UserID, again.Kind, again.Workshop, "APO-12"); !ok || f.ID != "s1" {
			t.Errorf("found by its new subject = %+v, %v", f, ok)
		}
		if f, ok, _ := s.FindAssistantSession(ctx, again.UserID, again.Kind, again.Workshop, old); ok && f.ID == "s1" {
			t.Error("still found by its old subject")
		}
	})

	t.Run("refuses what it cannot keep", func(t *testing.T) {
		s := open(t)
		for name, v := range map[string]assistant.Session{
			"no id":      {UserID: "u", Kind: assistant.KindFlow, Subject: "x"},
			"no person":  {ID: "s", Kind: assistant.KindFlow, Subject: "x"},
			"a bad kind": {ID: "s", UserID: "u", Kind: "widget", Subject: "x"},
			"no subject": {ID: "s", UserID: "u", Kind: assistant.KindFlow},
			"a NUL byte": {ID: "s", UserID: "u", Kind: assistant.KindFlow, Subject: "x\x00"},
		} {
			if err := s.SaveAssistantSession(ctx, v); !errors.Is(err, assistant.ErrInvalid) {
				t.Errorf("%s = %v", name, err)
			}
		}
		if err := s.SaveAssistantSession(ctx, sess("s1", "u1", assistant.KindFlow, "", "x")); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveAssistantSession(ctx, sess("s1", "u2", assistant.KindFlow, "", "x")); !errors.Is(err, assistant.ErrInvalid) {
			t.Errorf("a session changing owner = %v", err)
		}
		for name, m := range map[string]assistant.Message{
			"no session":  {Role: assistant.RolePerson, Text: "x"},
			"a bad role":  {SessionID: "s1", Role: "system", Text: "x"},
			"a NUL":       {SessionID: "s1", Role: assistant.RolePerson, Text: "x\x00"},
			"a bad fill":  {SessionID: "s1", Role: assistant.RoleAssistant, Fill: json.RawMessage("{nope")},
			"no such one": {SessionID: "s9", Role: assistant.RolePerson, Text: "x"},
		} {
			if _, err := s.AppendAssistantMessage(ctx, m); err == nil {
				t.Errorf("%s: appended", name)
			}
		}
		if err := s.SaveAssistantChoice(ctx, assistant.Choice{Integration: "x"}); !errors.Is(err, assistant.ErrInvalid) {
			t.Errorf("a choice with no person = %v", err)
		}
	})

	t.Run("messages are numbered and read after a sequence", func(t *testing.T) {
		s := open(t)
		for _, id := range []string{"s1", "s2"} {
			if err := s.SaveAssistantSession(ctx, sess(id, "u1", assistant.KindTicket, "w1", id)); err != nil {
				t.Fatal(err)
			}
		}
		fill := json.RawMessage(`{"title":"Fix the login page"}`)
		for i, m := range []assistant.Message{
			{SessionID: "s1", Role: assistant.RolePerson, Text: "make a ticket"},
			{SessionID: "s2", Role: assistant.RolePerson, Text: "elsewhere"},
			{SessionID: "s1", Role: assistant.RoleAssistant, Text: "done", Fill: fill, CostUSD: 0.25},
			{SessionID: "s1", Role: assistant.RoleAssistant, Error: "the harness failed"},
		} {
			got, err := s.AppendAssistantMessage(ctx, m)
			want := []int64{1, 1, 2, 3}[i]
			if err != nil || got.Seq != want || got.At.IsZero() {
				t.Fatalf("append %d = %+v, %v (want seq %d)", i, got, err, want)
			}
		}
		all, err := s.AssistantMessages(ctx, "s1", 0)
		if err != nil || len(all) != 3 || all[1].Text != "done" || all[1].CostUSD != 0.25 || all[2].Error != "the harness failed" {
			t.Fatalf("messages = %+v, %v", all, err)
		}
		var f map[string]string
		if json.Unmarshal(all[1].Fill, &f) != nil || f["title"] != "Fix the login page" {
			t.Errorf("fill = %s", all[1].Fill)
		}
		after, _ := s.AssistantMessages(ctx, "s1", 2)
		if len(after) != 1 || after[0].Seq != 3 {
			t.Errorf("after 2 = %+v", after)
		}
		if none, err := s.AssistantMessages(ctx, "nope", 0); err != nil || len(none) != 0 {
			t.Errorf("a missing session's messages = %v, %v", none, err)
		}
	})

	t.Run("a request is kept with its answer and decided in place", func(t *testing.T) {
		s := open(t)
		if err := s.SaveAssistantSession(ctx, sess("s1", "u1", assistant.KindGeneral, "", "palette")); err != nil {
			t.Fatal(err)
		}
		req := assistant.Request{Why: "Start a run on it.", State: assistant.RequestPending, Acts: []assistant.Act{
			{Tool: "start_run", Title: "Start one attempt", Arguments: json.RawMessage(`{"id":"w1","key":"T-1"}`)},
		}}
		plain, err := s.AppendAssistantMessage(ctx, assistant.Message{SessionID: "s1", Role: assistant.RolePerson, Text: "run it"})
		if err != nil {
			t.Fatal(err)
		}
		m, err := s.AppendAssistantMessage(ctx, assistant.Message{SessionID: "s1", Role: assistant.RoleAssistant, Text: "Shall I?", Request: &req})
		if err != nil {
			t.Fatal(err)
		}
		req.Acts[0].Tool = "changed after" // the store kept its own copy
		got, _ := s.AssistantMessages(ctx, "s1", 0)
		if len(got) != 2 || got[0].Request != nil || got[1].Request == nil || got[1].Request.Acts[0].Tool != "start_run" ||
			!sameJSON(got[1].Request.Acts[0].Arguments, `{"id":"w1","key":"T-1"}`) || got[1].Request.State != assistant.RequestPending {
			t.Fatalf("messages = %+v", got)
		}
		done := *got[1].Request
		done.State, done.Reported = assistant.RequestDone, true
		done.Acts = []assistant.Act{{Tool: "start_run", Arguments: json.RawMessage(`{}`), Status: 201, Result: "run r1"}}
		if err := s.SetAssistantRequest(ctx, "s1", m.Seq, assistant.RequestPending, done); err != nil {
			t.Fatal(err)
		}
		// The state moves once: a write expecting it pending finds it done.
		if err := s.SetAssistantRequest(ctx, "s1", m.Seq, assistant.RequestPending, done); !errors.Is(err, assistant.ErrStale) {
			t.Errorf("a second move from pending = %v", err)
		}
		got, _ = s.AssistantMessages(ctx, "s1", 1)
		if r := got[0].Request; r.State != assistant.RequestDone || !r.Reported || r.Acts[0].Status != 201 || r.Acts[0].Result != "run r1" {
			t.Errorf("decided = %+v", r)
		}
		for name, err := range map[string]error{
			"a message with none": s.SetAssistantRequest(ctx, "s1", plain.Seq, assistant.RequestPending, done),
			"no such message":     s.SetAssistantRequest(ctx, "s1", 99, assistant.RequestPending, done),
			"no state":            s.SetAssistantRequest(ctx, "s1", m.Seq, assistant.RequestDone, assistant.Request{Acts: done.Acts}),
			"no acts":             s.SetAssistantRequest(ctx, "s1", m.Seq, assistant.RequestDone, assistant.Request{State: assistant.RequestDone}),
		} {
			if !errors.Is(err, assistant.ErrInvalid) {
				t.Errorf("%s = %v", name, err)
			}
		}
		if _, err := s.AppendAssistantMessage(ctx, assistant.Message{SessionID: "s1", Role: assistant.RoleAssistant,
			Request: &assistant.Request{State: "maybe", Acts: done.Acts}}); !errors.Is(err, assistant.ErrInvalid) {
			t.Errorf("a request in no state = %v", err)
		}
	})

	t.Run("choices", func(t *testing.T) {
		s := open(t)
		c, err := s.AssistantChoice(ctx, "u1")
		if err != nil || c.Integration != "" || c.UserID != "u1" {
			t.Errorf("no choice yet = %+v, %v", c, err)
		}
		if err := s.SaveAssistantChoice(ctx, assistant.Choice{UserID: "u1", Integration: "int-1", Model: "acme/one"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveAssistantChoice(ctx, assistant.Choice{UserID: "u1", Integration: "int-2", Model: "acme/two"}); err != nil {
			t.Fatal(err)
		}
		if c, _ := s.AssistantChoice(ctx, "u1"); c.Integration != "int-2" || c.Model != "acme/two" {
			t.Errorf("choice = %+v", c)
		}
		if c, _ := s.AssistantChoice(ctx, "u2"); c.Integration != "" {
			t.Errorf("another's choice = %+v", c)
		}
	})
}

// sameJSON compares JSON as values: a store may reformat it (jsonb).
func sameJSON(got json.RawMessage, want string) bool {
	var a, b any
	return json.Unmarshal(got, &a) == nil && json.Unmarshal([]byte(want), &b) == nil && reflect.DeepEqual(a, b)
}
