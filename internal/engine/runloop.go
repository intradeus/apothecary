package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/run"
	"apothecary/internal/signalspec"
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

// exec bundles the per-run goroutine's context so stage executors stay short.
// bg survives cancellation for terminal event appends. res is the project's
// resolved configuration (DESIGN2 §6.1), held for the run's whole life —
// config edits apply to new runs only.
type exec struct {
	e   *Engine
	rs  *runState
	h   *runHandle
	res *Resolution
	ctx context.Context
	bg  context.Context
	// envEnsured records that ensureEnv ran (and succeeded) once this loop
	// invocation, so it is done lazily before the first stage that needs the
	// workspace — never for a human_gate.
	envEnsured bool
	// redact scrubs engine-injected secrets out of everything that leaves the
	// workspace (PLAN5 M21). Built once per loop from the resolution.
	redact *redactor
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
		if errors.Is(err, ErrUnresolvable) {
			_ = x.append(&event.RunFailed{Reason: err.Error()})
		} else {
			e.log.Error("run loop: resolution failed; run left non-terminal for recovery", "run", runID, "err", err)
		}
		return
	}
	defer res.release()
	x.res = res
	x.redact = newRedactor(res, e.RunToken(runID))

	fl, err := e.pinnedFlow(bg, view.ProjectID, view.FlowName, view.FlowHash)
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
			_ = x.append(&event.RunFailed{Reason: fmt.Sprintf("flow %q no longer parses: %v", view.FlowName, err)})
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
				Options: op.Options, AnswerKind: op.Kind(), key: op.Key,
				RaisedAt: e.clock(), done: make(chan struct{}),
			}
		}
	}
	e.mu.Unlock()

	if err := x.entryPhaseStarted(); err != nil {
		x.exit(err)
		return
	}
	for !rs.done {
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
			// done: a phase ends (the run goes idle, or completes when there
			// is no merge request to react to); a flow without phases ends.
			if fl.Phased() {
				if err := x.phaseDone(); err != nil {
					x.exit(err)
					return
				}
				continue
			}
			if err := x.complete(); err != nil {
				x.exit(err)
			}
			return
		}
		st := &fl.Stages[rs.pos]
		var err error
		if rs.ph == phAfterComplete {
			err = x.routeAfterComplete(st)
		} else if rs.ph == phAfterFail && st.Type != flow.StageAction {
			err = x.routeAfterFail(st)
		} else {
			// Ensure the workspace lazily, right before a stage that uses it —
			// and NEVER for a stage that only parks on something outside the
			// run (a gate or a wait). Ensuring it up front blocked a run parked
			// at a gate whose workspace had been reaped: recovery tried to
			// re-provision it before re-arming the gate, so answers were
			// silently buffered and dropped (found live: rejecting a final_gate
			// returned {"status":"answered"} but nothing happened).
			if st.Type != flow.StageHumanGate && st.Type != flow.StageWait && !x.envEnsured {
				if eerr := x.ensureEnv(); eerr != nil {
					x.exit(eerr)
					return
				}
				if rs.done { // env_failed is terminal
					return
				}
				x.envEnsured = true
				// A fresh run: before any stage spends anything, make sure the
				// agent in the workspace accepts every model the flow pins.
				if !rs.anyStageStarted() {
					if missing := x.checkAgentModels(); len(missing) > 0 {
						_ = x.fail("the flow pins models the workspace's agent does not offer: " +
							strings.Join(missing, "; "))
						return
					}
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
	switch {
	case errors.Is(cause, errAborted):
		// A deliberate stop is abandonment, not failure (PLAN5 M24) — the
		// same terminal the flow's own `abort` edge reaches.
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

// append writes events through the single-writer goroutine, keeps the
// in-memory view current, and feeds the telemetry sink (§7).
//
// Every payload passes through the redactor first: this is the one place
// workspace output becomes durable (and reaches prompts, gates and the API),
// so scrubbing here covers every producer at once rather than trusting each
// call site to remember (PLAN5 M21).
func (x *exec) append(ps ...event.Payload) error {
	for _, p := range ps {
		switch v := p.(type) {
		case *event.StageFailed:
			v.Error = x.redact.scrub(v.Error)
		case *event.EdgeTaken:
			v.Feed = x.redact.scrub(v.Feed)
		case *event.EnvFailed:
			v.Error = x.redact.scrub(v.Error)
		case *event.RunAbandoned:
			v.Reason = x.redact.scrub(v.Reason)
		case *event.RunFailed:
			v.Reason = x.redact.scrub(v.Reason)
		// The transcript is workspace output verbatim — an agent that cats
		// .env or echoes a token puts it here first.
		case *event.AgentPrompted:
			v.Text = x.redact.scrub(v.Text)
		case *event.AgentMessage:
			v.Text = x.redact.scrub(v.Text)
		case *event.AgentTool:
			v.Title = x.redact.scrub(v.Title)
			v.Input = x.redact.scrub(v.Input)
			v.Output = x.redact.scrub(v.Output)
			for i := range v.Diffs {
				v.Diffs[i].OldText = x.redact.scrub(v.Diffs[i].OldText)
				v.Diffs[i].NewText = x.redact.scrub(v.Diffs[i].NewText)
			}
		case *event.AgentPlan:
			for i := range v.Entries {
				v.Entries[i].Content = x.redact.scrub(v.Entries[i].Content)
			}
		case *event.AgentNotice:
			v.Text = x.redact.scrub(v.Text)
		case *event.MessageQueued:
			v.Text = x.redact.scrub(v.Text)
		}
	}
	evs, err := x.e.store.Append(x.bg, x.rs.runID, ps...)
	if err != nil {
		return fmt.Errorf("append: %w", err)
	}
	for i := range evs {
		run.Apply(&x.rs.view, &evs[i])
	}
	x.e.emit(evs)
	return nil
}

// advance follows the stage's explicit on_success target (DESIGN3 §2.1):
// done parks pos past the end, which the run loop reads as completion.
func (x *exec) advance(st *flow.Stage) {
	if st.OnSuccess == flow.TerminalDone {
		x.rs.pos = len(x.rs.fl.Stages)
	} else {
		x.rs.pos = x.rs.fl.Index(st.OnSuccess) // parse-validated: always >= 0
	}
	x.rs.ph = phStart
	x.rs.feed = ""
	x.rs.feedSentinel = ""
	x.rs.feedAttempts = 0
}

func (x *exec) complete() error {
	rs := x.rs
	rs.done = true
	return x.append(&event.RunCompleted{
		DurationMS:    x.e.clock().Sub(rs.view.CreatedAt).Milliseconds(),
		TotalCostUSD:  rs.cost,
		TotalTurns:    rs.turns,
		UnwantedPings: rs.unwanted,
	})
}

func (x *exec) abandon(stalledAt, edgeKind, reason string) error {
	x.rs.done = true
	return x.append(&event.RunAbandoned{StalledAt: stalledAt, Edge: edgeKind, Reason: reason})
}

func (x *exec) fail(reason string) error {
	x.rs.done = true
	return x.append(&event.RunFailed{Reason: reason})
}

// takeEdge follows a recoverable edge of the given kind (on_fail,
// on_reject, on_timeout — the registry lives in the flow package). The
// edge is a chain: each link carries its own cumulative max, and a full
// link falls through to its then fallback (retry-then-escalate). A link
// targeting the abort terminal stops the run deliberately; an absent edge
// or an exhausted chain abandons it (§2, §3).
func (x *exec) takeEdge(st *flow.Stage, kind, feedContent string) error {
	rs := x.rs
	// The feed becomes an abandon reason and rs.feed (which an agent reads as
	// $FAILURE/$DECISION), neither of which passes through append's scrub.
	feedContent = x.redact.scrub(feedContent)
	edge := st.Edge(kind)
	if edge == nil {
		// A rejection is a decision, not a breakage: saying "unrecoverable
		// failure" about a human (or a signal) deliberately saying no would
		// misreport the run's history.
		reason := fmt.Sprintf("stage %s: unrecoverable failure with no %s", st.ID, kind)
		if kind == flow.EdgeReject {
			reason = fmt.Sprintf("stage %s was rejected and declares no %s", st.ID, kind)
			if strings.TrimSpace(feedContent) != "" {
				reason += ": " + tail(strings.TrimSpace(feedContent), 2<<10)
			}
		}
		return x.abandon(st.ID, "", reason)
	}
	depth := 0
	for e := edge; e != nil; e, depth = e.Then, depth+1 {
		if e.To == flow.TerminalAbort {
			reason := fmt.Sprintf("aborted by flow: %s at stage %s", kind, st.ID)
			if strings.TrimSpace(feedContent) != "" {
				reason += ": " + tail(strings.TrimSpace(feedContent), 2<<10)
			}
			return x.abandon(st.ID, kind, reason)
		}
		key := chainKey(st.ID, kind, depth)
		count := rs.edgeCounts[key] + 1
		if e.Max != flow.UnlimitedMax && count > e.Max {
			continue // this link is exhausted; fall through to then
		}
		// The content always rides along; whether the target renders it is the
		// target's decision (it binds $DECISION or $FAILURE, or it does not).
		// Persisted in the event so a crash between edge_taken and the next
		// stage_started loses nothing.
		feed := tail(feedContent, 8<<10)
		if err := x.append(&event.EdgeTaken{From: st.ID, To: e.To, Edge: kind, Count: count, Max: e.Max, Depth: depth, Feed: feed}); err != nil {
			return err
		}
		rs.edgeCounts[key] = count
		rs.pos = rs.fl.Index(e.To)
		rs.ph = phStart
		rs.feed = feed
		// Which sentinel the content fills follows from the edge kind, so
		// replay derives it from the persisted event rather than storing it.
		rs.feedSentinel = flow.FeedSentinel(kind)
		rs.feedAttempts = e.Max + 1
		return nil
	}
	return x.abandon(st.ID, kind, fmt.Sprintf("%s from %s exhausted (max %s)", kind, st.ID, chainMax(edge)))
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
	x.rs.ph = phStart
	return x.takeEdge(st, kind, feed)
}

// routeAfterComplete handles recovery when the crash hit between a
// stage_completed and its routing: no re-execution, only the decision.
func (x *exec) routeAfterComplete(st *flow.Stage) error {
	if x.emptyRound(st) {
		x.endPhase()
		return nil
	}
	if st.Type == flow.StageAgent && x.verdictPath(st) != "" {
		v, err := x.readVerdict(st)
		if err != nil {
			if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputContract, Error: err.Error()}); err != nil {
				return err
			}
			return x.takeEdge(st, flow.EdgeFail, "")
		}
		if !v.Approve {
			return x.takeEdge(st, flow.EdgeReject, strings.Join(v.Comments, "\n"))
		}
	}
	x.advance(st)
	return nil
}

// --- agent stages -----------------------------------------------------------

type agentRes struct {
	r   ports.AgentResult
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
}

// invIO is what flows between a running invocation and the loop: the
// agent's transcript events, the human's messages, and the transcript they
// are recorded into.
type invIO struct {
	events chan ports.AgentEvent
	done   chan struct{} // closed when the loop stops listening
	inbox  *inbox
	tr     *transcript
}

func newInvIO(tr *transcript) *invIO {
	return &invIO{events: make(chan ports.AgentEvent, 256), done: make(chan struct{}), inbox: newInbox(), tr: tr}
}

func (io *invIO) input(prompt string) ports.AgentInput {
	return ports.AgentInput{
		Prompt: prompt,
		Inbox:  io.inbox,
		// Blocks while the loop is listening (ordering: everything the
		// runner emitted is queued before its result is), never after.
		Events: func(ev ports.AgentEvent) {
			select {
			case io.events <- ev:
			case <-io.done:
			}
		},
	}
}

// drain records whatever events are already queued.
func (io *invIO) drain() error {
	for {
		select {
		case ev := <-io.events:
			if err := io.tr.handle(ev); err != nil {
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
	call func(ctx context.Context, in ports.AgentInput) (ports.AgentResult, error)) (invocation, error) {
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
		rs.iters[st.ID] = iter
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter, Harness: st.Harness, Model: st.Model,
			Agent: st.Agent, AgentHash: x.rs.fl.AgentHash(st.Agent)}); err != nil {
			return err
		}
		delete(rs.pausedAsks, st.ID) // a fresh attempt is parked on nothing
		// A fresh attempt starts from a clean slate, so the output contract
		// below cannot be satisfied by an earlier attempt's file (M18).
		x.clearOutputs(flow.EffectiveOutputs(st, rs.fl.Agent(st.Agent)))
	}
	rs.ph = phIn
	started := x.e.clock()

	prompt := x.assemblePrompt(st, iter)
	runner := x.res.runnerFor(st.Harness) // per-step harness (default when unpinned)
	tr := x.newTranscript(st.ID, iter)
	spec := ports.AgentSpec{Agent: st.Agent, Harness: st.Harness, Model: st.Model, MaxTurns: st.MaxTurns}

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
	reply, replyKind := "", ""
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

		if crashed && haveSession {
			crashed = false
			// The engine restarted mid-attempt. The agent process lives in
			// the workspace and may have outlived it: follow the turn still
			// running (or the one that finished while nobody listened)
			// instead of starting another.
			if ra, ok := runner.(ports.AgentReattacher); ok && ra.Reattachable(x.ctx, rs.envID, session, rs.agentCursor[st.ID]) {
				if err := x.append(&event.StageResumed{StageID: st.ID, Iteration: iter, Reason: event.ResumeRestart,
					SessionID: string(session)}); err != nil {
					return err
				}
				var err error
				inv, err = x.invoke(st, budget, tr, x.liveAsks(st), "",
					func(ctx context.Context, in ports.AgentInput) (ports.AgentResult, error) {
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
				if stop, err := x.pauseAttempt(st, iter, invocation{open: live, res: agentRes{r: ports.AgentResult{Session: session}}}); stop || err != nil {
					return err
				}
				asks, reply = x.pausedAsks(st), ""
				continue
			}
		}
		crashed = false

		resuming := reply != "" && haveSession
		if resuming {
			reason := event.ResumeAnswered
			if len(asks) == 0 {
				reason = event.ResumeRestart
			}
			if err := x.append(&event.StageResumed{StageID: st.ID, Iteration: iter, Reason: reason,
				PingIDs: askIDs(asks), SessionID: string(session)}); err != nil {
				return err
			}
			delete(rs.pausedAsks, st.ID) // consumed
		}
		asks = nil

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
			func(ctx context.Context, in ports.AgentInput) (ports.AgentResult, error) {
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
	x.publishLog(st.ID, iter, res.r.Output)
	// Capture whatever this attempt produced BEFORE routing: a failed
	// attempt's partial outputs are the interesting ones (M27).
	x.publishOutputs(st, iter, flow.EffectiveOutputs(st, rs.fl.Agent(st.Agent)))
	if inv.outcome == outcomeTimedOut {
		if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonTimeout}); err != nil {
			return err
		}
		// Whatever the cancelled attempt printed rides along for edges that
		// declare feed: output.
		return x.takeEdge(st, failEdgeKind(st, event.ReasonTimeout), tail(res.r.Output, 8<<10))
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
		return x.takeEdge(st, flow.EdgeFail, tail(res.r.Output, 8<<10))
	}

	// Output contract (§3): every effective output (decl defaults overlaid
	// with stage rebinds) must exist. All missing paths are reported in one
	// failure so a human sees the whole gap, not the first file only.
	if missing := x.missingPaths(flow.EffectiveOutputs(st, x.rs.fl.Agent(st.Agent))); len(missing) > 0 {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputContract,
			Error: fmt.Sprintf("declared output(s) not readable: %s", strings.Join(missing, ", "))}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, "")
	}
	// A verdict-typed output had its existence checked with every other output
	// above, so this read can now only fail on shape — one report per cause.
	var verdict *ports.Verdict
	if x.verdictPath(st) != "" {
		v, err := x.readVerdict(st)
		if err != nil {
			// The file exists (checked above) but does not hold what its type
			// promises — a different fault from "never wrote it" (M18).
			if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputInvalid, Error: err.Error()}); aerr != nil {
				return aerr
			}
			return x.takeEdge(st, flow.EdgeFail, "")
		}
		verdict = v
	}
	// A review_replies output is checked against the threads this round
	// loaded before anything is posted: a reply to an unknown thread is this
	// stage's fault, retried with the reason as $FAILURE.
	if msg := x.checkReviewReplies(st); msg != "" {
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
	if res.r.Usage != (ports.Usage{}) {
		completed.Usage = &event.Usage{
			InputTokens:  res.r.Usage.InputTokens,
			OutputTokens: res.r.Usage.OutputTokens,
			CostUSD:      res.r.Usage.CostUSD,
		}
	}
	if err := x.append(completed); err != nil {
		return err
	}
	if res.r.Session != "" {
		rs.sessions[st.ID] = res.r.Session
	}
	rs.cost += res.r.Usage.CostUSD
	rs.turns += res.r.TurnsUsed

	if verdict != nil {
		if err := x.append(&event.VerdictReceived{StageID: st.ID, Approve: verdict.Approve, NComments: len(verdict.Comments)}); err != nil {
			return err
		}
	}
	if stop, err := x.checkBudget(st); stop || err != nil {
		return err
	}
	if verdict != nil && !verdict.Approve {
		return x.takeEdge(st, flow.EdgeReject, strings.Join(verdict.Comments, "\n"))
	}
	x.advance(st)
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
	paused := &event.StagePaused{StageID: st.ID, Iteration: iter, Reason: event.PauseAskHuman,
		PingIDs: pingIDs(inv.open), TurnsUsed: inv.res.r.TurnsUsed, SessionID: string(inv.res.r.Session)}
	if inv.res.r.Usage != (ports.Usage{}) {
		paused.Usage = &event.Usage{InputTokens: inv.res.r.Usage.InputTokens,
			OutputTokens: inv.res.r.Usage.OutputTokens, CostUSD: inv.res.r.Usage.CostUSD}
	}
	if err := x.append(paused); err != nil {
		return false, err
	}
	if inv.res.r.Session != "" {
		rs.sessions[st.ID] = inv.res.r.Session
	}
	rs.cost += inv.res.r.Usage.CostUSD
	rs.turns += inv.res.r.TurnsUsed
	// A question-per-turn loop would otherwise never meet a budget check:
	// nothing else bounds an attempt that keeps asking.
	if stop, err := x.checkBudget(st); stop || err != nil {
		return true, err
	}
	rs.pausedAsks[st.ID] = pingIDs(inv.open)
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
	msgs := make([]ports.UserMessage, 0, len(x.rs.pendingMsgs))
	ids := make([]string, 0, len(x.rs.pendingMsgs))
	for _, m := range x.rs.pendingMsgs {
		msgs = append(msgs, ports.UserMessage{ID: m.MessageID, Text: m.Text})
		ids = append(ids, m.MessageID)
	}
	if err := x.append(&event.MessageDelivered{MessageIDs: ids, StageID: st.ID, Iteration: iter}); err != nil {
		return text, err
	}
	x.rs.delivered(ids)
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
		if len(open) > 0 {
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
				if err := io.drain(); err != nil {
					return invocation{}, err
				}
				return invocation{res: res, outcome: out, open: open, left: left()}, nil
			case ev := <-io.events:
				if err := io.tr.handle(ev); err != nil {
					return invocation{}, err
				}
			case <-grace.C:
				x.e.log.Warn("harness ignored cancellation; abandoning attempt", "run", x.rs.runID, "stage", st.ID)
				return invocation{outcome: out, open: open, left: left()}, nil
			}
		}
	}

	for {
		select {
		case res := <-resCh:
			if err := io.drain(); err != nil {
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
			return invocation{res: res, outcome: out, open: open, left: left()}, nil
		case ev := <-io.events:
			if ev.Kind == ports.AgentEvSession && ev.Text != "" && x.rs.sessions[st.ID] != ports.SessionID(ev.Text) {
				// Recorded the moment it exists, so a crash mid-invocation
				// resumes (or reattaches to) this session, not a new one.
				if err := x.append(&event.AgentSession{StageID: st.ID, Iteration: io.tr.iter, SessionID: ev.Text}); err != nil {
					return invocation{}, err
				}
				x.rs.sessions[st.ID] = ports.SessionID(ev.Text)
				continue
			}
			if err := io.tr.handle(ev); err != nil {
				return invocation{}, err
			}
		case req := <-x.h.messages:
			q := &event.MessageQueued{MessageID: req.msg.ID, Text: req.msg.Text, Interrupt: req.msg.Interrupt, StageID: st.ID}
			if err := x.append(q); err != nil {
				req.ack <- err
				return invocation{}, err
			}
			x.rs.pendingMsgs = append(x.rs.pendingMsgs, q)
			io.inbox.push(req.msg)
			req.ack <- nil
		case req := <-x.h.mailbox:
			x.e.mu.Lock()
			req.p.StageID = st.ID // the stage whose agent asked (PLAN4 M15)
			x.e.mu.Unlock()
			raised := &event.PingRaised{PingID: req.p.ID, Type: req.p.Type,
				Question: req.p.Question, Context: req.p.Context, Key: req.p.key,
				Files: req.p.Files, Link: req.p.Link, StageID: st.ID, Options: req.p.Options, AnswerKind: req.p.AnswerKind}
			if err := x.append(raised); err != nil {
				close(req.ack)
				return invocation{}, err
			}
			// Keep the working state a mirror of what replay would build, so
			// a resume reads the same whether the answer arrived in this
			// process or was found in the log after a restart.
			x.rs.raisedPings[raised.PingID] = raised
			// Snapshot the attached files as they are RIGHT NOW: the agent
			// resumes the moment this ping is answered, and the record must
			// keep what the human was actually shown.
			x.publishPingFiles(req.p, st.ID)
			x.rs.unwanted++
			x.e.notify(x.bg, ports.Notification{
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
			// Nobody answered while the turn stayed live: park. The open
			// questions are handed back first, so an answer racing the
			// cancel is delivered by resume rather than lost in the tool call.
			parkC = nil
			for _, p := range open {
				p.handedBack.Store(true)
			}
			cancelRun(errParked)
			return collect(outcomePaused)
		case <-timer.C:
			cancelRun(errStageTimeout)
			return collect(outcomeTimedOut)
		case <-x.ctx.Done():
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

	// §6: free-text agent (ask_human) answers are recorded as answer:"text";
	// every other kind keeps the choice in answer and the note in feedback.
	answer, feedback := recordedAnswer(ar.p.AnswerKind, ar.ans)
	recorded := &event.PingAnswered{PingID: ar.p.ID, Answer: answer, Feedback: feedback,
		AttachmentIDs: ar.ans.AttachmentIDs}
	if err := x.append(recorded); err != nil {
		// Roll the in-memory claim back so a retried answer can succeed
		// after recovery (the event log has no ping_answered).
		x.e.mu.Lock()
		ar.p.answered = false
		ar.p.answer = nil
		x.e.mu.Unlock()
		return false, err
	}
	x.rs.answeredPings[recorded.PingID] = recorded
	// Deliver any files the human attached to this answer into the workspace,
	// recording their paths so the ask_human tool-return (agent pings) and the
	// gate feed (gate pings) can point the agent at them.
	if paths := x.pingAnswerPaths(ar.p.ID); len(paths) > 0 {
		x.e.mu.Lock()
		ar.p.deliveredPaths = paths
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
		rs.iters[st.ID] = iter
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
	}
	rs.ph = phIn
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
	// In a reaction phase, in the subject's checkout (PLAN6 R12): the phase
	// acts on that merge request's branch only.
	dirs := []string{event.PrimaryCheckoutDir}
	if co := x.subjectCheckout(); co != nil {
		dirs = []string{co.Dir}
	} else if st.EachRepo {
		dirs = rs.checkoutDirs()
	}
	var res ports.ExecResult
	var err error
	var combined strings.Builder
	for _, dir := range dirs {
		cctx, cancel := context.WithTimeout(x.ctx, timeout)
		res, err = x.res.Envp.Exec(cctx, rs.envID, ports.Command{Script: script, Dir: dir, Timeout: timeout})
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
			ExitCode: &code, Error: tail(res.Output, 8<<10)}); err != nil {
			return err
		}
		return x.takeEdge(st, flow.EdgeFail, tail(res.Output, 8<<10))
	}
	// A command that exits 0 without writing what it declared has not done its
	// job — the same contract agent stages carry.
	if missing := x.missingPaths(st.Outputs); len(missing) > 0 {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputContract,
			Error: fmt.Sprintf("declared output(s) not readable: %s", strings.Join(missing, ", "))}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, tail(res.Output, 8<<10))
	}
	if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
		return err
	}
	x.advance(st)
	return nil
}

