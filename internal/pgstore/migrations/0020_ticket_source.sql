-- Ticket provenance: where an imported ticket was copied from (Jira in v1).
-- Empty for tickets typed in by hand. This is a record of the import, NOT a
-- live link — apothecary never writes back to the tracker and never re-reads
-- the issue, so the columns stay meaningful after the integration row is
-- deleted (hence source_kind alongside source_id, and no FK).
ALTER TABLE tickets
    ADD COLUMN source_id   text NOT NULL DEFAULT '',
    ADD COLUMN source_kind text NOT NULL DEFAULT '',
    ADD COLUMN source_key  text NOT NULL DEFAULT '',
    ADD COLUMN source_url  text NOT NULL DEFAULT '';

-- The import path refuses a second copy of the same issue into the same
-- project (a duplicate is nearly always a mis-click), and this is the lookup
-- it does.
CREATE INDEX tickets_source_idx ON tickets (project_id, source_key)
    WHERE source_key <> '';
