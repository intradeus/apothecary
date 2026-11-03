-- The v1.0.0 schema: every migration of the development chain, folded into
-- one. Four groups of tables:
--   * accounts (users, auth_methods, sessions, api_tokens, ssh_keys): who a
--     request is;
--   * the config plane (integrations through notices): durable, edited
--     through the API, each configured row owned by one user (user_id —
--     there is no organisation level: a person sees nothing of anyone
--     else's);
--   * the event store (events) and its projections (runs, pings,
--     ping_review_comments, snapshots, run_facts, stage_attempts,
--     run_skillsets): projections are updated in the same transaction as
--     each append and are rebuildable from events alone (apothecaryd
--     rebuild-projections, which TRUNCATEs them — so nothing may FK a
--     projection);
--   * standalone durable side-tables (channels, run_exposed_ports,
--     attachments, egress_audit, ask_threads, ask_messages): not
--     projections, rebuild leaves them alone.
--
-- Columns keep the order the development chain gave them: a column added
-- later sits at the end of its table.

-- --- accounts ----------------------------------------------------------------

CREATE TABLE users (
    id           text PRIMARY KEY,
    handle       text NOT NULL,
    email        text NOT NULL,
    name         text NOT NULL,
    admin        boolean NOT NULL DEFAULT false,
    disabled_at  timestamptz,
    last_seen_at timestamptz,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL
);
CREATE UNIQUE INDEX users_handle_idx ON users (lower(handle));
CREATE UNIQUE INDEX users_email_idx  ON users (lower(email));

-- One row per way a user can authenticate: kind = password today; oauth,
-- passkey later as more rows of the same shape. secret is the verifier's
-- material (a password hash, a passkey public key); subject the identity on
-- the other side (OAuth issuer+subject, a credential id), unique per kind
-- when set. A user has at most one password.
CREATE TABLE auth_methods (
    id           text PRIMARY KEY,
    user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind         text NOT NULL,
    subject      text NOT NULL DEFAULT '',
    secret       bytea NOT NULL DEFAULT '',
    config       jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    last_used_at timestamptz
);
CREATE INDEX auth_methods_user_idx ON auth_methods (user_id);
CREATE UNIQUE INDEX auth_methods_subject_idx ON auth_methods (kind, subject) WHERE subject <> '';
CREATE UNIQUE INDEX auth_methods_password_idx ON auth_methods (user_id) WHERE kind = 'password';

-- id is the sha256 of the cookie value; the value itself lives only in the
-- browser.
CREATE TABLE sessions (
    id           text PRIMARY KEY,
    user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    user_agent   text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

-- Personal API tokens: hash of the secret, a display prefix.
CREATE TABLE api_tokens (
    id           text PRIMARY KEY,
    user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         text NOT NULL,
    hash         text NOT NULL UNIQUE,
    prefix       text NOT NULL,
    created_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    expires_at   timestamptz
);
CREATE INDEX api_tokens_user_idx ON api_tokens (user_id);

-- The public keys a person registers to reach their runs' workspaces through
-- the engine's SSH door. The fingerprint is unique across the instance: it is
-- what the door looks an offered key up by.
CREATE TABLE ssh_keys (
    id           text PRIMARY KEY,
    user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         text NOT NULL,
    public_key   text NOT NULL,
    fingerprint  text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL,
    last_used_at timestamptz
);
CREATE INDEX ssh_keys_user_idx ON ssh_keys (user_id);

-- --- config plane -----------------------------------------------------------

-- Secrets are secretbox-sealed JSON; config is plain jsonb. Names are unique
-- per owner. Each user has one built-in ticketsource of kind `internal`
-- (Apothecary's own tracker), created lazily by the config service
-- (confstore.Service.InternalTracker); integrations_internal_idx is what that
-- get-or-create relies on.
CREATE TABLE integrations (
    id         text PRIMARY KEY,
    port       text NOT NULL,             -- repository|provisioner|harness|ticketsource
    kind       text NOT NULL,
    name       text NOT NULL,
    config     jsonb NOT NULL DEFAULT '{}',
    secrets    bytea NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    user_id    text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX integrations_owner_port_name_idx ON integrations (user_id, port, name);
CREATE UNIQUE INDEX integrations_internal_idx ON integrations (user_id) WHERE kind = 'internal';

-- A user's model credentials — an API key, or a login whose refresh token the
-- engine keeps and refreshes — referenced by harness integrations. Sealed by
-- the config service like every secret here; meta is what a listing shows
-- without the master key (expiry, last refresh, status, account label).
CREATE TABLE provider_credentials (
    id         text PRIMARY KEY,
    user_id    text NOT NULL,
    provider   text NOT NULL,
    kind       text NOT NULL,
    name       text NOT NULL,
    secrets    bytea NOT NULL,
    meta       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX provider_credentials_owner_idx ON provider_credentials (user_id);

-- A ticketsource's statuses: fetched from the remote (and extended by sync)
-- for a remote tracker, edited by the user for the internal one. remote_id
-- is the tracker's id for the status (Jira's status id; GitHub
-- "open"/"closed"; GitLab "opened"/"closed"; the row's own id on the internal
-- tracker), so a rename follows the id. is_done ("the work is over": the poll
-- skips tickets in it) and flow_step (which status `ticket_transition to:
-- <step>` moves to, at most one per step) are the two columns the user sets.
CREATE TABLE tracker_statuses (
    id              text PRIMARY KEY,
    integration_id  text NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    remote_id       text NOT NULL,
    name            text NOT NULL,
    remote_category text NOT NULL DEFAULT '',   -- new|indeterminate|done|'' (unknown)
    position        integer NOT NULL DEFAULT 0,
    is_done         boolean NOT NULL DEFAULT false,
    flow_step       text,                       -- todo|in_progress|in_review|done|blocked, or none
    UNIQUE (integration_id, remote_id)
);
CREATE UNIQUE INDEX tracker_statuses_step_idx
    ON tracker_statuses (integration_id, flow_step) WHERE flow_step IS NOT NULL;

-- A ticketsource's issue types, each mapped onto one change type (feat,
-- fix, …): what a ticket of that type is, and what its branch is named after.
CREATE TABLE tracker_types (
    id             text PRIMARY KEY,
    integration_id text NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    remote_id      text NOT NULL,
    name           text NOT NULL,
    change_type    text NOT NULL DEFAULT 'feat',
    position       integer NOT NULL DEFAULT 0,
    UNIQUE (integration_id, remote_id)
);

-- The repo_account/provisioner FKs make integration deletion fail while a
-- project references it; the API maps the violation to 409 naming the
-- projects. harnesses is the SET of available harness integration ids (the
-- flow owns the default harness); DeleteIntegration guards it with a
-- `harnesses ? id` scan. repo is the primary repository (the
-- devcontainer's), repos the additional ones checked out beside it. Names
-- are unique per owner.
--
-- default_flow has no FK: built-in flows (internal/flow/builtin) resolve by
-- name without a flows row. UpsertProject only lands while the flow
-- resolves, and DeleteFlow only deletes while no project references the name.
CREATE TABLE projects (
    id              text PRIMARY KEY,
    name            text NOT NULL,
    repo_account_id text NOT NULL REFERENCES integrations(id),
    provisioner_id  text NOT NULL REFERENCES integrations(id),
    harnesses       jsonb NOT NULL DEFAULT '[]'::jsonb,
    repo            jsonb NOT NULL,
    repos           jsonb NOT NULL DEFAULT '[]'::jsonb,
    commands        jsonb NOT NULL DEFAULT '{}',
    workspace       jsonb NOT NULL DEFAULT '{}',
    default_flow    text NOT NULL,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    user_id         text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX projects_owner_name_idx ON projects (user_id, name);

-- The values a flow reads where it says ${secrets.<name>} — an MCP server's
-- token, an API key in a header. One row per secret, each value sealed by
-- the config service (the master key never reaches the database), so listing
-- names needs no key and setting one is a single upsert.
CREATE TABLE project_secrets (
    project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       text NOT NULL,
    value      bytea NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, name)
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

-- A project's values for a flow's parameters: what Workshop → Flows →
-- Configure saves. The document declares each parameter and its default; the
-- project binds values here; a run may override. No FK to flows — a built-in
-- template has no row there, and binding one is the common case — and never
-- part of the flow's hash: a binding changes nothing in the document.
CREATE TABLE flow_bindings (
    project_id text NOT NULL,
    flow_name  text NOT NULL,
    params     jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, flow_name),
    CONSTRAINT flow_bindings_project_fkey
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

-- Per-user settings (reaper, retention).
CREATE TABLE settings (
    key     text NOT NULL,
    value   jsonb NOT NULL,
    user_id text NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, key)
);

-- The capacity caps — max concurrent runs and max live environments — an
-- admin's setting. One row for the whole instance; no row means the
-- defaults. -1 = no cap.
CREATE TABLE instance_limits (
    id                  boolean PRIMARY KEY DEFAULT true CHECK (id),
    max_concurrent_runs integer NOT NULL CHECK (max_concurrent_runs = -1 OR max_concurrent_runs > 0),
    max_environments    integer NOT NULL CHECK (max_environments = -1 OR max_environments > 0),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- A ticket is a unit of work that owns many runs (each run is one attempt).
-- base_branch is the human-chosen branch stem; each run derives its own
-- branch as <base_branch>-<run-id tail>. Flow is a per-run choice, not here.
-- user_id is its project's owner (kept on the row so the list needs no join).
--
-- Every ticket has exactly one tracker: its remote issue when it was imported
-- (source_id set), otherwise the owner's internal tracker. An imported ticket
-- is a MIRROR of its issue (a sync overwrites title, body, status, type,
-- labels and comments); a sourceless one is written in place. source_* record
-- where it was copied from — hence source_kind beside source_id, and no FK to
-- the integration; source_type is the tracker's word for its type ("Bug").
--
-- status_id / type_id belong to the ticket's tracker; NULL on an imported
-- ticket until its first sync. change_type (feat, fix, hotfix, perf, docs,
-- chore) stays a column, derived from the type, so run_if, branch names and
-- the list need no join. remote_id is the issue's stable id (a Jira key
-- changes on a project move); remote_updated_at guards the sync against
-- applying an older snapshot; sync_state is '' (never synced), ok, error or
-- gone (the issue no longer exists: polling stops). status_changed_by says
-- who moved it: sync, run:<id>/<stage>, or user. automation_id names the
-- automation that imported it, so a later poll that finds the same issue can
-- tell "mine, still without a run" from "a person's" (left alone).
CREATE TABLE tickets (
    id                text PRIMARY KEY,
    project_id        text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title             text NOT NULL,
    goal              text NOT NULL DEFAULT '',
    base_branch       text NOT NULL DEFAULT '',
    source_id         text NOT NULL DEFAULT '',
    source_kind       text NOT NULL DEFAULT '',
    source_key        text NOT NULL DEFAULT '',
    source_url        text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    automation_id     text NOT NULL DEFAULT '',
    user_id           text NOT NULL DEFAULT '',
    change_type       text NOT NULL DEFAULT 'feat',
    source_type       text NOT NULL DEFAULT '',
    status_id         text REFERENCES tracker_statuses(id),
    type_id           text REFERENCES tracker_types(id),
    labels            text[] NOT NULL DEFAULT '{}',
    remote_id         text NOT NULL DEFAULT '',
    remote_updated_at timestamptz,
    synced_at         timestamptz,
    sync_state        text NOT NULL DEFAULT '',
    sync_error        text NOT NULL DEFAULT '',
    status_changed_at timestamptz,
    status_changed_by text NOT NULL DEFAULT ''
);
CREATE INDEX tickets_project_idx ON tickets (project_id);
CREATE INDEX tickets_user_idx ON tickets (user_id, created_at DESC);
CREATE INDEX tickets_status_idx ON tickets (status_id);
CREATE INDEX tickets_type_idx ON tickets (type_id);
CREATE INDEX tickets_sync_idx ON tickets (source_id) WHERE source_id <> '';
-- One copy of an issue per user and tracker.
CREATE UNIQUE INDEX tickets_source_key_idx
    ON tickets (user_id, source_id, lower(source_key)) WHERE source_id <> '';
-- A merge request's ticket (no source_id) is found by its URL in its project.
CREATE INDEX tickets_project_key_idx
    ON tickets (project_id, lower(source_key)) WHERE source_key <> '';

-- A ticket's comments: the remote's mirrored (origin remote, keyed by the
-- tracker's comment id), a person's typed in Apothecary (user), and a run's
-- (apothecary, keyed by the run and the stage occurrence — the internal
-- tracker's idempotency key, in place of the trailer a remote comment
-- carries).
CREATE TABLE ticket_comments (
    id         text PRIMARY KEY,
    ticket_id  text NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    remote_id  text NOT NULL DEFAULT '',
    author     text NOT NULL DEFAULT '',
    body_md    text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    origin     text NOT NULL CHECK (origin IN ('remote', 'user', 'apothecary')),
    run_id     text NOT NULL DEFAULT '',
    stage_id   text NOT NULL DEFAULT ''
);
CREATE INDEX ticket_comments_ticket_idx ON ticket_comments (ticket_id, created_at);
CREATE UNIQUE INDEX ticket_comments_remote_idx
    ON ticket_comments (ticket_id, remote_id) WHERE remote_id <> '';
CREATE UNIQUE INDEX ticket_comments_run_idx
    ON ticket_comments (ticket_id, run_id, stage_id) WHERE run_id <> '';

-- Links on an internal ticket (ticket_link / ticket_link_branch): upserted on
-- URL.
CREATE TABLE ticket_links (
    ticket_id text NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    url       text NOT NULL,
    title     text NOT NULL DEFAULT '',
    PRIMARY KEY (ticket_id, url)
);

-- The ways a run begins without a person pressing Start. Each names a flow
-- and parameter overrides, and either polls a tracker query — importing what
-- it finds as tickets of the project, one run each — fires on a cron
-- schedule for one ticket, or creates a run on each merge request others
-- open in a repository (repo; '' = the primary), once each.
-- internal/automation watches them from the daemon; the last poll, the last
-- firing and the last error are kept here for the workshop to show.
CREATE TABLE automations (
    id             text PRIMARY KEY,
    project_id     text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name           text NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('tracker_query', 'cron', 'merge_request')),
    source_id      text NOT NULL DEFAULT '',
    query          text NOT NULL DEFAULT '',
    schedule       text NOT NULL DEFAULT '',
    ticket_id      text NOT NULL DEFAULT '',
    flow           text NOT NULL,
    params         jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled        boolean NOT NULL DEFAULT true,
    last_polled_at timestamptz,
    last_fired_at  timestamptz,
    last_error     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    repo           text NOT NULL DEFAULT ''
);
CREATE INDEX automations_project_idx ON automations (project_id);

-- Skills and skillsets: a user's library of Agent Skills folders (SKILL.md +
-- support files), grouped into skillsets a run picks at creation. Skills are
-- mutable rows whose bytes live in the blob store (files is the manifest:
-- path, sha256, size, mode).
CREATE TABLE skills (
    id          text PRIMARY KEY,
    user_id     text NOT NULL DEFAULT '',
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    files       jsonb NOT NULL DEFAULT '[]',
    hash        text NOT NULL,
    source      jsonb,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);
CREATE UNIQUE INDEX skills_owner_name_idx ON skills (user_id, name);
-- A remote skill is imported once per owner: the same folder URL is the
-- same skill (refresh it, don't import it again).
CREATE UNIQUE INDEX skills_owner_source_idx ON skills (user_id, (source->>'url')) WHERE source IS NOT NULL;

-- Skillsets are versioned: every change to a member's content or to the
-- membership that changes the set's hash appends an immutable version (seq
-- 1, 2, …) — the run pins (skillset, seq).
CREATE TABLE skillsets (
    id          text PRIMARY KEY,
    user_id     text NOT NULL DEFAULT '',
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    source      jsonb,
    head_seq    integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);
CREATE UNIQUE INDEX skillsets_owner_name_idx ON skillsets (user_id, name);

-- Current membership, ordered. RESTRICT: a skill a skillset still lists is
-- not deletable (the API answers 409, naming the sets).
CREATE TABLE skillset_skills (
    skillset_id text NOT NULL REFERENCES skillsets(id) ON DELETE CASCADE,
    skill_id    text NOT NULL REFERENCES skills(id) ON DELETE RESTRICT,
    position    integer NOT NULL,
    PRIMARY KEY (skillset_id, skill_id)
);
CREATE INDEX skillset_skills_skill_idx ON skillset_skills (skill_id);

-- Immutable snapshots: manifest = [{skill_id, name, description, hash,
-- files:[{path, sha256, size, mode}]}]. Deleting the skillset takes its
-- versions with it (a live run blocks the delete; a finished run keeps its
-- pins in the event log and the bytes stay reachable through blobs only
-- while some version still names them).
CREATE TABLE skillset_versions (
    skillset_id text NOT NULL REFERENCES skillsets(id) ON DELETE CASCADE,
    seq         integer NOT NULL,
    hash        text NOT NULL,
    manifest    jsonb NOT NULL,
    created_at  timestamptz NOT NULL,
    PRIMARY KEY (skillset_id, seq)
);

-- Setup problems a person must act on — a login to sign in again, a tracker
-- that cannot be read, a failing channel, an image that will not build. Rows
-- are never pruned, only dismissed. notices_open_key is the dedupe key: while
-- a keyed notice is uncleared a repeat bumps it instead of adding a row, and
-- clearing it re-arms the key.
CREATE TABLE notices (
    id           text PRIMARY KEY,
    user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind         text NOT NULL,
    key          text NOT NULL DEFAULT '',
    title        text NOT NULL,
    body         text NOT NULL DEFAULT '',
    link         text NOT NULL DEFAULT '',
    count        integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    cleared_at   timestamptz,
    dismissed_at timestamptz
);
CREATE UNIQUE INDEX notices_open_key ON notices (user_id, key) WHERE key <> '' AND cleared_at IS NULL;
CREATE INDEX notices_user_idx ON notices (user_id, last_seen_at DESC) WHERE dismissed_at IS NULL;

-- The analytics dashboards and scorecards a person builds: configuration,
-- not projections — they survive a rebuild.
CREATE TABLE analytics_dashboards (
    id         text PRIMARY KEY,
    user_id    text NOT NULL,
    name       text NOT NULL,
    filters    jsonb NOT NULL DEFAULT '[]',
    panels     jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX analytics_dashboards_user_idx ON analytics_dashboards (user_id);

CREATE TABLE analytics_scorecards (
    id         text PRIMARY KEY,
    user_id    text NOT NULL,
    name       text NOT NULL,
    terms      jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX analytics_scorecards_user_idx ON analytics_scorecards (user_id);

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
-- all their runs are archived. view is the whole run.View as JSON next to the
-- typed columns SQL filters on; dormant and rebuilding are the two view
-- fields the JSON omits (json:"-") that run.Apply reads. user_id is the
-- project's owner at creation (run_created); ticket_id scopes the run list by
-- ticket. runs_env_idx serves the SSH door's lookup of the live run behind a
-- workspace name (env_id is cleared when the workspace is destroyed).
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
    updated_at    timestamptz NOT NULL,
    view          jsonb   NOT NULL DEFAULT '{}'::jsonb,
    dormant       boolean NOT NULL DEFAULT false,
    rebuilding    boolean NOT NULL DEFAULT false,
    user_id       text NOT NULL DEFAULT '',
    ticket_id     text NOT NULL DEFAULT ''
);
CREATE INDEX runs_user_idx ON runs (user_id, created_at DESC);
CREATE INDEX runs_env_idx ON runs (env_id) WHERE env_id <> '';
CREATE INDEX runs_user_ticket_idx ON runs (user_id, ticket_id);

-- stage_id is the flow stage in flight when the ping was raised (the join key
-- from reviews to stages and prompt versions). answer_kind is the declared
-- answer shape: yes_no (approve|reject) or form (an elicitation's fields,
-- answered accept + content or decline + note). files/link/file_types mirror
-- ping_raised's typed payload; answer_attachment_ids and curated (what a
-- person made of a review document the gate showed) mirror ping_answered.
-- phase is the phase the ping belongs to, ends_phase whether giving up on it
-- ends that phase run rather than the run.
CREATE TABLE pings (
    ping_id               text PRIMARY KEY,
    run_id                text NOT NULL,
    stage_id              text NOT NULL DEFAULT '',
    type                  text NOT NULL,
    answer_kind           text NOT NULL DEFAULT '',
    question              text NOT NULL DEFAULT '',
    context               text NOT NULL DEFAULT '',
    files                 jsonb,
    link                  text,
    status                text NOT NULL DEFAULT 'pending', -- pending|answered
    answer                text NOT NULL DEFAULT '',
    feedback              text NOT NULL DEFAULT '',
    answer_attachment_ids jsonb,
    raised_at             timestamptz NOT NULL,
    answered_at           timestamptz,
    phase                 text    NOT NULL DEFAULT '',
    ends_phase            boolean NOT NULL DEFAULT false,
    form                  jsonb,
    content               jsonb,
    user_id               text NOT NULL DEFAULT '',
    file_types            jsonb,
    curated               jsonb
);
CREATE INDEX pings_status_idx ON pings (status);
CREATE INDEX pings_user_status_idx ON pings (user_id, status);

-- What a person said about the files a ping showed, one row per comment,
-- each on one file (path) and, on a text file, a stretch of its lines
-- (start_line 0: the whole file — always, on an image). A projection of
-- ping_answered.review, rebuilt with the pings it belongs to.
CREATE TABLE ping_review_comments (
    ping_id        text NOT NULL,
    run_id         text NOT NULL,
    idx            int  NOT NULL,
    path           text NOT NULL,
    start_line     int  NOT NULL DEFAULT 0,
    end_line       int  NOT NULL DEFAULT 0,
    heading        text NOT NULL DEFAULT '',
    quote          text NOT NULL DEFAULT '',
    body           text NOT NULL,
    attachment_ids jsonb,
    PRIMARY KEY (ping_id, idx)
);
CREATE INDEX ping_review_comments_path_idx ON ping_review_comments (run_id, path);

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

-- Run analytics (docs/observability-analytics-plan.md): the facts run.Apply
-- folds beside the view — where a run's time went, what it spent, how often
-- it retried, asked and was held — per run (run_facts), per stage attempt
-- (stage_attempts, keyed by its stage_started event's seq) and per pinned
-- skillset (run_skillsets). No foreign keys: a projection row may land
-- before or without its siblings during a rebuild.
CREATE TABLE run_facts (
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
CREATE INDEX run_facts_user_created_idx ON run_facts (user_id, created_at);
CREATE INDEX run_facts_user_flow_idx ON run_facts (user_id, flow_name, flow_hash);
CREATE INDEX run_facts_user_ticket_idx ON run_facts (user_id, ticket_id);

CREATE TABLE stage_attempts (
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
CREATE INDEX stage_attempts_user_stage_model_idx ON stage_attempts (user_id, stage_id, model);

CREATE TABLE run_skillsets (
    run_id      text NOT NULL,
    skillset_id text NOT NULL,
    user_id     text NOT NULL DEFAULT '',
    name        text NOT NULL DEFAULT '',
    seq         integer NOT NULL DEFAULT 0,
    hash        text NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, skillset_id)
);

-- --- durable side-tables ----------------------------------------------------

-- Channel secrets are plaintext jsonb: accepted v1 residual.
CREATE TABLE channels (
    id         text PRIMARY KEY,
    kind       text NOT NULL,
    config     jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    user_id    text NOT NULL DEFAULT ''
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
-- user_id is whoever uploaded it, from the moment it lands. remote_id is the
-- tracker's id for an imported attachment, so a sync copies only the files
-- the issue gained since ('' for an upload); one copy per file per ticket.
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
    user_id    text NOT NULL DEFAULT '',
    remote_id  text NOT NULL DEFAULT '',
    CHECK (ticket_id IS NULL OR run_id IS NULL),   -- exactly one owner
    CHECK (ping_id IS NULL OR run_id IS NOT NULL)  -- an answer file names its run
);
CREATE INDEX attachments_ticket_idx ON attachments (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX attachments_run_idx    ON attachments (run_id)    WHERE run_id IS NOT NULL;
CREATE INDEX attachments_ping_idx   ON attachments (ping_id)   WHERE ping_id IS NOT NULL;
CREATE INDEX attachments_orphan_idx ON attachments (created_at)
    WHERE ticket_id IS NULL AND run_id IS NULL;
CREATE INDEX attachments_user_idx ON attachments (user_id);
CREATE UNIQUE INDEX attachments_remote_idx
    ON attachments (ticket_id, remote_id) WHERE remote_id <> '';

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

-- The Ask pane: a run's read-only session beside its stages, answering
-- questions about the code. Its conversation is not the run's story (no run
-- events): one thread per run — the harness session it continues and the
-- model it runs on — and its messages, in order.
CREATE TABLE ask_threads (
    run_id     text PRIMARY KEY,
    session_id text NOT NULL DEFAULT '',
    model      text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ask_messages (
    run_id   text NOT NULL,
    seq      bigint NOT NULL,
    id       text NOT NULL,
    role     text NOT NULL CHECK (role IN ('question', 'answer')),
    text     text NOT NULL,
    user_id  text NOT NULL DEFAULT '',
    model    text NOT NULL DEFAULT '',
    cost_usd double precision NOT NULL DEFAULT 0,
    turns    integer NOT NULL DEFAULT 0,
    error    text NOT NULL DEFAULT '',
    at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, seq)
);
