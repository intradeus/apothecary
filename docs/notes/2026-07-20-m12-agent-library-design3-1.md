# M12 — Agent library (DESIGN3 §1)

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
