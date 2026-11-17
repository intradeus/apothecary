# operates_on nothing

Why: a run needed a ticket or a merge request to exist. To get a bare
workspace — "just me, launching a sandbox to do some manual work" — one had
to write a ticket first; the built-in `workspace-only` read `run.ticket` for
that reason alone. The owner asked for a third value of `operates_on`:
`nothing`, a run with no source at all, started by hand or on a schedule,
with agent stages allowed (their prompts are what they are given).

What now:

- **Language.** `operates_on: nothing` (FLOWLANG §1). The flow needs its
  `main` chain. What needs a ticket is refused where it is written: an input
  from `run.ticket` or `run.ticket.comments`, a `run_if` on
  `run.ticket.tracker` or `run.ticket.change_type`, a tracker action
  (`actionspec.RequiresTracker`). On a ticket flow started with no ticket
  the engine skips these at run time; here they could never do anything, so
  the parser says so. Merge request stages, watchers and outcomes stand: the
  run has a branch and may open one.
- **Work items.** `workitem/nothing` is the registered kind (the registry's
  kinds are exactly the language's values). It has no items: `Requested`
  names none, `Held` none. `Kind` gained `Alone()`, so `StartsOn` refuses a
  start of such a flow that names any item — a ticket may otherwise
  accompany any flow. No item means no lock and no one-run-at-a-time: any
  number run at once, bounded by the environment cap.
- **Start.** `POST /api/projects/{id}/runs`, everything optional: `title`
  (`manual run - <UTC time>` when left out; the dialog sends the same in the
  person's local time), `branch` (the repository's default), `new_branch`,
  `on_branch`.
  The route refuses a flow that is not `nothing` — the engine itself still
  takes a bare start of a ticket flow (tests, old runs), so the handler
  checks. In the UI it is the third tab of New run, which starts the run
  there since there is no item page to go to.
- **Branch.** Two options were on the table: always fork, or check out the
  source branch and let the person branch by hand. The owner chose both,
  fork by default: the run gets `apothecary/<name>-<run tail>` forked from
  the branch picked, local to the workspace until something pushes it
  (`checkout.BranchScript` only switches); with `on_branch` the run's branch
  IS the one picked (`Ticket.Branch == SourceBranch`, which the branch step
  already handles by switching to it). On the branch itself `push_branch`
  pushes to it — it pushes `HEAD:refs/heads/<run branch>` — which is why it
  is opt-in. The owner then asked that the fresh branch's name could be
  typed, as a ticket's can: `new_branch` is used as written (no suffix: one
  run, one name, and a rerun keeps it), where a ticket's is a base each of
  its runs adds a tail to. It needed nothing of the engine, which takes a
  bare start's `Ticket.Branch` as written. Because the branch step re-enters
  a branch the remote already holds, a typed name could put a run on an
  existing branch without the opt-in: the route refuses `new_branch` with
  `on_branch` and `new_branch` naming the branch it starts from, and the
  form refuses the name of any branch the repository lists (Start is
  disabled). A branch the list does not show is not caught: the run
  re-enters it. The dialog shows that name rather than leaving it to be
  found out: the field opens already written, `apothecary/<name>-<tail>` —
  the engine's own shape, following the run's name until the person writes
  another — and ticking or unticking the checkbox ("Checkout branch
  <branch>") puts the written one back. The engine's tail is the run id's,
  which no one knows before the run exists, so the dialog draws four of the
  same letters and sends the name as `new_branch`: what was read is what
  the run gets. Two things follow. A run started from the dialog has a
  start-chosen branch, so its rerun stays on that branch, where an
  engine-named one (the API with no `new_branch`, a schedule, the field
  emptied) gets a new tail. And an unnamed run is called what the dialog
  showed when it opened, not the minute Start was pressed, or the name and
  the branch would disagree. The ticketless fallback name changed for every
  bare start, from
  `apothecary/<26-char run id>-<slug>` to the ticket runs' shape, and
  records its base; a rerun names an engine-named branch again (new tail)
  and keeps one the start chose, old-format names included.
- **Schedules.** A `cron` automation may name a `nothing` flow and then
  takes no `ticket_id` (a ticket flow still needs one; `tracker_query` and
  `merge_request` automations refuse a `nothing` flow). Each slot starts a
  run named `<automation> - <UTC time>`. Such a run holds no work item to be
  busy on, so the automation is: `run_created` now records `automation`
  (its id, for every kind of automation; `View.AutomationID`), and a slot
  that falls while the automation's previous run is still going is skipped
  and recorded, like a slot on a busy ticket. Without it a flow that waits
  on a person would fill the instance one workspace a slot. No new column:
  the link rides on the event and the runs projection.
- **`workspace-only`** is now `operates_on: nothing` with no input. It can
  no longer be started from a ticket; flow pickers beside a ticket, and on
  the rerun of a ticket's or a merge request's run, leave `nothing` flows
  out (`FlowSelect exclude`).

Not built: a goal typed at the start and handed to agent stages, a source
branch or on-branch choice on a schedule (it forks from the default
branch), and attachments on the start.

Tests that hold it: `flow` `TestOperatesOnNothing`,
`TestOperatesOnNothingHasNoTicket`; `workitem` `TestStartsOn`,
`TestKindsAreTheLanguagesOperatesOn`, `nothing` `TestNothingHasNoItems`;
`engine` `TestRunOnNothing`, `TestRunOnNothingOnTheBranchItself`,
`TestRunOnNothingOnANamedBranch`,
`TestRerunRegeneratesAutoBranch`, `TestRerunRegeneratesALegacyAutoBranch`;
`httpapi` `TestStartARunOnNothing`, `TestAFlowOnNothingIsNotStartedOnATicket`;
`confstore` `TestAutomationValidates`; `automation` `TestCronOnNothing`,
`TestCronOnNothingFailures`; `builtin` (workspace-only); CPUI
`NewRunDialog`, `NothingRunForm`, `manualRunName`, `AutomationsTab`,
`FlowSelect`, `inspector` tests.
