-- Questions are forms (2026-10-27): ACP's elicitation/create shape — a list
-- of fields, answered accept + content or decline + note — replaces the
-- choice / multi / text answer kinds and their options. Unreleased, so the
-- log is rewritten rather than read both ways; applying the migration
-- rebuilds the pings projection from it. A no-op on a fresh database and on
-- a second run.
ALTER TABLE pings ADD COLUMN IF NOT EXISTS form    jsonb;
ALTER TABLE pings ADD COLUMN IF NOT EXISTS content jsonb;
ALTER TABLE pings DROP COLUMN IF EXISTS options;

-- What each old question asked: its kind (inferred, as the engine did, where
-- the event predates answer_kind), its options as {id, label, detail} (a
-- bare string once meant id = label), and the id its one field takes — the
-- thread selection's "threads", every agent question's "answer".
CREATE TEMP TABLE old_questions ON COMMIT DROP AS
SELECT run_id, seq, payload->>'ping_id' AS ping_id,
       CASE
         WHEN coalesce(payload->>'answer_kind', '') <> '' THEN payload->>'answer_kind'
         WHEN payload->>'type' = 'select' THEN 'multi'
         WHEN jsonb_array_length(coalesce(payload->'options', '[]'::jsonb)) > 0 THEN 'choice'
         ELSE 'text'
       END AS kind,
       CASE WHEN payload->>'type' = 'select' THEN 'threads' ELSE 'answer' END AS field_id,
       coalesce((SELECT jsonb_agg(CASE WHEN jsonb_typeof(o) = 'string'
                                       THEN jsonb_build_object('id', o, 'label', o)
                                       ELSE o END)
                   FROM jsonb_array_elements(coalesce(payload->'options', '[]'::jsonb)) o),
                '[]'::jsonb) AS options
  FROM events
 WHERE type = 'ping_raised'
   AND payload->>'type' NOT IN ('gate', 'phase_confirm', 'debug')
   AND coalesce(payload->>'answer_kind', '') NOT IN ('yes_no', 'form');

-- The answers first, while the questions still say what they were.
UPDATE events e
   SET payload = (e.payload - 'answer' - 'feedback' - 'content') || CASE
     -- "other": none of the options fit, and the note was the answer.
     WHEN q.kind = 'choice' AND e.payload->>'answer' = 'other' THEN
       jsonb_build_object('answer', 'decline', 'feedback', coalesce(e.payload->>'feedback', ''))
     WHEN q.kind = 'choice' THEN
       jsonb_build_object('answer', 'accept', 'feedback', coalesce(e.payload->>'feedback', ''),
                          'content', jsonb_build_object(q.field_id, e.payload->>'answer'))
     WHEN q.kind = 'multi' THEN
       jsonb_build_object('answer', 'accept', 'feedback', coalesce(e.payload->>'feedback', ''),
                          'content', jsonb_build_object(q.field_id, coalesce(
                            (SELECT jsonb_agg(btrim(l))
                               FROM regexp_split_to_table(e.payload->>'answer', E'\n') l
                              WHERE btrim(l) NOT IN ('', 'none')),
                            '[]'::jsonb)))
     -- Free text was recorded as answer:"text" with the words in feedback.
     ELSE
       jsonb_build_object('answer', 'accept', 'feedback', '',
                          'content', jsonb_build_object(q.field_id,
                            CASE WHEN e.payload->>'answer' = 'text' THEN e.payload->>'feedback'
                                 ELSE e.payload->>'answer' END))
   END
  FROM old_questions q
 WHERE e.type = 'ping_answered'
   AND e.run_id = q.run_id
   AND e.payload->>'ping_id' = q.ping_id
   AND e.payload->>'answer' NOT IN ('accept', 'decline');

UPDATE events e
   SET payload = (e.payload - 'options' - 'answer_kind') || jsonb_build_object(
     'answer_kind', 'form',
     'form', jsonb_build_object('fields', jsonb_build_array(
       jsonb_strip_nulls(jsonb_build_object(
         'id', q.field_id,
         'type', CASE q.kind WHEN 'multi' THEN 'array' ELSE 'string' END,
         'required', CASE WHEN q.kind = 'multi' THEN NULL ELSE true END,
         'options', CASE WHEN q.kind = 'text' THEN NULL ELSE q.options END,
         -- The thread selection starts with every thread picked.
         'default', CASE WHEN q.kind = 'multi'
                         THEN (SELECT jsonb_agg(o->'id') FROM jsonb_array_elements(q.options) o)
                    END)))))
  FROM old_questions q
 WHERE e.type = 'ping_raised'
   AND e.run_id = q.run_id
   AND e.seq = q.seq;
