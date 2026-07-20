# ⚗ Apothecary

A self-hosted software factory: **a ticket goes in, a reviewed merge
request comes out.** AI agents and deterministic gates alternate along a
flow you define, inside disposable sandboxed workspaces, with humans at
exactly two touchpoints — plan approval and final review.

- **Configured entirely in the UI.** Forge accounts, workspace
  provisioning, the coding agent and its model auth, projects, flows —
  all live in the database, encrypted where secret. The daemon boots from
  five environment variables.
- **Vendors behind ports.** GitLab or GitHub, opencode or codex, Coder
  workspaces — each is an adapter; swapping one is a dropdown, not a
  refactor.
- **Events are the source of truth.** Every transition is an event in
  Postgres; the UI is a projection; analytics are queries.
- **Sandboxed by default.** Agents run in per-run containers behind a
  default-deny egress firewall derived from your configuration, and prove
  it with a deterministic environment check.

## Prerequisites

- A Linux host with **Docker** (the reference deployment uses host
  networking and the docker socket).
- A **[Coder](https://coder.com) deployment** the host can reach (a local
  `docker compose` Coder works fine) and a session token for it.
- A **forge account**: GitLab PAT (`api` scope, Developer+ on the target
  project) or GitHub PAT (`repo` scope, or fine-grained Contents:RW +
  Pull requests:RW).
- **Model auth for your coding agent**: an opencode `auth.json` (API keys
  and/or subscription login) or, for codex, an OpenAI API key / ChatGPT
  `auth.json`.
