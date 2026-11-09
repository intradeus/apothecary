<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="cpui/src/assets/lockups/hero/apothecary-hero-on-dark-1390x322.png">
    <source media="(prefers-color-scheme: light)" srcset="cpui/src/assets/lockups/hero/apothecary-hero-on-light-1390x322.png">
    <img alt="Apothecary — software, bottled" src="cpui/src/assets/lockups/hero/apothecary-hero-on-dark-1390x322.png" width="720">
  </picture>
</p>

<p align="center">
  <b>A self-hosted software factory: a ticket goes in, a reviewed merge request comes out.</b>
</p>

<p align="center">
  <a href="https://github.com/intradeus/apothecary/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/intradeus/apothecary/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="CHANGELOG.md"><img alt="Changelog" src="https://img.shields.io/badge/changelog-semver-ff6b2c"></a>
  <img alt="Go 1.26" src="https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white">
  <img alt="Svelte 5" src="https://img.shields.io/badge/svelte-5-FF3E00?logo=svelte&logoColor=white">
  <a href="#kubernetes"><img alt="Docker or Kubernetes" src="https://img.shields.io/badge/runs%20on-docker%20%7C%20kubernetes-326CE5?logo=kubernetes&logoColor=white"></a>
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#kubernetes">Kubernetes</a> ·
  <a href="#configure-all-in-the-ui">Configure</a> ·
  <a href="#how-a-run-works">How a run works</a> ·
  <a href="#security-model-short-form">Security</a> ·
  <a href="#development">Development</a>
</p>

---

AI agents and deterministic gates alternate along a flow you define, inside
disposable sandboxed workspaces, with humans at exactly two touchpoints —
plan approval and final review.

- **Configured entirely in the UI.** Repository accounts, workspace
  provisioning, the coding agent and its model auth, projects, flows —
  all live in the database, encrypted where secret. The daemon boots from
  five environment variables.
- **Vendors behind ports.** GitLab or GitHub, opencode, codex or omp; swapping one is a dropdown, not a refactor.
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

