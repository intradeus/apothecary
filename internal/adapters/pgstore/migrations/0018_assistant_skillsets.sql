-- The assistant's skills: the skillsets a person chose for their assistant
-- (refs, no seq the latest), and the versions a session pinned when it
-- opened, installed on each of its turns.

ALTER TABLE assistant_choices ADD COLUMN skillsets jsonb NOT NULL DEFAULT '[]';
ALTER TABLE assistant_sessions ADD COLUMN skillsets jsonb NOT NULL DEFAULT '[]';
