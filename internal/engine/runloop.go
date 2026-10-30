package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"apothecary/internal/actionspec"
	"apothecary/internal/agentspec"
	"apothecary/internal/bootstrap"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/redact"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/shell"
	"apothecary/internal/signalspec"
	"apothecary/internal/wsname"
)

var errStageTimeout = errors.New("stage timeout")

// recoveryReprobeDelay is how long ensureEnv waits before re-probing a
// non-running env on recovery, so a container mid-restart isn't misread as
// gone and needlessly re-provisioned. A var so tests can compress it.
var recoveryReprobeDelay = 2 * time.Second

// resumeReply is what Resume sends when re-entering a crashed agent stage
// with a previously recorded session (§2 crash recovery).
const resumeReply = "The apothecary engine restarted while this stage was in flight. " +
	"Continue the task from where you left off: re-check the repository state and make sure you produce the required outputs."

// handbackPrompt is what the session is sent when the holder of a take-over
// releases it: the one turn that stands between the hold and the stage's
// output check. Fixed on purpose — the holder's words were the turns before.
const handbackPrompt = "The operator has handed the task back to you. " +
	"Continue the task this stage gave you, taking into account everything the operator said and changed while they held the session. " +
	"Re-check the repository state and make sure you produce the required outputs."

// exec bundles the per-run goroutine's context so stage executors stay short.
// bg survives cancellation for terminal event appends. res is the project's
// resolved configuration (DESIGN2 §6.1), held for the run's whole life —
// config edits apply to new runs only.
type exec struct {
	e   *Engine
	rs  *runState
	h   *runHandle
	res *resolution.Resolution
	ctx context.Context
	bg  context.Context
	// envEnsured records that ensureEnv ran (and succeeded) once this loop
	// invocation, so it is done lazily before the first stage that needs the
	// workspace — never for a human_gate.
	envEnsured bool
	// followed is the head the adopted merge request's checkout was last
	// brought to this loop (followAdopted: once per head).
	followed string
	// materialised are the curated documents written back this loop, by
	// path: each is written once, before the first stage that reads it.
	materialised map[string]*event.ReviewCurated
	// lastObs is the idle loop's latest poll of the run's merge requests:
	// what a hand start picks its subject from. Nil before the first poll.
	lastObs []mrObservation
	// redact scrubs engine-injected secrets out of everything that leaves the
	// workspace (PLAN5 M21). Built once per loop from the resolution.
	redact *redact.Redactor
}

func (e *Engine) runLoop(ctx context.Context, runID string, h *runHandle) {
	defer e.finish(runID, h)
	bg := context.WithoutCancel(ctx)

	evs, err := e.store.Events(bg, runID, 0)
	if err != nil || len(evs) == 0 {
		e.log.Error("run loop: loading events", "run", runID, "err", err)
		return
	}
	view := run.Reduce(evs)
	x := &exec{e: e, h: h, ctx: ctx, bg: bg}
	// Whichever way the loop ends, a run that ended for good gives up its
	// credentials — one place, so completion, abandonment, failure, abort and
	// a failed env are all covered (see secrets.go). bg, not ctx: an abort has
	// already cancelled ctx by the time we get here.
	defer func() {
		if x.rs != nil && x.rs.view.State.Terminal() {
			e.ScrubWorkspaceSecrets(bg, x.rs.view)
			e.stopRunServices(bg, x.rs)
		}
	}()

	// Resolve the project's configuration. Unresolvable (project deleted,
	// pre-v2 run without one) is terminal and honest; anything else is
	// treated as transient — the run stays non-terminal for the next boot.
	if view.State.Terminal() {
		x.rs = replay(evs, nil)
		return
	}
	res, err := e.resolveForRun(bg, view)
	if err != nil {
		x.rs = &runState{runID: runID, view: view}
		if errors.Is(err, resolution.ErrUnresolvable) {
			_ = x.append(&event.RunFailed{Reason: err.Error()})
		} else {
			e.log.Error("run loop: resolution failed; run left non-terminal for recovery", "run", runID, "err", err)
		}
		return
	}
	defer res.Close()
	x.res = res
	x.redact = redact.ForResolution(res, e.RunToken(runID), e.authSink.BrokerToken(runID))
	// A run that stops (for good, or for a restart) holds no workspace a
	// refreshed login should be pushed into; the next loop re-registers.
	defer e.authSink.Drop(runID)

	fl, err := e.RunFlow(bg, view)
	if err != nil {
		x.rs = &runState{runID: runID, view: view}
		var perr *flow.Error
		switch {
		case errors.Is(err, flow.ErrNotFound):
			_ = x.append(&event.RunFailed{Reason: fmt.Sprintf("flow %q not found", view.FlowName)})
		case errors.As(err, &perr):
			// The stored flow no longer parses (edited into invalid YAML, or
			// written against an older grammar). Retrying can never fix that,
			// and leaving the run non-terminal spins it through recovery on
			// every boot: fail honestly with the parse error a human can act on.
			_ = x.append(&event.RunFailed{Reason: fmt.Sprintf("flow %q no longer parses, or no longer resolves with this run's parameters: %v", view.FlowName, err)})
		default:
			// Transient source error (DB blip): leave the run non-terminal
			// so the next boot recovers it.
			e.log.Error("run loop: flow load failed; run left non-terminal for recovery", "run", runID, "err", err)
		}
		return
	}
	if fl.Hash != view.FlowHash {
		// Only reachable when the pinned version is gone from the store
		// (pruned, or a pre-pinning run): pinnedFlow already logged it.
		e.log.Warn("running a flow version other than the one pinned at run creation",
			"run", runID, "flow", fl.Name, "pinned", view.FlowHash, "using", fl.Hash)
	}
	rs := replay(evs, fl)
	x.rs = rs
	if rs.done {
		return
	}
	if rs.pos < 0 {
		// The recorded current stage no longer exists in the (drifted) flow
		// document. Failing honestly beats reporting a half-finished run
		// completed, which is what falling through would do.
		x.rs = rs
		_ = x.fail(fmt.Sprintf("flow %q changed: the run's current stage no longer exists in it", fl.Name))
		return
	}

	e.mu.Lock()
	e.tokens[runID] = e.newID()
	// Re-register open pings so answers can still be delivered after a restart.
	for _, op := range rs.openPings {
		if _, ok := e.pings[op.PingID]; !ok {
			e.pings[op.PingID] = &Ping{
				ID: op.PingID, RunID: runID, Type: op.Type, Question: op.Question,
				Context: op.Context, Files: op.Files, Link: op.Link, StageID: op.StageID,
				Form: op.Form, AnswerKind: op.Kind(), key: op.Key,
				Phase: op.Phase, EndsPhase: op.EndsPhase,
				RaisedAt: e.clock(), done: make(chan struct{}),
			}
		}
	}
	e.mu.Unlock()

	if err := x.beginRun(); err != nil {
		x.exit(err)
		return
	}
	for !rs.done {
		x.checkFold()
		if cause := context.Cause(ctx); cause != nil {
			x.exit(cause)
			return
		}
		if rs.idle {
			// Between phases (PLAN6): watch the merge requests until a
			// phase starts or the run ends.
			if err := x.idle(); err != nil {
				x.exit(err)
				return
			}
			continue
		}
		if rs.pos < 0 {
			_ = x.fail(fmt.Sprintf("flow %q changed: the run's current phase no longer exists in it", fl.Name))
			return
		}
		if rs.pos >= len(fl.Stages) {
			// The chain is over. After `done` the run completes, whatever
			// else is parked or armed; otherwise only the phase ends, and the
			// idle loop decides what follows — a parked phase resuming,
			// another phase opening, or the run's end (endCheck).
			end := x.phaseDone
			switch {
			case rs.abortsRun:
				end = x.runAbort
			case rs.endsRun:
				end = x.runDone
			}
			// What its stages started ends with the chain.
			x.stopChainServices(rs.phase, rs.endsRun || rs.abortsRun)
			if err := end(); err != nil {
				x.exit(err)
				return
			}
			continue
		}
		st := &fl.Stages[rs.pos]
		var err error
		if rs.ph == phDebug {
			// Parked on the debug leaf: a human has the workspace. Like a
			// gate this never provisions; continuing re-enters st below.
			err = x.stageDebug(st)
		} else if rs.ph == phAfterComplete {
			err = x.routeAfterComplete(st)
		} else if rs.ph == phAfterFail && st.Type != flow.StageAction {
			err = x.routeAfterFail(st)
		} else if skip, note := x.stageCondition(st); skip && rs.ph == phStart {
			// A condition that does not hold: the stage completes as
			// skipped before anything is provisioned (FLOWLANG §2 when).
			err = x.skipStage(st, note)
		} else {
			// Ensure the workspace lazily, right before a stage that uses it —
			// and NEVER for a stage that only parks on something outside the
			// run (a gate or a wait) or acts there (a remote tracker action
			// with no file input). Ensuring it up front blocked a run parked
			// at a gate whose workspace had been reaped: recovery tried to
			// re-provision it before re-arming the gate, so answers were
			// silently buffered and dropped (found live: rejecting a final_gate
			// returned {"status":"answered"} but nothing happened).
			if needsWorkspace(st) && !x.envEnsured {
				if eerr := x.ensureEnv(); eerr != nil {
					x.exit(eerr)
					return
				}
				if rs.done { // env_failed is terminal
					return
				}
				x.envEnsured = true
				// A fresh run: before any stage spends anything, make sure the
				// agent in the workspace accepts every model the flow pins. A
				// gate, a wait or a remote action may have run already — none
				// of them had a workspace to check in.
				if !rs.anyWorkspaceStageStarted() {
					if missing := x.checkAgentModels(); len(missing) > 0 {
						_ = x.fail("the flow pins models the workspace's agent does not offer: " +
							strings.Join(missing, "; "))
						return
					}
				}
			}
			if needsWorkspace(st) {
				x.followAdopted()
				// What a person approved at a gate reaches the file before
				// the stage that reads it starts (the gate had no workspace).
				if merr := x.materialiseCurated(st); merr != nil {
					x.exit(merr)
					return
				}
			}
			switch st.Type {
			case flow.StageAgent:
				err = x.stageAgent(st)
			case flow.StageCommand:
				err = x.stageCommand(st)
			case flow.StageHumanGate:
				err = x.stageGate(st)
			case flow.StageAction:
				err = x.stageAction(st)
			case flow.StageWait:
				err = x.stageWait(st)
			case flow.StageService:
				err = x.stageService(st)
			}
		}
		if err != nil {
			x.exit(err)
			return
		}
	}
}

// exit handles abnormal loop termination: abort marks the run failed,
// pause parks it (non-terminal, skipped by Recover until play), shutdown
// leaves it for Recover, anything else is logged and left non-terminal
// (recovered by replay on next boot).
func (x *exec) exit(cause error) {
	defer x.checkFold()
	switch {
	case errors.Is(cause, errAborted):
		// A deliberate stop is abandonment, not failure (PLAN5 M24) — the
		// same terminal the flow's own `abort` edge reaches. A take-over in
		// force ends with it, on the record.
		for id, h := range x.rs.holds {
			_ = x.append(&event.HoldReleased{StageID: id, Iteration: x.rs.iters[id], UserID: h.taken.UserID,
				Reason: event.HoldEndedWithStage, DurationMS: max(x.e.clock().Sub(h.takenAt).Milliseconds(), 0), HoldSpend: h.banked})
		}
		_ = x.append(&event.RunAbandoned{StalledAt: x.rs.view.CurrentStage, Reason: "aborted by user"})
	case errors.Is(cause, errPaused):
		_ = x.append(&event.RunPaused{StageID: x.rs.view.CurrentStage, Reason: event.PauseUser})
	case errors.Is(cause, errWaitingOnHuman):
		// Parked on a human, not stopped by one: the run keeps reading
		// waiting_on_human, gives its concurrency slot back, and comes alive
		// again when the answer lands (Engine.AnswerPing).
		_ = x.append(&event.RunPaused{StageID: x.rs.view.CurrentStage, Reason: event.PauseWaiting})
	case errors.Is(cause, errShutdown), cause == nil:
	default:
		x.e.log.Error("run loop error; run left non-terminal for recovery",
			"run", x.rs.runID, "err", cause)
	}
}

