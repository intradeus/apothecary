# Apothecary v2 — Config Plane & Service Distribution — Execution Spec (v1.0)

> Audience: the implementing agent. This document is **normative** and
> **amends DESIGN.md**: sections of DESIGN.md not amended here remain in
> force (run lifecycle §2, flow schema §3, prompt assembly §3.2, events §6,
> shim §10, bootstrap order §11, reaper §14 semantics). On conflict, this
> file wins. Where something is genuinely unspecified, make the most boring
> choice consistent with this spec and record it in
> `docs/IMPLEMENTATION_NOTES.md`. PLAN2.md is the narrative; this file wins
> on conflict.

## 0. Fixed technology choices (delta over DESIGN.md §0)

| Concern | Choice (MUST use) |
|---|---|
| Secrets at rest | NaCl secretbox via `golang.org/x/crypto/nacl/secretbox` (only new Go dependency) |
| Bootstrap config | environment variables only (§1); `/etc/factory/factory.yaml` is retired |
| Second forge | GitHub REST API v3 (`api.github.com`), PAT auth, no vendor SDK |
| Second harness | codex CLI (`@openai/codex`, pinned), exec mode + JSON event stream |
| Image builds | `docker` CLI against the mounted socket; no docker Go SDK |
| Embedded assets | `go:embed` for `workspace/` (base image context), `workspace/template/`, `flows/v1.yaml` |
| Distribution | one apothecaryd OCI image + reference `docker-compose.yaml` in `deploy/` |

Everything else in DESIGN.md §0 (Go/pgx/yaml/ULID/stdlib HTTP/SvelteKit,
Coder, opencode, GitLab) is unchanged.

## 1. Bootstrap configuration (env vars)

`internal/config` becomes an env loader. Exact set — anything else in the
environment is ignored; a missing required var refuses boot naming it:

| var | req | default | meaning |
|---|---|---|---|
| `APOTHECARY_POSTGRES_DSN` | yes | — | pgx DSN |
| `APOTHECARY_MASTER_KEY` | yes (serve) | — | base64 (std) of exactly 32 bytes; anything else refuses boot |
| `APOTHECARY_LISTEN` | no | `127.0.0.1:8080` | unchanged semantics (reverse tunnel, loopback-only) |
| `APOTHECARY_RUNS_DIR` | no | `/var/lib/apothecary/runs` | logs + artifacts; volume mount in Docker |
| `APOTHECARY_CPUI_DIR` | no | `""` (API only) | the shipped image sets `/opt/apothecary/cpui` |
| `APOTHECARY_OTEL_ENDPOINT` | no | `""` (disabled) | OTLP/gRPC collector |
| `APOTHECARY_BASE_IMAGE` | no | `""` (build embedded, §8.1) | expert override of the base workspace image ref |

Commands: `apothecaryd keygen` prints a fresh base64 master key and nothing
else. `serve` loses `-config` and keeps `-dev`. `validate`,
`rebuild-projections` unchanged. `envcheck` and `import-config` per §9/§14.

Everything that was user config in factory.yaml (repo, commands, gitlab,
coder, providers, flows_dir, cpui aside) moves to the DB (§2). `limits` and
`reaper` move to the `settings` table (§6.4).

## 2. Database — migration `0002_config_plane.sql`

