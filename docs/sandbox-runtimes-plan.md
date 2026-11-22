# Plan: sandbox runtimes, sizes, Kata and stop-on-idle

Status: built (2026-11-22), phases 0 to 7, with the deviations recorded in
the 2026-11-22 notes in `docs/notes/`. Phase 5's install step is
`deploy/k3s/install.sh --kata`. Its tests are CI's strict `kata` suite, with
a nested cluster checked inside the Kata sandbox in place of the whole
`sandbox:test:k8s`. EKS is documented and untried. A spike ran on a
bare-metal box (appendix A); its findings are what the phases rest on.
Owner: the agent implementing it. Read `AGENTS.md` first; every rule there
applies (conventional commits, no model ids, `task fmt`, `task check`, new
numbered migrations only, a note in `docs/notes/` with each deviation).

Items marked **verify** were not confirmed against the code or a running
cluster when this plan was written. Settle each in the phase that names it,
and record what you found in that phase's note.

Each phase is one pull request, in this order. A phase leaves the tree
green and the product working; none depends on a later one.

## Why

- A run's sandbox on Kubernetes is a pod that nests a docker daemon, under
  sysbox (production) or privileged (CI, a dev box). Under sysbox the
  sandbox owns 65536 user ids, so anything inside it that needs a user
  namespace of its own above that range cannot run: a stock k3d cluster
  with a `hostUsers: false` pod, which is Apothecary's own assistant pod
  (`docs/notes/2026-11-20-live-suites-skip-loudly-and-never-in-ci.md`). No
  id setting fixes it for a stock kubelet: it picks a pod's range anywhere
  in the 32-bit space (`pkg/kubelet/kubelet_pods.go`, `getKubeletMappings`).
- The goal is that any project that runs on a developer's Linux laptop
  runs in a sandbox (desktop applications aside). That needs a sandbox
  with a kernel of its own: a micro-VM per sandbox (Kata Containers).
- A VM holds the memory it touches. So sandboxes need sizes, and idle ones
  need to stop.
- Not every project needs a VM. The runtime is a per-project choice among
  the ones an admin allows.

## The model

Three independent layers, plus a size. Only the runtime is new.

| Layer | Question | Options | Status |
|---|---|---|---|
| Host | who provisions | Kubernetes | `Host` in `internal/adapters/workspace/host.go` |
| Runtime | what isolates the sandbox, what can run in it | `sysbox`, `kata`, `privileged`; later `plain` | implicit today (`Privileged` + `RuntimeClass`) |
| Definition | how the workspace is set up inside | `devcontainer`; later `startup` | `Definition` in `definition.go` |
| Size | CPU and memory | raw numbers | new |

Runtimes apply to run sandboxes only. The engine, egress gateways,
assistant pods and browser pods stay on the cluster's default runtime.

What each runtime can do (`plain` is reserved, not built here):

| Capability | plain | sysbox | kata | privileged |
|---|---|---|---|---|
| nested containers (a docker daemon, compose) | no | yes | yes | yes |
| nested Kubernetes (k3d, kind) | no | yes | yes | yes |
| user-namespaced pods inside it | no | no | yes | yes |
| sandbox root is contained from the node | yes | yes | yes | no |
| node requirement | none | sysbox | KVM | none |
| memory | shared with the node | shared | held per VM | shared |

`privileged` is for CI and a single-tenant dev cluster: never offered to
projects unless an admin allows it. A definition declares what it needs
(`devcontainer`: nested containers); the engine refuses a project whose
runtime cannot give it, at configuration time, naming the fix.

