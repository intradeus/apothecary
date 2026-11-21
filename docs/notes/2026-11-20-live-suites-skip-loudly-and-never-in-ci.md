# Live suites skip loudly, and never in CI

Why: a run of Apothecary on this repository failed its test stage on
`TestLiveK8sAssistantPod` (`task sandbox:test:k8s`) with no change to the
assistant pod. The pod sat at `Init:0/4` for the five minutes `Ensure`
waits, then failed with `kubectl wait`'s timeout and nothing else; only
`kubectl describe` showed the cause, repeated every 15 seconds:

    FailedCreatePodSandBox: … failed to start noop process for unshare:
    fork/exec /proc/self/exe: operation not permitted

The assistant pod is the only live-test pod with `hostUsers: false`
(`2026-11-19-the-assistant-pod.md`). The kubelet maps such a pod's ids far
above 65536 (a k3d node on a plain docker host: `0 3035496448 65536`). A
workspace's docker daemon runs under sysbox (the chart's
`runtimeClassName`), in a user namespace of its own that holds 65536 ids,
and a nested user namespace can only map ids its parent holds: in a
`0 0 65536` namespace, `unshare --user --map-users=0:65536:65536` is
refused writing `uid_map`, `0:1000:1000` works. So a k3d cluster in a
sysbox workspace cannot run the assistant pod, whatever the branch, and no
docker setting in the workspace changes that. Nor does a wider range for
the workspace (the kubelet's `idsPerPod`): a stock kubelet, with no
`kubelet` user and no `getsubids` in its node image, picks a pod's range
anywhere in the 32-bit space. (`docker run alpine unshare -U` failing
there proves nothing: docker's default seccomp profile refuses it on any
host.) CI's runners are plain VMs and run the test as written. A sandbox
with a kernel of its own runs it too: `docs/sandbox-runtimes-plan.md`.

Reproducing it on a laptop showed a second way the suite fails without a
code change: kubectl v1.31's port-forward stops carrying connections after
one ends early (`error copying from remote stream to local connection: …
broken pipe`, then every later dial hangs), which the assistant suite does
on every turn; v1.34.1, the pinned one, does not. The k8s tasks checked
task's and k3d's versions but not kubectl's.

What now:

- **The assistant live test asks first.** `userNamespaces` starts the
  smallest `hostUsers: false` pod (the gateway image, `true`). If its
  sandbox is refused for its user namespace (`userNamespaceRefused`:
  `FailedCreatePodSandBox` naming unshare, a user namespace or an id map),
  the test skips at once and quotes the event; any other probe failure
  fails the test. Another `FailedCreatePodSandBox` (a fresh cluster's
  network plugin) is waited out.
- **Every live suite skips through `internal/platform/livetest`.**
  `livetest.Skip` skips locally and fails under `APOTHECARY_LIVE_STRICT=1`,
  which CI's `sandbox-test` sets: a suite CI depends on cannot go green by
  not running there. `internal/devtools/livetests` fails the tree on a
  bare `Skip`, `Skipf` or `SkipNow` in a `//go:build live` file.
- **An assistant pod that never comes Ready says why.** `kube.Client.
  LastWarning` reads the pod's newest Warning event; `K8sHost.Ensure`
  appends it to the wait's error (the person's assistant status shows it),
  so the cause is in the error, not only in `kubectl describe`.
- **`pin:kubectl`** guards `sandbox:test:k8s` and `k8s:test-live`, read
  from the devcontainer's `KUBECTL_VERSION` like the other pins.

Not done: running the assistant suite nested in CI (sysbox on a runner,
k3d inside it) would only show the skip; the nested case is what the probe
names. The engine's own port-forward (`Forward`, an engine outside the
cluster) runs the image's kubectl, the pinned one.

Tests: `TestLastWarningIsTheNewest` (kube), `TestK8sEnsureSaysWhyThePodIsNotReady`
and `TestUserNamespaceRefused` (assistant), `TestSkipSkipsLocally`,
`TestSkipFailsUnderStrict` (livetest), `TestLiveSuitesSkipThroughLivetest`,
`TestTheCheckCatchesABareSkip` (devtools/livetests), `TestToolPinsMatchCI`
(devtools/devcontainer: the kubectl pin and its use). Run on a k3d cluster
with kubectl v1.34.1 and `APOTHECARY_LIVE_STRICT=1`: the probe passes and
`TestLiveK8sAssistantPod` passes; with kubectl v1.31.2 it fails as above.
