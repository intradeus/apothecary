# Every CI job is one task of the same name; check and ci run them in parallel (2026-11-12)

- **CI's job names and the Taskfile's task names used to differ.**
  - CI's "unit" job ran the Postgres tests too, while the Taskfile's
    `test:unit` meant "nothing running".
  - The CPUI's coverage run had no task at all.
- **Now each job `<side>-<check>` runs exactly `task <side>:<check>`:**
  - `go:test` (was `cover:go`), `go:lint` (gofmt, vet, `api:check`),
    `ui:test`, `ui:lint` (the production build included: a prerender error
    shows only there), `repo:lint` (model ids, `chart:lint`).
  - `sandbox:images`, `sandbox:test:harness`, `sandbox:test:k8s`,
    `engine:image`.
  - The variants keep the side: `go:test:run` (args, nothing running),
    `go:test:pg`, `go:test:linux`, `go:test:stress`, `ui:test:run`.
  - Gone, with no aliases: `test:unit*`, `test:integration*`, `cover*`,
    `lint:*`, and the old `verify` (the pre-commit gate `check` replaces).
- **The umbrellas run checks side by side** (`checks`, a bash runner), each
  into `build/checks/<task>.log`. They print one line per check (ok or
  FAIL, seconds, log path), then the last 40 lines of each failure, and exit
  1 if any failed.
  - `lint` runs every lint; `test` runs every test suite.
  - `check` is the pre-push gate: the lints, `go:test` and `ui:test`.
  - `ci` runs everything CI runs, locally (it never skips a side as CI
    does). It was first named `verify`, too close to `check` to tell apart.
- **What the runner holds apart:**
  - `ui:test` and `ui:lint` both write `cpui/.svelte-kit`, so they run one
    after the other (`ui:lint` still runs when `ui:test` fails).
  - The sandbox suites wait for `sandbox:images`, then run side by side, k8s
    with `APOTHECARY_IMAGES_PREBUILT=1`.
  - Ctrl-C stops them all.
  - Measured: `task check` in 2m36 on 20 cores, every check green; the Go
    tests stayed green beside vitest and the lints.
- **No exemption marker for the model-id lint.** `lint:models-ok` is gone
  at the user's call.
  - Its one user, the Gemini sign-in's User-Agent, now drops the model
    segment Gemini CLI's own carries:
    `GeminiCLI/<version> (linux; x64; terminal)`.
  - Unverified against Code Assist's project lookup, the only call that
    sends it.
