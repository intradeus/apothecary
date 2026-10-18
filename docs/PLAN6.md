# Apothecary v6 — Phases: the run's life after the merge request (plan)

> Status: **implemented 2026-10-18 (M28–M33)** — see IMPLEMENTATION_NOTES,
> *Phases: the run's life after the merge request*, for what was built and
> the deviations from this text (entry phase first; explicit `on_success` in
> the template YAML below, which had two success cycles as written).
> Answers TODO #3 ("merge conflicts / rebase? PR comments fixes?"). Milestones
> M28–M33 continue PLAN5's numbering.

## 0. The problem and the decision

Today a run's story ends when its merge request opens. `final_gate` parks the
run with `auto_approve: mr_merged` / `auto_reject: mr_closed`; merging
completes it, closing abandons it, and everything that happens to the MR in
between — a reviewer leaving twelve comments, `main` moving under the branch
until it no longer merges — is invisible to the run. Fixing either means a
human doing it by hand or a second ticket that re-plans from scratch.

The decision: **a flow describes the whole life of the ticket, A to Z, and a
run is one ticket's life.** A flow is a list of **phases**. One phase is the
entry, started by the ticket (today's whole flow). Every other phase is a
*reaction*: it declares the forge signal that starts it, runs its own
stages on the branch of **one merge request** (its *subject*), and ends.
Phases are **not a sequence** — a conflict can arrive before, after or
between two review rounds, and the triggers alone decide what runs — but
**one phase runs at a time**, in the ticket's run and workspace. A phase run
works on one merge request (its *subject*); a multi-repo run's merge
requests take their turns. The run ends from its merge requests, not from a
phase: all merged completes it, one closed abandons it.

Three shapes were considered and rejected, with the reasons kept so they are
not re-litigated:

- **Separate follow-up flows** (`review_fixes.yaml`, `conflict_fix.yaml`,
  each started by its signal as a new run). Clean per document, but one job
  becomes three documents, agents and prompts get copied, and the ticket's
  story is spread over unrelated run pages. A phase keeps one document, one
  run and one story.
- **`on_signal` edges on the final gate** routing back into the delivery
  pipeline (`mr_conflicts → sync → test → push → final_gate`). It loops the
  main flow, which the author must then read as a cycle; it grows the gate's
  syntax; and a review fix would pass through the adversarial reviewer while a
  human is already reviewing.
- **Fixing every conflict the moment the forge reports it.** Spends agent
  tokens on every move of `main` and on every single comment while a reviewer
  is mid-review. Instead: conflicts are **confirmed** by a human before any
  token is spent; review comments are **debounced** — when comments arrive
  someone is actively reviewing, so the phase starts only after a quiet hour
  and fixes everything in one batch, then replies to each thread.

## 1. Rules (normative — the acceptance criteria of the whole plan)

- **R1 — Entry.** Exactly one phase has `on: ticket`. It is what a filed
  ticket starts, and it runs exactly as a flow runs today. It ends when its
  stages reach `done`.
- **R2 — Reactions.** Every other phase has `on: <signal>`. Its `on:` is the
  entire decision of when it runs. Document order carries **no sequencing**.
- **R3 — One at a time.** A phase run has a **subject**: one of the run's
  merge requests, i.e. one checkout. One phase run is active at a time for
  the whole run — same workspace, same cursor. A trigger that holds while a
  phase is running (for this subject or another) is re-evaluated when that
  phase run ends; if it still holds, it runs then.
- **R4 — Tiebreak.** When several triggers hold at the same moment, subjects
  are taken in checkout order (the primary first) and, within a subject,
  phases in document order — explicit and visible, nothing else. The
  template lists `conflicts` before `review_fixes` so a comment fix is
  tested on a branch that already merges cleanly.
- **R5 — Confirm.** A phase with `confirm: true` raises an inbox decision
  when its trigger holds; the phase starts on approve, when nothing else is
  running. A pending confirmation is not a running phase. On approve the
  phase's actions evaluate the live condition — a conflict that vanished
  meanwhile makes `sync_target` a no-op.
- **R6 — No re-fire on the same fact.** Each trigger has a key per subject
  (the target tip for conflicts, the newest pending comment for reviews). A
  phase run — or a declined confirmation — for a key blocks another until
  the key changes. `max` bounds a phase's runs per subject over the merge
  request's life.
- **R7 — Budgets.** Each phase run resets the counters of its own edges
  (`fix.on_fail max 2` is per review round). `budget` stays run-wide and now
  covers the whole life.
- **R8 — Idle.** Between phase runs the run is simply idle with its merge
  requests open. Idle is the only moment triggers are evaluated, subject by
  subject. There is no waiting phase and no other state machine.
- **R9 — Ending.** All of the run's merge requests merged → `completed`; one
  closed unmerged → `abandoned`; a human may abort at any time. Implicit for
  phased flows (zero syntax); the editor draws it as fixed terminals.
- **R10 — Nothing existing changes.** A document with top-level `stages:` is
  a single `on: ticket` phase and keeps today's semantics exactly, including
  `final_gate` + `auto_approve`/`auto_reject`. Grammar version stays **3**:
  the change is additive.
- **R11 — The agent never runs git.** Merging the target, continuing a
  paused merge, pushing and posting replies are engine actions or command
  stages; the agent edits files and writes artifacts, as everywhere else.
- **R12 — A phase run acts on its subject only.** `$MR_URL` is the subject's
  merge request; `$CHANGED` is the subject checkout's diff since the phase
  run started; command stages run in the subject checkout (`each_repo` is
  refused inside a reaction phase); `sync_target`, `load_review_comments`
  and `reply_review_comments` touch that merge request and no other. The
  agent is told which checkout is the subject and that the others are
  read-only context: a fix that needs another repository is a reply, not an
  edit.
- **R13 — A phase run is the run itself.** Phases execute in the ticket's
  run: its event log, its cursor, its workspace (rebuilt on the same
  branches when the reaper took it), its `budget`. No sub-runs. `abort`
  keeps its meaning: the run is abandoned.

## 2. Grammar (`FLOWLANG §1/§2/§3`)

```yaml
name: default
version: 3
defaults: { … }                        # unchanged: per-stage fallbacks
budget:   { max_cost_usd: 25.0, max_total_turns: 600 }   # unchanged, now the whole life
agents:   [ … ]                        # unchanged: defined once, shared by every phase

phases:
  - id: deliver
    on: ticket                         # the entry; exactly one
    stages: [ … ]                      # today's stages, minus final_gate
  - id: conflicts
    on: { signal: mr_conflicts, confirm: true }
    stages: [ … ]
  - id: review_fixes
    on: { signal: mr_review_settled, quiet_for: 1h, max: 10 }
    stages: [ … ]
```

`on:` is a signal slug, or a mapping `{signal, quiet_for?, confirm?, max?}`.
`stages:` inside a phase is the existing stage grammar, untouched. Parse
rules, all errors:

- `phases:` and top-level `stages:` are mutually exclusive; `phases:` needs at
  least one entry; exactly one phase has `on: ticket`.
- Phase ids are slugs, unique; stage ids are unique **across the document**
  (one namespace, so the graph and the timeline need no prefixes).
- An edge or `on_success` inside a phase may target only stages of the same
  phase, or `done` / `abort`. `done` ends the phase run (its subject is idle
  again; for the entry phase, the delivery is complete). `abort` keeps its
  meaning: the run is abandoned.
- `each_repo` is refused on a stage of a reaction phase (R12): the phase run
  already is one checkout's.
- `signal` must be in the registry; a signal whose `Needs` is
  `merge_request` requires a `create_mr` in the `ticket` phase (same check
  `wait` stages have today).
- `quiet_for` is a duration ≥ 1m; `confirm` a bool; `max` ≥ 1, or `-1` only
  with `confirm: true` — the existing rule that unlimited needs a human on the
  link. Default `max: 10`. None of the three applies to `on: ticket`.
- `trigger` and `phase` are reserved stage ids in addition to `done`,
  `abort`, `start`.

The editor draws phases as columns, each with its own `done`; the terminals
`mr_merged → completed` and `mr_closed → abandoned` are drawn once, fixed.

## 3. M28 — Forge facts (`ports.Forge`)

Everything below is driven by two facts the forge port does not expose yet:
whether the merge request merges cleanly, and what its review threads say.
This milestone is valuable on its own — the run page shows both — and is a
prerequisite for every other.

- `MRState(ctx, mr)` becomes **`MRStatus(ctx, mr) → {State, HasConflicts,
  HeadSHA, Target}`**. GitLab: `GET /projects/:id/merge_requests/:iid`
  already returns `has_conflicts`, `detailed_merge_status`, `sha`,
  `target_branch` — same call, more fields. GitHub: `GET /repos/:r/pulls/:n`
  returns `mergeable`, `mergeable_state` (`dirty` = conflicts), `head.sha`,
  `base.ref`; `mergeable: null` means "computing", reported as unknown, never
  as a conflict.
- **`MRThreads(ctx, mr) → []Thread{ID, Path, Line, DiffHunk, Resolved,
  Comments[]{Author, Body, CreatedAt, Ours}}`**. GitLab: the discussions API
  (resolvable discussions, system notes dropped). GitHub: REST
  `pulls/:n/comments` + `pulls/:n/reviews` for bodies, and GraphQL
  `pullRequest.reviewThreads` for the thread grouping and `isResolved`, which
  REST does not expose. A review in `CHANGES_REQUESTED` state with a body is
  one thread.
- **`ReplyThread(ctx, mr, threadID, body)`** and **`ResolveThread(ctx, mr,
  threadID)`** (GitLab REST `resolved=true`; GitHub GraphQL
  `resolveReviewThread`).
- **"Ours"** is decided by a hidden HTML marker in every reply the engine
  posts (`<!-- apothecary run=<id> thread=<id> -->`), not by account identity
  — it is what makes replies idempotent (M31) and it works for a token that
  belongs to a human.
- Fake forge: scripted `HasConflicts`, threads, replies and resolution, with
  snapshots for assertions. GitLab and GitHub adapters get table tests
  against recorded responses.
- UI (`run.View`): `mr_status {conflicts, head_sha}` and `pending_threads`
  per merge request; the run header and the gate card show "mergeable" /
  "conflicts with main" and "3 unresolved threads · last 12m ago".

Acceptance: the two adapters pass their table tests; the fake drives a
scenario where a run's gate card flips to "conflicts" when the fake says so;
`MRState` has no remaining callers.

## 4. M29 — Phases in the grammar and the engine

Grammar per §2 (`internal/flow`: `DocPhase`, `Flow.Phases`, `Stage.Phase`;
`stages:` shorthand builds one `ticket` phase so every consumer sees phases).

Engine (`internal/engine`):

- **Events**: `phase_started {phase, subject, key, by: signal|human, tip}`,
  `phase_completed {phase, subject}`, `phase_declined {phase, subject, key}`.
  The run state gains the current phase and subject and, per subject, the
  keys already started or declined; replay rebuilds all of it. Edge
  counters are keyed by phase run (the `phase_started` count), which is R7;
  `budget` stays run-wide and now covers the whole life.
- **Entry phase end**: when a phased flow's `ticket` phase reaches `done`,
  the run does not complete. If it holds no merge request it completes (a
  flow without `create_mr` behaves as today); otherwise it is **idle**:
  projected state `in_review` in `run.View` (a projection for the UI, not
  machinery — R8), workspace parked, the existing MR poller armed.
- **Trigger evaluation** lives in that poller: while idle, subjects are
  visited in checkout order and each subject's reaction phases in document
  order (R4); the first signal that holds with an unseen key starts the
  phase for that subject (`by: signal`) or raises its confirm ping. One
  phase run at a time (R3). The ending signals (R9) are evaluated first,
  over all merge requests.
- **Phase start**: ensure the workspace (rebuild on the same branches when
  the reaper took it — the rebuild path exists; bootstrap's `git switch`
  tracks the remote branches); record `phase_started` with the subject's
  branch tip; run the phase's stages with the existing run loop, the
  subject being the cursor's context (R12: command stages run in its
  checkout, `$MR_URL` / `$CHANGED` resolve to it).
