-- Automations (flow v4): the ways a run begins without a person pressing
-- Start. Each names a flow, one of its run_start phases and parameter
-- overrides, and either polls a tracker query — importing what it finds as
-- tickets of the project, one run each — or fires on a cron schedule for one
-- ticket. internal/automation watches them from the daemon; the last poll,
-- the last firing and the last error are kept here for the workshop to show.
CREATE TABLE IF NOT EXISTS automations (
    id             text PRIMARY KEY,
    project_id     text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name           text NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('tracker_query', 'cron')),
    source_id      text NOT NULL DEFAULT '',
    query          text NOT NULL DEFAULT '',
    schedule       text NOT NULL DEFAULT '',
    ticket_id      text NOT NULL DEFAULT '',
    flow           text NOT NULL,
    phase          text NOT NULL DEFAULT '',
    params         jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled        boolean NOT NULL DEFAULT true,
    last_polled_at timestamptz,
    last_fired_at  timestamptz,
    last_error     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS automations_project_idx ON automations (project_id);

-- A ticket an automation imported remembers which one, so a later poll that
-- finds the same issue can tell "mine, still without a run" (a crash between
-- the import and the run) from "a person's" (left alone).
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS automation_id text NOT NULL DEFAULT '';
