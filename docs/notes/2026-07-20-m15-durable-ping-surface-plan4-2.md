# M15 — Durable ping surface (PLAN4 §2)

The request/response pairs the projection always stored are now the API.
As designed, with these notes:

- `ping_raised` gains `stage_id` (both raise sites: gates stamp their
  stage; ask_human pings stamp the agent stage in flight). Migration 0007
  adds the column; `rebuild-projections` backfills historical rows from
  events (pre-M15 events project as "").
- `run.PingView` + `ReducePings` are the shared projection shape: pgstore
  serves it from SQL (`ListPings(status, limit)` newest-first capped 500,
  `GetPing`), and `httpapi.EventPingStore` derives the identical view by
  folding events — Mem-backed tests and the registry-less path use it, so
  SQL and fold can never drift semantically. The old `PendingPings`
  map-shaped reader is deleted.
- `GET /api/pings` now takes `status=pending|answered|all` (+`limit`);
  pending stays oldest-first (inbox order), history newest-first.
  `GET /api/pings/{id}` returns the full pair. The engine's in-memory
  registry remains the ANSWERING path (and the fallback when no
  projection is wired — bare test servers); answered pings no longer
  vanish when their run ends or the daemon restarts.
- Inbox: Pending | Answered tabs; question/context/feedback render as
  markdown everywhere (same escaped-HTML posture as plans). The review
  page loads from the projection: pending → the M14 workbench, answered →
  a read-only record of the exchange.
- Run detail already showed Q&A in its timeline (unchanged).
- Live smoke (scratch PG): pending ping carried `stage_id=plan_gate`;
  reject stored answer+feedback+answered_at; history listed and survived
  the run completing (all 3 gates queryable after run end).
