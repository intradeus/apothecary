package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/engine/ask"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// The run's chats take files the sender uploaded: claimed for the run, all
// or nothing, once the message is accepted, and named in what the agent
// reads. A file that is not the sender's to send refuses the message (400,
// never 404) and nothing is recorded.

const msgFlow = `name: msg
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build the thing
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
`

func saveAs(t *testing.T, files *testFiles, user, name string) filestore.Attachment {
	t.Helper()
	a, err := files.att.Save(context.Background(), user, name, "text/plain", strings.NewReader("contents of "+name))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	return a
}

func runLog(t *testing.T, x *env, runID string) []event.Event {
	t.Helper()
	evs, err := x.store.Events(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestSendMessageWithFiles(t *testing.T) {
	files := newTestFiles(t, 1<<10, []string{"text/*"})
	x := newEnvWith(t, engine.Config{}, files.deps, msgFlow)
	files.serve(x)
	got := make(chan string, 1)
	x.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		select {
		case <-call.In.Inbox.Wake():
		case <-ctx.Done():
			return agentport.AgentResult{}, ctx.Err()
		}
		msgs := call.In.Inbox.Take()
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvUserMessage, Messages: msgs})
		got <- msgs[0].Text
		return agentport.AgentResult{TurnsUsed: 1, Session: "s"}, nil
	}
	runID := x.createTicketFlow(t, "T", "msg")
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, data := x.do(t, "POST", "/api/runs/"+runID+"/hold", nil, nil)
		if resp.StatusCode == http.StatusAccepted {
			break
		}
		if resp.StatusCode != http.StatusConflict || time.Now().After(deadline) {
			t.Fatalf("take over = %d: %s", resp.StatusCode, data)
		}
		time.Sleep(5 * time.Millisecond)
	}
	path := "/api/runs/" + runID + "/messages"
	mine := saveAs(t, files, testUser, "notes.txt")
	theirs := saveAs(t, files, "someone-else", "theirs.txt")
	taken := saveAs(t, files, testUser, "taken.txt")
	if err := files.att.ClaimForRun(context.Background(), testUser, []string{taken.ID}, "other-run", ""); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{"someone else's": theirs.ID, "already sent": taken.ID, "unknown": "ghost"} {
		resp, data := x.do(t, "POST", path, SendMessageRequest{Text: "look", AttachmentIDs: []string{mine.ID, bad}}, nil)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "not one of your unsent uploads") {
			t.Errorf("%s file = %d: %s", name, resp.StatusCode, data)
		}
	}
	if a, _ := files.att.Get(context.Background(), mine.ID); !a.Unclaimed() {
		t.Errorf("a refused message claimed %+v", a)
	}
	nine := make([]string, filestore.MaxUploadFiles+1)
	if resp, data := x.do(t, "POST", path, SendMessageRequest{Text: "look", AttachmentIDs: nine}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("nine files = %d: %s", resp.StatusCode, data)
	}
	if resp, data := x.do(t, "POST", path, SendMessageRequest{Text: " "}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("nothing = %d: %s", resp.StatusCode, data)
	}
	for _, ev := range runLog(t, x, runID) {
		if _, ok := ev.Payload.(*event.MessageQueued); ok {
			t.Fatalf("a refused message reached the record: %+v", ev.Payload)
		}
	}

	// Files alone are a message.
	resp, data := x.do(t, "POST", path, SendMessageRequest{AttachmentIDs: []string{mine.ID}}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("files alone = %d: %s", resp.StatusCode, data)
	}
	if text := <-got; !strings.HasSuffix(text, "- .apothecary/attachments/takeover/notes.txt") {
		t.Errorf("delivered %q", text)
	}
	x.e.Wait(runID)
	if a, _ := files.att.Get(context.Background(), mine.ID); a.RunID != runID || a.Chat != filestore.ChatTakeOver {
		t.Errorf("claimed = %+v", a)
	}
	var q *event.MessageQueued
	for _, ev := range runLog(t, x, runID) {
		if p, ok := ev.Payload.(*event.MessageQueued); ok {
			q = p
		}
	}
	if q == nil || q.Text != "" || len(q.AttachmentIDs) != 1 || q.AttachmentIDs[0] != mine.ID {
		t.Errorf("message_queued = %+v", q)
	}
}

func TestAskWithFiles(t *testing.T) {
	files := newTestFiles(t, 1<<10, []string{"text/*"})
	x := newEnvWith(t, engine.Config{}, files.deps, askFlow)
	files.serve(x)
	id := x.createTicketFlow(t, "T", "asky")
	x.e.Wait(id)
	path := "/api/runs/" + id + "/ask"
	mine := saveAs(t, files, testUser, "notes.txt")
	theirs := saveAs(t, files, "someone-else", "theirs.txt")

	res, body := x.do(t, "POST", path, AskRequest{Text: "why?", AttachmentIDs: []string{mine.ID, theirs.ID}}, nil)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "not one of your unsent uploads") {
		t.Fatalf("someone else's file = %d %s", res.StatusCode, body)
	}
	_, body = x.do(t, "GET", path, nil, nil)
	st := decode[ask.AskState](t, body)
	if len(st.Messages) != 0 {
		t.Errorf("a refused question was recorded: %s", body)
	}
	for _, th := range st.Threads {
		if th.Busy {
			t.Errorf("a refused question left its thread busy: %s", body)
		}
	}
	if a, _ := files.att.Get(context.Background(), mine.ID); !a.Unclaimed() {
		t.Errorf("a refused question claimed %+v", a)
	}

	res, body = x.do(t, "POST", path, AskRequest{AttachmentIDs: []string{mine.ID}}, nil)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("files alone = %d %s", res.StatusCode, body)
	}
	if q := decode[ask.Message](t, body); len(q.AttachmentIDs) != 1 || q.AttachmentIDs[0] != mine.ID || q.Text != "" {
		t.Errorf("question = %s", body)
	}
	if a, _ := files.att.Get(context.Background(), mine.ID); a.RunID != id || a.Chat != filestore.ChatAsk {
		t.Errorf("claimed = %+v", a)
	}
	env := envport.EnvID(run.Reduce(runLog(t, x, id)).EnvID)
	if data, err := x.envp.ReadFile(context.Background(), env, ".apothecary/attachments/ask/notes.txt"); err != nil || string(data) != "contents of notes.txt" {
		t.Errorf("delivered = %q, %v", data, err)
	}
	waitFor(t, "the answer", func() bool {
		_, b := x.do(t, "GET", path, nil, nil)
		st := decode[ask.AskState](t, b)
		return len(st.Messages) == 2
	})
	_, body = x.do(t, "GET", path, nil, nil)
	if st := decode[ask.AskState](t, body); len(st.Messages[0].AttachmentIDs) != 1 {
		t.Errorf("state = %s", body)
	}
}
