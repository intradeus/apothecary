package engine

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

// Files a person pastes into the run's chats: claimed for the run once the
// message is accepted, all or nothing, delivered read-only under takeover/
// or ask/, and named in what the agent reads.

const pngBytes = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

// chatFlowDoc is two agent stages, each reading the task.
const chatFlowDoc = `name: chat
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: scout, type: agent, harness: $harness, model: $model, agent: builder, input: { ticket: { from: run.ticket, type: ticket } } }
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder, input: { ticket: { from: run.ticket, type: ticket } } }
`

// modeEnvp records the mode each file is written with (the fake keeps none),
// and refuses a write to failOn.
type modeEnvp struct {
	*fake.EnvProvisioner
	mu     sync.Mutex
	modes  map[string]fs.FileMode
	failOn string
}

func (m *modeEnvp) WriteFile(ctx context.Context, id envport.EnvID, path string, data []byte, mode fs.FileMode) error {
	m.mu.Lock()
	if path == m.failOn {
		m.mu.Unlock()
		return errors.New("disk full")
	}
	if m.modes == nil {
		m.modes = map[string]fs.FileMode{}
	}
	m.modes[path] = mode
	m.mu.Unlock()
	return m.EnvProvisioner.WriteFile(ctx, id, path, data, mode)
}

func (m *modeEnvp) mode(path string) fs.FileMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.modes[path]
}

func uploadAs(t *testing.T, att *filestore.Attachments, user, name, body string) filestore.Attachment {
	t.Helper()
	a, err := att.Save(context.Background(), user, name, "", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond) // the next upload is later
	return a
}

func wantFile(t *testing.T, h *harness, runID, path, body string) {
	t.Helper()
	data, err := h.envp.ReadFile(context.Background(), envport.EnvID(h.view(t, runID).EnvID), path)
	if err != nil || string(data) != body {
		t.Errorf("%s in the workspace = %q, %v", path, data, err)
	}
}

