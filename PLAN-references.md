# Plan: one naming standard for attempts and what they hold

Status: agreed in discussion, not implemented. Not meant to be committed as
is. Once it lands, the standard lives in `docs/flow-language.md` and the
reasoning in a note in `docs/notes/`.

Builds on the run journal (`PLAN-impasse-and-run-journal.md`, Part 2), on
this branch.

## Why

The run journal names three kinds of entry, and the names don't hold up:

1. **One shared namespace.** An attempt is `review-5`, a decision
   `decision-3`, a note `note-12`: all `word-number`. A stage named
   `decision` or `note` produces attempt ids identical to decision or note
   ids, and the decision wins the lookup.
2. **Note ids shift.** `note-12` is "the twelfth note readable right now",
   counted across the whole run. If retention drops an earlier notes file,
   or a crash captures one twice, later numbers move, and an id an agent saw
   in its prompt can point to another note when it asks for it.
3. **No single vocabulary.** The UI says "attempt", the journal "pass", the
   events "iteration". An agent sees `decision-3`, while the API and the UI
   know that decision by a ping UUID. A person can't tell an agent "look at
   review attempt 5" in words both of them share.

## Decisions taken

| # | Decision |
|---|---|
| R1 | Two characters, one meaning each: **`#` is "number n of"**, **`/` is "inside"**. |
| R2 | One word, **attempt**, everywhere: "pass" goes from the journal and the docs; "iteration" goes from the events, the Go code, the database, the API, analytics and telemetry. Run-level wording that says "attempt" for a Try again becomes "try". |
| R3 | The forms: `<stage>#<n>`, `<stage>#<n>/decision#<k>`, `<stage>#<n>/note#<k>`, `run`, `run/decision#<k>`. Nothing else. |
| R4 | A decision's `k` is its place among **every ping raised in that attempt**, in raise order, whatever its type. Assigned when the ping is raised and recorded on `ping_raised`. The ledger shows only decisions that say something; the records file shows all of them. |
| R5 | A note's `k` is its place in **its attempt's** notes file. An attempt keeps one capture per output (the last), so a re-capture after a crash cannot add notes. |
| R6 | **`#` is reserved.** No name the flow defines or the engine stores may contain it: stage, agent, sequence, service, port and parameter names, output paths, stored file names. Files of the repository read with `repo:` are exempt: they never become a reference. |
| R7 | Two files with the same name in one folder: the second is `name-2.ext` (never `#`). |
| R8 | Journal files mirror references: `.apothecary/journal/<reference>.md`. |
| R9 | Attachments are delivered under `.apothecary/attachments/<reference>/`; the ticket's under `.apothecary/attachments/ticket/`. |
| R10 | A log snapshot is named `<stage>#<n>.log`. |
| R11 | No reference for a single output. An attempt's non-text outputs (screenshots) are written as real files beside its journal file. |
| R12 | The UI shows the reference wherever it names an attempt or a decision, as a monospace chip that copies on click. |
| R13 | The readable name is a **reference** (`ref` in JSON and in the tool's parameter), kept apart from the opaque **ids** (run, ping and snapshot UUIDs) that APIs and the database key on. |
| R14 | No compatibility: the deployed events database is dropped. The column rename is still a new migration, since `0001_init.sql` is frozen. |

---

## The standard

```
reference := attempt [ "/" entry ]
           | "run" [ "/" decision ]
attempt   := stage-id "#" n
entry     := decision | note
decision  := "decision#" k
note      := "note#" k
n, k      := 1, 2, 3, …   (decimal, no leading zero)
stage-id  := a stage's id as the flow has it: a slug, or a sequence path
             of slugs joined by dots (settle.resolve)
```

| Reference | Is |
|---|---|
| `review#5` | review's fifth attempt |
| `stage-2#3` | the third attempt of `stage-2` |
| `settle.resolve#2` | the second attempt of the `resolve` stage of the `settle` sequence |
| `build#3/decision#1` | the first ping raised in or about build's third attempt: a question its agent asked, or the debug, impasse, gate or manual decision on it |
| `build#3/note#2` | the second note in that attempt's notes file |
| `run` | the run as a whole (`journal_read()` with no reference) |
| `run/decision#1` | the first ping raised outside any attempt (a phase confirmation, a selection) |

- **The attempt a ping belongs to** is its stage's latest attempt when it is
  raised. A debug or impasse ping comes after the attempt ended, and is
  about that attempt. A ping with no stage, or whose stage has not started
  an attempt, is `run/decision#k`.
- `run` and `ticket` carry no `#`, so they can never be an attempt, and no
  stage id needs reserving.
- One formatter and one parser in Go (a small package, e.g.
  `internal/run/ref`, beside `event`), with the UI's formatter tested
  against the same examples.

## Reserving `#` (R6)

Most names are slugs already and can't hold `#`. The reservation makes that
explicit and closes the places that can hold it today:

| Name | Today | Change |
|---|---|---|
| stage, agent, sequence, service, schema ids; parameter names | slugs | spec says `#` is reserved; the parser's error names the reason when a name contains it |
| port names | `[a-z0-9][a-z0-9._-]*` | as above |
| output `to:` paths (and so the `artifacts/` paths read from them) | any repo-relative path | refused with `#`: "`#` is reserved for references (`review#5`)" |
| `repo:` input paths | any file of the repository | **unchanged**: a real file may be `c#.md` |
| uploaded file names (attachments) | `SafeName` | `#` becomes `-` |
| files an agent writes into a `files` output, captured as snapshots | `SafeName` | `#` becomes `-` in the stored name |
| the engine's own names (`review#5.log`) | — | built by the reference formatter, checked by its grammar, never put through `SafeName` |

The flow editor (`cpui/src/lib/domain/flowdoc.ts`) applies the same rule to
output paths, so a flow is refused in the editor as by the parser.

---

## Part 1 — `iteration` becomes `attempt` (R2, R14)

Mechanical, done first so later parts write the new name once.

- **Events**: the `iteration` key becomes `attempt` on `stage_started`,
  `stage_paused`, `stage_resumed` and `snapshot_captured`. Go fields
  `Iteration` become `Attempt`.
- **State**: `runcore.State.Iters` becomes `Attempts`; locals follow.
- **Files and database**: `filestore.Snapshot.Iteration` becomes `Attempt`.
  New migration `0005_attempt.sql` renames the column in `snapshots` and
  `stage_attempts`. The index `snapshots_run_idx` keeps its name and works
  on the renamed column. `pgstore`'s column lists follow.
- **Run facts and API**: `run.Facts` and the swarm view's `iteration` become
  `attempt`; `task api` regenerates the spec and the client.
- **Analytics**: the `iteration` dimension becomes `attempt` ("Attempt").
  Saved queries naming the old dimension stop working, which is accepted.
- **Telemetry**: parameter names follow; trace and span ids are derived from
  the number as before, so they don't change.
- **UI**: `derive.ts`, fixtures, the Dossier, the heatmap.
- **Wording**: "attempt" means a stage attempt only. The Try again strings
  that say "attempt" for a new try of the run (`run.rerun.direction`,
  `run.rerun.directionPh`, the matching placeholder in `routes.ts`) say
  "try".
- **Spec**: `stage_started.iteration` and the other mentions in
  `docs/flow-language.md`.

## Part 2 — references on pings (R4, R13)

- `ping_raised` gains `ref` (`build#3/decision#1`), computed by the engine
  as it raises the ping, from the state it already holds: the stage's
  latest attempt and the pings already raised in it. A ping raised again
  under its idempotency key after a crash is the same ping, with the same
  reference.
- `run.PingView` and the API's ping carry `ref`; the inbox and the ping
  card read it from there.

## Part 3 — the journal (R3–R5, R8)

- **Ids become references**: the attempt replaces the "pass" (`jPass`
  becomes `jAttempt`, `passID` the formatter); decisions take their `ref`
  from `ping_raised`; notes are numbered within their attempt.
- **One capture per output per attempt**, keyed by port name and path, the
  last kept. Duplicates from a crash collapse, and their notes with them.
- **Wording**: "pass" becomes "attempt" in the ledger, the files, the tool
  and the docs. History reads `review: 5 attempts (review#1 to review#5);
  the latest, review#5: …`. Every line ends with `[<reference>]`.
- **Routes**: `GET /api/runs/{run}/journal` (everything) and
  `GET /api/runs/{run}/journal/{ref...}`. A malformed reference is a 400
  that names the grammar; an unknown one is a 404.
- **Tool**: `journal_read(ref: "review#5")`, its description and schema
  giving the forms. The shim encodes `#` as `%23` in the URL.
- **Files** mirror references under `.apothecary/journal/`:
  - `run.md`: every record;
  - `review#5.md`: everything an attempt left;
  - `build#3/decision#1.md` or `build#3/note#2.md`: a decision or a note
    past 8 KiB (shorter ones come back inline).

  The shim accepts a relative path of at most two segments, each made of
  `[A-Za-z0-9._#-]` and not starting with a dot, and refuses anything else.
  The tool's result gives the path quoted, which keeps it safe in any shell.
- The folder is still emptied at each fresh agent attempt.

## Part 4 — non-text outputs beside the attempt file (R11)

- When the tool writes `review#5.md`, it also writes each non-text output of
  that attempt as the real file in `.apothecary/journal/review#5/`. The file
  is named after the output's stored file name: `#` is already gone from
  it, and a second file with the same name becomes `-2` (R7). The attempt
  file lists each output with the path it was written to, instead of "listed,
  not embedded".
- The attempt entry lists those files by name, size and type. The shim then
  fetches each as raw bytes from `GET /api/runs/{run}/journal/{ref...}?file=<name>`
  (`application/octet-stream`), so nothing binary is base64'd into JSON.
- Limits are the existing capture limits: 20 files per `files` output, none
  over 10 MiB (`attach.go`). A file whose snapshot was dropped by retention
  is listed as "no longer kept" and not written.
- An agent whose harness shows images opens them with its usual file reader.

## Part 5 — attachments (R7, R9)

- An answer's attachments are delivered to
  `.apothecary/attachments/<ping ref>/<name>`, e.g.
  `.apothecary/attachments/build#3/decision#1/screenshot.png`.
  Attachments on review comments in a gate's answer are the same ping's.
- The ticket's are delivered to `.apothecary/attachments/ticket/<name>`
  (today's `goal/`).
- **Names**: the stored name (`SafeName`, `#` already gone); a second file
  with the same name in one folder becomes `name-2.ext`, in attachment
  creation order. The order is fixed, so re-delivery after a restart gives
  the same paths. The random eight-character folder goes away.
- `attachmentPath` takes the ping's reference. The journal names delivered
  files through the same function, so the two always agree.

## Part 6 — logs (R10)

- `publishLog` names the snapshot `<stage>#<n>.log`; the UI's download
  follows it.

## Part 7 — UI (R12)

- A `RefChip` component: monospace, copies on click, with a test beside it,
  role tokens only (`docs/frontend-standards.md`).
- Shown, beside the existing words, in:
  - **timeline rows**: `review#5`;
  - **the transcript's attempt header**: `review#5`;
  - **the record view**: `review#5`, before the output's name;
  - **the inbox and the ping card**: the ping's `ref`, e.g. `build#3/decision#1`;
  - **the run table, the flow view's tooltip, the Dossier**: `stage#n` where
    each shows an attempt today.
- The UI formats an attempt's reference from the stage and its number (one
  helper, tested against the Go examples). A ping's reference comes from the
  API; the UI never builds one.

## Part 8 — spec and notes

- `docs/flow-language.md`: a **References** section with the grammar, the
  table above, where references appear (journal, attachments, logs, UI), and
  `#` reserved. The id rules, the `run.journal` row (§4), the `journal_read`
  table (§9) and the event field names are updated, and "pass" is gone.
- A new note, `docs/notes/<date>-one-naming-standard-for-attempts.md`: why,
  what now, the tests. The branch's own journal notes are updated to the new
  forms.
- `README.md` if it names any of these.

---

## Tests

- **Grammar**: the formatter and parser round-trip every form; stage ids
  with dashes, digits and dots (`stage-2#3`, `settle.resolve#2`); refused:
  `review`, `review#0`, `review#05`, `review#5/note`, `decision#1` alone,
  `run#1/…` read as a stage named `run`, extra segments. The UI helper
  agrees on the same examples.
- **Reservation**: the parser refuses `#` in an output path, with the reason,
  and in every kind of name; a `repo:` path with `#` is accepted; the editor
  matches; `SafeName` turns `#` into `-`.
- **Ping references**: the order within an attempt across ping types; a
  debug and an impasse ping belong to the attempt they're about; a ping
  outside any attempt is `run/decision#k`; the same reference after a
  restart and after a crash re-raise; the API's ping carries it.
- **Journal**:
  - no clash for stages named `decision`, `note` or `run`;
  - note numbers survive a dropped and a re-captured notes file in another
    attempt;
  - a crash re-capture adds no notes;
  - every reference the ledger prints resolves;
  - a malformed reference is a 400 and an unknown one a 404;
  - files land at their mirrored paths; the shim refuses a path that would
    leave the folder.
- **Images**: an attempt with a PNG output writes the PNG beside the attempt
  file, byte for byte; a dropped snapshot is listed and not written; two
  outputs with the same file name get `-2`.
- **Attachments**: paths per ping reference and for the ticket; same-name
  files get `-2`; re-delivery after a restart gives the same paths; the
  journal names the same paths.
- **Rename**: events round-trip with `attempt`; the migration applies on a
  fresh database (Postgres tests); analytics answers on the `attempt`
  dimension.
- **UI**: `RefChip` (copy, label), and each view shows the reference.
- The usual mutation pass: break each rule, confirm a test fails.

## Order of work

1. Part 1 (the rename), with the migration and `task api`.
2. The reference package and the reservation.
3. Part 2 (pings carry their reference).
4. Part 3 (the journal), then Part 4 (images).
5. Part 5 (attachments) and Part 6 (logs).
6. Part 7 (UI).
7. Part 8 (spec and notes), kept up to date as each part lands.

Each step: `task fmt`, `task check` in the devcontainer (Postgres tests
included, for the migration), `task api` when routes or API types change.

## Small points left for implementation

| # | Point |
|---|---|
| P1 | The reference package's name and home (`internal/run/ref` proposed); archtest decides who may import it. |
| P2 | Exactly where the chip sits in each view, within `docs/frontend-standards.md`. |
| P3 | Whether the shim fetches images one request at a time or a few in parallel. |
