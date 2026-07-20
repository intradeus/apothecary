package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"factory/internal/event"
	"factory/internal/flow"
	"factory/internal/ports"
	"factory/internal/run"
)

var errStageTimeout = errors.New("stage timeout")

// resumeReply is what Resume sends when re-entering a crashed agent stage
// with a previously recorded session (§2 crash recovery).
const resumeReply = "The factory engine restarted while this stage was in flight. " +
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

	fl, err := e.flows.Get(bg, view.FlowName)
	if err != nil {
		x.rs = &runState{runID: runID, view: view}
		_ = x.append(&event.RunFailed{Reason: fmt.Sprintf("flow %q not found", view.FlowName)})
		return
	}
	if fl.Hash != view.FlowHash {
		e.log.Warn("flow file changed since run creation; continuing with current version",
			"run", runID, "flow", fl.Name)
	}
	rs := replay(evs, fl)
	x.rs = rs
	if rs.done {
		return
	}

	e.mu.Lock()
	e.tokens[runID] = e.newID()
	// Re-register open pings so answers can still be delivered after a restart.
	for _, op := range rs.openPings {
		if _, ok := e.pings[op.PingID]; !ok {
			e.pings[op.PingID] = &Ping{
				ID: op.PingID, RunID: runID, Type: op.Type, Question: op.Question,
				Context: op.Context, PayloadRef: op.PayloadRef, RaisedAt: e.clock(),
				gate: gatePingType(op.Type), done: make(chan struct{}),
			}
		}
	}
	e.mu.Unlock()

	if err := x.ensureEnv(); err != nil {
		x.exit(err)
		return
	}
	if rs.done { // env_failed is terminal
		return
	}

	for !rs.done {
		if cause := context.Cause(ctx); cause != nil {
			x.exit(cause)
			return
		}
		if rs.pos < 0 || rs.pos >= len(fl.Stages) {
			if err := x.complete(); err != nil {
				x.exit(err)
			}
			return
		}
		st := &fl.Stages[rs.pos]
		var err error
		if rs.ph == phAfterComplete {
			err = x.routeAfterComplete(st)
		} else if rs.ph == phAfterFail && st.Type != flow.StageForgeMR {
			err = x.routeAfterFail(st)
		} else {
			switch st.Type {
			case flow.StageAgent:
				err = x.stageAgent(st)
			case flow.StageCommand:
				err = x.stageCommand(st)
			case flow.StageHumanGate:
				err = x.stageGate(st)
			case flow.StageForgeMR:
				err = x.stageForgeMR(st)
			}
		}
		if err != nil {
			x.exit(err)
			return
		}
	}
}

// exit handles abnormal loop termination: abort marks the run failed,
// shutdown leaves it for Recover, anything else is logged and left
// non-terminal (recovered by replay on next boot).
func (x *exec) exit(cause error) {
	switch {
	case errors.Is(cause, errAborted):
		_ = x.append(&event.RunFailed{Reason: "aborted"})
	case errors.Is(cause, errShutdown), cause == nil:
	default:
		x.e.log.Error("run loop error; run left non-terminal for recovery",
			"run", x.rs.runID, "err", cause)
	}
}

