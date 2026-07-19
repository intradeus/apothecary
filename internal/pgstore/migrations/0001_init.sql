-- Initial schema (DESIGN.md §6, §9).
CREATE TABLE events (
    run_id  text        NOT NULL,
    seq     bigint      NOT NULL,
    ts      timestamptz NOT NULL,
    type    text        NOT NULL,
    payload jsonb       NOT NULL,
    PRIMARY KEY (run_id, seq)
);

-- Read-model projection, updated in the same transaction as each append;
-- rebuildable from events (factoryd rebuild-projections).
CREATE TABLE runs (
    run_id        text PRIMARY KEY,
    state         text NOT NULL,
    ticket_title  text NOT NULL DEFAULT '',
    flow_name     text NOT NULL DEFAULT '',
    flow_hash     text NOT NULL DEFAULT '',
    current_stage text NOT NULL DEFAULT '',
    mr_url        text NOT NULL DEFAULT '',
    env_id        text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);

CREATE TABLE pings (
    ping_id     text PRIMARY KEY,
    run_id      text NOT NULL,
    type        text NOT NULL,
    question    text NOT NULL DEFAULT '',
    context     text NOT NULL DEFAULT '',
    payload_ref text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'pending', -- pending|answered
    answer      text NOT NULL DEFAULT '',
    feedback    text NOT NULL DEFAULT '',
    raised_at   timestamptz NOT NULL,
    answered_at timestamptz
);

CREATE INDEX pings_status_idx ON pings (status);

-- Channel secrets are plaintext jsonb: accepted v1 residual (§9, §15).
CREATE TABLE channels (
    id         text PRIMARY KEY,
    kind       text NOT NULL,
    config     jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
