-- Ping attribution (PLAN4 M15): the flow stage in flight when the ping was
-- raised — the join key from reviews to stages and (via
-- stage_started.agent_hash) to prompt versions. Historical rows backfill
-- with `apothecaryd rebuild-projections`.
ALTER TABLE pings ADD COLUMN stage_id text NOT NULL DEFAULT '';
