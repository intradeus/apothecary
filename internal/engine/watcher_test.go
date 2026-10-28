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
	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// mergeGit is enough of git for the watchers: a commit graph with
// merge commits, a remote, paused merges and rebases with conflict markers,
// and leased pushes — parsed from the scripts the engine sends.
type mergeGit struct {
	mu      sync.Mutex
	parents map[string][]string
	branch  string // the run branch, checked out
	head    string
	remote  map[string]string
	n       int
	// conflicts > 0 makes the next merge/rebase (or continued rebase step)
	// conflict in conflict.go; markers are the files still carrying markers.
	conflicts int
	paused    string // "merge <sha>" | "rebase <sha>" | ""
	markers   []string
	merges    int
	rebases   int
	pushes    []string
	execs     []string
}

func newMergeGit(target string) *mergeGit {
	g := &mergeGit{parents: map[string][]string{"c0": nil}, remote: map[string]string{target: "c0"}}
	return g
}

func (g *mergeGit) commit(parents ...string) string {
	g.n++
	id := fmt.Sprintf("m%d", g.n)
	g.parents[id] = parents
	return id
}

func (g *mergeGit) ancestor(a, b string) bool {
	seen := map[string]bool{}
	var walk func(c string) bool
	walk = func(c string) bool {
		if c == a {
			return true
		}
		if c == "" || seen[c] {
			return false
		}
		seen[c] = true
		for _, p := range g.parents[c] {
			if walk(p) {
				return true
			}
		}
		return false
	}
	return walk(b)
}

func (g *mergeGit) resolve(ref string) string {
	ref = unq(ref)
	if strings.HasPrefix(ref, "origin/") {
		return g.remote[strings.TrimPrefix(ref, "origin/")]
	}
	if ref == "HEAD" {
		return g.head
	}
	return ref
}

// advanceTarget moves the remote target branch on by one commit.
func (g *mergeGit) advanceTarget(target string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.remote[target] = g.commit(g.remote[target])
	return g.remote[target]
}

