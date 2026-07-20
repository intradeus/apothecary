# Reference configuration artifacts

Ready-to-load configuration for a standard plan-gated pipeline — the
UI-era successor of the v1 built-in flow.

- `flows/v1.yaml` — the 9-stage flow in the explicit-edge format
  (`on_success` on every stage, `done` terminal). Paste into a new flow
  in the UI, or `PUT /api/flows/v1`. Validate anytime:
  `task validate -- artifacts/flows/v1.yaml`.
- `agents/*.md` — one file per agent the flow references (planner,
  builder, reviewer, redactor): frontmatter = the Agents panel's fields,
  body = the prompt.

`examples/agents/*.yaml` holds the same specs in the neutral agentspec
format; if you edit prompts here, mirror them there — these are
hand-maintained copies, not generated.

Before running the flow on a project: create the four agents on the
project's Agents panel (paste from `agents/*.md`), set a `verify` command
on the project, and check the review stage's `openai/gpt-5` pin is
routable by your harness auth (or change/remove it).
