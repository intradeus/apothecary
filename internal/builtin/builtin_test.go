package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/actionspec"
	"apothecary/internal/flow"
	"apothecary/internal/signalspec"
)

// builderFlow is the template most of these tests check, by its name.
const builderFlow = "builder"

// TestDefaultFlowBindingsClean is the wiring invariant this feature exists
// for: the default flow's stage bindings satisfy its agents' declared I/O
// contract with zero warnings. Contract VIOLATIONS are parse errors since v3
// (the agents are in the document), so reaching this test at all proves the
// errors are clean; what is left to assert is that nothing merely suspicious
// survives either.
func TestDefaultFlowBindingsClean(t *testing.T) {
	for _, w := range Flow(builderFlow).Warnings {
		t.Errorf("binding warning: %s", w)
	}
}

// TestBuiltinFlowsBindCleanly: every built-in template parses and binds with
// zero warnings — each is what a human copies to start
// from. Reaching the accessor at all proves they parse (load panics).
func TestBuiltinFlowsBindCleanly(t *testing.T) {
	names := FlowNames()
	if len(names) == 0 {
		t.Fatal("no built-in flows")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			f := Flow(name)
			if f == nil {
				t.Fatal("not loaded")
			}
			for _, w := range f.Warnings {
				t.Errorf("binding warning: %s", w)
			}
			for i := range f.Stages {
				if s := &f.Stages[i]; s.Type == flow.StageHumanGate && s.Question == "" {
					t.Errorf("gate %s has no question", s.ID)
				}
			}
		})
	}
}

// TestShippedFlowsBindCleanly: the flows this repo ships as references parse
// and bind their own agents' contracts with zero warnings. They are what a
// human copies to start from, so a stale one teaches the wrong grammar.
// (The built-in default is covered above; these are the two on disk.)
func TestShippedFlowsBindCleanly(t *testing.T) {
	for _, path := range []string{"../../examples/flows/v5.yaml", "../flow/testdata/v5.yaml"} {
		t.Run(path, func(t *testing.T) {
			f, err := flow.Load(path)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			for _, w := range f.Warnings {
				t.Errorf("binding warning: %s", w)
			}
		})
	}
}

// TestBuiltinPromptsBindingNeutral guards the rewording: a prompt must not name
// a header the engine no longer renders, nor an artifact path the flow is free
// to rebind. What a stage is given is described in its INPUT sections.
func TestBuiltinPromptsBindingNeutral(t *testing.T) {
	for _, sp := range Flow(builderFlow).Agents {
		for _, stale := range []string{"PREVIOUS ATTEMPT FEEDBACK", "# ARTIFACT:", "artifacts/plan.md", "artifacts/verdict.json"} {
			if strings.Contains(sp.Prompt, stale) {
				t.Errorf("agent %q prompt names %q, which the flow controls — describe the input instead", sp.Name, stale)
			}
		}
	}
}

// TestDefaultsParse locks the compiled-in defaults: the binary must never
// ship a default that fails to parse (load panics on the first accessor).
func TestDefaultsParse(t *testing.T) {
	f := Flow(builderFlow)
	if f == nil {
		t.Fatalf("no built-in flow named %q", builderFlow)
	}
	if len(f.Agents) == 0 {
		t.Fatal("the default flow declares no agents")
	}
}