// foldCheck, when set (tests only), is handed the run at each point where
// its live state must equal the fold of its log: the top of every loop
// iteration, after each idle tick, and on exit.
var foldCheck func(x *exec)

func (x *exec) checkFold() {
	if foldCheck != nil && x.rs != nil {
		foldCheck(x)
	}
}

// append writes events through the single-writer goroutine, folds each into
// the run's state — the same apply replay uses, so the live state is the
// fold of the log — and feeds the telemetry sink (§7).
//
// Every payload passes through the redactor first: this is the one place
// workspace output becomes durable (and reaches prompts, gates and the API),
// so scrubbing here covers every producer at once rather than trusting each
// call site to remember (PLAN5 M21).
func (x *exec) append(ps ...event.Payload) error {
	for _, p := range ps {
		switch v := p.(type) {
		case *event.StageFailed:
			v.Error = x.redact.Scrub(v.Error)
		case *event.EdgeTaken:
			v.Feed = x.redact.Scrub(v.Feed)
		case *event.EnvFailed:
			v.Error = x.redact.Scrub(v.Error)
		case *event.ServiceFailed:
			// A health command's output rides its reason.
			v.Reason = x.redact.Scrub(v.Reason)
		case *event.RunAbandoned:
			v.Reason = x.redact.Scrub(v.Reason)
		case *event.RunFailed:
			v.Reason = x.redact.Scrub(v.Reason)
		case *event.PingRaised:
			// The debug leaf hands a human what the stage printed.
			v.Question = x.redact.Scrub(v.Question)
			v.Context = x.redact.Scrub(v.Context)
		// The transcript is workspace output verbatim — an agent that cats
		// .env or echoes a token puts it here first.
		case *event.AgentPrompted:
			v.Text = x.redact.Scrub(v.Text)
		case *event.AgentMessage:
			v.Text = x.redact.Scrub(v.Text)
		case *event.AgentTool:
			v.Title = x.redact.Scrub(v.Title)
			v.Input = x.redact.Scrub(v.Input)
			v.Output = x.redact.Scrub(v.Output)
			for i := range v.Diffs {
				v.Diffs[i].OldText = x.redact.Scrub(v.Diffs[i].OldText)
				v.Diffs[i].NewText = x.redact.Scrub(v.Diffs[i].NewText)
			}
		case *event.AgentPlan:
			for i := range v.Entries {
				v.Entries[i].Content = x.redact.Scrub(v.Entries[i].Content)
			}
		case *event.AgentNotice:
			v.Text = x.redact.Scrub(v.Text)
		case *event.MessageQueued:
			v.Text = x.redact.Scrub(v.Text)
		}
	}
	unlock := x.e.appendLock(x.rs.runID)
	evs, err := x.e.store.Append(x.bg, x.rs.runID, ps...)
	unlock()
	if err != nil {
		return fmt.Errorf("append: %w", err)
	}
	for i := range evs {
		x.rs.apply(&evs[i])
	}
	x.e.emit(evs)
	return nil
}

// advance follows the stage's explicit on_success target (DESIGN3 §2.1):
// end parks pos past the last stage, which the run loop reads as the
// phase's end; done does the same and marks the run done.
func (x *exec) advance(st *flow.Stage) { x.advanceWith(st, "") }

// advanceWith follows on_success carrying verdict — what an approval said
// (flow.GivesVerdict) — to the target's edge.verdict; "" carries nothing.
func (x *exec) advanceWith(st *flow.Stage, verdict string) {
	switch st.OnSuccess {
	case flow.TerminalEnd, flow.TerminalDone, flow.TerminalAbort:
		x.rs.pos = len(x.rs.fl.Stages)
		x.rs.endsRun = st.OnSuccess == flow.TerminalDone
		x.rs.abortsRun = st.OnSuccess == flow.TerminalAbort
		if x.rs.abortsRun {
			x.rs.abortAt, x.rs.abortReason = st.ID, "aborted by flow: on_success at stage "+st.ID
		}
	default:
		x.rs.pos = x.rs.fl.Index(st.OnSuccess) // parse-validated: always >= 0
	}
	x.rs.ph = phStart
	x.rs.feed, x.rs.feedSentinel, x.rs.feedAttempts = verdict, "", 0
	if verdict != "" {
		x.rs.feedSentinel = flow.SourceVerdict
	}
}

func (x *exec) complete() error {
	rs := x.rs
	return x.append(&event.RunCompleted{
		DurationMS:    x.e.clock().Sub(rs.view.CreatedAt).Milliseconds(),
		TotalCostUSD:  rs.cost,
		TotalTurns:    rs.turns,
		UnwantedPings: rs.unwanted,
	})
}

func (x *exec) abandon(stalledAt, edgeKind, reason string) error {
	return x.append(&event.RunAbandoned{StalledAt: stalledAt, Edge: edgeKind, Reason: reason})
}

func (x *exec) fail(reason string) error {
	return x.append(&event.RunFailed{Reason: reason})
}

// takeEdge follows a recoverable edge of the given kind (on_fail,
// on_reject, on_timeout — the registry lives in the flow package). The
// edge is a chain: each link carries its own cumulative max, and a full
// link falls through to its then fallback (retry-then-escalate). A link
// targeting end finishes the phase, done the run, abort abandons the run;
// an absent edge or an exhausted chain is a dead end (deadEnd: the run is
// abandoned in the main chain or an outcome, a watcher's round ends — §3).
func (x *exec) takeEdge(st *flow.Stage, kind, feedContent string) error {
	rs := x.rs
	// The feed becomes an abandon reason and rs.feed (which an agent reads as
	// edge.failure/edge.verdict), neither of which passes through append's
	// scrub. A failure keeps its tail, a verdict its head (capVerdict).
	feedContent = x.redact.Scrub(feedContent)
	feedTail := func(s string) string {
		if kind == flow.EdgeReject {
			return capVerdict(s)
		}
		return shell.Tail(s, 8<<10)
	}
	edge := st.Edge(kind)
	if edge == nil {
		// A rejection is a decision, not a breakage: saying "unrecoverable
		// failure" about a human (or a signal) deliberately saying no would
		// misreport the run's history.
		reason := fmt.Sprintf("stage %s: unrecoverable failure with no %s", st.ID, kind)
		if kind == flow.EdgeReject {
			reason = fmt.Sprintf("stage %s was rejected and declares no %s", st.ID, kind)
			if strings.TrimSpace(feedContent) != "" {
				reason += ": " + shell.Tail(strings.TrimSpace(feedContent), 2<<10)
			}
		}
		return x.deadEnd(st, "", reason)
	}
	depth := 0
	for e := edge; e != nil; e, depth = e.Then, depth+1 {
		if e.To == flow.TerminalEnd || e.To == flow.TerminalDone {
			// The phase ends here — a best-effort stage whose failure is not
			// worth routing anywhere — or, with done, the run (FLOWLANG §3).
			// Recorded like any link.
			if err := x.append(&event.EdgeTaken{From: st.ID, To: e.To, Edge: kind, Depth: depth,
				Feed: feedTail(feedContent)}); err != nil {
				return err
			}
			return nil
		}
		if e.To == flow.TerminalAbort {
			reason := fmt.Sprintf("aborted by flow: %s at stage %s", kind, st.ID)
			if strings.TrimSpace(feedContent) != "" {
				reason += ": " + shell.Tail(strings.TrimSpace(feedContent), 2<<10)
			}
			return x.abandon(st.ID, kind, reason)
		}
		key := chainKey(st.ID, kind, depth)
		count := rs.edgeCounts[key] + 1
		if e.To == flow.ReservedDebug {
			// The debug leaf: park here, on this stage, for a human with the
			// workspace (stageDebug). Counted like any link — the timeline
			// shows n/∞ — but never exhausted: the human decides when to stop.
			feed := feedTail(feedContent)
			if err := x.append(&event.EdgeTaken{From: st.ID, To: flow.ReservedDebug, Edge: kind, Count: count,
				Max: flow.UnlimitedMax, Depth: depth, Feed: feed}); err != nil {
				return err
			}
			return nil
		}
		if e.Max != flow.UnlimitedMax && count > e.Max {
			continue // this link is exhausted; fall through to then
		}
		// The content always rides along; whether the target renders it is the
		// target's decision (it binds edge.verdict or edge.failure, or it does not).
		// Persisted in the event so a crash between edge_taken and the next
		// stage_started loses nothing.
		feed := feedTail(feedContent)
		if err := x.append(&event.EdgeTaken{From: st.ID, To: e.To, Edge: kind, Count: count, Max: e.Max, Depth: depth, Feed: feed}); err != nil {
			return err
		}
		return nil
	}
	return x.deadEnd(st, kind, fmt.Sprintf("%s from %s exhausted (max %s)", kind, st.ID, chainMax(edge)))
}

// chainMax renders a chain's budget for the abandon reason: "5" for a
// single link, "2+3" for retry-then-escalate chains. (An unlimited final
// link never exhausts, so ∞ only appears in mixed diagnostics.)
func chainMax(e *flow.Edge) string {
	parts := []string{}
	for ; e != nil; e = e.Then {
		if e.Max == flow.UnlimitedMax {
			parts = append(parts, "∞")
		} else {
			parts = append(parts, fmt.Sprintf("%d", e.Max))
		}
	}
	return strings.Join(parts, "+")
}

// failEdgeKind picks the edge kind for a stage failure: timeouts route
// through on_timeout when the stage declares it, everything else (and
// timeouts without one) through on_fail.
func failEdgeKind(st *flow.Stage, reason string) string {
	if reason == event.ReasonTimeout && st.Edge(flow.EdgeTimeout) != nil {
		return flow.EdgeTimeout
	}
	return flow.EdgeFail
}

// checkBudget abandons the run when §3 budget ceilings are exceeded.
// Returns true when the run should stop.
func (x *exec) checkBudget(st *flow.Stage) (bool, error) {
	b := x.rs.fl.Budget
	if x.rs.cost > b.MaxCostUSD {
		return true, x.abandon(st.ID, "", fmt.Sprintf("budget exceeded: cost %.2f USD > max %.2f", x.rs.cost, b.MaxCostUSD))
	}
	if x.rs.turns > b.MaxTotalTurns {
		return true, x.abandon(st.ID, "", fmt.Sprintf("budget exceeded: %d turns > max %d", x.rs.turns, b.MaxTotalTurns))
	}
	return false, nil
}

// routeAfterFail handles recovery when the crash hit between a stage_failed
// and its edge decision: the failure (with its persisted output tail) is
// routed directly, without re-executing the stage. action stages instead
// re-enter their executor, which must be idempotent (create_mr via the
// recorded mr_created) — a half-finished side effect on a real service cannot
// be replayed away.
func (x *exec) routeAfterFail(st *flow.Stage) error {
	feed := ""
	kind := flow.EdgeFail
	if x.rs.lastFailure != nil {
		feed = x.rs.lastFailure.Error
		kind = failEdgeKind(st, x.rs.lastFailure.Reason)
	}
	return x.takeEdge(st, kind, feed)
}

// routeAfterComplete handles recovery when the crash hit between a
// stage_completed and its routing: no re-execution, only the decision.
func (x *exec) routeAfterComplete(st *flow.Stage) error {
	if x.emptyRound(st) {
		x.endPhase()
		return nil
	}
	var v *Verdict
	if st.Type == flow.StageAgent && x.verdictPath(st) != "" && !x.rs.elided[st.ID] {
		var err error
		if v, err = x.readVerdict(st); err != nil {
			if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputContract, Error: err.Error()}); err != nil {
				return err
			}
			return x.takeEdge(st, flow.EdgeFail, "")
		}
	}
	// The same routing as the live completion; the verdict an approval
	// carries was replayed from its stage_completed.
	return x.afterComplete(st, v)
}

// --- agent stages -----------------------------------------------------------

type agentRes struct {
	r   agentport.AgentResult
	err error
}

// agentOutcome is how one harness invocation of an attempt ended.
type agentOutcome int

