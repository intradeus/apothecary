# Flow v4: two ends, and a reaction never ends the run

Found reviewing the default flow (2026-10-23): its reactions closed with
`on_success: done`, which reads as "the run is done" while the engine only
ended the phase — and nothing in the language could say "this run is
finished". Now:

- **`end` and `done`.** `end` (`flow.TerminalEnd`) finishes a phase: the run
  goes back to waiting. `done` finishes the run, whatever else is parked or
  armed (`runState.endsRun` → `exec.runDone`: `phase_completed`, then
  `run_completed`; open pings are withdrawn with the run). A phase's implicit
  last-stage success is `end`. Both are reserved ids, take no max/then, and
  are accepted as edge scalars.
- **A dead end outside an entrypoint ends the round.** A failure with no
  edge, an exhausted chain, or a person giving up at the debug leaf abandons
  the run only in an entrypoint phase (`exec.deadEnd`). In a reaction or a
  hand-started phase it ends that phase run, `phase_completed.reason` says
  why, and the run goes back to waiting. The debug ping carries `phase` and
  `ends_phase` (migration 0005 adds both to the pings projection), so the
  inbox says "End conflicts — the run keeps waiting" rather than "Abandon the
  run".
- **Reactions are unbounded by default.** The default `max` of 10 for firing
  phases and the rule that `max: -1` needs a start other than auto are gone:
  an occurrence key never fires twice, and the run's budget bounds the cost.
  `max` stays as an optional cap.
- **Warnings** (`endingWarnings`): a stage of a firing phase that ends the
  whole run (`done`, `abort`); a phase that opens a merge request and says
  `done` without waiting for it while other phases react to it. The second
  replaces a "create_mr never waited on" warning the reference promised and
  the code never had.
- The canvas draws an `end` sink always and `done` only when a route reaches
  it; edges to `done` used to point at a node id that did not exist.
