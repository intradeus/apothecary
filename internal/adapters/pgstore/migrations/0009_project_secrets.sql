-- Project secrets (2026-10-26): the values a flow reads where it says
-- ${secrets.<name>} — an MCP server's token, an API key in a header. One row
-- per secret, each value sealed by the config service (the master key never
-- reaches the database), so listing names needs no key and setting one is a
-- single upsert. They go with their project.
CREATE TABLE IF NOT EXISTS project_secrets (
    project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       text NOT NULL,
    value      bytea NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, name)
);
