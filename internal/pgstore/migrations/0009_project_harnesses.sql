-- Per-step harness: a project holds a SET of harness integrations, one per
-- kind, and a flow stage may pin which harness KIND it runs on. harness_id
-- stays as the DEFAULT (stages that don't pin a harness use it). The set is
-- stored as a JSONB array of integration ids, mirroring repo/commands/
-- workspace; the default is always a member of the set.
ALTER TABLE projects ADD COLUMN harnesses jsonb NOT NULL DEFAULT '[]'::jsonb;

-- Backfill: every existing project's single harness becomes its one-element
-- set. New projects start empty and are populated by the service layer.
UPDATE projects SET harnesses = jsonb_build_array(harness_id)
WHERE harnesses = '[]'::jsonb AND harness_id <> '';
