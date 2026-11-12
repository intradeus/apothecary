-- A personal token may carry an inactivity window instead of (or beside) a
-- fixed expiry: refused once its last use — or its creation, never used — is
-- idle_seconds old, every use sliding it. The CLI's browser-approved sign-in
-- mints these. NULL: no window (every token made before, and the UI's).
ALTER TABLE api_tokens ADD COLUMN idle_seconds integer;
