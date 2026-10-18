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
- **Sandboxed by default.** Agents run in per-run Docker-in-Docker
  sandboxes whose entire egress (the workspace AND any compose siblings)
  is filtered by an external per-project gateway — a default-deny
  allowlist (or allow-all denylist) derived from your configuration,
  hot-reloadable and proven with a deterministic environment check.
- **Live agent sessions.** The coding agent runs as a long-lived session
  inside the workspace, driven over the Agent Client Protocol: you watch
  its conversation and tool calls as they happen, message it mid-run,
  browse its files and diff, and open a terminal — and its questions to
  you simply wait for your answer.

## Prerequisites

- A Linux host with **Docker** (the reference deployment uses host
  networking and the docker socket). The devcontainer provisioner runs each
  run's workspace inside its own **Docker-in-Docker sandbox** (a
  `docker:dind` container), so repo compose files run verbatim with no
  cross-run collisions and teardown is total. By default that sandbox is
  `--privileged` — see [Unprivileged sandboxes](#unprivileged-sandboxes-optional)
  to drop that. Its `RunsDir` must be a **persistent path, not under `/tmp`**
  (dind masks `/tmp`) — the default `/var/lib/apothecary/runs` is fine.
- A **[Coder](https://coder.com) deployment** the host can reach (a local
  `docker compose` Coder works fine) and a **long-lived API token** for it
  (see [Configure](#configure-all-in-the-ui) — a `coder login` session token
  works but expires, silently stalling provisioning).
- A **forge account**: GitLab PAT (`api` scope, Developer+ on the target
  project) or GitHub PAT (`repo` scope, or fine-grained Contents:RW +
  Pull requests:RW).
- **Model auth for your coding agent**: an opencode `auth.json` (API keys
  and/or subscription login) or, for codex, an OpenAI API key / ChatGPT
  `auth.json`.
- A target repository containing `AGENTS.md` (the repo's conventions).
  Agents themselves are defined **per project in the UI** (v3 agent
  library) — five defaults (planner, builder, committer, reviewer,
  redactor) ship compiled into the binary and appear in every project;
  saving one under the same name creates your editable override.

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
   - *Provisioner*: Coder URL + **API token** (+ template name, default
     `apothecary` — the engine manages the template's content for you).

     > **Use a long-lived API token, not a login session token.** The engine
     > holds this token and drives the `coder` CLI with it for the life of the
     > deployment. A token from `coder login` is a *session* token governed by
     > the deployment's session-duration setting (24h by default): once it
     > expires, provisioning fails and existing workspaces show as
     > **`workspace?`** (status unknown) in the UI. Instead mint a dedicated
     > token:
     >
     > ```sh
     > coder tokens create --name apothecary --lifetime 1y
     > ```
     >
     > Paste that into the *API token* field and **Test**. Its lifetime is
     > capped by the deployment's `CODER_MAX_TOKEN_LIFETIME` (a Coder admin
     > setting) — raise that server-side if you need longer than the default.
     > Rotate by creating a new token and updating the integration; the old
     > one can then be revoked with `coder tokens remove <name>`.
   - *Harness*: opencode (paste your `auth.json` — it carries ALL model
     credentials) or codex (API key and/or ChatGPT `auth.json`).
   - *Ticket source* (optional): Jira. Lets you create a ticket **from** a
     Jira issue — search the tracker on the New Ticket form, pick an issue,
     and its summary, description and attachments are copied in. Read-only:
     nothing is ever written back, and the copy is taken once (edit the
     apothecary ticket freely afterwards).

     > **Which Jira?** *Cloud* is the Atlassian-hosted one, at
     > `<site>.atlassian.net` — credential is an **account email + API
     > token**, API version **3**. *Data Center* (and the retired *Server*)
     > is self-hosted on your own domain — credential is a **personal access
     > token** with the email field left **empty**, API version **2**. Both
     > are the same form; the email field is what selects between basic and
     > bearer auth.
     >
     > *Cloud token:* sign in as the account whose issues you want to see →
     > <https://id.atlassian.com/manage-profile/security/api-tokens> →
     > **Create API token** — the plain, **unscoped** one. (A token made with
     > *Create API token with scopes* is only accepted on Atlassian's
     > `api.atlassian.com` gateway; against your `<site>.atlassian.net` URL it
     > returns `401 Client must be authenticated to access this resource`.)
     > An unscoped token is no wider than the human: it inherits that
     > account's permissions, and this integration only ever issues the four
     > reads listed below. If you do scope one (for the gateway), the read
     > scopes are `read:jira-user` and `read:jira-work` — those two are
     > exactly enough. If your site's picker shows granular scopes instead, the set
     > that matches what this integration reads is `read:user:jira`
     > (`/myself` + the assignee name), `read:issue-details:jira`,
     > `read:issue:jira`, `read:issue-meta:jira` (the issue), `read:field:jira`,
     > `read:status:jira`, `read:issue-type:jira` (the requested fields),
     > `read:project:jira` (key resolution + `project = …` filters) and
     > `read:attachment:jira` (the files). Atlassian also documents
     > `read:avatar:jira` and `read:group:jira` on those endpoints because the
     > *responses* carry avatar URLs and group memberships — nothing here
     > reads either, so start without them and add only what a refused call
     > names (Jira's message is surfaced verbatim). Never grant `write:`,
     > `manage:` or `delete:` — no code path here writes. Label + expiry →
     > copy it (shown once) and paste it with that account's email. Revoke on
     > the same page.
     >
     > *Data Center token:* profile menu → **Profile → Personal Access
     > Tokens** → **Create token** → label + expiry → copy. Paste into *API
     > token*, leave *Account email* empty. DC PATs have **no scope picker** —
     > the token carries the full permissions of the account that minted it,
     > so use a service account for least privilege. (If your admin disabled
     > PATs, a username + password in the email/token fields works too —
     > that path is plain basic auth.)
     >
     > Scopes only cap a token; they grant nothing. Either way the account
     > needs Jira's **Browse Projects** permission on the projects you import
     > from, and the token can never see more than that human can. The four
     > calls this integration makes, and nothing else:
     > `GET /myself` (the Test button), `POST /search/jql` (the picker's
     > search, falling back to `GET /search` on older sites),
     > `GET /issue/{KEY}?fields=…,attachment` (preview + import), and a `GET`
     > of each attachment's content URL.
     >
     > Optionally set a **JQL search filter** (`project = PROJ`); it is ANDed
     > into every search so the picker only shows issues you care about — it
     > narrows what you see and cannot widen it. Then **Test**.
     >
     > The New Ticket search box takes a **bare number** (`2624` — resolved as
     > `<PROJECT>-2624` against the filter's project, or against your most
     > recently active projects when no filter is set), a **key**
     > (`PROJ-2624`, also `proj 2624`), free **words** (text search), or
     > `jql:…` for raw JQL.
2. **Flows** — create one (copy the built-in *default* template or start
   from the skeleton; the editor validates with line-precise errors, and
   the *Available models* panel lists the exact `provider/model` strings
   your harness can route).
3. **Projects** — New project: pick the forge → *Browse repositories* →
   set commands (`setup` is optional; `verify` is what test stages run) →
   pick provisioner + harness → add toolchain **presets** to the
   Dockerfile fragment (Go, Python, Node, JVM, C toolchain) → default
   flow. *Additional repositories* (optional) makes it a multi-repo
   project: each is cloned beside the primary on every run and published
   as its own merge request when changed; leave the locator empty for one
   the agent should only read. The primary's devcontainer must carry their
   toolchains.
4. **Preflight**, then **Envcheck** on the project page. Preflight checks
   the configuration without building anything — forge access, AGENTS.md,
   the devcontainer, the flow's references, and that every agent stage's
   model is one the harness's credentials can reach. Envcheck provisions a
   throwaway workspace and proves the whole contract — image, egress
   gateway (a non-allowlisted host is blocked; each derived domain
   reachable; wildcard entries are patterns and skipped), harness binary,
   that the agent **in the workspace** offers every model the flow pins,
   ask-human shim, engine tunnel — with named pass/fail steps. The verdict
   shows as soon as the probes finish (the workspace is torn down after);
   starting another envcheck cancels the running one.
5. **File a ticket** on the Tickets page — by hand, or imported from a
   configured ticket source. Answer the plan gate in the Inbox;
   merge the draft MR/PR at the end (the run completes itself when you
   merge — all of them, for a multi-repo project).

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

## How a run works

```
apothecaryd (engine: flow interpreter, event log, API + UI)
  │  unix sockets in the bind-mounted run folder — no container networking
  ▼
per-run Docker-in-Docker sandbox ── egress only via the project gateway
  └─ the repo's own devcontainer (the workspace)
       ├─ shim sidecar   one per workspace: HTTP on /opt/apothecary/sidecar.sock
       │    ├─ the coding agent, long-lived, spoken to in ACP
       │    │    (opencode acp · codex-acp) ── ask_human MCP server (shim)
       │    ├─ files + git views of the checkout
       │    └─ terminals (PTY over WebSocket)
       └─ shim relay / portscan   engine bridge, app port discovery
```

- **The engine drives every step**; the workspace never schedules work.
  Command stages are execs; an agent stage asks the workspace's sidecar to
  open (or resume) a session on the long-lived agent, sets the stage's
  agent and model, and prompts it. The sidecar normalizes the agent's
  updates — message text, tool calls with input/output/diffs, plans, usage —
  and the engine records them as the run's transcript.
- **The run page** has a *Chat* tab (the transcript, live; your messages to
  the agent), *Changes* (the run's diff against the branch it forked from),
  *Files* (the checkout, read-only), *Terminal* (a shell in the repo) and
  the raw *Timeline*.
- **Messaging the agent**: *Send* queues a message for the end of its
  current turn, *Send now* interrupts the turn so it reads it at once —
  either way into the same session.
- **ask_human blocks** until you answer (inbox, run page, or chat), with
  keep-alives so the harness never times the call out. If nobody answers
  within the live hold (30 min), the engine parks the run — cancels the
  turn, frees its slot — and resumes the same session with your answer.
- **Engine restarts don't restart agents.** The agent lives with the
  workspace; after a restart the engine follows the turn that was in flight
  (or the one that finished meanwhile) from the last event it recorded.
- **Models are checked early.** A model the provider no longer serves is
  refused when the run is created; one the installed harness doesn't offer
  fails the run before its first stage — not three stages in.
- **Several repositories**: a project can list additional repositories;
  a run clones each beside the primary, the agent sees them all, commit
  and push run in each, and every repository the run changed gets its own
  merge request. The run page's workspace views take a repository picker.
- **After the merge request opens, the run stays with it** (*in review*)
  until it merges (completed) or is closed (abandoned). The default flow
  reacts meanwhile, one thing at a time:
  - **conflicts** — when the target moves and the MR no longer merges, you
    get an inbox question; on *Run it* the engine merges the target in, an
    agent resolves any conflict by editing the files (never running git),
    and the result is tested and pushed (leased, never a blind force).
    *Not now* is remembered until the target moves again.
  - **review comments** — once reviewers have commented (inline, in a
    review, or in the conversation) and then gone quiet for an hour, you get
    a checklist of the pending threads and untick the ones not worth fixing
    (wrong, or already handled). The ticked ones are fixed in one batch,
    tested, pushed, and answered on their own threads. Unticked and answered
    threads don't come back unless a reviewer writes on them again.
  The run page's *In review* card shows each MR's mergeability and pending
  threads, and has a *Run now* button per phase.

The full design and its rationale: `docs/IMPLEMENTATION_NOTES.md`, sections
*Live agent sessions: the workspace sidecar and ACP*, *Several
repositories* and *Phases: the run's life after the merge request*; the
flow-side contract is in `docs/flow-language.md`.

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
assumed). The agent runs with its harness's approvals off — the sandbox is
the boundary, not the agent's permission prompts. Workspaces get: a per-run Docker-in-Docker sandbox on its own
`--internal` docker network with no external route, all egress forced
through a per-project gateway that filters by hostname (TLS SNI / HTTP
Host, no MITM) against the derived allowlist — a privileged sandbox can't
bypass it because enforcement lives outside it; a per-run bearer token
scoped to their own ping endpoints,
and secrets injected as files at bootstrap — never through provisioner
parameters; when a run ends its agent process is stopped and the
credential files removed. The run page's terminal and workspace views go
through the same API, which — like the rest of it — assumes a trusted
network (bind it to localhost or put auth in front). Integration secrets are secretbox-encrypted at rest under
`APOTHECARY_MASTER_KEY` and never leave the engine unmasked. Residual risks
and their rationale: `docs/IMPLEMENTATION_NOTES.md`.

### Unprivileged sandboxes (optional)

By default the per-run sandbox is `--privileged`, because a nested dockerd
needs to mount overlayfs, manage cgroups and program netfilter. That means
**root in a sandbox is root on the host** — so a hostile repo (a
`devcontainer.json` with `"runArgs": ["--privileged"]`, or a compose service
with `privileged: true`, both of which run verbatim by design) or an escape
from the workspace container reaches every other run's checkout, the host
docker socket, and `APOTHECARY_MASTER_KEY` — i.e. every integration secret
you have configured.

Install [sysbox](https://github.com/nestybox/sysbox/blob/master/docs/user-guide/install.md)
on the host and set one variable in `deploy/.env`:

```sh
APOTHECARY_SANDBOX_RUNTIME=sysbox-runc
```

The sandbox is then created with `--runtime=sysbox-runc` and **no**
`--privileged`: root inside it is an unprivileged uid on the host, `/proc` and
`/sys` are virtualized, and the only bind mount is the run's own folder. A
compromised run is confined to what that run already legitimately holds. Boot
and the provisioner's **Test** button fail with a precise error if the daemon
has no such runtime, so a typo can't silently degrade to privileged.

Caveats: **Linux only** (sysbox installs systemd units on the host, so it can't
go into Docker Desktop's VM on macOS/Windows — leave the variable unset there;
Docker Desktop Business subscribers get the same technology via Enhanced
Container Isolation instead). Official packages cover Ubuntu/Debian, with
Fedora/Rocky/Alma/CentOS/Amazon Linux listed as compatible and other distros
building from source. Kernel ≥5.12. Sysbox's own guidance is to prefer a
purpose-built image over `docker:dind`, so verify your `dind_image` under
sysbox before relying on it.

**One deliberate carve-out:** the gateway also hosts the project's
pull-through Docker Hub cache on `:5000`, and each run's inner dockerd pulls
through it (`--registry-mirror`). Those upstream fetches originate in the
gateway itself, so they are *not* subject to the hostname policy — a project
whose policy denies `registry-1.docker.io` can still pull public Hub images
via the mirror. Everything the workspace itself dials is filtered as before.

## Image cache

Each project's gateway keeps a warm image cache so the project's runs stop
cold-pulling the same base images. It lives in the `apo-gw-<project>-cache`
volume and is never reaped; the gateway container is likewise kept warm
(`--restart unless-stopped`) instead of being torn down with the project's
last run. To reclaim the space, or to force a rebuild of the gateway:

```sh
docker rm -f apo-gw-<project>            # next run recreates it
docker volume rm apo-gw-<project>-cache  # disposable: pure cache
```