- **Confirm pings**: a gate-like ping type `phase_confirm` carrying the
  phase, the subject, the signal's explanation and the subject's `$MR_URL`;
  approve starts the phase once the run is idle, decline appends
  `phase_declined` (R6). It shows in the inbox and on the run page like a
  gate.
- **Manual start**: `POST /api/runs/{id}/phases/{phase}/start?repo=<name>`
  (`by: human`, key = the live one), refused while a phase is running; the
  run page shows a button per reaction phase on each subject's idle card.
- **Phase end**: `done` → `phase_completed`, the run is idle again and the
  poller re-evaluates. `abort` abandons the run as today.
- **Ending**: all MRs merged → `run_completed`; any closed → `run_abandoned`;
  human abort as today. For single-phase flows nothing changes (R10).
- **Recovery**: mid-phase is today's recovery within the phase; while idle
  it re-arms the poller. A `phase_started` without `phase_completed` on
  replay is "in this phase, on this subject, at this stage", exactly like
  today's cursor.
- UI: run timeline grouped by phase run, each labelled with its subject;
  one idle card per subject with status, pending threads, countdown to
  `quiet_for` and the per-phase buttons; inbox confirm item; flow editor
  columns; the New Ticket dropdown lists flows with a `ticket` phase.

Acceptance (scenario tests on the fake forge/git, `-race`):

