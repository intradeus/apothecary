# A merged pull request is not tested again; caches, pins (2026-11-12)

- **Main skips what a pull request's green run already tested.**
  - A commit SHA can't tell: the merge makes a new commit, and the pull
    request's run tested its own merge commit (refs/pull/N/merge).
  - The tree can: git's hash of the files.
  - The `passed` job, the last of a pull request's run (only when every job
    passed or was skipped), uploads an artifact named `ci-green-<tree>` of
    the tree it tested, kept 30 days.
  - On a push to main, `changes` looks the tree up. If it finds a marker,
    nothing runs and the run is green, so manual-release's gate (a green
    run on that SHA) still holds.
- **The conditions for that skip:**
  - The push moved main from its last green commit (`before` = that
    commit). Every file is then either one the pull request touched (green
    on these exact files) or one that commit holds (green there).
  - The marker comes from this repository's own run (a fork's pull request
    runs the fork's ci.yml), from a `ci.yml` run that concluded success.
  - Main moved meanwhile, or a merge produced other files: the trees differ
    and the usual filtered run happens.
- **`passed` must not be a required check:** GitHub counts a skipped
  required check as passing, and `passed` is skipped when a job fails.
- **Caches:**
  - `cpui/node_modules`, keyed on the lockfile: `npm ci` runs only when
    the lockfile changed (35–45 s a UI job before).
  - go-test and go-lint keep their own Go build cache. It is saved from main
    once a week (the key carries the ISO week), so at most two new entries
    a week compete with the image layers for the 10 GB. setup-go's own cache
    was keyed on go.sum alone: saved once per dependency change by whichever
    job ended first, and never refreshed. Whether it is worth it shows in
    go-lint's time on the second run of a week.
- **pr-title no longer runs on every push** (`synchronize`): a push can't
  change the title, and each run billed a minute. If pr-title becomes a
  required check, `synchronize` has to come back (a required check must
  report on the head commit).
- **Every action is pinned to a commit**, with its version as a comment;
  `.github/dependabot.yml` proposes updates monthly, grouped into one pull
  request titled `ci(deps): …`.
- **With manual-release** (checked the same day):
  - Its gate asks for a successful `ci.yml` run on the release SHA. A main
    run whose tests and lints the marker skipped is one, since a skipped job
    counts as passed.
  - A release commit (`chore(release): vX [skip ci]`, CHANGELOG.md only)
    gets no ci run, so main's last green commit stays the one before it.
    The shortcut therefore accepts a gap between that commit and the push's
    `before` when it changes only CHANGELOG.md, which no check reads.
    Otherwise the first merge after every release would always run in full.
  - The release builds its images from main's Actions cache, which a run on
    main can't fill from a pull request's. So a skipped merge still runs
    `sandbox-images` and `engine-image` when their inputs changed (outputs
    `images`, `engine`); only the tests and lints are skipped.
  - Simulated on a scratch repository with a stand-in GitHub API: a merge on
    the green commit or after a release commit, its tree marked, skips; no
    marker, a non-green commit in between, or a previous head missing from
    history (a force-push) runs as usual.
- **Node 24 actions, a pinned runner image** (same day, from the run's
  annotations):
  - Every action moved to its newest release, which runs on Node 24.
    GitHub deprecated Node 20 and was forcing those actions onto Node 24
    with a warning.
    - checkout 7, setup-go 7, setup-node 7, upload-artifact 7, cache 6,
      setup-task 3, setup-helm 5, ghaction-github-runtime 4,
      setup-buildx-action 4, paths-filter 4.
    - Their breaking changes don't touch this repository's use. checkout 6
      keeps credentials in a file of their own, and manual-release's `git
      push` still finds them. setup-go 6 reads the toolchain from go.mod as
      before. setup-node's automatic npm cache is turned off explicitly:
      `node_modules` is cached by the workflow itself.
  - `runs-on: ubuntu-26.04`, not `ubuntu-latest`. During GitHub's move of
    `ubuntu-latest` to 26.04, a job could land on either image. The disk
    figures in the sandbox jobs were measured on 26.04.
  - `.github/actionlint.yaml` declares that label (actionlint 1.7.12 does
    not know it yet), and `repo:lint` now runs actionlint on the workflows.
- **The first run on GitHub** (same day): three jobs failed.
  - `repo-lint`: the runner has shellcheck, so actionlint ran it there; this
    machine has none. It flagged the background disk-freeing step's
    `sh -c '…$RUNNER_TEMP…'`. That step is now a plain background subshell,
    and the workflows were checked with shellcheck's image.
  - `ui-test`: a vitest worker died mid-suite ("Worker exited
    unexpectedly"). A private repository's runner has 2 vCPUs and about
    7 GB, not the 4 and 16 assumed above. vitest recycles a vmThreads worker
    past RAM ÷ workers by default (about 3.5 GB there), above the ~2 GB heap
    a worker gets, while a VM context keeps memory a file left behind.
    `vmMemoryLimit: '1GB'` recycles workers well before that. On 2 cores
    here, with workers capped at a 2 GB heap, the run passes and takes 236 s
    instead of 300 s: smaller heaps collect faster. The crash itself was
    not reproduced locally: running Node 22, CI's version, was not possible
    here.
  - `sandbox-test (k8s)`: `TestLiveK8sBrowserSession` failed as it does on
    this machine: no pod reached the test process over host.k3d.internal or
    host.docker.internal, and the probe printed nothing, so DNS and the
    firewall were indistinguishable. The probe now logs what each name
    resolved to and whether it connected. On a failure the test prints
    CoreDNS's NodeHosts, and it no longer waits out 3 minutes once the probe
    has ended. The cluster network's gateway, which needs no DNS, is now a
    third candidate (APOTHECARY_LIVE_HOST_GATEWAY, from `docker network
    inspect`). It passed on main with the old flow, which pushed to the
    registry and restarted dockerd; whether one of those was what made it
    pass is open until the next run's probe log.
