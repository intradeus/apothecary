-- Agent I/O contract (DESIGN4 §1): declared inputs/outputs on agent
-- definitions. Additive jsonb columns default to empty arrays so existing
-- rows are unaffected and their content hashes (omitempty) stay stable.
ALTER TABLE agents
    ADD COLUMN inputs  jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN outputs jsonb NOT NULL DEFAULT '[]';
