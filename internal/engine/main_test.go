package engine

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/event"
	"apothecary/internal/flow"
)

// The fold invariant: at every point the run loop settles (the top of an
// iteration, after an idle tick, on exit) its live runState must equal the
// fold of its own log. Every scenario test checks it — TestMain installs the
// hook, the harness fails a test whose runs broke it.

func TestMain(m *testing.M) {
	foldCheck = checkFoldInvariant
	os.Exit(m.Run())
}

// foldAllow lists the runState fields the live loop may disagree on with
// the fold: none. view is never compared (run.Apply, inside the fold, keeps
// it).
var foldAllow = map[string]bool{}

var foldViolations = struct {
	mu   sync.Mutex
	runs map[string][]string
}{runs: map[string][]string{}}

func checkFoldInvariant(x *exec) {
	if x.rs.fl == nil || x.rs.runID == "" {
		return
	}
	evs, err := x.e.store.Events(context.Background(), x.rs.runID, 0)
	if err != nil || len(evs) == 0 {
		return
	}
	rep := replay(evs, x.rs.fl)
	if rep.ph == phAfterComplete && x.rs.ph != phAfterComplete && rep.pos >= 0 && rep.pos < len(rep.fl.Stages) {
		// The moves the next event records, not this one. One: a completed
		// stage's on_success.
		routeSuccess(rep, &rep.fl.Stages[rep.pos], rep.completedFeed)
	}
	if rep.ph == phDebug && x.rs.ph == phStart && x.rs.pos == rep.pos {
		// The third: continuing from the debug leaf (routeDebug), recorded
		// by the stage's next stage_started.
		rep.ph, rep.gateAnswer = phStart, nil
		rep.feed, rep.feedSentinel, rep.feedAttempts = "", "", 0
	}
	if x.rs.pos >= len(rep.fl.Stages) && !rep.idle && rep.pos >= 0 && rep.pos < len(rep.fl.Stages) {
		// Another: a phase whose end was decided (endPhase — a watcher's
		// dead end, an empty round) and is recorded by the next step's
		// phase_completed, which carries the reason.
		rep.pos, rep.ph = len(rep.fl.Stages), phStart
		rep.endsRun, rep.abortsRun, rep.endReason = false, false, x.rs.endReason
		rep.gateAnswer = nil // a debug leaf's rejection ends the round
	}
	diffs := diffState(x.rs, rep)
	if len(diffs) == 0 {
		return
	}
	foldViolations.mu.Lock()
	defer foldViolations.mu.Unlock()
	if len(foldViolations.runs[x.rs.runID]) < 20 {
		foldViolations.runs[x.rs.runID] = append(foldViolations.runs[x.rs.runID], diffs...)
	}
}

// routeSuccess is advanceWith on a replayed state.
func routeSuccess(rs *runState, st *flow.Stage, verdict string) {
	switch st.OnSuccess {
	case flow.TerminalEnd, flow.TerminalDone, flow.TerminalAbort:
		rs.pos = len(rs.fl.Stages)
		rs.endsRun = st.OnSuccess == flow.TerminalDone
		rs.abortsRun = st.OnSuccess == flow.TerminalAbort
		if rs.abortsRun {
			rs.abortAt, rs.abortReason = st.ID, "aborted by flow: on_success at stage "+st.ID
		}
	default:
		rs.pos = rs.fl.Index(st.OnSuccess)
	}
	rs.ph = phStart
	rs.feed, rs.feedSentinel, rs.feedAttempts = verdict, "", 0
	if verdict != "" {
		rs.feedSentinel = flow.SourceVerdict
	}
}

// checkFoldAfter fails t if any run in store broke the fold invariant on a
// field foldAllow does not list.
func checkFoldAfter(t *testing.T, store *event.MemStore) {
	ids, _ := store.RunIDs(context.Background())
	foldViolations.mu.Lock()
	defer foldViolations.mu.Unlock()
	for _, id := range ids {
		var bad []string
		for _, d := range foldViolations.runs[id] {
			if !foldAllow[strings.SplitN(d, ":", 2)[0]] {
				bad = append(bad, d)
			}
		}
		delete(foldViolations.runs, id)
		if len(bad) > 0 {
			t.Errorf("run %s: live state differs from the fold of its log:\n  %s", id, strings.Join(bad, "\n  "))
		}
	}
}

