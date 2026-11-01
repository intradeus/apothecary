package runcore

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports/repoport"
	"apothecary/internal/run/event"
)

// A wait point settles on its chain's subject when it has one, else on the
// run's merge requests: a signal that waits for every one holds once each has
// it or is past it, and can no longer hold once one cannot; any other holds on
// the first, and is impossible only when all of them are gone.
func TestWaitState(t *testing.T) {
	now := time.Unix(1000, 0)
	two := func() *State {
		rs := idleState(t)
		rs.MRs = []MRRecord{{Repo: "repo", Ref: repoport.MRRef{URL: "http://one"}}, {Repo: "web", Ref: repoport.MRRef{URL: "http://two"}}}
		return rs
	}
	obs := func(rs *State, a, b repoport.MRStatus) []MRObservation {
		return []MRObservation{{MR: &rs.MRs[0], Status: a}, {MR: &rs.MRs[1], Status: b}}
	}
	open := repoport.MRStatus{State: repoport.MROpen}
	merged := repoport.MRStatus{State: repoport.MRMerged}
	closed := repoport.MRStatus{State: repoport.MRClosed}
	conflict := repoport.MRStatus{State: repoport.MROpen, Mergeable: repoport.MergeConflicts}
	mergeable := repoport.MRStatus{State: repoport.MROpen, MergeableNow: true}

	cases := []struct {
		name              string
		slug, subject     string
		a, b              repoport.MRStatus
		holds, impossible bool
		why               string
	}{
		{name: "every: one still open", slug: "mr_merged", a: merged, b: open},
		{name: "every: both merged", slug: "mr_merged", a: merged, b: merged, holds: true},
		{name: "every: one closed can no longer hold", slug: "mr_merged", a: merged, b: closed, impossible: true, why: "closed unmerged: http://two"},
		{name: "every: merged is past mergeable", slug: "mr_mergeable", a: merged, b: mergeable, holds: true},
		{name: "every: a subject answers for itself", slug: "mr_merged", subject: "repo", a: merged, b: open, holds: true},
		{name: "any: holds on the first", slug: "mr_conflicts", a: open, b: conflict, holds: true},
		{name: "any: nothing yet", slug: "mr_conflicts", a: open, b: open},
		{name: "any: one gone is not all", slug: "mr_conflicts", a: merged, b: open},
		{name: "any: one gone, the subject's", slug: "mr_conflicts", subject: "repo", a: merged, b: open, impossible: true, why: "the merge request merged: http://one"},
		{name: "any: every one gone", slug: "mr_conflicts", a: merged, b: closed, impossible: true, why: "every merge request is merged or closed"},
		{name: "any: a subject nobody observed", slug: "mr_conflicts", subject: "gone", a: conflict, b: conflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := two()
			holds, impossible, why := rs.WaitState(c.slug, c.subject, obs(rs, c.a, c.b), now)
			if holds != c.holds || impossible != c.impossible {
				t.Fatalf("holds %v, impossible %v (%q)", holds, impossible, why)
			}
			if !strings.Contains(why, c.why) {
				t.Errorf("why = %q, want %q", why, c.why)
			}
		})
	}
	t.Run("nothing opened, nothing can hold", func(t *testing.T) {
		rs := idleState(t)
		rs.MRs = nil
		holds, impossible, why := rs.WaitState("mr_merged", "", nil, now)
		if holds || !impossible || !strings.Contains(why, "opened no merge request") {
			t.Errorf("holds %v, impossible %v, why %q", holds, impossible, why)
		}
	})
	t.Run("a signal nothing observes never settles", func(t *testing.T) {
		rs := idleState(t)
		if holds, impossible, _ := rs.WaitState("manual", "", open2(rs), now); holds || impossible {
			t.Errorf("holds %v, impossible %v", holds, impossible)
		}
	})
}

func open2(rs *State) []MRObservation {
	return []MRObservation{{MR: &rs.MRs[0], Status: repoport.MRStatus{State: repoport.MROpen}}}
}