- parse: the §2 rules, each with its line-precise error; `stages:` shorthand
  parses to one `ticket` phase; the default template parses.
- entry phase `done` with an MR → `in_review`; without an MR → `completed`.
- a signal holds → confirm ping → approve → the phase runs → idle again;
  decline → `phase_declined`, no new ping until the key changes.
- an automatic phase runs without a human; two triggers holding at once for
  one subject run in document order; a trigger during a phase runs after it;
  a phase's edge counters are fresh on its second run while `budget`
  accumulates.
- **two subjects**: conflicts on both → one phase run after the other, the
  primary first; a conflict on `api` and a review round on `web` → both run,
  in turn; each with its own `$MR_URL`, `$CHANGED` and checkout.
- `each_repo` in a reaction phase → parse error; `max` reached → the phase
  no longer starts for that subject (the idle card says so).
- merged → `completed`; closed → `abandoned`; human abort while idle.
- engine restart mid-phase resumes the stage on the right subject; restart
  while idle re-arms.

## 5. M30 — Signals `mr_conflicts` and `mr_review_settled`

Both derive from forge facts with **no stored cursor**, so there is nothing to
drift between the forge and the log:

- **`mr_conflicts`** holds when `MRStatus.HasConflicts` is true. Key:
  `(target tip, head sha)` — the pair the forge computed the conflict for.
  `Requires: forge`, `Needs: merge_request`.