func (g *mergeGit) exec(_ *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := strings.TrimSpace(cmd.Script)
	g.execs = append(g.execs, s)
	f := strings.Fields(s)
	ok := func(out string) (ports.ExecResult, error) { return ports.ExecResult{Output: out}, nil }
	fail := func(out string) (ports.ExecResult, error) { return ports.ExecResult{ExitCode: 1, Output: out}, nil }
	conflict := func(op, onto string) (ports.ExecResult, error) {
		g.conflicts--
		g.paused, g.markers = op+" "+onto, []string{"conflict.go"}
		return fail("CONFLICT (content): Merge conflict in conflict.go")
	}
	switch {
	case strings.HasPrefix(s, "git switch "):
		// bootstrap: the run branch, from origin/<source>
		g.branch = unq(f[2])
		if g.head == "" {
			g.head = g.resolve(strings.Fields(s[strings.Index(s, "origin/"):])[0])
		}
		return ok("")
	case strings.HasPrefix(s, "git fetch origin"):
		return ok("")
	case strings.HasPrefix(s, "d=$(git rev-parse --git-dir)"):
		if g.paused == "" {
			return ok("none\n")
		}
		return ok(g.paused + "\n")
	case strings.HasPrefix(s, "files=$(git diff"):
		return ok(strings.Join(g.markers, "\n"))
	case strings.HasPrefix(s, "git diff --name-only --diff-filter=U"):
		if g.paused != "" {
			return ok("conflict.go\n")
		}
		return ok("")
	case strings.HasPrefix(s, "git merge-base --is-ancestor "):
		if g.ancestor(g.resolve(f[3]), g.resolve(f[4])) {
			return ok("")
		}
		return fail("")
	case strings.HasPrefix(s, "git merge --no-edit "):
		onto := g.resolve(f[3])
		if g.conflicts > 0 {
			return conflict("merge", onto)
		}
		g.head = g.commit(g.head, onto)
		g.merges++
		return ok("")
	case strings.HasPrefix(s, "git rebase ") && !strings.Contains(s, "--"):
		onto := g.resolve(f[2])
		if g.conflicts > 0 {
			return conflict("rebase", onto)
		}
		g.head = g.commit(onto)
		g.rebases++
		return ok("")
	case strings.HasPrefix(s, "git merge --abort"), strings.HasPrefix(s, "git rebase --abort"):
		g.paused, g.markers = "", nil
		return ok("")
	case strings.HasPrefix(s, "git add -A && git commit --no-edit"):
		if !strings.HasPrefix(g.paused, "merge ") || len(g.markers) > 0 {
			return fail("error: Committing is not possible because you have unmerged files.")
		}
		onto := strings.TrimPrefix(g.paused, "merge ")
		g.head, g.paused = g.commit(g.head, onto), ""
		g.merges++
		return ok("")
	case strings.HasPrefix(s, "git add -A && { GIT_EDITOR=true git rebase --continue"):
		if !strings.HasPrefix(g.paused, "rebase ") || len(g.markers) > 0 {
			return fail("error: unresolved conflicts")
		}
		onto := strings.TrimPrefix(g.paused, "rebase ")
		if g.conflicts > 0 {
			return conflict("rebase", onto)
		}
		g.head, g.paused = g.commit(onto), ""
		g.rebases++
		return ok("")
	case strings.HasPrefix(s, "git rev-parse "):
		return ok(g.resolve(f[2]) + "\n")
	case strings.HasPrefix(s, "git push -u --force-with-lease="):
		lease := strings.TrimPrefix(f[3], "--force-with-lease=")
		ref, expected, _ := strings.Cut(lease, ":")
		b := strings.TrimPrefix(unq(ref), "refs/heads/")
		if g.remote[b] != unq(expected) {
			return fail(fmt.Sprintf("! [rejected] %s (stale info)", b))
		}
		g.remote[b] = g.head
		g.pushes = append(g.pushes, "lease:"+b)
		return ok("")
	case strings.HasPrefix(s, "git push -u origin"):
		b := g.branch
		if have, ok2 := g.remote[b]; ok2 && !g.ancestor(have, g.head) {
			return fail(fmt.Sprintf("! [rejected] %s (non-fast-forward)", b))
		}
		g.remote[b] = g.head
		g.pushes = append(g.pushes, "plain:"+b)
		return ok("")
	case strings.HasPrefix(s, "git add -A"):
		g.head = g.commit(g.head) // the build / fix commit
		return ok("")
	case strings.HasPrefix(s, "git rev-list --count "):
		a, b, _ := strings.Cut(unq(f[3]), "..")
		if g.ancestor(g.resolve(a), g.resolve(b)) && g.resolve(a) != g.resolve(b) {
			return ok("1\n")
		}
		return ok("0\n")
	}
	return ok("")
}

func (g *mergeGit) snapshot() (merges, rebases int, pushes []string, paused string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.merges, g.rebases, append([]string(nil), g.pushes...), g.paused
}

