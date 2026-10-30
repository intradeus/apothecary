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
	"apothecary/internal/runcore"
)

// The fold invariant: at every point the run loop settles (the top of an
// iteration, after an idle tick, on exit) its live runcore.State must equal the
// fold of its own log. Every scenario test checks it — TestMain installs the
// hook, the harness fails a test whose runs broke it.

func TestMain(m *testing.M) {
	foldCheck = checkFoldInvariant
	os.Exit(m.Run())
}

// foldAllow lists the runcore.State fields the live loop may disagree on with
// the fold: none. view is never compared (run.Apply, inside the fold, keeps
// it).
var foldAllow = map[string]bool{}

var foldViolations = struct {
	mu   sync.Mutex
	runs map[string][]string
}{runs: map[string][]string{}}

func checkFoldInvariant(x *exec) {
	if x.rs.Flow == nil || x.rs.RunID == "" {
		return
	}
	evs, err := x.e.store.Events(context.Background(), x.rs.RunID, 0)
	if err != nil || len(evs) == 0 {
		return
	}
	rep := runcore.Replay(evs, x.rs.Flow)
	if rep.Ph == runcore.PhAfterComplete && x.rs.Ph != runcore.PhAfterComplete && rep.Pos >= 0 && rep.Pos < len(rep.Flow.Stages) {
		// The moves the next event records, not this one. One: a completed
		// stage's on_success.
		rep.RouteSuccess(&rep.Flow.Stages[rep.Pos], rep.CompletedFeed)
	}
	if rep.Ph == runcore.PhDebug && x.rs.Ph == runcore.PhStart && x.rs.Pos == rep.Pos {
		// The third: continuing from the debug leaf (routeDebug), recorded
		// by the stage's next stage_started.
		rep.Ph, rep.GateAnswer = runcore.PhStart, nil
		rep.Feed, rep.FeedSentinel, rep.FeedAttempts = "", "", 0
	}
	if x.rs.Pos >= len(rep.Flow.Stages) && !rep.Idle && rep.Pos >= 0 && rep.Pos < len(rep.Flow.Stages) {
		// Another: a phase whose end was decided (EndPhase — a watcher's
		// dead end, an empty round) and is recorded by the next step's
		// phase_completed, which carries the reason.
		rep.EndPhase()
		rep.EndReason = x.rs.EndReason
		rep.GateAnswer = nil // a debug leaf's rejection ends the round
	}
	diffs := diffState(x.rs, rep)
	if len(diffs) == 0 {
		return
	}
	foldViolations.mu.Lock()
	defer foldViolations.mu.Unlock()
	if len(foldViolations.runs[x.rs.RunID]) < 20 {
		foldViolations.runs[x.rs.RunID] = append(foldViolations.runs[x.rs.RunID], diffs...)
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

// diffState compares every runcore.State field but view, by name. Nil and empty
// maps and slices are equal.
func diffState(live, rep *runcore.State) []string {
	fields := []struct {
		name string
		a, b any
	}{
		{"runID", live.RunID, rep.RunID},
		{"branch", live.Branch, rep.Branch},
		{"sourceBranch", live.SourceBranch, rep.SourceBranch},
		{"params", live.Params, rep.Params},
		{"skillsets", live.Skillsets, rep.Skillsets},
		{"envID", live.EnvID, rep.EnvID},
		{"envRequested", live.EnvRequested, rep.EnvRequested},
		{"pos", live.Pos, rep.Pos},
		{"ph", live.Ph, rep.Ph},
		{"endsRun", live.EndsRun, rep.EndsRun},
		{"abortsRun", live.AbortsRun, rep.AbortsRun},
		{"abortAt", live.AbortAt, rep.AbortAt},
		{"abortReason", live.AbortReason, rep.AbortReason},
		{"endReason", live.EndReason, rep.EndReason},
		{"iters", live.Iters, rep.Iters},
		{"edgeCounts", live.EdgeCounts, rep.EdgeCounts},
		{"actionPending", live.ActionPending, rep.ActionPending},
		{"sessions", live.Sessions, rep.Sessions},
		{"links", live.Links, rep.Links},
		{"cost", live.Cost, rep.Cost},
		{"turns", live.Turns, rep.Turns},
		{"unwanted", live.Unwanted, rep.Unwanted},
		{"feed", live.Feed, rep.Feed},
		{"feedSentinel", live.FeedSentinel, rep.FeedSentinel},
		{"feedAttempts", live.FeedAttempts, rep.FeedAttempts},
		{"completedFeed", live.CompletedFeed, rep.CompletedFeed},
		{"verdictRecorded", live.VerdictRecorded, rep.VerdictRecorded},
		{"ci", live.CI, rep.CI},
		{"lastFailure", live.LastFailure, rep.LastFailure},
		{"checkouts", live.Checkouts, rep.Checkouts},
		{"mrs", live.MRs, rep.MRs},
		{"phase", live.Phase, rep.Phase},
		{"subject", live.Subject, rep.Subject},
		{"idle", live.Idle, rep.Idle},
		{"phaseTip", live.PhaseTip, rep.PhaseTip},
		{"phaseHead", live.PhaseHead, rep.PhaseHead},
		{"triggerSeen", live.TriggerSeen, rep.TriggerSeen},
		{"phaseRuns", live.PhaseRuns, rep.PhaseRuns},
		{"confirms", live.Confirms, rep.Confirms},
		{"round", live.Round, rep.Round},
		{"roundScope", live.RoundScope, rep.RoundScope},
		{"roundLoaded", live.RoundLoaded, rep.RoundLoaded},
		{"selectPing", live.SelectPing, rep.SelectPing},
		{"skipped", live.Skipped, rep.Skipped},
		{"dropped", live.Dropped, rep.Dropped},
		{"curated", live.Curated, rep.Curated},
		{"curatedSeen", live.CuratedSeen, rep.CuratedSeen},
		{"pushed", live.Pushed, rep.Pushed},
		{"adopted", live.Adopted, rep.Adopted},
		{"heads", live.Heads, rep.Heads},
		{"parked", live.Parked, rep.Parked},
		{"parkSince", live.ParkSince, rep.ParkSince},
		{"anyStarted", live.AnyStarted, rep.AnyStarted},
		{"elided", live.Elided, rep.Elided},
		{"openPings", live.OpenPings, rep.OpenPings},
		{"gateAnswer", live.GateAnswer, rep.GateAnswer},
		{"raisedPings", live.RaisedPings, rep.RaisedPings},
		{"answeredPings", live.AnsweredPings, rep.AnsweredPings},
		{"pausedAsks", live.PausedAsks, rep.PausedAsks},
		{"agentCursor", live.AgentCursor, rep.AgentCursor},
		{"pendingMsgs", live.PendingMsgs, rep.PendingMsgs},
		{"holds", live.Holds, rep.Holds},
		{"pausedHold", live.PausedHold, rep.PausedHold},
		{"handbackOwed", live.HandbackOwed, rep.HandbackOwed},
		{"done", live.Done, rep.Done},
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
