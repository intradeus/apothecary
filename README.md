<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="cpui/src/assets/lockups/hero/apothecary-hero-on-dark-1390x322.png">
    <source media="(prefers-color-scheme: light)" srcset="cpui/src/assets/lockups/hero/apothecary-hero-on-light-1390x322.png">
    <img alt="Apothecary — software, bottled" src="cpui/src/assets/lockups/hero/apothecary-hero-on-dark-1390x322.png" width="720">
  </picture>
</p>

<p align="center">
  <b>A self-hosted software factory that runs your own flows: AI agents, commands and human gates, chained the way you want, in sandboxed workspaces.</b>
</p>

<p align="center">
  <a href="https://github.com/intradeus/apothecary/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/intradeus/apothecary/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="CHANGELOG.md"><img alt="Changelog" src="https://img.shields.io/badge/changelog-semver-ff6b2c"></a>
  <img alt="Go 1.26" src="https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white">
  <img alt="Svelte 5" src="https://img.shields.io/badge/svelte-5-FF3E00?logo=svelte&logoColor=white">
  <a href="#install"><img alt="Kubernetes" src="https://img.shields.io/badge/runs%20on-kubernetes-326CE5?logo=kubernetes&logoColor=white"></a>
</p>

<p align="center">
  <a href="#features">Features</a> ·
  <a href="#how-a-run-works">How a run works</a> ·
  <a href="#security">Security</a> ·
  <a href="#install">Install</a> ·
  <a href="#configure">Configure</a> ·
  <a href="#development">Development</a>
</p>

---

Apothecary runs **flows** on your repositories. A flow is a graph you write:
agent stages, shell commands, human gates, waits on a merge request, and
watchers that react to what happens after it opens. Each run gets its own
sandboxed workspace, built from the repository's own devcontainer, with its
egress filtered outside it.

A run can start from a ticket, a merge request, a schedule or a tracker
query. Between the start and the end, it does whatever your flow says.

## Features

- **Your flows.** Edit them as a graph or as YAML ([the flow
  language](docs/flow-language.md)). Five ship built in:
  - `builder`: ticket → plan → build → review → merge request.
  - `reviewer`: reviews someone's merge request, and again after each push.
  - `conflict-resolver`: settles a merge request's conflicts.
  - `hands-on`: you build; agents commit, review and open the merge request.
  - `workspace-only`: a durable workspace, nothing else.

  Each flow carries its own agents. *Configure* binds a built-in flow's
  harness and model per workshop; *Copy to edit* makes it yours.
- **Humans where the flow puts them.** Gates (approve a plan, curate review
  replies), questions the agent asks mid-run, and a debug stop when a stage
  keeps failing. All of them land in one Inbox.
- **Any way in.**
  - Tickets typed by hand, or imported from Jira, GitHub Issues or GitLab
    Issues and kept in sync.
  - Merge requests.
  - **Automations**: a cron schedule, or a tracker query polled for new
    issues.
- **After the merge request opens, the run stays with it.** The `builder`
  flow merges the target back in on conflicts, fixes review comments in
  batches, and repairs failing CI. It asks first, if you want it to.
- **Watch and steer live.** The run page is a desk of views: the agent's
  session, its changes, files, terminal, logs, egress, ports, services and
  vitals. *Take over* to talk to the agent; the *Ask* pane answers questions
  about the code without touching the run.
- **From your machine.** Forward a run's ports, `ssh` in, or open it in
  VS Code, all through the one HTTPS address.
- **Harnesses and models.** opencode, codex or omp. Model credentials (API
  keys or subscription logins) live under *Providers*. A **skills** library,
  grouped into skillsets, is installed per run.
- **Several repositories per workshop**, one merge request per repository
  changed. **GitHub or GitLab**.
- **A browser per workshop**: agents drive it to check the app they built
  and attach screenshots to the merge request.
- **Analytics.** Dashboards and scorecards over your runs. Every transition
  is an event in Postgres, so the UI is a projection and analytics are
  queries.

## How a run works

```
apothecaryd (the engine: flow interpreter, event log, API + UI)
  │  the sandbox pod's dial-in mux
  ▼
per-run sandbox pod (its own docker daemon) ── egress only via the workshop's gateway
  └─ the repo's own devcontainer (the workspace)
       └─ shim sidecar: the coding agent (long-lived, spoken to over ACP),
          file and git views, terminals, the ask_human tool, port discovery
```

- **The engine drives every step.** Command stages are execs. Agent stages
  prompt one long-lived agent session through the sidecar, and its messages,
  tool calls and diffs stream into the run's transcript.
