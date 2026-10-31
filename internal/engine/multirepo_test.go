package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/mrs"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/runinput"
)

// fakeGit is enough of git for the engine's publish stages: branches
// pointing at commits, a parent chain, a remote holding what was pushed, and
// the handful of commands the engine issues — parsed from the scripts it
// sends through the fake provisioner. One per checkout dir.
type fakeGit struct {
	mu       sync.Mutex
	branches map[string]string
	parents  map[string]string
	remote   map[string]string
	head     string
	n        int
	// noChanges: the commit command finds nothing to commit (an untouched
	// extra repo).
	noChanges bool
	pushes    []string
}

func newFakeGit(source string) *fakeGit {
	g := &fakeGit{branches: map[string]string{}, parents: map[string]string{}, remote: map[string]string{}}
	g.parents["c0"] = ""
	g.remote[source] = "c0"
	return g
}

func unq(s string) string { return strings.Trim(s, `'"`) }

func (g *fakeGit) resolve(ref string) string {
	ref = unq(ref)
	if strings.HasPrefix(ref, "origin/") {
		return g.remote[strings.TrimPrefix(ref, "origin/")]
	}
	if ref == "HEAD" {
		return g.branches[g.head]
	}
	if tip, ok := g.branches[ref]; ok {
		return tip
	}
	if _, ok := g.parents[ref]; ok {
		return ref
	}
	return ""
}

func (g *fakeGit) commit(parent string) string {
	g.n++
	id := fmt.Sprintf("c%d", g.n)
	g.parents[id] = parent
	return id
}

// chain lists the commits of tip down to (excluding) base, oldest first.
func (g *fakeGit) chain(tip, base string) []string {
	var out []string
	for c := tip; c != "" && c != base; c = g.parents[c] {
		out = append([]string{c}, out...)
	}
	return out
}

func (g *fakeGit) exec(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := strings.TrimSpace(cmd.Script)
	f := strings.Fields(s)
	ok := func(out string) (envport.ExecResult, error) { return envport.ExecResult{ExitCode: 0, Output: out}, nil }
	fail := func(out string) (envport.ExecResult, error) { return envport.ExecResult{ExitCode: 1, Output: out}, nil }
	switch {
	case strings.HasPrefix(s, "git switch "):
		// bootstrap: git switch "b" 2>/dev/null || git switch -c "b" origin/src
		b := unq(f[2])
		if _, exists := g.branches[b]; !exists {
			if i := strings.Index(s, "origin/"); i >= 0 {
				g.branches[b] = g.resolve(strings.Fields(s[i:])[0])
			} else {
				return fail("fatal: invalid reference: " + b)
			}
		}
		g.head = b
		return ok("")
	case strings.HasPrefix(s, "git rev-list --count "):
		a, b, _ := strings.Cut(unq(f[3]), "..")
		return ok(fmt.Sprintf("%d\n", len(g.chain(g.resolve(b), g.resolve(a)))))
	case strings.HasPrefix(s, "git push -u origin HEAD"):
		g.remote[g.head] = g.branches[g.head]
		g.pushes = append(g.pushes, g.head+"->"+g.branches[g.head])
		return ok("")
	case strings.HasPrefix(s, "git diff --name-status"):
		return ok("M\tfile.go\n")
	case strings.HasPrefix(s, "git add -A"):
		if g.noChanges {
			return ok("")
		}
		g.branches[g.head] = g.commit(g.branches[g.head])
		return ok("")
	}
	return ok("")
}

// gitPerDir routes the fake provisioner's exec by checkout dir.
func gitPerDir(gits map[string]*fakeGit) func(*fake.Env, envport.Command) (envport.ExecResult, error) {
	return func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if g, ok := gits[cmd.Dir]; ok {
			return g.exec(env, cmd)
		}
		return envport.ExecResult{ExitCode: 0}, nil
	}
}

