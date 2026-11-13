# Phases: the run's life after the merge request

PLAN6, built as M28–M33. A flow may now describe a ticket's whole life: the
entry phase (`on: ticket`) is today's pipeline ending at the open merge
request, and reaction phases — `conflicts`, `review_fixes` in the default
template — run while the merge request is open, when their trigger holds for
one merge request (the phase run's *subject*). One phase run at a time, in the
ticket's own run, workspace and event log; the run ends when its merge
requests merge (completed) or one is closed (abandoned). The grammar change is
additive (`phases:` beside `stages:`, still version 3); flows with top-level
stages behave exactly as before.

### Repository host facts (M28)

`RepoHost.MRState` became `MRStatus` — state, mergeability (`clean | conflicts |
unknown`), head sha, target branch and, only when conflicting, the target's
tip (one extra branch lookup) — and the port gained `MRThreads`, `ReplyThread`
and `ResolveThread`. GitLab is all REST (discussions; `detailed_merge_status`
"checking"/"unchecked" is *unknown*, never a conflict). GitHub needs GraphQL
for review threads — REST exposes neither the grouping nor `isResolved` — and
for replying and resolving; a review's top-level body becomes its own thread
(`review:<id>`), answered with a conversation comment that carries the thread
marker so it can be found again. `mergeable: null` is *unknown*.

"Ours" is decided by a marker, not by account: every reply ends with an
invisible `<!-- apothecary run=<run>/<phase>-<n> thread=<id> -->`. A thread is
**pending** when it is unresolved and its last comment carries no marker. The
round suffix lets reconciliation after a crash recognise this round's replies
only — an earlier round's answer on the same thread never suppresses a new one.

`GET /api/runs/{id}/mrs/status` reads all of it live for the run page (an
observation, never recorded).

### The engine (M29, M30)

- **Events**: `phase_started {phase, subject, key, by, tip}`,
  `phase_completed`, `phase_declined`; replay rebuilds the current phase, the
  per-(phase, subject) run counts and every trigger key started or declined.
  A phase start resets the edge counters of that phase's stages — `max` is per
  round — while the budget stays run-wide.
- **Idle** is not a phase: when a phase reaches `done` the loop enters
  `idle()`, which polls the repository host on the existing `MRPollInterval` (threads on
  `ThreadPollInterval`, default 4× — a heavier call), takes confirmation
  answers and manual starts, and returns when a phase starts or the run ends.
  It is resident like a signal-armed gate but **does not hold a concurrency
  slot** (`busyLocked` skips idle handles; the scheduler admits a queued run as
  the loop goes idle). A phase starting from idle does not wait for a slot, so
  the cap can be exceeded by phases briefly — they are short, and blocking a
  conflict fix behind unrelated tickets would be worse.
- **Triggers** are pure functions of repository host facts, evaluated per merge request
  in checkout order, then per phase in document order. `mr_conflicts` keys on
  `(target tip, head)`; `mr_review_settled` holds when the newest pending
  reviewer comment is older than `quiet_for`, keyed on that comment. No cursor
  is stored, so nothing can drift from what the reviewer sees.
- **Confirmation** is a `phase_confirm` ping answered like a gate
  (approve/reject, no reason required on reject — the UI says *Run it* / *Not
  now*). It belongs to no stage: replay keeps it apart from a stage's gate
  answer. One open question per (phase, subject).
- **Subject scoping (R12)**: command stages run in the subject's checkout,
  `$MR_URL` and `$CHANGED` resolve to it (`$CHANGED` from the head recorded at
  the phase start), agents get a `# SUBJECT` prompt section.
- **Run view**: state `in_review` while idle with merge requests open;
  `phase`, `phase_subject`, `idle` on the view; `in_review` counts as active
  for flow deletion. `GET /api/runs/{id}/phases` lists the reaction phases with
  their runs per merge request; `POST /api/runs/{id}/phases/{phase}/start?repo=`
  starts one by hand (idle only; `max` applies; a human start consumes no
  trigger key).

### Review rounds (M31)

`load_review_comments` writes the subject's pending threads to
`artifacts/review.md` (+ `.json`) and records `review_comments_loaded`; a
re-entry rewrites the same set. The builder takes them as an optional
`comments` input. The `replier` agent writes `artifacts/replies.yaml`, a
`review_replies`-typed output validated when its stage ends against the loaded
thread ids — an unknown thread is that stage's `output_invalid`, retried with
the reason, before anything is posted. `reply_review_comments` records
`action_started`, posts each reply with its marker (resolving only where the
file says), and on a re-entry posts only the threads that do not yet carry
this round's marker.

**Choosing what to fix.** Not every comment needs a change — it may be
wrong, or the author handled it by hand. With `select: human` (the template's
choice) `load_review_comments` raises a `select` ping — a checklist of the
pending threads, options `"<id> — <where> — <author>: <first line>"`, answered
with the chosen options one per line or `none` — and parks the run (the stage
re-enters on the answer, as a parked gate does). Only the chosen threads are
written for the fixer and may be replied to; the others are recorded in
`review_comments_loaded.skipped` with their last comment id and left out of
later rounds — and out of the trigger — until that thread gets a newer
comment. An empty selection ends the phase right after the load: no agent
turn, no commit, no push.

**GitHub comments.** Every comment counts, not only reviews: review threads,
review bodies and conversation comments (`issue:<id>` threads, answered with
a conversation comment carrying the marker). Comments from bot accounts
(`user.type == "Bot"`: CI, dependency bots) are left out — otherwise every CI
report would open a review round.

### Answer kinds: one shape vocabulary for every human question

Three kinds of question had grown three sets of flags: a gate was
`gate: true`, an `ask_human` with options was "has options", the thread
picker was `type == select`, and validation, the card and the inbox badge
each re-derived what answer a ping takes. `ping_raised` now **declares** it
— `answer_kind: decision | choice | multi | text` — and options are
structured, `{id, label, detail}` (older events' bare strings decode as
`{id: s, label: s}`; `PingRaised.Kind()` infers the kind for pings written
before the field, as does the Postgres projection on read, migration 0025).

What follows from one field: `validateAnswer` is one table; `recordedAnswer`
and the tool-return renderer read the kind; `Ping.gate` and the
`decisionPingType` family are gone (what remains is `agentPingType`, which
is about *who* asked — the unwanted-ping count and the "wanted" badge are
about origin, not shape); the thread picker's options carry the thread id,
"where — who" and the first words as id/label/detail instead of one string
split on " — "; and `ask_human` gained `multi: true` and structured options
for free (the shim passes them through; `RaiseQuestion` validates them; 16
options at most). Not done, on purpose: multi-question forms, and structured
plan comments — the annotation composer stays a feedback editor over a file
payload, which any decision or choice ping with a file already gets.

### Conflicts (M32, M33)

`sync_target` fetches the subject's target and merges (or rebases) it in, in
the engine. Already contained → nothing happens and nothing is recorded. A
conflict leaves the operation **paused** (`MERGE_HEAD` / `rebase-merge`) and
fails the stage with the files named; the template routes that to the
`resolver` agent, which edits the files and runs no git. The retried stage
recognises the paused operation, checks the unmerged files for the labelled
markers (`<<<<<<< `, `>>>>>>> `; a bare `=======` is legitimate content), and
finishes it — `git commit --no-edit`, or `rebase --continue` (`--skip` for a
commit the resolution emptied). A paused operation of another strategy or an
older target tip is aborted and redone; a non-conflict failure aborts and
restores the branch. Six resolver rounds, then a human gets the paused merge.

Actions may carry `on_fail` again (for outcomes like these); infrastructure
errors stay terminal.

`push_branch` replaces the template's `git push` command: a first push is
plain; a branch it pushed before is pushed with `--force-with-lease` against
the tip recorded in `branch_pushed` — or, in a reaction phase that has not
pushed yet, the head recorded at the phase start. A remote branch holding
commits the run never saw is refused, never overwritten. That is what makes
`strategy: rebase` safe, and the parser requires a `push_branch` in any phase
that rebases.

### Fixed on the way

- `action_started` was never cleared on replay once the action completed (the
  multi-repo change dropped the clearing on `mr_created`). Harmless for a
  one-shot `create_mr`; a reaction phase re-runs its actions, so round two
  would have started in reconcile mode. It now clears on the stage's
  `stage_completed`.

### Deviations from PLAN6 as written

- The entry phase must be the **first** phase (one rule instead of a search;
  it also makes the flattened stage list start with the entry).
- `trigger` / `phase` were not reserved as stage ids — nothing needs it.
- The plan's template YAML had two success cycles (the parser caught both):
  `sync`'s implicit next stage was `resolve` (which routes back to `sync`),
  and each phase's last real stage fell through into its rescue gate. The
  template states `on_success` explicitly there.
- Reply markers carry a round id (above) instead of reconciling by time.
- The default template's rescue gates follow the template's philosophy (each
  resumes the stage that escalated), except the resolver's, which resumes
  `sync` — the human who resolves by hand replaces the agent.

### Residuals

- Answering a confirmation while *another* phase's command stage runs gets
  the existing "run is busy — try again" after 30 s: only agent stages, gates
  and the idle loop drain answers.
- One replies file and one commit per round; a round cannot split its fixes
  across commits.
- Phase runs on different merge requests take turns (one cursor); parallel
  phase runs would be the multi-cursor rework.
