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

## Coder adapter & workspace (M2, §11, §12)

- **`ports.Command` gained `Dir`** ("" = home, "repo" = checkout): command
  stages and `$CHANGED` run in the repo root explicitly instead of relying
  on inline `cd` prefixes; the clone step runs in home. Adapters own the cwd
  handling.
- **Read/WriteFile path convention**: relative = repo-root-relative
  (artifacts/…), absolute = absolute (/factory/…). Transfer is base64 over
  `coder ssh` stdin; WriteFile sends a one-line script + payload on the same
  stdin (bash consumes the first line, `base64 -d` the rest).
- **Exec scripts travel on stdin** (`bash -eo pipefail -s`), sidestepping CLI
  quoting entirely. Nonzero exit is a result; only transport failures
  (process spawn error or ssh exit 255) go through the §12 3× retry.
- **Status via `coder list --output json`** instead of REST (§12 lists REST)
  — one code path with the rest of the CLI usage; Links stay REST. CLI flags
  verified against Coder v2.35.2; behavior, not flags, is normative.
- **Provision is idempotent**: an existing running workspace for the run id
  is reused (crash recovery re-provision path); readiness = build status
  `running` + an `ssh -- true` agent probe.
- **Bootstrap extracted to `engine.RunBootstrap`** (pure port calls, no
  events) so `factoryd envcheck` runs the identical sequence standalone; the
  run loop wraps it and maps failures to env_failed. Added §11 step 3 git
  auth: `GitToken` written to /factory/git-credentials + credential.helper
  store — the token never lands in .git/config; omitted for public repos.