const (
	outcomeReturned agentOutcome = iota // the harness finished its turn
	outcomePaused                       // ...leaving its own questions unanswered
	outcomeTimedOut
)

// errParked cancels an invocation whose agent has been waiting on a human
// for longer than the live hold (Config.AskHumanHold).
var errParked = errors.New("parked: waiting on a human")

// invocation is one harness call inside a stage attempt: what it returned, how
// it ended, the questions it left open, and what survives of the attempt's
// timeout budget (suspended while a ping is pending, so it carries forward).
type invocation struct {
	res     agentRes
	outcome agentOutcome
	open    []*Ping
	left    time.Duration
	// holdPark: a pause on the holder of a take-over (they went quiet past
	// the live hold), not on the agent's own questions.
	holdPark bool
	// holdSpend is what the take-over in force spent during this invocation.
	holdSpend event.HoldSpend
}

// liveHold is the engine's in-flight view of a take-over during one
// invocation: the turns the holder's messages prompted and what they spent,
// whether the agent sits idle waiting on the holder (the stage clock is
// suspended then, as for a pending question), and a release waiting for the
// turn in flight to end.
type liveHold struct {
	st        *heldState
	spend     event.HoldSpend
	heldTurns map[string]bool
	idle      bool
	releasing bool
	releaseBy string
}

func (x *exec) liveHold(st *flow.Stage) *liveHold {
	h := x.rs.holds[st.ID]
	if h == nil {
		return nil
	}
	return &liveHold{st: h, heldTurns: map[string]bool{}}
}

// holdReq is a take-over request on its way into the run loop: take, or
// release (the handback follows).
type holdReq struct {
	take   bool
	userID string
	ack    chan error
}

// takeHold records a take-over of the stage's live session.
func (x *exec) takeHold(st *flow.Stage, iter int, userID string) error {
	taken := &event.HoldTaken{StageID: st.ID, Iteration: iter, UserID: userID}
	return x.append(taken)
}

// releaseHold closes a take-over with its account: every leg's spend, the
// time since it was taken, and why it ended. A release by the holder owes
// the session the handback prompt.
func (x *exec) releaseHold(st *flow.Stage, iter int, h *heldState, by, reason string, leg event.HoldSpend) error {
	total := h.banked
	total.Add(leg)
	if by == "" && h.taken != nil {
		by = h.taken.UserID
	}
	rel := &event.HoldReleased{StageID: st.ID, Iteration: iter, UserID: by, Reason: reason,
		DurationMS: max(x.e.clock().Sub(h.takenAt).Milliseconds(), 0), HoldSpend: total}
	return x.append(rel)
}

// endHold closes a take-over the attempt ended underneath: the harness
// returned, died or timed out while someone held it.
func (x *exec) endHold(st *flow.Stage, iter int, leg event.HoldSpend) error {
	h := x.rs.holds[st.ID]
	if h == nil {
		return nil
	}
	return x.releaseHold(st, iter, h, "", event.HoldEndedWithStage, leg)
}

// holdReentry is what a session under a take-over is sent when the loop
// (re-)enters its stage.
type holdReentry struct {
	reply, kind, reason string
	// park: nothing to send — the attempt parks on its holder.
	park bool
}

// holdReentry decides what a session under a take-over (or owed its
// handback) gets next: the handback its release owes, the holder's pending
// messages, or nothing yet — then the attempt parks on the holder, unless a
// turn from before a restart is still live to be followed (mayReattach).
func (x *exec) holdReentry(st *flow.Stage, iter int, mayReattach bool, runner agentport.AgentRunner, session agentport.SessionID) (holdReentry, error) {
	rs := x.rs
	switch {
	case rs.handbackOwed[st.ID]:
		return holdReentry{reply: handbackPrompt, kind: event.PromptHandback, reason: event.ResumeReleased}, nil
	case rs.holds[st.ID] == nil:
		return holdReentry{}, nil
	case len(rs.pendingMsgs) > 0:
		n := len(rs.pendingMsgs)
		text, err := x.withPendingMessages(st, iter, "")
		if err != nil {
			return holdReentry{}, err
		}
		rs.holds[st.ID].banked.Messages += n // delivered on the hold's account
		return holdReentry{reply: strings.TrimPrefix(text, "\n\n---\n\n"), kind: event.PromptMessages, reason: event.ResumeMessage}, nil
	case rs.pausedHold[st.ID]:
		return holdReentry{park: true}, nil
	case mayReattach:
		if ra, ok := runner.(agentport.AgentReattacher); ok && ra.Reattachable(x.ctx, rs.envID, session, rs.agentCursor[st.ID]) {
			return holdReentry{}, nil // the crashed path follows the live turn
		}
		return holdReentry{park: true}, nil
	default:
		return holdReentry{park: true}, nil
	}
}

// parkOnHold parks an attempt whose holder has nothing queued for it when
// no invocation is there to do the parking (a restart, a play, a loop
// re-entry). The hold stays in force.
func (x *exec) parkOnHold(st *flow.Stage, iter int, session agentport.SessionID) error {
	if !x.rs.pausedHold[st.ID] {
		if err := x.append(&event.StagePaused{StageID: st.ID, Iteration: iter, Reason: event.PauseHeld,
			SessionID: string(session)}); err != nil {
			return err
		}
	}
	return errWaitingOnHuman
}

// invIO is what flows between a running invocation and the loop: the
// agent's transcript events, the human's messages, and the transcript they
// are recorded into.
type invIO struct {
	events chan agentport.AgentEvent
	done   chan struct{} // closed when the loop stops listening
	inbox  *inbox
	tr     *transcript
}

func newInvIO(tr *transcript) *invIO {
	return &invIO{events: make(chan agentport.AgentEvent, 256), done: make(chan struct{}), inbox: newInbox(), tr: tr}
}

func (io *invIO) input(prompt string) agentport.AgentInput {
	return agentport.AgentInput{
		Prompt: prompt,
		Inbox:  io.inbox,
		// Blocks while the loop is listening (ordering: everything the
		// runner emitted is queued before its result is), never after.
		Events: func(ev agentport.AgentEvent) {
			select {
			case io.events <- ev:
			case <-io.done:
			}
		},
	}
}

