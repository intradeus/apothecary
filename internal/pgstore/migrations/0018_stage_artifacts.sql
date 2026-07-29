-- Per-attempt artifact snapshots (PLAN5 M27). A stage's declared outputs and
-- its full log were previously ephemeral three times over: attempt N+1
-- overwrote attempt N at the same path, the workspace died with the run, and
-- the event store kept only 8 KB tails. So "show me the plan the human
-- rejected at attempt 1" was unanswerable.
--
-- Snapshots reuse this table (bytes on the filesystem, metadata here) with
-- two new scopes — stage_output and stage_log — plus the attribution a run
-- timeline needs. They are referenced from artifact_published events, which
-- carry the id and never the bytes: events stay replay-cheap while the
-- content they point at can be any size.
ALTER TABLE artifacts ADD COLUMN stage_id  text NOT NULL DEFAULT '';
ALTER TABLE artifacts ADD COLUMN iteration int  NOT NULL DEFAULT 0;
-- The declared output's NAME (its role: "plan", "verdict"), not its path —
-- the path is the filename, the name is what the contract calls it.
ALTER TABLE artifacts ADD COLUMN output_name text NOT NULL DEFAULT '';

-- The run timeline reads a run's snapshots in attempt order.
CREATE INDEX artifacts_stage_idx ON artifacts (run_id, stage_id, iteration);