- **Docker** — a Linux host, or macOS through Docker (`task local`, where the
  engine itself runs in a container; `task test:integration:docker` passes on OrbStack,
  Docker Desktop is untested) — **or a Kubernetes cluster** (k3s included — see
  [Kubernetes](#kubernetes)). The reference deployment uses host networking
  and the docker socket. The docker provisioner runs each
  run's workspace inside its own **Docker-in-Docker sandbox** (a
  `docker:dind` container), so repo compose files run verbatim with no
  cross-run collisions and teardown is total. By default that sandbox is
  `--privileged` — see [Unprivileged sandboxes](#unprivileged-sandboxes-optional)
  to drop that. Its `RunsDir` must be a **persistent path on the docker host,
  not under `/tmp`** (dind masks `/tmp`) — the default `/var/lib/apothecary/runs`
  is fine. On macOS that path lives inside the docker VM, never in a folder of
  the Mac: the engine's unix sockets under it don't cross a macOS file share.
- A **repository account**: GitLab PAT (`api` scope, Developer+ on the target
  project) or GitHub PAT (`repo` scope, or fine-grained Contents:RW +
  Pull requests:RW).
- **Model auth for your coding agent**: an opencode `auth.json` (API keys
  and/or subscription login), for codex an OpenAI API key / ChatGPT
  `auth.json`, or for omp provider API keys (`KEY=VALUE` lines) and/or
  subscription logins (CLIProxyAPI credential JSON).
- A target repository containing `AGENTS.md` (the repo's conventions).
  Agents themselves are defined **per project in the UI** (v3 agent
  library) — five defaults (planner, builder, committer, reviewer,
  redactor) ship compiled into the binary and appear in every project;
  saving one under the same name creates your editable override.

## Install

```sh
git clone <this repo> && cd apothecary
cp deploy/.env.example deploy/.env
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
> plane (it builds workspace images on your daemon). Workspaces themselves
> never see the socket.

## Kubernetes

Apothecary runs on any Kubernetes cluster — k3s included — from the Helm
chart in `deploy/helm/apothecary`. The chart deploys the engine (and,
optionally, a Postgres); the engine then creates every workspace object
itself, in a namespace of its own:

- **one sandbox pod per run**: `docker:dind` plus git and the devcontainer
  CLI, privileged (or unprivileged under a sysbox `RuntimeClass`), with the
  run's checkout and inner docker state on a volume. Compose devcontainers
  run verbatim inside it, exactly as on the docker host;
- **one egress gateway per project** (a Deployment): the same filter as on
  docker. Each sandbox pod's default route is a WireGuard tunnel to it, and a
  network policy lets the pod send nothing but that tunnel and HTTP to the
  engine — so the gateway, which judges by TLS SNI / HTTP Host and records
  every denial, is the only way out. The cluster's own pod and service ranges
  and cloud metadata are never reachable through it;
- **one browser pod per project** (Microsoft's Playwright MCP image, pinned by
  the engine, with the shim seeded in), reaching the engine's session proxy
  only.

```sh
helm upgrade --install apothecary deploy/helm/apothecary \
  -n apothecary --create-namespace \
  --set masterKey="$(docker run --rm ghcr.io/intradeus/apothecary/apothecaryd:latest keygen)" \
  --set postgres.password=<strong> \
  --set ingress.enabled=true --set ingress.host=apothecary.example.com \
  --set ingress.appsDomain=apps.example.com
```

Exposed workspace ports and the browser editor are served by the engine at
`<port>-<run>.<apps domain>`: point a wildcard DNS record (and certificate)
at the ingress. Each app host asks the run's owner to sign in once.

Cluster requirements:

- **NetworkPolicy enforcement** (k3s has it built in; most CNIs do). Without
  it the sandbox could route around its gateway.
- The **`wireguard` kernel module** on the nodes (in-tree since Linux 5.6;
  loaded on demand).
- Nodes may confine `/usr/bin/wg` with AppArmor (Ubuntu 26.04 ships
  `/etc/apparmor.d/wg`, which also attaches to a privileged container's `wg`):
  nothing to do, the tunnel reads its key from `/etc/wireguard`, the one place
  that profile allows. Sandbox images up to v3.0.0 read it from
  `/secrets/wg` and their `wg` init container fails with
  `fopen: Permission denied`; upgrade, or allow that path on the node
  (`echo '/secrets/wg/** r,' | sudo tee -a /etc/apparmor.d/local/wg && sudo
  apparmor_parser -r /etc/apparmor.d/wg`).
- **Privileged pods** in the workspaces namespace (the chart labels it), or
  [sysbox](https://github.com/nestybox/sysbox) on the nodes and the
  integration's *runtime class* set with *privileged* off — which is also
  what opens the [daemon door](#docker-in-workspaces) under
  `APOTHECARY_WORKSPACE_DOCKER=auto`.
- A **local or block storage class** for workspace volumes: the inner docker
  daemon cannot run on NFS.
- Clusters whose pod or service ranges differ from k3s's defaults:
  `--set workspaces.protectCIDRs=<pods>,<services>,169.254.0.0/16`.

Locally, with [k3d](https://k3d.io) and [helm](https://helm.sh) installed:

```sh
task k8s:cluster    # a k3d cluster + image registry; never touches ~/.kube/config
task k8s:images     # build the five images from the working tree, push as :local
task k8s:deploy     # helm install with values-k3d.yaml
# → http://apothecary.localhost:8090
task k8s:test-live  # the provisioner's and the browser's live tests against the cluster
task k8s:down
```

Every kubectl call these tasks make names `deploy/.k3d-kubeconfig`
explicitly; `task k8s:kubectl -- get pods -A` is the shortcut. A docker
daemon on the containerd image store (the default of a fresh Docker 29
install) pushes to the registry over HTTPS unless `/etc/docker/daemon.json`
lists `k3d-registry.localhost:5111` under `insecure-registries`;
`task k8s:images` checks and says so.

## Configure (all in the UI)

1. **Integrations** — add and **Test** each:
   - *Repositories*: GitLab or GitHub + token.
   - *Provisioner*: **Docker** (a Docker-in-Docker sandbox per run on the
     engine's docker daemon; nothing to fill in) or **Kubernetes** (a sandbox
     pod per run; on a Helm install every field can stay empty — the chart's
     `APOTHECARY_K8S_*` settings fill them). Either way the workspace inside
     the sandbox is the repo's own `.devcontainer/devcontainer.json`.
   - *Harness*: opencode (paste your `auth.json` — it carries ALL model
     credentials), codex (API key and/or ChatGPT `auth.json`) or omp
     (oh-my-pi: provider API keys as `ANTHROPIC_API_KEY=…` lines and/or
     Claude/ChatGPT/Gemini logins as CLIProxyAPI JSON).
   - *Ticket source* (optional): Jira, GitHub Issues or GitLab Issues. Lets you create a
     ticket **from** a tracker issue — search the tracker on the New Ticket
     form and pick an issue. The ticket then **follows the issue**: title,
     description, status, type, labels and comments mirror the tracker and
     are read-only in apothecary (the base branch stays yours). A sync
     re-reads them every 5 minutes, skipping tickets in a Done status; a
     ticket is also refreshed when a run starts on it, after a flow's tracker
     stage writes to it, and on demand (Refresh on the ticket or the
     integration — done tickets included). A ticket typed in by hand lives on
     your **internal tracker** (one per user, created on first use, with
     To do / In progress / In review / Done / Blocked).

     Each tracker has a **status list** and a **type list** on its
     integration page. Per status: a **Done** checkbox (done tickets drop out
     of the sync and can be filtered out of the ticket list) and an optional **flow step** (todo,
     in_progress, in_review, done, blocked — each held by at most one
     status), which `ticket_transition to: <step>` moves to. Per type: its
     change type (feat, fix, hotfix, perf, docs, chore), which names the
     branch. Remote lists come from the tracker and only those columns are
     yours; the internal tracker's lists are entirely editable.

     What is written back to a remote tracker: what the tracker actions
     (`ticket_transition`, `ticket_comment`, `ticket_set_field`,
     `ticket_link` — see `docs/flow-language.md` §2) do, each as a stage the
     flow's author placed, and a status, label or comment change you make on
     the ticket page. On a hand-written ticket the same actions write to the
     internal tracker (any field other than `labels` skips). Only **one run
     per ticket** may be active at a time. Deleting a ticket source whose
     tickets are still linked asks first, then moves them onto your internal
     tracker (status by name, else Done/To do), keeping their comments.

     > **GitHub Issues:** a fine-grained token with *Issues: read and write*
     > on the repositories (read-only is enough to import). Keys are
     > `owner/repo#123`; the only statuses are `open` and `closed` and the
     > only type `issue`, the only writable fields `labels`, `assignees` and
     > `milestone`.

     > **GitLab Issues:** an access token with the `api` scope (`read_api`
     > is enough to import). Keys are GitLab's full reference,
     > `group/project#123`; GitLab's newer "work items" (issues, incidents,
     > tasks) all import through its issues API, and those issue types
     > (issue, incident, task, test case) are its type list. The only
     > statuses are `opened` and `closed`, the only writable fields
     > `labels`, `assignees` and `milestone`; files uploaded into the
     > description come along.

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
     > account's permissions, and this integration only ever issues the
     > reads listed below unless a flow of yours writes. If you do scope one (for the gateway), the read
     > scopes are `read:jira-user` and `read:jira-work` — those two are
     > exactly enough. If your site's picker shows granular scopes instead, the set
     > that matches what this integration reads is `read:user:jira`
     > (`/myself` + the assignee name), `read:issue-details:jira`,
     > `read:issue:jira`, `read:issue-meta:jira` (the issue), `read:field:jira`,
     > `read:status:jira`, `read:issue-type:jira` (the requested fields and
     > the status and type lists), `read:comment:jira` (the comments the sync
     > reads), `read:project:jira` (key resolution + `project = …` filters) and
     > `read:attachment:jira` (the files). Atlassian also documents
     > `read:avatar:jira` and `read:group:jira` on those endpoints because the
     > *responses* carry avatar URLs and group memberships — nothing here
     > reads either, so start without them and add only what a refused call
     > names (Jira's message is surfaced verbatim). Grant `write:jira-work`
     > (granular: `write:issue:jira`, `write:comment:jira`,
     > `write:attachment:jira`, `write:issue.remote-link:jira`, plus
     > `read:field:jira`) **only** if your flows run tracker actions or you
     > change tickets from apothecary; never `manage:` or `delete:`. Label +
     > expiry → copy it (shown once) and paste it with that account's email.
     > Revoke on the same page.
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
     > from, and the token can never see more than that human can. The
     > reads, import and sync, and nothing else:
     > `GET /myself` (the Test button), `POST /search/jql` (the picker's
     > search, falling back to `GET /search` on older sites),
     > `GET /issue/{KEY}?fields=…,labels,attachment` (preview, import, and a
     > single ticket's refresh), a `GET` of each attachment's content URL,
     > `GET /status` + `GET /issuetype` (the status and type lists), and the
     > sync: the search with JQL `key in (…)`, up to 100 issues a request,
     > then `GET /issue/{KEY}/comment` per refreshed ticket. The writes, each
     > only when a flow's tracker action runs or you change the ticket's
     > status, labels or comments: `GET /issue/{KEY}/transitions` +
     > `POST /issue/{KEY}/transitions`, `POST /issue/{KEY}/comment` (with
     > `POST /issue/{KEY}/attachments` first for a comment's files),
     > `GET /field` + `PUT /issue/{KEY}`, and `POST /issue/{KEY}/remotelink`.
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
3. **Projects** — New project: pick the repository account → *Browse repositories* →
   set commands (`setup` is optional; `verify` is what test stages run) →
   pick provisioner + harness → add toolchain **presets** to the
   Dockerfile fragment (Go, Python, Node, JVM, C toolchain) → default
   flow. *Additional repositories* (optional) makes it a multi-repo
   project: each is cloned beside the primary on every run and published
   as its own merge request when changed; leave the locator empty for one
   the agent should only read. The primary's devcontainer must carry their
   toolchains.
4. **Preflight**, then **Envcheck** on the project page. Preflight checks
   the configuration without building anything — repository access, AGENTS.md,
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
and apply without a restart. Notification mirrors (Matrix, Slack, Discord) live under
*Channels*.

## Releases & upgrade

Releases are semver tags (`vX.Y.Z`) cut by CI when you ask for one; nothing
is published on its own.

1. A push to `main` (or a pull request squash-merged into it) runs `ci`.
2. When you want a release: *Actions → manual-release → Run workflow* on
   `main`. It releases `main`'s head, and refuses to unless `ci` passed on
   that commit. Its job summary shows the version and the `CHANGELOG.md`
   section it adds.
3. CI commits `CHANGELOG.md` as `chore(release): vX.Y.Z [skip ci]`, tags it
   `vX.Y.Z`, pushes both to `main`, and pushes the five images to
   `ghcr.io/intradeus/apothecary/<name>` as `:vX.Y.Z`, `:latest` and
   `:sha-…`.
4. Once the images are up, it creates the GitHub Release `vX.Y.Z`: that
   version's `CHANGELOG.md` section, a link to the diff since the previous
   release, and the `docker pull` commands of its images. *Watch → Custom → Releases* on the repository notifies you of each
   one.

Running it again after a failed image push re-publishes that release's
images, then creates the GitHub Release if it is missing.

The version follows the conventional commits since the last tag (rules in
`.cz.toml`):

| commits since the last tag | bump |
|---|---|
| `type!:` or a `BREAKING CHANGE` footer | major |
| `feat` | minor |
| `fix`, `chore`, `perf`, `refactor`, `build`, `revert` | patch |
| only `docs`, `test`, `ci`, `style` | no release |

Every commit message follows that convention: the `commit-msg` hook
(`lefthook.yml`) checks it, and it is registered on its own by `npm ci` in
`cpui` — nothing else to install. A pull request's title becomes the
squashed commit, so the `pr-title` workflow checks it the same way.

```sh
task commit             # write one interactively (needs commitizen: brew install commitizen)
task release:preview    # the version and changelog the next release would get (same)
```

No GitHub setup is needed: the release job pushes its commit and tag to
`main` with the workflow's own token, so `main` must not refuse pushes from
GitHub Actions (no ruleset or branch protection blocking them).

Optional, if you merge pull requests by squashing: on the repository page,
the **Settings** tab (repository admins only) → **General** → scroll to the
**Pull Requests** section → under **Allow squash merging**, set *Default
commit message* to **Pull request title**. The squashed commit then carries
the title `pr-title` checked.

### Deploying a release

Both production setups follow `:latest`, so a deployment picks up a release
by re-pulling. Compose:

```sh
task prod:upgrade       # pull apothecaryd, up -d, print the running version
```

To stay on a fixed version instead, pin it in `deploy/.env`:

```sh
# deploy/.env
APOTHECARY_IMAGE=ghcr.io/intradeus/apothecary/apothecaryd:v3.0.0
```

Helm (`deploy/helm/apothecary/values-production.yaml` lists the secret to
create first):

```sh
helm upgrade --install apothecary deploy/helm/apothecary -n apothecary \
  -f deploy/helm/apothecary/values-production.yaml --set ingress.host=<host>
# each new release: the pull policy is Always, so a restart re-pulls :latest
kubectl -n apothecary rollout restart deployment/apothecaryd
```

Pin all five images with `--set image.tag=vX.Y.Z`. The project browser is
Microsoft's Playwright MCP image, pinned by digest in the engine, so it moves
only with an engine release.

(If the GHCR package is private: `docker login ghcr.io` with a
`read:packages` PAT first, or make the package public in its settings.)

Migrations apply at boot, and the workspace shim and the gateway image are
content-addressed — they rebuild themselves on first use after their
sources change.

To run an **uncommitted tree** as the real containerized service, use the
local-build stack — it builds from the working tree and runs beside the
published-image stack (own ports: engine `127.0.0.1:8081`, postgres
`127.0.0.1:15434`; own volumes; same `deploy/.env` secrets):

```sh
docker compose -p apothecary-local -f deploy/docker-compose.local.yaml up -d --build
```

Alternatives: `PREFIX=apothecary deploy/images.sh dev` builds a versioned
image set for the main stack (`APOTHECARY_IMAGE=apothecary/apothecaryd:dev`
in `deploy/.env`), and `go run ./cmd/apothecaryd serve -dev` skips docker
entirely (fake kinds, seeded sandbox project).

## How a run works

```
apothecaryd (the engine: flow interpreter, event log, API + UI)
  │  unix sockets in the bind-mounted run folder — no container networking
  ▼
per-run Docker-in-Docker sandbox ── egress only via the project gateway
  └─ the repo's own devcontainer (the workspace)
       ├─ shim sidecar   one per workspace: HTTP on /opt/apothecary/sidecar.sock
       │    ├─ the coding agent, long-lived, spoken to in ACP
       │    │    (opencode acp · codex-acp · omp acp) ── ask_human MCP server (shim)
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
- **The run page** has a *Chat* tab (the transcript, live), *Changes* (the run's diff against the branch it forked from),
  *Files* (the checkout, read-only), *Terminal* (a shell in the repo) and
  the raw *Timeline*.
- **Talking to the agent**: only after *Take over*. While you hold it, each
  message is a turn in the same session, delivered when its current turn
  ends, and the stage cannot end until you *Release*. To ask about the code
  without touching the run, use the *Ask* pane.
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
  until it merges (completed) or is closed (abandoned). The built-in
  `builder` flow reacts meanwhile, one thing at a time:
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
| `apothecaryd user list` | every account |
| `apothecaryd user reset-password <handle>` | set an account's password from the terminal (the way back in after a lockout) |
| `apothecaryd user set-admin <handle> [-off]` | make or unmake an instance admin |
| `apothecaryd shim-hash` | the shim source hash a published shim image must carry |
| `apothecaryd tools-hash` | the tools bundle hash a published tools image carries |
| `apothecaryd version` | build version |

Boot environment (the whole deployment plane): `APOTHECARY_POSTGRES_DSN`,
`APOTHECARY_MASTER_KEY` (required); `APOTHECARY_LISTEN` (default
`127.0.0.1:8080`), `APOTHECARY_RUNS_DIR`, `APOTHECARY_CPUI_DIR`,
`APOTHECARY_BASE_IMAGE`, `APOTHECARY_LOG_FORMAT` (`text`|`json`),
`APOTHECARY_LOG_LEVEL` (`debug`|`info`|`warn`|`error`),
`APOTHECARY_DEBUG_ADDR` (loopback `host:port` serving pprof),
`APOTHECARY_GRAFANA_URL` (optional). Telemetry export (traces, metrics,
logs) is configured by the standard `OTEL_*` variables — set
`OTEL_EXPORTER_OTLP_ENDPOINT` to turn it on. The host's
capacity — max concurrent runs (default 3) and max live environments
(default 6) — is not an environment variable: an admin sets it in
**System → Limits**, the same for every user, and it applies to the next run
that starts. On Kubernetes the chart also sets the
`APOTHECARY_K8S_*` variables (namespace, engine URL, images, protected
ranges), `APOTHECARY_APPS_DOMAIN` and `APOTHECARY_PUBLIC_URL`
(`internal/platform/config/env.go` documents each). `APOTHECARY_SSH_LISTEN` and
`APOTHECARY_SSH_ADVERTISE` turn the SSH door on (below); unset, there is
none.

Probes: `GET /healthz` answers 200 while the process serves HTTP;
`GET /readyz` answers 503 while the engine drains for shutdown or cannot
reach Postgres (2s; the answer says only `database unreachable` or
`database timeout`, the cause is logged), 200 otherwise. Both are public;
the Helm chart uses them for its readiness and liveness probes.

Traces are short: each stage attempt is a trace of its own, linked to an
instant `run` span; Tempo finds a run's attempts with
`{span.apothecary.run_id="<run id>"}`.

Platform observability: the engine exports traces, metrics and logs over
OTLP when the standard `OTEL_EXPORTER_OTLP_ENDPOINT` (and optionally
`OTEL_EXPORTER_OTLP_PROTOCOL`, `_HEADERS`, `OTEL_RESOURCE_ATTRIBUTES`) is
set. For development, `observability/docker-compose.yaml` runs
`grafana/otel-lgtm` (OTLP on 127.0.0.1:14317/14318, Grafana on
127.0.0.1:3001) with six provisioned dashboards — platform, engine, LLM
spend, humans, feature usage, and run analytics over the `run_facts` /
`stage_attempts` tables (set `APOTHECARY_PG_PASSWORD`, and
`APOTHECARY_PG_ADDR` if Postgres is not the default deployment's). In
production, point `OTEL_*` at your own backend and import the same
dashboards. `APOTHECARY_GRAFANA_URL` puts a link to them in
**System → Observability**, which only an admin sees.

## Accounts

The first visit to the UI claims the instance: it creates the first
account, an admin. From then on every request is a signed-in user's —
a browser session (cookie) or a personal API token (Account page; sent as
`Authorization: Bearer apo_…`). Each user owns their integrations,
workshops, tickets, runs, channels and settings and sees nothing of anyone
else's; an admin manages accounts (System → Users) and nothing more. There
is no organisation level and no role matrix. Passwords are the one sign-in
method today; the method table is built for OAuth and passkeys to join it.

## SSH and VS Code

A run's workspace is reachable from a desktop — plain `ssh`, or VS Code's
Remote-SSH with its terminal and file tree — through the engine's **SSH
door**: one listener the engine host exposes, usually on a VPN address
(`APOTHECARY_SSH_LISTEN=100.64.0.9:2222`, with
`APOTHECARY_SSH_ADVERTISE=engine.tail1234.ts.net:2222` naming what people
connect to). No sshd runs in any container and no container port is
published: the door authenticates the public keys registered on an account
(**Account → SSH keys**), routes by username — the workspace's name, which
the run page prints — to a run that account owns, and proxies the session
to an SSH server in the workspace's sidecar, over the same private path the
engine already uses to reach it. One host key for every run; its
fingerprint is on the run page to verify on first connect.

From a run's **Env ▾** menu: *Open in VS Code* (a `vscode://` link
Remote-SSH understands — install the Remote-SSH extension once), the `ssh`
command to copy, and the door's host key. The first connection from VS Code
downloads its server into the workspace: that download rides the project's
egress gateway, so allow `update.code.visualstudio.com` and
`vscode.download.prss.microsoft.com` there (the held-connection review
offers it), or set `remote.SSH.localServerDownload` to `always` on the
desktop. Not served in this version: the sftp subsystem (use `scp -O` or
rsync) and remote forwards (`-R`). A session ends when the workspace does
(a rebuild, the reaper); VS Code reconnects on its own.

Desktop VS Code keeps its own extensions over SSH. The ones that run in
the workspace — language servers, the harness extensions — install into
the workspace's `~/.vscode-server`, a set apart from code-server's. Two
desktop settings make that automatic:
`remote.defaultExtensionsIfInstalledLocally` lists the extensions every
remote gets when the desktop has them too, and
`remote.downloadExtensionsLocally` set to `true` downloads them on the
desktop and pushes them over the session instead of having the workspace
reach Microsoft's marketplace. The Env ▾ menu copies both lines for the
desktop's `settings.json`, the list pre-filled with the extensions the
workspace's own editor runs. (`remote.SSH.defaultExtensions` is the
Remote-SSH extension's own variant — SSH hosts only, installed whether or
not the desktop has them — and shows up in the settings UI only once that
extension is installed.) A harness extension signs in with the workspace's
own credentials, the ones the engine seeded, never the desktop's.

When the sidecar itself is broken and the door cannot dial, the operator's
way in is the run's inner docker daemon, on the engine host:
`DOCKER_HOST=unix://<RunsDir>/<workspace>/docker.sock docker exec -it <container> bash`
— on Kubernetes `kubectl -n <namespace> exec -it <workspace> -c sandbox --
docker exec -it <container> bash`.

## Development

Four units of checks, each a task and a CI job (`.github/workflows/ci.yml`,
on every pull request and push to `main`; a release needs all four green):

```sh
task test:unit                     # unit: Go (`go test ./...`) + the CPUI (vitest) — nothing running, macOS or Linux; args go to go test (-- -count=1 ./...)
task test:integration              # integration: every suite below (needs docker)
task test:integration:pg           #   the Postgres adapter and boot tests, on throwaway containers
task test:integration:docker       #   docker workspaces (lifecycle, egress gateway, file tree) + the browser, inside the engine image
task test:integration:harness      #   the pinned harness CLIs (tools image) against what the engine hands them
task test:integration:k8s          #   the Kubernetes provisioner and project browser on a local k3d cluster
task lint                          # lint: gofmt, go vet (linux + darwin, every build tag), model ids, API contract, svelte-check, CPUI linters
task build                         # build: apothecaryd, shim, gateway, the CPUI, the five images

task test                          # unit, then integration
task test:linux                    # the Go unit tests in a Linux container, incl. the Linux-only build-tagged ones
task verify                        # pre-commit gate: lint, unit tests with coverage, binaries, CPUI build
task cover                         # test coverage of both halves, as pages (coverage/go, cpui/coverage)
go run ./cmd/apothecaryd serve -dev   # real engine/API/UI, fake vendors
task dev                           # the same, on :18080 against a scratch Postgres (API only)
task dev:up                        # ...plus the built UI, master key written to deploy/.env if missing
cd cpui && npm run dev             # UI dev server (proxies /api)

devcontainer up --workspace-folder . && devcontainer exec --workspace-folder . task test:unit   # the repo's own devcontainer (no docker inside it: the pg suite runs on the host, or add the docker-outside-of-docker feature locally)
```

Development happens on macOS or Linux, and `task test:unit` passes on both
(CI runs it on Linux): a unit test fakes what it needs (fixture `/proc` and
`/sys` roots, injected paths) rather than reading the machine. Anything that
needs a real environment is an integration test and runs in containers.
Running Apothecary itself on macOS means running it in Docker
(`task local`), never a native engine: the engine talks to its workspaces over
unix sockets under RunsDir, which must be a path on the docker host (inside
the docker VM on macOS), since those sockets do not cross a macOS file share.
`go run ./cmd/apothecaryd serve -dev` stays for UI and API work against fake
vendors.

The backend is a modular monolith. `internal/engine` runs a run: its
lifecycle and the interpreter's loop, whose decisions — the state a run is
in, the fold that rebuilds it from the run's events, where a stage routes —
live in `internal/run/runcore`, pure and tested without a harness. Each other
concern is its own package behind an interface it declares (`mrs`,
`envstate`, `wsinfo`, `envcheck`, `egress`, `ask`, `runports`, `services`,
…), never importing the engine; `cmd/apothecaryd/wire.go` builds and wires
them. The ports the adapters implement are split by capability under
`internal/ports/` (`envport`, `agentport`, `repoport`, …). `go test
./internal/archtest` enforces who may import whom.

`internal/` is grouped by what a package is about:

| Folder | Holds |
|---|---|
| `flow/` | the flow language, its registries and the built-in templates |
| `run/` | what a run is: its event log, its fold, what starts it |
| `engine/` | the interpreter and the services around a live run |
| `workspace/` | everything about a run's container |
| `project/` | what a project is configured with |
| `account/` | who a person is and what they signed in to |
| `skills/` | the skills format and its library |
| `ports/`, `adapters/` | the boundaries, and what implements them |
| `httpapi/` | the HTTP API |
| `platform/` | building blocks with no domain knowledge |

Design documents live in `docs/` — `DESIGN.md` (v1 engine),
`DESIGN2.md` (config plane & distribution), with every deviation recorded
in `docs/IMPLEMENTATION_NOTES.md`.

## Developing Apothecary with Apothecary

This repo is a project like any other: it carries `AGENTS.md` and its own
`.devcontainer/` (compose: a Go/Node toolchain container beside a
`postgres:16`), so an Apothecary instance can run agent runs on it. The
recipe:

1. **The door.** The Postgres suites run on testcontainers, so the project
   turns on *Docker in workspaces* (step 3; the devcontainer itself declares
   no docker feature) and the instance must open the
   [daemon door](#docker-in-workspaces): on your own laptop,
   `APOTHECARY_WORKSPACE_DOCKER=on` in `deploy/.env` (sandboxes stay
   privileged — a run on this repo is then root on your machine, your call);
   on a shared server, `APOTHECARY_SANDBOX_RUNTIME=sysbox-runc` instead,
   which opens it by itself. A closed door refuses the project with a
   message naming both. The engine host must also reach `ghcr.io` (the
   feature is fetched engine-side on docker).
2. **Integrations.** A GitHub repository account (classic PAT with `repo`),
   a Docker provisioner, a harness; the model is bound later under
   Workshop → Flows → Configure.
3. **Project.** Repository `https://github.com/intradeus/apothecary`,
   locator `intradeus/apothecary`, default branch `main`, AGENTS.md path
   left default. Workspace: **Docker in workspaces** on. Commands: `verify` =
   `task lint && task test:unit && task test:integration:pg`; optionally
   `setup` = `bash .devcontainer/post-create.sh`.
4. **Egress.** *Add sensible defaults*, then add `get.helm.sh` (helm comes
   from there) — everything else the build and the hooks dial (Debian, the
   Go module proxy, npm, GitHub, Docker Hub through the mirror,
   `download.docker.com` for the feature) is in the defaults.
5. **Service `default`.** Start `task dev:up`, health
   `http://localhost:18080/healthz`, timeout `15m` (the first start builds
   the UI and compiles the daemon), ports `[{18080, "UI"}]`. A screenshot
   stage then lands on "Claim this apothecary" and creates the first
   account of that throwaway instance.
6. **Flow.** The builder template; `screenshots` needs the project's
   Browser enabled.
7. Preflight (AGENTS.md and the devcontainer are found) → Envcheck → a ticket.

`AGENTS.md` tells the agent what to run and what never to run inside a
workspace (the image-building and sandbox-nesting suites).

## Security model (short form)

Engine and host are trusted; **workspaces are not** (prompt injection is
assumed). The agent runs with its harness's approvals off — the sandbox is
the boundary, not the agent's permission prompts. Workspaces get: a per-run Docker-in-Docker sandbox on its own
`--internal` docker network with no external route, all egress forced
through a WireGuard tunnel to a per-project gateway that filters by hostname (TLS SNI / HTTP
Host, no MITM) against the derived allowlist — a privileged sandbox can't
bypass it because enforcement lives outside it (the host needs the
`wireguard` kernel module, mainline since 5.6); the run's own docker daemon
reaches a workspace only through the *daemon door* — open by itself under an
unprivileged sandbox, by the operator's explicit `APOTHECARY_WORKSPACE_DOCKER=on`
otherwise, and a repo asking for it while it is closed is refused before
anything is built ([Docker in workspaces](#docker-in-workspaces)); a per-run bearer token
scoped to their own ping endpoints,
and secrets injected as files at bootstrap — never through provisioner
parameters; when a run ends its agent process is stopped and the
credential files removed. Every API route needs a signed-in user (the run
token reaches only its own run's ping endpoints), and a resource that is
not the caller's answers 404 — so a workspace, which can reach the API
through its bridge, can do nothing with it but ask its own questions.
Browser sessions are HttpOnly cookies; writes with a cookie must come from
the UI's own origin. The engine itself still speaks plain HTTP: put TLS in
front of it before leaving localhost, and the workspace-port relay stays
bound to the listen host. Integration secrets are secretbox-encrypted at
rest under `APOTHECARY_MASTER_KEY` and never leave the engine unmasked.
Residual risks and their rationale: `docs/IMPLEMENTATION_NOTES.md`.

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
building from source. Kernel ≥5.12. The default `docker:29-dind` sandbox
image is verified under sysbox — the full provisioning path, egress gateway
included (`docs/IMPLEMENTATION_NOTES.md`). If you override `dind_image`,
re-verify it with the live test described at the top of
`internal/adapters/workspace/live_test.go`. On a Linux host this is the
recommended setting; it stays opt-in only because it is a host install we
don't ship.

### Docker in workspaces

A repo whose tests or tooling need a docker daemon — testcontainers,
`docker compose`, a `docker run` from the agent — declares the standard
`ghcr.io/devcontainers/features/docker-outside-of-docker` feature (or a
`/var/run/docker.sock` mount) in its devcontainer, exactly as it would for VS
Code. In a run that socket is the **daemon door**: the run's own sandbox
daemon, also listening on its default socket. Everything the workspace
starts through it is a sibling container inside the same sandbox — it dies
with the run and egresses through the project's gateway like the workspace
itself; testcontainers' reaper and port detection work with no overrides.

Whether the door is open is one deployment setting, `APOTHECARY_WORKSPACE_DOCKER`:

| value | the door |
|---|---|
| `auto` (default) | open only when the sandbox is unprivileged (`APOTHECARY_SANDBOX_RUNTIME`; on Kubernetes a runtime class with *privileged* off). There the sandbox is the boundary already, so handing a run its own daemon adds nothing it does not hold. |
| `on` | open under privileged sandboxes too. **A workspace that takes it is then root on the host** — the daemon can start a privileged container in a privileged sandbox, which is the host. Fine for an instance on your own laptop running your own repos; not for a shared server, where sysbox is the answer. |
| `off` | never. |

A repo that asks for the door while it is closed is refused at provision,
before anything is built, with a message naming what asked and both ways
out — instead of docker's late "bind source path does not exist" when the
feature's mount finds no socket. `runArgs: ["--privileged"]` and compose
`privileged: true` are not the door and keep running verbatim, as above.
The feature installs the docker CLI from `download.docker.com`
(`moby: false`) or `packages.microsoft.com` (its default), both in the
offered egress defaults.

A repo that does not declare the feature can still get docker from the
project: **Workshop → Settings → Workspace → Docker in workspaces** adds the
same (pinned) feature to the build through the devcontainer CLI's
`--additional-features`, with the repo's files untouched. It is an ask like
the repo's own: the door decides, a closed one refuses the project's runs
naming the setting, and a repo that already declares the feature is left
alone.

**One deliberate carve-out:** the gateway also hosts the project's caches:
the pull-through Docker Hub cache on `:5000` (each run's inner dockerd pulls
through it, `--registry-mirror`), the devcontainer build cache on `:5001`,
and the VS Code extension gallery on `:5002` — an Open VSX proxy the run's
code-server is pointed at, so the browser editor's marketplace works inside
allow-mode projects and a project's runs share one copy of every extension.
Those upstream fetches originate in the gateway itself, so they are *not*
subject to the hostname policy — a project whose policy denies
`registry-1.docker.io` or `open-vsx.org` can still pull public Hub images
via the mirror and install extensions via the gallery. Everything the
workspace itself dials is filtered as before.

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