// missingPaths returns the bindings' paths that cannot be read in the
// workspace, in declaration order. Sentinels are engine-provided, never files.
// writtenOutputs reports which of a stage's declared outputs already exist —
// used when resuming a paused attempt, to tell an agent that answered its own
// question by guessing exactly which files it now has to revisit.
func (x *exec) writtenOutputs(st *flow.Stage) []string {
	var written []string
	for _, b := range flow.EffectiveOutputs(st, x.rs.fl.Agent(st.Agent)) {
		if b.Value == "" || flow.IsSentinel(b.Value) {
			continue
		}
		if _, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, b.Value); err == nil {
			written = append(written, b.Value)
		}
	}
	return written
}

func (x *exec) missingPaths(bindings []flow.IOBinding) []string {
	var missing []string
	for _, b := range bindings {
		if b.Value == "" || flow.IsSentinel(b.Value) {
			continue
		}
		if _, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, b.Value); err != nil {
			missing = append(missing, b.Value)
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
func (x *exec) clearOutputs(bindings []flow.IOBinding) {
	var paths []string
	for _, b := range bindings {
		if b.Value == "" || flow.IsSentinel(b.Value) {
			continue
		}
		paths = append(paths, shellQuote(b.Value))
	}
	if len(paths) == 0 {
		return
	}
	// Best-effort: a workspace that cannot delete is not a stage failure —
	// the output contract still has the final say.
	if _, err := x.res.Envp.Exec(x.ctx, x.rs.envID, ports.Command{
		Script:  "rm -f -- " + strings.Join(paths, " "),
		Dir:     "repo",
		Timeout: time.Minute,
	}); err != nil {
		x.e.log.Warn("clearing declared outputs failed", "run", x.rs.runID, "err", err)
	}
}

// --- human gates ------------------------------------------------------------

func (x *exec) stageGate(st *flow.Stage) error {
	rs := x.rs

	// Recovery: answered pre-crash but routing unrecorded.
	if rs.ph == phIn && rs.gateAnswer != nil {
		ans := rs.gateAnswer
		rs.gateAnswer = nil
		return x.routeGate(st, PingAnswer{Answer: ans.Answer, Feedback: ans.Feedback}, x.e.clock())
	}

	var p *Ping
	if rs.ph == phIn {
		if op := rs.openGatePing(st.ID); op != nil {
			x.e.mu.Lock()
			p = x.e.pings[op.PingID]
			x.e.mu.Unlock()
		}
	}
	started := x.e.clock()
	if p == nil {
		iter := rs.iters[st.ID] + 1
		rs.iters[st.ID] = iter
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
		rs.ph = phIn
		// The gate's input: an artifact to review (a File) or a sentinel the
		// engine resolves to a Link. TYPED HERE, at sentinel resolution — this
		// is the one place that knows which is which, so nothing downstream
		// ever sniffs a string for "http" (DESIGN-FILES §4).
		var files []string
		var link string
		if len(st.Inputs) > 0 {
			if v := st.Inputs[0].Value; flow.IsSentinel(v) {
				link = x.resolveGateInput(v)
			} else {
				files = []string{v}
			}
		}
		p = &Ping{
			ID: x.e.newID(), RunID: rs.runID, Type: PingGate,
			Question: gateQuestion(st, rs.view.TicketTitle),
			// A gate over several merge requests lists them all; the link
			// stays the first (the primary repository's).
			Context: x.mrListMarkdown(),
			Files:   files, Link: link, StageID: st.ID, RaisedAt: x.e.clock(),
			AnswerKind: event.AnswerDecision, done: make(chan struct{}),
		}
		x.e.mu.Lock()
		x.e.pings[p.ID] = p
		x.e.mu.Unlock()
		if err := x.append(&event.PingRaised{PingID: p.ID, Type: p.Type, Question: p.Question, Context: p.Context,
			Files: p.Files, Link: p.Link, StageID: st.ID, AnswerKind: event.AnswerDecision}); err != nil {
			return err
		}
		// The plan a human approves is the single most important document in
		// the run — snapshot it as reviewed, not as the workspace has it later.
		x.publishPingFiles(p, st.ID)
		x.e.notify(x.bg, ports.Notification{
			RunID: rs.runID, PingID: p.ID, Type: p.Type,
			Title: p.Question, Body: p.Primary(),
		})
	}

	// auto_approve / auto_reject (§6): a signal answers the gate on the human's
	// behalf — "they merged it instead of clicking approve", "they closed it,
	// which is a no". The gate keeps accepting a manual answer the whole time;
	// whichever lands first wins, which is why this lives on the gate rather
	// than in a separate wait stage (the engine runs one stage at a time).
	type autoAnswer struct {
		slug   string
		answer string
		poll   func() (bool, error)
	}
	var autos []autoAnswer
	for _, a := range []autoAnswer{{slug: st.AutoApprove, answer: "approve"}, {slug: st.AutoReject, answer: "reject"}} {
		if poll := x.signalPoller(st, a.slug); poll != nil {
			a.poll = poll
			autos = append(autos, a)
		}
	}
	// answerFromSignal polls each armed signal and, on the first that fired,
	// records the answer it supplies exactly as an operator's would.
	answerFromSignal := func() (*PingAnswer, error) {
		for _, a := range autos {
			fired, err := a.poll()
			if err != nil {
				return nil, err
			}
			if !fired {
				continue
			}
			ans := PingAnswer{Answer: a.answer, Feedback: "auto: " + a.slug}
			x.e.mu.Lock()
			already := p.answered
			if !already {
				p.answered = true
				p.answer = &ans
			}
			x.e.mu.Unlock()
			if !already {
				if err := x.append(&event.PingAnswered{PingID: p.ID, Answer: ans.Answer, Feedback: ans.Feedback}); err != nil {
					return nil, err
				}
				close(p.done)
			}
			return &ans, nil
		}
		return nil, nil
	}

	if len(autos) == 0 {
		// Nothing to poll and nothing to run: a gate with no signal armed is
		// pure waiting, so park the run and give the slot back. The ping is
		// answerable from the durable projection while the loop is gone, and
		// replay routes the answer on re-entry (rs.gateAnswer, above) — the
		// path crash recovery has always taken.
		//
		// Signal-armed gates below are the exception that keeps this honest:
		// auto_approve/auto_reject poll an external service on a ticker, so
		// they need a live loop and stay resident.
		return errWaitingOnHuman
	}

	var pollC <-chan time.Time
	if len(autos) > 0 {
		// Poll once immediately: a signal that fired while the engine was down
		// (or before the gate armed) answers NOW, not one interval later —
		// recovery re-entry lands here too.
		ans, err := answerFromSignal()
		if err != nil {
			return err
		}
		if ans != nil {
			return x.routeGate(st, *ans, started)
		}
		t := time.NewTicker(x.e.cfg.MRPollInterval)
		defer t.Stop()
		pollC = t.C
	}
	for {
		select {
		case ar := <-x.h.answers:
			handled, err := x.processAnswer(ar)
			if err != nil {
				return err
			}
			if handled && ar.p == p {
				ans := ar.ans
				ans.Feedback += attachedFilesNote(ar.p.deliveredPaths)
				return x.routeGate(st, ans, started)
			}
		case <-pollC:
			ans, err := answerFromSignal()
			if err != nil {
				return err
			}
			if ans != nil {
				return x.routeGate(st, *ans, started)
			}
		case <-x.ctx.Done():
			return context.Cause(x.ctx)
		}
	}
}

// resolveGateInput turns a gate's bound input into what the human is handed: a
// link for a sentinel, the path itself for an artifact to review.
func (x *exec) resolveGateInput(v string) string {
	switch v {
	case flow.SentinelMRURL:
		// Inside a reaction phase, the merge request it acts on.
		if mr := x.subjectMR(); mr != nil {
			return mr.Ref.URL
		}
		return x.rs.view.MRURL
	case flow.SentinelWorkspace:
		// Workspace hand-off: point the human at the running environment
		// (terminal beats IDE beats whatever else env_ready recorded).
		return workspaceLink(x.rs.links)
	}
	return v
}

// signalPoller returns a poll function for a signalspec slug, or nil when the
// slug is empty or the signal cannot fire for this run yet (no MR to watch).
// A poll reports whether the signal has fired; a hard error is an append
// failure or run cancellation. Repeated integration failures raise ONE blocker
// notification per streak — polling that fails silently forever reads as
// "this is broken" from the outside, which is worse than the failure itself.
func (x *exec) signalPoller(st *flow.Stage, slug string) func() (bool, error) {
	if slug == "" {
		return nil
	}
	rs := x.rs
	// The MR signals are the same poll against different outcomes.
	var want ports.MRState
	switch slug {
	case signalspec.MRMerged:
		want = ports.MRMerged
	case signalspec.MRClosed:
		want = ports.MRClosed
	default:
		return nil
	}
	if len(rs.mrs) == 0 {
		return nil // nothing to watch: no MR has been opened
	}
	fails := 0
	// A run with several merge requests (a stack, a multi-repo change) is
	// merged when ALL of them are, and closed as soon as ANY is closed
	// unmerged — someone decided against part of it.
	return func() (bool, error) {
		allMerged, anyClosed := true, false
		for _, mr := range rs.mrs {
			forge := x.res.forgeFor(mr.Repo)
			if forge == nil {
				continue
			}
			status, serr := forge.MRStatus(x.ctx, mr.Ref)
			state := status.State
			if serr != nil {
				if x.ctx.Err() != nil {
					return false, context.Cause(x.ctx)
				}
				fails++
				x.e.log.Warn("signal poll failed", "run", rs.runID, "signal", slug, "attempt", fails, "err", serr)
				if fails == signalFailNotify {
					x.e.notify(x.bg, ports.Notification{
						RunID: rs.runID, Type: event.PingBlocker,
						Title: fmt.Sprintf("cannot read %s (%d failed polls): %s", slug, fails, rs.view.TicketTitle),
						Body:  serr.Error() + " — detection is blind until this recovers.",
					})
				}
				return false, nil
			}
			if state != ports.MRMerged {
				allMerged = false
			}
			if state == ports.MRClosed {
				anyClosed = true
			}
		}
		fails = 0
		if want == ports.MRMerged {
			return allMerged, nil
		}
		return anyClosed, nil
	}
}

// signalFailNotify is the consecutive-failure count at which a signal poll
// raises a blocker notification (once per streak).
const signalFailNotify = 3

func (x *exec) routeGate(st *flow.Stage, ans PingAnswer, started time.Time) error {
	if ans.Answer == "approve" {
		if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
			return err
		}
		x.advance(st)
		return nil
	}
	return x.takeEdge(st, flow.EdgeReject, ans.Feedback)
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
		rs.iters[st.ID] = iter
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
		rs.ph = phIn
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
	case actionspec.PushBranch:
		routed, err = x.actionPushBranch(st)
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
	x.rs.ph = phStart
}

// --- wait stages ------------------------------------------------------------

// stageWait blocks until an external signal fires (FLOWLANG §3). Like a gate it
// parks the run on something outside it, so with no timeout it waits
// indefinitely; a stage timeout bounds it and routes on_timeout (else on_fail).
// Re-entry on recovery simply re-polls: waiting is idempotent.
func (x *exec) stageWait(st *flow.Stage) error {
	rs := x.rs
	if rs.ph != phIn {
		iter := rs.iters[st.ID] + 1
		rs.iters[st.ID] = iter
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
		rs.ph = phIn
	}
	started := x.e.clock()

	poll := x.signalPoller(st, st.WaitFor)
	if poll == nil {
		// The signal can never fire for this run — waiting forever would look
		// like a hang, so say what is missing instead.
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInputMissing,
			Error: fmt.Sprintf("cannot wait for %s: %s", st.WaitFor, waitPrereq(st.WaitFor))}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, "")
	}
	if fired, err := poll(); err != nil {
		return err
	} else if fired {
		return x.completeWait(st, started)
	}

	var deadline <-chan time.Time
	if st.Timeout > 0 {
		t := time.NewTimer(st.Timeout)
		defer t.Stop()
		deadline = t.C
	}
	ticker := time.NewTicker(x.e.cfg.MRPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			fired, err := poll()
			if err != nil {
				return err
			}
			if fired {
				return x.completeWait(st, started)
			}
		case <-deadline:
			if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonTimeout,
				Error: fmt.Sprintf("%s did not fire within %s", st.WaitFor, st.Timeout)}); aerr != nil {
				return aerr
			}
			return x.takeEdge(st, failEdgeKind(st, event.ReasonTimeout), "")
		case <-x.ctx.Done():
			return context.Cause(x.ctx)
		}
	}
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
	return flow.VerdictPath(st, x.rs.fl.Agent(st.Agent))
}

