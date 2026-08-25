-- DESIGN-FILES phase 2 cutover: the backend now runs on attachments +
-- snapshots (0021); this removes the old world and reshapes the pings
-- projection for the typed ping payload.
--
-- pings: payload_ref/payload_refs were a stringly projection ("is it a URL?"
-- was sniffed with HasPrefix). ping_raised now carries typed files + link,
-- and ping_answered carries the answer's attachment ids — the projection
-- mirrors the events. The table is TRUNCATE-rebuilt by rebuild-projections,
-- so old rows need no migration: re-run `apothecaryd rebuild-projections`
-- after upgrading if you care about pre-cutover pings (their events don't
-- replay either — the project is unreleased and dev DBs are disposable).
ALTER TABLE pings
    DROP COLUMN payload_ref,
    DROP COLUMN payload_refs,
    ADD COLUMN files jsonb,
    ADD COLUMN link text,
    ADD COLUMN answer_attachment_ids jsonb;

-- tickets: attachments.ticket_id (0021) is the single truth for the
-- ticket→attachment link; the jsonb id array goes.
ALTER TABLE tickets DROP COLUMN artifact_ids;

-- The fused store. Its two tenants live in attachments + snapshots now.
DROP TABLE artifacts;