// drain records whatever events are already queued, through record.
func (io *invIO) drain(record func(agentport.AgentEvent) (bool, error)) error {
	for {
		select {
		case ev := <-io.events:
			if _, err := record(ev); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// invoke runs one harness invocation to its end, servicing pings, messages
// and the transcript while it runs.
func (x *exec) invoke(st *flow.Stage, budget time.Duration, tr *transcript, initialOpen []*Ping, prompt string,
	call func(ctx context.Context, in agentport.AgentInput) (agentport.AgentResult, error)) (invocation, error) {
	runCtx, cancelRun := context.WithCancelCause(x.ctx)
	io := newInvIO(tr)
	resCh := make(chan agentRes, 1)
	in := io.input(prompt)
	go func() {
		r, err := call(runCtx, in)
		resCh <- agentRes{r, err}
	}()
	x.h.agentLive.Store(true)
	inv, err := x.awaitAgent(st, resCh, cancelRun, budget, io, initialOpen)
	x.h.agentLive.Store(false)
	cancelRun(nil)
	close(io.done)
	// Messages the agent never took stay pending (they are queued in the log
	// and in rs.pendingMsgs) and ride into the next prompt.
	if ferr := tr.flushAll(); ferr != nil && err == nil {
		err = ferr
	}
	return inv, err
}

func (x *exec) stageAgent(st *flow.Stage) error {
	rs := x.rs
	reenter := rs.ph == phIn
	iter := rs.iters[st.ID]
	if !reenter {
		iter++
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter, Harness: st.Harness, Model: st.Model,
			Agent: st.Agent, AgentHash: x.rs.fl.AgentHash(st.Agent)}); err != nil {
			return err
		}
		// What the agent reads with a schema is a precondition, like a
		// command's inputs: an agent handed a malformed file would build on
		// it. Upstream wiring, not this stage's work. Checked before the
		// outputs are cleared, as a command's are — inputs must survive.
		if reason, msg := x.checkStructuredInputs(st); reason != "" {
			if err := x.append(&event.StageFailed{StageID: st.ID, Reason: reason, Error: msg}); err != nil {
				return err
			}
			return x.takeEdge(st, flow.EdgeFail, msg)
		}
		// A fresh attempt starts from a clean slate, so the output contract
		// below cannot be satisfied by an earlier attempt's file (M18).
		x.clearOutputs(st.Outputs)
	}
	started := x.e.clock()

	prompt := x.assemblePrompt(st, iter)
	runner := x.res.RunnerFor(st.Harness) // per-step harness (default when unpinned)
	tr := x.newTranscript(st.ID, iter)
	spec := agentport.AgentSpec{Agent: st.Agent, Harness: st.Harness, Model: st.Model, MaxTurns: st.MaxTurns}
	// The agent's MCP servers ride every session this attempt opens or
	// loads, resolved now — with the project's secrets as they are today.
	mcp, err := x.stageMCP(st)
	if err != nil {
		return x.failStageMCP(st, err)
	}

	// ONE ATTEMPT, ONE SESSION, SEVERAL INVOCATIONS. The agent runs as a
	// live session: a question it cannot answer blocks in ask_human while
	// the human is around, and the human's messages are delivered into the
	// same session. A question nobody answers within the live hold PARKS the
	// attempt — the turn is cancelled, the run gives its slot back, and the
	// session is resumed with the answer when it lands. Each pass through
	// the loop is one invocation; iter does not move, the timeout budget
	// carries across, and each invocation banks its own spend as it ends.
	//
	// On re-entry, the questions this attempt parked on — with whatever the
	// log already knows about them. The run had no loop while it waited, so
	// an answer given in that window exists only as an event; the live ping
	// registry never saw it.
	asks := x.pausedAsks(st)
	reply, replyKind, resumeReason := "", "", ""
	crashed := reenter && len(asks) == 0
	if crashed {
		reply, replyKind = resumeReply, event.PromptRestart
	}
	budget := st.Timeout
	var inv invocation
	for {
		if len(asks) > 0 {
			if unanswered(asks) {
				// Still nothing to do but wait. Park the run and give the
				// slot back rather than hold a goroutine, a workspace and a
				// scheduling slot against a human's attention span.
				return errWaitingOnHuman
			}
			if _, ok := rs.sessions[st.ID]; !ok {
				// An answer with nowhere to go. Re-running the agent from
				// scratch would drop the human's words on the floor and let
				// it guess again — the exact failure this path exists to
				// prevent — so fail honestly and let the flow route it.
				const why = "the agent asked a question but its harness recorded no session to resume, so the answer cannot be delivered"
				if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonHarnessError, Error: why}); err != nil {
					return err
				}
				return x.takeEdge(st, flow.EdgeFail, why)
			}
			// What the agent wrote BEFORE the answer arrived: the pause does
			// not depend on obedience, but an artifact written from a guess
			// does need saying out loud, by name.
			reply, replyKind = x.answerReply(asks, x.writtenOutputs(st)), event.PromptAnswer
		}
		session, haveSession := rs.sessions[st.ID]

		// A take-over in force (or its handback owed) decides what the
		// session gets next, ahead of the restart reply: the holder's words,
		// the handback — or nothing yet, and the attempt parks on them.
		if len(asks) == 0 {
			hr, err := x.holdReentry(st, iter, crashed && haveSession, runner, session)
			if err != nil {
				return err
			}
			if hr.park {
				return x.parkOnHold(st, iter, session)
			}
			if hr.reply != "" {
				reply, replyKind, resumeReason = hr.reply, hr.kind, hr.reason
				crashed = false
			}
		}

		if crashed && haveSession {
			crashed = false
			// The engine restarted mid-attempt. The agent process lives in
			// the workspace and may have outlived it: follow the turn still
			// running (or the one that finished while nobody listened)
			// instead of starting another.
			if ra, ok := runner.(agentport.AgentReattacher); ok && ra.Reattachable(x.ctx, rs.envID, session, rs.agentCursor[st.ID]) {
				if err := x.append(&event.StageResumed{StageID: st.ID, Iteration: iter, Reason: event.ResumeRestart,
					SessionID: string(session)}); err != nil {
					return err
				}
				var err error
				inv, err = x.invoke(st, budget, tr, x.liveAsks(st), "",
					func(ctx context.Context, in agentport.AgentInput) (agentport.AgentResult, error) {
						in.MCPServers = mcp
						r, _, err := ra.Reattach(ctx, rs.envID, session, rs.agentCursor[st.ID], in)
						return r, err
					})
				if err != nil {
					return err
				}
				budget = inv.left
				if inv.outcome != outcomePaused {
					break
				}
				if stop, err := x.pauseAttempt(st, iter, inv); stop || err != nil {
					return err
				}
				asks, reply = x.pausedAsks(st), ""
				continue
			}
			// The turn that was live is gone. Questions it was blocked on
			// can still be answered — the session is resumed with them, as
			// for any parked attempt.
			if live := x.liveAsks(st); len(live) > 0 {
				if stop, err := x.pauseAttempt(st, iter, invocation{open: live, res: agentRes{r: agentport.AgentResult{Session: session}}}); stop || err != nil {
					return err
				}
				asks, reply = x.pausedAsks(st), ""
				continue
			}
		}
		crashed = false

		resuming := reply != "" && haveSession
		if resuming {
			reason := resumeReason
			if reason == "" {
				reason = event.ResumeAnswered
				if len(asks) == 0 {
					reason = event.ResumeRestart
				}
			}
			if err := x.append(&event.StageResumed{StageID: st.ID, Iteration: iter, Reason: reason,
				PingIDs: askIDs(asks), SessionID: string(session)}); err != nil {
				return err
			}
		}
		asks, resumeReason = nil, ""

		text, kind := prompt, event.PromptStage
		if resuming {
			text, kind = reply, replyKind
		}
		text, err := x.withPendingMessages(st, iter, text)
		if err != nil {
			return err
		}
		if err := x.prompted(st.ID, iter, kind, text); err != nil {
			return err
		}
		inv, err = x.invoke(st, budget, tr, nil, text,
			func(ctx context.Context, in agentport.AgentInput) (agentport.AgentResult, error) {
				in.MCPServers = mcp
				if resuming {
					return runner.Resume(ctx, rs.envID, session, in)
				}
				return runner.Run(ctx, rs.envID, spec, in)
			})
		if err != nil {
			return err // cancelled: caller inspects cause
		}
		budget = inv.left
		if inv.outcome != outcomePaused {
			break
		}
		if stop, err := x.pauseAttempt(st, iter, inv); stop || err != nil {
			return err
		}
		// Whether this parks or resumes immediately is one question — is
		// anything still unanswered? — and the top of the loop already asks
		// it. An answer that landed while the turn was winding down resumes
		// right here instead of parking a run that has nothing to wait for.
		asks, reply = x.pausedAsks(st), ""
	}
	res := inv.res
	// A take-over the attempt ended underneath is closed with its account
	// before anything routes.
	if err := x.endHold(st, iter, inv.holdSpend); err != nil {
		return err
	}
	x.publishLog(st.ID, iter, res.r.Output)
	// Capture whatever this attempt produced BEFORE routing: a failed
	// attempt's partial outputs are the interesting ones (M27).
	x.publishOutputs(st, iter, st.Outputs)
	if inv.outcome == outcomeTimedOut {
		if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonTimeout}); err != nil {
			return err
		}
		// Whatever the cancelled attempt printed rides along for edges that
		// declare feed: output.
		return x.takeEdge(st, failEdgeKind(st, event.ReasonTimeout), shell.Tail(res.r.Output, 8<<10))
	}
	if res.err != nil {
		// Run cancellation (abort/pause/shutdown) can surface as the harness
		// result racing the ctx branch in awaitAgent; it is not a stage
		// failure — mirror stageCommand and let the loop exit on the cause.
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonHarnessError, Error: res.err.Error()}); err != nil {
			return err
		}
		return x.takeEdge(st, flow.EdgeFail, shell.Tail(res.r.Output, 8<<10))
	}

	// Output contract (§3): every effective output (decl defaults overlaid
	// with stage rebinds) must exist. All missing paths are reported in one
	// failure so a human sees the whole gap, not the first file only.
	if missing := x.missingPaths(st.Outputs); len(missing) > 0 {
		// The retry is told which files it did not write (edge.failure).
		msg := fmt.Sprintf("declared output(s) not readable: %s", strings.Join(missing, ", "))
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputContract, Error: msg}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, msg)
	}
	// Every output with a structure — a built-in type's schema, a flow schema
	// — is checked first, all violations at once, in the words the agent
	// read in its OUTPUT CONTRACT: a shape error is reported as one, never as
	// a parser's complaint about a field it could not read.
	if msg := x.checkStructuredOutputs(st); msg != "" {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputInvalid, Error: msg}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, msg)
	}
	// A verdict-typed output had its existence and shape checked above, so
	// this read can now only fail on what a schema cannot say — one report
	// per cause.
	var verdict *Verdict
	if x.verdictPath(st) != "" {
		v, err := x.readVerdict(st)
		if err != nil {
			// The file exists (checked above) but does not hold what its type
			// promises — a different fault from "never wrote it" (M18).
			if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputInvalid, Error: err.Error()}); aerr != nil {
				return aerr
			}
			return x.takeEdge(st, flow.EdgeFail, err.Error())
		}
		verdict = v
	}
	// A review_replies output is checked against the threads this round
	// loaded before anything is posted: a reply to an unknown thread, or a
	// thread left without one, is this stage's fault, retried with the
	// reason as $FAILURE.
	if msg := x.checkReviewReplies(st); msg != "" {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputInvalid, Error: msg}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, msg)
	}
	// So is a review_threads output, against what the round loaded and what
	// a person dropped, before post_review reads it.
	if msg := x.checkReviewThreads(st); msg != "" {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputInvalid, Error: msg}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, msg)
	}

	completed := &event.StageCompleted{
		StageID:    st.ID,
		DurationMS: x.e.clock().Sub(started).Milliseconds(),
		TurnsUsed:  res.r.TurnsUsed,
		SessionID:  string(res.r.Session),
	}
	if res.r.Usage != (agentport.Usage{}) {
		completed.Usage = &event.Usage{
			InputTokens:  res.r.Usage.InputTokens,
			OutputTokens: res.r.Usage.OutputTokens,
			CostUSD:      res.r.Usage.CostUSD,
		}
	}
	// An approval with comments carries them on (flow.GivesVerdict).
	if verdict != nil && verdict.Approve {
		completed.Feed = x.redact.Scrub(verdictText(st.ID, verdict))
	}
	if err := x.append(completed); err != nil {
		return err
	}
	return x.afterComplete(st, verdict)
}

// afterComplete routes a completed stage — live, or recovered after a crash
// between its stage_completed and its routing: the verdict is recorded (once:
// a recovered stage may have it on the log already), an agent stage's spend
// is held to the budget, then a rejection takes on_reject and anything else
// on_success, carrying an approval's verdict. A skipped stage spent nothing
// and judged nothing: it goes straight on.
func (x *exec) afterComplete(st *flow.Stage, verdict *Verdict) error {
	if x.rs.elided[st.ID] {
		x.advanceWith(st, x.rs.completedFeed)
		return nil
	}
	if verdict != nil && !x.rs.verdictRecorded {
		if err := x.append(&event.VerdictReceived{StageID: st.ID, Approve: verdict.Approve, NComments: len(verdict.Comments)}); err != nil {
			return err
		}
	}
	if st.Type == flow.StageAgent {
		if stop, err := x.checkBudget(st); stop || err != nil {
			return err
		}
	}
	if verdict != nil && !verdict.Approve {
		return x.takeEdge(st, flow.EdgeReject, verdictText(st.ID, verdict))
	}
	x.advanceWith(st, x.rs.completedFeed)
	return nil
}

// pauseAttempt records an invocation that ended with questions open: the
// attempt is parked on them, not over. It banks what the invocation spent
// (real whether or not the run ever resumes) and the session the answer will
// re-enter. Outputs are NOT published: a half-written artifact is not this
// attempt's result. stop reports that a budget check ended the run.
func (x *exec) pauseAttempt(st *flow.Stage, iter int, inv invocation) (stop bool, err error) {
	rs := x.rs
	for _, p := range inv.open {
		// Any answer from here on is news to the agent: it arrives by resume.
		p.handedBack.Store(true)
	}
	x.publishLog(st.ID, iter, inv.res.r.Output)
	reason := event.PauseAskHuman
	if inv.holdPark {
		reason = event.PauseHeld
	}
	paused := &event.StagePaused{StageID: st.ID, Iteration: iter, Reason: reason,
		PingIDs: pingIDs(inv.open), TurnsUsed: inv.res.r.TurnsUsed, SessionID: string(inv.res.r.Session)}
	if inv.res.r.Usage != (agentport.Usage{}) {
		paused.Usage = &event.Usage{InputTokens: inv.res.r.Usage.InputTokens,
			OutputTokens: inv.res.r.Usage.OutputTokens, CostUSD: inv.res.r.Usage.CostUSD}
	}
	// A take-over in force banks this leg's spend on the pause, whatever
	// the pause is on: the release that closes it accounts for every leg.
	if h := rs.holds[st.ID]; h != nil {
		leg := inv.holdSpend
		paused.Hold = &leg
	}
	if err := x.append(paused); err != nil {
		return false, err
	}
	// A question-per-turn loop would otherwise never meet a budget check:
	// nothing else bounds an attempt that keeps asking.
	if stop, err := x.checkBudget(st); stop || err != nil {
		return true, err
	}
	return false, nil
}

// liveAsks are this stage's open questions that were being waited on LIVE
// (raised, unanswered, never parked) — re-registered from the log when the
// loop started.
func (x *exec) liveAsks(st *flow.Stage) []*Ping {
	parked := map[string]bool{}
	for _, id := range x.rs.pausedAsks[st.ID] {
		parked[id] = true
	}
	var out []*Ping
	x.e.mu.Lock()
	defer x.e.mu.Unlock()
	for _, op := range x.rs.openAgentPings() {
		if op.StageID != st.ID || parked[op.PingID] {
			continue
		}
		if p := x.e.pings[op.PingID]; p != nil && !p.answered && !p.handedBack.Load() {
			out = append(out, p)
		}
	}
	return out
}

// withPendingMessages appends the human messages nobody has delivered yet
// (sent while no invocation could take them) to the next prompt.
func (x *exec) withPendingMessages(st *flow.Stage, iter int, text string) (string, error) {
	if len(x.rs.pendingMsgs) == 0 {
		return text, nil
	}
	msgs := make([]agentport.UserMessage, 0, len(x.rs.pendingMsgs))
	ids := make([]string, 0, len(x.rs.pendingMsgs))
	for _, m := range x.rs.pendingMsgs {
		msgs = append(msgs, agentport.UserMessage{ID: m.MessageID, Text: m.Text})
		ids = append(ids, m.MessageID)
	}
	if err := x.append(&event.MessageDelivered{MessageIDs: ids, StageID: st.ID, Iteration: iter}); err != nil {
		return text, err
	}
	return text + "\n\n---\n\n" + formatMessages(msgs), nil
}

