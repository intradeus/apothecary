# A take-over stops the agent's turn

Why: a take-over felt slow. The hold was taken at once, but the ACP runner
read its inbox only when a turn ended, so the agent finished whatever it was
doing — minutes, at times — before it waited on the holder, and their first
message waited with it. Release looked slow for the same reason: right after a
take-over, the turn in flight it waits for was usually the agent's own.

What now:

- A take-over carries `force`, on by default: `Engine.TakeOver(run, user,
  force)`, `POST /api/runs/{id}/hold` with an optional `{"force": bool}`
  (no body, an empty one, or no field, is `true`; a body of unknown length,
  chunked or HTTP/2 without `Content-Length`, is decoded like any other, so
  a chunked `force: false` stays passive), and a "Stop the agent now"
  checkbox beside Take over, checked by default and not remembered.
  `force: false` is a passive take-over, exactly the old behaviour; it stays
  for a later feature. `force` is not on the record: it only decides what
  happens at the moment of taking, and the stopped turn is already there as
  a `turn_end` with stop reason `cancelled`.
- The engine decides (`runcore.PlanHoldRequest` → `HoldTakeAndStop`): a
  forced take with one of the agent's turns open, by the id its
  `turn_start` gave. It asks the runner through the inbox:
  `agentport.Inbox.Interrupted(turn)`, signalled on `Wake`. Naming the turn
  keeps a request that arrives late from stopping the holder's next one.
- After an engine restart the turn followed (`AgentReattacher.Reattach`)
  may have started before the recorded cursor, so its `turn_start` never
  comes again. `Reattachable` now also returns that turn's id, and the
  engine holds it as the open turn from the invocation's start: a forced
  take-over stops it too, and a passive one no longer reads the agent as
  idle while it works.
- The ACP runner cancels that turn (`session/cancel`), follows it to its
  end, and carries on as at any turn's end: the same session waits for the
  holder. `/agent/cancel` names the session, not the turn, so the next
  prompt waits for the stop request's answer: a request still on its way
  when the turn ended on its own would stop the holder's turn. A stop that
  does not end (or is not answered) within the cancel grace fails the
  invocation, as a harness error. A runner that cannot stop a turn ignores
  the request and behaves as before.
- Exception: a turn waiting on the agent's own question (`ask_human` open)
  is not stopped, even forced. The agent is not working then, and a turn
  ending on an open question parks the run under the hold; the holder
  answers it instead. The stop is decided under `Engine.mu`, the lock
  `RaiseQuestion` registers a question under: a question counts as open
  from its registration (`runHandle.raising`, until the invocation admits
  it), not only once the loop has taken it from the mailbox; and once a stop
  is decided (`runHandle.stopping`, until a turn or the invocation ends) a
  question is refused, `ErrTurnStopping` (409), rather than opened on a turn
  about to end. A runner that ignores the stop therefore has its turn's
  questions refused until that turn ends; every runner here honours it.
- The dev fake harness fakes a take-over: its demo turn ends, cancelled, when
  stopped, and it serves a held session — each holder message and the
  release's handback get a short reply turn — so `task dev:up` can show a
  forced take, a passive one, messages and the release.

Release, reviewed: one fix. It hands back at once when the agent waits on
the holder, and otherwise after the turn in flight. After a forced take-over
that turn is only the stopped one still winding down, or the holder's own (the
design of `2026-10-28-take-over.md`); re-admitting a parked run cannot be
avoided. After a passive take-over it can still be the agent's own turn, which
is what passive means. The fix: a release behind a holder's message handed
back at the end of the turn in flight, even with that message still to be
delivered — once the runner had taken it, the engine no longer saw it queued —
so the agent got the message and the handback in one prompt, and the hold
counted neither. The hold's leg now counts the holder's messages queued and
not yet reported delivered (`HoldLeg.Waiting`); the release waits on them, and
hands back at the end of their turn. `Inbox.Take` now says a runner reports
what it took as a `user_message` before the turn it starts, as every runner
here already did.

This narrows what `2026-10-31-nobody-talks-to-an-agent-without-taking-it-over.md`
removed: no message interrupts the agent; only a forced take-over can stop a
turn, and it names that turn.

Held by: `TestPlanHoldRequest`, `TestHoldLegWaitsUntilTheHoldersMessagesAreDelivered`
(runcore); `TestAForcedTakeOverStopsTheAgentsTurn`,
`TestAPassiveTakeOverLetsTheTurnFinish`, `TestReleaseRightAfterAForcedTakeIsPrompt`,
`TestAForcedTakeOverLeavesATurnWaitingOnItsQuestion`,
`TestAForcedTakeOverBesideAQueuedQuestionLeavesTheTurn`,
`TestAForcedTakeOverBesideARegisteredQuestionLeavesTheTurn`,
`TestAQuestionFromAStoppingTurnIsRefused`,
`TestAReleaseBehindAQueuedMessageWaitsForItsTurn`,
`TestRestartForcedTakeOverStopsTheReattachedTurn` (engine);
`TestReattachability`, `TestReattachFollowsTheTurnInFlight`,
`TestStoppingATurnKeepsTheSession`, `TestAStopForAnotherTurnIsIgnored`,
`TestAStoppedTurnThatNeverEndsGivesUp`, `TestALateStopRequestCannotHitTheHoldersTurn`
(acpagent); `TestDemoTurnStopsWhenTakenOver`,
`TestFakeHarnessServesATakeOver`, `TestFakeHarnessWithoutAHoldReturnsAfterItsTurn`
(kinds); `TestTakeOverForce` (httpapi); `Composer.test.ts` (the box is
checked again for the next take-over, after a passive one is released),
`store.svelte.test.ts`, `SessionView.test.ts`, `api/index.test.ts`.