```sql
CREATE TABLE integrations (
    id         text PRIMARY KEY,          -- ULID
    port       text NOT NULL,             -- forge|provisioner|harness|provider
    kind       text NOT NULL,             -- gitlab|github|coder|opencode|codex|anthropic|openai|openrouter
    name       text NOT NULL,             -- user label
    config     jsonb NOT NULL DEFAULT '{}',
    secrets    bytea NOT NULL DEFAULT '', -- secretbox(JSON object), §3
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (port, name)
);

CREATE TABLE projects (
    id             text PRIMARY KEY,      -- ULID
    name           text NOT NULL UNIQUE,
    forge_id       text NOT NULL REFERENCES integrations(id),
    provisioner_id text NOT NULL REFERENCES integrations(id),
    harness_id     text NOT NULL REFERENCES integrations(id),
    provider_ids   jsonb NOT NULL DEFAULT '[]',  -- list of integration ids
    repo           jsonb NOT NULL,        -- {url, locator, default_branch, agents_md_path}
    commands       jsonb NOT NULL DEFAULT '{}',  -- {setup?, verify?, ...} — setup OPTIONAL (v2 decision)
    workspace      jsonb NOT NULL DEFAULT '{}',  -- {dockerfile_fragment?, extra_domains?[], template?}
    default_flow   text NOT NULL DEFAULT 'v1',
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL
);

CREATE TABLE flows (
    name       text PRIMARY KEY,
    yaml       text NOT NULL,
    hash       text NOT NULL,             -- sha256 of yaml
    updated_at timestamptz NOT NULL
);
CREATE TABLE flow_versions (
    hash       text PRIMARY KEY,
    yaml       text NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE settings (
    key   text PRIMARY KEY,
    value jsonb NOT NULL
);

ALTER TABLE runs ADD COLUMN project_id text NOT NULL DEFAULT '';
```

Rules (MUST): the FK `RESTRICT` makes integration deletion fail while a
project references it — the API maps that to 409 naming the projects.
`runs.project_id` carries **no FK**: runs are kept forever; deleting a
project requires all its runs archived (API-enforced), after which runs keep
the orphan id and the CPUI renders "(deleted project)". Provider-id entries
in `provider_ids` are validated in code (jsonb has no FK). `channels` stays
as-is (not folded into integrations in v2).

## 3. Secrets at rest

- Ciphertext layout: `nonce(24 bytes) || secretbox(plaintext)`; plaintext is
  a JSON object of the kind's secret fields, e.g. `{"token":"glpat-…"}`.
  Empty secrets = empty bytea.
- Key: decoded `APOTHECARY_MASTER_KEY`. Wrong key at decrypt (any integration)
  fails the operation with a clear "master key mismatch" error — never
  silently returns garbage.
- **API masking (MUST):** GET responses render each secret field as
  `"••••" + last 4 chars` (fields ≤ 8 chars: just `"••••"`), under a
  `secrets` object separate from `config`. The raw value never leaves the
  engine: not in API responses, events, logs, Coder parameters, or telemetry
  attributes.
- **Update semantics (MUST):** PUT bodies carry `config` (full replace) and
  `secrets` (merge): keys present replace, keys absent keep the stored
  value, key set to `""` deletes. This lets the UI resubmit a form without
  ever having held the secret.

## 4. Kind registry & capability surface

New package `internal/kinds`. A **kind** describes one integration type and
constructs adapters from stored config+secrets.

```go
type Config struct {
    Values  map[string]any    // integrations.config
    Secrets map[string]string // decrypted secretbox payload
}

type Field struct {
    Name, Label, Placeholder string
    Secret, Required         bool
    Default                  string
}
type Descriptor struct {
    Port, Kind, Label string
    Fields            []Field // drives the generic CPUI form (§11)
}

type ForgeKind interface {
    Descriptor() Descriptor
    Account(cfg Config) (ForgeAccount, error)                    // account-level client
    Forge(cfg Config, repo ports.RepoRef) (ports.Forge, error)   // project-bound port instance
}
type ForgeAccount interface {
    Test(ctx context.Context) error
    ListRepos(ctx context.Context) ([]RepoInfo, error) // project wizard picker
    GitCredential() (user, pass string)                // bootstrap step 3 (§6.2)
    EgressDomains() []string
}
type RepoInfo struct{ Name, URL, Locator, DefaultBranch string }

type ProvisionerKind interface {
    Descriptor() Descriptor
    New(cfg Config, deps ProvisionerDeps) (Provisioner, error)
}
type Provisioner interface {
    ports.EnvProvisioner
    Test(ctx context.Context) error // cheap: authenticated buildinfo/version call
    Close() error
}
type ProvisionerDeps struct{ RunsDir string; EnginePort int }

type HarnessKind interface {
    Descriptor() Descriptor
    New(cfg Config, envp ports.EnvProvisioner) (Harness, error)
}
type Harness interface {
    ports.AgentRunner
    Test(ctx context.Context) error          // static: config/auth material well-formed (no remote call)
    Binary() string                          // executable probed by envcheck (`command -v`)
    InstallLayer() []string                  // Dockerfile lines for §8.2 composition
    EgressDomains() []string
    SeedAuth(ctx context.Context, env ports.EnvID, providers []ProviderCreds) error // bootstrap step 2
    Prepare(ctx context.Context, env ports.EnvID) error                             // bootstrap step 4
}
type ProviderCreds struct {
    Kind    string
    EnvVars map[string]string // resolved name→value
    Secrets map[string]string
}

type ProviderKind interface {
    Descriptor() Descriptor
    New(cfg Config) (Provider, error)
}
type Provider interface {
    Test(ctx context.Context) error   // cheap authenticated GET (list-models or equivalent)
    EnvVars() map[string]string       // e.g. {"OPENROUTER_API_KEY": <key>}
    EgressDomains() []string
}
```

