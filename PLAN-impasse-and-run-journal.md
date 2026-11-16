# Plan: the impasse leaf, skipping a stage, and the run journal

Status: draft for review. Nothing here is implemented. Not meant to be
committed as is — once agreed, each part lands with its own note in
`docs/notes/`.

Already done and separate (uncommitted, staged): continuing from the debug
leaf restores the phase's retry budgets
(`docs/notes/2026-11-15-continuing-from-the-debug-leaf-restores-retry-budgets.md`).

## Why

1. A reviewer's REJECT is a verdict, not a failure. When the rejection budget
   runs out, the built-in `builder` flow sends the run to the debug leaf,
   which offers the wrong choices: continue re-runs the reviewer, and nobody
   can overrule it.
2. Nothing lets a person skip a stage.
3. What people decide during a run reaches later agents badly: the
   `run.ping_answers` input is capped at 32 KiB with the oldest dropped
   first, a note like "skip point 2" does not carry what "point 2" was, and
   code a person edits by hand at the debug leaf is not recorded at all — a
   reviewer can ask the builder to undo it.
4. An agent that departs from its direction without asking anyone leaves no
   trace either: the reviewer reads it as a deviation from the plan.

## Decisions taken

| # | Decision |
|---|---|
| D1 | A new reserved edge target, `impasse`, written `then: impasse` (or `on_reject: impasse`). No `version:` gate — the product is not released. |
| D2 | `impasse` is accepted on any `on_reject`, a human gate's included. A flow that makes little sense is still valid. Not on `on_fail` / `on_timeout`: those have the debug leaf. |
| D3 | Three answers: **keep going**, **skip stage**, **give up**. |
| D4 | Keep going does not re-run the rejected stage. It takes the stage's `on_reject` chain again from its first link (in `builder`: back to `build`, carrying the verdict), with **that stage's `on_reject` budget only** reset. |
| D5 | The debug leaf keeps its wider reset: every retry chain of the phase. |
| D6 | Skip stage completes the stage as skipped and follows `on_success`. |
| D7 | Give up is a dead end, as at the debug leaf: the run is abandoned in the main chain or an outcome, a watcher's round ends. |
| D8 | The impasse does not hold the checkout for a person, like a gate. The answer carries everything. The run has one cursor, so nothing else runs meanwhile except an outcome, which ends the run. |
| D9 | Skippability is inferred: a stage whose `on_reject` chain can reach `impasse` may be skipped. No `skippable:` field yet. Skip exists only at the impasse for now; manual skipping comes later. |
| D10 | Built-in flows: only `builder`'s `review` changes — `on_reject: { to: build, max: 5, then: impasse }`. `hands-on` is left alone. |
| D11 | The run journal is built by the engine from the event log and its snapshots; the workspace holds no standing copy. It reaches an agent two ways: a **ledger** pushed in the prompt, and a read-only **tool** to pull the rest. |
| D12 | The impasse card takes a free-text note; the verdict it answers is attached to the record. Dismissing single reviewer comments is a later refinement. |
| D13 | Continuing from the debug leaf requires a summary of what the person did. Always, even when nothing changed in the workspace. |
| D14 | A new answer kind, `choice`: the ping lists the allowed choices by id (each may say `note_required`), the answer names one in the existing `answer` field. The engine sends no wording; the clients label the choices. |
| D15 | Replay needs no marker for "keep going": the `ping_answered` that carries the choice is the record, and the reset is applied when it is folded. |
| D16 | At the debug leaf the engine records **which files** the person changed, not the diff inside them. Best effort: when it cannot be determined, the entry says so and the summary stands alone. |
| D17 | (Replaced by D20: the journal is not materialised as files.) |
| D18 | The merge request description does not mention a skipped review. |
| D19 | An agent may add notes to the journal through an optional declared output: what it did differently from its direction that no person was asked about. Part of this plan. |
| D20 | The ledger holds what prevents a repeated mistake — owner decisions and discarded corrections in full, agent notes as claims — and each line carries the id to pass to the tool for more. |
| D21 | Everything a stage pass left is reachable through the tool: its result, verdict, outputs and log, written as one file per pass under `.apothecary/journal/` for the agent to search with its own tools. No paging, no cache. |
| D22 | Checking an agent's output against the ledger (a reviewer comment that reopens a discarded correction must cite it) is not in this plan. |

