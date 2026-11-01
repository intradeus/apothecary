package engine

// A stage whose work lands outside the run: the repository host, the tracker,
// git in the workspace. Each can fail at each step, and the difference
// between "the flow can route this" and "the run cannot go on" is the
// difference between a retry and a person being paged. These tables pin it.

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/ports/trackerport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
	"apothecary/internal/run/runinput"
)

// testTime is when a scripted reviewer wrote: past every window a test uses.
var testTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// faultHost fails the reads and writes the fake has no switch for.
type faultHost struct {
	*fake.RepoHost
	merge, resolve, status, threads, find error
	threadsAfter                          int // MRThreads calls let through
}

func (f *faultHost) MergeMR(ctx context.Context, mr repoport.MRRef, o repoport.MergeOptions) error {
	if f.merge != nil {
		return f.merge
	}
	return f.RepoHost.MergeMR(ctx, mr, o)
}

func (f *faultHost) ResolveThread(ctx context.Context, mr repoport.MRRef, id string) error {
	if f.resolve != nil {
		return f.resolve
	}
	return f.RepoHost.ResolveThread(ctx, mr, id)
}

func (f *faultHost) MRStatus(ctx context.Context, mr repoport.MRRef) (repoport.MRStatus, error) {
	if f.status != nil {
		return repoport.MRStatus{}, f.status
	}
	return f.RepoHost.MRStatus(ctx, mr)
}

func (f *faultHost) MRThreads(ctx context.Context, mr repoport.MRRef) ([]repoport.ReviewThread, error) {
	if f.threads != nil {
		if f.threadsAfter == 0 {
			return nil, f.threads
		}
		f.threadsAfter--
	}
	return f.RepoHost.MRThreads(ctx, mr)
}

// runOut runs doc on an open merge request of the harness's host and
// returns the stage's failure.
func runOut(t *testing.T, h *harness, ticket runinput.Ticket, flowName string, in runinput.Input) (string, *event.StageFailed) {
	t.Helper()
	if ticket.Title == "" {
		ticket.Title = "T"
	}
	id, err := h.e.CreateRunWith(context.Background(), ticket, flowName, testProject, in)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	sf, _ := findPayload[*event.StageFailed](h.events(t, id))
	return id, sf
}

func wantFailure(t *testing.T, sf *event.StageFailed, reason, text string) {
	t.Helper()
	if sf == nil {
		t.Fatal("no stage_failed")
	}
	if sf.Reason != reason {
		t.Errorf("reason = %q, want %q (%s)", sf.Reason, reason, sf.Error)
	}
	if !strings.Contains(sf.Error, text) {
		t.Errorf("error = %q, want it to say %q", sf.Error, text)
	}
}

const reviewPostDoc = `name: rp
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
agents:
  - name: author
    prompt: write it
    inputs:
      - { name: threads, type: mr_threads }
    outputs:
      - { name: review, type: review_threads }
main:
  - { id: poll, type: action, action: load_review_comments, with: { scope: all }, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } } }
  - { id: read, type: agent, harness: $harness, model: $model, agent: author,
      input: { threads: { from: artifacts/threads.yaml, type: mr_threads } }, output: { review: { to: artifacts/review.yaml, type: review_threads } } }
  - { id: post, type: action, action: post_review, input: { review: { from: artifacts/review.yaml, type: review_threads } }, on_success: done }
`

// reviewPosted runs reviewPostDoc with the agent writing body.
func reviewPosted(t *testing.T, body string, arm func(h *harness, f *faultHost, ref repoport.MRRef)) (*harness, string, *event.StageFailed, *faultHost) {
	t.Helper()
	h := newHarness(t, mustFlow(t, reviewPostDoc))
	f := &faultHost{RepoHost: h.repoHost}
	h.res.RepoHost = f
	ref := h.repoHost.AddOpenMR("alice", "Add caching", "feature/cache", "main", "a1")
	h.repoHost.AddComment(ref.IID, "d1", "a.go", "rev", "rename this", testTime)
	if arm != nil {
		arm(h, f, ref)
	}
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		h.writeFile(t, call.Env, "artifacts/review.yaml", body)
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	id, sf := runOut(t, h, runinput.Ticket{}, "rp", runinput.Input{MR: &runinput.MR{IID: ref.IID}})
	return h, id, sf, f
}

func TestPostReviewFailures(t *testing.T) {
	boom := errors.New("host says no")
	const replyD1 = "replies:\n  - { thread: d1, body: done }\n"
	cases := []struct {
		name    string
		body    string
		arm     func(h *harness, f *faultHost, ref repoport.MRRef)
		reason  string
		want    string
		final   run.State
		replied int
	}{
		{"the note cannot be posted", noteA, func(h *harness, _ *faultHost, _ repoport.MRRef) { h.repoHost.ReplyErr = boom },
			event.ReasonInfra, "posting the review: host says no", run.Failed, 0},
		{"a reply cannot be posted", replyD1, func(h *harness, _ *faultHost, _ repoport.MRRef) { h.repoHost.ReplyErr = boom },
			event.ReasonInfra, "replying in thread d1: host says no", run.Failed, 0},
		{"a thread cannot be resolved", "replies:\n  - { thread: n1, body: done, resolve: true }\n", func(h *harness, f *faultHost, ref repoport.MRRef) {
			h.repoHost.AddComment(ref.IID, "n1", "b.go", "factory", "note\n\n"+repoport.NoteMarker("old/round", 0, "nit"), testTime)
			f.resolve = boom
		}, event.ReasonInfra, "resolving thread n1: host says no", run.Failed, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, id, sf, _ := reviewPosted(t, c.body, c.arm)
			wantFailure(t, sf, c.reason, c.want)
			if s := h.view(t, id).State; s != c.final {
				t.Errorf("state = %s", s)
			}
		})
	}
}

