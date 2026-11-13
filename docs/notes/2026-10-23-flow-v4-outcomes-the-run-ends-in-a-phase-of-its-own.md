# Flow v4: outcomes — the run ends in a phase of its own

Second pass on the ends (2026-10-23), from modeling the default flow with the
user: the first v4 cut ended the delivery phase with `wait_merge` →
`ticket_done` → `done`, so "the run is over" was the tail of the entry chain,
and a close could only be said as that wait's `on_fail`. Reactions could
never speak about the run's end at all. Now the end is a phase like the
others — an **outcome**, opened by the merge request's fate:

- **`signalspec.Outcome`** on `mr_merged` and `mr_closed`. Both stay
  waitable (a `wait` may still park on them); a phase `on:` one of them is an
  outcome (`flow.Phase.IsOutcome`; `Observed()` = fires or outcome, which is
  what preflight's repository host check and the end rule read). It takes `start`
  (auto/confirm/manual) like a trigger, no `max` (it fires once), no
  `quiet_for`.
- **`then`** (`DocPhase.Then`, `Phase.Then`): a stage-less outcome says
  `then: done` or `then: abort`. Stages or `then`, never both; `then` only on
  an outcome. Its span is empty (`Last = First - 1`) — the reachability walk
  and anything indexing `Stages[ph.First]` must skip it (the one engine
  reader, `startPhase`, sets the cursor past the end instead).
- **An outcome ends the run.** Its last stage's implicit success is `done`
  (so is the shorthand's: nothing could follow either); any route to `end`
  inside it is a parse error. `on_success: abort` is allowed everywhere
  (`runState.abortsRun`, `exec.runAbort` → `phase_completed`, then
  `run_abandoned` with `stalled_at` empty for a then-phase, reason
  "aborted by flow: outcome closed" / "…on_success at stage x"). A dead end
  inside an outcome abandons the run like an entrypoint's
  (`endsRunAtDeadEnd`): the run's end itself failed.
- **The done rule.** A flow needs some route to `done` — a success, an edge,
  or a `then: done` (`Flow.EndsRun`) — or it is refused: it would say nothing
  about when the work is finished. Test fixtures that had no end gained a
  `shipped` outcome or an explicit `done`.
- **Scheduler.** `evaluateIdle` walks the outcomes before the reactions: a
  held outcome beats any reaction. `waitState` decides when one holds
  (merged = every merge request merged, closed = any one closed unmerged; no
  merge request = never); `outcomeOf` names the subject and key — merged has
  no subject and the key `merged`, closed takes the closed checkout and
  `closed:<repo>`. `start: confirm` reuses `askToStart` (now subject + link,
  not an observation) with the question "…and end the run?"; a decline marks
  the key seen and the run goes on to its end rule. `anyArmed` counts an
  outcome that never ran as armed while any merge request is open — that,
  not a parked wait, is what keeps the default's runs in review.
- **Default flow.** `deliver` ends with `in_review` (`on_fail: end`);
  `merged` (ticket → done, best effort, `done`) and `closed` (ticket → todo,
  `abort` either way) are outcomes. The builtin test holds the shape and
  refuses a `wait` in the default.
- **API/UI.** `PhaseSummary`/`PhaseInfo` carry `outcome` and `then`; the
  signal registry carries `outcome`. The editor offers the outcome signals
  under "and ends the run on…", a `then` select on a stage-less outcome, no
  max; the graph draws an outcome's marker green with its edge to `done` /
  `abort` when it has no stages; the run page's Phases card says "ends the
  run on…", "once". `flowdoc.OUTCOME_SIGNALS` mirrors the registry only for
  the document model's implicit `done` (the pickers read the API).
- `HasSubject()` is false for `mr_merged`: it holds for every merge request
  at once, so its phase acts on none in particular (a subject action there
  warns, as in any subject-less phase).