// TestDefaultFlowPhilosophy: every agent referenced by the default flow is
// a built-in agent (a fresh install must resolve the whole flow), every
// agent/command stage declares on_fail, and every on_fail chain ends at the
// debug leaf — a human with the workspace, then the same stage again, so the
// success chain replays from the failure point and never skips a stage.
func TestDefaultFlowPhilosophy(t *testing.T) {
	f := Flow(builderFlow)
	for i := range f.Stages {
		s := &f.Stages[i]
		if s.Type == flow.StageAgent && f.Agent(s.Agent) == nil {
			t.Errorf("stage %s references agent %q, which the flow does not declare", s.ID, s.Agent)
		}
		if s.Type != flow.StageAgent && s.Type != flow.StageCommand {
			continue
		}
		e := s.Edge(flow.EdgeFail)
		if e == nil {
			t.Errorf("stage %s: no on_fail", s.ID)
			continue
		}
		for e.Then != nil {
			e = e.Then
		}
		if e.To != flow.ReservedDebug {
			t.Errorf("stage %s: on_fail chain ends at %q, want the debug leaf", s.ID, e.To)
		}
	}
	for i := range f.Stages {
		if s := &f.Stages[i]; s.Type == flow.StageHumanGate && showsWorkspace(s) {
			t.Errorf("gate %s hands out the workspace by hand; that is the debug leaf's job", s.ID)
		}
	}
	// The run's whole life: the merge request ends it — no final gate — and
	// the watchers are the conflict fix (asked first) and the debounced
	// review round.
	if f.Main() == nil || f.StartsOnMR() || f.Stage("final_gate") != nil {
		t.Fatal("the default flow starts on a ticket, has a main chain and no final gate")
	}
	conflicts, review := f.Phase("conflicts"), f.Phase("review_fixes")
	if conflicts == nil || conflicts.Kind != flow.PhaseWatcher || conflicts.On.Start != flow.StartConfirm ||
		review == nil || review.Kind != flow.PhaseWatcher || review.On.QuietFor == 0 {
		t.Errorf("watchers: conflicts %+v review %+v", conflicts, review)
	}
	if f.Agent("replier").ReviewRepliesOutput() == nil {
		t.Error("the replier must declare a review_replies output, or bad replies reach the repository")
	}
	// The run ends in an outcome: merged finishes it (the ticket's done is
	// best effort), closed unmerged sends the ticket back and abandons it.
	merged, closed := f.Phase("merged"), f.Phase("closed")
	if merged == nil || !merged.IsOutcome() || closed == nil || !closed.IsOutcome() {
		t.Fatalf("outcomes: merged %+v closed %+v", merged, closed)
	}
	if st := f.Stage("ticket_done"); st == nil || st.Phase != "merged" || st.OnSuccess != flow.TerminalDone ||
		st.Edge(flow.EdgeFail) == nil || st.Edge(flow.EdgeFail).To != flow.TerminalDone {
		t.Errorf("ticket_done = %+v, want best effort inside the merged outcome", st)
	}
	if st := f.Stage("ticket_back"); st == nil || st.Phase != "closed" || st.OnSuccess != flow.TerminalAbort ||
		st.Edge(flow.EdgeFail) == nil || st.Edge(flow.EdgeFail).To != flow.TerminalAbort {
		t.Errorf("ticket_back = %+v, want the closed outcome to abandon the run either way", st)
	}
	for i := range f.Stages {
		if s := &f.Stages[i]; s.Type == flow.StageWait {
			t.Errorf("stage %s: the default parks on nothing — its outcomes end the run", s.ID)
		}
	}
	// Every human_gate says what it is asking: a gate with no question would
	// park the run on an unexplained decision.
	for i := range f.Stages {
		if s := &f.Stages[i]; s.Type == flow.StageHumanGate && s.Question == "" {
			t.Errorf("gate %s has no question", s.ID)
		}
	}
	// The MR is an action, and the reviewer's verdict is what makes the review
	// stage's on_reject reachable.
	if mr := f.Stage("mr"); mr.Type != flow.StageAction || mr.Action != actionspec.CreateMR {
		t.Errorf("mr stage = %s/%s, want action/%s", mr.Type, mr.Action, actionspec.CreateMR)
	}
	if f.Agent("reviewer").VerdictOutput() == nil {
		t.Error("the reviewer must declare a verdict output, or review.on_reject can never fire")
	}
}

// showsWorkspace reports whether a gate hands the human the workspace links.
func showsWorkspace(st *flow.Stage) bool {
	for _, in := range st.Inputs {
		if in.Value == flow.SentinelWorkspace {
			return true
		}
	}
	return false
}

// TestDefaultFlowRetryChannelDeclared: every stage an on_fail or on_reject
// edge loops back to must bind the matching sentinel, or the retry would run
// blind — the agent would repeat itself with no idea why it was sent back.
func TestDefaultFlowRetryChannelDeclared(t *testing.T) {
	f := Flow(builderFlow)
	for i := range f.Stages {
		for kind, e := range f.Stages[i].Edges {
			for link := e; link != nil; link = link.Then {
				tgt := f.Stage(link.To)
				if tgt == nil || tgt.Type != flow.StageAgent {
					continue // gates and commands cannot read feedback
				}
				want := flow.FeedSentinel(kind)
				bound := false
				for _, in := range tgt.Inputs {
					if in.Value == want {
						bound = true
					}
				}
				if !bound {
					t.Errorf("stage %s: %s loops back to agent stage %s, which binds no %s",
						f.Stages[i].ID, kind, tgt.ID, want)
				}
			}
		}
	}
}

