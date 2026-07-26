# Apothecary Software Factory — Execution Spec (v1.0, final)

> Audience: the implementing agent. This document is **normative**: MUST/MUST
> NOT are requirements. Where something is genuinely unspecified, make the
> most boring choice consistent with this spec and record it in
> `docs/IMPLEMENTATION_NOTES.md` — do not invent features, dependencies, or
> abstractions not listed here. PLAN.md is the narrative overview; this file
> wins on conflict.

## 0. Fixed technology choices

| Concern | Choice (MUST use) |
|---|---|
| Engine language | Go ≥ 1.24, single module `factory`, single binary `apothecaryd` |
| DB | PostgreSQL ≥ 16, access via `jackc/pgx/v5`, hand-written SQL, migrations as numbered `.sql` files applied at boot |
| YAML | `gopkg.in/yaml.v3` |
| IDs | ULID (`oklog/ulid/v2`) for run/ping ids |
| HTTP | stdlib `net/http`; SSE hand-rolled (flusher loop) |
| CPUI | SvelteKit + TypeScript, no component library, talks only to the engine API |
| Provisioning | Coder, Terraform `coder/docker` provider template |
| Harness | opencode CLI |
| Forge | GitLab REST API v4 (not `glab`) |

## 1. Project configuration & preflight (code-driven, never AI-guessed)

The factory operates on one project repo (v1 is single-project). **Principle
(MUST): agents never guess project mechanics.** Infrastructure is the
user-authored Coder template; checkout is engine code (§11); environment
setup is a user-provided command; health checks are deterministic engine
code. All project mechanics are configuration (§13):

- `repo.url`, `repo.default_branch`, `repo.agents_md_path` (default
  `AGENTS.md`; if not at repo root, bootstrap symlinks it to `AGENTS.md` so
  the harness picks it up natively).
- `project.commands`: a user-provided map of named shell commands. `setup`
  is mandatory (dependency install / env prep — run at bootstrap step 6);
  the rest (e.g. `verify`) are referenced from flow `command` stages via
  `run_ref`. **No build tool is assumed or enforced** — Makefile, just, npm
  scripts, anything: the user supplies the exact commands.
- `.apothecary/agents/` specs (§5) must exist in the repo.

**Preflight at run creation (engine code, no AI):** `Forge.CheckAccess`
(token valid, project reachable, scopes sufficient) → `Forge.FileExists`
for `repo.agents_md_path` and `.apothecary/agents/` → config validation
(every `run_ref` used by the flow resolves in `project.commands`). Any
failure refuses the run naming the exact missing item. The user's `setup`
command failing at bootstrap fails the run deterministically as
`env_failed` with the script's output — command-stage semantics, just
before the flow starts.

## 2. Run lifecycle

States: `pending → provisioning → running ⇄ waiting_on_human → completed |
abandoned | failed`, plus terminal-adjacent `archived`. Transitions:

| from | to | on |
|---|---|---|
| pending | provisioning | slot free (§14 concurrency) |
| provisioning | running | env_ready |
| provisioning | failed | env_failed after 1 retry |
| running | waiting_on_human | ping_raised (any type) |
| waiting_on_human | running | ping_answered |
| running | completed | final_gate approved (MR merged or manual confirm) |
| running | abandoned | edge max exhausted, budget exceeded, or unrecoverable stage failure with no `on_fail` |
| running/waiting | failed | abort requested, or infrastructure error after retries |
| completed/abandoned/failed | archived | user action or reaper (§14) |

**Aborts:** `POST /api/runs/{id}/abort` cancels the run context, kills any
in-flight exec, emits `run_failed{reason:"aborted"}`. Environment is kept
(reaper rules apply).

**Crash recovery (MUST):** on boot the engine replays events per run. Any run
whose last event is non-terminal is resumed: `provisioning` → poll env
status, re-provision if absent; `running` with an open `stage_started` →
re-enter that stage: `agent` stages via `Resume` if a `SessionID` was
recorded, else re-`Run`; `command` stages are re-executed from scratch
(commands MUST therefore be safe to re-run — `make lint`/`make test`/`git
push` all are); `waiting_on_human` → nothing, wait for answer. Iteration
counters and budgets are reconstructed from events, never stored separately.

