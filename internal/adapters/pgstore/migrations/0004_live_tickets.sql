-- Live tickets: one source per workshop, no sync. A workshop has exactly one
-- ticket source (projects.ticket_source_id): a remote tracker, read live, or
-- the owner's internal tracker. A remote issue's tickets row is a small
-- record created when something needs it (a run, an automation, a base
-- branch), unique by (source_id, remote_id) and owned by no workshop. The
-- mirror goes: remote statuses and comments, flow steps, sync state.

-- --- 1. every owner's internal tracker ---------------------------------------

-- The owner of every project and ticket gets one when missing (ids derived
-- from the owner, as the first migration made them); the second name is for
-- an owner who already called another tracker "Apothecary tickets", as
-- confstore.createInternal does. secrets '' is the sealed form of "no
-- secrets".
INSERT INTO integrations (id, port, kind, name, config, secrets, created_at, updated_at, user_id)
SELECT 'internal-' || o.user_id, 'ticketsource', 'internal',
       CASE WHEN EXISTS (SELECT 1 FROM integrations n
                          WHERE n.user_id = o.user_id AND n.port = 'ticketsource' AND n.name = 'Apothecary tickets')
            THEN 'Apothecary tickets (built in)' ELSE 'Apothecary tickets' END,
       '{}', '', now(), now(), o.user_id
  FROM (SELECT user_id FROM projects UNION SELECT user_id FROM tickets) o
 WHERE NOT EXISTS (SELECT 1 FROM integrations i WHERE i.user_id = o.user_id AND i.kind = 'internal');

-- An internal tracker with no lists gets the stock ones, with the ids
-- confstore.seedInternal derives (remote_id = id on the internal tracker).
INSERT INTO tracker_statuses (id, integration_id, remote_id, name, position, is_done)
SELECT i.id || '-' || s.suffix, i.id, i.id || '-' || s.suffix, s.name, s.position, s.is_done
  FROM integrations i
 CROSS JOIN (VALUES
        ('todo',        'To do',       0, false),
        ('in_progress', 'In progress', 1, false),
        ('in_review',   'In review',   2, false),
        ('done',        'Done',        3, true),
        ('blocked',     'Blocked',     4, false)
     ) AS s(suffix, name, position, is_done)
 WHERE i.kind = 'internal'
   AND NOT EXISTS (SELECT 1 FROM tracker_statuses x WHERE x.integration_id = i.id);

INSERT INTO tracker_types (id, integration_id, remote_id, name, change_type, position)
SELECT i.id || '-' || t.ct, i.id, i.id || '-' || t.ct, t.name, t.ct, t.position
  FROM integrations i
 CROSS JOIN (VALUES
        ('feat',   'Feature',     0),
        ('fix',    'Bug fix',     1),
        ('hotfix', 'Hotfix',      2),
        ('perf',   'Performance', 3),
        ('docs',   'Docs',        4),
        ('chore',  'Chore',       5)
     ) AS t(ct, name, position)
 WHERE i.kind = 'internal'
   AND NOT EXISTS (SELECT 1 FROM tracker_types x WHERE x.integration_id = i.id);

-- --- 2. each workshop's ticket source -----------------------------------------

