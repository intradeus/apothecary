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

- `EnvSpec{RunID, RepoURL, Branch}` — exactly the non-secret workspace
  parameters of §11. `Command{Script, Timeout}`; `ExecResult{ExitCode,
  Output}` where Output is the combined tail (adapters stream full logs to
  the runs dir per §12).
- `AgentSpec{Agent, Harness, Model, MaxTurns}` — resolved values from the
  flow, agent name resolves workspace-side (§5.2).
- `RepoRef{URL, DefaultBranch, ProjectID}` — ProjectID is the GitLab-side id
  from config (§13); other repository hosts may ignore it.
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
- **ask_human answers in events**: a form's `answer` is `accept` (values in
  `content`, by field id) or `decline` (the reason in `feedback`); the shim
  receives the engine-composed text.
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
  (best-effort);
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
  inbox → curl answer) live before its formal test. **Superseded
  (2026-11-02)**: in opencode 1.17.8 edit, write and patch share ONE
  permission, `edit`, so `write: true` re-allowed every edit — a read-only
  agent with outputs could change any file (found by
  `TestOpencodeConformance`). A read-only agent now gets
  `permission.edit: {"*": deny, <each output path>: allow}` (a files
  output's directory as `<dir>/*`, which covers nested files); the paths are
  its stages' `to:` (`agentspec.Spec.OutputPaths`, filled by
  `flow.WorkspaceAgents`). One with no outputs gets `edit: deny`.
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

*Superseded by "Observability and run analytics (TODO 23)" below: the
collector/Prometheus/Grafana stack, `APOTHECARY_OTEL_ENDPOINT` and the three
event-table dashboards are gone.*

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
  (project.workspace.template override), since provisioner adapters bind
  their template at construction. Generations are refcounted by live
  resolutions; a config edit stales the old instance, closed on last
  release (locked by a resolve test). Repository host/harness/provider adapters are
  stateless and constructed per resolution.
- **M7 requires `workspace.template` on every project** — the §8 image
  pipeline lands in M9; until then the §8.4 custom-template path is the
  only path. The validation moves behind the pipeline in M9.
  `import-config` sets it to the v1 template, keeping behavior
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
- **M7 acceptance evidence** (ephemeral Postgres 16 in docker): migration
  0002 applied; full store suite green; serve booted from APOTHECARY_* env
  vars alone; dev project seeded; ticket → 9-stage v1 flow → gates
  answered via API → `run_completed` with project_id projected;
  import-config on a synthetic v1 factory.yaml created 4 integrations +
  project (template=factory, locator=123) + settings, zero plaintext at
  rest; the **containerized** engine (deploy/Dockerfile, host network,
  env-only boot) served the CPUI, seeded dev, and completed the same
  9-stage flow, with the docker CLI verified inside the image; `go test ./...` green.
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
- **The project wizard is one sectioned form**, not a stepper: repository host pick →
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
  Valid ports are repository|provisioner|harness.
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
  (Dockerfile + shim + firewall) and the workspace template are `go:embed`ed by
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
  template missing.
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
  slams egress shut (loopback + established only, no lock
  file) on any unexpected script failure — fail-closed by construction.
- **M9 acceptance evidence** (live, Lu's machine, deployed stack):
  greeter-factory-test (Go+make fragment) full envcheck PASS incl.
  `verify` (`make verify` on the real repo) — first pass built
  apothecary/base:e6830826… + ws:f9f248db… (cached=false), re-check
  cached=true; py-smoke (python preset) PASS on its own image
  (ws:987b4243…, base layer shared); the engine pushed template version
  vc101fd0f… and provisioned real workspaces from the built tags through
  the new parameters; induced failures produced named steps —
  `egress:doesnotexist.invalid` (deny checks green, firewall held) and a
  wrong provisioner token surfacing its 401 through the Test endpoint. The make-not-found first attempt was itself the composability
  working: the stripped base genuinely lacks what fragments don't add.

## GitHub repository host + codex harness (M10, DESIGN2 §12–§13)

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
- **README.md** is the M11 install path: prerequisites (docker,
  repository host PAT, harness auth, target repo with AGENTS.md +
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
  dashboards updated to match).
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
  provisioner ReadFile/WriteFile paths. Belt-and-braces: agent-spec names must
  be lowercase slugs, flow model strings a strict charset.
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
- workspace firewall: IPv6 is fail-closed (kernel-has-v6 + failing drop
  now trips the ERR trap; fail_closed also drops v6); release.yml checks
  the REMOTE for the version tag (re-runs were always red).

**Known-and-deliberately-left**
- opencode prompt rides argv (`"$(cat …)"`) with the ~2 MB ARG_MAX
  ceiling; codex uses stdin. Switching opencode to stdin is untested
  against the CLI — revisit with a live run.
- Env-capacity admission is still check-then-create (single-user risk).

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
- `ports.RepoHost` gained `ListDir`/`ReadFile` (gitlab tree/files APIs,
  github contents API, fake in-memory) solely for the one-time
  `POST /api/projects/{id}/agents/import-repo`; existing names are
  skipped, never overwritten. The fake repository host ships one importable
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
`RepoHost.ListDir`/`ReadFile` port methods added solely for it (gitlab,
github, fake adapters). `agentspec.Parse` stays (name-rule + spec
parsing). Reference agent prompts now live in `artifacts/agents/*.md`
(+ `artifacts/flows/v2.yaml`, the expanded-form reference flow), with the
neutral YAML specs in `examples/agents/`.

## Live run feedback (working card + workspace log tail)

Between stage_started and a stage's end the UI used to show nothing —
stage logs were post-hoc and the provisioner buffered exec output in
memory. Now:

- `ports.ExecLogTailer` is an OPTIONAL EnvProvisioner capability
  (`TailExecLog(env, n)`). Read-side by design: a sink parameter on Exec
  would have rippled into the AgentRunner path (harness adapters call
  Envp.Exec directly); a tail lets each adapter capture however it wants
  and the API degrade honestly when a provisioner can't stream.
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
  passes its port to the resolver (forwarded into every workspace, like
  the engine port) and to the engine
  (`SetEgress`/`EgressController`). The run loop sets the policy from
  `res.EgressDomains` (already = derived domains ∪ project extra_domains)
  before any workspace network use, and drops it at teardown.
- Workspace: bootstrap writes `/apothecary/proxy.env`
  (HTTP(S)_PROXY → 127.0.0.1:proxyPort with the run creds, NO_PROXY =
  loopback); every bootstrap step and both harness adapters source it, so
  git clone and agent invocations all leave through the proxy. The
  template gains an `egress_proxy` parameter → `APOTHECARY_EGRESS_PROXY`;
  when set, `init-firewall.sh` runs in PROXY MODE: OUTPUT drops everything
  except loopback (reaches the tunneled proxy + shim + code-server) and
  established. No IP pinning, no DNS, no
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
403). The allowlist was fine — gitlab.com is contributed by the repository host
account's EgressDomains (github: github.com/api/codeload; gitlab: the
base_url host) and unioned into every run. Fix: the provisioner's Exec sources
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
capability — the provisioner's Reattach re-establishes the tunnel
(idempotent); ensureEnv reuse path calls it. (2) reuse path
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
piece being the reference @devcontainers/cli. Decision: the repo-devcontainer goal ("run whatever the
target repo defines") is implemented on **@devcontainers/cli + docker CLI**
(`internal/adapters/devcontainer`).

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
  propagate verbatim and stdout/stderr stay separated — no exit-255
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
  the embedded source in a `golang:alpine` stage. Same daemon that runs workspaces ⇒ arch always matches.
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
- devcontainer: `shim relay --reverse` over a bind-mounted unix
  socket (host→container IP is blocked on hardened hosts, so the reverse
  relay mirrors the engine bridge). The dead container-IP `app` links the
  devcontainer adapter used to emit are gone — unreachable off-host.
- API `POST /api/runs/{id}/expose-port`; `cmd/apothecaryd` binds listeners
  on the CPUI's own interface (loopback by default; the deploy already runs
  `network_mode: host`). Same no-auth trust level as the CPUI itself —
  documented. This SUPERSEDES the earlier appPort/compose-collision
  follow-up: exposure needs no published host ports.

## Devcontainer egress firewall

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
  unix-socket bridge/relay survive the DROP.
- **`ports.EgressOpen` marker → `ports.EgressReporter` (per-env method).**
  The firewall varies per env, so a type-level marker no longer fits: the
  devcontainer adapter firewalls image/Dockerfile configs but leaves
  **compose** open (its primary service reaches sibling services by name,
  which default-deny would break) and honors a per-project `firewall: off`
  switch. It records the fact in memory + an on-disk marker (survives engine
  restart) and reports it; `envcheck` keys its firewall invariants on the
  reported value (skips honestly for open envs), defaulting to closed when
  the capability is absent.
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

*Superseded by "Tickets follow their tracker (TODO 49)" below: a sourced
ticket now mirrors its issue (title, body, status, type, labels, comments)
and is re-read by a sync; the "copy taken once" below no longer holds.
Deleting the integration unlinks its tickets onto the internal tracker.*

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
- **Not a project reference.** Unlike repository host/provisioner/harness, a ticket
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
the bare name failed. (Since 2026-10-28 that text rides the role instead —
see *ask_human mechanics ride the role, not the flow*.)

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

