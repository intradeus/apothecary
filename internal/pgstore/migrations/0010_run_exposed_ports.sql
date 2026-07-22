-- Persisted workspace-port exposures (portrelay): a small standalone
-- durable table (like `channels`), NOT an event projection. Lets an active
-- run's exposed ports come back on the SAME host port after an engine
-- restart. Rows are pruned when the run's environment is torn down
-- (destroy/archive/reap).
CREATE TABLE run_exposed_ports (
    run_id     text        NOT NULL,
    ws_port    integer     NOT NULL,
    host_port  integer     NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, ws_port)
);
