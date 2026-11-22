# Idle sandboxes stop and keep their disks

Phase 6 of `docs/sandbox-runtimes-plan.md`, stop-on-idle: a sandbox nobody
uses is stopped after a period, and its checkout and containers are kept. Its
next use starts it again.

## What sleeps, and when

- **The period.** System → Limits has the instance default (minutes; 0 is
  never, the default). A workshop can set its own minutes, or -1 for never
  (`SandboxPolicy.IdleAfter`, column `sandbox_idle_minutes`, migration
  0020).
- **Only runs with no live loop sleep.** A run whose loop is live (a stage
  working, a hold, a run waiting for an answer) keeps its sandbox. The plan
  called the agent "a user"; tying sleep to the loop covers that without a
  heartbeat from the harness. A completed, paused or parked run's sandbox
  is a candidate.
- **What counts as use** (`internal/workspace/idle`, keyed by run): an
  open terminal, a question to the run's assistant, an exposed port's or
  the editor's connections, an SSH session, and every read of the
  workspace's files (the Files pane, Changes, a diff). Each holds the run
  busy while open and marks it used when it ends. The Files pane's live
  refresh polls only while the pane is on screen, so a person looking at
  it keeps the sandbox up; a background tab does not. The idle time runs from the last
  use, or from when this engine first saw the sandbox (its first sweep
  after a start) for one nobody touched since.
- **The sweep** runs every minute (`Engine.RunIdleSweep`). It skips a run
  with an envstate operation in flight (rebuild, destroy, a wake). A sleep
  that fails is retried by the next sweep. A run whose sandbox cannot be
  kept (below) is passed over without a word: the instance's default
  reaches it too, and a warning every minute for every such run would
  only repeat itself.
- **A sweep and a use never cross.** They take two claims in opposite
  orders. The sweep claims the `sleeping` op, then looks at the run's use
  and its loop again, and lets the run be if either moved since its first
  look. A use takes the tracker first (`Engine.UseEnv`; a loop registers
  its handle), then waits out a `sleeping` or `waking` op
  (`envstate.WaitOps`) before reading the status. Whichever comes second
  sees the other: the sweep finds the run in use, or the use finds the
  sleep in flight and wakes the sandbox after it. Without this a stage, or
  a terminal, starting during a sweep entered the pod being deleted.

## What sleeping is

- `envport.EnvSleeper`. The kubernetes host deletes the pod and keeps its
  claims and its gateway. A marker `.asleep` in the engine-side run folder
  makes `Status` answer `asleep` rather than `absent`, so the reconciler
  neither reports the sandbox gone nor provisions one.
- **Waking is a Provision of the same spec.** It finds the claims, starts a
  pod on them, and removes the marker. `WakeEnv` then does what a rebuild
  does after its provision: it writes the run env, delivers attachments,
  and reopens exposed ports.
- **Only a sandbox on its claims sleeps.** With `emptydir` storage the disk
  goes with the pod, so stopping would be a destroy; a Kata sandbox's disks
  are always its own emptyDirs (the note
  2026-11-22-kata-sandboxes-run-on-emptydir-disks). So the answer is per
  runtime: `CanSleep(runtime)` refuses either, and `Sleep` asks the live
  pod (a pod built under kata before its workshop switched to sysbox is
  still a VM). The provisioner's runtime offer carries the reasons, the
  storage's (`noSleep`) and each approved runtime's
  (`approved[].noSleep`). The form shows the one for the workshop's
  runtime, and the save refuses a workshop's own minutes there
  (`idleFits`, against the workshop's own provisioner, as `runtimeFits`
  checks).
- **A sleeping sandbox still counts toward `MaxEnvironments`.** Its claims
  hold storage, and it keeps its `EnvID`. Freeing the slot would let a wake
  overrun the cap.
- **Events.** `env_slept` and `env_woke` (by what: stage, terminal,
  editor, port, ssh, files, person, ask; or the error) are on the run's
  record. They are not fact-bearing, so a run's facts and flow state are
  the same asleep or awake, and they do not move the run's `UpdatedAt`: a
  completed run's reaping window counts from its last real change, not
  from the sweep that stopped its sandbox. The envstate operations
  `sleeping` and `waking` are what the badges show.
