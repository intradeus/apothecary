# Kubernetes

The cluster side in full; the README has the summary. A fresh Ubuntu box is set up
end to end by `deploy/k3s/install.sh` ([its guide](../deploy/k3s/README.md)).

Apothecary runs on any Kubernetes cluster, k3s included, from the Helm
chart in `charts/apothecary`. The chart deploys the engine (and,
optionally, a Postgres). The engine then creates every workspace object
itself, in a namespace of its own:

- **one sandbox pod per run**: `docker:dind` plus git and the devcontainer
  CLI, unprivileged under a sysbox `RuntimeClass` (in a user namespace of
  its own) by default, with the run's checkout and inner docker state on a
  volume. Compose devcontainers run verbatim inside it;
- **one egress gateway per project** (a Deployment). Each sandbox pod's
  default route is a WireGuard tunnel to it, and a
  network policy lets the pod send nothing but that tunnel and HTTP to the
  engine — so the gateway, which judges by TLS SNI / HTTP Host and records
  every denial, is the only way out. The cluster's own pod and service ranges
  and cloud metadata are never reachable through it;
- **one browser pod per project** (Microsoft's Playwright MCP image, pinned by
  the engine, with the shim seeded in), reaching the engine's session proxy
  only.

## Cluster requirements

- **NetworkPolicy enforcement** (k3s has it built in; most CNIs do). Without
  it the sandbox could route around its gateway.
- The **`wireguard` kernel module** on the nodes (in-tree since Linux 5.6;
  loaded on demand). Where a kernel has none (a Kata guest's, or the nodes
  of a k3d cluster inside a Kata workspace), the sandbox and the gateway
  each run `wireguard-go` instead, on their own, with no privilege beyond
  their existing manifests: it needs a TUN device, which they make with
  the runtime's default `CAP_MKNOD` when none is handed out. The gateway carries all of a
  project's runs, so userspace costs it CPU and throughput the module does
  not: keep the module where the nodes can have it.
