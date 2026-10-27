-- Users (2026-10-27, TODO 25): accounts, the ways each proves who they are,
-- browser sessions and personal tokens — and an owner on every row a person
-- configures. There is no organisation level: a user owns their
-- integrations, projects (and so their flows, automations and secrets),
-- tickets, runs, pings, channels, attachments and settings, and sees nothing
-- of anyone else's.
--
-- New installs only: nothing here claims rows made before accounts existed.
-- (The one database configured under the single-operator release was
-- assigned to its owner by hand, once.)

CREATE TABLE IF NOT EXISTS users (
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
CREATE UNIQUE INDEX IF NOT EXISTS users_handle_idx ON users (lower(handle));
CREATE UNIQUE INDEX IF NOT EXISTS users_email_idx  ON users (lower(email));

-- One row per way a user can authenticate: kind = password today; oauth,
-- passkey later as more rows of the same shape. secret is the verifier's
-- material (a password hash, a passkey public key); subject the identity on
-- the other side (OAuth issuer+subject, a credential id), unique per kind
-- when set. A user has at most one password.
CREATE TABLE IF NOT EXISTS auth_methods (
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
CREATE INDEX IF NOT EXISTS auth_methods_user_idx ON auth_methods (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS auth_methods_subject_idx ON auth_methods (kind, subject) WHERE subject <> '';
CREATE UNIQUE INDEX IF NOT EXISTS auth_methods_password_idx ON auth_methods (user_id) WHERE kind = 'password';

-- id is the sha256 of the cookie value; the value itself lives only in the
-- browser.
CREATE TABLE IF NOT EXISTS sessions (
    id           text PRIMARY KEY,
    user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    user_agent   text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id);

-- Personal API tokens: hash of the secret, a display prefix.
CREATE TABLE IF NOT EXISTS api_tokens (
    id           text PRIMARY KEY,
    user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         text NOT NULL,
    hash         text NOT NULL UNIQUE,
    prefix       text NOT NULL,
    created_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    expires_at   timestamptz
);
CREATE INDEX IF NOT EXISTS api_tokens_user_idx ON api_tokens (user_id);

-- --- ownership -------------------------------------------------------------

-- Names are unique per owner now, not per instance: two people may each
-- have a project called "api".
ALTER TABLE integrations ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT '';
ALTER TABLE integrations DROP CONSTRAINT IF EXISTS integrations_port_name_key;
CREATE UNIQUE INDEX IF NOT EXISTS integrations_owner_port_name_idx ON integrations (user_id, port, name);

ALTER TABLE projects ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT '';
ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_name_key;
CREATE UNIQUE INDEX IF NOT EXISTS projects_owner_name_idx ON projects (user_id, name);

-- A ticket's owner is its project's (kept on the row so the list needs no
-- join).
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS tickets_user_idx ON tickets (user_id, created_at DESC);

ALTER TABLE channels ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT '';

-- An upload's owner is whoever uploaded it, from the moment it lands and
-- before anything claims it.
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS attachments_user_idx ON attachments (user_id);

-- Projections: run_created carries user_id (the project's owner at
-- creation); the runs and pings rows carry it for the per-user lists.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS runs_user_idx ON runs (user_id, created_at DESC);
ALTER TABLE pings ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS pings_user_status_idx ON pings (user_id, status);

-- Settings are per user (reaper, retention). Limits left the table for the
-- deployment env (APOTHECARY_MAX_CONCURRENT_RUNS, APOTHECARY_MAX_ENVIRONMENTS).
ALTER TABLE settings ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT '';
ALTER TABLE settings DROP CONSTRAINT IF EXISTS settings_pkey;
ALTER TABLE settings ADD PRIMARY KEY (user_id, key);
