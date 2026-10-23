package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/filestore"
	"apothecary/internal/ports"
)

// newFiles wires REAL filestore stores over the in-package Mem — the engine's
// two ports are satisfied by the concrete types, so the tests exercise the
// same claim/capture semantics production runs (DESIGN-FILES §5.2).
func newFiles(t *testing.T) (*filestore.Attachments, *filestore.Snapshots, *filestore.Mem) {
	t.Helper()
	mem := filestore.NewMem()
	blobs := filestore.NewBlobs(t.TempDir())
	att := filestore.NewAttachments(mem, blobs, 1<<20, []string{"text/*", "application/pdf", "image/png"})
	snaps := filestore.NewSnapshots(mem, blobs, 1<<20)
	return att, snaps, mem
}

// snapshotsFor lists a run's captured snapshots via the store.
func snapshotsFor(t *testing.T, snaps *filestore.Snapshots, runID string) []filestore.Snapshot {
	t.Helper()
	out, err := snaps.ForRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func snapshotBody(t *testing.T, snaps *filestore.Snapshots, id string) string {
	t.Helper()
	data, _, err := snaps.Bytes(context.Background(), id)
	if err != nil {
		t.Fatalf("snapshot %s bytes: %v", id, err)
	}
	return string(data)
}

// Goal attachments are delivered into the workspace (under the git-excluded
// .apothecary/ tree) AND listed in the agent's prompt so it can read them.
func TestGoalAttachmentsDeliveredAndPrompted(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	att, snaps, _ := newFiles(t)
	h.e.SetFiles(att, snaps)

	up, err := att.Save(context.Background(), "spec.pdf", "application/pdf", strings.NewReader("%PDF-1.4"))
	if err != nil {
		t.Fatal(err)
	}
	runID, err := h.e.CreateRun(context.Background(),
		Ticket{Title: "T", AttachmentIDs: []string{up.ID}}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	// The upload was claimed for the run at create.
	got, err := att.Get(context.Background(), up.ID)
	if err != nil || got.RunID != runID {
		t.Fatalf("attachment not claimed for the run: %+v (%v)", got, err)
	}

	wantPath := ".apothecary/attachments/goal/" + up.ID[:8] + "/spec.pdf"
	// The builder prompt lists the attachment (path + filename + mime).
	var prompt string
	for _, c := range h.runner.RunCalls {
		prompt = c.In.Prompt
	}
	if !strings.Contains(prompt, "# ATTACHED FILES") || !strings.Contains(prompt, wantPath) {
		t.Errorf("prompt missing attached-files section / path:\n%s", prompt)
	}
	// The file was actually written into the workspace.
	env := h.view(t, runID).EnvID
	data, err := h.envp.ReadFile(context.Background(), ports.EnvID(env), wantPath)
	if err != nil || string(data) != "%PDF-1.4" {
		t.Errorf("attachment not delivered into workspace: %q, %v", data, err)
	}
}

// The provisioning window of the workspace exec log (clone + build +
// bootstrap) is captured as a log snapshot under the pseudo-stage id — it
// used to live only in the adapter's exec.log and die with the workspace.
func TestProvisionLogPublished(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	att, snaps, _ := newFiles(t)
	h.e.SetFiles(att, snaps)

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	var log string
	for _, sn := range snapshotsFor(t, snaps, runID) {
		if sn.StageID == provisionLogStage && sn.Kind == filestore.KindLog {
			if sn.Iteration != 1 {
				t.Errorf("provision log iteration = %d, want 1", sn.Iteration)
			}
			log = snapshotBody(t, snaps, sn.ID)
		}
	}
	if !strings.Contains(log, "git clone") {
		t.Errorf("provision log = %q, want the bootstrap transcript", log)
	}
}

// A bootstrap failure ends the run with env_failed — exactly when a human
// needs the provisioning log, and exactly when it used to vanish with the
// workspace. The capture must happen on that exit path too.
func TestProvisionLogPublishedOnBootstrapFailure(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	att, snaps, _ := newFiles(t)
	h.e.SetFiles(att, snaps)

	h.envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "git clone") {
			return ports.ExecResult{ExitCode: 1, Output: "fatal: repository not found"}, nil
		}
		return ports.ExecResult{}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	var log string
	for _, sn := range snapshotsFor(t, snaps, runID) {
		if sn.StageID == provisionLogStage && sn.Kind == filestore.KindLog {
			log = snapshotBody(t, snaps, sn.ID)
		}
	}
	if !strings.Contains(log, "git clone") {
		t.Errorf("provision log after bootstrap failure = %q, want the transcript", log)
	}
}

// TestAttemptSnapshotsSurviveRetries is the point of the attempt record:
// attempt 1's output stays inspectable after attempt 2 overwrites the file.
// Before, a retry left nothing behind — the workspace holds one version per
// path and the event store kept only 8 KB tails, so "the plan the human
// rejected" was gone.
func TestAttemptSnapshotsSurviveRetries(t *testing.T) {
	doc := `name: snap
version: 2
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: plan, type: agent, agent: planner, input: { failure: $FAILURE }, on_success: check }
  - { id: check, type: command, run: verify, on_success: done,
      on_fail: { to: plan, max: 1 } }
`
	h := newHarness(t, mustFlow(t, doc))
	att, snaps, _ := newFiles(t)
	h.e.SetFiles(att, snaps)

	attempt := 0
	h.agentScript(map[string]func(fake.RunCall) (ports.AgentResult, error){
		"planner": func(c fake.RunCall) (ports.AgentResult, error) {
			attempt++
			h.writeFile(t, c.Env, "artifacts/plan.md", fmt.Sprintf("plan revision %d", attempt))
			return ports.AgentResult{}, nil
		},
	})
	// The check fails once, so the planner runs twice and rewrites the file.
	checks := 0
	h.envp.ExecFunc = func(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if !strings.Contains(cmd.Script, "verify") {
			return ports.ExecResult{}, nil
		}
		checks++
		if checks == 1 {
			return ports.ExecResult{ExitCode: 1, Output: "boom"}, nil
		}
		return ports.ExecResult{}, nil
	}

	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "snap", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	// Both revisions are captured, attributed to their attempt, with the
	// role and path in their OWN fields (no OutputName overload).
	want := map[int]string{1: "plan revision 1", 2: "plan revision 2"}
	found := map[int]string{}
	for _, sn := range snapshotsFor(t, snaps, runID) {
		if sn.StageID == "plan" && sn.Kind == filestore.KindOutput {
			found[sn.Iteration] = snapshotBody(t, snaps, sn.ID)
			if sn.Name != "plan" || sn.Path != "artifacts/plan.md" {
				t.Errorf("snapshot name/path = %q/%q, want plan/artifacts/plan.md", sn.Name, sn.Path)
			}
		}
	}
	for iter, body := range want {
		if found[iter] != body {
			t.Errorf("attempt %d snapshot = %q, want %q", iter, found[iter], body)
		}
	}

	// The failing command's whole log is captured too, not just its tail.
	var log string
	for _, sn := range snapshotsFor(t, snaps, runID) {
		if sn.StageID == "check" && sn.Kind == filestore.KindLog {
			log = snapshotBody(t, snaps, sn.ID)
		}
	}
	if !strings.Contains(log, "boom") {
		t.Errorf("check log = %q, want the command's output", log)
	}

	// Every snapshot is linked from the timeline by reference, never by value.
	published := 0
	for _, ev := range h.events(t, runID) {
		if p, ok := ev.Payload.(*event.SnapshotCaptured); ok {
			published++
			if p.SnapshotID == "" || p.Kind == "" {
				t.Errorf("snapshot_captured carries no id/kind: %+v", p)
			}
		}
	}
	if published < 3 { // 2 plan outputs + at least one log
		t.Errorf("snapshot_captured count = %d, want the outputs and logs of every attempt", published)
	}
}

// A ping's attached files are snapshotted when the ping is RAISED, which is
// what makes the event log's file refs mean anything later: they used to be
// repo-relative paths read live out of the workspace, so an answered ping
// stopped being answerable the moment the workspace was reaped — and
// re-opening one showed the file's CURRENT content, not what was reviewed.
func TestPingAttachmentsAreSnapshotted(t *testing.T) {
	doc := `name: ping
version: 2
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
stages:
  - { id: build, type: agent, agent: builder }
`
	h := newHarness(t, mustFlow(t, doc))
	att, snaps, _ := newFiles(t)
	h.e.SetFiles(att, snaps)

	runIDCh := make(chan string, 1)
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (ports.AgentResult, error) {
		id := <-runIDCh
		h.writeFile(t, call.Env, "artifacts/dbs.md", "as reviewed")
		p, err := h.e.RaisePing(id, "clarification", "which db?", "", nil, []string{"artifacts/dbs.md"})
		if err != nil {
			return ports.AgentResult{}, err
		}
		if _, ok := p.Await(context.Background()); !ok {
			return ports.AgentResult{}, fmt.Errorf("ping was not answered")
		}
		// The agent keeps working and rewrites the very file the human was
		// shown. The record must not follow it.
		h.writeFile(t, call.Env, "artifacts/dbs.md", "rewritten afterwards")
		return ports.AgentResult{TurnsUsed: 1}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), Ticket{Title: "T"}, "ping", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- runID

	p := h.waitPing(t, "clarification")
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "postgres"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)

	// Captured under the ping, the ref verbatim in Path (its own field).
	pingSnaps, err := snaps.ForPing(context.Background(), p.ID)
	if err != nil || len(pingSnaps) != 1 {
		t.Fatalf("ping snapshots = %+v (%v), want exactly one", pingSnaps, err)
	}
	sn := pingSnaps[0]
	if sn.Kind != filestore.KindPingFile || sn.Path != "artifacts/dbs.md" || sn.Filename != "dbs.md" {
		t.Errorf("snapshot = %+v, want a ping_file named by its ref", sn)
	}

	// The snapshot holds what the human was shown — not the rewrite.
	if got := snapshotBody(t, snaps, sn.ID); got != "as reviewed" {
		t.Errorf("snapshot body = %q, want the content as reviewed", got)
	}

	// And it survives the workspace: destroying the env leaves the record
	// intact (reads go through the store, never the env).
	if err := h.envp.Destroy(context.Background(), ports.EnvID(h.view(t, runID).EnvID)); err != nil {
		t.Fatal(err)
	}
	if got := snapshotBody(t, snaps, sn.ID); got != "as reviewed" {
		t.Errorf("post-destroy read = %q", got)
	}
	// The event carries the ping id — the log alone answers "what did this
	// ping show" (DESIGN-FILES §4).
	linked := false
	for _, ev := range h.events(t, runID) {
		if sc, ok := ev.Payload.(*event.SnapshotCaptured); ok && sc.PingID == p.ID {
			linked = true
			if sc.Path != "artifacts/dbs.md" || sc.Kind != string(filestore.KindPingFile) {
				t.Errorf("snapshot_captured for ping = %+v", sc)
			}
		}
	}
	if !linked {
		t.Error("no snapshot_captured event names the ping")
	}
}
