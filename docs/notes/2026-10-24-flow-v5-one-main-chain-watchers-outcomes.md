# Flow v5: one main chain, watchers, outcomes

2026-10-24, the user: "I like the concept of watchers — phases which react to
events. Different than the main phase … a flow becomes 1 phase only — the
main action phase … and in the background, while it runs, watchers for merge
conflicts, PR comments etc." The design (plan: *Flow v5*) keeps v4's
machinery — wait points, one workspace and mutex per run, the idle loop,
blocks, parameters, pinning — and changes the document. This is its first
step: the grammar, the engine, the templates, the API and the UI. `use:`
(other flows' chains run in place, compiled into the pinned text) and one
writer per branch are the next ones.

- **The document** (`internal/flow`): `version: 5`; `run_start: ticket |
  merge_request` says what runs are started on (a person or an automation
  starts one — never something the flow listens for); `main:` is the chain a
  run starts with; `watchers:` run their chain on each new occurrence of a
  signal, or by hand (`on: manual`), and never end the run; `outcomes:` fire
  once and end it, after their chain if any, `then: done` (default) or
  `abort`. `run: X` is a chain of one include of `X` named after its watcher
  (`again.read`, …), and `main` is a block other chains may run. The parser
  still produces one runtime `Flow` with `Phases` — now tagged with a `Kind`
  (main, watcher, outcome) and ordered by section — so the scheduler, the
  holds, the start policies and the done rule did not change. A flow with no
  `main` is a caretaker: it must be `run_start: merge_request` and its runs
  start idle. v4 documents are refused with a message naming the v5 spelling
  (`phases:` / top-level `stages:`).
- **One way in.** `mr_opened` is gone (it was only a `run_start` with a merge
  request), and so is the per-run entrypoint: `RunInput.Phase`,
  `run_created.phase`, the view's `begin_phase`, the start-run, rerun, merge
  request and automation bodies' `phase`, and the automations' column
  (migration `0008_automation_phase.sql`). Run creation checks the flow's
  `run_start` against what the run is started on; an automation's kind must
  fit its flow's (`merge_request` ↔ `merge_request`, `tracker_query` / `cron`
  ↔ `ticket`) at save. The main chain is never started by hand
  (`ErrPhaseNotStartable`: "rerun the run").
- **The section decides** (`internal/signalspec`). A signal says what it is —
  `Kind` start (`run_start`), hand (`manual`) or observed — and any observed
  signal may open a watcher, fire an outcome, be waited on or answer a gate.
  The engine evaluates every one per merge request (`signalOn`): it holds (an
  occurrence key), the merge request is past it (merged: past approval and
  being mergeable), or it can never hold there again. Watchers run per merge
  request — every merge request now, not only open ones, so a watcher on
  `mr_merged` or `mr_closed` works; waits and gates on the chain's subject, or
  on the whole run when it has none; outcomes on the whole run, by the
  registry's new `All`: `mr_merged`, `mr_approved` and `mr_mergeable` hold
  once every merge request has them (no subject), the others as soon as one
  does (that one the subject). `mr_approved` as a watcher is keyed by the
  approved head: a push, approved again, is a new round. `wait for:
  mr_mergeable` parses now.
- **Merges and reviews pin to the newest head the chain knows**
  (`phaseHead`): the tip it began on, then each tip it pushed to its subject
  (`notePush`, shared by replay and the live push; kept across a park).
  `mr_merge` after the chain's own rebase and push merged nothing before —
  pinned to the head the chain began on, the repository refused it. The
  rebase → wait mergeable → merge pattern (a `ship` watcher) works now
  (`TestMRMergeIsPinnedToTheHeadTheRoundPushed`).
- **Outcomes always carry their `then`** (`done` by default), so the run page
  and the editor say how each ends; the done rule counts only a chain-less
  outcome's `then`, a chained one's last stage being counted as a stage.
- **Templates**: `builder` (main, watchers `conflicts` / `review_fixes`,
  outcomes `merged` / `closed` with `then: abort`), `reviewer` and
  `conflict-resolver` (`run_start: merge_request`, main runs the block, the
  watcher `again` runs it again) — stage ids unchanged (`review.*`,
  `rereview.*`, `settle.*`, `resettle.*`); the phase their runs begin with is
  `main`. The `-dev` sandbox flow too (`serve -dev` refused to start on it —
  now `TestDevFlowParses`).
- **The UI.** The editor's document model reads and writes the three
  sections (`run:` written back when a chain is one include of its own name);
  the canvas groups the rows under faint section labels in the marker column
  (main, watchers, outcomes — once a flow has more than one chain), the main
  marker says what runs are started on, an outcome's marker wears its end
  (green done, rose abort). The inspector has a panel per kind — the main
  chain's `run_start`; a watcher's signal (observed ones and *by hand*),
  policy, quiet and max; an outcome's signal, policy and `then` — and the
  flow panel adds watchers and outcomes. A chain added after the layout was
  made goes at the end of its section and the sections below move down
  (`placeChain`). The run page's Phases window is **Watchers**; the start-run
  form lost its entrypoint picker (a flow started on merge requests asks
  which one); the Repositories tab and the automation form offer flows by
  `run_start`.
- **Not compatible with v4 data.** A saved v4 flow no longer parses (the
  editor shows the error; re-saving it in v5 fixes it), and a run whose pinned
  flow is v4 fails on recovery with the parse error, as any flow that stops
  parsing does.
