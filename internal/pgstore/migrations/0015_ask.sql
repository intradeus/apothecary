-- The Ask pane (2026-10-28): a run's read-only session beside its stages,
-- answering questions about the code. Its conversation is not the run's
-- story (no run events): one thread per run — the harness session it
-- continues and the model it runs on — and its messages, in order.

CREATE TABLE IF NOT EXISTS ask_threads (
    run_id     text PRIMARY KEY,
    session_id text NOT NULL DEFAULT '',
    model      text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ask_messages (
    run_id   text NOT NULL,
    seq      bigint NOT NULL,
    id       text NOT NULL,
    role     text NOT NULL CHECK (role IN ('question', 'answer')),
    text     text NOT NULL,
    user_id  text NOT NULL DEFAULT '',
    model    text NOT NULL DEFAULT '',
    cost_usd double precision NOT NULL DEFAULT 0,
    turns    integer NOT NULL DEFAULT 0,
    error    text NOT NULL DEFAULT '',
    at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, seq)
);