// awaitAgent waits for one harness invocation while servicing pings, human
// messages and the transcript. The stage timeout is suspended while any ping
// is pending (§3 defaults.timeout) and what is left of it carries to the next
// invocation of the same attempt.
//
// A question blocks the agent's turn (ask_human waits in the tool call) for
// up to Config.AskHumanHold; past that the run is PARKED — the turn is
// cancelled and the invocation ends as a pause, to be resumed with the answer.
// An invocation that returns with questions still open is a pause too: the
// agent stopped rather than guessing, and the caller holds the attempt until
// the human answers. Letting it through instead is what silently turned an
// unanswered clarification into an approved-looking plan.
func (x *exec) awaitAgent(st *flow.Stage, resCh <-chan agentRes, cancelRun context.CancelCauseFunc, budget time.Duration,
	io *invIO, initialOpen []*Ping) (invocation, error) {
	remaining := budget
	open := append([]*Ping(nil), initialOpen...)
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	lastArm := time.Now()
	// The take-over in force, if any: carried in from the log (taken in an
	// earlier leg of this attempt) and tracked live from here.
	hold := x.liveHold(st)
	if hold != nil {
		io.inbox.setHeld(true)
	}
	holdLeg := func() event.HoldSpend {
		if hold != nil {
			return hold.spend
		}
		return event.HoldSpend{}
	}
	turnOpen := false
	// record files one agent event: a new harness session is recorded the
	// moment it exists, so a crash mid-invocation resumes (or reattaches
	// to) it, not a new one — whichever path meets the event, the loop's
	// select, a drain after the result, or a wind-down; everything else
	// goes to the transcript. session says it was a new session.
	record := func(ev agentport.AgentEvent) (session bool, err error) {
		if ev.Kind == agentport.AgentEvSession && ev.Text != "" && x.rs.sessions[st.ID] != agentport.SessionID(ev.Text) {
			if err := x.append(&event.AgentSession{StageID: st.ID, Iteration: io.tr.iter, SessionID: ev.Text}); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, io.tr.handle(ev)
	}

	suspend := func() {
		if !timer.Stop() {
			// The timer fired concurrently with the ping; drain the stale
			// tick so the later Reset re-arms cleanly instead of firing a
			// spurious instant timeout after the human answers.
			select {
			case <-timer.C:
			default:
			}
		}
		remaining -= time.Since(lastArm)
		if remaining <= 0 {
			remaining = time.Millisecond
		}
	}
	resume := func() {
		lastArm = time.Now()
		timer.Reset(remaining)
	}
	// left is what the caller carries to the next invocation: the timer is
	// already stopped-and-debited whenever a ping is open.
	left := func() time.Duration {
		if len(open) > 0 || (hold != nil && hold.idle) {
			return remaining
		}
		return remaining - time.Since(lastArm)
	}
	var park *time.Timer
	var parkC <-chan time.Time
	armPark := func() {
		park = time.NewTimer(x.e.cfg.AskHumanHold)
		parkC = park.C
	}
	disarmPark := func() {
		if park != nil {
			park.Stop()
			park, parkC = nil, nil
		}
	}
	defer disarmPark()
	if len(open) > 0 {
		suspend()
		armPark()
	}
	// idleOnHolder: the agent's turn ended with nothing queued while someone
	// holds the session. The clock stops for the holder's thinking time,
	// and past the live hold the run parks on them.
	idleOnHolder := func() {
		if hold == nil || hold.idle || len(open) > 0 {
			return
		}
		hold.idle = true
		suspend()
		armPark()
	}
	busyAgain := func() {
		if hold == nil || !hold.idle {
			return
		}
		hold.idle = false
		resume()
		disarmPark()
	}
	// handBack ends the hold: its account is recorded, the handback prompt
	// is queued as the session's next turn (before the runner is let go of,
	// so a runner that sees the hold gone always finds it), and the stage
	// clock runs again.
	handBack := func(by string) error {
		if err := x.releaseHold(st, io.tr.iter, hold.st, by, event.HoldReleasedByUser, hold.spend); err != nil {
			return err
		}
		if err := x.prompted(st.ID, io.tr.iter, event.PromptHandback, handbackPrompt); err != nil {
			return err
		}
		io.inbox.push(agentport.UserMessage{Text: handbackPrompt, Verbatim: true})
		busyAgain()
		hold = nil
		io.inbox.setHeld(false)
		return nil
	}

	// collect waits for a cancelled invocation to wind down, still recording
	// its transcript — but doesn't trust the harness to honor the
	// cancellation: after a grace period it abandons the goroutine rather
	// than wedge the run.
	collect := func(out agentOutcome) (invocation, error) {
		grace := time.NewTimer(60 * time.Second)
		defer grace.Stop()
		for {
			select {
			case res := <-resCh:
				if err := io.drain(record); err != nil {
					return invocation{}, err
				}
				return invocation{res: res, outcome: out, open: open, left: left(), holdSpend: holdLeg()}, nil
			case ev := <-io.events:
				if _, err := record(ev); err != nil {
					return invocation{}, err
				}
			case <-grace.C:
				x.e.log.Warn("harness ignored cancellation; abandoning attempt", "run", x.rs.runID, "stage", st.ID)
				return invocation{outcome: out, open: open, left: left(), holdSpend: holdLeg()}, nil
			}
		}
	}

	for {
		select {
		case res := <-resCh:
			if err := io.drain(record); err != nil {
				return invocation{}, err
			}
			out := outcomeReturned
			// Questions still open and the turn over: the agent ended it on
			// them. An error is not a pause — a harness that died
			// mid-question has nothing left to resume into, so it routes as
			// the failure it is.
			if len(open) > 0 && res.err == nil {
				out = outcomePaused
			}
			return invocation{res: res, outcome: out, open: open, left: left(), holdSpend: holdLeg()}, nil
		case ev := <-io.events:
			if session, err := record(ev); err != nil {
				return invocation{}, err
			} else if session {
				continue
			}
			switch ev.Kind {
			case agentport.AgentEvUserMessage:
				// The holder's words reaching the agent, on the hold's account.
				if hold != nil {
					for _, m := range ev.Messages {
						if !m.Verbatim {
							hold.spend.Messages++
						}
					}
				}
			case agentport.AgentEvTurnStart:
				turnOpen = true
				if hold != nil {
					// A turn starting under the hold is the holder's: it is
					// what their message prompted.
					busyAgain()
					if !hold.heldTurns[ev.Turn] {
						hold.heldTurns[ev.Turn] = true
						hold.spend.Turns++
					}
				}
			case agentport.AgentEvTurnEnd:
				turnOpen = false
				if hold == nil {
					break
				}
				if hold.heldTurns[ev.Turn] && ev.Usage != nil {
					hold.spend.Add(event.HoldSpend{Usage: &event.Usage{InputTokens: ev.Usage.InputTokens,
						OutputTokens: ev.Usage.OutputTokens, CostUSD: ev.Usage.CostUSD}})
				}
				if len(open) > 0 {
					// The turn ended on the agent's own open question. The
					// holder is the one to answer it, but the tool call it came
					// in on is gone: park on the question as any attempt does,
					// and resume with the answer under the same hold.
					for _, p := range open {
						p.handedBack.Store(true)
					}
					cancelRun(errParked)
					return collect(outcomePaused)
				}
				if hold.releasing {
					if err := handBack(hold.releaseBy); err != nil {
						return invocation{}, err
					}
				} else if !io.inbox.pending() {
					idleOnHolder()
				}
			}
		case req := <-x.h.messages:
			q := &event.MessageQueued{MessageID: req.msg.ID, Text: req.msg.Text, Interrupt: req.msg.Interrupt, StageID: st.ID}
			if err := x.append(q); err != nil {
				req.ack <- err
				return invocation{}, err
			}
			io.inbox.push(req.msg)
			req.ack <- nil
		case req := <-x.h.holds:
			var err error
			switch {
			case req.take && hold != nil:
				err = ErrAlreadyHeld
			case req.take:
				if err = x.takeHold(st, io.tr.iter, req.userID); err == nil {
					hold = x.liveHold(st)
					io.inbox.setHeld(true)
					if !turnOpen && !io.inbox.pending() {
						idleOnHolder()
					}
				}
			case hold == nil:
				err = ErrNotHeld
			case hold.releasing:
				// Already handing back when the turn in flight ends.
			case turnOpen || io.inbox.pending():
				// The handback follows the turn in flight (or the one the
				// queued message is about to start).
				hold.releasing, hold.releaseBy = true, req.userID
			default:
				err = handBack(req.userID)
			}
			req.ack <- err
			if err != nil && !errors.Is(err, ErrAlreadyHeld) && !errors.Is(err, ErrNotHeld) {
				return invocation{}, err
			}
		case req := <-x.h.mailbox:
			x.e.mu.Lock()
			req.p.StageID = st.ID // the stage whose agent asked (PLAN4 M15)
			x.e.mu.Unlock()
			raised := &event.PingRaised{PingID: req.p.ID, Type: req.p.Type,
				Question: req.p.Question, Context: req.p.Context, Key: req.p.key,
				Files: req.p.Files, Link: req.p.Link, StageID: st.ID, Form: req.p.Form, AnswerKind: req.p.AnswerKind}
			if err := x.append(raised); err != nil {
				close(req.ack)
				return invocation{}, err
			}
			// Keep the working state a mirror of what replay would build, so
			// a resume reads the same whether the answer arrived in this
			// process or was found in the log after a restart.
			// Snapshot the attached files as they are RIGHT NOW: the agent
			// resumes the moment this ping is answered, and the record must
			// keep what the human was actually shown.
			x.publishPingFiles(req.p, st.ID)
			x.e.notify(x.bg, notifyport.Notification{
				RunID: x.rs.runID, PingID: req.p.ID, Type: req.p.Type,
				Title: fmt.Sprintf("%s: %s", req.p.Type, x.rs.view.TicketTitle),
				Body:  req.p.Question,
			})
			if len(open) == 0 {
				suspend()
				armPark()
			}
			open = append(open, req.p)
			close(req.ack)
		case ar := <-x.h.answers:
			handled, err := x.processAnswer(ar)
			if err != nil {
				return invocation{}, err
			}
			// An answer the agent never saw does not close the question: once
			// the turn was handed back (parked), the only way in is a resume,
			// so the ping stays open and the attempt pauses on it below.
			if handled && agentPingType(ar.p.Type) && !ar.p.handedBack.Load() {
				open = dropPing(open, ar.p)
				if len(open) == 0 {
					resume()
					disarmPark()
				}
			}
		case <-parkC:
			parkC = nil
			if hold != nil && hold.idle {
				// The holder went quiet: park on them, hold in force.
				cancelRun(errParked)
				inv, err := collect(outcomePaused)
				inv.holdPark = true
				return inv, err
			}
			// Nobody answered while the turn stayed live: park. The open
			// questions are handed back first, so an answer racing the
			// cancel is delivered by resume rather than lost in the tool call.
			for _, p := range open {
				p.handedBack.Store(true)
			}
			cancelRun(errParked)
			return collect(outcomePaused)
		case <-timer.C:
			cancelRun(errStageTimeout)
			return collect(outcomeTimedOut)
		case <-x.ctx.Done():
			if hold != nil {
				// Bank the leg for whoever closes the hold next (an abort's
				// account; a pause re-reads the log and starts the leg over).
				hold.st.banked.Add(hold.spend)
			}
			cancelRun(context.Cause(x.ctx))
			return invocation{}, context.Cause(x.ctx)
		}
	}
}

// ask is one question a paused attempt is waiting on. A parked run has no
// loop, so by the time one exists again the answer may live only in the log —
// which is why this is built from replay state and not from the in-memory ping
// registry, whose entries were pruned when the loop exited.
type ask struct {
	id       string
	question string
	answer   *event.PingAnswered // nil while still open
}

// pausedAsks re-collects the questions this stage parked on (stage_paused),
// with whatever answers the log has since recorded.
func (x *exec) pausedAsks(st *flow.Stage) []ask {
	ids := x.rs.pausedAsks[st.ID]
	if len(ids) == 0 {
		return nil
	}
	out := make([]ask, 0, len(ids))
	for _, id := range ids {
		a := ask{id: id, answer: x.rs.answeredPings[id]}
		if r := x.rs.raisedPings[id]; r != nil {
			a.question = r.Question
		}
		out = append(out, a)
	}
	return out
}

// unanswered reports whether any question is still open — i.e. whether there
// is still nothing for the agent to do.
func unanswered(asks []ask) bool {
	for _, a := range asks {
		if a.answer == nil {
			return true
		}
	}
	return false
}

func askIDs(asks []ask) []string {
	if len(asks) == 0 {
		return nil
	}
	out := make([]string, 0, len(asks))
	for _, a := range asks {
		out = append(out, a.id)
	}
	return out
}

func dropPing(ps []*Ping, p *Ping) []*Ping {
	for i, q := range ps {
		if q == p {
			return append(ps[:i], ps[i+1:]...)
		}
	}
	return ps
}

func pingIDs(ps []*Ping) []string {
	if len(ps) == 0 {
		return nil
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

// processAnswer appends ping_answered and delivers the answer to whoever is
// waiting on the ping. Returns false if the ping was already answered.
//
// Every path acks: the caller is blocked waiting to hear whether its answer
// was recorded, and silence would leave a human believing an answer landed
// that nothing ever read.
func (x *exec) processAnswer(ar answerReq) (handled bool, err error) {
	defer func() {
		if ar.ack == nil {
			return
		}
		switch {
		case err != nil:
			ar.ack <- err
		case !handled:
			ar.ack <- fmt.Errorf("ping %s: %w", ar.p.ID, ErrPingAnswered)
		default:
			ar.ack <- nil
		}
	}()
	x.e.mu.Lock()
	if ar.p.answered {
		x.e.mu.Unlock()
		return false, nil
	}
	ar.p.answered = true
	ans := ar.ans
	ar.p.answer = &ans
	x.e.mu.Unlock()

	recorded := &event.PingAnswered{PingID: ar.p.ID, Answer: ar.ans.Answer, Feedback: ar.ans.Feedback,
		Content: ar.ans.Content, AttachmentIDs: ar.ans.AttachmentIDs, Review: ar.ans.Review, Curated: ar.ans.Curated}
	if err := x.append(recorded); err != nil {
		// Roll the in-memory claim back so a retried answer can succeed
		// after recovery (the event log has no ping_answered).
		x.e.mu.Lock()
		ar.p.answered = false
		ar.p.answer = nil
		x.e.mu.Unlock()
		return false, err
	}
	// Deliver any files the human attached to this answer into the workspace,
	// recording their paths so the ask_human tool-return (agent pings) and the
	// gate feed (gate pings) can point the agent at them.
	if files := x.pingAnswerFiles(ar.p.ID); len(files) > 0 {
		x.e.mu.Lock()
		ar.p.deliveredFiles = files
		x.e.mu.Unlock()
	}
	close(ar.p.done)
	return true, nil
}

// --- command stages ---------------------------------------------------------

func (x *exec) stageCommand(st *flow.Stage) error {
	rs := x.rs
	script := st.Run
	if st.RunRef != "" {
		script = x.res.Commands[st.RunRef]
		if script == "" {
			// The command was deleted from project.commands after run
			// creation. Executing "" would exit 0 and silently pass a gate.
			if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra,
				Error: fmt.Sprintf("run_ref %q no longer exists in project.commands", st.RunRef)}); err != nil {
				return err
			}
			return x.fail(fmt.Sprintf("stage %s: run_ref %q no longer exists in project.commands", st.ID, st.RunRef))
		}
	}
	fresh := rs.ph != phIn
	if fresh { // §2: on recovery, open command stages re-execute without a new attempt
		iter := rs.iters[st.ID] + 1
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
	}
	started := x.e.clock()

	// Declared inputs are a precondition (FLOWLANG §4): a script whose input
	// is missing usually fails in a confusing way (or worse, succeeds having
	// done nothing). Checking first names the real problem — upstream wiring.
	if missing := x.missingPaths(st.Inputs); len(missing) > 0 {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInputMissing,
			Error: fmt.Sprintf("declared input(s) not readable: %s", strings.Join(missing, ", "))}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, "")
	}
	// What it reads with a schema must hold it (FLOWLANG §4): a script fed a
	// malformed file fails in a confusing way, or succeeds having done
	// nothing.
	if reason, msg := x.checkStructuredInputs(st); reason != "" {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: reason, Error: msg}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, msg)
	}

	// A fresh attempt starts from a clean slate, so the output check below
	// cannot pass on an earlier attempt's file (M18). After the input check:
	// the two never overlap in practice, and inputs must survive regardless.
	if fresh {
		x.clearOutputs(st.Outputs)
	}

	// A command stage may bound its own wall clock; unset inherits the flow
	// default (FLOWLANG §2, PLAN5 M23).
	timeout := st.Timeout
	if timeout == 0 {
		timeout = rs.fl.Defaults.Timeout
	}
	// Once in the primary, or — each_repo — once per checkout, stopping at
	// the first failure. Each checkout gets the full timeout.
	// On a subject, in its checkout (PLAN6 R12): the phase acts on that
	// merge request's branch only.
	dirs := []string{event.PrimaryCheckoutDir}
	if co := x.subjectCheckout(); co != nil {
		dirs = []string{co.Dir}
	} else if st.EachRepo {
		dirs = rs.checkoutDirs()
	}
	var res envport.ExecResult
	var err error
	var combined strings.Builder
	marker := commandMarker(rs.runID, st.ID)
	for _, dir := range dirs {
		cctx, cancel := context.WithTimeout(x.ctx, timeout)
		res, err = x.res.Envp.Exec(cctx, rs.envID, envport.Command{Script: script, Dir: dir, Timeout: timeout, Marker: marker})
		cancel()
		if err != nil {
			break
		}
		if len(dirs) > 1 {
			fmt.Fprintf(&combined, "=== %s ===\n", dir)
		}
		combined.WriteString(res.Output)
		if res.ExitCode != 0 {
			break
		}
	}
	res.Output = combined.String()
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonTimeout}); aerr != nil {
				return aerr
			}
			return x.takeEdge(st, failEdgeKind(st, event.ReasonTimeout), "")
		}
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra, Error: err.Error()}); aerr != nil {
			return aerr
		}
		return x.fail("infrastructure error: " + err.Error())
	}
	x.publishLog(st.ID, rs.iters[st.ID], res.Output)
	x.publishOutputs(st, rs.iters[st.ID], st.Outputs)
	if res.ExitCode != 0 {
		code := res.ExitCode
		// The output tail rides in the event so recovery can route the
		// failure without re-executing the command.
		if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonExitNonzero,
			ExitCode: &code, Error: shell.Tail(res.Output, 8<<10)}); err != nil {
			return err
		}
		return x.takeEdge(st, flow.EdgeFail, shell.Tail(res.Output, 8<<10))
	}
	// A command that exits 0 without writing what it declared has not done its
	// job — the same contract agent stages carry.
	if missing := x.missingPaths(st.Outputs); len(missing) > 0 {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputContract,
			Error: fmt.Sprintf("declared output(s) not readable: %s", strings.Join(missing, ", "))}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, shell.Tail(res.Output, 8<<10))
	}
	// And what it writes with a schema must hold it, before anything reads it.
	if msg := x.checkStructuredOutputs(st); msg != "" {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputInvalid, Error: msg}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, msg)
	}
	if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
		return err
	}
	x.advance(st)
	return nil
}

