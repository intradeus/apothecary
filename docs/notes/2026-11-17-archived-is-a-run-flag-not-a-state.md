# Archived is a run flag, not a state

Why: `2026-07-19-events-projections-2-6.md` added `run_archived` because
DESIGN §2 listed `archived` as a run state and §6 wants every projected
state produced by an event. That made archiving overwrite how the run
ended: `run_archived` set `State` to `archived` over `completed`,
`abandoned` or `failed`. The runs list lost whether an archived run had
succeeded, and `run_facts.success` (`State == Completed`) turned false for
every successful run someone archived, so success and first-pass rates fell
each time a run was cleaned up, and the `state` dimension ended up reading
`archived` for every finished run. Archiving is cleanup — the workspace
goes, the run moves out of the way — and says nothing about the outcome;
the reaper already destroyed a completed run's workspace and left it
`completed`. This note reverses the "state" half of that older one: the
event stays, and now produces a flag.

What now:

- **`run.View.ArchivedAt`** (`archived_at` in the API) is set by the first
  `run_archived` and never moved by a later one (archiving again is the
  destroy's retry). `State` keeps the outcome; `Archived` is gone from
  `run.State` and from `Terminal()`, which is outcome-only.
- **Guards read the flag**: `Engine.Archive` (an archived run with an env
  is still the destroy's retry, without one "already archived"),
  `checkEnvCapacity` (archived runs are "pending cleanup", never
  "archivable"), the workspace, SSH, services and Ask lookups, and the war
  room, which skips archived runs outright — `run_archived` moves
  `UpdatedAt` into the afterglow.
- **The reaper checks the flag first**: an archived run with an env is
  reclaimed at once whatever its outcome. A switch on the state alone would
  have stopped retrying the destroy of an archived abandoned or failed run.
- **`RetryRun` refuses an archived run.** It used to be refused by its
  state; an archived failed run would now pass, and a failed provisioning
  would get a fresh workspace. The UI offers no retry on one.
- **Postgres**: `0007_run_archived_flag.sql` adds `runs.archived_at`
  (project deletion is blocked by `archived_at IS NULL` instead of
  `state <> 'archived'`) and `run_facts.archived`, the analytics `archived`
  dimension on the runs and skillsets grains. It carries the
  `-- rebuild: projections` header: the first boot that applies it replays
  every run, which gives each archived run its outcome back (`run_archived`
  always follows the event that ended the run) and fills both columns. No
  data fix. A saved filter on `state = archived` matches nothing after it;
  the `archived` dimension replaces it.
- **The rebuild request is durable.** Until now `migrate` committed a
  migration and its ledger row, and `Open` rebuilt afterwards: a boot whose
  rebuild failed or was cut short left the migration applied and the next
  boot served unrebuilt rows — for 0007, `state = 'archived'` with no flag.
  `schema_migrations.rebuild_pending` (a column the ledger code adds, like
  `name` and `checksum`) is set in the transaction that applies a
  migration with the header, and cleared in the transaction of the rebuild
  that replays it, so every boot rebuilds before serving while one is
  pending.
- **CPUI**: the status chip shows the outcome plus an "Archived" marker
  (DESIGN §3.1); the runs lists filter Finished on outcome and on archived
  apart, both "any" by default so the list reads as before.

`2026-11-15-environment-teardown-finishes-or-is-retried.md` says "archived
run" throughout: it now means a run with the flag, whatever its state.

Tests: `run.TestArchiveKeepsTheOutcome`, `TestReduceFullLifecycle`;
engine `TestArchive`, `TestRetryRefusals`,
`TestReaperReclaimsArchivedLeftoversWhateverTheOutcome`,
`TestEnvironmentCapacityNamesPendingCleanup`; httpapi `TestGetSwarm`;
pgstore `TestRunArchivedFlagMigration`,
`TestRunFactsArchivedKeepsTheOutcome`, `TestConfCRUDRoundtrip` (project
deletion), `TestOpenFailsWhenThePostMigrationRebuildFails` (the boot after
a failed rebuild replays, the one after that does not); the analytics
contract (`archived` and `state` filter apart on the runs and skillsets
grains, success rate unchanged by an archive, drill rows carry the flag);
and the CPUI's `run/filter.test.ts` and `StatusChip.test.ts`.
