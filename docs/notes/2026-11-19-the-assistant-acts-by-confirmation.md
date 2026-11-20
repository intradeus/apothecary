# The assistant acts by confirmation

Phase 5 of `docs/assistant-plan.md`: the assistant asks the person to
confirm acts, reads runs and analytics, and sits on more pages. What the
plan left open, and how it was settled; what it left undone.

## Why

The assistant's token reads and fills, and that stays. A person who asks
it to "start a run on this" should not have to leave the conversation to
do it, and the agent must not do it on its own. The plan's open question,
whether every act needs a confirmation or a standing permission per kind
of act would do, is settled as **every act** (the plan's recommendation).

## What now

- **`request`, a second bound tool** beside `fill`. Like `fill`, it is the
  session token's alone, listed for no other token (`boundRoute`), and
  never in a client's grant. Its input is `why` and up to 10 `acts`, each
  a write tool of the face and that tool's arguments. The engine holds
  each act to the catalog before the session sees it: the tool must exist,
  be served, be a write, and not be the assistant's own. The arguments are
  validated against the tool's input schema, and an error names the
  schema so the agent corrects it. The never-a-tool list of phase 4
  therefore bounds what may be requested.
- **Proposed in a turn, kept with its answer.** A request is taken only
  while a question is being answered (the token lives for the turn
  anyway). One holding the turn's secrets is refused, as a fill is. A
  second request in one answer replaces the first. The request rides the
  answer's message (`assistant_messages.request`, migration
  `0016_assistant_requests`), state pending.
- **Confirmed as the person.** `POST …/messages/{seq}/confirm` runs each
  act through its route, in order, with the confirming request's own
  credentials: a cookie session with what proved it same-origin, or the
  caller's token. So the person's actions decide, and a token that may
  talk to its assistant but not file tickets gets the refusal as the
  act's outcome. The first failure stops the rest, which are kept as not
  run. Each act's status and result (cut at 2 KiB) are stored. `…/decline`
  runs nothing.
- **A request is decided once, whatever fails** (found in review). A
  confirm first moves the request from pending to running with a write
  conditional on its state (`SetAssistantRequest`'s `from`), then runs the
  acts, then moves it from running to done. A second click, another tab,
  or a store that fails after the acts ran never runs them again: a
  decided or running request answers 409 (`ErrStale`). One left running
  (the engine stopped mid-way) shows as interrupted, is never confirmable,
  and the agent is told its outcome is unknown. A tool no longer served
  answers 503 as its act's outcome, without running.
- **The card names what an act's ids are**: the workshop's name, the
  run's title, from what the browser already knows, so an act aimed at an
  unexpected run (a prompt injection in a ticket's text, read across the
  person's workshops by a palette session) is visible before the click.
- **The outcome reaches the agent once**: the next prompt lists every
  decided request not yet reported, then marks them reported. A request
  still pending is listed as waiting each time, until decided.
- **The stream** carries a `request` frame: one proposed mid-answer (no
  message yet: the panel shows it with no buttons), and one decided (its
  message, replaced, in every tab).
- **More pages:**
  - **A run**: a view of the run's desk, beside Ask. It reads the run
    through the new `read_run_journal` tool. Its fill is a flow's name and
    whole YAML; it is not applied on the run page but held there with
    *Open in the flow editor*, which hands it to that flow's editor through
    the tab's session storage. The editor takes it once its load has
    finished (so the stored flow's own check does not land over it) and
    applies it against the flow as it is now, with Undo; a flow that fails
    to load drops it. The run stays pinned to the version it started on.
  - **A dashboard** (one's own; a built-in is duplicated to change it):
    its fill replaces the panels in the edit mode, with Undo, kept by
    Save. Panels are checked by the analytics service's own rule
    (`analytics.Service.CheckPanels`), and the browser maps the engine's
    shape to its own in the API layer (`api.assistant.panelsOf`).
  - **None**: the command palette's *Ask the assistant* opens a session
    (`general`, subject `palette`) that fills nothing and acts only
    through requests. On a workshop's pages it reads that workshop's
    repositories.
- **Read tools**: `read_run_journal` (a person's read of the run journal,
  until now the run token's alone), and `query_analytics`,
  `analytics_values` and `drill_analytics`.

Not done, though the plan lists them: pages for a skillset, an automation
and the New run dialog. Each is a page definition and a form; the
palette's session and requests already cover their acts.

## Tests

- `internal/assistant`: `TestARequestIsConfirmedAndReported` (proposed
  only in a turn and with the session's token, refused with a secret,
  replaced, kept with the answer, run in order until a failure, decided
  once, reported once), `TestARequestDeclinedOrWaiting` (declined runs
  nothing, pending is said to wait, a second click is busy),
  `TestNewPagesFill`; the store contract's request case, on memory and
  Postgres.
- `internal/httpapi`: `TestAssistantRequestTool` (bound tools per page,
  each refused act), `TestAssistantRequestConfirmed` (from a ticket's page
  one confirmation starts the run; a narrower caller's refusal is the
  outcome; decline), `TestRunHistoryForAPerson`.
- CPUI: `AssistantPanel.test.ts` (the request card),
  `fillGuard.svelte.test.ts` (`NoFill`, `HandoffGuard`), `handoff.test.ts`,
  `run/views/AssistantView.test.ts`, `FlowEditorPage.test.ts` (the
  handoff taken once), `analytics/Dashboard.test.ts`,
  `PaletteAssistant.test.ts`, `CommandPalette.test.ts`.