// What the review guards refuse, at the end of the stage that wrote it: the
// run does not get to post a note that repeats itself, or reply where it
// cannot.
func TestReviewGuards(t *testing.T) {
	ours := func(h *harness, _ *faultHost, ref repoport.MRRef) {
		h.repoHost.AddComment(ref.IID, "n1", "b.go", "factory", "name it\n\n"+repoport.NoteMarker("old/round", 0, "nit"), testTime)
	}
	cases := []struct {
		name, body string
		arm        func(h *harness, f *faultHost, ref repoport.MRRef)
		want       string
	}{
		{"a reply in no loaded thread", "replies:\n  - { thread: ghost, body: x }\n", nil, `replies[0]: thread "ghost" is not one of the threads loaded (d1)`},
		{"resolving someone else's thread", "replies:\n  - { thread: d1, body: x, resolve: true }\n", nil, `replies[0]: thread "d1" is someone else's`},
		{"a file that is not a review", "- not: a review\n", nil, "artifacts/review.yaml"},
		{"a note that repeats ours in other words of place", "notes:\n  - { path: b.go, line: 9, kind: nit, body: name it, rationale: style }\n", ours,
			"says what apothecary's thread n1 already says on b.go"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, id, sf, _ := reviewPosted(t, c.body, c.arm)
			wantFailure(t, sf, event.ReasonOutputInvalid, c.want)
			if len(h.repoHost.SnapshotMRs()[0].Reviews) != 0 {
				t.Error("a refused review reached the host")
			}
			if s := h.view(t, id).State; s != run.Abandoned {
				t.Errorf("state = %s", s)
			}
		})
	}
}

// An empty review posts nothing and still completes.
func TestAnEmptyReviewPostsNothing(t *testing.T) {
	h, id, sf, _ := reviewPosted(t, "notes: []\n", nil)
	if sf != nil {
		t.Fatalf("failed: %+v", sf)
	}
	if v := h.view(t, id); v.State != run.Completed {
		t.Errorf("state = %s", v.State)
	}
	if evs := h.events(t, id); countType(evs, event.TypeReviewPosted) != 0 || len(h.repoHost.SnapshotMRs()[0].Reviews) != 0 {
		t.Errorf("an empty review posted: %v", eventTypes(evs))
	}
}

// A subject action in a chain with no subject is routable, and says how to
// guard it; a subject whose repository nobody reaches is the run's failure.
func TestSubjectActionsWithoutASubject(t *testing.T) {
	for _, action := range []string{"mr_merge", "load_review_comments", "reply_review_comments", "post_review", "sync_target"} {
		t.Run(action, func(t *testing.T) {
			input := ""
			output := ""
			pre := ""
			switch action {
			case "reply_review_comments":
				input = ", input: { replies: { from: artifacts/replies.yaml, type: review_replies } }"
				pre = "  - { id: w, type: command, run: \"./w.sh\", output: { r: { to: artifacts/replies.yaml, type: review_replies } } }\n"
			case "post_review":
				input = ", input: { review: { from: artifacts/review.yaml, type: review_threads } }"
				pre = "  - { id: w, type: command, run: \"./w.sh\", output: { r: { to: artifacts/review.yaml, type: review_threads } } }\n"
			case "load_review_comments":
				output = ", output: { threads: { to: artifacts/threads.yaml, type: mr_threads } }"
			}
			doc := "name: sub\nversion: 5\nparameters: { harness: fake, model: fake/model }\nmain:\n" + pre +
				"  - { id: act, type: action, action: " + action + input + output + " }\n"
			h := newHarness(t, mustFlow(t, doc))
			h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
				if strings.Contains(cmd.Script, "./w.sh") {
					env.Files["artifacts/replies.yaml"] = []byte("[]\n")
					env.Files["artifacts/review.yaml"] = []byte("notes: []\n")
				}
				return envport.ExecResult{}, nil
			}
			id, sf := runOut(t, h, runinput.Ticket{}, "sub", runinput.Input{})
			wantFailure(t, sf, event.ReasonInputMissing, "run_if: run.subject")
			if s := h.view(t, id).State; s != run.Abandoned {
				t.Errorf("state = %s", s)
			}
		})
	}
}

// The subject's repository has gone from the project between the run
// starting and the stage: not routable.
func TestSubjectActionsWhenNothingReachesTheRepository(t *testing.T) {
	for _, stage := range []string{
		`{ id: act, type: action, action: mr_merge }`,
		`{ id: act, type: action, action: load_review_comments, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } } }`,
	} {
		h := newHarness(t, mustFlow(t, "name: sub\nversion: 5\noperates_on: merge_request\nparameters: { harness: fake, model: fake/model }\nmain:\n  - { id: first, type: command, run: \"./first.sh\" }\n  - "+stage+"\n"))
		ref := h.repoHost.AddOpenMR("alice", "t", "feature/x", "main", "a1")
		h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			if strings.Contains(cmd.Script, "./first.sh") {
				h.res.RepoHost = nil
			}
			return envport.ExecResult{}, nil
		}
		id, sf := runOut(t, h, runinput.Ticket{}, "sub", runinput.Input{MR: &runinput.MR{IID: ref.IID}})
		wantFailure(t, sf, event.ReasonInfra, "no integration reaches repository")
		if s := h.view(t, id).State; s != run.Failed {
			t.Errorf("state = %s", s)
		}
	}
}