**Single-writer rule (MUST):** one goroutine owns each run; all events for a
run are appended by it; `(run_id, seq)` is unique with seq monotonic per run.

## 3. Flow definition — full schema

Flows live in `flows/*.yaml`, loaded and validated at run creation; the
run stores `flow_name` + SHA-256 `flow_hash` of the raw file.

Top-level fields:

| field | type | req | meaning |
|---|---|---|---|
| `name` | string | yes | unique flow name |
| `description` | string | no | free text |
| `defaults.timeout` | duration | no (30m) | per-stage wall clock, **suspended while a ping is pending** |
| `defaults.harness` | enum `opencode` | no (opencode) | runner for agent stages |
| `defaults.model` | string `provider/model` | yes | model fallback |
| `defaults.max_turns` | int | no (50) | agent-stage turn cap fallback |
| `budget.max_cost_usd` | float | no (10.0) | sum of `Usage.CostUSD` across the run; exceeding ⇒ abandoned |
| `budget.max_total_turns` | int | no (300) | same, in turns (covers harnesses that report no cost) |
| `stages` | list | yes | ordered; execution starts at first entry and proceeds in list order unless an edge redirects |

> **Superseded.** The flow language has since been reworked and versioned;
> `docs/flow-language.md` is the normative reference (cited in code as
> `FLOWLANG §n`). What follows records the original v1 schema: `forge_mr`
> became a generic `action` stage, the `verdict` flag became a typed agent
> output, gate `ping`/`payload` became `question`/`input`, and every stage's
> reads and writes are now declared. Kept for the reasoning, not the syntax.

Per-stage fields:

| field | applies to | req | meaning |
|---|---|---|---|
| `id` | all | yes | unique slug |
| `type` | all | yes | `agent` \| `command` \| `human_gate` \| `forge_mr` |
| `agent` | agent | yes | name resolving to `.apothecary/agents/<name>.yaml` |
| `model`, `harness`, `max_turns`, `timeout` | agent | no | override defaults |
| `input` | agent | no | ordered list of: `ticket`, artifact paths, `$CHANGED` |
| `output` | agent | no | artifact path the agent MUST produce; missing/unparsable ⇒ `stage_failed{reason:"output_contract"}` |
| `verdict` | agent | no (false) | if true, agent MUST write `artifacts/verdict.json` (§5.3); REJECT triggers `on_reject` |
| `run` / `run_ref` | command | one of | `run`: literal bash; `run_ref`: key into `project.commands` (§1). Either way executed `bash -eo pipefail -c` in repo root |
| `ping` | human_gate | yes | `plan_approval` \| `final_review` |
| `payload` | human_gate | no | artifact path or `mr_url` shown to the human |
| `auto_complete` | human_gate | no | `mr_merged`: engine polls `Forge.MRState` every 60s and approves the gate when merged |
| `body` | forge_mr | yes | artifact path used as MR description |
| `on_fail` / `on_reject` | command+agent / agent+human_gate | no | edge `{to, max, feed}`; `feed` ∈ `output` (last 8 KB of command output) \| `comments` (verdict comments) \| `feedback` (human text). Exhausted edge ⇒ abandoned |

Validation at load (MUST): unique ids; edges reference existing stage ids;
`model` only on agent stages; exactly one `forge_mr` at most; `human_gate`
with `auto_complete` must be last; command stages carry exactly one of
`run`/`run_ref`; unknown fields are errors.

### 3.1 flows/v1.yaml (canonical, ships in repo)

