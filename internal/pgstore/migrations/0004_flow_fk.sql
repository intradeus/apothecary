-- Close the flow-delete/project-create race at the DB level: the app-side
-- referrer check in DeleteFlow is check-then-delete with no transaction.
-- NOT VALID skips existing rows (older DBs may predate the app-side check);
-- new writes are enforced.
ALTER TABLE projects ALTER COLUMN default_flow DROP DEFAULT;
ALTER TABLE projects
    ADD CONSTRAINT projects_default_flow_fkey
    FOREIGN KEY (default_flow) REFERENCES flows(name) ON DELETE RESTRICT
    NOT VALID;
