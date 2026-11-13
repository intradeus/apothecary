# Postgres store (§0, §6, §9)

- **Watch fan-out is in-process** (no LISTEN/NOTIFY): the engine is the only
  writer (§2) and v1 is one apothecaryd per machine, so subscribers are notified
  after commit in-memory; replay comes from the table with seq-based dedup
  against the live buffer. Revisit only with multi-process engines.
- Seq assignment is `MAX(seq)+1` inside the append transaction — safe under
  the single-writer rule, no sequence object needed.
- The runs and pings projections are folded in the append transaction via
  the same `run.Apply` code the in-memory reducer uses; `apothecaryd
  rebuild-projections` truncates and replays through that exact path.
  Ping rows insert with `ON CONFLICT DO NOTHING` so rebuild is idempotent.
- Migrations: embedded numbered `.sql` files, tracked in
  `schema_migrations`, applied in a transaction each at Open.
- Store tests require a real Postgres via `TEST_POSTGRES_DSN` and skip
  otherwise (verified against an ephemeral local instance).
