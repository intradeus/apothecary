-- Egress audit trail: the durable record of what each workspace reached (or was
-- blocked from) on the network. The per-project egress gateway aggregates every
-- connection into (run, host, port, verdict) tuples with counts, byte totals and
-- first/last timestamps; the engine ingests them here so the trail survives the
-- ephemeral gateway (which is reaped when a project's last run ends).
--
-- Counts/bytes are cumulative within a gateway's lifetime, so ingestion upserts
-- with GREATEST (monotonic) — see pgstore.UpsertEgressAudit.
CREATE TABLE egress_audit (
    project    text NOT NULL DEFAULT '',
    run        text NOT NULL,
    host       text NOT NULL,       -- SNI / HTTP Host / DNS-resolved name / dst IP
    dst_ip     text NOT NULL DEFAULT '',
    port       int  NOT NULL DEFAULT 0,
    proto      text NOT NULL DEFAULT '', -- tls | http | other
    verdict    text NOT NULL,        -- allow | deny
    count      bigint NOT NULL DEFAULT 0,
    bytes_out  bigint NOT NULL DEFAULT 0,
    bytes_in   bigint NOT NULL DEFAULT 0,
    first_seen timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL,
    PRIMARY KEY (run, host, port, verdict)
);
CREATE INDEX egress_audit_project_idx ON egress_audit (project, last_seen DESC);
CREATE INDEX egress_audit_run_idx ON egress_audit (run, last_seen DESC);
CREATE INDEX egress_audit_verdict_idx ON egress_audit (verdict);