```yaml
name: v1
description: plan-gated build with cross-vendor adversarial review
defaults: { timeout: 30m, harness: opencode, model: anthropic/claude-sonnet-4-6, max_turns: 50 }
budget: { max_cost_usd: 10.0, max_total_turns: 300 }
stages:
  - { id: plan,       type: agent, agent: planner, input: [ticket],
      output: artifacts/plan.md, max_turns: 25 }
  - { id: plan_gate,  type: human_gate, ping: plan_approval, payload: artifacts/plan.md,
      on_reject: { to: plan, max: 3, feed: feedback } }
  - { id: build,      type: agent, agent: builder, input: [ticket, artifacts/plan.md], max_turns: 80 }
  - { id: test,       type: command, run_ref: verify,
      on_fail: { to: build, max: 5, feed: output } }
  - { id: review,     type: agent, agent: reviewer, model: openai/gpt-5,
      input: [ticket, artifacts/plan.md, $CHANGED], verdict: true, max_turns: 30,
      on_reject: { to: build, max: 3, feed: comments } }
  - { id: redact,     type: agent, agent: redactor, input: [ticket, $CHANGED],
      output: artifacts/mr.md, max_turns: 15 }
  - { id: push,       type: command, run: "git push -u origin HEAD" }
  - { id: mr,         type: forge_mr, body: artifacts/mr.md }
  - { id: final_gate, type: human_gate, ping: final_review, payload: mr_url,
      auto_complete: mr_merged }
```

Note the split found in review: **`push` is a workspace command; `mr` is
engine-side via the Forge port.** The engine reads `artifacts/mr.md` out of
the workspace (`EnvProvisioner.ReadFile`) and calls
`Forge.CreateDraftMR(repo, branch, title, body)`; title = first `# ` heading
of mr.md, else the ticket title.

### 3.2 Prompt assembly (verbatim template)

The engine builds every agent prompt exactly as:

```
# TASK
{ticket.title}

{ticket.goal}
{for each artifact in input, in order:}
# ARTIFACT: {path}
{content, truncated to last 16 KB}
{if input contains $CHANGED:}
# CHANGED FILES
{output of: git diff --stat origin/<default>...HEAD}
Inspect changed files directly with your read tools; do not ask for the diff.
{if iteration > 1:}
# PREVIOUS ATTEMPT FEEDBACK (iteration {n} of {max})
{feed content, truncated to last 8 KB}
# OUTPUT CONTRACT
{if output:} Before finishing you MUST write {output} (relative to repo root).
{if verdict:} Before finishing you MUST write artifacts/verdict.json exactly as
{"verdict":"APPROVE"|"REJECT","comments":["..."]} — comments required when REJECT.
```

Role/system prompt comes from the agent spec via harness config (§5), never
duplicated here. `$CHANGED` is deliberately a stat + file list, not the full
diff — reviewers read the repo themselves; this bounds tokens and was chosen
over passing full diffs.

## 4. Domain ports (final)

As previously specified, with two additions found necessary in review:
`EnvProvisioner` gains `ReadFile(ctx, env, path) ([]byte, error)` and
`WriteFile(ctx, env, path string, data []byte, mode fs.FileMode) error`
(needed for bootstrap §11 and forge_mr §3.1). Full interfaces:

```go
type EnvProvisioner interface {
    Provision(ctx, spec EnvSpec) (EnvID, error) // blocks until ready; 1 internal retry
    Exec(ctx, env EnvID, cmd Command) (ExecResult, error)
    ReadFile(ctx, env EnvID, path string) ([]byte, error)
    WriteFile(ctx, env EnvID, path string, data []byte, mode fs.FileMode) error
    Links(ctx, env EnvID) ([]AccessLink, error)
    Status(ctx, env EnvID) (EnvStatus, error)
    Destroy(ctx, env EnvID) error
}
type AgentRunner interface {
    Run(ctx, env EnvID, spec AgentSpec, in AgentInput) (AgentResult, error)
    Resume(ctx, env EnvID, s SessionID, reply string) (AgentResult, error)
}
type Forge interface {
    CheckAccess(ctx, repo RepoRef) error                     // §1 preflight: token, project, scopes
    FileExists(ctx, repo RepoRef, path string) (bool, error) // §1 preflight
    CreateDraftMR(ctx, repo RepoRef, branch, title, body string) (MRRef, error)
    MRState(ctx, mr MRRef) (MRState, error) // open|merged|closed
}
type NotificationChannel interface {
    Send(ctx, n Notification) error // errors are logged, never fail the run
}
```

