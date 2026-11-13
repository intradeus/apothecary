# CPUI config surface (M8, DESIGN2 §11)

- **Forms are descriptor-driven end-to-end**: IntegrationForm renders from
  `GET /api/kinds` fields — adding a backend kind (github, codex in M10)
  needs zero frontend work. Secret inputs are write-only: password inputs
  (textarea for `*_json` fields — a naming convention, not schema; recorded
  as the boring heuristic), placeholder shows the stored mask, and only
  typed keys are sent, so an untouched field keeps its value (§3 merge).
  Clearing a secret from the UI is deliberately unsupported (edge case;
  the API's `""`-deletes semantics remain curl-able).
- **The project wizard is one sectioned form**, not a stepper: repository host pick →
  Browse repositories (ListRepos → click fills url/locator/branch/name) →
  commands as free key/value rows (setup optional and labeled so) →
  adapter picks → workspace (template required with an M9 note, fragment
  textarea already wired for M9) → default flow. Same component edits
  existing projects.
- **Flow editor is a plain textarea** (§11: no component library; a code
  editor widget is a later upgrade): Validate button surfaces the parser's
  line-precise errors verbatim; Save bumps the content-addressed version;
  history offers "load into editor" (restore = save that text again —
  versions themselves stay immutable).
- Ticket form gained the required project select (defaults to the newest
  project); flow field empty = project default. Runs list/table carry a
  project column and honor `/?project=` deep links from project pages.
- Nav grew to Runs · Inbox · Projects · Flows · Integrations · Settings ·
  Channels — flat wins over nesting at this page count.
- **M8 verification**: svelte-check 0 errors; the full wizard sequence
  (create integrations → Test → repo picker → project → preflight → ticket
  on the project default flow → gates → run_completed with project_id →
  flow validate/save/versions → settings PUT) exercised live against
  `serve -dev` + Postgres through the exact endpoints the UI calls; the
  production compose stack rebuilt and rolled to the new UI. The
  real-MR-through-the-UI click-through (fresh DB, real tokens) is Lu's
  acceptance pass, per the M5 precedent.