// watcherFlowDoc is the builder's shape, small: the main chain, then the
// watchers — a confirmed conflict fix (engine merge, resolver for
// conflicts) and a review round (load, fix, reply, post).
const watcherFlowDoc = `name: phased
version: 5
parameters: { harness: fake, model: fake/model }
agents:
  - name: builder
    prompt: build it
    inputs:
      - { name: comments, type: file, optional: true }
      - { name: failure, type: failure, optional: true }
  - name: redactor
    prompt: describe it
    outputs:
      - { name: mr, type: file }
  - name: resolver
    prompt: resolve it
    inputs:
      - { name: conflict, type: failure, optional: true }
  - name: replier
    prompt: reply
    inputs:
      - { name: comments, type: file }
      - { name: failure, type: failure, optional: true }
    outputs:
      - { name: replies, type: review_replies }
main:
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder }
  - { id: commit, type: command, run: "git add -A && git commit -m build" }
  - { id: push, type: action, action: push_branch }
  - { id: redact, type: agent, harness: $harness, model: $model, agent: redactor, output: { mr: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } } }
watchers:
  - id: conflicts
    on: { signal: mr_conflicts, start: confirm }
    stages:
      - { id: sync, type: action, action: sync_target, with: { strategy: STRATEGY }, on_success: sync_test,
          on_fail: { to: resolve, max: 2, then: debug } }
      - { id: resolve, type: agent, harness: $harness, model: $model, agent: resolver, input: { conflict: { from: edge.failure, type: failure, optional: true } }, on_success: sync }
      - { id: sync_test, type: command, run: "./verify.sh" }
      - { id: sync_push, type: action, action: push_branch, on_success: end, on_fail: debug }
  - id: review_fixes
    on: { signal: mr_review_settled, quiet_for: 1m }
    stages:
      - { id: load, type: action, action: load_review_comments, output: { review: { to: artifacts/review.md, type: file } } }
      - { id: fix, type: agent, harness: $harness, model: $model, agent: builder, input: { comments: { from: artifacts/review.md, type: file } } }
      - { id: fix_commit, type: command, run: "git add -A && git commit -m fix" }
      - { id: fix_push, type: action, action: push_branch }
      - { id: reply, type: agent, harness: $harness, model: $model, agent: replier, input: { comments: { from: artifacts/review.md, type: file }, failure: { from: edge.failure, type: failure, optional: true } },
          output: { replies: { to: artifacts/replies.yaml, type: review_replies } }, on_fail: { to: reply, max: 1 } }
      - { id: post, type: action, action: reply_review_comments, input: { replies: { from: artifacts/replies.yaml, type: review_replies } } }
outcomes:
  - id: shipped
    on: mr_merged
    then: done
`

type watcherHarness struct {
	*phasedHarness
	git *mergeGit
	// resolverClears: the resolver agent removes the conflict markers.
	resolverClears bool
	// replies is what the replier agent writes (YAML).
	replies func() string
	mu2     sync.Mutex
	review  []string // the review.md the fixer was given
}

func newWatcherHarness(t *testing.T, strategy string) *watcherHarness {
	t.Helper()
	ph := newPhasedHarness(t, strings.Replace(watcherFlowDoc, "STRATEGY", strategy, 1))
	rh := &watcherHarness{phasedHarness: ph, git: newMergeGit("main"), resolverClears: true}
	ph.envp.ExecFunc = func(env *fake.Env, cmd ports.Command) (ports.ExecResult, error) {
		if cmd.Dir == "repo" {
			return rh.git.exec(env, cmd)
		}
		return ports.ExecResult{}, nil
	}
	inner := ph.runner.RunFunc
	ph.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		switch call.Spec.Agent {
		case "resolver":
			if rh.resolverClears {
				rh.git.mu.Lock()
				rh.git.markers = nil
				rh.git.mu.Unlock()
			}
		case "builder":
			if data, err := ph.envp.ReadFile(ctx, call.Env, "artifacts/review.md"); err == nil && strings.Contains(call.In.Prompt, "# SUBJECT") {
				rh.mu2.Lock()
				rh.review = append(rh.review, string(data))
				rh.mu2.Unlock()
			}
		case "replier":
			if rh.replies != nil {
				ph.writeFile(t, call.Env, "artifacts/replies.yaml", rh.replies())
			}
		}
		return inner(ctx, call)
	}
	return rh
}

// conflict makes the repository host report a conflict against a moved target.
func (rh *watcherHarness) conflict(t *testing.T) {
	t.Helper()
	tip := rh.git.advanceTarget("main")
	rh.repoHost.SetTargetSHA(1, tip)
	rh.repoHost.SetMergeable(1, ports.MergeConflicts, "head")
}

// pick answers the round's thread selection with the given thread ids
// (none = select nothing), returning the options offered.
func (rh *watcherHarness) pick(t *testing.T, ids ...string) []string {
	t.Helper()
	p := rh.waitPing(t, event.PingSelect)
	if p.AnswerKind != event.AnswerForm || len(p.Form.Fields) != 1 || p.Form.Fields[0].ID != event.SelectField {
		t.Fatalf("selection ping = kind %q form %+v", p.AnswerKind, p.Form)
	}
	opts := p.Form.Fields[0].Options
	chosen := []any{}
	for _, o := range opts {
		for _, id := range ids {
			if o.ID == id {
				chosen = append(chosen, o.ID)
			}
		}
	}
	if err := rh.e.AnswerPing(p.ID, PingAnswer{Answer: "accept", Content: map[string]any{event.SelectField: chosen}}); err != nil {
		t.Fatal(err)
	}
	// What was offered, as "<id> — <label>: <detail>" for assertions.
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.ID + " — " + o.Label + ": " + o.Detail
	}
	return out
}

