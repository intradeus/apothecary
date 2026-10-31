package engine

import (
	"context"
	"errors"
	"time"

	"apothecary/internal/agentspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/runcore"
)

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

func (x *exec) stageAgent(st *flow.Stage) error {
	rs := x.rs
	reenter := rs.Ph == runcore.PhIn
	iter := rs.Iters[st.ID]
	if !reenter {
		iter++
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter, Harness: st.Harness, Model: st.Model,
			Agent: st.Agent, AgentHash: x.rs.Flow.AgentHash(st.Agent)}); err != nil {
			return err
		}
	}
	// A crash between stage_started and the first prompt asked nothing of the
	// agent: that attempt begins now, under the iteration already recorded.
	if reenter = reenter && rs.AttemptBegun(st.ID); !reenter {
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
			if _, ok := rs.Sessions[st.ID]; !ok {
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
		session, haveSession := rs.Sessions[st.ID]

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
			if ra, ok := runner.(agentport.AgentReattacher); ok && ra.Reattachable(x.ctx, rs.EnvID, session, rs.AgentCursor[st.ID]) {
				if err := x.append(&event.StageResumed{StageID: st.ID, Iteration: iter, Reason: event.ResumeRestart,
					SessionID: string(session)}); err != nil {
					return err
				}
				var err error
				inv, err = x.invoke(st, budget, tr, x.liveAsks(st), "",
					func(ctx context.Context, in agentport.AgentInput) (agentport.AgentResult, error) {
						in.MCPServers = mcp
						r, _, err := ra.Reattach(ctx, rs.EnvID, session, rs.AgentCursor[st.ID], in)
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
					return runner.Resume(ctx, rs.EnvID, session, in)
				}
				return runner.Run(ctx, rs.EnvID, spec, in)
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
	// Run cancellation (abort/pause/shutdown) can surface as the harness
	// result racing the ctx branch in awaitAgent; it is not a stage
	// failure — mirror stageCommand and let the loop exit on the cause.
	if inv.outcome != outcomeTimedOut && res.err != nil && x.ctx.Err() != nil {
		return context.Cause(x.ctx)
	}
	obs, verdict := x.observeAttempt(st, inv)
	if f := runcore.ClassifyAttempt(st, obs); f != nil {
		return x.failAttempt(st, f)
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

// observeAttempt reads what an agent attempt left, up to the first fault. All
// missing paths are reported in one failure so a human sees the whole gap, not
// the first file only.
func (x *exec) observeAttempt(st *flow.Stage, inv invocation) (runcore.AttemptObservation, *Verdict) {
	res := inv.res
	o := runcore.AttemptObservation{TimedOut: inv.outcome == outcomeTimedOut, Output: shell.Tail(res.r.Output, 8<<10)}
	if res.err != nil {
		o.Err = res.err.Error()
	}
	if o.TimedOut || res.err != nil {
		return o, nil
	}
	// Output contract (§3): every effective output (decl defaults overlaid
	// with stage rebinds) must exist.
	if o.Missing = x.missingPaths(st.Outputs); len(o.Missing) > 0 {
		return o, nil
	}
	// Every output with a structure — a built-in type's schema, a flow schema
	// — is checked first, all violations at once, in the words the agent
	// read in its OUTPUT CONTRACT: a shape error is reported as one, never as
	// a parser's complaint about a field it could not read.
	if o.Invalid = x.checkStructuredOutputs(st); o.Invalid != "" {
		return o, nil
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
			o.Invalid = err.Error()
			return o, nil
		}
		verdict = v
	}
	// A review_replies output is checked against the threads this round
	// loaded before anything is posted: a reply to an unknown thread, or a
	// thread left without one, is this stage's fault, retried with the
	// reason as $FAILURE.
	if o.Invalid = x.checkReviewReplies(st); o.Invalid != "" {
		return o, nil
	}
	// So is a review_threads output, against what the round loaded and what
	// a person dropped, before post_review reads it.
	o.Invalid = x.checkReviewThreads(st)
	return o, verdict
}

// failAttempt records a failed attempt and routes it.
func (x *exec) failAttempt(st *flow.Stage, f *runcore.AttemptFailure) error {
	if err := x.append(&event.StageFailed{StageID: st.ID, Reason: f.Reason, Error: f.Error, ExitCode: f.ExitCode}); err != nil {
		return err
	}
	return x.takeEdge(st, f.EdgeKind, f.Feed)
}

// pauseAttempt records an invocation that ended with questions open: the
// attempt is parked on them, not over. It banks what the invocation spent
// (real whether or not the run ever resumes) and the session the answer will
// re-enter. Outputs are NOT published: a half-written artifact is not this
// attempt's result. stop reports that a budget check ended the run.
func (x *exec) pauseAttempt(st *flow.Stage, iter int, inv invocation) (stop bool, err error) {
	rs := x.rs
	x.h.parking.Store(true)
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
	if h := rs.Holds[st.ID]; h != nil {
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
	for _, id := range x.rs.PausedAsks[st.ID] {
		parked[id] = true
	}
	var out []*Ping
	x.e.mu.Lock()
	defer x.e.mu.Unlock()
	for _, op := range x.rs.OpenAgentPings() {
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
	if len(x.rs.PendingMsgs) == 0 {
		return text, nil
	}
	msgs := make([]agentport.UserMessage, 0, len(x.rs.PendingMsgs))
	ids := make([]string, 0, len(x.rs.PendingMsgs))
	for _, m := range x.rs.PendingMsgs {
		msgs = append(msgs, agentport.UserMessage{ID: m.MessageID, Text: m.Text})
		ids = append(ids, m.MessageID)
	}
	if err := x.append(&event.MessageDelivered{MessageIDs: ids, StageID: st.ID, Iteration: iter}); err != nil {
		return text, err
	}
	return text + "\n\n---\n\n" + formatMessages(msgs), nil
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

// PausedAsks re-collects the questions this stage parked on (stage_paused),
// with whatever answers the log has since recorded.
func (x *exec) pausedAsks(st *flow.Stage) []ask {
	ids := x.rs.PausedAsks[st.ID]
	if len(ids) == 0 {
		return nil
	}
	out := make([]ask, 0, len(ids))
	for _, id := range ids {
		a := ask{id: id, answer: x.rs.AnsweredPings[id]}
		if r := x.rs.RaisedPings[id]; r != nil {
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
		if _, err := x.res.Envp.ReadFile(x.ctx, x.rs.EnvID, b.Value); err == nil {
			written = append(written, b.Value)
		}
	}
	return written
}
