# The run journal replaces ping answers

Why: `run.ping_answers` told a later agent what the task's owner answered,
and nothing else. A reviewer could not tell that a correction it was about
to ask for had been overruled at the impasse, nor how many times the build
had already gone round; and the section was capped at 32 KiB, dropping the
oldest answers first — the ones a long run most needs not to repeat. The
plan (Part 2) replaces it with a run journal: a ledger pushed in the prompt
and a tool to pull the rest.

What now: the source is `run.journal`, its type `journal` (renamed, not
aliased: the old name was never released). It renders a `# RUN JOURNAL`
section in three parts, built from the event log as the prompt is built
(`internal/engine/journal.go`):

- **Owner decisions**, oldest first, each with its reference
  (`build#3/decision#1`, the naming standard of
  `2026-11-16-one-naming-standard-for-attempts.md`), the stage it was about,
  when, the question and the decision. What counts is what `run.ping_answers` counted, with one change:
  every impasse answer is a decision now, with the verdict it answered in
  place of a question, since a skip or a keep going overrules or upholds a
  verdict whether or not a note was written. A gate's reject reads "sent it
  back", a debug continue "continued", in words rather than `approve` /
  `reject`. Each carries what it answers (the plan's 2.1): a gate's, the
  files and link it showed; a skip at the impasse, the outputs the stage
  declares and did not write — what a later stage will not find; a debug
  continue, the end of the failure's output. Who decided is not named: the
  journal says "the owner" (a skip, "skipped by the owner" against a
  `run_if`'s plain "skipped"), as an account id tells an agent nothing.
  None is ever dropped: the 32 KiB cap is gone, and a body past
  1500 bytes shows its head, cut on a line, and names the
  `journal_read(ref: "build#3/decision#1")` call that gives all of it.
- **Attempts**: per stage, how many and how the latest went — its end
  (completed, skipped by the owner, failed and why, paused, running now),
  its verdict, the edge it took. A short summary rather than an index: the
  references of every attempt follow from the count and the range shown
  (`review#1 to review#5`), since attempt numbers never repeat.
- **Reading more**: the `journal_read` tool and its three calls. The tool
  itself is the next step on this branch; until it lands, the section names
  a tool the agent does not have.

The journal is one fold (`buildJournal`) over the log, with snapshot
metadata (outputs and logs per attempt) and the decisions' delivered
attachments, so the ledger and the tool's routes cannot disagree.

The `builder` flow's builder, reviewer and redactor read `run.journal` where
they read `run.ping_answers`; the reviewer's prompt says an overruled
correction is not to be asked for again. The merge request description
still says nothing of a skipped review (the plan's D18): the redactor reads
the journal as context, not as a list to report.

Tests: `internal/engine/journal_test.go` — the ledger an agent reads after
a gate's feedback, two forms, an unanswered question and a skip at the
impasse (`TestTheLedgerCarriesTheOwnersDecisions`); three hundred 20 KB
decisions, none dropped and each cut with its reference
(`TestTheLedgerDropsNoDecision`); which pings count (`TestJournalDecides`);
each attempt's end, verdict and edge read off the log, one capture per
output (`TestBuildJournalReadsEachAttempt`); the decision words; an
attachment named in its decision, at its delivered path; an unreadable log said so;
`TestDecisionsCarryWhatTheyAnswer` (a gate's files and link, a failure's
output, a skip's unwritten outputs); `TestALongRun` (3000 attempts and
their notes leave the ledger a line per stage and 30 notes, under 16 KiB,
and a 5 MiB log is written to its attempt's file whole). The
UI's agent editor previews the section under its new name.
