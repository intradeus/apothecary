# The userns rig runs k3s under systemd

`deploy/userns/rig.sh` is the userns runtime's throwaway node, what `task
sandbox:test:userns` and CI's `userns` suite run on. Unlike the sysbox and
Kata rigs, its container runs systemd as its first process, with k3s as a
unit (`deploy/userns/Dockerfile`, `k3s.service`).

## Why

First tried as the other rigs are, the stock k3s image as the container's
main process: k3s then uses the cgroupfs driver. A pod under the
`runc-userns` handler got its cgroup mounted read-write, as
`cgroup_writable` says, but every file in it stayed owned by the node's
root, an unmapped id in the pod. `mkdir /sys/fs/cgroup/init` and a write to
`cgroup.subtree_control` were refused, so a sandbox's docker daemon could
not run. runc hands a user-namespaced pod its cgroup through systemd's
delegation only. Under systemd (k3s's containerd then sets `SystemdCgroup =
true` itself), the same pod makes and removes a child cgroup.

## What now

- The node image: Ubuntu 24.04 with systemd, k3s's whole `/bin` from
  `kata.env`'s `K3S_IMAGE`, and the kubelet's range in `/etc/subuid` and
  `/etc/subgid` (`kubelet:65536:268435456`, 64 pods of `IDS_PER_POD`). The
  container: privileged, a private cgroup namespace, `/run` and `/run/lock`
  on tmpfs, stopped with `SIGRTMIN+3`.
- Written into the k3s volume before the first start: the handler in
  `config-v3.toml.tmpl` and the kubelet's `userNamespaces.idsPerPod:
  4194304` in `kubelet.conf.d`. The node has no pod before both are there,
  so the kubelet's hazard (a running pod of another mapping length) cannot
  arise.
- The kubelet's data is a volume of its own, on the host's filesystem,
  which takes idmapped mounts.
- The kubelet's image GC is off (`imageGCHighThresholdPercent: 100`). On a
  docker host whose disk was 93% full, past the kubelet's 85% threshold,
  the node deleted the suite's imported images between the import and the
  run, and the sandbox pod sat in `ImagePullBackOff`: an imported image
  cannot be pulled again. The other rigs have the same exposure and keep
  their defaults.
- The smoke pod is shaped as the provisioner's sandbox and checks what the
  runtime rests on: a child cgroup made and removed, and more than 65536
  ids in its `uid_map`.

Not tried: the rig on CI's runner. It needs the runner to start systemd in a
privileged container with a private cgroup namespace, and the `wireguard`
module (a sandbox in a user namespace has no `wireguard-go` fallback).

Tests: the rig's own smoke pod; `task sandbox:test:userns`
(`TestLiveSandboxLifecycle`, `TestLiveK8sRuntimeProbe` under
`APOTHECARY_LIVE_RUNTIME=userns`).
