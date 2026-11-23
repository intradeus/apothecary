# A Kata workspace runs user-namespaced pods but not the gateways

Updates 2026-11-20-live-suites-skip-loudly-and-never-in-ci and the
`AGENTS.md` paragraph that follows it. Both describe a workspace under
sysbox. Kata is now a runtime a workshop can choose.

## Why

Under sysbox, a k3d cluster on the workspace's docker cannot start a
`hostUsers: false` pod: the workspace owns 65536 ids, and the nested
kubelet maps the pod far above them. `TestLiveK8sAssistantPod` skips there
and says why. A Kata workspace has a kernel of its own, so that limit does
not apply. The 2026-11-20 note says only "a sandbox with a kernel of its
own runs it too", and that is not the whole answer.

## What a Kata workspace runs

- **User-namespaced pods: yes.** `liveNestedKubernetes`, at the end of
  `TestLiveK8sLifecycle` under Kata in CI's kata suite, starts a k3s node on
  the sandbox's docker, runs a `hostUsers: false` pod there and reads its
  uid map. That is the assistant pod's user-namespace shape
  (2026-11-22-kata-runs-in-ci-with-a-nested-cluster-inside-its-sandbox).
- **The k8s suites in full: not on the stock guest kernel.** Every sandbox,
  assistant pod and browser of a k3d cluster inside the workspace reaches
  the network through an egress gateway pod. The gateway sets up kernel
  WireGuard. A k3d node inside the VM runs on the guest's kernel, and the
  stock guest kernel has no WireGuard and loads no modules. Only the
  sandbox side falls back to wireguard-go
  (2026-11-22-the-sandbox-tunnel-falls-back-to-wireguard-go). So the
  nested gateways do not start. The spike ran `task sandbox:test:k8s`
  inside a Kata sandbox only on a guest kernel rebuilt with WireGuard
  (`docs/sandbox-runtimes-plan.md`, appendix A).

## What now

`AGENTS.md` says both: the sysbox skip is expected, and a Kata workspace
runs user-namespaced pods but not the nested gateways. In both cases the
k8s suites are CI's to run, and CI fails on any live-suite skip
(`APOTHECARY_LIVE_STRICT`). Two changes would make a Kata workspace run
them in full: a guest kernel with WireGuard (one config line), or a
wireguard-go fallback in the gateway. Neither is planned.

## Tests that hold it

`liveNestedKubernetes` (CI's kata suite). Nothing runs the k8s suites
inside a Kata workspace, by the reasoning above.