A `Registry` holds kinds by (port, kind); `serve` registers the real set,
`-dev` registers fakes (fake kinds wrap `internal/adapters/fake` and a
no-op Harness whose `InstallLayer` is empty). `ports.HarnessPreparer` and
the engine's type-assert are **deleted** — `Prepare` is now part of the
Harness contract (fake harness: no-op).

### 4.1 Kind catalog (v2 ships exactly these)

| port/kind | config fields | secret fields | Test | EgressDomains |
|---|---|---|---|---|
| forge/gitlab | `base_url` (default `https://gitlab.com`) | `token` | `GET /api/v4/user` | gitlab host |
| forge/github | `base_url` (default `https://api.github.com`) | `token` | `GET /user` | `github.com`, `api.github.com`, `codeload.github.com` |
| provisioner/coder | `url`, `template` (default `factory`), `extra_forwards` | `token` | authenticated buildinfo | — (gateway handling stays in the firewall script) |
| harness/opencode | `version` (default = pinned) | `auth_json` (optional, subscription/router auth) | static | `openrouter.ai` when auth_json mentions it; else none |
| harness/codex | `version` (default = pinned) | `auth_json` (optional, ChatGPT auth) | static | `api.openai.com`, `chatgpt.com`, `auth.openai.com` |
| provider/anthropic | — | `api_key` | list-models | `api.anthropic.com` |
| provider/openai | — | `api_key` | list-models | `api.openai.com` |
| provider/openrouter | — | `api_key` | key-info/list-models | `openrouter.ai` |

Provider env mapping: anthropic→`ANTHROPIC_API_KEY`, openai→
`OPENAI_API_KEY`, openrouter→`OPENROUTER_API_KEY`.

## 5. Port amendments (`internal/ports`)

- `RepoRef.ProjectID int` → **`Locator string`** (forge-kind-specific:
  GitLab numeric id as string, GitHub `owner/repo`). Filled by the repo
  picker; adapters parse their own locator. GitLab adapter keeps its
  bound-at-construction shape via `ForgeKind.Forge(cfg, repo)`.
- `EnvSpec` gains **`Image string`** and **`EgressDomains []string`** —
  both non-secret, both become Coder rich parameters (§8.3). Existing
  fields unchanged.
- `AgentSpec.Harness` stays (events/telemetry attribute).
- Engine config `GitToken` is deleted; bootstrap step 3 uses
  `ForgeAccount.GitCredential()` (gitlab: `("oauth2", token)`; github:
  `("x-access-token", token)`) written to `/factory/git-credentials` as
  today.

## 6. Engine changes

### 6.1 Resolver

```go
type Resolution struct {
    ProjectID, ProjectName string
    RepoRef       ports.RepoRef
    AgentsMDPath  string
    Commands      map[string]string       // setup now optional
    GitUser, GitPass string
    Envp          kinds.Provisioner
    Harness       kinds.Harness           // is the ports.AgentRunner
    HarnessKind   string
    Forge         ports.Forge
    Providers     []kinds.ProviderCreds
    ProviderEnv   map[string]string       // merged EnvVars of all providers
    EgressDomains []string                // §8.4 merged/deduped/sorted
    Image         ImageSpec               // {Tag, Dockerfile} or {Template custom} (§8)
}
type Resolver interface {
    Resolve(ctx context.Context, projectID string) (Resolution, error)
}
```

