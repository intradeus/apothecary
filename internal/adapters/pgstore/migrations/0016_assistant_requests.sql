-- The assistant on more pages (a run, a dashboard, and none: the command
-- palette's), and its requests: the acts an answer asks the person to
-- confirm, with what came of each once they did.

ALTER TABLE assistant_sessions DROP CONSTRAINT assistant_sessions_kind_check;
ALTER TABLE assistant_sessions ADD CONSTRAINT assistant_sessions_kind_check
  CHECK (kind IN ('flow', 'ticket', 'skill', 'run', 'dashboard', 'general'));

ALTER TABLE assistant_messages ADD COLUMN request jsonb;
