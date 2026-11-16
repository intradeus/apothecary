# One naming standard for attempts

Why: the run journal named its entries `review-5`, `decision-3` and
`note-12`, all `word-number`, and the names did not hold. A stage named
`decision` or `note` produced attempt ids identical to decision or note ids,
and the decision won the lookup. A note's number was its place among every
note readable at the time, across the run, so a notes file dropped by
retention, or captured twice after a crash, moved every later number: an id
an agent read in its prompt could name another note when it asked for it.
And the run had three words for one thing — the UI said "attempt", the
journal "pass", the events "iteration" — while an agent saw `decision-3` for
what the API and the UI knew only by a ping UUID. No person could tell an
agent "look at review attempt 5" in words both of them shared.

What now: one grammar, in `internal/run/ref` (the formatter and the parser;
anything may import it), and the spec's §1 *References*:
`<stage>#<n>`, `<stage>#<n>/decision#<k>`, `<stage>#<n>/note#<k>`, `run`,
`run/decision#<k>`. `#` is "number n of", `/` is "inside". The choices it
rests on:

- **`#`, not `-`, as the separator.** A stage id may end in a digit
  (`stage-2#3`), and `#` already reads as "number". `run` and `ticket` carry
  no `#`, so no stage id needs reserving: `run#1` is a stage named `run`.
- **`#` is reserved** everywhere a name could become one: the flow parser
  refuses it in stage, sequence and port names, output paths and the paths
  read from them (`flow.ReservedRef`, the error saying why); `SafeName`
  turns it to `-` in every stored file name a person or an agent gave. A
  `repo:` file keeps its name: it never becomes a reference. The engine's
  own names (`review#5.log`) are built by the formatter and never pass
  through `SafeName`.
- **A decision's number is its place among every ping raised in its
  attempt**, whatever the type, not only the ones the ledger shows: the
  number is assigned as the ping is raised, when what it will decide is not
  known yet. The engine records it on `ping_raised.ref`
  (`runcore.State.PingRefs`, called in the append funnel, so a batch is
  numbered in order and a replay gives the same); a ping raised again after
  a crash keeps it. A debug or impasse ping belongs to the attempt it is
  about — its stage's latest. The projection stores it (`pings.ref`,
  migration `0005_references.sql`) and the API's ping carries it; the UI
  never builds one.
- **A note's number is its place in its own attempt's notes**, and an
  attempt keeps one capture per output (port and path, the last), so a
  re-capture adds none and another attempt's loss moves none.
- **One word, attempt**: `iteration` is renamed in the events, the Go code,
  the database (the same migration), the API, analytics (the `attempt`
  dimension; saved queries naming the old one stop working) and telemetry;
  "pass" is gone from the journal. Where "attempt" meant a try of the run
  (the rerun direction), it now says run or try. No compatibility: the
  deployed events database is dropped, so old logs are not read.
- **Files mirror references**: the journal's at
  `.apothecary/journal/<reference>.md` (`run.md` for the whole run); an
  attempt's outputs that are not text written as themselves beside it
  (`review#5/shot.png`, fetched as bytes, never base64 in JSON) rather than
  each output getting a reference of its own; an answer's attachments under
  `.apothecary/attachments/<ping's reference>/`, the task's under `ticket/`
  (the random eight-character folder is gone); a log is `<stage>#<n>.log`.
  Two files with one name in a folder: the second is `name-2.ext`, in a
  fixed order (creation, then id; output order), so a re-delivery gives the
  same paths (`filestore.UniqueName`).
- **A reference names; an id keys.** JSON and the tool's parameter say
  `ref`; run, ping and snapshot UUIDs stay opaque, and nothing parses a
  reference for one. A malformed reference is a 400 naming the grammar, an
  unknown one a 404.

The run page shows the reference as a chip that copies it, wherever it names
an attempt or a ping.

Tests: `internal/run/ref/ref_test.go` (every form round-trips; what the
grammar does not produce is refused — the UI's `cpui/src/lib/domain/ref.test.ts`
holds the same list); `internal/flow/reserved_ref_test.go`;
`internal/platform/filestore` `TestHashIsReservedInStoredNames`,
`TestUniqueName`; `internal/run/runcore/pingref_test.go` (order across
types, debug pings, the run's own, batches, a re-raise, a replay);
`internal/engine/handedits_test.go` (a debug ping's recorded reference);
`internal/adapters/pgstore` `TestPingRefIsProjected`;
`internal/engine/journal_test.go` (`TestJournalNamesNeverClash`,
`TestNoteReferencesStand`, `TestEveryLedgerReferenceResolves`,
`TestNonTextOutputsBesideTheAttempt`, `TestJournalRead`);
`internal/engine/deliver_test.go` `TestAttachmentPaths`;
`internal/engine/artifacts_test.go` (log names); the journal's route and
shim tests (escaped references, files beside, paths that would leave the
folder).