// commandMarker is one command stage's envport.Command.Marker: the run and the
// stage, since a workspace only ever hosts one run. An attempt's exec client
// dies with its timeout or with the engine process; the adapter uses the
// marker to kill what the previous attempt left running before this one
// starts (see envport.Command.Marker).
func commandMarker(runID, stageID string) string { return runID + ":" + stageID }

// missingPaths returns the bindings' paths that cannot be read in the
// workspace, in declaration order. Sentinels are engine-provided, never files.
// writtenOutputs reports which of a stage's declared outputs already exist —
// used when resuming a paused attempt, to tell an agent that answered its own
// question by guessing exactly which files it now has to revisit.
func (x *exec) writtenOutputs(st *flow.Stage) []string {
	var written []string
	for _, b := range st.Outputs {
		if b.Value == "" || flow.IsEngineSource(b.Value) {
			continue
		}
		if b.Type == agentspec.TypeFiles {
			if held, _ := x.dirFiles(b.Value); len(held) > 0 {
				written = append(written, b.Value)
			}
			continue
		}
		if _, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, b.Value); err == nil {
			written = append(written, b.Value)
		}
	}
	return written
}

func (x *exec) missingPaths(ports []flow.Port) []string {
	var missing []string
	for _, b := range ports {
		// An engine source is no file; an optional input may be absent.
		if b.Value == "" || flow.IsEngineSource(b.Value) || b.Optional {
			continue
		}
		path := flow.FilePath(b.Value)
		if b.Type == agentspec.TypeFiles {
			// A directory, there when it holds a file.
			if held, _ := x.dirFiles(path); len(held) == 0 {
				missing = append(missing, emptyDir(path))
			}
			continue
		}
		if _, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, path); err != nil {
			missing = append(missing, path)
		}
	}
	return missing
}

// clearOutputs deletes a stage's declared outputs before a FRESH attempt
// (PLAN5 M18). Without it the post-stage existence check passes on a file an
// EARLIER attempt wrote: attempt 2 could "succeed" without producing
// anything, and a stale verdict could route the run. Deleting first makes
// the existing check a freshness check — whatever exists afterwards was
// written by the attempt that just ran.
//
// Never called on crash-recovery re-entry: a resumed harness session
// continues mid-work, and its partial outputs must survive.
func (x *exec) clearOutputs(bindings []flow.Port) {
	var paths, dirs []string
	for _, b := range bindings {
		if b.Value == "" || flow.IsEngineSource(b.Value) {
			continue
		}
		if b.Type == agentspec.TypeFiles {
			// Without its slash: a symlink planted where the directory goes
			// is removed itself, never followed into what it points at.
			dirs = append(dirs, shell.Quote(strings.TrimSuffix(b.Value, "/")))
			continue
		}
		paths = append(paths, shell.Quote(b.Value))
	}
	var scripts []string
	if len(paths) > 0 {
		scripts = append(scripts, "rm -f -- "+strings.Join(paths, " "))
	}
	if len(dirs) > 0 {
		// A files output starts empty, and there: the attempt only writes
		// into it.
		list := strings.Join(dirs, " ")
		scripts = append(scripts, "rm -rf -- "+list+" && mkdir -p -- "+list)
	}
	// Best-effort: a workspace that cannot delete is not a stage failure —
	// the output contract still has the final say.
	for _, script := range scripts {
		if _, err := x.res.Envp.Exec(x.ctx, x.rs.envID, envport.Command{
			Script:  script,
			Dir:     "repo",
			Timeout: time.Minute,
		}); err != nil {
			x.e.log.Warn("clearing declared outputs failed", "run", x.rs.runID, "err", err)
		}
	}
}

// --- human gates ------------------------------------------------------------

func (x *exec) stageGate(st *flow.Stage) error {
	rs := x.rs

	// Recovery: answered pre-crash but routing unrecorded.
	if rs.ph == phIn && rs.gateAnswer != nil {
		return x.routeGate(st, rs.gateAnswer, x.e.clock())
	}

	var p *Ping
	if rs.ph == phIn {
		p = x.openYesNo(st, PingGate)
	}
	if p == nil {
		iter := rs.iters[st.ID] + 1
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
		// The gate's inputs: files to review and at most one link source
		// the engine resolves. TYPED HERE, at source resolution — this is
		// the one place that knows which is which, so nothing downstream ever
		// sniffs a string for "http" (DESIGN-FILES §4).
		files, types, link := x.gateFiles(st)
		p = &Ping{
			Type:     PingGate,
			Question: gateQuestion(st, rs.view.TicketTitle),
			// A gate over several merge requests lists them all; the link
			// stays the first (the primary repository's).
			Context: x.mrListMarkdown(),
			Files:   files, FileTypes: types, Link: link, StageID: st.ID, AnswerKind: event.AnswerYesNo,
		}
		if err := x.raiseYesNo(p); err != nil {
			return err
		}
		// The plan a human approves is the single most important document in
		// the run — snapshot it as reviewed, not as the workspace has it later.
		x.publishPingFiles(p, st.ID)
		x.e.notify(x.bg, notifyport.Notification{
			RunID: rs.runID, PingID: p.ID, Type: p.Type,
			Title: p.Question, Body: p.Primary(),
		})
	}

	// A gate is a wait point (FLOWLANG §1): the phase parks on the decision
	// and gives the workspace back, so other phases may run while a person
	// thinks. The idle loop takes the answer — the human's, or the one a
	// signal supplies through auto_approve / auto_reject (they merged it
	// instead of clicking approve) — and routes it (tryResume). Recovery
	// re-registers the open ping from the log and lands here again.
	return x.parkAt(st, p.ID)
}

