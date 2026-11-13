# omp harness (TODO 42 retired, 2026-10-27)

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