`AgentResult{Session, Verdict *Verdict, Output string, TurnsUsed int, Usage}`
as before; `Usage{InputTokens, OutputTokens int, CostUSD float64}`.
Fakes for all four ports live in `internal/adapters/fake` and are the test
substrate for every scenario in §15.

## 5. Agent specs and harness translation

### 5.1 `.apothecary/agents/<name>.yaml` schema

```yaml
name: reviewer                 # = filename stem
description: adversarial code reviewer
read_only: true                # deny edit/write/patch tools
allow_bash: true               # bash tool availability (see caveat below)
prompt: |
  You are an adversarial code reviewer for this repository...
```

**Honesty note (replaces an earlier overclaim):** `read_only: true` denies
the harness's edit/write tools, but if `allow_bash: true` the agent can still
mutate files through shell. v1 accepts this residual for the reviewer
(it needs `make test`); the mitigation is that review runs on a branch whose
final state is human-reviewed anyway. Record as known limitation.

### 5.2 Translation (corrects "nothing generated")

At bootstrap (§11) the engine **generates** harness-native config from the
neutral specs: for opencode, `.opencode/` in the workspace containing one
agent entry per spec (system prompt = `prompt`, tool permissions from
`read_only`/`allow_bash`) plus the `ask_human` MCP server registration
(§10). Generated files are workspace-local, `.gitignore`d, regenerated every
provision. The adapter then invokes:

`opencode run --format json --dangerously-skip-permissions --agent <name> -m <provider/model> "<assembled prompt>"`

parsing the NDJSON stream: `step_finish` events accumulate `Usage`
(cost + input/output tokens) and count turns; the final session id is
`SessionID`; `Resume` maps to `opencode run --session <id> ... "<reply>"`.
Flag names MUST be verified against the pinned opencode version in M3 and
corrected in IMPLEMENTATION_NOTES if drifted — behavior, not flags, is
normative.

### 5.3 The four v1 agents (prompts ship in the target repo)

`planner` (read_only: true, allow_bash: false): explores the repo, writes
`artifacts/plan.md`: goal restatement, files to touch, step list, test plan,
risks. `builder` (read_only: false, allow_bash: true): implements the
approved plan; commits as it goes (§11 git policy); never pushes. `reviewer`
(read_only: true, allow_bash: true): §5.1 example; verdict contract; judges
against plan, AGENTS.md conventions, and the user's DDD/SOLID standards.
`redactor` (read_only: true, allow_bash: true): writes `artifacts/mr.md`
(first line `# <MR title>`, then summary, changes, test evidence, review
notes). Full prompt texts are authored during M2 as part of the template
work; the schema above is what the engine enforces.

## 6. Events — exact payloads

Table `events(run_id text, seq bigint, ts timestamptz, type text, payload
jsonb, PRIMARY KEY(run_id, seq))`. Payload keys (all events also implicitly
carry run_id/seq/ts):

`run_created{ticket_title, ticket_goal, flow_name, flow_hash, repo_url,
branch}` · `env_requested{}` · `env_ready{env_id, links:[{kind,url}]}` ·
`env_failed{error}` · `stage_started{stage_id, iteration, harness?, model?}`
· `stage_completed{stage_id, duration_ms, turns_used?, usage?{input_tokens,
output_tokens, cost_usd}, session_id?}` · `stage_failed{stage_id, reason:
exit_nonzero|timeout|harness_error|output_contract|infra, exit_code?,
error?}` · `edge_taken{from, to, edge:on_fail|on_reject, count, max}` ·
`ping_raised{ping_id, type, question, payload_ref?}` · `ping_answered
{ping_id, answer:approve|reject|text, feedback?}` · `verdict_received
{stage_id, approve, n_comments}` · `mr_created{mr_url, mr_iid, branch}` ·
`run_completed{duration_ms, total_cost_usd, total_turns, unwanted_pings}` ·
`run_abandoned{stalled_at, edge?, reason}` · `run_failed{reason}` ·
`env_destroyed{env_id}`.