// append writes events through the single-writer goroutine, keeps the
// in-memory view current, and feeds the telemetry sink (§7).
func (x *exec) append(ps ...event.Payload) error {
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

func (x *exec) advance() {
	x.rs.pos++
	x.rs.ph = phStart
	x.rs.feed = ""
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

// takeEdge follows an on_fail/on_reject edge, abandoning the run when the
// edge is exhausted (§2, §3).
func (x *exec) takeEdge(st *flow.Stage, edge *flow.Edge, kind, feedContent string) error {
	rs := x.rs
	if edge == nil {
		return x.abandon(st.ID, "", fmt.Sprintf("stage %s: unrecoverable failure with no %s", st.ID, kind))
	}
	key := edgeKey(st.ID, kind)
	count := rs.edgeCounts[key] + 1
	if count > edge.Max {
		return x.abandon(st.ID, kind, fmt.Sprintf("%s from %s exhausted (max %d)", kind, st.ID, edge.Max))
	}
	feed := ""
	if edge.Feed != "" {
		feed = tail(feedContent, 8<<10)
	}
	// Feed is persisted in the event so a crash between edge_taken and the
	// next stage_started loses nothing.
	if err := x.append(&event.EdgeTaken{From: st.ID, To: edge.To, Edge: kind, Count: count, Max: edge.Max, Feed: feed}); err != nil {
		return err
	}
	rs.edgeCounts[key] = count
	rs.pos = rs.fl.Index(edge.To)
	rs.ph = phStart
	rs.feed = feed
	rs.feedAttempts = edge.Max + 1
	return nil
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
// routed directly, without re-executing the stage. forge_mr stages instead
// re-enter their executor, which is idempotent via the recorded MR.
func (x *exec) routeAfterFail(st *flow.Stage) error {
	feed := ""
	if x.rs.lastFailure != nil {
		feed = x.rs.lastFailure.Error
	}
	x.rs.ph = phStart
	return x.takeEdge(st, st.OnFail, "on_fail", feed)
}

// routeAfterComplete handles recovery when the crash hit between a
// stage_completed and its routing: no re-execution, only the decision.
func (x *exec) routeAfterComplete(st *flow.Stage) error {
	if st.Type == flow.StageAgent && st.Verdict {
		v, err := x.readVerdict(st)
		if err != nil {
			if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputContract, Error: err.Error()}); err != nil {
				return err
			}
			return x.takeEdge(st, st.OnFail, "on_fail", "")
		}
		if !v.Approve {
			return x.takeEdge(st, st.OnReject, "on_reject", strings.Join(v.Comments, "\n"))
		}
	}
	x.advance()
	return nil
}

// --- agent stages -----------------------------------------------------------

type agentRes struct {
	r   ports.AgentResult
	err error
}

func (x *exec) stageAgent(st *flow.Stage) error {
	rs := x.rs
	reenter := rs.ph == phIn
	iter := rs.iters[st.ID]
	if !reenter {
		iter++
		rs.iters[st.ID] = iter
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter, Harness: st.Harness, Model: st.Model}); err != nil {
			return err
		}
	}
	rs.ph = phIn
	started := x.e.clock()

	prompt := x.assemblePrompt(st, iter)
	session, haveSession := rs.sessions[st.ID]

	runCtx, cancelRun := context.WithCancelCause(x.ctx)
	defer cancelRun(nil)
	resCh := make(chan agentRes, 1)
	go func() {
		var r ports.AgentResult
		var err error
		if reenter && haveSession {
			r, err = x.res.Runner.Resume(runCtx, rs.envID, session, resumeReply)
		} else {
			r, err = x.res.Runner.Run(runCtx, rs.envID,
				ports.AgentSpec{Agent: st.Agent, Harness: st.Harness, Model: st.Model, MaxTurns: st.MaxTurns},
				ports.AgentInput{Prompt: prompt})
		}
		resCh <- agentRes{r, err}
	}()

	res, timedOut, err := x.awaitAgent(st, resCh, cancelRun, reenter)
	if err != nil {
		return err // cancelled: caller inspects cause
	}
	x.writeStageLog(st.ID, iter, res.r.Output)
	if timedOut {
		if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonTimeout}); err != nil {
			return err
		}
		return x.takeEdge(st, st.OnFail, "on_fail", "")
	}
	if res.err != nil {
		if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonHarnessError, Error: res.err.Error()}); err != nil {
			return err
		}
		return x.takeEdge(st, st.OnFail, "on_fail", tail(res.r.Output, 8<<10))
	}

	// Output contract (§3): declared artifact must exist.
	if st.Output != "" {
		if _, err := x.res.Envp.ReadFile(x.ctx, rs.envID, st.Output); err != nil {
			if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputContract,
				Error: fmt.Sprintf("declared output %s not readable: %v", st.Output, err)}); aerr != nil {
				return aerr
			}
			return x.takeEdge(st, st.OnFail, "on_fail", "")
		}
	}
	var verdict *ports.Verdict
	if st.Verdict {
		verdict = res.r.Verdict
		if verdict == nil {
			v, err := x.readVerdict(st)
			if err != nil {
				if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputContract, Error: err.Error()}); aerr != nil {
					return aerr
				}
				return x.takeEdge(st, st.OnFail, "on_fail", "")
			}
			verdict = v
		}
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
		return x.takeEdge(st, st.OnReject, "on_reject", strings.Join(verdict.Comments, "\n"))
	}
	x.advance()
	return nil
}

