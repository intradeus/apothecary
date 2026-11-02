-- rebuild: projections
-- Curated review gates (2026-10-30, TODO 45): a gate over a review document
-- — the reviewer's notes before post_review posts them, a round's replies
-- before reply_review_comments — tells the page each shown file's port type
-- (file_types, a projection of ping_raised.file_types), and its answer
-- carries what the person made of the document (curated, a projection of
-- ping_answered.curated): which notes and replies are posted, and how they
-- read. Applying the migration rebuilds the pings projection from the log.
-- A no-op on a fresh database and on a second run.
ALTER TABLE pings ADD COLUMN IF NOT EXISTS file_types jsonb;
ALTER TABLE pings ADD COLUMN IF NOT EXISTS curated    jsonb;
