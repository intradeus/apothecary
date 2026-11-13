# CI builds each image once, and the k8s suite imports instead of pushing (2026-11-11)

- **Profiled first, cold** (no build cache or base images), on a 20-core box:
  13 minutes in all.
  - Building all five images: 4m47, of which the engine image took 1m53 and
    the tools bundle 1m52.
  - Pushing them to the cluster's registry: 3m50.
  - The live tests: 3m.
  - Memory stayed under 1.5 GB for the cluster and pods. CPU was about 3
    cores on average, with bursts to every core during builds.
- **CI built the same images three times:** the build job (five), the k8s
  suite (five) and the harness suite (tools).
- **Now one `images` job builds the four the suites run.**
  - It fills the GitHub Actions cache (`deploy/images.sh` with
    `CACHE=gha-warm`, one cache scope per image).
  - The harness and k8s jobs wait for it, then build through that cache
    (`CACHE=gha-read`): every layer a hit, which is a download.
  - The engine image moved to the build job; no suite runs it (the live
    tests run the engine as a test process).
  - Not artifacts: those count against the account's storage (about 2 GB on
    Pro), and a 1–1.5 GB image set per run would exhaust it. The Actions
    cache is a separate 10 GB per repository; eviction there costs speed,
    never a failure.
- **The k8s suite imports its images into the k3d node** (`k3d image import`,
  `APOTHECARY_LIVE_PULL_POLICY=IfNotPresent`) instead of pushing to the
  registry and pulling back. The tools bundle is copied once, not twice, and
  CI no longer reconfigures dockerd for an insecure registry. `k8s:images`
  (push) stays for the `k8s:deploy` dev loop.
- **Path filters.** On a pull request, `images`, harness and k8s run only
  when it touches the sandbox side (`changes` job, dorny/paths-filter).
  Pushes to main run everything, so manual-release still only releases a
  fully tested commit.
- **Measured locally after the change:** cold 8m11 (−37%), warm 6m02
  (−53%). Warm, the image import (1m40) and the tests are what remains.
