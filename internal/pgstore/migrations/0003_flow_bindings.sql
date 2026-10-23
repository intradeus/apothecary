-- A project's values for a flow's parameters (flow v4 `parameters:`): what
-- Workshop → Flows → Configure saves. The document declares each parameter
-- and its default; the project binds values here; a run may override. Keyed
-- by (project, flow name) with no FK to flows — a built-in template has no
-- row there, and binding one is the common case — and never part of the
-- flow's hash: a binding changes nothing in the document. Dropped with the
-- project.
CREATE TABLE IF NOT EXISTS flow_bindings (
    project_id text NOT NULL,
    flow_name  text NOT NULL,
    params     jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, flow_name),
    CONSTRAINT flow_bindings_project_fkey
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);
