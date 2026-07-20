# DESIGN3 — Agents, Flows, Plan Review (execution spec)

> Implements PLAN3.md (approved). Decisions locked: per-project copiable
> agents; `@xyflow/svelte`; hard retire of repo `.apothecary/agents/`;
> `marked`. Milestones M12 → M13 → M14, each gated on explicit go.

## 1. M12 — Agent library

### 1.1 Schema (migration 0005)

```sql
CREATE TABLE agents (
    project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        text NOT NULL,   -- lowercase slug (agentspec.validSlug)
    description text NOT NULL DEFAULT '',
    read_only   bool NOT NULL DEFAULT false,
    allow_bash  bool NOT NULL DEFAULT false,
    prompt      text NOT NULL,
    updated_at  timestamptz NOT NULL,
    PRIMARY KEY (project_id, name)
);
-- Content-addressed history, the flows pattern. hash = sha256[:16] of the
-- canonical spec JSON (name,description,read_only,allow_bash,prompt —
-- project_id excluded: identical prompts share a version across projects,
-- which is exactly right for copy-tracking analytics).
CREATE TABLE agent_versions (
    hash       text PRIMARY KEY,
    name       text NOT NULL,
    spec       jsonb NOT NULL,
    created_at timestamptz NOT NULL
);
```

### 1.2 confstore

```go
type Agent struct {
    ProjectID   string    `json:"project_id"`
    Name        string    `json:"name"`
    Description string    `json:"description"`
    ReadOnly    bool      `json:"read_only"`
    AllowBash   bool      `json:"allow_bash"`
    Prompt      string    `json:"prompt"`
    Hash        string    `json:"hash"` // current version
    UpdatedAt   time.Time `json:"updated_at"`
}
```

Service methods (validation via `invalidf`, statuses via the v2 error
taxonomy):

- `ListAgents(ctx, projectID)` / `GetAgent(ctx, projectID, name)`
- `SaveAgent(ctx, a)` — upsert. Validates: project exists, name is a
  slug (reuse `agentspec` rules), prompt non-empty. Writes the
  `agent_versions` row (INSERT … ON CONFLICT DO NOTHING — hash PK).
- `DeleteAgent(ctx, projectID, name)` — **guard**: refuse
  (`ErrReferenced`) when the project's `default_flow` has an agent stage
  naming it. Other flows are advisory only (preflight catches them at
  run creation).
- `CopyAgents(ctx, dstProjectID, srcProjectID, names []string)` —
  empty `names` = all. Refuses (`ErrConflict`) on any name collision in
  dst; no silent overwrite (delete first if replacement is intended).
- `AgentVersions(ctx, projectID, name)` / `AgentVersion(ctx, hash)`.

`DB` interface + pgstore + mem grow accordingly (mem mirrors pg
semantics, per the v2 parity rule).

### 1.3 Repo import (one-time transition)

`ports.Forge` gains two read methods (all four adapters):

```go
ListDir(ctx, repo RepoRef, path string) ([]string, error)  // file names
ReadFile(ctx, repo RepoRef, path string) ([]byte, error)
```

- gitlab: repository tree API + raw file API (existing PathEscape
  convention). github: contents API (dir array / base64 file). fake:
  in-memory map.
- `POST /api/projects/{id}/agents/import-repo` lists
  `.apothecary/agents/`, parses each `*.yaml|*.yml` with
  `agentspec.Parse`, saves per file; response reports
  `{imported: [names], skipped: [{file, error}]}`. Existing names are
  skipped (no overwrite), reported as such.

### 1.4 Harness plumbing (the cutover)

- `kinds.Harness.Prepare(ctx, env)` →
  `Prepare(ctx, env, specs []*agentspec.Spec)`.
- opencode/codex adapters: `PrepareEnv(ctx, env, specs)` writes the MCP
  config (`opencode.json` / `config.toml`) **always** — the ask_human
  shim must exist even for zero agents — and one agent file per spec.
  `readSpecs`, the `===APOTHECARY-SPEC` marker and the repo exec path
  are **deleted**.
- `engine.Resolution` gains `Agents []*agentspec.Spec` and
  `AgentHashes map[string]string`; the resolver loads the project's full
  agent set at Resolve. `Resolution.Prepare` keeps its
  `func(ctx, env) error` shape — the resolver closes over the specs.
- Bootstrap/envcheck call sites unchanged (closure), so
  `apothecaryd envcheck` keeps working; its `prepare` step detail notes
  the agent count.

### 1.5 Engine

- `event.StageStarted` gains `Agent string` and
  `AgentHash string` (omitempty). `stageAgent` populates both from
  `res.AgentHashes[st.Agent]` — the analytics hook: outcome events are
  now joinable to the exact prompt version.
