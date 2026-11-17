-- rebuild: projections
-- Archived is a flag on a run, not a state: run_archived no longer replaces
-- how the run ended (completed, abandoned, failed), it sets runs.archived_at.
-- run_facts.archived is the analytics dimension, apart from state, so success
-- and first_pass stay what the run earned. The header replays every run from
-- the event log: each archived run gets its outcome back in runs.state and
-- run_facts.state, and both new columns are filled.

ALTER TABLE runs ADD COLUMN archived_at timestamptz;
ALTER TABLE run_facts ADD COLUMN archived boolean NOT NULL DEFAULT false;
