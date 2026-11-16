package engine

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine/redact"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runinput"
)

const (
	treeParked = "1111111111111111111111111111111111111111"
	treeFixed  = "2222222222222222222222222222222222222222"
)

// debugWith runs debugLeafDoc with the workspace's git answering as given:
// tree is what workTreeScript prints (by call: park, then continue), diff
// what diff-tree prints (exit 1 when diffFails). It answers continue with
// a summary and returns the run's events and every script git was asked.
func debugWith(t *testing.T, trees []string, diff string, diffFails bool) ([]event.Event, []string) {
	t.Helper()
	h := newHarness(t, mustFlow(t, debugLeafDoc))
	var verifies, treeCalls atomic.Int32
	var scripts []string
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		if (strings.Contains(cmd.Script, "git write-tree") || strings.Contains(cmd.Script, "git diff-tree")) && cmd.Timeout != handEditsTimeout {
			// The park's runs before the ping: a hung git must not hold it
			// for a stage's whole timeout.
			t.Errorf("%q ran under %s, want %s", cmd.Script, cmd.Timeout, handEditsTimeout)
		}
		switch {
		case strings.Contains(cmd.Script, "make lint"):
			if verifies.Add(1) <= 2 {
				return envport.ExecResult{ExitCode: 1, Output: "segfault"}, nil
			}
		case strings.Contains(cmd.Script, "git write-tree"):
			scripts = append(scripts, cmd.Dir+": write-tree")
			i := int(treeCalls.Add(1)) - 1
			if i >= len(trees) || trees[i] == "" {
				return envport.ExecResult{ExitCode: 128, Output: "fatal: not a git repository"}, nil
			}
			return envport.ExecResult{Output: "warning: CRLF\n" + trees[i] + "\n"}, nil
		case strings.Contains(cmd.Script, "git diff-tree"):
			scripts = append(scripts, cmd.Dir+": "+cmd.Script)
			if diffFails {
				return envport.ExecResult{ExitCode: 128, Output: "fatal: bad object"}, nil
			}
			return envport.ExecResult{Output: diff}, nil
		}
		return envport.ExecResult{}, nil
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "dbg", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p := h.waitPing(t, PingDebug)
	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve", Feedback: "pinned the lockfile"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(id)
	return h.events(t, id), scripts
}

// The run parks with the working copy's tree on the debug ping; the continue
// compares it with the tree now and records the paths that differ, before
// the failing stage runs again — and the journal shows them beside the
// person's summary.
func TestHandEditsRecordWhatThePersonChanged(t *testing.T) {
	evs, scripts := debugWith(t, []string{treeParked, treeFixed}, "go.sum\ninternal/x.go\n", false)
	raised, _ := findPayload[*event.PingRaised](evs)
	if raised == nil || raised.Trees["repo"] != treeParked {
		t.Fatalf("ping_raised trees = %+v", raised)
	}
	// The debug ping is recorded with its reference: the first ping about
	// the attempt that failed.
	failed := 0
	for _, e := range evs {
		if s, ok := e.Payload.(*event.StageStarted); ok && s.StageID == "test" {
			failed = s.Attempt
		}
		if e.Payload == raised {
			break
		}
	}
	if want := fmt.Sprintf("test#%d/decision#1", failed); raised.Ref != want {
		t.Errorf("debug ping ref = %q, want %q", raised.Ref, want)
	}
	var edits *event.HandEdits
	for i, e := range evs {
		if p, ok := e.Payload.(*event.HandEdits); ok {
			edits = p
			if next, ok := evs[i+1].Payload.(*event.StageStarted); !ok || next.StageID != "test" {
				t.Errorf("hand_edits is followed by %T, want the re-run", evs[i+1].Payload)
			}
		}
	}
	if edits == nil || edits.PingID != raised.PingID || edits.StageID != "test" || len(edits.Checkouts) != 1 ||
		edits.Checkouts[0].Checkout != "repo" || strings.Join(edits.Checkouts[0].Paths, ",") != "go.sum,internal/x.go" {
		t.Fatalf("hand_edits = %+v", edits)
	}
	if want := "repo: git diff-tree -r --no-renames --name-only " + treeParked + " " + treeFixed; scripts[len(scripts)-1] != want {
		t.Errorf("diff = %q, want %q", scripts[len(scripts)-1], want)
	}
	j := buildJournal(evs, nil, nil)
	if len(j.decisions) != 1 {
		t.Fatalf("decisions = %d", len(j.decisions))
	}
	if got := decisionAnswer(j.decisions[0]); got != "continued: the failing stage ran again\nWhat they did: pinned the lockfile\nFiles they changed: go.sum, internal/x.go" {
		t.Errorf("decision = %q", got)
	}
}

