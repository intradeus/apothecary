# Plan: Kubernetes-only deployment, one chart, no public workspace ports

Status: implemented on `refactor/k8s-native-refactoring` (2026-11-11). What
was decided while building it, and where it deviates from this plan, is in
`docs/IMPLEMENTATION_NOTES.md`, "Kubernetes only, one chart, tunnels and the
apothecary client". Not done yet: the k3d tunnel test.
Owner: the agent implementing it. Read `AGENTS.md` first;
every rule there applies (conventional commits, no model ids, `task fmt`,
`task lint`, `task test:unit`, `task cover:go` with the 95% floor, new
numbered migrations only, no heavy tasks inside a workspace).

Items marked **verify** were not confirmed against the code when this plan
was written: check them before building on them.

## Goal

A new user installs Apothecary with one Helm command on any Kubernetes
cluster (single-node k3s on a dev box, a cluster reachable only over a VPN,
EKS) and every
feature works. It must be easy to deploy and safe by default.

## Decisions

1. **Kubernetes is the only provisioner.** The Docker provisioner and the
   docker-compose deployment are removed. Docker *inside* a run's sandbox pod
   (the inner dockerd that runs the repository's devcontainer) is unchanged.
2. **One chart, standard layout**, published as an OCI artifact, with tested
   example values files, one per deployment shape.
3. **No workspace port is ever published as a raw network port.** There is no
   port pool, and the SSH door has no network port of its own: SSH rides the
   HTTPS connection through the CLI. The only network surface is HTTPS (443).
   Every path is authenticated: browser session, or personal bearer token
   (plus the user's SSH key for SSH).
4. **A developer reaches a run's ports through a tunnel to their own
   machine**: `apothecary forward <run>` mirrors the sandbox's listening
   ports onto `localhost`, over the HTTPS connection. The frontend calling
   `localhost:8080` works because both ports land in the sandbox. Nothing
   changes in the client's repository and there is no per-repository
   configuration.
5. **Nothing is specific to one VPN or ingress product.** A private
   deployment is the same chart with whatever ingress class and Service
   annotations the operator's network needs. Tailscale appears in the docs as
   one worked example, never in templates, values names or engine code.
6. **Clients are one section.** The web UI stays in `cpui/`; the terminal
   client is one Go binary, `apothecary`, which starts as plain commands and
   later grows a TUI in the same binary.
7. **One PR** for all of it, on the existing branch
   `refactor/k8s-native-refactoring`.
8. **Out of scope for now**: a preview pane in the run page, shareable app
   URLs for teammates and demos (visibility levels, public links), and any
   rewriting of an app's
   responses. The existing apps domain stays as it is: optional, owner-only.

Non-goals: multi-replica engine, a Postgres operator, an installer for
Kubernetes itself.

## What already exists (do not rebuild)

| Piece | Where |
|---|---|
| SSH door, with `ssh -L` forwarding dialled inside the workspace | `internal/workspace/sshdoor`, `workspace/shim/sshd_forward.go` |
| Personal bearer tokens on the API | `internal/httpapi/auth.go` (`AuthenticateToken`) |
| A connection to any workspace port (`PortConn`) | `envport.WorkspacePortExposer`, `internal/workspace/runports` |
| Observation of listening ports | `internal/workspace/runports/observed.go` |
| Owner-only web apps at `<port>-<run>.<apps domain>` | `internal/httpapi/apps.go`, `portrelay.RouteByHost` |
| The agent's browser, where `localhost` is the run | `internal/adapters/browser` (`proxy.go`) |

---

## Part 1 — Remove the Docker provisioner and the compose deployment

Everything after assumes one host.

### Delete

| Path | Note |
|---|---|
| `internal/adapters/workspace/host_docker.go`, `docker_paths_test.go` | docker sandbox host |
| `internal/adapters/browser/host_docker.go` | docker browser host |
| `internal/adapters/egressgw/host_docker.go`, `wg_docker.go` | docker gateway host |
| `internal/project/kinds/docker.go` and its tests | the `docker` kind |
| `internal/workspace/wsimage`, `Resolver.WarmImages`, the tools-image notice | the kubernetes kind never reads `ShimBin` / `ToolsDir` / `GatewayImage`; the last caller is the browser's `Shim:` in `cmd/apothecaryd/wire.go`, which the Kubernetes path replaces. **Verify** the live tests, then delete the lot, and drop the `go:embed` of sources only `wsimage` compiled |
| `ProvisionerDeps.SandboxRuntime`, `APOTHECARY_SANDBOX_RUNTIME` | see "daemon door" below |
| `sandboxState`'s `legacy` result and every pre-sandbox branch | docker-only |
| `deploy/docker-compose.yaml`, `deploy/docker-compose.local.yaml` | |
| Taskfile `local*`, `prod:*`, `test:integration:docker` | |
| the `docker` entry of the CI matrix | |

`test:integration:harness` runs against docker today: port it to the k3d
cluster of `test:integration:k8s`; do not drop the coverage.

The `Host` interfaces in `workspace`, `browser` and `egressgw` now have one
implementation each. Keep a seam only where unit-test fakes depend on it.

### Corrections to carry

- **No backward compatibility.** Nothing was ever deployed on the docker
  provisioner: no migration, no special case for its kind.
- **Daemon door.** The warning in `wire.go` tests `SandboxRuntime == ""`,
  which is always true on Kubernetes. Move the check to the integration
  (`k8sHost.unprivileged()`), and reword `APOTHECARY_WORKSPACE_DOCKER`'s
  comments to drop the docker host.
- **Runtime class.** It is per integration today (`runtime_class`,
  `privileged` in `kinds/kubernetes.go`) with no deployment default. Add
  `APOTHECARY_K8S_RUNTIME_CLASS` and `APOTHECARY_K8S_PRIVILEGED` to the
  kind's defaults so the chart sets them once.
- **First provisioner.** **Verify** whether a fresh install has a
  provisioner integration at all. If not, seed one `kubernetes` integration
  from the `APOTHECARY_K8S_*` defaults at first boot, so a fresh install can
  run a workspace without a manual step.

README: the compose install path and all docker-host wording go; Kubernetes
becomes the install section (Part 4). `task dev:up` (fake kinds) and
`task k8s:*` (k3d) are the two dev loops.

Check `go test ./internal/archtest` and the coverage floor after the delete;
add tests on the Kubernetes paths rather than lowering the floor.

---

## Part 2 — The chart

Move `deploy/helm/apothecary` to `charts/apothecary`.

```
charts/apothecary/
  Chart.yaml               # version and appVersion stamped by the release
  values.yaml              # every knob, commented; safe defaults
  values.schema.json
  README.md                # generated by helm-docs
  templates/
    _helpers.tpl  NOTES.txt  serviceaccount.yaml  rbac.yaml  secret.yaml
    pvc.yaml  deployment.yaml  service.yaml  ingress.yaml
    postgres.yaml  namespace-workspaces.yaml
    tests/connection.yaml  # helm test: GET /healthz through the Service
  ci/                      # chart-testing values; each is a documented example
    k3d-values.yaml  devbox-values.yaml  private-values.yaml
    eks-private-values.yaml  eks-public-values.yaml
```

Rules:

- The release workflow stamps `version` and `appVersion`, packages the chart
  and pushes it to `oci://ghcr.io/intradeus/charts/apothecary`. Install is
  `helm install apothecary oci://… --version X.Y.Z -f my-values.yaml`.
- `image.tag` defaults to `appVersion`; `pullPolicy` defaults to
  `IfNotPresent`. The in-repo chart keeps `appVersion: latest` for dev, so
  the helper renders `Always` whenever the resolved tag is `latest`.
- `task lint:helm` runs `helm lint` and `helm template` on every
  `ci/*-values.yaml` and checks the generated README. If `ct lint` is added,
  pass `--check-version-increment=false` (the version only moves at release).
- `replicas` leaves the values: the Deployment is fixed at 1 with
  `strategy: Recreate` (in-process state). Say why in `values.yaml`.
- Safe defaults: `workspaces.runtimeClassName: sysbox-runc`,
  `workspaces.privileged: false`. Privileged sandboxes are an explicit value,
  never a fallback; `ci/k3d-values.yaml` sets it for the dev cluster.
- Sandbox node selectors and tolerations become values.

### The `access` block

It replaces the top-level `service`, `ssh` and `ingress` blocks.
`service-ssh.yaml`, `ssh.advertise` and `APOTHECARY_SSH_ADVERTISE` go: the
door is reached only through the tunnel endpoint (Part 3).

```yaml
access:
  http:
    service: { type: ClusterIP, port: 8080, annotations: {} }
    ingress:
      enabled: false
      className: ""
      annotations: {}
      host: ""            # apothecary.example.com — required when enabled
      tls: []
    # Optional, unchanged behaviour: owner-only web apps (the in-browser
    # editor included) at <port>-<run>.<appsDomain>. Needs a wildcard DNS
    # record and certificate. Keep it on a different domain from the UI.
    appsDomain: ""
```

`APOTHECARY_PUBLIC_URL` is rendered from `access.http.ingress.host`, or from
a required `access.publicURL` when the HTTP Service is reached directly.
Nothing is discovered at runtime.

---

## Part 3 — Tunnels and the CLI

### Engine

1. **Port tunnel.** `GET /api/runs/{id}/ports/{port}/tunnel` upgrades to a
   WebSocket and pipes it to the workspace port through the provisioner's
   `PortConn`. Access: session or personal bearer token, and the caller must
   own the run. It sends keepalive pings (ALB idles out at 60 s). One
   WebSocket per TCP connection.
2. **Listening ports.** `GET /api/runs/{id}/ports/listening` returns what the
   observer sees, for the CLI to mirror. **Verify** what `observed.go`
   exposes today and reuse it.
3. **SSH over HTTPS.** `GET /api/ssh/tunnel` upgrades to a WebSocket and
   hands the stream to the SSH door in-process. The door still checks the
   user's SSH key; the bearer token only gets the connection that far. On
   Kubernetes the door binds no TCP listener; `APOTHECARY_SSH_LISTEN` stays
   for `task dev:up`. The run page prints `ssh <workspace>` and points at
   `apothecary ssh-config` instead of a host:port.
4. **No random-port relay on Kubernetes.** With `APOTHECARY_K8S_NAMESPACE`
   set and no apps domain, `portrelay` binds nothing: an exposure records the
   port and the run page shows the forward command instead of a URL. The
   listener mode stays for `task dev:up` only.
5. **The agent's browser** forwards hosts other than `localhost` through the
   project's egress gateway instead of refusing them, so a frontend that
   calls a deployed backend works when the egress policy allows it.

`routes.go` changes: run `task api`.

### Terminal client: `apothecary`

One client binary, released for Linux, macOS and Windows beside the images.

Layout:

| Path | Holds |
|---|---|
| `cmd/apothecary` | `main` only: wiring and command registration |
| `internal/client/api` | the typed API client (login, runs, ports, tunnels); the one place that speaks HTTP to the engine |
| `internal/client/cli` | the commands below, thin over `api` |
| `internal/client/tui` | later; not created in this PR |

`cpui/` does not move: a rename would touch the Taskfile, CI, the embed path,
the lint scripts and every doc for no behaviour change. "Clients" is a
section of the README's development table (web: `cpui/`; terminal:
`cmd/apothecary` + `internal/client`). Add `internal/client` to
`internal/archtest`: it may import nothing of the engine except shared API
types, and nothing of the engine imports it.

For now the binary is commands only, in the same command style as
`apothecaryd` (**verify** what it uses and reuse it; add no CLI framework for
five commands). The TUI will be built on Bubble Tea (with Bubbles and Lip
Gloss): it is Go, so it lives in this binary and reuses `internal/client/api`,
and its message-driven update loop fits what the TUI will show, runs and
pings arriving over the event stream. Do not add the dependency in this PR.
What this PR owes the TUI: commands hold no logic of their own (everything
goes through `api`), output goes through one printer, and `apothecary` with
no arguments prints help (the TUI will take that slot).

| Command | What it does |
|---|---|
| `apothecary login <url>` | stores a personal token. **Verify** how tokens are created today; add a browser approval flow if they can only be made in the UI |
| `apothecary forward <run>` | mirrors every listening port of the sandbox onto the same local port, and follows ports as they open and close. A taken local port is reported and mapped to a free one |
| `apothecary forward <run> 5432[:15432]` | one port, optional local port |
| `apothecary ssh-config` | writes `ProxyCommand apothecary ssh-proxy %h` entries for the user's runs |
| `apothecary ssh-proxy <workspace>` | stdin/stdout over the SSH tunnel endpoint |
| `apothecary open <run>` | later: a browser window with its own profile whose `localhost` is the run (`--proxy-server` to a local proxy, `<-loopback>` in the bypass list), for two runs side by side |

Listeners bind `127.0.0.1` only.

### UI

The run's Ports pane lists listening ports, each with a copy button for
`apothecary forward <run> <port>`, plus one "forward everything" command.
With an apps domain, web ports keep their Open link. Without one, "Open
editor" becomes "Open in VS Code" (Remote-SSH); the System hub says the apps
domain is not configured.

---

## Part 4 — Deployment shapes and docs

All shapes: sysbox on the nodes that run sandboxes, Postgres bundled or
external, `existingSecret` or `masterKey` + `postgres.password`.

| Shape | File | Access |
|---|---|---|
| Dev box (k3s) | `devbox-values.yaml` | Traefik ingress + cert-manager with a domain; or `access.http.service.type: LoadBalancer` and plain HTTP, documented as LAN only |
| Private (VPN only) | `private-values.yaml` | the generic ingress with the operator's own class and annotations; nothing else differs. Without a wildcard name there is no apps domain: editor through Remote-SSH, apps through `apothecary forward` |
| EKS private | `eks-private-values.yaml` | ALB, scheme internal, `alb.ingress.kubernetes.io/target-type: ip`, ACM certificate; RDS; gp3 `WaitForFirstConsumer` |
| EKS public | `eks-public-values.yaml` | the same, internet-facing |

No shape has a TCP Service: SSH and every workspace port ride HTTPS through
the CLI.

The private shape must hold for any VPN: WireGuard, a corporate VPN, a
tailnet, a cloud private link. The chart's part is to pass `className`,
`annotations`, `tls` and Service annotations through untouched, and to leave
`tls` free-form so an ingress controller that names its host there works
without a template branch. The docs carry one worked example with the
Tailscale operator, written as "for example", next to a plain
ingress-on-a-private-address example.

EKS notes for the docs: sandbox nodes on an Ubuntu AMI with sysbox, labelled
and tainted (Bottlerocket is not supported); EBS volumes pin a run's pod to
one zone; images may be mirrored to ECR through `image.overrides`.

Dev-box bootstrap: upstream the k3s scripts (node setup, sysbox install and
smoke test, the `hostUsers: false` admission policy) into `deploy/k3s/`,
idempotent, with `--help`. The README install section becomes: bring a
cluster or run `deploy/k3s/install.sh`; pick an example values file;
`helm install`; open the URL `NOTES.txt` prints and claim the instance;
install the CLI.

---

## Order of work

One PR on `refactor/k8s-native-refactoring`, titled
`refactor!: kubernetes-only deployment, one chart, tunnels and the apothecary client`.
Keep the commits separate and in this order, each one building and passing
`task test:unit`:

1. `refactor!: remove the docker provisioner and compose deployment` (Part 1).
2. `feat(workspace): port and ssh tunnels over https` (Part 3, engine).
3. `feat(client): apothecary login, forward and ssh commands` (Part 3, client).
4. `refactor(chart)!: charts/apothecary, schema, helm-docs, OCI publish, access block` (Part 2). After the tunnels, because it removes the SSH Service.
5. `docs: install guides for dev box, private network and EKS` + `deploy/k3s/` (Part 4).

Extend `test:integration:k8s`: forward a port from a test workspace through
the tunnel endpoint and read from it; ssh through the tunnel endpoint.

## Acceptance

- `helm template` renders for every `ci/*-values.yaml`; the schema rejects an
  enabled ingress without a host and privileged sandboxes without the
  explicit value.
- Fresh install on k3s with only the master key and Postgres password set: a
  run starts a frontend on 3000 that calls `localhost:8080`, and a Postgres
  on 5432. After `apothecary forward <run>` on another machine, the page at
  `localhost:3000` loads its data and `psql -h localhost` connects, with no
  change to the repository.
- `ssh <workspace>` works through `apothecary ssh-config` with only 443 open.
- The same on a cluster reachable only over a VPN (validated on a tailnet;
  nothing in the chart or engine names it). EKS is validated by rendering and one manual
  install before the release that claims it; the docs say which.
- A port scan of the deployment finds 443 and nothing else.
- No reference to the docker provisioner or the compose deployment remains,
  except inner-dockerd and devcontainer compose wording.
- `task fmt lint test:unit cover:go` green; `docs/IMPLEMENTATION_NOTES.md`
  has an entry per decision above.