- Nodes may confine `/usr/bin/wg` with AppArmor (Ubuntu 26.04 ships
  `/etc/apparmor.d/wg`, which also attaches to a privileged container's `wg`):
  nothing to do, the tunnel reads its key from `/etc/wireguard`, the one place
  that profile allows. Sandbox images up to v3.0.0 read it from
  `/secrets/wg` and their `wg` init container fails with
  `fopen: Permission denied`; upgrade, or allow that path on the node
  (`echo '/secrets/wg/** r,' | sudo tee -a /etc/apparmor.d/local/wg && sudo
  apparmor_parser -r /etc/apparmor.d/wg`).
- **[sysbox](https://github.com/nestybox/sysbox) on the nodes that run
  sandboxes**, for the chart's default runtime (`workspaces.runtime:
  sysbox`, RuntimeClass `sysbox-runc`; the others are in
  [Sandbox runtimes](#sandbox-runtimes)). The RuntimeClass's own scheduling
  (`nodeSelector`, `tolerations`) places sandbox pods on those nodes. This is
  also what opens the [daemon door](../README.md#docker-in-workspaces) under
  `APOTHECARY_WORKSPACE_DOCKER=auto`. A cluster of your own without sysbox
  (k3d, a dev box) can run sandboxes privileged instead
  (`workspaces.runtime: privileged`): sandbox root is then root on its node.
  Under a runtime class the sandbox pod
  is also created with `hostUsers: false`, sysbox's documented pod shape on
  Kubernetes: a user namespace of the pod's own, made by the kubelet and
  adopted by sysbox. Without it sysbox still makes one for the container,
  but containerd has set up the pod's network namespace before that, and
  the sandbox sysctls it then writes fail from a user namespace that does
  not own it — `ip_unprivileged_port_start: permission denied`, and the pod
  never starts. So the cluster needs Kubernetes user namespaces (on by
  default since 1.33): **containerd ≥ 2.0.5** (or CRI-O), **sysbox ≥ 0.7**
  (an older `sysbox-runc` answers no `features` and the kubelet rejects the
  pod: `RuntimeClass handler "sysbox-runc" does not support user
  namespaces`), a kernel ≥ 6.3, and idmapped mounts on the workspace
  storage class (ext4, xfs, btrfs; one more reason NFS is out).
- **AppArmor on Ubuntu nodes** meets sysbox twice:
  - **`fusermount3` (Ubuntu 25.04 and later, 26.04 included).**
    `/etc/apparmor.d/fusermount3` lets FUSE mount under a few trees only,
    and sysbox-fs mounts the virtualised `/proc` and `/sys` of every
    container under `/var/lib/sysboxfs` — so no sandbox starts at all
    (`failed to pre-register with sysbox-fs`; `journalctl -u sysbox-fs`
    says `fusermount3: mount failed: Permission denied`, the kernel log
    `apparmor="DENIED" operation="mount" profile="fusermount3"`). Allow
    sysbox's mounts in the profile's local override, with the rules its
    maintainers give, and reload:

    ```sh
    sudo tee -a /etc/apparmor.d/local/fusermount3 <<'EOF'
    # allow Sysbox to create fuse mounts
    mount fstype=@{fuse_types} options=(nosuid,nodev,rw) -> /var/lib/sysboxfs/**/,
    mount fstype=@{fuse_types} options=(nosuid,nodev,ro) -> /var/lib/sysboxfs/**/,
    umount /var/lib/sysboxfs/**/,
    EOF
    sudo apparmor_parser -r /etc/apparmor.d/fusermount3 && sudo systemctl restart sysbox
    ```

    The kernel log then still shows one `fusermount3` denial of
    `dac_override` per sandbox start: harmless, the profile withholds that
    capability on purpose and the rules above do not grant it.

  - **Unprivileged user namespaces (24.04 and later).**
    `kernel.apparmor_restrict_unprivileged_userns=1` lets only a process
    that is root on the host, or runs under a profile allowing it, create
    a user namespace. sysbox and a privileged sandbox are host root and
    unaffected; under sysbox nothing in a sandbox is — that is the point —
    so a tool in the workspace that sandboxes itself with a user namespace
    of its own (a harness's bubblewrap command sandbox, Chrome, rootless
    podman, docker's userns-remap) is denied: `bwrap: setting up uid map:
    Permission denied` in the run, `apparmor="DENIED"
    operation="userns_create" profile="unconfined"` in the node's kernel
    log. Lift it (`echo kernel.apparmor_restrict_unprivileged_userns=0 |
    sudo tee /etc/sysctl.d/60-apothecary-userns.conf && sudo sysctl
    --system`), or ship a profile with a `userns,` rule for each such
    binary (it attaches by the binary's path, as the `wg` profile above
    does).
- **sysbox (and containerd, k3s) must not restart while sandboxes run.**
  sysbox-fs loses track of the running containers, and every exec into
  them fails from then on: `OCI runtime exec failed: … unsafe procfs
  detected: openat2 fsmount:fscontext:proc/thread-self/fd/: operation not
  permitted`. Ubuntu's `needrestart` restarts services on its own after
  unattended upgrades (libc, systemd), so exclude them from it
  (`deploy/k3s/install.sh` writes this file) and restart them only with no
  run active:

  ```sh
  sudo mkdir -p /etc/needrestart/conf.d
  sudo tee /etc/needrestart/conf.d/apothecary.conf <<'EOF'
  $nrconf{override_rc}{qr(^sysbox)} = 0;
  $nrconf{override_rc}{qr(^containerd)} = 0;
  $nrconf{override_rc}{qr(^k3s)} = 0;
  EOF
  ```

  If it happens anyway, the run's workspace shows **broken**: Rebuild (or
  the engine's recovery of a run it restarted under) replaces the pod,
  keeping its volume — the checkout, agent commits and inner docker state.
  With `storage: emptydir` the checkout goes with the pod.
- **The assistant** (chart `assistant.enabled`, on by default) runs one pod
  per person who uses it, in the workspaces namespace, beside that person's
  own egress gateway and volume (`assistant.volumeSize`, `storageClass`).
  The pod runs unprivileged in a user namespace of its own (`hostUsers:
  false`, so Kubernetes user namespaces, as above, on any node it lands
  on), and each conversation runs under **Landlock ABI 6 or later: Linux ≥
  6.12** with Landlock among the node's LSMs (`cat
  /sys/kernel/security/lsm`). On an older kernel the pod starts but every
  question is refused with the reason, rather than run unconfined. A
  person's pod stops after `assistant.idle` unused (default 30m) and their
  volume is deleted after `assistant.volumeDays` unused (default 30; 0
  never). Turn it off with `--set assistant.enabled=false`.
- A **local or block storage class** for workspace volumes: the inner docker
  daemon cannot run on NFS.
- Clusters whose pod or service ranges differ from k3s's defaults:
  `--set workspaces.protectCIDRs=<pods>,<services>,169.254.0.0/16`.
- **On EKS:**
  - Sandbox nodes are a managed node group on an Ubuntu EKS AMI with
    `sysbox-deploy-k8s`, labelled and tainted for sandboxes. Check
    sysbox's supported OS and kernel list against the AMI. Bottlerocket is
    not supported.
  - EBS volumes are single-zone, so use a `WaitForFirstConsumer` gp3
    StorageClass.
  - An ALB in front of the engine needs
    `alb.ingress.kubernetes.io/target-type: ip`, because the engine's
    Service is ClusterIP.
  - Images can be mirrored to ECR with `image.overrides`.
  - **Kata on EKS needs KVM on the sandbox nodes**, which ordinary EC2
    instances do not expose: bare-metal instances (`*.metal`), or an
    instance type that offers nested virtualization with it turned on (C8i,
    M8i or R8i; in the launch template, or Karpenter's
    `cpuOptions.nestedVirtualization`). amd64 only: the pinned Kata release
    is amd64's, so no Graviton. On any other node a Kata sandbox never
    starts. Not tried yet: check that a Kata pod reaches ClusterIP services
    (sandboxes reach the engine and their gateway that way) before relying
    on it.

## Sandbox runtimes

What isolates a run's sandbox. The deployment names a default
(`workspaces.runtime`) and the runtimes its nodes run
(`workspaces.runtimes`); an admin may narrow those on the provisioner
(System → Provisioner, whose Test starts a pod under each), and a workshop
picks among them (Settings → Workspace).

| Runtime | Isolation | Nodes need | Storage | Stops when idle |
|---|---|---|---|---|
| `sysbox` (default) | containers in a user namespace of the pod's own; root in the sandbox is an unprivileged uid on the node | sysbox ≥ 0.7, containerd ≥ 2.0.5, kernel ≥ 6.3 ([above](#cluster-requirements)) | `pvc` or `emptydir` | with `pvc` |
| `kata` | a VM per sandbox (Kata Containers, Cloud Hypervisor); the sandbox is privileged inside its own guest kernel, never on the node | KVM (`/dev/kvm`), Kata ≥ 4.4 with a `kata` RuntimeClass | a raw-block claim from the provisioner's VM storage class, else disks of its own in its VM | with a VM storage class |
| `privileged` | none: sandbox root is root on its node | nothing | `pvc` or `emptydir` | with `pvc` |

All three run docker inside, compose and k3d included.

### Installing Kata

Kata is the cluster's to install; the chart only makes its RuntimeClass,
when asked (step 4). On a one-node k3s
box, `deploy/k3s/install.sh --kata` does steps 1 to 4 below and step 5's
chart setting, and smoke-tests a Kata pod. `deploy/kata/rig.sh` is another
worked example (a throwaway k3s node in docker, what `task
sandbox:test:kata` and CI run on). Both take the release and its checksum
from `deploy/kata/kata.env`, and step 2's edits from
`deploy/kata/configure.sh`. On a real node by hand:

1. Unpack Kata's static release (`kata-static-<version>-amd64.tar.zst`)
   into `/opt/kata`.
2. Copy `share/defaults/kata-containers/runtime-rs/configuration-clh-runtime-rs.toml`
   to `configuration-apothecary.toml` beside it and set:

   ```toml
   emptydir_mode = "block-plain"          # each emptyDir an ext4 disk in the guest
   static_sandbox_resource_mgmt = true    # the VM is the pod's limits, no more
   reclaim_guest_freed_memory = true      # freed guest memory goes back to the node
   mem_agent_enable = true
   memcg_period_secs = 30
   compact_period_secs = 30
   ```

3. Add the runtime to containerd (k3s: in
   `/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.tmpl`, after
   `{{ template "base" . }}`):

   ```toml
   [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.kata]
     runtime_type = "io.containerd.kata.v2"
     runtime_path = "/opt/kata/runtime-rs/bin/containerd-shim-kata-v2"
     privileged_without_host_devices = true
     [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.kata.options]
       ConfigPath = "/opt/kata/share/defaults/kata-containers/runtime-rs/configuration-apothecary.toml"
   ```

   `privileged_without_host_devices` is what keeps a privileged sandbox
   from the node's devices: it gets its VM's.
4. Create the RuntimeClass, with the VM's own memory and CPU as overhead
   (the scheduler counts it, the pod's cgroup holds it):

   ```yaml
   apiVersion: node.k8s.io/v1
   kind: RuntimeClass
   metadata: {name: kata}
   handler: kata
   overhead:
     podFixed: {cpu: 250m, memory: 320Mi}
   ```

   Add a `scheduling` block if only some nodes have Kata. Or let the chart
   make it: `workspaces.kataRuntimeClass.create=true`, with the Kata
   nodes' labels in its `nodeSelector` and their taints in its
   `tolerations` (`charts/apothecary/ci/kata-values.yaml`). The class is
   cluster-wide, so one release per cluster makes it; leave it off where
   `install.sh --kata` made its own.
5. Deploy with `--set workspaces.runtimes='{sysbox,kata}'` (or
   `workspaces.runtime=kata` for Kata by default), and on the provisioner
   set a `storage_size`. Its storage (`pvc` by default) stays the other
   runtimes'. A Kata RuntimeClass of another name goes in the provisioner's
   runtime class as `kata=<class>` (a class alone is the default
   runtime's).
6. For Kata sandboxes that survive their pod (stopping when idle, a node
   restart), set the provisioner's **VM storage class** to a class that
   provisions raw block volumes (`volumeMode: Block`: EBS gp3, TopoLVM,
   Longhorn, Ceph RBD; k3s's `local-path` cannot). Each Kata sandbox then
   gets the run's claim (`storage_size`) as one disk in its VM, which the
   sandbox formats on first use and mounts for its checkout and inner
   docker. Without it, a Kata sandbox gets two emptyDirs of its own, disks
   in its VM that go with its pod. A filesystem claim is never used for
   Kata: it would reach the VM over virtio-fs, where docker cannot keep its
   layers.

**Kata sandboxes must be sized** (System → Limits sets a default, a workshop
its own): an unsized one is refused, not given Kata's 2 GB default. Without
a VM storage class, each of a Kata sandbox's two disks (checkout, inner
docker) holds up to `storage_size`: the guest sees the node's whole disk,
and the kubelet evicts the pod once either holds more (CI's kata suite
checks it), so leave the node's disk room for that. With one, the claim's
size is the disk's.

**On EKS**, see [the EKS requirements](#cluster-requirements): Kata needs
KVM on the sandbox nodes.

### Sizing

A sandbox's size (CPUs, memory) is its pod's limits: the workshop's own,
else the instance default, never above the instance ceiling (System →
Limits). A sized pod requests all of its memory and a quarter of its CPUs
(at least 100m), so **the scheduler plans capacity by memory limits**, and
CPU is shared: sandboxes mostly wait on people and agents.

Under **Kata**, the RuntimeClass overhead comes on top of each pod, and the
VM is the limit: the guest gets exactly the memory and one vCPU more than
the CPUs. A node with 64 GiB holds about seven 8 GiB Kata sandboxes once the
overhead and the system's share are counted. Under sysbox and privileged an
unsized sandbox may use what the node has, or, under a ceiling, up to the
ceiling: it is limited there and requests only 100m and 1 GiB, so a large
ceiling never makes it unschedulable. Under Kata an unsized sandbox is
refused (a ceiling alone does not size a VM). Each sandbox's tunnel and
relay sidecars request 10m and 32 MiB more.

A new size or runtime applies to the next run's sandbox, or a rebuild: a
rebuild replaces the pod when its shape changed and the run's checkout is
on its claim (`pvc`, or a Kata sandbox's block claim). On `emptydir`
storage, Kata without a VM storage class, and between Kata and another
runtime (a disk for one, a filesystem for the other), a rebuild keeps the
pod and says the change waits for the next run.

The run page's graphs show what a sandbox reached against its limit, and a
workshop's Settings → Workspace lists its recent runs' peaks: the evidence
for its size. A process killed at the memory limit, or the sandbox
container killed and restarted, is told in the run's workspace log.

### Size hints and workspace variables

A sandbox's tools cannot always tell how big it is: under sysbox, `nproc`
and Node's `os.cpus()` report the node's CPUs, whatever the limit, and a
test runner that sizes its workers from them (jest's `maxWorkers: "50%"`)
starts far more than the sandbox can hold. So every process in a workspace
gets the sandbox's size as hints:

| Variable | Value | Unset when |
|---|---|---|
| `APO_CPUS` | the pod's CPU limit, rounded up to whole CPUs (`4000m` → `4`, `1500m` → `2`) | the sandbox has no CPU limit |
| `APO_MEMORY_MIB` | the pod's memory limit in MiB | it has no memory limit |

The limit is the workshop's size, else the instance default, else the
ceiling (which limits an unsized sandbox). Under Kata the hint is the limit
too, not the guest's vCPU count, which Kata pads with one of its own. The
hints are the limits of the pod the sandbox runs as, so like the size they
change when the pod is made again (the next run, or a rebuild that replaces
it), never on a live one.

A workshop sets its own variables in Settings → Workspace → Environment
variables (`workspace.env` in the project API): `JEST_MAX_WORKERS=2`, or
`NX_PARALLEL=${APO_CPUS}`. `${APO_CPUS}` and `${APO_MEMORY_MIB}` in a value
expand to the hint (empty without one); nothing else expands. Names are
`[A-Za-z_][A-Za-z0-9_]*`. The `APOTHECARY_` and `APO_` prefixes are the
engine's, and so are `PATH`, `HOME`, `TERM`, `COLORTERM`, the harness's
launch and broker variables and the provider keys: all refused. At most 64
variables, 4 KiB a value. They are plain configuration, shown back by the
API and the form: credentials go in the workshop's secrets.

Both reach every process in the workspace: command stages, the harness and
the shells it opens, the run's terminal, SSH sessions, services, and the
browser editor. Last wins: the repository's `containerEnv` and `remoteEnv`,
then the hints, then the workshop's variables. A changed variable reaches
the next process started in a live workspace, with no rebuild. The browser
editor is the exception: it keeps what it had when it started, until the
sandbox is made again. The workspace log says, at each provision, which
hints and variables were set.

### Stopping idle sandboxes

A sandbox nobody uses (no stage working, no terminal, editor, exposed port,
SSH session or question to the assistant) can be stopped after a period:
System → Limits sets the instance default (empty: never), a workshop its
own or never. The pod is deleted; its volumes and the gateway stay, and the
sandbox still counts toward `max_environments`. Its next use (a stage, the
terminal, the editor, a port, SSH, the run page's **Wake**) starts a pod on
the same volumes in seconds; processes it ran start again from the
project's hooks. With `emptydir` storage, or under Kata without a VM
storage class (its disks are then its VM's own), there is nothing to keep,
so those sandboxes are never stopped: the form says why for the workshop's
runtime, and refuses minutes there.

## A local cluster

With [k3d](https://k3d.io) and [helm](https://helm.sh) installed:

```sh
task k8s:cluster    # a k3d cluster + image registry; never touches ~/.kube/config
task k8s:images     # build the six images from the working tree, push as :local
task k8s:deploy     # helm install with charts/apothecary/ci/k3d-values.yaml
# → http://apothecary.localhost:8090
task k8s:test-live  # the provisioner's and the browser's live tests against the cluster
task k8s:down
```

Every kubectl call these tasks make names `deploy/.k3d-kubeconfig`
explicitly; `task k8s:kubectl -- get pods -A` is the shortcut. A docker
daemon on the containerd image store (the default of a fresh Docker 29
install) pushes to the registry over HTTPS unless `/etc/docker/daemon.json`
lists `k3d-registry.localhost:5111` under `insecure-registries`;
`task k8s:images` checks and says so.