// awaitAgent waits for the harness result while servicing pings. The stage
// timeout is suspended while any ping is pending (§3 defaults.timeout).
func (x *exec) awaitAgent(st *flow.Stage, resCh <-chan agentRes, cancelRun context.CancelCauseFunc, reenter bool) (agentRes, bool, error) {
	remaining := st.Timeout
	open := 0
	if reenter {
		open = len(x.rs.openAgentPings())
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	lastArm := time.Now()
	if open > 0 && !timer.Stop() {
		<-timer.C
	}

	suspend := func() {
		if timer.Stop() {
			remaining -= time.Since(lastArm)
			if remaining <= 0 {
				remaining = time.Millisecond
			}
		}
	}
	resume := func() {
		lastArm = time.Now()
		timer.Reset(remaining)
	}

	for {
		select {
		case res := <-resCh:
			return res, false, nil
		case req := <-x.h.mailbox:
			if err := x.append(&event.PingRaised{PingID: req.p.ID, Type: req.p.Type,
				Question: req.p.Question, PayloadRef: req.p.PayloadRef}); err != nil {
				close(req.ack)
				return agentRes{}, false, err
			}
			x.rs.unwanted++
			x.e.notify(x.bg, ports.Notification{
				RunID: x.rs.runID, PingID: req.p.ID, Type: req.p.Type,
				Title: fmt.Sprintf("%s: %s", req.p.Type, x.rs.view.TicketTitle),
				Body:  req.p.Question,
			})
			if open == 0 {
				suspend()
			}
			open++
			close(req.ack)
		case ar := <-x.h.answers:
			handled, err := x.processAnswer(ar)
			if err != nil {
				return agentRes{}, false, err
			}
			if handled && !ar.p.gate {
				open--
				if open == 0 {
					resume()
				}
			}
		case <-timer.C:
			cancelRun(errStageTimeout)
			res := <-resCh
			_ = res
			return agentRes{}, true, nil
		case <-x.ctx.Done():
			cancelRun(context.Cause(x.ctx))
			return agentRes{}, false, context.Cause(x.ctx)
		}
	}
}

// processAnswer appends ping_answered and delivers the answer to whoever is
// waiting on the ping. Returns false if the ping was already answered.
func (x *exec) processAnswer(ar answerReq) (bool, error) {
	x.e.mu.Lock()
	if ar.p.answered {
		x.e.mu.Unlock()
		return false, nil
	}
	ar.p.answered = true
	ans := ar.ans
	ar.p.answer = &ans
	x.e.mu.Unlock()

	answer, feedback := ar.ans.Answer, ar.ans.Feedback
	if !ar.p.gate {
		// §6: agent (ask_human) answers are recorded as answer:"text".
		answer, feedback = "text", ar.ans.Answer
	}
	if err := x.append(&event.PingAnswered{PingID: ar.p.ID, Answer: answer, Feedback: feedback}); err != nil {
		return false, err
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
	}
	if rs.ph != phIn { // §2: on recovery, open command stages re-execute without a new attempt
		iter := rs.iters[st.ID] + 1
		rs.iters[st.ID] = iter
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
	}
	rs.ph = phIn
	started := x.e.clock()

	timeout := rs.fl.Defaults.Timeout
	cctx, cancel := context.WithTimeout(x.ctx, timeout)
	res, err := x.res.Envp.Exec(cctx, rs.envID, ports.Command{Script: script, Dir: "repo", Timeout: timeout})
	cancel()
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonTimeout}); aerr != nil {
				return aerr
			}
			return x.takeEdge(st, st.OnFail, "on_fail", "")
		}
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra, Error: err.Error()}); aerr != nil {
			return aerr
		}
		return x.fail("infrastructure error: " + err.Error())
	}
	x.writeStageLog(st.ID, rs.iters[st.ID], res.Output)
	if res.ExitCode != 0 {
		code := res.ExitCode
		// The output tail rides in the event so recovery can route the
		// failure without re-executing the command.
		if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonExitNonzero,
			ExitCode: &code, Error: tail(res.Output, 8<<10)}); err != nil {
			return err
		}
		return x.takeEdge(st, st.OnFail, "on_fail", tail(res.Output, 8<<10))
	}
	if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
		return err
	}
	x.advance()
	return nil
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
		if op := rs.openGatePing(st.Ping); op != nil {
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
		payloadRef := st.Payload
		if payloadRef == flow.PayloadMRURL {
			payloadRef = rs.view.MRURL
		}
		p = &Ping{
			ID: x.e.newID(), RunID: rs.runID, Type: string(st.Ping),
			Question:   fmt.Sprintf("%s: %s", st.Ping, rs.view.TicketTitle),
			PayloadRef: payloadRef, RaisedAt: x.e.clock(),
			gate: true, done: make(chan struct{}),
		}
		x.e.mu.Lock()
		x.e.pings[p.ID] = p
		x.e.mu.Unlock()
		if err := x.append(&event.PingRaised{PingID: p.ID, Type: p.Type, Question: p.Question, PayloadRef: p.PayloadRef}); err != nil {
			return err
		}
		x.e.notify(x.bg, ports.Notification{
			RunID: rs.runID, PingID: p.ID, Type: p.Type,
			Title: p.Question, Body: p.PayloadRef,
		})
	}

	var pollC <-chan time.Time
	if st.AutoComplete == flow.AutoCompleteMRMerged && rs.mr != nil {
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
				return x.routeGate(st, ar.ans, started)
			}
		case <-pollC:
			state, err := x.res.Forge.MRState(x.ctx, *rs.mr)
			if err != nil {
				x.e.log.Warn("auto_complete poll failed", "run", rs.runID, "err", err)
				continue
			}
			if state == ports.MRMerged {
				x.e.mu.Lock()
				already := p.answered
				if !already {
					p.answered = true
					p.answer = &PingAnswer{Answer: "approve", Feedback: "auto: MR merged"}
				}
				x.e.mu.Unlock()
				if !already {
					if err := x.append(&event.PingAnswered{PingID: p.ID, Answer: "approve", Feedback: "auto: MR merged"}); err != nil {
						return err
					}
					close(p.done)
				}
				return x.routeGate(st, PingAnswer{Answer: "approve"}, started)
			}
		case <-x.ctx.Done():
			return context.Cause(x.ctx)
		}
	}
}

