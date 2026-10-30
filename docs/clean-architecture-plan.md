# Clean architecture refactor (Go backend) — plan

Status: approved 2026-10-30, in progress — Phases 0–1 done. Scope approved: interpreter core in its own package, recovery bugs fixed in scope, ports split by capability. Anything found while implementing that contradicts this plan is recorded under "Deviations" — never silently. Line numbers are from HEAD 8abd1a4 (branch `refactor/rewrite-cleanup`); re-locate by symbol name if they drift.

## Context

TODO.md item 7 ("redo everything in clean architecture"). The backend is already hexagonal in spirit (ports/adapters, acyclic graph, runs event-sourced as short-lived per-run streams). Its problem is modularity, not paradigm:

- `internal/engine` is a god package: ~16k lines; `Engine` has 89 exported methods, of which run lifecycle is ~1/5. Workspace ops, egress, ask pane, logins, browser, ports, envcheck, resources share one struct, one `e.mu`, one bag of maps.
- Hubs: `internal/ports` (78 types in one file, 27 importers) and `internal/httpapi` (talks to a concrete `*engine.Engine` for everything).
- The run interpreter keeps state through two paths: `replay()` folds events into `runState`, but the live loop mutates `runState` by hand (~40 sites) and only applies `run.Apply` to the view on append. They disagree in 11 places; four are real recovery bugs.

Outcome: a modular monolith — engine reduced to run lifecycle + interpreter shell; each other concern in its own package behind a consumer-declared interface; one fold for replay and live; pure routing/planning in `internal/runcore`; `ports` split by capability; the import graph enforced by a test. **Zero change to the HTTP API, DB schema, event wire format, and UI.**

## Decisions (final — do not reopen)

1. **Modular monolith.** No new processes, no new infra.
2. **Event sourcing stays for runs only.** Everything else stays plain hexagonal / transaction scripts.
3. **`internal/event` stays one package.** It is the run stream's wire contract (one stream; engine builds every payload; unregistered types decode silently to `*event.Unknown`, so splitting risks silent projection corruption for no gain).
4. **Extracted packages never import `internal/engine`** (nor httpapi, pgstore, confstore, kinds, adapters). They declare the interfaces they consume; `*engine.Engine` satisfies them; compile-time assertions `var _ pkg.Iface = (*engine.Engine)(nil)` live in `cmd/apothecaryd/wire.go`.
5. **No compat shims.** No type aliases or forwarding methods left behind; every caller is updated in the same commit (project no-compat policy).
6. **Interpreter**: one fold (`apply`) for replay and live; pure functions for routing, budget, attempt classification, idle planning; then all of it moves to `internal/runcore` with exported fields. `awaitAgent`'s select loop, `stageAgent`'s invocation loop and the action executors stay imperative in engine.
7. **One sanctioned non-event state change remains**: the cursor move after a successful stage (no event records on_success routing). Recovery re-derives it through the *same* code path as live (D1).
8. **Recovery bugs are fixed in scope**, each in its own `fix(engine):` commit, with the crash regression test committed first and shown to fail on the parent.
9. **`ports` splits into capability sub-packages with a `port` suffix** (`envport`, `agentport`, `repoport`, `trackerport`, `egressport`, `browserport`, `notifyport`) — the suffix avoids clashes with variables (`env`) and packages (`internal/browser`, `adapters/workspace`). Type names stay identical (pure move; stutter accepted).
10. **Naming**: never "decide"/"decision" (project vocab reserves verdict/yes_no), never forge/block/when. Pure functions are named `plan…`, `resolve…`, `classify…`, `…Target`.

## Ground rules for every commit

- One numbered step = one commit unless the step says otherwise. Messages: `refactor(<area>): …`, `fix(engine): …`, `test(engine): …`.
- Gate before each commit:
  `test -z "$(gofmt -l ./cmd ./internal)" && go vet ./... && go test ./... && task lint:models && go run ./cmd/openapigen -check`
  If a step touches `internal/pgstore`, also run `TEST_POSTGRES_DSN=… go test ./internal/pgstore/` against the **separate `apothecary_test` db on :15434** (tests TRUNCATE; never the data dbs).
- End of every phase: `go test -race ./internal/...` no worse than the 0.2 baseline.
- `api/openapi.yaml` and `cpui/src/lib/api/generated/` never change. `internal/httpapi/openapi.go` `typeNameOverrides` is keyed `"<last pkg segment>.<Type>"`: when a listed type moves package (`engine.CheckRun`, `engine.PreflightStep`, …) update the key in the same commit. If `openapigen -check` reports a clash for a bare-named type that moved, add an override that keeps the old component name.
- No SQL migration (`0001_init.sql` is frozen). No change to event `Type` strings or json tags.
- Golden event logs (0.3) never change except in a commit whose message names the fix that changed them.
- Tests move with code. Test files are not importable across packages, so a moved test either (a) builds a local fixture in the new package (`fake.NewEnvProvisioner`/other fakes from `internal/adapters/fake`, a `resolution.Resolution` literal, `flow.Parse`, an `event.MemStore` seeded by hand), or (b) stays in `internal/engine` as an integration test that builds the new service from the harness's `h.store`/`h.res`. Tests poking engine internals (`h.e.clock`, `h.e.envStatus`, `h.e.observed`, `h.e.cfg`, `h.e.mu`, `h.e.handles`, `h.e.beginEnvOp` …) are rewritten against the new package's API. When a test leaves the engine package, delete its golden file in the same commit.
- Style: every new package gets a package doc comment saying *why* it exists (tone: `internal/run/run.go`, `internal/egressgw`); existing comments move with their code; full-sentence "why" comments; no comment noise; fix comments that the move makes stale (e.g. ones naming removed `Set…` methods).
- Do not touch `cpui/`.

## Target package map (end state)