---

## Part 1 — the impasse leaf and skipping a stage

### 1.1 Flow language (`internal/flow`, `docs/flow-language.md`)

- `flow.ReservedImpasse = "impasse"` beside `ReservedDebug`: reserved as a
  stage id (`sequences.go` `idProblem`), listed in the schema's targets
  (`schema.go`), accepted as scalar sugar and as `then:`.
- Parse rules (`parse.go`, next to the debug leaf's):
  - only inside an `on_reject` chain; `on_fail: impasse`, `on_timeout:
    impasse` and `on_success: impasse` are errors that say why;
  - takes no `max` and no `then`, like `debug`;
  - `on_reject: impasse` alone is valid (every rejection asks a person).
- Dataflow: a stage that can reach `impasse` is a **conditional producer**
  — the rule `run_if` stages already follow (`bindings.go`): what it writes
  is produced only on some routes, so a later required read of it is a parse
  error whose message names the impasse.
- Spec: §3 gains the impasse leaf next to the debug leaf; the edge table and
  the reserved-id list are updated; the "What `max` counts" paragraph is
  corrected (today it says "Try again" is the only reset — a chain's start,
  the debug leaf's continue and the impasse's keep going reset too, each
  with its scope).

### 1.2 The `choice` answer kind (`internal/run/event`, `internal/engine`, `internal/httpapi`)

```jsonc
// ping_raised
{ "type": "impasse", "stage_id": "review", "answer_kind": "choice",
  "choices": [ { "id": "keep_going" }, { "id": "skip" }, { "id": "give_up" } ],
  "ends_phase": false,          // existing field: what give_up does here
  "context": "<the verdict>" }

// the answer — the endpoint and fields of today
{ "answer": "skip", "feedback": "the reviewer is wrong about the migration" }
```

- `event.AnswerChoice = "choice"`; `PingRaised.Choices []Choice{ID,
  NoteRequired}`.
- The answer is refused unless it names one of the ping's choices, and
  unless it has a note where the choice requires one.
- Yes/no and form pings are untouched; old logs replay unchanged.
- `task api` regenerates the OpenAPI document and the UI's client.

### 1.3 Engine (`internal/run/runcore`, `internal/engine`)

- A new interpreter phase `PhImpasse` and step `StepImpasse`, modelled on
  `PhDebug`: `edge_taken{to: impasse}` parks the cursor on the stage that
  was rejected; replay restores it from that event.
- A new ping type `impasse`. It carries facts only, as the debug ping does:
  the stage, how many rounds were rejected, and the verdict that exhausted
  the chain (the edge's feed, plus a reference to that attempt's verdict
  snapshot).
- Routing the answer — one `State` method per decision, called live and by
  replay (on the `ping_answered`, D15):
  - **keep going**: delete the counters of the stage's `on_reject` chain
    (non-impasse links only, so the timeline numbers each visit), then take
    the chain's first link as a normal rejection: `edge_taken{from: stage,
    to: build, count: 1}` with the verdict as feed.
  - **skip stage**: clear the stage's declared outputs (a stale REJECT
    verdict must never read as current), append
    `stage_completed{skipped: true, note, by}` and follow `on_success` with
    no feed. `by` is new on the event: who skipped it. A `run_if` skip
    leaves it empty.
  - **give up**: `deadEnd`, as the debug leaf's rejection.
- The impasse never provisions and no person holds the checkout (D8).
- Pass numbers are untouched: `Iters` only ever rises, so logs and the UI
  keep counting (`review-6.log` follows `review-5.log`).

### 1.4 Clients

- Web UI (`cpui/`): a card for `choice` pings; for the impasse, the
  verdict, a note field and the three buttons, the card saying what each
  does before the click (give up names whether it abandons the run or ends
  the round, as the debug card does). The flow editor draws the impasse
  like the debug leaf: one per stage that escalates to it, beside the stage.
- Terminal client (`cmd/apothecary`, `internal/client`): answer a `choice`
  ping.
- Until Part 2 lands, the note travels through `run.ping_answers`
  (`pinganswers.go` `answerSaysSomething`): an impasse answer counts when it
  has a note, and a **skip always counts**, note or not.

### 1.5 Built-in flow

`builder.yaml`, `review`: `on_reject: { to: build, max: 5, then: impasse }`.
`on_fail` keeps the debug leaf. The header's "failure philosophy" comment
gains the distinction: a failure goes to the debug leaf, a rejection to the
impasse.

### 1.6 Tests

- Parser: the accept/refuse table for `impasse` (every edge kind, with
  `max`, with `then`, as a stage id); the dataflow error for a required read
  of a skippable stage's output.
- Answers: a `choice` ping refuses an unknown choice and a missing required
  note.
- runcore: each decision live and replayed gives the same state; keep going
  resets that stage's `on_reject` chain and nothing else (another stage's
  counters, the stage's own `on_fail` chain, another phase); the impasse
  count is kept.
- Engine scenarios: five rejections park at the impasse; keep going sends
  the verdict to `build` and five more rounds are available; skip clears the
  verdict, records who, and the chain goes on; give up abandons (main chain)
  or ends the round (watcher); restart at each crash window.
- Every built-in flow: the exhaustive check `debug_continue_test.go` does
  for the debug leaf, for every stage that reaches `impasse`.
- The same mutation pass as for the debug fix: break each rule, confirm a
  test fails.

---

## Part 2 — the run journal

### 2.1 What it is

The goal: every agent can know what was already done in the run, so it does
not repeat a mistake or ask for a correction that was discarded.

The journal is a **view**, not a file. The facts are already in the engine:
the event log (every ping, answer, verdict, edge) and the snapshots it
captures as each attempt ends (declared outputs, the full log). The engine
renders the view when it is asked — as a prompt is built, or as the tool is
called. The workspace holds no standing copy: a file appears there only
when an agent asks for one (2.3) and is rewritten on the next call, so
nothing can go stale or need restoring after a workspace is recreated. It
replaces `run.ping_answers`.

**Rule that makes it readable: an entry carries what it answers.** No entry
is a bare note.

| Entry | Class | Carries |
|---|---|---|
| an `ask_human` answer | owner decision | the question, the answer, the stage and pass that asked |
| a gate's feedback | owner decision | the question, what the gate showed, the person's words and comments |
| an impasse decision | owner decision | the full verdict it answers, the choice, the note |
| a skip | owner decision | the stage, who, why (the note), what it would have produced |
| a debug continue | owner decision | what failed (the stage and its output), the required summary, the files the person changed |
| an agent's note | agent note | the stage and pass, what it did differently, why, which files |
| a stage pass | history | what ran, how it ended, its verdict, its outputs, its log |

### 2.2 The ledger — pushed in the prompt

A stage that binds the journal input (working name `run.journal`) gets a
`# RUN JOURNAL` section, built fresh for that prompt:

- **Owner decisions**, in full, oldest first, never dropped. They are
  direction, as `run.ping_answers` says today: where one differs from the
  plan or another input, it stands. An impasse's "keep going" with a note
  and an overruled verdict appear here with the verdict they answer — this
  is what tells a reviewer a correction was discarded. A long body (a
  verdict) is given by its head and its id.
- **Agent notes**, one line each, under their own heading, presented as
  **claims to check, not direction**: a builder cannot excuse itself to the
  reviewer by writing a note. The current chain run's notes are listed;
  older rounds' are counted, with the tool call that fetches them (the all-records file).
- **History**, as a short summary — how many passes each stage has had and
  how the latest ended — not an index of every pass.
- Every line ends with its entry id, and the section ends with how to get
  more: the tool's name and one example call.
- An instruction: before asking for a change or redoing work, check it
  against this section.

### 2.3 The tool — pulled on demand

**Can every agent use it, a read-only one included? Yes.** The mechanism
exists: `ask_human` is a tool served by the shim in every workspace
(`workspace/shim/askhuman.go`, an MCP server on stdio), registered with
each harness by its adapter, talking to the engine's API with the run's own
token. `read_only` denies a harness's edit and write tools on repository
files; it does not remove the shim's tools — the reviewer has `ask_human`
today. The journal tool is read-only by nature and travels the same path.

