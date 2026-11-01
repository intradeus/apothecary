package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"apothecary/internal/engine/redact"
	"apothecary/internal/flow"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/platform/shell"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
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
	rs  *runcore.State
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
	// cutLeg is what the take-over in force spent in the invocation a cancel
	// cut short; heldMessages are the holder's messages a re-entry delivered
	// in the prompt of the invocation about to start.
	cutLeg       event.HoldSpend
	heldMessages int
	// lastObs is the idle loop's latest poll of the run's merge requests:
	// what a hand start picks its subject from. Nil before the first poll.
	lastObs []runcore.MRObservation
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
		if x.rs != nil && x.rs.View.State.Terminal() {
			e.ScrubWorkspaceSecrets(bg, x.rs.View)
			e.stopRunServices(bg, x.rs)
		}
	}()

	// Resolve the project's configuration. Unresolvable (project deleted,
	// pre-v2 run without one) is terminal and honest; anything else is
	// treated as transient — the run stays non-terminal for the next boot.
	if view.State.Terminal() {
		x.rs = runcore.Replay(evs, nil)
		return
	}
	res, err := e.resolveForRun(bg, view)
	if err != nil {
		x.rs = &runcore.State{RunID: runID, View: view}
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
		x.rs = &runcore.State{RunID: runID, View: view}
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
	rs := runcore.Replay(evs, fl)
	x.rs = rs
	if rs.Done {
		return
	}
	if rs.Pos < 0 {
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
	for _, op := range rs.OpenPings {
		if _, ok := e.pings[op.PingID]; !ok {
			e.pings[op.PingID] = &Ping{
				ID: op.PingID, RunID: runID, Type: op.Type, Question: op.Question,
				Context: op.Context, Files: op.Files, FileTypes: op.FileTypes, Link: op.Link, StageID: op.StageID,
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
	for !rs.Done {
		x.checkFold()
		x.h.parking.Store(false)
		if cause := context.Cause(ctx); cause != nil {
			x.exit(cause)
			return
		}
		step, st, note := rs.NextStep()
		var err error
		switch step {
		case runcore.StepIdle:
			// Between phases (PLAN6): watch the merge requests until a
			// phase starts or the run ends.
			if err := x.idle(); err != nil {
				x.exit(err)
				return
			}
			continue
		case runcore.StepGone:
			_ = x.fail(fmt.Sprintf("flow %q changed: the run's current phase no longer exists in it", fl.Name))
			return
		case runcore.StepChainOver:
			// The chain is over. After `done` the run completes, whatever
			// else is parked or armed; otherwise only the phase ends, and the
			// idle loop decides what follows — a parked phase resuming,
			// another phase opening, or the run's end (runcore.State.PlanIdle).
			end := x.phaseDone
			switch {
			case rs.AbortsRun:
				end = x.runAbort
			case rs.EndsRun:
				end = x.runDone
			}
			// What its stages started ends with the chain.
			x.stopChainServices(rs.Phase, rs.EndsRun || rs.AbortsRun)
			if err := end(); err != nil {
				x.exit(err)
				return
			}
			continue
		case runcore.StepDebug:
			// Parked on the debug leaf: a human has the workspace. Like a
			// gate this never provisions; continuing re-enters st below.
			err = x.stageDebug(st)
		case runcore.StepRouteComplete:
			err = x.routeAfterComplete(st)
		case runcore.StepRouteFail:
			err = x.routeAfterFail(st)
		case runcore.StepSkip:
			// A condition that does not hold: the stage completes as
			// skipped before anything is provisioned (FLOWLANG §2 run_if).
			err = x.skipStage(st, note)
		case runcore.StepRun:
			// Ensure the workspace lazily, right before a stage that uses it —
			// and NEVER for a stage that only parks on something outside the
			// run (a gate or a wait) or acts there (a remote tracker action
			// with no file input). Ensuring it up front blocked a run parked
			// at a gate whose workspace had been reaped: recovery tried to
			// re-provision it before re-arming the gate, so answers were
			// silently buffered and dropped (found live: rejecting a final_gate
			// returned {"status":"answered"} but nothing happened).
			if runcore.NeedsWorkspace(st) && !x.envEnsured {
				if eerr := x.ensureEnv(); eerr != nil {
					x.exit(eerr)
					return
				}
				if rs.Done { // env_failed is terminal
					return
				}
				x.envEnsured = true
				// A fresh run: before any stage spends anything, make sure the
				// agent in the workspace accepts every model the flow pins. A
				// gate, a wait or a remote action may have run already — none
				// of them had a workspace to check in.
				if !rs.AnyWorkspaceStageStarted() {
					if missing := x.checkAgentModels(); len(missing) > 0 {
						_ = x.fail("the flow pins models the workspace's agent does not offer: " +
							strings.Join(missing, "; "))
						return
					}
				}
			}
			if runcore.NeedsWorkspace(st) {
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
// pause parks it (non-terminal, skipped by Recover until play), an outcome
// takes the cursor, shutdown leaves it for Recover, anything else is logged
// and left non-terminal (recovered by replay on next boot).
func (x *exec) exit(cause error) {
	defer x.checkFold()
	cause = x.leave(cause)
	var fired outcomeFired
	switch {
	case errors.As(cause, &fired):
		x.takeCursor(fired)
	case errors.Is(cause, errAborted):
		// A deliberate stop is abandonment, not failure (PLAN5 M24) — the
		// same terminal the flow's own `abort` edge reaches.
		x.endHolds()
		_ = x.append(&event.RunAbandoned{StalledAt: x.rs.View.CurrentStage, Reason: "aborted by user"})
	case errors.Is(cause, errPaused):
		_ = x.append(&event.RunPaused{StageID: x.rs.View.CurrentStage, Reason: event.PauseUser})
	case errors.Is(cause, errWaitingOnHuman):
		// Parked on a human, not stopped by one: the run keeps reading
		// waiting_on_human, gives its concurrency slot back, and comes alive
		// again when the answer lands (Engine.AnswerPing).
		_ = x.append(&event.RunPaused{StageID: x.rs.View.CurrentStage, Reason: event.PauseWaiting})
	case errors.Is(cause, errShutdown), cause == nil:
	default:
		x.e.log.Error("run loop error; run left non-terminal for recovery",
			"run", x.rs.RunID, "err", cause)
	}
}

// endHolds ends the take-overs in force, on the record: their stage is over.
func (x *exec) endHolds() {
	for id, h := range x.rs.Holds {
		var spent event.HoldSpend
		spent.Add(h.Banked)
		spent.Add(x.cutLeg)
		_ = x.append(&event.HoldReleased{StageID: id, Iteration: x.rs.Iters[id], UserID: h.Taken.UserID,
			Reason: event.HoldEndedWithStage, DurationMS: max(x.e.clock().Sub(h.TakenAt).Milliseconds(), 0), HoldSpend: spent})
	}
}

// leave closes the loop to every verb, for good. A loop parking on a human
// reads its cancel cause only here: a pause or an abort that landed first is
// how it exits, and one that lands after sees it leaving and acts on the
// parked run instead (cancelLoop).
func (x *exec) leave(cause error) error {
	x.h.parking.Store(true)
	x.e.mu.Lock()
	x.h.leaving = true
	x.e.mu.Unlock()
	if errors.Is(cause, errWaitingOnHuman) {
		if c := context.Cause(x.ctx); c != nil {
			return c
		}
	}
	return cause
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
	event.Clean(ps...)
	unlock := x.e.appendLock(x.rs.RunID)
	evs, err := x.e.store.Append(x.bg, x.rs.RunID, ps...)
	unlock()
	if err != nil {
		return fmt.Errorf("append: %w", err)
	}
	for i := range evs {
		x.rs.Apply(&evs[i])
	}
	x.e.emit(evs)
	return nil
}

func (x *exec) complete() error {
	rs := x.rs
	return x.append(&event.RunCompleted{
		DurationMS:    x.e.clock().Sub(rs.View.CreatedAt).Milliseconds(),
		TotalCostUSD:  rs.Cost,
		TotalTurns:    rs.Turns,
		UnwantedPings: rs.Unwanted,
	})
}

func (x *exec) abandon(stalledAt, edgeKind, reason string) error {
	return x.append(&event.RunAbandoned{StalledAt: stalledAt, Edge: edgeKind, Reason: reason})
}

func (x *exec) fail(reason string) error {
	return x.append(&event.RunFailed{Reason: reason})
}

// takeEdge follows a recoverable edge of the given kind (on_fail,
// on_reject, on_timeout — the registry lives in the flow package) to the
// link ChooseEdge picks, and records it. A link targeting end finishes the
// phase, done the run, abort abandons the run; an absent edge or an
// exhausted chain is a dead end (deadEnd: the run is abandoned in the main
// chain or an outcome, a watcher's round ends — §3).
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
	ch := rs.ChooseEdge(st, kind)
	switch ch.Kind {
	case runcore.EdgeNone:
		// A rejection is a verdict, not a breakage: saying "unrecoverable
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
	case runcore.EdgeExhausted:
		return x.deadEnd(st, kind, fmt.Sprintf("%s from %s exhausted (max %s)", kind, st.ID, chainMax(ch.Edge)))
	case runcore.EdgeAbort:
		reason := fmt.Sprintf("aborted by flow: %s at stage %s", kind, st.ID)
		if strings.TrimSpace(feedContent) != "" {
			reason += ": " + shell.Tail(strings.TrimSpace(feedContent), 2<<10)
		}
		return x.abandon(st.ID, kind, reason)
	case runcore.EdgeEnd:
		// The phase ends here — a best-effort stage whose failure is not
		// worth routing anywhere — or, with done, the run (FLOWLANG §3).
		// Recorded like any link.
		return x.append(&event.EdgeTaken{From: st.ID, To: ch.Edge.To, Edge: kind, Depth: ch.Depth,
			Feed: feedTail(feedContent)})
	case runcore.EdgeDebug:
		// The debug leaf: park here, on this stage, for a human with the
		// workspace (stageDebug). Counted like any link — the timeline
		// shows n/∞ — but never exhausted: the human decides when to stop.
		return x.append(&event.EdgeTaken{From: st.ID, To: flow.ReservedDebug, Edge: kind, Count: ch.Count,
			Max: flow.UnlimitedMax, Depth: ch.Depth, Feed: feedTail(feedContent)})
	}
	// The content always rides along; whether the target renders it is the
	// target's choice (it binds edge.verdict or edge.failure, or it does not).
	// Persisted in the event so a crash between edge_taken and the next
	// stage_started loses nothing.
	return x.append(&event.EdgeTaken{From: st.ID, To: ch.Edge.To, Edge: kind, Count: ch.Count, Max: ch.Edge.Max,
		Depth: ch.Depth, Feed: feedTail(feedContent)})
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

// checkBudget abandons the run when §3 budget ceilings are exceeded.
// Returns true when the run should stop.
func (x *exec) checkBudget(st *flow.Stage) (bool, error) {
	if over, reason := runcore.BudgetExceeded(x.rs.Flow.Budget, x.rs.Cost, x.rs.Turns); over {
		return true, x.abandon(st.ID, "", reason)
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
	if x.rs.LastFailure != nil {
		feed = x.rs.LastFailure.Error
		kind = runcore.FailEdgeKind(st, x.rs.LastFailure.Reason)
	}
	return x.takeEdge(st, kind, feed)
}

// routeAfterComplete handles recovery when the crash hit between a
// stage_completed and its routing: no re-execution, only the decision.
func (x *exec) routeAfterComplete(st *flow.Stage) error {
	if x.emptyRound(st) {
		x.rs.EndPhase()
		return nil
	}
	var v *Verdict
	if st.Type == flow.StageAgent && x.verdictPath(st) != "" && !x.rs.Elided[st.ID] {
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

// afterComplete routes a completed stage — live, or recovered after a crash
// between its stage_completed and its routing: the verdict is recorded (once:
// a recovered stage may have it on the log already), an agent stage's spend
// is held to the budget, then a rejection takes on_reject and anything else
// on_success, carrying an approval's verdict. A skipped stage spent nothing
// and judged nothing: it goes straight on.
func (x *exec) afterComplete(st *flow.Stage, verdict *Verdict) error {
	if x.rs.Elided[st.ID] {
		x.rs.RouteSuccess(st, x.rs.CompletedFeed)
		return nil
	}
	if verdict != nil && !x.rs.VerdictRecorded {
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
	x.rs.RouteSuccess(st, x.rs.CompletedFeed)
	return nil
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

// verdictPath is where this stage's verdict must be written, or "" when its
// agent renders none. Single source for prompt assembly, the post-run read and
// crash recovery.
func (x *exec) verdictPath(st *flow.Stage) string {
	return flow.TypedPath(st, agentspec.TypeVerdict)
}

// readVerdict reads and parses the stage's verdict output (§5.3 contract) at
// the path its agent declares — rebindable per stage, so never hardcoded.
func (x *exec) readVerdict(st *flow.Stage) (*Verdict, error) {
	path := x.verdictPath(st)
	if path == "" {
		return nil, fmt.Errorf("stage %s: agent %q declares no verdict output", st.ID, st.Agent)
	}
	data, err := x.res.Envp.ReadFile(x.ctx, x.rs.EnvID, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("stage %s: %s missing", st.ID, path)
		}
		return nil, err
	}
	return parseVerdict(path, data)
}
