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
  the editor's connections, an SSH session. Each holds the run busy while
  open and marks it used when it ends. The idle time runs from the last
  use, or from when this engine first saw the sandbox (its first sweep
  after a start) for one nobody touched since.
- **The sweep** runs every minute (`Engine.RunIdleSweep`). It skips a run
  with an envstate operation in flight (rebuild, destroy, a wake). A sleep
  that fails is retried by the next sweep.

## What sleeping is

- `envport.EnvSleeper`. The kubernetes host deletes the pod and keeps its
  claims and its gateway. A marker `.asleep` in the engine-side run folder
  makes `Status` answer `asleep` rather than `absent`, so the reconciler
  neither reports the sandbox gone nor provisions one.
- **Waking is a Provision of the same spec.** It finds the claims, starts a
  pod on them, and removes the marker. `WakeEnv` then does what a rebuild
  does after its provision: it writes the run env, delivers attachments,
  and reopens exposed ports.
- **Only `pvc` storage sleeps.** With `emptydir` the disk goes with the pod,
  so stopping would be a destroy. `CanSleep` refuses there. The
  provisioner's runtime offer carries the reason (`noSleep`), the form
  shows it, and a workshop's own minutes are refused on save. Kata
  sandboxes are emptydir only (the note
  2026-11-22-kata-sandboxes-run-on-emptydir-disks), so they never sleep.
- **A sleeping sandbox still counts toward `MaxEnvironments`.** Its claims
  hold storage, and it keeps its `EnvID`. Freeing the slot would let a wake
  overrun the cap.
- **Events.** `env_slept` and `env_woke` (by what: stage, terminal,
  editor, port, ssh, person, ask; or the error) are on the run's record.
  They are not fact-bearing, so a run's facts and flow state are the same
  asleep or awake. The envstate operations `sleeping` and `waking` are what
  the badges show.

## Wakes

- **A stage** (`ensureEnv`): a run resumed onto a sleeping sandbox wakes it
  before anything runs, waiting out a sleep still in flight.
- **The terminal, the editor, an exposed port, the assistant:** each wakes
  the sandbox before connecting.
- **SSH** wakes before dialling upstream, with up to 5 minutes for the pod.
- **A person:** the run header's Wake button (`POST /api/runs/{id}/wake`).
  A wake already under way answers 503 with `Retry-After`. A wake that
  fails answers 502 with the error.

Processes the sandbox ran (dev servers, services) do not survive. They
start again from the project's hooks, as after a rebuild.

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
    status, wake by provision, the emptydir refusal);
  - `TestAnIdleWorkspaceSleepsAndWakes`, `TestWhatKeepsAWorkspaceUp`,
    `TestSleepAndWakeFailures` and `TestAStageWakesAfterASleepInFlight`
    (engine);
  - `TestAQuestionWakesAndHoldsTheWorkspace` (ask);
  - `TestWakeWorkspace` and `TestWorkshopIdleSettingIsChecked` (httpapi);
  - `limits_ask_test.go` (pgstore);
  - in the UI: `SandboxIdleField`, `LimitsSection`, `WorkspaceStatus`,
    `WorkspaceControls`, and `derive` (the two events).
- Live: `TestLiveK8sLifecycle` sleeps and wakes when the host can
  (`liveSleepWake`).
