# One cursor (2026-10-31)

A run has one cursor, always: one chain at a time holds it, moving or parked
at a wait point, or it stands at the hub between chains. All of a run's work
lands on one branch, so nothing runs beside anything else. This overturns the
v4 rule that a gate or a wait frees the run for watchers
(`docs/flow-language.md` §1 is the normative text).

- **Parked is one wait point.** `State.Parked` was a map of chain snapshots
  saved at a park and restored at the resume. Nothing can overwrite a parked
  chain's fields anymore, so `PhaseRun` and the restore are gone.
  `ParkPoint{Stage, Ping, Since}` is all a park adds. A log where a watcher ran
  during a wait does not replay to a sane cursor; nothing is released, so no
  upgrader.
- **Outcomes override everything but a take-over.** An outcome is terminal, so
  it takes the cursor from a parked chain, a running stage, and a run with no
  loop (an agent's question, the thread checklist, the debug leaf, a pause).
  Nothing polled those, so `Engine.RunOutcomes` does, at the signal cadence, for
  the runs the idle tick cannot reach; `runcore.State.PlanOutcome` says what may
  be overridden. A running loop stops as a pause stops it (`outcomeFired` is the
  cancel cause), `exit` records the outcome's `phase_started`, and a fresh loop
  picks it up through recovery, so no second path runs a chain. A dormant run
  gets the same event plus `run_unpaused` (reason `outcome`) under `wakeMu`.
  `ChainMerged` keeps a chain that merged by itself from being cut off at
  random, and a live action stage finishes. A take-over landing between a
  pass's read and its cancel loses the race and ends on the record, as with an
  abort. An outcome that reads `run.ci.failure` gets no report when it starts
  on a dormant run.
- **Confirmations stay open at the hub.** A question is not a chain: blocking
  on one would let an unanswered CI question hold up every automatic review
  round. So an answer can still reach the loop during another chain's command,
  which is why R1 needed fixing rather than vanishing.
- **R1 is fixed.** It was a false error, not a wait: after 30 s `AnswerPing`
  said "not accepted — try again" while the answer sat in `h.answers` and
  landed later, so a retry with a different answer was refused and the first
  counted. The queue is now the acceptance: a person hears within
  `answerLanding`, and a second answer is refused. A crash before it lands
  loses it, and the question reopens with the run.
- **Left as it is:** R3, the second `mr_merged` a crash can record; only the
  timeline reads it. No routing events for now.
- **Gates are answered by people.** `auto_approve` and `auto_reject` are gone
  from the grammar, the engine and the flow editor. No built-in flow used them,
  and an outcome covers a merge or a close. What went with them: an approval on
  the repository host can no longer answer a gate.
