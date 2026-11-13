# The Ask pane (TODO 41, part 1, 2026-10-28)

A flow's `ask: { harness: <kind | $param> }` gives every run an Ask pane: a
read-only session beside the run, from the moment the workspace is up until
it is destroyed, that answers questions about the code as it stands. Not a
stage: it runs in parallel with whatever stage is working, on its own
harness if the flow says so, and nothing of it is a run event. User
decisions: the flow names only the harness (the model is picked in the
pane, live), it must not have write access (a stage may be editing), and
the name is "Ask".

- **The sidecar hosts several agent processes and one turn per session.**
  Until now the shim ran ONE process (a new command line killed the old)
  and ONE turn at a time per workspace (`errBusy`), which made a parallel
  session impossible. Now `agentHost` keeps `procs` by command-line id
  (`/agent/start` returns `proc`; a session request names it, or the one
  process running), `sessions` by id (each knowing its process) and `turns`
  by session — a prompt is refused only while THAT session's turn is in
  flight. `/agent/status` reports `turns` (plural; `Status.Turn(session)`
  on the client), `agent_exit` carries `proc`, and a different command line
  no longer kills the first process (a stage switching harness leaves the
  old one idle). The engine's reattach reads the session's turn.
- **Read-only, per harness.** `ports.AgentInput.ReadOnly` opens the session
  read-only; `ports.ReadOnlyCapable` says whether a runner can promise it.
  The ACP profile says how: opencode by the agent definition (`ReadOnlyBy:
  agent` — the engine prepares the reserved `ask` agent with edit, patch,
  write and bash off, and `ask_human_*` off so a question it raised cannot
  land on the stage), codex by session mode (`read-only`, codex-acp 2.2.2's
  spelling beside `agent-full-access`; the sidecar's `setMode` also accepts
  the same mode under a `-`-prefixed spelling). A `read_only` opencode agent
  keeps `write` only when it DECLARES outputs — the planner/reviewer still
  write their artifacts, an agent with nothing to write gets none. omp has
  no per-session mode and its named agents cannot start a session, but it
  takes a per-PROCESS tool list: a read-only session is a SECOND omp process
  (`ReadOnlyBy: process`, `ReadOnlyCommand` = `omp acp
  --tools=read,grep,glob,web_search,browser --approval-mode=always-ask`,
  no `--yolo`) beside the stage's — possible only because the sidecar now
  hosts several processes. browser reads pages and drives the app, never
  the repository (and needs a Chromium the workspace may not have). A
  read-only session also gets none of the profile's own MCP servers
  (ask_human), so an Ask answer cannot raise a ping as the stage's — on
  omp; codex's ask_human is config-wide and stays a residual. A harness
  that promises none of the three keeps the pane off, with the reason. The
  sidecar is the backstop: a read-only session's permission request is
  granted only for a tool whose kind plainly reads (read, search, fetch,
  think); anything else — including a request with no kind — is refused
  with the reject option.
- **Engine (`internal/engine/ask.go`).** `AskState` / `Ask` / `AskSetModel`
  / `AskWatch` / `AskMessages`. The conversation is `internal/ask` rows (a
  thread: session id + model; messages), in Postgres via migration 0015 or
  in memory. A question is recorded and its answer runs detached (15 min
  cap) on the harness the flow names, `Run` the first time and `Resume`
  after, as `spec.Agent = ask`; the answer's text streams as delta frames
  through an SSE of its own (`/api/runs/{id}/ask/stream`: messages with id =
  seq, `delta` and `state` events), scrubbed by the run's redactor, and
  lands with its model, cost and turns (or its error). One question at a
  time per run. The model defaults to the flow's `model` parameter when the
  harness reaches it (the provider's list, else what the agent advertises),
  else the first reachable; a pick in the pane is kept on the thread and
  the live session is switched over (`AgentInput.Model` on Resume — the
  sidecar now applies a model to a live session too) before the next
  question. Preflight refuses an `ask:` harness outside the project's set,
  like a stage's.
- **Agent prepared into the workspace.** `agentsFor(fl)` appends the
  builtin `ask` spec (read-only, no bash, a prompt that forbids changes and
  questions and warns that another agent may be editing) at the three
  Prepare sites (bootstrap, rebuild, envcheck). `agentspec.AskAgent` is the
  reserved name; a flow with `ask:` cannot declare an agent called `ask`.
- **UI.** An Ask window in the desk (beside Session in every preset); the
  flow editor's formula panel has an "Ask pane" field (a harness kind or
  `$harness`; empty = off).
- **Residuals.** On codex, ask_human is an MCP server of the whole config,
  so an Ask answer that insists on asking a question would raise a ping
  against the run as the stage's (the role prompt forbids it; opencode's
  tool is off, omp's read-only process has no servers at all). omp's
  read-only command line was checked against omp 18.12.2 locally (`omp
  acp` starts and answers initialize with `--tools` and
  `--approval-mode`); a live turn on the pinned 18.13.1 in a workspace —
  ask it to write a file and run a command, expect neither — is still
  owed, as is every omp live turn (TODO 42). The three builtin templates
  declare `ask: { harness: $harness }`: every run of them gets the pane on
  the flow's own harness (user decision, 2026-10-28). Models are asked
  once per run per engine process (credentials rotated mid-run need a
  restart to show up).
