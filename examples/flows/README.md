# Reference flows

Ready-to-load configuration for a standard plan-gated pipeline. The flow
language itself is specified in `docs/flow-language.md`.

- `v3.yaml` — the reference flow in the expanded form the graph editor
  emits (`on_success` on every stage, rescue gates last, one key per
  line). Paste into a new flow in the UI, or `PUT /api/flows/reference`.
  Validate anytime: `task validate -- examples/flows/v3.yaml`.

The agents it references (planner, builder, committer, reviewer,
redactor) are **declared inside the flow document itself** (FLOWLANG v3):
a flow carries its agents, so there is nothing to install separately and
no copies here to drift.

Before running the flow on a project: set a `verify` command on the
project, and check the review stage's model pin is routable by your
harness auth (or change/remove it).
