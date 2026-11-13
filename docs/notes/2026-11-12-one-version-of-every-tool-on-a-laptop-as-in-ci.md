# One version of every tool, on a laptop as in CI (2026-11-12)

- **Why:** two CI-only failures in a row came from differences between the
  laptop and CI, not from the code.
  - The vitest worker crash: a 2-core, 7 GB runner against a 20-core laptop.
  - The dagre failure: CI's Node 22 loaded dagre's ES module as CommonJS
    under the VM pool, where the laptop's Node 24 did not. Reproduced in a
    `node:22` container.
  - Before those, shellcheck: CI's runner image had it, the laptop did not,
    so actionlint checked more in CI.
  - Also different: Go (1.27.2 on the laptop, 1.26.4 in CI), helm (4.3.0
    and 3.16.2), task (3.49.1, 3.54.0 in the devcontainer, "3.x" in CI).
- **Now every tool has one pin, and every place takes it from there:**

  | Tool | Pin | CI | Devcontainer | Laptop |
  |---|---|---|---|---|
  | Go 1.26.4 | `go.mod` | setup-go | `golang:1.26.4` | Taskfile `GOTOOLCHAIN` (Go fetches it) |
  | Node 26.13.1 (LTS) | `.nvmrc` | setup-node's `node-version-file` | `node:26.13.1` | `fnm use` |
  | npm 11.20.0 | `cpui/package.json` engines (the npm of that Node) | with Node | with Node | with Node |
  | helm 4.3.0, task 3.54.0, shellcheck 0.11.0 | `.devcontainer/Dockerfile` ARGs | installed exactly | installed | installed by hand |

  The engine image builds the CPUI on `node:26.13.1-bookworm-slim` too. The
  Taskfile's two `golang` images read `go.mod` instead of a copy of it.
- **Enforced three ways:**
  - `cpui/.npmrc` engine-strict: `npm ci` refuses another Node or npm.
  - `pin:task`, `pin:node`, `pin:helm` and `pin:shellcheck` are deps of the
    checks that need them, so a check refuses to run on another version and
    says what to install.
  - `internal/devtools/devcontainer` (TestOneNodeEverywhere,
    TestToolPinsMatchCI) holds every copy to its pin: the workflows, both
    Dockerfiles, package.json and the Taskfile.
- **Node 26 rather than 24:** 26.13.1 became LTS on 2026-11-10, no
  dependency's engines refuse it, and `@types/node` was already 26. Helm
  moved to 4 the same way: the chart lints and renders identically under it.
- **npm 11.20 runs no install script unless approved.** esbuild and
  lefthook (the commit hooks) are approved in package.json's
  `allowScripts`, for their exact versions; a version bump needs approving
  again. Node 26 ships no corepack, so the devcontainer's symlink to it went.
- **Machine size is the one difference left:** CI's runner has 2 vCPUs and
  about 7 GB. `docker run --cpuset-cpus=0,1 --memory=7g` reproduces it, and
  the whole CPUI suite passed that way on node:26.13.1. `--cpus=2` does not:
  `nproc` still reports every core, so vitest starts 20 workers.
- **The dagre `server.deps.inline` workaround is gone:** Node 26 loads it.