func TestFlowSourceLayering(t *testing.T) {
	db := flow.MapSource{}
	src := FlowSource(db)

	// Empty DB: the built-in resolves.
	f, err := src.Get(context.Background(), "prj", builderFlow)
	if err != nil || f.Name != builderFlow {
		t.Fatalf("Get(default) = %v, %v", f, err)
	}
	// Unknown names still miss.
	if _, err := src.Get(context.Background(), "prj", "nope"); !errors.Is(err, flow.ErrNotFound) {
		t.Fatalf("Get(nope) err = %v, want ErrNotFound", err)
	}
	// A DB row with the same name shadows the built-in.
	shadow, err := flow.Parse("default", []byte("name: default\nversion: 5\nparameters: { harness: fake, model: a/b }\nmain:\n  - { id: s, type: command, run: \"true\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	db[builderFlow] = shadow
	f, err = src.Get(context.Background(), "prj", builderFlow)
	if err != nil || len(f.Stages) != 1 {
		t.Fatalf("shadowed Get(default) = %v, %v — the DB must win", f, err)
	}
}

// TestDefaultFlowNeverDropsARunUnasked holds the default flow to its own
// failure philosophy: every agent and command stage has a failure route, and
// no chain runs out on its own — once its retries are spent, every edge of a
// stage that can run again reaches a person (the debug leaf, a gate) or an
// end the flow states (end, done, abort). An exhausted chain would otherwise
// abandon the run without anyone being asked. And a watcher never ends the
// run, nor is it capped.
func TestDefaultFlowNeverDropsARunUnasked(t *testing.T) {
	f := Flow(builderFlow)
	reserved := map[string]bool{flow.TerminalEnd: true, flow.TerminalDone: true, flow.TerminalAbort: true, flow.ReservedDebug: true}
	// A chain can only run out on a stage that runs more than once in a
	// phase run: one on a cycle of routes. A one-shot stage's max-1 detour
	// (a best-effort tracker move going on to the next stage) never exhausts.
	next := map[string][]string{}
	for i := range f.Stages {
		st := &f.Stages[i]
		next[st.ID] = append(next[st.ID], st.OnSuccess)
		for _, kind := range []string{flow.EdgeFail, flow.EdgeReject, flow.EdgeTimeout} {
			for link := st.Edge(kind); link != nil; link = link.Then {
				next[st.ID] = append(next[st.ID], link.To)
			}
		}
	}
	onCycle := func(id string) bool {
		seen := map[string]bool{}
		stack := append([]string(nil), next[id]...)
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if cur == id {
				return true
			}
			if seen[cur] || reserved[cur] {
				continue
			}
			seen[cur] = true
			stack = append(stack, next[cur]...)
		}
		return false
	}
	for i := range f.Stages {
		st := &f.Stages[i]
		if (st.Type == flow.StageAgent || st.Type == flow.StageCommand) && st.Edge(flow.EdgeFail) == nil {
			t.Errorf("stage %s has no on_fail: its failure would be a dead end", st.ID)
		}
		if ph := f.Phase(st.Phase); ph != nil && ph.Fires() && st.OnSuccess == flow.TerminalDone {
			t.Errorf("stage %s: watcher %s ends the whole run", st.ID, ph.ID)
		}
		if !onCycle(st.ID) {
			continue
		}
		for _, kind := range []string{flow.EdgeFail, flow.EdgeReject, flow.EdgeTimeout} {
			link := st.Edge(kind)
			for link != nil && link.Then != nil {
				link = link.Then
			}
			if link == nil {
				continue
			}
			if !reserved[link.To] && link.Max != flow.UnlimitedMax {
				if tgt := f.Stage(link.To); tgt == nil || tgt.Type != flow.StageHumanGate {
					t.Errorf("stage %s: %s runs out after %d at %s with nowhere to go — end the chain with then: debug", st.ID, kind, link.Max, link.To)
				}
			}
		}
	}
	for i := range f.Phases {
		if ph := &f.Phases[i]; ph.Fires() && ph.On.Max != flow.UnlimitedMax {
			t.Errorf("watcher %s is capped at %d runs; watchers run as often as their signal brings something new", ph.ID, ph.On.Max)
		}
	}
}

// TestReviewerTemplate: the reviewer is started on someone's merge request,
// reviews it through one block from its main chain and again after the
// author's pushes, never builds or pushes, and ends with the merge request.
func TestReviewerTemplate(t *testing.T) {
	f := Flow("reviewer")
	if f == nil {
		t.Fatal("no built-in reviewer")
	}
	main, again := f.Main(), f.Phase("again")
	if !f.StartsOnMR() || main == nil || !main.OnMR() {
		t.Fatalf("main = %+v, run_start %q", main, f.StartsOn)
	}
	if again == nil || again.Kind != flow.PhaseWatcher || again.On.Signal != signalspec.MRHeadMoved || again.On.QuietFor < time.Minute {
		t.Errorf("again = %+v", again)
	}
	for _, id := range []string{"merged", "closed"} {
		if ph := f.Phase(id); ph == nil || !ph.IsOutcome() || ph.Then != flow.TerminalDone {
			t.Errorf("outcome %s = %+v", id, ph)
		}
	}
	for _, id := range []string{"review.post", "rereview.post"} {
		if st := f.Stage(id); st == nil || st.Action != actionspec.PostReview {
			t.Errorf("stage %s = %+v", id, st)
		}
	}
	for i := range f.Stages {
		st := &f.Stages[i]
		if st.Type == flow.StageAction && (st.Action == actionspec.PushBranch || st.Action == actionspec.CreateMR || st.Action == actionspec.MRMerge) {
			t.Errorf("stage %s: the reviewer never pushes, opens or merges", st.ID)
		}
	}
	if sp := f.Agent("pr_reviewer"); sp == nil || !sp.ReadOnly || sp.ReviewThreadsOutput() == nil {
		t.Errorf("pr_reviewer = %+v", sp)
	}
	if p := f.Param("review_gate"); p == nil || p.Default != "false" {
		t.Errorf("review_gate = %+v: a review posts by itself unless a workshop asks", p)
	}
}

// TestConflictResolverTemplate: started on a merge request, it takes the
// target in, resolves, tests and pushes to the merge request's branch —
// again on each new conflict, after asking — and ends with the merge
// request. It never opens, merges or reviews one.
func TestConflictResolverTemplate(t *testing.T) {
	f := Flow("conflict-resolver")
	if f == nil {
		t.Fatal("no built-in conflict-resolver")
	}
	main, again := f.Main(), f.Phase("again")
	if !f.StartsOnMR() || main == nil || !main.OnMR() {
		t.Fatalf("main = %+v, run_start %q", main, f.StartsOn)
	}
	if again == nil || again.Kind != flow.PhaseWatcher || again.On.Signal != signalspec.MRConflicts || again.On.Start != flow.StartConfirm {
		t.Errorf("again = %+v: a later conflict is taken on after asking, by default", again)
	}
	for _, id := range []string{"merged", "closed"} {
		if ph := f.Phase(id); ph == nil || !ph.IsOutcome() || ph.Then != flow.TerminalDone {
			t.Errorf("outcome %s = %+v", id, ph)
		}
	}
	for _, inc := range []string{"settle", "resettle"} {
		for stage, action := range map[string]string{"sync": actionspec.SyncTarget, "push": actionspec.PushBranch} {
			if st := f.Stage(inc + "." + stage); st == nil || st.Action != action {
				t.Errorf("stage %s.%s = %+v", inc, stage, st)
			}
		}
		if st := f.Stage(inc + ".resolve"); st == nil || st.Agent != "resolver" {
			t.Errorf("stage %s.resolve = %+v", inc, st)
		}
	}
	for i := range f.Stages {
		st := &f.Stages[i]
		if st.Type == flow.StageAction && (st.Action == actionspec.CreateMR || st.Action == actionspec.MRMerge || st.Action == actionspec.PostReview) {
			t.Errorf("stage %s: the conflict resolver never opens, merges or reviews a merge request", st.ID)
		}
	}
	if sp := f.Agent("resolver"); sp == nil || sp.ReadOnly {
		t.Errorf("resolver = %+v: it edits the conflicting files", sp)
	}
	if p := f.Param("strategy"); p == nil || p.Default != "merge" {
		t.Errorf("strategy = %+v: someone's commits are not rewritten unless a workshop asks", p)
	}
}
