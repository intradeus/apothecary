-- Instance limits (2026-10-27): the capacity caps — max concurrent runs and
-- max live environments — are an admin's setting, edited in the UI, instead
-- of deployment environment variables. One row for the whole instance; no row
-- means the defaults. -1 = no cap.

CREATE TABLE IF NOT EXISTS instance_limits (
    id                  boolean PRIMARY KEY DEFAULT true CHECK (id),
    max_concurrent_runs integer NOT NULL CHECK (max_concurrent_runs = -1 OR max_concurrent_runs > 0),
    max_environments    integer NOT NULL CHECK (max_environments = -1 OR max_environments > 0),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
