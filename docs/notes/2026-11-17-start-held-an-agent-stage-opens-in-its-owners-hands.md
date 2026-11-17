# Start held: an agent stage opens in its owner's hands

Why: working with an agent turn by turn — collaborative planning, a design
back-and-forth — meant taking over its session (2026-10-28-take-over), and
that was a race: the agent had usually finished, or written its outputs,
before the owner clicked Take over. The workaround was a prompt that made the
agent ask a question first, only to keep the stage open.

What now:

- **The field.** `start_held: true` (or `false`, or a bool `$parameter`) on a
  `type: agent` stage; any other type is the usual "does not apply to type"
  parse error, and a sequence stage refuses it like every stage field.
  `flow.DocBool` decodes it (a YAML/JSON bool or a `$name`, the way `DocInt`
  does an int), `Stage.StartHeld` is the resolved value. The editor writes
  it only when it says something (`true` or a parameter), and the agent
  inspector picks off, on, or one of the flow's bool parameters.
- **The hold.** `stageAgent` takes it (`takeStartHold`) when an attempt has
  not begun (`!AttemptBegun`) and no hold is in force: after the input check,
  the MCP resolution and crew boarding, so no early failure leaves a hold
  behind, and idempotent across a crash between `hold_taken` and the stage
  prompt. It is the run's owner's, `run.View.UserID` — on a run an
  automation started that is the project's owner (the resolver's
  `UserID: p.UserID`), the person the automation's blocker notices already
  go to. Every attempt starts held: a retry and an `on_reject` redo are new
  attempts, and the previous one's hold was closed when it ended.
- **The opening turn is on the record.** The plan had nothing new
  persisted, and kept "this is the opening turn" in the `exec` alone; review
  found two crash windows that lost it. A crash after `agent_prompted:stage`
  but before the harness recorded a session made the attempt look begun, so
  re-entry parked on the owner without the agent having answered; and a
  restart that reattached to the opening turn in flight counted it as the
  holder's and told nobody. So the hold says what it is owed:
  `hold_taken` carries `start: true`, and a new `hold_opened` event records
  the opening turn's end. Replay folds them into `HeldState.Opening`. While
  it is set, `holdReentry` sends nothing and never parks — the attempt's own
  path sends the stage prompt (no session yet), follows the live turn
  (reattach), or asks the session to carry on (restart) — and the
  invocation's `liveHold` opens on the agent's turn, whether this invocation
  saw that turn start or only its end.
- **The opening turn is the agent's.** It answers the stage prompt, so the
  hold's account (`hold_released` turns, messages, cost) leaves it out. From
  `hold_opened` on, the take-over is unchanged: parking, wake on message,
  release, the handback prompt, the output contract, and outcomes waiting
  for the release. The timeline and the transcript show `hold_opened` as the
  agent having answered the stage prompt, the owner's turn next.
- **The owner can act before the opening prompt.** From `hold_taken` on the
  run shows in their hands, but the invocation that takes messages and
  releases starts only after the stage prompt is written; in between,
  `SendMessage` said "no agent is running" and `Release` "nobody holds"
  (review). `admitOpening` sets the run's `agentLive` as soon as a hold
  owed its opening turn is in force (and again before each invocation of
  such an attempt), so a message or a release waits on the loop's channel
  for that invocation instead of being refused. A message is pushed to its
  inbox — delivered once, as the turn after the opening one, not appended to
  the stage prompt the plan named, which may already be on the log by then.
  A release while the opening turn is still owed (not started, or in
  flight) waits for it: the handback follows the opening turn.
- **The owner is told** when that opening turn ends with nothing queued and
  the session goes idle on them: one `Engine.notify` with `Type: "held"` to
  the run owner's channels, sent after `hold_opened` is recorded. A later
  turn, a woken one or a re-entry after `hold_opened` says nothing; like
  every channel send it is best-effort, and a crash between the record and
  the send loses it (the inbox still lists the run).
- **The inbox entry is derived**, not a ping (the owner's choice at
  planning): the CPUI inbox lists `fleet.held` — live, unarchived runs with
  a `hold` — under *In your hands*, linking each run, and the inbox badge
  (sidebar, mobile tab, palette) counts `fleet.inboxCount`, pings plus held
  runs. A manual take-over shows there too: it is the same state.
- **Budgets.** `max_turns` is not enforced by any runner today
  (`AgentSpec.MaxTurns` is read by none), so held turns already do not count
  against it. Held turns' turns and spend keep counting against
  `max_total_turns` and `max_cost_usd`, as for any take-over (the owner's
  choice for `max_total_turns`): a long collaborative stage can pause the
  run over budget, and Play grants another budget's worth.

Tests: `internal/engine/hold_test.go` — `TestStartHeldOpensInTheOwnersHands`
(hold taken for the owner before the stage prompt, one turn then waiting, one
`held` notification, a message as the next turn, release through the
handback, the account excluding the opening turn),
`TestStartHeldHoldsEveryAttempt` (an `output_contract` retry and an
`on_reject` redo each take a hold before their prompt),
`TestStartHeldParksOnAQuietOwner`,
`TestStartHeldAdmitsTheOwnerBeforeItsPrompt` (a message and a release while
the opening prompt's write is stalled, the hold already on the log),
`TestRecoveredStartHoldSendsTheStagePrompt`
(a crash before the prompt, and after it with no session: the prompt is
sent, not parked on), `TestRecoveredStartHoldFollowsTheOpeningTurn` (a
reattached opening turn, its start seen or not, notifies once and stays off
the account); `internal/run/event` `TestEveryRegisteredTypeRoundTrips`
(`hold_opened`); CPUI `derive.test.ts` and `transcriptItems.test.ts`
(`hold_opened`);
`internal/engine/outcomes_test.go` `TestAnOutcomeWaitsForAStartHold`;
`internal/flow` `TestStartHeld`, `TestDocBoolJSON`; CPUI
`AgentStage.test.ts` (start in your hands), `flowdoc.test.ts` (the trip),
`Inbox.test.ts` (in your hands), `fleet.svelte.test.ts` (`fleet.held`), and
the inbox counts in `Sidebar.test.ts` and `CommandPalette.test.ts`.
