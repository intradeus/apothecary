-- A yes/no question's answer kind is "yes_no" (2026-10-26): "decision" was
-- the word for two things, and a judgement is now a verdict everywhere.
-- Unreleased, so the log is rewritten rather than read both ways: the
-- projection, and the ping_raised events it is rebuilt from. A no-op on a
-- fresh database and on a second run.
UPDATE pings SET answer_kind = 'yes_no' WHERE answer_kind = 'decision';

UPDATE events SET payload = jsonb_set(payload, '{answer_kind}', '"yes_no"')
 WHERE type = 'ping_raised' AND payload->>'answer_kind' = 'decision';