func TestLoadingReviewThreadsCanFail(t *testing.T) {
	h := newHarness(t, mustFlow(t, reviewPostDoc))
	ref := h.repoHost.AddOpenMR("alice", "t", "feature/x", "main", "a1")
	h.repoHost.ThreadsErr = errors.New("rate limited")
	id, sf := runOut(t, h, runinput.Ticket{}, "rp", runinput.Input{MR: &runinput.MR{IID: ref.IID}})
	wantFailure(t, sf, event.ReasonInfra, "listing review threads: rate limited")
	if s := h.view(t, id).State; s != run.Failed {
		t.Errorf("state = %s", s)
	}
}

// writeRefusing is a workspace that takes no file at one path.
type writeRefusing struct {
	*fake.EnvProvisioner
	path string
}

func (w writeRefusing) WriteFile(ctx context.Context, id envport.EnvID, path string, data []byte, mode fs.FileMode) error {
	if path == w.path {
		return errors.New("disk full")
	}
	return w.EnvProvisioner.WriteFile(ctx, id, path, data, mode)
}

func TestLoadedThreadsThatCannotBeWritten(t *testing.T) {
	h := newHarness(t, mustFlow(t, reviewPostDoc))
	ref := h.repoHost.AddOpenMR("alice", "t", "feature/x", "main", "a1")
	h.res.Envp = writeRefusing{h.envp, "artifacts/threads.yaml"}
	_, sf := runOut(t, h, runinput.Ticket{}, "rp", runinput.Input{MR: &runinput.MR{IID: ref.IID}})
	wantFailure(t, sf, event.ReasonInfra, "writing artifacts/threads.yaml: disk full")
}

// A long first comment is cut for the question's list, on a person's page.
func TestThreadOptionCutsALongFirstComment(t *testing.T) {
	o := threadOption(repoport.ReviewThread{ID: "t1", Path: "a.go", Line: 4, Comments: []repoport.ReviewComment{
		{Author: "alice", Body: strings.Repeat("word ", 100)}}})
	if o.Label != "a.go:4 — alice" {
		t.Errorf("label = %q", o.Label)
	}
	if !strings.HasSuffix(o.Detail, "…") || len([]rune(o.Detail)) > 201 {
		t.Errorf("detail = %q", o.Detail)
	}
	plain := threadOption(repoport.ReviewThread{ID: "t2", Comments: []repoport.ReviewComment{{Author: "bob", Body: "short"}}})
	if plain.Label != "conversation — bob" || plain.Detail != "short" {
		t.Errorf("option = %+v", plain)
	}
}

func TestAtSaysTheFileOnce(t *testing.T) {
	if got := at("a.yaml", errors.New("a.yaml: bad")); got != "a.yaml: bad" {
		t.Errorf("at = %q", got)
	}
	if got := at("a.yaml", errors.New("bad")); got != "a.yaml: bad" {
		t.Errorf("at = %q", got)
	}
}

const replyDoc = `name: rr
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
agents:
  - name: replier
    prompt: answer them
    inputs:
      - { name: comments, type: mr_threads }
    outputs:
      - { name: replies, type: review_replies }
main:
  - { id: load, type: action, action: load_review_comments, with: { select: all }, output: { threads: { to: artifacts/threads.yaml, type: mr_threads } } }
  - { id: reply, type: agent, harness: $harness, model: $model, agent: replier, input: { comments: { from: artifacts/threads.yaml, type: mr_threads } }, output: { replies: { to: artifacts/replies.yaml, type: review_replies } } }
  - { id: post, type: action, action: reply_review_comments, input: { replies: { from: artifacts/replies.yaml, type: review_replies } }, on_success: done }
`

func TestReplyingFailures(t *testing.T) {
	boom := errors.New("host says no")
	cases := []struct {
		name string
		body string
		arm  func(h *harness, f *faultHost)
		want string
	}{
		{"a reply cannot be posted", "- { thread: d1, did: fixed, reply: renamed }\n", func(h *harness, _ *faultHost) { h.repoHost.ReplyErr = boom },
			"replying to thread d1: host says no"},
		{"a thread cannot be resolved", "- { thread: d1, did: fixed, reply: renamed, resolve: true }\n", func(_ *harness, f *faultHost) { f.resolve = boom },
			"resolving thread d1: host says no"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, replyDoc))
			f := &faultHost{RepoHost: h.repoHost}
			h.res.RepoHost = f
			ref := h.repoHost.AddOpenMR("alice", "t", "feature/x", "main", "a1")
			h.repoHost.AddComment(ref.IID, "d1", "a.go", "rev", "rename this", testTime)
			c.arm(h, f)
			h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
				h.writeFile(t, call.Env, "artifacts/replies.yaml", c.body)
				return agentport.AgentResult{TurnsUsed: 1}, nil
			}
			id, sf := runOut(t, h, runinput.Ticket{}, "rr", runinput.Input{MR: &runinput.MR{IID: ref.IID}})
			wantFailure(t, sf, event.ReasonInfra, c.want)
			if s := h.view(t, id).State; s != run.Failed {
				t.Errorf("state = %s", s)
			}
		})
	}
}

