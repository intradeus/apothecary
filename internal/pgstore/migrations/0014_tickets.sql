-- Tickets: a first-class unit of work that owns many runs (each run is one
-- attempt). Previously "ticket" was only a transient input to run creation
-- (folded into run_created); this makes it a standalone entity so runs can be
-- started, re-run and (later) seeded from an external tracker like Jira.
--
-- base_branch is the human-chosen branch stem; each run derives its own
-- actual branch as <base_branch>-<2-char run-id tail> (unique per attempt, no
-- remote collisions). Flow is NOT here — it's a per-run choice at run start.
CREATE TABLE tickets (
    id           text PRIMARY KEY,
    project_id   text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title        text NOT NULL,
    goal         text NOT NULL DEFAULT '',
    base_branch  text NOT NULL DEFAULT '',
    artifact_ids jsonb NOT NULL DEFAULT '[]',
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL
);
CREATE INDEX tickets_project_idx ON tickets (project_id);
