-- Tickets that follow their tracker (2026-11-01, TODO 49,
-- docs/ticket-sync-plan.md): every ticket has exactly one tracker — its
-- remote issue when it was imported (source_id set), otherwise Apothecary's
-- own internal tracker — and the internal ticket only ever reads from it. An
-- imported ticket is a MIRROR of its issue (a sync overwrites title, body,
-- status, type, labels and comments); a sourceless one is written in place.
--
-- Statuses and types live on the ticketsource integration, one list each:
-- fetched from the remote (and extended by sync) for a remote tracker, edited
-- by the user for the internal one. Each user has one built-in ticketsource
-- integration of kind `internal`, created here for existing users and lazily
-- by the config service for new ones (confstore.Service.InternalTracker).
--
-- Re-runnable: every statement is IF NOT EXISTS / ON CONFLICT DO NOTHING or
-- idempotent. Jira's free-text status_map / type_map config is left in place:
-- their rows need the tracker's ids, so the first list refresh applies them
-- by name and drops the keys (tickets.SyncLists, legacymaps.go).

-- --- the lists ---------------------------------------------------------------

-- A tracker's statuses. remote_id is the tracker's id for the status (Jira's
-- status id; GitHub "open"/"closed"; GitLab "opened"/"closed"; the row's own
-- id on the internal tracker), so a rename follows the id. is_done ("the work
-- is over": the poll skips tickets in it) and flow_step (which status
-- `ticket_transition to: <step>` moves to, at most one per step) are the two
-- columns the user sets.
CREATE TABLE IF NOT EXISTS tracker_statuses (
    id              text PRIMARY KEY,
    integration_id  text NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    remote_id       text NOT NULL,
    name            text NOT NULL,
    remote_category text NOT NULL DEFAULT '',   -- new|indeterminate|done|'' (unknown)
    position        integer NOT NULL DEFAULT 0,
    is_done         boolean NOT NULL DEFAULT false,
    flow_step       text,                       -- todo|in_progress|in_review|done|blocked, or none
    UNIQUE (integration_id, remote_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS tracker_statuses_step_idx
    ON tracker_statuses (integration_id, flow_step) WHERE flow_step IS NOT NULL;

-- A tracker's issue types, each mapped onto one change type (feat, fix, …):
-- what a ticket of that type is, and what its branch is named after.
CREATE TABLE IF NOT EXISTS tracker_types (
    id             text PRIMARY KEY,
    integration_id text NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    remote_id      text NOT NULL,
    name           text NOT NULL,
    change_type    text NOT NULL DEFAULT 'feat',
    position       integer NOT NULL DEFAULT 0,
    UNIQUE (integration_id, remote_id)
);

-- One internal tracker per user: what the get-or-create relies on.
CREATE UNIQUE INDEX IF NOT EXISTS integrations_internal_idx
    ON integrations (user_id) WHERE kind = 'internal';

-- --- the ticket's state ------------------------------------------------------

-- status_id / type_id belong to the ticket's tracker (the internal one when
-- source_id is empty); NULL on an imported ticket until its first sync.
-- change_type stays a column, derived from the type, so run_if, branch names
-- and the list need no join. remote_id is the issue's stable id (a Jira key
-- changes on a project move); remote_updated_at guards the sync against
-- applying an older snapshot; sync_state is '' (never synced), ok, error or
-- gone (the issue no longer exists: polling stops). status_changed_by says
-- who moved it: sync, run:<id>/<stage>, or user.
ALTER TABLE tickets
    ADD COLUMN IF NOT EXISTS status_id         text REFERENCES tracker_statuses(id),
    ADD COLUMN IF NOT EXISTS type_id           text REFERENCES tracker_types(id),
    ADD COLUMN IF NOT EXISTS labels            text[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS remote_id         text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS remote_updated_at timestamptz,
    ADD COLUMN IF NOT EXISTS synced_at         timestamptz,
    ADD COLUMN IF NOT EXISTS sync_state        text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS sync_error        text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS status_changed_at timestamptz,
    ADD COLUMN IF NOT EXISTS status_changed_by text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS tickets_status_idx ON tickets (status_id);
CREATE INDEX IF NOT EXISTS tickets_type_idx ON tickets (type_id);
CREATE INDEX IF NOT EXISTS tickets_sync_idx ON tickets (source_id) WHERE source_id <> '';

-- A ticket's comments: the remote's mirrored (origin remote, keyed by the
-- tracker's comment id), a person's typed in Apothecary (user), and a run's
-- (apothecary, keyed by the run and the stage occurrence — the internal
-- tracker's idempotency key, in place of the trailer a remote comment
-- carries).
CREATE TABLE IF NOT EXISTS ticket_comments (
    id         text PRIMARY KEY,
    ticket_id  text NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    remote_id  text NOT NULL DEFAULT '',
    author     text NOT NULL DEFAULT '',
    body_md    text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    origin     text NOT NULL CHECK (origin IN ('remote', 'user', 'apothecary')),
    run_id     text NOT NULL DEFAULT '',
    stage_id   text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ticket_comments_ticket_idx ON ticket_comments (ticket_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS ticket_comments_remote_idx
    ON ticket_comments (ticket_id, remote_id) WHERE remote_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS ticket_comments_run_idx
    ON ticket_comments (ticket_id, run_id, stage_id) WHERE run_id <> '';

-- Links on an internal ticket (ticket_link / ticket_link_branch): upserted on
-- URL.
CREATE TABLE IF NOT EXISTS ticket_links (
    ticket_id text NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    url       text NOT NULL,
    title     text NOT NULL DEFAULT '',
    PRIMARY KEY (ticket_id, url)
);

-- An imported attachment remembers the tracker's id for it, so the sync
-- copies only the files the issue gained since (tickets.Syncer). '' for an
-- upload. One copy per file per ticket; unclaimed rows (ticket_id NULL) are
-- not constrained until the claim. A file deleted on the tracker stays: runs
-- pin attachment ids at start.
ALTER TABLE attachments
    ADD COLUMN IF NOT EXISTS remote_id text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS attachments_remote_idx
    ON attachments (ticket_id, remote_id) WHERE remote_id <> '';

-- --- one copy of an issue per user and tracker -------------------------------

-- Replaces the import path's scan over every ticket. Existing duplicates are
-- refused loudly — a person decides which copy to keep — rather than dropped.
DO $$
DECLARE dups text;
BEGIN
    SELECT string_agg(format('%s/%s (%s)', source_id, lower(source_key), ids), '; ')
      INTO dups
      FROM (SELECT user_id, source_id, lower(source_key) AS source_key,
                   string_agg(id, ', ' ORDER BY created_at) AS ids
              FROM tickets
             WHERE source_id <> ''
             GROUP BY user_id, source_id, lower(source_key)
            HAVING count(*) > 1) d;
    IF dups IS NOT NULL THEN
        RAISE EXCEPTION 'tickets: the same issue was imported more than once; delete all but one copy of each and restart: %', dups;
    END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS tickets_source_key_idx
    ON tickets (user_id, source_id, lower(source_key)) WHERE source_id <> '';
-- A merge request's ticket (no source_id) is found by its URL in its project.
DROP INDEX IF EXISTS tickets_source_idx;
CREATE INDEX IF NOT EXISTS tickets_project_key_idx
    ON tickets (project_id, lower(source_key)) WHERE source_key <> '';

-- --- every existing user's internal tracker ----------------------------------

-- Every account, plus the owner of any sourceless ticket (rows made before
-- accounts existed are owned by ''). Ids are derived, so a re-run finds them.
-- secrets '' is the sealed form of "no secrets".
INSERT INTO integrations (id, port, kind, name, config, secrets, created_at, updated_at, user_id)
SELECT 'internal-' || u.user_id, 'ticketsource', 'internal', 'Apothecary tickets', '{}', '', now(), now(), u.user_id
  FROM (SELECT id AS user_id FROM users
        UNION
        SELECT DISTINCT user_id FROM tickets WHERE source_id = '') u
 WHERE NOT EXISTS (SELECT 1 FROM integrations i WHERE i.user_id = u.user_id AND i.kind = 'internal')
ON CONFLICT DO NOTHING;

-- Its statuses (remote_id = id on the internal tracker) and one type per
-- change type — the seed confstore.InternalStatusSeed / InternalTypeSeed
-- give a tracker created later.
INSERT INTO tracker_statuses (id, integration_id, remote_id, name, remote_category, position, is_done, flow_step)
SELECT i.id || '-' || s.step, i.id, i.id || '-' || s.step, s.name, s.category, s.position, s.is_done, s.step
  FROM integrations i
 CROSS JOIN (VALUES
        ('todo',        'To do',       'new',           0, false),
        ('in_progress', 'In progress', 'indeterminate', 1, false),
        ('in_review',   'In review',   'indeterminate', 2, false),
        ('done',        'Done',        'done',          3, true),
        ('blocked',     'Blocked',     'indeterminate', 4, false)
     ) AS s(step, name, category, position, is_done)
 WHERE i.kind = 'internal'
   AND NOT EXISTS (SELECT 1 FROM tracker_statuses x WHERE x.integration_id = i.id)
ON CONFLICT DO NOTHING;

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
   AND NOT EXISTS (SELECT 1 FROM tracker_types x WHERE x.integration_id = i.id)
ON CONFLICT DO NOTHING;

-- Sourceless tickets (hand-written and merge-request tickets) start in To do,
-- typed by their change type. Imported tickets stay NULL until their first
-- sync.
UPDATE tickets t
   SET status_id = s.id
  FROM integrations i
  JOIN tracker_statuses s ON s.integration_id = i.id AND s.flow_step = 'todo'
 WHERE t.source_id = '' AND t.status_id IS NULL
   AND i.kind = 'internal' AND i.user_id = t.user_id;

UPDATE tickets t
   SET type_id = y.id
  FROM integrations i
  JOIN tracker_types y ON y.integration_id = i.id
 WHERE t.source_id = '' AND t.type_id IS NULL
   AND i.kind = 'internal' AND i.user_id = t.user_id
   AND y.change_type = t.change_type;

-- --- Jira's status and type maps --------------------------------------------

-- Nothing here: the integration's status_map / type_map stay in its config
-- until the first metadata fetch applies them to the lists (by name — the
-- rows hold the tracker's ids, unknown until then) and drops them.