// workspaceLink picks the link a debugging human wants first: the web
// terminal, else the IDE, else the first recorded link. "" when the
// workspace recorded none (the ping still renders, just without a link).
func workspaceLink(links []event.Link) string {
	for _, kind := range []string{"terminal", "ide"} {
		for _, l := range links {
			if l.Kind == kind {
				return l.URL
			}
		}
	}
	for _, l := range links {
		if l.URL != "" { // command-only links (ssh) can't be a ping deep link
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
		if err == nil && status != ports.EnvRunning && status != ports.EnvAbsent {
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
			// Transient (host daemon blip, Coder unreachable): re-provisioning a
			// healthy env and re-running bootstrap on top of it would be worse
			// than waiting for the next boot.
			return fmt.Errorf("recovery: env status: %w", err)
		}
		if status == ports.EnvRunning {
			// Re-establish the reverse tunnel — it died with the previous
			// engine process, so the workspace's engine/proxy loopback
			// forwards are gone (found live: git push to 127.0.0.1:<proxy>
			// refused after an engine restart + resume).
			if r, ok := x.res.Envp.(ports.WorkspaceReattacher); ok {
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
			x.autoExpose(rs.envID)
			// Re-materialize goal + answer artifacts into the recovered
			// workspace (idempotent; the associations are durable).
			x.deliverRunAttachments()
			return nil
		}
		x.e.log.Warn("environment not running on recovery; re-provisioning", "run", rs.runID, "env", rs.envID, "status", status)
	}
	if rs.view.State == run.Pending {
		if err := x.append(&event.EnvRequested{}); err != nil {
			return err
		}
	}
	// The container/workspace name derives from the branch STEM, not the
	// suffixed actual branch: base already ends where the run-id tail begins,
	// so workspaceName re-appending the tail would double it (apo-…-h1-h1).
	// Legacy runs have no base — fall back to the actual branch.
	nameBranch := rs.view.BranchBase
	if nameBranch == "" {
		nameBranch = rs.branch
	}
	spec := ports.EnvSpec{
		RunID:   rs.runID,
		RepoURL: x.res.RepoRef.URL,
		Branch:  rs.branch,
		Name:    workspaceName(nameBranch, rs.runID),
		Project: x.res.ProjectName,
		// Only used by provisioners that don't get the §8 image: a
		// repo-authored devcontainer must install the harness itself.
		HarnessInstall:       x.res.HarnessInstall,
		EgressDomains:        x.res.EgressDomains,
		EgressMode:           x.res.EgressMode,
		EgressDenylist:       x.res.EgressDenylist,
		EgressBlocked:        x.res.EgressBlocked,
		EgressAllowedCIDRs:   x.res.EgressAllowedCIDRs,
		EgressAllowedPorts:   x.res.EgressAllowedPorts,
		CodeServerExtensions: x.res.CodeServerExtensions,
		// For clone-first provisioners (devcontainer): the engine-side
		// checkout needs the forge credential before the env exists.
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
		rs.done = true
		return x.append(&event.EnvFailed{Error: err.Error()})
	}
	rs.envID = id
	if err := x.bootstrap(id); err != nil {
		return err
	}
	if rs.done {
		return nil
	}
	// After bootstrap (repo cloned, .apothecary/ git-excluded): deliver the
	// human's goal + answer artifacts into the workspace for the agent.
	x.deliverRunAttachments()
	links, err := x.res.Envp.Links(x.ctx, id)
	if err != nil {
		x.e.log.Warn("links unavailable", "run", rs.runID, "err", err)
	}
	evLinks := make([]event.Link, 0, len(links))
	for _, l := range links {
		evLinks = append(evLinks, event.Link{Kind: l.Kind, URL: l.URL, Command: l.Command})
	}
	// Auto-expose declared + previously-exposed ports; safe to fold into
	// env_ready because this append happens on the run's own writer
	// goroutine BEFORE the event exists (out-of-band appends would race the
	// seq and clobber run state — see portrelay). The live registry stays
	// authoritative (GET /runs/{id}/ports); persisted allocations make the
	// re-exposed URLs match the pre-restart ones (best-effort).
	for _, l := range x.autoExpose(id) {
		evLinks = append(evLinks, l)
	}
	rs.links = evLinks // live counterpart of replay's env_ready capture
	return x.append(&event.EnvReady{EnvID: string(id), Links: evLinks})
}

// autoExpose publishes the union of the ports the environment DECLARES
// (devcontainer forwardPorts) and the ports this run had exposed before a
// restart (portRelay.SavedPorts — includes MANUAL exposures, which would
// otherwise be lost on recovery). Each re-binds its persisted host port so
// the URL survives. Best-effort — a failed exposure is a warning, never a
// failed run.
func (x *exec) autoExpose(id ports.EnvID) []event.Link {
	exposer, ok := x.res.Envp.(ports.WorkspacePortExposer)
	if !ok || x.e.portRelay == nil {
		return nil
	}
	seen := map[int]bool{}
	wsPorts := exposer.AppPorts(x.ctx, id)
	wsPorts = append(wsPorts, x.e.portRelay.SavedPorts(x.rs.runID)...)
	var out []event.Link
	for _, port := range wsPorts {
		if seen[port] {
			continue
		}
		seen[port] = true
		if url := x.e.portRelay.Existing(x.rs.runID, port); url != "" {
			out = append(out, event.Link{Kind: "app", URL: url})
			continue
		}
		pc, err := exposer.ExposePort(x.ctx, id, port)
		if err != nil {
			x.e.log.Warn("auto-expose failed", "run", x.rs.runID, "port", port, "err", err)
			continue
		}
		url, err := x.e.portRelay.Expose(x.rs.runID, port, pc)
		if err != nil {
			x.e.log.Warn("auto-expose failed", "run", x.rs.runID, "port", port, "err", err)
			continue
		}
		out = append(out, event.Link{Kind: "app", URL: url})
	}
	return out
}

// bootstrap runs the §11 ordered steps. A setup failure is a deterministic
// env_failed with the command's output tail (§1) — agents never repair the
// environment.
func (x *exec) writeRunEnv(id ports.EnvID) error {
	runEnv := fmt.Sprintf("APOTHECARY_RUN_ID=%s\nAPOTHECARY_RUN_TOKEN=%s\nAPOTHECARY_ENGINE_URL=%s\n",
		x.rs.runID, x.e.RunToken(x.rs.runID), x.e.cfg.EngineURL)
	return x.res.Envp.WriteFile(x.ctx, id, "/apothecary/run.env", []byte(runEnv), 0o600)
}

// reseedSecrets restores the credential files in a workspace the loop is
// re-entering, undoing the terminal scrub (secrets.go) for a resumed run. It
// writes exactly what bootstrap step 2 writes — same function, so the two can
// never drift — and touches nothing else: the checkout, the branch and the
// agent's commits are already there.
func (x *exec) reseedSecrets(id ports.EnvID) error {
	res := x.res
	return WriteWorkspaceSecrets(x.ctx, res.Envp, id, BootstrapSpec{
		RepoURL:     res.RepoRef.URL,
		ProviderEnv: res.ProviderEnv,
		GitUser:     res.GitUser,
		GitPass:     res.GitPass,
		SeedAuth:    res.SeedAuth,
		Extras:      x.extraCheckouts(),
	})
}

func (x *exec) bootstrap(id ports.EnvID) error {
	res := x.res
	err := RunBootstrap(x.ctx, res.Envp, id, BootstrapSpec{
		RunID:        x.rs.runID,
		Token:        x.e.RunToken(x.rs.runID),
		EngineURL:    x.e.cfg.EngineURL,
		RepoURL:      res.RepoRef.URL,
		SourceBranch: sourceBranchOr(x.rs.sourceBranch, res.RepoRef.DefaultBranch),
		Branch:       x.rs.branch,
		AgentsMDPath: res.AgentsMDPath,
		ProviderEnv:  res.ProviderEnv,
		GitUser:      res.GitUser,
		GitPass:      res.GitPass,
		GitName:      res.GitName,
		GitEmail:     res.GitEmail,
		SeedAuth:     res.SeedAuth,
		Prepare:      res.Prepare,
		Agents:       x.rs.fl.AgentSpecs(),
		Setup:        res.Commands["setup"], // optional in v2: "" skips step 6
		Timeout:      x.rs.fl.Defaults.Timeout,
		Extras:       x.extraCheckouts(),
	})
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		x.rs.done = true
		// The workspace exists (Provision succeeded) — record its id so
		// capacity counting, archive and the reaper can destroy it.
		return x.append(&event.EnvFailed{Error: err.Error(), EnvID: string(id)})
	}
	return nil
}

// readVerdict reads and parses the stage's verdict output (§5.3 contract) at
// the path its agent declares — rebindable per stage, so never hardcoded.
func (x *exec) readVerdict(st *flow.Stage) (*ports.Verdict, error) {
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
	return parseVerdict(data)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