`engine.Config` shrinks to `{EngineURL, RunsDir, MRPollInterval}` plus the
`Resolver`, a `FlowSource` (§7) and a `SettingsSource` (§6.4);
`engine.New(cfg, store, resolver, flows, settings, channels)`. Scenario
tests use a static resolver over the fakes.

Adapter instances are cached per `(integration id, updated_at)`. A config
edit invalidates the cache for **future resolutions only**; runs in flight
keep their instances. Provisioner instances are refcounted by active runs
and `Close()`d when drained. Crash recovery resolves against **current**
project config and logs a warning when integrations changed since
`run_created` (flow-hash-drift precedent).

### 6.2 Bootstrap (v1 §11 order kept, executors swapped)

Step 2 = write `provider.env` from `Resolution.ProviderEnv` **and**
`Harness.SeedAuth(env, Providers)`. Step 3 uses `GitUser/GitPass`. Step 4 =
`Harness.Prepare(env)`. Step 6 runs `commands.setup` **only when present**;
absent = skipped (no event, log line only). `RunBootstrap` takes the
`Resolution`; envcheck reuses it unchanged.

### 6.3 Events, projection, preflight

- `run_created` gains `project_id` (projection fills `runs.project_id`;
  absent in old events → `""`; `rebuild-projections` tolerant).
- New event **`image_built{tag, cached}`** appended between
  `env_requested` and `env_ready` whenever the image pipeline ran (cached
  hit ⇒ `cached: true`). Projection ignores it; telemetry adds
  `factory.project_id` to run spans.
- Preflight at run creation becomes: resolve project (every integration id
  exists and constructs) → flow exists in the DB → **harness check**: flow
  stages that pin `harness:` must name the project's harness kind (one
  harness per project; mixed-harness flows out of scope) → `CheckAccess` →
  `FileExists` (agents_md, `.apothecary/agents/`) → `run_ref` resolution.
  **Model strings are deliberately NOT validated against provider kinds**:
  harnesses route `provider/model` themselves (openrouter serves
  `anthropic/…`), so any such check would false-negative. Refusals name the
  exact missing item (v1 §1 rule).
- Flow parser change: `harness` values are validated as slugs only
  (`^[a-z][a-z0-9_-]*$`); the `opencode` enum is deleted. Kind existence is
  preflight's job. `defaults.harness` omitted ⇒ resolved to the project's
  harness kind at run creation.

### 6.4 Settings

Keys and defaults: `limits.max_concurrent_runs` (3),
`limits.max_environments` (6), `reaper.destroy_completed_after` (`"48h"`,
positive duration), `reaper.destroy_abandoned` (`"never"`, only value).
`SettingsSource` reads the DB on every admission decision and reaper pass —
no cache, no watch. PUT validates the same rules v1 config enforced.

## 7. Flows in the DB

- `FlowSource` interface: `Get(ctx, name)`, `GetVersion(ctx, hash)`;
  engine's `map[string]*flow.Flow` is replaced by it. Loaded flows parse
  through the existing `internal/flow` parser at read time (cache by hash).
- Save path (`PUT /api/flows/{name}`): parse+validate first (line-precise
  errors to the UI verbatim); on success upsert `flows` and insert
  `flow_versions` (`ON CONFLICT DO NOTHING`). Names are immutable ids —
  rename = create + delete. Versions are never deleted.
- Delete refused (409, naming the blocker) if any `projects.default_flow`
  references the name or a non-terminal run pins it.
- Seeding: on boot with an empty `flows` table, insert the embedded
  `flows/v1.yaml`. `import-config` (§14) imports a `flows_dir`.

## 8. Workspace image pipeline & template

### 8.1 Base image (factory-owned)

