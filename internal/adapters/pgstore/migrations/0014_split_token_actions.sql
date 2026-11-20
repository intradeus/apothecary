-- run:operate and project:write were split (docs/notes, "finer actions"):
-- answering a run, its egress, a workshop's flows and automations, and its
-- secrets are actions of their own now. A token granted the old action
-- keeps everything it could do: it gains the actions split from it.

UPDATE api_tokens
   SET actions = actions || ARRAY['run:answer', 'run:egress']
 WHERE actions @> ARRAY['run:operate']
   AND NOT actions @> ARRAY['run:answer'];

UPDATE api_tokens
   SET actions = actions || ARRAY['project:author', 'project:secrets']
 WHERE actions @> ARRAY['project:write']
   AND NOT actions @> ARRAY['project:author'];
