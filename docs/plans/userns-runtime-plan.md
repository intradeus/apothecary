# Plan: the `userns` sandbox runtime (plain runc in a user namespace)

Status: phases 0 to 5 built (2026-11-27), on one branch; phase 6 measured,
and its quota alone built (the owner's choice: no cpuset). What phase 0 found, and each
deviation from this plan, is in the notes of 2026-11-27
(`ls docs/notes | grep 2026-11-27`). It plans the ticket "Adding
runc-userns sandbox runtime", written from a spike on the k3s host
(k3s v1.36.4, containerd 2.3.4, runc, kernel 7.0, cgroup v2, systemd
cgroup driver). Read `AGENTS.md` first; every rule there applies
(conventional commits, no model ids, `task fmt`, `task check`, a note in
`docs/notes/` with each decision or deviation).

Two marks are used below:

- **verify**: not confirmed against a running cluster when this plan was
  written. Settle it in the phase that names it, and record what you found
  in that phase's note.
- **plan's choice**: a decision this plan made that the ticket did not.
  The owner may overrule it.

Each phase is one pull request, in this order. A phase leaves the tree
green and the product working. Phase 0 is a morning on the k3s host and
changes no code.

## Why

A workshop picks between two contained runtimes today. Sysbox shares the
node's kernel but cannot run a user-namespaced pod on a cluster nested in
the sandbox
(`docs/notes/2026-11-26-sysbox-cannot-run-nested-user-namespaced-pods-even-with-a-wider-id-range.md`).
Kata runs everything, in a VM: memory held per sandbox, KVM on the node.
The spike found a third way with nothing to install: plain runc, a
Kubernetes user-namespaced pod with `procMount: Unmasked`, and a containerd
handler with `cgroup_writable = true`.

## The recipe the spike established

| Layer | What it takes |
|---|---|
| containerd | a handler `runc-userns`: `runtime_type = "io.containerd.runc.v2"`, `cgroup_writable = true`, `SystemdCgroup = true` (containerd ≥ 2.1, kernel ≥ 6.3, Kubernetes ≥ 1.36) |
| the pod | `runtimeClassName: runc-userns`, `hostUsers: false` |
| the sandbox container | `procMount: Unmasked`; capabilities `SYS_ADMIN, NET_ADMIN, MKNOD, SYS_RESOURCE, SYS_PTRACE`; seccomp and AppArmor `Unconfined`; `/var/lib/docker` on a volume |
| cgroups | before dockerd, every process moved to `/sys/fs/cgroup/init` and the controllers enabled for children |
| privileged inner containers | a wrapper around runc, docker's default runtime, turns a container's `/sys` mount into a read-only rbind of the pod's `/sys` at `create` |
| a nested kubelet | `--kubelet-arg=feature-gates=KubeletInUserNamespace=true` |
| nested user-namespaced pods | the node's kubelet gives each user-namespaced pod more than 65536 ids (`userNamespaces.idsPerPod`, with a `kubelet:` range in `/etc/subuid` and `/etc/subgid`); the nested kubelet gets the range above the first 65536 (a `kubelet` user, a `getsubids`, `max-pods` to fit); the nested node keeps its containerd state and `/var/lib/kubelet` on tmpfs |

The hazard: a kubelet refuses to start (`wrong user namespace length`)
while any running user-namespaced pod's mapping differs from its
`idsPerPod`, in either direction.

## What the tree says (checked 2026-11-26)

- **The cgroup step is already in the image.** `docker:29-dind`'s
  `/usr/local/bin/dind`, which `dockerd-entrypoint.sh` runs dockerd through,
  moves every process to `/sys/fs/cgroup/init` and writes
  `cgroup.subtree_control`, in a loop, whenever
  `/sys/fs/cgroup/cgroup.controllers` exists. `apo-dockerd` ends in `exec
  dockerd-entrypoint.sh`, so under a writable cgroup this should need no new
  code (phase 0, check 5).
- **The image has no jq.** It has Node, and a Go build stage (wireguard-go).
  The image ships runc 1.5.3 at `/usr/local/bin/runc`.
- **A runtime's capabilities are fixed per name.** `Runtime.Capabilities()`
  reads one table. `Choose`, `Satisfying`, the API's runtime offer
  (`internal/httpapi/confapi.go`, `getRuntimeOffer`) and the live test's gate
  all read it. A capability that depends on the integration needs a second
  input (decision 2).
- **Nothing asks for UserNamespaces today.** `devcontainer` needs
  NestedContainers; a project's Docker adds Contained. The capability is
  shown in the form and gates `liveNestedKubernetes`.
- **The k8s suite starts privileged pods.** Every gateway has a privileged
  `sysctl` init container (`internal/adapters/egressgw/host_k8s.go`), and
  `TestLiveK8sRuntimeProbe` starts a privileged probe. The ticket's table
  shows unprivileged pods on the nested cluster. A privileged pod there is
  created by the nested node's own containerd and runc, which the sandbox's
  `/sys` wrapper does not reach (phase 0, check 2).
- **The rigs have no systemd.** `deploy/sysbox/rig.sh` and
  `deploy/kata/rig.sh` run k3s as a container's main process, so their
  containerd uses the cgroupfs driver. The recipe names the systemd driver
  (phase 0, check 7).
- **The ticket's `TestLiveK8sLifecycle` is `TestLiveSandboxLifecycle`** since
  the suites were split
  (`docs/notes/2026-11-24-sandbox-suites-split-into-the-cluster-and-each-runtime.md`).

## Decisions

1. **Names.** Runtime `userns`, RuntimeClass and handler `runc-userns` (the
   ticket's).
2. **The gated capability lives in the choice, not the table**
   (**plan's choice**). The `envport` row for `userns` is NestedContainers,
   NestedKubernetes, Contained: what it gives on any cluster.
   `RuntimeChoice` gains what a provisioner's cluster adds to a runtime:

   ```go
   // Adds is what this provisioner's cluster gives a runtime beyond its
   // row: userns gets UserNamespaces where the nodes' kubelets hand a
   // user-namespaced pod more than 65536 ids.
   Adds map[Runtime]RuntimeCapabilities

   func (c RuntimeChoice) Capabilities(rt Runtime) RuntimeCapabilities // the row, with Adds
   ```

   `Choose`, the fix a refusal names, and the API's offer read
   `c.Capabilities(rt)`. Rejected: a second runtime name for the wide case
   (two names for one pod shape), and a table changed at boot (the flag is
   per integration, not per deployment).
3. **The flag is the integration's alone.** A provisioner field
   `nested_user_namespaces` (on/off, default off), carried to
   `KubeConfig.NestedUserNamespaces`. No chart value and no
   `APOTHECARY_K8S_*` default, like `vm_storage_class`: it states a fact
   about nodes that an admin checked.
4. **One pod trait.** A `sandboxRuntime` trait (working name
   `usernsNesting`) gives the sandbox container its security context and
   the image its switch (`APO_USERNS_NESTING=1`). `wg` is untouched: it
   already gets `NET_ADMIN` alone when the runtime is not privileged. The
   runtime probe's pod carries the same security context, so a cluster that
   refuses `procMount: Unmasked` fails the integration's Test, not a run.
5. **The wrapper is a static Go binary** (**plan's choice**, within the
   ticket's "small static binary or a script"): `apo-runc`, source in
   `workspace/aporunc/` (stdlib only, like the shim and the gateway), built
   in the sandbox image's existing Go stage. Go because the image has no
   jq, runc is called several times per container, and the logic then sits
   under `go test` and the coverage floor. `workspace/embed.go` embeds
   `shim` and `gateway` only, so the new directory changes no hash.
6. **The wrapper rewrites only a writable sysfs** (**plan's choice**, to
   confirm against the spike's script): a mount whose destination is `/sys`
   and whose options lack `ro`. An unprivileged container keeps its own
   fresh read-only sysfs, with its own network namespace's view of
   `/sys/class/net`. A privileged one gets the pod's view, read-only: a
   limit to state in the note.
7. **The sandbox refuses a wrong cluster by name** (**plan's choice**).
   Under `APO_USERNS_NESTING`, `apo-dockerd` exits with the fix when
   `/proc/self/uid_map` is the identity map (the handler ran a pod without
   `hostUsers: false`) or `/sys/fs/cgroup` is not writable (the handler
   lacks `cgroup_writable`).
8. **CPU visibility is its own last phase**, a spike with a decision at its
   end (phase 6). `APO_CPUS` stays whatever it finds.

## Phase 0: settle on the k3s host

The host has the handler from the spike. Each check is minutes. Their
results shape phases 1, 4 and 5, and go in those phases' notes.

1. **A writable `/sys` instead of the wrapper** (**plan's choice**,
   untested). The kernel refuses a fresh read-write sysfs because the one
   visible in the pod is read-only. A sysfs superblock belongs to the user
   namespace that owns its network namespace, and with `hostUsers: false`
   that is the pod's. So `mount -o remount,rw /sys` in the sandbox
   container, before dockerd, may be allowed. Files in `/sys` stay owned by
   the node's root, so it should grant nothing. Try it, then start a
   privileged container with stock runc.
   - It works: phase 1 is two lines of `apo-dockerd` and no wrapper, a
     deviation from the ticket to record. Check 2 probably passes with it.
   - It is refused: build the wrapper as the ticket says.
2. **A privileged pod on a nested cluster.** In a userns sandbox, with a
   k3d cluster started with the kubelet gate: one pod with a privileged
   container (the gateway's `sysctl` init container is the real case). Try
   it with the wrapper alone, and with check 1's remount.
   - If it fails both ways, the next thing to try is the wrapper as the
     nested containerd's runc (`BinaryName`).
   - If nothing works, `task sandbox:test:k8s` cannot run in a userns
     workspace. Tell the owner before phase 5: it changes what the ticket
     promises.
3. **The tunnel.** The `wg` container with `NET_ADMIN` alone brings up
   `wg0` under the handler, and the sandbox resolves and reaches a host
   through its gateway. There is no wireguard-go fallback in a user
   namespace (no `/dev/net/tun`, and `mknod` is refused), so the nodes need
   the `wireguard` module, as under sysbox.
4. **The claim.** The spike kept `/var/lib/docker` on an emptyDir. With
   `storage: pvc` (local-path): docker's overlay2 on the claim's `docker`
   subPath, then a sleep and a wake.
5. **The cgroup step.** With no change to the image, dind's own step
   succeeds under the handler, and a `kubectl exec` still works after it.
6. **A kept claim across runtimes.** A run under sysbox, the workshop moved
   to userns, a rebuild (`shapeChange` replaces the pod on the same claim):
   are the checkout and docker's layers still owned and usable? If not,
   either `shapeChange` keeps the pod with a note, as it does between Kata
   and the others, or the docs say the change is for the next run.
7. **A rig.** k3s under systemd in one privileged container (the sysbox
   rig's image with systemd as its first process), the handler in its
   containerd template: is `/sys/fs/cgroup` in a pod writable and owned by
   the pod's root? This decides whether phase 4 has a rig and a CI suite.

## Phase 1: the sandbox image

Inert until a pod sets the switch: no other runtime's sandbox changes.

- `workspace/aporunc/` (skip if check 1 passed): `main` calls one function
  that takes argv and an exec seam.
  - It finds runc's subcommand past the global flags (`--root`, `--log`,
    `--log-format`, `--systemd-cgroup`, …) and the bundle (`--bundle x`,
    `--bundle=x`, `-b x`, else the working directory).
  - On `create` and `run` it rewrites `config.json` per decision 6. Every
    other field keeps its bytes (`json.RawMessage`); the write is a
    temporary file renamed over the original, same mode.
  - It then execs `/usr/local/bin/runc` with the arguments it was given.
    Any other subcommand, or a config it cannot read, goes to runc
    untouched.
- `deploy/images/sandbox/Dockerfile`: the binary built in a Go stage and
  copied in. `deploy/images.sh` passes the source as a named build context
  (`--build-context`), since the image's context is its own directory.
  Add the new directory to the path filter of CI's `sandbox-images` job.
- `deploy/images/sandbox/apo-dockerd`, under `APO_USERNS_NESTING=1`:
  decision 7's two refusals, then `--add-runtime` and `--default-runtime`
  for the wrapper (or the remount). No cgroup code unless check 5 failed.
- Tests: the wrapper's unit tests on sample `config.json` files (a
  privileged container's spec, an unprivileged one, one without `/sys`):
  only the writable `/sys` rewritten, every other mount and field
  untouched, a non-`create` call passed through with its arguments, each
  bundle flag form. Keep `main` to one line: the Go floor is 95%.
- Note: the wrapper (or its absence), decisions 5 to 7.

## Phase 2: the runtime

- `internal/ports/envport/runtime.go`: `RuntimeUserns`, its row, and
  decision 2. Tests: the row; `Missing` and the refusal's fix with and
  without `Adds` (a workshop that needs UserNamespaces is told `kata` when
  the flag is off, and may keep `userns` when it is on); `ParseRuntime`'s
  list.
- `internal/adapters/kube/manifests.go`: `SecurityContext` gains
  `procMount` and `appArmorProfile`.
- `internal/adapters/workspace/runtime.go`: the row (class `runc-userns`,
  `ownUserNamespace`, not privileged, the trait) with a comment that says
  why each part of the security context is there.
- `host_k8s.go`, `podManifest`: the sandbox container's security context
  and the switch from the trait. `runtime_probe.go`: the same security
  context on the probe.
- `KubeConfig.NestedUserNamespaces`. The adapter states which runtimes it
  widens (one function the kind calls to fill `Adds`), so the trait and the
  capability cannot drift apart.
- **plan's choice**, small: at provision, when the flag is on and the
  sandbox's `/proc/self/uid_map` shows 65536 ids, the workspace log says
  the node does not give what the provisioner claims. On a cluster where
  only some nodes have the kubelet setting, this is the only sign.
- `internal/project/kinds/kubernetes.go`: the field
  `nested_user_namespaces` with the hazard in its help (on only when every
  node that runs userns sandboxes has the kubelet setting; never change
  that setting while a user-namespaced pod runs on the node); `userns` in
  the kind's help, the runtime field's help and the runtime class help
  (`userns: runc-userns`); `kubernetesChoice` fills `Adds`.
- `internal/httpapi/confapi.go`: the offer reads `c.Capabilities(rt)`. The
  shape does not change, so no `task api`.
- CPUI: `form.project.rtAboutUserns` in `SandboxRuntimeField.svelte`'s
  `about` map, with its test. Draft: "Pick this if you need docker,
  compose, or a Kubernetes cluster inside the sandbox on a shared kernel,
  and user-namespaced pods on that cluster where this cluster allows them.
  It runs without sysbox's syscall filtering: for your own code, not code
  you do not trust."
- Tests:
  - the golden `testdata/pods/userns-pvc-gateway.json`, and the existing
    goldens byte for byte;
  - the security context on the sandbox container only (not `wg`, `relay`,
    the seeds);
  - `hostUsers: false` with the default class and with a class override
    (`runtime_class: userns=<other>`);
  - a table invariant: a row with the trait has `ownUserNamespace` and is
    not privileged;
  - the kind: the flag's values, `Adds` with it on and off;
  - the provision warning.
- Note: no new runtime binary; the gated capability and why it is the
  choice's; the security trade-off (below). It also corrects
  `2026-11-26-reverting-the-kubelet-ids-per-pod-setting-needs-the-wide-pods-gone-first.md`:
  widening is not safe either.

The security trade-off, for the note, the UI line and the docs: isolation
rests on the user namespace, with the capabilities namespaced. Seccomp and
AppArmor are off for the sandbox container, so the whole kernel API is
reachable from it. A kernel bug is a wider door than under sysbox's
filtering, and much wider than Kata's VM. Fine for the operator's own
projects; not the runtime for untrusted code.

## Phase 3: the cluster side

- Chart: `workspaces.usernsRuntimeClass` (`create` false, `name` and
  `handler` `runc-userns`, `nodeSelector`, `tolerations`; no overhead),
  rendered like Kata's (`templates/runtimeclass-kata.yaml`), with the same
  refusal when `userns` is the default runtime and
  `workspaces.runtimeClassName` names another class. `userns` joins the
  schema's `runtime` and `runtimes` enums and the comments in
  `values.yaml`. A worked example `ci/userns-values.yaml`. `task
  chart:lint` gains its refusals; `task chart:docs`.
- `README.md`, cluster requirements: the versions, the handler's TOML, and
  that the handler must never run a pod without `hostUsers: false` (a
  host-root pod could rewrite its own limits; the engine only builds it
  with `hostUsers: false`, and the sandbox refuses to start otherwise).
- `docs/kubernetes.md`:
  - a `userns` row in the runtimes table, and "All three run docker
    inside" becomes four;
  - installing it: the handler (k3s: `config-v3.toml.tmpl`), the
    RuntimeClass, the namespace's Pod Security level (`privileged`, the
    chart's default: `Unmasked` and `Unconfined` are refused below it), the
    `wireguard` module (check 3);
  - a project's own nested cluster: the kubelet gate, with the k3d and the
    kind spelling;
  - nested user-namespaced pods: the kubelet drop-in, the `subuid` and
    `subgid` range, the provisioner's flag, what the nested node needs
    (range, `max-pods`, tmpfs, so its images are held in memory and count
    against the sandbox's limit);
  - the hazard and the safe order for any change of `idsPerPod`, up or
    down: find every user-namespaced pod on the node (sandboxes under
    sysbox and userns, and assistant pods), stop or sleep them, change the
    drop-in and the ranges, restart the kubelet. If the kubelet is already
    crash-looping, put the old value back first;
  - EKS: the handler and the kubelet setting in the node group's bootstrap,
    no special instance type. Documented and untried, as Kata on EKS is;
    **verify** the EKS AMI's containerd is 2.1 or later before writing a
    version.
- Note: the chart's class (as the Kata one has).

## Phase 4: the live suite

- If check 7 passed: `deploy/userns/rig.sh` (`up`, `import`, `down`, like
  the sysbox rig), on `kata.env`'s `K3S_IMAGE`. The node gets the handler,
  the RuntimeClass, and the kubelet's `idsPerPod` with its ranges, all
  before its first pod, so the hazard cannot arise. Its smoke pod is shaped
  as the provisioner's and checks a writable `/sys/fs/cgroup` and the id
  count.
- `task sandbox:test:userns`: `TestLiveSandboxLifecycle$` and
  `TestLiveK8sRuntimeProbe` under `APOTHECARY_LIVE_RUNTIME=userns`.
  `live_runtime_test.go` gains the `userns` case.
- `TestLiveSandboxLifecycle` under userns also checks what the runtime is
  chosen for, as one helper: a privileged container starts; a container
  over its memory limit is killed (137) and the sandbox lives; a compose
  project comes up; a Kubernetes node with the kubelet gate is Ready and
  serves a Service by name.
- Split `liveNestedKubernetes` in two: the node and an ordinary pod (any
  runtime with NestedKubernetes), then the user-namespaced pod (a runtime
  whose capabilities, read through the host with the flag, include
  UserNamespaces). Kata and privileged run both as today.
- CI: a `userns` entry in the `sandbox-test` matrix, with the sysbox
  suite's prerequisites (kubectl pin, the `wireguard` module), strict.
- If check 7 failed: no rig and no CI suite. The suite runs by hand against
  the k3s host's kubeconfig, and the note says why (the ticket allows
  this). k3s installed on the runner itself is the alternative to weigh
  there: the runner is a throwaway VM with systemd.
- Note: the rig, or why there is none.

## Phase 5: nested Kubernetes for this repository

- `Taskfile.yml`, `k8s:cluster`: the kubelet gate on every cluster it
  creates. **verify** that CI's k3d suites and a laptop are unchanged by
  it. Inside a user namespace with more than 65536 ids (read from
  `/proc/self/uid_map`), the node also gets its range and tmpfs
  directories.
- The node's additions are one definition, shared by `k8s:cluster` and the
  live test (**plan's choice**; start from the spike's script): a few
  lines over `K3S_IMAGE` adding the `kubelet` user, a `getsubids` that
  answers from `/proc/self/uid_map`, and the tmpfs mounts, built where it
  is used. k3d runs every `/bin/k3d-entrypoint-*.sh` before k3s, which is
  where the mounts can go. The live test reads the file and sends it on
  stdin, since the sandbox has no checkout of this repository.
- `liveNestedKubernetes`'s second half under userns with the flag
  (`APOTHECARY_LIVE_NESTED_USERNS=1` sets
  `KubeConfig.NestedUserNamespaces`): the range and the tmpfs directories.
  `task sandbox:test:userns` sets it and `APOTHECARY_LIVE_NESTED_K8S` on a
  rig whose kubelet has the setting.
- The proof: `task sandbox:test:k8s` in a userns workspace on the k3s
  host, the provisioner's flag on. `TestLiveK8sAssistantPod` runs instead
  of skipping. Record the time, and the sandbox's memory at its peak: the
  nested node's images (the tools bundle is about 2.3 GB) are in memory.
- `AGENTS.md`: the paragraph on the sysbox skip gains the userns case.
  `README.md`, "Developing Apothecary with Apothecary": the memory figure
  if the 16 GB there no longer holds.
- Note: what the nested node needs and what it costs in memory; check 2's
  outcome.

## Phase 6: CPU visibility

The ticket's last item: tools in the workspace should see the sandbox's
CPUs, ideally without `APO_CPUS`.

What is possible:

- `nproc`, Node's `os.availableParallelism()` and Go's `NumCPU` read the
  process's CPU affinity. Only a cpuset changes it. A CPU limit is a quota,
  which they do not read.
- Runtimes that do read the quota (recent Go, the JVM) read their own
  cgroup's `cpu.max`. An inner container has a cgroup namespace of its own
  and sees no limit there: the pod's limit is on an ancestor.
- `/proc/cpuinfo` and Node's `os.cpus()` list every core whatever is done
  short of lxcfs, which is a node component
  (`docs/notes/2026-11-24-workspaces-get-size-hints-and-project-variables.md`).
  A tool that counts those still needs `APO_CPUS`.

So the candidate is a cpuset and a quota on the inner containers, set by
the sandbox itself. Under userns the sandbox owns its cgroup tree, so
`apo-dockerd` can make the parent cgroup of dockerd's containers
(`--cgroup-parent`), give it a `cpuset.cpus` of as many CPUs as the limit
rounded up, and a `cpu.max`. Every inner container (the workspace, compose
services, a k3d node and its kubelet's capacity) then reports that many.

The cost is pinning. A cpuset names specific cores, and sandboxes on one
node choose without knowing of each other. Two busy sandboxes on the same
cores wait while other cores idle; the quota alone never does that. The
kubelet's static CPU manager would coordinate, but it needs Guaranteed pods
and exclusive cores, which ends the CPU sharing that sizing rests on
(`docs/kubernetes.md`, Sizing).

The spike, on the k3s host:

1. The cpuset and quota on the parent cgroup under userns: `nproc`,
   `os.availableParallelism()`, a Go program and `jest --showConfig` in the
   workspace; a k3d node's capacity.
2. How to choose cores (a window at an offset drawn per pod, or the idlest
   at start) and what two busy sandboxes on overlapping cores lose,
   measured.
3. Whether sysbox and privileged sandboxes take the same step (their
   cgroup is writable too). If so it is one feature for every runtime with
   a nested daemon, not a userns trait.

Then the owner decides: build it (on by default, or a workshop setting),
or keep `APO_CPUS` alone and say why in a note. Either way the hints stay:
they are the only answer for a tool that counts `/proc/cpuinfo`.

**Measured (2026-11-27, step 1, on the userns rig).** A userns sandbox pod
limited to 2 CPUs on a 20-CPU node; Go 1.26 and Node 22 in its inner
containers:

| Inner container | `nproc` | Go `NumCPU` / `GOMAXPROCS` | Node `availableParallelism()` / `cpus().length` | its own `cpu.max` |
|---|---|---|---|---|
| as docker starts it | 20 | 20 / 20 | 20 / 20 | `max` |
| `--cpus 2` (a quota) | 20 | 20 / **2** | **2** / 20 | `200000 100000` |
| `--cpuset-cpus 0-1 --cpus 2` | **2** | **2** / **2** | **2** / 20 | `200000 100000` |

What that changes: a quota alone, with no cpuset and so no pinning, already
gives Go's `GOMAXPROCS` and Node's (libuv's) `availableParallelism()` the
sandbox's size, because both read the container's own `cpu.max`. Only
`nproc` and Go's `NumCPU` need the cpuset. The sandbox's own limit is on an
ancestor, outside an inner container's cgroup namespace: the container sees
`max`. So there is a cheaper candidate than the cpuset: `apo-runc`, which
already rewrites each container's spec at `create`, gives a container that
has no CPU quota the sandbox's own (`/sys/fs/cgroup/cpu.max` at the
sandbox's root), under userns alone. It costs nothing in pinning. It leaves
`nproc`, `/proc/cpuinfo` and `os.cpus()` at the node's count, and so the
`APO_CPUS` hints. Steps 2 and 3 were not run: they matter only if the owner
wants `nproc` too, at the cost of the cpuset.

**Decided (2026-11-27): the quota alone,** in `apo-runc`, no cpuset
(`docs/notes/2026-11-27-apo-runc-gives-an-inner-container-the-sandboxs-cpu-quota.md`).

## Not in this plan

- `userns` as the default runtime, or retiring sysbox.
- containerd's `slow_chown` for nested layers on disk instead of tmpfs.
  Revisit if phase 5's memory figure hurts.
- The `plain` runtime (`docs/plain-runtime-and-startup-ticket.md`).
- `deploy/k3s/install.sh --userns`. The rig's handler template and kubelet
  drop-in are written so the installer can take them later, as it does
  Kata's.
- An admission policy that refuses a pod naming `runc-userns` without
  `hostUsers: false`. The README states the rule; the engine and the
  sandbox hold their side of it.

## Pointers

- `internal/ports/envport/runtime.go`, `internal/adapters/workspace/runtime.go`,
  `host_k8s.go` (`podManifest`), `runtime_probe.go`, `k8s_shape.go`.
- `internal/project/kinds/kubernetes.go`, `internal/httpapi/confapi.go`
  (`getRuntimeOffer`), `cpui/src/lib/workshop/form/SandboxRuntimeField.svelte`.
- `deploy/images/sandbox/`, `deploy/images.sh`, `deploy/sysbox/` (the rig to
  model), `deploy/kata/kata.env` (`K3S_IMAGE`).
- `internal/adapters/workspace/live_k8s_test.go` (`liveNestedKubernetes`),
  `live_runtime_test.go`, `internal/adapters/assistant/live_k8s_test.go`.
- `charts/apothecary/templates/runtimeclass-kata.yaml` and
  `docs/notes/2026-11-23-the-chart-can-make-the-kata-runtimeclass.md`.
- `docs/notes/2026-11-22-the-sandbox-runtime-is-one-named-value.md` and
  `2026-11-22-a-workshop-picks-its-sandbox-runtime-from-the-provisioners-approved-list.md`:
  how a runtime is added and chosen.
- The two 2026-11-26 notes on sysbox's limit and the kubelet's `idsPerPod`.
