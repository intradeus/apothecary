# CI runs per side of the tree, main included; vitest shares one jsdom per worker (2026-11-12)

What a push to main used to cost, measured from its job logs: about 59
billed minutes (unit 10, lint 6, build 11, docker 11, harness 6, k8s 14,
pg 1).

- **The CPUI's coverage run was 8 of unit's 10 minutes.** vitest built a
  jsdom for each of the 318 test files: 152 s of 470 s.
  - `pool: 'vmThreads'` builds one per worker and still gives each test file
    its own context. On 4 cores it went from 235 s to 139 s, with the same
    tests and coverage.
  - `isolate: false` was slower (370 s) and broke every file that mocks a
    module.
  - The price: inside a VM context Node's globals belong to another realm.
    `structuredClone` returned objects Svelte would not make reactive, so
    PanelEditor's draft no longer used it; `$state.snapshot` is already a
    copy. jsdom's `location` cannot be stubbed there, so
    `workspace.terminalUrl` takes the page's location as an argument.
- **Jobs per side of the tree:**
  - Named `<side>-<check>`, the side being when the job runs: `go-test`,
    `go-lint`, `ui-test`, `ui-lint`, `sandbox-images`, `sandbox-test`
    (harness, k8s), `engine-image`, and `repo-lint` (model ids and the chart,
    which read Markdown), which always runs. CI's old "unit" job ran the
    Postgres tests too: the Taskfile's unit/integration split is about what a
    test needs on your machine, not what a CI job holds.
  - `integration-pg` is gone: `task go:test` already runs every Postgres
    test.
  - The build job is gone: `go-lint` builds the binaries, `ui-lint` the CPUI.
    `engine-image` runs when its build inputs change (its Dockerfile
    compiles both again).
- **Main is filtered too, against the last commit ci passed on main**, not
  against the previous commit.
  - The filter must not compare with the previous commit: a docs commit on
    top of a red code commit would go green, and manual-release would
    release it.
  - Comparing with the last green ancestor means that a green commit has
    had everything changed since the previous green one tested.
  - No green ancestor (a force-push, the first run), or a run by hand:
    everything runs.
  - This replaces "pushes to main run everything" in the entry above.
- **Lint:**
  - The plain Linux vet is gone: no file is built only without a tag, so
    the tagged vet covers it.
  - The client is now vetted for Windows. Nothing compiled it before the
    release, which built it after pushing the tag; the release now builds the
    client before anything is public.
- **The release builds its images from ci's Actions cache** (`gha-read`). A
  run on main reads only caches main wrote, never a pull request's.
- **Every job has a `timeout-minutes`** (GitHub's default is six hours).
- **Freeing disk (1.5 minutes per job) only where it is needed.**
  - The `sandbox-images` job keeps only the builder's cache (about 6 GB), and the
    harness suite needs about 10 GB: both now run on the runner's free disk
    as it is.
  - The k8s suite peaks at about 15 GB: its images are in docker and,
    during the import, in the k3d node too. It still deletes the unused
    toolchains, but in the background while the images load, and waits for
    the deletion only before the cluster starts.
  - These sizes were measured locally. Each job prints `df` first, so the
    first GitHub run shows the runner's real headroom.
