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
  `stage_failed.error`; `action` stages instead re-enter their executor,
  which must be idempotent (create_mr via the recorded `mr_created`). Crash
  after `stage_completed` but before routing → route only; a stage whose agent
  declares a verdict output re-reads it at its bound path and does not
  re-append `verdict_received`.
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
  **rewrites `/apothecary/run.env`** in a still-running environment with the
  fresh token; the shim (M3) re-reads the file on 401 before giving up.
- **Flow-hash drift on recovery** (flow file edited while a run was in
  flight) logs a warning and continues with the current file.
- Engine `Shutdown` cancels run goroutines *without* terminal events (runs
  resume via `Recover`); only `Abort` writes `run_failed{aborted}`.
- Bootstrap step 4 (.opencode generation from `.apothecary/agents/`) is a
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

## Config & serve (§13)

- `reaper.destroy_abandoned` accepts only `"never"` in v1 — anything else is
  a config error rather than a silently-ignored knob.
- Secrets resolve from env-var names at wiring time (`token_env`, `*_env`);
  provider keys map to the standard `ANTHROPIC_API_KEY`/`OPENAI_API_KEY`
  names inside the workspace and unset providers are omitted (§11).
- `apothecaryd serve -dev` wires the in-memory fake adapters (agents honor
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
  events) so `apothecaryd envcheck` runs the identical sequence standalone; the
  run loop wraps it and maps failures to env_failed. Added §11 step 3 git
  auth: `GitToken` written to /factory/git-credentials + credential.helper
  store — the token never lands in .git/config; omitted for public repos.