-- The single remote tracker the workshop's imported tickets and tracker_query
-- automations name (one of the owner's that still exists), else the owner's
-- internal tracker: two trackers is ambiguous, none is the internal one.
ALTER TABLE projects ADD COLUMN ticket_source_id text;

UPDATE projects p SET ticket_source_id = s.source_id
  FROM (SELECT n.project_id, min(n.source_id) AS source_id
          FROM (SELECT project_id, source_id FROM tickets WHERE source_id <> ''
                UNION
                SELECT project_id, source_id FROM automations
                 WHERE kind = 'tracker_query' AND source_id <> '') n
          JOIN projects o ON o.id = n.project_id
          JOIN integrations i ON i.id = n.source_id AND i.user_id = o.user_id
                             AND i.port = 'ticketsource' AND i.kind <> 'internal'
         GROUP BY n.project_id
        HAVING count(DISTINCT n.source_id) = 1) s
 WHERE s.project_id = p.id;

UPDATE projects p SET ticket_source_id = i.id
  FROM integrations i
 WHERE p.ticket_source_id IS NULL AND i.user_id = p.user_id AND i.kind = 'internal';

ALTER TABLE projects
    ALTER COLUMN ticket_source_id SET NOT NULL,
    ADD CONSTRAINT projects_ticket_source_id_fkey
        FOREIGN KEY (ticket_source_id) REFERENCES integrations(id);

-- --- 3. tracker_query automations poll the workshop's source -----------------

-- One that polled another tracker would now run its query against the
-- workshop's: it is disabled, and says why, until someone checks it.
UPDATE automations a
   SET enabled = false,
       last_error = format('Disabled: this automation polled %s, but its workshop''s ticket source is %s. Check its query, then enable it again.',
                           COALESCE((SELECT name FROM integrations WHERE id = a.source_id), a.source_id),
                           COALESCE((SELECT name FROM integrations WHERE id = p.ticket_source_id), p.ticket_source_id))
  FROM projects p
 WHERE p.id = a.project_id AND a.kind = 'tracker_query'
   AND a.source_id <> '' AND a.source_id <> p.ticket_source_id;

ALTER TABLE automations DROP COLUMN source_id;

-- --- 4. sourceless tickets are the internal tracker's -------------------------

-- Hand-written and merge-request tickets: their id is their id on the
-- internal tracker. The old one-copy-per-key index goes first: a user's
-- hand-written tickets all share the empty key.
DROP INDEX tickets_source_key_idx;

UPDATE tickets t
   SET source_id = i.id, source_kind = 'internal', remote_id = t.id
  FROM integrations i
 WHERE t.source_id = '' AND i.user_id = t.user_id AND i.kind = 'internal';

-- A cron automation runs a pinned ticket, which must be one the workshop
-- shows: its owner's, from its ticket source. One pinned to a ticket of
-- another source (an ambiguous workshop sent to its internal tracker, say)
-- is disabled, and says why, until someone pins another.
UPDATE automations a
   SET enabled = false,
       last_error = format('Disabled: this automation runs ticket %s, which is from %s, but its workshop''s ticket source is %s. Pin a ticket of the workshop''s source, then enable it again.',
                           t.id,
                           COALESCE((SELECT name FROM integrations WHERE id = t.source_id), t.source_id),
                           COALESCE((SELECT name FROM integrations WHERE id = p.ticket_source_id), p.ticket_source_id))
  FROM projects p, tickets t
 WHERE p.id = a.project_id AND a.kind = 'cron' AND t.id = a.ticket_id
   AND (t.source_id <> p.ticket_source_id OR t.user_id <> p.user_id);

-- --- 5. remote tickets without a remote id ----------------------------------

-- Imported before remote_id was kept and never synced since: a placeholder
-- no issue ever matches, so their runs still resolve.
UPDATE tickets SET remote_id = 'legacy:' || id
 WHERE source_kind <> 'internal' AND remote_id = '';

-- --- 6. one record per issue -----------------------------------------------

-- The same issue imported by two users or workshops: the oldest record
-- stays the issue's, the others keep their runs and are never matched again.
UPDATE tickets t SET remote_id = 'dup:' || t.id
  FROM (SELECT id, row_number() OVER (PARTITION BY source_id, remote_id ORDER BY created_at, id) AS n
          FROM tickets) d
 WHERE d.id = t.id AND d.n > 1;

-- --- 7. statuses are read from the remote tracker ----------------------------

-- Only the internal tracker keeps status rows; a remote ticket's status is
-- read live. Flow steps are gone: ticket_transition names the status.
UPDATE tickets t SET status_id = NULL
 WHERE t.status_id IS NOT NULL
   AND (t.source_kind <> 'internal' OR NOT EXISTS (
        SELECT 1 FROM tracker_statuses s JOIN integrations i ON i.id = s.integration_id
         WHERE s.id = t.status_id AND i.kind = 'internal'));

DELETE FROM tracker_statuses s
 USING integrations i
 WHERE i.id = s.integration_id AND i.kind <> 'internal';

DROP INDEX tracker_statuses_step_idx;
ALTER TABLE tracker_statuses
    DROP COLUMN flow_step,
    DROP COLUMN remote_category;

-- --- 8. comments: a person's and a run's, on internal tickets ----------------

-- A remote ticket's comments are read from its tracker.
DELETE FROM ticket_comments c
 USING tickets t
 WHERE t.id = c.ticket_id AND t.source_kind <> 'internal';
DELETE FROM ticket_comments WHERE origin = 'remote';

DROP INDEX ticket_comments_remote_idx;
ALTER TABLE ticket_comments
    DROP CONSTRAINT ticket_comments_origin_check,
    ADD CONSTRAINT ticket_comments_origin_check CHECK (origin IN ('user', 'apothecary')),
    DROP COLUMN remote_id;

-- --- 9. tickets belong to their source, not to a workshop ---------------------

-- Tickets outlive the workshop that first ran them (runs point at them);
-- the sync's columns go with the sync.
DROP INDEX tickets_project_idx;
DROP INDEX tickets_project_key_idx;
DROP INDEX tickets_sync_idx;
ALTER TABLE tickets
    DROP COLUMN project_id,
    DROP COLUMN remote_updated_at,
    DROP COLUMN synced_at,
    DROP COLUMN sync_state,
    DROP COLUMN sync_error,
    ADD CONSTRAINT tickets_source_remote_key UNIQUE (source_id, remote_id);

-- A run started while the tracker is unreachable finds its record by the
-- key it was last seen under (several records may share one: not unique).
CREATE INDEX tickets_source_seen_key_idx ON tickets (source_id, source_key) WHERE source_key <> '';
