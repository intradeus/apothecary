# Live agent sessions: the workspace sidecar and ACP

Agents used to run as one-shot CLI invocations (`opencode run --format json`,
`codex exec --json`), started by `devcontainer exec` per stage and parsed
after they exited. Four things followed from that and all four were felt: the
run page could only tail raw NDJSON; nothing could be said to a running agent;
`ask_human` had to hand the turn back after a 20 s grace hold ("END YOUR TURN
NOW") because no tool call could be held open past the harness's timeout; and
an engine restart killed the exec client while the agent kept running,
orphaned, beside the resumed one.

Compared against kandev (kdlbs/kandev): the difference was never who
orchestrates — both engines drive every step — but that kandev keeps a
long-lived agent process behind a per-environment sidecar (agentctl) and talks
to it in the Agent Client Protocol. This change does the same.

### The sidecar (`shim sidecar`)

The static shim grew a fourth role. One sidecar per workspace, started on
first use by the devcontainer adapter through `Exec` — so it runs as the
remote user with the devcontainer's environment, exactly as the harness always
did — and detached by the shim itself (no setsid/nohup assumed in the repo's
image). It serves HTTP on `/opt/apothecary/sidecar.sock`, which the engine
dials host-side through the bind mount: the same channel as the engine bridge
and the port relays, no container networking. Singleton by socket.

It owns:

- **the agent** — one ACP process (JSON-RPC over stdio), started on demand and
  reused across stages; sessions are created or loaded per attempt, the
  stage's agent is applied as the session mode (opencode's modes are its
  agents) and the flow's `provider/model` as the session model, trying the
  closest advertised id when the agent spells models differently.
  `session/update` is normalized into a small event vocabulary (message,
  thought, tool_call/tool_update with input/output/diffs, plan, usage with
  cumulative cost, turn_start/turn_end) and kept in a replayable buffer with a
  boot id and sequence numbers. Permission requests are auto-approved (the
  harnesses are configured not to ask; this is the backstop that keeps a stray
  prompt from hanging a turn). Files and terminals are NOT offered to the
  agent as client capabilities: it works on the checkout directly.
- **files** — one directory level at a time (ignored entries flagged, `.git`
  hidden) and file contents capped at 1 MB; paths resolve through symlinks and
  must stay inside the checkout.
- **git** — the run's changes against the merge base with
  `origin/<source branch>`, committed and not, plus untracked files, and a
  unified diff per file.
- **terminals** — a PTY shell in the repo over a WebSocket. stdlib only (the
  shim builds from its own source tree with no module dependencies), so the
  RFC 6455 framing and the three PTY ioctls are hand-written; Linux only.

### Harnesses

`internal/adapters/acpagent` is the one AgentRunner for every ACP agent. A
harness kind contributes a profile: opencode is `opencode acp` (verified
against 1.17.8), codex is the `@agentclientprotocol/codex-acp` bridge (pinned
2.2.2, installed beside the codex CLI) in its `agent-full-access` mode, with
the role prompt prepended to the first prompt because codex has no named
agents. The old NDJSON parsers are gone.

opencode in ACP mode swallows a failed model call: the turn ends `end_turn`
with no output and the cause appears only in its log. It now runs with
`--print-logs --log-level WARN`, and the sidecar turns "a turn that produced
nothing and logged an error" into a failed turn carrying those log lines —
found live, where the free tier refused the pinned version and the stage would
otherwise have looked like an agent that silently did nothing.

### The transcript

The runner streams `ports.AgentEvent`s; the run loop coalesces them into
events (`agent_prompted`, `agent_session`, `agent_message` per text segment,
`agent_tool` per finished call, `agent_plan` per change, `agent_notice`) —
never one per token, because every append is a transaction with a projection
update. Token-level progress travels as ephemeral `delta` frames on the run's
SSE stream (no id, so a reconnect never resumes from one), superseded by the
persisted event with the same segment / tool call id. Everything passes the
redactor, deltas included.

`agent_session` is appended the moment the session exists, so a crash
mid-invocation resumes the right session even before the first invocation
ended — previously the session id was only known after the process exited.

### Messages to a running agent

`POST /runs/{id}/messages` records `message_queued` and hands the message to
the running invocation's inbox. The runner takes queued messages when a turn
ends and sends them as the next turn of the same session; `interrupt` cancels
the turn in flight (ACP `session/cancel`) first. Taking IS delivering
(`message_delivered`). A message nobody took before the invocation ended rides
into the next prompt. With no agent invocation running the endpoint answers
409 rather than queueing into a void.

### `ask_human` blocks again — and the engine parks

