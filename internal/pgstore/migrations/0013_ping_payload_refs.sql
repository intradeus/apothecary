-- ask_human can now attach MANY workspace files (was a single payload_ref).
-- The full list rides here; payload_ref stays for old rows and for the
-- PRIMARY ref (first, or a URL) that drives the review-vs-link-out decision.
-- Backfill: apothecaryd rebuild-projections.
ALTER TABLE pings ADD COLUMN payload_refs jsonb;