const multiRepoFlowDoc = `name: multi
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build it
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder, on_success: commit }
  - { id: commit, type: command, each_repo: true, run: "git add -A && if ! git diff --cached --quiet; then git commit -F artifacts/commit.md; fi", on_success: push }
  - { id: push, type: command, each_repo: true, run: "git push -u origin HEAD", on_success: redact }
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } }, on_success: mr }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } }, on_success: final_gate }
  - { id: final_gate, type: human_gate, input: { mr: { from: run.mr.url, type: link } }, on_success: done, on_reject: abort }
outcomes:
  - { id: shipped, on: mr_merged, then: done }
`

// A project with extra repositories: every checkout is cloned and branched,
// each_repo commands run in each, and a merge request opens for each repo the
// run changed — on that repo's repository host, onto that repo's default
// branch.
func TestMultiRepoRunPublishesOneMergeRequestPerChangedRepo(t *testing.T) {
	h := newHarness(t, mustFlow(t, multiRepoFlowDoc))
	apiRepoHost := &fake.RepoHost{}
	h.res.ExtraRepos = []resolution.ExtraRepo{
		{Name: "api", URL: "https://gitlab.local/g/api.git", Locator: "2", DefaultBranch: "develop"},
		{Name: "docs", URL: "https://gitlab.local/g/docs.git", Locator: "3", DefaultBranch: "main"},
		{Name: "vendor", URL: "https://gitlab.local/g/vendor.git", DefaultBranch: "main"}, // context only
	}
	h.res.RepoHosts = map[string]repoport.RepoHost{"api": apiRepoHost, "docs": &fake.RepoHost{}}
	gits := map[string]*fakeGit{"repo": newFakeGit("main"), "repos/api": newFakeGit("develop"),
		"repos/docs": newFakeGit("main"), "repos/vendor": newFakeGit("main")}
	gits["repos/docs"].noChanges = true // the run touched nothing there
	h.envp.ExecFunc = gitPerDir(gits)
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if call.Spec.Agent == "redactor" {
			h.writeFile(t, call.Env, "artifacts/mr.md", "# Across repos\n")
		}
		if call.Spec.Agent == "builder" && !strings.Contains(call.In.Prompt, "# REPOSITORIES") {
			t.Errorf("builder prompt lacks the repositories section:\n%s", call.In.Prompt)
		}
		return agentport.AgentResult{TurnsUsed: 1, Session: "s"}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "multi", testProject)
	if err != nil {
		t.Fatal(err)
	}
	gate := h.waitPing(t, PingGate)

	// Bootstrap cloned and branched every extra beside the primary.
	var cloned, committed []string
	for _, c := range h.envp.ExecCalls {
		if strings.Contains(c.Cmd.Script, "git clone") && strings.Contains(c.Cmd.Script, "repos/") {
			cloned = append(cloned, c.Cmd.Script[strings.Index(c.Cmd.Script, "repos/"):])
		}
		if strings.HasPrefix(c.Cmd.Script, "git add -A") {
			committed = append(committed, c.Cmd.Dir)
		}
	}
	if len(cloned) != 3 {
		t.Errorf("extra clones = %v", cloned)
	}
	if strings.Join(committed, " ") != "repo repos/api repos/docs repos/vendor" {
		t.Errorf("each_repo commit ran in %v", committed)
	}
	v := h.view(t, runID)
	if len(v.Checkouts) != 4 || v.Checkouts[1].Dir != "repos/api" || v.Checkouts[1].Source != "develop" {
		t.Errorf("checkouts = %+v", v.Checkouts)
	}
	// One MR for the primary (onto main), one for api (onto develop, on api's
	// own repository host); none for docs (unchanged) or vendor (no locator).
	primary, api := h.repoHost.SnapshotMRs(), apiRepoHost.SnapshotMRs()
	if len(primary) != 1 || primary[0].Target != "main" || len(api) != 1 || api[0].Target != "develop" {
		t.Fatalf("MRs: primary %+v api %+v; failures: %s", primary, api, failures(h.events(t, runID)))
	}
	if len(v.MRs) != 2 || v.MRs[1].Repo != "api" {
		t.Errorf("view MRs = %+v", v.MRs)
	}
	if !strings.Contains(gate.Context, "api: ") {
		t.Errorf("gate context = %q", gate.Context)
	}
	// Done only when BOTH merged.
	h.repoHost.SetMRState(primary[0].Ref.IID, repoport.MRMerged)
	time.Sleep(30 * time.Millisecond)
	if st := h.view(t, runID).State; st != run.WaitingOnHuman {
		t.Fatalf("state after one merge = %s", st)
	}
	apiRepoHost.SetMRState(api[0].Ref.IID, repoport.MRMerged)
	h.e.Wait(runID)
	if st := h.view(t, runID).State; st != run.Completed {
		t.Errorf("final state = %s", st)
	}
}

