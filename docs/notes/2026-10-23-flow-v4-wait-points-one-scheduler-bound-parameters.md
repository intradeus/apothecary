# Flow v4: wait points, one scheduler, bound parameters, canonical statuses

The flow language felt static for two reasons. One YAML carried three
concerns — the recipe, the delivery lifecycle and the environment bindings —
and the lifecycle part was hardwired: the entry was `Phases[0]` by position,
manual only, once; a run could only end from its merge requests; reactions
were evaluated only against open MRs; `create_mr` was entry-only; a `wait`
polled inside the stage loop and a gate paused the whole run. About fifteen of
the parse rules existed only to force a graph into that envelope, and the
engine had five waiting mechanisms (phase triggers, confirm pings, hand
starts, wait stages, gates) that were one thing implemented five ways. And
nothing was reusable across environments: `to: "In Progress"` was a Jira
literal, and there were no parameters, so the same recipe on another project
was a full copy that drifted.

v4 (2026-10-23, no compatibility: the project is unreleased, dev databases
were reset) collapses the mechanisms into one primitive and moves the
environment out of the document. FLOWLANG *The model* is the normative
statement; what the code does:

- **Wait points, one scheduler** (`internal/engine/scheduler.go`,
  `phase.go`, `state.go`). A phase's `on:`, a `wait` stage and a `human_gate`
  are one thing: a source, a policy (`auto` / `confirm` / `manual`), a
  subject, an occurrence key. The idle loop walks every wait point in document
  order — parked stages of live phase runs and unopened heads alike — and
  opens or resumes one per tick; the chosen phase run holds the workspace
  until its next wait point or `done`. A wait or gate stage records
  `phase_parked` and returns to the loop instead of polling or pausing;
  `phase_resumed` closes the park. Per-phase-run cursors (`phaseRun`) replace
  the one cursor; a phase has at most one live run. Holds under `start:
  manual` are memory-only, recomputed each tick, exposed on the run page.
- **The end rule.** `endCheck`: a run completes when nothing is parked and no
  firing phase is armed (policy auto/confirm, an open MR subject, runs left,
  or an open confirm ping). `done` only finishes a phase; a closed-unmerged MR
  abandons a run only through the flow (`wait_merge … on_fail: abort`). The
  parser warns about a `create_mr` no wait ever waits on.
- **Explicit entrypoints.** `run_start` joined the signal registry (`Entry`);
  a flow needs one such phase, may have several, and a run is created at one
  of them (`RunCreated.Phase`, `run.View.BeginPhase`; the start form and the
  rerun modal pick). `manual` is a phase only the run page opens; any phase
  without a live run can be started by hand (`StartPhase` takes a `subject`
  body). `max` counts hand starts, defaults to 10 for firing signals and
  unlimited otherwise.
- **Parameters** (`internal/flow` `parameters:`, `Flow.Resolve`,
  `internal/engine/params.go`). One block declares every knob with its
  default; six engine-known names always exist. Values are layered flow
  default ← project binding (`flow_bindings`, `confstore/bindings.go`, the
  workshop's Configure sheet) ← run override, recorded on `run_created`.
  `Resolve` re-parses the pinned bytes with the values, so parse-time and
  resolve-time validation are one code path and a bad binding is refused
  where it is bound. `$lower` is a parameter, `$UPPER` a sentinel; a reference
  is allowed at typed sites only (stage model/harness/max_turns/timeout/
  run_ref, action inputs, `on.start/quiet_for/max`, `when`).
- **`when:`** (`condition.go`): `tracker`, `subject`, a bool parameter, `not
  x`; false completes the stage as skipped.
- **Canonical statuses** — *superseded by TODO 49 (below): they are flow
  steps (`trackerport.FlowStep`), `TransitionTo` and Jira's `status_map` are
  gone, and a step resolves through the integration's status list.*
  `ticket_transition` takes `to` (todo / in_progress / in_review / done /
  blocked) or `status` (the tracker's word), exactly one. A step no status
  holds skips the stage rather than failing, so one flow fits every tracker.
- **Preflight** runs on the resolved flow (a bound harness or model lands on
  every agent stage and is held to the project's set and model list) and
  checks every firing phase's signal; `PreflightProject` gained a
  `parameters` step.

Decisions worth remembering: gates park like waits, so a phase started by
hand may edit the workspace while a gate is open — the person chose that;
`ask_human` and the debug leaf still hold the workspace. `create_mr` is
Single per phase and adopts an existing MR via `FindMR`, so re-running the
delivery phase by hand is safe. Subject actions without a subject fail
routably (`input_missing`) rather than at parse. A stale project binding is
dropped with a warning at run creation; a stale run override refuses the run.

**Automations** (slice 3, `internal/automation`, `confstore/automations.go`,
`0004_automations.sql`) are the ways a run begins without a person, and they
live outside the engine: a rule that creates a run at a `run_start` phase
with its params, `by: automation`, the run none the wiser. `tracker_query`
polls a ticketsource integration (the ticket factory's import path), imports
each issue as a ticket stamped with the automation id, and starts one run
per import; a duplicate is adopted only when it is this automation's own
import still without a run (a crash between the two), a person's import is
left alone. `cron` (five fields, UTC, `internal/schedule`) fires once per
slot after the last firing — no catch-up, and the firing is marked on the
row BEFORE the run is created, so a crash cannot fire it twice; an edit
counts from the edit. A refused run (preflight) lands in `last_error` and
one blocker notification per streak; the ticket stays for a person. The
rule is validated at save the way a firing would be (the integration's
port, the schedule, the flow, an entrypoint of it, params via `ParseBound`).
The daemon runs a pass every two minutes.
