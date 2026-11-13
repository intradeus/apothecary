# Clean architecture refactor (TODO 7, 2026-10-30)

The backend was hexagonal in spirit already — ports and adapters, an
acyclic graph, runs event-sourced as one short stream each — and its
trouble was modularity, not paradigm: `internal/engine` was a god package
(16.5k lines, 89 exported methods on `Engine`, of which run lifecycle was
about a fifth, all under one mutex), `internal/ports` and `internal/httpapi`
were hubs, and the interpreter kept a run's state two ways — `replay`
folded events into it, the live loop mutated it by hand at ~40 sites — that
disagreed in eleven places, four of them recovery bugs. The plan, its
baselines and every deviation are in `docs/clean-architecture-plan.md`;
this is the outcome.

- **A modular monolith.** The engine is the run lifecycle and the
  interpreter's shell (11.2k lines, 33 exported methods). Every other
  concern is its own package behind an interface it declares, and never
  imports the engine: `mrs`, `resources`, `envstate`, `wsinfo`, `envcheck`,
  `egress`, `ask` (service beside its store), `authsink`, `runports`,
  `browsermcp`, `services`, plus the leaves `shell`, `wsname`, `bootstrap`,
  `modelcheck`, `redact`, `resolution`. `cmd/apothecaryd/wire.go` builds
  them, asserts that `*engine.Engine` satisfies each consumer interface, and
  hands them to `engine.New(Deps)` — no setters. httpapi holds one field per
  service and is split into per-area route files; `Server.Drain` ends event
  streams and ping holds on shutdown (a restart took the full 10 s before).
- **`ports` split by capability** — `envport`, `agentport`, `repoport`,
  `trackerport`, `egressport`, `browserport`, `notifyport` (the suffix keeps
  them from shadowing `env`, `internal/browser`, …). Type names unchanged.
  `ports/doc.go` maps them.
- **One fold.** `runcore.State.Apply` is the fold; `Replay` is a new
  state plus `Apply` over the log, and the engine's `append` applies it to
  every event it writes. The live state equals the fold of the log at every
  settling point — an engine test hook (`foldCheck`, nil in production)
  replays each run at the top of every loop iteration, after each idle tick
  and on exit, and fails the test on any field that differs. What the loop
  still sets without an event is listed on `State`: three decisions the
  next event records (a success's `on_success`, a phase end decided without
  a terminal edge, a debug-leaf continue — each re-derived by recovery the
  same way) and two facts no event carries (a caretaker begins idle; the
  env id before `env_ready`).
- **`internal/runcore`** is the interpreter's functional core: `State`, the
  fold, and the pure decisions — `RouteSuccess`, `EndPhase`, `ChooseEdge`,
  `StageCondition`, `NextStep`, `BudgetExceeded` — table-tested without a
  harness. It imports only value packages and reads no clock (archtest).
  The run loop, `awaitAgent`, the stage executors and the action executors
  stay in the engine.
- **Guard rails.** `internal/archtest` turns the package map into a test.
  Golden event logs (`internal/engine/testdata/golden/`) record every
  scenario's event types; a commit that changes one names the fix.

Recovery bugs fixed (each with a crash test shown failing on its parent):

- **D1** — a crash between an agent stage's `stage_completed` and its
  routing: recovery skipped `verdict_received` and the budget check, so a
  run over budget completed. Live and recovered completions share one path.
- **D8** — a gate answered while its phase was parked lost the answer on
  `phase_resumed`; recovery asked the question again.
- **D9** — a wait recovered in its resume window started its timeout over.
- **D10** — a run parked on a quiet take-over holder restarted (and
  re-parked) on every boot.
- Found by the fold check: an agent's `agent_session` could be dropped when
  the harness's result arrived before its session event, so a crash would
  start the agent over instead of resuming it.
- Also found on the way: the SSH door's reply/close race, and an Ask pane
  that never recovered from a lost session.

Behaviour that changed on purpose: a refused merge stays pending, so the
retry reads the merge request's status before merging again (one fewer
`action_started`); a parked time and a hold's start are their events' own
timestamps; an edge to `end`/`done` clears the feed live as replay did.

Left as they were, recorded: an answer sent during a command or action
stage waits up to 30 s and reports busy (R1); the Ask service's `cancel` is
never called (R2); a crash between `mr_merged` and `stage_completed` can
record a second `mr_merged` (R3).

Follow-ups, the same day, after the plan closed:

- **The rest of the interpreter's decisions are pure.** `runcore` now plans
  the idle tick (`PlanIdle`, with `PlanResume`), classifies how an attempt
  ended (`ClassifyAttempt`) and holds the take-over tables (`PlanHoldRequest`,
  `HoldLeg`). A plan accounts for what its earlier actions will have done: a
  decline silences its occurrence, an ask counts for the same-tick dedup and
  keeps the run alive. `awaitAgent`'s select loop stays imperative.
- **A crash at every event** (`internal/engine/crash_test.go`). Five scenarios
  run clean, then once per event with the process dying right after writing
  it: a store that refuses everything from there on, then a fresh engine over
  the same log and the same fake world, so what the dead process did survives
  it, 153 crash points in all. It found that a crash between `stage_started`
  and an attempt's first prompt resumed the session the LAST attempt left,
  dropping the retry's prompt and its feed, which could abandon the run. A
  session now belongs to one attempt (`stage_started` forgets it), an attempt
  that never reached its agent starts fresh under its recorded iteration, and
  a re-executed command clears its outputs.
- **A verb landing while a run parks acts on the parked run.** Between the
  decision to park and the loop being gone, a pause or an abort returned
  success and did nothing, and a release or a message was refused. The loop
  stops taking deliveries when it decides to park (`parking`) and reads its
  cancel cause once, on leaving, under the lock `cancelLoop` takes; the others
  wait in `liveLoop` and take the dormant path. Its test also caught two
  writes to the fold's state with no event behind them.
- **`runloop.go` split by stage type** (`stage_*.go`, `invocation.go`,
  `provision.go`, the take-over in `hold.go`); **`internal/runinput`** holds
  what starts a run, so `automation` no longer imports the engine; the
  adapters share `shell.Quote` and `shell.Tail`.
