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
  (`envport.SandboxSizes.Effective`). A ceiling with no default bounds every
  sandbox. A project saved above the ceiling is refused (400). A project
  saved before the ceiling was lowered is capped at its next sandbox, not
  refused.
- **Pod resources**: memory is requested at its limit. Memory cannot be
  overcommitted safely: a node short of it evicts or kills. CPU is limited
  at the size and requested at a quarter of it (`cpuRequestShare`), with a
  100m floor (`minCPURequest`) and never above the limit. Agents idle most
  of the time, so reserving their full CPU would leave nodes empty.
- **A size applies to the next provision or rebuild**, never to a running
  sandbox. Kubernetes cannot resize a running pod's memory down safely, and
  the pod spec is fixed at creation.
- **Out of memory.** Verified under sysbox on the host's docker: an inner
  process over the limit is killed (exit 137), the sandbox stays up, and
  the cgroup's `memory.events` `oom_kill` count goes up. The sampler reads
  that count (`ResourceSample.OOMKills`), and the run page says so beside
  the graphs.
- **Usage evidence** for choosing a size: the resources sampler keeps each
  project's last 10 runs' peak memory, limit and OOM kills
  (`GET /api/projects/{id}/sandbox-usage`), shown under the size fields. It
  is memory-only, like the graphs, so an engine restart forgets it. A table
  for it would be history nobody asked for.
- **A pod that cannot be scheduled says why.** While a sandbox is Pending,
  its newest warning event (for example `Insufficient memory`) is appended
  to the progress line and to the timeout error.

## Tests that hold it

`internal/ports/envport/size_test.go`; `TestSandboxResources` and
`TestK8sUnschedulablePodSaysWhy` (workspace adapter);
`TestParseCgroupSampleOOMKills`; `TestSandboxSizesAreALimit` and
`TestSandboxUsage` (httpapi); `internal/workspace/resources/usage_test.go`;
`internal/project/integrations/size_test.go`; the `sized-sysbox-pvc` pod
golden; and in the UI, `SizeFields`, `SandboxSizeField`, `LimitsSection`
and `ResourceGraphs` tests.