| Package | Holds | May import (internal) |
|---|---|---|
| `engine` | run lifecycle (create/retry/rerun/pause/play/abort/archive/recover/shutdown), run goroutine + effect shell, pings, holds, SendMessage, rebuild, preflight, reaper + `destroyEnv`, secret scrub, MCP stage, service stage, transcript/live hub, tokens | anything except httpapi, pgstore, confstore, kinds, adapters |
| `runcore` | `State`, `Apply`, `Replay`, phase enum, every `runState` method, pure routing/budget/condition/attempt/idle/hold planning | event, flow, run, actionspec, agentspec, signalspec, reviewfile, `ports/*` (value types only) |
| `resolution` | `Resolution`, `Resolver`, `StaticResolver`, `ExtraRepo`, `Service*`, `ErrUnresolvable`, `ForRun`, availability checks | actionspec, agentspec, event, flow, run, signalspec, ports/* |
| `run` | view, reducer, + `Reader{View, All}` | event, reviewfile |
| `shell` | `Quote`, `Tail` | — |
| `wsname` | workspace naming (`For`, `ForRun`, `Slug`, `RunIDTail`) | run |
| `bootstrap` | workspace bootstrap scripts | agentspec, event, skills, shell, ports/* |
| `modelcheck` | model/auth checks | resolution, flow, ports/* |
| `redact` | secret redactor | resolution |
| `mrs` | MR read side | resolution, run, ports/* |
| `resources` | resource sampler | resolution, run, wsname, ports/* |
| `envstate` | env-op gate, reconcile status/dirty, badges | resolution, run, wsname, ports/* |
| `wsinfo` | checkouts, workspace info, live log tail, SSH target/info, build cache, `ErrNoWorkspace` | resolution, run, wsname, envstate, sidecar, shell, ports/* |
| `envcheck` | env check registry + standalone check | resolution, bootstrap, modelcheck, wsname, shell, flow, agentspec, ports/* |
| `egress` | live egress controls + audit ingest/query | resolution, run, wsname, ports/* |
| `ask` | Ask pane `Service` (+ existing `Store`/`MemStore`) | resolution, run, flow, modelcheck, redact, agentspec, ports/* |
| `authsink` | credential push into live workspaces, auth broker | resolution, run, redact, ports/* |
| `runports` | port exposure, observed ports, auto-expose, editor open/reopen | resolution, run, portrelay, event, ports/* |
| `browsermcp` | browser MCP sessions + port cache | resolution, run, ports/* |
| `services` | service shim protocol + run-page service control | resolution, run, event, sidecar, shell, wsinfo, ports/* |
| `httpapi` | driving adapter; per-area route files; one field per application service | anything except pgstore, adapters |

`automation` keeps importing `engine` for value types (`Ticket`, `RunInput`, `RunMR`).

---

## Phase 0 — Safety net

**0.1 Plan doc.** Copy this file to `docs/clean-architecture-plan.md` (shape of `docs/model-providers-plan.md`: Status line, sections). Add empty "Deviations" and "Baselines" sections.

**0.2 Baselines.** `go test ./... -count=1` three times, `go test -race ./internal/... -count=1` once; record pass/fail and flaky tests under "Baselines".

**0.3 Golden event logs (engine).**
- Register once, in `newHarnessSourceOn` (scenario_test.go — every harness constructor ends there, including `newPhasedHarness` via `newHarness`), and **before** its `t.Cleanup(h.e.Shutdown)` (scenario_test.go:209) so that, cleanups being LIFO, rendering runs after Shutdown.
- Keep a package-level `map[*testing.T][]*harness` (mutex-guarded). The first registration for a `t` installs the renderer cleanup; later harnesses of the same test append themselves. At cleanup the renderer walks the harnesses in construction order, de-duplicates by `h.store` pointer (tests like phase_test.go:443 share a store), and writes one file.
- Rendering: for each distinct store, `RunIDs` sorted (ULIDs sort by time); each run becomes `# run <n>` (index, not id) followed by one `goldenLine(ev)` per event: `<type>` plus `stage=<StageID>` read via `reflect.ValueOf(ev.Payload).Elem().FieldByName("StageID")` when present; `EdgeTaken` adds `edge=<From>:<kind>-><to>`; `RunPaused` adds `reason=`; `Phase*` adds `phase=`; `PingRaised` adds `ping=<type>`.
- File: `internal/engine/testdata/golden/<t.Name() with "/" → "__">.log`. Env `GOLDEN=record` writes; unset compares when the file exists (missing file = no check).
- Procedure: `GOLDEN=record go test ./internal/engine/ -count=1`, then `go test ./internal/engine/ -count=3`; delete the golden file of every test that fails comparison (non-deterministic) and list them under "Baselines". Commit the rest.

**0.4 Import-graph test.** `internal/archtest/arch_test.go` (package `archtest`, stdlib only). Walk `internal/` and `cmd/` from the module root (find it by walking up to `go.mod`), skip `testdata` dirs, call `build.Default.ImportDir(dir, 0)`, skip `*build.NoGoError` (dirs like `internal`, `internal/adapters`, `internal/sidechat`, `internal/pgstore/migrations`), compute the package path as `"apothecary/" + filepath.ToSlash(rel)` (module-mode `ImportPath` is "."), and check `pkg.Imports` (non-test). An import is stdlib when its first path element has no dot; third-party modules (dot in first element) are allowed everywhere; rules only constrain `apothecary/...` imports. Rule table rows: `{pkg prefix, mayImport []prefix (internal allow-list) | denyImport []prefix, importedOnlyBy []prefix}`. Initial rows (verified true today):
- `internal/ports` → no internal imports.
- `internal/adapters/` imported only by `internal/kinds`, `internal/adapters/`, `cmd/`.
- `internal/pgstore` imported only by `cmd/`.
- `internal/engine` imported only by `cmd/`, `internal/httpapi`, `internal/automation`, `internal/resolve` (removed in 1.4).
- `internal/event`, `internal/run`, `internal/flow` deny engine, httpapi, pgstore, confstore, kinds, adapters.
Every later step that creates a package adds its row from the target map in the same commit.

---

## Phase 1 — Low-level prerequisites (no behaviour change)

**1.1 `internal/shell`**: `Quote` (from `shellQuote`, engine/bootstrap.go:334), `Tail` (from `tail`, engine/runloop.go:2657). Update every caller (bootstrap, envcheck, secrets, service, runloop …).

**1.2 `internal/wsname`**: `For(branch, runID string)` (`workspaceName`, engine.go:2970), `ForRun(v run.View, runID string)` (`envNameForRun`, engine.go:848), `Slug(s string, maxLen int)`, `RunIDTail(runID string)` (engine.go:2926-2981). Branch naming at engine.go:1167-1175 uses `wsname.Slug`/`wsname.RunIDTail`; `maxBranchSlug` stays in engine. Naming output byte-identical; move the tests.

**1.3 Agent specs**: `askAgentSpec` + `askPrompt` (engine/askagent.go) → `agentspec.Ask()` beside `agentspec.AskAgent`; `agentsFor(fl)` → method `(*flow.Flow).WorkspaceAgents()`, nil-safe exactly like today. Callers: runloop.go:2620, rebuild.go:129, envcheck.go:363.

**1.4 `internal/resolution`**:
- Move from `engine/resolution.go`: `Resolution`, `Resolver`, `StaticResolver`, `ExtraRepo`, `ErrUnresolvable`, methods `repoHostFor`, `release`, `actionAvailable`, `phaseAvailable`, `signalAvailable`, `runnerFor`, `harnessSet`, `harnessInSet`. From `engine/service.go`: types `Service`, `ServiceHealth`, `ServicePort`, methods `Resolution.service`, `Resolution.servicesAvailable`, `(*Service).address`, `(*Service).probe`, `(ServicePort).address`. From `engine/mcp.go`: `Resolution.mcpAvailable`.
- Export as: `RepoHostFor`, `Close` (was `release`; the `Release` field stays), `ActionAvailable`, `PhaseAvailable`, `SignalAvailable`, `RunnerFor`, `HarnessSet`, `HarnessInSet`, `Service`, `ServicesAvailable`, `MCPAvailable`, `Address`, `Probe`.
- `(*Service).request(timeout)` returns `shimServiceRequest`, which is not a resolution concern: turn it into a free engine function `serviceRequest(s *resolution.Service, timeout time.Duration) shimServiceRequest` for now (it moves to `services` in 2.11).
- Add `ForRun(ctx context.Context, r Resolver, v run.View) (*Resolution, error)` with the body of `engine.resolveForRun` (resolution.go:366); the engine method calls it.
- Stay in engine: `preflightTicket`, `pinnedFlow`, `usesServiceStages`, `SettingsSource` (resolution.go:375), `StaticSettings`, `Limits`.
- Update: `internal/resolve` (drop its engine import; `var _ resolution.Resolver = (*Resolver)(nil)`), the `internal/kinds` comment, engine, httpapi `confapi.go`, `services.go`, `tickets.go`, tests incl. `server_test.go`, `services_test.go`. Archtest: remove `internal/resolve` from engine's allowed importers; add the resolution row.

**1.5 `internal/bootstrap`**: move `engine/bootstrap.go` whole (`BootstrapSpec`, `ExtraCheckout`, `RunBootstrap`, `WriteWorkspaceSecrets`, `SourceBranchOr` exported, script helpers). Uses `shell.Quote` and `shell.Tail` (bootstrap.go:215). Move its tests.

**1.6 `internal/modelcheck`**: from `engine/models.go`: `checkModels`→`Models`, `checkAuth`→`Auth`, `agentModelCheck`→`AgentModels`, `closestModels`→`Closest`, `agentAccepts`→`Accepts`, `agentModelTimeout`→`Timeout`, `modelCheck`→`Result`; `contains` callers switch to `slices.Contains`. `(x *exec).checkAgentModels` stays in engine.

**1.7 `internal/redact`**: `redactor` → `redact.Redactor` (`New()`, `Add(secret, label)`, `Scrub(s)`); `newRedactor(res, tokens...)` → `redact.ForResolution(res *resolution.Resolution, tokens ...string)`; `jsonLeaves`, `minSecretLen` stay unexported inside. Identical behaviour; move `redact_test.go`.

**1.8 `run.Reader`**: `type Reader struct{ Store event.Store }` in `internal/run` with `View(ctx, runID) (View, error)` (body of `engine.runView`, workspace.go:87) and `All(ctx) ([]View, error)` (body of `engine.views`, engine.go:1765, incl. the `ViewSource` fast path). Engine holds a `*run.Reader`; its inline copies (ExposeRunPort, egressCtrl, OpenEditor, OpenBrowserMCP, resolveScanner, RebuildEnv, WorkspaceLogTail) use it.

**1.9 Review-thread helpers**: `pendingThreads`, `newestPending` (engine/review.go:16,32) → exported `ports.PendingThreads`, `ports.NewestPending` beside `IsFactoryReply` (they move again in Phase 4).

---

## Phase 2 — Extract the leaf concerns (one commit each, in this order)

Common rules:
- The new package exports a service struct built by `New(…)`; its own mutex replaces any use of `e.mu`.
- `httpapi.Server` gains one exported field per service (concrete pointer, e.g. `EnvCheck *envcheck.Registry`); handlers switch from `s.Engine.X` to it in the same commit; test constructors `newEnv` (server_test.go:106) and `newConfEnv` (confapi_test.go:55) build the service. Each route keeps its current `Enabled` expression.
- `main.go` builds the service. Until 3.1, a collaborator the *engine* needs is passed with a `Set…` method like the existing ones; 3.1 converts them all to `Deps`.
- Ticker loops leave main as a `Run(ctx context.Context, every time.Duration)` method on the service that preserves today's behaviour exactly (eager first pass or not; interval); main calls `go svc.Run(reaperCtx, …)`.

**2.1 `internal/mrs`** — `ListMRs`, `MR`, `RepoHostUser` (engine.go:1289-1341), `MRLive` + `MRStatuses` (review.go:48-157). Deps: `resolution.Resolver`, `*run.Reader`. Callers: httpapi mrs.go, workspace.go:102, routes.go:308. Split `automation.Engine` (automation/watcher.go:34-39) into two `Watcher` fields: `Runs` (`CreateRunWith`, `RunsOfTicket`) and `MRs` (`ListMRs`, `RepoHostUser`); update main and `watcher_test.go`'s `fakeEngine`. Create `cmd/apothecaryd/wire.go` with the first `var _` assertions. Engine tests that call these to observe (observe_test.go:315, multirepo_test.go:259,273) build an `mrs.Service` from `h.res`/`h.store`.

**2.2 `internal/resources`** — `SampleResourcesOnce`, `RunResources`, `resourceWindow` (engine.go:2622-2701). `Sampler{Runs, Resolver, Clock, Log}`; `Run` = eager pass then 10s ticker (main.go:524-540). resources_test.go: local fixture (rule (a)). `lifecycle_test.go:175 TestPollersReadTheProjection` stays in engine as an integration test (it also covers `ReapOnce`/`Recover` and uses the engine-test `viewStore`); it builds the sampler (and in 2.3 the envstate store) from `h.store`/`h.res`.

**2.3 `internal/envstate`** — op gate (`envOp`, `envOpErrorTTL`, `ErrEnvOpInFlight`, `beginEnvOp` → `(*Store).Begin(runID, kind string) (release func(error), err error)`), status/dirty maps (`SetStatus`, `ClearDirty`, `Statuses`, `Dirty`), `ReconcileEnvsOnce` → `Reconcile(ctx)`, `EnvBadges` → `Badges(ctx)` (including deleting expired failed ops), `EnvDirty`, `EnvStatuses` (engine.go:300-360, 2517-2735). `Run` = eager pass then 30s (main.go:502-518). Engine uses it in rebuild.go:48,168-172 and `destroyEnv` (engine.go:1594). httpapi server.go:288-332 and the `ErrEnvOpInFlight` mapping (:485). Move `rebuild_test.go:173 TestEnvBadgesOverlayAndTTL` here (clock field injected). `reaper_test.go` stays in engine.

**2.4 `internal/wsinfo`** — from workspace.go: `Checkouts`, `Workspace`, `Checkout`, `ErrNoSuchCheckout`, `ErrNoSidecar` (`SendMessage`, `ErrNoAgentRunning`, `maxMessageLen` stay in engine); `WorkspaceLogTail` + `ErrNoLiveLog` (engine.go:2413-2439, :587); `ssh.go` whole (`SSHTarget`, `SetSSHDoorKey` → `SetDoorKey` under the service's own mutex, `SSHInfo`, `runByEnv`, `sshDoorKeyPath`); build cache (`buildCacheCtrl`, `BuildCache`, `ClearBuildCache`, `ErrBuildCacheOff`; engine.go:888-927, :576); and **`ErrNoWorkspace`** (service.go:531 — ssh.go:53,56,125 return it): update service.go:666,748,775 and httpapi services.go:72 to `wsinfo.ErrNoWorkspace`. Deps: `Runs`, `Resolver`, `*envstate.Store`. main: the `sshTargets` adapter (main.go:795-805) wraps wsinfo; `SetDoorKey` is still called **after** `Recover` (main.go:388). httpapi workspace.go, server.go:576-582, ssh.go:24, confapi.go:737-753. `ssh_test.go` stays in engine as an integration test (rule (b)); the SSHInfo part of editor_test.go likewise.

**2.5 `internal/envcheck`** — all of envcheck.go: `Registry` (`Start`, `Cancel`, `Latest`, `Get` ← `StartEnvCheck`, `CancelEnvCheck`, `LatestEnvCheck`, `EnvCheck`) whose own mutex guards the registry *and* every `st.snap` mutation; `Run` (← `RunEnvCheck`), `Options`, `CheckStep`, `CheckRun`. The free `destroyEnv(envp, env, emit)` becomes unexported `teardown`. Consumes `interface{ BoundFlow(ctx context.Context, projectID, name string) (*flow.Flow, error) }` — export `engine.boundFlow` (params.go:111) as `BoundFlow`. Config: `EngineURL`, clock, id generator. `egresscheck.go` stays in engine (it is a provisioning step). Update `typeNameOverrides["engine.CheckRun"]` → `"envcheck.CheckRun"`; httpapi confapi.go:351-378, routes.go:626,634, openapi.go:311, auth.go:298 (`ownerOf` ScopeEnvCheck → `s.EnvCheck.Get`); `cmd/apothecaryd/envcheck.go:79-81`. envcheck_test.go and `models_test.go:160` move with a local fixture (rule (a); `firewallAwareExec`, `waitCheck`, `hasStep` move with them); if a test needs a full run, it stays in engine (rule (b)).

**2.6 `internal/egress`** — move exactly these symbols (the line range also contains code for 2.4/2.9): `ErrEgressOff` (engine.go:35), `EgressAuditStore` (+`SetEgressAudit` removed), `egressCtrl`, `EgressDenials`, `AllowEgress`, `SetEgressRule`, `RunEgressPolicy`, `auditRows`, `IngestEgressAuditOnce`, `RunEgressAudit`, `ProjectEgressAudit`. Service fields: `Runs`, `Resolver`, `Audit AuditStore`, `ActiveRuns func() []string`. Add `(*Engine).ActiveRunIDs() []string` (the snapshot of `e.handles` keys under `e.mu`, logic at engine.go:1018-1023). `Run` = 5s ticker, **no** eager pass (main.go:546-559).
Introduce in engine:
```go
type EnvTeardown interface {
    // Forget drops per-run state; called right after the env-op gate opens, before the
    // project check, so it also runs for pre-v2 runs and when resolution fails.
    Forget(runID string)
    // BeforeDestroy runs after the run resolved, immediately before Envp.Destroy.
    BeforeDestroy(ctx context.Context, v run.View, res *resolution.Resolution)
}
```
`destroyEnv` (engine.go:1585-1621) calls every hook's `Forget` where the relay/browser/observed calls are today (engine.go:1600-1604) and every hook's `BeforeDestroy` where the egress flush is today (1613-1619). Egress implements `BeforeDestroy` (final audit flush) and a no-op `Forget`. Hooks run in registration order runports, browsermcp, egress (2.9/2.10 add theirs; until then the remaining inline calls stay where they are). Note in the commit: the in-memory forget order becomes relay → observed → browser (independent maps; harmless).
Construction cycle (egress needs the engine's active runs; the engine needs egress as a hook): main builds egress with `ActiveRuns: nil`, builds the engine, then sets `egressSvc.ActiveRuns = e.ActiveRunIDs` before any background loop starts; `IngestOnce` treats nil as "no active runs". httpapi server.go:604-752; `TestEgressAllow` (server_test.go:1165) uses `s.Egress`.

**2.7 Ask → `internal/ask/service.go`** — engine/ask.go into the existing `ask` package (keep `ask.go` = Store/MemStore) as `Service`, with types `AskState`, `AskFrame`, `ErrAskOff`, `ErrAskBusy`. Its own mutex replaces `e.mu` for `asks` and every `askRun` field. Consumes: `*run.Reader`, `resolution.Resolver` (+`resolution.ForRun`), `interface{ RunFlow(ctx context.Context, v run.View) (*flow.Flow, error); RunToken(runID string) string }` (export `engine.runFlow` as `RunFlow`), `modelcheck`, `redact.ForResolution`. Local consts replace `deltaEvery` and `maxMessageLen` (same values; comment says they mirror the engine's). Keep the never-called `a.cancel` as is (residual R2). main: `SetAskStore` removed. httpapi ask.go, routes.go:293. `ask_test.go` stays in engine as an integration test building `ask.Service` from the harness (rule (b)).

**2.8 `internal/authsink`** — logins.go: `authSink`, `setAuthSink`→`Set`, `dropAuthSink`→`Drop`, `PushCredential`→`Push`, `BrokerToken`, `AuthBrokerPath`, `providerEnvFor`→`ProviderEnv`, `RunAuthBroker`; the broker key becomes a constructor argument. Engine imports it (runloop.go:103,106,2580-2612; rebuild.go:122). main: `keeper.OnRefresh = sink.Push`. httpapi credentials.go:280-295. `logins_test.go` (builds `&Engine{}` today) moves here against the new type. `ScrubWorkspaceSecrets` stays in engine.

**2.9 `internal/runports`** — observed.go whole; `ExposeRunPort`, `RunPorts`, `ErrPortsOff` (engine.go:793-841); `(x *exec).autoExpose` (runloop.go:2525-2562) → `AutoExpose(ctx context.Context, runID string, envp ports.EnvProvisioner, id ports.EnvID) []event.Link` (synchronous, never appends); editor.go `OpenEditor`; rebuild's relay/editor block (rebuild.go:151-163) → `ReopenAfterRebuild(…)` keeping the same call order. Owns the `*portrelay.Manager`. `PortDiscoveryAutoExpose`/`PortDiscoveryMaxAuto` move from `engine.Config` to `runports.Config` (main.go:242-246). Engine consumes `interface{ AutoExpose(…); ReopenAfterRebuild(…); Expose(…) }` with today's signatures; runports implements `EnvTeardown.Forget` (relay `CloseRun` + forget observed) and a no-op `BeforeDestroy`. httpapi server.go:798-879 (editor :814). observed_test.go, expose_test.go, editor_test.go move as in-package tests (observed_test.go:85-86 rewritten against `runports.Config`); parts needing a run harness stay in engine (rule (b)).

**2.10 `internal/browsermcp`** — `OpenBrowserMCP`, `BrowserMCP`, `BrowserStatus`, `BrowserMCPSession` (+`Serve`, `Close`), `BrowserMCPCommand`, `ErrBrowserOff`, `portCache` (mcp.go:32,44,166-292). Engine keeps `ProjectSecretSource`/`SetProjectSecrets`, `stageMCP`, `failStageMCP`, `resolveMCPServer` (imports `browsermcp.BrowserMCPCommand`, mcp.go:126), `warmBrowser`, and its `ports.Browser` reference (warm + "browser present" in preflight). browsermcp implements `EnvTeardown.Forget` (port cache) and a no-op `BeforeDestroy`. httpapi browser.go. Split mcp_test.go: session tests move, stage tests stay.

**2.11 `internal/services`** — from service.go move: shim protocol as `ShimRequest`/`ShimHealth` (← `shimServiceRequest`/`shimServiceHealth`), `Request(s *resolution.Service, timeout)` (← engine `serviceRequest` from 1.4), `Shim` (← `shimService`), `lastJSON`, `States`/`State`/`Log` (← `serviceStates`/`serviceState`/`serviceLog`), the `svc*` phase constants as `Phase…`, `Live` (← `svcLive`), `StopBudget` (← `serviceStopBudget`), `Expose(ctx, exposer, runID string, svc *resolution.Service, log *slog.Logger)` (← `exposeService`), `previewURL`; run-page control: `ServiceInfo`, `ServicePortInfo`, `RunServices` type+method, `serviceRun`, `hasWorkspace`, `serviceInfo`, `firstNonEmpty`, `StartService`, `watchService`, `serviceWatchEvery`, `StopService`, `ServiceLog`, `ErrNoSuchService`; it returns `wsinfo.ErrNoWorkspace`. Consumes `interface{ AppendEvents(ctx context.Context, runID string, ps ...event.Payload) error }` — rename `engine.appendAndEmit` to exported `AppendEvents` (same per-run `appendLock`; doc comment: the only write path outside the run loop) — and the runports exposer. Engine keeps the stage executor (`stageService`, `completeService`, `stopChainServices`, `stopRunServices`, `stopStageServices`, `serviceInput`, `serviceLink`, `usesServiceStages`) importing `services`; move `appendLock` to its own engine file. httpapi services.go, routes.go:337-347. Split service_test.go: stage tests stay, run-page control tests move (local fixture with `fake.NewServices`).

After Phase 2: `go test -race ./internal/...`, archtest rows complete for all new packages.

---

## Phase 3 — Wiring and HTTP

**3.1 `engine.New(Deps)`** — replace `New(cfg, store, resolver, flows, settings, channels)` and **every** `(*Engine).Set…` method (`SetFlowBindings`, `SetSink`, `SetPortRelay`/runports setter, `SetFiles`, `SetSkills`, `SetPingLookup`, `SetProjectSecrets`, `SetBrowser`, and any setter added in Phase 2) with one `Deps` struct: `Config, Store, Resolver, Flows, Settings, Channels, Bindings, Sink, Attachments, Snapshots, Skills, PingLookup, ProjectSecrets, Browser, RunPorts, EnvState, AuthSink, Teardown []EnvTeardown, Clock, NewID, Log`. Defaults for nil: `Settings` → `StaticSettings{L: Limits{}.withDefaults()}`; `Clock` → `time.Now`; `NewID` → ulid; `Log` → `slog.Default()`; `EnvState` → `envstate.New` over the same reader/resolver/clock; `AuthSink` → `authsink.New` with a nil key. (`wsinfo.SetDoorKey` is not an engine setter and stays.)
Tests: add `newHarnessWith(t, opt func(*Deps), src, flows...)`; `newHarnessSourceOn` takes the option and `newPhasedHarness`/other helpers forward it. Convert every post-construction setter call to the option — artifacts, attach, curation, mcp, params, phase (:418), review, rungoal, sink, skills, unbound tests — and the positional `New(...)` calls at phase_test.go:449,713, server_test.go:147, confapi_test.go:70, plus httpapi artifacts/skills/browser/confapi tests. Harnesses pass `Clock` via `Deps` instead of assigning `h.e.clock`.

**3.2 `cmd/apothecaryd` split** — `cmdServe` (main.go:172-608) into `wire.go` (`buildApp(ctx, boot) (*app, func(), error)`: construction in today's order, `Recover` after all wiring, SSH door key after `Recover`), `background.go` (reaper, keeper, envstate/resources/egress `Run`, automation — all on `reaperCtx`), `main.go` (serve + shutdown in today's order: `srv.Shutdown(10s)`, `e.Shutdown()`, then the deferred closers in reverse). Behaviour identical; add no waits.

**3.3 httpapi split by area** — rename `Server.Engine` → `Server.Runs`. Split `server.go` (1216 lines) into area files (`runs.go`, `events.go`, `egress.go`, `runports.go`, `pings.go`, `channels.go`; `server.go` keeps the struct, `New`, shared helpers). Split `routes()` (routes.go:115-744) into per-area `func (s *Server) <area>Routes() []Route` in those files; `routes()` concatenates them **in today's order**. `guard`/`ownerOf` stay central (keep the `{id}`/`{pid}` path names). Gate: `openapigen -check` unchanged, `TestSpecAndRoutesAgree`, `TestEveryRouteDeclaresItsAccess`, conformance tests green. While here: `specOnce` (openapi.go:505-518) → `sync.Once`.

---

## Phase 4 — Split `internal/ports` by capability (pure move, type names unchanged)

| Package | Moves |
|---|---|
| `ports/envport` | `EnvProvisioner` + optional capabilities (`WorkspaceReattacher`, `ExecLogTailer`, `WorkspacePortExposer`, `WorkspaceRebuilder`, `EditorStarter`, `EngineURLProvider`, `WorkspaceDirtyChecker`, `SiblingPortScanner`, `ResourceSampler`, `BuildCacheController`, `Sidecar`, `WorkspaceSSH`), `PortConn`, `EnvID`, `EnvSpec`, `Command`, `ExecResult`, `EnvStatus`, `TreeFile`, `FileEntry`, `ObservedPort`, `ResourceSample`, `BuildCacheInfo`, `EditorSettings`, `ProjectHostKey`, `CodeServerPort`, `ListFilesLimit` |
| `ports/agentport` | `AgentRunner`, `AgentReattacher`, `AgentModelLister`, `AgentStopper`, `ReadOnlyCapable`, `Inbox`, `SessionID`, `AgentSpec`, `AgentInput`, `AgentEvent` + `AgentEv*`, `AgentTool`, `AgentDiff`, `AgentPlanEntry`, `UserMessage`, `MCPServer`, `Usage`, `AgentResult` |
| `ports/repoport` | `RepoHost`, `RepoRef`, MR/check/review types, `Attachment`, markers, `StackedOn`, `IsFactoryReply`, `PendingThreads`, `NewestPending`, `AggregateChecks`, `DiffLines`, `FoldedNotes`, `ErrMergeRefused`; `review_test.go` moves here |
| `ports/trackerport` | `TicketSource`, `TicketWriter`, `TicketAttacher`, `SourceTicket`, `SourceAttachment`, `ChangeType` (+parse/guess), `TicketStatus` (+helpers), `TicketLink`, `ErrStatus*`/`ErrField*` |
| `ports/egressport` | `EgressController`, `EgressAuditor`, `EgressDenial`, `EgressStatus`, `EgressPolicy` (+`Status`, `egressMatches`), audit record/row/filter; `egress_test.go` moves here |
| `ports/browserport` | `Browser`, `BrowserSession`, `BrowserStatus` |
| `ports/notifyport` | `NotificationChannel`, `Notification` |

- `Verdict`, `VerdictComment` move into engine (only engine uses them; they follow into runcore in 5.6 if runcore uses them).
- Exported helpers with no users (`ReviewMarkerOf`, `MarkerRound`, `ChangeTypes`): delete only if the build proves no user; otherwise move with their family.
- Allowed sub-package edges: any → `envport`; `agentport` → `envport`; `egressport` → `envport`; `trackerport` → `repoport` (`TicketAttacher.Attach` returns `Attachment`). If another cycle appears, move the shared type down and record it under Deviations. Archtest: `internal/ports/` may import only `internal/ports/`.
- Mechanics, one sub-package per commit: create the package file and move the declarations with their comments; fix references *inside* the moved code and moved in-package tests by hand (they were unqualified, e.g. egressport → `envport.EnvID`); rewrite callers per symbol with `gofmt -w -r 'ports.X -> envport.X' ./internal ./cmd`; then fix import lines by hand (or with `goimports` if available offline). Delete `ports/ports.go`/`ticketsource.go` when empty.

---

## Phase 5 — Interpreter functional core

Facts this phase relies on (verified):
- `replay(evs, fl)` (state.go:409-688) is pure. `x.append` (runloop.go:317) scrubs payloads, appends under `appendLock`, then applies only `run.Apply` (:373) and `forgetCurated` (:324).
- Live hand mutations happen *after* `x.append` returns (e.g. `takeEdge` runloop.go:509-516, `parkAt` scheduler.go:75, cost/turns :1140) and some *before* (`iters++` before `StageStarted`, runloop.go:2291, condition.go:40).
- `event.MemStore.Clock` is injectable (event/store.go:36). There is no `TestMain` in `internal/engine` today.
- No `runState` field holds a channel, func or engine machinery pointer; all are values/maps/slices/pointers to event/flow/ports values.

**5.1 Fold-invariant check (test only).**
- Hook `var foldCheck func(x *exec)` (nil in production), called at the top of each `for !rs.done` iteration in `runLoop` (runloop.go:168), after each `evaluateIdle`, and in `x.exit`. Not from `append`.
- New `internal/engine/main_test.go` with `TestMain` installing it: load the run's events from the store, `replay` them with `x.rs.fl`; if `replayed.ph == phAfterComplete && live.ph != phAfterComplete`, apply the same success routing to the replayed copy (a test helper mirroring `advanceWith`; replaced by `successTarget` in 5.5c); then `diffState(live, replayed) []string` — hand-written, in-package, an explicit list of every `runState` field (no reflect on unexported fields). `view` is excluded permanently (`run.Apply` already covers it, and out-of-loop `ServiceStarted/…` appends via `AppendEvents` legitimately reach only the replayed view).
- Violations go into a mutex-guarded map keyed by run id; the harness cleanup fails the test if any of its runs has one.
- Start with `foldAllow` = every field that fails today (run the suite; list them under Baselines). Each later step shrinks it; at the end of 5.4 it is empty (`view` aside).

**5.2 Extract the fold.** `func (rs *runState) apply(ev *event.Event)` = the per-event body of `replay` (incl. `run.Apply` on `rs.view`); `replay` = new state + loop `apply` + any post-loop code that exists today. No behaviour change.

**5.3 Memos off the state.** `runState` becomes exactly the fold. Per-loop memos move to `exec`: `followed` (phase.go:113) → `x.followed`; live deletion of curated paths (curation.go:190) → `x.materialised map[string]bool` that `materialiseCurated` consults. Golden unchanged.

**5.4 Live fold.**
- *Commit A (atomic)*: `x.append` calls `x.rs.apply(&ev)` for each appended event, replacing `run.Apply` + `forgetCurated`, **and in the same commit deletes every non-idempotent hand mutation the fold now also performs**: `rs.cost +=`/`rs.turns +=` (runloop.go:1140-1141, 1193-1194), `rs.unwanted++` (:1522), `rs.pendingMsgs = append` (:1472), `rs.confirms = append` (phase.go:645), `rs.startPhase` at phase.go:659 (`phaseRuns++`), `rs.noteCurated` at curation.go:151 (`dropped` append), `h.banked.Add(leg)` (~runloop.go:1185; the fold adds `p.Hold`), `rs.park` at scheduler.go:75. Keep a hand mutation only where the fold needs a value the event does not carry, and document it.
- *Commits B…*: remove the idempotent hand mutations (`iters`, `ph`, `edgeCounts`, `addMR`, `sessions`, `delivered`, …) event type by event type; each keeps golden + fold check green.
- The only hand mutation left at the end is the success cursor move (`advanceWith`); document it on `runState` (Decision 7).

Divergences, each its own commit (bugs: regression test first, shown failing on the parent):
- **D1 (bug) — recovery after `StageCompleted`.** `routeAfterComplete` (runloop.go:579) re-reads the verdict but skips `VerdictReceived` and `checkBudget`. Fix: one `afterComplete(st *flow.Stage, v *ports.Verdict)` path for live and recovery. Live passes the verdict it parsed; recovery reads it with `readVerdict` only when `st.Type == StageAgent && verdictPath != ""`. Append `VerdictReceived` only when `v != nil` and a new fold flag (set by `VerdictReceived`, cleared by `StageStarted`) is unset. Call `checkBudget` only for agent stages (live calls it only at runloop.go:1148). Skip both for a skipped completion (`rs.elided[st.ID]` — `skipStage`/`actionSkip` do neither). Keep the `emptyRound` short-circuit first. Test: seed a log ending at an agent `StageCompleted`, with the verdict file present in the fake workspace; with the budget exceeded `Recover` abandons the run; with budget fine exactly one `VerdictReceived` follows the `StageCompleted`.
- **D2** — edge to `end`/`done`: fold semantics win (feed cleared live too). Covered by golden + fold check.
- **D3** — `actionPending`: the fold keeps clearing it **only** on `StageCompleted` (state.go:497, unchanged; `infraFail`/`trackerFail` rely on it surviving `StageFailed` so a retry reconciles). Delete the success-path hand deletes (publish.go:94,158; merge_review.go:188,217,238; review_round.go:391; ticket.go:163,208), each followed by `StageCompleted`. Also delete the refused-merge hand delete (merge_review.go:229): a refused merge now stays pending and the next attempt checks `MRStatus` first (strictly safer). That removes one `ActionStarted` on the retry — the golden change is named in the commit. Residual R3: a crash between `MRMerged` and `StageCompleted` can append a second `MRMerged` (write-only history event).
- **D4** — `curated`/`followed`: done in 5.3.
- **D5** — `openPings`, `agentCursor` are now maintained live by the fold (harmless; `SnapshotCaptured` already uses max at state.go:465).
- **D6** — `elided` is now set by the fold for `actionSkip` (ticket.go:44). Automatic.
- **D7** — clock: the fold uses `ev.TS` for park `since` and hold `takenAt`; live code stops reading `e.clock` for them. Every harness gives one clock to both `event.MemStore.Clock` and `Deps.Clock` (`newPhasedHarness`'s `offsetClock`, phase_test.go:73), keeping wait-timeout tests deterministic.
- **D8 (bug) — gate answer lost after `PhaseResumed`.** Test: seed `RunCreated`, `PhaseStarted`, `StageStarted(gate)`, `PingRaised(gate)`, `PhaseParked{PingID}`, `PingAnswered`, `PhaseResumed`; `Recover` → no second `PingRaised`; the run routes by the recorded answer. Fix in `unpark` (state.go:298), after restoring the parked record: `if pr.ping != "" { if a := rs.answeredPings[pr.ping]; a != nil { rs.gateAnswer = a } }` (the answer is already in `answeredPings`, which live `tryResume` uses, scheduler.go:127).
- **D9 (bug) — wait timeout restarts after a crash in the resume window.** The window is `PhaseResumed` → crash before `StageCompleted`/`StageFailed` (scheduler.go:92-124): replay unparks (`ph = phIn`), `stageWait` at `phIn` appends a new `PhaseParked`, whose `ev.TS` becomes the new `since`. Test: seed `StageStarted(wait)`, `PhaseParked` at T0, `PhaseResumed`; `Recover` at T0+½timeout with the shared clock → the run re-parks and the timeout edge fires at T0+timeout (not T0+½timeout+timeout). Fix in the fold: `parkSince map[string]time.Time` keyed by stage id, set by the first `PhaseParked` of a stage after its `StageStarted`, cleared by `StageStarted`; `park` uses `parkSince[stage]` instead of `ev.TS` when present. No wire change.
- **D10 (bug) — held runs restart every boot.** A held run's log is `HoldTaken` → `StagePaused{Reason: PauseHeld}` → `RunPaused{Reason: PauseWaiting}` (`PauseHeld` is a `StagePaused` reason, event.go:152); `Recover`'s dormant branch (engine.go:1819-1834) sees no pings, `answersAllIn` true, and starts it, and it re-parks. Test: seed exactly that sequence; `Recover` twice → no new events, no loop started. Fix: in the dormant branch, after loading `evs`, `if len(replay(evs, nil).pausedHold) > 0 { continue }` (`pausedHold` is set by `StagePaused{held}` and cleared by `StageResumed`, state.go:526,537). Only `wakeHeld` wakes such runs.
- Residuals recorded, not fixed: R1 an answer sent during a command/action stage waits up to 30s and reports busy (engine.go:2298-2310); R2 ask `a.cancel` never called; R3 above.

**5.5 Pure functions** (still in engine; files `routing.go`, `attempt.go`, `idleplan.go`; one commit per sub-step with table tests; no I/O, no clock reads — `now` is a parameter):
a. Already pure → `routing.go`: `chainKey`, `chainMax`, `failEdgeKind`, `needsWorkspace`, `anyWorkspaceStageStarted`, `gateQuestion`, `gateVerdict`/`verdictText`/`capVerdict`/`renderCuration`, `parseVerdict` (prompt.go:292), `unanswered`, `waitPrereq`, the classification inside `trackerFail`.
b. `budgetExceeded(budget, cost, turns) (bool, string)`; `checkBudget` is the shell.
c. `successTarget(rs, st, verdict)` used by `advanceWith`, the outcome case of `startPhase`, and the 5.1 helper (delete the helper).
d. `resolveEdge(rs, st, kind, feed string, scrub func(string) string) edgeOutcome` → `{edge *event.EdgeTaken | deadEnd{reason, kind} | abandon{reason}}`; `takeEdge` appends the edge (the fold moves pos/ph, incl. `phDebug`) or runs `deadEnd`/`abandon`.
e. `stageCondition` pure over the facts it reads; `nextStep(rs) step` for the dispatch switch (runloop.go:168-273).
f. Attempt classification: the shell gathers checks **in today's order with today's short-circuit** (I/O unchanged) into `attemptObservation`; `classifyAttempt(obs) attemptOutcome` → `{fail{reason, edgeKind} | complete{feed, verdict}}` (agent runloop.go:1030-1155, command :1707-1844).
g. `routeAfterFail`/`afterComplete` built on (c)/(d).
h. Idle: `signalOn`, `waitState`, `outcomeHolds`, `anyArmed` take `now`; `planResume(pr, st, obs, now, answeredPings)` for `tryResume`; `planIdle(rs, obs, now) idlePlan` (ordered actions: resume / decline / ask / begin / complete, plus holds) reproducing `evaluateIdle` (phase.go:305) exactly — several declines and confirm-asks per tick, confirm dedup that sees asks planned earlier in the same tick, stop at the first begin/resume. `evaluateIdle` executes the plan. Guarded by phase_test, watcher_test, outcome_test.
i. From `awaitAgent`, only the pure tables: the hold request switch (:1477-1498) → `holdAction(…)`, outcome classification (:1400), hold-spend accounting. The select loop stays.

**5.6 Move to `internal/runcore`.**
- Commit A, inside engine: rename `runState` fields and the 5.5 functions to their exported forms (`pos`→`Pos`, …), using gopls rename if available, else careful per-field `gofmt -w -r 'x.pos -> x.Pos'`-style rewrites plus build-driven fixes.
- Commit B: move into `internal/runcore`: `runState` (→ `State`), the phase enum, `apply` (→ `Apply`), `replay` (→ `Replay`), **every** `func (rs *runState)` method — including `park`, `unpark`, `startPhase`, `noteCurated`, `forgetCurated`, `notePush`, `addMR`, `delivered`, `decidesHere`, `phaseEverRan`, `anyParked`, `roundIDs`, `roundThread`, `openPing`, `openAgentPings`, `noteCursor`, `checkout`, `checkoutDirs`, `noteHead`, `knownHead`, `mrFor`, `firstMR`, `unskippedRound` (curation.go:198) — the types `phaseRun`, `heldState`, `mrRecord` and any other type used by a field, the helpers `checkoutsOf`, `needsWorkspace`, `agentPingType`, `triggerKey`, `phaseRunKey`, `chainKey`, and all 5.5 functions. Use `event.PingGate` etc. directly inside runcore (engine keeps its `PingGate`/`PingDebug` re-exports). Engine's `exec.rs` becomes `*runcore.State`.
- Move `state_test.go` and the 5.5 table tests to runcore; the fold-invariant hook and `diffState` stay in engine (`diffState` now compares exported fields).
- Archtest: runcore row from the target map, plus a check that no non-test `.go` file under `internal/runcore` contains `time.Now(`.

---

## Phase 6 — Docs

- `docs/IMPLEMENTATION_NOTES.md` (newest at the bottom): `## Clean architecture refactor (TODO 7, <date>)` — package map, decisions, D1–D10 outcomes, residuals R1–R3, deviations.
- README "Development": a short package-map paragraph.
- `TODO.md`: remove item 7.
- `docs/clean-architecture-plan.md`: Status → done, with the commit range.

## Out of scope, on purpose

UI (`cpui/`); splitting `internal/event`; httpapi sub-packages; services/Temporal; rewriting `awaitAgent`, `stageAgent`'s invocation loop or the action executors; DB schema; public API changes; renaming stuttering types after the ports split; adopting golangci-lint; fixing R1–R3.

## Verification

- Every commit: the gate. Every phase end: `go test -race ./internal/...` vs baseline, and `go test ./internal/engine/ -count=3` (golden + fold check stable).
- Throughout: `git diff --stat main -- api/openapi.yaml cpui/` stays empty.
- Each Phase 5 bug test is run on the parent commit (fails) and on the fix commit (passes).
- End-to-end smoke after Phases 2, 3 and 5: `task build`, then `go run ./cmd/apothecaryd serve -dev` (fake adapters + seeded project) and `cd cpui && npm run dev`. Create a ticket run on the seeded project, answer its gate, and exercise every moved panel: workspace badge/status, resources graph, ports + observed ports, egress denials/allow, services start/stop/log, Ask pane question, editor open, MR list, envcheck from project settings. Restart the daemon mid-run and confirm the run resumes.
- Final: `task verify` (includes the untouched UI check/build).

## Baselines

Measured at 8abd1a4 on 2026-10-30 (go1.27.2, linux/amd64).

- `go test ./... -count=1` ×3: runs 1 and 2 green (51 packages ok); run 3 failed `internal/sshdoor` `TestDoorRefusals` ("the disabled user's session is still open", door_test.go:407). Pre-existing flake.
- `go test -race ./internal/... -count=1`: green (47 packages ok), no data race reported.
- Pre-existing `internal/engine` flakes, seen only under repeated runs (`-count=10` and up, or `-race -count=6` and up), reproduced with the golden hook disabled too: `TestPauseOnAParkedRunHoldsItThroughTheAnswer`, `TestForkMergeRequestIsReadOnlyAndRerunStaysOnIt`, `TestSSHDoorReachesTheRunsWorkspace`; under `-race`: `TestHoldParksOnAQuietHolderAndWakesOnTheirMessage`, `TestTakeOverKeepsTheSessionOpenUntilReleased`, `TestReleasingAParkedHoldResumesWithTheHandback`. A phase-end check is "no worse" when it fails only on these.
- Golden event logs (0.3): 253 recorded; 3 removed as non-deterministic after `go test ./internal/engine/ -count=40` and `-race -count=6` sweeps — `TestRunSourceBranch` and `TestCreateRunRefusesATrackerThatCannotWrite` (the test ends while a run is still going, so Shutdown cuts the log at a timing-dependent point), `TestTakeOverKeepsTheSessionOpenUntilReleased` (one diff in a race sweep). 250 committed; re-swept clean (`-count=30`, `-race -count=8`).

## Deviations

- **0.3** Golden files are `testdata/golden/<test>.golden`, not `.log`: the repository's `.gitignore` ignores `*.log`. Failed or skipped tests are neither compared nor recorded. The repeat sweep used `-count=40` (plus `-race`) instead of `-count=3`, to catch rarer non-determinism.
- **1.1** Only the engine's `shellQuote`/`tail` moved; the adapters (`acpagent`, `workspace`, `github`, `gitlab`, `wsimage`, …) keep their own private copies — consolidating them is outside this refactor.
- **1.4** The shim request builder became the free engine function `serviceRequest(s *resolution.Service, timeout)` as planned; the merged signal/phase doc comment was split so `SignalAvailable` and `PhaseAvailable` each carry their own.
- **1.5** `engine/bootstrap.go` had no tests of its own to move.
- **1.6** Only `TestAgentAccepts` (pure) moved to `modelcheck`; the other model tests drive a harness and stay in the engine (rule (b)).
- **1.8** The same load-and-reduce block was also inlined in `Archive`, `Rerun`, `RetryRun` and `Abort`; those use `run.Reader.View` too (identical behaviour). `Play` keeps its own load because it reads the events again for pings. httpapi's private copy of `views` (`Server.runViews`) now delegates to `run.Reader.All`. `run.Reader` gained a unit test.
- **1.9** `ports.PendingThreads`/`NewestPending` gained a table test.