- Preflight: the `repo_file:.apothecary/agents/` check is **removed**
  (AGENTS.md check stays). New check, same shape as run_ref: every
  `agent` stage in the flow names an agent the project has —
  `agent:<name>` step in PreflightProject, refusal in CreateRun
  preflight.
- Dev seeding (`-dev`): seeds two fake agents on the fake project so the
  dev flow keeps running.

### 1.6 API + UI

Routes (config plane):

```
GET    /api/projects/{id}/agents
PUT    /api/projects/{id}/agents/{name}        {description, read_only, allow_bash, prompt}
GET    /api/projects/{id}/agents/{name}
DELETE /api/projects/{id}/agents/{name}
GET    /api/projects/{id}/agents/{name}/versions
GET    /api/agent-versions/{hash}
POST   /api/projects/{id}/agents/copy          {from_project_id, names?}
POST   /api/projects/{id}/agents/import-repo
```

UI: an **Agents** tab on the project page (`/projects/[id]`): list with
read_only/allow_bash badges, editor (slug name locked after create,
description, toggles, prompt textarea), version history with plain-text
diff (flow-versions pattern), "Copy from project…" dialog, "Import from
repo" button. Delete confirms and surfaces the default-flow guard error.

### 1.7 Cleanup + docs

`examples/target-repo/.apothecary/` moves to `examples/agents/*.yaml`
(import samples). README/DESIGN2 references to the repo contract dir are
updated: a target repo now needs only `AGENTS.md`. IMPLEMENTATION_NOTES
records the breaking change.

### 1.8 Acceptance (M12)

Fake-stack e2e: create agents via API → run → fake harness receives the
specs (not repo files) → `stage_started` carries `agent_hash`. Editing
the prompt changes the hash on the next run, old runs keep the old hash.
Preflight names a missing agent. Copy + import round-trip. Live: Lu's
project runs green with repo `.apothecary/` deleted.

## 2. M13 — Flow builder

### 2.1 Document model: explicit edge graph (parser + engine first)

The success chain becomes an **explicit edge**, uniform with
`on_fail`/`on_reject`, and the edge set is a registry so new kinds are
additive:

```go
// flow package
type Edge struct{ To string; Max int; Feed bool }
type Stage struct {
    ...
    Edges map[string]*Edge   // kind -> edge; kinds from the table below
}
var edgeKinds = map[string]edgeKindSpec{
    "on_success": {hasMax: false, hasFeed: false, mustTerminate: true},
    "on_fail":    {hasMax: true,  hasFeed: true},
    "on_reject":  {hasMax: true,  hasFeed: true},
    // future: "on_timeout", "on_budget", … — one entry + one engine
    // raise-site each; parse, validation, routing, events generalize.
}
```

Rules:

- `on_success: <stage id | done>` — `done` is a reserved terminal id
  (stage names may not shadow it). Reaching a stage with
  `on_success: done` completes the run.
- **YAML sugar / back-compat**: an absent `on_success` defaults to the
  next stage in document order (last stage → `done`) — existing flows
  parse unchanged and hand-written YAML stays terse. The parse result is
  always fully explicit; the graph editor always *emits* explicit edges.
- Entry = the first stage in the document.
- **Success DAG rule**: the `on_success` graph must be acyclic and every
  stage must reach `done` through it. Loops remain possible only via
  edge kinds carrying `max` (`on_fail`/`on_reject`) — a success-only
  cycle through command stages would consume no turns and no cost, and
  no budget would ever bound it.
- Reachability: every stage reachable from entry via some edge, as
  today.

Engine: `advance()` (pos++) is replaced by following the stage's
`on_success` edge (`Index(to)`, `done` → complete); `takeEdge` and
`edgeCounts` already key by kind string and generalize as-is;
`routeAfterComplete` recovery follows the same edge. `EdgeTaken.Edge`
already carries the kind. Success transitions keep emitting
`stage_completed` + next `stage_started` (no extra edge event on the hot
path); replay is unchanged.

Flow hashes change when a flow is re-saved with explicit edges — normal
versioning, old runs keep their pinned hash. This document-model work
lands (with parser + engine + replay tests) **before** any editor code.

### 2.2 Layout storage (migration 0006)

```sql
ALTER TABLE flows ADD COLUMN layout jsonb;   -- {nodes: {id: {x,y}}, viewport?}
```

`FlowRow` gains `Layout json.RawMessage`. Layout is **excluded from the
hash** (hash stays sha of the YAML): moving a node never creates a flow
version. `PUT /api/flows/{name}` accepts optional `layout`; a PUT whose
YAML hash is unchanged updates layout only (no new version row — the
hash-PK `ON CONFLICT DO NOTHING` already guarantees this).