// Best effort, said so: nothing changed; a tree missing at the park or at
// the continue; a parked tree the workspace no longer has.
func TestHandEditsBestEffort(t *testing.T) {
	for _, c := range []struct {
		name      string
		trees     []string
		diffFails bool
		want      string
	}{
		{"nothing changed", []string{treeParked, treeParked}, false, "Files they changed: none"},
		{"no tree at the park", []string{"", treeFixed}, false, "Files they changed: could not be determined — its working copy was not recorded as the run parked"},
		{"no tree at the continue", []string{treeParked, ""}, false, "Files they changed: could not be determined — its working copy could not be read at the continue"},
		{"the workspace recreated", []string{treeParked, treeFixed}, true, "the workspace was recreated while parked"},
	} {
		t.Run(c.name, func(t *testing.T) {
			evs, _ := debugWith(t, c.trees, "", c.diffFails)
			j := buildJournal(evs, nil, nil)
			if got := decisionAnswer(j.decisions[0]); !strings.Contains(got, c.want) {
				t.Errorf("decision = %q, want %q", got, c.want)
			}
		})
	}
}

// Several checkouts read one line each; a long list is cut with a count; a
// continue from an older log, with no hand_edits, names no files.
func TestHandEditsWords(t *testing.T) {
	many := make([]string, handEditsShown+3)
	for i := range many {
		many[i] = "f"
	}
	got := handEditsWords(&event.HandEdits{Checkouts: []event.CheckoutEdits{
		{Checkout: "repo", Paths: []string{"a.go"}},
		{Checkout: "web", Paths: many},
		{Checkout: "docs"},
	}})
	if !strings.HasPrefix(got, "\nFiles they changed:\n- repo: a.go\n- web: f, f") || !strings.HasSuffix(got, " and 3 more\n- docs: none") {
		t.Errorf("several = %q", got)
	}
	if handEditsWords(nil) != "" || handEditsWords(&event.HandEdits{}) != "" {
		t.Error("no record, some words")
	}
}

// Every checkout of a multi-repository run is fingerprinted in its own
// directory; with no workspace there is nothing to take.
func TestWorkTreesPerCheckout(t *testing.T) {
	h := newHarness(t, mustFlow(t, debugLeafDoc))
	var dirs []string
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		dirs = append(dirs, cmd.Dir)
		if cmd.Dir == "repos/web" {
			return envport.ExecResult{Output: "not a tree"}, nil
		}
		return envport.ExecResult{Output: treeParked}, nil
	}
	env, err := h.envp.Provision(context.Background(), envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	st := replayFor(env)
	st.Flow = h.flows["dbg"]
	st.Checkouts = []event.Checkout{{Name: "repo", Dir: "repo", Primary: true}, {Name: "web", Dir: "repos/web"}}
	x := &exec{e: h.e, ctx: context.Background(), bg: context.Background(), res: h.res, rs: st, redact: redact.New()}
	if got := x.workTrees(); len(got) != 1 || got["repo"] != treeParked || strings.Join(dirs, ",") != "repo,repos/web" {
		t.Errorf("trees = %v from %v", got, dirs)
	}
	x.rs.EnvID = ""
	if x.workTrees() != nil {
		t.Error("trees without a workspace")
	}
}

// The parked tree comes back from the event log into a git command line, so
// only a tree id ever reaches it: anything else reads as not recorded. Equal
// trees need no diff.
func TestHandEditsTrustsOnlyATreeID(t *testing.T) {
	h := newHarness(t, mustFlow(t, debugLeafDoc))
	var scripts []string
	h.envp.ExecFunc = func(_ *fake.Env, cmd envport.Command) (envport.ExecResult, error) {
		scripts = append(scripts, cmd.Script)
		return envport.ExecResult{Output: treeParked}, nil
	}
	env, err := h.envp.Provision(context.Background(), envport.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	st := replayFor(env)
	st.Flow = h.flows["dbg"]
	st.Checkouts = []event.Checkout{{Name: "repo", Dir: "repo", Primary: true}, {Name: "web", Dir: "repos/web"}}
	st.RaisedPings = map[string]*event.PingRaised{"p": {PingID: "p", Trees: map[string]string{
		"repo": treeParked, "web": "HEAD; touch pwned",
	}}}
	x := &exec{e: h.e, ctx: context.Background(), bg: context.Background(), res: h.res, rs: st, redact: redact.New()}
	rec := x.handEdits(st.Flow.Stage("test"), "p")
	if len(rec.Checkouts) != 2 || rec.Checkouts[0].Unknown != "" || len(rec.Checkouts[0].Paths) != 0 ||
		rec.Checkouts[1].Unknown != "its working copy was not recorded as the run parked" {
		t.Errorf("hand edits = %+v", rec)
	}
	for _, s := range scripts {
		if strings.Contains(s, "diff-tree") {
			t.Errorf("ran %q", s)
		}
	}
}