func (x *exec) routeGate(st *flow.Stage, ans PingAnswer, started time.Time) error {
	if ans.Answer == "approve" {
		if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
			return err
		}
		x.advance()
		return nil
	}
	return x.takeEdge(st, st.OnReject, "on_reject", ans.Feedback)
}

// --- forge_mr stages --------------------------------------------------------

func (x *exec) stageForgeMR(st *flow.Stage) error {
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

	if rs.mr == nil { // recovery guard: never create the MR twice
		body, err := x.res.Envp.ReadFile(x.ctx, rs.envID, st.Body)
		if err != nil {
			if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonOutputContract,
				Error: fmt.Sprintf("MR body %s not readable: %v", st.Body, err)}); aerr != nil {
				return aerr
			}
			return x.abandon(st.ID, "", fmt.Sprintf("MR body artifact %s missing", st.Body))
		}
		title := mrTitle(string(body), rs.view.TicketTitle)
		ref, err := x.res.Forge.CreateDraftMR(x.ctx, x.res.RepoRef, rs.branch, title, string(body))
		if err != nil {
			if x.ctx.Err() != nil {
				return context.Cause(x.ctx)
			}
			if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra, Error: err.Error()}); aerr != nil {
				return aerr
			}
			return x.fail("forge MR creation failed: " + err.Error())
		}
		rs.mr = &ref
		if err := x.append(&event.MRCreated{MRURL: ref.URL, MRIID: ref.IID, Branch: rs.branch}); err != nil {
			return err
		}
	}
	if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
		return err
	}
	x.advance()
	return nil
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
	if rs.envID != "" {
		status, err := x.res.Envp.Status(x.ctx, rs.envID)
		if err == nil && status == ports.EnvRunning {
			// Tokens are regenerated per boot; refresh the workspace copy so
			// the ask_human shim can re-authenticate after an engine restart.
			if err := x.writeRunEnv(rs.envID); err != nil {
				x.e.log.Warn("refreshing run.env on recovery failed", "run", rs.runID, "err", err)
			}
			return nil
		}
		x.e.log.Warn("environment missing on recovery; re-provisioning", "run", rs.runID, "env", rs.envID)
	}
	if rs.view.State == run.Pending {
		if err := x.append(&event.EnvRequested{}); err != nil {
			return err
		}
	}
	spec := ports.EnvSpec{
		RunID:         rs.runID,
		RepoURL:       x.res.RepoRef.URL,
		Branch:        rs.branch,
		Image:         x.res.Image,
		EgressDomains: x.res.EgressDomains,
	}
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
	links, err := x.res.Envp.Links(x.ctx, id)
	if err != nil {
		x.e.log.Warn("links unavailable", "run", rs.runID, "err", err)
	}
	evLinks := make([]event.Link, 0, len(links))
	for _, l := range links {
		evLinks = append(evLinks, event.Link{Kind: l.Kind, URL: l.URL})
	}
	return x.append(&event.EnvReady{EnvID: string(id), Links: evLinks})
}