A `runs` read-model table `(run_id pk, state, ticket_title, flow_name,
flow_hash, current_stage, mr_url, env_id, created_at, updated_at)` is
updated in the same transaction as each append — projection, rebuildable
from events (provide `apothecaryd rebuild-projections`).

## 7. Telemetry

The engine emits OTel (OTLP/gRPC to a collector configured in §13; disabled
when unset): span per run, child span per stage attempt, attrs
`factory.run_id`, `factory.stage`, `factory.harness`, `factory.model`;
metrics: `factory_runs_total{state}`, `factory_pings_total{type}`,
`factory_stage_cost_usd`, `factory_stage_turns`. Harness-native telemetry is
never load-bearing; `Usage` mapping per §5.2 is the source for cost.

## 8. HTTP API (engine)

Bind: `0.0.0.0:8080` — v1 runs on one machine (localhost mode). The CPUI
and curl use `http://localhost:8080`; workspaces reach the engine at
`http://host.docker.internal:8080`, which the Coder template maps to the
host gateway (containers cannot reach the host's `127.0.0.1` — this mapping
is what makes localhost mode work). No auth except: `POST/GET
/api/runs/{id}/pings*` require `Authorization: Bearer <per-run token>`.
Remote access (tailnet or otherwise) is a later step, together with
multi-user auth.

| method+path | body → response |
|---|---|
| POST `/api/tickets` | `{title, goal, flow?="v1"}` → `201 {run_id}` (409 if concurrency cap) |
| GET `/api/runs` | → list of runs read-model |
| GET `/api/runs/{id}` | → run + last 100 events |
| GET `/api/runs/{id}/events` | SSE stream (replay from `?after_seq=`) |
| GET `/api/runs/{id}/logs/{stage}` | → text tail (`?n=` bytes, default 64 KB) |
| POST `/api/runs/{id}/abort` · `/archive` | → 202 |
| POST `/api/runs/{id}/pings` | shim: `{type, question, context?}` → **long-poll**: 200 `{answer, feedback?}` when answered, else 204 after 55 s (shim re-polls with `ping_id`) |
| GET `/api/runs/{id}/pings/{pid}` | shim re-poll → 200/204 as above |
| POST `/api/pings/{pid}/answer` | CPUI: `{answer, feedback?}` → 200 |
| GET `/api/pings?status=pending` | CPUI inbox |
| GET/POST/DELETE `/api/channels` | channel config CRUD (`{kind:"matrix", homeserver, access_token, room_id}`) |

## 9. Database DDL (initial migration)

`events` and `runs` per §6; `pings(ping_id pk, run_id, type, question,
context, payload_ref, status pending|answered, answer, feedback, raised_at,
answered_at)`; `channels(id pk, kind, config jsonb, created_at)`;
`schema_migrations(version pk)`. Channel secrets are plaintext jsonb —
acceptable v1 (single-user dev machine); note in security section.

## 10. ask_human shim (exact contract)

A ~100-line Node script in the workspace image, registered as an MCP stdio
server exposing one tool:
`ask_human{type: "clarification"|"blocker"|"permission", question: string,
context?: string}`. It reads `APOTHECARY_ENGINE_URL`, `APOTHECARY_RUN_ID`,
`APOTHECARY_RUN_TOKEN` from env (written at bootstrap). Protocol: POST the
ping; on 204, re-poll `GET .../pings/{ping_id}` every 5 s (server holds 55 s)
indefinitely — the stage timeout is suspended while a ping is pending, so
the shim never gives up; on network error, retry with backoff capped at
60 s. Tool returns the answer text (for gates the engine, not the shim,
raises pings). If the engine is unreachable > 10 min, return a tool error
string `"ask_human unavailable: <err>"` and let the agent proceed.

