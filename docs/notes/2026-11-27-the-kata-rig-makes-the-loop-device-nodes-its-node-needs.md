# The Kata rig makes the loop device nodes its node needs

Adds to 2026-11-23-kata-sandboxes-keep-their-disk-on-a-raw-block-claim,
which gave the rig (`deploy/kata/rig.sh`) its Block pool on loop devices
and called the pool verified. It worked only on a host with loop device
nodes to spare.

## Why

CI's kata suite failed at the first sandbox, before any VM started:

    FailedMapVolume: MapVolume.MapBlockVolume failed for volume "apo-block-2":
    blkUtil.AttachFileDevice failed … losetup -f <map path> failed: exit status 1

Three facts make it:

- **The node's `/dev` does not follow the kernel.** A privileged
  container's `/dev` is a tmpfs holding the host's device nodes as they
  were when the container started. A loop device the kernel makes later
  appears in `/sys/block`, and in the host's `/dev`, never in the node's.
- **The kubelet takes a loop device of its own per pod on a Block volume.**
  It holds the mapped volume open through one (`losetup -f <map path>`), on
  whichever number the kernel hands out: the lowest free one. So a node
  whose pool is six loop devices needs a seventh node for its first
  sandbox, an eighth for the second.
- **The rig's own fallback never worked.** It ran `mknod` on what `losetup
  -f` printed when the node was missing. The k3s image's losetup
  (util-linux 2.40) prints `/dev/loopN (lost)` then, and `mknod` refused
  the name. The pool was only ever made from nodes the host already had.

Why the suite passed until then, and what changed, was not seen: the
runner's log does not show its loop devices, and nothing on the branch that
failed touches the rig. The likely account: an Ubuntu kernel makes eight
loop devices at boot, six for the pool and two for the lifecycle test's two
sandboxes, none to spare; with two in use on the runner when the node
starts, the pool takes the six left and the first sandbox's volume cannot
be mapped. The rig's `up` line now prints the kernel's highest loop device,
so the next log says.

## What now

- **`loop_nodes`, on every `up`, before the pool.** It makes a node in the
  node's `/dev` for every loop device the kernel has (`/sys/block/loop*`)
  and for the next `LOOP_SPARE` numbers: three per pool volume (its own,
  its pod's, and that of a pod replacing that one). A node the kernel has
  no device for yet costs nothing: nothing opens it until the kernel hands
  out its number.
- **The pool attaches with `losetup -f --show`**, one step that finds the
  device and attaches the file, in place of find, `mknod`, attach.
- **`down` detaches the kubelet's loop devices first.** With a pod still
  on a volume, detaching the pool's device alone left two behind on the
  host until a reboot: the kubelet's, and under it the pool's, holding the
  deleted file.
- The minor of `loopN` is taken as `N`, as before. That holds while the
  loop module's `max_part` is 0, its default.

## Verified, and not

- On a plain k3s container (the rig's `block_pool`, sourced from the file;
  no Kata), on a host whose `/dev` had no spare loop node:
  - before: with only the pool's six nodes, the pod's volume fails as in
    CI, and adding the seventh node by hand lets it start;
  - after: three pods on Block claims start, the kubelet's devices on
    numbers made after the node started.
- On the rig itself (`rig.sh up`, on a host with KVM): two Kata pods on
  `apo-block` claims start, each with the volume as a disk in its guest.
  `rig.sh down` with both still up leaves no loop device attached on the
  host (two before, for one pod).
- Not run locally: `task sandbox:test:kata`, the suite that failed. CI's
  `kata` suite holds it.

## Tests that hold it

- Live (`task sandbox:test:kata`, CI's `kata` suite):
  `TestLiveSandboxLifecycle` provisions a sandbox on an `apo-block` claim,
  a second run's beside it (the cross-run check), and sleeps and wakes one.
