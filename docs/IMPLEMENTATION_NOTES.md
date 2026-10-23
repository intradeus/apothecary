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

## Ticket sources: import from Jira (new port `ticketsource`)

A ticket can now be **copied from an external tracker** instead of typed in.
The shape is the usual one — port (`ports.TicketSource`), adapter
(`internal/adapters/jira`), kind (`kinds.Jira`, registered under the new
`ticketsource` port) — plus a **ticket factory** (`internal/tickets`) that
both creation paths go through, so an imported ticket is indistinguishable
downstream from a hand-written one.

Decisions worth recording:

- **The port is read-only.** It has `Test/Search/Fetch/OpenAttachment` and no
  write surface: apothecary never transitions an issue, comments on it, or
  syncs back. The import is a *copy taken once* — title, body and attachment
  bytes land in apothecary's own storage, and `tickets.source_{id,kind,key,url}`
  record where they came from. `source_kind` is stored next to `source_id`
  (and there is no FK) precisely so a ticket survives deleting the
  integration. *(Amended: the read port stays as it is; write-back arrived
  later as an optional capability a flow opts into stage by stage — see
  "Tracker actions" below.)*
- **Not a project reference.** Unlike forge/provisioner/harness, a ticket
  source is picked *per import* on the ticket form, never bound to a project.
  A tracker outage can therefore only fail an import — it can never stall a
  run, and `confstore.validateProject` gained nothing.
- **Attachments go through the ordinary artifact store** (`Save`, so the MIME
  allowlist and size cap apply), landing as `ticket_goal` scope — the same
  thing an upload becomes, hence delivered into every run of the ticket. A
  file that is too large or disallowed is *skipped with a named warning*
  rather than failing the import; the create response carries `warnings[]`
  and the form makes the operator acknowledge them before navigating on.
- **Duplicate imports are refused** (409 naming the existing ticket): two
  tickets tracking one issue diverge silently, and it is nearly always a
  mis-click.
- **Jira's two shapes are absorbed in the adapter.** Descriptions arrive as
  ADF (JSON node tree) on API v3 and as wiki markup on v2 — `adf.go` renders
  both to markdown so nothing past the adapter sees vendor markup, degrading
  unknown node types to their children rather than dropping them. Search tries
  Cloud's current `POST /search/jql` and falls back to the classic
  `GET /search` on 404/410/405 (Data Center, older Cloud). Auth is Basic
  email+token, or Bearer when the email is left empty (DC PAT).
- **Attachment URLs are re-checked against the configured site** before the
  credential is attached: the download URL comes from the tracker's own
  response, and a tracker must not be able to aim an authenticated engine
  request at a host of its choosing.
- Free text becomes JQL in one place (`Client.JQL`): an issue key looks the
  issue up, a `jql:` prefix passes through verbatim, empty means "assigned to
  me and unresolved", anything else is a text search — all ANDed with the
  integration's optional scope filter.

`serve -dev` seeds a fake tracker (`kinds.FakeTicketSource`, two issues and an
attachment) so the whole flow is exercisable with no Jira.
- **Setup prose is descriptor data, not UI copy.** `kinds.Descriptor.Help` and
  `kinds.Field.Help` were added so a kind can carry its own "where do I get
  this credential" instructions; `IntegrationForm` renders the descriptor help
  in a `<details>` (open when adding) and linkifies bare URLs. The Jira help
  names the *exact* least-privilege token scopes — classic `read:jira-user` +
  `read:jira-work`, or the granular equivalents — precisely because
  Atlassian's scoped-token picker offers dozens and the four calls this
  adapter makes (`/myself`, `/search/jql`, `/issue/{key}`, attachment content)
  are all reads. Data Center PATs have no scope picker at all (they inherit
  the account), which is worth saying out loud rather than letting an
  operator hunt for it. The JQL field is labelled "search filter", not
  "scope", so the word only ever means the token's permissions.

## Ping attachments are snapshotted, not read live

`ask_human`'s attachments (and a gate's reviewed document) were the one thing
the event log referenced without owning: `payload_refs` held repo-relative
paths and `PingArtifactAt` read them out of the live workspace. Two bugs fell
out of that, and they are the reason this changed:

- **The reference outlived the referent.** Once the workspace was reaped (or
  the run archived) an answered ping's attachment was simply gone, so the
  inbox could no longer show what a decision had been made about — in a system
  whose stated premise is that events are the source of truth.
- **It answered the wrong question.** The agent resumes the moment a ping is
  answered and often rewrites the very file it asked about, so re-opening the
  ping showed the file's CURRENT content rather than what the human approved.
  The audit trail lied, quietly.

Now `publishPingPayload` captures each non-URL ref at raise time through the
existing snapshot path (redacted, `Capture`d, an `artifact_published` event) as
scope `ping_payload`, keyed by ping id with the ref verbatim in `OutputName` —
which is what the read path matches on, since two refs can share a basename. No
schema change: the artifacts table already carries `ping_id` + `scope`.

Reads prefer the snapshot and need no live ping — the rows are keyed by ping
id, so an answered ping stays readable after the engine's in-memory registry
drops it, which is precisely the case that used to 404. The live workspace read
remains as the fallback for pings raised before this and for a capture that
failed. `ping_payload` joins the stage scopes in surviving `DeleteForRun`
(`artifacts.IsAuditScope`): what a human was shown when they approved something
is audit trail, not run input.

