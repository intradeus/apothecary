# The assistant token lives with its harness session

Corrects `2026-11-19-the-assistant.md` (the token "minted when the turn
starts and revoked when it ends") and
`2026-11-19-the-assistant-acts-by-confirmation.md` ("the token lives for
the turn anyway").

## Why

A session's second question failed at once: Claude Code reported the
`apothecary` MCP server as needing authentication. The token was not short
(an hour, `tokenTTL`); it was revoked at the end of every turn. The harness
process and its ACP session outlive the turn in the pod, and ACP fixes a
session's MCP servers, the `Authorization` header included, when the
session is opened (`session/new`, `session/load`). On the next turn the
engine minted a new token and sent it with `/agent/session`, but the shim
answers a session still live in its process as it is (`loaded: false`),
switching only its mode or model. The harness called the face with the
revoked token and got a 401. The unit tests' fake agent host answered every
known session `loaded: true`, so they never saw it.

A turn-scoped token would mean a new harness session each turn, losing the
conversation's context; the task's owner wanted that context kept.
Refreshing the token behind the harness would need a relay in the pod
holding a refreshable credential, which the harness can read as it reads
its header today: one more component, no narrower holder.

## What now

- **The token is the harness session's.** It is minted when a harness
  session takes its MCP servers, stays valid across turns, and is revoked
  only when replaced (`Service.token`): on the first turn, after an
  engine restart (the secret is held in memory only), or when less than
  half its life is left. `answer` no longer revokes it.
- **A replacement reaches a live session by a restart.** When `token`
  mints a new one and the shim answers the session as already live
  (`loaded: false`), `run` stops that session's harness process
  (`sidecar.Client.StopProc`, `/agent/stop` naming the process; each
  session has its own, confined to its home), starts it again and opens
  the session again: it is loaded with the new header, its context kept.
  This costs a `session/load` at most every half `tokenTTL` of use, and
  after an engine restart.
- **Fill is a turn's**, as request already was: outside a turn it answers
  "no question is being answered". Between turns the token keeps its read
  actions (one workshop, read only), held by the session's idle, confined
  harness process alone, for at most its life.

## Tests

- `internal/assistant`: the fake agent host now behaves as the shim does
  (a live session is not loaded again and keeps its first servers; stopping
  a process forgets its sessions). `TestATurn` (no token minted or revoked
  between turns, the live session holds the live token, fill refused
  between turns), `TestTokenRenewedRestartsTheLiveHarness` (an aged token
  and an engine restart each stop the process once, load the session with
  the new bearer, revoke the old token, and give no instructions again),
  `TestTokenIsRemintedWhenOld`.
- `internal/workspace/sidecar`: `TestAgentCallsShapeTheirRequests` (stop
  one process; one already gone is no error).
- `internal/httpapi`: `TestAssistantFillIsTheSessionsTokens` and
  `TestAssistantStream` fill with a question in flight (`env.asking`).