- **Questions wait for you.** An agent's question holds its turn for 30
  minutes; after that the run parks, frees its slot, and resumes the same
  session with your answer.
- **Restarts are safe.** The agent lives with the workspace, so an engine
  restart picks up the turn in flight. A model the harness doesn't offer
  fails the run before its first workspace stage, not three stages in.

The flow-side contract is [docs/flow-language.md](docs/flow-language.md);
design decisions are in [docs/notes/](docs/notes/), one file each.

## Security

Engine and host are trusted; **workspaces are not** (prompt injection is
assumed). The agent runs with its harness's approvals off, because the
sandbox is the boundary, not the agent's permission prompts.

- **One sandbox pod per run**, unprivileged under sysbox by default.
- **Egress:** a network policy lets the pod send only a WireGuard tunnel to
  its workshop's gateway, plus HTTP to the engine. The gateway filters by
  hostname (TLS SNI / HTTP Host, no MITM) against an allowlist or denylist,
  and records every denial. A privileged sandbox can't bypass it, because
  enforcement lives outside the sandbox.
- **Secrets** are written as files at bootstrap, and removed when the run
  ends. At rest they are encrypted under `APOTHECARY_MASTER_KEY` and never
  leave the engine unmasked.
- **The run's token** reaches only its own run's endpoints (its questions,
  its ports); any other resource answers 404.
- **Exposure:** nothing but the engine's HTTP address is published. Ports
  and SSH go through the client's tunnels: a personal token plus run
  ownership, and a registered key for SSH. Web apps get browser links only
  with an apps domain, behind their owner's sign-in.

### Unprivileged sandboxes

A nested dockerd needs to mount overlayfs, manage cgroups and program
netfilter. Run privileged, **root in a sandbox is root on its node**, and
`"runArgs": ["--privileged"]` or compose `privileged: true` run verbatim by
design.

