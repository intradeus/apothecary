-- The provisioner is the instance's, not a person's: one row with no owner
-- (user_id ''), edited by an admin in System → Provisioner, that every
-- workshop builds on. projects.provisioner_id stays, so a workshop could
-- name another later. The one kept is the instance's own if it has one
-- (a -dev seed), else the oldest; every workshop moves onto it, and the
-- people's own copies go.

UPDATE projects SET provisioner_id = (
    SELECT id FROM integrations WHERE port = 'provisioner'
    ORDER BY (user_id <> ''), created_at, id LIMIT 1)
WHERE provisioner_id <> (
    SELECT id FROM integrations WHERE port = 'provisioner'
    ORDER BY (user_id <> ''), created_at, id LIMIT 1);

DELETE FROM integrations WHERE port = 'provisioner' AND id <> (
    SELECT id FROM integrations WHERE port = 'provisioner'
    ORDER BY (user_id <> ''), created_at, id LIMIT 1);

UPDATE integrations SET user_id = '' WHERE port = 'provisioner';
