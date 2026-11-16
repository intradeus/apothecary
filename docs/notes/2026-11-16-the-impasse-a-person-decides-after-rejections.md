# The impasse: a person decides after rejections

Why: the built-in reviewer sent its rejections to the debug leaf
(`on_reject: { to: build, max: 3, then: debug }`). A REJECT is a verdict, not
a failure: nothing is broken in the workspace for a person to mend, and the
leaf's one way back — re-running the reviewer — asks the same reviewer the
same question. What a person needs to decide there is whether the reviewer is
right: send the work back again, overrule it, or stop. Nothing in the flow
language could express "overrule": no stage could be skipped by a person.

What now: a fourth reserved edge target, `impasse` (FLOWLANG §3), reached
from `on_reject` only — the parser refuses it on `on_success`, `on_fail` and
`on_timeout`, and `max`/`then` on it. Taking it parks the chain like a gate
(`parkAt`: the workspace is released, the idle loop resumes it through
`PlanResume`) on a ping of type `impasse` and a new answer kind, `choice`:
the ping lists its choices by id (`keep_going`, `skip`, `give_up`; each may
say `note_required`) and the engine sends no wording — the clients label
them. Not a `yes_no`: that kind carries a polarity (approve is the good
outcome, reject the bad one that fires `on_reject` and spends its budget),
and none of the impasse's answers is either — skipping a stage is neither
the work passing nor failing. Not a `form`: a form is information for the
asker, always declinable, its pick buried in `content`; the impasse must be
decided, and its decision is the answer itself. FLOWLANG §9 now sets the
three kinds side by side. The question says which stage and how many rejections in a row; the
context is the verdict that spent the chain.

- **keep going** restores the rejected stage's `on_reject` chain only — no
  other stage's, no other kind's (the debug leaf's continue restores the
  whole phase; this is a narrower decision) — and the rejection takes that
  chain again from its first link with the same verdict. The reset is folded
  from the `ping_answered` itself (`runcore.State.RestoreRejectBudget`), so
  live and replay agree with no new event. The impasse link keeps its count,
  as the debug links do.
- **skip** clears the stage's outputs (a stale REJECT must never read as
  current), records `stage_started` and `stage_completed{skipped, note, by}`,
  and follows `on_success` with nothing carried. Because any stage that
  reaches the impasse may be skipped, the validator counts its outputs as
  produced only on some routes, as a `run_if` stage's (`Stage.Skippable`,
  `definitelyProduced`).
- **give up** is a dead end, as at the debug leaf.

`on_reject: impasse` alone offers skip and give up only: keep going would ask
again at once. Every answer now records who gave it (`ping_answered.by`, from
the API's authenticated user); a skip carries it onto `stage_completed.by`.
The note travels through `run.ping_answers` until the run journal (the plan's
Part 2) lands: an impasse answer counts there when it has a note, a skip
always.

Pass numbers are not budgets: `stage_started.iteration` keeps counting
through a restored budget, so the timeline's attempt and the stage's
`<n>.log` never repeat. The spec's "What `max` counts" now lists every
restore — try again, continue at debug, keep going at the impasse.

The built-in `builder` flow's review rejects five times to build, then the
impasse (`on_reject: { to: build, max: 5, then: impasse }`); no rejection in
a built-in flow reaches debug any more.

The Postgres pings projection gains a `choices` column (migration
`0004_ping_choices.sql`; `0001` stays frozen): the inbox reads pings from it,
and a choice ping read back without its choices would show no button to
answer it. The in-memory store and the run page fold the events and never
had the gap, which is why only the Postgres test sees it.

The UI: a choice card (one button per choice the ping carries, the verdict
shown, an optional note, a choice that needs one held until it is written),
an `Impasse` pill, a stage row waiting on "rejected — your call", a person's
skip reading "skipped by a person" rather than a `run_if`'s "nothing to act
on". The flow editor draws an impasse leaf per stage, beside its debug leaf,
with no edge out (the person's choice, not the drawing, says what follows);
the route picker offers it on `on_reject` only, and a rejection's default
fallback is now the impasse, a failure's still the debug leaf. The terminal
client answers no pings and is unchanged.

Tests: `internal/flow/impasse_test.go` (the leaf parses, its refusals, its
outputs conditional); `internal/engine/impasse_test.go` —
`TestImpasseKeepGoing`, `TestImpasseSkip`, `TestImpasseGiveUp`,
`TestImpasseAtAGate` (a gate's own reject is routed before the impasse asks,
never taken for its answer), `TestImpasseAloneOffersNoKeepGoing`,
`TestImpasseAcrossRestart` (unanswered, and answered before its routing),
`TestImpasseKeepGoingRestoresOnlyThatChain` (every stage of every built-in
flow with an `on_reject`: exactly its own non-impasse links restored);
`TestValidateAnswerByKind` (choices); `TestDefaultFlowPhilosophy` (the
builder's review shape, no rejection to debug); `TestPingChoicesAreProjected`
(pgstore: the choices round-trip, as the events reduce them). Breaking it ten ways — no
reset; another kind reset too; the impasse link reset; the stale verdict kept
on skip; keep going offered alone; impasse allowed from `on_fail`; a
skippable stage's output read as certain; an unknown choice or a missing
required note accepted — fails at least one of these each, and within
seconds: the engine tests bound their wait on the run and name the ping left
open.