## 11. Workspace: template, bootstrap, git policy

**Coder template** `factory` (Terraform, docker provider): dev container
(image below) + project services (postgres/queue/redis per the project's
needs) on a per-workspace network; the dev container gets a
`host.docker.internal → host-gateway` hosts entry (docker provider `host`
block) so the shim can reach the engine; `coder_app`s: code-server, web
terminal, app port. Template parameters carry **only non-secrets**: run_id, repo_url,
branch name. **No secret ever passes through Coder parameters** (they're
visible in Coder's UI/state — found in review); secrets arrive in step 4
below.

**Image** (one Dockerfile in `workspace/`): Ubuntu base, Go + Node
toolchains, opencode (pinned version), git, ripgrep, the ask_human shim,
`init-firewall.sh`, non-root user `factory`, home on a Coder persistent
volume. No docker socket, `cap_drop: ALL` after firewall init.

**Firewall allowlist (explicit):** Anthropic API, OpenAI API, the GitLab
host, `proxy.golang.org`, `sum.golang.org`, `registry.npmjs.org`, and the
engine — the container's default-gateway IP on the engine port (what
`host.docker.internal` resolves to). Everything else denied, including DNS
to other names (resolve-then-ipset pattern).

**Bootstrap (engine-driven, ordered, after env_ready):**
1. `WriteFile /apothecary/run.env` (0600): `APOTHECARY_RUN_ID`, `APOTHECARY_RUN_TOKEN`,
   `APOTHECARY_ENGINE_URL`.
2. `WriteFile` provider keys file (0600) sourced into agent invocations:
   `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` (only those the flow needs), and
   seed opencode `auth.json` if subscription auth is configured.
3. `Exec`: `git clone` via token-in-URL rewritten to a credential helper
   (token never lands in `.git/config`), `git switch -c <branch>` from the
   default branch. Branch name: `factory/<run_id_lower26>-<slug(title,24)>`.
4. `Exec`: generate `.opencode/` from `.apothecary/agents/` (§5.2); if
   `repo.agents_md_path` ≠ `AGENTS.md`, symlink it to `AGENTS.md`; append
   generated paths to `.git/info/exclude`.
5. `Exec`: `git config user.name "Factory" && git config user.email
   "factory@local"`.
6. `Exec`: the user's `project.commands.setup` (dependency install / env
   prep). Non-zero exit ⇒ `env_failed` with the last 8 KB of output —
   agents never install dependencies or repair the environment themselves.

**Git policy:** the builder commits incrementally with messages
`factory(build): <summary>`; nothing pushes except the `push` stage; force
push is forbidden; the MR branch is never rebased by the factory. Human
takes over entirely after `final_gate` raises — the factory never touches
the branch again once the MR exists.

## 12. Coder adapter mapping

| port call | implementation |
|---|---|
| Provision | `coder create --template factory --yes` + rich params (non-secrets), then poll `coder list` until running; then bootstrap §11 |
| Exec | `coder ssh <ws> -- bash -eo pipefail -c '<script>'`, streamed to `/var/lib/apothecary/runs/<run>/<stage>-<iter>.log` |
| Read/WriteFile | `coder ssh <ws> -- cat` / `tee` (base64-wrapped for binary safety) |
| Links | Coder REST: workspace apps → `{ide, terminal, app}` URLs |
| Status / Destroy | Coder REST / `coder delete --yes` |

Engine holds one Coder session token (config). All `coder` CLI calls go
through a single wrapper with 3× retry on transient failures.

## 13. Engine configuration (`/etc/factory/factory.yaml`)

