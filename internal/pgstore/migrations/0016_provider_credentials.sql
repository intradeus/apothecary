-- Provider credentials (2026-10-28, docs/model-providers-plan.md): a user's
-- model credentials — an API key, or a login whose refresh token the engine
-- keeps and refreshes — referenced by harness integrations instead of each
-- harness integration carrying an opaque auth blob. Sealed by the config
-- service like every secret here; meta is what a listing shows without the
-- master key (expiry, last refresh, status, account label).
CREATE TABLE IF NOT EXISTS provider_credentials (
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
CREATE INDEX IF NOT EXISTS provider_credentials_owner_idx ON provider_credentials (user_id);

-- Harness integrations of the old shape held their credentials in their own
-- sealed blob; they are not carried over (unreleased — the owner set them up
-- again against provider credentials). Projects lose the references with them.
DELETE FROM integrations WHERE port = 'harness';
UPDATE projects SET harnesses = '[]'::jsonb;
