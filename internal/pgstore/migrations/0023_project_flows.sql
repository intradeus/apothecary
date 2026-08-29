-- Flows belong to a project.
--
-- A flow is not portable the way it looked on paper: a command stage runs
-- `make verify`, a stage names an agent that knows this repository's layout,
-- an egress rule assumes this project's registry. A globally-named "default"
-- that every project shares can only ever hold what is true everywhere, which
-- is almost nothing. So identity becomes (project_id, name).
--
-- Two things survive the move deliberately:
--   * the built-ins (internal/builtin) stay global TEMPLATES, resolved by name
--     for any project that has no row of its own — saving one creates that
--     project's editable copy, exactly the shadowing that already existed,
--     now per project;
--   * copying: a flow can be cloned into another project, which is how a good
--     one spreads now that it cannot be shared by reference.
--
-- Existing rows are copied into EVERY project, because that is what they
-- already meant: a flow named X resolved for every project, so every project
-- keeps resolving the same document. Duplicate rows are the honest cost of
-- the model — the alternative silently changes what some project runs.

ALTER TABLE flows ADD COLUMN project_id text;
ALTER TABLE flow_versions ADD COLUMN project_id text;

-- The old keys go FIRST. The fan-out below writes one row per project for each
-- global flow, and `name` (resp. `hash`) is still the primary key until it is
-- dropped — the second copy of any flow would collide with the first.
ALTER TABLE flows DROP CONSTRAINT flows_pkey;
ALTER TABLE flow_versions DROP CONSTRAINT flow_versions_pkey;

-- Fan the global rows out per project, then drop the originals. A row for a
-- project that no longer exists never had a reader.
INSERT INTO flows (project_id, name, yaml, hash, layout, updated_at)
SELECT p.id, f.name, f.yaml, f.hash, f.layout, f.updated_at
FROM flows f CROSS JOIN projects p
WHERE f.project_id IS NULL;

INSERT INTO flow_versions (project_id, hash, name, yaml, created_at)
SELECT p.id, v.hash, v.name, v.yaml, v.created_at
FROM flow_versions v CROSS JOIN projects p
WHERE v.project_id IS NULL;

DELETE FROM flows WHERE project_id IS NULL;
DELETE FROM flow_versions WHERE project_id IS NULL;

ALTER TABLE flows ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE flow_versions ALTER COLUMN project_id SET NOT NULL;

-- (project_id, name) is the identity; deleting a project takes its flows and
-- their history with it — nothing else can reach them.
ALTER TABLE flows ADD PRIMARY KEY (project_id, name);
ALTER TABLE flows ADD CONSTRAINT flows_project_fkey
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE;

-- A version is one project's history of one document. The same content saved
-- in two projects is two rows with the same hash: content-addressing still
-- dedupes WITHIN a project, which is what the version list shows.
ALTER TABLE flow_versions ADD PRIMARY KEY (project_id, hash);
ALTER TABLE flow_versions ADD CONSTRAINT flow_versions_project_fkey
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE;

DROP INDEX IF EXISTS flow_versions_name_idx;
CREATE INDEX flow_versions_project_name_idx ON flow_versions (project_id, name);
