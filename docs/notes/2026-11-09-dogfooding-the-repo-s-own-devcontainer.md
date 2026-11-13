# Dogfooding: the repo's own devcontainer (2026-11-09)

Apothecary now runs on its own repository (README, "Developing Apothecary
with Apothecary"). What the repo gained:

- `AGENTS.md` (Preflight requires it) — the working guide for an agent on
  this repo: what to run, what never to run in a workspace, the hard rules.
  `lint:models` scans it too. (No `CLAUDE.md` alias: deliberately, AGENTS.md
  is the one file.)
- `.devcontainer/`: compose-based — `app` built from
  `.devcontainer/Dockerfile` (`golang:<go.mod>-bookworm`, Node from the
  official image by `COPY --from`, helm from `get.helm.sh` with its published
  checksum, go-task by `go install`, a non-root `dev`, the Go module cache
  pre-warmed from `go.mod`/`go.sum` so the gateway's build cache makes a run's
  first build fast) beside a `postgres:16` `db`; no docker feature of its own
  — in a run the project's "Docker in workspaces" setting injects it (a guard
  test keeps it that way, so it is never installed twice), in VS Code a dev
  adds the feature or runs the pg suite on the host. It mounts `..` relative
  to `.devcontainer/` — the
  checkout, and never `../..`, which in a run is the run folder with its
  sockets; the CLI does not expand `${localWorkspaceFolder}` in compose
  files (docker compose sees an empty variable), so the relative path is the
  one spelling that works everywhere. `cpui/node_modules` is a named volume
  (the directory pre-created in the image, owned by `dev`): the container's
  `npm ci` must not delete and rebuild the host's — the first local run did
  exactly that under a host-side vitest and broke both — and a mac's native
  binaries would not run in the container anyway. `init: true` and a tmpfs
  `/tmp` for the same reasons as `task test:linux`. Hooks: `go mod
  download`, `npm ci` (which installs the lefthook commit hook).
- `Taskfile.yml`: `DEV_DSN` honors `APOTHECARY_DEV_DSN` (compose sets it to
  the `db` sibling; `dev:db` is then skipped); `dev:app` (built UI + daemon
  on :18080), `dev:key` (a master key into the gitignored `deploy/.env` when
  missing), `dev:up` (both, in a fresh task process so the dotenv just
  written is read) — the project's `default` service.
- Guards: `internal/devtools/devcontainer` pins the Dockerfile's Go, Node and
  helm to go.mod, `deploy/Dockerfile` and CI, checks the compose mount, the
  feature and that AGENTS.md exists; `pgtest`'s image test covers the compose
  `db`.
- Egress for the project: the offered defaults plus `get.helm.sh`.
