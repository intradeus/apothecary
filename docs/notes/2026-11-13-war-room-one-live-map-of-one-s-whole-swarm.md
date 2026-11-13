# War room: one live map of one's whole swarm (2026-11-13)

- **What:** a `/war-room` page that shows every sandbox the signed-in person
  has in flight, across all their workshops, on one tactical map (DESIGN
  7.11). It is fed by one new endpoint, `GET /api/swarm`.
- **Scope is "mine".** The units are the caller's runs from
  `run.Reader.List(UserID)`, the same per-user query the runs list makes,
  never `run.Reader.All`. There is no admin or instance-wide mode: ownership
  is per user everywhere else, so the war room is too. The units are every
  non-terminal run, plus runs that ended in the last five minutes (by their
  last event), so the map can show how they ended before they leave.
  Archived runs never show.
- **The crew board is observation, not history.** `engine.crewBoard`
  (`internal/engine/crew.go`) holds, per run, the agent on station: stage,
  iteration, harness kind (the stage's own, else the resolution's
  fallback), agent binding, since, whether a turn is active, the latest
  in-flight tool's title, and the subagent spawn calls in flight.
  - `stageAgent` boards the run once the attempt's MCP servers resolve and
    leaves on every return, so a run parked on a question, failed, done or
    cancelled is off the board.
  - The transcript feeds it tool updates and turn start/end. A finished
    call (completed or failed) drops out; `turn_end` clears everything
    still in flight, because a cancelled turn never reports its pending
    calls finished.
  - Titles go through the run's redactor, as tool input and output do.
  - It has its own mutex (the loop writes, HTTP reads) and `Engine.Crew()`
    returns deep copies.
  - Like the live deltas and the resource window, it is memory only: no
    event, no migration. After an engine restart it is empty until the loop
    is back in the stage (a reattached turn boards again), so a unit shows
    its state without a pilot for a moment. The resource window restarts
    empty too. Both are acceptable for an observation surface.
  - `resources.Sampler.Latest(runID)` gives the newest sample without
    copying a 360-sample window per run per poll.
- **Subagents are recognised by the shape of the tool call.** ACP has no
  notion of a subagent: a spawn is a tool call like any other. So
  `subagentSpawns(title, input)` reads the call's title and raw input, and
  counts 0 for anything it does not know: a drone is never counted on a
  guess. What it knows, from the sources of the pinned versions (read, not
  run against a live harness):
  - **opencode 1.17.8**: the `task` tool, kind `think`, raw input
    `{description, prompt, subagent_type, ...}` → 1. Its first `tool_call`
    usually carries `{}`; the arguments arrive with `in_progress`, so the
    board re-classifies on every update. Calls inside the subagent are not
    forwarded (its child session is not an ACP session).
  - **codex-acp 2.2.2 over codex 0.160.1**: the collab tool `spawn_agent`,
    surfaced with the title `spawnAgent`, kind `other` → 1. Its siblings
    (`wait`, `sendInput`, `closeAgent`, …) spawn nothing. A client that
    advertised native subagent sessions would get child sessions instead;
    the shim does not, so the tool call is what arrives.
  - **omp 18.13.1**: the `task` tool, kind `other`. One assignment
    `{agent, task, solutionSpace, ...}` → 1; a batch
    `{context, tasks: [{task, ...}, ...]}` → one per assignment. Progress
    comes as content on the call, not as nested tool calls.
  - The shim truncates raw input at 16 KiB. A batch cut short is no longer
    JSON and counts 0, as the classifier promises for anything malformed.
    A harness upgrade that changes a shape does the same: drones stay at
    zero, never at a wrong count.
  - The count is spawn calls in flight, not deeper activity: nested calls
    are not forwarded by opencode or omp, and codex's are not told apart.
- **One polled snapshot, not a stream.** The page polls `GET /api/swarm`
  every 2 s while open. Resource samples only change every ~10 s, there is
  no global event bus to stream from, and a typed JSON route gets its
  schema and client from the route table, where an SSE stream's deltas are
  hand-parsed. It costs one per-user `List` per open war room every 2 s,
  the query the `fleet` poll already makes every 4 s. A stream can carry
  the same `Swarm` document later if a sub-second feel is wanted. The kill
  feed is the client's diff of consecutive snapshots (`$lib/swarm/feed`),
  so it starts empty: the board as first found is not news.
- **Dev fakes act it out.** The fake provisioner samples synthetic,
  bounded, drifting cpu/memory/pids per workspace. The fake harness has a
  `demo` field: `on` acts each turn out over ~30 s — ordinary tools, one
  subagent, then a batch of two — before writing its outputs. The dev seed
  turns it on for `dev-harness`; every test that registers the fakes keeps
  the 200 ms fake. The demo turn emits no session: the fake has none to
  resume, and a recorded one would send a retried stage down the resume
  path.