// gateFiles is what a gate shows: its files in port order — a folder's
// (type files) each, as attachments are bounded: the first
// attachmentMaxFiles in all, none past attachmentMaxBytes (a snapshot of it
// would be cut) — each structured file's port type by path (what offers
// curation), and the link its link port resolves to.
func (x *exec) gateFiles(st *flow.Stage) (files []string, types map[string]string, link string) {
	for _, in := range st.Inputs {
		switch {
		case flow.IsEngineSource(in.Value):
			link = x.resolveGateInput(in.Value)
			continue
		case in.Type != agentspec.TypeFiles:
			path := flow.FilePath(in.Value)
			files = append(files, path)
			if agentspec.IsStructuredType(in.Type) {
				if types == nil {
					types = map[string]string{}
				}
				types[path] = in.Type
			}
			continue
		}
		dir := flow.FilePath(in.Value)
		held, err := x.dirFiles(dir)
		if err != nil {
			x.e.log.Warn("listing a gate's folder", "run", x.rs.runID, "stage", st.ID, "path", dir, "err", err)
		}
		for _, f := range held {
			if f.Size > attachmentMaxBytes {
				x.e.log.Warn("file too large to review", "run", x.rs.runID, "stage", st.ID, "path", f.Path, "size", f.Size)
				continue
			}
			files = append(files, f.Path)
		}
	}
	if len(files) > attachmentMaxFiles {
		x.e.log.Warn("a gate shows more files than are reviewed", "run", x.rs.runID, "stage", st.ID,
			"files", len(files), "shown", attachmentMaxFiles)
		files = files[:attachmentMaxFiles]
	}
	return files, types, link
}

// resolveGateInput turns a gate's input into what the human is handed: the
// link for a link source, the workspace path of a file to review.
func (x *exec) resolveGateInput(v string) string {
	if link := x.sourceLink(v); link != "" || flow.IsEngineSource(v) {
		return link
	}
	return flow.FilePath(v)
}

// sourceLink resolves a link source (run.mr.url, run.workspace,
// run.service.<name> — the human's preview link), or "".
func (x *exec) sourceLink(v string) string {
	switch v {
	case flow.SourceMRURL:
		// On a subject, the merge request the phase acts on.
		if mr := x.subjectMR(); mr != nil {
			return mr.Ref.URL
		}
		return x.rs.view.MRURL
	case flow.SourceWorkspace:
		// Workspace hand-off: the environment's app link, when it has one.
		return workspaceLink(x.rs.links)
	}
	if name := flow.SourceService(v); name != "" {
		return x.serviceLink(name)
	}
	return ""
}

// openYesNo returns the live Ping for a still-open yes/no question of this stage
// and type — one runLoop re-registered from the log after a restart — or nil
// when the stage has to raise one.
func (x *exec) openYesNo(st *flow.Stage, ptype string) *Ping {
	op := x.rs.openPing(st.ID, ptype)
	if op == nil {
		return nil
	}
	x.e.mu.Lock()
	defer x.e.mu.Unlock()
	return x.e.pings[op.PingID]
}

// raiseYesNo registers a yes/no question and records it: the caller fills
// what is asked and of which stage, this mints the id and the clock. What
// happens next (a snapshot of the reviewed file, the channels) is the
// caller's.
func (x *exec) raiseYesNo(p *Ping) error {
	p.ID, p.RunID, p.RaisedAt, p.done = x.e.newID(), x.rs.runID, x.e.clock(), make(chan struct{})
	x.e.mu.Lock()
	x.e.pings[p.ID] = p
	x.e.mu.Unlock()
	raised := &event.PingRaised{PingID: p.ID, Type: p.Type, Question: p.Question, Context: p.Context,
		Files: p.Files, FileTypes: p.FileTypes, Link: p.Link, StageID: p.StageID, AnswerKind: p.AnswerKind,
		Phase: p.Phase, EndsPhase: p.EndsPhase}
	if err := x.append(raised); err != nil {
		return err
	}
	// As replay has it: what the ping showed, for the answer's review.
	return nil
}

// --- the debug leaf --------------------------------------------------------

// stageDebug is the debug leaf (FLOWLANG §3): st took `then: debug`, and the
// run parks here for a human who has the workspace. No stage of its own —
// the ping belongs to st — and nothing is spent while they look. The engine
// composes no prose: the ping carries which stage, what it printed and where
// the workspace is, and the card is the UI's. Continue re-enters st as a
// fresh attempt (the cursor never left it); giving up is a dead end — it
// abandons the run in the main chain or an outcome, and ends a watcher's
// round.
// Like a gate it never provisions: ensureEnv runs lazily when st re-enters,
// and re-seeds what a human may have changed.
func (x *exec) stageDebug(st *flow.Stage) error {
	rs := x.rs
	// Recovery: answered pre-crash, routing unrecorded.
	if rs.gateAnswer != nil {
		ans := rs.gateAnswer
		rs.gateAnswer = nil
		return x.routeDebug(st, PingAnswer{Answer: ans.Answer, Feedback: ans.Feedback})
	}
	if x.openYesNo(st, PingDebug) == nil {
		p := &Ping{Type: PingDebug, Question: st.ID + " failed", Context: shell.Tail(rs.feed, 4<<10),
			Link: workspaceLink(rs.links), StageID: st.ID, AnswerKind: event.AnswerYesNo,
			Phase: rs.phase, EndsPhase: !x.endsRunAtDeadEnd()}
		if err := x.raiseYesNo(p); err != nil {
			return err
		}
		x.e.notify(x.bg, notifyport.Notification{
			RunID: rs.runID, PingID: p.ID, Type: p.Type,
			Title: p.Question, Body: p.Primary(),
		})
	}
	// Pure waiting: give the slot back. The answer wakes the run, and replay
	// lands on gateAnswer above.
	return errWaitingOnHuman
}

// routeDebug acts on the human's decision at the debug leaf.
func (x *exec) routeDebug(st *flow.Stage, ans PingAnswer) error {
	rs := x.rs
	if ans.Answer == "approve" {
		// Continue: back into st, fresh — pos never moved. What advance does
		// after a gate's approval, by hand: no feed, no "attempt n of m".
		rs.ph = phStart
		rs.feed, rs.feedSentinel, rs.feedAttempts = "", "", 0
		return nil
	}
	reason := fmt.Sprintf("debug rejected at stage %s", st.ID)
	if fb := strings.TrimSpace(ans.Feedback); fb != "" {
		reason += ": " + shell.Tail(fb, 2<<10)
	}
	return x.deadEnd(st, "", reason)
}

// routeGate follows a gate's answer, carrying the verdict — the person's
// summary, their comments file by file, what they attached — to on_reject,
// or to on_success when the approval said something. What they made of a
// review document the gate showed goes on the record first (recordCuration):
// approved, it is written back before the next reader starts; rejected, the
// redo reads what they dropped.
func (x *exec) routeGate(st *flow.Stage, rec *event.PingAnswered, started time.Time) error {
	var shown []string
	if r := x.rs.raisedPings[rec.PingID]; r != nil {
		shown = r.Files
	}
	curated, err := x.recordCuration(st, rec)
	if err != nil {
		return err
	}
	verdict := gateVerdict(st.ID, rec, shown, x.answerFileRefs(rec.PingID))
	if note := renderCuration(curated); note != "" && rec.Answer != "approve" {
		verdict = capVerdict(strings.TrimRight(verdict, "\n") + "\n" + note)
	}
	if rec.Answer == "approve" {
		verdict = x.redact.Scrub(verdict)
		if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds(), Feed: verdict}); err != nil {
			return err
		}
		x.advanceWith(st, verdict)
		return nil
	}
	return x.takeEdge(st, flow.EdgeReject, verdict)
}

// --- action stages ----------------------------------------------------------

// stageAction dispatches to the executor for the stage's registered action
// (actionspec). Executors must be idempotent: an action stage always re-enters
// its executor on recovery (the routeAfterFail shortcut skips it), because a
// half-finished side effect on a real service cannot be replayed away.
func (x *exec) stageAction(st *flow.Stage) error {
	rs := x.rs
	if rs.ph != phIn {
		iter := rs.iters[st.ID] + 1
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
		// What it reads must be there and hold its shape, as for any stage:
		// an action handed a malformed file would act on it. Not while an
		// effect is pending: the executor reconciles first, and an effect
		// that already landed needs neither the file nor the workspace.
		if reason, msg := x.checkStructuredInputs(st); reason != "" && !rs.actionPending[st.ID] {
			if err := x.append(&event.StageFailed{StageID: st.ID, Reason: reason, Error: msg}); err != nil {
				return err
			}
			return x.takeEdge(st, flow.EdgeFail, msg)
		}
	}
	started := x.e.clock()

	// Executors report routed=true when they already decided where the run
	// goes next (an on_fail or on_next edge, a terminal failure); otherwise
	// the stage completes and follows on_success.
	var routed bool
	var err error
	switch st.Action {
	case actionspec.CreateMR:
		routed, err = x.actionCreateMR(st)
	case actionspec.LoadReviewComments:
		routed, err = x.actionLoadReviewComments(st)
	case actionspec.ReplyReviewComments:
		routed, err = x.actionReplyReviewComments(st)
	case actionspec.SyncTarget:
		routed, err = x.actionSyncTarget(st)
	case actionspec.Commit:
		routed, err = x.actionCommit(st)
	case actionspec.PushBranch:
		routed, err = x.actionPushBranch(st)
	case actionspec.MRMerge:
		routed, err = x.actionMRMerge(st)
	case actionspec.PostReview:
		routed, err = x.actionPostReview(st)
	case actionspec.TicketTransition:
		routed, err = x.actionTicketTransition(st)
	case actionspec.TicketComment:
		routed, err = x.actionTicketComment(st)
	case actionspec.TicketSetField:
		routed, err = x.actionTicketSetField(st)
	case actionspec.TicketLink:
		routed, err = x.actionTicketLink(st)
	case actionspec.TicketLinkBranch:
		routed, err = x.actionTicketLinkBranch(st)
	default:
		// Unreachable: the parser validates the slug against the registry.
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra,
			Error: fmt.Sprintf("action %q has no executor in this build", st.Action)}); aerr != nil {
			return aerr
		}
		return x.fail(fmt.Sprintf("stage %s: action %q has no executor", st.ID, st.Action))
	}
	if err != nil || rs.done || routed {
		return err
	}

	if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
		return err
	}
	if x.emptyRound(st) {
		x.endPhase()
		return nil
	}
	x.advance(st)
	return nil
}

// endPhase routes straight to the phase's end (the run loop records it).
func (x *exec) endPhase() {
	x.rs.pos = len(x.rs.fl.Stages)
	x.rs.endsRun, x.rs.abortsRun = false, false
	x.rs.ph = phStart
}

// --- wait stages ------------------------------------------------------------

// stageWait is a wait point mid-chain (FLOWLANG §2): the phase parks until
// the signal holds, and the idle loop resumes it (tryResume) — at once when
// the signal already holds, since the loop evaluates before its first tick.
// A signal that can never hold for this run fails the stage now instead of
// waiting forever; a timeout is counted from the park.
func (x *exec) stageWait(st *flow.Stage) error {
	rs := x.rs
	if rs.ph != phIn {
		iter := rs.iters[st.ID] + 1
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
	}
	if sig := signalspec.Get(st.WaitFor); sig != nil && sig.Needs == signalspec.NeedsMR && len(rs.mrs) == 0 {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInputMissing,
			Error: fmt.Sprintf("cannot wait for %s: %s", st.WaitFor, waitPrereq(st.WaitFor))}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, "")
	}
	return x.parkAt(st, "")
}

func (x *exec) completeWait(st *flow.Stage, started time.Time) error {
	if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
		return err
	}
	x.advance(st)
	return nil
}

// waitPrereq explains what a run must have done before a signal can fire.
func waitPrereq(slug string) string {
	if sig := signalspec.Get(slug); sig != nil && sig.Needs == signalspec.NeedsMR {
		return "this run has opened no merge request (an action: create_mr stage must run first)"
	}
	return "its prerequisite has not happened in this run"
}