// A crash left an intent on the log: the re-entry asks the host what landed
// before it does anything, and a host that will not say fails the run rather
// than post a second time.
func TestReentriesThatCannotTellWhatLanded(t *testing.T) {
	boom := errors.New("host down")
	t.Run("post_review", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, reviewPostDoc))
		f := &faultHost{RepoHost: h.repoHost, threads: boom}
		h.res.RepoHost = f
		env := h.seedMRRun(t, "r1", "rp", "poll", "read", "post")
		h.writeFile(t, env, "artifacts/review.yaml", "replies:\n  - { thread: d1, body: x }\n")
		h.seed(t, "r1", &event.ActionStarted{StageID: "post", Action: "post_review"})
		recoverAndSettle(t, h, "r1")
		sf, _ := findPayload[*event.StageFailed](h.events(t, "r1"))
		wantFailure(t, sf, event.ReasonInfra, "cannot tell which replies were already posted: host down")
	})
	t.Run("reply_review_comments", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, replyDoc))
		f := &faultHost{RepoHost: h.repoHost, threads: boom}
		h.res.RepoHost = f
		env := h.seedMRRun(t, "r1", "rr", "load", "reply", "post")
		h.writeFile(t, env, "artifacts/replies.yaml", "- { thread: d1, did: fixed, reply: renamed }\n")
		h.seed(t, "r1", &event.ActionStarted{StageID: "post", Action: "reply_review_comments"})
		recoverAndSettle(t, h, "r1")
		sf, _ := findPayload[*event.StageFailed](h.events(t, "r1"))
		wantFailure(t, sf, event.ReasonInfra, "cannot tell which replies were already posted: host down")
	})
	t.Run("a reply already there is not posted twice", func(t *testing.T) {
		h := newHarness(t, mustFlow(t, reviewPostDoc))
		env := h.seedMRRun(t, "r1", "rp", "poll", "read", "post")
		h.writeFile(t, env, "artifacts/review.yaml", "replies:\n  - { thread: d1, body: x }\n")
		h.seed(t, "r1", &event.ActionStarted{StageID: "post", Action: "post_review"})
		x := &exec{rs: replayOf(t, h, "r1")}
		h.repoHost.AddComment(1, "d1", "a.go", "factory", "x\n\n"+repoport.ReplyMarker(x.roundID(), "d1"), testTime)
		recoverAndSettle(t, h, "r1")
		if got := len(h.repoHost.SnapshotThreads(1)[0].Comments); got != 2 {
			t.Errorf("thread has %d comments, want the reviewer's and one reply", got)
		}
		posted, ok := findPayload[*event.ReviewPosted](h.events(t, "r1"))
		if !ok || len(posted.Replied) != 1 {
			t.Errorf("review_posted = %+v", posted)
		}
	})
	t.Run("mr_merge", func(t *testing.T) {
		mergeDoc := "name: mg\nversion: 5\noperates_on: merge_request\nparameters: { harness: fake, model: fake/model }\nmain:\n  - { id: merge, type: action, action: mr_merge, on_success: done }\n"
		t.Run("cannot tell", func(t *testing.T) {
			h := newHarness(t, mustFlow(t, mergeDoc))
			f := &faultHost{RepoHost: h.repoHost, status: boom}
			h.res.RepoHost = f
			h.seedMRRun(t, "r1", "mg", "merge")
			h.seed(t, "r1", &event.ActionStarted{StageID: "merge", Action: "mr_merge"})
			recoverAndSettle(t, h, "r1")
			sf, _ := findPayload[*event.StageFailed](h.events(t, "r1"))
			wantFailure(t, sf, event.ReasonInfra, "cannot tell whether the merge already happened: host down")
		})
		t.Run("already merged is this merge", func(t *testing.T) {
			h := newHarness(t, mustFlow(t, mergeDoc))
			f := &faultHost{RepoHost: h.repoHost, merge: errors.New("must not merge twice")}
			h.res.RepoHost = f
			h.seedMRRun(t, "r1", "mg", "merge")
			h.seed(t, "r1", &event.ActionStarted{StageID: "merge", Action: "mr_merge"})
			h.repoHost.SetMRState(1, repoport.MRMerged)
			v := recoverAndSettle(t, h, "r1")
			if v.State != run.Completed {
				t.Fatalf("state = %s: %v", v.State, eventTypes(h.events(t, "r1")))
			}
			if countType(h.events(t, "r1"), event.TypeMRMerged) != 1 {
				t.Errorf("events = %v", eventTypes(h.events(t, "r1")))
			}
		})
		t.Run("a transport failure", func(t *testing.T) {
			h := newHarness(t, mustFlow(t, mergeDoc))
			f := &faultHost{RepoHost: h.repoHost, merge: boom}
			h.res.RepoHost = f
			ref := h.repoHost.AddOpenMR("alice", "t", "feature/x", "main", "a1")
			id, sf := runOut(t, h, runinput.Ticket{}, "mg", runinput.Input{MR: &runinput.MR{IID: ref.IID}})
			wantFailure(t, sf, event.ReasonInfra, "merging "+ref.URL+": host down")
			if s := h.view(t, id).State; s != run.Failed {
				t.Errorf("state = %s", s)
			}
		})
	})
}