One tool on the shim's server, `journal_read`:

| Call | Returns |
|---|---|
| `journal_read(id)` — a stage pass | the **path of one file** holding all of it — its record, verdict, text outputs and full log — with its size and line count |
| `journal_read(id)` — a decision or a note | the entry inline (it is a few lines) |
| `journal_read()` — no id | the path of one file holding **every record of the run**: decisions, notes, each pass's result and verdict, no raw logs. This is how an agent searches the whole history, older rounds' notes included |

There is no list tool: the ledger in the prompt is the list, and a pass's
id is its stage and pass number, so "review: 5 passes" already names
`review-1` to `review-5`. Filtering is the agent's, with grep.

- **Files, not pages.** A tool's result lands in the agent's context, so
  large content is never returned: it is written under
  `.apothecary/journal/` (inside the checkout, already excluded from git)
  and the agent works on it with its own tools — grep, sed, the harness's
  file reader. No paging, no cursor, no search parameter: the engine
  streams the whole snapshot and the shim writes it.
- **No cache.** The file is the local copy; the same call writes it again.
  The engine stays the truth, so a file a stage edited is simply
  overwritten by the next call.
- **One file per pass**, named by its id (`review-5.md`): sections for the
  record, each text output, then the log. An output that is not text (a
  screenshot) is listed by name and size, not embedded.
- **Ids** come from the log, so they are stable across restarts: a pass is
  its stage and pass number (`review-5`, as its log file is named today); a
  decision is its ping.
- **Engine routes** behind it (run-token auth, read-only, this run only):
  `GET /api/runs/{run}/journal` and `GET /api/runs/{run}/journal/{id}`.
  They read the event log and the snapshot store; a log or output served
  this way is the snapshot, already scrubbed of secrets when captured.
  `task api` regenerates the spec.
- **Who is offered the tool**: the agents of a stage that binds
  `run.journal`. The shim already offers `ask_human` conditionally; the
  same switch carries this.
- **Limits said plainly**: a snapshot whose content was dropped by
  retention answers "no longer kept"; the pass being run now is not in the
  journal until it ends.
- Tool calls appear in the stage's transcript, so what an agent looked up
  is on the record.

### 2.4 Agent notes (D19)

- **What goes in**: a change from the stage's direction — the plan, the
  ticket, a verdict it was sent — that the agent made on its own. **Not**
  what a person already answered: an `ask_human` answer is in the journal as
  a decision, and repeating it would duplicate it. The agent's prompt says
  so.
- **How**: a typed, **optional** output, declared like any other —
  `notes: { to: artifacts/notes.yaml, type: journal_notes, optional: true }`
  — with a small schema: per note, what was done differently, why, the
  files. The engine captures it with the attempt's other outputs and
  renders it; the agent never touches the journal.
- **Flow language change**: outputs gain `optional: true`. Today a declared
  output that is not written fails the stage (`output_contract`). An
  optional one may be absent; when present it must hold its type
  (`output_invalid` otherwise). A reader of an optional output follows the
  conditional-producer rule (1.1): it must mark its input optional.
