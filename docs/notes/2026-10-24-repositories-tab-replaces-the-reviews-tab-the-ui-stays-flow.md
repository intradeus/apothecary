# Repositories tab replaces the Reviews tab: the UI stays flow-agnostic

2026-10-24, the user: "Review is a flow… flows shouldn't have distinctions in
the UI… the UI should be as abstract as possible." The Reviews tab (above)
was built around one template. It is gone; in its place:

- **Repositories tab** (Workshop → Repositories, beside Tickets): what the repository host
  integration brings in — the repository's open merge requests — the way
  Tickets shows what the tracker does. Each row: the merge request (its
  author; draft, fork, "opened by the factory"), its branches, its newest
  run (state, flow, when; all its runs through its ticket) and **Start a
  run**: a picker of every entrypoint on `mr_opened` of every flow of the
  workshop (`flow · phase` when a flow has several). A review, a rebase,
  anything a flow does on a merge request is one pick away; none is named.
- **API, generic too**: `GET /api/projects/{id}/mrs` now returns those rows
  (`MRRow`: the merge request, `mine`, ticket, newest run); `POST
  /api/projects/{id}/mrs/{iid}/runs {flow?, phase?, params?, repo?}` starts
  a run of any flow on it (the project's default flow when unnamed — no
  built-in name baked in). The guard is per flow: the same flow twice at
  once on one merge request is refused (a double click), different flows
  may run side by side. `/reviews` and `/mrs/{iid}/review` are removed.
- The start-run form's merge-request picker reads the same `/mrs`.
- The rest of the UI was audited for the same rule (next entry).