```yaml
listen: "0.0.0.0:8080"                  # v1 localhost mode; workspaces use host.docker.internal
postgres_dsn: postgres://factory:...@localhost/factory
repo: { url: "https://gitlab.com/<group>/<project>.git", default_branch: main, agents_md_path: AGENTS.md }
project:
  commands:                               # user-provided; no build tool assumed
    setup:  "./scripts/factory-setup.sh"  # mandatory — deps install, env prep
    verify: "make lint && make test"      # example; referenced via run_ref
gitlab: { base_url: "https://gitlab.com", token_env: GITLAB_TOKEN, project_id: 123 }
coder: { url: "http://localhost:7080", token_env: CODER_SESSION_TOKEN, template: factory }
providers: { anthropic_env: ANTHROPIC_API_KEY, openai_env: OPENAI_API_KEY }
flows_dir: /etc/factory/flows
runs_dir: /var/lib/apothecary/runs        # logs + artifact copies, kept forever
limits: { max_concurrent_runs: 3, max_environments: 6 }
reaper: { destroy_completed_after: 48h, destroy_abandoned: never }
otel: { endpoint: "" }                  # empty = disabled
```

## 14. Reaper and limits

Reaper loop (hourly): destroy envs of `completed` runs older than the
configured window and of `archived` runs immediately; never `abandoned`
(inspection material) — but when total envs ≥ `max_environments`, refuse new
tickets with 409 and raise a `blocker`-type notification naming the runs to
archive. Concurrency: runs beyond `max_concurrent_runs` queue in `pending`,
FIFO.

## 15. Security model (recap + residuals)

Trust boundaries: engine/host = trusted; workspace = untrusted (prompt
injection assumed possible). Enforcements: egress allowlist; per-run bearer
token scoping a workspace to its own ping endpoints; repo-scoped GitLab
token (contents+MR only); no Coder token or docker socket in workspaces;
secrets never in Coder parameters. **Accepted residuals
(documented, not fixed in v1):** exfiltration via `git push` to the one
authorized repo; reviewer bash caveat (§5.1); channel secrets plaintext in
Postgres; Coder server holds the docker socket (trusted control plane); the
engine API listens on `0.0.0.0` with no auth beyond the ping tokens —
acceptable on a single-user dev machine, revisited with remote
access/multi-user auth.

## 16. Milestones with acceptance criteria

| # | deliverable | done when |
|---|---|---|
| M1 | domain, flow parser+validator, interpreter, eventstore, projections, fakes, HTTP API | `go test ./...` green incl. scenario tests: happy path; test-fail→build loop→pass; review REJECT→build→APPROVE; edge exhaustion→abandoned; ping mid-build answered; plan_gate reject with feedback; budget exceeded→abandoned; engine kill -9 mid-`build`→restart→resumes via fake Resume; stage timeout→on_fail; invalid flow rejected with line-precise error |
| M2 | Coder adapter, workspace image, template, bootstrap | against local Coder: provision→bootstrap incl. the user `setup` command→`Exec` of `project.commands.verify` on the real repo passes; firewall verified (`curl example.com` fails, provider APIs reachable); ReadFile/WriteFile round-trip |
| M3 | opencode adapter + shim | a real ticket runs plan→plan_gate (answered via curl)→build→test green; `Usage` populated from NDJSON; kill engine mid-build, restart, run finishes; flag drift recorded in IMPLEMENTATION_NOTES |
| M4 | GitLab adapter + forge_mr + auto_complete | full v1 flow ends in a real draft MR; merging it in GitLab completes the run within 90 s |
| M5 | CPUI (run list, timeline from SSE, notifications inbox with answer box, channel settings + Matrix adapter, deep links) | full run driven end-to-end without curl or ssh |
| M6 | OTel + Grafana dashboards (cost/run, unwanted pings/merged MR, loop heatmap by stage) | dashboards render from 3+ real runs |

Build strictly in order; each milestone merges only when its criteria pass.

## 17. Out of scope for v1 (unchanged)

Jira, CI-status gates inside the loop, statistical A/B, additional
harnesses, multi-project, multi-user auth, post-MR comment-fix flows,
answering pings from channels, kanban anything.