### 2.3 Graph ↔ document mapping

All three chains are explicit edges (§2.1). In the graph:

- One node per stage (typed: agent / command / human_gate / forge_mr),
  plus Start (entry) and Done (terminal) markers.
- Edges are typed and styled per kind — solid `on_success`, dashed
  `on_fail`, dashed `on_reject` (distinct colors/labels); the edge
  inspector shows the fields the kind's registry entry declares (`max`,
  `feed`). New kinds appear in the palette from the same registry.
- The editor enforces §2.1 validation live: success-DAG violations and
  unreachable nodes are marked on the canvas.
- Serialization writes stages in a topological order of the success
  graph with fully explicit edges.
- Flow-level panel: name (locked), budgets, defaults.

Serialization always goes through the canonical YAML → existing
`POST /api/flows/validate` (line-precise errors surface in the editor)
→ existing save pipeline. The YAML tab edits the same document; switching
tabs re-parses/re-renders.

### 2.4 Node inspectors + context picker

Editor context = a **project** (default: first project whose
`default_flow` is this flow; switchable). It feeds the dropdowns:

- agent node: agent (project agents), harness (project's harness kind,
  as the optional pin), model (`GET /api/integrations/{harness_id}/models`),
  max_turns, timeout, input (ordered multi-select: ticket / artifact
  path / $CHANGED), output, verdict toggle.
- command node: run_ref (project commands) or literal script.
- human_gate: ping type, payload (artifact path / mr_url), auto_complete.
- forge_mr: body artifact.

Every dropdown has a free-text mode — assistance, not a cage. Unknown
values (agent not in context project) render as warnings, not errors:
flows are portable; preflight is the authority per-project.

### 2.5 Dependency

`@xyflow/svelte` (first CPUI runtime dependency; bundled, no CDN).
Custom node components stay in-house — the library provides canvas,
drag, zoom, edge rendering only.

### 2.6 Acceptance (M13)

Document model: a legacy flow (no `on_success`) parses to the same
routing as before (replay/e2e green); an explicit branching flow —
success edges skipping over stages — routes correctly; a success-only
cycle is rejected at validation with the offending stages named.
Editor: a flow built purely by clicking runs green on the fake stack and
round-trips (graph → YAML → graph) byte-stable. Layout-only edits leave
`hash`/versions untouched. A YAML-tab edit reflects in the graph.
Validation errors from the server render on the offending node.

## 3. M14 — Plan review workbench

### 3.1 Artifact endpoint

```
GET /api/pings/{pid}/artifact
→ 200 {path, content}       (payload is an artifact path)
→ 409                        (payload is mr_url — UI keeps the link-out)
→ 404 / 410                  (unknown ping / env gone)
```

Engine method `PingArtifact(ctx, pingID)`: ping (active-run registry) →
run view (store reduce) → `resolver.Resolve(projectID)` →
`Envp.ReadFile(env, payloadRef)`, released immediately. Content capped
at 1 MiB (tail beyond); path comes from the flow document, never from
the request.

### 3.2 Review UI

Gate page (inbox → ping detail) for artifact payloads:

- `marked` renders the markdown; each rendered block keeps its source
  line range (marked lexer tokens carry positions).
- Select text (or click a block's margin) → comment box. Comments
  accumulate locally: `{quote, heading, lines, text}`; drafts persist in
  localStorage keyed by ping id.
- **Request changes** composes one deterministic feedback string:

```
PLAN REVIEW — N comments:

1. [§ <nearest heading>, lines <a>–<b>]
   > <quoted excerpt, trimmed to ~200 chars>
   <comment text>
...
```

  and submits it through the **existing** `answerPing(reject, feedback)`
  — the engine, edges, and feed prompt are untouched.
- **Approve** submits clean (comments discarded with a confirm).
  Approve-with-notes stays out of scope.

### 3.3 Acceptance (M14)

Live plan gate: plan renders in the UI, three anchored comments, one
submit → the builder stage's PREVIOUS ATTEMPT FEEDBACK contains all
three with their anchors. mr_url gates keep the current behavior.

## 4. Cross-cutting

- Tests per milestone: confstore agent CRUD/copy/guards, adapter
  Prepare-with-specs, preflight agent checks, httpapi routes (+ the
  secret-leak suite untouched), layout/hash independence, feedback
  composition (pure function, unit-tested).
- IMPLEMENTATION_NOTES entry per milestone, deviations recorded as
  always.
- No new deployment/env surface: everything ships in the image; CI
  unchanged.