The tool call now waits for the answer: the shim long-polls the ping, sending
MCP progress notifications, which opencode uses to reset its per-call timeout
(codex's `tool_timeout_sec` is raised for the server instead). A raise carries
an idempotency key, so a retry after a lost response returns the same ping
instead of asking twice.

The engine still owns the long wait. If nobody answers within
`Config.AskHumanHold` (30 min), it parks the run itself: the open questions
are marked handed back (the long-poll answers 410), the turn is cancelled, the
attempt pauses with its session recorded, and the slot is given back — the
same pause/resume machinery as before, without depending on the model obeying
"end your turn".

### Restarts

The agent process lives in the sidecar, i.e. as long as the container. On
re-entry after an engine restart the runner first asks whether the session's
turn is still running, or finished after the last transcript event recorded
(`agent_*` events carry the sidecar cursor); if so it follows the turn from
there instead of prompting again. If the turn is gone but it had asked a
question, the attempt parks on that question and resumes the session with the
answer. Only then does it fall back to "the engine restarted, continue".

### Residuals

- Cost spent by a reattached turn between the last recorded event and the
  restart is not recoverable (the sidecar reports cumulative session cost).
- The sidecar's event buffer is in memory (20k events): a sidecar restart
  (container restart) loses an in-flight turn, which the runner reports.
- Live deltas are scrubbed per update; a secret split across two updates could
  reach a live viewer before the persisted (fully scrubbed) segment replaces it.
- The terminal and workspace endpoints are as unauthenticated as the rest of
  the API, and reachable from inside the workspace through the engine bridge,
  like every other route — DESIGN's localhost residual applies.

### What the agent will run, checked before it runs

Found live: the built-in flow's review stage pinned `opencode-go/glm-5.2`. The
provider's live model list still served it, but opencode 1.17.8 in the
workspace did not offer it, so runs built a workspace, planned, built — and
failed at review when the session refused the model. Two checks now, because
there are two sources of truth:

- **The provider's list, before anything is built** — `Resolution.Models`
  (the harness kind's `Models()`: the provider's live endpoint, models.dev as
  fallback). Run creation refuses a stage whose model is not in it, naming the
  stage and the closest ids from the same provider; preflight shows it as a
  `models` step. An unreachable list does not block a run (a provider blip is
  not a verdict on the flow); preflight says the check could not be made.
  Harnesses that run any model string declare `kinds.AnyModel` (the fake) and
  are skipped.
- **The agent's own list, in the workspace** — `ports.AgentModelLister`: the
  sidecar returns what the agent advertised for a session, and the ACP runner
  opens a throwaway one to read it. A fresh run checks every agent stage right
  after its workspace is ready and fails before the first stage; envcheck has
  it as the `agent_models` step. Matching follows the agent's spelling: codex
  lists bare ids with an effort suffix (`gpt-5-codex/high` covers
  `openai/gpt-5-codex`). An agent that cannot be asked, or reports nothing, is
  logged and not failed — its stage would surface the problem itself.

### Envcheck: no more waiting it out

- **One per project, superseding.** Starting a check cancels the project's
  running one instead of refusing with "already running"; `DELETE
  /api/envchecks/{id}` cancels outright. A cancelled check reports why once
  (`stopped`) instead of failing every remaining probe.
- **Verdict before teardown.** The check is pass/fail as soon as its probes
  end; destroying the workspace follows (`tearing_down`, then a `destroy`
  step), and another check can start meanwhile.
- **Probes in parallel.** Egress probes run eight at a time (dozens of hosts,
  up to 10 s each, used to be sequential).
- **Re-attach.** `GET /api/projects/{id}/envcheck` is the project's latest
  check, so the project page follows a check started before it was opened.
- **Cleanup of a cancelled build.** A provision cancelled or failed mid-build
  returns no env id but may leave a sandbox, a volume and an egress network;
  the check destroys by the workspace name, which is the env identity.
- **Two probe bugs.** Wildcard allowlist entries (`*.docker.io`) were curl'ed
  literally (exit 3, malformed URL) — now skipped as patterns. And the check
  provisioned without the harness install list, so on a repo image without
  opencode `harness_binary` could never pass — it now builds what a run would.

### Run-page gate: the review link

The run page fed the ping card from the `ping_raised` event's `payload_ref`,
a field the event stopped carrying when attachments became typed
(`files` / `link`). The card never saw the plan file, so the gate showed no
"review & comment" link there while the inbox (fed by the projection, which
still derives `payload_ref`) did. The run page now derives the primary the
same way: the link, else the first file.
