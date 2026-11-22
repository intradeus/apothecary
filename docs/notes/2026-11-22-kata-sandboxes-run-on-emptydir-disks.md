# Kata sandboxes run on emptydir disks

Phase 5 of `docs/sandbox-runtimes-plan.md`: the `kata` runtime, a VM per
sandbox. The plan left four things to verify and one to decide. This note
records them, as measured on the rig (`deploy/kata/rig.sh`: k3s
v1.36.4 in a privileged container with the host's `/dev/kvm`, Kata 4.4.0
static, Cloud Hypervisor, the stock guest kernel 6.18.62).

## The decision: `pvc` storage is refused, not supported

A claim from a directory-backed class (k3s `local-path`, most `hostPath`
provisioners) reaches the guest over virtio-fs, where docker cannot keep its
layers. A block-backed class (EBS) or a raw-block claim might work, but each
would need its own tests and setup that the fresh-box install does not
provide. So a Kata sandbox's volumes are emptyDirs, which Kata's
`emptydir_mode = "block-plain"` turns into ext4 disks:

- `workspace.CheckStorage` refuses `kata` with `pvc` storage, both when an
  integration approves it and when a pod is built.
- `workspace.DefaultStorage`: an integration that sets no storage gets
  `emptydir` when it approves a VM runtime, else `pvc` as before. The field
  lost its `pvc` form default, so an empty field means "the runtime's".
- A sandbox's checkout lasts as long as its pod; a replaced pod starts
  afresh, as `emptydir` storage already did under the other runtimes.

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
  cluster's job. Also not done: `deploy/k3s/install.sh`'s Kata step, and EKS
  with nested virtualisation (`docs/kubernetes.md`). These are phase 7 docs
  and stay unverified.
- CI: whether the runner exposes `/dev/kvm` is unchecked, so there is no
  strict Kata job.
- Kernel WireGuard in the guest needs a kernel built with it; the stock one
  tunnels through wireguard-go (phase 4).

## Tests that hold it

- Unit:
  - `TestKataSandboxShape`, `TestCheckStorage`, the golden
    `sized-kata-emptydir-gateway`, `TestOutOfClusterDialsAVMPodByExec`
    (workspace);
  - `TestExecConn*` (kube);
  - `TestRelayStdio` (shim);
  - `TestKubernetesRuntimeChoice`'s kata cases (kinds).
- Live: `task sandbox:test:kata` runs `TestLiveK8sLifecycle` under
  `APOTHECARY_LIVE_RUNTIME=kata` and `TestLiveK8sRuntimeProbe`'s Kata probe
  on the rig. Both passed (lifecycle 135 s).
