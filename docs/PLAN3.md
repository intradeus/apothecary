# Apothecary v3 — Agents, Flows, Plan Review (light plan)

> Status: **approved** with decisions: (1) agents are per-project,
> copiable across projects; (2) `@xyflow/svelte` for the graph editor;
> (3) hard retire of the repo `.apothecary/agents/` contract;
> (4) `marked` for plan rendering. Execution spec: DESIGN3.md.

## 1. The idea

v2 made all configuration UI-driven — except the two things a user
actually iterates on daily: **agent prompts** (stuck in the target repo)
and **flows** (hand-written YAML), plus the single most frequent human
interaction, **plan review** (read a file in the repo, come back, type a
comment blind). v3 moves the iteration loop into the UI:

1. **Agent library** — agents live in apothecary (DB, versioned,
   UI-edited), not in the target repo. Evolving a prompt based on run
   feedback/analytics is a UI edit, not a commit to every repo.
2. **Flow builder** — flows are edited as a flowchart with validated
   dropdowns (agent, harness, model come from data the system already
   has), not free-typed YAML.
3. **Plan review workbench** — the plan artifact renders in the UI;
   comments anchor to sections; one submit composes them into a single
   structured feedback prompt for the on_reject edge.

The run machine stays untouched again: events, bootstrap, firewall,
gates, edges, budgets all as-is. v3 changes where authoring happens.

## 2. Agent library (repo → DB)

### Data model

```sql
agents(project_id text references projects(id) on delete cascade,
       name text,               -- slug, same charset rule as today
       description text,
       read_only bool, allow_bash bool,
       prompt text,
       updated_at timestamptz,
       primary key (project_id, name))
agent_versions(hash text pk,    -- content-addressed, like flow_versions
       project_id text, name text, spec jsonb, created_at timestamptz)
```

**Per-project** (decision #1): a flow's `agent: builder` resolves against
the run's project — exactly how `run_ref` already resolves against
`project.commands`. Flows stay global and portable; each project defines
what its `builder` is. Agents are **copiable across projects** in the UI
(and via API) so a good prompt spreads without retyping.

Versioning mirrors flows: every save writes an immutable
`agent_versions` row; `stage_started` events additionally record the
agent hash used, so analytics can correlate outcome with prompt version
— the feedback loop this feature exists for.

### Plumbing

- `agentspec.Spec` stays the internal currency. The confstore gains
  agent CRUD; the resolver loads the specs a flow references into the
  `Resolution`.
- `Harness.Prepare(ctx, env)` becomes `Prepare(ctx, env, specs)` — the
  opencode/codex adapters currently shell out to `cat` spec files from
  the repo (`readSpecs`); that path is deleted. They write agent
  markdown / role files from the specs they are handed.
- Preflight: the `.apothecary/agents/` repo check is replaced by "every
  agent referenced by the flow exists in the library" (same shape as the
  existing run_ref check). `AGENTS.md` stays a repo file — it describes
  the repo, not the agents.
- Flow save validates agent references against the library (warning, not
  error: library entries may be created after the flow).
- **Hard cutover**: the target-repo `.apothecary/` contract dir is
  retired entirely (v3 breaking change, like the v2 `.factory` rename).
  A one-time "import from repo" button on the agents page eases the
  transition (reads the current specs via the forge API).

### UI

Agents page: list, editor (name, description, read_only/allow_bash
toggles, prompt textarea), version history with diff view (reuse the
flow-versions pattern), delete guarded by "referenced by flow X".

## 3. Flow builder (YAML → flowchart)

### The document stays canonical

The YAML document model (stages + edges) already *is* a graph — v3
makes it fully explicit: the success chain becomes an `on_success` edge,
uniform with `on_fail`/`on_reject`, behind an extensible edge-kind
registry (a future `on_timeout` is a table entry, not a redesign; see
DESIGN3 §2.1 for the back-compat sugar and the success-DAG rule). The
builder is a **view over the same document**: the editor loads the flow,
renders nodes/edges, and every edit re-emits the canonical YAML through
the existing save/validate/version pipeline. Flow storage, hashing and
versioning are unchanged; the engine's routing generalizes from
"next in array" to "follow the edge". The YAML editor remains as an
"advanced" tab — the two views edit the same thing.

