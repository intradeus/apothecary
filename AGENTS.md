# Working on Apothecary

Apothecary runs AI-agent runs on a repository inside a sandboxed workspace.
This file is for an agent working on Apothecary itself — including a run of
Apothecary on this repo (README, "Developing Apothecary with Apothecary").

## What this is

- Go engine: `cmd/apothecaryd` (wiring, CLI), `internal/` (grouped by what a
  package is about — see the README's "Development" table), `workspace/`
  (the in-workspace shim, the egress gateway and the tools-bundle sources,
  `go:embed`ed into the daemon). `go test ./internal/archtest` enforces who
  may import whom.
- UI: `cpui/` (SvelteKit, static build served by the daemon).
- `Taskfile.yml` holds every dev command; `task --list` shows them.
- Design decisions and their deviations are appended to
  `docs/IMPLEMENTATION_NOTES.md` (newest at the bottom); `docs/flow-language.md`
  is the flow spec; `docs/frontend-standards.md` the UI rules.

## Run these (repo root, inside the devcontainer)

| When | Command |
|---|---|
| before pushing, always | `task fmt`, then `task lint` (gofmt, go vet with every build tag on linux+darwin, model ids, API contract, svelte-check + the CPUI linters, helm lint) |
| unit tests | `task test:unit` (Go + vitest; nothing running needed) |
| Postgres-backed tests | `task test:integration:pg` (throwaway `postgres:16` containers on the run's docker daemon — the project's *Docker in workspaces* setting) |
| you changed Go | `task cover:go` — the 95% floor (`cmd/coverreport`) only ever rises |
| you changed `internal/httpapi/routes.go` | `task api` (regenerates `api/openapi.yaml` + `cpui/src/lib/api/generated`; a stale spec fails a unit test) |
| one package | `task test:unit:go -- ./internal/x -run TestY -v` |
| bring the app up | `task dev:up` (below) |

## Never run in a workspace

`task test:integration:docker`, `test:integration:harness`,
`test:integration:k8s`, `test:integration`, `test`, `verify`, `build`,
`build:images`, `local*`, `k8s:*`, `prod:*`, `deploy/images.sh`: they build
multi-gigabyte images, nest sandboxes or start a k3d cluster. CI runs them.

## Hard rules

- **No model id anywhere** — code, flows, tests, docs, this file. `task
  lint:models` fails the tree on one. Models are bound per project in the UI.
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
