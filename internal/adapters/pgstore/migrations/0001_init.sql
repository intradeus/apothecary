-- Initial schema. Three groups of tables:
--   * the config plane (integrations, projects, flows, settings, tickets):
--     durable, edited through the API;
--   * the event store (events) and its projections (runs, pings, snapshots):
--     projections are updated in the same transaction as each append and are
--     rebuildable from events alone (apothecaryd rebuild-projections, which
--     TRUNCATEs them — so nothing may FK a projection);
--   * standalone durable side-tables (channels, run_exposed_ports,
--     attachments, egress_audit): not projections, rebuild leaves them alone.

-- --- config plane -----------------------------------------------------------

-- Secrets are secretbox-sealed JSON; config is plain jsonb.
CREATE TABLE integrations (
    id         text PRIMARY KEY,
    port       text NOT NULL,             -- repository|provisioner|harness|ticketsource
    kind       text NOT NULL,
    name       text NOT NULL,
    config     jsonb NOT NULL DEFAULT '{}',
    secrets    bytea NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (port, name)
);

-- The repo_account/provisioner FKs make integration deletion fail while a
-- project references it; the API maps the violation to 409 naming the
-- projects.
-- harnesses is the SET of available harness integration ids (the flow owns
-- the default harness); DeleteIntegration guards it with a `harnesses ? id`
-- scan. repo is the primary repository (the devcontainer's), repos the
-- additional ones checked out beside it.
--
-- default_flow has no FK: built-in flows (internal/builtin) resolve by name
-- without a flows row. UpsertProject only lands while the flow resolves, and
-- DeleteFlow only deletes while no project references the name.
CREATE TABLE projects (
    id              text PRIMARY KEY,
    name            text NOT NULL UNIQUE,
    repo_account_id text NOT NULL REFERENCES integrations(id),
    provisioner_id  text NOT NULL REFERENCES integrations(id),
    harnesses       jsonb NOT NULL DEFAULT '[]'::jsonb,
    repo            jsonb NOT NULL,
    repos           jsonb NOT NULL DEFAULT '[]'::jsonb,
    commands        jsonb NOT NULL DEFAULT '{}',
    workspace       jsonb NOT NULL DEFAULT '{}',
    default_flow    text NOT NULL,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL
);

-- Flows belong to a project: (project_id, name) is the identity. The
-- built-ins stay global templates, resolved by name for any project with no
-- row of its own; saving one creates that project's editable copy. layout is
-- the flow builder's node positions — display metadata, EXCLUDED from the
-- content hash, so moving a node never creates a version.
CREATE TABLE flows (
    project_id text NOT NULL,
    name       text NOT NULL,
    yaml       text NOT NULL,
    hash       text NOT NULL,
    layout     jsonb,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, name),
    CONSTRAINT flows_project_fkey
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

-- Content-addressed history of one project's flow documents: the same
-- content saved in two projects is two rows with the same hash.
CREATE TABLE flow_versions (
    project_id text NOT NULL,
    hash       text NOT NULL,
    name       text NOT NULL,
    yaml       text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, hash),
    CONSTRAINT flow_versions_project_fkey
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);
CREATE INDEX flow_versions_project_name_idx ON flow_versions (project_id, name);

CREATE TABLE settings (
    key   text PRIMARY KEY,
    value jsonb NOT NULL
);

-- A ticket is a unit of work that owns many runs (each run is one attempt).
-- base_branch is the human-chosen branch stem; each run derives its own
-- branch as <base_branch>-<run-id tail>. Flow is a per-run choice, not here.
--
-- source_* record where an imported ticket was copied from (empty when typed
-- in by hand). A record of the import, not a live link — hence source_kind
-- beside source_id, and no FK to the integration.
CREATE TABLE tickets (
    id          text PRIMARY KEY,
    project_id  text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title       text NOT NULL,
    goal        text NOT NULL DEFAULT '',
    base_branch text NOT NULL DEFAULT '',
    source_id   text NOT NULL DEFAULT '',
    source_kind text NOT NULL DEFAULT '',
    source_key  text NOT NULL DEFAULT '',
    source_url  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);
CREATE INDEX tickets_project_idx ON tickets (project_id);
-- The import path refuses a second copy of the same issue into a project.
CREATE INDEX tickets_source_idx ON tickets (project_id, source_key)
    WHERE source_key <> '';

-- --- event store + projections ----------------------------------------------

CREATE TABLE events (
    run_id  text        NOT NULL,
    seq     bigint      NOT NULL,
    ts      timestamptz NOT NULL,
    type    text        NOT NULL,
    payload jsonb       NOT NULL,
    PRIMARY KEY (run_id, seq)
);

