-- A choice ping (answer_kind choice: the impasse) lists the answers it takes,
-- [{id, note_required}], in the order shown; the inbox draws a button for
-- each. Nothing to backfill: no ping raised before this column was a choice.
ALTER TABLE pings ADD COLUMN choices jsonb;
