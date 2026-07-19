# Implementation notes

Decisions made where DESIGN.md was genuinely unspecified (per its §0 rule:
boring choice, recorded here). Newest at the bottom.

## Flow parser (§3)

- **Slug charset** for flow names, stage ids, agent names and `run_ref` keys:
  `^[a-z][a-z0-9_-]*$`.
- **Feed/edge pairing is validated**, not just the enum: `feed: output` only on
  `on_fail`, `feed: comments` only on agent `on_reject`, `feed: feedback` only
  on human_gate `on_reject`. A mismatched pairing is meaningless, so it is a
  load-time error.
- **`on_reject` on an agent stage requires `verdict: true`** — without a
  verdict nothing can trigger the edge.
- **`timeout` override is agent-only** (per the §3 field table); command stages
  always use `defaults.timeout`.
- **Edge `max` is required** (no default); `to`/`max` missing are load errors.
- **Artifact paths** (`input` entries, `output`, `payload`, `body`) must be
  repo-root-relative: no leading `/`, no `.` / `..` segments.
- The generalized form of "`model` only on agent stages": every type-specific
  field is rejected on stage types it doesn't apply to (e.g. `on_fail` on a
  `human_gate` is an error).
- Line-precise errors report the stage's line in the YAML; unknown-field errors
  carry yaml.v3's own `line N:` text.

## Events & projections (§2, §6)

- **Added `run_archived{}` to the event catalog.** §2 lists `archived` as a
  state and §6 requires projections to be rebuildable from events alone, but
  the §6 catalog has no event producing that state — one or the other had to
  give. Empty payload; emitted by the archive API / reaper.
- **Event payloads are pointers by construction**: `EventType()` uses pointer
  receivers so only `*event.X` satisfies `Payload`. Keeps the projection's
  type switch and the codec's registry in agreement.
- **Store carries `Watch`** (replay after seq N, then live) — the SSE
  endpoint (§8 `?after_seq=`) needs it; putting it on the store keeps the
  HTTP layer projection-free.
- **`View.TicketGoal`** is carried in the projection struct (not a §6
  read-model column, excluded from JSON) because crash recovery needs the
  goal to re-assemble prompts without re-reading the whole event log twice.
- Timestamps are stamped by the store (UTC, injectable clock in MemStore);
  seq is assigned by the store, starting at 1 per run.

## Ports (§4)

- `EnvSpec{RunID, RepoURL, Branch}` — exactly the non-secret Coder template
  parameters of §11. `Command{Script, Timeout}`; `ExecResult{ExitCode,
  Output}` where Output is the combined tail (adapters stream full logs to
  the runs dir per §12).
- `AgentSpec{Agent, Harness, Model, MaxTurns}` — resolved values from the
  flow, agent name resolves workspace-side (§5.2).
- `RepoRef{URL, DefaultBranch, ProjectID}` — ProjectID is the GitLab-side id
  from config (§13); other forges may ignore it.
- Fake `AgentRunner` hooks receive the stage context so tests can emulate a
  harness being killed on timeout/abort.

## Engine / interpreter (§2, §3)

- **Provision retry is engine-side**: one whole-Provision retry before
  `env_failed` (§2 "failed after 1 retry"). Adapters may additionally retry
  transient sub-steps internally, but the deterministic retry the state
  machine promises lives in the engine where the fakes can test it.
- **Crash-resume sessions**: only `stage_completed` carries a session id, so
  an open (crashed) stage resumes via the *last recorded session for that
  stage* (a previous iteration); with none, it re-Runs with a fresh §3.2
  prompt. The Resume reply is a fixed continuation message.
- **Recovery re-execution rules**: crash after `stage_failed` but before the
  edge decision → the failure is **routed directly** (no re-execution, no
  duplicate attempt events) using the output tail persisted in
  `stage_failed.error`; `forge_mr` stages instead re-enter their executor,
  which is idempotent via the recorded `mr_created`. Crash after
  `stage_completed` but before routing → route only; verdict stages re-read
  `artifacts/verdict.json` and do not re-append `verdict_received`.
- **Edge feed content IS persisted**: `edge_taken` carries a `feed` key
  (≤8 KB tail; extension over the §6 catalog) and `stage_failed.error`
  carries the command output tail for `exit_nonzero` — recovery never loses
  feedback. Likewise `ping_raised` gained an optional `context` key
  (mirrors the §9 pings.context column) so recovered pings keep it.
- **"iteration n of m"** in the §3.2 feedback header: n = attempt number,
  m = edge max + 1 (total allowed attempts).
- **Verdict source of truth**: `AgentResult.Verdict` if the adapter parsed
  one, else the engine reads `artifacts/verdict.json` itself. REJECT with no
  comments (or missing/unparsable file) is `stage_failed{output_contract}`.
- **ask_human answers in events**: recorded as `answer:"text"` with the reply
  text in `feedback` (matches the §6 enum); the shim receives the raw text.
- **Gate reject on a gate with no `on_reject`** (e.g. final_gate) abandons the
  run (`unrecoverable failure with no on_reject`).
- **Budget checks are strict-greater** and run after each agent stage
  completion; command stages contribute no cost/turns.