`workspace/` (Dockerfile + `ask-human.mjs` + `init-firewall.sh`) is
embedded in the binary. The v2 base Dockerfile **removes** the Go
toolchain, `build-essential`/`make`/`unzip`, and the opencode install; it
**keeps** Ubuntu 24.04, ca-certificates/curl/git/ripgrep/jq/
openssh-client/sudo/iptables/iproute2/dnsutils, Node (shim runtime),
code-server, the `factory` user, shim + firewall + sudoers exactly as
today. Tag: `apothecary/base:<hex sha256[:16] of the embedded tree>`;
ensured on demand (`docker image inspect` → build from embedded context).
`APOTHECARY_BASE_IMAGE` overrides the ref entirely (expert; envcheck gates).

### 8.2 Composition (per project)

```
FROM <base-ref>
USER root
# ---- toolchain (project.workspace.dockerfile_fragment) ----
<fragment verbatim>
# ---- harness (<kind>) ----
<Harness.InstallLayer() lines>
USER factory
WORKDIR /home/factory
```

Fragment rules (MUST, enforced at project save **and** build): physical
lines whose first token (case-insensitive, after whitespace) is `FROM`,
`COPY`, `ADD`, `USER`, `ENTRYPOINT` or `CMD` are rejected — the build
context contains only the composed Dockerfile, and the engine owns
user/entry semantics. `RUN`/`ENV`/`ARG`/`LABEL`/comments are allowed.
Toolchain **presets** (go, node, python, jvm — exact snippets authored at
implementation) are `{name, dockerfile_lines, egress_domains}` triples
served by the API; inserting one appends its lines to the fragment and its
domains to `workspace.extra_domains` — visible and editable afterwards,
never tracked as a hidden dependency.

Tag: `apothecary/ws:<hex sha256[:16] of baseRef + "\0" + fragment + "\0" +
harness layer>` — unchanged inputs are a cache hit, no rebuild.

### 8.3 Build execution

`docker build -t <tag> -` with the Dockerfile as a one-file tar on stdin,
via the mounted socket. Ensured lazily at run start (emits `image_built`;
failure ⇒ `env_failed` with the build tail) and manually via
`POST /api/projects/{id}/build-image` (SSE log stream). Build logs also
land in `runs_dir/builds/<tag>.log`. Coder's docker provider uses the same
daemon, so built tags are directly usable (single-machine deployment).

### 8.4 Template & firewall

The Coder template is embedded and **engine-pushed**: template version
name = `v<sha256[:16] of embedded template tree>`; before Provision the
adapter compares the active version name and pushes when drifted
(idempotent, cached in memory). Template deltas from v1: `workspace_image`
and `extra_allowed_domains` become **`coder_parameter`s** (non-secret ✓)
wired to the container image and `APOTHECARY_EXTRA_DOMAINS`; everything else
(per-run network, NET_ADMIN, home volume, host-gateway for the Coder
agent) is unchanged.

The firewall allowlist is **fully derived**: engine computes
`EnvSpec.EgressDomains` = repo host (from repo.url) ∪ every resolved
adapter's `EgressDomains()` ∪ `workspace.extra_domains`, deduped/sorted.
`init-firewall.sh` drops its built-in provider/registry names and keeps
only mechanics: resolve-then-pin, gateway allowance on the Coder port,
`APOTHECARY_EXTRA_DOMAINS` consumption.

**Custom template escape hatch:** `workspace.template` set ⇒ the engine
never pushes, passes only v1's params (run_id/repo_url/branch), skips the
image pipeline entirely, and envcheck is the sole contract enforcer. This
is also the compatibility path `import-config` uses (§14).

## 9. Dynamic envcheck

Engine-side check runner (CLI becomes a wrapper). Check catalog — each
step reports `{id, title, status: pass|fail|skip, detail}`:

| id | derivation | pass criterion |
|---|---|---|
| `provision` | project's image/params (or custom template) | env ready |
| `tunnel` | always | workspace curls the engine's loopback probe endpoint through the reverse tunnel |
| `shim` | always | `/opt/apothecary/ask-human.mjs` present, `node` executes it with `--selfcheck` |
| `firewall_deny_http` | always | `curl https://example.com` fails |
| `firewall_deny_dns` | always | resolving a non-pinned name fails |
| `egress:<domain>` | one per derived allowlist entry | TCP+TLS connect succeeds — **any** HTTP status counts (401/403 fine); connectivity, not auth, is under test |
| `clone` | always | bootstrap step 3 succeeds |
| `harness` | harness kind | `command -v <Harness.Binary()>` succeeds |
| `seed_auth` / `prepare` | harness kind | both calls succeed |
| `setup` | only when `commands.setup` set | exit 0 |
| `rw_roundtrip` | always | binary-safe ReadFile/WriteFile round-trip |
| `links` | always | non-empty AccessLinks |
| `destroy` | unless `keep` | env destroyed |

API: `POST /api/projects/{id}/envcheck {keep?: bool}` → `202 {check_id}`
(409 if one is already running for the project); `GET
/api/envchecks/{id}` → status + steps; `GET /api/envchecks/{id}/events` →
SSE. Results are in-memory (last 20) + a log file under
`runs_dir/envchecks/`; not events, not runs — diagnostics, not history.
CLI: `apothecaryd envcheck -project <name|id> [-keep]` (drives the same code
path; requires the env vars of §1).

## 10. HTTP API (delta)