// seedMRRun seeds a run on an open merge request, partway through its main
// chain: every stage but the last completed, the last underway.
func (h *harness) seedMRRun(t *testing.T, runID, flowName string, stages ...string) envport.EnvID {
	t.Helper()
	ctx := context.Background()
	h.repoHost.AddOpenMR("alice", "t", "feature/x", "main", "a1")
	h.repoHost.AddComment(1, "d1", "a.go", "rev", "rename this", testTime)
	env, err := h.envp.Provision(ctx, envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	co := event.Checkout{Name: event.PrimaryCheckoutName, Dir: event.PrimaryCheckoutDir, Branch: "feature/x", Source: "main", Primary: true}
	ps := []event.Payload{
		&event.RunCreated{TicketTitle: "T", FlowName: flowName, FlowHash: h.flows[flowName].Hash, ProjectID: testProject,
			Branch: "feature/x", SourceBranch: "main", Checkouts: []event.Checkout{co},
			MR: &event.AdoptedMR{Repo: event.PrimaryCheckoutName, IID: 1, URL: "https://gitlab.local/mr/1", Head: "a1"}},
		&event.MRCreated{Repo: event.PrimaryCheckoutName, Branch: "feature/x", Target: "main", MRURL: "https://gitlab.local/mr/1", MRIID: 1, Head: "a1"},
		&event.PhaseStarted{Phase: flow.MainPhase, Subject: event.PrimaryCheckoutName, Tip: "a1"},
		&event.EnvReady{EnvID: string(env)},
	}
	for i, st := range stages {
		ps = append(ps, &event.StageStarted{StageID: st, Iteration: 1})
		if i == len(stages)-1 {
			break
		}
		if st == "poll" || st == "load" {
			ps = append(ps, &event.ReviewCommentsLoaded{StageID: st, Subject: event.PrimaryCheckoutName, Scope: "all",
				Threads: []event.LoadedThread{{ID: "d1", Path: "a.go", Line: 1}}})
		}
		ps = append(ps, &event.StageCompleted{StageID: st, TurnsUsed: 1})
	}
	h.seed(t, runID, ps...)
	return env
}

// replayOf is the run's state as the log folds.
func replayOf(t *testing.T, h *harness, runID string) *runcore.State {
	t.Helper()
	return runcore.Replay(h.events(t, runID), h.flows[h.view(t, runID).FlowName])
}

// --- the tracker -------------------------------------------------------------

func TestTrackerFailures(t *testing.T) {
	boom := errors.New("tracker down")
	writerStage := "  - { id: w, type: command, run: \"./w.sh\", output: { a: { to: artifacts/a.md, type: file } } }\n"
	doc := func(stage string) string {
		return "name: tk\nversion: 2\nparameters: { harness: fake, model: fake/model }\nmain:\n" + writerStage + "  - " + stage + "\n"
	}
	comment := "{ id: act, type: action, action: ticket_comment, input: { body: { from: artifacts/a.md, type: file } } }"
	cases := []struct {
		name, stage string
		file        string
		arm         func(h *harness, tr *fake.TicketSource)
		reason      string
		want        string
	}{
		{"a comment the tracker refuses", comment, "note", func(_ *harness, tr *fake.TicketSource) { tr.WriteErr = boom }, event.ReasonInfra, "ticket_comment: tracker down"},
		{"an empty comment", comment, "   \n", nil, event.ReasonInputMissing, "comment body artifacts/a.md is empty"},
		{"a field the tracker refuses", "{ id: act, type: action, action: ticket_set_field, with: { field: Notes, text: x } }", "", func(_ *harness, tr *fake.TicketSource) { tr.WriteErr = boom },
			event.ReasonInfra, "ticket_set_field: tracker down"},
		{"a field append that cannot read the field", "{ id: act, type: action, action: ticket_set_field, with: { field: Labels, text: x, mode: append } }", "",
			func(_ *harness, tr *fake.TicketSource) { tr.KnownFields = []string{"Notes"} }, event.ReasonInputInvalid, "Labels"},
		{"a branch link the tracker refuses", "{ id: act, type: action, action: ticket_link_branch }", "", func(_ *harness, tr *fake.TicketSource) { tr.WriteErr = boom }, event.ReasonInfra, "tracker down"},
		{"a branch the host cannot read", "{ id: act, type: action, action: ticket_link_branch }", "", func(h *harness, _ *fake.TicketSource) { h.repoHost.BranchErr = boom }, event.ReasonInfra, "reading branch"},
		{"a merge request link with no merge request", "{ id: act, type: action, action: ticket_link }", "", nil, event.ReasonInputMissing, "no merge request to link"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, tr := trackerHarness(t, doc(c.stage))
			h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
				if strings.Contains(cmd.Script, "./w.sh") {
					env.Files["artifacts/a.md"] = []byte(c.file)
				}
				return envport.ExecResult{}, nil
			}
			if c.arm != nil {
				c.arm(h, tr)
			}
			id, sf := runOut(t, h, linkedTicket, "tk", runinput.Input{})
			wantFailure(t, sf, c.reason, c.want)
			if s := h.view(t, id).State; s != run.Abandoned {
				t.Errorf("state = %s: a tracker failure is routable", s)
			}
		})
	}
}