Node **layout** (x/y positions) is display metadata, not content: it
lives in a new `flows.layout jsonb` column and is *excluded from the
hash* (moving a node must not create a flow version). Missing layout →
auto-layout (topological, left-to-right).

### Nodes and dropdowns

| Node | Fields (all dropdown/validated where possible) |
|---|---|
| agent | agent (library dropdown), harness (kind dropdown, optional pin), model (dropdown via existing `GET /api/integrations/{id}/models`), max_turns, timeout, input (ordered picker: ticket / artifact / $CHANGED), output, verdict toggle |
| command | run_ref (dropdown from a chosen project's commands) or literal script |
| human_gate | ping type, payload (artifact / mr_url), auto_complete |
| forge_mr | body artifact |

Edges are drawn: `on_fail` / `on_reject` arrows carry max + feed toggle.
Budgets and defaults live in a flow-settings panel.

Model/run_ref dropdowns need a context (which harness integration's
models? which project's commands?): the editor has a **context picker**
(defaults to the first project using this flow). Free-text entry always
remains as the escape hatch — the dropdown is assistance, not a cage.

Validation is the existing server-side `flow.Parse` via
`POST /api/flows/validate`, surfaced live in the editor; the graph view
additionally greys unreachable nodes and marks missing edge targets.

### Editor tech (decision #2)

A graph editor needs drag, pan, zoom, and edge routing — weeks of
hand-rolled SVG polish, or the `@xyflow/svelte` package (mature,
tree-shaken into the bundle, no runtime CDN — CSP-clean).
**Recommendation: take the dependency.** It would be the CPUI's first
component-ish dependency; the no-component-library rule was about forms,
and this is exactly the case a specialized library earns its keep.

## 4. Plan review workbench

### Artifact into the UI

The workspace is alive while a gate waits — the engine can already read
from it (`Envp.ReadFile`, used for verdicts/MR bodies). New endpoint:

```
GET /api/pings/{id}/artifact
→ resolves ping → run → env, reads the gate's payload artifact
  (flow-declared path, not user input), returns
  {path, content, content_type}
```

The gate page renders the markdown (small renderer dep or the ~100-line
subset we need; decision #4) with headings/sections addressable.

### Anchored comments, single submit

- Selecting text (or clicking a section's margin) opens a comment box;
  comments accumulate locally: `{quote, section_heading, line_range,
  text}`.
- One **Request changes** submit composes them into a single feedback
  string — quote + location + comment per item:

```
PLAN REVIEW — 3 comments:

1. [§ Database changes, lines 12–18]
   > we will add a nullable column and backfill later
   Comment: backfill in the same migration; later never comes.

2. …
```

- That string rides the existing reject path unchanged — `on_reject`
  edge, `feed`, "PREVIOUS ATTEMPT FEEDBACK" prompt section. The engine
  doesn't learn anything new; the composition happens client-side.
- **Approve** stays clean (no comments). "Approve with notes" is out of
  scope for v3 — noted for later.
- Comments are UI state until submitted (a page reload loses them —
  acceptable v3 cut; localStorage draft if cheap).

Also applies to `final_review` gates whose payload is an artifact; for
`mr_url` payloads the link-out behavior stays.

## 5. Milestones

| # | What | Proves |
|---|---|---|
| **M12** | Agent library: tables + CRUD API/UI + versions, resolver/Prepare plumbing, preflight swap, repo-import button, delete `readSpecs` | a prompt edited in the UI changes the next run; stage events carry agent hash |
| **M13** | Flow builder: layout column, xyflow graph editor, node config panels with agent/model/run_ref dropdowns, YAML tab, live validation | a flow built entirely by clicking runs green; round-trips with the YAML tab |
| **M14** | Plan review workbench: ping artifact endpoint, markdown render, anchored multi-comment → single feedback submit | a plan gate is reviewed end-to-end without leaving the UI |

Order: M12 first (M13's agent dropdown needs the library). M14 is
independent — it can be pulled before M13 if the review pain is more
pressing than the YAML pain.

## 6. Decisions (resolved)

1. **Agent scope** — per-project, copiable across projects.
2. **Graph editor** — `@xyflow/svelte`.
3. **Repo cutover** — hard retire `.apothecary/agents/`, with a one-time
   import button.
4. **Markdown rendering** — `marked`.