func TestATakeOverMessageCarriesFiles(t *testing.T) {
	att, snaps, _ := newFiles(t)
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Attachments, d.Snapshots = att, snaps }, mustFlow(t, chatFlowDoc))
	env := &modeEnvp{EnvProvisioner: h.envp}
	h.res.Envp = env
	r := &heldRunner{firstEnd: make(chan struct{})}
	var mu sync.Mutex
	var prompts []string
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		mu.Lock()
		prompts = append(prompts, call.In.Prompt)
		first := len(prompts) == 1
		mu.Unlock()
		if !first {
			return agentport.AgentResult{Session: "s2", TurnsUsed: 1}, nil
		}
		r.turn(call.In, "working on it", r.firstEnd)
		return r.drive(ctx, call.In)
	}
	goal := uploadAs(t, att, "", "spec.md", "the spec")
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T", AttachmentIDs: []string{goal.ID}}, "chat", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID)
	takeOverWhenLive(t, h, runID, "u1")
	close(r.firstEnd)

	shot := uploadAs(t, att, "u1", "image.png", pngBytes)
	theirs := uploadAs(t, att, "u2", "image.png", pngBytes)
	// One file that is not the sender's refuses the whole message.
	if _, err := h.e.SendMessage(runID, "u1", "look", []string{shot.ID, theirs.ID}); !errors.Is(err, filestore.ErrNotClaimable) {
		t.Fatalf("someone else's file = %v, want ErrNotClaimable", err)
	}
	if got, _ := att.Get(context.Background(), shot.ID); !got.Unclaimed() {
		t.Errorf("a refused message claimed %+v", got)
	}
	if q, ok := findPayload[*event.MessageQueued](h.events(t, runID)); ok {
		t.Errorf("a refused message reached the record: %+v", q)
	}

	id, err := h.e.SendMessage(runID, "u1", "what about this?", []string{shot.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A second paste, files alone: browsers name every screenshot alike.
	again := uploadAs(t, att, "u1", "image.png", pngBytes+"2")
	waitUntil(t, "the first message to be delivered", func() bool { return len(r.delivered()) == 1 })
	if _, err := h.e.SendMessage(runID, "u1", "  ", []string{again.ID}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the second message to be delivered", func() bool { return len(r.delivered()) == 2 })

	batches := r.delivered()
	const note = "\n\nThe human attached these files — read them with your tools:\n"
	if got := batches[0][0]; got.ID != id || got.Text != "what about this?"+note+"- .apothecary/attachments/takeover/image.png" {
		t.Errorf("first message = %+v", got)
	}
	if got := batches[1][0].Text; got != strings.TrimPrefix(note, "\n\n")+"- .apothecary/attachments/takeover/image-2.png" {
		t.Errorf("files alone = %q", got)
	}
	wantFile(t, h, runID, ".apothecary/attachments/takeover/image.png", pngBytes)
	wantFile(t, h, runID, ".apothecary/attachments/takeover/image-2.png", pngBytes+"2")
	if m := env.mode(".apothecary/attachments/takeover/image.png"); m != 0o444 {
		t.Errorf("mode = %o, want 444", m)
	}
	if got, _ := att.Get(context.Background(), shot.ID); got.RunID != runID || got.Chat != filestore.ChatTakeOver || got.PingID != "" {
		t.Errorf("claimed = %+v", got)
	}
	q, _ := findPayload[*event.MessageQueued](h.events(t, runID))
	if q == nil || q.MessageID != id || len(q.AttachmentIDs) != 1 || q.AttachmentIDs[0] != shot.ID {
		t.Errorf("message_queued = %+v", q)
	}

	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	// The next stage's ATTACHED FILES are the task's alone: the chat's came
	// with their message.
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("%d stages prompted", len(prompts))
	}
	_, files, _ := strings.Cut(prompts[1], "# ATTACHED FILES")
	if !strings.Contains(files, ".apothecary/attachments/ticket/spec.md") || strings.Contains(files, "takeover/") {
		t.Errorf("attached files of the next stage:\n%s", files)
	}
}

// Files sent to a run parked on its holder wake it like any message: they
// reach the workspace with the run's others, and the resume names them. A
// refused claim leaves it parked, with nothing on the record.
func TestAParkedTakeOverWakesWithFiles(t *testing.T) {
	att, snaps, _ := newFiles(t)
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Attachments, d.Snapshots = att, snaps }, mustFlow(t, askFlowDoc))
	h.e.cfg.AskHumanHold = 30 * time.Millisecond
	r := &heldRunner{firstEnd: make(chan struct{})}
	h.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		r.turn(call.In, "working on it", r.firstEnd)
		return r.drive(ctx, call.In)
	}
	resumed := make(chan string, 1)
	h.runner.ResumeFunc = func(ctx context.Context, call fake.ResumeCall) (agentport.AgentResult, error) {
		resumed <- call.Reply
		r.turn(call.In, "resumed", nil)
		return r.drive(ctx, call.In)
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent[*event.AgentSession](t, h, runID)
	takeOverWhenLive(t, h, runID, "u1")
	close(r.firstEnd)
	waitUntil(t, "the run to park", func() bool { return h.view(t, runID).Dormant })
	h.e.Wait(runID)
	h.e.cfg.AskHumanHold = time.Hour

	shot := uploadAs(t, att, "u1", "image.png", pngBytes)
	theirs := uploadAs(t, att, "u2", "other.png", pngBytes)
	before := len(h.events(t, runID))
	if _, err := h.e.SendMessage(runID, "u1", "look", []string{shot.ID, theirs.ID}); !errors.Is(err, filestore.ErrNotClaimable) {
		t.Fatalf("someone else's file = %v, want ErrNotClaimable", err)
	}
	if n := len(h.events(t, runID)); n != before || !h.view(t, runID).Dormant {
		t.Errorf("a refused message woke the run: %d events (was %d)", n, before)
	}
	if got, _ := att.Get(context.Background(), shot.ID); !got.Unclaimed() {
		t.Errorf("a refused message claimed %+v", got)
	}

	if _, err := h.e.SendMessage(runID, "u1", "", []string{shot.ID}); err != nil {
		t.Fatalf("files to a parked hold: %v", err)
	}
	var reply string
	select {
	case reply = <-resumed:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never resumed")
	}
	if !strings.Contains(reply, "read them with your tools:\n- .apothecary/attachments/takeover/image.png") {
		t.Errorf("resume prompt = %q", reply)
	}
	wantFile(t, h, runID, ".apothecary/attachments/takeover/image.png", pngBytes)
	// The resumed invocation takes the hold back once its turn is under way;
	// a release before then finds nobody holding the live session.
	waitUntil(t, "the resumed turn to end", func() bool {
		return countType(h.events(t, runID), event.TypeAgentMessage) >= 2
	})
	if err := h.e.Release(runID, "u1"); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
}

// Files are refused before anything else where they cannot go anywhere.
func TestATakeOverMessageRefusesFilesItCannotTake(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	if _, err := h.e.SendMessage("r1", "u1", "", []string{"a"}); !errors.Is(err, errAttachmentsOff) {
		t.Errorf("attachments off = %v", err)
	}
	if _, err := h.e.SendMessage("r1", "u1", "", nil); err == nil || !strings.Contains(err.Error(), "message is empty") {
		t.Errorf("nothing at all = %v", err)
	}
	ids := make([]string, filestore.MaxUploadFiles+1)
	if _, err := h.e.SendMessage("r1", "u1", "hi", ids); err == nil || !strings.Contains(err.Error(), "at most 8 files") {
		t.Errorf("too many = %v", err)
	}
}