| method+path | body → response |
|---|---|
| GET `/api/kinds` | → descriptor list (§4 `Descriptor`, drives forms) |
| GET/POST `/api/integrations` | POST `{port, kind, name, config, secrets}` → 201; secrets masked in all responses (§3) |
| GET/PUT/DELETE `/api/integrations/{id}` | PUT per §3 merge rules; DELETE → 409 naming referencing projects |
| POST `/api/integrations/{id}/test` | → `{ok, error?}` (runs the kind's `Test`) |
| GET `/api/integrations/{id}/repos` | forge only → `[]RepoInfo` (wizard picker) |
| GET/POST `/api/projects` · GET/PUT/DELETE `/api/projects/{id}` | fragment rules validated on save (§8.2); DELETE requires all runs archived |
| POST `/api/projects/{id}/preflight` | → step results (§6.3 checks, no provisioning) |
| POST `/api/projects/{id}/envcheck` · GET `/api/envchecks/{id}[/events]` | §9 |
| POST `/api/projects/{id}/build-image` | → SSE build log (§8.3) |
| GET `/api/presets` | → toolchain presets (§8.2) |
| GET/POST `/api/flows` · GET/PUT/DELETE `/api/flows/{name}` | PUT validates first; errors verbatim from the parser |
| POST `/api/flows/validate` | `{yaml}` → `{ok, errors[]}` (line-precise) |
| GET `/api/flows/{name}/versions` · GET `/api/flow-versions/{hash}` | history / exact text |
| GET/PUT `/api/settings` | §6.4 keys, validated |
| POST `/api/tickets` | body gains **`project_id` (required)**; `flow` defaults to the project's `default_flow` |
| GET `/api/runs` | rows gain `project_id`; optional `?project=` filter |

Auth posture unchanged (loopback bind, per-run ping tokens). All v1 routes
otherwise unchanged.

## 11. CPUI (M8 scope)

Settings area: **Integrations** (list grouped by port; generic form
rendered from `Descriptor.Fields` — secret inputs write-only with
"set/••••1234" state; Test button per row), **Projects** (wizard: name →
forge pick → repo picker via `ListRepos` → branch/agents_md → commands
(setup optional, prominent) → provisioner/harness/providers pick →
workspace editor with preset insert buttons + extra_domains → default
flow; detail page: preflight, envcheck panel with live steps, build-image
log), **Flows** (list, plain `<textarea>` editor + Validate + Save,
version history with diff-less view of exact text — a code editor widget
is a later upgrade, recorded), **Settings** (limits/reaper), **Channels**
(existing). Ticket form gains the project dropdown. No component library
(v1 §0 rule holds).

## 12. GitHub forge adapter

REST v3, `Authorization: Bearer <token>`, `Accept:
application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`. Locator
= `owner/repo`.

| port call | implementation |
|---|---|
| `Account.Test` | `GET /user` |
| `ListRepos` | `GET /user/repos?per_page=100&affiliation=owner,collaborator,organization_member`, paginated |
| `CheckAccess` | `GET /repos/{locator}` and require `permissions.push` |
| `FileExists` | `GET /repos/{locator}/contents/{path}?ref=<default>`; trailing-slash dirs: response is an array |
| `CreateDraftMR` | `POST /repos/{locator}/pulls` `{title, head, base, body, draft:true}` → number/html_url; **no `Draft:` title prefix** (native flag — deviation from the GitLab adapter, recorded) |
| `MRState` | `GET /repos/{locator}/pulls/{n}`: `merged_at` set → merged; `state:"closed"` → closed; else open |
| `GitCredential` | `("x-access-token", token)` |

Token guidance (docs + Test error text): classic PAT `repo` scope, or
fine-grained with Contents:RW + Pull requests:RW.

## 13. Codex harness adapter

Behavior is normative; exact flags verified against the pinned version at
implementation and drift recorded (M3 precedent).

- `InstallLayer`: `npm install -g @openai/codex@<pin>`. `Binary`: `codex`.
- `Run`: non-interactive exec mode with the JSON event stream, model from
  `AgentSpec.Model`, cwd = repo, approvals fully disabled (must never
  prompt). Prompt travels via workspace file (v1 pattern). Sandbox: the
  container + firewall **is** the sandbox; use the least-privileged codex
  mode that still allows repo writes + command exec (expected:
  workspace-write; verified at impl).
- `Resume`: codex's exec-resume with the recorded session id; reply is the
  fixed continuation message (v1 semantics).
- Agent identity: codex has no named-agent registry — each stage runs with
  the agent spec's `prompt` delivered as system-level instructions
  (mechanism — instructions file/profile vs prompt prefix — chosen at
  impl). The §3.2 engine prompt is unchanged. `read_only`/`allow_bash`
  are enforced only at instruction level — **weaker than opencode;
  documented residual** (v1 §5.1 precedent; mitigation unchanged: branch
  output is human-reviewed).
- `Prepare`: writes codex config (`$CODEX_HOME/config.toml`) registering
  the `ask_human` MCP stdio server (`node /opt/apothecary/ask-human.mjs`) and
  non-interactive defaults. Nothing lands in the repo.
- `SeedAuth`: `OPENAI_API_KEY` arrives via provider.env (openai provider);
  optional `auth_json` secret (ChatGPT subscription) written to
  `$CODEX_HOME/auth.json` (0600).
- `Usage`: turns = model-turn events; input/output tokens from the
  stream's usage payloads; **`CostUSD` = 0** (codex reports no dollar
  cost) — budgets ride `max_total_turns`, which §3 designed for exactly
  this.

## 14. `apothecaryd import-config`

`import-config -config /etc/factory/factory.yaml [-force]` — one-shot,
requires the v1 env vars (`GITLAB_TOKEN` etc.) present since the file only
names them. Refuses a non-empty DB (any project) without `-force`.
Creates: forge/gitlab, provisioner/coder, harness/opencode (auth.json file
content read and stored as the `auth_json` secret), provider/anthropic +
provider/openai for set env vars; one project from `repo` + `project.
commands` + `default_flow: v1`, with **`workspace.template` set to the
configured Coder template** (§8.4 compatibility path — behavior stays
byte-identical until layered images are adopted); flows from `flows_dir`;
settings from `limits`/`reaper`. Prints a summary naming every created
row.

## 15. Distribution (`deploy/`)

- **Image** (multi-stage): build CPUI (node) → build apothecaryd
  (CGO_DISABLED) → runtime layer containing apothecaryd, `/opt/apothecary/
  cpui`, the pinned `coder` CLI binary, the `docker` CLI, ca-certs, git
  not required. `ENTRYPOINT ["apothecaryd","serve"]`; `APOTHECARY_CPUI_DIR`
  preset. Version-tagged; `latest` floats.
