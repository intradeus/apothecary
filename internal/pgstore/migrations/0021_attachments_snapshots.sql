-- DESIGN-FILES §3.2: the artifacts table's two tenants get their own homes.
-- ATTACHMENTS are human-given files whose ownership is STRUCTURAL (which
-- owner columns are set) instead of a free-text scope; SNAPSHOTS are the
-- engine-captured record whose kind is CHECK'd and whose per-kind fields are
-- CHECK'd coherent. The old `artifacts` table keeps serving until phase 2
-- removes its last consumer (0022 drops it).
--
-- FK rule: RebuildProjections TRUNCATEs runs and pings, and TRUNCATE refuses
-- FK-referenced tables — so nothing here may FK runs/pings. Only tickets
-- (durable config plane) gets one, and even that is a backstop: the store
-- deletes attachment rows itself (DeleteForTicket) so the blob sweep sees
-- them go.

-- Human-given files. All owner columns NULL = unclaimed (GC-eligible);
-- ticket_id = ticket attachment (shared by every run of the ticket);
-- run_id alone = run-goal attachment; run_id + ping_id = ping-answer file.
CREATE TABLE attachments (
    id         text PRIMARY KEY,
    ticket_id  text REFERENCES tickets(id) ON DELETE CASCADE,
    run_id     text,            -- no FK: runs is a TRUNCATEd projection
    ping_id    text,            -- no FK: pings is a TRUNCATEd projection
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

-- Engine-captured record: a stage attempt's declared output, its log, or a
-- ping file (what the agent showed the human, as shown). Insert-only; never
-- delivered into a workspace. A PROJECTION of snapshot_captured events —
-- rebuild-projections TRUNCATEs and re-folds it (blobs on disk untouched).
CREATE TABLE snapshots (
    id         text PRIMARY KEY,
    run_id     text NOT NULL,   -- no FK (projection)
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
