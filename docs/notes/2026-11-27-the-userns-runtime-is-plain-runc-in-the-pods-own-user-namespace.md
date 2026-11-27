# The userns runtime is plain runc in the pod's own user namespace

A fourth sandbox runtime, `userns`: plain runc, a Kubernetes pod with
`hostUsers: false`, under a containerd handler `runc-userns` that makes the
pod's cgroup writable (`cgroup_writable = true`, containerd 2.1 or later).
Nothing is installed on the node but that handler. The plan is
`docs/plans/userns-runtime-plan.md`.

## Why

Sysbox cannot run a user-namespaced pod on a cluster nested in its sandbox
(`2026-11-26-sysbox-cannot-run-nested-user-namespaced-pods-even-with-a-wider-id-range.md`),
and Kata needs KVM and holds memory per VM. A spike on the k3s host found
plain runc nests docker and Kubernetes in a user-namespaced pod once the
pod's `/proc` is unmasked and its cgroup is its own.

## What now

- `envport.RuntimeUserns`: NestedContainers, NestedKubernetes, Contained.
  Not UserNamespaces in its row: whether a pod inside can have a user
  namespace of its own depends on how many ids the node's kubelet gives the
  sandbox, which is a fact about the cluster.
- **The cluster's addition lives in the choice, not the table.**
  `RuntimeChoice.Adds` is what a provisioner's cluster gives a runtime
  beyond its row, and `RuntimeChoice.Capabilities(rt)` is the row with it.
  `Choose`, the fix a refusal names (`RuntimeChoice.Satisfying`) and the
  API's runtime offer read it, so a workshop that needs user namespaces is
  told `kata` where the cluster adds nothing and may keep `userns` where it
  does. The package-level `Satisfying` is gone (only tests called it).
  Rejected: a second runtime name for the wide case (two names for one pod
  shape), and a table changed at boot (the fact is per integration).
- The fact is the integration's alone: the Kubernetes provisioner's
  `nested_user_namespaces` (on/off, default off; anything else refused),
  with the kubelet hazard in its help. There is no chart value or
  `APOTHECARY_K8S_*` default: it states something an admin checked about
  the nodes. It reaches `KubeConfig.NestedUserNamespaces`. The adapter says
  which runtimes it widens (`NestedUserNamespaceAdds`: the rows with the
  `usernsNesting` trait, not sysbox), so trait and capability cannot drift.
  With the flag on, a provision whose sandbox's `uid_map` holds 65536 ids or
  fewer says so in the workspace log: on a cluster where only some nodes
  have the kubelet setting, it is the only sign.
- The adapter's row: class `runc-userns`, `ownUserNamespace`, not
  privileged, and the `usernsNesting` trait. The trait gives the sandbox
  container its security context (`usernsSecurity`: `procMount: Unmasked`,
  `SYS_ADMIN, NET_ADMIN, MKNOD, SYS_RESOURCE, SYS_PTRACE`, seccomp and
  AppArmor `Unconfined`; the comment says why each) and the image its switch
  (`APO_USERNS_NESTING=1`). The runtime probe's pod gets the same security
  context (`sandboxSecurity`, shared by both), so a cluster that refuses
  `Unmasked` fails the integration's Test, not a run.
- The tunnel's `wg` container keeps `NET_ADMIN` alone, as under sysbox, and
  under userns also gets `procMount: Unmasked` and AppArmor `Unconfined`, a
  deviation from the plan (which left it untouched). On the rig it
  crash-looped: `sysctl: error setting key 'net.ipv4.conf.all.rp_filter':
  Read-only file system`. containerd mounts `/proc/sys` read-only for an
  unprivileged container, and its default AppArmor profile denies writes
  there; sysbox serves `/proc/sys` writable itself, so it never showed. The
  pod's root owns its network namespace's sysctls in a user namespace of its
  own, so unmasked they are writable with `NET_ADMIN`, and nothing else.
  `kube.SecurityContext` gained `procMount` and `appArmorProfile`.
- The chart can make the RuntimeClass (`workspaces.usernsRuntimeClass`),
  as it does Kata's (`2026-11-23-the-chart-can-make-the-kata-runtimeclass.md`):
  off by default, refused when userns is the default runtime and
  `workspaces.runtimeClassName` names another class; no overhead.
- The workshop form says when to pick it, with a second line where the
  cluster adds user namespaces (`form.project.rtAboutUserns`,
  `rtAboutUsernsWide`).

**The security trade-off.** Isolation rests on the user namespace, with
every capability the namespace's. Seccomp and AppArmor are off for the
sandbox container, so the whole kernel API is reachable from it: a kernel
bug is a wider door than under sysbox's filtering, and much wider than
Kata's VM. Fine for the operator's own projects; not the runtime for
untrusted code. The README, `docs/kubernetes.md`, the kind's help and the
form say so.

**Correction to
`2026-11-26-reverting-the-kubelet-ids-per-pod-setting-needs-the-wide-pods-gone-first.md`:**
widening `idsPerPod` is not safe either. The spike saw the kubelet refuse to
start (`wrong user namespace length`) whenever a running user-namespaced
pod's mapping differs from `idsPerPod`, in either direction. Any change of
it, up or down, is made with no such pod on the node: the order in
`docs/kubernetes.md`, "Nested user namespaces".

On the userns rig (`deploy/userns/rig.sh`) the whole lifecycle suite
passes under userns: a provision in 1m21s, the tunnel on kernel WireGuard
through the project's gateway, the egress checks, and a sleep and a wake on
a local-path claim (16s). The provisioner's Test passes under it.

Not tried: a workshop moved between sysbox and userns, rebuilt on the same
claim. Both give the pod idmapped volumes, so the checkout's and docker's
files should keep their owners, and `shapeChange` replaces the pod as
between any two container runtimes. If a rebuild there ever fails, the
next run is the way out.

Tests: `internal/ports/envport/runtime_test.go` (the row, `Adds` in
`Choose` and `Satisfying`), `internal/httpapi/runtime_test.go` (the offer),
`internal/project/kinds/config_test.go`
(`TestKubernetesNestedUserNamespaces`), `internal/adapters/workspace`'s
`runtime_test.go` (the shape, the security context on the sandbox
container, `wg`'s, the class override, the trait's invariant, the probe,
the warning) and the golden `testdata/pods/userns-pvc-gateway.json`, the other
goldens unchanged; the chart's `ci/userns-values.yaml` and `task
chart:lint`'s refusals; `SandboxRuntimeField.test.ts`.