// failures renders every stage_failed for a diagnostic.
func failures(evs []event.Event) string {
	var out []string
	for _, e := range evs {
		if f, ok := e.Payload.(*event.StageFailed); ok {
			out = append(out, f.StageID+": "+f.Reason+": "+f.Error)
		}
		if f, ok := e.Payload.(*event.RunAbandoned); ok {
			out = append(out, "abandoned: "+f.Reason)
		}
	}
	return strings.Join(out, " | ")
}

// The run page's live view: the repository host's mergeability and pending
// review threads per merge request, nothing recorded.
func TestMRStatusesReportMergeabilityAndPendingThreads(t *testing.T) {
	h := newHarness(t, mustFlow(t, multiRepoFlowDoc))
	h.envp.ExecFunc = gitPerDir(map[string]*fakeGit{"repo": newFakeGit("main")})
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		if call.Spec.Agent == "redactor" {
			h.writeFile(t, call.Env, "artifacts/mr.md", "# T\n")
		}
		return agentport.AgentResult{TurnsUsed: 1, Session: "s"}, nil
	}
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "multi", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.waitPing(t, PingGate)
	waitEvent[*event.PhaseParked](t, h, runID)
	before := len(h.events(t, runID))
	t0 := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	h.repoHost.SetMergeable(1, repoport.MergeConflicts, "abc")
	h.repoHost.AddComment(1, "d1", "a.go", "rev", "rename", t0)
	h.repoHost.AddComment(1, "d2", "b.go", "rev", "add a test", t0.Add(time.Minute))
	// d3 was answered by the factory: not pending.
	h.repoHost.AddComment(1, "d3", "c.go", "rev", "typo", t0)
	h.repoHost.AddComment(1, "d3", "c.go", "factory", "fixed "+repoport.ReplyMarker(runID, "d3"), t0.Add(2*time.Minute))
	got, err := mrs.New(resolution.StaticResolver{R: h.res}, &run.Reader{Store: h.store}).MRStatuses(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Mergeable != repoport.MergeConflicts || got[0].HeadSHA != "abc" || got[0].PendingThreads != 2 ||
		got[0].LastCommentAt == nil || !got[0].LastCommentAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("status = %+v", got)
	}
	if got[0].StackedOn != nil {
		t.Errorf("stacked on %+v with no merge request on its target", got[0].StackedOn)
	}
	// An open merge request whose branch the run's targets: the run's is
	// stacked on it.
	base := h.repoHost.AddOpenMR("bob", "The base", got[0].Target, "release", "z1")
	if got, err = mrs.New(resolution.StaticResolver{R: h.res}, &run.Reader{Store: h.store}).MRStatuses(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if s := got[0].StackedOn; s == nil || s.IID != base.IID || s.Branch != got[0].Target || s.Title != "The base" {
		t.Errorf("stacked on = %+v, want !%d", s, base.IID)
	}
	if after := len(h.events(t, runID)); after != before {
		t.Errorf("reading the status appended %d events", after-before)
	}
}
