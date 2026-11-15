# A long run's page opens without freezing (2026-11-15)

- **Why:** opening a long run took up to 10 s. The page drew its header,
  then froze while the event stream replayed the log. Each SSE message is
  its own browser task, so each replayed event committed on its own: a
  new copy of the log (`[...events, ev]`), every derivation over it again
  (totals, pings, the transcript, the timeline, the flow), a
  scroll-to-bottom, and for each historical `run_`/`ping_`/`env_`/`hold_`
  event a fresh `GET /api/runs/:id`. That is quadratic in the log's
  length. Each of those GETs read and reduced the whole log on the
  server too.
- **The run store batches the stream** (`cpui/src/lib/run/store.svelte.ts`).
  Messages, events and live deltas in arrival order, wait
  `STREAM_BATCH_MS` (16 ms, about a frame) and land in one `flush()`:
  one new log, one live state, and each refresh the batch calls for
  (run, fleet, record, services) asked once. `events` and `live` are
  `$state.raw`, because both are always replaced whole and a deep proxy
  re-wrapped every event on every read. Stopping the page drops a batch
  that has not landed.
  - Measured with `RunPage` mounted in vitest (jsdom), replaying a
    synthetic log with one reactive flush per message as the browser
    does: 200 events took 8 s, 400 took 28 s, 800 took 118 s before; 800
    take 2.5 s and 3000 take 7.4 s after (that is mostly rendering 3000
    rows into jsdom once).
- **`GET /api/runs/:id` reads the run's row, not its log.** It takes the
  view from the projection (`run.ViewLookup`, the row the ownership guard
  already reads) and the last 100 events from the new `event.Store.Tail`
  (Postgres: the primary key read backwards). The SSE route checks that
  the run exists the same way, before `Watch` replays the log. A store
  without rows (the in-memory one) still reduces the log. The event
  contract holds `Tail` on both stores.
