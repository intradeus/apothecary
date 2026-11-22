# Sandbox telemetry labels a run by its workshop's runtime

Phase 7 of `docs/sandbox-runtimes-plan.md` asks for sandbox counts by
runtime and state, and memory held against limits, with no stored value in
a label.

## What now

- `apothecary.sandboxes` (engine): run sandboxes by `runtime` and `state`.
  The state is the workspace badge: the reconciled status, overlaid by an
  op in flight (`running`, `asleep`, `sleeping`, `waking`, `rebuilding`,
  …). Absent sandboxes are not counted.
- `apothecary.sandboxes.memory.used` and `.limit` (bytes, the resource
  sampler): the last sample pass, summed per `runtime`: every sandbox that
  answered it, a completed run's not yet reaped included, a sleeping one's
  not (the note 2026-11-22-idle-sandboxes-stop-and-keep-their-disks).
  Used is the cgroup's working set, and the limit is the node's memory
  when the sandbox is uncapped.
- Both are on the engine dashboard (`observability/grafana/dashboards/engine.json`).

## The decision: the runtime label is the workshop's, not the pod's

No port reports the runtime a sandbox was actually created under. The label
is what the run's resolution chose at the last reconcile or sample
(`Resolution.WorkspaceRuntime`), and the resolver names the provisioner's
default there. A workshop that switches runtime relabels its existing
sandboxes until they are replaced, which is at most one sandbox life of
drift. Reading it from the pod would need a new port method and a probe per
run on every pass. Labels are closed sets: the engine's runtime names, plus
`default` (a provisioner that offers no choice) and `unreconciled` (a run
the reconciler has not probed yet).

## Tests that hold it

`TestSandboxCounts` (engine), `TestMemoryGaugesByRuntime` (resources),
`TestReconcileRecordsLiveStatusAndDirty` (envstate, the runtime map).
