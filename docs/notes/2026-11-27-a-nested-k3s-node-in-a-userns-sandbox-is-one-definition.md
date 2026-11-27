# A nested k3s node in a userns sandbox is one definition

What a k3s node needs to run inside a userns sandbox is one file,
`deploy/userns/nested-node.Dockerfile`: an image over `kata.env`'s
`K3S_IMAGE`, built from that file alone (`docker build - < it`) where it is
used. `task k8s:cluster` uses it as k3d's node image when the sandbox's
docker runs containers through `apo-runc`. The live suite's nested check
(`liveNestedKubernetes`) uses it under every runtime, sending the file on
stdin, since the sandbox has no checkout of this repository.

## Why

Each requirement was found on the userns rig, in a sandbox shaped as the
provisioner's, with stock `rancher/k3s` run by the sandbox's docker:

- k3s exited: `cannot enter cgroupv2 "/sys/fs/cgroup/kubepods" with domain
  controllers -- it is in an invalid state`. Its processes stayed in the
  node's root cgroup. k3d's own entrypoint moves them out; plain `docker
  run` does not. The image's entrypoint does that step, then k3s.
- With the cgroup step and the `KubeletInUserNamespace` gate, the node was
  Ready and an ordinary pod ran. A privileged pod failed at its pause
  container's sysfs. With `apo-runc` as the node's runc, through a
  containerd drop-in, it ran
  (`2026-11-27-a-privileged-container-in-a-userns-sandbox-gets-a-read-only-sysfs.md`).
- A user-namespaced pod failed: `failed to create network namespace for
  sandbox: failed to set effective UID: invalid argument`. With no `kubelet`
  user, the kubelet never asks `getsubids` and picks ids outside the
  sandbox's map. With a `kubelet` user, a `getsubids` that answers `0:
  kubelet 65536 <the sandbox's ids less 65536>` and `max-pods` that fits,
  it ran, mapped at 2818048 in the sandbox's 4194304 ids.
- Without tmpfs for the kubelet's and containerd's state, the same pod
  failed: `failed to prepare idmapped overlay: failed to create idmapped
  mount`. An idmapped mount needs a filesystem mounted inside the user
  namespace. So the nested node's images are held in memory, and count
  against the sandbox's memory limit. `/var/lib/kubelet` is a `VOLUME` of
  the k3s image, so docker backs it with a directory on its own disk: the
  setup mounts its tmpfs over that, and skips only a tmpfs already there.
  The first version skipped any mount point, and the pod then failed with
  `failed to set MOUNT_ATTR_IDMAP on /var/lib/kubelet/pods/…/etc-hosts:
  operation not permitted`.
- The rig's cluster DNS (10.43.0.10) sits in k3s's default service range,
  so a nested node on the defaults captured its own sandbox's resolver.
  The live check gives the nested cluster other ranges.

## What now

- The image's setup (`/bin/k3d-entrypoint-apo-userns.sh`, which k3d runs
  before k3s, and `/bin/apo-node`, the entrypoint outside k3d) does nothing
  in the node's own users (an identity `uid_map`: a laptop, CI, a privileged
  or Kata sandbox). In a user namespace it adds the kubelet gate. With
  `/bin/apo-runc` mounted in and a read-only `/sys`, it sets `apo-runc` as
  containerd's runc. With more than 131072 ids, it adds the range, the
  `getsubids`, `max-pods` and the tmpfs. Kubelet arguments go in
  `/etc/rancher/k3s/config.yaml.d` (`kubelet-arg+`).
- A deviation from the plan, which put the kubelet gate on every cluster
  `k8s:cluster` creates: the gate is set by the image, only inside a user
  namespace, and `k8s:cluster` uses the image only in a userns sandbox.
  Elsewhere the cluster is created as before, so CI's k3d suites and a
  laptop are unchanged.
- `liveNestedKubernetes` now runs an ordinary pod that resolves a Service
  by name and a privileged pod, under every runtime with NestedKubernetes,
  and the user-namespaced pod where the runtime's capabilities on this
  cluster (its integration's flag included) have UserNamespaces. Under
  userns the suite also runs `liveUsernsNesting`. `task sandbox:test:userns`
  sets `APOTHECARY_LIVE_NESTED_K8S` and `APOTHECARY_LIVE_NESTED_USERNS=1`
  (the rig's kubelet gives a pod 4194304 ids).

`k8s:cluster`'s k3d command was run by hand in a userns sandbox pod on the
rig (k3d 5.9.0 copied in, the image built from this file): the node was
Ready, and a privileged and a user-namespaced pod ran on it.

Not yet done: the proof the plan asks for, `task sandbox:test:k8s` in a
userns workspace with the provisioner's flag on, where
`TestLiveK8sAssistantPod` then runs instead of skipping, with its time and
the sandbox's peak memory. It needs an instance with a userns provisioner;
this branch was built against a rig on a laptop.

Tests: `TestLiveSandboxLifecycle` under `APOTHECARY_LIVE_RUNTIME=userns` on
the rig (`task sandbox:test:userns`: the nested node Ready in 6s, the
user-namespaced pod mapped at 589824), and under privileged with
`APOTHECARY_LIVE_NESTED_K8S` on the same rig (the setup does nothing
there). Kata's suite, which runs the same check, was not run here.