// gateQuestion is what the human is asked: the stage's question when it has
// one, else a plain fallback naming the stage. The ticket title rides along so
// a notification identifies the run.
func gateQuestion(st *flow.Stage, ticketTitle string) string {
	q := st.Question
	if q == "" {
		q = fmt.Sprintf("Gate: %s", st.ID)
	}
	if ticketTitle == "" {
		return q
	}
	return fmt.Sprintf("%s — %s", q, ticketTitle)
}

// verdictPath is where this stage's verdict must be written, or "" when its
// agent renders none. Single source for prompt assembly, the post-run read and
// crash recovery.
func (x *exec) verdictPath(st *flow.Stage) string {
	return flow.TypedPath(st, agentspec.TypeVerdict)
}

// workspaceLink is what run.workspace hands a person: the workspace's first
// app link (a port exposed at provision), "" when the environment declared
// none — the ping still renders, just without a link. The workspace itself
// is not behind a URL the log records: the run page has its terminal and
// editor, the SSH door its ssh route.
func workspaceLink(links []event.Link) string {
	for _, l := range links {
		if l.URL != "" {
			return l.URL
		}
	}
	return ""
}

// mrTitle extracts the first `# ` heading, else falls back to the ticket title (§3.1).
func mrTitle(body, ticketTitle string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if t, ok := strings.CutPrefix(line, "# "); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
	}
	return ticketTitle
}

// --- provisioning & bootstrap (§11) ----------------------------------------

func (x *exec) ensureEnv() error {
	rs := x.rs
	// Egress policy is delivered to the per-project gateway by the provisioner
	// at Provision time (from the EnvSpec egress fields), so there is nothing to
	// prime here.
	if rs.envID != "" {
		status, err := x.res.Envp.Status(x.ctx, rs.envID)
		// A container caught mid-restart reads Starting/Stopped for a moment;
		// re-probe once after a short pause before deciding to re-provision, so
		// a transient blip doesn't nuke a workspace that's just coming back up.
		// EnvAbsent is skipped — a genuinely gone container won't reappear.
		if err == nil && status != envport.EnvRunning && status != envport.EnvAbsent {
			select {
			case <-time.After(recoveryReprobeDelay):
			case <-x.ctx.Done():
				return context.Cause(x.ctx)
			}
			status, err = x.res.Envp.Status(x.ctx, rs.envID)
		}
		if err != nil {
			if x.ctx.Err() != nil {
				return context.Cause(x.ctx)
			}
			// Transient (host daemon blip, cluster unreachable): re-provisioning a
			// healthy env and re-running bootstrap on top of it would be worse
			// than waiting for the next boot.
			return fmt.Errorf("recovery: env status: %w", err)
		}
		if status == envport.EnvRunning {
			// Re-establish the reverse tunnel — it died with the previous
			// engine process, so the workspace's engine/proxy loopback
			// forwards are gone (found live: git push to 127.0.0.1:<proxy>
			// refused after an engine restart + resume).
			if r, ok := x.res.Envp.(envport.WorkspaceReattacher); ok {
				if err := r.Reattach(x.ctx, rs.envID); err != nil {
					if x.ctx.Err() != nil {
						return context.Cause(x.ctx)
					}
					return fmt.Errorf("recovery: re-establish workspace tunnel: %w", err)
				}
			}
			// Per-boot workspace state changes across restarts: the run token.
			// Refresh it so the shim re-authenticates. Egress needs no refresh —
			// the per-project gateway kept filtering across the restart.
			if err := x.writeRunEnv(rs.envID); err != nil {
				x.e.log.Warn("refreshing run.env on recovery failed", "run", rs.runID, "err", err)
			}
			// Re-seed the credentials. A resumed run re-enters a workspace the
			// terminal scrub emptied (secrets.go), so without this "try again"
			// would run with no model auth and no git token. Idempotent, so the
			// paths that never scrubbed (play, crash recovery) just rewrite the
			// same bytes.
			if err := x.reseedSecrets(rs.envID); err != nil {
				return fmt.Errorf("recovery: re-seed workspace credentials: %w", err)
			}
			// Exposures also died with the previous process; re-create them
			// (fresh ports — the live registry, not the old env_ready event,
			// is what the UI trusts).
			x.e.runPorts.AutoExpose(x.ctx, rs.runID, x.res.Envp, rs.envID)
			// Re-materialize goal + answer artifacts into the recovered
			// workspace (idempotent; the associations are durable).
			x.deliverRunAttachments()
			return nil
		}
		x.e.log.Warn("environment not running; re-provisioning — whatever was changed by hand in the old container is gone with it",
			"run", rs.runID, "env", rs.envID, "status", status)
	}
	// The first provisioning announces itself (the timeline's "provisioning
	// workspace…"); a re-provisioning after a reap does not. The run may
	// already be past pending — a remote action first in the flow runs
	// before any workspace — so the record of the request is what decides.
	if !rs.envRequested {
		if err := x.append(&event.EnvRequested{}); err != nil {
			return err
		}
	}
	// The container/workspace name derives from the branch STEM, not the
	// suffixed actual branch: base already ends where the run-id tail begins,
	// so wsname.For re-appending the tail would double it (apo-…-h1-h1).
	// Legacy runs have no base — fall back to the actual branch.
	nameBranch := rs.view.BranchBase
	if nameBranch == "" {
		nameBranch = rs.branch
	}
	spec := envport.EnvSpec{
		RunID:   rs.runID,
		RepoURL: x.res.RepoRef.URL,
		Branch:  rs.branch,
		Name:    wsname.For(nameBranch, rs.runID),
		Project: x.res.ProjectID,
		// The name is a docker label only; the id keys the gateway and caches.
		ProjectName:        x.res.ProjectName,
		EgressDomains:      x.res.EgressDomains,
		EgressMode:         x.res.EgressMode,
		EgressDenylist:     x.res.EgressDenylist,
		EgressBlocked:      x.res.EgressBlocked,
		EgressAllowedCIDRs: x.res.EgressAllowedCIDRs,
		EgressAllowedPorts: x.res.EgressAllowedPorts,
		// For clone-first provisioners (devcontainer): the engine-side
		// checkout needs the repository host credential before the env exists.
		GitUser: x.res.GitUser,
		GitPass: x.res.GitPass,
	}
	// Whatever provisioning + bootstrap print is captured on every exit past
	// this point — ready, env_failed, abort mid-build — so the log survives
	// the workspace itself (see publishProvisionLog).
	defer x.publishProvisionLog(spec.Name)
	id, err := x.res.Envp.Provision(x.ctx, spec)
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		id, err = x.res.Envp.Provision(x.ctx, spec) // §2: failed after 1 retry
	}
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		return x.append(&event.EnvFailed{Error: err.Error()})
	}
	rs.envID = id
	if err := x.bootstrap(id); err != nil {
		return err
	}
	if rs.done {
		return nil
	}
	if err := probeEgress(x.ctx, x.res.Envp, id, x.res); err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		// Kept, like a failed bootstrap's: the workspace is the evidence.
		return x.append(&event.EnvFailed{Error: err.Error(), EnvID: string(id)})
	}
	// After bootstrap (repo cloned, .apothecary/ git-excluded): deliver the
	// human's goal + answer artifacts into the workspace for the agent.
	x.deliverRunAttachments()
	// The workspace's links are its app ports: auto-expose the declared +
	// previously-exposed ones; safe to fold into env_ready because this
	// append happens on the run's own writer goroutine BEFORE the event
	// exists (out-of-band appends would race the seq and clobber run state
	// — see portrelay). The live registry stays authoritative (GET
	// /runs/{id}/ports); persisted allocations make the re-exposed URLs
	// match the pre-restart ones (best-effort). Getting INTO the workspace
	// is not a link the log records: the run page has its terminal and
	// editor, the SSH door its ssh/VS Code route (GET /runs/{id}/ssh).
	evLinks := x.e.runPorts.AutoExpose(x.ctx, x.rs.runID, x.res.Envp, id)
	return x.append(&event.EnvReady{EnvID: string(id), Links: evLinks})
}

// bootstrap runs the §11 ordered steps. A setup failure is a deterministic
// env_failed with the command's output tail (§1) — agents never repair the
// environment.
func (x *exec) writeRunEnv(id envport.EnvID) error {
	runEnv := fmt.Sprintf("APOTHECARY_RUN_ID=%s\nAPOTHECARY_RUN_TOKEN=%s\nAPOTHECARY_ENGINE_URL=%s\n",
		x.rs.runID, x.e.RunToken(x.rs.runID), x.e.engineURLFor(x.res.Envp, id))
	return x.res.Envp.WriteFile(x.ctx, id, "/apothecary/run.env", []byte(runEnv), 0o600)
}

// reseedSecrets restores the credential files in a workspace the loop is
// re-entering, undoing the terminal scrub (secrets.go) for a resumed run. It
// writes exactly what bootstrap step 2 writes — same function, so the two can
// never drift — and touches nothing else: the checkout, the branch and the
// agent's commits are already there.
func (x *exec) reseedSecrets(id envport.EnvID) error {
	res := x.res
	x.e.authSink.Set(x.rs.runID, res, id, x.redact)
	return bootstrap.WriteWorkspaceSecrets(x.ctx, res.Envp, id, bootstrap.BootstrapSpec{
		RepoURL:     res.RepoRef.URL,
		ProviderEnv: x.e.authSink.ProviderEnv(res, x.rs.runID, x.e.engineURLFor(res.Envp, id)),
		GitUser:     res.GitUser,
		GitPass:     res.GitPass,
		SeedAuth:    res.SeedAuth,
		Register:    x.registerSecret,
		Extras:      x.extraCheckouts(),
	})
}

// registerSecret teaches the run's redactor a credential the engine is about
// to write into the workspace.
func (x *exec) registerSecret(s string) { x.redact.Add(s, "harness-auth") }

func (x *exec) bootstrap(id envport.EnvID) error {
	res := x.res
	x.e.authSink.Set(x.rs.runID, res, id, x.redact)
	trees, err := x.e.skillTrees(x.ctx, x.rs.skillsets)
	if err != nil {
		err = fmt.Errorf("bootstrap skills: %w", err)
	} else {
		err = bootstrap.RunBootstrap(x.ctx, res.Envp, id, bootstrap.BootstrapSpec{
			RunID:         x.rs.runID,
			Token:         x.e.RunToken(x.rs.runID),
			EngineURL:     x.e.engineURLFor(res.Envp, id),
			RepoURL:       res.RepoRef.URL,
			SourceBranch:  bootstrap.SourceBranchOr(x.rs.sourceBranch, res.RepoRef.DefaultBranch),
			Branch:        x.rs.branch,
			Fetch:         x.primaryFetch(),
			AgentsMDPath:  res.AgentsMDPath,
			ProviderEnv:   x.e.authSink.ProviderEnv(res, x.rs.runID, x.e.engineURLFor(res.Envp, id)),
			GitUser:       res.GitUser,
			GitPass:       res.GitPass,
			GitName:       res.GitName,
			GitEmail:      res.GitEmail,
			SeedAuth:      res.SeedAuth,
			Register:      x.registerSecret,
			Prepare:       res.Prepare,
			Agents:        x.rs.fl.WorkspaceAgents(),
			Timeout:       x.rs.fl.Defaults.Timeout,
			Extras:        x.extraCheckouts(),
			Skills:        trees,
			SkillPins:     x.rs.skillsets,
			InstallSkills: res.InstallSkills,
		})
	}
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		// The workspace exists (Provision succeeded) — record its id so
		// capacity counting, archive and the reaper can destroy it.
		return x.append(&event.EnvFailed{Error: err.Error(), EnvID: string(id)})
	}
	return nil
}

// readVerdict reads and parses the stage's verdict output (§5.3 contract) at
// the path its agent declares — rebindable per stage, so never hardcoded.
func (x *exec) readVerdict(st *flow.Stage) (*Verdict, error) {
	path := x.verdictPath(st)
	if path == "" {
		return nil, fmt.Errorf("stage %s: agent %q declares no verdict output", st.ID, st.Agent)
	}
	data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("stage %s: %s missing", st.ID, path)
		}
		return nil, err
	}
	return parseVerdict(path, data)
}