Decisions already taken (the owner's):

1. A project picks its runtime from a list the admin approves.
2. Sizes are raw numbers (CPUs, memory), with an admin default and ceiling.
3. The names are `privileged`, `sysbox`, `kata` (and `plain`, reserved).
4. WireGuard stays the sandbox-to-gateway tunnel. No custom guest kernel:
   the sandbox falls back to userspace WireGuard where the kernel has none.
5. Stop-on-idle is optional, per project: an idle time in minutes, `-1`
   never.
6. `devcontainer` stays the only definition. `startup` (a docker-less
   `.apothecary/startup.json`) and `plain` are designed for, not built.

## Phase 0: land the live-suite fix

The branch `fix/k8s-namespace-issue-for-assistant-pod` holds it, committed:
the assistant live test's user-namespace probe, `livetest.Skip` and
`APOTHECARY_LIVE_STRICT`, `kube.Client.LastWarning`, `pin:kubectl`. Its
note and the `AGENTS.md` paragraph say "under sysbox".

- Pull request, CI green, merge. CI has not run it yet: strict mode is new
  there.

Known and unrelated: `TestSSHExecDeliversAllOutputToASlowClient`
(`workspace/shim`) fails under the full suite's load and passes alone.
Note it in the pull request; do not fix it here.

## Phase 1: the runtime seam (no behaviour change)

Make the runtime one named value the pod builder consults.

- In `internal/adapters/workspace`: a `runtime` struct of traits, unexported
  like `Host`. Two values now, `privileged` and `sysbox`:
  - runtime class (default name; the integration may override it);
  - privileged security context for the sandbox and the `wg` container;
  - `hostUsers: false`;
  - `contained`: sandbox root is not node root. This replaces
    `unprivileged()` and decides docker access under
    `APOTHECARY_WORKSPACE_DOCKER=auto`;
  - capability flags: nested containers, nested Kubernetes, user
    namespaces inside. Data only in this phase; phase 3 reads them;
  - fields phase 5 fills: block volumes and no `subPath`, an entrypoint
    prelude, limits required, pod annotations.
- `podManifest` and `devcontainer.go:549` read traits; no `if` on a
  runtime's name outside the table.
- `KubeConfig` takes `Runtime string`. The Kubernetes integration
  (`internal/project/kinds/kubernetes.go`) gains a `runtime` field. The
  old `privileged` and `runtime_class` fields keep working: `privileged=on`
  is `privileged`; `off` with a class is `sysbox`. `runtime_class` stays
  as the class-name override.
- `Definition` gains what it needs from a runtime (`devcontainer`: nested
  containers). Reword the interface comment: "inside a sandbox's docker
  daemon" is devcontainer's detail, not the contract. No method changes.
- Chart: `workspaces.runtime`, mapped from `privileged`/`runtimeClassName`
  when unset. `task chart:docs`.

Done when: the pod manifests for both runtimes are byte-identical to
today's (a golden test over `podManifest`), `task check` passes, and
`task sandbox:test:k8s` passes where it did before.

## Phase 2: sandbox sizes

- `envport.EnvSpec` gains a size: CPUs and memory.
- Project setting: beside `Docker` in `confstore` (the workspace
  settings). Empty means the admin's default.
- Admin: a default and a ceiling for each, with the run caps
  (`engine.Limits`, System → Limits). A project's value above the ceiling
  is refused when saved.
- The pod: memory request = limit; CPU limit = the size, CPU request low
  (a fraction, a constant to choose and note) so CPU overcommits and memory
  does not. All runtimes.
- A size change applies to the next provision or rebuild, never to a live
  sandbox. Say so in the setting's help.
- Capacity: a pod that does not fit stays `Pending`. Surface the
  scheduler's reason (`LastWarning`) in the run's provisioning log instead
  of a bare timeout.
- Out of memory: when a sandbox dies at its limit, the run says so and
  names the setting. **Verify** how an inner OOM shows under sysbox today
  (the sandbox container's `memory.events`), and reuse `sample`.
- UI: the two fields, and the project's peak memory over recent runs
  beside them (from the samples the engine already takes). Follow
  `docs/frontend-standards.md`. `task api` after route changes.
- A new numbered migration if any of this is a column, never an edit.

Done when: a project's size reaches the pod, the ceiling refuses, a
pending pod's reason reaches the log, and the tests hold each.

## Phase 3: a project picks its runtime

- Admin: the list of approved runtimes (System → Limits or the Kubernetes
  integration's deployment defaults; choose one and note why). Default:
  what the deployment runs today, alone.
- Project: the integration's `runtime` field offers only approved ones.
- The requirement check: the project's definition and settings against the
  runtime's capability flags, when the project is saved and again at
  provision (`envcheck`). The refusal names the runtime that would do.
- `test()` on the integration starts a throwaway pod under the chosen
  runtime, so a cluster without it fails at the Test button.

Done when: an unapproved runtime cannot be saved, a mismatch is refused
with its fix, and Test fails cleanly on a missing runtime class.

## Phase 4: WireGuard without the kernel module

Kata's stock guest kernel (6.18) has no WireGuard and loads no modules; it
has TUN. Userspace WireGuard over TUN reached a kernel-WireGuard gateway
at 2.0 Gbit/s up, 3.2 down (appendix A).

- The sandbox image gains a static `wireguard-go`, pinned by version and
  checksum like the image's other tools.
- `apo-wg-up`: try `ip link add wg0 type wireguard`; when the kernel
  refuses, start `wireguard-go wg0` and go on with the same `wg` and `ip`
  calls. Log which one runs. The watchdog must also notice a dead
  `wireguard-go` and restart it.
- `/dev/net/tun` must exist in the `wg` container: **verify** under each
  runtime; create it when missing and allowed.
- The gateway is unchanged: it runs on the node's kernel.
- Test: force the fallback with an environment switch, and run the
  workspace live test with it on (`TestLiveK8sLifecycle`), so CI covers
  userspace on a kernel that has the module.

Done when: the workspace live test passes on both paths.

## Phase 5: the Kata runtime

The third row of the table. Everything here was done by hand in the spike;
appendix A has the recipe.

Engine:

- Traits for `kata`: class `kata`, privileged security contexts (the VM is
  the boundary), no `hostUsers`, `contained`, all capability flags, limits
  required (they size the VM: a pod without them gets Kata's 2 GB default).
- Volumes. Kata does not support `volumeMount.subPath`, and docker cannot
  keep layers on Kata's shared filesystem (overlayfs on virtio-fs fails).
  Today one `data` volume is split by `subPath` into `run` and `docker`
  (`host_k8s.go:472`, `:491`). Under Kata each needs a volume of its own on
  a block device.
  - `emptydir` storage: Kata's `emptydir_mode = "block-plain"` gives each
    `emptyDir` an ext4 disk. Tested.
  - `pvc` storage: **verify**. A volume from a block-backed class (EBS)
    should reach the guest as a disk; a directory-backed class (k3s
    `local-path`) arrives over virtio-fs and docker fails on it. Decide
    and note: require a block-backed class, or a raw-block claim the
    sandbox formats, and what the fresh-box install provides.
  - Block `emptyDir`s ignored size limits in the spike (each showed the
    host's whole disk). **Verify** and bound them.
- Entrypoint prelude: Kata gives a privileged container a minimal `/dev`.
  Nested privileged containers inherit it and k3s dies on a missing
  `/dev/kmsg`. Before dockerd, mount `devtmpfs` aside and copy in the
  nodes `/dev` lacks (appendix A). Put it in the sandbox image, run only
  under this runtime.
- Inner docker's MTU must not exceed the pod interface's. **Verify** how
  the sandbox sets it today, and that it holds under Kata.
- The engine reads and writes the run folder through `kubectl exec`
  (`k8sfs.go`): unchanged. The checkout must sit on the block volume, not
  virtio-fs (4 to 7 times slower for small files).

Cluster (not the engine's job; the engine only names the class):

- Kata 4.4 or later, Cloud Hypervisor. QEMU did not work in the spike's
  rig (the guest never answered over vsock): not investigated, so only
  Cloud Hypervisor is supported until someone does.
- Kata's configuration: `emptydir_mode = "block-plain"`,
  `reclaim_guest_freed_memory = true`, the mem-agent on
  (`mem_agent_enable = true`; choose its periods and note them: 30 s
  reclaimed 5.1 GB of idle cache to 3.2 GB in six minutes, the default is
  600 s), `static_sandbox_resource_mgmt = true`. Memory hotplug stays off:
  it saved nothing and showed the guest 2 GB more than its pod's limit.
- containerd: the runtime entry with `privileged_without_host_devices =
  true`. Do not allow `io.katacontainers.*` pod annotations unless a
  trait needs one.
- The RuntimeClass, with `overhead.podFixed` (the VM's own memory counts
  against the pod's limit: the spike's pod sat exactly at it) and the
  scheduling that places sandboxes on KVM nodes. In the chart.
- `deploy/k3s/install.sh`: a Kata step (`kata-deploy`, **verify** on k3s),
  a preflight for `/dev/kvm`, and a smoke pod shaped like a sandbox.
- EKS, documented in `docs/kubernetes.md`: C8i, M8i and R8i instances with
  nested virtualization on (launch template, or Karpenter's
  `cpuOptions.nestedVirtualization`); no Graviton. **Verify** on a real
  cluster that a Kata pod reaches ClusterIP services (an AWS sample
  reports it does not); sandboxes reach the engine and their gateway.

Tests:

- A live suite on a Kata cluster. The spike's rig needs no root: a k3s
  node in one privileged container with `/dev/kvm`, Kata unpacked beside
  it (appendix A). Make it a task (`sandbox:test:kata`), skipped through
  `livetest.Skip` where there is no `/dev/kvm`.
- CI: **verify** that the runner image exposes `/dev/kvm`; if it does, a
  third `sandbox-test` suite, strict.
- Inside a Kata sandbox, `task sandbox:test:k8s` passes, the assistant pod
  included. Reword phase 0's note and `AGENTS.md` accordingly.

Done when: the engine provisions a run under `kata` end to end on the rig
(provision, exec, files, tunnel, exposure, destroy), with `emptydir` and
with the chosen `pvc` answer.

## Phase 6: stop-on-idle

A parked run needs its disk, not its memory. A Kata pod started in 4 to 7
seconds in the spike.

- Setting: idle minutes per project, `-1` never; an admin default. Any
  runtime (under sysbox it frees less, and still frees CPU and pods).
- Idle is all of: no agent invocation in flight, no stage running, no
  terminal, SSH, editor or browser session open, no traffic on an exposed
  port, for the whole period. **Verify** which of these the engine can
  observe today; one it cannot observe keeps the sandbox up.
- Stop deletes the pod and keeps the claim, the run's secrets (its
  WireGuard identity is reused across re-provisions already) and the
  gateway peer. It requires `pvc` storage: with `emptydir` the setting is
  refused, saying why.
- Resume is the existing re-provision path (`Reattach`, `Provision` on an
  existing claim): on a stage start, a human opening a terminal, the
  editor or an exposed port. **Verify** the docker data on the claim
  brings the workspace container back without a rebuild.
- What is lost: processes. Project services come back through the
  definition's start hooks and the project's `services`; say so in the
  setting's help.
- State: the run shows "sleeping", and the first request after it waits
  with a visible "waking" state, never a timeout.
- Journal: stop and wake are events. They must not change the run-facts
  fold (`docs/notes/2026-11-02-observability-and-run-analytics.md`: time
  advances only on fact-bearing events).
- Limits: decide and note whether a sleeping sandbox counts toward
  `MaxEnvironments` (proposed: yes, it holds storage; it frees a
  concurrent-run slot only as today).

Done when: an idle run stops after its period, wakes on each trigger with
its checkout and containers, and `-1` never stops.

## Phase 7: documentation and operations

- `README.md` cluster requirements, `docs/kubernetes.md`,
  `docs/operations.md`: the runtimes, their node requirements, sizing
  guidance (plan capacity by memory limits under Kata), the settings.
- Telemetry: sandbox counts by runtime and state, memory held against
  limits. No stored value in a label.
- The dashboards under `observability/grafana` get the panels.

## Not in this plan

- `startup` and the `plain` runtime: a docker-less definition in an
  unprivileged pod, the cheap path for most web projects. The seam is
  ready after phase 1; the table above has its row.
- Kata peer pods (a cloud instance per sandbox): a fourth runtime entry,
  installed at cluster level; volumes are the open question.
- A desktop display as a new surface; Windows and macOS targets.
- Retiring sysbox once `plain` and `kata` cover its cases.
- A guest kernel with WireGuard built in (one config line, a four-minute
  build): only if userspace throughput becomes a problem.
- Collecting pod logs into the telemetry backend.

## Appendix A: the spike (2026-11-20 and 21)

Host: bare metal, 22 cores, 61 GB, Ubuntu 26.04, kernel 7.0, `/dev/kvm`.
Kata 4.4.0 static release, Cloud Hypervisor, guest kernel 6.18.62.
Docker access only, no root: everything ran in one container.

The rig:

    docker run -d --name kata-node --privileged --tmpfs /run --tmpfs /var/run \
      -v kata-k3s:/var/lib/rancher/k3s \
      -v $PWD/root/opt/kata:/opt/kata:ro \
      -v $PWD/config-v3.toml.tmpl:/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.tmpl:ro \
      rancher/k3s:v1.36.4-k3s1 server --disable traefik --disable metrics-server --disable servicelb

`root/opt/kata` is the unpacked `kata-static-4.4.0-amd64.tar.zst`. The
containerd template is k3s's base plus:

    [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.kata]
      runtime_type = "io.containerd.kata.v2"
      runtime_path = "/opt/kata/runtime-rs/bin/containerd-shim-kata-v2"
      privileged_without_host_devices = true
      [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.kata.options]
        ConfigPath = "/opt/kata/share/defaults/kata-containers/runtime-rs/configuration-clh-runtime-rs.toml"

with a copy of that configuration edited as phase 5 lists. The `/dev`
prelude, run before dockerd in the sandbox container:

    mkdir -p /tmp/devt && mount -t devtmpfs devtmpfs /tmp/devt
    for n in /tmp/devt/*; do b=$(basename $n); [ -e /dev/$b ] || cp -a $n /dev/$b 2>/dev/null; done
    mkdir -p /dev/net; [ -e /dev/net/tun ] || mknod /dev/net/tun c 10 200
    umount /tmp/devt

What ran, in a `docker:29-dind` pod under Kata (privileged, 12 CPUs,
24 GB, block `emptyDir`s, inner docker at MTU 1400 under a 1450 pod):

- the pod Running in 4 to 7 seconds; its id map `0 0 4294967295`;
- a stock k3d cluster up in 15 seconds, and a `hostUsers: false` pod in
  it, mapped at 3741908992, with no kubelet configuration;
- this repository's devcontainer built inside it, and `task
  sandbox:test:k8s` with `APOTHECARY_LIVE_STRICT=1`: workspace lifecycle
  183 s, browser 20 s, assistant pod 68 s, all passing. That run used a
  guest kernel rebuilt with `CONFIG_WIREGUARD=y`; userspace WireGuard on
  the stock kernel was tested separately, tunnel only.

Measured against plain docker on the same host:

| | Kata | host |
|---|---|---|
| sha256 of 3 GiB | 10.6 s | 11.0 s |
| 4k random read/write, block volume | 111k / 48k per second | 121k / 52k |
| the same on virtio-fs | 30k / 13k | |
| 20k small files, block volume | 0.65 s | 0.51 s |
| the same on virtio-fs | 3.5 s | |

Memory the host holds for an 8 GB pod after filling 5 GB of file cache
and freeing 3 GB of process memory:

| Configuration | at once | +60 s | +6 min |
|---|---|---|---|
| static sizing, free-page reporting | 7.6 GB | 5.1 GB | 5.1 GB |
| the same, mem-agent at 30 s | 8.2 GB | 5.2 GB | 3.2 GB, falling |
| hotplug | 7.7 GB | 5.1 GB | 5.1 GB |
| hotplug, mem-agent | 8.1 GB | 5.1 GB | 3.4 GB, falling |

Not tested: the engine provisioning under Kata (the pods were written by
hand), `pvc` storage, Kata installed on a node's own k3s, EKS and nested
virtualization, CPU overcommit, what the mem-agent costs a busy workload.
