-- Config plane (DESIGN2.md §2): integrations, projects, flows, settings.
-- Secrets are secretbox-sealed JSON (§3); config is plain jsonb.
CREATE TABLE integrations (
    id         text PRIMARY KEY,
    port       text NOT NULL,             -- forge|provisioner|harness|provider
    kind       text NOT NULL,
    name       text NOT NULL,
    config     jsonb NOT NULL DEFAULT '{}',
    secrets    bytea NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (port, name)
);

-- The FK RESTRICT makes integration deletion fail while a project
-- references it; the API maps the violation to 409 naming the projects.
CREATE TABLE projects (
    id             text PRIMARY KEY,
    name           text NOT NULL UNIQUE,
    forge_id       text NOT NULL REFERENCES integrations(id),
    provisioner_id text NOT NULL REFERENCES integrations(id),
    harness_id     text NOT NULL REFERENCES integrations(id),
    provider_ids   jsonb NOT NULL DEFAULT '[]',
    repo           jsonb NOT NULL,
    commands       jsonb NOT NULL DEFAULT '{}',
    workspace      jsonb NOT NULL DEFAULT '{}',
    default_flow   text NOT NULL DEFAULT 'v1',
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL
);

CREATE TABLE flows (
    name       text PRIMARY KEY,
    yaml       text NOT NULL,
    hash       text NOT NULL,
    updated_at timestamptz NOT NULL
);

-- name is an addition over the DESIGN2 §2 sketch: the per-flow version
-- listing (GET /api/flows/{name}/versions) needs the linkage.
CREATE TABLE flow_versions (
    hash       text PRIMARY KEY,
    name       text NOT NULL,
    yaml       text NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX flow_versions_name_idx ON flow_versions (name);

CREATE TABLE settings (
    key   text PRIMARY KEY,
    value jsonb NOT NULL
);

-- Runs carry their project (DESIGN2 §6.3). No FK: runs are kept forever,
-- projects are deletable once all their runs are archived.
ALTER TABLE runs ADD COLUMN project_id text NOT NULL DEFAULT '';
