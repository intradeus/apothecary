-- The Ask pane holds several sessions per run: ask_threads keeps one row per
-- thread (its own harness session and model), keyed by the run and the
-- thread's id, and each message names the thread it belongs to. seq stays
-- per run, across every thread.
--
-- What was there becomes each run's first thread, id 'main': its creation
-- is its first message's time (else its last save), and a run with messages
-- but no thread row (a first answer that failed before the thread was
-- saved) gets one.
ALTER TABLE ask_threads ADD COLUMN id text NOT NULL DEFAULT 'main' CHECK (id <> '');
ALTER TABLE ask_threads ALTER COLUMN id DROP DEFAULT;
ALTER TABLE ask_threads ADD COLUMN created_at timestamptz;
UPDATE ask_threads t SET created_at = COALESCE(
    (SELECT MIN(m.at) FROM ask_messages m WHERE m.run_id = t.run_id), t.updated_at);
ALTER TABLE ask_threads
    ALTER COLUMN created_at SET NOT NULL,
    ALTER COLUMN created_at SET DEFAULT now(),
    DROP CONSTRAINT ask_threads_pkey,
    ADD PRIMARY KEY (run_id, id);

INSERT INTO ask_threads (run_id, id, created_at, updated_at)
SELECT m.run_id, 'main', MIN(m.at), MAX(m.at)
FROM ask_messages m
WHERE NOT EXISTS (SELECT 1 FROM ask_threads t WHERE t.run_id = m.run_id)
GROUP BY m.run_id;

ALTER TABLE ask_messages ADD COLUMN thread_id text;
UPDATE ask_messages SET thread_id = 'main';
ALTER TABLE ask_messages
    ALTER COLUMN thread_id SET NOT NULL,
    ADD CONSTRAINT ask_messages_thread_id_check CHECK (thread_id <> '');
