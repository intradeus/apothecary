# `task test` in the devcontainer (2026-11-13)

- **Why:** `task test` (go:test, ui:test, the sandbox images and both
  sandbox suites) failed in the devcontainer: the k8s suite needs k3d and
  kubectl, which only CI installed, and three things assumed the docker
  daemon's host is the machine `task` runs on.
- **k3d v5.9.0 and kubectl v1.34.1 are pinned like the other tools:**
  `.devcontainer/Dockerfile` ARGs with per-arch checksums, CI's
  sandbox-test (k8s) installs exactly those (kubectl was the runner's
  unpinned one), `TestToolPinsMatchCI` holds the versions and the amd64
  checksums together, and `pin:k3d` guards `sandbox:test:k8s`. kubectl is
  the engine image's (`deploy/Dockerfile`), held to it by the same test: the
  live tests drive the cluster as the engine does, one minor below k3d's
  default k3s.
- **Container mode, decided by `deploy/self-container.sh`:** inside a
  container of the very daemon the CLI drives (the devcontainer with the
  docker-outside-of-docker feature; a run's workspace has the same shape),
  it finds its own id from the hostname bind in `/proc/self/mountinfo` and
  checks it with `docker inspect`. Elsewhere it answers nothing and every
  task behaves as before.
  - `k8s:cluster` joins the `k3d-<cluster>` network and points the
    kubeconfig at `k3d-<cluster>-serverlb:6443`: the host's
    `0.0.0.0:<port>` is not the container's. The shared kubeconfig is
    then valid on one side only; the other side's status check fails on
    it and rewrites it. `k8s:down` disconnects first, so k3d can remove
    the network.
  - `k8s:test-live` offers the container's IP on that network as the first
    `APOTHECARY_LIVE_HOST_GATEWAY` candidate: the browser test's pods must
    reach the test process, which is no longer behind the network gateway.
  - `go:test:linux` and `sandbox:test:harness` bind the checkout by its
    path on the daemon's host (the source of the bind mount holding it):
    `/workspaces/apothecary` does not exist there.
- **Verified** in the repo's devcontainer brought up by the devcontainer CLI
  with the engine's pinned docker-outside-of-docker feature, as `dev`, on
  Docker Desktop (arm64): `task test` all green in 624 s, both live k8s
  tests and the four conformance tests passing.
- **The feature's socat proxy truncates attached output.** On Docker
  Desktop the socket is root's, so the feature serves it to `dev` through
  `socat`, which closes a connection 0.5 s after one side half-closes: an
  attached `docker run` loses whatever its container prints after that.
  The exit code still arrives (a separate wait call), so a check's ok/FAIL
  is right, but a failing harness or go:test:linux log can be empty; rerun
  it detached (`docker run -d`, `docker logs`) to read it.
- **`task ci` in a run** (same day): the goal is a run that verifies with
  everything CI runs but the macOS and Windows builds, taking that load off
  CI.
  - `task ci` now runs `engine:image` too: it said "everything CI runs" but
    left out CI's engine-image job.
  - A run's workspace is a container of its sandbox's own daemon, the shape
    container mode handles: k3d's nodes are its siblings, the live tests'
    sandbox pods run in them. Containers on one kernel, not VMs: the cost
    is a daemon per level, not a guest OS.
  - The egress defaults already allow every nested pull: Docker Hub
    directly (k3s's system images and the nested gateway's mirror pull
    with their own containerd, not through the sandbox's mirror), ghcr.io
    (k3d's images), mcr.microsoft.com (the browser). `dl.k8s.io` (kubectl)
    and `get.helm.sh` (helm), which this repo's devcontainer build fetches
    and every project had to add by hand, are in the defaults now too.
  - kubectl is now the engine image's v1.34.1 in the devcontainer and CI
    too (it was briefly a second pin, v1.35.5).
  - AGENTS.md: the heavy suites run in a workspace with docker; `task check`
    while iterating, `task ci` from the verifying stage; never the release
    builds.
  - Measured in the devcontainer on Docker Desktop (11 CPUs, 9.4 GB):
    `task ci` peaked at 8.3 GB. Eight checks passed; `ui:test` lost two
    timer-bound tests (Inbox selection, FlowEditorPage delete) under the
    load, taking 771 s instead of 488 s, and passed alone (7304 tests).
  - Not yet verified in a run under sysbox: k3d's privileged nodes on the
    sandbox's daemon, and WireGuard links created at the nested level.
    The README lists the project settings (storage 60Gi, verify timeout
    60m, 16 GB).
- **The UI tests' async waits get 10 s, not 1 s** (same day). Testing
  Library's `waitFor`/`findBy*` and vitest's `vi.waitFor` default to 1 s.
  Under `task ci` (every suite at once) the UI tests ran about 8× slower:
  983 took over a second, against 120 alone, and two waits that hold in
  milliseconds idle timed out (Inbox selection, FlowEditorPage delete).
  `src/test/setup.ts` sets `asyncUtilTimeout` and wraps `vi.waitFor` with
  one `ASYNC_TIMEOUT`; a wait that names its own timeout keeps it, and
  `src/test/setup.test.ts` holds all three (it failed before the change: a
  1.2 s wait gave up at 972 ms). A wait still ends as soon as it holds, so
  only a real failure reports later. Under `task ci` again both passed, the
  Inbox one at 4.8 s.
  - That run lost a different test, PanelEditor's long edit (1.3 s idle),
    to the 30 s test timeout at 38 s: no wait in it, the Docker VM (9.4 GB)
    was swapping (about 3 GB in swap). Memory, not timing: `testTimeout`
    stays, and the README asks 16 GB for a run that verifies with
    `task ci`.