- **Per-run bearer tokens are regenerated on every engine boot** (not
  persisted — they're secrets and events are not a secret store). Recovery
  **rewrites `/factory/run.env`** in a still-running environment with the
  fresh token; the shim (M3) re-reads the file on 401 before giving up.
- **Flow-hash drift on recovery** (flow file edited while a run was in
  flight) logs a warning and continues with the current file.
- Engine `Shutdown` cancels run goroutines *without* terminal events (runs
  resume via `Recover`); only `Abort` writes `run_failed{aborted}`.
- Bootstrap step 4 (.opencode generation from `.factory/agents/`) is a
  placeholder script until the opencode adapter lands (M3); the AGENTS.md
  symlink + git-exclude part is real.

## Scheduling & limits (§14)

- Runs beyond `max_concurrent_runs` queue in `pending`, FIFO by creation.
  On recovery, runs already holding resources (provisioning and beyond)
  restart unconditionally; still-pending runs re-queue FIFO.
- The env cap counts live environments = `env_ready` without a later
  `env_destroyed`, computed from events. At the cap `POST /api/tickets`
  returns 409 naming the archivable (terminal, env-holding) runs, and a
  `blocker`-type notification with the same list fans out to channels.
- **Archive** requires a terminal run (active runs must be aborted first),
  appends `run_archived`, and destroys the environment immediately
  (+ `env_destroyed`) per §14. The append happens outside a run goroutine —
  safe because terminal runs have no writer.
- The hourly reaper (destroy completed envs after a window) is deferred to
  the serve wiring; Archive covers the manual path meanwhile.

## HTTP API (§8)

- The ping long-poll 204 carries the ping id in an **`X-Ping-ID` response
  header** (present on 200 too) — a 204 has no body, and the shim needs the
  id to re-poll. The 200 body also includes `ping_id`.
- Ask_human POST holds for `HoldTimeout` (55 s per §8, injectable in tests);
  the re-poll GET holds the same way.
- `GET /api/pings` supports only `status=pending` (other values 400); the
  inbox is served from the engine's in-memory registry until the Postgres
  pings projection lands.
- Stage logs are written by the engine to `runs_dir/<run>/<stage>-<iter>.log`
  (best-effort; §12 makes the Coder adapter stream there in M2);
  `GET .../logs/{stage}` serves the highest iteration, `?n=` tails bytes
  (default 64 KB). 404 when runs_dir is unset or no file exists.
- Channel CRUD accepts the §8 flat body ({kind, ...config}); the registry is
  in-memory until the §9 channels table lands, and implements the
  NotificationChannel port itself so the engine fans out through it without
  knowing about CRUD.
- Error mapping: env cap → 409, preflight/validation → 400, already-answered
  ping → 409, unknown things → 404. SSE pre-checks run existence (404),
  streams `id: <seq>` + `data: <envelope JSON>`, keepalive comment every 15 s.

## Postgres store (§0, §6, §9)

- **Watch fan-out is in-process** (no LISTEN/NOTIFY): the engine is the only
  writer (§2) and v1 is one factoryd per machine, so subscribers are notified
  after commit in-memory; replay comes from the table with seq-based dedup
  against the live buffer. Revisit only with multi-process engines.
- Seq assignment is `MAX(seq)+1` inside the append transaction — safe under
  the single-writer rule, no sequence object needed.
- The runs and pings projections are folded in the append transaction via
  the same `run.Apply` code the in-memory reducer uses; `factoryd
  rebuild-projections` truncates and replays through that exact path.
  Ping rows insert with `ON CONFLICT DO NOTHING` so rebuild is idempotent.
- Migrations: embedded numbered `.sql` files, tracked in
  `schema_migrations`, applied in a transaction each at Open.
- Store tests require a real Postgres via `TEST_POSTGRES_DSN` and skip
  otherwise (verified against an ephemeral local instance).

## Config & serve (§13)

- `reaper.destroy_abandoned` accepts only `"never"` in v1 — anything else is
  a config error rather than a silently-ignored knob.
- Secrets resolve from env-var names at wiring time (`token_env`, `*_env`);
  provider keys map to the standard `ANTHROPIC_API_KEY`/`OPENAI_API_KEY`
  names inside the workspace and unset providers are omitted (§11).
- `factoryd serve -dev` wires the in-memory fake adapters (agents honor
  output contracts by parsing the §3.2 contract line, reviewers approve) so
  full flows run end-to-end through the real engine + API + Postgres before
  M2-M4 land. Without `-dev`, serve refuses with a clear message.
- The workspace-visible engine URL is derived from the listen port
  (`http://host.docker.internal:<port>`, §8).
- Channel configs persist in the §9 channels table (write-through from the
  registry); persisted channels of kinds with no registered factory (matrix
  before M5) are skipped with a warning at boot, not dropped.
- Graceful shutdown: HTTP drain (10 s), then engine.Shutdown — runs stay
  non-terminal and resume via Recover on next boot. Verified end-to-end:
  kill -9 mid-`redact` against Postgres → restart → run resumed
  (re-provisioned the dead fake env), finished with MR + final gate.
- The §14 reaper runs hourly in serve; `destroy_completed_after` window,
  abandoned never reaped, failed runs kept (unspecified in the design —
  treated as inspection material like abandoned).
