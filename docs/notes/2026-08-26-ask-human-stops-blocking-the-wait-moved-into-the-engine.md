# `ask_human` stops blocking: the wait moved into the engine

Found live, and worth stating plainly because the failure was silent. A
planner asked a clarification (`/health` and `/healthz`, or just `/healthz`?),
opencode killed the MCP tool call at exactly 60.06 s with
`MCP error -32001: Request timed out`, handed the model the error — and the
model did the reasonable thing with an error: it guessed, wrote the plan, and
finished the stage. Six seconds later the human answered a question whose
caller no longer existed, and a plan nobody had approved sat in the gate
looking finished. Nothing in the timeline said any of that had happened.

The shim was not at fault: it long-polled forever, exactly as designed. The
design was. **A blocking MCP tool call is the wrong place to park a human.**
The spec says so itself — implementations SHOULD time out requests, MAY reset
the clock on progress notifications, and SHOULD enforce a maximum regardless
(MCP 2025-06-18, Lifecycle §Timeouts). Elicitation would not have saved us
either: the outer `tools/call` timer keeps ticking under it. What lets Claude
Code wait indefinitely is not a protocol feature — it is that the question
never leaves the harness's own control channel (`canUseTool` is an in-process
promise; opencode's permission raise and codex's `execCommandApproval` are
server→client requests on their own event loops). Every one of those is
harness-specific. We need one mechanism that works on all of them.

So the question became **turn-ending**, and the wait moved to the only party
that can hold it: us.

1. The shim posts the ping with `hold_ms` (20 s) — a GRACE period, not the
   wait. A human already in the inbox answers in-band and the agent never
   loses its turn.
2. On 204 the tool returns *success* with `postedNotice`: your question is
   posted, END YOUR TURN, the answer arrives as your next message with your
   work intact. Success, not an error — an error is what invites a guess.
3. `stageAgent` now runs **one attempt across several harness invocations**.
   An invocation that returns with pings still open is an `outcomePaused`, not
   a result: the engine appends `stage_paused`, banks that leg's turns/cost,
   checks the budget, and waits in `awaitAnswers` with no timer of any kind.
   When every question is answered it appends `stage_resumed` and calls
   `Resume(session, answerReply(...))`. `iter` never moves; the stage timeout
   budget carries across legs; outputs are published only when the attempt
   actually ends.

Two properties fall out that the blocking version could not have:

- **The wait is durable.** `stage_paused` carries the harness session id, so
  replay re-enters a paused attempt, re-registers its pings and resumes into
  the same session — a restart mid-question no longer throws away everything
  the agent did before asking. (Before this, `rs.sessions` was only populated
  from `stage_completed`, so a crash mid-stage always restarted the agent.)
- **Guessing is now detectable.** `awaitAgent` used to take
  `case res := <-resCh: return res` regardless of open pings. That one line is
  what let the guessed plan through. An invocation that returns with a
  question outstanding can no longer be mistaken for a finished stage — and a
  harness that *died* mid-question still routes as the failure it is
  (`res.err != nil` is never a pause; there is nothing left to resume into).

`event.StagePaused`/`StageResumed` are deliberately generic — reason-tagged
(`ask_human`, `answered`, `engine_restart`) rather than ask_human-specific —
so any future "park this stage, continue it later" reuses them. Crash recovery
already does: re-entering a session after a restart now says so in the
timeline instead of resuming silently.

Belt and braces, cheap: the opencode MCP registration now sets `timeout`
(2 min, vs. the client default of 60 s that bit us). The shim never holds a
call that long any more, so it is only a floor under the grace hold.

One more thing the exec log showed: the model's FIRST attempt called the tool
as `ask_human` and got *"Model tried to call unavailable tool 'ask_human'.
Available tools: ask_human_ask_human, …"* — opencode namespaces MCP tools
`<server>_<tool>`. It retried and found it; a less persistent model would
simply have guessed. The default flow's prompts now name that shape
explicitly and tell the agent never to conclude the tool is missing because
the bare name failed. (Since 2026-10-28 that text rides the role instead —
see *ask_human mechanics ride the role, not the flow*.)