Found live: the built-in flow's review stage pinned `opencode-go/glm-5.2`. The
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
(`Project.repos`: name, URL, default branch, optional repository host locator;
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
  reconciles per checkout through the repository host (`FindMR`) *before* reading the
  body, so a crash in the record window never duplicates one; `mr_created`
  now carries `repo` and `target`. A repository listed **without a locator**
  has no repository host and is **context-only**: cloned for the agent to read, never
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

### Repository host facts (M28)

`RepoHost.MRState` became `MRStatus` — state, mergeability (`clean | conflicts |
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
  `idle()`, which polls the repository host on the existing `MRPollInterval` (threads on
  `ThreadPollInterval`, default 4× — a heavier call), takes confirmation
  answers and manual starts, and returns when a phase starts or the run ends.
  It is resident like a signal-armed gate but **does not hold a concurrency
  slot** (`busyLocked` skips idle handles; the scheduler admits a queued run as
  the loop goes idle). A phase starting from idle does not wait for a slot, so
  the cap can be exceeded by phases briefly — they are short, and blocking a
  conflict fix behind unrelated tickets would be worse.
- **Triggers** are pure functions of repository host facts, evaluated per merge request
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
- **Config dirty hint** (a "dirty workspace"): the adapter records a
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
  `ports.TicketWriter` (`Transition/Comment/ListComments/SetFields/Fields/AddLink`
  — `Comments` became `ListComments` in TODO 49)
  is discovered by type assertion on the `ports.TicketSource` adapter, so one
  integration — one credential — serves both the import and the write-back,
  and an adapter that never learned to write is simply not a valid target
  (`resolve.Resolver.TicketWriter` refuses it by name). The read port's "the
  engine never writes back" promise narrows to "nothing writes unless a flow
  says so, stage by stage" (TODO 49 adds: or the user changes the ticket's
  status, labels or comments in apothecary).
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
- **Unlinked ticket ⇒ skip, visibly.** *(Superseded by TODO 49, below: a
  ticket with no source lives on its owner's internal tracker, so tracker
  stages act on it; a skip now means "no ticket", a ticket deleted mid-run, a
  step no status holds, or a non-label field on the internal tracker.)* The
  skip appends `stage_completed{skipped: true, note}` and follows
  `on_success`. Two new fields on an existing event rather than a new type:
  replay, the projection, telemetry and the UI's "attempt ended" lists all
  tolerate a zero-cost completion, and the timeline renders the note.
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
The repository host triggers keep `Trigger` only; `Entry` is the seam for a tracker
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
- **Canonical statuses** — *superseded by TODO 49 (below): they are flow
  steps (`trackerport.FlowStep`), `TransitionTo` and Jira's `status_map` are
  gone, and a step resolves through the integration's status list.*
  `ticket_transition` takes `to` (todo / in_progress / in_review / done /
  blocked) or `status` (the tracker's word), exactly one. A step no status
  holds skips the stage rather than failing, so one flow fits every tracker.
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
  what preflight's repository host check and the end rule read). It takes `start`
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

## Flow v4: repository observation — CI, approvals, the merge verdict, others' pushes

Second pass, step 3 (2026-10-24). Four signals, each one registry entry and
one predicate in the idle loop; the scheduler, the policies and the
occurrence keys did not change.

- **Port.** `MRStatus` stays one repository host call and gains what that call already
  carries: `MergeableNow` (GitHub `mergeable_state` clean/has_hooks and not a
  draft; GitLab `detailed_merge_status == mergeable`) and `Draft`. Checks and
  approvals are two new methods, `MRChecks(mr, head)` and `MRApproval(mr)`,
  so a flow that reads neither never pays for them (`readsOf` scans phase
  heads, wait stages and gate auto-answers; `fake.RepoHost.ChecksCalls` holds
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

Second pass, step 4 (2026-10-24): the two host-side acts the modeled flows
need beyond opening a merge request — auto-merge (a reaction on
`mr_mergeable`) and the reviewer flow.

- **Port.** `MergeMR(mr, {Method, Head, DeleteBranch})` and `PostReview(mr,
  {Head, Summary, Notes, ID})`. A merge the repository host declines is a typed
  `*ErrMergeRefused` (GitHub 405/409/422, GitLab 405/406/409/422) that the
  engine turns into a routable `input_invalid` failure; anything else is the
  repository host not answering (infra, terminal). GitLab refuses `rebase` outright:
  it merges by the project's merge method, and quietly merging another way
  would be worse than saying so.
- **Pinned head.** `mr_merge` passes the phase's starting tip (`phaseTip`) as
  the head to merge — the head the repository host called mergeable, or the head a
  person started the phase on. A push after the decision makes the repository host
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
  first of the kind asked for). Preflight checks its repository host like an observed
  signal's. `HasSubject` now also excludes `mr_approved` (it holds for every
  merge request, like `mr_merged` — a slip from step 3).
- **Port.** `GetMR`, `ListMRs` (open, newest first, ≤ 300), `Whoami` (for
  the automation). `MRInfo.HeadRef` is the repository host's own ref for the head
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

## Automations: merge_request — a run on each merge request others open

Second pass, step 5 (second half), 2026-10-24.

- **Kind `merge_request`** watches one repository (`repo`, "" = the primary;
  migration 0006 adds the column and widens the kind check). Each pass lists
  the open merge requests (newest first, the watcher's limit) and, for each
  one that is not a draft and not opened by the integration's own account
  (`Engine.RepoHostUser` → `Whoami`: the factory's own runs' merge requests),
  makes a ticket — title "<title> (!<iid> by <author>)", goal the
  description and a line naming the merge request, `source_kind
  merge_request`, key and URL the merge request's, no source id, so no
  tracker: tracker actions skip — and creates one run on it
  (`RunInput.MR`) at the automation's `mr_opened` entrypoint. Once each:
  a ticket with that key and runs is skipped; this automation's ticket
  without a run (a crash between the two) is adopted; a person's ticket for
  the same merge request is theirs. A draft is picked once it is marked
  ready.
- **Entrypoint kinds are validated per automation kind**: merge_request needs an
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

## Built-in template: reviewer

Second pass, step 6 (first piece), 2026-10-24 — ahead of the Reviews tab,
which needs a flow to start.

- `internal/builtin/flows/reviewer.yaml`: created on someone's merge request
  (`first_look` on `mr_opened`), reviews it through one `review` block —
  `pr_reviewer` (read-only, bash for tests; `$DIFF_TARGET`, `$DECISION`,
  `$FAILURE`) writes a `review_threads` file, an optional gate
  (`$review_gate`, off by default) lets a person read it, `post_review` posts
  it — and again from a reaction on `mr_head_moved` after the author's pushes
  stop for `$rereview_quiet` (10m). Outcomes `merged`/`closed` say `then:
  done`: either way the review is over. Never pushes, opens or merges (the
  builtin test holds that).
- Re-reviews were meant not to repeat themselves by reading their earlier
  review file. That never worked: the engine deletes a stage's outputs
  before every fresh attempt (M18), so the re-review never saw it. Since
  2026-10-30 the sequence polls the merge request first (`load_review_comments`
  with `scope: all`) and the engine refuses a repeated note — see *Review
  memory* below.
- Notes are prefixed `[blocking]`, `[suggestion]`, `[question]`, `[nit]`, at
  most fifteen (the action allows fifty): the summary takes the rest.
- `TestBuiltinReviewerTemplateRuns` drives the shipped YAML itself through
  the engine harness (harness/model swapped for the fake).

## Workshop Reviews tab

2026-10-24, asked for by the user: a page listing the repository's merge
requests with a button to start a review.

- **API.** `GET /api/projects/{id}/reviews?repo=` — each open merge request
  (`ports.MRInfo`), `mine` when the repository host integration's own account opened it
  (the factory's runs), the merge request's ticket (looked up by its URL in
  one ticket scan) and that ticket's newest run with the run count. `POST
  /api/projects/{id}/mrs/{iid}/review {flow?, phase?, params?, repo?}` — the
  merge request must be open; its ticket is found or made
  (`tickets.Factory.ForMR`, shared with the merge_request automation through
  `FindByKey`/`CreateForMR`, so a person and the automation file reviews
  under the same ticket); 409 while one of the ticket's runs is not
  terminal; the run is created on the merge request at the flow's
  `mr_opened` entrypoint (`reviewer` by default). A ticket made by a start
  that is then refused is deleted with the refusal.
- **UI.** A Reviews tab between Tickets and Flows: others' merge requests
  first, the factory's own after them — flagged "opened by the factory", but
  reviewable like any other (the user wanted the option; only the merge_request
  automation never picks them); a row shows the newest run's state (linked)
  and "Review" / "Open the review" / "Review again". The flow is picked among those with an `mr_opened` entrypoint,
  remembered per workshop in localStorage (`reviewer` when present).
  Refreshes every 30s while visible.
- **Fake repository host per locator.** The dev `fake` repository host kind used to hand out a
  new, empty repository host on every resolution; it now keeps one per repository
  locator for the process (`kinds.FakeRepoHostOf`), which is what made the API
  test possible — and the dev world now remembers its merge requests.
  Tests sharing it reset it (`fake.RepoHost.Reset`).
- Not done: a repository picker for extra repositories, CI state per row
  (one more repository host read per merge request per refresh).

## Repositories tab replaces the Reviews tab: the UI stays flow-agnostic

2026-10-24, the user: "Review is a flow… flows shouldn't have distinctions in
the UI… the UI should be as abstract as possible." The Reviews tab (above)
was built around one template. It is gone; in its place:

- **Repositories tab** (Workshop → Repositories, beside Tickets): what the repository host
  integration brings in — the repository's open merge requests — the way
  Tickets shows what the tracker does. Each row: the merge request (its
  author; draft, fork, "opened by the factory"), its branches, its newest
  run (state, flow, when; all its runs through its ticket) and **Start a
  run**: a picker of every entrypoint on `mr_opened` of every flow of the
  workshop (`flow · phase` when a flow has several). A review, a rebase,
  anything a flow does on a merge request is one pick away; none is named.
- **API, generic too**: `GET /api/projects/{id}/mrs` now returns those rows
  (`MRRow`: the merge request, `mine`, ticket, newest run); `POST
  /api/projects/{id}/mrs/{iid}/runs {flow?, phase?, params?, repo?}` starts
  a run of any flow on it (the project's default flow when unnamed — no
  built-in name baked in). The guard is per flow: the same flow twice at
  once on one merge request is refused (a double click), different flows
  may run side by side. `/reviews` and `/mrs/{iid}/review` are removed.
- The start-run form's merge-request picker reads the same `/mrs`.
- The rest of the UI was audited for the same rule (next entry).

## The rest of the UI, audited for flow-agnosticism

Same rule as the Repositories tab: nothing in the UI assumes one particular flow,
its stage ids, its agent names or its artifacts. What the audit found and
what changed:

- **New flow was broken.** The skeleton named an agent `builder` it never
  declared, so the document did not parse. It now declares one neutral
  agent (`worker`, stage `work`); `apothecaryd validate` accepts it.
- **Plan review → document review.** The workbench reviews whatever file a
  ping hands over (`PlanReview.svelte`/`planreview.ts` →
  `DocReview.svelte`/`docreview.ts`, `plan.*` → `doc.*` strings). Markdown
  is rendered and anchored by heading as before; any other file (YAML,
  JSON, code) is shown as written, split at blank lines into listings that
  comments anchor to by line. The feedback is headed `REVIEW OF <path> — N
  comments:` (was `PLAN REVIEW`), the call to action is `Review <file>`
  (was "Review plan") and the page title names the file (was "the recipe").
  The reviewer template's gate on `artifacts/review.yaml` reads right now.
- **Run page.** The stage strip shows the phase the run began at, then every
  other phase in the order it first started, grouped and labelled — no
  phase is the main one (was: the begun phase, other stages tacked on). The
  header's merge-request slot shows the merge request a run was created on
  (`adopted`, new on the run in the UI) and the ones it opened; the "not
  opened yet" placeholders appear only when the pinned flow has a
  `create_mr` stage, and a run with neither shows no slot.
- **Wording.** "builder" (composer placeholder, agent note), "keeps the
  plan, build and commits" (retry), the agent's to-do list called "The
  recipe" (now "Checklist"; *recipe* stays the lore word for a run's flow),
  outcome text naming merges and closes, "review verdict: APPROVE", the
  project form pre-filling a `verify` command, and the flow editor's
  `builder` / `artifacts/plan.md` / `verify` / "implementation plan"
  placeholders — all made neutral. `in_review` keeps its label: the
  projection sets it only when the run has open merge requests.

## "Repository" everywhere — in the UI, the code and the schema

2026-10-24: the old name of the repository port meant nothing to anyone, said
the user, first for the UI and then "a full rewrite — migrations, internal…".
It is gone from the repo:

- **Vocabulary.** People read *repository*: the workshop's **Repositories**
  tab (`?tab=repos`, `ReposTab.svelte`), the Integrations group, the project's
  *repository account*, run-page and engine messages ("no integration reaches
  repository …", "needs a repository integration (GitLab, GitHub)", health
  step `repo_access`). `portLabel()` (`format.ts`) prints ports, never raw.
- **Code.** The port is `ports.RepoHost` (`res.RepoHost`, `RepoHosts` per
  extra repo, `repoHostFor`); kinds are `RepoHostKind` / `RepoAccount` /
  `RegisterRepoHost`; fakes `fake.RepoHost`, `kinds.FakeRepoHost(Of)`; the
  account's user `RepoHostUser`. A host-neutral word where *repo* alone would
  be ambiguous: `RepoRef` is one repository, `RepoHost` the service holding
  them.
- **Stored and API values.** Port value `repository` (integrations.port, a
  spec's `requires`); the project column and field `repo_account_id`
  (`repoAccountId` in the UI); the automation kind `merge_request` (its
  constant `AutomationMergeRequest`). The parser no longer names the retired
  stage type that opened a merge request — a stray one gets "unknown type".
- **Migrations edited in place.** `0001_init.sql` (the column, comments) and
  `0006_merge_request_automations.sql` (renamed; the kind check) carry the new
  names and `TestInitMigrationIsFrozen` holds the new hash. That breaks the
  freeze's promise on purpose (noted at `preSquashHead`): a database created
  before this edit does not match the code. The project is unreleased, so such
  a database is reset (`task dev:db:reset`) or upgraded once by hand — renaming
  the column and its foreign key, rewriting the port and kind values, and
  re-adding the kind check — never by a migration that would keep the old
  names in the repo.

## The flow editor opens a block beside the flow

2026-10-24, the user: an include node only said "include" — "not being able
to see in the flow what this node does". Two changes in the editor:

- **The node says what it runs.** Its chip reads *block* (`STAGE_WORD`), its
  line the block's chain (`review: read → gate → post`), its tooltip the
  block's description.
- **Selecting it opens the block beside the flow.** The canvas splits: the
  flow on the left, the block on the right (55%), drawn by the same
  `FlowGraph` in a read-only *block mode* (`block`, `readonly` props): a
  marker named after the block, its stages stacked top to bottom
  (`columnLayout` — a block is a short chain, read best down a narrow pane),
  and its two ways out instead of the run's ends — *back · X* (the including
  stage's success, X resolved) and *fail · Y* (its on_fail), new terminal
  tones `exit` / `failexit`. An include inside the block opens that block in
  its place, with a breadcrumb back. Selecting anything else, clicking the
  empty flow canvas or Escape closes the pane; clicks inside it keep it open.
  Both graphs fit again on every open and close (`FitOnInit` `refit`,
  animated, after the canvas has resized), the block's with a lower legible
  floor so it always shows whole.
- Blocks are still edited in the YAML tab; the view is for reading.

## The built-in "default" flow is "builder"; the editor's layout per phase

2026-10-24, three fixes the user asked for together:

- **Opening the built-in flow crashed the editor** ("Cannot read properties
  of undefined (reading 'on_success')"). `autoLayout` walked a phase's
  success chain until `end` or `done` — the `closed` outcome ends on
  `on_success: abort`, so it looked `abort` up as a stage. The walk now stops
  at anything that is not a stage of the document.
- **Renamed: `default` → `builder`** (`internal/builtin/flows/builder.yaml`).
  *Default* still means what a project starts runs with
  (`projects.default_flow`); the template is called by what it does.
  Migration `0007_builder_flow.sql` moves
  what named the built-in — a project's default flow, its binding, its
  automations — and leaves alone a project's own saved flow named `default`
  (a flow's YAML `name:` must equal its id, so a copy is not renamed behind
  its back). Runs keep the flow name they were created with.
- **Phases as rows, beginnings in a column.** Auto-arrange already gave each
  phase its own band; now every phase marker (the entry's and each later
  phase's) starts at the same x, the widest one setting the column, so the
  entrypoints read down the left edge. Arranging fits the view again, and a
  fit that has to zoom in to stay legible anchors a flow taller than the
  canvas at its top-left — the entry — instead of its vertical middle.

## No code names a flow

2026-10-24, the user: "Why do we need DefaultFlowName in the code? … No
backend code must use \"builder\" anywhere; it should just be used for
display / in a dropdown." `builtin.DefaultFlowName` is gone, and with it the
fallback that gave a project created without a `default_flow` the built-in
template: `default_flow` is now required (400 "default_flow is required"),
always the project's choice — the project form's dropdown lists the flows it
can run, templates included, and preselects the first. Templates are found
like any flow, by the name a project or a run gives (`builtin.Flow(name)`,
every `flows/*.yaml`). The `-dev` seed, which must give its project a flow
that resolves before the project's own `dev` flow can exist, borrows
whichever template `builtin.FlowNames()` lists first. Tests name the template
they exercise.

## A pass for phones

2026-10-24, the user: "The mobile view is very bad for the runs… default to
the focus layout — the focus layout should have a single pane with all
windows… with a horizontal scroll for when the tabs go off-screen." Every
page was loaded at 390×844 (touch) and measured for content past the edge;
what changed:

- **Focus is one window** holding all fourteen views (it was the flow strip
  over the rest).
- **The run page on a narrow screen is compact** (page ≤ 760px wide):
  `DeskState.setCompact` swaps in the focus layout in memory — the saved
  layout is set aside, never written over, and comes back as it was when the
  page is wide again; the view in front stays in front. Compact tabs are
  tapped (no drag, so a swipe scrolls the row: `touch-action: pan-x`),
  without hide ×, split, maximise or the layouts button. The page itself
  scrolls: the header scrolls away and the window keeps the page's full
  height (`--desk-h`), so it never collapses to nothing on a short phone.
- **Tab rows scroll sideways and keep the selected one in view**
  (`keepInView`, `lib/ui/inview.ts`): the run window's tabs, the workshop's
  sections (their selected tab was off-screen from Flows on), and the side
  nav in its row form (System sections, the workshop form's steps), whose
  items no longer shrink to "I…", "C", "Me…". Their ends fade.
- **Wrapping instead of clipping**: section headers (Integrations' "+ Add"
  buttons), pane heads, the Flows tab's rows (name, then version and age,
  then the actions), the Agents tab's "runs on" (stage and harness whole).
- **A bug any width had**: short-form parameters (`harness: fake`) read as
  "undefined" on the Agents tab — `normalizeDoc` now gives every parameter
  its long form, as the editor already assumed.

## The run header and the alert banner on a phone

2026-10-24, the user, on a run created on someone's merge request: "very
ugly — the run, workspace and merge request cards are awkward, the egress
blocked warning is not mobile friendly."

- **The title is three lines at most**, at every width (`line-clamp`, the
  whole of it its tooltip): a merge request's title plus its author's handle
  (`(!21 by project_84607299_bot_…)`) ran to five lines of large italic.
  On a phone it is smaller (`--fs-section`, its accent `--fs-mark`) and the
  state sits under it.
- **Run, Workspace and Merge requests are one card on a phone**: the three
  glass groups stack edge to edge, a row each — what it is on the left,
  its buttons on the right — instead of three cards of three widths. The
  workspace's id gives its room to the buttons there.
- **`Banner` wraps on a phone**: the alert and its aside, then the items
  (the blocked hosts, which were cut off before), then the buttons, right
  aligned. Its buttons are one group pushed right at any width.
- The compact run page never scrolls sideways (`overflow-x: hidden`); the
  banner had pushed it 140px wide.

## Built-in template: conflict-resolver

2026-10-24, the user: "add a new builtin flow: conflict resolver". A third
template beside `builder` and `reviewer`, for a run created on a merge
request (`on: mr_opened` — the Repositories tab or a merge-request
automation): it brings the merge request up to date with its target.

- **One block, `settle`**: `sync_target` (the engine merges or rebases —
  `$strategy`, `merge` by default so nobody's commits are rewritten unasked);
  a conflict pauses it with the files named and sends the `resolver` agent
  (the builder's resolver, told the change is someone else's: the TASK is the
  merge request, `changes` is `$DIFF_TARGET`) to edit the markers away, up to
  six rounds, then `debug`; the project's tests (`$verify`); `push_branch` to
  the merge request's branch, leased against the head the phase began on.
- **Phases**: `first_pass` (the entrypoint) runs it; `again` (on
  `mr_conflicts`, `start: $again_start`, `confirm` by default) runs it each
  time the target moves and it conflicts anew; `merged` / `closed` end the
  run. It never opens, merges or reviews a merge request.
- **A fork** cannot be pushed to: the push fails to `debug`, where a person
  has the workspace with the resolution in it.
- Tests: `TestConflictResolverTemplate` (shape), and
  `TestBuiltinConflictResolverTemplateRuns` (engine, end to end with the
  fake git: conflict → resolver → merge finished → tests → leased push; a
  second conflict confirmed and settled; closing ends the run).

## Flow v5: one main chain, watchers, outcomes

2026-10-24, the user: "I like the concept of watchers — phases which react to
events. Different than the main phase … a flow becomes 1 phase only — the
main action phase … and in the background, while it runs, watchers for merge
conflicts, PR comments etc." The design (plan: *Flow v5*) keeps v4's
machinery — wait points, one workspace and mutex per run, the idle loop,
blocks, parameters, pinning — and changes the document. This is its first
step: the grammar, the engine, the templates, the API and the UI. `use:`
(other flows' chains run in place, compiled into the pinned text) and one
writer per branch are the next ones.

- **The document** (`internal/flow`): `version: 5`; `run_start: ticket |
  merge_request` says what runs are started on (a person or an automation
  starts one — never something the flow listens for); `main:` is the chain a
  run starts with; `watchers:` run their chain on each new occurrence of a
  signal, or by hand (`on: manual`), and never end the run; `outcomes:` fire
  once and end it, after their chain if any, `then: done` (default) or
  `abort`. `run: X` is a chain of one include of `X` named after its watcher
  (`again.read`, …), and `main` is a block other chains may run. The parser
  still produces one runtime `Flow` with `Phases` — now tagged with a `Kind`
  (main, watcher, outcome) and ordered by section — so the scheduler, the
  holds, the start policies and the done rule did not change. A flow with no
  `main` is a caretaker: it must be `run_start: merge_request` and its runs
  start idle. v4 documents are refused with a message naming the v5 spelling
  (`phases:` / top-level `stages:`).
- **One way in.** `mr_opened` is gone (it was only a `run_start` with a merge
  request), and so is the per-run entrypoint: `RunInput.Phase`,
  `run_created.phase`, the view's `begin_phase`, the start-run, rerun, merge
  request and automation bodies' `phase`, and the automations' column
  (migration `0008_automation_phase.sql`). Run creation checks the flow's
  `run_start` against what the run is started on; an automation's kind must
  fit its flow's (`merge_request` ↔ `merge_request`, `tracker_query` / `cron`
  ↔ `ticket`) at save. The main chain is never started by hand
  (`ErrPhaseNotStartable`: "rerun the run").
- **The section decides** (`internal/signalspec`). A signal says what it is —
  `Kind` start (`run_start`), hand (`manual`) or observed — and any observed
  signal may open a watcher, fire an outcome, be waited on or answer a gate.
  The engine evaluates every one per merge request (`signalOn`): it holds (an
  occurrence key), the merge request is past it (merged: past approval and
  being mergeable), or it can never hold there again. Watchers run per merge
  request — every merge request now, not only open ones, so a watcher on
  `mr_merged` or `mr_closed` works; waits and gates on the chain's subject, or
  on the whole run when it has none; outcomes on the whole run, by the
  registry's new `All`: `mr_merged`, `mr_approved` and `mr_mergeable` hold
  once every merge request has them (no subject), the others as soon as one
  does (that one the subject). `mr_approved` as a watcher is keyed by the
  approved head: a push, approved again, is a new round. `wait for:
  mr_mergeable` parses now.
- **Merges and reviews pin to the newest head the chain knows**
  (`phaseHead`): the tip it began on, then each tip it pushed to its subject
  (`notePush`, shared by replay and the live push; kept across a park).
  `mr_merge` after the chain's own rebase and push merged nothing before —
  pinned to the head the chain began on, the repository refused it. The
  rebase → wait mergeable → merge pattern (a `ship` watcher) works now
  (`TestMRMergeIsPinnedToTheHeadTheRoundPushed`).
- **Outcomes always carry their `then`** (`done` by default), so the run page
  and the editor say how each ends; the done rule counts only a chain-less
  outcome's `then`, a chained one's last stage being counted as a stage.
- **Templates**: `builder` (main, watchers `conflicts` / `review_fixes`,
  outcomes `merged` / `closed` with `then: abort`), `reviewer` and
  `conflict-resolver` (`run_start: merge_request`, main runs the block, the
  watcher `again` runs it again) — stage ids unchanged (`review.*`,
  `rereview.*`, `settle.*`, `resettle.*`); the phase their runs begin with is
  `main`. The `-dev` sandbox flow too (`serve -dev` refused to start on it —
  now `TestDevFlowParses`).
- **The UI.** The editor's document model reads and writes the three
  sections (`run:` written back when a chain is one include of its own name);
  the canvas groups the rows under faint section labels in the marker column
  (main, watchers, outcomes — once a flow has more than one chain), the main
  marker says what runs are started on, an outcome's marker wears its end
  (green done, rose abort). The inspector has a panel per kind — the main
  chain's `run_start`; a watcher's signal (observed ones and *by hand*),
  policy, quiet and max; an outcome's signal, policy and `then` — and the
  flow panel adds watchers and outcomes. A chain added after the layout was
  made goes at the end of its section and the sections below move down
  (`placeChain`). The run page's Phases window is **Watchers**; the start-run
  form lost its entrypoint picker (a flow started on merge requests asks
  which one); the Repositories tab and the automation form offer flows by
  `run_start`.
- **Not compatible with v4 data.** A saved v4 flow no longer parses (the
  editor shows the error; re-saving it in v5 fixes it), and a run whose pinned
  flow is v4 fails on recovery with the parse error, as any flow that stops
  parsing does.

## Blocks are sequences

2026-10-24, the user: "I don't like block" — and of five names and five more,
"just go with sequence". "Template" was out: it already means the built-in
flows (builder, reviewer, conflict-resolver). One word now names the concept
everywhere:

- **Grammar** (still `version: 5`, unreleased): `sequences:` lists the chains
  written once; a **sequence stage** runs one in its place with `sequence:
  X` (was `include:`); a watcher or an outcome that only runs one says
  `sequence: X` itself (was `run: X` — which also freed `run:` to mean only
  a command's script). The main chain is the sequence `main`. The old
  `blocks:`, `include:` and watcher `run:` are refused with the new spelling
  (`Doc.LegacyBlocks`, `DocStage.Include` among the retired fields,
  `DocWatcher.LegacyRun`).
- **Code**: `internal/flow/sequences.go` (was `blocks.go`), `DocSequence`,
  `Stage.Sequence` (the path of sequence stages a stage was expanded
  through), `maxSequenceDepth`; the parser's messages say "sequence" and
  "sequence stage". The templates' `review` and `settle` are sequences.
- **UI**: `StageKind` `'sequence'`, `DocSequence`, `SequenceView`, the
  sequence pane beside the flow, the inspector's *Sequence* field, the node
  chip *sequence*; the API document carries `sequences` and a stage's
  `sequence`.
- **Not renamed**: the plain word — the "settings block" of parameters,
  blocked hosts, blocker pings — and the history in this file.

## The flow editor writes everything the YAML says

2026-10-24, the user: "users should be able to 100% fully recreate whatever is
in the yaml directly in the UI" — "make sure that all the options are
represented". What the editor could not write before, and now can:

- **Agents** — the flow carries them (FLOWLANG §1, v3) and a stage's agent
  must be one, yet none could be declared or edited. The flow panel lists
  them (+ agent); an agent panel edits name (renaming every stage that runs
  it), description, read-only, allow shell, the prompt, inputs (name,
  description, optional) and outputs (name, path, type — file, verdict,
  review_threads, review_replies — description), renaming a port renaming
  the bindings under it. An agent stage links to its agent, or declares the
  one it names.
- **Sequences** — the flow panel lists them (+ sequence); a sequence panel
  edits id, description, its stages (reordered ↑ ↓: the first is the entry
  and a stage saying nothing goes on to the next), adds a stage of each kind
  and lists what runs it; it deletes once nothing does. Its stages are
  edited in place: a selection carries the sequence it is in
  (`Selection.sequence`), the inspector works on that sequence's list — ids,
  routes and neighbours its own, `fail` among the edge targets, "next" / "out"
  for a success that says nothing — and the pane beside the flow stays open
  on it, re-laid out on every change.
- **Chains** — each panel adds a stage of each kind to its own chain, wired
  after the last (`appendStage`, `appendSequenceStage`); a watcher's or an
  outcome's **Runs** picks its own stages or a sequence, `main` included
  (`sequence: X`); + main chain gives a flow that only watches its main
  chain back. Quiet for applies to any debounced signal and reads a string
  `$param`; a command takes a timeout; an action's enum input offers the
  enum `$params` that fit; the six engine-read parameters take a description
  and allowed values like any other.
- Two defaults changed with it: the first watcher or outcome turns a main
  chain's closing `done` into `end` (`armMain` — with `done` the run would
  complete before anything could watch), and `docToYaml` writes the six
  engine-read parameters first, in their order (the new-flow skeleton too).
- A refused rename (not a slug, or taken) puts the field back.

Checked by rebuilding the built-in `conflict-resolver` from the New flow
skeleton through the editor only — parameters, its agent and prompt, the
`settle` sequence with its routes, `main`, the `again` watcher, both outcomes —
and comparing what the saved document compiles to with the template's: equal,
line numbers aside. On the way, a bug: a stage changed after it joined the
reactive document kept its old id (the proxy had already read it), so
`setChainSequence` now gives `appendStage` its fields up front (`init`).

## The hub: what the run waits on between its chains

2026-10-24, the user: watchers "are just a line under" the main chain. The
canvas now draws what a run does: the main chain, then the run **waiting** on
its merge requests, and what branches from that.

- **The hub** (`FlowHubNode`, layout key `hub`) stands, once a flow has
  watchers or outcomes, left of the marker column under the main chain: the
  main chain's `end` routes land on it (a drop on it means `end`), and one
  dashed line runs from it down to each watcher's and outcome's marker. A flow
  without a main chain starts at it.
- **Each chain has its own ends**, at the end of its row: a watcher's `end`
  (back to waiting), an outcome's `done` / `abort` (`__end:<phase>`, layout
  keys `end:<phase>` …, `chainEnds`, `terminalKey`). Nothing routes across the
  canvas to a shared terminal any more; the shared ones are the main chain's.
- **Frames**: each watcher and outcome is wrapped in a faint wash of its
  marker's hue (a `ViewportPortal` behind the nodes, from where they stand
  now, so it follows a drag); its corner folds the chain to its marker, which
  counts what it hides. Dragging a marker takes its chain along, saved where
  it lands.
- Dataflow lines join dots within a chain only: a watcher reading the main
  chain's plan shows the path on its dot instead of a line across the canvas.
- `autoLayout` and `placeChain` place the hub (on a row of its own above the
  first watcher or outcome) and each chain's ends; a layout stored before
  them still opens sensibly — the hub left of the first marker, each chain's
  ends right of its last stage — and auto-arrange redoes it.

## The flow editor: every route in one shape, and the way back

2026-10-25, the user, of the stage panel: "why is this different for on
success vs on fail/reject/timeout?" — the success target was a dropdown, the
failure routes chips that opened an edge editor. The data differs (one target,
always there, vs optional chains of links with a limit and fallbacks), the
editing need not:

- **Routes**, in the stage panel: one row per link, the same shape for all —
  the route's kind in its hue, its target, and for a failure link to a stage
  ×N (∞ where a person decides each time: from a gate, or to one); `then`
  rows for the fallbacks, "+ then", × to remove a link (the links after it
  move up; the last one takes the route with it), "+ on_timeout" for a route
  the stage lacks. The separate edge panel is gone: selecting an edge — on
  the canvas, or now by its label, which Svelte Flow reports no click for
  (`data.pick` in `FlowLabeledEdge`) — opens its stage with that route picked
  out and scrolled to.
- **The way back** — "when we go into those sub-editors, can we add a go back
  button which goes back to the stage we were just looking at": the inspector
  now moves two ways, `onselect` (another selection in this one's place — a
  rename, a delete) and `onopen` (from this panel into another — Edit agent,
  Edit sequence, a sequence's stage, Run by, the flow panel's chips, a stage
  just added). The page keeps the trail `onopen` leaves; every panel offers
  "← stage build" (agent, sequence, watcher, *The formula*) back along it.
  A rename renames what the trail remembers (a sequence's stages under the
  sequence's new name), a delete goes back where it came from, a panel that
  no longer exists is skipped, and selecting on the canvas starts afresh.

## `run_start:` is `operates_on:`

2026-10-26, the user: `run_start: merge_request` — and the editor's "Runs
start on: a merge request someone opened" — "gives the impression that it is
automated", as if a merge request starting were what launches a run. The key
says what a run works on, chosen by whoever starts it; of ten names, the user
chose `operates_on` (over `starting_point`, which keeps the "start" reading).

- **Grammar** (still `version: 5`, unreleased): `operates_on: ticket |
  merge_request`, default `ticket`; `run_start:` is refused with "run_start:
  is now operates_on:" (`Doc.LegacyRunStart`). The templates `reviewer` and
  `conflict-resolver` say `operates_on: merge_request`.
- **Code and API**: `Doc.OperatesOn`, `Flow.OperatesOn`, `OperatesOnTicket` /
  `OperatesOnMR`, `Flow.OperatesOnMR()`; flow responses and validations carry
  `operates_on`; the UI's `FlowDoc.operates_on`, `Flow.operatesOn`,
  `OPERATES_ON`.
- **UI**: the field reads *Operates on* (a ticket / a merge request someone
  opened), its help saying it is not what starts a run; the graph's main
  marker reads *main · operates on a merge request* (a flow without a main
  chain: *run · operates on …*).
- **Not renamed**: the `run_start` *signal* — how the main chain opens, the
  run being started by a person or an automation — which is what it says.
  Saved flows and pinned run texts that still say `run_start:` no longer
  parse (no compatibility before release): convert them by hand.

## No `run_start` signal

2026-10-26, the user, of the `run_start` signal kept through the rename:
"remove it — its old". It was v4's way to mark the chain a run starts with
(`on: run_start`, several entrypoints allowed); in v5 the main chain is that
by its section, and the parser only filled the signal in for it and refused it
everywhere else. Now the main chain has no opening signal (`Phase.On.Signal`
empty; `Phase.OpensOn()` names "the run's start" in messages), the registry
loses the entry and the `start` kind (`manual` is the one signal not
observed), and `on: run_start` is an unknown signal like any other. The API's
phase summaries carry an empty signal for the main chain; the editor tests a
chain's kind, not its signal.

## A `$parameter` where none is read is refused

2026-10-26, the user, finding parameters "not really dynamic": `description:
$note` on a watcher was accepted and kept the text "$note" — the only hint a
warning that nothing reads `note`. Parameters are read at their sites only
(typed, enum-checked); everywhere else `$name` is now refused when it is a
field's whole value (`notAParam`, naming the sites in its message): the
flow's, a stage's, a watcher's, an outcome's, a sequence's and a parameter's
descriptions, a sequence stage's, a gate's `question`, a `run:` script, an
agent's prompt, description, its inputs' and outputs' descriptions and its
outputs' paths, and every path a stage binds (the paths refused before, now
saying why). Names already refused it. A declared parameter named inside a
script or a prompt (`$name`, `${name}`) is a warning (`untemplatedWarnings`):
text with a `$` in it is legitimate — `echo $HOME`, a cost — but neither is
templated. The built-in templates raise none of it.

## The YAML tab is a code editor, checked against the flow's schema

2026-10-26, the user: "update the yaml editor to be with colors … and could we
enforce a yaml schema on the editor directly?"

- **The schema** (`flow.Schema`, `GET /api/flows/schema`): JSON Schema
  2020-12 of the document as written — shorthand and all — built in Go from
  the registries (stage types, observed signals, actions and, per action
  through `if`/`then`, its inputs and their enums; start policies; output
  types; the six engine-read parameters with their defaults). Tests: every
  template and example validates; typos (an unknown key, a stage type, a
  signal, a start policy, an action input, `then`, the version) do not; and
  the schema names exactly the YAML keys the parser's structs read.
- **The editor** (`YamlEditor.svelte`, lazy-loaded with the tab): CodeMirror 6
  with the YAML mode, highlighted from the theme's tokens, `$param` and
  `$SENTINEL` marked; `codemirror-json-schema` for key completion and hover
  docs; one linter running the schema check and then the parser
  (`/api/flows/validate`, debounced), the parser's error dropped on a line the
  schema already marks; `yamlValues.ts` completes every enum value — empty or
  started, block or flow style — which the library did only on a started
  value and a schema's first branch. The editor chunk is ~270 KB gzipped,
  fetched only by the YAML tab.
- **The parser's unknown key** reads `unknown key "mian" in the document — it
  takes name, version, …` (was Go's `field mian not found in type
  flow.Doc`), for every decoded part: a stage, a watcher or an outcome, `on:`,
  a route, a parameter, an agent and its inputs and outputs.

## Structured agent outputs: built-in types and flow schemas

2026-10-26, TODO #33, the user: "make sure the agent respected whatever was
asked of it … enforce things like a schema output on an expected agent
output". The rule they set: *if a built-in stage requires a structured input,
it deserves a built-in type; if the stage is the user's, the user defines the
structure.*

- **Built-in types** stay the four something built in reads: `verdict` (the
  router), `review_threads` (`post_review`), `review_replies`
  (`reply_review_comments`), and the new `commit_message` (`commit`). An
  action's file input now names its type (`actionspec.InputSig.Type`), and the
  parser refuses an agent output of another shape bound to it — it was only a
  description before.
- **Flow schemas** (`schemas:` — `{name, description, json_schema}`, JSON
  Schema 2020-12 in YAML), named by an agent input's or output's `schema:`.
  Compiled at parse with every loader removed (`jsonschema.SchemeURLLoader{}`):
  a `$ref` to `file://` or `https://` is a parse error, never a read. Static:
  every agent output writing a structured input's path must declare the same
  structure. Runtime: outputs checked at stage end (`output_invalid`, the
  violations as `$FAILURE`), schema'd agent inputs before the stage starts
  (`input_missing` / `input_invalid`, before outputs are cleared — as a
  command's). The schema is rendered verbatim into the OUTPUT CONTRACT.
  `commit_message`'s shape is a built-in schema through the same machinery
  (`flow.TypeSchema`), so it is checked and shown the same way.
- **`DocJSON`** keeps the YAML node, not a decoded map, so the editor gets
  the author's key order and re-emits it (Go maps would sort `properties`
  before `type`). Unexported field: an exported `yaml.Node` leaked into the
  OpenAPI components.
- **`commit` action** replaces the builder template's `git commit -F` command:
  the message is piped to `git commit -F -` (shell-quoted, no temp file),
  every checkout or the subject's, a clean tree left alone (idempotent
  re-entry). Considered first: jq in the dind image (wrong level — commands
  run in the repo's own image), jq via the harness feature (installs into the
  user's image), a shim wrapper. The shim got a real `jq` anyway
  (`apothecary-shim jq`, gojq's own CLI, pinned in the builder Dockerfile and
  held to go.mod by a test; `/opt/apothecary` appended to every Exec'd
  script's PATH, never prepended).
- A verdict that fails its shape now feeds the error to the retry as
  `$FAILURE` (it fed nothing; the review types already did).
- Not done: "unstaged code exists" (TODO #33's other half) — an agent that
  must leave changes is not a file, so it fits neither a type nor a schema.

## Commands declare structures too; a schema narrows a type

Same day, the user: "is there a reason why command stages can't declare a
schema? … why can't the 2 share the same schema in output → input?" There
was none — the schema lived on agent decls only because commands had no decl.

- **Long form** `name: {path, schema}` on a command's `input:`/`output:`
  (`IOPair.Schema`, `IOBinding.Schema`); both keys required, nothing else
  allowed, refused on any other stage type with the reason. The document's
  JSON keeps it as `{path, schema}`; the editor splits it on load into
  `input_schema`/`output_schema` beside the plain `StageIO` (every consumer
  of bindings stays `name → path`) and `docToYaml` joins it back.
  `BindingRows` carries the schema on the row, so renaming a binding keeps it.
- **Agreement is by writer, whoever it is**: agent outputs (decl), command
  bindings (long form), action outputs (opaque files) all count. A command
  writing for an action's *typed* input is exempt — it cannot declare a
  built-in type, and the action parses the file anyway (a flow test feeds
  `post_review` from a script).
- **Runtime**: a command's structured inputs are checked after the existence
  check, its outputs after its exit-0 existence check (`checkFiles`, shared
  with agents).
- **`schema` beside `type`** (was mutually exclusive): the file holds both;
  `Flow.OutputSchemas` lists the type's built-in schema then the flow's, both
  rendered into the contract; the by-hand type checks (verdict, review_*) run
  first. Agreement: a writer satisfies a reader when it has every structure
  the reader names (`shape.satisfies`).

## Ports: every value a stage reads or writes, typed, with its source

Same day, after a long design exchange with the user, which rewrote the flow
grammar in place (still `version: 5` — the app is unreleased, so no upgrader).

- **What the user asked for, in order.** Stages should declare their inputs
  and outputs "as a function does" — typed, without pointing at a location —
  and "more declarative = better": every stage restates its I/O even where an
  agent's or an action's contract already says it. Rejected along the way:
  edges passing every argument (the function-call model taken literally:
  ~19 edge sites re-passing `plan` in the builder, and chains opened by a
  signal have no caller); `producer.output` references (a reader would name a
  stage); a named run scope (names would need a uniqueness that paths
  already give); `$IN_`/`$OUT_` environment variables for scripts (the user
  dislikes them — scripts keep literal paths).
- **What it is.** A port is `{from | to, type | schema, description?,
  optional?}`. Sources: a path some stage writes, `repo:<path>`, `run.*`
  (ticket, git.status/diff_target/diff_phase, mr.url, workspace) and
  `edge.*` (failure, decision). Paths stay the wiring — a path written by
  several stages gives the most recent, two stages to a third use two paths
  — so "functions over a typed run scope" is the workspace's files keyed by
  path. Edges carry control and only the transition's payload.
- **Contracts say what, stages say where.** Agent decls are typed and lose
  `path:`; a stage states every output and every required input, and may
  narrow (add a schema), never change a type. Actions: file ports typed in
  the registry (`PortSig`), literals under `with:` (`SettingSig`); every
  action output is stated (load_review_comments' `review`). create_mr's link
  became `run.mr.url`, not an output.
- **The TASK section is a port now** (`run.ticket`, type `ticket`): an agent
  without one never sees the ticket — the conflict resolver deliberately.
- **Checks became errors**: nothing writes a path (the hint is `repo:`),
  not written on every route (unless `optional`), writers disagreeing, an
  edge source no edge fills. A typo'd `run.tiket` suggests `run.ticket`
  (Levenshtein ≤ 3). The runtime checks every stage's file inputs before it
  runs and agent/command outputs after — except an action with a pending
  effect, which reconciles first (caught by the test-conversion agent: a
  retried ticket_comment on a rebuilt workspace).
- **Editor**: `PortRows` replaces `BindingRows` — contract rows for agents and
  actions, free rows for commands and gates, a source datalist (engine
  sources + paths the flow writes), type and schema selects; a value the
  engine provides fixes the type. The doc model keeps the wire shape
  (`input`/`output` maps of `DocPort`, `with` a string map).
- The `{path, schema}` command long form from earlier the same day is gone:
  every port carries its shape.

## Screenshots in the merge request: services, the project browser, MCP, files

The test the user set the flow language: bring the app up, have an agent look
at it in a browser, put the screenshots in the merge request. Four pieces,
built at once (2026-10-26), each usable without the others.

- **`type: files`** — a directory port (`artifacts/shots/`, the trailing `/`
  is the type's mark). Emptied before a fresh attempt (`rm -rf` of the path
  without its slash, so a planted symlink goes, never its target), present
  when it holds ≥1 file, listed (never pasted) in a reader's prompt, captured
  one snapshot per file (≤20, ≤10 MB — the store tail-truncates, which would
  corrupt an image). `file` readers are not satisfied by it. New required
  `EnvProvisioner.ListFiles` (POSIX `find` + `wc -c`, busybox-safe, same
  containment guard as a read).
- **Attachments** — `create_mr` / `ticket_comment` take an optional
  `attachments` (`files`). `RepoHost.UploadAttachment` → `{URL, Link,
  Markdown}`: GitLab `POST /projects/:id/uploads`; GitHub
  `uploads.github.com/user-attachments/assets` (what `gh` uses — classic PAT
  with `repo` only; fine-grained/app tokens 404, GHES has no such endpoint —
  both refused with that sentence); Jira issue attachments (v3 links them —
  ADF media needs a Media API id; v2 wiki `!name!`) through the optional
  `ports.TicketAttacher`. Body rewrite: link/image targets and whole-path
  mentions become the upload; unmentioned files follow under Screenshots /
  Attachments. Limits checked before any upload. create_mr reconciles
  (FindMR) before reading anything, so an adopted MR never re-uploads; a
  crash between upload and creation leaves orphan uploads, never a second
  MR. Known: a path inside a fenced code block is rewritten too.
- **Services** — declared in the project (`workspace.services`: start, stop?,
  one health probe, ports), named by flows through a parameter (`service:
  $app`, default `default` — the user's own project calls it `aio`; nothing
  hardcodes either). A shim supervisor (`apothecary-shim service …`)
  detaches with setsid, survives the exec and engine restarts, keeps
  `/apothecary/services/<name>/{spec,state}.json` + `output.log` in the
  container (a rebuild honestly wipes them with the processes) and is read
  through the sidecar (exec fallback). A start that exits 0 daemonized
  something; then only the probe decides. Stage-started services stop at
  their chain's end (every one at the run's); human-started ones never by the
  engine. Human actions append events from outside the loop, so appends now
  take a per-run mutex. Health flips are polled state, not events. Run page:
  `GET/POST /api/runs/{id}/services[/{name}/start|restart|stop|logs]`.
- **MCP** — flow `mcp_servers:` (builtin | command | url; `${secrets.x}` in
  env/headers) + agent `mcp:`. Resolved at stage start (secrets re-read and
  taught to the redactor) into `ports.AgentInput.MCPServers` and sent on the
  ACP `session/new`/`session/load` — not harness config files: opencode
  reads `opencode.json` once per process, so per-stage secrets and a server
  per session (one Chromium each) only work through ACP. opencode keeps a
  session's servers for the process, so each agent's markdown turns the
  others' tools off (`"browser_*": false`). Known: two concurrent sessions
  on one server in one workspace (a watcher beside the main chain) — the
  second replaces the first's process; sidechat sessions get no toggles.
  Project secrets: new table `project_secrets` (one sealed row each) in
  migration 0009 — not 0001, which `TestInitMigrationIsFrozen` pins for
  databases that applied it.
- **The project browser** — `apo-browser-<project>` (egress-gateway
  lifecycle: create on need, recreate when stale and idle, stop after 10
  idle minutes, warmed at run creation), `--network none`, caps dropped;
  image `node:22` + `@playwright/mcp@0.0.83` + its Chromium + the shim.
  Session: the workspace's `shim browser-mcp` upgrades
  `GET /api/runs/{id}/browser-mcp` (run token), the engine `docker exec`s
  `shim browser-session -- playwright-mcp --isolated …` with
  `--proxy-server` at a per-session unix socket the daemon serves: loopback
  hosts dial the run's port through `ExposePort`, every other host 403.
  Named screenshots are taken inline and written into the workspace by the
  shim (Playwright would write them in the container). Known: an agent can
  run code in the shared container, so sessions of one project can reach each
  other's proxy sockets (projects stay isolated); no reconnect mid-session.
- **Builder template** — `screenshots: false` + `app: default`; on, `app_up`
  (service, best effort: a dead app opens the MR without shots) and `shoot`
  (a `photographer` agent with the browser, to the debug leaf like every
  agent) run after the review; the redactor may show shots inline and
  `create_mr` attaches them. Off, run creation asks for no service and no
  browser: `Flow.Services/Secrets/UsesBuiltin` skip stages `when:` folded
  off.

## Reviews: a gate over many files, and a decision with a shape

A gate shows what a run made — a plan, a folder of screenshots — and the
person answers file by file (2026-10-26). Three changes, one idea: the
review page's structure reaches the engine, and the engine writes the text.

- **A gate shows files and one link.** Any number of `file`/`files` ports
  plus at most one link (was: exactly one port). A folder is expanded when the
  gate opens (`gateFiles`): its files in order, the attachment bounds (≤20 in
  all, none over 10 MB — a cut snapshot ruins an image), each snapshotted as
  shown. `Ping.Files` was already a list; only the gate ever put one in it.
- **A review is structured.** `PingAnswered.Review []ReviewComment{path,
  start_line, end_line, heading, quote, body, attachment_ids}`; `feedback` is
  its summary. Checked on both answer paths (`validateReview`): each on a
  shown file, non-empty, a real range, an image (by extension) whole-file only,
  its attachments the answer's. Projected one row per comment into
  `ping_review_comments` (migration 0010 — 0001 is frozen), rebuilt with the
  pings, read back with them. The page no longer composes a feedback text
  (`composeFeedback` is gone): one Go renderer (`renderReview`) writes it
  grouped by file in the order shown, numbered, attachments named where a
  comment names them and listed after otherwise — for a gate's decision and
  for an `ask_human` answer alike (`composeAnswerText`).
- **An approval carries its decision.** `flow.Decides` — a gate, or an agent
  with a verdict — makes on_success carry `edge.decision` when the approval
  said something (a summary, comments, files; a verdict's comments). The
  validator counts that as filling `edge.decision`. Persisted as
  `StageCompleted.Feed` (as `EdgeTaken.Feed` is for a rejection), so recovery
  between the gate and the next stage hands it on (`completedFeed`). Replay of
  `stage_completed` now also clears a feed the stage was entered with — the
  live `advance` always did, replay did not, so a run recovered after a
  rejection loop could re-hand the old rejection to the stage after next.
  Decisions keep their head (32 KB, `capDecision`); failures their tail (8 KB).
- Found on the way: live gate pings never entered `rs.raisedPings` (replay
  put them there), so anything reading what a gate showed differed between a
  live run and a recovered one; `raiseDecision` now mirrors replay.
- Not done: the engine checks a comment's lines are a range, not that they
  fall inside the file (that would mean reading the snapshot on every answer).

## One word for a judgement, `run_if`, and what failed in CI

Three renames and a source (2026-10-26).

- **verdict, everywhere.** `edge.decision`/`type: decision` are `edge.verdict`/
  `type: verdict`: a person's review at a gate and an agent's verdict file are
  one thing now, so they have one name. `verdict` stays a file type and is the
  one an engine source also gives (`SourceVerdict` → `TypeVerdict`); the type
  rules needed no exception beyond the source table, since a port's source
  already says file or value. `flow.Decides` → `GivesVerdict`,
  `engine/decision.go` → `verdict.go`.
- **The verdict file has the review's shape.** `{verdict, summary?, comments:
  [remark | {path, line?, end_line?, quote?, body}]}`, a built-in JSON Schema
  (shown in the OUTPUT CONTRACT, checked with the outputs, as commit_message)
  plus `parseVerdict`'s rules a schema cannot say: a REJECT says why (a
  summary now counts), a path `filepath.IsLocal`, a range runs forward.
  `ports.Verdict{Approve, Summary, Comments []VerdictComment}`. One renderer
  for both givers (`renderVerdict` → `renderReview`): remarks first, then by
  file — the gate's order, else first mention.
- **yes_no.** The answer kind of approve/reject questions (gates, watcher and
  outcome confirmations, the debug leaf) was also "decision"; it is `yes_no`.
  Migration 0011 rewrites `pings.answer_kind` and, the app being unreleased,
  the `ping_raised` events themselves rather than reading both spellings.
- **`run_if:`** replaces a stage's `when:` (Go `Stage.RunIf *Cond`,
  `Cond*` terms). A document still saying `when:` gets the retired-name
  message with the line rewritten.
- **`run.ci.failure`** — what failed, never the log: `RepoHost.CIFailures`
  (GitHub: the newest check run per name; an Actions run is its job, whose
  failed steps and workflow run's `path` — a called workflow's `@ref` cut —
  say where; an app's run its output title; a failed status its description.
  GitLab: the head pipeline's failed jobs, not allowed-to-fail, with stage
  and the project's `ci_config_path`). Read once in `beginPhase` for a chain
  that reads the source, kept on `phase_started.ci` (replayed into `rs.ci`,
  parked with the phase run), rendered by `ciFailureText`. Only a `ci_failed`
  watcher's stages may read it (the parser says so); the editor offers it
  there only. `builder` gains the `ci_fix` watcher (`$ci_start`, default
  confirm) and a `ci_fixer` agent told to re-run the failed steps itself and
  not to loosen CI.

## Parameters declare their type; lists, and labels on the merge request

What a parameter holds is declared, never read off its default (2026-10-26).

- **`type:`** — `bool | int | float | string | duration` — is required on a
  flow's own parameter (the six the engine reads keep their bare default and
  the engine's type; another type, or a list, is refused). The default reads
  *as* the type, so YAML quoting stops deciding it; a missing type is refused
  with the one the old inference would have given. `duration` is a real type
  now: `timeout` (known) is one, and `timeout`/`quiet_for` sites take one
  (they took a string that parsed as a duration). `DocParamValue.Kind` became
  `Hint` — what the YAML wrote, used only for the bare known defaults and
  that error message.
- **`list: true`** — any number of values of the type; the default is a YAML
  list, a bound value a JSON array, canonicalised typed (`EncodeParamList`).
  An enum constrains each item. `paramSite.list` keeps lists out of scalar
  sites and the reverse. Bindings stay `map[string]string`: a list is its
  JSON text, so no store changed.
- **The first reader: `create_mr`'s `labels`** (`actionspec.KindList`). A
  `with:` value may now be a YAML list (`IOPair.Items`); a list setting takes
  that or a list `$parameter`, each item non-blank and no `$` inside.
  `RepoHost.AddMRLabels` (GitHub: `POST issues/{n}/labels`; GitLab:
  `add_labels` on the merge request — both create a missing label) runs
  after `CreateDraftMR`, best effort: a refusal is logged and the merge
  request stands. `builder` labels its merge request from `mr_labels`,
  default `[apothecary]`.
- Editor: the parameter card gets Type and List controls (the default field
  follows the type; changing type keeps what still parses); the Configure
  sheet and the start form draw a list as checkboxes (enum) or a
  comma-separated field, sending a JSON array; timeouts offer duration
  parameters, a list setting a list parameter or items written in place.

## Questions are forms — ACP elicitation (2026-10-27, TODO 34)

- **One question shape**: `event.Form` — an ordered list of `event.Field`
  mirroring ACP `elicitation/create` / MCP elicitation properties (string
  [format, pattern, length], string+options, array+options, number,
  integer, boolean). `answer_kind` is `yes_no | form`; `choice`, `multi`,
  `text`, `options`, `multi`, `SelectNone` and the `other` escape hatch are
  gone. A form is answered `accept` + `content` or `decline` + note
  (`validateForm`; one renderer, `composeFormText`, for the tool return and
  the resume message). The review round's thread selection is a one-field
  array form (`threads`, every thread picked by default).
- **ask_human** takes `fields` (none = one free-text field `answer`).
- **The sidecar bridges harness questions**: `initialize` advertises
  `elicitation.form`; `elicitation/create` (workspace/shim/elicitation.go)
  maps `requestedSchema.properties` IN ORDER onto fields, raises the ping
  through ask_human's own client, and answers accept/decline — cancel when
  parked or when the agent sends `$/cancel_request` (rpcConn now tracks the
  agent's requests in flight). `url` mode is declined. A codex
  `_meta.codex.isSecret` field is never asked; it is answered with a notice.
  A message-only form becomes one `proceed` boolean.
- **What actually uses it today**: codex-acp sends its `request_user_input`
  here (it answered `{answers: {}}` while we advertised no forms), but codex
  only offers that tool in Plan mode (or behind the under-development
  `default_mode_request_user_input` flag) — left off on purpose. MCP
  servers' elicitations codex forwards come the same way. opencode's
  `question` tool is disabled under `OPENCODE_CLIENT=acp` and its ACP layer
  has no bridge, so opencode asks only through ask_human.
- **Migration 0012** adds `pings.form` / `pings.content`, drops
  `pings.options`, and rewrites old `ping_raised` / `ping_answered` events
  into forms (choice/multi/text; `other` → decline).

## GitLab Issues as a ticket source (2026-10-27, TODO 355)

A third `ticketsource` kind, `gitlab` (`kinds.GitLabIssues`,
`internal/adapters/gitlab/issues.go`), with the read port, `TicketWriter` and
`TicketAttacher` — the GitHub Issues shape, on the repository adapter's
REST v4 client. Decisions worth recording:

- **REST issues, not the work items GraphQL API.** GitLab is moving issues,
  tasks, incidents and epics onto one "work items" framework whose native API
  is GraphQL. The REST issues API still serves every issue-type work item and
  is what the rest of the package speaks, so the adapter stays on it; epics
  (group-level) are out. What only GraphQL reaches — the configurable work
  item *Status* (in progress, in review…) — was why `TransitionTo` knew only
  todo/done. *(Superseded by TODO 49, below: steps resolve through the
  integration's status list, opened/closed, and GitLab types are its
  `issue_type` list.)*
- **The key is GitLab's own full reference**, `group/project#123`
  (`references.full`, falling back to the `web_url` path): an iid is unique
  only within its project, and nested groups make the path any depth. It is
  sent URL-encoded as `:id`, so no numeric-id lookup precedes a call.
- **Search scope is projects and groups.** Each configured scope is one list
  call, merged by key and re-sorted by `updated_at`; none configured falls
  back to `/issues`. There is no query language on the REST side, so the
  search box lifts GitLab's filter-bar words (`~label`, `is:`, `assignee:`,
  `author:`, `milestone:`, `type:`) into parameters and searches the rest.
- **Description uploads are attachments.** Unlike GitHub, GitLab hosts the
  files pasted into an issue (`/uploads/<secret>/<file>`, project-relative).
  Fetch lists them and rewrites the links absolute (the body is read outside
  GitLab once imported); `OpenAttachment` downloads through
  `GET /projects/:id/uploads/:secret/:filename` (GitLab 17.4+), host-checked
  before the token goes along. A file that fails is the factory's usual
  named warning.
- **Write-back** is `state_event` close/reopen, notes for comments and links
  (system notes are left out of `Comments`, so the link marker scan sees only
  real notes), and labels / assignees (username → id via `/users`) /
  milestone (title → id, ancestors included) for `ticket_set_field`.
  `Attach` reads the issue's `project_id` and reuses `UploadAttachment`.

## Accounts: multi-user, no organisation level (2026-10-27, TODO 25)

The plan started as guilds — organisations with members and roles, the
shape the UI redesign had sketched. Lu dropped it on reflection: a solo
install wants one account, a team wants one account per person, and the
"shared" things a team would have (one project row per repo, a review
automation that fires once per MR, covering each other's pings) are worth
less than roles, invites and a cross-guild desk cost. So the product is
multi-USER. There is no org table, no role matrix; `user_id` goes on almost
every row, every list is the caller's, every item route is the owner's or
a 404. "Add a collaborator to a project" is the cheap primitive if sharing
is ever wanted — and ownership is an explicit column, not an implicit
"everything is mine", so that door stays open.

### What landed

- **`internal/auth`**: the profile (`users`), a pluggable method table
  (`auth_methods`: kind = password today; OAuth and passkeys are more rows
  of the same shape with a `Verifier` per kind), browser `sessions` and
  personal `api_tokens`, both stored as sha256 of the secret. Passwords are
  argon2id in PHC format (parameters travel with the hash). `Setup` creates
  the first account as admin. Migration `0013_users.sql` — new installs
  only: the product is not shipped anywhere, so nothing in the code claims
  rows made before accounts existed. Lu's own database was assigned to his
  account by hand, once, with the same statements an adopter would have run
  (every `user_id=''` row, plus the `run_created` payloads so a projection
  rebuild keeps the owner — precedent 0012).
- **Ownership**: `user_id` on integrations, projects, tickets (copied from
  the project on every write), channels, attachments (the uploader), the
  runs and pings projections (from `run_created.user_id`, which the engine
  fills from `Resolution.UserID` = the project's owner — so automations'
  runs are the project owner's too). Names are unique per owner, not per
  instance. Settings are per user; the capacity caps left the table for the
  boot env (`APOTHECARY_MAX_CONCURRENT_RUNS`, `APOTHECARY_MAX_ENVIRONMENTS`,
  `-1` = no cap) because they protect the host, not a preference.
- **The guard** (`httpapi/auth.go`): every route in the table declares its
  `Auth` (user by default, admin, run token, public) and `Scope` (which
  resource its path names). One wrapper resolves the principal (bearer
  `apo_…` token, else the session cookie), refuses cookie writes from
  another origin (`Sec-Fetch-Site`, then `Origin`), and answers 404 for a
  resource whose owner is not the caller. `TestEveryRouteDeclaresItsAccess`
  keeps a new endpoint from being open by accident; the public allowlist is
  three routes. This also closes the old gap where the devcontainer bridge
  pumped the whole unauthenticated API into every workspace: the run token
  still reaches only its own run's four routes, and nothing else answers.
- **Reaper and retention per owner**: `ReapOnce` takes a window-per-user
  function, orphan GC runs per owner, and the blob sweep's mark set takes a
  cutoff per owner (`LiveBlobSHAs(map)` via `unnest`).
- **Notifications go to the owner's channels**: `ports.Notification.UserID`,
  filled by `Engine.notify` from the run when the caller did not; a
  channel with no owner (tests, dev mode) receives everything.
- **Host resources keyed by project id** (not name): gateway container,
  policy dir, cache volumes, browser container, docker labels
  (`apothecary.project=<id>` + `apothecary.project.name=<name>` for
  `docker ps`), the gwproject marker and the build-cache registry path —
  through one helper, `ports.ProjectHostKey`. Two users with a project
  called "api" no longer share a gateway; a rename no longer moves one. The
  name is deliberately NOT a `--id-label` on the devcontainer container: the
  CLI matches on every id-label, so a renamed project would stop matching
  its own workspace.
- **CLI**: `apothecaryd user list | reset-password | set-admin` — the way
  back in after a lockout.
- **UI**: the `/g/[guild]` segment is gone (old links redirect); `$lib/urls`
  builds paths; `$lib/session.svelte` gates the shell on the boot check
  (setup page → sign-in page → app) and a 401 interceptor drops back to
  sign-in in place. The sidebar's guild switcher is a user menu (Account,
  Users for admins, Sign out); System gained Account (profile, password,
  tokens) and Users (admin), Limits is read-only, Members is gone.

### Deliberate residuals

- One master key for everyone's secrets: per-user keys would be envelope
  encryption while one process holds every key in memory anyway.
- The engine speaks plain HTTP and the workspace-port relay opens raw host
  ports with no auth: fine on loopback, a leak on a network. TLS in front
  and an authenticated app proxy (wildcard subdomain, session cookie on the
  parent domain) are the next step before remote users get exposed ports.
- `apothecaryd user reset-password` echoes the typed password (no raw-mode
  dependency); `-password` exists for scripts.
- Any user may answer any of their own pings; there is no assignment, and
  no one can answer another person's — the run waits for its owner.
- The desk layout stays in localStorage per browser, not per user.

## Kubernetes (TODO 30)

Workspaces, and the engine itself, deploy on any Kubernetes cluster (k3s
locally through k3d, full k8s in production) with the docker path's features
intact. The plan was reviewed and locked with the user on 2026-10-27.

### Shape: host × definition

`internal/adapters/devcontainer` became `internal/adapters/workspace`, cut
along two axes:

- **Host** (`host.go`) is where the sandbox runs: `dockerHost` (the DinD
  container on the engine's daemon — a pure move; the sandbox lifecycle
  stays on `Adapter`, which the unit tests drive) or `k8sHost` (a pod).
- **Definition** (`definition.go`) is what the repo asks for, detected per
  repo after the clone and recorded per env (`<run>/definition`):
  `devcontainerDef` today. A future `.apothecary/apothecary.json` is a second
  definition, not a third provisioner.

The provisioner kinds are the hosts: `docker` (renamed from `devcontainer`;
unreleased, so no alias — recreate the integration) and `kubernetes`. The
unit tests kept their logic unchanged through the move (one compat helper
file for `configFiles`/`configDigest`).

Path parity is what keeps the seam small: a sandbox pod mounts its volume at
`<RunsDir>/<name>`, so the checkout, the shim dir and the devcontainer CLI's
bind sources are the same strings on both hosts. Only the filesystem they
name differs: `osFS` on docker, `execFS` (one `kubectl exec` script per call)
on Kubernetes. The engine keeps its own `<RunsDir>/<name>` for exec.log and
markers either way.

### Kubernetes host

- kubectl CLI through the adapter's `CmdRunner` seam (`internal/kube`), no
  client-go. `PodRunner` maps exit codes: kubectl prints `command terminated
  with exit code N` for a remote failure (stripped from stderr and the tee);
  a markerless failure matching kubectl's own transport errors is a
  `TransportError`, which `runWith` retries like a failed spawn.
- The clone runs in the pod. The credential rides stdin (a one-line script,
  then the credential) into a 0600 file the clone removes — never argv or
  environment.
- The pod: `shim-seed` (copies the shim from the `apothecary/shim` image,
  writes `shim.hash`), `wg` (native sidecar: the tunnel, then a watchdog),
  `relay` (native sidecar: `shim relay --serve :7070`, the dial-in mux),
  then `sandbox` (`apo-dockerd`: the dind entrypoint with a CGNAT address
  pool and, with a gateway, its DNS, mirror, build cache and the tunnel MTU).
- Dial-in: the engine reaches `sidecar.sock` / `app-<port>.sock` through the
  mux — `APO1 <token> <socket>\n`, answered `OK\n` once the socket connects
  (so "nothing serves it yet" is distinguishable). In-cluster by pod IP,
  out-of-cluster by a supervised `kubectl port-forward`. Never one
  `kubectl exec` per connection: a page load would cost seconds.
- The engine URL is provisioner-supplied (`ports.EngineURLProvider`): the
  docker host keeps loopback parity, the Kubernetes host gives the engine's
  Service. `run.env`, bootstrap, rebuild, envcheck's tunnel probe and
  `waitReady` all use it.
- Stats read the sandbox container's cgroup v2 (`cpu.stat`, `memory.*`,
  `pids.current`, `cpu.max`) and `eth0` from `/proc/net/dev`; CPU% needs two
  readings, so the first sample after an engine start is a gap.
- A shim whose `shim.hash` differs from the engine's `workspace.ShimSourceHash`
  fails a fresh provision (publish the shim image of the engine's version);
  on Reattach it is only logged, since the next pod start reseeds it.

### Egress: one gateway, one tunnel

The gateway daemon and image are the same on both hosts, and on both a run's
egress reaches it over a per-run **WireGuard** peer. What differs is the
network the tunnel itself rides:

- docker: the run's `--internal` network, which also carries DNS, the image
  mirror and the build cache (addressed to the gateway, so the run's subnet
  attributes them; the tunnel address is attributed too). The engine brings
  `wg0` up in the sandbox with a throwaway gateway-image container sharing its
  network namespace (`egressgw/wg_docker.go`: the dind image has no `wg`).
  Identities are files under `<RunsDir>/gw/<project>/` (the gateway's key,
  `peers/<run>.json`). It used to be a plain next hop across that bridge
  (`default via <gateway>`), which put internet-bound frames on the host
  bridge: once anything loaded `br_netfilter` (a local k3s's kube-router, via
  its `-m physdev` rules), the host's iptables judged them and Docker's
  internal-network rule dropped every one — all egress dead, DNS fine;
- Kubernetes: a per-run peer. The pod's default route is `wg0`;
  the network policy allows only UDP 51820 to the project's gateway and TCP to
  the engine. Each peer is allowed exactly its `/32` (`10.90.0.0/16`), which
  is also its `routes.json` subnet, so attribution is the same lookup — and
  unforgeable. Keys are X25519 from stdlib `crypto/ecdh`; run identities live
  in `apo-wg-<run>` secrets and are reused across re-provisions.

Gotchas the design review caught, all handled in `apo-wg-up`:
- the gateway's endpoint needs its own `/32` route out `eth0`, or the tunnel's
  outer packets route into `wg0` and the handshake never completes;
- the CNI's pod-network routes must stay (the engine's dial-in replies use
  them; the network policy is what fences them);
- MTU is `eth0 − 60` (IPv4 outer header), computed at runtime;
- a replaced gateway pod strands the UDP conntrack entry behind its Service
  IP: the watchdog rebinds the listen port after three minutes without a
  handshake.

The engine↔gateway channel is now one **control API** on both hosts
(`workspace/gateway/control.go`, bearer token): policy and run routes in,
denials, audit and build-cache facts out. Docker publishes it on the engine
host's loopback (`-p 127.0.0.1::9041`; host→container works on hardened
hosts); Kubernetes reaches the gateway's Service. The bind-mounted
`gw/<project>` file store is gone; the gateway keeps its state on its own
volume (`--state-dir`) and generates its registry configs itself. A run link
never reaches the control port (an nft `input` drop for non-uplink
interfaces). Docker gateways of the previous shape are recreated on first use
(`apothecary.gw.shape=2`), their runs re-attached at their old addresses.

`--protect-cidrs` (k3s defaults `10.42.0.0/16,10.43.0.0/16,169.254.0.0/16`)
closes the hole a cluster opens: the gateway's uplink is the pod network, so
without it an allowlisted IP range or a deny-mode project would reach cluster
services and cloud metadata. It is enforced twice — an nft forward drop before
either mode's accepts, and a denial in the TLS/HTTP filter — and the
gateway's own network policy excludes the same ranges.

### Browser, apps, deployment

- Browser pods have no ingress and reach only the engine's `SessionMux`
  (`:7071`, the same preamble with a per-session token) and the cluster DNS.
- `portrelay.RouteByHost`: exposures become `<port>-<run>.<apps domain>`,
  served by the engine's HTTP server. App hosts are other origins, so access
  is handed over per host (`internal/httpapi/apps.go`): a one-time code from
  the UI host to the run's owner, traded for a host-only cookie that never
  reaches the app. code-server (`--auth none`) is therefore behind the user's
  session.
- Images are files now (`workspace/images/*`, `deploy/images/sandbox`), built
  by `wsimage` on a docker host and published by `deploy/images.sh` (six
  images per release since the tools bundle; CI and `release.sh` use it).
- Packaging is a Helm chart (`deploy/helm/apothecary`; `values-k3d.yaml` for
  local). The engine runs in-cluster with `APOTHECARY_K8S_TRANSPORT=direct`;
  its RBAC is a Role in the workspaces namespace only.

### Verified live (k3d, k3s v1.35.5, 2026-10-27)

`TestLiveK8sLifecycle` (`task k8s:test-live`) passes with the gateway on:
provision ≈ 55–63 s (in-pod clone over the gateway's raw-port escape hatch,
`devcontainer up` of a `debian:bookworm-slim` image with, at the time, the
code-server feature — now the tools bundle mounted), exec and exit codes, binary file round trip, declared ports, the
engine Service from inside the workspace, the sidecar and an exposed port
through the dial-in mux over `kubectl port-forward`, cgroup stats, a denied
host recorded and attributed to the run, a live allow passing on the next
request, a route around the tunnel dropped by the network policy, the audit
trail, reattach after an adapter close, and Destroy. `wg0` came up on the
gateway without a manual `modprobe` (the kernel autoloads it), MTU 1390 on
flannel's 1450. The chart installs on k3d (`task k8s:deploy`) and a
`kubernetes` integration with every field empty passes its Test in-cluster.

Bugs the live runs found, fixed:
- the control-port `input` drop also dropped loopback, which is how
  `kubectl port-forward` arrives — now `iifname != { EXT, "lo" }`;
- the gateway Deployment ignored the image pull policy (a rebuilt `:local`
  never reached the node);
- kubectl skips its in-cluster fallback whenever a config override such as
  `--request-timeout` is passed, and dials `localhost:8080` (the engine
  itself). `kube.Kubeconfig` now writes an explicit in-cluster kubeconfig
  (server, CA file, token file — the token by path, since it rotates).

### Deliberate residuals

- Privileged sandbox pods are host root, as on docker; sysbox
  (`runtime_class`, privileged off) is the unprivileged path, designed but not
  live-verified on Kubernetes yet.
- Every exec costs an API round trip. The ~fifteen feature-materialisation
  execs a provision used to cost are gone with the tools bundle (TODO 39);
  one read of the seeded bundle's hash remains.
- An out-of-cluster engine (`go run` against k3d) needs an IP engine URL and
  `engine_cidr` for the policies; it is the test path, not a deployment.
- One engine per workspaces namespace.

## Instance limits are an admin setting (2026-10-27)

Reverses the TODO 25 decision that the capacity caps live in the deployment
environment. `APOTHECARY_MAX_CONCURRENT_RUNS` and `APOTHECARY_MAX_ENVIRONMENTS`
are gone — no env override at all, by the user's choice. The caps are one row
(`instance_limits`, migration 0014; no row = the defaults 3 and 6, `-1` = no
cap), served to the engine as `engine.StoredLimits`, which the engine already
read on every admission — so a change applies to the next run that starts,
with no restart. `GET /api/limits` is for every user; `PUT /api/limits` is
admin-only and validated (a positive count or -1). System → Limits is
editable for admins (a number and a "No cap" toggle per cap) and read-only
for everyone else. The compose file and the Helm chart no longer set them.

## omp harness (TODO 42 retired, 2026-10-27)

A third harness kind, `omp`: oh-my-pi (github.com/can1357/oh-my-pi, MIT, a
Pi fork), pinned at 18.13.1 and driven through the same ACP runner as opencode
and codex. Checked against the release binary in a Debian container: the
install line, `initialize`, `session/new` and `omp auth-broker import --json`.
No model turn has been run yet: that needs real credentials.

- **Install.** The npm package's `omp` is `#!/usr/bin/env bun` (bun ≥1.3.14,
  no Node build), so the harness layer downloads the release's standalone
  binary instead: `omp-linux-[musl-]{x64,arm64}`, checked against the
  release's `SHA256SUMS.txt`, into `/usr/local/bin/omp`. The line starts with
  `curl`, so curl is the only prerequisite the harness feature provides.
  musl gets `libstdc++ libgcc`. The binary is ~276 MB.
- **Launch.** `env OMP_MCP_TIMEOUT_MS=0 OMP_SKIP_SETUP=1 omp acp --yolo`.
  - Without an explicit approval mode, omp's ACP mode still sends
    `session/request_permission` for bash and destructive edits; `--yolo`
    turns that off. The shim's auto-approve stays as the backstop.
  - `OMP_MCP_TIMEOUT_MS=0`: omp aborts an MCP call after 30 s by default,
    and `ask_human` blocks.
  - `~/.omp/agent/config.yml` repeats `tools.approvalMode: yolo` for a human
    in the terminal, and turns off `startup.checkUpdate` and
    `marketplace.autoUpdate`.
- **MCP rides the session.** In ACP mode omp ignores every MCP config file
  (`createAcpSessionFactory` forces `enableMCP: false`; upstream issue
  #1234: disk servers shadowed the client's). Writing `ask_human` into
  `~/.omp/agent/mcp.json` would be silently ignored. So `acpagent.Profile`
  gained `MCPServers`: servers that ride every session (new and reloaded)
  ahead of the stage's own. A stage server with the same name is refused.
- **Model.** omp has no `session/set_model`. The model is a config option
  (`configId: "model"`, values `provider/id`), and the shim's existing
  fallback sets it. omp's ACP option list only offers providers it holds
  credentials for.
- **Roles are in-band.** omp's named agents (`~/.omp/agent/agents/*.md`) are
  task subagents only. A session cannot start as one, and its modes are
  `default`/`plan`. So the role preamble codex used is now shared, in
  `acpagent.WriteRoles`/`RolePreamble`, and omp uses it too. `read_only` and
  `allow_bash` are instruction-level, the same residual as codex.
- **Auth: two secret fields.**
  - `env` holds provider API keys as `KEY=VALUE` lines. They ride
    provider.env, where omp reads them natively. The env-var → provider map
    comes from omp's `providers/<id>.kdl`.
  - `oauth_json` holds subscription logins as CLIProxyAPI credential JSON
    (`type` claude|codex|gemini|antigravity, `access_token`,
    `refresh_token`, `expired`), one object or an array. omp keeps
    credentials in SQLite (`~/.omp/agent/agent.db`), so SeedAuth stages the
    documents and runs `omp auth-broker import <dir> --json` as the
    workspace user. A login omp skips fails bootstrap with omp's own
    reason. The staging dir is removed.
  - `agent.db` is the AuthFile, removed at run end; omp also writes
    refreshed tokens there. The descriptor gained `Field.Multiline`, so a
    non-`_json` secret can be a textarea.
- **Models come from omp's own catalog, not models.dev.** A subscription
  serves its own model set: a ChatGPT login is `openai-codex`, 9 models on
  `chatgpt.com/backend-api`, not the API's 73. Antigravity serves Claude
  models. Only omp's catalog knows this. `Models()` reads omp's bundled
  `packages/catalog/src/models.json` at the pinned tag from
  raw.githubusercontent.com (~12 MB, once per version per process). That is
  what the binary ships and what its ACP option list offers. The shared
  egress/probe tables are keyed by models.dev id, so `ompProvider` carries
  both ids (`together`/`togetherai`, `fireworks`/`fireworks-ai`,
  `opencode-zen`/`opencode`).
- **Egress.** `catalog.stencil.so` (omp's catalog refresh), the key
  providers' `providerDomains`, and per-login hosts: claude →
  api.anthropic.com, console.anthropic.com, platform.claude.com; codex →
  chatgpt.com, auth.openai.com; gemini → cloudcode-pa / oauth2.googleapis.com.
  The Anthropic token-refresh host is not confirmed from source, so both
  candidates are listed.
- **Cost.** omp sends `usage_update` with a cumulative USD `cost`, so budgets
  work as they do for opencode.

## Stacked merge requests (TODO 43 retired, 2026-10-27)

- **Stacked means it targets another open merge request's branch.**
  `ports.StackedOn(target, open)` finds the base among the repository's
  open merge requests (`ListMRs`). A fork is never a base, because its
  branch lives in another repository. Nothing is recorded; the hosts have
  no stacking concept of their own, so it is read off branch names on
  every listing.
- **Where it shows.** The workshop's merge-request rows (`MRRow.stacked_on`)
  carry a "stacked on !N" pill linking to the base. The flow picker warns
  that what a run merges goes into the base's branch, not the main line.
  The run page's live status (`MRLive.stacked_on`, open MRs only) says the
  same next to mergeability, for a run's own merge request too: a run
  forked off a branch that has a merge request opens a stacked one.
- **Cost.** `MRStatuses` lists open merge requests once per repository
  that has an open one. A list that fails leaves no notice; the status it
  already read is still returned.

## Tools bundle (TODO 39 retired, 2026-10-27)

The harness CLIs and code-server no longer ride into repo-authored dev
containers as injected devcontainer features. They arrive the way the shim
does — apothecary-owned material the HOST mounts — so a future
`.apothecary/apothecary.json` definition gets them for free, and nothing of
apothecary's touches the repo's build any more (no features, no apt/npm/curl
inside the image build, no Node pulled into images that have none).

- **One image, `apothecary/tools:<hash>`** (`workspace/images/tools`, built
  with that directory as its context): opencode, codex, codex-acp and omp as
  standalone builds for both libcs, and code-server's standalone tree (glibc
  only: it bundles its own node and the project ships no musl build), laid
  out as `/tools/{gnu,musl}/bin`. 2.3 GB (omp is 276 MB per libc, codex 290
  MB, code-server's tree about a gigabyte). codex-acp has no prebuilt
  binaries, so it is compiled from the npm package with `bun build --compile`
  as its own package.json does — in a glibc bun image for the gnu variant and
  an Alpine one for musl, because bun on a musl host silently builds musl for
  an unsuffixed target (found live: both outputs had one BuildID and asked
  for ld-musl). It finds codex through `CODEX_PATH`, which the wrappers set
  to the bundled static codex; that pins `CodexVersion` at 0.160.1, what
  codex-acp 2.2.2 requires — the npm install had been running codex-acp
  against its own nested 0.160.x all along. x64 takes the `-baseline` builds
  (no AVX2 requirement, what opencode's own installer falls back to). The
  musl wrappers add the bundle's libstdc++/libgcc to `LD_LIBRARY_PATH` only
  when the image lacks its own. Verified on `debian:bookworm-slim` and
  `alpine:3.21`: every tool answers `--version`.
- **Versions are release pins:** the Dockerfile's ARG defaults, held to
  `kinds.{Opencode,Codex,CodexACP,Omp}Version` by
  `TestToolsDockerfilePinsMatchKinds`. The per-integration `version` /
  `acp_version` fields are gone (user decision): a CI-published bundle cannot
  honor them on Kubernetes, and one bundle per version set would be
  gigabytes each. `workspace.ToolsHash()` (`apothecaryd tools-hash`: the hash
  of the build directory) names the bundle everywhere — `TOOLS_HASH` inside
  it, the image tag, the cache dir.
- **Delivery, docker:** `wsimage.EnsureTools` builds and extracts to
  `<RunsDir>/tools-cache/<hash>` (create + cp, like the shim). The sandbox
  bind-mounts the PARENT dir read-only at its own path, so a Rebuild sees an
  upgrade's new bundle without a sandbox recreate — and so no bundle dir is
  ever swept here (a running workspace may execute from it; a sweep must
  first check what sandboxes mount; ~2 GB per engine version until then).
  `devcontainer up --mount` adds `/opt/apothecary/tools`; the CLI's mount
  parser keeps only type/source/target (verified in its dist), read-only
  comes from the sandbox's `:ro`. `cmdServe` warms the build at boot
  (`Resolver.WarmImages`, same Builder so its lock serialises with a
  provision) — the first build takes minutes. A sandbox from before the
  mount carries no `apothecary.sandbox.shape=2` label and is recreated on
  first use with its /var/lib/docker volume kept: the gateway's shape
  mechanism, because Rebuild and recovery re-enter `ensureSandbox` on an
  existing sandbox and docker refuses a bind whose source is missing.
- **Delivery, Kubernetes:** a `tools-seed` init container (the image's
  `seed.sh`) copies `/tools` onto the run volume once per bundle version
  (`TOOLS_HASH` equal ⇒ skip); `tools_image` on the integration
  (`APOTHECARY_TOOLS_IMAGE`, the sixth image of `deploy/images.sh` and the
  chart). The engine checks the seeded hash like the shim's but only warns
  on a mismatch — tool versions are not an ABI. Every first pod start copies
  the bundle onto the run PVC; image volumes (default-on since 1.35,
  containerd ≥ 2.1) would mount it instead, later.
- **Links, not PATH:** `rootInit` probes for musl's loader and `ln -sfn`s the
  matching `bin/` entries into `/opt/apothecary`, which every Exec'd PATH
  already has APPENDED — `command -v opencode`, the ACP launch and `omp
  auth-broker import` are untouched, and an image shipping its own harness
  still wins. Absolute container paths into the mount, so an engine upgrade
  needs no relink on Reattach; Rebuild relinks anyway.
- **The editor runs as the workspace's user, through Exec.** User
  requirement: the integrated terminal must be a real workspace shell. So
  `StartEditor` is one `devcontainer exec` script — remote user,
  containerEnv/remoteEnv, the probed login PATH plus /opt/apothecary, hence
  the repo's toolchain AND the harness CLIs in the terminal: settings
  re-applied, the extension union (the definition's own list through the
  new `Definition.EditorExtensions` ∪ `EditorSettings.Extensions`) installed
  into `/apothecary/code-server/extensions` whenever the marker holds
  another list, then a detached start (a bash background with every fd
  redirected; no nohup/setsid assumed) and the bounded wait. One exec where
  there were four (they count on Kubernetes). The old path ran `docker exec`
  as the image's default user, which could differ from `remoteUser`
  (settings and `$HOME/repo` in the wrong home). A musl image has no
  code-server link: the script exits 61, reported as "needs a glibc-based
  image". `EnvSpec.{HarnessInstall,CodeServerExtensions}` and
  `Harness.InstallLayer` are gone; the extension list rides `EditorSettings`.
- **Extensions come through the gateway** (user decision): `--gallery`
  serves an Open VSX reverse proxy on :5002 (`workspace/gateway/gallery.go`),
  reachable from every run like the mirror; `EXTENSIONS_GALLERY` points the
  run's code-server at it — the install step AND the editor's own
  marketplace UI, inside allow-mode projects too (gateway-originated fetches
  are the documented carve-out). Version-addressed responses
  (`/api/{ns}/{name}/{ver}/file/…`, `/vscode/asset|unpkg/…`) are cached for
  good under `<state>/gallery` and served from there first (a project's
  second run installs with no upstream fetch; an upstream outage does not
  matter); queries pass through, their absolute upstream URLs rewritten to
  the listener as the client addressed it; storage 302s (Azure blob) are
  followed on the gateway, `/vscode/item`'s is handed to the browser. The
  build-cache wipe clears it too. No gateway (`gwip` marker absent) ⇒ no
  override: code-server reaches Open VSX itself, egress being open. The
  gateway's address is a per-run marker (`gwip`, beside `gwproject`) so
  StartEditor finds it after an engine restart.
- **Residuals.** The image is big (two copies of each bun-built CLI plus
  code-server's tree); `-baseline` costs AVX2 users a little speed; the
  gallery cache has no size cap; an editor on musl images would need
  code-server in a sidecar container, which breaks terminal parity —
  rejected. Four engine model-check tests (`TestAgentAccepts`,
  `TestRunRefusedForAModelItsHarnessCannotReach`, …) were already failing
  on the tree this landed on; untouched.


## Take-over (TODO 41, part 2, 2026-10-28)

The whisper delivers one message into the running stage's live session and
the agent carries on; the engine then reads the end of the agent's next turn
as the end of the stage — right for a note, wrong for a conversation. There
was no signal telling "done with the task" from "answered your question":
the ACP runner returned whenever a turn ended with an empty inbox, and a
follow-up only landed if it beat the reply's turn end. A take-over makes the
human's intent explicit.

- **Hold / release.** `POST /api/runs/{id}/hold` puts the live agent session
  in the signed-in user's hands; `DELETE` hands it back
  (`Engine.TakeOver/Release`, `internal/engine/hold.go`). While held,
  `ports.Inbox.Held()` is true and the ACP runner's `drive` loop waits at a
  turn end for the next message instead of returning — every message is a
  turn in the same session, interrupt still works. A release records
  `hold_released` and queues the fixed handback prompt (a `Verbatim` inbox
  message, logged as `agent_prompted{kind: handback}`) as the session's last
  turn; a release during a turn in flight waits for that turn. Only once the
  handback turn ends does the invocation return and the output contract run.
- **On the record.** `hold_taken{stage, iteration, user}` and
  `hold_released{reason released|stage_ended, duration_ms, turns, messages,
  usage}`. Turns are the ones the holder's messages prompted (turn_start
  under the hold); their spend comes from the new per-turn `Usage` on the
  runner's `turn_end` event. Every leg counts: a `stage_paused` under a hold
  banks `hold` (its leg's spend) and the release sums them. The attempt
  ending underneath a hold (harness error, timeout, abort) closes it with
  `stage_ended`.
- **Quiet holder.** The stage clock stops while the agent idles on the
  holder (as for a pending ping). Past `AskHumanHold` the run parks:
  `stage_paused{reason: held}` → waiting_on_human, dormant, slot freed, hold
  in force. The holder's next message or the release wakes it
  (`wakeHeld`: `run_unpaused{reason: hold}`, then
  `stage_resumed{message|released}` resumes the session with the message or
  the handback). A turn ending on the agent's own open question under a hold
  parks on the question as any attempt does, hold kept.
- **Projection and UI.** `run.View.Hold` (who, since, banked turns /
  messages / cost). The composer's Take over button (only while an agent
  stage runs), the held state (amber, live clock, Release), "Handing back…"
  until the engine records it; an "In your hands" pill in the header; "· in
  your hands" on run rows; transcript status lines and timeline entries with
  the hold's account.
- **Residuals.** A release clicked during a turn in flight is in-memory
  until that turn ends (a crash in that window keeps the hold; click again).
  Part 1 of TODO 41 — a read-only companion chat at flow level, not a stage —
  is not built yet.

## The Ask pane (TODO 41, part 1, 2026-10-28)

A flow's `ask: { harness: <kind | $param> }` gives every run an Ask pane: a
read-only session beside the run, from the moment the workspace is up until
it is destroyed, that answers questions about the code as it stands. Not a
stage: it runs in parallel with whatever stage is working, on its own
harness if the flow says so, and nothing of it is a run event. User
decisions: the flow names only the harness (the model is picked in the
pane, live), it must not have write access (a stage may be editing), and
the name is "Ask".

- **The sidecar hosts several agent processes and one turn per session.**
  Until now the shim ran ONE process (a new command line killed the old)
  and ONE turn at a time per workspace (`errBusy`), which made a parallel
  session impossible. Now `agentHost` keeps `procs` by command-line id
  (`/agent/start` returns `proc`; a session request names it, or the one
  process running), `sessions` by id (each knowing its process) and `turns`
  by session — a prompt is refused only while THAT session's turn is in
  flight. `/agent/status` reports `turns` (plural; `Status.Turn(session)`
  on the client), `agent_exit` carries `proc`, and a different command line
  no longer kills the first process (a stage switching harness leaves the
  old one idle). The engine's reattach reads the session's turn.
- **Read-only, per harness.** `ports.AgentInput.ReadOnly` opens the session
  read-only; `ports.ReadOnlyCapable` says whether a runner can promise it.
  The ACP profile says how: opencode by the agent definition (`ReadOnlyBy:
  agent` — the engine prepares the reserved `ask` agent with edit, patch,
  write and bash off, and `ask_human_*` off so a question it raised cannot
  land on the stage), codex by session mode (`read-only`, codex-acp 2.2.2's
  spelling beside `agent-full-access`; the sidecar's `setMode` also accepts
  the same mode under a `-`-prefixed spelling). A `read_only` opencode agent
  keeps `write` only when it DECLARES outputs — the planner/reviewer still
  write their artifacts, an agent with nothing to write gets none. omp has
  no per-session mode and its named agents cannot start a session, but it
  takes a per-PROCESS tool list: a read-only session is a SECOND omp process
  (`ReadOnlyBy: process`, `ReadOnlyCommand` = `omp acp
  --tools=read,grep,glob,web_search,browser --approval-mode=always-ask`,
  no `--yolo`) beside the stage's — possible only because the sidecar now
  hosts several processes. browser reads pages and drives the app, never
  the repository (and needs a Chromium the workspace may not have). A
  read-only session also gets none of the profile's own MCP servers
  (ask_human), so an Ask answer cannot raise a ping as the stage's — on
  omp; codex's ask_human is config-wide and stays a residual. A harness
  that promises none of the three keeps the pane off, with the reason. The
  sidecar is the backstop: a read-only session's permission request is
  granted only for a tool whose kind plainly reads (read, search, fetch,
  think); anything else — including a request with no kind — is refused
  with the reject option.
- **Engine (`internal/engine/ask.go`).** `AskState` / `Ask` / `AskSetModel`
  / `AskWatch` / `AskMessages`. The conversation is `internal/ask` rows (a
  thread: session id + model; messages), in Postgres via migration 0015 or
  in memory. A question is recorded and its answer runs detached (15 min
  cap) on the harness the flow names, `Run` the first time and `Resume`
  after, as `spec.Agent = ask`; the answer's text streams as delta frames
  through an SSE of its own (`/api/runs/{id}/ask/stream`: messages with id =
  seq, `delta` and `state` events), scrubbed by the run's redactor, and
  lands with its model, cost and turns (or its error). One question at a
  time per run. The model defaults to the flow's `model` parameter when the
  harness reaches it (the provider's list, else what the agent advertises),
  else the first reachable; a pick in the pane is kept on the thread and
  the live session is switched over (`AgentInput.Model` on Resume — the
  sidecar now applies a model to a live session too) before the next
  question. Preflight refuses an `ask:` harness outside the project's set,
  like a stage's.
- **Agent prepared into the workspace.** `agentsFor(fl)` appends the
  builtin `ask` spec (read-only, no bash, a prompt that forbids changes and
  questions and warns that another agent may be editing) at the three
  Prepare sites (bootstrap, rebuild, envcheck). `agentspec.AskAgent` is the
  reserved name; a flow with `ask:` cannot declare an agent called `ask`.
- **UI.** An Ask window in the desk (beside Session in every preset); the
  flow editor's formula panel has an "Ask pane" field (a harness kind or
  `$harness`; empty = off).
- **Residuals.** On codex, ask_human is an MCP server of the whole config,
  so an Ask answer that insists on asking a question would raise a ping
  against the run as the stage's (the role prompt forbids it; opencode's
  tool is off, omp's read-only process has no servers at all). omp's
  read-only command line was checked against omp 18.12.2 locally (`omp
  acp` starts and answers initialize with `--tools` and
  `--approval-mode`); a live turn on the pinned 18.13.1 in a workspace —
  ask it to write a file and run a command, expect neither — is still
  owed, as is every omp live turn (TODO 42). The three builtin templates
  declare `ask: { harness: $harness }`: every run of them gets the pane on
  the flow's own harness (user decision, 2026-10-28). Models are asked
  once per run per engine process (credentials rotated mid-run need a
  restart to show up).

## ask_human mechanics ride the role, not the flow (2026-10-28)

The builder template's planner and builder prompts each carried a paragraph on
how `ask_human` works: that the harness may list it under a prefixed name, that
it waits for the human, that a guess is worse than a wait. Two things were
wrong with that. It is platform mechanics in flow content — every agent stage
can ask (FLOWLANG §9), so every flow author would have to paste the same
paragraph, and a flow that forgot it would re-run the namespacing incident
above. And it was mostly a copy of the tool's own description, which the model
already reads (`workspace/shim/askhuman.go`); the one fact a tool description
cannot carry is *the name the harness lists it under*, and that is a harness
fact, not a flow fact.

So the two renderers that turn a spec's prompt into what the agent reads now
append a standing section after it, `acpagent.AskHumanRules(listedAs)`:
opencode's agent definition (`agentMarkdown`), which knows the exact name
(`ask_human_ask_human`, server_tool), and the in-band `# ROLE` preamble codex
and omp get (`acpagent.RoleText`), which does not and says it may be prefixed.
The section names the three types and that the call waits; form and decline
semantics stay in the tool description, where they were. The Ask pane's agent
is skipped — it has no ask_human (opencode denies `ask_human_*`, a read-only
session gets no profile MCP server) and its prompt says never to ask.

The flows now state only their policy: "ask the human (type=clarification)
BEFORE writing the plan rather than guessing", "ask the human (type=blocker)
instead of improvising a redesign". The agent editor's "what a stage adds"
preview lists the section, so what the YAML shows and what the agent reads
stay the same thing. `agentspec.Hash` is unchanged: it addresses the authored
prompt, as it did for the READ-ONLY lines the role already appended.

## No model ids in the tree: unbound parameters, explicit stage harness and model (2026-10-28)

Commit 8d0f33c renamed a model id across the repo and broke four engine tests
on the way: their deliberately absent fixture model became present. The
inventory behind that one incident found about ninety test documents, three
templates, the UI's new-flow skeleton, the dev flow and the docs all carrying
a real model id, for one reason — the parser demanded a literal
`parameters.model` in every document, even when a project binding would have
supplied one. Model ids change under documents that must not, so every rename
was a sweep, and a template pinned yesterday's model until someone noticed.
Decisions, in the user's words: no per-project default; only the flow's bound
override and the run's override, the run winning; each stage declares what it
runs on; two stages pointing at one model parameter from two harnesses fail
the YAML; the dropdowns infer the harness.

- **A parameter may have no default** (`flow.Param.Required`). `harness` and
  `model` always exist, unbound when the document leaves them out; any other
  declaration may omit `default` too. Only a site reading a **model** or a
  **harness** tolerates that (`paramSite.unbound`): `lookup` refuses an
  unbound `run_ref` or timeout at parse time as before. The document parses,
  saves and edits; `Flow.Unbound()` lists what has no value and where it is
  read, `flow.UnboundError` says it the way a run is refused — *"model" has
  no value (read by build, review): bind it in Workshop → Flows → Configure,
  or set it when starting the run* — at run creation right after
  `fl.Resolve`, and as the project preflight's `parameters` step
  (`bindFlow`).
- **Every agent stage states `harness:` and `model:`** — a kind or a
  `provider/model`, or a `$parameter`; the flow-wide fallback is gone
  (`Stage.Model/Harness` are the stage's own, `""` while unbound). The usual
  spelling, `harness: $harness, model: $model`, is now written on every
  template and test stage: 358 insertions by a sweep script, kept
  deliberately so what a stage runs on is read off the stage.
- **Stages sharing a model parameter share a harness** (`roles.go`), judged
  *as written*: the same kind, or the same `$parameter` — a literal at one
  stage and `$harness` at the other is refused even when today's binding
  agrees. The same pass derives `Param.Role` (model | harness, one role per
  parameter) and `Param.Harness` (a model parameter's one harness: a kind or
  `$name`), which is what the forms needed.
- **`default: $other`** (`Param.DefaultRef`): a default that reads another
  parameter — `review_model: { type: string, default: $model }` — same type
  and shape, no cycles; bound wins over followed; an unbound end of the chain
  is reported as that parameter. `resolveParams` fills the chain last, after
  bindings and overrides, so `review_model` follows an overridden `model`.
- **Templates** carry no model: `model` is left out of the parameters block
  with a comment saying why; the builder's `review_model` defaults to
  `$model`. The UI skeleton and the dev flow (`fake/model`) likewise.
- **Forms**: `ParamFields` picks the control by `role`, not by name, and each
  model field suggests the models of *its* harness — `paramValues.ts`
  resolves `Param.harness` against the form's layers (override, binding,
  default, followed default), and the Configure sheet and the start-run form
  fetch one list per harness kind the flow's model parameters need. A
  parameter with no value reads *no value yet*; one following another reads
  *follows $model*. The stage inspector marks harness and model required with
  `$harness` / `$model` placeholders; the parameters editor writes a
  model/harness-read parameter with an empty default as one with none.
- **Tests** never name a vendor: `fake/model` and `fake/other` where a value
  is only required, `acme/alpha-1..3` (+ `alpha-codex/high`, `other/alpha-1`,
  `acme/kappa-3`) where model matching is the subject, `go-model-N`,
  `alpha-1`, `gamma-2`, `luna-6`, `kappa-2` as catalog fixtures in `kinds`.
  `TestBuiltinFlowsCarryNoModel` holds the templates and the shipped example
  to it; `task lint:models` (in `task verify`) greps the tree for
  vendor-looking ids outside the notes and the UX mocks.
- The project-level default harness/model stays absent (2026-07 decision);
  `Resolution.HarnessKind` remains the envcheck fallback, now documented as
  unreachable by a stage since run creation refuses an unbound harness.

## Model providers, logins and the keeper (2026-10-29)

Built from docs/model-providers-plan.md. The why is there; this records what
shipped, the facts it rests on, and where it differs from the plan.

**Shape.** `internal/providers` is the closed set (17 providers: keys, the
four OAuth flows ChatGPT / Anthropic / Google gemini-cli + Antigravity /
Copilot) with each harness's native mapping, plus the credential type and the
importers. `confstore` holds `provider_credentials` (migration 0016, which
also deletes old-shape harness integrations and clears projects' harness
sets — unreleased, no carry-over). A harness integration's config is
`credential_ids`; validation checks owner, provider/harness/kind
compatibility and sinks (two credentials that would overwrite each other in
one harness — an OpenAI key and a ChatGPT login in opencode's auth.json, the
two OpenCode gateways on omp's `OPENCODE_API_KEY`). `internal/logins` is the
keeper (refreshers, single-flight `Fresh`, `Sweep`/`Run`, `OnRefresh`) and
the sign-in flows. The resolver opens every harness credential through
`Keeper.Fresh`; harnesses render from them (`kinds/credentials.go`).

**Facts verified in the pinned sources, which the design leans on.**
- codex 0.160.1 (codex-rs `login/`): `TokenData.refresh_token` is a required
  `String`, so `""` parses; `id_token` must be a parseable JWT (the keeper
  keeps it); proactive refresh only within 5 min of `exp` (the 8-day rule
  only without one); before any refresh codex re-reads auth.json and uses a
  changed file without calling the token endpoint. Hence push works.
- opencode 1.17.8: `Auth.all()` reads auth.json on every call (no cache) —
  the plan's open check; no harvest branch was needed. OAuth entry
  `{type, refresh, access, expires(ms), accountId?, enterpriseUrl?}`. It has
  ChatGPT and Copilot logins; Claude Pro/Max is not in 1.17.8 (key only).
  Copilot sends `refresh` as the bearer with `expires: 0`.
- omp 18.13.1: endpoints, client ids and request shapes for all four flows
  (rules/auth/*.kdl, registry/oauth/*.ts); the broker wire (strict ArkType
  schemas, `__remote__` sentinel, ETag generation, 404 on the stream →
  long-poll). Copilot's GitHub token is long-lived and is itself the bearer:
  nothing to refresh, nothing withheld.

**Differences from the plan.**
- *Antigravity sign-in* is not offered: its project discovery depends on a
  client fingerprint (version + changelist) omp discovers at runtime.
  Import (CLIProxyAPI with `project_id`) and refresh work. Gemini CLI
  sign-in discovers an existing Code Assist project or onboards the free
  tier; an account that needs `GOOGLE_CLOUD_PROJECT` is told to import.
- *Moonshot* is not in the table (no verified native mapping); Cerebras and
  Fireworks are.
- *The broker token* is not the run token (minted per boot, so a long-lived
  omp would 401 after an engine restart) but HMAC(instance secret, run id):
  `confstore.InstanceSecret("auth-broker")` — 32 random bytes sealed in the
  instance's settings (owner ""). Served at `/api/runs/{id}/auth-broker/…`,
  access kind `AccessBrokerToken`.
- *No harvest anywhere.* Every harness gets access tokens only (codex
  `refresh_token: ""`, opencode `refresh: ""`, omp the sentinel), except
  Copilot whose single token is the bearer.
- *omp API keys* stay env lines; the broker serves only logins.
- The integration *Test* for a login does not probe: a fresh token from the
  keeper is the proof. ChatGPT logins are additionally asked for their
  account's model list (`GET chatgpt.com/backend-api/codex/models`), which is
  also codex's model dropdown and refuses a flow model outside the plan.

**Push.** `Engine.PushCredential` (keeper `OnRefresh`) rewrites the harness
files of live runs whose resolution's `CredentialIDs` holds the credential,
registering each token with that run's redactor first (`SeedAuth` now takes a
`register` callback that is called before any write). Sinks register at
bootstrap and re-entry and drop when the run loop ends.

**Tests that hold the security rules.** `resolve/TestNoRefreshTokenReaches
AWorkspace` (rule 3, real codex/opencode/omp harnesses), `engine/
TestPushCredentialScopesAndRedactsFirst` (rules 2 and 4), `httpapi/
TestCredentialsAPIIsOwnerScopedAndNeverEchoes` and `TestAuthBrokerTakesOnly
ItsRunsToken` (rule 1), `kinds/TestOmpBrokerWire` (schema + sentinel).
`task test:integration:harness` runs the real pinned omp from the tools bundle
against the broker face (`kinds/TestOmpConformance`): it fetched the snapshot,
fell back from the stream, and listed the served logins' models — and none
against an empty broker. The same task holds opencode and codex to what the
engine generates for them (`TestOpencodeConformance`, `TestCodexConformance`,
build tag `conformance`; see "Harness conformance tests").

**Found while running it.** The settings table stores JSON; the instance
secret was first written as a bare string and Postgres refused it at boot.
`Mem.PutSetting` now rejects non-JSON too, and pgstore has a round-trip test.

**Known limits.** The keeper is single-process (one engine; a second
replica would need a DB lease per credential). Signing out of a provider when
a credential is deleted is not done. Pending sign-ins live in memory (an
engine restart loses one in progress).

## Skills and skillsets (TODO 38, 2026-10-29)

A user's library of Agent Skills folders (`SKILL.md` + support files),
grouped into versioned skillsets a run picks at creation and the engine
installs into every harness in the project's set. Nothing of it is in the
flow grammar: skills are a per-run selection, like the source branch.

**Ownership and shape.** User-owned, global across workshops (the System
hub, beside Integrations) — the per-user model has no organisation level,
and skills a repository wants for everyone already live in its own
`.agents/skills` / `.opencode/skills`, which every harness reads from the
checkout. Three tables (`0017_skills.sql`): `skills` (mutable rows: name,
frontmatter description, a `files` manifest of `{path, sha256, size, mode}`,
a content `hash`, an optional `source`), `skillsets` + `skillset_skills`
(ordered current membership; `ON DELETE RESTRICT` so a listed skill is not
deletable — 409 naming the sets), and `skillset_versions` (`seq`, `hash`,
an immutable `manifest` snapshot of every member's files). Bytes live in
the blob store; both jsonb paths are in `LiveBlobSHAs`, or the sweep would
reclaim them after the grace hour. Owner columns are the plain `user_id`
of 0013 (no FK to users).

**Version policy.** `skillstore.Store.bump`: a skillset's hash is
`SetHash` over its members' (name, content hash), order-independent; a new
`v{seq}` is appended only when that differs from the head's. Every content
change funnels through it — a member edit cuts one version in each set
listing the skill, a membership change or rename cuts one, a remote refresh
cuts one at the end (not one per touched child). Reverting content appends
a new seq rather than pointing back; latest is always max seq. Renaming a
listed skill is allowed: the snapshots keep the old name.

**Remote import.** `skillstore.Importer`: a public folder URL
(`github.com/{o}/{r}/tree/{ref}/{path}`, a GitLab `/-/tree/` URL on any
host, or a direct `.tar.gz`/`.zip` with an optional subfolder) is fetched
the way `curl` would — a plain unauthenticated GET of the host's public
archive (codeload tarball; GitLab `/-/archive/…?path=`), no tokens, no git
clone, private repositories out of scope. The commit comes from the git
archive's PAX `comment`. A folder holding `SKILL.md` is one skill (unique
per owner by name and by source URL — importing it again says "refresh
instead"); otherwise every child directory with a `SKILL.md` is a skill and
the folder a skillset named after it. Children that fail validation or
clash with an existing name are skipped and reported as warnings, not
failures. A ref containing `/` cannot be told from the path and is read as
its first segment; no ref means `HEAD` (GitLab also tries `main`, then
`master`). Caps: 64 MiB per archive, 1 MiB per file, 8 MiB and 200 files
per skill.

**The run.** `StartRunRequest.skillsets: [{id, seq}]` (0 = latest) →
`engine.RunInput.Skillsets` → `CreateRunWith` resolves them through the
`SkillStore` port (the project owner's only; anyone else's reads as not
found) and stamps `RunCreated.Skillsets` pins `{id, name, seq, hash}` →
`run.View.Skillsets` (in the view JSON, so `DeleteSkillset` can refuse
while a live run pins the set) → replay → bootstrap. A rerun carries the
original's skillsets re-pinned to their latest (as the flow is re-read by
name); an explicit empty list clears them.

**Install.** Bootstrap, after `agent-configs` and before `Prepare`: `rm -rf
/apothecary/skills && mkdir -p` (always — a re-bootstrap must not keep an
older selection), then one `EnvProvisioner.WriteTree` per skill
(`skills.WorkspaceDir/<name>`) and a `manifest.json` of the pins. `WriteTree`
is new on the port: a tar built engine-side rides stdin behind a one-line
script, as `WriteFile`'s base64 does — one exec per tree instead of one per
file, the same path on docker and Kubernetes. Then, after `Prepare`,
`Resolution.InstallSkills` fans `Harness.InstallSkills(names)` out over
every harness in the set; each adapter owns its location and calls
`acpagent.InstallSkills`, which COPIES (`cp -R`, one exec) into: codex
`$HOME/.agents/skills`, opencode `~/.config/opencode/skills`, omp
`~/.omp/agent/skills`. Copy, not symlink: omp's symlink following is
undocumented, and skills are kilobytes. opencode and omp also scan each
other's and codex's directories, so a project with several harnesses shows
each skill in more than one place — same name, same bytes: opencode warns
and keeps one, omp's native copy shadows the rest. Modes are normalised to
0755 (executable at the source, or under `scripts/`) and 0644. Retry and
the recovery path reuse the workspace and install nothing new; rebuild runs
the whole bootstrap again. A pinned version that no longer exists (the set
was deleted after the run finished, then the run rebuilt) is skipped with a
warning, as a vanished flow version falls back.

**Where things are.** Format rules: `internal/skills` (frontmatter, name
grammar, path rules, caps, hashes). Library: `internal/skillstore` (Store,
Importer, Mem, the `DB` port; `skilltest.RunDBContract` runs against Mem
and Postgres). Postgres: `internal/pgstore/skills.go`. Port: `ports.TreeFile`
/ `WriteTree` (workspace adapter `tree.go`, fake). Seam:
`kinds.Harness.InstallSkills`, `acpagent/skills.go`. API: `httpapi/skills.go`
under `/api/skills`, `/api/skillsets` (scopes `ScopeSkill`, `ScopeSkillset`;
skill bodies carry base64 file contents under their own size cap). UI:
System → Skills, the picker on the start-run pane and the rerun modal,
chips on the run header.

**Known limits.** Manual refresh only (no polling). Automations start runs
with no skillsets; a workshop-level default list is the natural follow-up.
`tar` and `base64` must exist in the repo's devcontainer image (busybox is
fine). A GitHub folder import downloads the whole repository tarball (there
is no server-side path filter), bounded by the 64 MiB cap. There is no
Claude Code harness in apothecary; when one exists its directory is
`~/.claude/skills`.

## Review memory: poll first, never twice, curated gates (TODO 45, 2026-10-30)

The user: the AI reviewer re-posted its notes after every push, a round could
leave a thread unanswered, the review gate could only approve or reject the
whole file, and nothing told the owner why a note was worth posting. Three
decisions, all taken together: the record of what was said is the repository
host plus the event log — never a file of ids ("should we store comment ids in
a yaml?" — no: artifacts are per-attempt views, deleted before each fresh
attempt and gone with a reaped workspace, and on GitHub a note's thread id
does not exist until a later poll); the engine enforces what it can, the
prompt carries the rest; a gate curates by decisions over the document it
showed, resolved against the snapshot it pinned.

- **`internal/reviewfile`** is the leaf package for the three documents:
  `review_threads` (`{summary, notes: [{path, line, kind, body, rationale,
  evidence, example}], replies: [{thread, body, resolve, rationale}]}`),
  `review_replies` (`[{thread, did, reply, resolve, rationale, evidence}]`)
  and the new built-in `mr_threads` (`{merge_request, threads: [{id, path,
  line, resolved, ours, kind, hunk, comments: [{id, author, at, ours, new,
  did, body}]}], dropped}`). Strict decode by extension, JSON Schemas for all
  three registered as built-in type schemas (printed in the OUTPUT CONTRACT,
  checked first — the agent-stage check order is now schema, verdict,
  replies, review), Go parsers for what a schema cannot say, and the
  curation types. The posted text is a note's body (its `kind` rendered back
  as the leading `[kind]` tag — a typed field, because `body: [blocking] …`
  unquoted is a YAML sequence), a reply's text and the summary; the owner-only
  fields never leave the engine. `did` names a reply's result because
  "outcome" is a flow word. `parseVerdict` decodes by extension too
  (`flow.DecodeStructured`); the templates bind `artifacts/verdict.yaml`.
- **Poll first.** `load_review_comments` gained `scope: pending | all` and
  writes `mr_threads` to its `threads` port (the markdown and the undeclared
  JSON sidecar are gone). `all` loads every thread, ours included, picks
  nothing and never ends the phase; the reviewer sequence is now `poll → read
  → gate → post`. The event `review_comments_loaded` keeps `threads:
  [{id, path, line, ours, resolved, last}]` and `scope`, replayed into
  `rs.round`. Markers carry words — `kind=` on a note, `did=` on a reply —
  read back with `ports.MarkerAttr`; `MarkerThread` reads a word, not up to
  ` -->`, and reconciliation compares `IsReplyTo(round, thread)`. The fake
  repository host materialises posted notes as threads, which is what made
  cross-round tests possible at all.
- **Never twice.** At the reviewer's stage end (`reviewGuards`): a reply
  names a loaded thread once and resolves only ours (G3); a note on the
  `(path, line)` of an open thread of ours (G1), or saying what one says on
  the same file (G1b, normalised body from the `mr_threads` input), or equal
  to a note a person dropped at a gate (G2) fails the stage with
  `output_invalid` and the message says "continue it under replies" / "do
  not propose it again" — the retry reads it. Exact rules on purpose: a
  note on a line a push moved is not caught, and that is the prompt's job;
  what is caught is never a false positive. `post_review` posts the notes as
  a review and the replies in their threads (`ReplyThread` + `ResolveThread`,
  reconciled after a crash by the round marker), and posts no review object
  when there are only replies. `review_posted` gained `review_id`, `replied`,
  `resolved`.
- **Every thread answered.** `reviewfile.CheckReplies` requires one reply per
  loaded thread (minus the ones a person held back at the gate:
  `rs.unskippedRound`); a comment judged wrong gets `did: declined` with a
  rationale, never silence; `review_replies_posted` gained `declined`. The
  loader marks comments after our last word `new`, so a heated thread's
  newest ask is what the fixer and replier answer.
- **Curated gates.** `ping_raised.file_types` (path → port type, structured
  files only) tells the page which files curate and the engine which parser
  checks the answer. `ping_answered.curated` is, per path, decisions over the
  document AS SHOWN — `{summary?, items: [{item: notes/<i> | replies/<i>,
  keep, body?, resolve?}]}`, absent items kept as written — validated on both
  answer paths against the ping-file snapshot (`validateCuration`; the
  `SnapshotStore` port gained `ForPing`/`Bytes` for exactly this read), and
  refused when the file was not captured. `routeGate` resolves them once per
  ping into `review_curated` (the curated document, the dropped notes, the
  replies held back, the threads skipped with their last comment id) on
  approve and reject alike; replay folds it into `rs.dropped`, `rs.skipped`
  and `rs.curated`. A gate has no workspace (it is routed from the idle
  loop, and a parked gate's workspace may be reaped), so the curated file is
  written when the next stage that needs the workspace and reads the path
  starts (`materialiseCurated`), and forgotten when a stage that writes the
  path starts — live in `append`, the same rule replay applies. No gate
  `output:` port: the capability is the port type's, "every gate is the
  same machine" holds. A rejection's drops ride the verdict (`renderCuration`).
  The HTTP API decodes the shown documents server-side
  (`GET /api/pings/{pid}/curation`), so the page never parses a review;
  `pings` projection columns `file_types`, `curated` (migration 0019).
- **Templates.** reviewer: `poll` first, the gate shows `review` + `threads`,
  the prompt reads the threads first and says when to continue a thread,
  close it or stay silent, and never to propose a dropped note. builder: the
  loader and the fixer/replier ports are `mr_threads`; the replier writes
  `did`/`rationale` and reads the gate's `rejection`; a `reply_gate`
  (`$reply_gate`, on by default — a person reads what goes to human
  reviewers) sits before `post`. The dev fake writes a document that holds
  each declared output's shape (the contract now prints the type beside the
  role: `- path — role (type): …`), a reply per thread of its `mr_threads`
  input included.
- **Known limits.** A file input is inlined into the prompt tail-cut at 16 KB:
  a long threads file loses its head (hunks are written for open threads
  only). Curation memory is run-scoped — a rerun is a new run, though the
  host's markers carry across. A paraphrased dropped note reaches the gate
  again; the person drops it again.

## The SSH door (TODO 46, 2026-10-30)

The user: "connect my desktop VS Code to the container, with access to
terminal and all of that" — from a laptop that may not be the engine's
machine, over a VPN (tailscale). Nothing SSH existed: no sshd in any image,
no key model, `vscode`/`ssh` link kinds declared and never produced. And the
engine never dials a workspace IP (host→container blocked on docker; only
the pod mux port on Kubernetes) — it has one path into a workspace, the shim
directory's unix sockets / the token mux, over which `sidecar.sock` is
already reached.

- **Shape.** `apothecaryd` runs an SSH server, the *door* (`internal/sshdoor`),
  on `APOTHECARY_SSH_LISTEN`; a person's `ssh -p 2222 <workspace>@<engine>`
  — or VS Code Remote-SSH, which is the same thing — is authenticated by
  the public keys registered on their account (`ssh_keys`, migration 0020,
  `/api/me/ssh-keys`), routed by *username = workspace name* (the env id, a
  typed `runs.env_id` column cleared when the workspace is destroyed, so
  `RunByEnv` finds exactly the live run) to a run the key's account owns,
  and then proxied — channel for channel, request for request, both ways,
  sshpiper-style, understanding none of it (`proxy.go`) — to an SSH server
  in the workspace's shim sidecar (`workspace/shim/sshd*.go`, on
  `/opt/apothecary/ssh.sock`, as the remote user). The sidecar implements
  sshd's session semantics: a login shell in the repo on the existing PTY
  code, `exec` in `$HOME`, env allowlist, window-change, signals,
  exit-status/exit-signal, `direct-tcpip` dialed inside the workspace (what
  Remote-SSH uses to reach the server it starts). No subsystem (sftp is the
  documented limit: `scp -O`, rsync work), no `-R`.
- **Why not sshd-in-container or Remote Tunnels.** A dropbear/OpenSSH per
  workspace means a third-party binary per libc flavour, user keys copied
  and refreshed in workspaces, a host key per workspace and a ProxyJump
  stanza in every ssh_config (a raw TCP jump routes only by jump target).
  Remote Tunnels relay through Microsoft with a login per workspace and the
  proprietary CLI in the bundle. The door terminates auth against the user
  table (a key added works at once), has one stable host key, needs no
  client configuration, and reuses the sidecar transport and the ownership
  model the instance already has.
- **Keys.** Both of the door's keys derive from sealed instance secrets
  (`InstanceSecret("ssh-door-host")`, `("ssh-door-client")`, the auth-broker
  precedent): the host key so a person verifies one fingerprint for every
  run, the door key so a workspace can admit the door across engine
  restarts. The engine writes the door's public key to
  `/apothecary/ssh_door.pub` right before each dial (`engine.SSHTarget`): a
  rebuilt container self-heals on the next connection. The sidecar re-reads
  the file on every attempt. Not the run token: `Engine.RunToken` exists
  only while a run's loop is up and is regenerated per boot — a completed
  run with a live workspace has none.
- **The shim** is built as its own module in docker
  (`workspace/images/shim/Dockerfile`: `go mod init` + `go get` pins), so
  `golang.org/x/crypto/ssh` is pinned there too, held equal to the root
  go.mod by `TestShimDependencyPinsMatchGoMod`; the shim stays one flat
  `package main` (a sub-package would have two import paths). A sidecar from
  before this change serves no `ssh.sock`; the adapter says so (rebuild).
- **The link.** `vscode://vscode-remote/ssh-remote+<hex(JSON
  {"hostName":"<user>@<host> -p <port>"})>/<path>`: Remote-SSH hex-encodes
  the destination string a person would type into "Connect to Host" (an
  authority is lower-cased and takes no spaces) and decodes it; the plain
  `user@host:port` form is its fallback. `path` is the devcontainer's
  workspace folder as the sidecar's `/health` now reports it
  (`remoteWorkspaceFolder` is not persisted; `$HOME/repo` is a symlink onto
  it). `GET /api/runs/{id}/ssh` serves it with the command, the fingerprint,
  the host key and the caller's key count; the run page's Env ▾ shows the
  rows; Account → SSH keys registers keys.
- **Config.** `APOTHECARY_SSH_LISTEN` (off when unset), `APOTHECARY_SSH_ADVERTISE`
  (derived from the listen host, else the public URL's host; an error when
  nothing can be derived — the address is a one-line fact of the
  deployment). Compose: bind the VPN address, not 0.0.0.0 (host network).
  Helm: `ssh.enabled/port/advertise`, a Service of its own
  (`service-ssh.yaml`; an Ingress cannot carry SSH). Users: the VS Code
  server download rides the project's egress gateway — allow
  `update.code.visualstudio.com` and `vscode.download.prss.microsoft.com`
  there, or set `remote.SSH.localServerDownload: always` on the desktop.
- **Limits.** Sessions drop when the engine restarts, the reaper destroys
  the workspace, or a rebuild replaces the container (VS Code reconnects).
  No run event per session (appending from outside the run loop is
  restricted); the door logs them. Per-address auth throttle (8 failures /
  15 min), key-only, identical refusals for unknown/foreign/gone/disabled.

### The docker-exec "terminal" link is gone (2026-10-30)

`Links()` left the provisioner port with it. The `terminal` access link was
`DOCKER_HOST=unix://<RunsDir>/<workspace>/docker.sock docker exec -it <ctr>
bash` (`kubectl exec … -- docker exec …` on Kubernetes): a copy-paste for an
operator sitting on the engine host, unreachable from anyone's laptop, and
the only link `Links()` still produced once app links moved to the port
relay. The SSH door supersedes it on every axis (over the VPN, per-user keys
and ownership, as the remote user, the same on both hosts), so the Env ▾
popover now opens on the door's rows and lists app/service links under them.
`env_ready.links` carries only the auto-exposed `app` links; `event.Link`
lost its `Command`; `run.workspace` is the workspace's first app link — what
it resolved to in production all along, the terminal link having no URL.
The envcheck "links" step went with it (the "shim" step already proves a
running container). The operator recipe lives in README's SSH section.

The Env ▾ popover also hands out the two desktop `settings.json` lines that
make a person's extensions follow them in — `remote.defaultExtensionsIfInstalledLocally`
(VS Code's own, every remote, installs what the desktop has too) and
`remote.downloadExtensionsLocally` (the desktop fetches the VSIX, so the
workspace never needs Microsoft's marketplace through the gateway). The
list is pre-filled from `GET /api/runs/{id}/ssh`'s `extensions`: what the
workspace's own editor installs, `EditorStarter.EditorExtensions` (the
definition's ∪ the project's), read without an exec. `remote.SSH.defaultExtensions`
was not chosen: Remote-SSH-only, and it installs ids the desktop may not
have. Microsoft's server cannot be pointed at the gateway gallery (its
gallery is baked into product.json), so the two editors keep separate
extension dirs; pre-seeding `~/.vscode-server/extensions` from code-server's
CLI is the follow-up to try, with a per-user extension list.

## Clean architecture refactor (TODO 7, 2026-10-30)

The backend was hexagonal in spirit already — ports and adapters, an
acyclic graph, runs event-sourced as one short stream each — and its
trouble was modularity, not paradigm: `internal/engine` was a god package
(16.5k lines, 89 exported methods on `Engine`, of which run lifecycle was
about a fifth, all under one mutex), `internal/ports` and `internal/httpapi`
were hubs, and the interpreter kept a run's state two ways — `replay`
folded events into it, the live loop mutated it by hand at ~40 sites — that
disagreed in eleven places, four of them recovery bugs. The plan, its
baselines and every deviation are in `docs/clean-architecture-plan.md`;
this is the outcome.

- **A modular monolith.** The engine is the run lifecycle and the
  interpreter's shell (11.2k lines, 33 exported methods). Every other
  concern is its own package behind an interface it declares, and never
  imports the engine: `mrs`, `resources`, `envstate`, `wsinfo`, `envcheck`,
  `egress`, `ask` (service beside its store), `authsink`, `runports`,
  `browsermcp`, `services`, plus the leaves `shell`, `wsname`, `bootstrap`,
  `modelcheck`, `redact`, `resolution`. `cmd/apothecaryd/wire.go` builds
  them, asserts that `*engine.Engine` satisfies each consumer interface, and
  hands them to `engine.New(Deps)` — no setters. httpapi holds one field per
  service and is split into per-area route files; `Server.Drain` ends event
  streams and ping holds on shutdown (a restart took the full 10 s before).
- **`ports` split by capability** — `envport`, `agentport`, `repoport`,
  `trackerport`, `egressport`, `browserport`, `notifyport` (the suffix keeps
  them from shadowing `env`, `internal/browser`, …). Type names unchanged.
  `ports/doc.go` maps them.
- **One fold.** `runcore.State.Apply` is the fold; `Replay` is a new
  state plus `Apply` over the log, and the engine's `append` applies it to
  every event it writes. The live state equals the fold of the log at every
  settling point — an engine test hook (`foldCheck`, nil in production)
  replays each run at the top of every loop iteration, after each idle tick
  and on exit, and fails the test on any field that differs. What the loop
  still sets without an event is listed on `State`: three decisions the
  next event records (a success's `on_success`, a phase end decided without
  a terminal edge, a debug-leaf continue — each re-derived by recovery the
  same way) and two facts no event carries (a caretaker begins idle; the
  env id before `env_ready`).
- **`internal/runcore`** is the interpreter's functional core: `State`, the
  fold, and the pure decisions — `RouteSuccess`, `EndPhase`, `ChooseEdge`,
  `StageCondition`, `NextStep`, `BudgetExceeded` — table-tested without a
  harness. It imports only value packages and reads no clock (archtest).
  The run loop, `awaitAgent`, the stage executors and the action executors
  stay in the engine.
- **Guard rails.** `internal/archtest` turns the package map into a test.
  Golden event logs (`internal/engine/testdata/golden/`) record every
  scenario's event types; a commit that changes one names the fix.

Recovery bugs fixed (each with a crash test shown failing on its parent):

- **D1** — a crash between an agent stage's `stage_completed` and its
  routing: recovery skipped `verdict_received` and the budget check, so a
  run over budget completed. Live and recovered completions share one path.
- **D8** — a gate answered while its phase was parked lost the answer on
  `phase_resumed`; recovery asked the question again.
- **D9** — a wait recovered in its resume window started its timeout over.
- **D10** — a run parked on a quiet take-over holder restarted (and
  re-parked) on every boot.
- Found by the fold check: an agent's `agent_session` could be dropped when
  the harness's result arrived before its session event, so a crash would
  start the agent over instead of resuming it.
- Also found on the way: the SSH door's reply/close race, and an Ask pane
  that never recovered from a lost session.

Behaviour that changed on purpose: a refused merge stays pending, so the
retry reads the merge request's status before merging again (one fewer
`action_started`); a parked time and a hold's start are their events' own
timestamps; an edge to `end`/`done` clears the feed live as replay did.

Left as they were, recorded: an answer sent during a command or action
stage waits up to 30 s and reports busy (R1); the Ask service's `cancel` is
never called (R2); a crash between `mr_merged` and `stage_completed` can
record a second `mr_merged` (R3).

Follow-ups, the same day, after the plan closed:

- **The rest of the interpreter's decisions are pure.** `runcore` now plans
  the idle tick (`PlanIdle`, with `PlanResume`), classifies how an attempt
  ended (`ClassifyAttempt`) and holds the take-over tables (`PlanHoldRequest`,
  `HoldLeg`). A plan accounts for what its earlier actions will have done: a
  decline silences its occurrence, an ask counts for the same-tick dedup and
  keeps the run alive. `awaitAgent`'s select loop stays imperative.
- **A crash at every event** (`internal/engine/crash_test.go`). Five scenarios
  run clean, then once per event with the process dying right after writing
  it: a store that refuses everything from there on, then a fresh engine over
  the same log and the same fake world, so what the dead process did survives
  it, 153 crash points in all. It found that a crash between `stage_started`
  and an attempt's first prompt resumed the session the LAST attempt left,
  dropping the retry's prompt and its feed, which could abandon the run. A
  session now belongs to one attempt (`stage_started` forgets it), an attempt
  that never reached its agent starts fresh under its recorded iteration, and
  a re-executed command clears its outputs.
- **A verb landing while a run parks acts on the parked run.** Between the
  decision to park and the loop being gone, a pause or an abort returned
  success and did nothing, and a release or a message was refused. The loop
  stops taking deliveries when it decides to park (`parking`) and reads its
  cancel cause once, on leaving, under the lock `cancelLoop` takes; the others
  wait in `liveLoop` and take the dormant path. Its test also caught two
  writes to the fold's state with no event behind them.
- **`runloop.go` split by stage type** (`stage_*.go`, `invocation.go`,
  `provision.go`, the take-over in `hold.go`); **`internal/runinput`** holds
  what starts a run, so `automation` no longer imports the engine; the
  adapters share `shell.Quote` and `shell.Tail`.

## One cursor (2026-10-31)

A run has one cursor, always: one chain at a time holds it, moving or parked
at a wait point, or it stands at the hub between chains. All of a run's work
lands on one branch, so nothing runs beside anything else. This overturns the
v4 rule that a gate or a wait frees the run for watchers
(`docs/flow-language.md` §1 is the normative text).

- **Parked is one wait point.** `State.Parked` was a map of chain snapshots
  saved at a park and restored at the resume. Nothing can overwrite a parked
  chain's fields anymore, so `PhaseRun` and the restore are gone.
  `ParkPoint{Stage, Ping, Since}` is all a park adds. A log where a watcher ran
  during a wait does not replay to a sane cursor; nothing is released, so no
  upgrader.
- **Outcomes override everything but a take-over.** An outcome is terminal, so
  it takes the cursor from a parked chain, a running stage, and a run with no
  loop (an agent's question, the thread checklist, the debug leaf, a pause).
  Nothing polled those, so `Engine.RunOutcomes` does, at the signal cadence, for
  the runs the idle tick cannot reach; `runcore.State.PlanOutcome` says what may
  be overridden. A running loop stops as a pause stops it (`outcomeFired` is the
  cancel cause), `exit` records the outcome's `phase_started`, and a fresh loop
  picks it up through recovery, so no second path runs a chain. A dormant run
  gets the same event plus `run_unpaused` (reason `outcome`) under `wakeMu`.
  `ChainMerged` keeps a chain that merged by itself from being cut off at
  random, and a live action stage finishes. A take-over landing between a
  pass's read and its cancel loses the race and ends on the record, as with an
  abort. An outcome that reads `run.ci.failure` gets no report when it starts
  on a dormant run.
- **Confirmations stay open at the hub.** A question is not a chain: blocking
  on one would let an unanswered CI question hold up every automatic review
  round. So an answer can still reach the loop during another chain's command,
  which is why R1 needed fixing rather than vanishing.
- **R1 is fixed.** It was a false error, not a wait: after 30 s `AnswerPing`
  said "not accepted — try again" while the answer sat in `h.answers` and
  landed later, so a retry with a different answer was refused and the first
  counted. The queue is now the acceptance: a person hears within
  `answerLanding`, and a second answer is refused. A crash before it lands
  loses it, and the question reopens with the run.
- **Left as it is:** R3, the second `mr_merged` a crash can record; only the
  timeline reads it. No routing events for now.
- **Gates are answered by people.** `auto_approve` and `auto_reject` are gone
  from the grammar, the engine and the flow editor. No built-in flow used them,
  and an outcome covers a merge or a close. What went with them: an approval on
  the repository host can no longer answer a gate.

## internal/ grouped (2026-10-31)

Fifty-eight flat folders under `internal/` became twelve, grouped by what a
package is about; the README has the map. Moves only: every folder name still
matches its package, so no identifier, OpenAPI name, event or golden changed.
Nothing merged, because a merge can create an import cycle and a move cannot.

`pgstore`, `browser`, `egressgw` and `kube` joined `adapters/`, so the rule on
who may import an adapter covers them. `platform/` may import only itself, and
`flow/` only itself. Earlier notes and `docs/clean-architecture-plan.md` keep
the paths of their day; so do the comments in the frozen SQL migrations.

## Nobody talks to an agent without taking it over (2026-10-31)

The whisper and the interrupt are gone. Ask (read-only, beside the run) and
take-over (the agent in your hands) cover what they were for, and an open input
beside a running agent invited a third, timing-dependent way in.
`Engine.SendMessage` refuses without a hold (`ErrNotHeld`, 409), including while
one is being released, and the run page shows only *Take over* until there is
one. `Interrupt` left `agentport`, the ACP runner, `message_queued`, the API and
the UI; an old log's `interrupt` field is ignored on decode. The runner still
cancels a turn when the engine's context ends (timeout, park, abort, outcome).

## One session per stage (2026-10-31)

An attempt sent back to a stage continues that stage's session instead of
opening a new one (TODO 44). It never worked that way: a fresh attempt always
called `Run`, and `resume` was only for a reply within an attempt.

`runcore.State.Sessions` now survives `stage_started`, so a stage keeps its
session for the run, whatever chain runs it. The prompt is the attempt's whole
prompt, not a short note: the inputs may have changed and it carries the feed.
`AgentInput.Attempt` tells a runner a Resume starts an attempt, and gives it the
stage's spec. `Resume` now sets the harness's mode when a session is reloaded;
retries reach that path, and codex would otherwise come back asking for
approvals. A Resume that cannot load the session (`ErrSessionGone`) falls back to
`Run`.

A crash after a continuing attempt's prompt was logged is the one new window:
nothing says the agent read it, so recovery sends it again instead of telling
it to carry on (`Continuing`). A stage with a session is not proof that this
attempt reached its agent, so `AttemptBegun` reads `Prompted` alone.

`reuse_session: false` on an agent stage opts out, for a stage that should look
afresh: `builder`'s adversarial `review`, whose verdict the next round should not
inherit. The fold drops the session at `stage_started`, so nothing else changes,
crash recovery included. It is on unless said, so `Stage.FreshSession` carries
the exception and the editor writes the key only to turn it off.

## File lists as a tree (TODO 47, 2026-11-01)

A run's Changes and a review's files (`review/FileList`: DocReview,
ReviewHistory, CurationPage) can show their files as a tree, from a List /
Tree switch (`app/FileLayoutSwitch`) in the pane's bar. The run's Files pane
was a tree already and is unchanged. The choice is one per-browser preference
(`prefs.fileLayout`, `apothecary.files.layout`) that every such list follows,
so a pane remounted on a repo switch keeps it. Tree is the default: only a
saved `list` shows the flat list.

`core/pathTree` turns paths into the tree: folders first, then files, in
code-unit order (git's, not the locale's); a folder holding only one folder
merges into one row, since a change list is sparse. Each pane keeps its folded
folders by path, so a Changes refresh keeps them. In tree mode Changes opens on
the first file the tree shows; a review still opens on the gate's first file
(`primary`, which old drafts hang off), and j/k (`ReviewFiles.step`) walk the
order shown (`ReviewFiles.order`), folded folders included. The narrow-width
picker (`FileList variant="select"`) keeps the gate's order.

## Tickets follow their tracker (TODO 49, 2026-11-01)

Built from docs/ticket-sync-plan.md, which has the why. A ticket used to be a
copy taken once ("Ticket sources", above); now **every ticket has exactly one
tracker** — its remote issue when it has a source, else its owner's internal
tracker — and every write goes there. What shipped, and where it differs from
the plan:

- **Data model** (migration `0021_ticket_tracker.sql`): per-integration
  `tracker_statuses` (name, remote category, position, `is_done`, `flow_step`
  — a step held by at most one status) and `tracker_types` (name,
  `change_type`); tickets gain `status_id`, `type_id`, `labels`, `remote_id`,
  `remote_updated_at`, `synced_at`, `sync_state` (`ok | error | gone`),
  `sync_error` and `status_changed_{at,by}`; `ticket_comments` (origin
  `remote | user | apothecary`, unique per remote id and per run+stage) and
  `ticket_links`; a unique index makes an issue one ticket per owner and
  tracker. `change_type` stays a column, derived from the type. A mirrored
  ticket refuses edits to title, goal, change type, status and labels through
  `PUT` (`confstore.RemoteOwnedError` → 409); the base branch stays editable.
- **Status and type lists.** Remote lists come from the new optional
  `trackerport.TicketMetadata` (Jira `/status` + `/issuetype`; GitHub
  open/closed and one type `issue`; GitLab opened/closed) or grow from the
  tickets a sync returns. A new status is auto-suggested
  (`confstore.EnsureRemoteStatuses`): category done ⇒ Done and the `done`
  step, category new ⇒ the `todo` step, each step only while free; a new
  type's change type is `GuessChangeType` of its name. On a remote tracker the
  user edits only Done, flow step and change type; on the internal one, the
  whole list (add, rename, reorder, delete — a status or type in use only with
  `replace_with`).
- **Jira's legacy `status_map` / `type_map`** stay in the integration's config
  through the migration (their rows need the tracker's ids). The first list
  refresh (`tickets.SyncLists`, `legacymaps.go`) applies them by name, case
  aside, after the fetched lists are stored: `status_map` (laid over the old
  defaults To Do / In Progress / In Review / Done; `step=` clears a step)
  gives each named status its flow step, taking it from the auto-suggested
  holder; `type_map` sets change types. Then the keys are dropped
  (`confstore.DropIntegrationConfig`, secrets untouched). An entry naming
  nothing on the tracker, or not parsing, is a warning — logged, and returned
  by `GET …/statuses?refresh=1` — and dropped all the same.
- **Flow steps replace canonical statuses.** `trackerport.TicketStatus` is
  `FlowStep`; `ticket_transition to: <step>` moves to whichever status holds
  the step, and a step no status holds skips the stage with a note naming the
  steps that have one. Adapters lost `TransitionTo` and their status maps.
- **The internal tracker** (`internal/project/tickets/internaltracker`) is a
  `TicketWriter` over the ticket row: one integration of kind `internal` per
  user (`confstore.KindInternal`), created on first use, seeded To do / In
  progress / In review / Done (done) / Blocked plus one type per change type,
  never created or deleted through the API. Transition by name
  (case-insensitive) or step; comments keyed by run+stage instead of trailer
  scanning; `labels` replace or append; any other field skips with a note
  (`ErrNotApplicable`); links upsert on URL. A ticket without a source is
  therefore no longer skipped by tracker stages — this narrows "Unlinked
  ticket ⇒ skip" from TODO 27.
- **Sync** (`tickets.Syncer`, single-flight per ticket). Triggers: the
  daemon's poll (`cmd/apothecaryd/background.go`, at boot then every 5 min)
  over `ListSyncable` — sourced, **not in a Done status**, not gone; a manual
  refresh of a ticket or of an integration (done ones included); after a
  ticket stage wrote to a remote tracker; before an agent stage that reads
  `run.ticket.comments`; and at run start. Done is a read-side filter: a done
  ticket reopened remotely is noticed only by a manual refresh or a run start.
  A snapshot older than `remote_updated_at` is dropped; a not-found key is
  retried by `remote_id` (a Jira move, a GitHub transfer) and otherwise the
  ticket is `gone` and polling stops. Engine refreshes are best-effort, 20 s
  each, and never fail a stage or refuse a run. A tracker refusing for its
  rate limit (`trackerport.ErrRateLimited`: a 429 on Jira, GitHub or GitLab,
  or GitHub's 403 with no requests remaining; the wait from `Retry-After` —
  seconds or an HTTP date — else the reset header) records nothing on the
  tickets: the pass stops reading, counts them `rate_limited`, and the poll
  skips the integration for that wait (the error backoff when none was
  given). A manual refresh that meets one sets the same hold.
- **Batched reads** (`trackerport.TicketSyncer.FetchMany`): Jira JQL
  `key in (…)` by 100, falling back to one `Fetch` per key when Jira rejects a
  batch naming a deleted key; GitHub one GraphQL query per repository with an
  aliased `issue(number:)` per key, by 100 (`/api/graphql` on Enterprise),
  mapped as the REST `Fetch` maps (the REST id is GraphQL's `databaseId`) —
  an unresolved number (deleted, transferred, a pull request) is left out and
  the sync fetches it alone; GitLab `iids[]=` by 100 per project.
- **Attachments follow too** (G14). An imported file keeps the tracker's id
  for it (`attachments.remote_id`, unique per ticket where non-empty; `''`
  for an upload), from the import on (`filestore.Attachments.SaveImported`).
  Every sync — a refresh by name and the poll's pass, which skips done
  tickets as for everything else — copies the files whose remote id the
  ticket does not hold yet, through the same code and so the same size cap
  and MIME allowlist as the import (`tickets.copyAttachments`), and claims
  them for the ticket; a held id is skipped, so a repeat sync copies nothing.
  A file the issue no longer lists is **kept**: runs pin attachment ids in
  `run_created`, and deleting the row would break them. A file with no
  remote id can't be matched, so only the import copies it. A ticket
  imported before remote ids were kept holds its files with `remote_id ''`:
  before downloading, the sync **adopts** one of them instead
  (`filestore.Attachments.AdoptRemote`, which sets the id under the unique
  index) — the oldest of the same stored name (case-sensitive) and the
  tracker's size, or, when the tracker gives no size, the only one of that
  name while only one new remote file has it; anything ambiguous is copied.
  A failed download, store or claim sets `sync_state=error` with the files
  named, but the mirror and the comments are written all the same; the sync
  is a `tickets.PartialSyncError`, which a pass counts `failed` (not
  `synced`), `Refresh` returns, and `POST /api/tickets/{id}/sync` answers
  with 200 and the ticket showing the error. The next pass retries the file,
  its id still not held (the poll does not back off for it); a file the
  upload policy refuses is only logged (the import already warned) and
  retried each pass too, so a raised limit takes it. No extra read: every
  `FetchMany` carries attachment metadata as `Fetch` does (the
  `TicketSyncer` contract — Jira asks for the `attachment` field, GitLab's
  list endpoint carries the description its uploads are parsed from, GitHub
  issues have no attachments).
- **One active run per ticket** (`engine.ErrTicketBusy`, checked in
  `CreateRunWith` before preflight under a per-ticket lock held until the run
  is queued). HTTP answers 409 with the busy run's `run_id` (ticket runs,
  merge-request runs, rerun); a cron automation slot on a busy ticket is
  marked fired with `last_error`, no catch-up. Runs without a ticket are
  exempt. Rerun starts from the ticket as it is now (`engine.Deps.Tickets`),
  falling back to the old run's copy when the ticket was deleted.
- **Deleting a ticket source** with linked tickets is 409 (`tickets`,
  `active_runs`) unless `?unlink_tickets=1`: then, in one transaction
  (`confstore.DeleteIntegrationUnlinking`), each ticket's source and remote
  fields are cleared and it moves to the internal tracker — the status of the
  same name (case aside), else the done step's if it was Done, else the todo
  step's; the type by change type; comments kept.
- **Prompts**: a new input `run.ticket.comments` (type `ticket_comments`,
  agents only; `limit` 1–200, default 20; `exclude_own`, default true, drops
  origin `apothecary` and remote comments carrying the engine's trailer).
  Rendered as `# TICKET DISCUSSION`, oldest first, every line quoted with
  `> `, marked untrusted, capped at 32 KiB (`flow.CommentsBudget`) by
  dropping the oldest. `# TASK` gains the ticket's key, status and labels.
- **API**: `POST /api/tickets/{id}/sync`, `/status`, `PUT …/labels`,
  `GET`/`POST …/comments`, `GET /api/tickets?done=`; `POST
  /api/integrations/{id}/sync`, `GET`/`PUT …/statuses` and `…/types`
  (`?refresh=1`).

### Where the build differs from the plan

- **GitLab types are the `issue_type` list** (issue, incident, task,
  test_case), not one `issue` type: collapsing them would lose
  incident → hotfix.
- **The refresh after a ticket stage runs only for a remote tracker**
  (`trackerTarget.remote`); the internal tracker wrote the row itself.
- **Backoff**: the poll leaves an integration alone for 15 minutes
  (`tickets.DefaultErrorBackoff`) after a failed pass, and **every** failed
  read — not only 401/403 — sets `sync_state=error` (a rate limit excepted,
  above). A manual refresh is never held back.
- **The run-start refresh is inside the engine** (`CreateRunWith` →
  `freshRunTicket`), taking title, goal, change type and the issue's key/URL
  from the refreshed row.
- **GitHub batches through GraphQL**, not `issues?since=`: listing by date
  pages through every issue updated since, while aliases read exactly the
  numbers asked for.

## Harness conformance tests (2026-11-02)

`task test:integration:harness` (CI: the integration job's `harness` entry)
runs `go test -tags conformance -run Conformance ./internal/project/kinds/`
in a golang container with the tools bundle the engine expects —
`apothecary/tools:<ToolsHash>`, built as `wsimage.EnsureTools` builds it when
absent, seeded into the `apothecary-harness-tools` volume by the bundle's own
`seed.sh` and mounted at `/opt/apothecary/tools`. A missing or different
bundle (its `TOOLS_HASH`) fails the tests; they never skip. Each runs the
kinds' real `Prepare`/`SeedAuth`/`EnvVars` output through the binary:

- **omp** (`TestOmpConformance`): the broker face, as above.
- **opencode** (`TestOpencodeConformance`): `opencode debug config` accepts
  the generated config and agents (opencode refuses unknown keys); `auth
  list` keeps every auth.json entry and `models` lists each provider, none
  without auth.json; over ACP the agents are session modes, and a turn on a
  fake Anthropic carries the auth.json key and the selected agent's prompt.
  The read-only reviewer writes its outputs (a file, a nested file of its
  files directory) and is refused three other paths and an edit of
  `README.md`; the builder, the control, writes them all.
- **codex** (`TestCodexConformance`): `codex mcp get ask_human --json` shows
  the generated server whole; `codex login status` takes the rendered
  ChatGPT auth.json; over codex-acp an API-key session opens in the engine's
  mode and its turn reaches a fake OpenAI with the key. This test found that
  codex-acp 2.2.2 refuses a session with only `OPENAI_API_KEY` ("Authentication
  required": it signs in from env only under `DEFAULT_AUTH_REQUEST`, and the
  sidecar never calls `authenticate`) — the kind now sets
  `DEFAULT_AUTH_REQUEST={"methodId":"api-key"}` beside the key.

Limits, on purpose:

- **A ChatGPT login opens no session offline**: on `session/new` codex's
  app-server runs workspace routing discovery against chatgpt.com, which
  neither env nor `CODEX_CONFIG` redirects; `codex login status` is the
  closest honest check.
- codex ignores `OPENAI_BASE_URL`; the test reaches its fake through
  `CODEX_CONFIG={"openai_base_url": …}`, which codex-acp passes to the
  session. opencode reaches its fake through the repository's own
  `opencode.json` (`provider.anthropic.options.baseURL`).
- Only the glibc builds run (the golang image is Debian); the musl ones are
  untested here.
- **codex and omp enforce `read_only` by instruction only** for a stage's
  agent: codex runs every stage session in `agent-full-access` and omp with
  its full tool set; only the Ask pane's session is read-only (codex's
  `read-only` mode, omp's `--tools=read,grep,glob,web_search` process). The
  role text says "do not modify … except the output artifacts"
  (`acpagent.RoleText`). Neither harness offers per-path write rules.
- The bundle lacks `codex-code-mode-host`: every codex turn warns "Code
  Mode is unavailable … will fail closed".

## Observability and run analytics (TODO 23, 2026-11-02)

Built from docs/observability-analytics-plan.md, which has the why. Two
products: Grafana over OTel signals for the operator, `/analytics` over
per-user fact tables for everyone. What shipped, and where it differs from
the plan:

- **Failed attempts keep their spend.** `stage_failed` carries `turns_used`
  and `usage`; budgets now count it, so a retry loop stops at
  `max_cost_usd`. runcore's `Cost`/`Turns`/`Unwanted` are gone; the numbers
  live in `run.View.Facts`, folded by `run.Apply`.
- **Facts fold** (`internal/run/facts.go`). Time only advances on
  fact-bearing events: letting service-health events split intervals made the
  live fold and a replay disagree by a rounded millisecond. Times are UTC to
  the microsecond (pgstore `Append` truncates), empty maps are nil and open
  attempts close in sorted stage order — all so a JSON round-trip of the view
  folds to the same facts as `Reduce`.
- **Projections** (migration 0022): `run_facts`, `stage_attempts`,
  `run_skillsets`, written in the append transaction through one `pgx.Batch`.
  Text written there is UTF-8-cleaned (`textArgs`). A migration rebuilds the
  projections only when its first lines say `-- rebuild: projections`.
- **Rebuild speed**: 1.14M events / 10k runs in 9.3s, against 57.1s for
  00b9682 on the same data. Two changes beyond the batch: a decoder goroutine
  streams runs to the projector, and ending a run withdraws its open pings by
  `ping_id` (the `status`-indexed update grew quadratically inside the one
  rebuild transaction).
- **Analytics** (`internal/analytics`, no module imports): a whitelisted
  catalog compiled to SQL with `user_id = $1` in every source, run read-only
  under a 5s statement timeout. Beyond the plan: zero-measure queries (`n` is
  the value); a bucketed result's first dim is `bucket`; `order.by` takes
  `bucket` and 0-based `m<i>`; queries and panels may use built-in or own
  scorecards only (someone else's is a 404); dashboard saves fill missing
  panel ids.
- **CPUI**: the runs list reads `run.facts` (no per-run event fetch). The
  filter bar is a multi-select popover (`FilterPick`), since `Select` picks
  one value; bar charts also drill from the tooltip layer, which takes the
  click; line and scatter drill from the panel menu. Heatmap and box plot
  are layerchart primitives.
- **Telemetry**: standard `OTEL_*` env (autoexport); otel at v1.47.0,
  contrib at v0.70.0. **A trace per stage attempt, not per run** (deviation
  from the plan's one run trace): a run parked on humans for days made one
  unbounded trace, exported only at its end. The run is an instant `run`
  span at `run_created` (trace ID `sha256(run_id)[:16]`); each attempt roots
  its own trace (`sha256(run_id\0stage\0iteration)`: `[:16]` trace, `[16:24]`
  span) linked to it, and `exec.attempt` runs the attempt's step with
  `x.ctx`/`x.bg` on that trace, so its agent invocation, queries, outbound
  calls and log lines land there. `workspace.provision` is a trace of its
  own, linked to the run. Between attempts the loop carries no span; pgx
  traces a statement only under a span, so that work (and background polls)
  makes no one-query traces. Find a run's attempts in Tempo with
  `{span.apothecary.run_id="<id>"}`. `logs.New` drops a context key the call
  site also sets. `telemetry.IDGenerator()` is exported for tests that need
  forced IDs. Outbound clients go through `telemetry.HTTPTransport` (otelhttp)
  except the two harness probes that use `http.DefaultClient` (their tests
  stub its transport).
- **No stored value reaches telemetry or logs**, ciphertext included:
  - DB spans never record arguments; `db.query.text` has every string
    literal blanked (`literalFree`), and a failure is only its SQLSTATE
    (`error.type`, `db.response.status_code`): Postgres messages quote the
    offending input, and pgx's encode errors print the argument (`%#v`).
  - Log lines (stderr and OTLP) pass every error attribute through
    `pgstore.RedactError` (a `logs.ErrorRedactor` wired in `cmdServe`): a
    driver error keeps its SQLSTATE, table, column and constraint, an encode
    error its argument index. Errors flattened to a plain string attribute
    are out of its reach.
  - Outbound spans' `url.full` keeps scheme, host and path with query values
    `REDACTED`; Slack and Discord use `telemetry.SecretURLTransport` (host
    only), since a webhook's path is its token — otelhttp alone recorded it.
- **Health**: `/healthz` and `/readyz` are the route table's only paths
  outside `/api/`. `/readyz` is public, so its `db` answer says only
  `database unreachable` or `database timeout`; the driver's error (DSN user,
  host, port) goes to the log at Warn.
- **Stack**: `observability/docker-compose.yaml` is one pinned
  `grafana/otel-lgtm`; six provisioned dashboards (`platform`, `engine`,
  `llm`, `humans`, `usage`, `runs`), datasources by variable. Verified live:
  every panel query of all six returned rows. Helm renders `OTEL_*` only
  when `otel.endpoint` is set.
- **Known gaps**: the usage gauge's `budget` feature counts every flow (each
  gets a default `max_cost_usd`); outbound-HTTP child spans were not seen
  live (`-dev` kinds make no HTTP calls).

## System notices (TODO 51, 2026-11-02)

Problems with apothecary itself that a person must act on, as opposed to run
pings (run-scoped, unchanged). Before this, these only logged or set a
status nobody watched (a disabled credential, a ticket's `sync_error`).

- **Domain** (`internal/notices`): a `Notice` belongs to one user and stays
  until that user dismisses it (manual dismiss only). A non-empty key
  deduplicates while uncleared: a repeat bumps `count` and `last_seen_at` and
  takes the new title/body/link. A clear re-arms the key and never hides
  anything; a dismissed, uncleared notice keeps absorbing repeats, so
  dismissing a persistent failure silences it until it clears and breaks
  again. `Service.Raise` never fails its caller (store errors log) and fans
  out to owners and/or admins (`Admin && !Disabled()`, an empty owner means
  the admins), each user once. A *new* notice is mirrored to the recipient's
  channels asynchronously, 30 s per send; `channel_failing` never is (it is
  about the mirror). `catalog.go` holds the exact wording of every kind.
- **Store**: migration `0023_notices.sql`; the dedupe is one
  `INSERT … ON CONFLICT` on a partial unique index over `(user_id, key)`
  where uncleared, `xmax = 0` telling insert from update. Rows go with their
  user; nothing prunes them. `noticestest` is the contract both stores pass.
- **Producers** (each a small interface in its own package, wired in
  `cmd/apothecaryd/wire.go`): the login keeper (refused for good → sign in
  again; failed and expired → expired; refreshed clears both); the ticket
  poll (`Syncer.RunOnce` only: an unreachable tracker or unresolved
  integration raises, a pass that synced ≥1 ticket clears; rate limits do
  neither); `confstore.EnsureRemoteStatuses/Types` (every inserted row,
  first fill included, one aggregated notice per call, no key); every
  channel send (`channels.Registry.Send`); the docker host's tools-bundle
  warm-up and browser-image build; Kubernetes shim/tools hash drift (owner
  and admins; a failed read says nothing); the SSH door stopping; the reaper's
  workspace destroys (`Engine.ReapOnce`) and storage clean-up
  (`background.go`).
- **Deliberately not notices**: environment capacity (keeps its channel-only
  blocker), the automation blocker and "cannot reach the repository" (project
  setup tier), fatal boot errors, tracker rate limits, synchronous user
  actions, run failures.
- **API and UI**: `GET /api/notices`, `POST /api/notices/{id}/dismiss`
  (`ScopeNotice`), `POST /api/notices/dismiss`. The CPUI's Notices page
  (`/notices`, after Inbox in the nav, with a count badge) lists them as
  banners with an Open action when the notice links somewhere. Titles and
  bodies are server-side English, like ping titles.

## The daemon door: docker in workspaces (2026-11-09)

Why: a repo whose tests need a docker daemon — this one (`pgtest` on
testcontainers-go, Ryuk included), any `docker compose` from an agent — had
no way to reach one inside a run. Compose siblings are started *from outside*
by `devcontainer up` (engine-side, `DOCKER_HOST` at the run's parity socket);
the workspace container itself held no socket, and the standard
`docker-outside-of-docker` feature failed late and cryptically on its bind
("source path does not exist").

- **Mechanism.** The sandbox's dockerd gets a second `--host`, its default
  `unix:///var/run/docker.sock` (`sandbox.go`). The dind image is Alpine
  (`/var/run → /run`), so the listener lands at `/run/docker.sock` and the
  feature's bind source resolves through the symlink. Explicit `dockerd` as
  arg1 keeps the entrypoint from adding its tcp listener. Kubernetes pods
  always had the default socket (`apo-dockerd`).
- **The setting.** `APOTHECARY_WORKSPACE_DOCKER=auto|on|off`
  (`config/env.go` → `integrations.New` → `kinds.ProvisionerDeps` → adapter
  `Config`, beside the runtime). `Adapter.doorOpen`: `on`/`off` are the
  operator's word; `auto` (default) is `Host.unprivileged()` — the runtime on
  docker, a runtime class with privileged off on Kubernetes. Why `auto` ties
  to privilege: under `--privileged` the sandbox daemon can start a
  privileged container, i.e. host root, so that hand-over is the operator's
  explicit call (a laptop running its own repos: fine; a shared server:
  sysbox); under a userns runtime the sandbox is the boundary already and its
  daemon adds nothing the run does not hold. `on` under privileged logs one
  boot WARN. A bad value refuses to boot.
- **Shape.** `apothecary.sandbox.shape=2` closed, `3` open — the shape follows
  `doorOpen`, so opening the door (by setting or by runtime) recreates an
  existing sandbox on its next Rebuild/recovery, and a closed-door sandbox is
  byte-identical to before. Side effect: flipping the runtime under `auto`
  now recreates too (before, the shape was constant and a sandbox could
  outlive a runtime change).
- **The refusal.** `Adapter.checkDaemonDoor`, in `provision()`'s detect
  callback — as soon as the checkout exists, before shim, tools, gateway and
  sandbox on docker; after the pod and clone on Kubernetes (the checkout
  lives there). `devcontainerDef.SandboxDaemonUse` reads the raw JSONC (no
  CLI call) and any referenced compose file: the feature (any registry, any
  tag, the old `docker-from-docker` name), a `mounts` or `runArgs` bind whose
  *source* is `/var/run/docker.sock` or `/run/docker.sock`, a compose volume
  of it. Deliberately never `privileged`, `runArgs --privileged` or
  `docker-in-docker` (the documented verbatim stance), nor a bind of some
  other source. Best-effort: a miss costs docker's own late error, never a
  false refusal. The message names what asked and both ways out; with `off`
  it says the setting is off. One policy on both hosts: a privileged pod under
  `auto` refuses too, although its socket physically exists (residual below).
- **The project's setting.** `WorkspaceConfig.Docker` ("Docker in
  workspaces", Workshop → Settings → Workspace) → `Resolution.WorkspaceDocker`
  → `EnvSpec.Docker` (the three builders: provision, rebuild, envcheck). In
  `provision()`'s detect callback it is an ask like the repo's declaration —
  the same refusal when the door is closed, naming the setting — and, when
  the repo declares nothing itself, `upOptions.docker` makes `Up` pass
  `--additional-features {"<pinned docker-outside-of-docker>":{}}`: the CLI's
  own way to extend a build without touching the repo's files, and the same
  feature the repo would declare for VS Code (install, socket proxy, remote
  user permissions all handled by it; image, Dockerfile and compose configs
  alike; both hosts). Never injected on top of a repo's own declaration (the
  feature would install twice). Chosen over a sandbox-wide default (every run
  would get a daemon it did not ask for — under `on`, every run host-root-
  capable) and over shipping a docker CLI in the tools bundle (a second
  mechanism, socket permissions for arbitrary users, a bundle rebuild).
- **Non-root remote users (found on the way).** `ensureSrc` clones as the
  engine's uid (root in the image) and bind-mounts; the CLI's
  `updateRemoteUserUID` does nothing when the CLI runs as root and never for
  compose; `rootInit` chowned only `/apothecary`. So any `remoteUser` other
  than root met a root-owned tree at its own hooks and at bootstrap's `git
  switch`. Now `up` runs with `--skip-post-create --skip-post-attach`,
  `rootInit` does `chown -R <user>: <workspaceFolder>`, then
  `devcontainerDef.RunUserCommands` (`devcontainer run-user-commands`, the
  CLI's own marker files keep once-per-container semantics) runs the hooks.
  Same place in the sequence as before — before bootstrap — on an owned tree.
  Host-side git is only the clone, so the chown changes nothing engine-side.
- **Testcontainers notes.** Inside the workspace `DOCKER_HOST` is unset and
  the feature's socat serves `/var/run/docker.sock`; testcontainers-go
  (v0.44) detects it runs in a container and uses the gateway IP of the
  daemon's default network for published ports — the sandbox bridge, reachable
  from the workspace; Ryuk binds `/var/run/docker.sock`, which now exists in
  the sandbox. No `TESTCONTAINERS_*` override. Containers it starts are
  siblings of the workspace on the run's daemon: destroyed with the run,
  egressing through the gateway. The feature's CLI install dials
  `download.docker.com` (`moby: false`, under `*.docker.com`; listed
  explicitly in the egress defaults now) or `packages.microsoft.com`.
- **Residuals.** (a) The parity socket `<RunsDir>/<run>/docker.sock` is
  bind-mountable by a `mounts` entry using `${localWorkspaceFolder}/../docker.sock`
  on any runtime — pre-existing, unchanged, and under privileged equal to what
  `runArgs --privileged` already grants. (b) Privileged Kubernetes pods keep
  the default socket whatever the setting (`apo-dockerd`): the refusal is
  policy there, not a physical barrier, until a relocation (an
  `APO_DOCKER_SOCK` env for `apo-dockerd` + `DOCKER_HOST` in the sandbox
  container's env; needs a sandbox-image version gate and a publish).
  (c) A plain socket mount without the feature yields a `root:docker 0660`
  socket; a non-root `remoteUser` needs the feature's socat proxy or a
  matching gid — the repo's concern. (d) Engine preflight does not see the
  door: refusing at run creation would need a `RepoHost` content read (three
  implementations), the door state through `resolution.Resolution`, the
  scanner in a shared package and `PreflightProject`; the adapter refusal
  already fires before anything expensive, so deferred.
- **Live.** `TestLiveLifecycle` asserts the socket exists iff the door is
  open; `TestLiveDaemonDoor` provisions a docker-outside-of-docker repo and
  drives the daemon (nested socket mount, as Ryuk does; under sysbox the
  shared tools cache stays read-only to its containers) or asserts the
  refusal; `TestLiveNonRootUser` the ownership hand-over.
  `test:integration:docker` forwards `APOTHECARY_SANDBOX_RUNTIME` and
  `APOTHECARY_WORKSPACE_DOCKER` into the test container — note the
  Taskfile's `dotenv` also applies `deploy/.env`, so "unset" through the
  task means "whatever .env says"; the privileged cells below were run with
  the variables forced empty on the `docker run` directly. Verified live on
  2026-11-09 (Arch, kernel 7.2, docker 29.9.1 host / 29.6.2 inner, sysbox-ce
  0.7.1, gateway on): sysbox + auto → posture `false sysbox-runc`, socket
  present, DooD repo drives the daemon (nested socket mount, `door-ok`), the
  tools cache mount seen as `ext4 ro,relatime,idmapped` with remount and
  write both refused; privileged + auto → posture `true runc`, no socket,
  the DooD repo refused before anything existed; privileged + on → socket
  present, daemon driven; `TestLiveNonRootUser` green in every cell;
  `TestLiveProjectDocker` (sysbox + auto): a repo declaring nothing, the
  project's setting on → the feature injected, `docker run` through the door.

## Dogfooding: the repo's own devcontainer (2026-11-09)

Apothecary now runs on its own repository (README, "Developing Apothecary
with Apothecary"). What the repo gained:

- `AGENTS.md` (Preflight requires it) — the working guide for an agent on
  this repo: what to run, what never to run in a workspace, the hard rules.
  `lint:models` scans it too. (No `CLAUDE.md` alias: deliberately, AGENTS.md
  is the one file.)
- `.devcontainer/`: compose-based — `app` built from
  `.devcontainer/Dockerfile` (`golang:<go.mod>-bookworm`, Node from the
  official image by `COPY --from`, helm from `get.helm.sh` with its published
  checksum, go-task by `go install`, a non-root `dev`, the Go module cache
  pre-warmed from `go.mod`/`go.sum` so the gateway's build cache makes a run's
  first build fast) beside a `postgres:16` `db`; no docker feature of its own
  — in a run the project's "Docker in workspaces" setting injects it (a guard
  test keeps it that way, so it is never installed twice), in VS Code a dev
  adds the feature or runs the pg suite on the host. It mounts `..` relative
  to `.devcontainer/` — the
  checkout, and never `../..`, which in a run is the run folder with its
  sockets; the CLI does not expand `${localWorkspaceFolder}` in compose
  files (docker compose sees an empty variable), so the relative path is the
  one spelling that works everywhere. `cpui/node_modules` is a named volume
  (the directory pre-created in the image, owned by `dev`): the container's
  `npm ci` must not delete and rebuild the host's — the first local run did
  exactly that under a host-side vitest and broke both — and a mac's native
  binaries would not run in the container anyway. `init: true` and a tmpfs
  `/tmp` for the same reasons as `task test:linux`. Hooks: `go mod
  download`, `npm ci` (which installs the lefthook commit hook).
- `Taskfile.yml`: `DEV_DSN` honors `APOTHECARY_DEV_DSN` (compose sets it to
  the `db` sibling; `dev:db` is then skipped); `dev:app` (built UI + daemon
  on :18080), `dev:key` (a master key into the gitignored `deploy/.env` when
  missing), `dev:up` (both, in a fresh task process so the dotenv just
  written is read) — the project's `default` service.
- Guards: `internal/devtools/devcontainer` pins the Dockerfile's Go, Node and
  helm to go.mod, `deploy/Dockerfile` and CI, checks the compose mount, the
  feature and that AGENTS.md exists; `pgtest`'s image test covers the compose
  `db`.
- Egress for the project: the offered defaults plus `get.helm.sh`.

## Sysbox pods get a user namespace of their own (2026-11-09)

Found deploying the Kubernetes provisioner under sysbox (runtime class,
privileged off — the posture `APOTHECARY_WORKSPACE_DOCKER=auto` opens the
daemon door for) on a k3s cluster with containerd 2 and Ubuntu nodes: the
sandbox pod never started, `ip_unprivileged_port_start: permission denied`.
The cluster-side workaround was an admission policy adding `hostUsers:
false` to every `sysbox-runc` pod; the fix is for the engine to say so.

- `podManifest` sets `hostUsers: false` whenever `unprivileged()` — the
  predicate that already drops the privileged security contexts and opens
  the daemon door. `kube.PodSpec.HostUsers *bool` carries it (a pointer:
  `false` must reach the wire through `omitempty`). Privileged pods leave it
  unset: they are host root regardless, and a user namespace there is
  nothing the operator asked for.
- Why: sysbox's documented pod shape on Kubernetes is `runtimeClassName` +
  `hostUsers: false` (K8s v1.30+ with containerd v2.0.5+ or CRI-O; sysbox
  0.7.0 added the `sysbox-runc features` command containerd needs before it
  admits the handler for user namespaces). Left on the host's user
  namespace, sysbox makes one for the container itself, but by then
  containerd has created the pod's network namespace, owned by the host's
  user namespace, and the sandbox sysctls it writes
  (`net.ipv4.ip_unprivileged_port_start`) fail from a user namespace that
  does not own it. With `hostUsers: false` the kubelet makes the user
  namespace first and sysbox adopts its mapping.
- Requirements recorded in the README (cluster requirements): containerd ≥
  2.0.5, sysbox ≥ 0.7 (an older `sysbox-runc` is rejected: `RuntimeClass
  handler "sysbox-runc" does not support user namespaces`), kernel ≥ 6.3,
  idmapped mounts on the storage class. And the Ubuntu 24.04+ AppArmor
  restriction on unprivileged user-namespace creation
  (`kernel.apparmor_restrict_unprivileged_userns`): sysbox and privileged
  pods are host root and unaffected; a tool inside an unprivileged sandbox
  that builds a user namespace (bubblewrap, Chrome, rootless podman) is
  denied until the node lifts the sysctl or confines it with a `userns`
  profile. And, found by the same deployment, Ubuntu 25.04+'s
  `fusermount3` profile, which denies sysbox-fs its FUSE mounts under
  `/var/lib/sysboxfs` so no sandbox starts (nestybox/sysbox#947, #1005,
  open against sysbox 0.7.0 on 26.04): the README carries the maintainers'
  three-rule local override. Both field helps (`privileged`,
  `runtime_class`) point at the README.
- Not changed: the docker provisioner (`APOTHECARY_SANDBOX_RUNTIME`) —
  docker hands sysbox no user namespace and sysbox makes its own, the
  live-verified path. The AppArmor point applies to Ubuntu docker hosts too.
- **`/sys` is the host's.** The same deployment's next failure: the `wg`
  init container crash-looped on `cat /sys/class/net/eth0/mtu`. Under
  sysbox with `hostUsers: false` the container's `/sys` is the host's sysfs
  — same device number, the node's interfaces (enp*, docker0, cni0, veths),
  no eth0 of the pod — while netlink (`ip link`) shows the pod's own `lo`
  and `eth0`. `apo-wg-up` now reads the MTU from `ip -o link show dev eth0`
  and fails loudly when there is none; it was the only `/sys` read in the
  sandbox scripts (the gateway's `sysClassNet` runs in the gateway
  container, never under the runtime class). Residual for the security
  model: a sysbox sandbox on Kubernetes can *read* the node's sysfs
  (interface names, hardware details; writes are refused, root there being
  an unprivileged host uid) — sysbox/containerd behaviour, worth an upstream
  report, and far less than a privileged sandbox exposes.
- The 2026-10-27 residual ("sysbox on Kubernetes designed but not
  live-verified") stands for the engine's own live test; the pod shape is now
  the one a real cluster accepted.

## The checkout is cloned at the run's source branch (2026-11-09)

- Found by a run meant to fix a repo's devcontainer: it built the broken one.
  Provision cloned with a plain `git clone` (the remote's default branch) on
  both hosts (`host_k8s.go ensureCheckout` inside the pod, `devcontainer.go
  ensureSrc` engine-side), detected and built the devcontainer from that
  checkout and ran its lifecycle hooks (`RunUserCommands`, still inside
  Provision), and only then did bootstrap's branch step `git switch -c <run>
  origin/<source>`. The source branch the engine already computed for
  bootstrap (`bootstrap.SourceBranchOr`) never reached the provisioner: the
  `EnvSpec` had no field for it (its `Branch`, the run branch, is read by no
  adapter — it does not exist on the remote when the clone happens).
- Now `EnvSpec.SourceBranch`, set by provision and rebuild from the same
  value bootstrap gets (the environment self-check sets the project's
  default branch, which its bootstrap forks from), and both clone sites pass `--branch <source>` when it
  is set — without `--single-branch`, so `origin/<source>` and the default
  branch stay available to bootstrap (`git switch -c <run> origin/<source>`
  is unchanged) and to the prompt's diffs. A source branch missing on the
  remote fails the clone, loudly, instead of silently building the default.
  The name was checked at run creation (`CheckBranch` on the ticket's source
  branch; a merge-request run's source is the target branch the host
  reported; the fallback is the repository's default branch), and `--`
  already preceded the URL and the path; `--branch`'s own argument is taken
  verbatim by git's option parser.
- Rebuild was less of a gap than it looked: the checkout (with the agent's
  commits, on the run branch) survives a rebuild on both hosts and the
  container is rebuilt from it, so an agent's devcontainer edits already got
  built. Only the clone after a reap (checkout gone) fell back to the default
  branch; it now lands on the source branch like the first one. Cloning the
  pushed run branch instead, for a re-provisioned run, is not done: the
  working tree is gone with the workspace either way, and bootstrap's
  `git switch <run>` picks the pushed branch up when it exists.
- A run created on a merge request forks from `FETCH_HEAD` (the host's head
  ref); its clone lands on the run's source branch — the target — not the
  merge request's branch, whose devcontainer it therefore still does not
  build with. Called acceptable here; it was not (next section: the
  checkout is put on the run's branch before the build, in every case).

## CI's unit job enforces both coverage floors (2026-11-09)

- The CPUI floor (`cpui/vite.config.ts` thresholds, lines 99%) was enforced
  only by a local `task cover` / `task verify`; CI's unit job ran
  `task test:unit`, which has no coverage. Main sat at 98.96% lines without
  anyone seeing it (found while fixing the Notices page, TODO 51). The unit
  job now runs `task cover`: `cover:go` (every package, Postgres tests
  included, `GO_COVER_MIN`) then `npm run test:coverage`, and uploads both
  reports as a workflow artifact (`coverage`, 14 days). A floor only rises;
  a change that drops under one fails the pull request.
- Not `task verify`: lint and build have their own jobs, and the pre-commit
  gate repeating them in CI would only lengthen it (TODO 52). The Postgres
  tests now run twice in CI: `cover:go` runs every package with
  `TEST_POSTGRES=1`, so the `pg` integration suite (the pgtest packages,
  same flag) is a subset of it. It stays for now as the fast, isolated
  signal on the Postgres adapter; dropping it to shorten CI is TODO 52's
  call.
- Getting back over the CPUI floor took tests, not a lower number: the run
  header's Stop and Archive (each asks first, naming the workspace it keeps
  or destroys; archive's toast offers the ticket) and the retry label read
  off the log were never exercised, and some forty one-line calls in
  `api/index.ts` (the thin reads: run ports/ssh/services, the Ask pane, a
  workspace file/changes/diff, a ping's payload and curation, skills and
  skillsets by id, the account calls) had no test saying which endpoint they
  reach with which arguments. They do now, as one table.

## Four goldens froze a race; the engine's test log is silent (2026-11-09)

- The first CI run of `task cover` failed on
  `TestRerunTicketedRunKeepsBaseNewTail`: its golden ended run 2 at
  `phase_started`, the state the loop was in when the test returned and the
  harness cancelled it. Under `-covermode=atomic -coverpkg=./...` on a shared
  runner the loop reached `env_requested` first. Four goldens had the same
  shape (the two rerun tests, the ticket rerun, the title-derived branch): a
  `CreateRun`/`Rerun` read through `h.view` and returned without
  `h.e.Wait`, while their sibling in the same file carried the rule in a
  comment ("to its end: the golden log must not depend on when the test
  stopped"). Each now waits; the goldens were re-recorded with the second
  run complete. Five local passes with CI's flags never showed it — the
  stress task is the stand-in, as before.
- An audit of every golden's last event found 57 that end short of a
  terminal or parked state; all but these four are at rest by design (a run
  idle after `phase_completed` waiting on its watchers, a bootstrap that
  failed, a watcher's confirmation ping, a deliberate mid-flight shutdown).
  A mechanical rule ("every run ends terminal or parked") would be wrong for
  them, so this stays a review item: a test that reads a run it created or
  rerun waits for it first.
- The job log carried 1,600 lines of INFO/WARN/ERROR from the engine: it
  logs through `slog.Default()` when `Deps.Log` is unset, the harness leaves
  it unset, and `go test` prints a failing package's whole stderr. Credential
  scrubs, flow warnings and the crash tests' "the process died" are all
  expected there and say nothing about the failure. The engine's `TestMain`
  now installs a discarding default handler; `APOTHECARY_TEST_LOGS=1` brings
  the lines back when they are what one is after.

## The checkout is on the run's branch before the devcontainer build (2026-11-09)

- The previous section's `--branch <source>` fixed only the new-run case. A
  run created on a merge request works on an existing branch, the merge
  request's head: its run branch is that branch, its source branch the merge
  request's target. The clone still landed on the target and built the
  target's devcontainer; the merge request's branch arrived with bootstrap,
  after the build. Found by a run opened on a merge request that fixes the
  devcontainer: it failed building main's, before bootstrap ever ran (the
  checkout's reflog held only the clone).
- Now the provisioner runs bootstrap's own branch step on the checkout,
  right after the clone and before `devcontainer up`, so the checkout is on
  exactly the commit the run works on in every case: a merge request's run
  switches to its branch (or fetches the host's head ref, the only way to a
  fork's head), a rerun re-enters the pushed branch (`git switch <branch>`
  takes the remote's up), a new run forks `origin/<source>`. The clone picks
  no branch of its own any more. Bootstrap's step, unchanged, finds the
  branch and re-enters it.
- One implementation, in a new leaf `internal/workspace/checkout`
  (`BranchScript`, `CredentialLine`; archtest row, shell only), used by both
  hosts' clone steps and by bootstrap, which had its own copies — and so had
  the adapters, of the credential line. Moving it also moved the branch
  names from Go quoting (`%q`) to shell quoting.
- `EnvSpec` gains `Fetch` beside `Branch` and `SourceBranch` (the run's
  primary checkout's fetch ref, `exec.primaryFetch` in provision; the view's
  primary checkout in rebuild, which also now passes it to its bootstrap —
  it did not, so a rebuild whose checkout was reaped would have forked a
  merge request's run off its target). An empty `Branch` (a bare caller)
  leaves the clone where it lands.
- The credential now reaches every git command of the clone script, not
  only the clone: a merge request's head is fetched with it. Both hosts name
  the store file through git's environment config
  (`GIT_CONFIG_COUNT/KEY_0/VALUE_0`, git ≥ 2.31), never argv, never the
  checkout's config; the file is removed when the script ends, as before.
  The Docker host runs the clone and the branch step as one `sh -c` script
  (the same single runner call its tests queue a response for); the
  Kubernetes host appends the step to the bash script that already carried
  the credential.
- Tests: both adapters assert the exact script for a new run, a merge
  request's run and no branch (`TestEnsureSrcEntersTheRunBranch`,
  `TestK8sEnsureCheckout`); the engine asserts the spec the provisioner
  received for a merge request's run (`TestRunOnSomeonesMergeRequest`:
  branch, source, fetch) and for a ticket run (`TestRunSourceBranch`); the
  rebuild test asserts its spec and the view helpers. A check that the
  checkout's HEAD equals the merge request's head before the build needs a
  real remote: that is the docker integration suite, CI only.

## The Kubernetes checkout keeps the modes git records (2026-11-10)

- Every file of a Kubernetes run's checkout was 0600 or 0700: `ensureCheckout`
  (`host_k8s.go`) set `umask 077` for the credential file as a plain
  statement of its script, so it held for `git clone` and the branch step
  too. A compose sibling running as a non-root user (a seed container
  bind-mounting the repo's `entrypoint.sh`, recorded 100755) failed with
  "Permission denied". There since Kubernetes support; runs failed earlier
  before. The Docker host never had it: it writes the credential file with
  an explicit 0600 from Go, and git runs under the engine's umask.
- Now only the credential write runs under 077, in a subshell; git writes
  under the pod's umask. `TestK8sCheckoutKeepsTrackedModes` runs the
  generated script for real (local bash and git, a `file://localhost`
  remote, a `git` wrapper on PATH that records the credential file's mode
  mid-script): 0755 for the executable, 0644 for the rest, 0600 for the
  credential, which is gone afterwards. It failed with 0700/0600 before the
  fix.
- Existing checkouts stay as they were: the clone is skipped when one
  exists, git does not repair the modes of files it already wrote, and a
  rebuild keeps the checkout. An affected run needs its workspace destroyed
  and provisioned again, or a one-time chmod of the checkout.

## A failed provisioning records what it left behind (2026-11-10)

- Runs whose workspace failed to provision could never be cleaned up. Both
  hosts create the sandbox (Kubernetes: the pod, its PVC, two secrets, a
  NetworkPolicy; Docker: the sandbox container and its volume) and the
  checkout before `devcontainer up`; when a later step failed, `Provision`
  returned no id, `env_failed` carried none, and archive (`v.EnvID != ""`)
  destroyed nothing. Found with twelve archived runs whose sandboxes still
  ran their full compose stacks and exhausted the host's async I/O pool.
- The port's contract now says it: a failed `Provision` returns, with the
  error, the id of what it left behind, or "" when it created nothing. The
  devcontainer adapter returns the workspace name on every failure past the
  name (Destroy tolerates a name with nothing behind it: Kubernetes deletes
  with `--ignore-not-found`, Docker finds no sandbox and removes the
  folder). The engine records it rather than guessing it from `spec.Name`:
  the name is an adapter convention the fake provisioner does not follow.
- `env_failed` gains `partial`: the id names leftovers, never a ready
  workspace. The run view keeps it (archive destroys it, the capacity count
  sees it, the run page shows it for inspection until then); the run's own
  state ignores it, so a retry provisions afresh under the same name
  instead of running in a container that was never bootstrapped. The
  bootstrap and egress-probe failures keep recording a non-partial id, as
  before.
- The same leak had a second door: a run aborted mid-provisioning ended
  abandoned with nothing recorded. An abort now records the leftovers the
  same way (`provisionStopped`); a pause or a shutdown records nothing, since
  the run provisions again under the same name when it resumes and the
  provisioner picks them up.
- The reaper is unchanged: it destroys completed runs' workspaces only, and
  failed or abandoned runs are never auto-reaped (they are evidence).
  Archive is what reclaims them.
- Tests: every Kubernetes failure stage returns the name and destroying it
  deletes the pod, the claim, the policy and the secret; every Docker
  failure stage returns the name; the engine records a partial leftover and
  archive destroys it (`TestFailedProvisionLeftoversAreArchived`), likewise
  after an abort (`TestAbortDuringProvisioningRecordsTheLeftovers`); the
  run-state fold ignores a partial id and keeps a bootstrapped one.
- Runs that failed before this release have no id on record. Their
  workspaces are removed by hand (`kubectl delete pod,pvc,secret,networkpolicy`
  by the workspace name, or `docker rm -f` on the sandbox).

## A non-root remote user can use the shim dir (2026-11-10)

- With `remoteUser: node` (common: the Node, JavaScript and TypeScript
  images), the sidecar never started on Kubernetes: it runs as the remote
  user and creates `sidecar.sock` and `ssh.sock` in `/opt/apothecary`, which
  the shim-seed init container makes root's, 0755 ("bind: permission
  denied" in `/apothecary/sidecar.log`). No sidecar means no file tree,
  diff, terminal, Ask pane, editor or SSH door. The docker host has the same
  failure whenever the remote user's uid is not the engine's: the CLI
  remaps it only when it runs as a non-root user (never for the engine
  image, which runs as root) and never for compose. Dev from source hid it
  (engine uid 1000 = `node`).
- The reverse relay behind app port exposure (`app-<port>.sock`, same dir)
  was started with a bare `docker exec`, i.e. as the image's default user:
  an image ending in `USER node` could expose no port.
- `rootInit` (root, after `up`, before the lifecycle hooks) now gives a
  non-root remote user the shim dir's group, writable: `chgrp "$(id -g
  <user>)"` + `chmod g+w`, not recursive. The group, not the owner: the
  engine must keep writing there whatever uid it runs as (`engine.sock`
  after a restart, the shim on upgrade), and on the docker host the dir is
  its own run folder seen through the sandbox. The shim binary and the tools
  bundle keep their owners. The devcontainer spec has no group setting; the
  gid is the one the user's processes run with. The reverse relay now runs
  with `-u root` (it only pipes bytes; its socket is 0666).
- Checked by hand against real `node:22-bookworm-slim` containers, one with
  root and one with `USER node` as default user, the shim dir root-owned
  0755 as the seed leaves it, the real static shim and the exact script
  `rootInit` generates: the sidecar fails with "bind: permission denied"
  before `rootInit`; after it the sidecar starts as `node` on the first
  call, `/health` answers from the host side, `ssh.sock` is served, an app
  port answers through a root-run reverse relay, and the dir stays root's.
  Unit tests pin the script (group and mode, no owner change, no
  recursion, nothing for root) and the relay's `-u root`.
- Workspaces provisioned before this need a rebuild, or the two commands run
  by hand as root inside them.


## The workspace container is always one the CLI created (2026-11-10)

- A repository whose `initializeCommand` runs `docker compose … up -d` with
  the devcontainer's own service (so VS Code reuses it) failed to provision:
  `workspace init: … chgrp: cannot access '/opt/apothecary'`, then "Dev
  container not found" on every exec. For a compose configuration the
  devcontainer CLI looks its container up by compose project and service,
  not by our id-labels, and reuses whatever it finds as it is: no
  `/opt/apothecary`, no `/apothecary`, none of our labels. Not specific to
  Kubernetes, and not a regression.
- `initializeCommand` runs on every `up`, a recovery's too, and the CLI's
  compose override changes the service's config, so compose replaces our own
  container with the repository's. A check for our container before `up`
  would therefore only have covered the first provision.
- `Adapter.up` checks after `up` instead: the returned container must carry
  the workspace's id-label (`docker ps -aq --filter id=… --filter label=…`
  on the run's daemon). When it does not, one more `up` with
  `--remove-existing-container` replaces it: the CLI removes the container it
  found and creates the service's container with our mounts and labels, the
  sibling services left running. If that still hands back a foreign
  container (or the call was already a recreate), the provision fails saying
  the CLI reused a container it did not create and pointing at
  `initializeCommand`, before anything runs in it. The exec log says when a
  container was replaced.
- Checked against the real CLI (0.86.0) and docker with a compose
  devcontainer whose `initializeCommand` starts the service: a plain `up`
  returns a foreign container on the first provision and again on a later
  one; the retry returns ours with the shim mount each time, and the sibling
  service keeps running. Unit tests pin the retry, the refusal, no second
  recreate after a recreate, and the lookup's failures.

## Budgets pause; retry provisions; provisioning is open and legible (2026-11-10)

- **A spent budget pauses the run.** `checkBudget` no longer abandons: it
  returns a `budgetPaused` error the loop's exit turns into
  `run_paused{reason: budget, note: <the ceiling that tripped>}`. The run
  reads `paused` (view `pause_reason`/`pause_note`), its work and workspace
  stay, and play grants another budget's worth: `RunUnpaused` after a budget
  pause sets `Facts.BudgetCostBase/BudgetTurnsBase` to the spend at that
  play, and `Facts.BudgetSpend` is what the ceilings are held to. The fold
  derives the window, so no new event field and replay agrees with the live
  state. The `apothecary.budget.stops` metric counts budget pauses. What
  still abandons a run: abort, the flow's `abort` terminal, and a dead end in
  the main chain or an outcome (an absent or exhausted failure edge, giving
  up at the debug leaf or a manual stage, a missing MR body). Retry accepts
  all of them in the same workspace.
- **Retry after `env_failed` provisions again.** The view gets
  `provision_failed`. `RetryRun` accepts such a run whether or not a stage
  started or anything was left, and records `run_retried{provision: true}`.
  runcore then clears the env id and `EnvRequested` and leaves the cursor
  where the failure caught it, so the loop requests the workspace again
  under the same name (the provisioner picks up the leftovers) and
  bootstraps it. A failed bootstrap left a running container with no
  checkout, so re-attaching to it would have skipped the bootstrap. Rebuild
  stays what it was: recreating a workspace that exists.
- **Provisioning egress is open.** The gateway keeps `Routes.Open`
  (run → deadline), set by `PUT /runs/{run}/open` and cleared by
  `DELETE /runs/{run}/open` or a detach. An open run's :80/:443 traffic is
  judged by `verdictOpen`: everything but the project's blocked domains,
  and in deny mode its denylist. Its raw egress is accepted by an nft
  `ip saddr` rule placed after the protected-destination drops. The
  provisioner opens the window as soon as the run is attached (docker and
  Kubernetes), with the provision timeout plus a minute as the deadline,
  and `provision` closes it on every exit. The deadline, enforced by the
  gateway's watch loop, closes a window an engine killed mid-build never
  closed. Per run, never a project-mode flip: other runs of the project keep
  the policy. A window the gateway refuses is logged and provisioning goes on
  under the policy.
- **The provisioning log says where it is.** Every step line carries
  `[+mm:ss]` since provisioning began. New steps: the checkout's repository
  and branch (userinfo stripped), the definition, container preparation,
  connecting to the engine, waiting for the workspace, and on Kubernetes
  reusing, creating or replacing the pod and cloning into it. While waiting
  for the pod, its phase and each unready container's state
  (`ImagePullBackOff: …`, exits, restarts) are written whenever they change
  (`kube.ContainerStatus.State`). The log ends with `=== workspace … ready
  in …` or `=== provisioning failed after …: <cause> ===`.
- **Relay links name the UI's host.** A relay bound to every interface
  cannot know the machine's reachable name. `portrelay.ForHost` rewrites
  its `localhost` URLs to the host of the request that asks (expose, open
  editor, the ports list, service previews). A specific bind, host routing
  and a request on localhost are left alone. `SSHAdvertiseAddr` no longer
  refuses a wildcard door with nothing to advertise: it returns `:port`, and
  `GET /runs/{id}/ssh` fills in the request's host. Residual: those
  listeners are raw TCP without sign-in, as they always were on a wildcard
  bind. `APOTHECARY_APPS_DOMAIN` is the mode behind the session. code-server
  over plain HTTP on a non-localhost origin has no secure context, so its
  webviews need TLS in front.
- **Thinking** reads in the body face at the label size. A thought you open
  or close keeps that state, keyed by its segment, through new messages and
  through the live thought giving way to the recorded one. Thoughts you
  never touched still follow the stream.

## The owner's answers reach later stages: run.ping_answers (2026-11-10)

- Found live: the builder asked, the answer changed the plan, the builder
  followed the answer — and the reviewer, whose inputs were the ticket, the
  plan and the diff, rejected the build for deviating from the plan. The
  answers lived in the pings, where no later stage could read them.
- A new engine source, `run.ping_answers`, of a new value type,
  `ping_answers`, read from the run's log (`run.ReducePings`) when the
  prompt is built. Engine-side on purpose: it is the record of what the
  person said, which a builder-written "decisions" file could forget or
  misquote, and any flow's stage opts in with one port.
- What counts: every answered agent question; a gate, debug leaf or manual
  stage only when the person wrote feedback or review comments; never a
  phase confirmation or a selection (they decide what runs, not what is
  built). Each answer is rendered by `composeAnswerText`, the renderer the
  asking agent read it through, its attachments named (`answerFileRefs`,
  no delivery: the workspace has them). Quoted line by line, oldest first,
  capped at `flow.PingAnswersBudget` (32 KiB): the oldest go first, a long
  question gives way to its answer, a long answer is cut on a line.
- Unlike a ticket's comments the answers are the owner's direction, and the
  section says so: they amend the plan and other inputs where they differ, a
  later answer over an earlier one. Nothing answered still renders a line,
  so a prompt naming the input never meets a silence.
- The `builder` template wires it into build, review, redact and the
  review-fix round; the reviewer's prompt checks the plan "as the task
  owner's answers amend it". Take-over messages are not in it: the source
  is about pings, and a held session's words reach that session already.

## The build cache only goes on an image that is built (2026-11-10)

- Found live, right after the container-replacement fix: the CLI replaced
  the foreign container, then failed to create its own. With a gateway,
  `up` always carried `--cache-from` (twice) and `--cache-to`. The CLI
  writes `cache_from` into its compose override under the service's
  `build:`, and the repo's prebuilt compose file had reset that service's
  build (`build: !reset null`). So `cache_from` landed directly on the
  service, and compose refused the override
  (`services.devcontainer.cache_from false schema`). It had never shown up
  before because the CLI skips the build when it reuses a container, and
  the repo's `initializeCommand` always left one to reuse.
- `devcontainerDef.cacheBuilds`: a single-container devcontainer keeps the
  flags. A compose one keeps them only when its service has a `build:` in
  the merged configuration (`docker compose -f … config --format json`,
  which resolves overrides and `!reset` the way the build will). If the
  configuration cannot be read, or names no such service, the cache stays
  off: the flags only ever buy speed, and a wrong guess fails the build.
  The provisioning log says when and why the cache is off. An image-only
  service has nothing to cache anyway.

## Kubernetes only, one chart, tunnels and the apothecary client (2026-11-11)

The plan is `docs/kubernetes-only-plan.md`. What was decided while building
it, and where it deviates:

- **The docker provisioner is gone.** That means its host
  (`workspace/host_docker.go`, `sandbox.go`), the docker browser and gateway
  hosts, the `docker` kind, `wsimage` (the engine-side image builds),
  `APOTHECARY_SANDBOX_RUNTIME`, the compose deployment and its tasks. The
  engine image drops docker, Node and the devcontainer CLI: they run in the
  sandbox pod, and the engine only runs `kubectl`.
- **No backward compatibility with the docker provisioner.** Nothing was
  ever deployed on it, so no migration and no special case: a `docker` kind
  is unknown like any other.
- **The `Host` seam stays.** The adapter's host-blind logic (definition,
  exec, sidecar, ports, editor, daemon-door decisions, egress orchestration)
  is tested on a test-only host (`testHost` in `fixtures_test.go`), which
  mirrors the pod host's prepare without a cluster.
- **The pin guards moved, not vanished.**
  - `TestShimDependencyPinsMatchGoMod` now reads the published shim
    Dockerfile (`workspace/embed_test.go`).
  - `TestToolsDockerfilePinsMatchKinds` lives in `internal/project/kinds`.
  - The embedded sources stay: the k8s host hashes them to detect a stale
    seeded shim or tools bundle.
- **`test:integration:harness` was not ported** (a deviation). It never used
  the docker provisioner; Docker is only its Linux test runner.
- **The sandbox shape has deployment defaults.** `APOTHECARY_K8S_RUNTIME_CLASS`
  and `APOTHECARY_K8S_PRIVILEGED` fill an integration's empty fields, and
  boot refuses privileged off without a runtime class. The daemon-door
  warning reads the deployment's setting. Sandbox placement is the
  RuntimeClass's own `scheduling` (sysbox's installer sets it up), not chart
  values or engine code.
- **Every account gets a Kubernetes provisioner** (`confstore.EnsureProvisioner`
  through `httpapi.Server.NewAccount`, wired only on Kubernetes). It runs
  when the instance is claimed and when an admin adds an account, and is a
  no-op for an account that has any provisioner. It is created at account
  time rather than first boot because integrations are per user.
- **One network surface: the engine's HTTPS address.**
  - Port tunnel: `GET /api/runs/{id}/ports/{port}/tunnel`.
  - SSH tunnel: `GET /api/ssh/tunnel?workspace=…`.
  - Both are WebSockets (`internal/platform/wstunnel`, on
    gorilla/websocket), because every ingress and load balancer passes a
    WebSocket and not all pass a bare upgrade.
  - Pings run on a timer to stay under 60-second idle timeouts. A failed
    ping write is how a dead peer shows.
  - Read deadlines are not used for liveness: gorilla answers pongs only
    while something reads, so they would kill an idle tunnel.
- **runports keeps one path per run and port** (`Dial`), because opening one
  starts a relay in the workspace. The path is dropped on teardown and on
  rebuild, and reopened once if it stops answering. Tunnels need no relay.
- **On Kubernetes without an apps domain the relay is off.** A listener per
  port would hand out URLs nothing reaches. `RunPorts.links` tells the UI,
  which then shows the forward command and no Expose button.
- **The SSH door has no port of its own on Kubernetes** (`APOTHECARY_SSH_LISTEN`
  is for the dev loop).
  - The CLI's `ssh-config` writes one wildcard block,
    `Host *.apothecary`, with `ProxyCommand apothecary ssh-proxy %h`.
  - It pins the door's single host key under one `HostKeyAlias`, with
    `StrictHostKeyChecking accept-new`. Trust on first use is acceptable
    here: it rides an authenticated TLS tunnel.
  - The tunnel names the workspace, so the door ignores the SSH username
    (`ServeConn`).
  - The door throttles failures by the API's user, because every tunnel
    shares the ingress's address.
  - The run page's SSH facts carry `setup`: the one-time command.
- **The terminal client** is `cmd/apothecary` (wiring only),
  `internal/client/api` (the one HTTP client) and `internal/client/cli`
  (thin commands, one printer).
  - archtest: the client imports only itself and `internal/platform`, and
    only the client and its binary import it.
  - `login` and `forward` were reworked the next day (below).
  - The commands use the standard `flag` package, as `apothecaryd` does. The
    TUI will be Bubble Tea, in the same binary, on the same `api` package;
    the dependency is not added yet.
- **The chart is `charts/apothecary`.**
  - Its schema refuses the removed keys (`ssh`, `ingress`, `service`,
    `engine.replicas`), unprivileged sandboxes without a runtime class, an
    ingress without a host, and an apps domain without the UI's address.
  - It runs one engine with `Recreate`.
  - `pullPolicy` is automatic: Always for latest/local/dev tags,
    IfNotPresent otherwise.
  - Examples in `ci/` are linted and rendered by `task lint:helm`, which also
    diffs the helm-docs README. helm-docs runs non-strict, because strict
    mode demands an entry for every parent object.
  - Nothing names a VPN product: the private example passes any ingress
    class through untouched.
- **Release.** The chart is pushed to `oci://ghcr.io/intradeus/charts`,
  stamped `X.Y.Z` / `vX.Y.Z`. The client is cross-built for
  Linux/macOS/Windows on amd64/arm64 and attached to the GitHub Release with
  `SHA256SUMS`.
- **Not done here:**
  - The k3d integration test that forwards a port and SSHs through the
    tunnel is not written yet.
  - The run-page preview pane and shareable app URLs are out of scope.

## The client signs in through the browser, forwards in the background, and updates itself (2026-11-11)

- **Browser login, the device authorization grant (RFC 8628, as
  `gh auth login` does).**
  - `apothecary login <url>` opens a sign-in (`POST /api/cli/logins`, public)
    and opens the browser on `/cli-login?code=XXXX-XXXX`.
  - The person approves it there, signed in as usual. The code shown on the
    page and in the terminal is what they compare.
  - The client polls `POST /api/cli/logins/token` with its secret device
    code until it gets a token.
  - One flow for a desktop and for a machine without a browser
    (`--no-browser` prints the address): no localhost callback, no PKCE.
  - Pending sign-ins live in the engine's memory (one replica), last 10
    minutes, and number at most 1000. A token is minted under the lock, so
    two racing approvals cannot mint two.
  - `--token <personal token>` stays, for scripts and CI. The password
    login is gone from the client.
- **The client's token expires after a stretch of disuse, not on a date.**
  - Migration 0002 adds `api_tokens.idle_seconds`. A token with one is
    refused once its last use (or its creation) is older than the window,
    and every use slides it (touched at most once a minute).
  - The window is `APOTHECARY_CLI_TOKEN_IDLE` (default `168h`). Tokens made
    in the UI never get one.
  - Account → API tokens shows such a token as the CLI's, "signs out after
    N days unused".
  - A token approved but never collected (the client quit) idles out by
    itself.
- **Local ports are random by default.**
  - A run's port lands on a free local port, and the client prints the
    mapping.
  - `--same` keeps the numbers, for a frontend that calls its API at
    `localhost:<port>`; `port:local` names one.
  - The local port a run's port got is remembered (`ports.json`, the last
    200 runs), so forwarding it again lands on the same one while it is
    free.
- **Forwards live in a per-user background daemon** (`__forward-daemon`).
  - It is started by the first `forward`, detached (its own session on Unix,
    `DETACHED_PROCESS` on Windows), so closing or killing the terminal
    leaves it running.
  - Commands reach it over a unix socket in `<config>/apothecary/forwards/`
    (0700, the socket 0600), with HTTP+JSON on it. Go speaks AF_UNIX on
    Windows 10 and later too.
  - It reads the sign-in again on every new forward and connection, so a new
    login reaches running forwards.
  - It drops a run whose workspace or sign-in is gone, and exits once nothing
    is forwarded, or 30 s after starting with nothing asked.
  - It writes its own log (`forward logs`). `forward status` names each run
    by its ticket key and title and its workspace. A run is named by its id
    or its workspace (the env id the SSH door also uses).
- **`apothecary update` installs the release of the engine's version, not
  the newest.**
  - The engine says its version on the public `/api/auth/status`; it is
    `main.version`, stamped at build time.
  - The client downloads `apothecary-<os>-<arch>` and `SHA256SUMS` from that
    GitHub Release, refuses a checksum mismatch, and replaces itself with
    one rename. Windows moves the running `.exe` aside first.
  - An engine built from source (`dev`) has no release to match. Once a day a
    command checks the engine's version and says when the client differs.
- **The UI copies the commands.**
  - The Ports view shows `apothecary forward <ws>` and `--same`, and a copy
    button per port (`apothecary forward <ws> <port>`).
  - The Services view has the same per-port copy button.
  - The commands name the workspace, not the run id.

## A fresh box, from nothing: deploy/k3s/install.sh (2026-11-11)

- **There were no scripts to upstream.** Nothing in the repository set up a
  node, so the install was written from the documented requirements. It has
  not been run end to end yet: the first run on a fresh box is its test.
- **One idempotent script** (bash, shellcheck-clean), in this order:
  1. preflight;
  2. wireguard, the sysbox `fusermount3` AppArmor override, the
     unprivileged-userns sysctl;
  3. sysbox from its `.deb`;
  4. k3s from a release channel, refusing Kubernetes < 1.33 and
     containerd < 2.0.5;
  5. a `sysbox-runc` runtime, through k3s's `config-v3.toml.tmpl`, which
     keeps k3s's own settings ("base") and the same `SystemdCgroup` as runc;
     then the RuntimeClass;
  6. a smoke-test pod shaped like a sandbox (sysbox, `hostUsers: false`,
     `docker:29-dind`, no privilege);
  7. Helm, cert-manager with `--domain`, then the chart.
- **No admission policy for `hostUsers: false`** (a deviation from the
  plan). The engine already sets it on every sandbox it creates under a
  runtime class.
- **The master key comes from the release's own `apothecaryd keygen`,** run
  once as a pod, so its format is the engine's by construction. It is kept
  with the database password in `/root/.apothecary/install.env` (0600), and
  reruns reuse them.
- **Two shapes.**
  - LAN: the UI as a LoadBalancer Service on port 8080 of the box (k3s's
    ServiceLB), plain HTTP, `access.publicURL` set to that address.
  - `--domain`: a Traefik ingress with a cert-manager Let's Encrypt
    certificate (HTTP-01).

## CI builds each image once, and the k8s suite imports instead of pushing (2026-11-11)

- **Profiled first, cold** (no build cache or base images), on a 20-core box:
  13 minutes in all.
  - Building all five images: 4m47, of which the engine image took 1m53 and
    the tools bundle 1m52.
  - Pushing them to the cluster's registry: 3m50.
  - The live tests: 3m.
  - Memory stayed under 1.5 GB for the cluster and pods. CPU was about 3
    cores on average, with bursts to every core during builds.
- **CI built the same images three times:** the build job (five), the k8s
  suite (five) and the harness suite (tools).
- **Now one `images` job builds the four the suites run.**
  - It fills the GitHub Actions cache (`deploy/images.sh` with
    `CACHE=gha-warm`, one cache scope per image).
  - The harness and k8s jobs wait for it, then build through that cache
    (`CACHE=gha-read`): every layer a hit, which is a download.
  - The engine image moved to the build job; no suite runs it (the live
    tests run the engine as a test process).
  - Not artifacts: those count against the account's storage (about 2 GB on
    Pro), and a 1–1.5 GB image set per run would exhaust it. The Actions
    cache is a separate 10 GB per repository; eviction there costs speed,
    never a failure.
- **The k8s suite imports its images into the k3d node** (`k3d image import`,
  `APOTHECARY_LIVE_PULL_POLICY=IfNotPresent`) instead of pushing to the
  registry and pulling back. The tools bundle is copied once, not twice, and
  CI no longer reconfigures dockerd for an insecure registry. `k8s:images`
  (push) stays for the `k8s:deploy` dev loop.
- **Path filters.** On a pull request, `images`, harness and k8s run only
  when it touches the sandbox side (`changes` job, dorny/paths-filter).
  Pushes to main run everything, so manual-release still only releases a
  fully tested commit.
- **Measured locally after the change:** cold 8m11 (−37%), warm 6m02
  (−53%). Warm, the image import (1m40) and the tests are what remains.
