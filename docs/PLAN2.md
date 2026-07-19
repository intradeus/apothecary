# Apothecary v2 — Config Plane & Service Distribution (light plan)

> Status: **draft — awaiting approval**. Once approved, a detailed execution
> spec follows (DESIGN2.md: exact schemas, port signatures, API routes,
> migration files), same process as v1.

## 1. The idea

v1 proved the factory: ticket in, reviewed MR out, one project, one user,
config on Lu's disk. v2 turns it into an installable service. The CPUI
becomes the **only entry point for user configuration** — forge accounts,
provisioner, harness, model providers, projects, flows, workspace contents
are all created and edited in the UI and stored in Postgres. The daemon
itself boots from a handful of env vars and ships as a Docker image.

Nothing about the run machine changes: events stay the source of truth, the
ask_human shim, firewall, reverse tunnel, bootstrap sequence and bounded
loops are untouched. v2 is about who supplies the configuration and how the
software is installed, not what it does.

## 2. The config boundary (the one principle)

Every knob belongs to exactly one of three planes:

| Plane | Owner | Mechanism | Contents |
|---|---|---|---|
| **Deployment** | operator installing the service | env vars on the container | `FACTORY_LISTEN`, `FACTORY_POSTGRES_DSN`, `FACTORY_MASTER_KEY`, `FACTORY_RUNS_DIR`, `FACTORY_OTEL_ENDPOINT` |
| **Policy** | user, in the UI | `settings` table | concurrency/env limits, reaper windows |
| **User config** | user, in the UI | DB tables below | integrations, projects, flows, workspace contents, channels |