// bootstrap runs the §11 ordered steps. A setup failure is a deterministic
// env_failed with the command's output tail (§1) — agents never repair the
// environment.
func (x *exec) writeRunEnv(id ports.EnvID) error {
	runEnv := fmt.Sprintf("FACTORY_RUN_ID=%s\nFACTORY_RUN_TOKEN=%s\nFACTORY_ENGINE_URL=%s\n",
		x.rs.runID, x.e.RunToken(x.rs.runID), x.e.cfg.EngineURL)
	return x.res.Envp.WriteFile(x.ctx, id, "/factory/run.env", []byte(runEnv), 0o600)
}

func (x *exec) bootstrap(id ports.EnvID) error {
	res := x.res
	err := RunBootstrap(x.ctx, res.Envp, id, BootstrapSpec{
		RunID:         x.rs.runID,
		Token:         x.e.RunToken(x.rs.runID),
		EngineURL:     x.e.cfg.EngineURL,
		RepoURL:       res.RepoRef.URL,
		DefaultBranch: res.RepoRef.DefaultBranch,
		Branch:        x.rs.branch,
		AgentsMDPath:  res.AgentsMDPath,
		ProviderEnv:   res.ProviderEnv,
		GitUser:       res.GitUser,
		GitPass:       res.GitPass,
		SeedAuth:      res.SeedAuth,
		Prepare:       res.Prepare,
		Setup:         res.Commands["setup"], // optional in v2: "" skips step 6
		Timeout:       x.rs.fl.Defaults.Timeout,
	})
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		x.rs.done = true
		return x.append(&event.EnvFailed{Error: err.Error()})
	}
	return nil
}

// readVerdict reads and parses artifacts/verdict.json (§5.3 contract).
func (x *exec) readVerdict(st *flow.Stage) (*ports.Verdict, error) {
	data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, "artifacts/verdict.json")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("stage %s: artifacts/verdict.json missing", st.ID)
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

// writeStageLog persists a stage attempt's output under runs_dir
// (<run>/<stage>-<iter>.log, §12/§13). Best-effort: failures are logged,
// never fail the run. No-op when RunsDir is unset.
func (x *exec) writeStageLog(stageID string, iter int, output string) {
	if x.e.cfg.RunsDir == "" {
		return
	}
	dir := filepath.Join(x.e.cfg.RunsDir, x.rs.runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		x.e.log.Warn("stage log dir", "run", x.rs.runID, "err", err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.log", stageID, iter))
	if err := os.WriteFile(path, []byte(output), 0o644); err != nil {
		x.e.log.Warn("stage log write", "run", x.rs.runID, "err", err)
	}
}