// The Ask pane's files go to ask/, written by the engine into the
// workspace the pane resolved; one that is not the asker's refuses them all.
func TestDeliverAskFiles(t *testing.T) {
	ctx := context.Background()
	att, snaps, _ := newFiles(t)
	h := newHarnessWith(t, func(_ *harness, d *Deps) { d.Attachments, d.Snapshots = att, snaps }, mustFlow(t, askFlowDoc))
	env, err := h.envp.Provision(ctx, envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	envp := &modeEnvp{EnvProvisioner: h.envp}
	shot := uploadAs(t, att, "u1", "image.png", pngBytes)
	theirs := uploadAs(t, att, "u2", "image.png", pngBytes)
	if _, err := h.e.DeliverAskFiles(ctx, envp, string(env), "r1", "u1", []string{shot.ID, theirs.ID}); !errors.Is(err, filestore.ErrNotClaimable) || strings.Contains(err.Error(), "not found") {
		t.Fatalf("someone else's file = %v", err)
	}
	if got, _ := att.Get(ctx, shot.ID); !got.Unclaimed() {
		t.Errorf("a refused question claimed %+v", got)
	}
	paths, err := h.e.DeliverAskFiles(ctx, envp, string(env), "r1", "u1", []string{shot.ID})
	if err != nil || len(paths) != 1 || paths[0] != ".apothecary/attachments/ask/image.png" {
		t.Fatalf("paths = %v, %v", paths, err)
	}
	if data, err := h.envp.ReadFile(ctx, env, paths[0]); err != nil || string(data) != pngBytes {
		t.Errorf("delivered = %q, %v", data, err)
	}
	if m := envp.mode(paths[0]); m != 0o444 {
		t.Errorf("mode = %o", m)
	}
	if got, _ := att.Get(ctx, shot.ID); got.RunID != "r1" || got.Chat != filestore.ChatAsk {
		t.Errorf("claimed = %+v", got)
	}
	if paths, err := h.e.DeliverAskFiles(ctx, envp, string(env), "r1", "u1", nil); paths != nil || err != nil {
		t.Errorf("no files = %v, %v", paths, err)
	}
	// A workspace that cannot take them refuses the question, and gives the
	// files back.
	again := uploadAs(t, att, "u1", "b.png", pngBytes)
	if _, err := h.e.DeliverAskFiles(ctx, envp, "gone", "r1", "u1", []string{again.ID}); err == nil || !strings.Contains(err.Error(), "delivering the files") {
		t.Errorf("no workspace = %v", err)
	}
	if got, _ := att.Get(ctx, again.ID); !got.Unclaimed() || got.Chat != "" {
		t.Errorf("an undelivered question kept its file: %+v", got)
	}
	// One file written before the next failed is taken away again.
	envp.failOn = ".apothecary/attachments/ask/c.png"
	third := uploadAs(t, att, "u1", "c.png", pngBytes)
	if _, err := h.e.DeliverAskFiles(ctx, envp, string(env), "r1", "u1", []string{again.ID, third.ID}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("a failed write = %v", err)
	}
	if _, err := h.envp.ReadFile(ctx, env, ".apothecary/attachments/ask/b.png"); err == nil {
		t.Error("a refused question left its file in the workspace")
	}
	for _, id := range []string{again.ID, third.ID} {
		if got, _ := att.Get(ctx, id); !got.Unclaimed() || got.Chat != "" {
			t.Errorf("a refused question kept %+v", got)
		}
	}
	if paths, _ := att.ForRun(ctx, "r1"); len(paths) != 1 {
		t.Errorf("the run's files = %+v, want the one delivered", paths)
	}
	// Given back, they can be asked with again.
	envp.failOn = ""
	paths, err = h.e.DeliverAskFiles(ctx, envp, string(env), "r1", "u1", []string{again.ID, third.ID})
	if err != nil || !slices.Equal(paths, []string{".apothecary/attachments/ask/b.png", ".apothecary/attachments/ask/c.png"}) {
		t.Errorf("asked again = %v, %v", paths, err)
	}
	off := newHarness(t, mustFlow(t, askFlowDoc))
	if _, err := off.e.DeliverAskFiles(ctx, envp, string(env), "r1", "u1", []string{"a"}); !errors.Is(err, errAttachmentsOff) {
		t.Errorf("attachments off = %v", err)
	}
}