- **Firewall (resolve-then-pin)**: allowed names resolve once at init, pin
  into /etc/hosts, then ALL DNS is dropped — other names cannot resolve at
  all. Host-gateway is allowed on the engine AND Coder ports (the workspace
  agent's own websocket dies otherwise). The repo host is derived from
  FACTORY_REPO_URL (container env, non-secret, via sudo env_keep — container
  root cannot read /proc/1/environ without CAP_SYS_PTRACE). Sudoers allows
  exactly `init-firewall.sh` with no arguments; a /run lock makes re-runs
  no-ops. Plain per-IP iptables rules, no ipset (kernel-module dependency).
  Two live-debugging findings baked into the script: (1) on user-defined
  docker networks DNS goes to the embedded resolver at **127.0.0.11 through
  a DNAT rule that rewrites the port before the filter chain** — the drop
  must match the destination IP, not port 53; (2) the startup script is
  `blocking`, so ssh (and therefore bootstrap) cannot run before the
  firewall is up. Residuals: pinned IPs can go stale mid-run (CDN
  rotation); NET_ADMIN stays granted (docker can't drop caps post-start)
  but re-running the script cannot change the rule set.
- **Workspace→engine path: reverse tunnel, not host.docker.internal**
  (deviation from §8/§11, improvement over §15). The original design had
  workspaces reach the engine at the host gateway, which requires opening
  the engine port on the HOST firewall to all containers. Instead the Coder
  adapter holds one supervised `coder ssh -R <port>:127.0.0.1:<port>` per
  workspace: a listener inside the workspace on ITS localhost, forwarded to
  the engine on the host's localhost, riding the agent's already-allowed
  websocket to the Coder server. Consequences: `FACTORY_ENGINE_URL` is
  `http://127.0.0.1:8080`; the engine (and §13 default `listen`) binds
  127.0.0.1 — the §15 "API on 0.0.0.0 with no auth" residual disappears; no
  UFW/host change needed; the firewall's gateway allowance shrinks to the
  Coder port only. Tunnels auto-restart on drop (the shim's §10 backoff
  covers the gap), stop on Destroy/Close, and re-establish via the
  idempotent Provision on recovery. envcheck proves the path end-to-end
  with a host-loopback probe listener fetched from inside the workspace.
  The template's host.docker.internal mapping remains — the Coder agent
  itself still needs it to reach the server.
- **`factoryd envcheck`** is the M2 acceptance driver: provision → bootstrap
  → verify command → firewall positive/negative checks (example.com blocked,
  foreign DNS dead, provider APIs + repo host reachable, engine port not
  filtered) → binary-safe Read/WriteFile round-trips → links, then destroy
  (`-keep` to inspect).
- Workspace image pins: Go 1.24.5, Node 22, opencode 1.17.8, code-server
  latest-at-build. The ask_human shim (§10) ships in the image at
  /opt/factory/ask-human.mjs — dependency-free Node MCP stdio server; it
  re-reads /factory/run.env on 401 (token rotation after engine restart).
- Template: params run_id/repo_url/branch only (non-secrets §11); per-run
  docker network + persistent home volume; NET_ADMIN for firewall init; the
  agent init script is rewritten to reach Coder via host.docker.internal.
- **Coder caps workspace names at 32 chars** — names use the run id's last
  16 chars (the ULID's full 80 random bits; the dropped prefix is only the
  timestamp).

## opencode adapter (M3, §5.2) — flag/behavior drift vs the design sketch

Verified against opencode 1.17.8 (pinned in the image):

- **No `--dangerously-skip-permissions` flag exists.** Permissions are
  config-side: the generated opencode.json sets permission allow for
  edit/bash/webfetch so non-interactive runs never hang on a prompt;
  restriction happens per-agent via tools.
- **No turn-cap flag in `run` mode** — `max_turns` is not enforceable
  per-invocation; the §3 run-level budgets (cost + total turns) remain the
  real bound. Recorded as accepted drift.
- `--format json` emits NDJSON `{type, sessionID, part}` events;
  `step_finish` parts carry `tokens{input,output,reasoning,cache{write,
  read}}` and `cost`. Mapping: TurnsUsed = count(step_finish); CostUSD =
  Σcost; InputTokens include cache write+read (what was actually sent);
  OutputTokens include reasoning. Session id rides every event.
- Prompts/replies travel via a workspace file (`$(cat /factory/prompt.txt)`)
  — no CLI quoting pitfalls, no size surprises; provider keys are sourced
  from /factory/provider.env inside the exec script.
- **Generated config lives in the workspace user's config dir**
  (~/.config/opencode/…), not `.opencode/` in the repo as §5.2 sketches:
  nothing generated lands in the repo, so nothing needs git-excluding, and
  repos with their own opencode config are not clobbered. auth.json seeding
  (providers.opencode_auth_json) covers router/subscription auth — this
  machine authenticates via openrouter.ai, which also required the firewall
  to learn `extra_allowed_domains` (template variable).
- **`read_only` semantics correction (found live)**: §5.1 says read_only
  denies write/edit/patch, but §5.3's read-only agents MUST create their
  artifact outputs (planner has no bash either — it would be unable to
  deliver plan.md at all). read_only therefore denies edit/patch and keeps
  write — and in opencode, `edit: false` disables the whole file-mod tool
  group, so `write: true` must be set EXPLICITLY. Caught by the planner
  itself raising an ask_human blocker ping ("I have no write tool") — which
  incidentally proved the full §10 path (MCP tool → shim → tunnel → engine
  inbox → curl answer) live before its formal test.
- `ports.HarnessPreparer` (optional interface) is how bootstrap step 4
  invokes harness config generation — an addition over the §4 "final" port
  set; fakes don't implement it and the engine type-asserts.
- M3 acceptance evidence (run 01KXW7Q86K4Q399JC5EYPJ712F): plan ($0.021,
  7 turns) → plan_gate approved via curl → kill -9 mid-build → restart →
  recovery re-entered build (re-Run; first attempt had no recorded session)
  → build ($0.024, 9 turns, committed `factory(build): add Farewell
  function and -bye flag`) → `make verify` green → run_completed
  {cost $0.045, 16 turns, unwanted_pings 0}.

## GitLab adapter (M4, §12/§13)

- REST v4 with PRIVATE-TOKEN auth; no glab, no GitLab types crossing the
  port. `CheckAccess` requires ≥ Developer (30) on the project (push
  branches + open MRs); `FileExists` uses the files HEAD endpoint, or the
  tree endpoint for trailing-slash directory paths (`.factory/agents/`);
  `CreateDraftMR` prefixes `Draft: ` and targets the default branch with
  remove_source_branch; `MRState` maps opened/locked→open.
- The adapter is **bound to the one v1 project at construction**
  (config gitlab.project_id): §4's `MRState(MRRef)` carries no repo.
- **The repo-scoped token deliberately CANNOT merge**: gitlab.com fully
  protects default branches (Maintainer-only merge), so the §4 flow's
  "Lu merges" is enforced by GitLab itself — the factory can push branches
  and open draft MRs but never merge them. Discovered via 401 on the merge
  API with the Developer-role project access token; recorded as the
  intended §15 posture, not a limitation.
- **Verdict-contract hardening (found live)**: gpt-5-mini produced a full,
  substantive verdict but printed the JSON as reply text instead of writing
  artifacts/verdict.json → output_contract → abandoned (review has no
  on_fail in the canonical flow). The §3.2 verdict contract line now
  explicitly demands the file-writing tool and states that printing does
  not count. The file requirement stays hard (that is what makes verdicts
  trustworthy); deployments can additionally give review a self-retry
  edge (`on_fail: {to: review, max: 2}`) — flows are data.
## Telemetry & dashboards (M6, §7)

- OTel deps added (go.opentelemetry.io/otel + SDK + OTLP gRPC exporters) —
  §0-sanctioned by §7's OTel mandate; recorded here per the no-new-deps
  rule.
- **Telemetry is a projection of the event stream**: the engine feeds every
  freshly appended event to an EventSink exactly once (never on recovery
  replay — metrics would double-count; locked by a test). The sink derives
  the §7 span-per-run / span-per-stage-attempt (explicit event timestamps)
  and the four metrics. Harness telemetry stays non-load-bearing.
  Recovered runs whose run_created predates the boot get a lazily started
  span; a stage attempt resumed mid-flight (no fresh stage_started) reports
  its metrics without harness/model attrs — accepted recovery edge.
- **Dashboards query Postgres directly** (Grafana postgres datasource on
  the events/runs tables) — "analytics are queries" per the plan; the
  Prometheus datasource carries the live §7 operational metrics. Three
  provisioned dashboards: cost/run, unwanted pings per merged MR (headline
  stat, target 0), loop heatmap by stage (+ where runs stalled).
- The observability compose stack (collector, Prometheus, Grafana) uses
  host networking, loopback binds, and **deliberately unusual ports**
  (grafana 3001, prometheus 19090, OTLP 14317, exporter 18889): this host
  already ran a grafana/prometheus/collector on the standard ports — three
  separate bind collisions found live, including the collector's own
  self-metrics on 8888 (now disabled).
- M6 acceptance: dashboards render from 6 runs (5 real: two M4 GitLab runs
  imported into the analytics DB, Lu's two CPUI click-through runs, one
  review-abandoned run) — all three dashboards' queries verified returning
  rows via the Grafana API; §7 metrics verified in Prometheus
  (factory_runs_total/pings_total/stage_cost_usd/stage_turns with
  stage+model attributes).
- Operational finding: gpt-5-mini as reviewer wrote unparsable
  verdict.json three times in one run (bare `!` in the comments array) —
  the review self-retry edge and the abandoned outcome behaved exactly as
  designed, and the failure is visible on the loop dashboard. Deployments
  wanting fewer review stalls should pick a stronger reviewer model
  (flows are data).

## CPUI & Matrix (M5)

- Matrix adapter: client-server API v3, PUT m.room.message with a ULID txn
  id (idempotent per attempt), plain + HTML bodies; registered as the
  `matrix` factory in the channels registry, so §8 channel CRUD constructs
  it from {homeserver, access_token, room_id}.
- The CPUI is a SvelteKit 2 / Svelte 5 SPA (adapter-static, no component
  library per §0) served by factoryd itself from `cpui_dir` with an
  index.html fallback — one binary + one static dir, no separate web
  server, same-origin API (no CORS). `vite dev` proxies /api for UI work.
- The SSE endpoint honors the browser `Last-Event-ID` reconnect header
  (same semantics as `?after_seq=`), so EventSource-driven timelines resume
  without gaps after network blips.
- Timeline, totals, deep links and the pending-ping answer box are all
  derived client-side from the raw event stream — the UI is a projection of
  events, per the §2 principle; no bespoke view endpoints were added.
- M4 acceptance evidence (run 01KXXBW7JP8K3ZJRPSBPNVJMSJ, project
  intradeus/greeter-factory-test): full v1 flow — preflight against real
  GitLab → plan → gate → build → verify → cross-vendor review APPROVE
  (gpt-5-mini, proper verdict.json) → redact → push → **real draft MR !2**
  → final_gate parked → Lu merged in GitLab → auto_complete fired in
  **0.85 s** (merged_at 18:27:56.436Z, run_completed 18:27:57.289Z; ≤90 s
  criterion). Totals: $0.072, 30 turns, 0 unwanted pings.
