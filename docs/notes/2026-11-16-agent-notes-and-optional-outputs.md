# Agent notes and optional outputs

Why: the journal held what the owner decided and how each attempt went, but
not what an agent chose on its own — a plan step it could not follow as
written, a scope it narrowed, a CI check it could not run. The next agent
then found the change differing from the plan with nothing to say why, and
either flagged it again or let it pass unexamined. The plan (Part 2, D19)
has agents leave notes, through a declared output the engine captures; the
agent never writes the journal.

What now:

- **Optional outputs** (FLOWLANG §4). An output port takes `optional: true`:
  the stage may leave it unwritten (`output_contract` lets it go), and one
  written must still hold its shape (`output_invalid` otherwise) — the
  runtime already treated an optional port so (`missingPaths`,
  `checkFiles`), only the parser refused it. It is refused on a `verdict`
  (the router reads it) and on an action's output (an action writes what it
  writes). An optional output is no writer for "written before it is read"
  (`definitelyProduced`), so a reader of it must read it `optional`, and the
  error says which output is optional. The prompt's OUTPUT CONTRACT lists
  optional outputs apart, under what the agent *may* write.
- **`journal_notes`**, a built-in file type: YAML or JSON, a `notes` key
  over a list of `{did, why, files?}`, checked by its schema. No note at all
  is the key with an empty list, `notes: []` (`{"notes": []}` in JSON), or
  with nothing after it, `notes:` — both accepted, as failing a stage over
  that difference of style would cost a whole retry. Its contract line says
  what goes in — only a significant departure from its direction made on
  its own: a step not followed or done another way, scope dropped or added,
  a verdict point not applied — what does not (the choices any work makes
  within its direction, so most attempts have none; a person's answer,
  already in the journal; the work that was asked for), and that the next
  stages read each note as a claim to check. The bar is high on purpose: a
  journal filled with small choices hides the departures that matter.
- **Required in the built-in flows, written with no note when there is
  none.** An attempt with nothing to note still writes the file, `notes: []`:
  an agent that never wrote it may never have read what it was for. An
  empty file is not enough — `touch` makes one, and so does an agent that
  never read the contract — while the key says it read the format and
  found nothing. One not written fails the stage (`output_contract`), one
  empty or without its key fails it as malformed (`output_invalid`). `optional:` stays in the
  language for any output, notes included; the built-in flows do not use
  it for notes, and the flow editor no longer starts a notes row optional.
  The dev fake writes `notes: []`. Since a forgotten or malformed notes
  file fails the stage, every stage that writes one has a retry before
  the debug leaf: `resolve` (the `conflicts` watcher's and
  `conflict-resolver`'s), which went straight to `debug`, now has
  `on_fail: { to: resolve, max: 2, then: debug }`, like `fix` and
  `ci_repair`.
- **In the journal**, notes are read from each attempt's snapshot when the
  journal is built (`loadNotes`) and numbered within their attempt
  (`build#3/note#2`, `2026-11-16-one-naming-standard-for-attempts.md`). The
  ledger has an **Agent notes** part between the decisions and the attempts,
  worded as claims, not direction: this chain run's latest 30 notes one line
  each (oldest of them first, each cut to 300 bytes) — the latest are what
  the next agent works on — the rest and the earlier rounds' counted, with
  the call that writes them all (the plan's I4). The bound is the prompt's
  only: the tool does not page. `journal_read` gives a
  note whole, inline; the records file has every note; an attempt's record
  names its notes, or says why they could not be read (swept, a store down,
  not a list) — the other attempts' notes stand.
- **Built-in flows** (the plan's I2): the agents that edit code declare
  `notes` and every stage running them writes `artifacts/journal-notes.yaml`
  — `builder`'s `builder` (its `build` and the review watcher's
  `fix`), `ci_fixer` (`ci_repair`), `resolver` (`resolve`), and
  `conflict-resolver`'s `resolver`. `hands-on` has no agent that edits code.
  The builder's and the CI fixer's "never write to artifacts/" rule now
  excepts the notes file; the CI fixer's "say so in a short note" is now its
  notes.
- The flow editor offers an optional switch on an agent's or a command's
  output (never a verdict's, never an action's) and keeps it through an
  edit — before, editing an output dropped it; `journal_notes` is among the
  file types. The editor's JSON
  Schema accepts `optional` on an output.

Tests: `internal/flow/optional_output_test.go` (an optional output parses,
a required read of it is refused naming why, a command's may be optional;
the `journal_notes` schema: `notes: []`, `notes:` and notes hold it; an
empty file, `{}`, a bare list, an unknown key do not), `ports_test.go` (an optional verdict and an
action's optional output refused), `schema_test.go` (the editor schema
matches the grammar); `internal/engine/journal_test.go` —
`TestAgentNotesReachTheJournal` (the builder's contract, the note in the
next stage's ledger as a claim with its reference, read whole, in the
records and the attempt's record), `TestAgentNotesRequiredButMayBeEmpty` (none written
fails `output_contract`; `notes: []`, `notes:` and `{"notes": []}` are no
note in the ledger or the attempt's record; an empty file, a bare list or a
malformed note fails `output_invalid`),
`TestOptionalOutputsInTheContract` (an optional output listed apart, under
what the agent may write, and left out without failing), `TestNotesBlock`,
`TestLoadNotesUnreadable`, `TestNoteReferencesStand`; `internal/project/kinds/fake_test.go` (the dev
fake writes `notes: []`); `internal/flow/builtin/builtin_test.go` —
`TestBuiltinJournalNotesAreRequired` (which stages keep notes, none
optional); `cpui/src/lib/flow/PortRows.test.ts` (a notes row
starts required). Breaking it
six ways — an optional output counted as written, an optional verdict
accepted, every round's notes listed as this one's, an optional output
listed as required, notes never loaded, a note id not resolved — fails at
least one of these each.
