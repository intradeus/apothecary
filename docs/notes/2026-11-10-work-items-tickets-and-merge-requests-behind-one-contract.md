# Work items: tickets and merge requests behind one contract (2026-11-10)

- Every place that started, guarded or listed runs branched on "ticket or
  merge request" by hand:
  - two busy errors (`ErrTicketBusy`, `ErrMRBusy`) with two lock maps;
  - `RunsOfTicket` beside `RunsOnMR(s)`;
  - `startsOn(fl, onMR bool)`;
  - `adoptMR` inside `CreateRunWith`;
  - `MRWork`, which sat in `project/tickets`;
  - a two-tab New run dialog and two copies of the start panel.

  A third kind would have touched all of them.
- `internal/workitem` is the contract. A kind is a value of a flow's
  `operates_on`. `Kind` says:
  - how a start names one of its items (`Requested`);
  - which items a run holds, read from its view (`Held`);
  - how a new run is pointed at it (`Prepare`: its branch, or a `Repoint`
    of a checkout onto the item's own branch);
  - whether a flow of its kind requires one (`Required`), and whether one
    may come along with a run on another kind (`Accompanies`).
- The `Registry` (built once in `workitem/items`, in preparation order)
  turns that into what the engine needs: `Requested`, `StartsOn`, `Lock`
  and `Idle` (`ErrBusy`), `Prepare`, `Holds`. The engine names no kind.
  Its `CreateRunWith` locks and checks the requested items, has the
  registry check the flow's `operates_on`, lets the items prepare the
  start, then applies their repoints. `RunsOf(ref)` and `ItemRuns(project,
  kind)` replace the per-kind run lookups.
- The two kinds:
  - `workitem/ticket` owns the base + run-tail branch rule.
  - `workitem/mergerequest` owns the MR's checkout (from `adoptMR`), `Work`
    (what the run is given), `Key`/`Ref`/`ParseKey` (`repo!iid`), and
    `CheckOpen`/`ErrNotOpen`.
- `CheckBranch` moved from the engine to `workspace/checkout`: both kinds
  and the API need it.
- A `Ref` carries its project: merge request numbers are only unique
  within a repository.
- Kept per kind, because they genuinely differ:
  - where items come from (`project/tickets` and trackers; the repository
    host via `engine/mrs`);
  - a ticket's tracker refresh and tracker preflight;
  - how a rerun rebuilds its inputs;
  - each kind's page.
- A test pins the registered kinds to the language's `operates_on`
  values, on both sides.
- The UI mirrors the contract:
  - `$lib/app/workItems.ts` is the registry of kinds. Each has its labels,
    icon, chord, list page, whether its items live per workshop, the New
    run dialog's texts, and an `options(projectId)` loader that gives each
    item's page.
  - The sidebar entries and the New run dialog's tabs are generated from
    it.
  - `$lib/run/StartRunPanel` is the one start panel every work item page
    mounts. A kind's own fields come in through `before` and `after`
    (a ticket's direction and source branch, a merge request's state).
- A new kind costs:
  - a `workitem/<kind>` package and a line in `workitem/items`;
  - its `operates_on` value in the language;
  - its source and start route;
  - a `WORK_ITEMS` entry and its page.

  The engine, the busy rule, the automations' "already processed" check,
  the dialog and the sidebar do not change.
