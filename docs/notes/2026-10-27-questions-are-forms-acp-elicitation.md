# Questions are forms — ACP elicitation (2026-10-27, TODO 34)

- **One question shape**: `event.Form` — an ordered list of `event.Field`
  mirroring ACP `elicitation/create` / MCP elicitation properties (string
  [format, pattern, length], string+options, array+options, number,
  integer, boolean). `answer_kind` is `yes_no | form`; `choice`, `multi`,
  `text`, `options`, `multi`, `SelectNone` and the `other` escape hatch are
  gone. A form is answered `accept` + `content` or `decline` + note
  (`validateForm`; one renderer, `composeFormText`, for the tool return and
  the resume message). The review round's thread selection is a one-field
  array form (`threads`, every thread picked by default).
- **ask_human** takes `fields` (none = one free-text field `answer`).
- **The sidecar bridges harness questions**: `initialize` advertises
  `elicitation.form`; `elicitation/create` (workspace/shim/elicitation.go)
  maps `requestedSchema.properties` IN ORDER onto fields, raises the ping
  through ask_human's own client, and answers accept/decline — cancel when
  parked or when the agent sends `$/cancel_request` (rpcConn now tracks the
  agent's requests in flight). `url` mode is declined. A codex
  `_meta.codex.isSecret` field is never asked; it is answered with a notice.
  A message-only form becomes one `proceed` boolean.
- **What actually uses it today**: codex-acp sends its `request_user_input`
  here (it answered `{answers: {}}` while we advertised no forms), but codex
  only offers that tool in Plan mode (or behind the under-development
  `default_mode_request_user_input` flag) — left off on purpose. MCP
  servers' elicitations codex forwards come the same way. opencode's
  `question` tool is disabled under `OPENCODE_CLIENT=acp` and its ACP layer
  has no bridge, so opencode asks only through ask_human.
- **Migration 0012** adds `pings.form` / `pings.content`, drops
  `pings.options`, and rewrites old `ping_raised` / `ping_answered` events
  into forms (choice/multi/text; `other` → decline).
