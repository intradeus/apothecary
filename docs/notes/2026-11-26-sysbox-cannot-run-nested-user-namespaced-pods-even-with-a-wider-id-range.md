# Sysbox cannot run nested user-namespaced pods, even with a wider id range

Follows 2026-11-23-a-kata-workspace-runs-user-namespaced-pods-but-not-the-gateways,
which gives the 65536-id limit as the reason a sysbox workspace cannot run a
`hostUsers: false` pod on a nested cluster. A spike on the k3s host
(2026-11-25/26) removed that limit, and the next one after it, and stopped at
a third that sits in sysbox's design. Kata stays the runtime for projects
that run user-namespaced pods inside their workspace. Apothecary itself is
one of them. Nothing in the code changes.

## Why

The hope was to drop Kata for this case. Kata holds memory per VM, needs
KVM (`*.metal` or nested virtualization on EKS), and its stock guest kernel
has no WireGuard. If sysbox could run nested user-namespaced pods given a
kubelet setting, one runtime would cover every project but kernel-level
work.

## What was tried

The check was `liveNestedKubernetes` (`internal/adapters/workspace/live_k8s_test.go`)
run by hand: a k3s node (`rancher/k3s:v1.36.4-k3s1`) on a sysbox sandbox's
docker, then a `hostUsers: false` busybox pod on it. The host runs k3s
v1.36.4, containerd 2, sysbox, kernel 7.0, cgroup v2. In order, what each
layer needed:

1. **cgroup v2 nesting: solved.** A plain `docker run rancher/k3s` fails
   with `cannot enter cgroupv2 "/sys/fs/cgroup/kubepods" with domain
   controllers -- it is in an invalid state`. The node container starts in
   its own root cgroup, with processes in it. The fix is what k3d's
   entrypoint does: move every process into `/sys/fs/cgroup/init`, then
   write every controller to `cgroup.subtree_control`. With it, the node
   is Ready in 6 s. A test that starts k3s by hand under sysbox needs this.
   Under Kata or privileged docker the test passes without it.
2. **The id range: solved.** The sandbox's user namespace maps 65536 ids.
   The nested containerd fails with `failed to create network namespace
   for sandbox …: failed to set effective UID: invalid argument`, because
   the pod's ids are not mapped. Two settings fix it:
   - On the host: the kubelet drop-in
     `/var/lib/rancher/k3s/agent/etc/kubelet.conf.d/50-userns.conf` sets
     `userNamespaces.idsPerPod: 4194304` (64 × 65536). It goes with
     `kubelet:4194304:4194304000` in `/etc/subuid` and `/etc/subgid`. A new
     sandbox then maps `0 <id> 4194304`. Sysbox adopts the wider mapping
     without complaint.
   - In the nested node: a range for its kubelet. It reads one only when a
     `kubelet` user exists and `getsubids kubelet` answers, and the k3s
     image has neither. Without them it picks ids anywhere in the 32-bit
     space (the Kata CI run mapped a pod at 2155610112), outside the
     sandbox's range. A `kubelet` line in `/etc/passwd`, a `getsubids`
     script printing `0: kubelet 65536 4128768`, and `max-pods` cut to 60
     (63 blocks fit) made the nested kubelet allocate inside it.
3. **Idmapped image layers: only on tmpfs.** Next: `failed to prepare
   idmapped overlay: failed to create idmapped mount: … operation not
   permitted`. The kernel idmaps a mount only when the filesystem supports
   it and the caller is privileged in the user namespace that mounted it.
   The nested containerd's layers sit on the node's root filesystem, the
   sandbox docker's overlayfs, which cannot be idmapped. A host-mounted
   disk (the claim's ext4) belongs to the host's namespace. Under sysbox
   only tmpfs passes both rules. With the layers on tmpfs
   (`/var/lib/rancher/k3s/agent/containerd`), the mount succeeded.
   containerd 2's overlay `slow_chown` fallback (copy and chown instead of
   idmapping) is the disk-backed alternative, but it was not exercised: the
   k3s `config-v3.toml.tmpl` written for it was not picked up. Once the
   wall below was found, the fallback no longer mattered.
4. **`/proc` in the pod's user namespace: blocked.** With the layers on
   tmpfs, runc fails: `error mounting "proc" to rootfs at "/proc": mount
   src=proc … invalid argument`. A process in a new user namespace may
   mount procfs only if a fully visible procfs is already in its mount
   namespace. Sysbox mounts over parts of `/proc` in every sandbox
   (`/proc/sys`, `/proc/meminfo`, `/proc/uptime`, …), so the sandbox can
   see its own limits. Those mounts are locked: nothing inside can remove
   them. Sysbox also traps `mount` calls in the sandbox. Either is enough to
   refuse the mount, and neither is a setting.

One untried idea: mount a second, unobstructed procfs in the nested node
before the pod starts, so the kernel finds a fully visible one. If sysbox's
trap is what refuses the mount, it changes nothing. If it worked, it would
be a third workaround stacked on the other two. It was left there.

## What now

- No code change. `envport.RuntimeSysbox` keeps `UserNamespaces: false`.
  `TestLiveK8sAssistantPod` still skips in a sysbox workspace, and that
  skip is still expected (`AGENTS.md`).
- Kata stays the runtime for projects that run user-namespaced pods in
  their workspace. Sysbox stays the default for everything else.
- The host's kubelet setting from the spike is not part of the
  documented host setup. Revert it: delete the drop-in and the `kubelet:`
  lines in `/etc/subuid` and `/etc/subgid`, then restart k3s. Kept, it is
  harmless (each user-namespaced pod gets a wider slice of an otherwise
  unused 32-bit id space) but buys nothing.
- If sysbox ever changes how it handles `/proc` for nested user namespaces,
  start from step 4: steps 1–3 are known.

## Tests that hold it

None new: the result is a limit, measured by hand. `liveNestedKubernetes`
stays gated on `NestedKubernetes && UserNamespaces`, so it runs only under
Kata (CI's kata suite).
