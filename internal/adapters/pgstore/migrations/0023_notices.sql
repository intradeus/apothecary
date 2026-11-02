-- Notices (2026-11-02, TODO 51): setup problems a person must act on — a
-- login to sign in again, a tracker that cannot be read, a failing channel,
-- an image that will not build. Owned by one user and gone with them; rows
-- are never pruned, only dismissed. notices_open_key is the dedupe key: while
-- a keyed notice is uncleared a repeat bumps it instead of adding a row, and
-- clearing it re-arms the key.
CREATE TABLE IF NOT EXISTS notices (
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
CREATE UNIQUE INDEX IF NOT EXISTS notices_open_key ON notices (user_id, key) WHERE key <> '' AND cleared_at IS NULL;
CREATE INDEX IF NOT EXISTS notices_user_idx ON notices (user_id, last_seen_at DESC) WHERE dismissed_at IS NULL;
