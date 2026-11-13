# Ask: several sessions per run (2026-11-13)

- **What:** a run's Ask pane holds any number of sessions, picked in a
  dropdown at the top right of the pane, with a "New session" button beside
  it. Each session has its own harness session id (whatever the `ask:`
  harness reports), its own model and its own messages. The owner chose
  busy per session, model per session, and existing conversations migrated.
- **Naming:** the engine, the database and the API keep *thread* for the
  stored conversation (`ask.Thread.ID`, `thread_id`); a thread's
  `session_id` stays the harness session it continues. The UI calls a
  thread a session, and the mapper translates.
- **Migration 0003_ask_threads:** `ask_threads` is keyed by
  `(run_id, id)` and gains `created_at`; `ask_messages` gains a NOT NULL
  `thread_id`. Every existing thread becomes its run's first, id `main`,
  created at its first message (else its last save). A run with messages
  but no thread row (a first answer that failed before the thread was
  saved) gets one too. `TestAskThreadsMigration` upgrades both shapes from
  0002.
- **seq stays per run:** messages are numbered across all of a run's
  threads, so the stream's `after_seq`/`Last-Event-ID` resume and
  `ask_cost_usd` stay per run. Each message names its thread, and every
  stream frame carries `thread_id`; the UI paints a frame into its own
  session only, and keeps each session's answer in progress apart.
- **Busy per thread, appends serialized per run:** two threads may answer
  at once, so the store's `MAX(seq)+1` would race into a primary-key
  conflict. The service holds one append lock per run across its threads.
  That covers one daemon process only, as the in-memory busy state and the
  stream hub already assume; a replicated daemon would need an advisory
  lock or a retry on unique violation.
- **No thread named:** an ask or a model pick without `thread_id` goes to
  the run's latest thread, opening the first when the run has none (under
  a per-run lock, so two first questions open one). The fresh-run path
  needs no separate create call. `POST /api/runs/{id}/ask/threads` opens a
  thread explicitly: no messages, the latest thread's model, its harness
  session on its first answer. An unknown `thread_id` is a 404.
- **`GET /ask` lost its top-level `model` and `busy`:** each thread in
  `threads` carries its own. With no thread yet, the pane shows no model
  until the first question or pick opens one. The terminal client does not
  use Ask.
- **Label:** a session reads as its first question, with its creation time;
  no title is stored.
- **Residual:** there is no cap on sessions answering at once; they share
  the workspace's agent process (omp: its read-only process).