func TestDeliveredDropsOnlyWhatArrived(t *testing.T) {
	rs := NewState(nil)
	rs.PendingMsgs = []*event.MessageQueued{{MessageID: "a"}, {MessageID: "b"}, {MessageID: "c"}}
	rs.Delivered(nil)
	if len(rs.PendingMsgs) != 3 {
		t.Fatalf("pending = %d", len(rs.PendingMsgs))
	}
	rs.Delivered([]string{"a", "c", "ghost"})
	if len(rs.PendingMsgs) != 1 || rs.PendingMsgs[0].MessageID != "b" {
		t.Errorf("pending = %+v", rs.PendingMsgs)
	}
}

func TestCheckoutLookups(t *testing.T) {
	rs := NewState(nil)
	if got := rs.CheckoutDirs(); !reflect.DeepEqual(got, []string{event.PrimaryCheckoutDir}) {
		t.Errorf("no checkouts = %v", got)
	}
	rs.Checkouts = []event.Checkout{{Name: event.PrimaryCheckoutName, Dir: "repo"}, {Name: "web", Dir: "repos/web"}}
	if got := rs.CheckoutDirs(); !reflect.DeepEqual(got, []string{"repo", "repos/web"}) {
		t.Errorf("dirs = %v", got)
	}
	if c := rs.Checkout(""); c == nil || c.Name != event.PrimaryCheckoutName {
		t.Errorf("default checkout = %+v", c)
	}
	if rs.Checkout("gone") != nil {
		t.Error("a checkout that is not there")
	}
}

func TestMergeRequestRecords(t *testing.T) {
	rs := NewState(nil)
	if rs.FirstMR() != nil {
		t.Error("a first merge request of none")
	}
	rs.AddMR(&event.MRCreated{Branch: "b", MRURL: "u1", MRIID: 1, Head: "h1"})
	rs.AddMR(&event.MRCreated{Repo: "web", Branch: "b", MRURL: "u2", MRIID: 2})
	if got := rs.FirstMR(); got == nil || got.IID != 1 {
		t.Errorf("first = %+v", got)
	}
	// "" is the primary, and a second creation for the branch replaces it.
	rs.AddMR(&event.MRCreated{Repo: event.PrimaryCheckoutName, Branch: "b", Target: "dev", MRURL: "u3", MRIID: 3})
	mr := rs.MRFor("", "b")
	if len(rs.MRs) != 2 || mr == nil || mr.Ref.IID != 3 || mr.Target != "dev" {
		t.Errorf("records = %+v", rs.MRs)
	}
	if rs.MRFor("web", "other") != nil {
		t.Error("a merge request of another branch")
	}
	if !rs.KnownHead(event.PrimaryCheckoutName, "h1") || rs.KnownHead("web", "h1") {
		t.Errorf("heads = %v", rs.Heads)
	}
}

func TestNoteHeadStartsItsSet(t *testing.T) {
	rs := &State{}
	rs.NoteHead("repo", "")
	if rs.Heads != nil {
		t.Errorf("an empty sha was noted: %v", rs.Heads)
	}
	rs.NoteHead("repo", "h1")
	if !rs.KnownHead("repo", "h1") {
		t.Error("the head was not noted")
	}
}

func TestPhaseEverRanIsNotFooledByAPrefix(t *testing.T) {
	rs := NewState(nil)
	rs.PhaseRuns = map[string]int{PhaseRunKey("review_fixes", "repo"): 1, PhaseRunKey("zero", "repo"): 0}
	if !rs.PhaseEverRan("review_fixes") {
		t.Error("it ran")
	}
	if rs.PhaseEverRan("review") {
		t.Error("another phase's run counted")
	}
	if rs.PhaseEverRan("zero") {
		t.Error("a count of none counted")
	}
}

// A flow-less replay has every workspace stage behind it: the check is for a
// fresh run on a flow.
func TestAnyWorkspaceStageStartedWithoutAFlow(t *testing.T) {
	if !NewState(nil).AnyWorkspaceStageStarted() {
		t.Error("a state with no flow owes a model check")
	}
}