`/etc/factory/factory.yaml` is retired: what was deployment moves to env
vars (12-factor, natural for a Docker service — decision #2/#3), what was
user config moves to the DB. `flows_dir` becomes an import source only.

## 3. Data model

Generalizing the pattern the channels registry already proved (DB-backed,
UI-CRUD'd, kind-discriminated factories restored at boot):

```sql
integrations(id pk, port text, kind text, name text,
             config jsonb, secrets bytea,          -- secrets encrypted (§4)
             created_at, updated_at)
-- port ∈ forge|provisioner|harness|provider
-- kind ∈ gitlab|github|coder|opencode|codex|anthropic|openai|openrouter…

projects(id pk, name text,
         forge_integration_id, provisioner_integration_id,
         harness_integration_id, provider_integration_ids jsonb,
         repo jsonb,        -- {url, project_id, default_branch, agents_md_path}
         commands jsonb,    -- {setup?, verify, …} — setup now optional (§8)
         workspace jsonb,   -- {dockerfile_fragment, extra_domains[], template?} (§6)
         default_flow text, created_at, updated_at)

flows(name pk, yaml text, hash text, updated_at)
flow_versions(hash pk, yaml text, created_at)  -- content-addressed; runs
                                               -- already pin flow_hash, now
                                               -- the exact text is always
                                               -- retrievable

settings(key pk, value jsonb)                  -- limits + reaper policy

runs += project_id
```

An **integration** is a credentialed connection (GitLab host + PAT, Coder
URL + token, OpenRouter key). A **project** picks one integration per port
and adds repo/commands/workspace specifics — which is exactly the intended
UX flow: connect forge → pick provisioner → pick harness → pick providers →
configure each → create project. Multi-project falls out of `runs.project_id`
almost for free.

The existing `channels` table already has this shape; it may fold into
`integrations` later but is not forced to move.

## 4. Secrets

UI-entered secrets land in the DB, so the v1 plaintext-jsonb residual
(§9/§15) goes away. NaCl secretbox (`golang.org/x/crypto` — new dependency,
recorded here per the no-new-deps rule) with a 32-byte key from
`FACTORY_MASTER_KEY` (env var — decision #2). That key is the one secret
that stays outside the UI; `factoryd keygen` generates it at install time.

API rules: secret fields are **write-only** — responses return a mask
(`•••• last4`), never the value; secrets never appear in events, logs, or
Coder template parameters (§11 rule unchanged).

## 5. Adapter registries and capability declarations

Every port gets a registry of kinds, each registered kind declaring:

| Capability | Ports | Purpose |
|---|---|---|
| `New(config, secrets)` | all | construct the adapter (today hand-wired in main.go) |
| `Test(ctx)` | all | cheap auth/reachability probe → the UI "Test connection" button (`Forge.CheckAccess` generalized) |
| `EgressDomains()` | all | firewall allowlist contributions — the openrouter lesson: adapters declare their domains, users don't remember them |
| `InstallLayer()` | harness | Dockerfile lines installing the harness (opencode → `npm i -g opencode-ai@pin`; codex → its installer) |
| `SeedAuth(env, providers)` | harness | write provider credentials in the harness's native format at bootstrap step 2 (replaces the `opencode_auth_json` file path: the opencode adapter *generates* auth.json from provider integrations) |
| `Prepare(env, agentspecs)` | harness | harness-native config generation (formalizes the existing `ports.HarnessPreparer` type-assert) |
| env-var mapping | provider | which env vars to write into provider.env (generalizes the hardcoded ANTHROPIC/OPENAI map) |

The engine stops receiving one global `envp/runner/forge` and gains a
**resolver**: `project_id → adapter set`, instances cached per integration
id. Config edits apply to **new runs only**; crash recovery resolves against
current config and warns on drift — same precedent as flow-hash drift.

Flow `harness:`/`model:` references are validated at **run-creation
preflight** against the project's integrations, not at flow-save time: a
flow mentioning codex is valid data, just not runnable on a project without
codex — preflight names the missing integration (§1 philosophy: refuse
naming the exact missing item).

## 6. Workspace: layered image, engine-pushed template

Workspaces stay per-run disposable cattle — users never point the factory at
a pre-created workspace (that would break isolation, concurrency, and the
reaper). What users own is the **image contents**; dropdowns exist at the
template/image level, not the workspace level.

- **`apothecary/base` (factory-owned, versioned):** Ubuntu + git/ripgrep +
  Node runtime (the shim needs it) + ask_human shim + init-firewall.sh +
  non-root `factory` user + code-server. The invariants, nothing else — no
  Go toolchain, no harness.
- **Toolchain layer (user-owned):** a Dockerfile fragment in
  `projects.workspace`, edited in the UI, appended after
  `FROM apothecary/base:<v>`. Starter presets (Go, Node, Python, JVM…) ship
  as insertable, editable snippets — power without a rigid checkbox matrix.
- **Harness layer (adapter-owned):** the project's harness contributes
  `InstallLayer()` automatically. Pick codex → no opencode in the image.
- **Build:** the engine composes the Dockerfile and runs `docker build`
  through the mounted Docker socket (§9), tagged content-addressed
  (`apothecary/ws:<sha-of-inputs>`) so unchanged config is a no-op rebuild;
  build logs stream to the UI. Built images land on the same daemon Coder's
  docker provider uses (single-machine deployment, §9).
- **Template stays engine-owned and becomes engine-pushed:** it encodes
  invariants (per-run network, NET_ADMIN for firewall init, tunnel plumbing,
  host-gateway mapping for the Coder agent) that must not silently break.
  `workspace_image` and `extra_allowed_domains` move from template
  *variables* to per-workspace *parameters* (both non-secret). Expert escape
  hatch: a project may name a custom template — legality enforced by
  envcheck (§7), not by trust.
- **Firewall allowlist becomes fully derived:** forge contributes the repo
  host, providers their API domains, harness its domains, toolchain presets
  their registries (`proxy.golang.org`, `registry.npmjs.org`, …), plus the
  project's free-form `extra_domains`.

## 7. envcheck becomes the dynamic compliance gate

What makes user customization safe: users may change image and template
because envcheck **proves the contract deterministically**.

- The check list derives from project config instead of being hardcoded:
  positive probes = the union of the adapters' `EgressDomains()`; negative
  probes (example.com blocked, foreign DNS dead) stay fixed; plus shim
  present, tunnel round-trip, clone, `setup` (when configured), harness
  binary on PATH.
- Exposed as an API-triggered **check run**
  (`POST /api/projects/{id}/envcheck`) with streamed results in the UI —
  provision → check → destroy, with a keep-for-inspection option.
  Per-integration `Test()` covers the cheap cases without provisioning.
- The CLI command remains as a thin wrapper over the same code.

## 8. Flows, projects, tickets

- Flows move to the DB (content-addressed versions, §3). UI: YAML editor +
  a validate endpoint reusing the existing parser — its line-precise errors
  were built for exactly this. Editing never rewrites history: each save is
  a new `flow_versions` row; running rows pin their hash as today.
- `POST /api/tickets` gains `project_id`; the ticket form gets a project
  picker.
- The Forge port gains `ListRepos(ctx)` so project creation offers a repo
  dropdown after the forge account connects. PAT auth now; OAuth only if
  multi-user ever happens.
- Agent specs (`.factory/agents/`) **stay in the target repo** — "prompts
  versioned with the code they govern" is a v1 principle worth keeping.
  Revisit only on real friction.
- `commands.setup` becomes **optional** (recommendation, veto-able): empty
  means bootstrap step 6 is skipped. The wizard still presents the field
  prominently — the "agents never repair the environment" principle depends
  on setup doing its job when one is needed.

## 9. Distribution: apothecary as a Docker service (decision #3)

- **One image** containing factoryd, the built CPUI, and the pinned `coder`
  CLI; a reference `docker-compose.yaml` runs it with Postgres. Host network
  mode by default (Linux target; precedent: the observability stack), so
  `listen 127.0.0.1:8080` behaves exactly as today; Postgres binds a
  loopback port. Bridge mode with published ports is the portable
  alternative, detailed in DESIGN2.
- **Docker socket mounted** into the factoryd container for §6 image builds.
  The engine was already trusted control plane; the socket makes that
  literal — documented residual alongside Coder's own socket (§15 posture).
- The reverse tunnel is namespace-agnostic: `coder ssh -R` targets the
  engine's own loopback wherever the engine runs, so containerizing factoryd
  changes nothing about the workspace→engine path.
- `FACTORY_RUNS_DIR` is a volume mount; migrations keep applying at boot.
- Coder itself stays an **external integration** (URL + token), user-managed
  — not bundled in the compose stack.

## 10. Migration

`factoryd import-config -config factory.yaml` — one-shot: reads the v1 file
+ env vars, creates the integrations, the one project, imports `flows_dir`
into the flows table. The current setup survives verbatim; the file is
retired afterwards.

## 11. Milestones (continuing v1 numbering)

| # | deliverable | done when |
|---|---|---|
| M7 | config plane: schema + secretbox + `keygen`, registries with `Test()`, CRUD API with write-only secrets, per-project resolver in the engine, env-var bootstrap, `import-config`, factoryd image + compose | after import, serve boots from env vars alone; `docker compose up` with `-dev` adapters drives a full fake flow through the containerized engine; all M1 scenario tests green through the resolver; grep proves no secret in any API response, event, or log |
| M8 | CPUI config surface: integration pages with Test buttons, project wizard (connect forge → repo picker → commands → adapter picks), flow editor with validation + version history, settings page (limits/reaper), project picker on tickets | fresh DB → configure everything through the UI only → full run ends in a real MR, no yaml/env edits beyond the deployment vars |
| M9 | workspace composability: base image split, toolchain fragment + presets, harness install layer, content-addressed builds with streamed logs, engine-pushed template with image/domains as parameters, derived firewall list, dynamic envcheck + check-run API/UI | two projects with different toolchains (Go and Python) both pass envcheck; the non-opencode project's image contains no opencode; a wrong token or missing domain produces a named, actionable failure in the UI |
| M10 | prove the ports: GitHub forge adapter **and** codex harness adapter (decision #1) | a GitHub-hosted project runs the full flow to a real draft PR incl. `auto_complete` on merge; a codex project runs plan→build→verify with `Usage` populated and session resume verified (kill mid-build, M3 precedent); neither adapter required engine changes beyond registration |
| M11 | ship it: versioned images, install docs, release process | clean machine + external Coder + tokens: `docker compose up` → configure in UI → merged MR, following only the README |

Build strictly in order; each milestone merges only when its criteria pass.
Codex flag/behavior drift vs. assumptions gets recorded in
IMPLEMENTATION_NOTES at implementation time (M3 precedent: behavior, not
flags, is normative).

## 12. Decisions taken

1. **M10 = GitHub forge + codex harness, both committed** (Lu needs both).
2. **Master key via env var** (`FACTORY_MASTER_KEY`) — fits the Docker
   service deployment.
3. **Apothecary ships as a Docker image** — drove §2 (env-var bootstrap),
   §9 (packaging, socket mount, host networking), M7/M11 acceptance.
4. **`commands.setup` optional** — recommended in §8, awaiting veto.

## 13. Out of scope (deliberately)

Multi-user / auth / OAuth forge connect, remote access, agent-spec editing
in the UI, multi-node Coder, additional provisioners (compose/k8s), flow
marketplace anything.
