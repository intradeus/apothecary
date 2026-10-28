# Reference flows

Ready-to-load configuration for a standard plan-gated pipeline. The flow
language itself is specified in `docs/flow-language.md`.

- `v5.yaml` — the reference flow in the expanded form the graph editor
  emits (`on_success` on every stage, rescue gates last, one key per
  line). Paste into a new flow in the UI, or `PUT /api/flows/reference`.
  Validate anytime: `task validate -- examples/flows/v5.yaml`.

The agents it references (planner, builder, committer, reviewer,
redactor) are **declared inside the flow document itself** (FLOWLANG v3):
a flow carries its agents, so there is nothing to install separately and
no copies here to drift.

Before running the flow on a project: set a `verify` command on the
project, and bind `model` (Workshop → Flows → Configure) — the flow carries
no model id of its own, so a run is refused until the project says which.

The two tracker stages (`in_progress`, `ticket_done`) move the ticket's
tracker issue in canonical statuses that the ticket's tracker translates
(Jira through its integration's status map, GitHub Issues by closing the
issue); on a ticket written by hand, or a status the tracker has no word
for, they skip.
