# Sandbox suites split into the cluster and each runtime

## Why

The k8s suite (`task sandbox:test:k8s`) did two jobs. It tested the
provisioner as a cluster integration: the runtime probe, the sandbox
tunnel, the project browser, the assistant pod. It also tested a workspace
sandbox's whole life, under whatever runtime its sandboxes ran, which was
privileged, the default of the lifecycle test. The kata suite ran that
lifecycle again under Kata. So privileged was tested only as a side effect
of the k8s suite, nothing named the runtime axis, and a third runtime had
no obvious place to go. Sysbox, the runtime EKS is expected to run, is
tested nowhere, and its installer's download was broken (below).

## What now

- **Two kinds of suite, told apart by test name.**
  - `TestLiveK8s*` is the provisioner as a cluster integration, under any
    runtime: the runtime probe and the cluster's refusals, the WireGuard
    tunnel, the browser, the assistant pod.
  - `TestLiveSandbox*` is a sandbox's whole life under one runtime: the
    lifecycle test, renamed from `TestLiveK8sLifecycle`, which reads
    `APOTHECARY_LIVE_RUNTIME`. A runtime's own checks join its suite by
    `-run` (`TestLiveKataDiskLimit` in kata's).
  - A new test lands in the right suite by its name.
- **Tasks.**
  - `sandbox:test:k8s` runs `TestLiveK8s`.
  - `sandbox:test:privileged` runs `TestLiveSandbox` with
    `APOTHECARY_LIVE_RUNTIME=privileged`.
  - `sandbox:test:kata` is as it was, on its rig.
  - The two k3d suites share one cluster through `sandbox:k3d:up` (images
    built unless prebuilt, the cluster, the import). Each suite keeps a
    cluster that was up and removes one it made.
  - `sandbox:test:k3d` runs both on one cluster, and the second runs when
    the first fails.
  - `task sandbox:test` (and `task test` / `task ci`) runs the harness
    suite beside the two k3d ones, each with its own line in
    `build/checks/summary`.
- **CI.** The `sandbox-test` matrix is `harness`, `k3d`, `kata`.
  - k3d brings its cluster up once, then runs the k8s suite and the
    privileged suite as separate steps. The privileged step runs when the
    k8s one failed, as long as the cluster came up.
  - It is one job, not two, because a k3d job's cost is mostly its
    cluster: about 15 GB of images loaded and imported on a 2-vCPU,
    7 GB runner. A second job would pay that again for the same cluster.
  - Each runtime that needs a node of its own gets its own matrix entry
    (kata: the rig on the runner's KVM).
- **Sysbox has a suite now** (`sandbox:test:sysbox`, a matrix entry). Its
  node is a rig like Kata's (`deploy/sysbox/rig.sh`): k3s in one privileged
  container, with sysbox's daemons (`sysbox-mgr`, `sysbox-fs`) running in
  the same container. So the runner needs nothing of sysbox installed, only
  the sysctls and the AppArmor rule its package would set, which the CI step
  applies.
  - The release is pinned once, in `deploy/sysbox/sysbox.env` (version and
    each `.deb`'s sha256). `deploy/k3s/install.sh` installs from the same
    file on a real box.
  - A spike weighed the alternatives:
    - sysbox installed on the runner itself: it rewrites and restarts the
      runner's docker, and nobody can run it locally;
    - kind or k3d nodes inside sysbox: there sysbox is the outer runtime,
      not the pods' handler;
    - a self-hosted or nightly job: loses per-PR coverage.
  - Sysbox recommends 4 CPUs and the runner has 2. sysbox-fs serves `/proc`
    over FUSE, so a slow runner is where this suite will be flaky first.
  - No nested Kubernetes under sysbox: the lifecycle test checks it only
    where the runtime claims user namespaces. A plain `rancher/k3s` node
    also failed to start inside a sysbox container in the spike (a cgroup v2
    "invalid state" error), although `envport` says sysbox has
    `NestedKubernetes`. That claim is unverified and is worth its own look.
- **The sysbox download in `install.sh` had gone stale.** It built
  `sysbox-ce_<v>-0.linux_<arch>.deb`. Since 0.7.0 the asset has no `-0`, so
  every fresh install 404ed. Its default, 0.7.0, also cannot run on
  containerd 2 with kernel 6.8+ (nestybox/sysbox#1013). It now reads
  `sysbox.env`, which pins 0.7.2 and the `.deb`'s sha256, and it refuses
  anything older than 0.7.1. A version other than the pinned one, or
  another `.deb`, needs its sha256 (`SYSBOX_DEB_SHA256`).
- Older notes keep the old test name; they describe what was true then.

## Tests that hold it

- `go test ./internal/devtools/livetests` holds every live suite to
  `livetest.Skip`, so CI's `APOTHECARY_LIVE_STRICT` fails a suite that
  would skip.
- `task repo:lint` (actionlint) checks the workflow. The k3d steps and
  their `-run` patterns are checked by CI's own `sandbox-test (k3d)` run.
- `TestToolPinsMatchCI` (`internal/devtools/devcontainer`): only
  `sysbox.env` sets the sysbox pins, the installer and the rig source it,
  the Taskfile reads it, and the installer and the rig's Dockerfile both
  download `sysbox-ce_${SYSBOX_VERSION}.linux_<arch>.deb`.
- `TestLiveK8sRuntimeProbe` runs a positive sysbox probe under
  `APOTHECARY_LIVE_RUNTIME=sysbox`, as it does for Kata.
- The rig's `up` runs a pod under the `sysbox-runc` class with
  `hostUsers: false` before the suite, so a broken node fails there.