func TestTrackerWriterThatCannotBeReached(t *testing.T) {
	doc := "name: tk\nversion: 2\nparameters: { harness: fake, model: fake/model }\nmain:\n  - { id: act, type: action, action: ticket_transition, with: { to: done } }\n"
	t.Run("at creation", func(t *testing.T) {
		h, _ := trackerHarness(t, doc)
		h.res.TicketWriter = func(context.Context, string) (trackerport.TicketWriter, error) {
			return nil, errors.New("not writable")
		}
		_, err := h.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
		wantErrText(t, err, `preflight: stage "act": action "ticket_transition" writes to ticket DEV-1's tracker, but not writable`)
		h.res.TicketWriter = nil
		_, err = h.e.CreateRun(context.Background(), linkedTicket, "tk", testProject)
		wantErrText(t, err, "which this build cannot")
	})
	t.Run("once running", func(t *testing.T) {
		h, tr := trackerHarness(t, doc)
		calls := 0
		h.res.TicketWriter = func(context.Context, string) (trackerport.TicketWriter, error) {
			if calls++; calls > 1 {
				return nil, errors.New("credential revoked")
			}
			return tr, nil
		}
		_, sf := runOut(t, h, linkedTicket, "tk", runinput.Input{})
		wantFailure(t, sf, event.ReasonInfra, "ticket DEV-1's tracker: credential revoked")
	})
	t.Run("gone while running", func(t *testing.T) {
		h, _ := trackerHarness(t, "name: tk\nversion: 2\nparameters: { harness: fake, model: fake/model }\nmain:\n  - { id: first, type: command, run: \"./first.sh\" }\n  - { id: act, type: action, action: ticket_transition, with: { to: done } }\n")
		h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
			if strings.Contains(cmd.Script, "./first.sh") {
				h.res.TicketWriter = nil
			}
			return envport.ExecResult{}, nil
		}
		_, sf := runOut(t, h, linkedTicket, "tk", runinput.Input{})
		wantFailure(t, sf, event.ReasonInfra, "this build cannot write to a ticket tracker")
	})
}

// A crash left a comment's intent on the log and the tracker will not say
// whether it landed: routable, and nothing is posted.
func TestACommentReentryThatCannotTell(t *testing.T) {
	h, tr := trackerHarness(t, "name: tk\nversion: 2\nparameters: { harness: fake, model: fake/model }\nmain:\n  - { id: w, type: command, run: \"./w.sh\", output: { a: { to: artifacts/a.md, type: file } } }\n  - { id: act, type: action, action: ticket_comment, input: { body: { from: artifacts/a.md, type: file } } }\n")
	env, err := h.envp.Provision(context.Background(), envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	h.seed(t, "r1", &event.RunCreated{TicketTitle: "T", FlowName: "tk", FlowHash: h.flows["tk"].Hash, ProjectID: testProject,
		Branch: "feat/x", TicketSourceID: "int-1", TicketSourceKey: "DEV-1", TicketID: "tk-1"},
		&event.PhaseStarted{Phase: flow.MainPhase}, &event.EnvReady{EnvID: string(env)},
		&event.StageStarted{StageID: "w", Iteration: 1}, &event.StageCompleted{StageID: "w"},
		&event.StageStarted{StageID: "act", Iteration: 1}, &event.ActionStarted{StageID: "act", Action: "ticket_comment"})
	tr.Err = errors.New("tracker down")
	v := recoverAndSettle(t, h, "r1")
	sf, _ := findPayload[*event.StageFailed](h.events(t, "r1"))
	wantFailure(t, sf, event.ReasonInfra, "cannot tell whether the comment was already posted: tracker down")
	if v.State != run.Abandoned {
		t.Errorf("state = %s", v.State)
	}
}

// A tracker that cannot host files fails a comment that has some, before
// anything is written.
type noAttacher struct{ trackerport.TicketWriter }

func TestACommentWithFilesOnATrackerThatCannotHostThem(t *testing.T) {
	doc := `name: tk
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: w, type: command, run: "./w.sh", output: { a: { to: artifacts/a.md, type: file }, f: { to: artifacts/shots/, type: files } } }
  - { id: act, type: action, action: ticket_comment, input: { body: { from: artifacts/a.md, type: file }, attachments: { from: artifacts/shots/, type: files } } }
`
	h, tr := trackerHarness(t, doc)
	h.res.TicketWriter = func(context.Context, string) (trackerport.TicketWriter, error) { return noAttacher{tr}, nil }
	h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if strings.Contains(cmd.Script, "./w.sh") {
			env.Files["artifacts/a.md"] = []byte("note")
			env.Files["artifacts/shots/one.png"] = []byte("png")
		}
		return envport.ExecResult{}, nil
	}
	_, sf := runOut(t, h, linkedTicket, "tk", runinput.Input{})
	wantFailure(t, sf, event.ReasonInfra, "tracker cannot host attachments")
	if len(tr.Posted) != 0 {
		t.Errorf("posted = %v", tr.Posted)
	}
}

func TestMergeItemsIsAUnion(t *testing.T) {
	for _, c := range []struct{ cur, add, want string }{
		{"", "a", "a"},
		{"a, b", "b, c", "a, b, c"},
		{"a", "a", "a"},
		{"a,,b", " ", "a, b"},
	} {
		if got := mergeItems(c.cur, c.add); got != c.want {
			t.Errorf("mergeItems(%q, %q) = %q, want %q", c.cur, c.add, got, c.want)
		}
	}
}

