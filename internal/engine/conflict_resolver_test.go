package engine

import (
	"context"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/run"
	"apothecary/internal/runinput"
)

// The built-in conflict-resolver template runs as written: started on
// someone's merge request that conflicts with its moved target, it merges
// the target in, has the resolver settle the conflict, tests and pushes to
// the merge request's own branch (leased against its head); a later conflict
// is taken on after asking; the merge request closing ends the run.
func TestBuiltinConflictResolverTemplateRuns(t *testing.T) {
	src, ok := builtin.FlowYAML("conflict-resolver")
	if !ok {
		t.Fatal("no built-in conflict-resolver")
	}
	src = strings.NewReplacer("  harness: opencode\n", "  harness: fake\n  model: fake/model\n").Replace(src)
	ph := newPhasedHarness(t, src)
	git := newMergeGit("main")
	// alice's branch: one commit on main as it was; main has moved since.
	git.parents["a1"] = []string{"c0"}
	git.remote["feature/cache"] = "a1"
	git.head = "a1"
	git.conflicts = 1
	tip := git.advanceTarget("main")
	inner := ph.runner.RunFunc
	ph.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if call.Spec.Agent == "resolver" {
			git.mu.Lock()
			git.markers = nil // the resolver edits the markers away
			git.mu.Unlock()
		}
		return inner(ctx, call)
	}
	ref := ph.repoHost.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	ph.repoHost.SetTargetSHA(ref.IID, tip)
	ph.repoHost.SetMergeable(ref.IID, repoport.MergeConflicts, "a1")
	// A push lands on the merge request: the repository host sees the new
	// head and recomputes — it merges cleanly now.
	ph.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if cmd.Dir != "repo" {
			return envport.ExecResult{}, nil
		}
		res, err := git.exec(env, cmd)
		if strings.HasPrefix(strings.TrimSpace(cmd.Script), "git push") && res.ExitCode == 0 {
			git.mu.Lock()
			head := git.remote["feature/cache"]
			git.mu.Unlock()
			ph.repoHost.SetHead(ref.IID, head)
			ph.repoHost.SetMergeable(ref.IID, repoport.MergeClean, head)
		}
		return res, err
	}

	runID, err := ph.e.CreateRunWith(context.Background(), runinput.Ticket{Title: "Add caching (!1 by alice)"}, "conflict-resolver", testProject, runinput.Input{MR: &runinput.MR{IID: ref.IID}})
	if err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseRuns(t, runID, flow.MainPhase, 1)
	merges, _, pushes, paused := git.snapshot()
	if merges != 1 || paused != "" || len(pushes) != 1 || pushes[0] != "lease:feature/cache" {
		t.Fatalf("merges %d paused %q pushes %v: one merge, pushed leased to the merge request's branch", merges, paused, pushes)
	}
	var done []string
	for _, e := range ph.events(t, runID) {
		if s, ok := e.Payload.(*event.StageCompleted); ok {
			done = append(done, s.StageID)
		}
	}
	if got := strings.Join(done, " "); got != "settle.resolve settle.sync settle.test settle.push" {
		t.Errorf("stages completed: %s", got)
	}

	// The target moves again and the merge request conflicts anew: asked
	// first, then settled.
	git.mu.Lock()
	head := git.remote["feature/cache"]
	git.conflicts = 1
	git.mu.Unlock()
	tip = git.advanceTarget("main")
	ph.repoHost.SetTargetSHA(ref.IID, tip)
	ph.repoHost.SetMergeable(ref.IID, repoport.MergeConflicts, head)
	ask := ph.waitPing(t, event.PingPhaseConfirm)
	if err := ph.e.AnswerPing(ask.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	ph.waitPhaseRuns(t, runID, "again", 1)
	if merges, _, pushes, _ = git.snapshot(); merges != 2 || len(pushes) != 2 {
		t.Fatalf("after the second conflict: merges %d pushes %v", merges, pushes)
	}

	ph.repoHost.SetMRState(ref.IID, repoport.MRClosed)
	ph.waitState(t, runID, run.Completed)
}
