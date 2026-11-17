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
  `turn_start` gave. It asks the runner through the inbox, signalled on
  `Wake`; a runner that can stop a turn takes the request up with
  `agentport.Inbox.StopTurn(turn)` (true: stop that turn now), and one that
  cannot never calls it. Naming the turn keeps a request that arrives late
  from stopping the holder's next one.
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
  invocation, as a harness error. A runner that cannot stop a turn never
  takes the request up and behaves as before: its turn runs on, as under a
  passive take-over.
- Exception: a turn waiting on the agent's own question (`ask_human` open)
  is not stopped, even forced. The agent is not working then, and a turn
  ending on an open question parks the run under the hold; the holder
  answers it instead. The stop is decided under `Engine.mu`, the lock
  `RaiseQuestion` registers a question under: a question counts as open
  from its registration (`runHandle.raising`, until the invocation admits
  it), not only once the loop has taken it from the mailbox. The decision
  only asks for the stop (`runHandle.stopAsked`); the runner taking it up
  (`StopTurn`, under the same lock) makes it `runHandle.stopping`. A
  question registered in between withdraws the request, so `StopTurn`
  answers false and the turn runs on with its question. Once a stop is
  taken up, until that turn or the invocation ends, a question is refused,
  `ErrTurnStopping` (409), rather than opened on a turn about to end. A
  runner that cannot stop a turn never takes it up, so its questions stay
  welcome. The turn's end is the runner reporting it (`Events`), not the
  loop reading that report: the loop may be behind, writing the transcript,
  while the runner has already started the holder's turn, which may ask.
  For the same reason a question carries the turn the runner was in when it
  was raised (`runHandle.agentTurn`), and a turn ends on a question (parking
  the run) only if it is that turn's. The holder's turn may also ask before
  its runner has reported it started (the ACP sidecar records `turn_start`
  ahead of the prompt, but the runner forwards it afterwards), so a question
  raised with no turn reported open also carries the turn last reported
  ended (`runHandle.endedTurn`, `Ping.after`): a runner reports a turn's end
  before it prompts the next, so that question is not the ended turn's.
- The dev fake harness fakes a take-over: its demo turn ends, cancelled, when
  stopped, and it serves a held session — each holder message and the
  release's handback get a short reply turn — so `task dev:up` can show a
  forced take, a passive one, messages and the release.

Release, reviewed: two fixes. It hands back at once when the agent waits on
the holder, and otherwise after the turn in flight. After a forced take-over
that turn is only the stopped one still winding down, or the holder's own (the
design of `2026-10-28-take-over.md`); re-admitting a parked run cannot be
avoided. After a passive take-over it can still be the agent's own turn, which
is what passive means. The fix: a release behind a holder's message handed
back at the end of the turn in flight, even with that message still to be
delivered — once the runner had taken it, the engine no longer saw it queued —
so the agent got the message and the handback in one prompt, and the hold
counted neither. The hold's leg now counts the holder's messages queued and
not yet reported delivered, and those delivered whose turn has not started
yet (`HoldLeg.Waiting`, cleared at the next `turn_start`); the release waits
on them, and hands back at the end of their turn. `Inbox.Take` now says a
runner reports what it took as a `user_message` before the turn it starts,
as every runner here already did.

The second: a release waiting on a turn that parks on the agent's own
question (the turn ends on it, or it outwaits the live hold) was forgotten
with the invocation — the answer resumed the session still held, and the
holder had to release again. The release now goes on the record as the run
parks (`hold_released`, with the holder's messages still queued on its
account; they ride into the answer's prompt). The handback it owes
(`HandbackOwed`) follows the answer's turn in the same invocation: the session
is resumed held until that turn ends, then sent the handback and let go of —
or, ending on another question, parks again with the handback still owed. A
new attempt of the stage owes nothing (`stage_started` clears it). A restart
between that `hold_released` and the `stage_paused` that follows it finds the
handback owed beside the question still open live: the question comes first
there too — its turn is followed if it survived, or the attempt parks on it —
and the handback follows the answer's turn. A release that comes only once
the question has parked the run with the hold in force (no loop: `Release`'s
dormant path) counts those still-queued messages the same way; the parked
leg's banked spend counts only delivered ones. A release also waits on an
open question of the agent's, one registered while its `turn_start` is still
on its way to the loop included (`HoldRequest.QuestionOpen`): handing back
at once would send the handback into the session about to park on that
question, and the answer's resumed session would never get it.

This narrows what `2026-10-31-nobody-talks-to-an-agent-without-taking-it-over.md`
removed: no message interrupts the agent; only a forced take-over can stop a
turn, and it names that turn.

Held by: `TestPlanHoldRequest`, `TestHoldLegWaitsUntilTheHoldersMessagesGetTheirTurn`,
`TestAHandbackIsOwedUntilSentOrTheAttemptEnds`
(runcore); `TestAForcedTakeOverStopsTheAgentsTurn`,
`TestAPassiveTakeOverLetsTheTurnFinish`, `TestReleaseRightAfterAForcedTakeIsPrompt`,
`TestAForcedTakeOverLeavesATurnWaitingOnItsQuestion`,
`TestAForcedTakeOverBesideAQueuedQuestionLeavesTheTurn`,
`TestAForcedTakeOverBesideARegisteredQuestionLeavesTheTurn`,
`TestAQuestionFromAStoppingTurnIsRefused`, `TestTheHoldersTurnMayAskWhileTheLoopIsBehindTheStop`,
`TestTheHoldersTurnMayAskBeforeItsStartIsReported`,
`TestARunnerThatCannotStopKeepsItsQuestions`,
`TestAQuestionBeforeTheStopIsTakenUpKeepsTheTurn`,
`TestAReleaseBehindAQueuedMessageWaitsForItsTurn`,
`TestAReleaseBetweenDeliveryAndTurnStartWaitsForTheTurn`,
`TestAReleaseBeforeAQuestionParksHandsBackAfterTheAnswer`,
`TestAReleaseBesideARegisteredQuestionHandsBackAfterTheAnswer`,
`TestReleasingAQuestionParkedHoldCountsItsQueuedMessage`,
`TestRestartBetweenAReleaseAndItsQuestionParkParksOnTheQuestion`,
`TestRestartForcedTakeOverStopsTheReattachedTurn` (engine);
`TestReattachability`, `TestReattachFollowsTheTurnInFlight`,
`TestStoppingATurnKeepsTheSession`, `TestAStopForAnotherTurnIsIgnored`,
`TestAStoppedTurnThatNeverEndsGivesUp`, `TestALateStopRequestCannotHitTheHoldersTurn`
(acpagent); `TestDemoTurnStopsWhenTakenOver`,
`TestFakeHarnessServesATakeOver`, `TestFakeHarnessWithoutAHoldReturnsAfterItsTurn`
(kinds); `TestTakeOverForce` (httpapi); `Composer.test.ts` (the box is
checked again for the next take-over, after a passive one is released),
`store.svelte.test.ts`, `SessionView.test.ts`, `api/index.test.ts`.