So the chart's default is sysbox (`workspaces.runtimeClassName:
sysbox-runc`, privileged off): root in a sandbox is an unprivileged uid on
the node, `/proc` and `/sys` are virtualised, and the pod gets its own user
namespace. Neither the chart nor the engine accepts unprivileged without a
runtime class, so a typo can't silently fall back to privileged.
`workspaces.privileged: true` is for a cluster of your own without sysbox
(k3d, a dev box), never a shared one.

### Docker in workspaces

A repo whose tests need docker declares the standard
`docker-outside-of-docker` devcontainer feature, as it would for VS Code.
Alternatively, the workshop's **Docker in workspaces** setting adds it
without touching the repo. Either way the workspace gets the run's own
sandbox daemon (the **daemon door**). Whatever it starts stays inside the
sandbox and egresses through the gateway too.

`APOTHECARY_WORKSPACE_DOCKER` decides when the door opens:

| value | the door |
|---|---|
| `auto` (default) | open when sandboxes are unprivileged |
| `on` | open under privileged sandboxes too, where **a workspace using it is root on its node** |
| `off` | never |

A repo that asks for a closed door is refused before anything is built.

## Install

### Cluster requirements

Any Kubernetes cluster, k3s included, with:
- **sysbox** on the nodes that run sandboxes: sysbox ≥ 0.7, containerd ≥
  2.0.5, kernel ≥ 6.3. Recent Ubuntu also needs two AppArmor overrides.
- **NetworkPolicy enforcement**, and the **`wireguard`** kernel module.
- A **local or block storage class**, not NFS.

[docs/kubernetes.md](docs/kubernetes.md) has the details, EKS and a local
k3d cluster.

### Installing

**A fresh Ubuntu box:** `sudo deploy/k3s/install.sh [--domain <host> --email
<address>]` installs k3s, sysbox and the chart
([guide](deploy/k3s/README.md)).

**A cluster you already have:**

1. Copy the values file that fits from
   [`charts/apothecary/ci/`](charts/apothecary/ci/) (`devbox`, `private`,
   `eks-private`, `eks-public`), and set your host names and secrets.
2. Install the chart. A chart version pins every image.

   ```sh
   helm install apothecary oci://ghcr.io/intradeus/charts/apothecary \
     --version <X.Y.Z> -n apothecary --create-namespace -f my-values.yaml
   ```
3. Open the address it prints and create the first account: it becomes the
   admin.
4. Install the `apothecary` client (from the release) on your machine:

   ```sh
   apothecary login https://apothecary.example.com   # approve in the browser
   apothecary ssh-config                             # once: ssh <workspace>.apothecary
   ```

To upgrade, run `helm upgrade` with the new `--version`, then
`apothecary update`. Migrations apply at boot. Keep the master key in a
Secret (`existingSecret`): it seals every stored secret.

Commands, environment variables, SSH, caches and releases are covered in
[docs/operations.md](docs/operations.md).

## Configure

Everything is configured in the UI and stored encrypted in the database.

1. **System → Providers:** your model credentials (API keys or subscription
   logins).
2. **System → Integrations:** add each, then **Test** it.
   - **Repository account:**
     - GitHub: a PAT with `repo`, or fine-grained Contents and Pull
       requests read/write.
     - GitLab: a PAT with `api`, Developer or above.
   - **Harness:** opencode, codex or omp.
   - **Ticket source** (optional): Jira, GitHub Issues or GitLab Issues; see
     [docs/ticket-sources.md](docs/ticket-sources.md).

   The Kubernetes provisioner is already there, filled from the chart.
3. **New workshop** (a workshop is one repository's project). The form walks
   through:
   - **Repository:** the account, the repository, extra repositories.
   - **Workshop:** name, default flow, and commands (`verify` is what test
     stages run, `setup` is optional).
   - **Execution:** the provisioner and the harnesses.
   - **Workspace:** egress, Docker in workspaces.
   - **Services**, **Browser** and **Secrets**.

   The repository needs an `AGENTS.md` and a `.devcontainer/`.
4. **Workshop → Flows:** *Configure* a built-in flow (bind its harness and
   model), or copy one to edit.
5. **Workshop → Health:** **Preflight** checks the configuration without
   building anything. **Envcheck** provisions a throwaway workspace and proves
   it end to end, egress included.
6. **Start a run** from a ticket or a merge request, or let an automation
   start it. Answer what reaches the Inbox.

Instance-wide settings live under **System**: limits, reaper, storage,
notification channels (Matrix, Slack, Discord), users (admins only). Each
account sees only its own integrations, workshops, tickets and runs.

## Development

The checks run with exactly CI's tools:
- Go: from `go.mod`.
- Node: from `.nvmrc`, with the npm it ships.
- helm, task, shellcheck, k3d and kubectl: the versions in
  `.devcontainer/Dockerfile`.

`task` refuses a check on any other version. Each CI job is the task of
the same name, and the devcontainer runs all of them, given docker.

```sh
task check            # before pushing: every lint and test, under the coverage floors
task ci               # everything CI runs but the macOS/Windows builds (docker, k3d, ~25 GB)
task go:test:run -- ./internal/x -run TestY -v    # one Go package
task ui:test:run -- src/lib/x                     # one CPUI test
task dev:up           # engine + UI on :18080 with fake vendors; task dev for the API alone
task k8s:cluster k8s:images k8s:deploy            # the real thing on a local k3d cluster
task --list           # everything else
```

Repository layout:

| Path | Holds |
|---|---|
| `cmd/apothecaryd` | the engine binary and its wiring |
| `cmd/apothecary`, `internal/client` | the terminal client (API only) |
| `internal/` | the engine, grouped by what a package is about: `flow`, `run`, `engine`, `workspace`, `project`, `workitem`, `account`, `skills`, `analytics`, `ports`/`adapters`, `httpapi`, `platform` |
| `workspace/` | the in-workspace shim, the egress gateway and the tools bundle |
| `cpui/` | the web UI (SvelteKit) |
| `charts/apothecary` | the Helm chart |

`go test ./internal/archtest` enforces who may import whom. Agent and
contributor rules are in [AGENTS.md](AGENTS.md), UI rules in
[docs/frontend-standards.md](docs/frontend-standards.md). Commits follow
conventional commits; releases are cut on demand by the `manual-release`
workflow ([docs/operations.md](docs/operations.md#releases--upgrade)).

## Developing Apothecary with Apothecary

This repository is a workshop like any other (`AGENTS.md`, a compose
devcontainer with Postgres). To run it on an instance:

1. **Integrations:** a GitHub repository account and a harness.
2. **Workshop:** this repository, branch `main`, **Docker in workspaces**
   on (the Postgres tests use testcontainers). Commands: `verify` = `task
   check`, or `task ci` for everything CI runs; `setup` = `bash
   .devcontainer/post-create.sh`.
3. **Egress:** *Add sensible defaults*.
4. **Service `default`:** `task dev:up`, health
   `http://localhost:18080/healthz`, timeout `15m`, port `18080`. With the
   Browser on, the builder's `screenshots` option captures the UI.
5. **Flow:** `builder`. Then Preflight, Envcheck, and a ticket.

**`verify` = `task ci`** builds the images and runs the k3d suites inside
the run's sandbox: containers within containers, not VMs. It needs:
- the Kubernetes integration's `storage_size` at `60Gi` or more;
- the verify stage's `timeout` at `60m` or more;
- about 16 GB of memory for the sandbox pod.