// Every checkout's branch and merge request is linked, named for its
// repository; one the host holds no integration for is passed over.
func TestTicketLinksAcrossRepositories(t *testing.T) {
	doc := `name: tk
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: branch, type: action, action: ticket_link_branch }
`
	h, tr := trackerHarness(t, doc)
	h.res.ExtraRepos = []resolution.ExtraRepo{{Name: "web", URL: "https://x/web.git", DefaultBranch: "main"}, {Name: "docs", URL: "https://x/docs.git", DefaultBranch: "main"}}
	h.res.RepoHosts = map[string]repoport.RepoHost{"web": &fake.RepoHost{}, "docs": &fake.RepoHost{Unpushed: true}}
	id, sf := runOut(t, h, linkedTicket, "tk", runinput.Input{})
	if sf != nil {
		t.Fatalf("failed: %+v", sf)
	}
	// Both hosts' pages share the fake's URL, which a link upserts on: the
	// calls say both went out, the last one named for its repository.
	calls := 0
	for _, c := range tr.Calls {
		if strings.HasPrefix(c, "link DEV-1") {
			calls++
		}
	}
	links := tr.Linked["DEV-1"]
	if calls != 2 || len(links) != 1 || !strings.HasPrefix(links[0].Title, "Branch (web): ") {
		t.Errorf("%d link calls, links = %+v", calls, links)
	}
	if s := h.view(t, id).State; s != run.Completed {
		t.Errorf("state = %s", s)
	}

	// Nothing to link from: visibly skipped.
	h2, _ := trackerHarness(t, doc)
	h2.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) { return envport.ExecResult{}, nil }
	first := true
	h2.res.TicketWriter = func(context.Context, string) (trackerport.TicketWriter, error) {
		if !first {
			h2.res.RepoHost = nil
		}
		first = false
		return newTracker(), nil
	}
	id2, sf2 := runOut(t, h2, linkedTicket, "tk", runinput.Input{})
	if sf2 != nil {
		t.Fatalf("failed: %+v", sf2)
	}
	sc, _ := findPayload[*event.StageCompleted](h2.events(t, id2))
	if sc == nil || !sc.Skipped || !strings.Contains(sc.Note, "no checkout of this run has a repository host") {
		t.Errorf("completed = %+v", sc)
	}
}

// --- taking the target in ----------------------------------------------------

const syncDoc = `name: sy
version: 5
operates_on: merge_request
parameters: { harness: fake, model: fake/model }
main:
  - { id: sync, type: action, action: sync_target, with: { strategy: STRATEGY } }
  - { id: push, type: action, action: push_branch, on_success: done }
`

// gitScript answers git commands by the first substring they contain; an
// entry's err is an exec failure (the workspace is gone), not a git one.
type gitAnswer struct {
	match string
	out   string
	code  int
	err   error
}

func gitScript(answers ...gitAnswer) func(*fake.Env, envport.Command) (envport.ExecResult, error) {
	return func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		for _, a := range answers {
			if strings.Contains(cmd.Script, a.match) {
				return envport.ExecResult{ExitCode: a.code, Output: a.out}, a.err
			}
		}
		return envport.ExecResult{}, nil
	}
}

func TestSyncFailures(t *testing.T) {
	gone := errors.New("exec: container gone")
	tip := gitAnswer{match: "git rev-parse 'origin/main'", out: "t1\n"}
	notIn := gitAnswer{match: "merge-base --is-ancestor", code: 1}
	none := gitAnswer{match: "rebase-merge", out: "none\n"}
	pausedMerge := gitAnswer{match: "rebase-merge", out: "merge t1\n"}
	pausedOther := gitAnswer{match: "rebase-merge", out: "rebase old\n"}
	cases := []struct {
		name     string
		strategy string
		answers  []gitAnswer
		reason   string
		want     string
		final    run.State
	}{
		{"fetch exec", "merge", []gitAnswer{{match: "git fetch origin 'main'", err: gone}}, event.ReasonInfra, "sync: exec: container gone", run.Failed},
		{"fetch refused", "merge", []gitAnswer{{match: "git fetch origin 'main'", code: 1, out: "no such ref"}}, event.ReasonExitNonzero, "fetching main failed: no such ref", run.Abandoned},
		{"target tip", "merge", []gitAnswer{{match: "git rev-parse", err: gone}}, event.ReasonInfra, "sync: exec", run.Failed},
		{"paused state", "merge", []gitAnswer{tip, {match: "rebase-merge", err: gone}}, event.ReasonInfra, "sync: exec", run.Failed},
		{"aborting a stale pause", "merge", []gitAnswer{tip, pausedOther, {match: "git rebase --abort", err: gone}}, event.ReasonInfra, "sync: exec", run.Failed},
		{"ancestry", "merge", []gitAnswer{tip, none, {match: "merge-base", err: gone}}, event.ReasonInfra, "sync: exec", run.Failed},
		{"merge exec", "merge", []gitAnswer{tip, none, notIn, {match: "git merge --no-edit", err: gone}}, event.ReasonInfra, "sync: exec", run.Failed},
		{"not a conflict", "merge", []gitAnswer{tip, none, notIn, {match: "git merge --no-edit", code: 1, out: "refusing"}}, event.ReasonExitNonzero, "taking main in (merge) failed: refusing", run.Abandoned},
		{"head unreadable", "merge", []gitAnswer{tip, none, notIn, {match: "git rev-parse HEAD", err: gone}}, event.ReasonInfra, "sync: exec", run.Failed},
		{"markers unreadable", "merge", []gitAnswer{tip, pausedMerge, {match: "diff-filter=U); if", err: gone}}, event.ReasonInfra, "sync: exec", run.Failed},
		{"continuing fails", "merge", []gitAnswer{tip, pausedMerge, {match: "git add -A", err: gone}}, event.ReasonInfra, "sync: exec", run.Failed},
		{"a rebase continuing into the next conflict", "rebase", []gitAnswer{tip, {match: "rebase-merge", out: "rebase t1\n"}, {match: "xargs -0 grep"}, {match: "git add -A", code: 1, out: "conflict"},
			{match: "git diff --name-only --diff-filter=U", out: "b.go\n"}}, event.ReasonExitNonzero, "conflicts in: b.go", run.Abandoned},
		{"a commit that leaves the op paused", "merge", []gitAnswer{tip, {match: "git add -A"}, pausedMerge, {match: "xargs -0 grep"}, {match: "git diff --name-only --diff-filter=U", out: "c.go\n"}}, event.ReasonExitNonzero, "conflicts in: c.go", run.Abandoned},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, strings.Replace(syncDoc, "STRATEGY", c.strategy, 1)))
			ref := h.repoHost.AddOpenMR("alice", "t", "feature/x", "main", "a1")
			h.envp.ExecFunc = gitScript(c.answers...)
			id, sf := runOut(t, h, runinput.Ticket{}, "sy", runinput.Input{MR: &runinput.MR{IID: ref.IID}})
			wantFailure(t, sf, c.reason, c.want)
			if s := h.view(t, id).State; s != c.final {
				t.Errorf("state = %s", s)
			}
		})
	}
}

