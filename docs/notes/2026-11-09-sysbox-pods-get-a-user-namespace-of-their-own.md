# Sysbox pods get a user namespace of their own (2026-11-09)

Found deploying the Kubernetes provisioner under sysbox (runtime class,
privileged off — the posture `APOTHECARY_WORKSPACE_DOCKER=auto` opens the
daemon door for) on a k3s cluster with containerd 2 and Ubuntu nodes: the
sandbox pod never started, `ip_unprivileged_port_start: permission denied`.
The cluster-side workaround was an admission policy adding `hostUsers:
false` to every `sysbox-runc` pod; the fix is for the engine to say so.

- `podManifest` sets `hostUsers: false` whenever `unprivileged()` — the
  predicate that already drops the privileged security contexts and opens
  the daemon door. `kube.PodSpec.HostUsers *bool` carries it (a pointer:
  `false` must reach the wire through `omitempty`). Privileged pods leave it
  unset: they are host root regardless, and a user namespace there is
  nothing the operator asked for.
- Why: sysbox's documented pod shape on Kubernetes is `runtimeClassName` +
  `hostUsers: false` (K8s v1.30+ with containerd v2.0.5+ or CRI-O; sysbox
  0.7.0 added the `sysbox-runc features` command containerd needs before it
  admits the handler for user namespaces). Left on the host's user
  namespace, sysbox makes one for the container itself, but by then
  containerd has created the pod's network namespace, owned by the host's
  user namespace, and the sandbox sysctls it writes
  (`net.ipv4.ip_unprivileged_port_start`) fail from a user namespace that
  does not own it. With `hostUsers: false` the kubelet makes the user
  namespace first and sysbox adopts its mapping.
- Requirements recorded in the README (cluster requirements): containerd ≥
  2.0.5, sysbox ≥ 0.7 (an older `sysbox-runc` is rejected: `RuntimeClass
  handler "sysbox-runc" does not support user namespaces`), kernel ≥ 6.3,
  idmapped mounts on the storage class. And the Ubuntu 24.04+ AppArmor
  restriction on unprivileged user-namespace creation
  (`kernel.apparmor_restrict_unprivileged_userns`): sysbox and privileged
  pods are host root and unaffected; a tool inside an unprivileged sandbox
  that builds a user namespace (bubblewrap, Chrome, rootless podman) is
  denied until the node lifts the sysctl or confines it with a `userns`
  profile. And, found by the same deployment, Ubuntu 25.04+'s
  `fusermount3` profile, which denies sysbox-fs its FUSE mounts under
  `/var/lib/sysboxfs` so no sandbox starts (nestybox/sysbox#947, #1005,
  open against sysbox 0.7.0 on 26.04): the README carries the maintainers'
  three-rule local override. Both field helps (`privileged`,
  `runtime_class`) point at the README.
- Not changed: the docker provisioner (`APOTHECARY_SANDBOX_RUNTIME`) —
  docker hands sysbox no user namespace and sysbox makes its own, the
  live-verified path. The AppArmor point applies to Ubuntu docker hosts too.
- **`/sys` is the host's.** The same deployment's next failure: the `wg`
  init container crash-looped on `cat /sys/class/net/eth0/mtu`. Under
  sysbox with `hostUsers: false` the container's `/sys` is the host's sysfs
  — same device number, the node's interfaces (enp*, docker0, cni0, veths),
  no eth0 of the pod — while netlink (`ip link`) shows the pod's own `lo`
  and `eth0`. `apo-wg-up` now reads the MTU from `ip -o link show dev eth0`
  and fails loudly when there is none; it was the only `/sys` read in the
  sandbox scripts (the gateway's `sysClassNet` runs in the gateway
  container, never under the runtime class). Residual for the security
  model: a sysbox sandbox on Kubernetes can *read* the node's sysfs
  (interface names, hardware details; writes are refused, root there being
  an unprivileged host uid) — sysbox/containerd behaviour, worth an upstream
  report, and far less than a privileged sandbox exposes.
- The 2026-10-27 residual ("sysbox on Kubernetes designed but not
  live-verified") stands for the engine's own live test; the pod shape is now
  the one a real cluster accepted.