- **Each pass's notes stand**: outputs are cleared before a fresh attempt,
  so the file holds one pass's notes; the journal keeps every pass's, from
  the snapshots.
- **Built-in flows**: every agent that changes code may write it —
  `builder`'s `build`, and the watchers' `fix`, `resolve` and `ci_repair`;
  `hands-on` and `conflict-resolver` likewise where an agent edits code.
  The exact list is settled against the flows at implementation.

### 2.5 The debug leaf

- Continue requires a summary (D13): the debug ping's `continue` carries
  `note_required` (D14's mechanism; whether the debug ping moves to the
  `choice` kind or keeps yes/no with the same check is an implementation
  detail, old logs replaying either way). Both clients ask for it.
- The engine records which files the person changed (D16): a fingerprint of
  the working copy (a git tree hash that includes uncommitted and new
  files, taken without touching the person's files, index or stash) when
  the run parks, another when they continue, and the list of paths that
  differ. One per checkout in a multi-repository run.
- Best effort: a workspace that is itself what broke, or one that was
  destroyed and recreated while parked, gives no list — the entry says the
  changes could not be determined, and the summary stands.

### 2.6 Where the code goes

- `internal/engine`: the journal's rendering beside `pinganswers.go` — one
  pure function from events and snapshot metadata to entries, used by both
  the ledger and the routes, so the two can never disagree.
- `internal/httpapi`: the two routes, under the run token like the shim's
  ping routes.
- `workspace/shim`: the tool on the existing MCP server; each
  harness adapter (`internal/adapters/…`) lists it where it lists
  `ask_human`.
- `internal/flow`: the `run.journal` source and input type (and
  `run.ping_answers` removed — not released, so no alias); the
  `journal_notes` output type with its schema; `optional` on outputs.
- Built-in flows: move from `run.ping_answers` to `run.journal`; the
  agents' prompts gain the "check the journal first" and the notes rules.

### 2.7 Tests

- Rendering from a fixed event log: every entry kind carries its context;
  no owner decision is dropped at any size; long bodies are referenced, not
  cut silently; agent notes are presented as claims; every line has an id
  the tool accepts.
- Tool and routes: the all-records file when no id is given; a small entry
  inline; a pass written as one file with its record, outputs and log, a
  non-text output listed; a second call rewrites an edited file; another
  run's token is refused; a dropped snapshot says so. One test per harness adapter that the tool
  is registered, a read-only agent's included.
- Ledger and tool agree: every id the ledger prints resolves.
- Optional outputs: absent is fine, malformed fails, a required read of one
  is a parse error.
- Agent notes: every pass's notes appear under its stage and pass; a note
  never appears as an owner decision.
- Debug: a continue without a summary is refused; a hand edit lists its
  files; no edit says so; an undeterminable workspace says so.
- A long run: a log of thousands of events renders a ledger within its
  bound, and a multi-megabyte pass is written to its file whole.

---

## Small points left for implementation

| # | Point |
|---|---|
| I1 | Final names: the `run.journal` source, the `journal_notes` type, the notes file's path, the tool. |
| I4 | The size under which an entry is returned inline, and the bound on the ledger's notes list. |
| I5 | Confirm on each harness that a read-only session keeps the shim's tools (true for `ask_human` today). |
| I2 | Which built-in agents declare the notes output (2.3 gives the starting list). |
| I3 | Whether the debug ping moves to the `choice` kind or keeps yes/no (2.4). |

## Order of work

1. Part 1 (impasse, skip, the `choice` answer kind), its note going through
   `run.ping_answers`.
2. Part 2, in this order: the journal's rendering and the ledger (taking
   over from `run.ping_answers`); the routes and the tool; agent notes
   and optional outputs; the debug summary and changed files.

Each step: `task fmt`, `task check` in the devcontainer, a note in
`docs/notes/`, `task api` when the routes change.
