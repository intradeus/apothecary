# Kata sandboxes run on emptydir disks

Phase 5 of `docs/sandbox-runtimes-plan.md`: the `kata` runtime, a VM per
sandbox. The plan left four things to verify and one to decide. This note
records them, as measured on the rig (`deploy/kata/rig.sh`: k3s
v1.36.4 in a privileged container with the host's `/dev/kvm`, Kata 4.4.0
static, Cloud Hypervisor, the stock guest kernel 6.18.62).

## The decision: a Kata sandbox's disks are always its own emptyDirs

A claim from a directory-backed class (k3s `local-path`, most `hostPath`
provisioners) reaches the guest over virtio-fs, where docker cannot keep its
layers. A block-backed class (EBS) or a raw-block claim might work, but each
would need its own tests and setup that the fresh-box install does not
provide. So a Kata sandbox's volumes are emptyDirs, which Kata's
`emptydir_mode = "block-plain"` turns into ext4 disks.

This is the runtime's trait (`vm`), not the integration's storage:

- The integration's `storage` (default `pvc`) is for its other runtimes. A
  Kata sandbox gets its two emptyDirs whatever it says, and no claim is
  made for it. Approving `kata` beside `sysbox` leaves the sysbox
  sandboxes on their claims, and stop-on-idle with them.
- The first version flipped the whole integration to `emptydir` when kata
  was approved and `storage` was empty (sysbox runs lost their claims and
  their stop-on-idle), and refused `pvc` + `kata` only when a run's
  provisioner was built (every run then failed). Both are gone with the
  storage check: `pvc` + `kata` is a valid integration.
- A Kata sandbox's checkout lasts as long as its pod; a replaced pod starts
  afresh. It cannot be stopped and kept (`workspace.CanStopUnder`): the
  note 2026-11-22-idle-sandboxes-stop-and-keep-their-disks says how a
  workshop is told.

## What was verified

- **Size limits**: in the guest, each block emptyDir shows the host's whole
  disk. The kubelet still enforces `sizeLimit`, though: the disk is a sparse
  `disk.img` inside the emptyDir's directory, and a pod that wrote 1.5 GB
  into a 1 GiB volume was evicted within 30 s. The guest mounts it with
  `discard`, so deleted files are freed on the host (three 500 MB
  write-and-delete cycles left 15 MB), and docker's churn does not evict a
  run. Each of the two volumes is bounded by the integration's
  `storage_size`.
- **Sizing**: a pod limited to 4 GiB and 2 CPUs gets a guest with 3,966 MiB
  and 3 vCPUs (Kata adds its one default vCPU). With
  `static_sandbox_resource_mgmt`, the VM is the limit and no more. The pod's
  cgroup is the limit plus the RuntimeClass overhead. Cloud Hypervisor held
  about 320 MB, so the rig's class asks `cpu: 250m, memory: 320Mi`. An
  unsized sandbox is refused (`needs a size`) instead of getting Kata's
  2 GB default.
- **`/dev`**: a privileged container's `/dev` has 15 entries, with neither
  `/dev/kmsg` nor `/dev/net/tun`. `apo-dockerd`'s prelude
  (`APO_DEV_PRELUDE=1`, set for VM runtimes only) mounts the guest's
  devtmpfs aside and copies in what is missing.
- **MTU**: the inner docker took the default 1500 whenever no gateway wrote
  `/run/apo/mtu`. `apo-dockerd` now uses the pod interface's MTU in that
  case, under every runtime.

## Not measured: the mem-agent periods

The rig's Kata configuration turns the mem-agent on with
`memcg_period_secs = 30` and `compact_period_secs = 30`
(`docs/kubernetes.md` copies them). They are a starting point, not a
measurement: with `reclaim_guest_freed_memory`, the agent's reclaim and
compaction are what hand a VM's freed memory back to the node, and a
shorter period hands it back sooner for some guest CPU. Nothing here
measured that trade, and no lifecycle test depends on the values. A
deployment that measures it should change both files and this note.

## The MTU fallback is for every runtime

`apo-dockerd` gives the inner daemon the pod interface's MTU when no
gateway wrote `/run/apo/mtu`, under every runtime, not Kata alone: docker's
1500 default is wrong on any overlay network (1450 on flannel's VXLAN), and
the guest only made it visible first. With a gateway (the default), the
tunnel's MTU is used as before, so the pods the golden manifests describe
behave the same.

## A finding: port-forward cannot reach a VM pod

`kubectl port-forward` has containerd dial the port inside the pod's network
namespace on the node. Under Kata the relay listens in the guest, so every
forward is refused ("connect: connection refused"). An engine inside the
cluster dials the pod's IP, which reaches the guest, so nothing changes for
it. An engine outside the cluster (transport `portforward`, the live suites)
dials a VM pod through `kubectl exec -i <pod> -c relay -- /apothecary-shim
relay --stdio-ack <sock>` instead (`kube.ExecConn`, `k8sHost.viaExec`).
`--stdio-ack` answers "OK" once the socket answers, which is the mux's own
ack, so a dial still tells "not served" apart from "served". Each connection
costs one kubectl process, which is fine for a dev engine.

## Not done here

- The chart does not install Kata's RuntimeClass: installing Kata is the
  cluster's job. `deploy/k3s/install.sh --kata` does it on a k3s box, and
  CI runs a strict Kata suite (both in the note
  2026-11-22-kata-runs-in-ci-with-a-nested-cluster-inside-its-sandbox). EKS
  with nested virtualisation is documented (`docs/kubernetes.md`) and
  untried.
- Kernel WireGuard in the guest needs a kernel built with it; the stock one
  tunnels through wireguard-go (phase 4).

## Tests that hold it

- Unit:
  - `TestKataSandboxShape` (on `pvc` too), `TestAVMSandboxTakesNoClaim`,
    `TestCanStop`, `TestK8sAVMSandboxCannotSleep`, the golden
    `sized-kata-emptydir-gateway`, `TestOutOfClusterDialsAVMPodByExec`
    (workspace);
  - `TestExecConn*` (kube);
  - `TestRelayStdio` (shim);
  - `TestKubernetesRuntimeChoice`'s kata cases and
    `TestKubernetesSleepRefusals` (kinds).
- Live: `task sandbox:test:kata` runs `TestLiveK8sLifecycle` under
  `APOTHECARY_LIVE_RUNTIME=kata` and `TestLiveK8sRuntimeProbe`'s Kata probe
  on the rig. Both passed (lifecycle 135 s).
