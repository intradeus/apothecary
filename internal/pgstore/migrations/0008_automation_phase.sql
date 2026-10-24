-- Flow v5 (2026-10-24): a flow has one main chain, so a run is no longer
-- begun at a phase its creator names — an automation names its flow, and
-- the flow's run_start says whether it is started on a ticket or a merge
-- request. The column an automation's phase lived in goes.
ALTER TABLE automations DROP COLUMN IF EXISTS phase;
