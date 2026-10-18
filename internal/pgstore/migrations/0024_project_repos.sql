-- Multi-repo tasks: a project may check out additional repositories beside
-- the primary one (the devcontainer's). Stored as a list next to `repo`,
-- which stays the primary, so single-repo projects read exactly as before.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS repos jsonb NOT NULL DEFAULT '[]'::jsonb;