func (rh *watcherHarness) approveConflictFix(t *testing.T, except ...string) string {
	t.Helper()
	ask := rh.waitPing(t, event.PingPhaseConfirm, except...)
	if err := rh.e.AnswerPing(ask.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	return ask.ID
}

// The confirmed conflict fix merges the target in, the resolver settles the
// conflict by editing (no git), the engine finishes the merge, tests and
// pushes — the first push was plain, this one leased against what the run
// last pushed.
func TestConflictPhaseMergesAndTheResolverSettlesIt(t *testing.T) {
	rh := newWatcherHarness(t, "merge")
	runID := rh.start(t)
	rh.git.mu.Lock()
	rh.git.conflicts = 1
	rh.git.mu.Unlock()
	rh.conflict(t)
	rh.approveConflictFix(t)
	rh.waitPhaseRuns(t, runID, "conflicts", 1)
	merges, _, pushes, paused := rh.git.snapshot()
	if merges != 1 || paused != "" {
		t.Fatalf("merges %d paused %q", merges, paused)
	}
	if len(pushes) != 2 || pushes[0] != "plain:"+rh.view(t, runID).Branch || !strings.HasPrefix(pushes[1], "lease:") {
		t.Errorf("pushes = %v (plain first, then leased)", pushes)
	}
	evs := rh.events(t, runID)
	synced, _ := findPayload[*event.TargetSynced](evs)
	if synced == nil || synced.Strategy != "merge" || synced.Subject != "repo" {
		t.Errorf("target_synced = %+v", synced)
	}
	var failed *event.StageFailed
	for _, e := range evs {
		if f, ok := e.Payload.(*event.StageFailed); ok && f.StageID == "sync" {
			failed = f
		}
	}
	if failed == nil || !strings.Contains(failed.Error, "conflict.go") || !strings.Contains(failed.Error, "paused") {
		t.Errorf("sync failure = %+v", failed)
	}
}

// A branch that already contains its target is a no-op: no merge, nothing
// recorded, the phase ends after its tests and an idempotent push.
func TestSyncWithNothingToTakeIn(t *testing.T) {
	rh := newWatcherHarness(t, "merge")
	runID := rh.start(t)
	rh.repoHost.SetTargetSHA(1, "c0") // the repository host says conflict, but main has not moved
	rh.repoHost.SetMergeable(1, ports.MergeConflicts, "head")
	rh.approveConflictFix(t)
	rh.waitPhaseRuns(t, runID, "conflicts", 1)
	merges, _, pushes, _ := rh.git.snapshot()
	if merges != 0 || len(pushes) != 1 {
		t.Errorf("merges %d pushes %v — nothing to take in, nothing to push", merges, pushes)
	}
	if s, _ := findPayload[*event.TargetSynced](rh.events(t, runID)); s != nil {
		t.Errorf("recorded a sync that did nothing: %+v", s)
	}
}

// A resolver that leaves markers is sent round again, then a human gets the
// paused merge; clearing the markers and approving continues it.
func TestUnresolvedConflictReachesAHuman(t *testing.T) {
	rh := newWatcherHarness(t, "merge")
	rh.resolverClears = false
	runID := rh.start(t)
	rh.git.mu.Lock()
	rh.git.conflicts = 1
	rh.git.mu.Unlock()
	rh.conflict(t)
	rh.approveConflictFix(t)
	gate := rh.waitPing(t, PingDebug)
	if gate.StageID != "sync" {
		t.Fatalf("parked at %s", gate.StageID)
	}
	var fails []string
	for _, e := range rh.events(t, runID) {
		if f, ok := e.Payload.(*event.StageFailed); ok && f.StageID == "sync" {
			fails = append(fails, f.Error)
		}
	}
	if len(fails) != 3 || !strings.Contains(fails[1], "markers remain in: conflict.go") {
		t.Fatalf("sync failures = %q", fails)
	}
	rh.git.mu.Lock()
	rh.git.markers = nil
	rh.git.mu.Unlock()
	if err := rh.e.AnswerPing(gate.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	rh.waitPhaseRuns(t, runID, "conflicts", 1)
	if merges, _, _, paused := rh.git.snapshot(); merges != 1 || paused != "" {
		t.Errorf("merges %d paused %q", merges, paused)
	}
}

// Rebase rewrites the branch: the push is leased against the head when the
// phase started, so it lands — unless someone pushed meanwhile, which is
// refused rather than overwritten.
func TestRebaseIsPushedLeased(t *testing.T) {
	rh := newWatcherHarness(t, "rebase")
	runID := rh.start(t)
	branch := rh.view(t, runID).Branch
	rh.git.mu.Lock()
	pushedTip := rh.git.remote[branch]
	rh.git.mu.Unlock()
	tip := rh.git.advanceTarget("main")
	rh.repoHost.SetTargetSHA(1, tip)
	rh.repoHost.SetMergeable(1, ports.MergeConflicts, pushedTip)
	rh.approveConflictFix(t)
	rh.waitPhaseRuns(t, runID, "conflicts", 1)
	_, rebases, pushes, _ := rh.git.snapshot()
	if rebases != 1 || len(pushes) != 2 || !strings.HasPrefix(pushes[1], "lease:") {
		t.Fatalf("rebases %d pushes %v", rebases, pushes)
	}

	// Someone pushes to the branch, then main moves again: the rebase's push
	// is refused, and the run parks on a human instead of overwriting.
	rh.git.mu.Lock()
	rh.git.remote[branch] = rh.git.commit(rh.git.remote[branch])
	stranger := rh.git.remote[branch]
	rh.git.mu.Unlock()
	tip2 := rh.git.advanceTarget("main")
	rh.repoHost.SetTargetSHA(1, tip2)
	// The repository host's head is still the one the run pushed: the
	// stranger's commit is exactly what the lease must protect.
	rh.repoHost.SetMergeable(1, ports.MergeConflicts, pushedTip)
	first := rh.waitPing(t, event.PingPhaseConfirm)
	if err := rh.e.AnswerPing(first.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	gate := rh.waitPing(t, PingDebug)
	if gate.StageID != "sync_push" {
		t.Fatalf("parked at %s", gate.StageID)
	}
	rh.git.mu.Lock()
	defer rh.git.mu.Unlock()
	if rh.git.remote[branch] != stranger {
		t.Errorf("the stranger's commit was overwritten: remote %s, want %s", rh.git.remote[branch], stranger)
	}
}

// A review round: the pending threads reach the fixer, the replier's answers
// are posted with markers, the threads stop being pending — no second round
// for the same comments.
func TestReviewRoundFixesAndReplies(t *testing.T) {
	rh := newWatcherHarness(t, "merge")
	rh.replies = func() string {
		return "- { thread: d1, reply: \"Renamed in the latest commit.\" }\n- { thread: d2, reply: \"Added a test.\", resolve: true }\n"
	}
	runID := rh.start(t)
	t0 := rh.clock.Now()
	rh.repoHost.AddComment(1, "d1", "a.go", "rev", "rename this", t0)
	rh.repoHost.AddComment(1, "d2", "b.go", "rev", "needs a test", t0)
	rh.clock.Advance(2 * time.Minute)
	rh.pick(t, "d1", "d2")
	rh.waitPhaseRuns(t, runID, "review_fixes", 1)
	rh.mu2.Lock()
	review := append([]string(nil), rh.review...)
	rh.mu2.Unlock()
	if len(review) != 1 || !strings.Contains(review[0], "Thread `d1`") || !strings.Contains(review[0], "needs a test") {
		t.Fatalf("the fixer was given: %q", review)
	}
	threads := rh.repoHost.SnapshotThreads(1)
	for _, th := range threads {
		last := th.Comments[len(th.Comments)-1]
		if !ports.IsFactoryReply(last.Body) {
			t.Errorf("thread %s has no reply: %+v", th.ID, th.Comments)
		}
		if th.ID == "d2" && !th.Resolved {
			t.Errorf("d2 should be resolved")
		}
	}
	posted, _ := findPayload[*event.ReviewRepliesPosted](rh.events(t, runID))
	if posted == nil || len(posted.ThreadIDs) != 2 || len(posted.Resolved) != 1 {
		t.Errorf("review_replies_posted = %+v", posted)
	}
	rh.clock.Advance(10 * time.Minute)
	time.Sleep(60 * time.Millisecond)
	if n := len(phaseStarts(rh.events(t, runID))); n != 2 {
		t.Errorf("answered comments started another round (%d phase starts)", n)
	}
}

// A reply to a thread the round did not load fails the replier's stage —
// before anything is posted — and its retry gets the reason.
func TestRepliesNamingAnUnknownThreadFailTheStage(t *testing.T) {
	rh := newWatcherHarness(t, "merge")
	var calls int
	rh.replies = func() string {
		calls++
		if calls == 1 {
			return "- { thread: ghost, reply: hi }\n"
		}
		return "- { thread: d1, reply: done }\n"
	}
	runID := rh.start(t)
	rh.repoHost.AddComment(1, "d1", "a.go", "rev", "rename", rh.clock.Now())
	rh.clock.Advance(2 * time.Minute)
	rh.pick(t, "d1")
	rh.waitPhaseRuns(t, runID, "review_fixes", 1)
	var invalid *event.StageFailed
	for _, e := range rh.events(t, runID) {
		if f, ok := e.Payload.(*event.StageFailed); ok && f.StageID == "reply" {
			invalid = f
		}
	}
	if invalid == nil || invalid.Reason != event.ReasonOutputInvalid || !strings.Contains(invalid.Error, `"ghost"`) {
		t.Fatalf("reply failure = %+v", invalid)
	}
	for _, th := range rh.repoHost.SnapshotThreads(1) {
		if len(th.Comments) != 2 {
			t.Errorf("thread %s: %d comments, want the review + one reply", th.ID, len(th.Comments))
		}
	}
}

// A crash between two replies: the run fails on the repository host error;
// trying again posts only the reply that is missing.
func TestReplyPostingReconcilesAfterAFailure(t *testing.T) {
	rh := newWatcherHarness(t, "merge")
	rh.replies = func() string { return "- { thread: d1, reply: one }\n- { thread: d2, reply: two }\n" }
	rh.repoHost.ReplyFailAfter = 1
	runID := rh.start(t)
	rh.repoHost.AddComment(1, "d1", "a.go", "rev", "a", rh.clock.Now())
	rh.repoHost.AddComment(1, "d2", "b.go", "rev", "b", rh.clock.Now())
	rh.clock.Advance(2 * time.Minute)
	rh.pick(t, "d1", "d2")
	rh.waitState(t, runID, run.Failed)
	rh.repoHost.ReplyFailAfter = 0
	if err := rh.e.RetryRun(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	rh.waitPhaseRuns(t, runID, "review_fixes", 1)
	for _, th := range rh.repoHost.SnapshotThreads(1) {
		var replies int
		for _, c := range th.Comments {
			if ports.IsFactoryReply(c.Body) {
				replies++
			}
		}
		if replies != 1 {
			t.Errorf("thread %s got %d replies, want exactly 1", th.ID, replies)
		}
	}
}

// A human picks which threads a round fixes: only those reach the fixer and
// get replies; the skipped one is left alone — not offered again — until a
// reviewer writes on it again.
func TestReviewRoundFixesOnlyTheSelectedThreads(t *testing.T) {
	rh := newWatcherHarness(t, "merge")
	rh.replies = func() string { return "- { thread: d1, reply: done }\n" }
	runID := rh.start(t)
	t0 := rh.clock.Now()
	rh.repoHost.AddComment(1, "d1", "a.go", "rev", "rename this", t0)
	rh.repoHost.AddComment(1, "d2", "b.go", "rev", "this is wrong, actually", t0)
	rh.clock.Advance(2 * time.Minute)
	offered := rh.pick(t, "d1")
	if len(offered) != 2 || !strings.Contains(offered[1], "b.go:1 — rev: this is wrong") {
		t.Fatalf("offered = %q", offered)
	}
	rh.waitPhaseRuns(t, runID, "review_fixes", 1)
	rh.mu2.Lock()
	review := append([]string(nil), rh.review...)
	rh.mu2.Unlock()
	if len(review) != 1 || !strings.Contains(review[0], "`d1`") || strings.Contains(review[0], "`d2`") {
		t.Fatalf("the fixer was given: %q", review)
	}
	loaded, _ := findPayload[*event.ReviewCommentsLoaded](rh.events(t, runID))
	if loaded == nil || len(loaded.ThreadIDs) != 1 || loaded.Skipped["d2"] == "" {
		t.Errorf("review_comments_loaded = %+v", loaded)
	}
	// A new comment elsewhere: the next round offers it, not the skipped d2.
	rh.repoHost.AddComment(1, "d3", "c.go", "rev", "one more", rh.clock.Now())
	rh.clock.Advance(2 * time.Minute)
	rh.replies = func() string { return "- { thread: d3, reply: done }\n" }
	offered = rh.pick(t, "d3")
	if len(offered) != 1 || !strings.HasPrefix(offered[0], "d3 — ") {
		t.Errorf("second round offered %q, want only d3", offered)
	}
	rh.waitPhaseRuns(t, runID, "review_fixes", 2)
	// The reviewer insists on d2: it is offered again.
	rh.repoHost.AddComment(1, "d2", "b.go", "rev", "no really, please change it", rh.clock.Now())
	rh.clock.Advance(2 * time.Minute)
	offered = rh.pick(t)
	if len(offered) != 1 || !strings.HasPrefix(offered[0], "d2 — ") {
		t.Errorf("third round offered %q, want d2 again", offered)
	}
}

// Selecting nothing ends the round at once: no agent turn is spent.
func TestReviewRoundWithNothingSelectedEndsAtOnce(t *testing.T) {
	rh := newWatcherHarness(t, "merge")
	runID := rh.start(t)
	rh.repoHost.AddComment(1, "d1", "a.go", "rev", "nit", rh.clock.Now())
	rh.clock.Advance(2 * time.Minute)
	before := len(rh.promptsOf("fix"))
	rh.pick(t)
	rh.waitPhaseRuns(t, runID, "review_fixes", 1)
	if n := len(rh.promptsOf("fix")); n != before {
		t.Errorf("the fixer ran %d time(s) for an empty round", n-before)
	}
	for _, e := range rh.events(t, runID) {
		if s, ok := e.Payload.(*event.StageStarted); ok && (s.StageID == "fix" || s.StageID == "reply") {
			t.Errorf("stage %s ran in an empty round", s.StageID)
		}
	}
	// Nothing new: not offered again.
	rh.clock.Advance(10 * time.Minute)
	time.Sleep(60 * time.Millisecond)
	for _, p := range rh.e.PendingPings() {
		if p.Type == event.PingSelect {
			t.Fatalf("asked again about skipped comments: %+v", p)
		}
	}
}

// A selection answer must name offered threads.
func TestSelectionAnswerIsValidated(t *testing.T) {
	rh := newWatcherHarness(t, "merge")
	rh.start(t)
	rh.repoHost.AddComment(1, "d1", "a.go", "rev", "nit", rh.clock.Now())
	rh.clock.Advance(2 * time.Minute)
	p := rh.waitPing(t, event.PingSelect)
	for _, bad := range []PingAnswer{{}, {Answer: "approve"}, {Answer: "accept", Content: map[string]any{event.SelectField: []any{"d9"}}},
		{Answer: "accept", Content: map[string]any{event.SelectField: "d1"}}} {
		if err := rh.e.AnswerPing(p.ID, bad); err == nil {
			t.Errorf("answer %+v accepted", bad)
		}
	}
}