Naming, while we were here: "attachment" is the human/inbound file,
"snapshot" the engine-captured one. `GET /api/pings/{pid}/artifacts` still
addresses by ref (the ping's own name for it) and now marks which entries are
`durable`, with the artifact id for a direct link.

## DESIGN-FILES authored — the artifact system redesign (design only)

`docs/DESIGN-FILES.md` is the target architecture for the file subsystem:
"artifact" retreats to the flow language; storage splits into **attachments**
(human input; ownership structural — ticket/run/answer columns with CHECKs)
and **snapshots** (engine-captured record; CHECK'd kind output|log|ping_file)
over a **content-addressed blob store** (sha256 identity, dedup, verify-on-
read, mark-and-sweep GC, no refcounts). Events become the sole authority for
run-scoped facts (`snapshot_captured` gains the ping id today's event lacks;
the snapshots table becomes a rebuildable projection). Ten verified
conflations dissolve by construction — including the ticket-attachment blob
leak, snapshot re-delivery into workspaces, finished runs listing zero ping
files, and inline-served workspace-authored HTML (trust-class headers).
**Nothing is implemented yet** — four shippable phases are specified in §15;
current code is unchanged.

## Opt-in unprivileged DinD sandbox (TODO 19 c)

`APOTHECARY_SANDBOX_RUNTIME` (deployment env, §1) names the OCI runtime each
run's DinD sandbox is created with. Unset — the default — is byte-identical to
before: `docker run --privileged docker:29-dind`. Set to `sysbox-runc` (sysbox
installed on the **host**) and `createSandbox` emits `--runtime` *instead of*
`--privileged`; the two are mutually exclusive, since sysbox rejects privileged
containers and keeping the flag would defeat the point.

What it changes, concretely: `--privileged` gives the sandbox all host devices,
unmasked `/proc`+`/sys` and no seccomp, so sandbox root **is** host root — and
the paths there are ordinary, not exotic: a repo's `devcontainer.json`
`runArgs` or a compose `privileged: true` run verbatim by design (see the
`--id-label` comment in devcontainer.go), and `Rebuild` re-provisions from the
checkout the agent has been writing to all run. From host root everything
follows: every other run's folder under `RunsDir`, the mounted host docker
socket, and `APOTHECARY_MASTER_KEY` out of `docker inspect` — i.e. every
integration secret. Under a user-namespace runtime the same compromise yields
root in a userns mapped to an unprivileged host uid, with only that run's own
folder bind-mounted: the blast radius becomes what the run already legitimately
holds. Egress is unaffected either way — enforcement was always outside the
sandbox.

**Opt-in, never auto-detected.** Auto-detection would make a run's isolation a
property of whatever happened to be installed on the machine; one env var makes
it readable off the deployment. The cost is that a typo would otherwise surface
minutes into a provision as docker's bare "Unknown runtime specified", so the
adapter probes `docker info --format '{{json .Runtimes}}'` before creating
anything and fails with the variable name plus the runtimes the daemon actually
has. Only success is cached (`runtimeOK`) — a transient docker blip must not
wedge the provisioner for the process lifetime. `Test()` runs the same probe,
so the integration's Test button is where a missing host install shows up.

**Limits (researched, not assumed):** sysbox is Linux-only — it installs
systemd units on the host, so it cannot go into Docker Desktop's VM; macOS and
Windows keep the privileged sandbox (Docker Desktop *Business* gets the same
technology transparently via Enhanced Container Isolation, which is not
something we can drive). Official packages are Ubuntu/Debian, with
Fedora/Rocky/Alma/CentOS/Amazon Linux listed compatible and everything else
building from source; kernel ≥5.12. Sysbox's own docs steer away from
`docker:dind` images (breakages at dind 20.10.7 and 27.4.0; v0.7.0 added
openat2 trapping so newer runc works inside), which left `docker:29-dind`
under sysbox as the one thing to verify live — done below.

**Verified live (2026-10-19) and the default decision.** Host: Arch, kernel
7.2.11, docker 29.9.1, sysbox-ce 0.7.1 (registered in daemon.json with
`--no-kernel-check`, Arch being an unlisted distro), sandbox image
`docker:29-dind` (dockerd 29.6.2 inside). Daemon level, mirroring
`createSandbox` by hand: the inner dockerd boots in ~1s (privileged: ~0.7s)
and listens on the parity socket; the sandbox's init is host uid 165536
(`uid_map 0 165536 65536`) with a 16-entry `/dev` where the privileged
sandbox sees every host device; the parity bind mount is an ID-mapped mount,
so the inner socket surfaces on the host as `root:2375 0660` and inner-root
writes land as host root — byte-identical to the privileged sandbox, which is
why the engine (root in deploy/) needs no change and why a uid-1000 developer
cannot talk to the inner socket under either runtime. Inner pulls, bind mounts
of the run folder, buildkit builds, the egress `ip route replace` + ipv6
sysctl, an inner `--privileged` container (full CapEff but still inside the
userns: sysrq read-only, the same 16 devices) and a depth-2 dind all work.
Provisioner level: `TestLiveLifecycle` (`-tags live`) passes end-to-end under
`sysbox-runc`, without and with the egress gateway (--internal network,
gateway DNS, pull-through mirror, default route via the gateway): provision →
exec → file round-trips → loopback bridge → port exposure → destroy, with the
sandbox inspected as `privileged=false runtime=sysbox-runc`. The live test
had rotted since the DinD move (stale `wsimage.New` signature; `t.TempDir()`
RunsDir, which the dind guard rightly rejects; a host-daemon `docker exec`
against a workspace that now lives on the inner daemon) — it is repaired and
now takes its RunsDir, sandbox runtime and gateway switch from the
environment, skips unless root, and asserts the sandbox posture; the recipe
to run it inside the engine image is in its header.

Decision: it **stays opt-in**. It cannot become the default because it is a
host-level install we do not ship (Linux-only, distro packaging, a daemon.json
entry), and auto-detection was rejected above so a deployment's isolation
stays readable off one variable. What changes is the recommendation: on a
Linux host, set it. TODO 19 is closed.

## DESIGN-FILES implemented — attachments + snapshots replace "artifacts"

All four phases of `docs/DESIGN-FILES.md` are in:

- **filestore + schema (0021/0022):** `internal/filestore` (content-addressed
  blobs under `<RunsDir>/blobs/<sha[:2]>/<sha>`; `Attachments` = upload
  policy, `Snapshots` = capture policy, `Mem` = the one shared test DB);
  `attachments` + `snapshots` tables with structural ownership / CHECK'd
  kinds; `internal/artifacts` and the `artifacts` table are gone, as are
  `tickets.artifact_ids` and `pings.payload_ref(s)` (now typed `files`+`link`
  columns). Blob deletion happens ONLY in the mark-and-sweep (never-unlink
  rule; 1h mtime grace closes the write race), with the live set from
  `pgstore.LiveBlobSHAs` honoring snapshot content retention.
- **Backend swap:** engine holds two narrow ports (`AttachmentStore`,
  `SnapshotStore` — the engine only WRITES snapshots; the ping-read trio and
  the live-workspace fallback are deleted, reads live in httpapi against the
  concrete stores). `engine/artifacts.go` split into `deliver.go` (0444
  read-only delivery, delete-then-write on re-delivery) and `capture.go`
  (ONE capture funnel; scrub is a required argument). Events:
  `artifact_published` → `snapshot_captured` (gains ping_id/mime/truncated/
  filename — everything a rebuild needs), `ping_raised` carries typed
  files/link (the five HasPrefix("http") sites are gone), `ping_answered`
  carries attachment_ids, `run_created.ticket_attachment_ids`. The snapshots
  table is a PROJECTION: projectTx folds snapshot_captured idempotently and
  rebuild-projections TRUNCATEs + re-folds it (blobs untouched).
- **API v2 + UI:** `/api/attachments` (+`/limits`), `/api/attachments/{id}`,
  `/api/snapshots/{id}` (ONE streaming serve helper: verified reads, nosniff,
  no-script CSP, active types forced to download — closes the inline-HTML
  hole), `/api/runs/{id}/files`, `/api/pings/{pid}/files` (server-side join;
  finished runs answer from the projection), `/api/tickets/{id}/attachments`;
  `attachment_ids` in every body. Stage logs serve from log snapshots — the
  runs-dir `<stage>-<iter>.log` files and `writeStageLog` are retired. UI:
  run-page "Files" panel over one fetch, FileChips replaces ArtifactList,
  AttachFiles mirrors the upload limits inline, PlanReview single-fetch with
  "not captured" visibility, answered pings render the reviewed doc from its
  snapshot, Settings gains the Storage & retention group
  (`retention.orphan_uploads_after`, `retention.snapshots_after`).
- **Vocabulary:** delivery dir is `.apothecary/attachments/`; repo-root
  reference dir moved to `examples/flows/`; flow-language.md §5 gained the
  terminology note (artifact = flow word only) and `snapshot_captured`;
  the retired `agentspec.Parse` importer and the stale `.apothecary/agents`
  comment are gone. Env knobs: `APOTHECARY_UPLOAD_MAX_BYTES` /
  `APOTHECARY_UPLOAD_MIME_ALLOW` (legacy ARTIFACT_* names still read) +
  `APOTHECARY_SNAPSHOT_MAX_BYTES`.

Old events decode as inert history: an event type the binary does not know
(`artifact_published`, or anything a future rename retires) comes back as
`event.Unknown` — every projection's type switch falls through it, SSE replay
serves the raw payload verbatim, and recovery / `rebuild-projections` work
across the cutover instead of bricking the boot on the first pre-cutover run
(the original strict decode did exactly that). Old runs simply render without
file lists (0022 dropped their rows); purging the dev DB remains the clean
option, never a requirement. A known type with a garbage payload still errors
loudly — that is corruption, not version skew.

## `ask_human` stops blocking: the wait moved into the engine

Found live, and worth stating plainly because the failure was silent. A
planner asked a clarification (`/health` and `/healthz`, or just `/healthz`?),
opencode killed the MCP tool call at exactly 60.06 s with
`MCP error -32001: Request timed out`, handed the model the error — and the
model did the reasonable thing with an error: it guessed, wrote the plan, and
finished the stage. Six seconds later the human answered a question whose
caller no longer existed, and a plan nobody had approved sat in the gate
looking finished. Nothing in the timeline said any of that had happened.

The shim was not at fault: it long-polled forever, exactly as designed. The
design was. **A blocking MCP tool call is the wrong place to park a human.**
The spec says so itself — implementations SHOULD time out requests, MAY reset
the clock on progress notifications, and SHOULD enforce a maximum regardless
(MCP 2025-06-18, Lifecycle §Timeouts). Elicitation would not have saved us
either: the outer `tools/call` timer keeps ticking under it. What lets Claude
Code wait indefinitely is not a protocol feature — it is that the question
never leaves the harness's own control channel (`canUseTool` is an in-process
promise; opencode's permission raise and codex's `execCommandApproval` are
server→client requests on their own event loops). Every one of those is
harness-specific. We need one mechanism that works on all of them.

So the question became **turn-ending**, and the wait moved to the only party
that can hold it: us.

1. The shim posts the ping with `hold_ms` (20 s) — a GRACE period, not the
   wait. A human already in the inbox answers in-band and the agent never
   loses its turn.
2. On 204 the tool returns *success* with `postedNotice`: your question is
   posted, END YOUR TURN, the answer arrives as your next message with your
   work intact. Success, not an error — an error is what invites a guess.
3. `stageAgent` now runs **one attempt across several harness invocations**.
   An invocation that returns with pings still open is an `outcomePaused`, not
   a result: the engine appends `stage_paused`, banks that leg's turns/cost,
   checks the budget, and waits in `awaitAnswers` with no timer of any kind.
   When every question is answered it appends `stage_resumed` and calls
   `Resume(session, answerReply(...))`. `iter` never moves; the stage timeout
   budget carries across legs; outputs are published only when the attempt
   actually ends.

Two properties fall out that the blocking version could not have:

- **The wait is durable.** `stage_paused` carries the harness session id, so
  replay re-enters a paused attempt, re-registers its pings and resumes into
  the same session — a restart mid-question no longer throws away everything
  the agent did before asking. (Before this, `rs.sessions` was only populated
  from `stage_completed`, so a crash mid-stage always restarted the agent.)
- **Guessing is now detectable.** `awaitAgent` used to take
  `case res := <-resCh: return res` regardless of open pings. That one line is
  what let the guessed plan through. An invocation that returns with a
  question outstanding can no longer be mistaken for a finished stage — and a
  harness that *died* mid-question still routes as the failure it is
  (`res.err != nil` is never a pause; there is nothing left to resume into).

`event.StagePaused`/`StageResumed` are deliberately generic — reason-tagged
(`ask_human`, `answered`, `engine_restart`) rather than ask_human-specific —
so any future "park this stage, continue it later" reuses them. Crash recovery
already does: re-entering a session after a restart now says so in the
timeline instead of resuming silently.

Belt and braces, cheap: the opencode MCP registration now sets `timeout`
(2 min, vs. the client default of 60 s that bit us). The shim never holds a
call that long any more, so it is only a floor under the grace hold.

One more thing the exec log showed: the model's FIRST attempt called the tool
as `ask_human` and got *"Model tried to call unavailable tool 'ask_human'.
Available tools: ask_human_ask_human, …"* — opencode namespaces MCP tools
`<server>_<tool>`. It retried and found it; a less persistent model would
simply have guessed. The default flow's prompts now name that shape
explicitly and tell the agent never to conclude the tool is missing because
the bare name failed.

## Parked, not blocked: a run waiting on a human gives up its slot

The ask_human rework above moved the wait for a human into the engine. That
made a cost visible that gates had been paying silently since M15: the wait was
held by a live goroutine. `scheduleLocked` admits runs while
`len(e.handles) < MaxConcurrentRuns` (default 3), so three runs parked on
unanswered questions blocked every other run in the queue — overnight, for as
long as nobody looked at the inbox.

The obvious idea — reuse the pause button — does not work as-is, and the reason
is worth recording: `Pause` cancels the run context, the loop exits, `finish`
prunes every ping of that run from `e.pings`, and `AnswerPing` then refuses
with `ErrRunNotActive`. A raw pause would make the question **unanswerable
until someone pressed play**, in the one flow whose whole point is that the
human answers whenever they get to it.

So: the same mechanism, tagged with why, plus a wake that needs no button.

- `run_paused` gained a `Reason`. `user` is the button and still reads
  `paused`. `waiting_on_human` is the engine parking a run that has nothing
  left to do — it keeps reading `waiting_on_human`, because a run waiting for
  YOU is not a run someone paused. `run.View.Dormant` (derived, not a column)
  carries "has no loop and is owed none" independently of the state, and
  `run_unpaused` clears it.
- `stageAgent` returns `errWaitingOnHuman` where it used to block; so does
  `stageGate` — **unless a signal is armed**. `auto_approve`/`auto_reject`
  poll an external service on a ticker and genuinely need a live loop, so
  signal-armed gates stay resident. Wait stages likewise.
- `AnswerPing` has two paths now. Live: hand it to the loop and WAIT FOR AN
  ACK. Dormant: validate against the durable projection, append `ping_answered`
  (plus `run_unpaused` when it was the last open question and no human has
  pressed pause), and re-queue the run. `validateAnswer` and `recordedAnswer`
  are shared by both, because two writers with two ideas of what a valid
  answer is would drift within a month.
- Re-entry needs no new machinery. Gates already route an answer found in the
  log (`rs.gateAnswer`), and the agent path rebuilds its resume message from
  `stage_paused.ping_ids` + the `ping_answered` events — the run had no loop
  when the human answered, so the live ping registry never saw it. `replay`
  now keeps `raisedPings`/`answeredPings`/`pausedAsks`, and the runtime path
  writes the same maps as it goes, so a resume reads identically whether the
  answer arrived in this process or is being found after a reboot.

### The three races, and what closes each

**An answer arriving as the loop exits.** It used to land in the buffered
`answers` channel of a loop nobody was draining any more: `AnswerPing` returned
nil and nothing ever happened. Answers now carry an `ack` channel; the sender
waits for it, `finish` fails anything still queued with `errNoLoop`, and the
caller falls through to the dormant path. Either the loop recorded it or the
sender is told to try the other door — never silence.

**An answer arriving as the loop starts.** Neither path owns the ping yet.
`AnswerPing` retries across both for `answerSettle` (5 s) rather than
returning "run is busy" to a human who would only click the same button again.

**An answer that beat the park.** The human answers while the agent is still
ending its turn — the tool call already handed the turn back, so the agent
cannot see it, but the ping is answered before the stage parks. `Ping.HandOff`
(set by the HTTP hold: `true` when the answer went back in-band, `false` on the
204) is what tells the loop the difference. An answer the agent never saw does
not close the question; the attempt pauses on it and delivers it on resume.
And when everything it parked on is already answered, the loop resumes on the
spot instead of parking a run with nothing to wait for.

### What had to follow

`PendingPings` folds the durable projection for runs with no loop. It read only
the in-memory registry, which after this would have hidden exactly the pings
waiting longest. The HTTP inbox was already projection-backed
(`EventPingStore` / pgstore's view), so it needed nothing — but the engine's
own list did. `Engine.SetPingLookup` wires pgstore's view in production; the
fallback folds the event log.

`Recover` skips dormant runs instead of `paused` ones, with one exception that
is the whole reason it is a rule and not a state check: a parked run whose
answers have all landed IS owed a loop. That is the crash window between
recording an answer and re-queuing the run, and skipping it would park the run
forever.

`Pause` works on a parked run (append `run_paused{user}`) — pausing something
that is waiting is meaningful: it says "do not pick this up when the answer
lands". `Play` refuses a run that is merely waiting, since playing it would
re-enter the stage and park again on the same unanswered question. And
`ping_answered` no longer forces the projection to `running`: a parked run has
no loop to put back to work, and a paused one keeps its pause.

### The behavioural change worth knowing

Answering no longer resumes a run instantly. The woken run goes back into
`e.queue` and waits for a slot behind whatever was admitted while it slept.
That is the trade: the factory keeps working while people think, and the answer
buys a place in the queue rather than a goroutine that was never released.

## One route table: the OpenAPI document, /docs, and the CPUI's client

The API surface used to be declared three times — a mux in `Handler()`, a
hand-written TypeScript client in `cpui/src/lib/api.ts`, and nothing at all for
anyone outside those two. Now it is declared once.

`internal/httpapi/routes.go` holds a table: method, path, operation id, tags,
summary, the handler, and the Go types of the request and response. `Handler()`
builds the mux from it and `openapi.go` reflects over the same table, so the
document cannot describe an endpoint the server lacks, or miss one it has —
two tests assert exactly that, both directions.

```
internal/httpapi/routes.go        one declaration per endpoint
   │  task api:spec
   ▼
api/openapi.yaml                  generated, committed, freshness-tested
   │  task api:client                     └─ served at /api/openapi.{json,yaml}
   ▼                                         and rendered at /docs
cpui/src/lib/api/generated/       machine-written, quarantined
   │
   ▼
cpui/src/lib/api/                 the anti-corruption layer
   │                                client.ts  transport + error unwrapping
   ▼                                mappers.ts wire → domain
cpui/src/lib/domain/              the frontend's own types
   │
   ▼
components                        never see a wire type
```

### What the generator had to be taught

Reflection alone produces a document that is subtly wrong, and each of these
was found by a test rather than by reading:

- **Required fields.** Nothing in a Go type says which fields are always
  present, so every property arrived optional and every mapper would have had
  to defend against fields that are in fact guaranteed. `encoding/json` already
  answers it: a field without `omitempty` (or `omitzero`) is always written.
- **Nullable collections.** Go marshals a nil slice or map as `null`, not `[]`.
  A client generated from "array, not nullable" breaks on the first empty one —
  which is what the conformance test hit on `/api/projects` (`commands: null`).
- **Custom marshallers.** `flow.DocIO` has a `MarshalJSON` that emits an
  ordered name → value mapping; reflection saw its Go fields and described an
  array of `{Name, Value}` pairs that the server never sends.
- **Named components.** Without `CreateComponentSchemas`, every schema inlines
  and the generated client is a wall of anonymous shapes with nothing to map
  from. Names collide across packages, so `typeNameOverrides` disambiguates and
  a collision is a generation-time error rather than two types quietly sharing
  one schema.

### The test that earned its place

`TestResponsesMatchTheSpec` drives the real handlers and validates the real
bytes against the generated schema (`openapi3filter`). The route table is
hand-written — it is the one place a human still says what an endpoint
returns — and a wrong declaration there is invisible: the code compiles, the
spec validates, and the client is confidently wrong. It caught
`/api/projects/{id}/branches` being declared `[]string` when it returns
`[]kinds.BranchInfo`, a bug that had already reached the mapper.

### The anti-corruption layer

The generated types are the engine's, in the engine's vocabulary (`run_id`,
`ticket_title`, `state: string`). Letting them into components means a rename
on the Go side becomes a thirty-file change, and every component learns to
handle `undefined` for fields that are guaranteed. So they stop at
`src/lib/api/`: the mappers rename, narrow open strings into the unions the UI
switches on (`RunState`, `PingType`, `SnapshotKind` — with an unrecognised
value falling back visibly instead of crashing a page), and turn absent into
`[]`. `cpui/scripts/check-api-boundary.mjs` fails the build if anything outside
that directory imports the generated client; `task api:check` runs it beside
the spec-freshness check, and `task verify` runs both.

Two deliberate exceptions live in the ACL and are commented there: the SSE
stream (`EventSource` is the browser's; OpenAPI describes the endpoint but
cannot hand us a stream) and the direct asset URLs (`/api/attachments/{id}`),
which the browser fetches itself from an `img src` or a download link.

### Swagger UI

`/docs`, from assets vendored into `internal/httpapi/swaggerui/` and embedded
with `go:embed` — not a CDN. An engine that runs behind its own egress firewall,
or on a machine with no internet, must still be able to show its own API.
`task api:swagger-ui` refreshes them.

## Flows belong to a project

The global flow was a fiction. A command stage runs `make verify`; an agent's
prompt knows where this repository keeps its migrations; an egress rule names
this project's registry. "The flow called default", shared by every project,
could only ever hold what is true everywhere — which is almost nothing. So
identity moved from `name` to `(project_id, name)`.

Two things survive the move deliberately, and they are what keep it from being
a downgrade:

- **The built-ins stay global templates.** `internal/builtin` documents belong
  to no project and resolve by name for anyone who has not saved their own.
  Saving one creates that project's editable copy — the shadowing that already
  existed, now per project. A fresh project still runs without authoring a flow
  first.
- **Copying replaces sharing.** `POST /api/projects/{id}/flows/{name}/copy`
  clones a flow into another project (optionally renaming it), which is how a
  good flow spreads when it can no longer be shared by reference. The copy is a
  new document with its own history: editing it never reaches back, which is
  the point of ownership and what the dialog says out loud. A template is
  copied through `POST /api/flow-templates/{name}/copy` instead: it belongs to
  no project, and a path cannot carry an empty `{id}` — the mux collapses the
  empty segment and the request lands on the UI's catch-all as a 405.

### The seam

`flow.Source.Get(ctx, name)` became `Get(ctx, projectID, name)` (and
`PinnedSource.GetVersion` likewise). Every engine call site already knew its
project — from a `Resolution`, a `run.View`, or `CreateRun`'s own argument — so
the change is threading, not restructuring. `MapSource` ignores the project on
purpose: ownership is a property of the STORE, not of the flow language.

### What the ordering change forces

A flow cannot exist before the project that owns it, so **a project is always
created on a built-in template** and authors or copies its own afterwards. That
inverted every fixture that used to create a flow and then a project pointing
at it, and it is why `seedDevProject` now creates the project, saves the dev
flow into it, and then re-points `default_flow`.

`flow.Rename` exists for the same reason: a document names itself and that name
is its id, so copying under a new name is a document edit. It rewrites through
the YAML node tree, so comments, key order and formatting survive and only the
top-level `name` is touched.

### Deletion, and what "immutable forever" now means

Deleting a FLOW still keeps its versions (§7). Deleting a PROJECT now takes its
flows and their history with it (`ON DELETE CASCADE`): a document naming a gone
project's commands has no reader, and `DeleteProject` already refuses while any
non-archived run exists. The run events — which carry `flow_name` and
`flow_hash` — are untouched, and they remain the audit trail.

The delete guards narrowed to match: only THIS project's `default_flow` and
THIS project's live runs block a flow's deletion. Another project's flow of the
same name is a different document and never interferes.

### Migration (0023)

Existing rows are copied into EVERY project, because that is what they already
meant: a flow named X resolved for every project, so every project keeps
resolving the same document. Duplicate rows are the honest cost of the model —
attaching each flow to one project instead would silently change what the
others run. `flow_versions` gets `project_id` in its primary key: the same
content saved in two projects is two rows with one hash, because they are two
histories.

The old primary keys are dropped BEFORE the fan-out insert, not after: `name`
(resp. `hash`) is still the key while the copies are being written, so the
second copy of any flow collides with the first. Found by running the migration
against the real local database, which had two flows and one project.

### The API and the UI

Flow routes live under their project (`/api/projects/{id}/flows/...`), with two
that do not belong to one: `/api/flows` (the cross-project overview) and
`/api/flow-templates` (the compiled-in library, whose `{name}/copy` adopts a
template into a project). `POST /api/flows/validate` stays global because it
is pure parsing.

The UI keeps its Flows page, grouped by project, with the templates in their own
section and a copy action on every row — cross-project cloning has one obvious
home, which it would not have inside a single project's page. The editor moved
to `/flows/[project]/[name]`.

## The devcontainer build cache

A run's inner daemon is thrown away with the run — `sandboxVolume` is keyed by
the RUN name, so `/var/lib/docker` (image store and buildkit cache alike) is
empty every single time. The per-project registry mirror on the gateway saved
the network round trip, but every run still extracted every layer and rebuilt
the devcontainer image from nothing. Measured on a real provision: **76.8s of a
89.8s devcontainer-CLI span was `docker compose build`**.

The cache now outlives the run in the one place that already does: the project
gateway. It gains a SECOND `registry serve` child on :5001 — writable, its own
storage volume — because the mirror on :5000 is in proxy mode and a registry in
proxy mode refuses pushes. The sandbox's dockerd trusts both
(`--insecure-registry`), and the build reads and writes through them.

### Two mechanisms, because the CLI has two

`devcontainer up` takes `--cache-from` and `--cache-to`, but they are not
equally supported, and the difference is invisible until you read the CLI's own
source:

- **`:cache`** — a buildkit cache manifest (`--cache-to ...,mode=max`, so
  intermediate stages too). Works for a single-container devcontainer. For a
  compose devcontainer the CLI refuses it outright ("--cache-to not
  supported.", its words); in `up` it is silently ignored, which is how the
  first attempt here looked like it worked while exporting nothing.
- **`:image`** — the built image itself, pushed after the build
  (`pushBuildCache`). The CLI always builds with `BUILDKIT_INLINE_CACHE=1` —
  including in the compose override file it generates — so the pushed image
  carries the metadata `--cache-from` restores from. This is the only export
  path a compose devcontainer has, and compose devcontainers are the common
  case here.

Both refs are offered as `--cache-from` every time; a missing one is a 404
buildkit shrugs at.

Three properties make it safe rather than clever:

- **`ignore-error=true`** on the export. A wiped, restarting or wedged cache
  registry costs a cache miss, never a failed build (verified directly: the
  export error is swallowed and the build exits 0). `pushBuildCache` is
  best-effort for the same reason — a failed push means a slower next build and
  nothing else.
- **Cache export needs a driver that supports it.** The default docker driver
  refuses ("Cache export is not supported for the docker driver") *unless* the
  containerd image store is on — and `docker:29-dind`, which the sandbox runs,
  has it on by default. Verified inside the sandbox image before building
  anything on top of it.
- **No gateway, no flags.** A provisioner without one behaves exactly as before
  rather than pointing buildkit at a registry that does not exist.

### Clearing it

`DELETE /api/projects/{id}/build-cache`, surfaced on the project page beside
its size. Empty-then-restart, in that order and both parts required: the wipe
removes the storage under the registry, and touching the config makes the
gateway's supervisor restart the child — a registry left serving manifests
whose blobs are gone would hand out cache entries that fail to pull, which is
worse than no cache.

It is a safe button by construction: the next build repopulates it, so the cost
of clearing needlessly is one slow build. That is what the confirmation says.

### The plumbing

`ports.BuildCacheController` is an optional EnvProvisioner capability (the same
shape as `EgressController`), so a provisioner that caches elsewhere — or not
at all — simply does not implement it and the API answers 503. One trap worth
recording: the gateway is keyed by project NAME, not id, because that is what
`Provision` passes it. Passing the id reads as "no cache" — `available:false`
on a project holding 800MB of cache, which is exactly how it was found.

### Rebuilding without it

A checkbox beside "Rebuild workspace" (`no_cache` on the rebuild endpoint →
`EnvSpec.NoCache` → `--build-no-cache`). Deliberately manual, and deliberately
NOT a config-change detector: buildkit's cache key already hashes what the
build actually reads — the resolved base-image digest, every instruction, every
context file — so a changed Dockerfile, devcontainer.json or compose build
config rebuilds itself. A detector of our own would hash what we GUESS it reads,
which is both weaker (a compose file outside the .devcontainer folder) and
noisier (a comment edit forcing 77s).

What a cache key genuinely cannot see is time: a floating `FROM node:22` whose
tag moved, an `apt-get update` layer from three months ago. No config changed,
so no detector would fire — a human pressing the button is the honest interface.

Two things make the checkbox true rather than decorative: `--no-cache` beats
`--cache-from` in buildkit (measured in a scratch daemon: 1s warm vs 32s with
--no-cache against the same populated registry), and the CLI drops its own
`--cache-from` when the flag is set. `--cache-to` and the push still run, so
ONE no-cache build repairs the cache for every run after it.

Worth recording next to this: the pull-through mirror's `proxy.ttl` is unset,
and the distribution default is 168h. For a floating tag that is up to a week
of staleness BEFORE the build cache is involved at all — buildkit resolves FROM
against the mirror, and a fresh sandbox has no local image to resolve from.
Tuning that is a one-line change nobody has made deliberately yet.

### What it costs on disk

~800MB per project for the greeter test project, and it is per project by
design. That is real, and it lands on the same disk as the sandbox volumes and
the image mirror: the box this was measured on hit 100% full during the A/B.
The cache is the cheapest of those to give back (one button, one slow build),
but a retention policy for build caches — or at least a size shown next to the
other reclaimables — is the obvious next piece of work.

## Live agent sessions: the workspace sidecar and ACP

Agents used to run as one-shot CLI invocations (`opencode run --format json`,
`codex exec --json`), started by `devcontainer exec` per stage and parsed
after they exited. Four things followed from that and all four were felt: the
run page could only tail raw NDJSON; nothing could be said to a running agent;
`ask_human` had to hand the turn back after a 20 s grace hold ("END YOUR TURN
NOW") because no tool call could be held open past the harness's timeout; and
an engine restart killed the exec client while the agent kept running,
orphaned, beside the resumed one.

Compared against kandev (kdlbs/kandev): the difference was never who
orchestrates — both engines drive every step — but that kandev keeps a
long-lived agent process behind a per-environment sidecar (agentctl) and talks
to it in the Agent Client Protocol. This change does the same.

### The sidecar (`shim sidecar`)

The static shim grew a fourth role. One sidecar per workspace, started on
first use by the devcontainer adapter through `Exec` — so it runs as the
remote user with the devcontainer's environment, exactly as the harness always
did — and detached by the shim itself (no setsid/nohup assumed in the repo's
image). It serves HTTP on `/opt/apothecary/sidecar.sock`, which the engine
dials host-side through the bind mount: the same channel as the engine bridge
and the port relays, no container networking. Singleton by socket.

It owns:

- **the agent** — one ACP process (JSON-RPC over stdio), started on demand and
  reused across stages; sessions are created or loaded per attempt, the
  stage's agent is applied as the session mode (opencode's modes are its
  agents) and the flow's `provider/model` as the session model, trying the
  closest advertised id when the agent spells models differently.
  `session/update` is normalized into a small event vocabulary (message,
  thought, tool_call/tool_update with input/output/diffs, plan, usage with
  cumulative cost, turn_start/turn_end) and kept in a replayable buffer with a
  boot id and sequence numbers. Permission requests are auto-approved (the
  harnesses are configured not to ask; this is the backstop that keeps a stray
  prompt from hanging a turn). Files and terminals are NOT offered to the
  agent as client capabilities: it works on the checkout directly.
- **files** — one directory level at a time (ignored entries flagged, `.git`
  hidden) and file contents capped at 1 MB; paths resolve through symlinks and
  must stay inside the checkout.
- **git** — the run's changes against the merge base with
  `origin/<source branch>`, committed and not, plus untracked files, and a
  unified diff per file.
- **terminals** — a PTY shell in the repo over a WebSocket. stdlib only (the
  shim builds from its own source tree with no module dependencies), so the
  RFC 6455 framing and the three PTY ioctls are hand-written; Linux only.

### Harnesses

`internal/adapters/acpagent` is the one AgentRunner for every ACP agent. A
harness kind contributes a profile: opencode is `opencode acp` (verified
against 1.17.8), codex is the `@agentclientprotocol/codex-acp` bridge (pinned
2.2.2, installed beside the codex CLI) in its `agent-full-access` mode, with
the role prompt prepended to the first prompt because codex has no named
agents. The old NDJSON parsers are gone.

opencode in ACP mode swallows a failed model call: the turn ends `end_turn`
with no output and the cause appears only in its log. It now runs with
`--print-logs --log-level WARN`, and the sidecar turns "a turn that produced
nothing and logged an error" into a failed turn carrying those log lines —
found live, where the free tier refused the pinned version and the stage would
otherwise have looked like an agent that silently did nothing.

### The transcript

The runner streams `ports.AgentEvent`s; the run loop coalesces them into
events (`agent_prompted`, `agent_session`, `agent_message` per text segment,
`agent_tool` per finished call, `agent_plan` per change, `agent_notice`) —
never one per token, because every append is a transaction with a projection
update. Token-level progress travels as ephemeral `delta` frames on the run's
SSE stream (no id, so a reconnect never resumes from one), superseded by the
persisted event with the same segment / tool call id. Everything passes the
redactor, deltas included.

`agent_session` is appended the moment the session exists, so a crash
mid-invocation resumes the right session even before the first invocation
ended — previously the session id was only known after the process exited.

### Messages to a running agent

`POST /runs/{id}/messages` records `message_queued` and hands the message to
the running invocation's inbox. The runner takes queued messages when a turn
ends and sends them as the next turn of the same session; `interrupt` cancels
the turn in flight (ACP `session/cancel`) first. Taking IS delivering
(`message_delivered`). A message nobody took before the invocation ended rides
into the next prompt. With no agent invocation running the endpoint answers
409 rather than queueing into a void.

### `ask_human` blocks again — and the engine parks

The tool call now waits for the answer: the shim long-polls the ping, sending
MCP progress notifications, which opencode uses to reset its per-call timeout
(codex's `tool_timeout_sec` is raised for the server instead). A raise carries
an idempotency key, so a retry after a lost response returns the same ping
instead of asking twice.

The engine still owns the long wait. If nobody answers within
`Config.AskHumanHold` (30 min), it parks the run itself: the open questions
are marked handed back (the long-poll answers 410), the turn is cancelled, the
attempt pauses with its session recorded, and the slot is given back — the
same pause/resume machinery as before, without depending on the model obeying
"end your turn".

### Restarts

The agent process lives in the sidecar, i.e. as long as the container. On
re-entry after an engine restart the runner first asks whether the session's
turn is still running, or finished after the last transcript event recorded
(`agent_*` events carry the sidecar cursor); if so it follows the turn from
there instead of prompting again. If the turn is gone but it had asked a
question, the attempt parks on that question and resumes the session with the
answer. Only then does it fall back to "the engine restarted, continue".

### Residuals

- Cost spent by a reattached turn between the last recorded event and the
  restart is not recoverable (the sidecar reports cumulative session cost).
- The sidecar's event buffer is in memory (20k events): a sidecar restart
  (container restart) loses an in-flight turn, which the runner reports.
- Live deltas are scrubbed per update; a secret split across two updates could
  reach a live viewer before the persisted (fully scrubbed) segment replaces it.
- The terminal and workspace endpoints are as unauthenticated as the rest of
  the API, and reachable from inside the workspace through the engine bridge,
  like every other route — DESIGN's localhost residual applies.

### What the agent will run, checked before it runs

Found live: the built-in flow's review stage pinned `opencode-go/glm-5.1`. The
provider's live model list still served it, but opencode 1.17.8 in the
workspace did not offer it, so runs built a workspace, planned, built — and
failed at review when the session refused the model. Two checks now, because
there are two sources of truth:

- **The provider's list, before anything is built** — `Resolution.Models`
  (the harness kind's `Models()`: the provider's live endpoint, models.dev as
  fallback). Run creation refuses a stage whose model is not in it, naming the
  stage and the closest ids from the same provider; preflight shows it as a
  `models` step. An unreachable list does not block a run (a provider blip is
  not a verdict on the flow); preflight says the check could not be made.
  Harnesses that run any model string declare `kinds.AnyModel` (the fake) and
  are skipped.
- **The agent's own list, in the workspace** — `ports.AgentModelLister`: the
  sidecar returns what the agent advertised for a session, and the ACP runner
  opens a throwaway one to read it. A fresh run checks every agent stage right
  after its workspace is ready and fails before the first stage; envcheck has
  it as the `agent_models` step. Matching follows the agent's spelling: codex
  lists bare ids with an effort suffix (`gpt-5-codex/high` covers
  `openai/gpt-5-codex`). An agent that cannot be asked, or reports nothing, is
  logged and not failed — its stage would surface the problem itself.

### Envcheck: no more waiting it out

- **One per project, superseding.** Starting a check cancels the project's
  running one instead of refusing with "already running"; `DELETE
  /api/envchecks/{id}` cancels outright. A cancelled check reports why once
  (`stopped`) instead of failing every remaining probe.
- **Verdict before teardown.** The check is pass/fail as soon as its probes
  end; destroying the workspace follows (`tearing_down`, then a `destroy`
  step), and another check can start meanwhile.
- **Probes in parallel.** Egress probes run eight at a time (dozens of hosts,
  up to 10 s each, used to be sequential).
- **Re-attach.** `GET /api/projects/{id}/envcheck` is the project's latest
  check, so the project page follows a check started before it was opened.
- **Cleanup of a cancelled build.** A provision cancelled or failed mid-build
  returns no env id but may leave a sandbox, a volume and an egress network;
  the check destroys by the workspace name, which is the env identity.
- **Two probe bugs.** Wildcard allowlist entries (`*.docker.io`) were curl'ed
  literally (exit 3, malformed URL) — now skipped as patterns. And the check
  provisioned without the harness install list, so on a repo image without
  opencode `harness_binary` could never pass — it now builds what a run would.

### Run-page gate: the review link

The run page fed the ping card from the `ping_raised` event's `payload_ref`,
a field the event stopped carrying when attachments became typed
(`files` / `link`). The card never saw the plan file, so the gate showed no
"review & comment" link there while the inbox (fed by the projection, which
still derives `payload_ref`) did. The run page now derives the primary the
same way: the link, else the first file.


## Several repositories (multi-repo tasks)

One ticket sometimes needs changes in more than one repository — an API and
the client that consumes it, a library and the service that pins it. The
engine's unit of safety is the run, so the question was how one run owns
several branches and several merge requests without the agent taking over
any of the publication.

### The model

A project may list **additional repositories** beside its primary
(`Project.repos`: name, URL, default branch, optional forge locator;
migration `0024_project_repos`). A run records a **checkout** per repository
in `run_created` — the primary at `~/repo`, the extras at `~/repos/<name>` —
all on the same branch name, each forked from its own default branch. Runs
older than this carry no checkout list and replay as exactly the primary.
From there:

- **bootstrap** clones and branches each one (`git switch <branch> ||
  git switch -c <branch> origin/<source>`, so a branch that already exists on
  the remote is tracked, as the primary's is), with a credential line per
  distinct host and the same git identity; the egress allowlist derives every
  host;
- the **prompt** carries a `# REPOSITORIES` section when there is more than
  one checkout, naming each one's path, branch and source, and `$CHANGED` is
  grouped per repository;
- a **command stage** marked `each_repo: true` (command stages only; the
  parser refuses it elsewhere) runs its script once per checkout, primary
  first, failing on the first non-zero exit with the repository named — the
  default template's `commit` and `push` say it, and the commit message file
  is addressed in the primary (`$HOME/repo/artifacts/commit.md`) since that is
  where the committer wrote it;
- **`create_mr`** opens one draft merge request per repository the run
  **changed** (`git rev-list --count origin/<source>..HEAD > 0`; an untouched
  extra gets none), each onto the branch that checkout forked from. It
  reconciles per checkout through the forge (`FindMR`) *before* reading the
  body, so a crash in the record window never duplicates one; `mr_created`
  now carries `repo` and `target`. A repository listed **without a locator**
  has no forge and is **context-only**: cloned for the agent to read, never
  published;
- the **gate** lists every merge request in its context (`$MR_URL` stays the
  first); `mr_merged` fires when *all* are merged and `mr_closed` when *any*
  is closed unmerged;
- the **sidecar** takes `--repos` and every workspace route takes `?repo=`
  (`GET /api/runs/{id}/workspace/repos` lists the checkouts), so the run
  page's *Changes*, *Files* and *Terminal* tabs switch repository.

### What the engine owns

Nothing changed in the division of labour: the agent writes code in whatever
checkouts the task needs; committing and pushing are command stages, the
merge requests are an action. The push is a plain `git push -u origin HEAD`
per checkout — the branch is the run's own, so a remote that moved is a
refused push to look at, never something to overwrite.

### API and UI

`Run.mrs` (repo, branch, target, url, iid) and `Run.checkouts` join
`Run.mr_url` (kept: the first one); `Project.repos`; `DocStage.each_repo`;
`mr_created` shows its target in the timeline. The run page shows
`MR 1/2 ↗ MR 2/2 ↗` instead of one link when there are several, and a
repository picker on the workspace tabs. The project form has an
*Additional repositories* section, with a "+ extra" shortcut on the
repository browser.

### Stacked changes: deliberately not in the main flow

An engine-owned stacked-change mode (layers planned by the planner, a branch
and merge request per layer, restack on rework) was built and then removed
before shipping. The reason is product shape, not feasibility: it put a loop
and a second git discipline inside the flow that every run follows, and made
the common case (one branch, one merge request per repository) carry the
machinery of the rare one. The direction instead is a **separate flow applied
to a finished branch** — split it into reviewable merge requests after the
fact, as its own run with its own gates — which keeps the per-run pipeline
simple and lets the split itself be reviewed. Pieces that the stack needed and
that stayed because multi-repo needed them too: `mr_created.repo/target`, the
run's merge-request list, the gate listing every merge request, and the
all-merged / any-closed signal rules.

### Residuals

- The extra repositories share the primary's devcontainer, so its image must
  carry their toolchains; the project form says so.
- One merge request description (`artifacts/mr.md`) serves every repository's
  merge request; per-repository descriptions are not supported.
- Stacked changes and several repositories were never combined; a later
  "split this branch" flow starts from one repository.

## Phases: the run's life after the merge request

PLAN6, built as M28–M33. A flow may now describe a ticket's whole life: the
entry phase (`on: ticket`) is today's pipeline ending at the open merge
request, and reaction phases — `conflicts`, `review_fixes` in the default
template — run while the merge request is open, when their trigger holds for
one merge request (the phase run's *subject*). One phase run at a time, in the
ticket's own run, workspace and event log; the run ends when its merge
requests merge (completed) or one is closed (abandoned). The grammar change is
additive (`phases:` beside `stages:`, still version 3); flows with top-level
stages behave exactly as before.

### Forge facts (M28)

`Forge.MRState` became `MRStatus` — state, mergeability (`clean | conflicts |
unknown`), head sha, target branch and, only when conflicting, the target's
tip (one extra branch lookup) — and the port gained `MRThreads`, `ReplyThread`
and `ResolveThread`. GitLab is all REST (discussions; `detailed_merge_status`
"checking"/"unchecked" is *unknown*, never a conflict). GitHub needs GraphQL
for review threads — REST exposes neither the grouping nor `isResolved` — and
for replying and resolving; a review's top-level body becomes its own thread
(`review:<id>`), answered with a conversation comment that carries the thread
marker so it can be found again. `mergeable: null` is *unknown*.

"Ours" is decided by a marker, not by account: every reply ends with an
invisible `<!-- apothecary run=<run>/<phase>-<n> thread=<id> -->`. A thread is
**pending** when it is unresolved and its last comment carries no marker. The
round suffix lets reconciliation after a crash recognise this round's replies
only — an earlier round's answer on the same thread never suppresses a new one.

`GET /api/runs/{id}/mrs/status` reads all of it live for the run page (an
observation, never recorded).

### The engine (M29, M30)

- **Events**: `phase_started {phase, subject, key, by, tip}`,
  `phase_completed`, `phase_declined`; replay rebuilds the current phase, the
  per-(phase, subject) run counts and every trigger key started or declined.
  A phase start resets the edge counters of that phase's stages — `max` is per
  round — while the budget stays run-wide.
- **Idle** is not a phase: when a phase reaches `done` the loop enters
  `idle()`, which polls the forge on the existing `MRPollInterval` (threads on
  `ThreadPollInterval`, default 4× — a heavier call), takes confirmation
  answers and manual starts, and returns when a phase starts or the run ends.
  It is resident like a signal-armed gate but **does not hold a concurrency
  slot** (`busyLocked` skips idle handles; the scheduler admits a queued run as
  the loop goes idle). A phase starting from idle does not wait for a slot, so
  the cap can be exceeded by phases briefly — they are short, and blocking a
  conflict fix behind unrelated tickets would be worse.
- **Triggers** are pure functions of forge facts, evaluated per merge request
  in checkout order, then per phase in document order. `mr_conflicts` keys on
  `(target tip, head)`; `mr_review_settled` holds when the newest pending
  reviewer comment is older than `quiet_for`, keyed on that comment. No cursor
  is stored, so nothing can drift from what the reviewer sees.
- **Confirmation** is a `phase_confirm` ping answered like a gate
  (approve/reject, no reason required on reject — the UI says *Run it* / *Not
  now*). It belongs to no stage: replay keeps it apart from a stage's gate
  answer. One open question per (phase, subject).
- **Subject scoping (R12)**: command stages run in the subject's checkout,
  `$MR_URL` and `$CHANGED` resolve to it (`$CHANGED` from the head recorded at
  the phase start), agents get a `# SUBJECT` prompt section.
- **Run view**: state `in_review` while idle with merge requests open;
  `phase`, `phase_subject`, `idle` on the view; `in_review` counts as active
  for flow deletion. `GET /api/runs/{id}/phases` lists the reaction phases with
  their runs per merge request; `POST /api/runs/{id}/phases/{phase}/start?repo=`
  starts one by hand (idle only; `max` applies; a human start consumes no
  trigger key).

### Review rounds (M31)

`load_review_comments` writes the subject's pending threads to
`artifacts/review.md` (+ `.json`) and records `review_comments_loaded`; a
re-entry rewrites the same set. The builder takes them as an optional
`comments` input. The `replier` agent writes `artifacts/replies.yaml`, a
`review_replies`-typed output validated when its stage ends against the loaded
thread ids — an unknown thread is that stage's `output_invalid`, retried with
the reason, before anything is posted. `reply_review_comments` records
`action_started`, posts each reply with its marker (resolving only where the
file says), and on a re-entry posts only the threads that do not yet carry
this round's marker.

**Choosing what to fix.** Not every comment needs a change — it may be
wrong, or the author handled it by hand. With `select: human` (the template's
choice) `load_review_comments` raises a `select` ping — a checklist of the
pending threads, options `"<id> — <where> — <author>: <first line>"`, answered
with the chosen options one per line or `none` — and parks the run (the stage
re-enters on the answer, as a parked gate does). Only the chosen threads are
written for the fixer and may be replied to; the others are recorded in
`review_comments_loaded.skipped` with their last comment id and left out of
later rounds — and out of the trigger — until that thread gets a newer
comment. An empty selection ends the phase right after the load: no agent
turn, no commit, no push.

**GitHub comments.** Every comment counts, not only reviews: review threads,
review bodies and conversation comments (`issue:<id>` threads, answered with
a conversation comment carrying the marker). Comments from bot accounts
(`user.type == "Bot"`: CI, dependency bots) are left out — otherwise every CI
report would open a review round.

### Answer kinds: one shape vocabulary for every human question

Three kinds of question had grown three sets of flags: a gate was
`gate: true`, an `ask_human` with options was "has options", the thread
picker was `type == select`, and validation, the card and the inbox badge
each re-derived what answer a ping takes. `ping_raised` now **declares** it
— `answer_kind: decision | choice | multi | text` — and options are
structured, `{id, label, detail}` (older events' bare strings decode as
`{id: s, label: s}`; `PingRaised.Kind()` infers the kind for pings written
before the field, as does the Postgres projection on read, migration 0025).

What follows from one field: `validateAnswer` is one table; `recordedAnswer`
and the tool-return renderer read the kind; `Ping.gate` and the
`decisionPingType` family are gone (what remains is `agentPingType`, which
is about *who* asked — the unwanted-ping count and the "wanted" badge are
about origin, not shape); the thread picker's options carry the thread id,
"where — who" and the first words as id/label/detail instead of one string
split on " — "; and `ask_human` gained `multi: true` and structured options
for free (the shim passes them through; `RaiseQuestion` validates them; 16
options at most). Not done, on purpose: multi-question forms, and structured
plan comments — the annotation composer stays a feedback editor over a file
payload, which any decision or choice ping with a file already gets.

### Conflicts (M32, M33)

`sync_target` fetches the subject's target and merges (or rebases) it in, in
the engine. Already contained → nothing happens and nothing is recorded. A
conflict leaves the operation **paused** (`MERGE_HEAD` / `rebase-merge`) and
fails the stage with the files named; the template routes that to the
`resolver` agent, which edits the files and runs no git. The retried stage
recognises the paused operation, checks the unmerged files for the labelled
markers (`<<<<<<< `, `>>>>>>> `; a bare `=======` is legitimate content), and
finishes it — `git commit --no-edit`, or `rebase --continue` (`--skip` for a
commit the resolution emptied). A paused operation of another strategy or an
older target tip is aborted and redone; a non-conflict failure aborts and
restores the branch. Six resolver rounds, then a human gets the paused merge.

Actions may carry `on_fail` again (for outcomes like these); infrastructure
errors stay terminal.

`push_branch` replaces the template's `git push` command: a first push is
plain; a branch it pushed before is pushed with `--force-with-lease` against
the tip recorded in `branch_pushed` — or, in a reaction phase that has not
pushed yet, the head recorded at the phase start. A remote branch holding
commits the run never saw is refused, never overwritten. That is what makes
`strategy: rebase` safe, and the parser requires a `push_branch` in any phase
that rebases.

### Fixed on the way

- `action_started` was never cleared on replay once the action completed (the
  multi-repo change dropped the clearing on `mr_created`). Harmless for a
  one-shot `create_mr`; a reaction phase re-runs its actions, so round two
  would have started in reconcile mode. It now clears on the stage's
  `stage_completed`.

### Deviations from PLAN6 as written

- The entry phase must be the **first** phase (one rule instead of a search;
  it also makes the flattened stage list start with the entry).
- `trigger` / `phase` were not reserved as stage ids — nothing needs it.
- The plan's template YAML had two success cycles (the parser caught both):
  `sync`'s implicit next stage was `resolve` (which routes back to `sync`),
  and each phase's last real stage fell through into its rescue gate. The
  template states `on_success` explicitly there.
- Reply markers carry a round id (above) instead of reconciling by time.
- The default template's rescue gates follow the template's philosophy (each
  resumes the stage that escalated), except the resolver's, which resumes
  `sync` — the human who resolves by hand replaces the agent.

### Residuals

- Answering a confirmation while *another* phase's command stage runs gets
  the existing "run is busy — try again" after 30 s: only agent stages, gates
  and the idle loop drain answers.
- One replies file and one commit per round; a round cannot split its fixes
  across commits.
- Phase runs on different merge requests take turns (one cursor); parallel
  phase runs would be the multi-cursor rework.

## Lifecycle polish (TODO 17 retired, 2026-10-19)

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
- **Config dirty hint** (Coder's "dirty workspace"): the adapter records a
  digest of the build inputs (.devcontainer/**, root .devcontainer.json, the
  Dockerfile/compose files the config names) at provision/rebuild;
  `ports.WorkspaceDirtyChecker.ConfigDirty` compares. The reconciler keeps it
  in memory next to env status, `run.View.EnvDirty` carries it on the read
  path, and the UI marks the Rebuild button. A rebuild clears it at once.

## The `debug` leaf (TODO 28 retired, 2026-10-19)

`debug` is a reserved edge target (`flow.ReservedDebug`, next to `ReservedStart`
— not a terminal: the run comes back from it). `then: debug` / `on_fail: debug`
replaces the ~20 `debug_<stage>` human gates the default flow carried. Taking
the link records `edge_taken{to: debug, max: -1}` and parks the run in a new
interpreter phase (`phDebug`) with the cursor still on the failing stage; the
engine raises a `ping_raised{type: debug, stage_id: <that stage>}` carrying only
facts — the `$FAILURE` text as context, the workspace link, the label
"<stage> failed" — and gives its slot back (`errWaitingOnHuman`). The card is
the UI's ("Stage X failed — check why"). Approve re-enters the stage as a
fresh attempt (no feed, like a gate's approve → advance); reject abandons at
that stage. No new event types, so old logs replay unchanged; stored
`run_resumed`-era logs are unaffected. Replay: `edge_taken` to `debug` sets
`pos = Index(From)` and `ph = phDebug` (a plain `Index("debug")` would be -1
and fail the run as "flow changed"); a `ping_answered` is the routing decision
only when the ping is the stage's own and the phase matches
(`runState.decidesHere`). Terminal events now clear `rs.openPings`, so a user
Abort while parked followed by Retry no longer re-registers a withdrawn ping.
`stageGate` and `stageDebug` share `openDecision` / `raiseDecision`.
`PingRaised.Question/Context` are scrubbed on append like the other
workspace-sourced payloads.

## Tracker actions: write-back through `ports.TicketWriter` (TODO 27)

A flow can now move, annotate and link the issue its ticket was imported
from: four actions (`ticket_transition`, `ticket_comment`, `ticket_set_field`,
`ticket_link`, FLOWLANG §2) behind one write port, with Jira and GitHub Issues
adapters. Decisions worth recording:

- **An optional capability on the source adapter, not a second port.**
  `ports.TicketWriter` (`Transition/Comment/Comments/SetFields/Fields/AddLink`)
  is discovered by type assertion on the `ports.TicketSource` adapter, so one
  integration — one credential — serves both the import and the write-back,
  and an adapter that never learned to write is simply not a valid target
  (`resolve.Resolver.TicketWriter` refuses it by name). The read port's "the
  engine never writes back" promise narrows to "nothing writes unless a flow
  says so, stage by stage".
- **Resolved per ticket, by integration id.** The tracker is the *ticket's*
  (`tickets.source_id`, snapshotted onto `run_created` as
  `ticket_source_{id,kind,key,url}` and carried on `run.View`), never the
  project's: the "not a project reference" decision stands, no project column
  was added. `engine.Resolution.TicketWriter` is a func field like `Models`
  and `Prepare` (nil in fakes that don't wire it). Preflight's per-project
  half (`actionAvailable`, `Requires: tracker`) checks only that the build can
  write back; the per-ticket half (`preflightTicket`, in `CreateRun` where
  the ticket is known) resolves the writer once — only when the ticket *is*
  linked.
- **Unlinked ticket ⇒ skip, visibly.** A ticket written by hand has no issue;
  a tracker stage on it appends `stage_completed{skipped: true, note}` and
  follows `on_success`, so one flow serves every ticket. Two new fields on an
  existing event rather than a new type: replay, the projection, telemetry
  and the UI's "attempt ended" lists all tolerate a zero-cost completion, and
  the timeline renders the note.
- **Failures are outcome failures.** `actionFail` → `on_fail` (else the run is
  abandoned), never `infraFail`: a status the workflow cannot reach, a field
  the tracker cannot write and a tracker that is down are all fixed outside
  the run. A new reason, `input_invalid`, names the first two (the flow's
  literal does not fit the tracker's schema); transport errors keep `infra`.
  "Best effort" is written as `on_fail: { to: <next>, max: 1 }`.
- **Remote actions need no workspace.** `actionspec.Action.Remote` +
  `engine.needsWorkspace`: a tracker action with no file input bound runs
  without provisioning, so "In Progress" lands seconds after the run starts
  and a "Done" after the final gate never re-provisions a reaped workspace.
  Three things followed: the model check's "no stage ran yet" guard became
  "no *workspace* stage ran yet" (it also fixes a gate-first flow silently
  skipping the check); `run.Apply` moves `pending → running` on
  `stage_started` (a run working before any workspace used to read as
  queued, and `Recover` would have re-queued it); and `ensureEnv` announces
  the first provisioning from the `env_requested` record rather than from
  the pending state.
- **Idempotency, per action.** Transition is read-compare-write; `append`
  mode is a union of comma-separated items; Jira links upsert on `globalId`
  and GitHub links carry an HTML-comment marker. Only a comment needs the
  intent pattern (`action_started` + reconcile through `Comments`), and its
  trailer — `— apothecary · run <id> · stage <id>` — is visible text because
  Jira renders comments as ADF and strips an HTML comment; it names the run,
  the stage and (in a phased flow) the phase run, never the attempt, since
  `actionPending` survives a failed attempt and the retry must recognise the
  comment it already posted.
- **Fields by display name.** A flow says `"Feature flags"`, not
  `customfield_10042`: the name is what the issue shows and what survives a
  Jira migration. The adapter resolves it through `GET /field` (cached per
  client), refuses an ambiguous name with the candidate ids, and coerces the
  text to the schema — a paragraph field takes ADF on v3 (`FromMarkdown`, the
  inverse of `ToMarkdown` for the subset the engine writes), labels split on
  commas, a select takes `{value}`. GitHub has `labels`, `assignees` and
  `milestone` and nothing else; `ErrFieldUnsupported` says so.
- **A free-text input kind.** `actionspec.KindText` is the author's literal
  in the tracker's vocabulary: the parser checks only blankness, the
  validator keeps it out of the dataflow, the editor draws it as a value
  (like an enum) rather than a connectable dot, and `ticket_set_field`'s
  `value`/`text` pair is "exactly one", the way a command takes `run` or
  `run_ref`.
- **No run-end hook.** "Done once the merge request merges" is a stage after
  the final gate (`auto_approve: mr_merged` → `ticket_transition`), which the
  language already expresses. (Flow v4, below, made the same true of a phased
  flow: the delivery phase parks on a `wait` for the merge and the move
  follows it.)

Not done, deliberately: a `Test`-button probe of write permissions (the
button's `error`-only result has nowhere to put "reads ok, writes denied");
Jira Cloud's development panel ("connect branch"), which needs an OAuth app
rather than an API token — the branch name already carries the issue key, so
the GitHub/GitLab-for-Jira app links it on its own, and `ticket_link` covers
the rest with a remote link.

## Migrations after the squash: 0001 is frozen, schema changes rebuild projections

Folding the chain into `0001_init.sql` (5385493) created a trap the next
commit fell into: 88171e3 added `runs.view/dormant/rebuilding` by editing 0001
in place. Every existing database had already applied version 1 — the
pre-squash ones collapsed to `{1}`, the fresh ones created with it — so none
of them ever saw the edit, and the daemon died at boot on `column "view"
does not exist`. Two rules now hold that line:

- **`0001_init.sql` is frozen** at its squash-time content, pinned by sha256
  in `TestInitMigrationIsFrozen`. The collapse logic (`preSquashHead`) relies
  on "ledger 1..25 ⇒ exactly the init schema", which is only true while the
  file does not move. Every schema change since is its own `NNNN_name.sql`,
  written to be a no-op where a fresh database already has the result
  (`ADD COLUMN IF NOT EXISTS`); `0002_runs_view.sql` is the catch-up for the
  three columns.
- **Applying any migration rebuilds the projections** (`pgstore.Open` →
  `RebuildProjections`). A schema change can add a projection column every
  existing row lacks, and the projections are derived data by design, so one
  replay of the event log at the migrating boot is always safe — and is what
  turns `view = '{}'` on 57 old runs back into their views. A fresh database
  rebuilds nothing (no runs); an unchanged one applies nothing.

`TestMigrateCatchesUpAnInitEditedInPlace` reproduces the local stack's exact
state (ledger `{1}`, columns missing, runs on the log) and checks the boot
after it: ledger `{1,2}`, every run's view rebuilt, a reopen idempotent.

## Flow editor: auto-arrange lays out from measured node sizes

`autoLayout` used to assume every node was 240 px wide and 160 px tall. A
stage node is 190–300 px wide and grows a row per dataflow dot, so wide
columns collided and the off-chain lane landed on tall chain nodes ("the order
is right, the nodes overlap"). It now takes the sizes Svelte Flow measured
(`node.measured`, reported by `FlowGraph.measuredSizes()` keyed like the
layout map): each column is as wide as its node, each row as tall as its
tallest, phase bands stack by their real height, and the terminals and
trigger markers are placed from the same sizes. The arrange button passes the
current sizes; a flow opened without a stored layout is placed with guesses
and re-placed once `FitOnInit` sees the nodes measured — before the fit, so
the viewport fits the final picture. Nothing is persisted by that re-place:
the next drag or arrange saves.

## Flow editor: one debug leaf per stage

The canvas used to draw `debug` as a single node every `on_fail: debug` /
`then: debug` edge converged on — a sink, like `abort`. It is not one: taking a
debug link parks the run with the cursor still on the stage that took it
(`phDebug`), the ping names that stage, and *continue* re-enters it. The editor
now draws one leaf per stage that escalates (`__debug:<stage>`, layout key
`debug:<stage>` like `trigger:<phase>`), beside that stage, with a dashed
return edge back to it ("then <stage> again"). `autoLayout` gives each row a
strip for its leaves. Dropping an edge on any leaf still means `to: debug`;
clicking a leaf or its return edge selects the stage. The YAML, parser and
engine are untouched — `debug` stays a reserved edge target, and the old
`layout.debug` key is simply unused. `abort` and `done` stay shared: those are
terminals.

The flow canvas is the one surface drawn opaque (`--xy-background-color:
var(--solidBg)`, the theme's "glass made opaque" token): the sigils turning
behind the shell read through a transparent canvas and fought the edges and
labels. Everything else keeps its glass.

Edge labels sit by their source, not at the path midpoint (`FlowLabeledEdge`,
edge type `labeled`). xyflow centres a label on its path, so a retry loop's
label landed on the node it loops around and every edge leaving a stage put
its label in the same crowded middle. Now each failure link's label hangs
under the handle it leaves from, one row per link of that stage
(`flowdoc.edgeLinks` — the single list both FlowGraph's numbering and
autoLayout's reservation read), and `autoLayout` leaves those rows free under
every row of nodes, above the debug-leaf strip. A link to `debug` no longer
spells out `→ debug`: its leaf sits right below. A leaf's return edge carries
a short "↻ again" to the right of the leaf.

## Flow editor review: where the sidebar disagreed with the canvas

A pass over the graph editor against the document model, checked in a headless
browser against the dev daemon. What was wrong, and what changed:

- **Implicit success read as "done".** YAML may omit `on_success` (next stage
  in document order). The canvas resolved that sugar; the inspector's select
  bound the raw field and showed `done` — `load` and `post` in the default
  flow's review phase. `normalizeDoc` now materializes every success target;
  `docToYaml` already wrote them explicitly, so no saved document changes.
- **Pickers offered illegal targets.** On-success and edge-target lists
  listed every stage, but a route may not leave its phase (a parse error at
  save). They now list the stage's own phase (`routableFrom`). Dropping an
  edge on another phase's stage on the canvas is refused with a notice
  instead of applied; moving a stage to another phase cuts the routes that
  would cross (`pruneCrossPhase`).
- **Trigger signals in wait and gate pickers.** `mr_conflicts` and
  `mr_review_settled` start phases and are refused as a `wait` or an
  auto-answer; the UI's signal type never carried `trigger`/`debounced`. It
  does now, and those pickers offer only waitable signals.
- **Phases had no editor.** A phase's trigger, `quiet_for`, `confirm`, `max`,
  description and id were reachable only through YAML. Clicking a phase's
  trigger marker (or the entry marker in a phased flow) now opens a phase
  view enforcing the parser's rules: quiet_for only for a debounced signal,
  unlimited runs only with confirm, the entry's trigger fixed to `ticket`.
- **Edges were opaque in the stage view.** A defined edge showed as a bare
  `on_fail →` chip; it now spells the chain (`on_fail → fix_commit_msg ×1 ·
  then debug`).
- **Defaults that broke phased flows.** A new edge pointed at the document's
  first stage (usually another phase); it now retries the stage itself (a
  gate's rejection goes back to the stage feeding it). A new fallback link is
  `debug`, the usual last resort. Renaming refuses `start` and non-slug ids,
  as the parser does.

Still YAML-only, deliberately not built in this pass: the flow's `agents:`
block (prompts, contracts, read_only/allow_bash), and adding or deleting
phases.

## One beginning per phase: `on: manual`

*Superseded by flow v4 (next entry): the entrypoint is `on: run_start`,
`confirm: true` became `start: confirm`, `ticket` is no longer read, and the
entry is no longer the first phase by position.*

A phase is one beginning and the stages it runs, and the three places that
said so disagreed. The YAML had `on: ticket` on the entry — a noun, a magic
string outside the signal registry — and a trigger signal on each reaction;
the canvas drew a solid "start" pill for the entry that named neither its
phase nor what begins it, and violet "phase: on signal" markers for reactions;
the inspector told the entry it "has no trigger to set". A flow without phases
had no beginning at all.

Now every beginning is a registry signal. `manual` (`signalspec.Manual`,
flagged `Trigger` and `Entry`) is the one a person gives: it never holds on
its own. The entry is `on: manual` — someone starts a run for a ticket — and a
reaction phase may be `on: manual` too: a phase only the run page starts
(`max` counts those starts; `confirm` is refused, nothing fires to confirm).
The forge triggers keep `Trigger` only; `Entry` is the seam for a tracker
event (*a ticket assigned*) to begin a run, as FLOWLANG §10 foresaw. The
entry is the first phase by position (`Phase.entry`, set by the parser), no
longer "the one on ticket", since manual begins reactions too. `ticket` is
still read, as `manual`, and the editor writes `manual` back: no version
bump, every saved flow parses.

The canvas draws one marker per phase, the entry included — "deliver: on
manual", "conflicts: on mr_conflicts (confirm)" — solid for the entry and
dashed for reactions, each pointing at its phase's first stage; a flow
without phases shows "on manual". The inspector's phase panel is one form:
*Begins on* offers the entry the signals flagged `Entry` and a reaction those
flagged `Trigger`, and shows `quiet_for`, `confirm` and `max` only where the
chosen signal takes them. `GET /api/signals` carries `entry`; the run page's
review card names a manual phase "only when you start it". The entry's
`phase_started` records `by: human` (it was `ticket`); the timeline reads
`by` only for reaction starts.

## Flow v4: wait points, one scheduler, bound parameters, canonical statuses

The flow language felt static for two reasons. One YAML carried three
concerns — the recipe, the delivery lifecycle and the environment bindings —
and the lifecycle part was hardwired: the entry was `Phases[0]` by position,
manual only, once; a run could only end from its merge requests; reactions
were evaluated only against open MRs; `create_mr` was entry-only; a `wait`
polled inside the stage loop and a gate paused the whole run. About fifteen of
the parse rules existed only to force a graph into that envelope, and the
engine had five waiting mechanisms (phase triggers, confirm pings, hand
starts, wait stages, gates) that were one thing implemented five ways. And
nothing was reusable across environments: `to: "In Progress"` was a Jira
literal, and there were no parameters, so the same recipe on another project
was a full copy that drifted.

v4 (2026-10-23, no compatibility: the project is unreleased, dev databases
were reset) collapses the mechanisms into one primitive and moves the
environment out of the document. FLOWLANG *The model* is the normative
statement; what the code does:

- **Wait points, one scheduler** (`internal/engine/scheduler.go`,
  `phase.go`, `state.go`). A phase's `on:`, a `wait` stage and a `human_gate`
  are one thing: a source, a policy (`auto` / `confirm` / `manual`), a
  subject, an occurrence key. The idle loop walks every wait point in document
  order — parked stages of live phase runs and unopened heads alike — and
  opens or resumes one per tick; the chosen phase run holds the workspace
  until its next wait point or `done`. A wait or gate stage records
  `phase_parked` and returns to the loop instead of polling or pausing;
  `phase_resumed` closes the park. Per-phase-run cursors (`phaseRun`) replace
  the one cursor; a phase has at most one live run. Holds under `start:
  manual` are memory-only, recomputed each tick, exposed on the run page.
- **The end rule.** `endCheck`: a run completes when nothing is parked and no
  firing phase is armed (policy auto/confirm, an open MR subject, runs left,
  or an open confirm ping). `done` only finishes a phase; a closed-unmerged MR
  abandons a run only through the flow (`wait_merge … on_fail: abort`). The
  parser warns about a `create_mr` no wait ever waits on.
- **Explicit entrypoints.** `run_start` joined the signal registry (`Entry`);
  a flow needs one such phase, may have several, and a run is created at one
  of them (`RunCreated.Phase`, `run.View.BeginPhase`; the start form and the
  rerun modal pick). `manual` is a phase only the run page opens; any phase
  without a live run can be started by hand (`StartPhase` takes a `subject`
  body). `max` counts hand starts, defaults to 10 for firing signals and
  unlimited otherwise.
- **Parameters** (`internal/flow` `parameters:`, `Flow.Resolve`,
  `internal/engine/params.go`). One block declares every knob with its
  default; six engine-known names always exist. Values are layered flow
  default ← project binding (`flow_bindings`, `confstore/bindings.go`, the
  workshop's Configure sheet) ← run override, recorded on `run_created`.
  `Resolve` re-parses the pinned bytes with the values, so parse-time and
  resolve-time validation are one code path and a bad binding is refused
  where it is bound. `$lower` is a parameter, `$UPPER` a sentinel; a reference
  is allowed at typed sites only (stage model/harness/max_turns/timeout/
  run_ref, action inputs, `on.start/quiet_for/max`, `when`).
- **`when:`** (`condition.go`): `tracker`, `subject`, a bool parameter, `not
  x`; false completes the stage as skipped.
- **Canonical statuses** (`ports.TicketStatus`, `TicketWriter.TransitionTo`).
  `ticket_transition` takes `to` (todo / in_progress / in_review / done /
  blocked) or `status` (the tracker's word), exactly one. Jira maps through
  the integration's status map (`status_map`, stock words by default), GitHub
  Issues to open/closed; a status a tracker has no word for skips the stage
  (`ErrStatusUnsupported`) rather than failing, so one flow fits both.
- **Preflight** runs on the resolved flow (a bound harness or model lands on
  every agent stage and is held to the project's set and model list) and
  checks every firing phase's signal; `PreflightProject` gained a
  `parameters` step.

Decisions worth remembering: gates park like waits, so a phase started by
hand may edit the workspace while a gate is open — the person chose that;
`ask_human` and the debug leaf still hold the workspace. `create_mr` is
Single per phase and adopts an existing MR via `FindMR`, so re-running the
delivery phase by hand is safe. Subject actions without a subject fail
routably (`input_missing`) rather than at parse. A stale project binding is
dropped with a warning at run creation; a stale run override refuses the run.

**Automations** (slice 3, `internal/automation`, `confstore/automations.go`,
`0004_automations.sql`) are the ways a run begins without a person, and they
live outside the engine: a rule that creates a run at a `run_start` phase
with its params, `by: automation`, the run none the wiser. `tracker_query`
polls a ticketsource integration (the ticket factory's import path), imports
each issue as a ticket stamped with the automation id, and starts one run
per import; a duplicate is adopted only when it is this automation's own
import still without a run (a crash between the two), a person's import is
left alone. `cron` (five fields, UTC, `internal/schedule`) fires once per
slot after the last firing — no catch-up, and the firing is marked on the
row BEFORE the run is created, so a crash cannot fire it twice; an edit
counts from the edit. A refused run (preflight) lands in `last_error` and
one blocker notification per streak; the ticket stays for a person. The
rule is validated at save the way a firing would be (the integration's
port, the schedule, the flow, an entrypoint of it, params via `ParseBound`).
The daemon runs a pass every two minutes.

## Flow v4: two ends, and a reaction never ends the run

Found reviewing the default flow (2026-10-23): its reactions closed with
`on_success: done`, which reads as "the run is done" while the engine only
ended the phase — and nothing in the language could say "this run is
finished". Now:

- **`end` and `done`.** `end` (`flow.TerminalEnd`) finishes a phase: the run
  goes back to waiting. `done` finishes the run, whatever else is parked or
  armed (`runState.endsRun` → `exec.runDone`: `phase_completed`, then
  `run_completed`; open pings are withdrawn with the run). A phase's implicit
  last-stage success is `end`. Both are reserved ids, take no max/then, and
  are accepted as edge scalars.
- **A dead end outside an entrypoint ends the round.** A failure with no
  edge, an exhausted chain, or a person giving up at the debug leaf abandons
  the run only in an entrypoint phase (`exec.deadEnd`). In a reaction or a
  hand-started phase it ends that phase run, `phase_completed.reason` says
  why, and the run goes back to waiting. The debug ping carries `phase` and
  `ends_phase` (migration 0005 adds both to the pings projection), so the
  inbox says "End conflicts — the run keeps waiting" rather than "Abandon the
  run".
- **Reactions are unbounded by default.** The default `max` of 10 for firing
  phases and the rule that `max: -1` needs a start other than auto are gone:
  an occurrence key never fires twice, and the run's budget bounds the cost.
  `max` stays as an optional cap.
- **Warnings** (`endingWarnings`): a stage of a firing phase that ends the
  whole run (`done`, `abort`); a phase that opens a merge request and says
  `done` without waiting for it while other phases react to it. The second
  replaces a "create_mr never waited on" warning the reference promised and
  the code never had.
- The canvas draws an `end` sink always and `done` only when a route reaches
  it; edges to `done` used to point at a node id that did not exist.

## Flow v4: outcomes — the run ends in a phase of its own

Second pass on the ends (2026-10-23), from modeling the default flow with the
user: the first v4 cut ended the delivery phase with `wait_merge` →
`ticket_done` → `done`, so "the run is over" was the tail of the entry chain,
and a close could only be said as that wait's `on_fail`. Reactions could
never speak about the run's end at all. Now the end is a phase like the
others — an **outcome**, opened by the merge request's fate:

- **`signalspec.Outcome`** on `mr_merged` and `mr_closed`. Both stay
  waitable (a `wait` may still park on them); a phase `on:` one of them is an
  outcome (`flow.Phase.IsOutcome`; `Observed()` = fires or outcome, which is
  what preflight's forge check and the end rule read). It takes `start`
  (auto/confirm/manual) like a trigger, no `max` (it fires once), no
  `quiet_for`.
- **`then`** (`DocPhase.Then`, `Phase.Then`): a stage-less outcome says
  `then: done` or `then: abort`. Stages or `then`, never both; `then` only on
  an outcome. Its span is empty (`Last = First - 1`) — the reachability walk
  and anything indexing `Stages[ph.First]` must skip it (the one engine
  reader, `startPhase`, sets the cursor past the end instead).
- **An outcome ends the run.** Its last stage's implicit success is `done`
  (so is the shorthand's: nothing could follow either); any route to `end`
  inside it is a parse error. `on_success: abort` is allowed everywhere
  (`runState.abortsRun`, `exec.runAbort` → `phase_completed`, then
  `run_abandoned` with `stalled_at` empty for a then-phase, reason
  "aborted by flow: outcome closed" / "…on_success at stage x"). A dead end
  inside an outcome abandons the run like an entrypoint's
  (`endsRunAtDeadEnd`): the run's end itself failed.
- **The done rule.** A flow needs some route to `done` — a success, an edge,
  or a `then: done` (`Flow.EndsRun`) — or it is refused: it would say nothing
  about when the work is finished. Test fixtures that had no end gained a
  `shipped` outcome or an explicit `done`.
- **Scheduler.** `evaluateIdle` walks the outcomes before the reactions: a
  held outcome beats any reaction. `waitState` decides when one holds
  (merged = every merge request merged, closed = any one closed unmerged; no
  merge request = never); `outcomeOf` names the subject and key — merged has
  no subject and the key `merged`, closed takes the closed checkout and
  `closed:<repo>`. `start: confirm` reuses `askToStart` (now subject + link,
  not an observation) with the question "…and end the run?"; a decline marks
  the key seen and the run goes on to its end rule. `anyArmed` counts an
  outcome that never ran as armed while any merge request is open — that,
  not a parked wait, is what keeps the default's runs in review.
- **Default flow.** `deliver` ends with `in_review` (`on_fail: end`);
  `merged` (ticket → done, best effort, `done`) and `closed` (ticket → todo,
  `abort` either way) are outcomes. The builtin test holds the shape and
  refuses a `wait` in the default.
- **API/UI.** `PhaseSummary`/`PhaseInfo` carry `outcome` and `then`; the
  signal registry carries `outcome`. The editor offers the outcome signals
  under "and ends the run on…", a `then` select on a stage-less outcome, no
  max; the graph draws an outcome's marker green with its edge to `done` /
  `abort` when it has no stages; the run page's Phases card says "ends the
  run on…", "once". `flowdoc.OUTCOME_SIGNALS` mirrors the registry only for
  the document model's implicit `done` (the pickers read the API).
- `HasSubject()` is false for `mr_merged`: it holds for every merge request
  at once, so its phase acts on none in particular (a subject action there
  warns, as in any subject-less phase).

## Flow v4: blocks — chains written once, expanded at parse time

Second pass, step 2 (2026-10-23). The default flow's model repeats chains
(commit + test after every agent change; plan … publish in deliver and
bugfix), and copying them invites the copies to drift. A block is written
once under `blocks:` and run by a stage that `include:`s it.

- **Grammar.** `blocks:` is a list like `phases:` — `{id, description,
  stages}` — rather than the `name: [stages]` mapping sketched while
  modeling: document order and line numbers come for free, the strict
  decoder applies inside it (a custom mapping unmarshaler would have
  bypassed `KnownFields`), and a block can say what it is for. An include is
  a stage `{id, include, description?, on_success?, on_fail?}` with no type.
- **Expansion is the parser's** (`internal/flow/blocks.go`), at the
  `DocStage` level inside `buildPhases`: every later rule — routes stay in
  their phase, `create_mr` once per phase, reachability, contracts, the done
  rule, the 128-stage limit (counted after expansion) — runs on ordinary
  stages, and the engine is untouched. Ids are the include path
  (`make.ship.test`; `idPathRe`), hand-written ids stay slugs, so nothing
  collides. `Stage.Include` records the path for whoever wants to group.
- **Routes.** In a block: its own ids (an include's id = its first stage,
  `entryOf`), terminals, and `fail` = the include's `on_fail`, *spliced* where
  it is said (each stage saying it gets its own copy and counters — a macro,
  not a shared budget). An include without `on_fail` makes `fail` a dead end.
  The block's last stage without `on_success` takes the include's; an include
  without one leaves it empty and the flattened sugar (next stage, or the
  phase's end) does the rest — which is why nested includes need no special
  case. In a phase: an include's id resolves to its first stage; a dotted
  target (into an include) is refused.
- **Checked whether used or not**: block ids, local ids (`fail` reserved
  everywhere now), include names, cycles and depth ≤ 3 (`blockDepth`). Only
  the expansion checks the stages themselves, so an unused block warns.
- **Lines**: each `DocStage` carries its source line (unexported) through
  the expansion, so an error inside a block points into the block.
- **cpui.** The editor keeps blocks as written (`FlowDoc.blocks`, emitted
  before `phases:`; their stages keep their success sugar) and shows an
  include as one node — kind `include` in `StageKind`, the block picker and
  the block's expanded stage list in the inspector, editing the block itself
  in the YAML tab. What shows a *run* — the stage strip, run facts, the
  agents tab — goes through `expandDoc`, a TS mirror of the expansion, since
  the events name expanded stages; checked against the Go expansion on the
  test document, both for the server's JSON and for raw YAML parsed in the
  browser (whose scalar edges `normalizeDoc` now unfolds).
- Not done: blocks as bands on the canvas (edit in place), parameters passed
  into a block.

## Flow v4: forge observation — CI, approvals, the merge verdict, others' pushes

Second pass, step 3 (2026-10-24). Four signals, each one registry entry and
one predicate in the idle loop; the scheduler, the policies and the
occurrence keys did not change.

- **Port.** `MRStatus` stays one forge call and gains what that call already
  carries: `MergeableNow` (GitHub `mergeable_state` clean/has_hooks and not a
  draft; GitLab `detailed_merge_status == mergeable`) and `Draft`. Checks and
  approvals are two new methods, `MRChecks(mr, head)` and `MRApproval(mr)`,
  so a flow that reads neither never pays for them (`readsOf` scans phase
  heads, wait stages and gate auto-answers; `fake.Forge.ChecksCalls` holds
  the line in a test). Plan deviation: no `UpdatedAt` — nothing needed it.
- **Checks.** GitHub: check runs (newest run of a name wins) plus combined
  commit statuses; GitLab: the head pipeline (pending when it is an older
  commit's), its failed jobs named when it failed, `allow_failure` jobs
  ignored. Cancelled/skipped/manual are `neutral`: no failure. The idle loop
  caches checks per head and asks again when the head moves, while they run,
  for a short while after a push when none are reported yet, and otherwise
  at the thread cadence.
- **Approval.** GitHub: each reviewer's latest decisive review (APPROVED,
  CHANGES_REQUESTED, DISMISSED); approved = someone approves and nobody
  requests changes. GitLab: `approved` and at least one `approved_by` —
  without approval rules GitLab says approved with nobody's approval.
- **Signals.** `ci_failed` [head:failed-names], `mr_mergeable` [head],
  `mr_head_moved` [head, debounced: `quiet_for` counts from when the idle
  loop first saw the head — memory only, so a restart or the end of a phase
  run restarts the quiet period], and `mr_approved`, an outcome (and
  waitable): every merge request approved or merged, impossible once one is
  closed.
- **Whose push.** `runState.heads` is what the run knows per checkout: every
  `push_branch` tip, the merge request's head at creation or adoption (new
  `mr_created.head`, read with one `MRStatus` call; best effort), and every
  phase run's starting tip. A head outside it is someone else's. A `git
  push` from a command stage is not recorded — such a flow would see its own
  push as someone else's; `push_branch` is the publishing primitive.
- **UI.** `MRLive` carries `checks`, `approval`, `mergeable_now`, `draft`
  (read for open merge requests); the MR status line says "ready to merge",
  "draft", "CI passed/running/failed: names" (the long form links the failed
  check) and "approved by …"; "mergeable" now reads "no conflicts", which is
  all it ever meant.

## Flow v4: mr_merge and post_review

Second pass, step 4 (2026-10-24): the two forge-side acts the modeled flows
need beyond opening a merge request — auto-merge (a reaction on
`mr_mergeable`) and the reviewer flow.

- **Port.** `MergeMR(mr, {Method, Head, DeleteBranch})` and `PostReview(mr,
  {Head, Summary, Notes, ID})`. A merge the forge declines is a typed
  `*ErrMergeRefused` (GitHub 405/409/422, GitLab 405/406/409/422) that the
  engine turns into a routable `input_invalid` failure; anything else is the
  forge not answering (infra, terminal). GitLab refuses `rebase` outright:
  it merges by the project's merge method, and quietly merging another way
  would be worse than saying so.
- **Pinned head.** `mr_merge` passes the phase's starting tip (`phaseTip`) as
  the head to merge — the head the forge called mergeable, or the head a
  person started the phase on. A push after the decision makes the forge
  refuse; `mr_mergeable` fires again for the new head. Templates should
  route that `on_fail: end` rather than to a person.
- **Reconciling.** Both record `action_started` first. `mr_merge` re-entered
  reads the state: already merged = this merge. `post_review` marks every
  part (`ReviewMarker(id, part)`, the id = phase run + stage): GitHub posts
  one review, all or nothing, after checking no review carries the marker;
  GitLab posts each note as a discussion and the summary as a note, each
  skipped when its marker is already there. `IsFactoryReply` recognises the
  marker, so a review's own notes never read as a reviewer's comments.
- **Lines.** A note goes on its line only when the line is in the diff
  (`ports.DiffLines` reads the unified patch; GitLab needs the old line for
  an unchanged one); otherwise it is folded into the summary with its place
  (`FoldedNotes`) — GitHub would refuse the whole review for one bad line,
  and an agent's line numbers are not always right. GitLab notes it will not
  anchor (400/422) are folded too.
- **Output type `review_threads`**: `{summary, threads: [{path, line,
  body}]}`, ≤ 50 threads, checked when the agent's stage ends
  (`checkReviewThreads`, next to `checkReviewReplies`) so a bad review is
  that stage's failure with `$FAILURE`, not post_review's.
- Events `review_posted` (inline, folded, already) and `mr_merged` (method,
  head, branch deleted) — the latter only for the run's own merge; a
  person's is seen by observation. `mr_merge` is `Remote`: no workspace.

## Sentinels: one range per name — $DIFF_TARGET, $DIFF_PHASE, $STATUS

2026-10-24, with the user, ahead of runs on others' merge requests.
`$CHANGED` meant two ranges by where it was bound: the branch against its
target in an entry phase, what *this round* changed (from the phase's
starting head) in a subject phase. A reviewer re-reviewing after the
author's push would have read an empty list — the round itself changed
nothing. The user asked for names in git terms that say exactly the range:

- `$DIFF_TARGET` = `git diff --name-status <target>...HEAD`, everywhere;
- `$DIFF_PHASE` = `git diff --name-status <phase start>...HEAD` (from the
  target when the phase began on no recorded head — the delivery);
- `$STATUS` = `git status --short` (was `$UNCOMMITTED`).

Not `$DIFF_HEAD` (git's `diff HEAD` is the uncommitted changes) nor
`$UNSTAGED` (it would exclude staged and untracked files). The prompt's
INPUT header prints the git range, so an agent reads what it was given. The
old names are refused with their replacement (`retiredSentinel`) — an error
message, not compatibility: saved flows using them must be edited, and an
in-flight run whose pinned flow binds one fails with the parse error. The
default flow's review-round `reply` stage reads `$DIFF_PHASE`; its reviewer
and redactor `$DIFF_TARGET`.

## Flow v4: runs on a merge request someone opened — `mr_opened`

Second pass, step 5 (first half), 2026-10-24: what the reviewer flow needs.

- **An entrypoint signal, not an option.** The plan had `RunInput.MR` on any
  entrypoint; a second Entry signal, `mr_opened` (`Brings: merge_request`),
  says it in the document instead. Parse time then knows the entry phase
  has a subject (`HasSubject`, so `post_review` there warns of nothing) and
  that the run has a merge request without a `create_mr` (`OnMR` in
  `definitelyHasMR` and `phaseShapeWarnings`); run creation holds the kinds
  apart (`entrypointOf(fl, name, onMR)`: an MR at a `run_start` phase, or
  none at an `mr_opened` one, is refused; the default entrypoint is the
  first of the kind asked for). Preflight checks its forge like an observed
  signal's. `HasSubject` now also excludes `mr_approved` (it holds for every
  merge request, like `mr_merged` — a slip from step 3).
- **Port.** `GetMR`, `ListMRs` (open, newest first, ≤ 300), `Whoami` (for
  the automation). `MRInfo.HeadRef` is the forge's own ref for the head
  (`refs/pull/<n>/head`, `refs/merge-requests/<iid>/head`), which works for
  forks too; `Fork` = head repository ≠ base.
- **Creation** (`adoptMR`): the merge request must be open; its checkout's
  branch is its source branch (a fork's, or a name `CheckBranch` refuses,
  becomes `mr-<iid>`), its source the target, `Checkout.Fetch` the head
  ref, `Checkout.Fork` the flag. `run_created.mr` records it (repo, iid,
  url, head, author, title); the projection adds it to `mrs` (with its
  author) and `adopted`; replay adds the merge request record — with its
  head known, so `mr_head_moved` fires only on the author's later pushes.
- **Bootstrap** (`branchScript`): an existing branch is re-entered, else
  `git fetch origin <ref> && git switch -c <branch> FETCH_HEAD`. For a
  same-repository branch `git switch <branch>` already succeeds by creating
  it from `origin/<branch>` — the same commit.
- **Subject from the start.** `beginRun` begins an `mr_opened` phase with
  the merge request as subject and its creation head as tip. `followAdopted`
  runs before a phase run's first workspace stage: `git fetch <ref> && git
  merge --ff-only FETCH_HEAD` once per starting tip — the author's branch
  moves under the run; a checkout that diverged (the run committed) is left
  alone. `push_branch` refuses a fork's checkout (routable).
- **API/UI.** `mr` on the start-run and rerun bodies; `GET
  /api/projects/{id}/mrs?repo=`; the start form shows a picker of open merge
  requests when the chosen entrypoint is on `mr_opened`; the editor offers
  the entrypoint; a rerun stays on the merge request (refused once it is no
  longer open).

## Automations: forge_mr — a run on each merge request others open

Second pass, step 5 (second half), 2026-10-24.

- **Kind `forge_mr`** watches one repository (`repo`, "" = the primary;
  migration 0006 adds the column and widens the kind check). Each pass lists
  the open merge requests (newest first, the watcher's limit) and, for each
  one that is not a draft and not opened by the integration's own account
  (`Engine.ForgeUser` → `Whoami`: the factory's own runs' merge requests),
  makes a ticket — title "<title> (!<iid> by <author>)", goal the
  description and a line naming the merge request, `source_kind
  merge_request`, key and URL the merge request's, no source id, so no
  tracker: tracker actions skip — and creates one run on it
  (`RunInput.MR`) at the automation's `mr_opened` entrypoint. Once each:
  a ticket with that key and runs is skipped; this automation's ticket
  without a run (a crash between the two) is adopted; a person's ticket for
  the same merge request is theirs. A draft is picked once it is marked
  ready.
- **Entrypoint kinds are validated per automation kind**: forge_mr needs an
  `mr_opened` entrypoint, tracker_query and cron a `run_start` one (the
  first of the kind when `phase` is empty). The workshop form offers only
  those (`StartRunOptions entries=…`) and never asks for a merge request
  (`askMR=false`).
- **Migrations 0003/0004 were not re-runnable** (`CREATE TABLE` without `IF
  NOT EXISTS`), against the rule that a post-squash migration is a no-op
  where its result exists. `TestMigrateCatchesUpAnInitEditedInPlace`
  re-applies everything after 0001 and caught it — but it only runs with
  `TEST_POSTGRES_DSN`, so nobody had. Fixed in place (an applied migration
  is never re-run, so the edit reaches only databases that need it); the
  pgstore suite passes on a throwaway Postgres 16.
