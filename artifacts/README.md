# Reference configuration artifacts

Ready-to-load configuration for a standard plan-gated pipeline. The flow
language itself is specified in `docs/flow-language.md`.

- `flows/v2.yaml` — the reference flow in the expanded form the graph
  editor emits (`on_success` on every stage, rescue gates last, one key
  per line). Paste into a new flow in the UI, or `PUT /api/flows/reference`.
  Validate anytime: `task validate -- artifacts/flows/v2.yaml`.

The agents it references (planner, builder, committer, reviewer,
redactor) are **compiled into the binary** and already present in every
project's Agents panel — saving one under the same name creates your
editable override. Their source of truth is
`internal/builtin/agents/*.yaml`; there are deliberately no copies of
them in this directory, because a copy is only ever a drift risk.

Before running the flow on a project: set a `verify` command on the
project, and check the review stage's model pin is routable by your
harness auth (or change/remove it).
