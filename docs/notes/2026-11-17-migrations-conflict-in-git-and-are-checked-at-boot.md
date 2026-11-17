# Migrations conflict in git and are checked at boot

Why: two branches each added a `0004_*.sql`. Git merged them without a
conflict, since the files have different names, and the duplicate number
only showed up as a failing boot after the rebase. The runner recorded
only the version number, so a database that had applied the branch's
`0004_live_tickets.sql` would, once the migration was renumbered to
`0006`, skip main's `0004_ping_choices.sql` and run `live_tickets` a
second time. Our runner is our own (`pgstore.migrate`, no library). Of the
tools we looked at, Atlas's `atlas.sum` makes parallel migrations a git
conflict (Rails' `schema.rb` version line and EF Core's model snapshot do
the same by other means). We copied that idea instead of adopting Atlas:
Atlas would bring its own revisions table, a migration of every existing
database, and no equivalent of our `-- rebuild: projections` header.

What now:

- **`internal/adapters/pgstore/migrations/migrations.sum`**: a header, a
  `total` line (the sha256 of the lines below it), then one line per
  migration, its file name and sha256, in version order. Adding, renaming,
  renumbering or editing a migration rewrites the total line, so two
  branches that each add one conflict there in git. Resolve the conflict
  by renumbering your migration after the other branch's, then run
  `task migrations:sum` (`cmd/migrationsum`, which calls
  `pgstore.MigrationsSum`). The file is not embedded and the daemon never
  reads it.
- **`schema_migrations` keeps `name` and `checksum`** for each applied
  migration. The runner owns that table, so it adds the columns itself
  (`ADD COLUMN IF NOT EXISTS`); a numbered migration cannot, because each
  migration's row is written with its name and checksum, and on a fresh
  database `0001` is recorded before any later migration could add them.
  At boot, before any pending migration runs, each applied version is
  compared with the file this binary ships under that number: another
  name (renumbered) or another checksum (edited after it ran) is refused,
  with what to do (reset a development database, else run the apothecaryd
  that migrated it).
- **A ledger from before this change** has no names: the first boot fills
  them from the shipped files and holds them from then on. Such a database
  cannot be checked for what happened before that boot. A development
  database that applied this branch's migration as `0004` before the
  rename is adopted with `0004_ping_choices.sql`'s name and must be reset
  by hand.
- `TestInitMigrationIsFrozen` stays: the sum file records every edit but
  can be regenerated; that test fails before an edited `0001` ships.

Tests: `internal/adapters/pgstore/migrate_test.go` —
`TestCommittedMigrationsSumIsCurrent` (the committed file matches the
directory), `TestMigrationsSumTotalChangesWithEveryMigration` (adding,
another addition, renumbering, editing a new or a shipped migration: each
gives its own total), `TestMigrateRefusesARenumberedMigration` (the
branch-renumbering case; the refused database runs nothing new),
`TestMigrateRefusesAnEditedMigration`,
`TestMigrateAdoptsALedgerWithoutChecksums` (old ledger shape filled at
boot, then held); `cmd/migrationsum/main_test.go`. Two branches from one
base, each adding a `0007_*.sql` and running `task migrations:sum`,
conflicted on `migrations.sum` when one was rebased onto the other.
