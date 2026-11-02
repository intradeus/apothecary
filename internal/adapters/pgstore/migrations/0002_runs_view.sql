-- rebuild: projections
-- The runs projection grew three columns after the chain was squashed into
-- 0001 (88171e3, "projection reads"): the whole run.View as JSON next to the
-- typed columns SQL filters on, plus the two view fields the JSON omits
-- (json:"-") that run.Apply reads — without them the incremental fold
-- disagrees with a full reduce (a parked run's answer would project as
-- "running"). They were first added by editing 0001 in place, which a database
-- that had already applied version 1 never saw: the daemon then died at boot
-- on `column "view" does not exist`. This is the catch-up. 0001 is frozen at
-- its squash-time content again (pgstore's TestInitMigrationIsFrozen), so a
-- fresh database and a caught-up one end with the same schema.
--
-- Applying any migration triggers a projection rebuild from the event log
-- (pgstore.Open), which fills `view` for every existing run.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS view       jsonb   NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS dormant    boolean NOT NULL DEFAULT false;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS rebuilding boolean NOT NULL DEFAULT false;
