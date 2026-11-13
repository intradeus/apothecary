# Harness conformance tests (2026-11-02)

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
