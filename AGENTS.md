# Working on Apothecary

Apothecary runs AI-agent runs on a repository inside a sandboxed workspace.
This file is for an agent working on Apothecary itself — including a run of
Apothecary on this repo (README, "Developing Apothecary with Apothecary").

## What this is

- Go engine: `cmd/apothecaryd` (wiring, CLI), `internal/` (grouped by what a
  package is about — see the README's "Development" table), `workspace/`
  (the in-workspace shim, the egress gateway and the tools-bundle sources,
  `go:embed`ed into the daemon). `go test ./internal/archtest` enforces who
  may import whom. Kubernetes is the only sandbox host.
- Clients: the web UI, `cpui/` (SvelteKit, static build served by the
  daemon); the terminal client, `cmd/apothecary` + `internal/client` (talks
  to the engine over its API only; archtest holds it to that).
- Deployment: the Helm chart, `charts/apothecary` (`ci/*-values.yaml` are
  its tested examples; `task chart:docs` regenerates its README).
- `Taskfile.yml` holds every dev command; `task --list` shows them.
- Design decisions and their deviations are appended to
  `docs/IMPLEMENTATION_NOTES.md` (newest at the bottom); `docs/flow-language.md`
  is the flow spec; `docs/frontend-standards.md` the UI rules.

## Run these (repo root, inside the devcontainer)

The devcontainer has exactly CI's tools; so must anywhere else a check runs.
One version each, pinned once:

| Tool | Pinned in | Outside the devcontainer |
|---|---|---|
| Go | `go.mod` (`go` line) | automatic: the Taskfile sets `GOTOOLCHAIN` to it |
| Node and npm | `.nvmrc`; npm is the one that Node ships (`cpui/package.json` engines) | `fnm use` / `nvm use` |
| helm, task, shellcheck, k3d, kubectl | `.devcontainer/Dockerfile` `ARG`s | install those versions |

`task` refuses a check on any other version (the `pin:*` tasks), and
`npm ci` refuses another Node or npm (`cpui/.npmrc` engine-strict). A
version bump changes its pin, and `go test ./internal/devtools/...` names
every copy that must follow (the CI workflows, the engine image, the
devcontainer).


| When | Command |
|---|---|
| before pushing, always | `task fmt`, then `task check`: every lint, the Go tests (Postgres included) and the CPUI tests, in parallel, under their coverage floors; logs in `build/checks/` |
| one check | the task named after its CI job: `task go:test`, `go:lint`, `ui:test`, `ui:lint`, `repo:lint` (go-test is `task go:test`, and so on) |
| one package | `task go:test:run -- ./internal/x -run TestY -v` (nothing running; Postgres-backed tests skip) |
| Postgres-backed tests | `task go:test:pg` (throwaway `postgres:16` containers on the run's docker daemon — the project's *Docker in workspaces* setting) |
| one CPUI test | `task ui:test:run -- src/lib/x` |
| coverage | `task go:test` holds the 95% floor (`cmd/coverreport`), `task ui:test` the floors in `cpui/vite.config.ts` (lines 99%); they only ever rise |
| you changed `internal/httpapi/routes.go` | `task api` (regenerates `api/openapi.yaml` + `cpui/src/lib/api/generated`; a stale spec fails a unit test) |
| bring the app up | `task dev:up` (below) |

## The heavy suites

`task ci` (and `test`, `sandbox:*`, `engine:image`, `k8s:*`) build
multi-gigabyte images and start a k3d cluster: about 25 GB of disk and ten
minutes or more. They run in a workspace with docker (the project's *Docker
in workspaces*), the devcontainer included. While you iterate, run `task
check`; leave `task ci` to the stage that verifies the run, unless your
change is to the images, the sandbox or the Kubernetes provisioner.

Never run `build`, `build:images` or `deploy/images.sh … --push`: they
build every platform's binaries or publish images.

## Hard rules

- **No model id anywhere** — code, flows, tests, docs, this file. `task
  repo:lint` fails the tree on one, and there is no exemption marker. Models are bound per project in the UI.
- **Conventional commits**: `<type>(<scope>)!: subject`, a blank line before
  any body; types build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test.
  The lefthook `commit-msg` hook (installed by `npm ci` in `cpui`) rejects
  anything else; so does the PR-title check.
- **Vocabulary**: "repository", never "forge"; "sequence", never "block";
  flow grammar words are `verdict`, `yes_no`, `run_if` (`docs/flow-language.md`).
- **Migrations**: `internal/adapters/pgstore/migrations/0001_init.sql` is
  frozen. A schema change is a new numbered file, never a rewrite.
- **Postgres tests** go through `internal/adapters/pgstore/pgtest` (a
  database per test on a throwaway server), never a fixed dev port or the
  dev database.
- **Frontend** follows `docs/frontend-standards.md`: a test beside every
  component or module, role tokens only, the `check-*` scripts in `cpui/scripts`.
- Only `build/` receives build output: a stray binary under `workspace/`
  would ship inside the daemon.

## Bringing the app up

`task dev:up` builds the UI, then runs the daemon from source with fake
kinds and a seeded dev project, API + UI on `http://localhost:18080`,
against the devcontainer's `db` sibling (`APOTHECARY_DEV_DSN`). It writes a
master key into `deploy/.env` (gitignored) when there is none. The first
visit shows "Claim this apothecary": create the first account (throwaway
database). `GET /healthz` is the liveness probe. `task dev` is the API
alone, for UI work with `cd cpui && npm run dev`.

## Pointers

`README.md` · `docs/flow-language.md` · `docs/frontend-standards.md` ·
`docs/IMPLEMENTATION_NOTES.md` · `.github/workflows/ci.yml` (what a release
needs green).