- A target repository containing `AGENTS.md` (the repo's conventions).
  Agents themselves are defined **per project in the UI** (v3 agent
  library) — `examples/agents/` has starter specs, and a legacy
  `.apothecary/agents/` directory can be imported with one click on the
  project's Agents panel.

## Install

```sh
git clone <this repo> && cd apothecary
cp deploy/env.example deploy/.env
docker run --rm ghcr.io/intradeus/apothecary/apothecaryd:latest keygen
#  -> paste the output into deploy/.env as APOTHECARY_MASTER_KEY,
#     and set APOTHECARY_PG_PASSWORD to anything strong
docker compose -p apothecary -f deploy/docker-compose.yaml up -d
```

(keygen runs via plain `docker run`: compose refuses to start anything —
including keygen itself — until the key it generates is set.)

Open **http://127.0.0.1:8080**. The stack is apothecaryd (engine + UI, bound
to the host's loopback) and Postgres 16 on `127.0.0.1:15432`; both restart
automatically. Database migrations apply at boot.

> The docker socket mount makes the engine part of the trusted control
> plane (it builds workspace images on your daemon — the same one Coder
> uses). Workspaces themselves never see the socket.

## Configure (all in the UI)

1. **Integrations** — add and **Test** each:
   - *Forge*: GitLab or GitHub + token.
   - *Provisioner*: Coder URL + session token (+ template name, default
     `apothecary` — the engine manages the template's content for you).
   - *Harness*: opencode (paste your `auth.json` — it carries ALL model
     credentials) or codex (API key and/or ChatGPT `auth.json`).
2. **Flows** — create one (a starter skeleton is offered; the editor
   validates with line-precise errors, and the *Available models* panel
   lists the exact `provider/model` strings your harness can route).
3. **Projects** — New project: pick the forge → *Browse repositories* →
   set commands (`setup` is optional; `verify` is what test stages run) →
   pick provisioner + harness → add toolchain **presets** to the
   Dockerfile fragment (Go, Python, Node, JVM, C toolchain) → default
   flow.
4. **Preflight**, then **Envcheck** on the project page. Envcheck
   provisions a throwaway workspace and proves the whole contract —
   image, firewall (default-deny, fail-closed), derived egress, harness
   binary, ask-human shim, reverse tunnel — with named pass/fail steps.
5. **File a ticket** on the Runs page. Answer the plan gate in the Inbox;
   merge the draft MR/PR at the end (the run completes itself when you
   merge).

Settings (concurrency, environment caps, reaper) live under *Settings*
and apply without a restart. Notification mirrors (Matrix) live under
*Channels*.

## Releases & upgrade

**Every push to `main` releases automatically** (`.github/workflows/
release.yml`): tests run, then a version-stamped image
(`v2.0.<run>`) is pushed to
`ghcr.io/intradeus/apothecary/apothecaryd` (also `:latest` and `:sha-…`)
and a matching git tag is created.

Compose runs `:latest` by default. To upgrade:

```sh
docker compose -p apothecary -f deploy/docker-compose.yaml pull apothecaryd
docker compose -p apothecary -f deploy/docker-compose.yaml up -d
```

To stay on a fixed version instead, pin it in `deploy/.env`:

```sh
# deploy/.env
APOTHECARY_IMAGE=ghcr.io/intradeus/apothecary/apothecaryd:v2.0.42
```

(If the GHCR package is private: `docker login ghcr.io` with a
`read:packages` PAT first, or make the package public in its settings.)

Migrations apply at boot, and the workspace base image and Coder template
are content-addressed — they rebuild/re-push themselves on first use after
their sources change.

To run an **uncommitted tree** as the real containerized service, use the
local-build stack — it builds from the working tree and runs beside the
published-image stack (own ports: engine `127.0.0.1:8081`, postgres
`127.0.0.1:15434`; own volumes; same `deploy/.env` secrets):

```sh
docker compose -p apothecary-local -f deploy/docker-compose.local.yaml up -d --build
```

Alternatives: `deploy/release.sh vX` builds a pinnable versioned image for
the main stack (`APOTHECARY_IMAGE=` in `deploy/.env`), and
`go run ./cmd/apothecaryd serve -dev` skips docker entirely (fake kinds,
seeded sandbox project).

## CLI

Everything ships in the image (`docker compose -p apothecary -f
deploy/docker-compose.yaml exec apothecaryd <cmd>`, or `run --rm` for
one-offs):

| command | purpose |
|---|---|
| `apothecaryd serve [-dev]` | the engine (`-dev`: fake adapters + seeded sandbox project) |
| `apothecaryd keygen` | generate `APOTHECARY_MASTER_KEY` |
| `apothecaryd envcheck -project <name> [-keep]` | the compliance gate, from the terminal |
| `apothecaryd validate <flow.yaml>` | check a flow file before pasting it into the UI |
| `apothecaryd rebuild-projections` | rebuild read models from the event log |
| `apothecaryd version` | build version |

Boot environment (the whole deployment plane): `APOTHECARY_POSTGRES_DSN`,
`APOTHECARY_MASTER_KEY` (required); `APOTHECARY_LISTEN` (default
`127.0.0.1:8080`), `APOTHECARY_RUNS_DIR`, `APOTHECARY_CPUI_DIR`,
`APOTHECARY_OTEL_ENDPOINT`, `APOTHECARY_BASE_IMAGE` (optional).

## Development

```sh
go test ./...                      # unit + scenario suites (in-memory fakes)
go run ./cmd/apothecaryd serve -dev   # real engine/API/UI, fake vendors
cd cpui && npm run dev             # UI dev server (proxies /api)
```

Design documents live in `docs/` — `DESIGN.md` (v1 engine),
`DESIGN2.md` (config plane & distribution), with every deviation recorded
in `docs/IMPLEMENTATION_NOTES.md`.

## Security model (short form)

Engine and host are trusted; **workspaces are not** (prompt injection is
assumed). Workspaces get: no docker socket, a per-run docker network, a
default-deny egress firewall pinned to the derived allowlist
(fail-closed), a per-run bearer token scoped to their own ping endpoints,
and secrets injected as files at bootstrap — never through provisioner
parameters. Integration secrets are secretbox-encrypted at rest under
`APOTHECARY_MASTER_KEY` and never leave the engine unmasked. Residual risks
and their rationale: `docs/IMPLEMENTATION_NOTES.md`.
