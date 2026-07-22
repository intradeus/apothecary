-- Human-attached artifacts (documents/images) sent with a run's goal or a
-- ping answer. This is the METADATA side-table; the blob BYTES live on the
-- filesystem under <RunsDir>/artifacts/<id>. Like `channels` /
-- `run_exposed_ports`, standalone durable state — NOT an event projection
-- (so rebuild-projections leaves it alone). scope: orphan (uploaded, not yet
-- attached — GC-eligible) | goal | ping_answer. Agent→human (ask_human)
-- files are NOT here: they live in the workspace, referenced by
-- pings.payload_refs.
CREATE TABLE artifacts (
    id         text PRIMARY KEY,
    run_id     text        NOT NULL DEFAULT '',
    ping_id    text        NOT NULL DEFAULT '',
    scope      text        NOT NULL DEFAULT 'orphan',
    filename   text        NOT NULL DEFAULT '',
    mime       text        NOT NULL DEFAULT 'application/octet-stream',
    size       bigint      NOT NULL DEFAULT 0,
    sha256     text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX artifacts_run_idx    ON artifacts (run_id);
CREATE INDEX artifacts_ping_idx   ON artifacts (ping_id);
CREATE INDEX artifacts_orphan_idx ON artifacts (scope, created_at);
