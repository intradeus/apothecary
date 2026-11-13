# Lifecycle polish (TODO 17 retired, 2026-10-19)

The status machine itself was done (replay-based recovery, two axes: run
state vs reconciled env status). What was left was clunk, fixed in one pass:

- **One verb, one meaning, every layer.** `retry` = re-enter the failed
  stage in the same workspace (`POST /runs/{id}/retry`, `Engine.RetryRun`,
  event `run_retried`; UI "Retry <stage>"). `rerun` = the ticket again as a
  NEW run (`POST /runs/{id}/rerun`, `Engine.Rerun`; UI "New run from
  this…"). `pause`/`play` = park / continue (UI "Pause"/"Play"). `restart`
  now means only the engine PROCESS restarting (`stage_resumed{engine_restart}`).
  Stored `run_resumed` events decode to `RunRetried` via a codec alias and
  the UI timeline accepts both spellings; the envelope keeps the type it was
  written with.
- **Shutdown cancels every loop, then waits** — before, it waited on each in
  turn, and awaitAgent's 60s cancellation grace made that N×60s worst case.
- **Pollers and the list read the runs projection.** `run.ViewSource` is an
  optional store capability (pgstore has it; the MemStore does not, so the
  engine falls back to reducing every log — `Engine.views`). ReapOnce,
  ReconcileEnvsOnce, SampleResourcesOnce, Recover and `GET /api/runs` all go
  through it. The runs table gained `view` (the whole run.View as jsonb)
  plus `dormant` and `rebuilding` columns — the two fields `run.Apply` reads
  that the JSON omits (json:"-"); without them the incremental fold had
  disagreed with a full reduce (a parked run's answer projected as
  "running"). Folded into 0001_init.sql (unreleased): dev databases need a
  reset (`task dev:db:reset` / `task local:destroy`), or
  `apothecaryd rebuild-projections` after adding the columns by hand.
- **Interrupted rebuild closed on boot**: `run.View.Rebuilding`
  (env_rebuild_started with no end) makes Recover append
  `env_rebuild_failed{interrupted}`.
- **Command-stage orphans**: `ports.Command.Marker` (set per stage by the
  engine) makes the devcontainer adapter tag the process tree
  (`--remote-env APOTHECARY_CMD=<run>:<stage>`) and, before the command,
  sweep /proc for an earlier attempt's tree still running (TERM, then KILL).
  The engine's script is untouched — tests match scripts by prefix.
- **Config dirty hint** (a "dirty workspace"): the adapter records a
  digest of the build inputs (.devcontainer/**, root .devcontainer.json, the
  Dockerfile/compose files the config names) at provision/rebuild;
  `ports.WorkspaceDirtyChecker.ConfigDirty` compares. The reconciler keeps it
  in memory next to env status, `run.View.EnvDirty` carries it on the read
  path, and the UI marks the Rebuild button. A rebuild clears it at once.
