# Operating Apothecary

The reference behind the README: the client and engine commands, the
environment, reaching runs, SSH, caches and releases.

## Commands

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
| `apothecaryd user reset-password <handle> [-password <pw>]` | set an account's password from the terminal (the way back in after a lockout) |
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
| `apothecary version` | the client's version |

`APOTHECARY_URL` and `APOTHECARY_TOKEN` stand in for `login` in scripts. With
no arguments the client prints its help. Once a day a command checks the
engine's version and says when the client differs from it.

## Environment

The chart sets these from its values; `internal/platform/config/env.go`
documents each.

- **Required:** `APOTHECARY_POSTGRES_DSN`, and `APOTHECARY_MASTER_KEY` for
  `serve` (it seals every stored secret).
- **Engine:** `APOTHECARY_LISTEN` (default `127.0.0.1:8080`),
  `APOTHECARY_RUNS_DIR`, `APOTHECARY_CPUI_DIR`, `APOTHECARY_LOG_FORMAT`
  (`text`|`json`), `APOTHECARY_LOG_LEVEL`, `APOTHECARY_DEBUG_ADDR` (loopback
  pprof), `APOTHECARY_GRAFANA_URL`.
- **Uploads:** `APOTHECARY_UPLOAD_MAX_BYTES`, `APOTHECARY_UPLOAD_MIME_ALLOW`,
  `APOTHECARY_SNAPSHOT_MAX_BYTES`.
- **Kubernetes:** `APOTHECARY_K8S_*` (namespace, engine URL, protected
  ranges, `RUNTIME_CLASS`, `PRIVILEGED`, …) and the images
  `APOTHECARY_SANDBOX_IMAGE`, `_SHIM_IMAGE`, `_TOOLS_IMAGE`, `_GATEWAY_IMAGE`,
  `_BROWSER_IMAGE`. `APOTHECARY_WORKSPACE_DOCKER`: the
  [daemon door](../README.md#docker-in-workspaces).
- **Access:** `APOTHECARY_PUBLIC_URL`, `APOTHECARY_APPS_DOMAIN` (needs the
  public URL), `APOTHECARY_APPS_SCHEME`, `APOTHECARY_PORT_AUTOEXPOSE`,
  `APOTHECARY_SSH_LISTEN`, `APOTHECARY_SSH_ADVERTISE`,
  `APOTHECARY_CLI_TOKEN_IDLE` (the client's sign-in, default 7 days unused).
- **Telemetry:** the standard `OTEL_*` variables; set
  `OTEL_EXPORTER_OTLP_ENDPOINT` to turn it on.

Run capacity (concurrent runs, default 3; live environments, default 6) is
not an environment variable: an admin sets it under **System → Limits**.

## Probes and telemetry

`GET /healthz` answers 200 while the process serves HTTP;
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

## Image cache

Each project's gateway keeps three caches, so the project's runs stop
fetching the same things cold: a pull-through Docker Hub mirror (`:5000`,
every run's inner dockerd pulls through it), the devcontainer build cache
(`:5001`) and an Open VSX extension gallery (`:5002`). It lives on the gateway's volume, the PVC
`apo-gw-<project>` in the workspaces namespace, and is never reaped. The
gateway itself is likewise kept running instead of being torn down with the
project's last run. To reclaim the space, or to force a fresh gateway:

```sh
kubectl -n apothecary-workspaces delete deploy/apo-gw-<project>   # the next run recreates it
kubectl -n apothecary-workspaces delete pvc/apo-gw-<project>      # disposable: pure cache
```

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
`task k8s:cluster k8s:images k8s:deploy` (see [Kubernetes](kubernetes.md#a-local-cluster)).
`go run ./cmd/apothecaryd serve -dev` (or `task dev:up`) skips the cluster
entirely: fake kinds, a seeded sandbox project.
