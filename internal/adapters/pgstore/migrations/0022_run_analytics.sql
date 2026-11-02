-- rebuild: projections
-- Run analytics (TODO 23, docs/observability-analytics-plan.md): the facts
-- run.Apply folds beside the view — where a run's time went, what it spent,
-- how often it retried, asked and was held — projected per run (run_facts),
-- per stage attempt (stage_attempts) and per pinned skillset
-- (run_skillsets), in the append transaction like every other projection.
-- Nothing here is a source of truth: the header above rebuilds them from the
-- log. No foreign keys: a projection row may land before or without its
-- siblings during a rebuild.
--
-- runs.ticket_id lets the run list scope by ticket in SQL. The analytics
-- dashboards and scorecards a person builds are configuration, not
-- projections: they survive a rebuild.

ALTER TABLE runs ADD COLUMN IF NOT EXISTS ticket_id text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS runs_user_ticket_idx ON runs (user_id, ticket_id);

CREATE TABLE IF NOT EXISTS run_facts (
    run_id             text PRIMARY KEY,
    user_id            text NOT NULL DEFAULT '',
    project_id         text NOT NULL DEFAULT '',
    ticket_id          text NOT NULL DEFAULT '',
    ticket_change_type text NOT NULL DEFAULT '',
    tracker_kind       text NOT NULL DEFAULT '',
    flow_name          text NOT NULL DEFAULT '',
    flow_hash          text NOT NULL DEFAULT '',
    started_by         text NOT NULL DEFAULT '',
    state              text NOT NULL DEFAULT '',
    end_reason         text NOT NULL DEFAULT '',
    stalled_at         text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL,
    ended_at           timestamptz,
    wall_ms            bigint NOT NULL DEFAULT 0,
    queued_ms          bigint NOT NULL DEFAULT 0,
    provisioning_ms    bigint NOT NULL DEFAULT 0,
    active_ms          bigint NOT NULL DEFAULT 0,
    waiting_human_ms   bigint NOT NULL DEFAULT 0,
    held_ms            bigint NOT NULL DEFAULT 0,
    paused_ms          bigint NOT NULL DEFAULT 0,
    human_response_ms  bigint NOT NULL DEFAULT 0,
    cost_usd           double precision NOT NULL DEFAULT 0,
    rework_cost_usd    double precision NOT NULL DEFAULT 0,
    input_tokens       bigint NOT NULL DEFAULT 0,
    output_tokens      bigint NOT NULL DEFAULT 0,
    turns              integer NOT NULL DEFAULT 0,
    stages             integer NOT NULL DEFAULT 0,
    attempts           integer NOT NULL DEFAULT 0,
    failed_attempts    integer NOT NULL DEFAULT 0,
    retries            integer NOT NULL DEFAULT 0,
    try_agains         integer NOT NULL DEFAULT 0,
    pings              integer NOT NULL DEFAULT 0,
    pings_unwanted     integer NOT NULL DEFAULT 0,
    pings_answered     integer NOT NULL DEFAULT 0,
    gates_rejected     integer NOT NULL DEFAULT 0,
    holds              integer NOT NULL DEFAULT 0,
    mrs_created        integer NOT NULL DEFAULT 0,
    success            boolean NOT NULL DEFAULT false,
    first_pass         boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS run_facts_user_created_idx ON run_facts (user_id, created_at);
CREATE INDEX IF NOT EXISTS run_facts_user_flow_idx ON run_facts (user_id, flow_name, flow_hash);
CREATE INDEX IF NOT EXISTS run_facts_user_ticket_idx ON run_facts (user_id, ticket_id);

-- One row per stage attempt, keyed by its stage_started event's seq.
CREATE TABLE IF NOT EXISTS stage_attempts (
    run_id             text NOT NULL,
    seq                bigint NOT NULL,
    user_id            text NOT NULL DEFAULT '',
    project_id         text NOT NULL DEFAULT '',
    flow_name          text NOT NULL DEFAULT '',
    flow_hash          text NOT NULL DEFAULT '',
    ticket_change_type text NOT NULL DEFAULT '',
    stage_id           text NOT NULL DEFAULT '',
    iteration          integer NOT NULL DEFAULT 0,
    harness            text NOT NULL DEFAULT '',
    model              text NOT NULL DEFAULT '',
    agent              text NOT NULL DEFAULT '',
    agent_hash         text NOT NULL DEFAULT '',
    outcome            text NOT NULL DEFAULT '',
    failure_reason     text NOT NULL DEFAULT '',
    started_at         timestamptz NOT NULL,
    ended_at           timestamptz,
    active_ms          bigint NOT NULL DEFAULT 0,
    waiting_ms         bigint NOT NULL DEFAULT 0,
    cost_usd           double precision NOT NULL DEFAULT 0,
    input_tokens       bigint NOT NULL DEFAULT 0,
    output_tokens      bigint NOT NULL DEFAULT 0,
    turns              integer NOT NULL DEFAULT 0,
    pings              integer NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, seq)
);
CREATE INDEX IF NOT EXISTS stage_attempts_user_stage_model_idx ON stage_attempts (user_id, stage_id, model);

-- The skillsets a run pinned at creation.
CREATE TABLE IF NOT EXISTS run_skillsets (
    run_id      text NOT NULL,
    skillset_id text NOT NULL,
    user_id     text NOT NULL DEFAULT '',
    name        text NOT NULL DEFAULT '',
    seq         integer NOT NULL DEFAULT 0,
    hash        text NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, skillset_id)
);

CREATE TABLE IF NOT EXISTS analytics_dashboards (
    id         text PRIMARY KEY,
    user_id    text NOT NULL,
    name       text NOT NULL,
    filters    jsonb NOT NULL DEFAULT '[]',
    panels     jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS analytics_dashboards_user_idx ON analytics_dashboards (user_id);

CREATE TABLE IF NOT EXISTS analytics_scorecards (
    id         text PRIMARY KEY,
    user_id    text NOT NULL,
    name       text NOT NULL,
    terms      jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS analytics_scorecards_user_idx ON analytics_scorecards (user_id);
