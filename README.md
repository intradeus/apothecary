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
  <a href="#kubernetes"><img alt="Kubernetes" src="https://img.shields.io/badge/runs%20on-kubernetes-326CE5?logo=kubernetes&logoColor=white"></a>
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

- **A Kubernetes cluster**: a single-node k3s box, EKS, or any other,
  reachable over the internet or only over your VPN. See
  [Kubernetes](#kubernetes) for what it needs (sysbox on the nodes that run
  sandboxes, NetworkPolicy enforcement, the wireguard module).
  Each run's workspace gets its own **sandbox pod with its own docker
  daemon**, so repo compose files run verbatim, with no cross-run collisions,
  and teardown is total.
- **Helm**, to install the chart.
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

**A fresh Ubuntu box?** `sudo deploy/k3s/install.sh` sets up everything:
k3s, sysbox and the chart. Add `--domain <host> --email <address>` for
HTTPS. [`deploy/k3s/README.md`](deploy/k3s/README.md) walks through each
step. On a cluster you already have:

1. **Pick the example values file** that matches your cluster from
   [`charts/apothecary/ci/`](charts/apothecary/ci/):
   - `devbox-values.yaml`: a k3s box, on a domain or a LAN address.
   - `private-values.yaml`: a cluster behind a VPN, with any ingress class.
   - `eks-private-values.yaml` and `eks-public-values.yaml`: EKS.

   Copy it, and set your host names and secrets.
2. **Install the chart:**

   ```sh
   helm install apothecary oci://ghcr.io/intradeus/charts/apothecary \
     --version <X.Y.Z> -n apothecary --create-namespace -f my-values.yaml
   ```

   A chart version pins every image. The chart's
   [README](charts/apothecary/README.md) lists every value.
3. **Open the address the install prints** and claim the instance by
   creating the first account. Each account gets a Kubernetes provisioner
   with the deployment's settings, so there is nothing to fill in.
4. **On your machine, install the `apothecary` client** (the release's
   binaries) and sign in once:

   ```sh
   apothecary login https://apothecary.example.com   # approve it in the browser
   apothecary ssh-config     # once: ssh <workspace>.apothecary, VS Code Remote-SSH
   ```

   The sign-in lasts as long as you keep using it: it expires after 7 days
   without use (`APOTHECARY_CLI_TOKEN_IDLE`), and every use extends it.
   `apothecary update` keeps the client at the engine's version.

Database migrations apply at boot. The master key (`apothecaryd keygen`)
seals every stored secret; keep it in a Secret (`existingSecret`).

## Reaching a run from your machine

Everything goes through Apothecary's one HTTPS address. No VPN is needed,
and no other port is published.

- **A run's ports on your machine:** `apothecary forward <run>`, the run
  named by its id or its workspace's name.
  - Every port the workspace listens on lands on a free port of your
    machine, and the client prints where (`3000 → localhost:43263`).
    Forwarding the same port again lands on the same local port while it is
    free, so bookmarks and database connections keep working.
  - The forward follows the run's ports as they open and close.
  - `apothecary forward <run> 5432` forwards one port, and `5432:15432` picks
    its local port.
  - `--same` keeps the port numbers: use it when a frontend calls its API at
    `localhost:<port>`, so it works as on your laptop.
  - Forwards run in the background until `apothecary forward stop <run>` (or
    `--all`). Closing the terminal does not end them, and several runs
    forward side by side.
  - `apothecary forward status` lists them, run by run, with each run's
    ticket. `apothecary forward logs` shows the forwarder's log.
- **A shell or desktop VS Code:** after `apothecary ssh-config`,
  `ssh <workspace>.apothecary`, or *Open in VS Code* from the run page.
- **Web apps in the browser, without the client:** set an apps domain
  (`access.http.appsDomain`, a wildcard DNS record and certificate). Each web
  port is then at `https://<port>-<run>.<apps domain>`, behind its owner's
  sign-in.

## Kubernetes

Apothecary runs on any Kubernetes cluster, k3s included, from the Helm
chart in `charts/apothecary`. The chart deploys the engine (and,
optionally, a Postgres). The engine then creates every workspace object
itself, in a namespace of its own:

- **one sandbox pod per run**: `docker:dind` plus git and the devcontainer
  CLI, unprivileged under a sysbox `RuntimeClass` (in a user namespace of
  its own) by default, with the run's checkout and inner docker state on a
  volume. Compose devcontainers run verbatim inside it;
- **one egress gateway per project** (a Deployment). Each sandbox pod's
  default route is a WireGuard tunnel to it, and a
  network policy lets the pod send nothing but that tunnel and HTTP to the
  engine — so the gateway, which judges by TLS SNI / HTTP Host and records
  every denial, is the only way out. The cluster's own pod and service ranges
  and cloud metadata are never reachable through it;
- **one browser pod per project** (Microsoft's Playwright MCP image, pinned by
  the engine, with the shim seeded in), reaching the engine's session proxy
  only.

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
- **[sysbox](https://github.com/nestybox/sysbox) on the nodes that run
  sandboxes** (the chart's default: `workspaces.runtimeClassName:
  sysbox-runc`, privileged off). The RuntimeClass's own scheduling
  (`nodeSelector`, `tolerations`) places sandbox pods on those nodes. This is
  also what opens the [daemon door](#docker-in-workspaces) under
  `APOTHECARY_WORKSPACE_DOCKER=auto`. A cluster of your own without sysbox
  (k3d, a dev box) can run sandboxes privileged instead
  (`workspaces.privileged: true`): sandbox root is then root on its node.
  Under a runtime class the sandbox pod
  is also created with `hostUsers: false`, sysbox's documented pod shape on
  Kubernetes: a user namespace of the pod's own, made by the kubelet and
  adopted by sysbox. Without it sysbox still makes one for the container,
  but containerd has set up the pod's network namespace before that, and
  the sandbox sysctls it then writes fail from a user namespace that does
  not own it — `ip_unprivileged_port_start: permission denied`, and the pod
  never starts. So the cluster needs Kubernetes user namespaces (on by
  default since 1.33): **containerd ≥ 2.0.5** (or CRI-O), **sysbox ≥ 0.7**
  (an older `sysbox-runc` answers no `features` and the kubelet rejects the
  pod: `RuntimeClass handler "sysbox-runc" does not support user
  namespaces`), a kernel ≥ 6.3, and idmapped mounts on the workspace
  storage class (ext4, xfs, btrfs; one more reason NFS is out).
- **AppArmor on Ubuntu nodes** meets sysbox twice:
  - **`fusermount3` (Ubuntu 25.04 and later, 26.04 included).**
    `/etc/apparmor.d/fusermount3` lets FUSE mount under a few trees only,
    and sysbox-fs mounts the virtualised `/proc` and `/sys` of every
    container under `/var/lib/sysboxfs` — so no sandbox starts at all
    (`failed to pre-register with sysbox-fs`; `journalctl -u sysbox-fs`
    says `fusermount3: mount failed: Permission denied`, the kernel log
    `apparmor="DENIED" operation="mount" profile="fusermount3"`). Allow
    sysbox's mounts in the profile's local override, with the rules its
    maintainers give, and reload:

    ```sh
    sudo tee -a /etc/apparmor.d/local/fusermount3 <<'EOF'
    # allow Sysbox to create fuse mounts
    mount fstype=@{fuse_types} options=(nosuid,nodev,rw) -> /var/lib/sysboxfs/**/,
    mount fstype=@{fuse_types} options=(nosuid,nodev,ro) -> /var/lib/sysboxfs/**/,
    umount /var/lib/sysboxfs/**/,
    EOF
    sudo apparmor_parser -r /etc/apparmor.d/fusermount3 && sudo systemctl restart sysbox
    ```

    The kernel log then still shows one `fusermount3` denial of
    `dac_override` per sandbox start: harmless, the profile withholds that
    capability on purpose and the rules above do not grant it.

  - **Unprivileged user namespaces (24.04 and later).**
    `kernel.apparmor_restrict_unprivileged_userns=1` lets only a process
    that is root on the host, or runs under a profile allowing it, create
    a user namespace. sysbox and a privileged sandbox are host root and
    unaffected; under sysbox nothing in a sandbox is — that is the point —
    so a tool in the workspace that sandboxes itself with a user namespace
    of its own (a harness's bubblewrap command sandbox, Chrome, rootless
    podman, docker's userns-remap) is denied: `bwrap: setting up uid map:
    Permission denied` in the run, `apparmor="DENIED"
    operation="userns_create" profile="unconfined"` in the node's kernel
    log. Lift it (`echo kernel.apparmor_restrict_unprivileged_userns=0 |
    sudo tee /etc/sysctl.d/60-apothecary-userns.conf && sudo sysctl
    --system`), or ship a profile with a `userns,` rule for each such
    binary (it attaches by the binary's path, as the `wg` profile above
    does).
- A **local or block storage class** for workspace volumes: the inner docker
  daemon cannot run on NFS.
- Clusters whose pod or service ranges differ from k3s's defaults:
  `--set workspaces.protectCIDRs=<pods>,<services>,169.254.0.0/16`.
- **On EKS:**
  - Sandbox nodes are a managed node group on an Ubuntu EKS AMI with
    `sysbox-deploy-k8s`, labelled and tainted for sandboxes. Check
    sysbox's supported OS and kernel list against the AMI. Bottlerocket is
    not supported.
  - EBS volumes are single-zone, so use a `WaitForFirstConsumer` gp3
    StorageClass.
  - An ALB in front of the engine needs
    `alb.ingress.kubernetes.io/target-type: ip`, because the engine's
    Service is ClusterIP.
  - Images can be mirrored to ECR with `image.overrides`.

Locally, with [k3d](https://k3d.io) and [helm](https://helm.sh) installed:

```sh
task k8s:cluster    # a k3d cluster + image registry; never touches ~/.kube/config
task k8s:images     # build the five images from the working tree, push as :local
task k8s:deploy     # helm install with charts/apothecary/ci/k3d-values.yaml
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
   - *Provisioner*: **Kubernetes**, a sandbox pod per run. Each account
     already has one with every field empty, filled from the chart's
     `APOTHECARY_K8S_*` settings; add another only to run on another cluster
     or with other settings. The workspace inside the sandbox is the repo's
     own `.devcontainer/devcontainer.json`.
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
   `vX.Y.Z`, pushes both to `main`, pushes the five images to
   `ghcr.io/intradeus/apothecary/<name>` as `:vX.Y.Z`, `:latest` and
   `:sha-…`, and the Helm chart to
   `oci://ghcr.io/intradeus/charts/apothecary` as `X.Y.Z`, pinned to those
   images.
4. Once the images are up, it creates the GitHub Release `vX.Y.Z`: that
   version's `CHANGELOG.md` section, a link to the diff since the previous
   release, the `docker pull` commands of its images, the chart's install
   command, and the `apothecary` client's binaries for Linux, macOS and
   Windows. *Watch → Custom → Releases* on the repository notifies you of each
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

A chart version pins every image, so upgrading is one command:

```sh
helm upgrade apothecary oci://ghcr.io/intradeus/charts/apothecary \
  --version <X.Y.Z> -n apothecary -f my-values.yaml
```

The project browser is Microsoft's Playwright MCP image, pinned by digest in
the engine, so it moves only with an engine release. Migrations apply at
boot. Update the `apothecary` client from the same release.

(If the GHCR packages are private: `helm registry login ghcr.io` and an
`imagePullSecrets` entry with a `read:packages` PAT, or make the packages
public in their settings.)

To run an **uncommitted tree** for real, on a local k3d cluster:
`task k8s:cluster k8s:images k8s:deploy` (see [Kubernetes](#kubernetes)).
`go run ./cmd/apothecaryd serve -dev` (or `task dev:up`) skips the cluster
entirely: fake kinds, a seeded sandbox project.

## How a run works

```
apothecaryd (the engine: flow interpreter, event log, API + UI)
  │  the sandbox pod's dial-in mux — no container IP is ever dialled
  ▼
per-run sandbox pod (its own docker daemon) ── egress only via the project gateway
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

Two binaries. The engine's commands ship in its image (`kubectl -n apothecary
exec deploy/apothecaryd -- apothecaryd <cmd>`):

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

The `apothecary` client runs on your machine (the release's binaries;
`task build:client` from source) and talks to an engine over its HTTPS
address:

| command | purpose |
|---|---|
| `apothecary login <url>` | sign in: the page opens, you approve the code it shows, and this machine gets a sign-in that lasts while you use it (expires after 7 days unused). `--token <personal token>` signs in with a token from **Account → API tokens** instead, for scripts and CI; `--no-browser` only prints the page's address |
| `apothecary forward <run> [port[:local]]... [--same]` | a run's ports on free local ports (`--same`: the same numbers), in the background until stopped; with no port, every port it listens on, kept in step |
| `apothecary forward status` | what is forwarded, run by run |
| `apothecary forward stop <run> [port]...` or `--all` | stop forwarding |
| `apothecary forward logs` | the background forwarder's log |
| `apothecary ssh-config` | once: route `ssh <workspace>.apothecary` and VS Code Remote-SSH through the engine |
| `apothecary ssh-proxy <host>` | the ssh `ProxyCommand` that `ssh-config` installs |
| `apothecary update [--check]` | install the client release matching the engine's version, checked against the release's `SHA256SUMS` |
| `apothecary logout` | sign out, revoking this machine's sign-in |

`APOTHECARY_URL` and `APOTHECARY_TOKEN` stand in for `login` in scripts. With
no arguments the client prints its help. Once a day a command checks the
engine's version and says when the client differs from it.

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
(`internal/platform/config/env.go` documents each), including the sandbox
shape every provisioner gets by default: `APOTHECARY_K8S_RUNTIME_CLASS` and
`APOTHECARY_K8S_PRIVILEGED`. `APOTHECARY_SSH_LISTEN` gives the SSH door a TCP
port of its own, for the dev loop; deployments reach it through the client's
tunnel instead.

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

A run's workspace is reachable from a desktop, with plain `ssh` or VS Code's
Remote-SSH (its terminal and file tree), through the engine's **SSH door**.
No sshd runs in any container, and no SSH port is published.

1. **Register a key** under **Account → SSH keys**.
2. **Run `apothecary ssh-config` once.** It writes one block into
   `~/.ssh/config`: every host named `<workspace>.apothecary` goes through
   `apothecary ssh-proxy`, inside the engine's HTTPS address.
3. **Connect:** `ssh <workspace>.apothecary`, or *Open in VS Code* from the
   run page.

The door authenticates the key, checks that the run is yours, and proxies the
session to an SSH server in the workspace's sidecar, over the engine's own
path into the workspace. The tunnel itself needs your client's token. The
door has one host key for every run, pinned under one alias; its fingerprint
is on the run page.

From a run's **Env ▾** menu: the one-time setup command, *Open in VS Code* (a
`vscode://` link that Remote-SSH understands; install the Remote-SSH
extension once), and the `ssh` command to copy. The first connection from VS
Code downloads its server into the workspace. That download rides the
project's egress gateway, so either:
- allow `update.code.visualstudio.com` and
  `vscode.download.prss.microsoft.com` there (the held-connection review
  offers it), or
- set `remote.SSH.localServerDownload` to `always` on the desktop.

Port forwards (`ssh -L`) work too, though `apothecary forward` is simpler.
Not served in this version: the sftp subsystem (use `scp -O` or rsync) and
remote forwards (`-R`). A session ends when the workspace does (a rebuild,
the reaper); VS Code reconnects on its own.

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
way in is the run's inner docker daemon:
`kubectl -n <namespace> exec -it <workspace> -c sandbox -- docker exec -it <container> bash`.

## Development

The checks run with exactly CI's tools: Go from `go.mod` (the Taskfile's
`GOTOOLCHAIN` fetches it), Node and npm from `.nvmrc` (`fnm use`), and helm,
task and shellcheck at the versions in `.devcontainer/Dockerfile`, which has
them all. `task` refuses a check on any other version.

Every CI job (`.github/workflows/ci.yml`, on every pull request and push to
`main`) is one task with the same name: the go-test job runs `task go:test`,
and so on. CI skips a side of the tree a change does not touch (Go, UI,
sandbox images, engine image); a release needs a green run.

```sh
task check                         # before pushing: every lint, the Go tests (Postgres included) and the CPUI tests, in parallel, under their coverage floors (needs docker for Postgres)
task ci                            # everything CI runs, locally and in parallel: check, then the sandbox images and suites; never skips a side as CI does (needs docker, k3d, ~15 GB of disk)
task lint                          #   every lint, in parallel; task test: every test suite
                                   #   (the umbrellas log each check to build/checks/ and print one line per check, then the tail of any failure)

task go:test                       # every Go test, Postgres ones included, under GO_COVER_MIN; pages in coverage/go/
task go:lint                       # gofmt, go vet (linux with every build tag, darwin, the client on windows), the API contract
task ui:test                       # the CPUI tests under their floors; pages in cpui/coverage/
task ui:lint                       # svelte-check, the CPUI linters, the production build
task repo:lint                     # no model id anywhere in the tree; the Helm chart and its README (chart:lint)
task sandbox:images                # the four images the sandbox suites run
task sandbox:test:harness          # the pinned harness CLIs (tools image) against what the engine hands them
task sandbox:test:k8s              # the Kubernetes provisioner and project browser on a local k3d cluster
task engine:image                  # the engine image

task go:test:run -- ./internal/x -run TestY -v   # Go tests, nothing running (Postgres-backed ones skip)
task go:test:pg                    # only the Postgres-backed Go tests, on throwaway containers
task go:test:linux                 # the Go tests in a Linux container, incl. the Linux-only build-tagged ones
task go:test:stress                # Go tests under CI-like load (2 CPUs, burners, -race)
task ui:test:run -- src/lib/x      # CPUI tests, args to vitest
task build                         # apothecaryd, the apothecary client, shim, gateway, the CPUI, the five images
go run ./cmd/apothecaryd serve -dev   # real engine/API/UI, fake vendors
task dev                           # the same, on :18080 against a scratch Postgres (API only)
task dev:up                        # ...plus the built UI, master key written to deploy/.env if missing
cd cpui && npm run dev             # UI dev server (proxies /api)

devcontainer up --workspace-folder . && devcontainer exec --workspace-folder . task go:test:run   # the repo's own devcontainer (no docker inside it: the pg suite runs on the host, or add the docker-outside-of-docker feature locally)
```

Development happens on macOS or Linux, and `task go:test:run` and `task
ui:test:run` pass on both (CI runs `task go:test` and `task ui:test` on Linux:
the same tests, Postgres ones included, under the coverage floors — `GO_COVER_MIN` in the Taskfile and the
thresholds in `cpui/vite.config.ts`, which only ever rise): a unit test fakes
what it needs (fixture `/proc` and
`/sys` roots, injected paths) rather than reading the machine. Anything that
needs a real environment is an integration test and runs in containers.
Running Apothecary for real means a cluster: locally, k3d
(`task k8s:cluster k8s:images k8s:deploy`, on Linux or macOS).
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
| `workitem/` | what runs are started on — a ticket, a merge request — one package per kind, registered once (`workitem/items`) |
| `engine/` | the interpreter and the services around a live run |
| `workspace/` | everything about a run's container |
| `project/` | what a project is configured with |
| `account/` | who a person is and what they signed in to |
| `skills/` | the skills format and its library |
| `ports/`, `adapters/` | the boundaries, and what implements them |
| `httpapi/` | the HTTP API |
| `client/` | the `apothecary` terminal client (`cmd/apothecary`): talks to an engine over its API only |
| `platform/` | building blocks with no domain knowledge |

The clients are two: the web UI in `cpui/`, and the terminal client. The
Helm chart is in `charts/apothecary`.

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
   [daemon door](#docker-in-workspaces). Under the chart's default
   (unprivileged sysbox sandboxes) it opens by itself. On a cluster of your
   own with privileged sandboxes, set `APOTHECARY_WORKSPACE_DOCKER: "on"` in
   `engine.extraEnv`: a run on this repo is then root on its node, your call.
   A closed door refuses the project with a message naming both.
2. **Integrations.** A GitHub repository account (classic PAT with `repo`)
   and a harness. The Kubernetes provisioner is already there. The model is
   bound later under Workshop → Flows → Configure.
3. **Project.** Repository `https://github.com/intradeus/apothecary`,
   locator `intradeus/apothecary`, default branch `main`, AGENTS.md path
   left default. Workspace: **Docker in workspaces** on. Commands: `verify` =
   `task check`; optionally
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
the boundary, not the agent's permission prompts. Workspaces get: a per-run
sandbox pod, unprivileged under sysbox by default, whose network policy lets
it send nothing but a WireGuard tunnel to its project's gateway and HTTP to
the engine. The gateway filters by hostname (TLS SNI / HTTP Host, no MITM)
against the derived allowlist. A privileged sandbox can't bypass it either,
because enforcement lives outside it (nodes need the `wireguard` kernel
module, mainline since 5.6). The run's own docker daemon
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
the UI's own origin. **Nothing but the engine's HTTP address is published.**
- **Workspace ports and SSH** reach people only through the client's
  tunnels, which need a personal token and the run's ownership (and, for SSH,
  a registered key).
- **Web apps and the editor** get browser links only with an apps domain,
  each behind its owner's sign-in.

The engine itself speaks plain HTTP, so terminate TLS at the ingress.
Integration secrets are secretbox-encrypted at
rest under `APOTHECARY_MASTER_KEY` and never leave the engine unmasked.
Residual risks and their rationale: `docs/IMPLEMENTATION_NOTES.md`.

### Sandbox privilege

A nested dockerd needs to mount overlayfs, manage cgroups and program
netfilter. Run privileged, **root in a sandbox is root on its node**: a
hostile repo (a `devcontainer.json` with `"runArgs": ["--privileged"]`, or a
compose service with `privileged: true`, both of which run verbatim by
design), or an escape from the workspace container, reaches every other
sandbox on that node.

So the chart's default is **unprivileged sandboxes under
[sysbox](https://github.com/nestybox/sysbox)**:
`workspaces.runtimeClassName: sysbox-runc`, privileged off. Root inside a
sandbox is then an unprivileged uid on the node, `/proc` and `/sys` are
virtualised, and the pod gets a user namespace of its own. A compromised
run is confined to what that run already legitimately holds.

- **Requirements:** sysbox installed on the nodes that run sandboxes, with
  the cluster requirements under [Kubernetes](#kubernetes) (containerd ≥
  2.0.5, sysbox ≥ 0.7, the AppArmor overrides on recent Ubuntu).
- **Refusing to downgrade:** the chart refuses unprivileged sandboxes
  without a runtime class, and the engine refuses `APOTHECARY_K8S_PRIVILEGED=off`
  without `APOTHECARY_K8S_RUNTIME_CLASS`, so a typo can't silently degrade to
  privileged.
- **Privileged instead:** set `workspaces.privileged: true`, for a cluster
  of your own without sysbox (k3d, a dev box). Never on a shared one.

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
| `auto` (default) | open only when the sandbox is unprivileged (a runtime class with *privileged* off, the chart's default). There the sandbox is the boundary already, so handing a run its own daemon adds nothing it does not hold. |
| `on` | open under privileged sandboxes too. **A workspace that takes it is then root on its node**: the daemon can start a privileged container in a privileged sandbox, which is the node. Fine on a cluster of your own running your own repos; not on a shared one, where sysbox is the answer. |
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

Each project's gateway keeps a warm image cache, so the project's runs stop
cold-pulling the same base images. It lives on the gateway's volume, the PVC
`apo-gw-<project>` in the workspaces namespace, and is never reaped. The
gateway itself is likewise kept running instead of being torn down with the
project's last run. To reclaim the space, or to force a fresh gateway:

```sh
kubectl -n apothecary-workspaces delete deploy/apo-gw-<project>   # the next run recreates it
kubectl -n apothecary-workspaces delete pvc/apo-gw-<project>      # disposable: pure cache
```
