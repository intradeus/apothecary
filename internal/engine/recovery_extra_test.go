package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// Crash between stage_failed and the edge decision: recovery routes the
// persisted failure directly — the command is NOT re-executed.
func TestCrashAfterFailureRoutesWithoutReexec(t *testing.T) {
	fl := mustFlow(t, loopFlowDoc)
	h := newHarness(t, fl)
	envID, _ := h.envp.Provision(context.Background(), ports.EnvSpec{RunID: "r-f"})
	ctx := context.Background()
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: testProject, FlowHash: fl.Hash, Branch: "b"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.StageCompleted{StageID: "build", TurnsUsed: 1},
		&event.StageStarted{StageID: "test", Iteration: 1},
		&event.StageFailed{StageID: "test", Reason: event.ReasonExitNonzero, Error: "FAIL: persisted output tail"},
		// kill -9 here, before edge_taken
	} {
		if _, err := h.store.Append(ctx, "r-f", p); err != nil {
			t.Fatal(err)
		}
	}
	verifyRuns := 0
	h.envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if strings.Contains(cmd.Script, "make lint") {
			verifyRuns++
		}
		return ports.ExecResult{ExitCode: 0}, nil
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-f")

	if v := h.view(t, "r-f"); v.State != run.Completed {
		t.Fatalf("state = %s, events: %v", v.State, eventTypes(h.events(t, "r-f")))
	}
	// The failed test run was routed, not re-executed: verify ran exactly
	// once (the fresh attempt after the rebuild).
	if verifyRuns != 1 {
		t.Errorf("verify executed %d times after recovery, want 1", verifyRuns)
	}
	et, ok := findPayload[*event.EdgeTaken](h.events(t, "r-f"))
	if !ok || et.Feed != "FAIL: persisted output tail" {
		t.Errorf("edge_taken = %+v, want persisted feed", et)
	}
	if len(h.runner.RunCalls) != 1 {
		t.Fatalf("builder ran %d times, want 1", len(h.runner.RunCalls))
	}
	if p := h.runner.RunCalls[0].In.Prompt; !strings.Contains(p, "FAIL: persisted output tail") {
		t.Errorf("rebuild prompt lost the failure feed:\n%s", p)
	}
}

// Crash between edge_taken and the next stage_started: the feed is persisted
// in the event and survives.
func TestCrashAfterEdgeKeepsFeed(t *testing.T) {
	fl := mustFlow(t, loopFlowDoc)
	h := newHarness(t, fl)
	envID, _ := h.envp.Provision(context.Background(), ports.EnvSpec{RunID: "r-e"})
	ctx := context.Background()
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: testProject, FlowHash: fl.Hash, Branch: "b"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "build", Iteration: 1},
		&event.StageCompleted{StageID: "build"},
		&event.StageStarted{StageID: "test", Iteration: 1},
		&event.StageFailed{StageID: "test", Reason: event.ReasonExitNonzero, Error: "FAIL: x"},
		&event.EdgeTaken{From: "test", To: "build", Edge: "on_fail", Count: 1, Max: 5, Feed: "FAIL: x"},
		// kill -9 here
	} {
		if _, err := h.store.Append(ctx, "r-e", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.Wait("r-e")

	if v := h.view(t, "r-e"); v.State != run.Completed {
		t.Fatalf("state = %s", v.State)
	}
	found := false
	for _, c := range h.runner.RunCalls {
		if strings.Contains(c.In.Prompt, "FAIL: x") && strings.Contains(c.In.Prompt, "(iteration 2 of 6)") {
			found = true
		}
	}
	if !found {
		t.Error("no builder prompt carried the persisted feed after recovery")
	}
}

// Recovery must refresh /apothecary/run.env so the workspace shim can
// re-authenticate with the regenerated token.
func TestRecoveryRefreshesRunEnvToken(t *testing.T) {
	fl := mustFlow(t, loopFlowDoc)
	h := newHarness(t, fl)
	envID, _ := h.envp.Provision(context.Background(), ports.EnvSpec{RunID: "r-t"})
	ctx := context.Background()
	block := make(chan struct{})
	h.runner.RunFunc = func(c context.Context, call fake.RunCall) (ports.AgentResult, error) {
		<-block
		return ports.AgentResult{TurnsUsed: 1}, nil
	}
	for _, p := range []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: "loop", ProjectID: testProject, FlowHash: fl.Hash, Branch: "b"},
		&event.EnvRequested{},
		&event.EnvReady{EnvID: string(envID)},
		&event.StageStarted{StageID: "build", Iteration: 1},
	} {
		if _, err := h.store.Append(ctx, "r-t", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	// Wait until the recovered loop reaches the agent stage, then check the file.
	deadline := time.Now().Add(2 * time.Second)
	var content []byte
	for time.Now().Before(deadline) {
		if data, err := h.envp.ReadFile(ctx, envID, "/apothecary/run.env"); err == nil {
			content = data
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	token := h.e.RunToken("r-t")
	if token == "" || !strings.Contains(string(content), "APOTHECARY_RUN_TOKEN="+token) {
		t.Errorf("run.env not refreshed with new token %q:\n%s", token, content)
	}
	close(block)
	h.e.Wait("r-t")
}

// §14: runs beyond max_concurrent_runs queue in pending, FIFO.
func TestConcurrencyQueueFIFO(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.settings.L.MaxConcurrentRuns = 1
	release := make(chan struct{})
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (ports.AgentResult, error) {
		<-release
		return ports.AgentResult{TurnsUsed: 1}, nil
	}

	id1, err := h.e.CreateRun(context.Background(), Ticket{Title: "first"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := h.e.CreateRun(context.Background(), Ticket{Title: "second"}, "loop", testProject)
	if err != nil {
		t.Fatal(err)
	}

	// Second run must sit in pending while the first holds the slot.
	time.Sleep(20 * time.Millisecond)
	if v := h.view(t, id2); v.State != run.Pending {
		t.Fatalf("queued run state = %s, want pending", v.State)
	}
	if got := len(h.runner.SnapshotRunCalls()); got != 1 {
		t.Fatalf("agents running = %d, want 1", got)
	}

	close(release)
	h.e.Wait(id1)
	h.e.Wait(id2)
	if v := h.view(t, id2); v.State != run.Completed {
		t.Errorf("second run state = %s", v.State)
	}
}