- **Compose** (reference): services `apothecaryd` (`network_mode: host`,
  mounts `/var/run/docker.sock` and a `factory-runs` volume, `env_file`)
  and `postgres:16` (`network_mode: host`, listening `127.0.0.1:15432` —
  non-default port per the observability-stack collision precedent,
  `factory-pgdata` volume, healthcheck; apothecaryd `depends_on` healthy).
  Host networking keeps `127.0.0.1:8080` semantics and the reverse tunnel
  byte-identical to the bare-metal deployment (the `-R` target is the
  engine's own loopback wherever it runs). Bridge mode is a documented
  alternative, not the default.
- Coder server is **not** bundled; CLI↔server version skew guidance goes
  in the README (pin CLI per release; Coder tolerates minor skew).
- Migrations keep applying at boot — upgrade = pull new image + restart.

## 16. Security model (v2 recap)

Boundaries unchanged (engine/host trusted, workspace untrusted). New
enforcements: secrets encrypted at rest (secretbox, §3), write-only over
the API, never in events/logs/parameters/telemetry; template invariants
engine-pushed; user-supplied Dockerfile fragments restricted (§8.2) and
everything user-customizable gated by deterministic envcheck (§9).
**Residuals (documented):** `APOTHECARY_MASTER_KEY` lives in the container
env (operator-controlled host; the standard trade-off); the docker socket
mount makes the engine root-equivalent on the host — it joins Coder as
literal trusted control plane; fragments execute as root at build time —
acceptable while operator == author (single-user; revisit before any
multi-user work); codex `read_only` is instruction-level only (§13);
per-run tokens, firewall, tunnel, git-push exfil residual unchanged from
v1 §15.

## 17. Milestones with acceptance criteria

| # | deliverable | done when |
|---|---|---|
| M7 | migration 0002, secretbox + `keygen`, `internal/kinds` registry (gitlab/coder/opencode/anthropic/openai/openrouter + dev fakes), CRUD API with masking, resolver + FlowSource + settings in engine, env-var bootstrap, `import-config`, `deploy/` image + compose | all M1 scenario tests green through the resolver; `import-config` on Lu's live factory.yaml then serve **from env vars alone** completes a real run via the custom-template path; `docker compose up` + `-dev` drives a full fake flow inside the container; an automated test greps API responses, event payloads and logs for planted secret values and finds none |
| M8 | CPUI: integrations (generic forms + Test), project wizard (incl. `ListRepos` picker), flow editor + validate + versions, settings page, ticket project picker | fresh DB → everything configured through the UI only → full run ends in a real MR; a wrong token surfaces in the UI via the Test button, not at run time |
| M9 | base-image split + embedded builds, fragment + presets, harness `InstallLayer`, content-addressed tags + `image_built`, engine-pushed template with image/domain parameters, derived firewall list, dynamic envcheck (API + UI + CLI wrapper) | two opencode projects with different toolchains (Go preset, Python preset) both pass full envcheck; identical workspace config produces a cache hit (`image_built{cached:true}`); an induced missing egress domain and a wrong Coder token each produce a named failing step in the UI |
| M10 | forge/github kind + harness/codex kind | a GitHub-hosted project runs the full v1 flow to a **real draft PR**, `auto_complete` fires on merge within 90 s; a codex project runs plan→gate→build→verify with token usage populated and a kill -9 mid-build resume (M3 precedent); `image_built` for the codex project's image and `command -v opencode` inside it fails (envcheck step proves absence); neither adapter changed engine code beyond kind registration |
| M11 | release: versioned images published, README install path, upgrade notes | clean machine + external Coder + tokens: follow only the README → `docker compose up` → configure in UI → merged MR |

Build strictly in order; each milestone merges only when its criteria
pass.

## 18. Out of scope for v2

Multi-user/auth, OAuth forge connect, remote (non-loopback) access,
agent-spec editing in the UI, mixed-harness flows, additional provisioners
(compose/k8s), multi-node Coder, registry-published workspace images, flow
marketplace anything, folding `channels` into `integrations`.