- **What is kept across a sleep** (`engine.EnvSleepHook`): the exposures
  stay on their URLs, dormant (`portrelay.Manager.Sleep`), and the first
  connection to one wakes the sandbox and is served by the exposure the
  wake opens again; an old link works. The hosts the run's egress had
  allowed stay allowed. The discovered ports, the tunnels' paths and the
  browser go with the processes.

## Wakes

- **A stage** (`ensureEnv`): a run resumed onto a sleeping sandbox wakes it
  before anything runs, waiting out a sleep still in flight.
- **The terminal, the editor, an exposed port (a new link or an old one),
  the Files view, the assistant:** each holds the sandbox, then wakes it,
  before connecting.
- **SSH** wakes before dialling upstream, with up to 5 minutes for the pod.
- **A person:** the run header's Wake button (`POST /api/runs/{id}/wake`).
- A use that arrives while a sleep or a wake is under way waits for it
  (bounded by the request) and meets the sandbox up: no "try again", which
  the terminal never did. Another op in flight (a rebuild, a destroy)
  answers 503 with `Retry-After`. A wake that fails answers 502 with the
  error.

Processes the sandbox ran (dev servers, services) do not survive. They
start again from the project's hooks, as after a rebuild.

## Telemetry

The memory gauges (`apothecary.sandboxes.memory.used` and `.limit`) add
up the last sample pass: every sandbox that answered it, a completed run's
not yet reaped included (it holds its node's memory, though its graphs are
over), a sleeping one's not (its last sample is not carried forward).

## Measured

On k3d (local-path pvc), `TestLiveK8sLifecycle`'s sleep step: the pod is
gone, the status is `asleep`, and a wake brings it back in about 10 s on the
same claims. The checkout keeps an uncommitted file, and the inner docker
keeps its container id.

## Tests that hold it

- Unit:
  - `TestIdleAfter` (envport);
  - the tracker's tests (`internal/workspace/idle`);
  - `sleep_test.go` in `internal/adapters/workspace` (Sleep, the marker,
    status, wake by provision, the emptydir refusal, the VM refusal by
    runtime and by live pod);
  - `TestAnIdleWorkspaceSleepsAndWakes`, `TestWhatKeepsAWorkspaceUp`
    (which also holds the sweep silent), `TestSleepAndWakeFailures` and
    `TestAStageWakesAfterASleepInFlight` (engine);
  - the races, each interleaving forced through a hook in the fake
    provisioner or the resolver: `TestASweepLeavesAWorkspaceUsedSinceItLooked`,
    `TestAUseDuringASleepWakesTheWorkspaceAfterIt`,
    `TestAStageStartingDuringASleepWakesTheWorkspace`,
    `TestASecondUseWaitsForTheWakeInFlight`, `TestAWakeUnderAnotherOpIsRefused`,
    `TestAStageWakeOfAWokenWorkspaceDoesNothing`, `TestASleepTellsItsHooks`
    (engine); `TestWaitOps` (envstate);
  - `TestKubernetesSleepRefusals` (kinds);
  - `TestAnExposureWakesItsSleepingWorkspace` and
    `TestADormantExposureThatCannotWake` (portrelay), `TestSleptKeepsTheExposures`
    (runports);
  - `TestSleepAndWakeDoNotMoveUpdatedAt` and `TestFactBearing` (run);
  - `TestMemoryGaugesByRuntime` (resources);
  - `TestAQuestionWakesAndHoldsTheWorkspace` (ask: the hold before the wake);
  - `TestWakeWorkspace` (httpapi: the Files view wakes, another op is a
    503) and `TestWorkshopIdleSettingIsChecked` (httpapi);
  - `limits_ask_test.go` (pgstore);
  - in the UI: `SandboxIdleField`, `LimitsSection`, `WorkspaceStatus`,
    `WorkspaceControls`, and `derive` (the two events).
- Live: `TestLiveK8sLifecycle` sleeps and wakes a sandbox on a claim
  (`liveSleepWake`: the checkout, the workspace container, and a project
  container with a restart policy running again after the wake), and
  checks that one on emptyDir refuses to sleep. Neither path is skipped.