- **`mr_review_settled`** holds when the MR has at least one **pending**
  thread and the newest pending comment is older than `quiet_for`. A thread
  is pending when it is unresolved and its latest comment is not ours
  (marker) — i.e. newer than our last reply in it, or we never replied. Key:
  the newest pending comment's id. `Requires: forge`, `Needs: merge_request`.
- Polling: status with every `MRPollInterval` (one call, already made);
  threads at a slower cadence (every 60 s, or 4× the interval, whichever is
  longer) — they are a second call per MR. The poller tolerates a forge
  error the way it does today: one blocker notification per streak, never a
  silent stop.
- Both are evaluated **per merge request** (R3): each subject has its own
  keys, its own quiet timer and its own `max`; nothing is pooled across a
  multi-repo run's merge requests.

Acceptance: table tests of both predicates over scripted thread lists (quiet
boundary, our own reply ends pending, resolved threads ignored, system notes
ignored, GitHub `mergeable: null` is not a conflict); a scenario where
comments at t, t+10m and t+50m start the phase once at t+110m.

## 6. M31 — The review phase: load, fix, reply

- **`load_review_comments`** (action, requires forge): for the subject's
  merge request, writes `artifacts/review.md` — one section per pending
  thread: file, line, author, the comments in order, the diff hunk, and the
  thread id; records
  `review_comments_loaded {thread_ids, newest}`. A second artifact
  `artifacts/review.json` carries the same for tooling. Idempotent: re-entry
  rewrites the files from the recorded ids, not from a new fetch, so a phase
  run fixes the set it started with.
