# opencode adapter (M3, §5.2) — flag/behavior drift vs the design sketch

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