- **Firewall (resolve-then-pin)**: allowed names resolve once at init, pin
  into /etc/hosts, then ALL DNS is dropped — other names cannot resolve at
  all. Host-gateway is allowed on the engine AND Coder ports (the workspace
  agent's own websocket dies otherwise). The repo host is derived from
  APOTHECARY_REPO_URL (container env, non-secret, via sudo env_keep — container
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
  websocket to the Coder server. Consequences: `APOTHECARY_ENGINE_URL` is
  `http://127.0.0.1:8080`; the engine (and §13 default `listen`) binds
  127.0.0.1 — the §15 "API on 0.0.0.0 with no auth" residual disappears; no
  UFW/host change needed; the firewall's gateway allowance shrinks to the
  Coder port only. Tunnels auto-restart on drop (the shim's §10 backoff
  covers the gap), stop on Destroy/Close, and re-establish via the
  idempotent Provision on recovery. envcheck proves the path end-to-end
  with a host-loopback probe listener fetched from inside the workspace.
  The template's host.docker.internal mapping remains — the Coder agent
  itself still needs it to reach the server.
- **`apothecaryd envcheck`** is the M2 acceptance driver: provision → bootstrap
  → verify command → firewall positive/negative checks (example.com blocked,
  foreign DNS dead, provider APIs + repo host reachable, engine port not
  filtered) → binary-safe Read/WriteFile round-trips → links, then destroy
  (`-keep` to inspect).
- Workspace image pins: Go 1.24.5, Node 22, opencode 1.17.8, code-server
  latest-at-build. The ask_human shim (§10) ships in the image at
  /opt/apothecary/ask-human.mjs — dependency-free Node MCP stdio server; it
  re-reads /apothecary/run.env on 401 (token rotation after engine restart).
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
  tree endpoint for trailing-slash directory paths (`.apothecary/agents/`);
  `CreateDraftMR` prefixes `Draft: ` and targets the default branch with
  remove_source_branch; `MRState` maps opened/locked→open.
- The adapter is **bound to the one v1 project at construction**
  (config gitlab.project_id): §4's `MRState(MRRef)` carries no repo.
- **The repo-scoped token deliberately CANNOT merge**: gitlab.com fully
  protects default branches (Maintainer-only merge), so the §4 mergin flow's
  is enforced by GitLab itself — the factory can push branches
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

## Config plane (M7, DESIGN2 §1-§7, §10, §14-§15)

- **flow_versions gained a `name` column** (deviation from the DESIGN2 §2
  sketch): `GET /api/flows/{name}/versions` needs the linkage; hash stays
  the PK, versions stay immutable.
- **Engine stays kinds-free**: `engine.Resolution` carries only port-typed
  fields plus plain funcs (SeedAuth/Prepare/Release); internal/resolve is
  the sole place kinds, confstore and the engine meet. Scenario tests run
  on `StaticResolver` over the bare fakes — the M1 suite survived the
  refactor unchanged in substance.
- **Provisioner cache key includes the effective template**
  (project.workspace.template override), since coder adapters bind their
  template at construction. Generations are refcounted by live
  resolutions; a config edit stales the old instance, closed on last
  release (locked by a resolve test). Forge/harness/provider adapters are
  stateless and constructed per resolution.
- **M7 requires `workspace.template` on every project** — the §8 image
  pipeline lands in M9; until then the §8.4 custom-template path is the
  only path. The validation moves behind the pipeline in M9.
  `import-config` sets it to the v1 Coder template, keeping behavior
  byte-identical after migration.
- The canonical v1 flow **no longer pins `harness: opencode`** — under
  §6.3 semantics an unpinned harness resolves to the project's kind, which
  is what makes the seeded flow portable across projects (and dev mode).
  The parser accepts any slug; kind existence is preflight's question.
- **The product ships NO built-in flows** (Lu, post-M8, superseding the
  earlier seed design): the `flows/` dir AND the embedded seed package are
  deleted; nothing is ever loaded from yaml at runtime and nothing is
  seeded into production DBs. Flows are user-defined in the UI, and
  `default_flow` is required with no fallback (the wizard demands an
  existing flow). The DESIGN §3.1 document survives only as the parser
  test fixture `internal/flow/testdata/v1.yaml`. `serve -dev` seeds its
  own inline `dev` flow — fake-sandbox convenience, dev mode only.
  `apothecaryd validate <file>` and import-config's flows_dir remain for
  user-supplied files at the CLI. Lu's production DB keeps its `v1` row:
  seeded pre-amendment and now referenced by his project — it is his user
  data, not product config.
- **Pre-v2 runs (no project_id) fail terminally on recovery** with an
  explicit reason, rather than spinning each boot; their environments must
  be destroyed manually (engine has no global provisioner anymore).
  Terminal pre-v2 runs are untouched.
- **Settings reads are per-decision** (admission, reaper pass, run
  finish) with defaults + a warning on read failure — a broken settings
  row must not brick admission.
- **Secret handling verified end-to-end**: secretbox roundtrip + wrong-key
  refusal unit-tested; the httpapi leak test plants secret values,
  configures everything over the API, drives a full fake run and greps
  every read endpoint plus every event payload; the import test checks the
  bytea at rest contains no plaintext (`position(...::bytea)`).
- **Dev seeding**: `serve -dev` registers fake kinds and, on an empty DB,
  creates fake integrations + a `dev` project (setup/verify = `true`,
  template `dev`), so the compose smoke test can file tickets immediately.
  A DB that already holds projects is never polluted.
- `engine.CreateRun` resolves twice per run (preflight + run goroutine) —
  deliberate: the run goroutine must resolve anyway for recovery, and
  instances are cached.
- **Coder release archives name their member `./coder`** — the deploy
  image extracts `./coder`, not `coder` (found live: tar exit 2 in the
  first image build).
- **M7 acceptance evidence** (ephemeral Postgres 16 in docker): migration
  0002 applied; full store suite green; serve booted from APOTHECARY_* env
  vars alone; dev project seeded; ticket → 9-stage v1 flow → gates
  answered via API → `run_completed` with project_id projected;
  import-config on a synthetic v1 factory.yaml created 4 integrations +
  project (template=factory, locator=123) + settings, zero plaintext at
  rest; the **containerized** engine (deploy/Dockerfile, host network,
  env-only boot) served the CPUI, seeded dev, and completed the same
  9-stage flow, with coder v2.35.2 and the docker CLI verified inside the
  image; `go test ./...` green.
- **Operator migration note**: a apothecaryd built from this commit no longer
  accepts `serve -config` — the v1 deployment must run `apothecaryd keygen`,
  export the §1 APOTHECARY_* vars, run `apothecaryd import-config -config
  /etc/factory/factory.yaml` once (with the old env present), then
  `apothecaryd serve`.

## CPUI config surface (M8, DESIGN2 §11)

- **Forms are descriptor-driven end-to-end**: IntegrationForm renders from
  `GET /api/kinds` fields — adding a backend kind (github, codex in M10)
  needs zero frontend work. Secret inputs are write-only: password inputs
  (textarea for `*_json` fields — a naming convention, not schema; recorded
  as the boring heuristic), placeholder shows the stored mask, and only
  typed keys are sent, so an untouched field keeps its value (§3 merge).
  Clearing a secret from the UI is deliberately unsupported (edge case;
  the API's `""`-deletes semantics remain curl-able).
- **The project wizard is one sectioned form**, not a stepper: forge pick →
  Browse repositories (ListRepos → click fills url/locator/branch/name) →
  commands as free key/value rows (setup optional and labeled so) →
  adapter picks → workspace (template required with an M9 note, fragment
  textarea already wired for M9) → default flow. Same component edits
  existing projects.
- **Flow editor is a plain textarea** (§11: no component library; a code
  editor widget is a later upgrade): Validate button surfaces the parser's
  line-precise errors verbatim; Save bumps the content-addressed version;
  history offers "load into editor" (restore = save that text again —
  versions themselves stay immutable).
- Ticket form gained the required project select (defaults to the newest
  project); flow field empty = project default. Runs list/table carry a
  project column and honor `/?project=` deep links from project pages.
- Nav grew to Runs · Inbox · Projects · Flows · Integrations · Settings ·
  Channels — flat wins over nesting at this page count.
- **M8 verification**: svelte-check 0 errors; the full wizard sequence
  (create integrations → Test → repo picker → project → preflight → ticket
  on the project default flow → gates → run_completed with project_id →
  flow validate/save/versions → settings PUT) exercised live against
  `serve -dev` + Postgres through the exact endpoints the UI calls; the
  production compose stack rebuilt and rolled to the new UI. The
  real-MR-through-the-UI click-through (fresh DB, real tokens) is Lu's
  acceptance pass, per the M5 precedent.

## Harness-owned model auth (post-M8 design amendment)

Lu's call after using the M8 UI: the harness/provider split read as
redundant (his openrouter key would exist twice — as a provider integration
AND inside opencode's auth_json). Amendment over DESIGN2 §4/§4.1, applied
in two steps (UI first, then full backend removal on his follow-up call):

- **The harness owns model auth.** opencode's `auth_json` (required,
  relabeled) is THE place for model credentials — API keys and/or
  subscription logins, in the harness's native format, written verbatim by
  SeedAuth (which now takes no provider argument). Future harness kinds
  (codex, M10) follow the same shape.
- **The provider port is REMOVED end to end**: `Provider`/`ProviderKind`/
  `ProviderCreds` interfaces, the anthropic/openai/openrouter kinds, the
  fake provider, resolver provider resolution, `projects.provider_ids`
  (migration `0003_drop_providers.sql` drops the column and deletes
  `port='provider'` rows), confstore `PortProvider`, and the UI section.
  Valid ports are forge|provisioner|harness.
- `Resolution.ProviderEnv` / bootstrap's provider.env **mechanism stays**
  (unfed, writes an empty file): it is the natural slot for a future
  harness kind that authenticates via env var (codex + OPENAI_API_KEY) —
  the harness would feed it, not a provider port.
- **Egress derivation coarsens by design**: the opencode harness declares
  the union of provider APIs it can route to (api.anthropic.com,
  api.openai.com, openrouter.ai) unconditionally, so credentials inside
  auth.json are never blocked by the workspace's own firewall. Broader
  than strictly needed — accepted residual, precision explicitly deferred
  ("update later"); `workspace.extra_domains` remains the manual lever.
- `import-config` no longer imports provider env keys; it prints a note
  telling the operator to fold them into the harness auth_json.

## Harness model listing (post-M8)

- `Harness.Models(ctx)` lists the exact `provider/model` strings flows may
  use, given the harness's configured auth. Engine-side (no workspace):
  the opencode kind fetches models.dev's api.json — the SAME catalog the
  opencode CLI bundles — cached 1 h in-process, URL stubbed in tests, and
  filters it to the top-level keys of auth_json (opencode's provider ids).
  Unknown auth keys (bespoke subscription logins) are skipped silently; no
  auth configured is a named error, not a 167-provider avalanche.
- Served as `GET /api/integrations/{id}/models` (harness port only); the
  flow editor gained an "Available models" panel — harness picker, filter,
  click-to-copy. Discoverability only: model strings in flows remain
  UNvalidated at save/preflight (the §6.3 rationale stands — harnesses
  route prefixes the engine cannot second-guess).
- Verified live: Lu's real opencode integration lists 343 openrouter
  models through the deployed endpoint.
- **Failed-invocation logs (found live)**: a plan stage died with
  `harness_error` (model `anthropic/claude-sonnet-4.6` unroutable under
  openrouter-only auth) and its stage log was EMPTY — the adapter's
  `Output` carried only parsed text events, and startup failures produce
  none. On nonzero exit the adapter now returns the RAW output, so the
  stage log and the on_fail feed show the real error. Model strings under
  router auth are `openrouter/<vendor>/<model>` — the first slash splits
  provider/model, the rest is the model id (validModel already allowed
  this); the models panel emits exactly these strings.

## Workspace composability & dynamic envcheck (M9, DESIGN2 §8–§9)

- **Single-copy embeds via `factory/workspace`**: the base-image context
  (Dockerfile + shim + firewall) and the Coder template are `go:embed`ed by
  a package living IN workspace/ — no duplicated files, no cross-directory
  embed gymnastics. Base tag = `apothecary/base:<sha16 of embedded tree>`;
  template version = `v<sha16 of template tree>`.
- **The base image lost Go, opencode and build-essential** — toolchains are
  fragments/presets, the harness is `InstallLayer()`. Node stays (shim
  runtime + npm-installed harnesses). Existing fragments referencing the
  old base ARGs (Lu's `${GO_VERSION}`) must pin versions explicitly — the
  presets do.
- **Composition & tags**: `wsimage.Compose` renders FROM base → USER root →
  fragment → harness layer → USER factory; `Tag` hashes the composed file
  (which embeds the base ref), so every input change moves the tag and
  identical config is a docker-side no-op. Builds run one-at-a-time
  (`docker build` via the socket, temp-dir context — no tar-stdin), logs to
  `runs_dir/builds/<tag>.log`.
- **image ensure is engine-side** (§8.3): between env_requested and
  provision; failure ⇒ deterministic `env_failed{image build: …}`; the new
  `image_built{tag, cached}` event may repeat on recovery re-entries
  (append-only log; projection ignores it).
- **Template push**: `EnsureTemplate` compares the provisioner's ACTIVE
  version name against `v<sha16>` (CLI `templates versions list -o json`)
  and pushes on drift, once per process. New params `workspace_image` /
  `extra_allowed_domains` carry defaults, so pre-M9 creates (custom path:
  v1 params only) keep working against the new active version. Fresh
  deployments fall back to `templates create` when push reports the
  template missing. Flags verified against Coder v2.35.
- **Firewall names removed from the script**: init-firewall.sh keeps only
  mechanics; the allowlist arrives fully derived via APOTHECARY_EXTRA_DOMAINS
  (repo host kept as belt-and-braces for custom templates).
- **workspace.template is optional again** (the M7 stopgap requirement is
  gone): empty = managed template + built image; set = custom-template
  escape hatch, no pipeline, no push, v1 params.
- **Dynamic envcheck**: `engine.RunEnvCheck` derives the §9 catalog from
  the Resolution (one egress step per domain, harness binary, image step
  on the pipeline path) and is shared by the CLI (`envcheck -project`) and
  the API (`POST /api/projects/{id}/envcheck` → 202 + poll
  `GET /api/envchecks/{id}`; in-memory last 20). **Deviations from the §9/
  §10 sketch, recorded**: the UI POLLS instead of SSE (2 s; check runs are
  minutes long — SSE machinery wasn't worth it yet), and the manual
  `build-image` endpoint is deferred (the envcheck image step and the run
  path cover builds; logs land in runs_dir/builds).
- UI: preset insert buttons (append visible fragment lines + their egress
  domains), the template field demoted to "advanced, usually empty", and
  an Envcheck panel (timeline of steps, keep-workspace toggle) on the
  project page.
- **Firewall fail-open bug (found by the acceptance run, fixed)**: an
  unresolvable allowlist name aborted init-firewall.sh at the `getent`
  pipeline (set -e) BEFORE any rule existed — the workspace came up with
  open egress, which envcheck loudly caught (three failing deny steps).
  Two fixes: `|| true` on the resolution pipeline (a typo'd domain is
  simply not allowed; its egress step fails by name), and an ERR trap that
  slams egress shut (loopback + established + Coder gateway only, no lock
  file) on any unexpected script failure — fail-closed by construction.
- **Coder version-JSON drift (found live)**: `templates versions list -o
  json` carries NO "active" field — the CLI's Active column derives from
  the template's `active_version_id`. The first re-check therefore
  re-pushed and collided on the version name. activeTemplateVersion now
  joins templates-list (active_version_id) with versions-list (id→name),
  and a name collision falls back to `templates versions promote
  --template=… --template-version=…` (flags verified against v2.35.2).
- **M9 acceptance evidence** (live, Lu's machine, deployed stack):
  greeter-factory-test (Go+make fragment) full envcheck PASS incl.
  `verify` (`make verify` on the real repo) — first pass built
  apothecary/base:e6830826… + ws:f9f248db… (cached=false), re-check
  cached=true; py-smoke (python preset) PASS on its own image
  (ws:987b4243…, base layer shared); the engine pushed template version
  vc101fd0f… to the live Coder and provisioned real workspaces from the
  built tags through the new parameters; induced failures produced named
  steps — `egress:doesnotexist.invalid` (deny checks green, firewall
  held) and a wrong Coder token surfacing Coder's 401 through the Test
  endpoint. The make-not-found first attempt was itself the composability
  working: the stripped base genuinely lacks what fragments don't add.

## GitHub forge + codex harness (M10, DESIGN2 §12–§13)

- **Neither adapter touched the engine** — two `Register*` lines in
  cmd/apothecaryd (the M10 port proof). The descriptor-driven UI picked both
  up with zero frontend changes.
- **GitHub** (§12 as specced): Bearer PAT + api-version header; Locator
  "owner/repo"; CheckAccess requires `permissions.push`; FileExists via
  the contents endpoint (array = directory); **native draft PRs, no
  `Draft:` title prefix** (deviation from the GitLab adapter, deliberate);
  MRState: `merged_at` wins over state (merged PRs report state
  "closed"); GitCredential `x-access-token:<PAT>`; egress github.com +
  api.github.com + codeload.github.com. Full httptest suite.
- **codex pinned at codex-cli 0.144.6**, flags AND JSONL probed live in a
  scratch container before writing the adapter:
  - Invocation: role file + §3.2 prompt are **piped via stdin (`-`)** —
    codex reads stdin, and our exec transport already uses stdin for
    scripts, so args were never an option; also dodges arg-size limits.
  - `--dangerously-bypass-approvals-and-sandbox`: codex's documented mode
    for "externally sandboxed environments" — precisely the factory
    workspace (container + default-deny firewall). codex's own sandbox
    would have blocked in-agent network (module downloads) otherwise.
  - JSONL: `thread.started.thread_id` → SessionID (resume:
    `codex exec resume <thread_id> -`); `turn.completed.usage`
    {input+cached → InputTokens, output → OutputTokens}, TurnsUsed =
    turn.completed count; `item.completed agent_message` → Output;
    non-JSON tracing lines interleave and are skipped. CostUSD stays 0 —
    budgets ride max_total_turns (§3 anticipated).
  - Agent identity is a **role-file preamble** (`/factory/agents/
    <name>.role`, generated by Prepare from .apothecary/agents specs);
    read_only/allow_bash are instruction-level only — documented §13
    residual. config.toml registers the ask_human MCP server.
  - Model strings: flows keep `openai/<model>`; the adapter strips the
    prefix (codex wants bare ids). Models() serves the openai catalog as
    `openai/<id>` accordingly.
- **`Harness.EnvVars()` added** (the slot the provider-port removal
  predicted): codex's `api_key` secret rides provider.env as
  OPENAI_API_KEY; opencode/fake return nil. Codex auth = api_key and/or
  ChatGPT auth.json (seeded to $CODEX_HOME); Test refuses when neither is
  set.
- **M10 evidence so far** (live, no model credentials needed):
  codex-smoke project envcheck FULL PASS — image ws:77267c07… built with
  the codex layer, `harness_binary: codex` in the workspace, bootstrap ran
  codex's Prepare (config.toml + role files from the real repo's specs),
  egress derived to codex's domains (api.openai.com/auth.openai.com/
  chatgpt.com — no openrouter, per-harness derivation); image assertions:
  codex 0.144.6 present + opencode ABSENT in the codex image, and the
  reverse in the Go/opencode image. **Pending Lu's tokens**: the real
  draft-PR run on a GitHub repo (auto_answer ≤90 s) and a live codex
  plan→build with usage + kill-resume — the credential-bearing halves of
  the §17 criteria.

## Ship it (M11, DESIGN2 §15/§17)

- **Versioning**: `apothecaryd version` + ldflags stamp (`deploy/Dockerfile`
  ARG VERSION); `deploy/release.sh [vX.Y.Z]` builds
  `apothecary/apothecaryd:<version>` (+ latest), prints the stamped version,
  and pushes when `REGISTRY=` is set. The compose file honors
  `APOTHECARY_IMAGE` so deployments pin releases in `deploy/.env`; default
  tracks the working tree (`:dev`) — the right default for a single-repo
  self-hosted install with no registry (this repo has no git remote).
- **README.md** is the M11 install path: prerequisites (docker, external
  Coder, forge PAT, harness auth, target repo with AGENTS.md +
  .apothecary/agents), compose install with keygen, the UI configuration
  walkthrough ending in envcheck + first ticket, upgrade notes
  (migrations at boot; base image + template are content-addressed and
  self-heal), full CLI table, dev workflow, and the short-form security
  model.
- **Acceptance caveat, stated honestly**: the §17 clean-machine criterion
  was validated by construction on THIS machine — the production stack
  runs from the compose file + env.example + keygen path the README
  prescribes, with data intact across every M7–M10 roll. A genuinely
  fresh box is the final proof.
- **CI/CD (post-M11, once the GitHub remote existed)**:
  `.github/workflows/release.yml` — every push to main runs `go test` +
  svelte-check, then builds with `--build-arg VERSION=v2.0.<run_number>`,
  pushes `ghcr.io/intradeus/apothecary/apothecaryd:{vX,latest,sha-…}` via
  the workflow GITHUB_TOKEN (packages:write), verifies the stamp
  (`docker run … version`), and creates the matching git tag
  (contents:write; skipped when it already exists — re-run safety).
  Monotonic run-number versioning over semver-by-hand: every merge IS a
  release, per Lu. Single-arch linux/amd64 (the deployment target);
  GHCR package visibility follows the repo — private packages need a
  `read:packages` login to pull.

## The great rename: factory → apothecary (post-M11, Lu's call)

Done pre-first-release — the only cheap moment. Renamed end to end:
- Go module `factory` → `apothecary` (every import path); binary/cmd
  `apothecaryd`; image `apothecary/apothecaryd`; CI image
  `ghcr.io/intradeus/apothecary/apothecaryd`; compose service.
- Env contract `APOTHECARY_*` → `APOTHECARY_*` — boot vars AND the workspace
  contract (run.env keys, template env, sudoers env_keep, firewall
  script, shim), all rolling together via the content-addressed base
  image + template (new hashes ⇒ rebuilt/re-pushed on next use).
- Workspace filesystem `/factory` → `/apothecary`, `/opt/apothecary` →
  `/opt/apothecary`, runs dir `/var/lib/apothecary/runs`; unix user
  `factory` → `apothecary` (home moves accordingly).
- Branch prefix `apothecary/<run>-<slug>`, workspace names
  `apothecary-<id>`, git identity `Apothecary <apothecary@local>`,
  telemetry `apothecary.*` attrs + `apothecary_*` metrics (Grafana
  dashboards updated to match), Coder-kind default template name
  `apothecary` (existing integrations keep their stored value).
- Historical docs swept too (`factoryd`, `FACTORY_*`, `/factory` paths) —
  the design docs now read with current spellings.
**Operational notes**: deploy/.env needs its two var names updated
(APOTHECARY_PG_PASSWORD, APOTHECARY_MASTER_KEY — values unchanged); the
stack must be RECREATED (down + up: new volumes, fresh DB, reconfigure in
the UI); pre-rename workspaces/images/templates are orphaned leftovers;
Prometheus metric history restarts under the new metric names.

## The great bug hunt (post-v2.0.1)

A four-subsystem adversarial review (~40 findings) followed by a fix-all
pass. The load-bearing fixes, by area:

**Confirmed-broken (would have bitten immediately)**
- CPUI `req()` discarded 202 bodies → the envcheck button destructured
  `undefined` and §9 was unusable from the UI. 202s now parse their body.
- `GET /api/channels` returned channel config verbatim — including the
  Matrix `access_token`. Responses now mask token/secret/password/key-
  shaped config values (storage stays the documented plaintext residual).
- Fresh-install keygen was a chicken-and-egg: compose's `:?` guard on
  APOTHECARY_MASTER_KEY refused to run keygen itself. Docs now use plain
  `docker run … keygen` (also avoids the orphan `deploy_` postgres the
  compose-run path spawned).
- Coder Provision reuse never issued `coder start` on a stopped/failed
  workspace — autostop or crash recovery spun for the full provision
  timeout. Reuse now starts non-running workspaces (waitReady arbitrates).
- Crash mid-bootstrap bricked the run: no env id is recorded before
  env_ready, so recovery re-provisioned (same workspace) and re-ran
  `git clone` → exit 128 → terminal env_failed. Bootstrap is now
  idempotent (clone-if-absent, switch-or-create branch, dedup'd exclude
  lines), a transient Status error no longer triggers re-provision, and
  `env_failed` carries the env id so capacity counting / archive / the
  reaper finally see (and can destroy) workspaces of failed bootstraps.

**Engine lifecycle**
- RaisePing/AnswerPing no longer block forever when no agent stage is
  draining the mailbox (bounded by run-done + 30s timeout); a finished
  run's pings are pruned (no permanently unanswerable inbox entries);
  processAnswer rolls back its claim if the event append fails.
- Flow drift: a recovered run whose current stage no longer exists in the
  flow now fails honestly instead of reporting run_completed (Index -1 was
  read as "past the end"). Transient flow-source errors leave the run
  recoverable instead of terminal (`flow.ErrNotFound` distinguishes).
- Stage-timeout path collects the cancelled attempt's output for the log
  and abandons a ctx-ignoring adapter after a 30s grace instead of
  wedging; the suspend/resume timer drains stale ticks (no spurious
  instant timeout after a human answers).
- `command` stages with a deleted run_ref fail loudly (empty script used
  to exit 0 and pass gates). Shutdown sets a closing flag so a finishing
  run can't admit a queued run nobody will stop.
- Envcheck: destroy failures now count (named return), the job registry
  never evicts running jobs and the duplicate guard is race-free; engine
  port parses via net/url (scheme defaults) instead of LastIndex(":").
- Provisioner cache is keyed by (integration, template) — two projects
  sharing an integration no longer thrash/close each other's adapters —
  and a stale entry is removed on eviction (double-Close fixed).

**Config plane**
- confstore gained `ErrInvalid`; API statuses are now honest (validation
  400, not-found 404, conflict 409, infra 500 — DB outages were 400s).
  Settings PUT merges over stored values, not compile-time defaults.
- Masked-secret round-trip (`••••last4` written back as the new secret)
  is rejected at validation.
- Fragment validation also rejects SHELL/ONBUILD and trailing `\`
  continuations (which would splice into engine-owned lines, e.g. the
  final `USER apothecary`).
- Migration 0004 adds a NOT VALID FK projects.default_flow → flows(name)
  as the delete/create race backstop; migrations run under a pg advisory
  lock; SSE fan-out drops slow subscribers instead of stalling every
  append while holding the store mutex.

**Adapters/kinds**
- All shell interpolation into workspace scripts is single-quoted
  (`%q` left `$`/backticks live inside double quotes): model strings,
  session ids, agent/role paths, provider.env values (sourced file), and
  coder ReadFile/WriteFile paths. Belt-and-braces: agent-spec names must
  be lowercase slugs, flow model strings a strict charset.
- Coder Exec no longer blind-retries ambiguous exit 255 (a re-run of a
  side-effectful agent invocation); spawn failures still retry, probes
  keep the 255 retry.
- opencode: EgressDomains are now DERIVED from auth_json's provider keys
  (same keys Models() filters by — google/xai/mistral/… map to their API
  hosts) so the firewall can't block a model the UI itself offered; Test
  fails on the missing-but-Required auth_json; first session id wins
  (child sessions no longer hijack Resume); cache-read tokens excluded
  from input accounting; Resume guards empty sessions. Spec listing is
  line-anchored (`===APOTHECARY-SPEC` marker, renamed) and includes .yml.
- codex: same quoting/marker/.yml/Resume-guard fixes.
- github: per-segment path escaping in FileExists (%2F was GitLab's
  convention), draft-PR 422 degrades to a "Draft: "-prefixed regular PR,
  rate-limit 403s are named as such, locator validation rejects URLs and
  strips .git.
- coder template: `templates create` fallback re-pushes under the
  content-addressed version name (no perpetual second-boot re-push); the
  engine now passes `--var coder_port=<from integration URL>` so non-7080
  Coder deployments keep agent reconnects.
- workspace firewall: IPv6 is fail-closed (kernel-has-v6 + failing drop
  now trips the ERR trap; fail_closed also drops v6); release.yml checks
  the REMOTE for the version tag (re-runs were always red).

**Known-and-deliberately-left**
- opencode prompt rides argv (`"$(cat …)"`) with the ~2 MB ARG_MAX
  ceiling; codex uses stdin. Switching opencode to stdin is untested
  against the CLI — revisit with a live run.
- Env-capacity admission is still check-then-create (single-user risk).
- Coder Exec still concatenates stdout+stderr on success (changing it
  would drop stderr from stage logs).

## M12 — Agent library (DESIGN3 §1)

Agents moved from the target repo (`.apothecary/agents/*.yaml`) into a
per-project, versioned, UI-edited library. As designed, with these notes:

- Schema 0005: `agents(project_id, name)` PK with ON DELETE CASCADE from
  projects; `agent_versions` content-addressed over the spec JSON
  (project_id excluded from hash — identical prompts share a version
  across projects, by design for copy analytics).
- `Harness.Prepare(ctx, env)` → `Prepare(ctx, env, specs)`; the
  opencode/codex `readSpecs` exec path (and the `===APOTHECARY-SPEC`
  marker protocol) is deleted. Adapters write their base config
  (ask_human MCP) even for zero specs; "no agents" is preflight's
  problem, not the adapter's.
- `ports.Forge` gained `ListDir`/`ReadFile` (gitlab tree/files APIs,
  github contents API, fake in-memory) solely for the one-time
  `POST /api/projects/{id}/agents/import-repo`; existing names are
  skipped, never overwritten. The fake forge ships one importable
  `sample.yaml` so the path is exercisable in dev.
- `stage_started` now carries `agent` + `agent_hash` — the analytics
  join key between outcomes and prompt versions.
- Preflight: the `repo_file:.apothecary/agents/` check became per-stage
  `agent X exists in the project library` (same shape as run_ref);
  AGENTS.md stays a repo requirement.
- Delete guard is honest but narrow: only the project's default flow
  blocks deletion (flows are global; other projects' preflights catch
  their own dangling references at run creation).
- Agent copy refuses name collisions wholesale (no partial copies, no
  silent overwrite).
- BREAKING: target repos no longer need (or use) `.apothecary/`;
  example specs moved to `examples/agents/`. Existing projects need
  their agents created/imported before the next run passes preflight.

## M13 — Flow builder (DESIGN3 §2)

The success chain is now an explicit edge behind an extensible kind
registry, and flows are edited as a graph. As designed, with these notes:

- flow model: `Stage.OnSuccess` (stage id or reserved `done`) + `Edges
  map[kind]*Edge`; `edgeKinds` registry drives applicability + feed rules.
  Adding a kind = one DocStage field + one registry row + one engine raise
  site. YAML sugar kept: absent on_success = next in document order (last
  = done) — every pre-M13 flow parses to identical routing (engine suite
  unchanged and green).
- Validation: success graph must be acyclic and reach `done` (a
  success-only cycle through command stages would burn no budget and loop
  forever); every stage must be reachable from the entry; `done` is a
  reserved id; auto_complete gates had to end the run (was
  "last stage").
- Engine: `advance()` follows the edge instead of pos++; `takeEdge` looks
  the edge up by kind (call sites just name the kind). Replay untouched —
  success transitions still emit no extra events.
- `flow.Doc*` (raw schema shape) exported with JSON tags; `ParseDoc`
  validates then returns it. `/api/flows/validate` returns `doc` — the
  single parsing authority; the editor never parses YAML itself (it only
  emits, via the `yaml` npm package).
- Layout: migration 0006 `flows.layout jsonb`, excluded from the hash;
  nil-layout saves keep the stored one (pg COALESCE + mem parity), so
  YAML-tab edits don't wipe positions and node drags never mint versions
  (verified live on a scratch PG: layout-only save → same hash, same
  version count; explicit-edge flow ran to completed).
- CPUI: `@xyflow/svelte` + `yaml` are the first runtime deps (bundled, no
  CDN). Graph tab = FlowGraph (canvas, typed colored handles:
  success/fail/reject) + FlowInspector (per-node forms, dropdowns via
  datalist — assistance with free text always available) + context picker
  (project → agents/models/run_refs). Serializer emits explicit
  on_success on every stage, stages in success-chain order; round-trip
  (doc → YAML → doc → YAML) verified byte-stable and the emitted YAML
  passes the Go parser.
- Not done (recorded): live per-node validation markers on the canvas
  (server errors render in the shared validation panel instead);
  changing the entry stage requires the YAML tab.

## M14 — Plan review workbench (DESIGN3 §3)

Plans render in the UI; comments anchor to sections and submit as one
feedback prompt. As designed, with these notes:

- `Engine.PingArtifact`: active-ping registry → run view (store reduce) →
  resolve project → `Envp.ReadFile` on the still-alive workspace,
  released immediately. Path comes from the flow document, never the
  request; content capped at 1 MiB (tail — the end of a plan beats the
  start). Sentinels: ErrArtifactIsURL (409, mr_url gates keep the
  link-out), ErrNoArtifact (410), ErrPingNotFound (404).
- `GET /api/pings/{pid}/artifact` — unauthenticated like the rest of the
  CPUI ping surface (single-user, loopback).
- CPUI: `marked` dep (raw HTML in agent markdown is ESCAPED via a
  renderer override — plans are semi-trusted LLM output). `planreview.ts`
  lexes top-level blocks with cumulative line numbers + nearest heading;
  `composeFeedback` renders the deterministic "PLAN REVIEW — N comments"
  prompt (anchor + ≤200-char quote + text per item). Both verified by a
  strip-types node test (exact line/heading anchors, exact output).
- Review page `/pings/{id}`: block hover → "+" (selection inside the
  block becomes the quote), sticky review sidebar, drafts in localStorage
  keyed by ping id (survive reloads; cleared on submit), Request changes
  = reject via the EXISTING answer path — engine/edges/feed untouched;
  Approve confirms before discarding unsent comments. Inbox links
  artifact gates to it; mr_url gates keep the old behavior.
- Live smoke (scratch PG + dev stack): gate ping → artifact fetched from
  the workspace → reject with a composed review → `edge_taken.feed`
  carries the full PLAN REVIEW text (the replanning agent sees it as
  PREVIOUS ATTEMPT FEEDBACK) → re-plan → approve → run completed.
- Not covered by a dedicated test: the 409 mapping for mr_url payloads
  (three-line handler branch; engine sentinel is unit-visible).

## M15 — Durable ping surface (PLAN4 §2)

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

## M16 — Richer questions: options + artifacts on ask_human (PLAN4 §3)

The shim protocol grows two optional fields; everything downstream
generalizes machinery that already existed. Notes:

- ask_human tool schema: `options?: string[]` (closed answer set, max 8,
  trimmed+deduped, ≤200 chars each) and `artifact?: path` (repo-relative,
  validated by flow.ValidArtifactPath — it becomes payload_ref, so the
  M14 review workbench works for ANY agent question, not just gates).
- Strict options (decision #1): when declared, AnswerPing refuses
  anything not in the set, naming the choices. Determinism is the point;
  the note field (feedback) carries anything beyond the pick.
- Recording: option answers keep the chosen option in
  ping_answered.answer with the note in feedback — the historical
  answer:"text" convention now applies only to free-text asks. The shim's
  tool result: free text returns bare (the old "text\n" prefix noise is
  gone); options return "<option>\n(note: …)".
- Event/projection: ping_raised.options; migration 0008 `options jsonb`;
  PingView/ReducePings carry it (EventPingStore parity for free).
- UI: inbox renders one button per option (+ optional note field); the
  review page's submit is now mode-aware — gates approve/request-changes,
  option asks answer via buttons with the composed review as the note,
  free asks send the composed review as the answer text.
- Old shims baked into running workspaces simply never send the new
  fields — no compat break; the base image content-hash change rebuilds
  the workspace image on next use as usual.
- Live smoke: migration 0008 applied; full dev loop green. Options
  covered end-to-end at the engine and HTTP layers (validation, delivery,
  durable recording); no live agent invokes options until a real codex/
  opencode run asks with them.

## Amendment: repo import removed (pre-release cleanup)

The M12 one-time `.apothecary/agents/` repo import is deleted end to end
(user decision: the project is unreleased, no legacy to migrate): the
Agents-panel button, `POST /api/projects/{id}/agents/import-repo`,
`IntegrationOps.RepoAgentFiles`, the resolver implementation, and the
`Forge.ListDir`/`ReadFile` port methods added solely for it (gitlab,
github, fake adapters). `agentspec.Parse` stays (name-rule + spec
parsing). Reference agent prompts now live in `artifacts/agents/*.md`
(+ `artifacts/flows/v2.yaml`, the expanded-form reference flow), with the
neutral YAML specs in `examples/agents/`.

## Live run feedback (working card + workspace log tail)

Between stage_started and a stage's end the UI used to show nothing —
stage logs were post-hoc and the coder adapter buffered `coder ssh`
output in memory. Now:

- `ports.ExecLogTailer` is an OPTIONAL EnvProvisioner capability
  (`TailExecLog(env, n)`). Read-side by design: a sink parameter on Exec
  would have rippled into the AgentRunner path (harness adapters call
  Envp.Exec directly); a tail lets each adapter capture however it wants
  and the API degrade honestly when a provisioner can't stream.
- coder: `CmdRunner.Run` gained a tee writer; Exec streams header +
  stdout/stderr into `<RunsDir>/<env>/exec.log` AS THE COMMAND RUNS
  (mutex-serialized MultiWriter; the post-hoc writeLog is gone — same
  file, same format, now incremental). TailExecLog reads the tail; the
  file is a coder-internal detail again.
- fake: in-memory per-env transcript, so dev mode and tests exercise the
  same surface.
- `Engine.WorkspaceLogTail` (type-asserts the capability, ErrNoLiveLog
  otherwise) → `GET /api/runs/{id}/workspace-log?n=` (text/plain tail,
  404 when unsupported/no env).
- Run page: non-pending active runs show a Working card — pulsing dot,
  current stage + ticking elapsed (derived from the SSE event stream's
  latest stage_started), and a collapsible autoscrolling live tail
  polled every 2.5 s (paused when the tab is hidden). Transport note:
  nothing here is websockets — run events are SSE, the tail and the
  inbox poll.

## M18 — Hostname-filtering egress proxy (permanent firewall fix)

The resolve-then-pin firewall's failure modes — silent blocks, IP
rotation, and per-provider domain sleuthing (found live: opencode-go's
opencode.ai silently blocked) — are replaced by an engine-side egress
proxy. Design:

- `internal/egressproxy`: a CONNECT + absolute-form HTTP proxy on
  loopback. Per-run Basic credentials `runID:HMAC(secret,runID)`
  (deterministic — survives engine restarts; identifies the run's policy,
  not a high-value secret). Filters BY HOSTNAME (exact or `*.suffix`)
  against a per-run allowlist; ports 443/80 only; records every denial
  (host ×count) for the run. Process-random signing secret.
- Wiring: `main.go` starts the proxy (unless APOTHECARY_EGRESS_PROXY=off),
  passes its port to the resolver (coder reverse-forwards it into every
  workspace, like the engine port) and to the engine
  (`SetEgress`/`EgressController`). The run loop sets the policy from
  `res.EgressDomains` (already = derived domains ∪ project extra_domains)
  before any workspace network use, and drops it at teardown.
- Workspace: bootstrap writes `/apothecary/proxy.env`
  (HTTP(S)_PROXY → 127.0.0.1:proxyPort with the run creds, NO_PROXY =
  loopback); every bootstrap step and both harness adapters source it, so
  git clone and agent invocations all leave through the proxy. The
  template gains an `egress_proxy` parameter → `APOTHECARY_EGRESS_PROXY`;
  when set, `init-firewall.sh` runs in PROXY MODE: OUTPUT drops everything
  except loopback (reaches the tunneled proxy + shim + code-server),
  established, and the coder gateway port. No IP pinning, no DNS, no
  per-provider guessing.
- Visibility (the point): `GET /api/runs/{id}/egress-denials` +
  a Working-card banner list blocked hosts live — a silent 30-minute hang
  becomes "⚠ egress blocked: telemetry.deepseek.com — add to extra
  domains and restart".
- extra_domains flows in unchanged (resolve.egressDomains already unions
  it → the proxy allowlist). The static provider→domain map stays as the
  auto-allowlist (now incl. opencode/opencode-go); a miss is a visible
  denial, not a hang, so no live catalog fetch on the resolution path.
- Fallback: proxy off (env, or provisioners that ignore the param) → the
  derived IP-allowlist firewall, unchanged. envcheck passes proxyPort 0.
- Not yet: proxy-mode envcheck assertions (checks still target allowlist
  mode); keeping API keys engine-side via proxy injection (future — the
  hook exists now that all egress is mediated).

## M18 follow-up — "add to egress" instant unblock

Since the proxy is engine-side in-memory state and the workspace already
routes everything through it, a run's allowlist updates live:
`egressproxy.AllowHost(runID, host)` appends to the run's policy and
clears the host's denial — the agent's next request succeeds with no
restart/re-provision. `POST /api/runs/{id}/egress-allow` does both: the
instant proxy unblock (Engine.AllowEgress) AND persists the host to the
project's workspace.extra_domains (best-effort, for future runs). The run
page's egress-blocked banner grows an "Add to egress" button per denied
host. Host validation rejects scheme/port/path; wildcards (`*.x`) allowed.

Scope note: one proxy process per apothecary instance, but policy is
per-RUN (map[runID]) — stricter than per-project. Each run's allowlist is
its project's derived domains ∪ that project's extra_domains, isolated by
per-run credentials; runs of different projects can never see each other's
allowlist. Per-project egress config already exists as
workspace.extra_domains (what the button persists to).

## M18 fix — command stages bypassed the proxy

Live bug: `git push` (a flow command stage) timed out 133s against
gitlab.com in proxy mode. Cause: only bootstrap steps and agent runs
sourced proxy.env; command stages ran raw via Envp.Exec and went DIRECT,
hitting the firewall DROP (dropped packets → long timeout, not a proxy
403). The allowlist was fine — gitlab.com is contributed by the forge
account's EgressDomains (github: github.com/api/codeload; gitlab: the
base_url host) and unioned into every run. Fix: coder.Exec sources
proxy.env for EVERY workspace command (one chokepoint — bootstrap,
command stages, agent runs all go through it). The explicit bootstrap/
adapter sourcing stays (redundant, harmless). "Add to egress" button
relabeled "Add to project's allowlist" (it already persisted to
workspace.extra_domains — the label now says so).

## Resume — "try again" from a failed stage

A run that abandons/fails at a late stage (e.g. push) no longer needs a
full restart. New forward event `run_resumed{stage_id}`: run.Apply →
Running; state.go replay un-terminals, re-enters the stage
(pos = Index(stage)), and clears all edgeCounts (fresh on_fail/on_reject
budgets) + feed/lastFailure/gateAnswer. History stays immutable — resume
is an append, not a rewind. Engine.ResumeRun validates
(abandoned|failed, has a CurrentStage, has an EnvID — abandoned/failed
runs are never reaped, so the workspace persists), appends the event, and
re-queues the run; ensureEnv reuses the existing workspace (agent commits
intact), and only the failed stage re-executes. POST
/api/runs/{id}/resume (202, same run id). UI: "Try again (<stage>)"
button on abandoned/failed runs, beside Restart (from-scratch, new run).
Verified: abandon at push → resume → same EnvID, no env destroy, run
completes; completed runs refuse resume.

## M18 fix 2 — proxy CONNECT truncated large transfers

Live: git push failed "Recv failure: Connection reset by peer" (routing
was now correct — the error moved from a firewall timeout to a proxy-side
reset). Bug: handleConnect's bidirectional io.Copy tore down BOTH sockets
the instant either direction ended, RSTing an in-flight transfer in the
other direction (clone downloads and survives; push uploads more and
trips it). Fix: half-close — each direction CloseWrite()s its own write
side on EOF and the full close waits for BOTH copies to finish; plus
forward any TLS bytes the client pipelined into the hijack buffer before
the 200. Regression test: 2 MiB bidirectional round-trip through the
tunnel.

## M18 fix 3 — egress broke after an engine restart (stale proxy.env + dead tunnel)

Live: git push "Failed to connect to 127.0.0.1:<port>" after rebuilding
the engine and hitting Try again. Root cause was a latent restart bug the
proxy exposed: the reverse tunnel is a subprocess that dies with the
engine, and the ensureEnv REUSE path (recovery/resume of a running
workspace) refreshed run.env but never re-established the tunnel — so the
workspace's engine/proxy loopback forwards were gone. The proxy also binds
a random port with a process-random cred secret, so a restart invalidated
the workspace's proxy.env. Fixes: (1) ports.WorkspaceReattacher optional
capability — coder.Reattach re-establishes the tunnel (waitReady +
ensureTunnel, idempotent); ensureEnv reuse path calls it. (2) reuse path
also rewrites proxy.env (current port + creds) via a shared
encodeEnvFile. This also fixes ask_human across restarts (same dead-tunnel
cause). Fake provisioner doesn't implement Reattach (no tunnel) — no-op.

## Flow control: abort terminal, on_timeout, then-chains, pause/play, rescue gates

Five additions making every way a run ends or loops an explicit, visible
part of the flow document (extends DESIGN3 §2.1's registry as designed):

- **`abort` terminal**: reserved edge target mirroring `done` (ids may
  shadow neither). `on_reject: abort` (scalar sugar or `{to: abort}`)
  stops the run deliberately → `run_abandoned` with the feedback in the
  reason — the explicit twin of the implicit "no edge / exhausted edge"
  abandons. `max`/`feed`/`then` are rejected on abort links; `to: done`
  is rejected on edges (only on_success completes a run). The graph
  editor renders an abort terminal node; the run-page Abort button is
  unchanged (kill switch → run_failed "aborted").
- **`on_timeout` edge kind**: one registry row + the two timeout raise
  sites (agent, command); recovery's routeAfterFail picks the kind from
  the persisted stage_failed reason. Absent on_timeout falls back to
  on_fail — existing flows behave identically. The agent timeout now
  passes the cancelled attempt's output tail as feed content (edges
  without feed still record "").
- **`then` chains (retry-then-escalate)**: an Edge is now a chain; each
  link keeps its own cumulative counter (edge_taken gains `depth`,
  replay keys edgeCounts by from|kind|depth; pre-chain events land on
  depth 0 unchanged). A full link falls through to its `then`; an
  exhausted chain abandons with the combined budget ("max 2+3").
  Counters are cumulative, not per-visit — total loop work stays bounded
  by the sum of maxes (plus flow budgets, as before).
- **pause/play**: `run_paused` / `run_unpaused` events + `paused` state
  (non-terminal; Recover skips it). Pause = cancel the loop with
  errPaused — a controlled crash; the goroutine exits, freeing a §14
  slot, and the workspace is kept. Play re-queues the run; replay
  re-enters mid-stage via the existing crash-recovery paths (agent
  sessions resume, commands re-execute, gates re-arm without a new
  ping_raised — run_unpaused carries `waiting` so the projection lands
  back on waiting_on_human). Play resets NO retry budgets (contrast
  ResumeRun's "try again"). Abort now also works handle-less (paused or
  queued runs): dequeue under the lock, append run_failed directly.
  stageAgent gained the same ctx-cancellation guard stageCommand had, so
  a cancel racing the harness result is never misread as harness_error.
- **Rescue human gate** + `input: {workspace: $WORKSPACE}`: parks the run
  and hands the human the workspace link (terminal > ide > first, from
  env_ready; runState.links now also set on the live provision path).
  approve = continue on_success, reject = on_reject (typically abort),
  feedback feeds along. Counted as a WANTED ping (gatePingType).

The shipped example (artifacts/flows/v2.yaml) exercises all of it:
test on_fail → build ×5 then debug (a gate showing $WORKSPACE) → abort on
reject; on_timeout self-retry then build; final_gate on_reject: abort.
Locked by flow.TestReferenceFlowParses.

## Compiled-in defaults (internal/builtin): flow "default" + agent library

The product now ships working defaults in the binary instead of requiring
manual seeding: internal/builtin embeds flows/default.yaml (the plan-gated
build with manual-debug rescue) and the five agents it references
(planner, builder, committer, reviewer, redactor). Nothing is ever written
to postgres; the layering is read-through with DB-wins shadowing:

- Engine: builtin.FlowSource(conf.FlowSource()) — a missing name falls
  back to the embedded flow, so an empty database still runs "default".
- resolve: agent names the project hasn't authored resolve to the
  embedded specs (hashed with the same content addressing); preflight and
  prompt materialization see them like any authored agent.
- confstore: project.default_flow defaults to "default" and validates
  against DB-then-builtin. DeleteAgent's referenced-guard allows deleting
  an override whose name has a builtin fallback (that IS reset-to-default)
  and keeps refusing when deletion would break the default flow.
- API: flows/agents responses carry builtin / shadows_builtin flags;
  GET serves embedded content when unshadowed; saving under a builtin
  name creates the editable override (copy-on-write); deleting the
  override resets; deleting an unshadowed builtin is a 409.
- UI: "built-in" badges, editor banner, "Reset to built-in" delete label,
  AgentsPanel "Customize" instead of Edit/Delete for builtin rows.

Upgrades propagate by rebuild (a seeded row would freeze at seed time);
runs keep pinning content hashes, so the existing drift warning covers a
binary upgrade changing the default. examples/agents/ remains as
documentation; the embedded copies under internal/builtin are canonical.

## Devcontainer provisioner (provisioner/devcontainer)

The TODO called for a DevPod adapter; research (2026-07-21, verified against
GitHub) found loft-sh/devpod abandoned-in-fact (no development since 2025-06,
v0.7 never shipped, maintainers silent) and the niche's only maintained
pieces being Coder's devcontainer support and the reference
@devcontainers/cli. Decision: the repo-devcontainer goal ("run whatever the
target repo defines") is implemented on **@devcontainers/cli + docker CLI**
(`internal/adapters/devcontainer`); a Coder+envbuilder template variant
remains the server-shaped alternative if ever needed.

Decisions and deviations:

- **Clone-first inversion.** The workspace definition lives in the repo, so
  the adapter materializes an engine-side checkout before `devcontainer up`
  (bind-mounted as the workspace folder). `ports.EnvSpec` gained
  `GitUser/GitPass` for exactly this: the credential rides a temp
  `credential.helper=store` file (never argv, never the container); the
  in-workspace credential entry is still bootstrap step 3's. The §11
  `$HOME/repo` contract is preserved via a root-init symlink onto the
  mounted folder, so bootstrap's idempotent clone step no-ops unchanged.
- **Repos without a devcontainer** fall back to a generated config around
  the §8 pipeline image (`--override-config`); repos with one are honored
  untouched. Custom `workspace.template` is rejected at kind construction.
- **Exec transport** is `devcontainer exec` (script on stdin): exit codes
  propagate verbatim and stdout/stderr stay separated — no coder exit-255
  ambiguity. Container state lives in docker labels (`apothecary.env=<ws>`);
  status/destroy/links go through `docker ps`/`rm`/`inspect`.
- **Engine bridge instead of tunnels.** Hardened docker hosts drop
  container→host IP traffic entirely (verified live: even a bare listener on
  the bridge gateway IP is unreachable), so workspace→engine rides
  docker-sanctioned channels only: engine-side unix listeners in the
  bind-mounted `/opt/apothecary` pump to the engine's loopback, and a ~30
  line `relay.mjs` (`docker exec -d node …`) re-exposes them on the
  workspace's own `127.0.0.1`. Loopback parity holds, so run.env/proxy.env
  are provisioner-invariant and the engine needed no URL-rewriting surface.
- **node is a hard workspace requirement** (shim + relay; harnesses are
  npm-distributed anyway). Missing node fails Provision with guidance to add
  `ghcr.io/devcontainers/features/node`. Auto-injecting the feature was
  rejected for MVP: it could shadow the repo's own node version.
- **Security posture (deliberate):** repo-authored containers get no
  NET_ADMIN and no apothecary firewall — egress is OPEN apart from
  engine-side proxy filtering of proxy-honoring tools. The
  `ports.EgressOpen` marker declares this; envcheck skips the firewall
  invariants with an honest reason instead of false-failing, and the tunnel
  probe stays valid (it now validates the relay). Kernel-level enforcement
  (injected devcontainer feature contributing capAdd + firewall, or an
  internal-network egress sidecar) is the recorded follow-up.
- **Concurrent runs and declared ports:** headless @devcontainers/cli does
  not publish `forwardPorts` (an IDE-session feature), so N parallel
  workspaces cost zero host ports; declared ports surface as `app` links on
  the container's own IP (per-instance unique, engine-host reachable).
  `appPort` and fixed compose `ports:` DO publish and can collide across
  concurrent runs of the same repo — the second `devcontainer up` fails
  loudly; remapping is a follow-up.
- **Known gaps:** harness binaries are not installed into repo-authored
  devcontainers (InstallLayer belongs to the image pipeline) — the repo's
  devcontainer must provide them for agent stages; compose side services
  are not reaped by Destroy (unlabeled by the CLI); `devcontainer stop`
  does not exist upstream, so there is no adapter-initiated stop (Destroy
  only), matching the port's surface.

Verified live against @devcontainers/cli 0.86.0 + docker 29.5 (unit fakes +
a `-tags live` end-to-end test: provision from a real repo, exec, file
round-trips, loopback engine probe through the relay, links, destroy).

## Static shim binary (retiring node from workspaces)

The two in-workspace node helpers (`ask-human.mjs` MCP server, the
devcontainer `relay.mjs`) are replaced by ONE static Go binary,
`workspace/shim/` (`shim ask-human` | `shim relay`), so repo-authored dev
containers no longer need node (the earlier hard "workspace has no node"
failure is gone). Decisions:

- **Built by docker, not the host toolchain.** `wsimage.EnsureShim`
  (content-addressed `apothecary/shim:<sha16>`, `docker build` →
  `create`/`cp`/`rm` extract, cached under `<RunsDir>/shim-cache/`) compiles
  the embedded source in a `golang:alpine` stage; the coder base image
  (`workspace/Dockerfile`) does the same via a multi-stage `COPY
  --from=shimbuild`. Same daemon that runs workspaces ⇒ arch always matches.
  `go:embed` of a prebuilt binary would be a chicken-and-egg; a nested
  `go.mod` would break embed, so the shim is a normal `package main` in the
  main module (`go mod init` happens inside the builder image).
- **wsimage now walks subdirectories** (`baseContextHash`/`ensureBase` used
  `fs.ReadDir(".")` and silently skipped dirs) — required to hash + ship the
  embedded `shim/` tree.
- The shim keeps `ask-human.mjs`'s protocol byte-for-byte (204 re-poll via
  `X-Ping-Id`, 401 → re-read run.env, 60 s backoff cap, 10 min unreachable
  limit, the `other —` rendering) and gains real unit tests the `.mjs` never
  had (httptest for the MCP loop, real sockets for the relay). MCP
  registration moved from `["node","…ask-human.mjs"]` to
  `["/opt/apothecary/shim","ask-human"]` in the opencode/codex ADAPTERS
  (`internal/adapters/{opencode,codex}`), not the kinds. **node stays in the
  pipeline image** — the npm-distributed harnesses still need it; only the
  shim's own runtime dependency died.

## Dynamic workspace-port exposure (browse the app under development)

A human can publish a port the workspace app listens on and open it in a
browser; N concurrent runs, zero fixed host ports. New optional
`ports.WorkspacePortExposer { ExposePort, AppPorts }` returning a
`ports.PortConn` (dial + close), plus `internal/portrelay.Manager` (one
kernel-allocated engine-host listener per (run, port), pumping browser
conns through the PortConn). Applied to BOTH provisioners.

- **Ownership is the engine's, not the adapter's.** Adapters are
  refcount-closed on config edits (`internal/resolve`), so a forward
  process parented in an adapter instance could be killed by an unrelated
  edit — the capability hands the engine a `PortConn` it owns for the run's
  life.
- **Never re-emit `env_ready` for dynamic links.** `run.Apply` would clobber
  `WaitingOnHuman`/`Paused` → `Running` (durable corruption), and an
  out-of-band append races the run loop's single-writer seq. Live exposures
  live in an in-memory registry served by `GET /api/runs/{id}/ports`; only
  provision-time auto-exposures (declared `forwardPorts`) fold into the
  single `env_ready` (appended on the writer goroutine). The CPUI merges the
  live registry over the event's `app` links (its 2.5 s poll), so restarts —
  which re-expose on fresh ports via the recovery path — stay correct.
- coder: supervised `coder port-forward <ws> --tcp <alloc>:<port>`
  subprocess (reuses the tunnel-supervision pattern; verified against Coder
  v2.35). devcontainer: `shim relay --reverse` over a bind-mounted unix
  socket (host→container IP is blocked on hardened hosts, so the reverse
  relay mirrors the engine bridge). The dead container-IP `app` links the
  devcontainer adapter used to emit are gone — unreachable off-host.
- API `POST /api/runs/{id}/expose-port`; `cmd/apothecaryd` binds listeners
  on the CPUI's own interface (loopback by default; the deploy already runs
  `network_mode: host`). Same no-auth trust level as the CPUI itself —
  documented. This SUPERSEDES the earlier appPort/compose-collision
  follow-up: exposure needs no published host ports.

## Devcontainer egress firewall (parity with the coder path)

The §11 default-deny egress firewall now covers repo-authored dev
containers. CLI gates verified live (@devcontainers/cli 0.86): a **relative**
`--additional-features './apothecary-firewall'` local ref resolves, and a
feature's `capAdd`+`entrypoint` apply on image, Dockerfile AND compose
configs.

- **Two mechanisms, one script.** Repos with no devcontainer use the §8
  pipeline image (which already carries iptables + init-firewall.sh + the
  sudoers grant): `writeFallbackConfig` adds `capAdd: [NET_ADMIN]`,
  `containerEnv` (the `APOTHECARY_*` inputs) and a BLOCKING
  `postStartCommand: sudo init-firewall.sh` (up waits on lifecycle hooks →
  no race). Repos WITH a devcontainer get an injected feature
  (`workspace/feature/`, materialized into
  `.devcontainer/apothecary-firewall/` + `.git/info/exclude`) whose
  container-start entrypoint sources `/opt/apothecary/firewall.env` (written
  per run into the shim mount — never feature options, which would bust the
  build cache) and runs the SAME `init-firewall.sh` verbatim. `waitReady`
  probes `/run/apothecary-firewall.done` when firewalled; fail-closed writes
  no lock, so a firewall failure times out into an honest `env_failed` with
  the container left up for inspection. Loopback is ACCEPTed first, so the
  unix-socket bridge/relay survive the DROP (`APOTHECARY_CODER_PORT=1`
  closes the Coder-only gateway hole).
- **`ports.EgressOpen` marker → `ports.EgressReporter` (per-env method).**
  The firewall varies per env, so a type-level marker no longer fits: the
  devcontainer adapter firewalls image/Dockerfile configs but leaves
  **compose** open (its primary service reaches sibling services by name,
  which default-deny would break) and honors a per-project `firewall: off`
  switch. It records the fact in memory + an on-disk marker (survives engine
  restart) and reports it; `envcheck` keys its firewall invariants on the
  reported value (skips honestly for open envs), defaulting to closed when
  the capability is absent (coder).
- **Limits:** compose stays egress-open (feature works, but firewalling
  would break sibling-service traffic — a future enhancement could allow the
  compose subnet); a non-root container user makes the entrypoint's iptables
  fail closed; a base image with no apt/apk/dnf fails the feature build (use
  the firewall-off escape hatch). Feature injection adds a first-provision
  image build (~apt install iptables), docker-layer-cached thereafter.

All three verified live (`-tags live`): the shim relay end-to-end on a
node-LESS `debian:bookworm-slim`; a full port-exposure round-trip
(portrelay → reverse relay → in-container → forward relay → engine); and the
firewall dropping foreign egress + DNS while loopback/relay stay alive.

## Per-project image mirror in the gateway + keep-warm (TODO 19 b+e)

Every run's inner dockerd used to cold-pull everything (repo base image,
feature layers, compose siblings) into its own per-run `/var/lib/docker`
volume, which dies with the run. The gateway is already per-project and
already multi-homed onto every run's `--internal` network, so it is the one
place a shared cache can sit with **no new networking**: its nft ruleset only
hooks `prerouting`/`forward`, so a gateway-local listener is reachable from
every run and the cache's own upstream fetch (gateway-originated, `output`
path) needs no rule either.

- **The mirror runs INSIDE `apo-gw-<project>`**, not as a sibling container:
  the distribution `registry` binary is COPYed into the gateway image from
  `registry:2` (digest-pinned — `gatewayHash` covers the Dockerfile *text*,
  not upstream content, so a re-tag would otherwise change the image without
  changing the tag) and supervised as a child by the gateway daemon
  (`workspace/gateway/registry.go`, stdlib `os/exec`: capped backoff on exit,
  SIGTERM-restart when the config file changes since the registry has no
  config hot-reload, child logs relayed under a `registry:` prefix). The
  `--mirror-config` flag is opt-in, so a gateway without it behaves exactly
  as before.
- **Cache = named volume `apo-gw-<project>-cache`** at `/var/lib/registry`,
  deliberately NOT under `gwDir` (which the reap used to delete). Config
  (`registry-config.yml`, `proxy.remoteurl` = Docker Hub) rides the existing
  identical-path gwDir bind mount, written only when the content changed so
  a provision doesn't restart the registry child on every pass.
- **Inner dockerd** gets `--registry-mirror=http://<gwIP>:5000
  --insecure-registry=<gwIP>:5000` (plain HTTP inside the `--internal` net).
  `--registry-mirror` applies only to docker.io, matching the single-upstream
  proxy; a down mirror is not a failure mode (dockerd falls back to upstream).
  `GatewayOff`/gatewayless runs get no flags.
- **Keep-warm (e):** the refcount reap in `DestroyRunEgress` is gone —
  destroying a project's last run keeps the gateway, its policy dir and its
  cache — plus `--restart unless-stopped` so it survives daemon restarts (a
  restarted container is a fresh netns, so stale nft rules are impossible).
- **Upgrade path replaces the reap.** The reap used to be how a new gateway
  build shipped. `ensureGateway` now resolves the image tag on *every* path
  (the warm path skipped it before) and recreates a gateway whose
  `.Config.Image` is stale — but only while the project has no *running*
  sandbox, since `rm -f` would sever egress for every attached run. Stopped
  sandboxes baked `--dns`/`--registry-mirror` at the old gwIP and can't be
  re-flagged by `docker start`, so the replacement is re-attached to each run
  network at its previous address (`network connect --ip`). Only the image
  tag is compared: argv changes here don't trigger a recreate on their own,
  but they always ride a source/Dockerfile change that bumps the tag.
- **Accepted carve-out:** cache misses are fetched by the gateway itself, so
  they bypass the SNI/Host policy — a project denying `registry-1.docker.io`
  can still pull public Hub images through the mirror. Documented in README;
  folding Hub auth into (g) credential brokering is the future step (the
  registry `proxy` block takes username/password).
- **Cache eviction:** `registry:2` proxy TTL is hardcoded at 168h and
  historically imperfect; `storage.delete.enabled: true` is what lets it
  evict at all. Blessed cleanup is dropping the volume (pure cache, recreated
  on the next ensure) rather than running `garbage-collect`. `registry:3`
  with a configurable `proxy.ttl` is the upgrade if it ever matters.

Verified live end-to-end: run 1 cold-pulls `alpine:3.20` through the mirror
(blobs land in the volume, `/v2/_catalog` lists `library/alpine`); the run and
its volume are destroyed; run 2 pulls the same tag in ~2s **with
`registry-1.docker.io` denied by policy** — proving both the cross-run warm
cache and the carve-out, while the sandbox's own direct `wget` to Hub is reset
and recorded as a denial. `pkill registry` in the gateway is restarted by the
supervisor within a second; touching the config restarts the child.

### (h) assessed and dropped — the relay stays

Routing the engine↔workspace relay through the gateway was considered and
rejected. The forward half (workspace→engine) would work — the sandbox
already routes to gwIP, and a gateway listener could hand off to a unix
socket on the gwDir mount, collapsing N per-run relays into one per-project
hop — but the socket seam remains either way (container→host-IP is blocked on
hardened hosts), so it removes nothing structural. The reverse half
(engine→workspace port exposure) is the blocker: inner containers sit behind
the inner dockerd's NAT on subnets that collide across runs and that the
gateway has no route to, which is precisely why `ExposePort` runs the relay
*inside* the workspace container. And `firewall: off` runs have no gateway at
all, so the unix path would have to be kept as a fallback regardless.
