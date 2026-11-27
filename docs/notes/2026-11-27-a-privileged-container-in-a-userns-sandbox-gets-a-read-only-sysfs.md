# A privileged container in a userns sandbox gets a read-only sysfs

The userns runtime (plain runc in the pod's own user namespace) runs its
sandbox's docker containers through `apo-runc` (`workspace/aporunc`), a
wrapper around runc that adds `ro` to a writable sysfs at `/sys` on
`create` and `run`. The ticket asked for a wrapper that turns the `/sys`
mount into a read-only rbind of the pod's `/sys`. This one keeps a fresh
sysfs and makes it read-only instead.

## Why

A privileged container's spec asks for a fresh read-write sysfs. Inside a
user namespace the kernel mounts a fresh sysfs only when one at least as
permissive is already visible (`mnt_already_visible`), and the pod's `/sys`
is read-only, so runc fails at `create` with `error mounting "sysfs" to
rootfs at "/sys": operation not permitted`.

Two ways out were tried on this host (kernel 7.3, runc 1.5.3, the sandbox
image's own runc) in a user namespace shaped as the pod (own user, network,
mount and pid namespaces, a read-only sysfs, the host's mounts gone):

- **Make the pod's `/sys` writable first**, before dockerd: refused. sysfs
  is a "global" filesystem, so its superblock belongs to the initial user
  namespace whatever network namespace it shows, and only the node's root
  can remount it read-write. A bind remount succeeds, but the mount still
  reads `ro` (the superblock's flag), and a fresh writable sysfs is still
  refused. So a wrapper is needed, as the ticket says.
- **The wrapper, two ways:** a fresh sysfs mounted `ro` in the container's
  own network namespace is allowed, and so is a read-only rbind of the
  pod's `/sys`. The fresh one shows the container's own interfaces in
  `/sys/class/net`. The rbind shows the sandbox's, which a k3d node's
  flannel and kubelet would read as the node's. So the wrapper adds `ro`
  and nothing else to the mount. (It later gained a second change, the
  CPU quota:
  `2026-11-27-apo-runc-gives-an-inner-container-the-sandboxs-cpu-quota.md`.)

Then on the userns rig (`deploy/userns/rig.sh`), in a sandbox pod shaped as
the provisioner's: `docker run --privileged` starts, adds a link and mounts
a tmpfs, and its `/sys` is a read-only sysfs of its own.

The same limit holds one level down. On a k3s node nested in the sandbox
(itself a privileged container, so with a read-only sysfs), a privileged
pod, even its pause container, fails the same way under the node's stock
runc. With `apo-runc` as that node's runc, through a containerd drop-in
(`config-v3.toml.d`, `BinaryName`; k3s's `--default-runtime` accepts only
runtimes it detects itself), the pod runs and writes a sysctl. A project
gateway's `sysctl` init container is that pod, so this is what lets the
k8s suite run in a userns workspace
(`2026-11-27-a-nested-k3s-node-in-a-userns-sandbox-is-one-definition.md`).

## What now

- `workspace/aporunc`: stdlib only, built in its own stage of the sandbox
  image from a named build context (`deploy/images.sh`). It reads the
  subcommand past runc's global flags and the bundle (`--bundle`, `-b`, the
  working directory). It changes only a `sysfs` mount whose destination is
  `/sys` and whose options lack `ro` (or `rro`), drops `rw`, adds `ro`, and
  rewrites `config.json` (a file beside it, renamed over it, same mode).
  Every other field keeps its value; JSON whitespace is not kept. Any other
  subcommand, or a spec it cannot read, reaches runc untouched. It execs
  `$APO_RUNC`, else the first `runc` on PATH that is not itself.
- `apo-dockerd`, under `APO_USERNS_NESTING=1`: refuses an identity
  `uid_map` (the handler ran the pod in the node's users) and a cgroup it
  cannot write (the handler lacks `cgroup_writable`), naming the fix. Then
  `--add-runtime apo-runc=… --default-runtime apo-runc`. No cgroup code:
  `docker:29-dind`'s own `dind` moves every process to `/sys/fs/cgroup/init`
  and enables the controllers. On the rig a container over its memory limit
  is killed (137) and the sandbox lives on.
- The limit to know: a privileged container in a userns sandbox cannot
  write `/sys`. Nothing written to `/sys` can reach the node anyway: its
  files are owned by the node's root.

Tests: `workspace/aporunc/runc_test.go` (the rewrite on a privileged spec,
what it leaves alone, unreadable specs, each argument form, the real runc's
lookup, the exec); the live suite's `liveUsernsNesting` (the privileged
container's read-only `/sys`, the kill at the limit, a compose project).
