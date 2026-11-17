# Claude Code harness

A fourth harness kind, `claude-code`, driven like codex: a long-lived ACP
agent in the workspace sidecar, through the bridge
`claude-agent-acp` (@agentclientprotocol/claude-agent-acp) 0.101.0. The
tools bundle ships the bridge's standalone release archive per libc, which
also carries the `claude` CLI it runs (2.1.318, read from the binary);
the bridge's wrapper points `CLAUDE_CODE_EXECUTABLE` at the bundled
`claude` of its libc. The two pins move together:
`ClaudeAgentACPVersion` / `ClaudeCodeVersion` in `internal/project/kinds`,
the Dockerfile ARG held by `TestToolsDockerfilePinsMatchKinds`, both
versions held by `TestClaudeCodeConformance`.

## Why, and what now

- **Credentials: an Anthropic API key or a Claude login, never both.** The
  owner chose key + login. Claude Code prefers `ANTHROPIC_API_KEY` over a
  login, so a login held beside a key would silently never be used:
  `providers.Sink` gives both one sink (`auth:anthropic`) for this harness,
  and the existing one-credential-per-sink validation refuses the pair.
  The key rides provider.env; the login is rendered to
  `~/.claude/.credentials.json` (`claudeAiOauth`: access token, an empty
  refresh token, `expiresAt` in ms, the sign-in's scopes), 0600, removed
  at run end. The engine's Anthropic sign-in already uses Claude Code's
  own OAuth client, so its tokens are Claude Code's.
- **Refreshes reach a running Claude Code.** The plan's main unknown: the
  keeper pushes a new access token by seeding the file again, ahead of
  expiry, while the old token is still valid. The conformance test shows
  the running process's next turn already uses the new token, with no
  401 to provoke a re-read. No process restart is needed.
- **Role delivery** is the in-band preamble (`acpagent.RolePreamble`), as
  for codex and omp: through ACP a session cannot start as a Claude Code
  named agent. `read_only` / `allow_bash` stay instruction-level, the same
  residual as codex and omp.
- **Stage mode** is `bypassPermissions` for every stage; the bridge clamps
  it to asking when the process runs as root unless `IS_SANDBOX` is set,
  so the command line sets it (a repo devcontainer may run as root; the
  conformance container does). The sidecar's auto-approve stays the
  backstop. A repo's own `CLAUDE.md` and `.claude/` settings apply to the
  agent, as they would to a developer; one setting
  `disableBypassPermissionsMode` falls back to asking, noisily.
- **Ask pane** is `ReadOnlyByMode` with Claude Code's `plan` mode: a write
  in plan mode becomes a permission request, which the sidecar refuses in
  a read-only session (the conformance test refuses as the sidecar does,
  and the file stays unwritten; the same write lands in a stage session).
- **ask_human** rides every session (`Profile.MCPServers`), as omp's does;
  the server definition omp used inline is `acpagent.AskHumanServer`, now
  shared. Claude Code lists it as `mcp__ask_human__ask_human`.
  - Deviation found on the pinned version: Claude Code defers MCP tools
    behind its ToolSearch tool by default, unloaded, so ask_human was not
    in the request's tool list and a call by its name would fail. The
    command line sets `ENABLE_TOOL_SEARCH=false`: MCP tools load up front,
    as they do on the other harnesses.
  - `MCP_TOOL_TIMEOUT` is a day, so a blocked ask_human is not cut short
    (omp's `OMP_MCP_TIMEOUT_MS=0`, codex's `tool_timeout_sec`).
- **Models** are the models.dev `anthropic` catalog as `anthropic/<id>`.
  The bridge has no `session/set_model`; the sidecar's config-option path
  sets the flow's string, and the bridge resolves `anthropic/<id>` to that
  id (conformance). An id the bridge does not offer fails at session
  start with the sidecar's "not available (available: …)" message; its
  fuzzy matching may resolve a near miss to a sibling of the same family.
- **Cost**: `usage_update` carries a USD cost (cumulative per session), so
  budgets work as on omp. With a login it is a nominal API-price figure,
  counted as spend, as for omp's logins.
- **Egress** is the credentials' domains (`api.anthropic.com`); auto-update
  and nonessential traffic are off on the agent's command line and in the
  generated `~/.claude/settings.json` for a human in the terminal.
- **Editor**: the `Anthropic.claude-code` extension from Open VSX, which
  reads the same `.credentials.json`.
- **Residuals.** The bundle grows by about 0.8–1 GB (two bun-built binaries
  per libc), paid on every first pod start. The release has no checksums
  file; trust is TLS plus the pinned tag, as for opencode and codex.
  Bedrock, Vertex, gateways, importing a person's own Claude Code login,
  and harness options are out of scope.

## Tests that hold it

- `internal/account/providers`: `TestClaudeCodeTakesAnthropicOnce` (both
  kinds, one sink).
- `internal/adapters/claudecode`: the profile (command, modes, ask_human),
  settings and role files, the real `$HOME`, the 0600 login file, write
  failures.
- `internal/project/kinds`: `TestClaudeCode*` (credentials, env,
  rendering, models, checks); registry, descriptor and editor-extension
  tables; `TestToolsDockerfilePinsMatchKinds`.
- `internal/project/integrations`: `TestNoRefreshTokenReachesAWorkspace`
  with a `claude-code` integration holding the Anthropic login.
- `TestClaudeCodeConformance` (tag `conformance`, `task
  sandbox:test:harness`): versions; the key face (key, preamble,
  ask_human loaded, cost, a flow model, root); read-only; the login face
  and a refresh pushed into a running session.
