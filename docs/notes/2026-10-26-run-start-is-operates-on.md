# `run_start:` is `operates_on:`

2026-10-26, the user: `run_start: merge_request` — and the editor's "Runs
start on: a merge request someone opened" — "gives the impression that it is
automated", as if a merge request starting were what launches a run. The key
says what a run works on, chosen by whoever starts it; of ten names, the user
chose `operates_on` (over `starting_point`, which keeps the "start" reading).

- **Grammar** (still `version: 5`, unreleased): `operates_on: ticket |
  merge_request`, default `ticket`; `run_start:` is refused with "run_start:
  is now operates_on:" (`Doc.LegacyRunStart`). The templates `reviewer` and
  `conflict-resolver` say `operates_on: merge_request`.
- **Code and API**: `Doc.OperatesOn`, `Flow.OperatesOn`, `OperatesOnTicket` /
  `OperatesOnMR`, `Flow.OperatesOnMR()`; flow responses and validations carry
  `operates_on`; the UI's `FlowDoc.operates_on`, `Flow.operatesOn`,
  `OPERATES_ON`.
- **UI**: the field reads *Operates on* (a ticket / a merge request someone
  opened), its help saying it is not what starts a run; the graph's main
  marker reads *main · operates on a merge request* (a flow without a main
  chain: *run · operates on …*).
- **Not renamed**: the `run_start` *signal* — how the main chain opens, the
  run being started by a person or an automation — which is what it says.
  Saved flows and pinned run texts that still say `run_start:` no longer
  parse (no compatibility before release): convert them by hand.