- **The builder gains an optional input `comments`**; its prompt says: if
  the prompt carries review comments, address every one in the subject
  checkout (named in the prompt — R12), and leave a note in the working tree
  only where a comment is declined on purpose. No new agent for fixing —
  the fixer is the builder.
- **`type: review_replies`** — a new output type (beside `verdict`):
  `artifacts/replies.yaml`, a list of `{thread, reply, resolve: false}`.
  Validated when the stage ends (`output_invalid` with the reason as
  `$FAILURE`): every `thread` is among the loaded ids, every `reply` is
  non-empty, no id twice; an empty list is a valid output. The `replier`
  agent (read-only, bash for `git diff`) writes it from `review.md` and
  `$CHANGED`; its prompt: say what was changed and where, or why not;
  `resolve` stays false — on most teams the reviewer resolves.
- **`reply_review_comments`** (action, requires forge): posts each reply
  with the marker, resolves where `resolve: true`, records
  `review_replies_posted {thread_ids}`. Intent + reconciliation as
  `create_mr` (PLAN5 M20): `action_started` before the first post; on
  re-entry the forge is asked which threads already carry this run's marker
  and only the rest are posted. A forge error is terminal, as for
  `create_mr`.
- Template phase (document order after `conflicts`):

  ```yaml
  - id: review_fixes
    on: { signal: mr_review_settled, quiet_for: 1h, max: 10 }
    stages:
      - { id: load,           type: action,  action: load_review_comments }
      - { id: fix,            type: agent,   agent: builder,
          input: { comments: artifacts/review.md, failure: $FAILURE },
          on_fail: { to: fix, max: 2, then: { to: debug_fix, max: -1 } } }
      - { id: fix_commit_msg, type: agent,   agent: committer, input: { failure: $FAILURE },
          on_fail: { to: fix_commit_msg, max: 1, then: { to: debug_fix, max: -1 } } }
      - { id: fix_commit,     type: command, run: "git add -A && git commit -F artifacts/commit.md",
          on_fail: { to: debug_fix, max: -1 } }
      - { id: fix_test,       type: command, run_ref: verify,
          on_fail: { to: fix, max: 3, then: { to: debug_fix, max: -1 } } }
      - { id: fix_push,       type: command, run: "git push -u origin HEAD",   # in the subject checkout (R12)
          on_fail: { to: debug_fix, max: -1 } }
      - { id: reply,          type: agent,   agent: replier,
          input: { comments: artifacts/review.md, changes: $CHANGED, failure: $FAILURE },
          on_fail: { to: reply, max: 1, then: { to: debug_fix, max: -1 } } }
      - { id: post,           type: action,  action: reply_review_comments,
          input: { replies: artifacts/replies.yaml } }
      - { id: debug_fix,      type: human_gate, input: { workspace: $WORKSPACE },
          question: "The review-fix round failed. Inspect the workspace, then approve to retry the failing stage (reject abandons the run).",
          on_success: fix, on_reject: abort }
  ```

  No adversarial reviewer in this phase by default: a human is reviewing.
  An author may add one.

