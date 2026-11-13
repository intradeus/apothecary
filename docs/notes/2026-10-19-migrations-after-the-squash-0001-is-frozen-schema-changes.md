# Migrations after the squash: 0001 is frozen, schema changes rebuild projections

Folding the chain into `0001_init.sql` (5385493) created a trap the next
commit fell into: 88171e3 added `runs.view/dormant/rebuilding` by editing 0001
in place. Every existing database had already applied version 1 — the
pre-squash ones collapsed to `{1}`, the fresh ones created with it — so none
of them ever saw the edit, and the daemon died at boot on `column "view"
does not exist`. Two rules now hold that line:

- **`0001_init.sql` is frozen** at its squash-time content, pinned by sha256
  in `TestInitMigrationIsFrozen`. The collapse logic (`preSquashHead`) relies
  on "ledger 1..25 ⇒ exactly the init schema", which is only true while the
  file does not move. Every schema change since is its own `NNNN_name.sql`,
  written to be a no-op where a fresh database already has the result
  (`ADD COLUMN IF NOT EXISTS`); `0002_runs_view.sql` is the catch-up for the
  three columns.
- **Applying any migration rebuilds the projections** (`pgstore.Open` →
  `RebuildProjections`). A schema change can add a projection column every
  existing row lacks, and the projections are derived data by design, so one
  replay of the event log at the migrating boot is always safe — and is what
  turns `view = '{}'` on 57 old runs back into their views. A fresh database
  rebuilds nothing (no runs); an unchanged one applies nothing.

`TestMigrateCatchesUpAnInitEditedInPlace` reproduces the local stack's exact
state (ledger `{1}`, columns missing, runs on the log) and checks the boot
after it: ledger `{1,2}`, every run's view rebuilt, a reopen idempotent.
