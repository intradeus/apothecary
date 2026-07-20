-- ask_human option sets (PLAN4 M16): agent-declared answer choices; the
-- recorded answer is always one of them. Backfill: rebuild-projections.
ALTER TABLE pings ADD COLUMN options jsonb;
