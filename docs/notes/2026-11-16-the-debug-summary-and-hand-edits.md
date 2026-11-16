# The debug summary and hand edits

Why: at the debug leaf a person takes the workspace, changes what they
judge needed, and continues. The journal recorded only "continued": the
agent that ran next found files changed that no plan, note or decision
explained, and could undo the fix or flag it. The plan (Part 2, D13, D16)
has the continue carry what the person did, and the engine record which
files they touched.

What now:

- **A summary is required to continue** (D13). The debug ping stays a
  `yes_no` (the plan's I3: a form would change the ping's kind, and with it
  the answer routes and the dormant-run path), and the engine validates the
  answer instead (`validateAnswer`, given the ping's type): an approve with
  blank feedback is refused with "continuing needs a summary of what you
  did — even "nothing"". Giving up needs none. Every caller of `AnswerPing`
  (live or dormant run, the route) goes through the check.
- **Hand edits** (D16), best effort. As the run parks, the debug ping
  records each checkout's working copy as a git tree (`ping_raised.trees`):
  a copy of the index stages everything (`git add -A`) and `git write-tree`
  names it, so the person's index, stash and files are never touched. At
  the continue the engine takes the trees again and records the paths that
  differ per checkout (`git diff-tree --name-only`) in a new `hand_edits`
  event, before the stage re-enters. Uncommitted and new files count;
  git-ignored and git-excluded ones (`artifacts/`, `.apothecary/`, which
  bootstrap excludes) do not; names only, never the diff. A checkout whose
  list cannot be had says why: its tree was not recorded as the run parked,
  could not be read at the continue, or the parked tree is gone (the
  workspace was recreated while parked). The summary stands either way.
  Each git call runs under 30 seconds (`handEditsTimeout`), not the
  stage's timeout: the park's runs before the debug ping is raised, in a
  workspace that may be what broke, and a hung git must not keep the
  person from learning the run needs them. One that runs out reads as not
  determined.
- **In the journal**, the debug decision shows the end of the failure's
  output under its question, and reads "continued: the failing stage ran
  again", then "What they did:" and the summary, then the files they
  changed — one line for a single checkout, a line per checkout otherwise,
  the first 50 and a count of the rest, "none", or why it could not be
  determined.
- **The web UI's ping card**: a debug ping asks for the summary in a box
  beside Continue, which stays disabled while it is blank; Ctrl/Meta+Enter
  continues. A manual stage's card asks for nothing new. The terminal client
  answers no pings, so it has nothing to change.

The three recorded goldens with a debug leaf (`TestDebugLeaf`,
`TestDebugLeafSurvivesRestart`, `TestUnresolvedConflictReachesAHuman`) were
re-recorded: the only change is the `hand_edits` line after each continue.

Tests: `internal/engine/handedits_test.go` —
`TestHandEditsRecordWhatThePersonChanged` (the parked tree on the ping, the
paths recorded before the re-run, the decision's words, every git call
under `handEditsTimeout`),
`TestHandEditsBestEffort` (nothing changed, no tree at the park or at the
continue, a parked tree gone — each said so), `TestHandEditsWords` (several
checkouts, a long list cut, an older log with none),
`TestWorkTreesPerCheckout` (each checkout in its own directory, none
without a workspace), `TestHandEditsTrustsOnlyATreeID` (a parked value
that is not a tree id never reaches the diff's command line — it comes back
from the event log — and equal trees run no diff);
`internal/engine/answerkind_test.go` —
`TestDebugContinueNeedsASummary`; `journal_test.go` (the decision words);
`internal/run/event/event_test.go` (`trees` and `hand_edits` round trip);
`cpui/src/lib/pings/PingCard.test.ts` (the summary box, Continue disabled
until written, Ctrl+Enter, none for a manual stage). Breaking it five ways — a
continue taken without a summary, the hand edits never appended, equal
trees diffed, any parked value trusted as a tree, a line that is not a tree
taken for one — fails at least one of these each.
