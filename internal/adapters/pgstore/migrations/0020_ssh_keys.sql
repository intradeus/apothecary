-- SSH keys (2026-10-30, TODO 46): the public keys a person registers to
-- reach their runs' workspaces through the engine's SSH door — like
-- personal tokens, owned by one user and gone with them. The fingerprint is
-- unique across the instance: it is what the door looks an offered key up
-- by. runs_env_idx serves the door's other lookup — the live run behind a
-- workspace name (env_id is cleared when the workspace is destroyed, so the
-- partial index holds only live ones).
CREATE TABLE IF NOT EXISTS ssh_keys (
    id           text PRIMARY KEY,
    user_id      text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         text NOT NULL,
    public_key   text NOT NULL,
    fingerprint  text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL,
    last_used_at timestamptz
);
CREATE INDEX IF NOT EXISTS ssh_keys_user_idx ON ssh_keys (user_id);
CREATE INDEX IF NOT EXISTS runs_env_idx ON runs (env_id) WHERE env_id <> '';