func TestPushFailures(t *testing.T) {
	gone := errors.New("exec: container gone")
	doc := "name: pu\nversion: 5\nparameters: { harness: fake, model: fake/model }\nmain:\n  - { id: push, type: action, action: push_branch, on_success: done }\n"
	cases := []struct {
		name    string
		answers []gitAnswer
		reason  string
		want    string
		final   run.State
	}{
		{"head exec", []gitAnswer{{match: "git rev-parse HEAD", err: gone}}, event.ReasonInfra, "push: exec", run.Failed},
		{"head refused", []gitAnswer{{match: "git rev-parse HEAD", code: 128, out: "not a repo"}}, event.ReasonExitNonzero, "repo: cannot read HEAD: not a repo", run.Abandoned},
		{"push exec", []gitAnswer{{match: "git rev-parse HEAD", out: "h1\n"}, {match: "git push", err: gone}}, event.ReasonInfra, "push: exec", run.Failed},
		{"push refused", []gitAnswer{{match: "git rev-parse HEAD", out: "h1\n"}, {match: "git push", code: 1, out: "rejected"}}, event.ReasonExitNonzero, "was refused", run.Abandoned},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, doc))
			h.envp.ExecFunc = gitScript(c.answers...)
			id, sf := runOut(t, h, runinput.Ticket{}, "pu", runinput.Input{})
			wantFailure(t, sf, c.reason, c.want)
			if s := h.view(t, id).State; s != c.final {
				t.Errorf("state = %s", s)
			}
		})
	}
}

// Publishing: the host failing at each step of opening a merge request.
func TestCreateMRFailures(t *testing.T) {
	boom := errors.New("host says no")
	doc := `name: pb
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: w, type: command, run: "./w.sh", output: { a: { to: artifacts/mr.md, type: file }, f: { to: artifacts/shots/, type: files } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file }, attachments: { from: artifacts/shots/, type: files } }, on_success: done }
`
	cases := []struct {
		name   string
		arm    func(h *harness)
		reason string
		want   string
		final  run.State
	}{
		{"cannot tell if it exists", func(h *harness) { h.repoHost.FindErr = boom }, event.ReasonInfra, "cannot tell whether the merge request was already created", run.Failed},
		{"cannot open it", func(h *harness) { h.repoHost.CreateErr = boom }, event.ReasonInfra, "opening the merge request failed: host says no", run.Failed},
		{"cannot attach its files", func(h *harness) { h.repoHost.UploadErr = boom }, event.ReasonInfra, "attaching files to the merge request failed", run.Abandoned},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, doc))
			h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
				if strings.Contains(cmd.Script, "./w.sh") {
					env.Files["artifacts/mr.md"] = []byte("# Title\n")
					env.Files["artifacts/shots/one.png"] = []byte("png")
				}
				return envport.ExecResult{}, nil
			}
			c.arm(h)
			id, sf := runOut(t, h, runinput.Ticket{}, "pb", runinput.Input{})
			wantFailure(t, sf, c.reason, c.want)
			if s := h.view(t, id).State; s != c.final {
				t.Errorf("state = %s", s)
			}
		})
	}
}

// An extra repository's commit count decides whether it gets a merge request,
// and a count that cannot be read is not "no changes".
func TestExtraRepositoryAheadCheck(t *testing.T) {
	doc := `name: pb
version: 2
parameters: { harness: fake, model: fake/model }
main:
  - { id: w, type: command, run: "./w.sh", output: { a: { to: artifacts/mr.md, type: file } } }
  - { id: mr, type: action, action: create_mr, input: { body: { from: artifacts/mr.md, type: file } }, on_success: done }
`
	cases := []struct {
		name string
		ans  gitAnswer
		want string
	}{
		{"exec", gitAnswer{match: "git rev-list", err: errors.New("gone")}, "repo web: gone"},
		{"refused", gitAnswer{match: "git rev-list", code: 128, out: "bad ref"}, "repo web: git rev-list: bad ref"},
		{"garbage", gitAnswer{match: "git rev-list", out: "many"}, `repo web: git rev-list: unexpected output "many"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, mustFlow(t, doc))
			h.res.ExtraRepos = []resolution.ExtraRepo{{Name: "web", URL: "https://x/web.git", DefaultBranch: "main"}}
			h.res.RepoHosts = map[string]repoport.RepoHost{"web": &fake.RepoHost{}}
			script := gitScript(c.ans)
			h.envp.ExecFunc = func(env *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
				if strings.Contains(cmd.Script, "./w.sh") {
					env.Files["artifacts/mr.md"] = []byte("# Title\n")
				}
				return script(env, cmd)
			}
			_, sf := runOut(t, h, runinput.Ticket{}, "pb", runinput.Input{})
			wantFailure(t, sf, event.ReasonInfra, c.want)
		})
	}
}
