# apo-runc gives an inner container the sandbox's CPU quota

Under the userns runtime, `apo-runc` (`workspace/aporunc`) makes a second
change to a container's spec at `create` and `run`: a container with no
CPU quota gets the sandbox's own, in `linux.resources.cpu`. It is phase 6
of `docs/plans/userns-runtime-plan.md`, built as its cheaper candidate:
the quota alone, with no cpuset.

## Why

In a userns sandbox limited to 2 CPUs on a 20-CPU node, an inner container
read `max` in its own `cpu.max`: the sandbox's limit sits on an ancestor,
outside the container's cgroup namespace. Go's `GOMAXPROCS` and Node's
`os.availableParallelism()` then counted 20. With `--cpus 2` alone, both
counted 2. With a cpuset as well, `nproc` and Go's `NumCPU` did too. The
measurements are in the plan's phase 6.

The owner chose the quota alone. A cpuset names specific cores, and
sandboxes on one node would choose them without knowing of each other: two
busy sandboxes on the same cores wait while other cores idle. The quota has
no such cost. It changes nothing the kernel enforces, since the sandbox's
cgroup holds the container to the same limit already. It only lets a
runtime that reads its own `cpu.max` see it.

## What now

- The limit is the tightest `cpu.max` on the wrapper's own cgroup and its
  ancestors, up to the root of its cgroup namespace (`sandboxCPU`). The
  wrapper runs in dockerd's cgroup (the dind script's `/init`, no limit),
  so that is the sandbox's, on the namespace's root, where the kubelet put
  the pod's CPU limit. No limit found, or no cgroup v2 tree, changes
  nothing.
- A container whose spec has a positive quota keeps it, even one wider than
  the sandbox's (`docker run --cpus 8` in a 2-CPU sandbox still reads 8: the
  kernel holds it to 2). Zero, -1 or none counts as none, and its period is
  replaced with the sandbox's.
- A k3s node nested in the sandbox gets the quota as any container does,
  and its pods get the node's, since `apo-runc` is that node's runc too
  (`2026-11-27-a-nested-k3s-node-in-a-userns-sandbox-is-one-definition.md`).
  A pod with a CPU limit of its own keeps its limit.
- Unchanged: `nproc`, `/proc/cpuinfo` and Node's `os.cpus()` count the
  node's CPUs, and the `APO_CPUS` hints stay
  (`2026-11-24-workspaces-get-size-hints-and-project-variables.md`).
- Userns only: `apo-runc` is docker's runtime only under
  `APO_USERNS_NESTING`. Whether sysbox and privileged sandboxes should take
  the same step (the plan's step 3) was not looked at.

Tests: `workspace/aporunc/runc_test.go` (`TestRewriteCPU`: the quota added
beside the other resources, a quota of its own kept, unreadable specs
refused; `TestSandboxCPU`: the walk up the cgroup tree; `TestRun`: the
quota written on create). The live suite's `liveUsernsNesting`, on the
userns rig, with the userns sandbox now sized at 2 CPUs (it had no limit,
so there was nothing to hand down): the sandbox's `cpu.max` is `200000
100000`, an inner container's is the same, and one started with `--cpus 1`
keeps `100000 100000`.
