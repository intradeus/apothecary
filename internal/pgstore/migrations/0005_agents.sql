-- Agent library (DESIGN3 §1): per-project agents, UI-edited, versioned.
-- The target repo's .apothecary/agents/ contract is retired.
CREATE TABLE agents (
    project_id  text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    read_only   bool NOT NULL DEFAULT false,
    allow_bash  bool NOT NULL DEFAULT false,
    prompt      text NOT NULL,
    hash        text NOT NULL,
    updated_at  timestamptz NOT NULL,
    PRIMARY KEY (project_id, name)
);

-- Content-addressed history (the flows pattern). project_id is absent from
-- the content and the hash: identical prompts share a version across
-- projects, which is what copy-tracking analytics want.
CREATE TABLE agent_versions (
    hash       text PRIMARY KEY,
    name       text NOT NULL,
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL
);