// diffState compares every runState field but view, by name. Nil and empty
// maps and slices are equal.
func diffState(live, rep *runState) []string {
	fields := []struct {
		name string
		a, b any
	}{
		{"runID", live.runID, rep.runID},
		{"branch", live.branch, rep.branch},
		{"sourceBranch", live.sourceBranch, rep.sourceBranch},
		{"params", live.params, rep.params},
		{"skillsets", live.skillsets, rep.skillsets},
		{"envID", live.envID, rep.envID},
		{"envRequested", live.envRequested, rep.envRequested},
		{"pos", live.pos, rep.pos},
		{"ph", live.ph, rep.ph},
		{"endsRun", live.endsRun, rep.endsRun},
		{"abortsRun", live.abortsRun, rep.abortsRun},
		{"abortAt", live.abortAt, rep.abortAt},
		{"abortReason", live.abortReason, rep.abortReason},
		{"endReason", live.endReason, rep.endReason},
		{"iters", live.iters, rep.iters},
		{"edgeCounts", live.edgeCounts, rep.edgeCounts},
		{"actionPending", live.actionPending, rep.actionPending},
		{"sessions", live.sessions, rep.sessions},
		{"links", live.links, rep.links},
		{"cost", live.cost, rep.cost},
		{"turns", live.turns, rep.turns},
		{"unwanted", live.unwanted, rep.unwanted},
		{"feed", live.feed, rep.feed},
		{"feedSentinel", live.feedSentinel, rep.feedSentinel},
		{"feedAttempts", live.feedAttempts, rep.feedAttempts},
		{"completedFeed", live.completedFeed, rep.completedFeed},
		{"verdictRecorded", live.verdictRecorded, rep.verdictRecorded},
		{"ci", live.ci, rep.ci},
		{"lastFailure", live.lastFailure, rep.lastFailure},
		{"checkouts", live.checkouts, rep.checkouts},
		{"mrs", live.mrs, rep.mrs},
		{"phase", live.phase, rep.phase},
		{"subject", live.subject, rep.subject},
		{"idle", live.idle, rep.idle},
		{"phaseTip", live.phaseTip, rep.phaseTip},
		{"phaseHead", live.phaseHead, rep.phaseHead},
		{"triggerSeen", live.triggerSeen, rep.triggerSeen},
		{"phaseRuns", live.phaseRuns, rep.phaseRuns},
		{"confirms", live.confirms, rep.confirms},
		{"round", live.round, rep.round},
		{"roundScope", live.roundScope, rep.roundScope},
		{"roundLoaded", live.roundLoaded, rep.roundLoaded},
		{"selectPing", live.selectPing, rep.selectPing},
		{"skipped", live.skipped, rep.skipped},
		{"dropped", live.dropped, rep.dropped},
		{"curated", live.curated, rep.curated},
		{"curatedSeen", live.curatedSeen, rep.curatedSeen},
		{"pushed", live.pushed, rep.pushed},
		{"adopted", live.adopted, rep.adopted},
		{"heads", live.heads, rep.heads},
		{"parked", live.parked, rep.parked},
		{"parkSince", live.parkSince, rep.parkSince},
		{"anyStarted", live.anyStarted, rep.anyStarted},
		{"elided", live.elided, rep.elided},
		{"openPings", live.openPings, rep.openPings},
		{"gateAnswer", live.gateAnswer, rep.gateAnswer},
		{"raisedPings", live.raisedPings, rep.raisedPings},
		{"answeredPings", live.answeredPings, rep.answeredPings},
		{"pausedAsks", live.pausedAsks, rep.pausedAsks},
		{"agentCursor", live.agentCursor, rep.agentCursor},
		{"pendingMsgs", live.pendingMsgs, rep.pendingMsgs},
		{"holds", live.holds, rep.holds},
		{"pausedHold", live.pausedHold, rep.pausedHold},
		{"handbackOwed", live.handbackOwed, rep.handbackOwed},
		{"done", live.done, rep.done},
	}
	var out []string
	for _, f := range fields {
		if !sameValue(f.a, f.b) {
			out = append(out, fmt.Sprintf("%s: live %s, fold %s", f.name, show(f.a), show(f.b)))
		}
	}
	return out
}

func sameValue(a, b any) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if (va.Kind() == reflect.Map || va.Kind() == reflect.Slice) && va.Len() == 0 && vb.Len() == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func show(v any) string {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Map {
		keys := make([]string, 0, rv.Len())
		for _, k := range rv.MapKeys() {
			keys = append(keys, fmt.Sprintf("%v=%s", k, show(rv.MapIndex(k).Interface())))
		}
		sort.Strings(keys)
		return "{" + strings.Join(keys, " ") + "}"
	}
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		return fmt.Sprintf("&%+v", rv.Elem().Interface())
	}
	s := fmt.Sprintf("%+v", v)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
