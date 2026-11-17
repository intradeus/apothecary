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
  go to. `holdReentry` sends nothing while the attempt has not begun, so the
  stage prompt goes first (any message queued meanwhile rides with it, via
  `withPendingMessages`) instead of parking on the owner before the agent
  has said anything. Every attempt starts held: a retry and an `on_reject`
  redo are new attempts, and the previous one's hold was closed when it
  ended.
- **The opening turn is the agent's.** It answers the stage prompt, so the
  hold's account (`hold_released` turns, messages, cost) leaves it out:
  `exec.openingTurn` marks the invocation that sends the stage prompt under
  a start hold, and its first `turn_start` is not added to the leg. From
  there the take-over is unchanged: parking, wake on message, release, the
  handback prompt, the output contract, and outcomes waiting for the
  release.
- **The owner is told** when that opening turn ends with nothing queued and
  the session goes idle on them: one `Engine.notify` with `Type: "held"` to
  the run owner's channels. A later turn, a woken one or a crash re-entry
  says nothing (the opening turn is past); like every channel send it is
  best-effort.
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
`TestStartHeldParksOnAQuietOwner`, `TestRecoveredStartHoldSendsTheStagePrompt`;
`internal/engine/outcomes_test.go` `TestAnOutcomeWaitsForAStartHold`;
`internal/flow` `TestStartHeld`, `TestDocBoolJSON`; CPUI
`AgentStage.test.ts` (start in your hands), `flowdoc.test.ts` (the trip),
`Inbox.test.ts` (in your hands), `fleet.svelte.test.ts` (`fleet.held`), and
the inbox counts in `Sidebar.test.ts` and `CommandPalette.test.ts`.