Acceptance: a scenario with three pending threads on one merge request →
the phase runs after the quiet hour on that subject only → builder prompt carries all three → replies posted with markers
→ threads no longer pending → a fourth comment later starts a second round
whose `fix.on_fail` budget is fresh; a crash between two reply posts
reconciles and posts only the missing one; a `replies.yaml` naming an
unknown thread fails `reply` with `output_invalid`.

## 7. M32 — The conflict phase: `sync_target` (merge) and the resolver

- **A literal-valued action input.** Action inputs are files or sentinels
  today; `sync_target` needs `strategy: merge | rebase`. `InputSig` gains
  `Kind: enum` with `Values`; the parser checks the literal against them
  (`FLOWLANG §4` binding table: *action — a literal from the signature*).
  Until M33, `rebase` is a parse error naming the milestone.
- **`sync_target`** (action): `git fetch origin <target>`; if
  `origin/<target>` is already an ancestor of HEAD → `on_success`, nothing
  recorded (R5's no-op). Else `git merge --no-edit origin/<target>`. Clean →
  `target_synced {strategy, target_tip, result_tip}` → `on_success`. A
  conflict leaves the merge **paused in the working tree** and fails the
  stage (`on_fail`) with the conflicting files named. On re-entry with a
  paused merge of the same target: the still-unmerged files are checked for
  the two labelled markers (`<<<<<<< `, `>>>>>>> `; a bare `=======` is a
  legitimate line in many files); markers left → fails again naming them;
  none → `git add -A && git commit --no-edit` → `target_synced`. A paused
  merge of a *different* target tip is aborted first and redone. All of it
  in the subject checkout, against the subject's source branch (R12).
- **Conflicting files for the confirm ping**: when the workspace is up, a
  side-effect-free `git merge-tree --write-tree origin/<target> HEAD` lists
  them; when it is parked, the ping says "files known once the workspace is
  up". Never a real merge to find out.
- **`resolver`** agent (read-write, bash allowed): edits only the named
  files, keeps both intents (the target's change and the branch's), removes
  every marker, runs the build/tests when they can run, and **runs no git**
  — the prompt names the commands that would corrupt the state. Reused from
  the stacked-change prototype, which validated the pattern.
- Template phase (first among the reactions, R4):

  ```yaml
  - id: conflicts
    on: { signal: mr_conflicts, confirm: true }
    stages:
      - { id: sync,       type: action,  action: sync_target, input: { strategy: merge },
          on_fail: { to: resolve, max: 6, then: { to: debug_sync, max: -1 } } }
      - { id: resolve,    type: agent,   agent: resolver,
          input: { conflict: $FAILURE }, on_success: sync,
          on_fail: { to: debug_sync, max: -1 } }
      - { id: sync_test,  type: command, run_ref: verify,
          on_fail: { to: debug_sync, max: -1 } }
      - { id: sync_push,  type: command, run: "git push -u origin HEAD",   # in the subject checkout (R12)
          on_fail: { to: debug_sync, max: -1 } }
      - { id: debug_sync, type: human_gate, input: { workspace: $WORKSPACE },
          question: "Merging the target conflicts and the resolver could not settle it. The merge is paused in the workspace: resolve the markers in the named files (edit the files only — the engine commits), then approve to retry (reject abandons the run).",
          on_success: sync, on_reject: abort }
  ```

  A merge never rewrites history, so the plain push stays correct and the
  reviewers' inline comments keep their anchors.

Acceptance: fake-git scenarios — up to date → no-op; clean merge → synced,
tested, pushed, idle; conflict → resolver clears markers → continued →
pushed; resolver leaves markers twice → human gate → human clears → approve
→ continued; the confirm ping lists the files from the dry run; declined →
no new ping until `main` moves; crash between the merge commit and the event
replays idempotently (the branch already contains the target).

## 8. M33 — Rebase strategy and `push_branch`

Rebase rewrites the branch, so the push after it must be a force — and a
force is only safe **leased against the tip the engine last pushed**, which
a `git push` command stage cannot know. Hence:

- **`push_branch`** (action): for the subject checkout (every checkout in
  the entry phase), `git push
  --force-with-lease=<branch>:<last recorded tip>` (first push: a plain push
  that refuses a remote branch holding commits this run never made), then
  `branch_pushed {repo, branch, tip}`. Idempotent on re-entry (tip equal →
  nothing to do). Templates replace their `git push -u origin HEAD` command
  stages with it; the command form stays valid for authors who prefer it
  with the merge strategy.
- **`strategy: rebase`** in `sync_target`: `git rebase origin/<target>`;
  conflicts pause per replayed commit and are resolved the same way, with
  `rebase --continue` (and `--skip` for a commit the resolution emptied);
  `target_synced` records the new tip; the push must be `push_branch`
  (parse error otherwise: *rebase needs a leased push*).
- The confirm ping for a rebase says so: reviewers' inline comments may lose
  their anchors on the forge.

Acceptance: fake-git scenarios mirroring M32 for rebase, plus: a remote that
moved under the branch makes the leased push fail with the reason, never
overwrite; a crash between the push and `branch_pushed` reconciles from the
remote's tip when it is an ancestor of ours.

## 9. Explicitly not doing (and why it is safe to wait)

- **Parallel phase runs** (two subjects at once). It needs a second cursor
  in the run loop and a second agent turn in the workspace — the
  parallel-cursors rework PLAN5 reserved. Subjects take turns (R3, R4);
  revisit when a multi-repo project actually feels the wait.
- **Cross-repository fixes inside a phase run.** A reviewer's "also update
  the client" on the `api` merge request is answered in the reply and left
  to a human or a new ticket (R12); letting a phase run edit a second
  checkout would race the phase runs of that checkout.
- **Entry triggers other than `ticket`** (`schedule`, forge issue events).
  The `on:` slot is the seam; nothing here needs them.
- **Per-ticket phase overrides.** Phases are the flow's; a ticket picks a
  flow.
- **Auto-resolving threads.** `resolve: false` by default; the replier may
  be told otherwise by a flow author.
- **Loops / `for_each`, a `parameters:` block, merging `defaults` and
  `budget`.** None is needed by the above; the last two would be a grammar
  break for cosmetics.
- **Reacting to CI results on the forge** (`mr_pipeline_failed`). A natural
  fourth phase later: same grammar, one more signal.

## 10. Decisions taken (defaults an author can change in the flow)

| decision | default | where it lives |
|---|---|---|
| conflicts need a human's go | `confirm: true` | the phase's `on:` |
| review fixes wait for a quiet review | `quiet_for: 1h` | the phase's `on:` |
| how to take the target in | `strategy: merge` (rebase in M33) | `sync_target`'s input |
| replies resolve threads | no | `replies.yaml`, written by the replier |
| rounds per MR | `max: 10` (confirm phases may say `-1`) | the phase's `on:` |
| a review fix is re-reviewed by the adversarial reviewer | no | add the stage to the phase |
| a phase run edits a repository other than its subject | never | R12 |
| phase runs on different merge requests run in parallel | no — they take turns, primary first | R3 / R4 |

Ordering of the work: M28 → M29 → M30 → M31 → M32 → M33. M28 and M29 are
independently useful (status on the run page; phases with manual start);
M30 is small; M31 and M32 are the features; M33 is optional until a project
asks for linear history.
