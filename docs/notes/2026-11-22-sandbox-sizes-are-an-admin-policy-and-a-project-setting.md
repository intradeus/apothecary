# Sandbox sizes are an admin policy and a project setting

Phase 2 of `docs/sandbox-runtimes-plan.md`. A run's sandbox now has a size
(CPU and memory), and nothing about it was obvious from the plan alone.

## Why

Before this, a sandbox pod had no resources at all: one run could take the
node's memory and the kernel chose which pod to kill. A size bounds each
sandbox, and the scheduler reserves room for it.

## What now

- **Two places hold a size.** The admin's policy (System → Limits) has a
  *default*, for a project that sets none, and a *ceiling*, the most any
  project may have. It lives in four integer columns of the limits row
  (`0019_sandbox_sizes.sql`, 0 = unset). A project's own size lives in its
  workspace JSON (`workspace.size`), so it needs no migration.
- **Each field resolves on its own**: the project's value, else the
  default, else nothing, then capped by the ceiling
  (`envport.SandboxPolicy.Effective`). A ceiling is never a size: a field
  neither sets is *limited* at the ceiling (`SandboxPolicy.Cap`,
  `EnvSpec.SizeCap`) and requested at a floor (100m CPU, 1 GiB memory,
  `cappedMemoryRequest`). Sizing it at the ceiling, as this first did, made
  a 64 GiB ceiling ask every unsized sandbox's node for 64 GiB while the
  form said "no limit"; the form now shows "up to" the ceiling. A Kata
  sandbox still needs a real size (its VM is sized from it).
- **Saving checks only what changes** (`SandboxPolicy.CheckChange`). A
  project saved above the ceiling is refused (400); one saved before the
  ceiling was lowered is capped at its next sandbox, and saving it again
  with that size is accepted (the form says it is capped), so lowering the
  ceiling does not lock every such project's form until someone edits its
  size.
- **Bounds**: a set field is 100 mCPU to 1000 CPUs, and 512 MiB to 16 TiB.
  The floor catches a slip of the unit (a dind sandbox under 512 MiB cannot
  start its daemon); the top keeps a value within the `integer` columns and
  what Kubernetes takes, so a typo is a 400, not a 500 from the store.
- **An unreadable policy is never no policy.** The resolver keeps the last
  policy it read and uses it when a read fails; with none read yet the
  resolve fails. It used to size nothing, which let a store blip lift
  every ceiling.
- **Sidecars**: the tunnel (`wg`) and the relay request 10m CPU and 32 MiB,
  so the scheduler counts them, with no limit (a CPU limit on wireguard-go
  would throttle the sandbox's whole network).
- **Pod resources**: memory is requested at its limit. Memory cannot be
  overcommitted safely: a node short of it evicts or kills. CPU is limited
  at the size and requested at a quarter of it (`cpuRequestShare`), with a
  100m floor (`minCPURequest`) and never above the limit. Agents idle most
  of the time, so reserving their full CPU would leave nodes empty.
- **A size applies to the next provision or rebuild**, never to a running
  sandbox. Kubernetes cannot resize a running pod's memory down safely, and
  the pod spec is fixed at creation. A provision reuses the run's pod as it
  is (a stage must not lose a pod it did not ask to change). A rebuild
  compares the pod's shape (runtime, class, user namespace, the sandbox's
  resources: the `apothecary.io/shape` annotation, `k8s_shape.go`) with
  what the workshop now gives, and replaces a pod of another shape when
  both keep the run on its claim. A pod whose disks go with it (emptydir
  storage, a VM runtime's own disks, or a switch between the two) is kept:
  replacing it would lose the checkout and any unpushed commit, so the
  rebuild log, and the form's help, say the change waits for the next run.
  A pod an older engine made has no shape and is replaced on a rebuild.
- **Out of memory.** Verified under sysbox on the host's docker: an inner
  process over the limit is killed (exit 137), the sandbox stays up, and
  the cgroup's `memory.events` `oom_kill` count goes up. The sampler reads
  that count (`ResourceSample.OOMKills`), and the run page says so beside
  the graphs. The run's exec log (its workspace log) also says it, once per
  kill (`k8s_oom.go`): the kernel's kills inside the sandbox, and the
  sandbox container itself killed (`lastState.terminated.reason:
  OOMKilled`) and restarted. What the log has told is kept in the run folder
  (`oom-seen`: the pod's uid, the container's restarts, the cgroup's
  kills), so an engine restart neither repeats it nor loses a kill that
  happened while it was down, and a new pod or container starts its count
  again. The log, not the run's events: these are observations of the
  sandbox, not transitions of the run.
- **Usage evidence** for choosing a size: the resources sampler keeps each
  project's last 10 runs' peak memory, limit and OOM kills
  (`GET /api/projects/{id}/sandbox-usage`), shown under the size fields. It
  is memory-only, like the graphs, so an engine restart forgets it. A table
  for it would be history nobody asked for. A project no run refers to any
  more leaves it at the next sample pass.
- **A pod that cannot be scheduled says why.** While a sandbox is Pending,
  its newest warning event (for example `Insufficient memory`) is appended
  to the progress line and to the timeout error.

## Tests that hold it

`internal/ports/envport/size_test.go`; `TestSandboxResources`,
`TestK8sUnschedulablePodSaysWhy`, `TestK8sRebuildReplacesAPodOfAnotherShape`,
`TestPodShape` and `TestK8sSampleTellsTheMemoryLimitsKills` (workspace
adapter); the `capped-privileged-pvc` pod golden;
`TestSampleOncePrunesGoneProjects`;
`TestParseCgroupSampleOOMKills`; `TestSandboxSizesAreALimit` and
`TestSandboxUsage` (httpapi); `internal/workspace/resources/usage_test.go`;
`internal/project/integrations/size_test.go`; the `sized-sysbox-pvc` pod
golden; and in the UI, `SizeFields`, `SandboxSizeField`, `LimitsSection`
and `ResourceGraphs` tests.
