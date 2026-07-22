-- The project no longer has a DEFAULT harness — it holds only a SET of
-- available harness integrations (the `harnesses` jsonb column, added in
-- 0009 and backfilled from harness_id). The flow now owns the default
-- harness (defaults.harness), so the harness_id column (and its FK) is
-- dropped. Referential integrity for harness deletion is preserved by the
-- `harnesses ? id` scan in DeleteIntegration.
ALTER TABLE projects DROP COLUMN harness_id;
