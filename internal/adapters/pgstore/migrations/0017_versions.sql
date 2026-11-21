-- History of tickets, dashboards and scorecards: each save that changes what
-- a person edits appends an immutable, numbered version (seq per object,
-- never reused), next to who made it. The parent row stays the object
-- itself: nothing references a version. A version goes with its parent
-- (ON DELETE CASCADE); the store keeps the newest 100 of each. author_kind:
-- user (a person), assistant (an assistant's fill or a confirmed assistant
-- request, saved by the person in author_id), run (author_id: the run),
-- sync (author_id: the ticket source integration) or backfill (the content
-- as it stood when history began). restored_from is the seq a restore
-- copied.

CREATE TABLE ticket_versions (
    ticket_id     text NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    seq           integer NOT NULL,
    title         text NOT NULL,
    goal          text NOT NULL,
    base_branch   text NOT NULL,
    change_type   text NOT NULL,
    -- No FK: an old version never holds a tracker type in place.
    type_id       text NOT NULL DEFAULT '',
    labels        text[] NOT NULL DEFAULT '{}',
    author_kind   text NOT NULL CHECK (author_kind IN ('user', 'assistant', 'run', 'sync', 'backfill')),
    author_id     text NOT NULL DEFAULT '',
    restored_from integer,
    created_at    timestamptz NOT NULL,
    PRIMARY KEY (ticket_id, seq)
);

CREATE TABLE analytics_dashboard_versions (
    dashboard_id  text NOT NULL REFERENCES analytics_dashboards(id) ON DELETE CASCADE,
    seq           integer NOT NULL,
    name          text NOT NULL,
    filters       jsonb NOT NULL,
    panels        jsonb NOT NULL,
    author_kind   text NOT NULL CHECK (author_kind IN ('user', 'assistant', 'backfill')),
    author_id     text NOT NULL DEFAULT '',
    restored_from integer,
    created_at    timestamptz NOT NULL,
    PRIMARY KEY (dashboard_id, seq)
);

CREATE TABLE analytics_scorecard_versions (
    scorecard_id  text NOT NULL REFERENCES analytics_scorecards(id) ON DELETE CASCADE,
    seq           integer NOT NULL,
    name          text NOT NULL,
    terms         jsonb NOT NULL,
    author_kind   text NOT NULL CHECK (author_kind IN ('user', 'assistant', 'backfill')),
    author_id     text NOT NULL DEFAULT '',
    restored_from integer,
    created_at    timestamptz NOT NULL,
    PRIMARY KEY (scorecard_id, seq)
);

-- History begins with what is there now.
INSERT INTO ticket_versions (ticket_id, seq, title, goal, base_branch, change_type, type_id, labels,
                             author_kind, author_id, created_at)
SELECT id, 1, title, goal, base_branch, change_type, COALESCE(type_id, ''), labels, 'backfill', '', updated_at
  FROM tickets;

INSERT INTO analytics_dashboard_versions (dashboard_id, seq, name, filters, panels, author_kind, author_id, created_at)
SELECT id, 1, name, filters, panels, 'backfill', '', updated_at
  FROM analytics_dashboards;

INSERT INTO analytics_scorecard_versions (scorecard_id, seq, name, terms, author_kind, author_id, created_at)
SELECT id, 1, name, terms, 'backfill', '', updated_at
  FROM analytics_scorecards;
