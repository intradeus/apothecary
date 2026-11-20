-- The assistant (docs/assistant-plan.md): a person's sessions, each about
-- one subject of one kind (a flow, a ticket, a skill), their messages in
-- order, and each person's standing choice of harness integration and model.

CREATE TABLE assistant_sessions (
  id              text PRIMARY KEY,
  user_id         text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('flow', 'ticket', 'skill')),
  workshop_id     text NOT NULL DEFAULT '',
  subject         text NOT NULL,
  harness_session text NOT NULL DEFAULT '',
  integration_id  text NOT NULL DEFAULT '',
  model           text NOT NULL DEFAULT '',
  token_id        text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX assistant_sessions_subject ON assistant_sessions (user_id, kind, workshop_id, subject, updated_at DESC);

CREATE TABLE assistant_messages (
  session_id text NOT NULL REFERENCES assistant_sessions (id) ON DELETE CASCADE,
  seq        bigint NOT NULL,
  role       text NOT NULL CHECK (role IN ('person', 'assistant')),
  text       text NOT NULL DEFAULT '',
  fill       jsonb,
  error      text NOT NULL DEFAULT '',
  cost_usd   double precision NOT NULL DEFAULT 0,
  at         timestamptz NOT NULL,
  PRIMARY KEY (session_id, seq)
);

CREATE TABLE assistant_choices (
  user_id        text PRIMARY KEY,
  integration_id text NOT NULL DEFAULT '',
  model          text NOT NULL DEFAULT '',
  updated_at     timestamptz NOT NULL DEFAULT now()
);
