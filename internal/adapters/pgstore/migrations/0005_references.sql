-- References (apothecary/internal/run/ref): one naming standard for a stage's
-- attempt (review#5) and what is inside it (review#5/decision#1).
--
-- The attempt number is called an attempt everywhere — in events, the API,
-- references and the UI — so the columns that hold it follow.
-- snapshots_run_idx keeps its name and covers the renamed column.
ALTER TABLE snapshots RENAME COLUMN iteration TO attempt;
ALTER TABLE stage_attempts RENAME COLUMN iteration TO attempt;

-- A ping carries its reference from ping_raised (build#3/decision#1, or
-- run/decision#2 outside any attempt): the inbox and the ping card show it.
ALTER TABLE pings ADD COLUMN ref text NOT NULL DEFAULT '';
