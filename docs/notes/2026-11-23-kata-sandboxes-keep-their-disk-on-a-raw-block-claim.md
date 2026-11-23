# Kata sandboxes keep their disk on a raw-block claim

Corrects part of 2026-11-22-kata-sandboxes-run-on-emptydir-disks, which left
a Kata sandbox on emptyDirs of its own: gone with its pod, so it could not
stop when idle, and a node restart lost the run.

## Why a raw-block claim

A Kata sandbox cannot take the run's claim the way sysbox does:

- A filesystem claim reaches the guest over virtio-fs, where docker cannot
  keep its layers (overlayfs on virtio-fs).
- Kata does not support `subPath`, which the other runtimes use to put the
  checkout and docker's data on the one claim.

A claim with `volumeMode: Block` reaches the guest as a virtio-blk disk,
whatever class provisions it. It is the one shape that works on any
cluster whose storage can provide it. A block-backed filesystem class (EBS)
in filesystem mode would also give the guest a disk, but only for some CSI
drivers, and nothing would tell the engine which.

## What now

- **One integration field, `vm_storage_class`** (`KubeConfig.VMStorageClass`).
  When it is empty, nothing changes. When it is set, a Kata sandbox's run
  gets a claim from that class with `volumeMode: Block`. The sandbox
  container gets the claim as the device `/dev/apo-data`
  (`volumeDevices`). The integration's `storage` stays the other runtimes'.
  The field is opt-in because k3s's `local-path`, the fresh-box default,
  cannot provide Block volumes.
- **The sandbox formats and mounts the disk itself** (`apo-dockerd`,
  `APO_DATA_DEVICE`):
  - It formats the disk as ext4 only when `blkid` finds no signature on it.
    A disk that holds another filesystem is refused, never formatted.
  - It mounts the disk and binds its `run` and `docker` directories over
    the run folder and `/var/lib/docker`. The bind is checked by device and
    inode, not `mountpoint`: `/var/lib/docker` is the image's `VOLUME`, so
    it is a mount point already.
  - `e2fsprogs` joins the sandbox image.
  - Alpine's `dumpe2fs` lives in `e2fsprogs-extra`, and the first version
    used it to detect a formatted disk. That check failed silently, so
    every boot reformatted the disk and wiped the run. `blkid` replaced it.
- **The init containers keep an emptyDir.** The shim and tools seeds and
  the relay mount a `run` emptyDir (4Gi, `vmSeedSize`) at the run folder,
  as before. They cannot reach the device: only the sandbox container
  formats it. The sandbox mounts that emptyDir at `/var/lib/apo-seed` and
  binds the seeded `shim` and `tools` into the folder on the disk. Each pod
  reseeds them.
- **Stop-on-idle follows the claim.** `CanStopUnder(runtime, storage,
  vmStorageClass)` allows a Kata stop with a VM storage class.
  `kubernetesChoice` computes each runtime's refusal on its own (no global
  `NoSleep`). `checkStop` asks the live pod: it allows a stop when the pod
  uses the claim, mounted or as a device. When no pod exists, the configured
  answer decides.
- **A shape change between a device and a mount** (kata ⇄ sysbox on one
  run) replaces the pod only when both use the claim the same way. When
  they don't, the run waits for its next provision and the note says why
  (`claimUse`, `shapeChange`). The disk's ext4 is not a directory tree that
  sysbox's subPaths could mount.
- **The rig has a Block pool** (`deploy/kata/rig.sh`): StorageClass
  `apo-block` with no provisioner, and static local Block volumes on loop
  devices. `down` detaches the loop devices. `sandbox:test:kata` sets
  `APOTHECARY_LIVE_VM_STORAGE_CLASS=apo-block`, so CI's kata suite runs
  `liveSleepWake` under Kata.

## Bounding a Kata emptyDir (verified, now in CI)

Without a VM storage class, each of a Kata sandbox's emptyDirs is a disk
whose size the guest cannot see: it shows the node's whole disk. The
kubelet bounds it, by measuring the disk's backing file and evicting past
`sizeLimit`. The rig showed it once. `TestLiveKataDiskLimit` now holds it
in CI's kata suite: a sized Kata pod writes 512 MiB into an emptyDir
limited to 256 MiB, and must be evicted, with the eviction naming the
volume. Its name does not start with `TestLiveK8s`, so the k8s suite's
`-run TestLiveK8s` never picks it up.

## Verified, and not

- Verified locally: `apo-dockerd`'s block logic, on a loop device in a
  privileged dind.
  - The first boot formats the disk.
  - A restart keeps the checkout and docker's data, and reseeds the shim
    and tools.
  - A second run of the script changes nothing.
  - A vfat disk is refused.
- Verified locally: the rig's Block pool, on a k3s container.
  - A Block claim binds, and the device appears at `/dev/apo-data`.
  - Data survives a new pod.
  - A released volume comes back Available.
- Not verified locally: Kata itself, which needs KVM. CI's kata suite
  covers the claim, sleep and wake, and the eviction.

## Tests that hold it

- Unit:
  - `TestCanStop`, table-driven over `CanStopUnder`;
  - `TestAVMSandboxOnABlockClaim`: the claim's mode and class, the device,
    the env, the mounts, init containers without the device, `claimUse`,
    `canStop`, and the kata→sysbox `shapeChange` note;
  - `TestK8sAVMSandboxCannotSleep`, with VM storage class cases and live
    pods with and without the device;
  - the golden `sized-kata-block-claim-gateway`;
  - `TestKubernetesSleepRefusals`, with a `vm_storage_class` case (kinds).
- Live (`task sandbox:test:kata`, CI's `kata` suite):
  - `TestLiveK8sLifecycle` sleeps and wakes a Kata sandbox on `apo-block`;
  - `TestLiveKataDiskLimit`.