-- project_id has no FK: runs are kept forever, projects are deletable once
-- all their runs are archived.
CREATE TABLE runs (
    run_id        text PRIMARY KEY,
    project_id    text NOT NULL DEFAULT '',
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

-- stage_id is the flow stage in flight when the ping was raised (the join key
-- from reviews to stages and prompt versions). answer_kind is the declared
-- answer shape (decision|choice|multi|text); options the agent-declared
-- choices. files/link mirror ping_raised's typed payload;
-- answer_attachment_ids mirror ping_answered.
CREATE TABLE pings (
    ping_id               text PRIMARY KEY,
    run_id                text NOT NULL,
    stage_id              text NOT NULL DEFAULT '',
    type                  text NOT NULL,
    answer_kind           text NOT NULL DEFAULT '',
    question              text NOT NULL DEFAULT '',
    context               text NOT NULL DEFAULT '',
    options               jsonb,
    files                 jsonb,
    link                  text,
    status                text NOT NULL DEFAULT 'pending', -- pending|answered
    answer                text NOT NULL DEFAULT '',
    feedback              text NOT NULL DEFAULT '',
    answer_attachment_ids jsonb,
    raised_at             timestamptz NOT NULL,
    answered_at           timestamptz
);
CREATE INDEX pings_status_idx ON pings (status);

-- Engine-captured record: a stage attempt's declared output, its log, or a
-- ping file (what the agent showed the human, as shown). Insert-only; never
-- delivered into a workspace. A projection of snapshot_captured events; the
-- blob bytes live on disk under <RunsDir>.
CREATE TABLE snapshots (
    id         text PRIMARY KEY,
    run_id     text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('output','log','ping_file')),
    stage_id   text NOT NULL,   -- '_provision' pseudo-stage allowed
    iteration  int  NOT NULL DEFAULT 0,
    ping_id    text,            -- ping_file only
    name       text,            -- declared output role ('plan'); NULL otherwise
    path       text,            -- workspace path captured; NULL for logs
    filename   text   NOT NULL,
    mime       text   NOT NULL,
    size       bigint NOT NULL,
    sha256     text   NOT NULL,
    truncated  boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'ping_file') = (ping_id IS NOT NULL)),
    CHECK ((kind = 'output')    = (name IS NOT NULL)),
    CHECK ((kind <> 'log')      = (path IS NOT NULL))
);
CREATE INDEX snapshots_run_idx   ON snapshots (run_id, stage_id, iteration);
CREATE INDEX snapshots_ping_idx  ON snapshots (ping_id) WHERE ping_id IS NOT NULL;
CREATE INDEX snapshots_sweep_idx ON snapshots (created_at);

-- --- durable side-tables ----------------------------------------------------

-- Channel secrets are plaintext jsonb: accepted v1 residual.
CREATE TABLE channels (
    id         text PRIMARY KEY,
    kind       text NOT NULL,
    config     jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Persisted workspace-port exposures (portrelay): lets an active run's
-- exposed ports come back on the SAME host port after an engine restart.
-- Pruned when the run's environment is torn down.
CREATE TABLE run_exposed_ports (
    run_id     text        NOT NULL,
    ws_port    integer     NOT NULL,
    host_port  integer     NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, ws_port)
);

-- Human-given files; bytes on disk under <RunsDir>. Ownership is structural:
-- all owner columns NULL = unclaimed (GC-eligible); ticket_id = ticket
-- attachment (shared by every run of the ticket); run_id alone = run-goal
-- attachment; run_id + ping_id = ping-answer file. Only tickets gets an FK
-- (runs/pings are TRUNCATEd projections), and even that is a backstop: the
-- store deletes attachment rows itself so the blob sweep sees them go.
CREATE TABLE attachments (
    id         text PRIMARY KEY,
    ticket_id  text REFERENCES tickets(id) ON DELETE CASCADE,
    run_id     text,
    ping_id    text,
    filename   text   NOT NULL,
    mime       text   NOT NULL,
    size       bigint NOT NULL,
    sha256     text   NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (ticket_id IS NULL OR run_id IS NULL),   -- exactly one owner
    CHECK (ping_id IS NULL OR run_id IS NOT NULL)  -- an answer file names its run
);
CREATE INDEX attachments_ticket_idx ON attachments (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX attachments_run_idx    ON attachments (run_id)    WHERE run_id IS NOT NULL;
CREATE INDEX attachments_ping_idx   ON attachments (ping_id)   WHERE ping_id IS NOT NULL;
CREATE INDEX attachments_orphan_idx ON attachments (created_at)
    WHERE ticket_id IS NULL AND run_id IS NULL;

-- Egress audit trail: what each workspace reached (or was blocked from). The
-- per-project gateway aggregates connections into (run, host, port, verdict)
-- tuples; the engine ingests them here so the trail outlives the ephemeral
-- gateway. Counts/bytes are cumulative within a gateway's lifetime, so
-- ingestion upserts with GREATEST (pgstore.UpsertEgressAudit).
CREATE TABLE egress_audit (
    project    text NOT NULL DEFAULT '',
    run        text NOT NULL,
    host       text NOT NULL,            -- SNI / HTTP Host / DNS-resolved name / dst IP
    dst_ip     text NOT NULL DEFAULT '',
    port       int  NOT NULL DEFAULT 0,
    proto      text NOT NULL DEFAULT '', -- tls | http | other
    verdict    text NOT NULL,            -- allow | deny
    count      bigint NOT NULL DEFAULT 0,
    bytes_out  bigint NOT NULL DEFAULT 0,
    bytes_in   bigint NOT NULL DEFAULT 0,
    first_seen timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL,
    PRIMARY KEY (run, host, port, verdict)
);
CREATE INDEX egress_audit_project_idx ON egress_audit (project, last_seen DESC);
CREATE INDEX egress_audit_run_idx     ON egress_audit (run, last_seen DESC);
CREATE INDEX egress_audit_verdict_idx ON egress_audit (verdict);
