# Plan: Kubernetes-only deployment, one chart for a dev box and EKS

Status: proposed. Owner: the agent implementing it. Read `AGENTS.md` first;
every rule there applies (conventional commits, no model ids, `task fmt`,
`task lint`, `task test:unit`, `task cover:go` with the 95% floor, new
numbered migrations only, no heavy tasks inside a workspace).

## Goal

A new user installs Apothecary with one Helm command on any Kubernetes
cluster — single-node k3s on a dev box, a tailnet-only box, or EKS — and
every feature works, including reaching exposed workspace ports over their
real protocol (HTTP, Postgres, Redis, Kafka, anything TCP) and the SSH door.
Pointing a domain at it is optional.

Three decisions frame the work:

1. **Kubernetes is the only provisioner.** The Docker provisioner (sandbox
   containers on the engine's own docker daemon) and the docker-compose
   deployment are removed. Docker *inside* a run's sandbox pod (the inner
   dockerd that builds and runs the repo's devcontainer / compose stack) is
   unchanged: that is how workspaces work, not a deployment mode.
2. **One chart, standard layout**, published as an OCI artifact, with a
   handful of tested example values files — one per deployment shape.
3. **Raw TCP is exposed through a fixed pool of ports** on one Service whose
   type the operator picks (NodePort, LoadBalancer, Tailscale, AWS NLB). The
   apps domain stays as the optional HTTPS path for web apps.

Non-goals: multi-replica engine, a Postgres operator, an installer for
Kubernetes itself (k3s/EKS are prerequisites; the docs link to them).

---

## Part 1 — Remove the Docker provisioner and the compose deployment

Do this first, in its own PR (`refactor!:` — it is breaking). Everything
after assumes one host.

### Code to delete

| Path | What it is |
|---|---|
| `internal/adapters/workspace/host_docker.go`, `docker_paths_test.go` | docker sandbox host |
| `internal/adapters/browser/host_docker.go` | docker browser host |
| `internal/adapters/egressgw/host_docker.go`, `wg_docker.go` | docker gateway host |
| `internal/project/kinds/docker.go` (+ its tests) | the `docker` provisioner kind |
| engine-side image builds in `internal/workspace/wsimage` that only the docker host uses (`EnsureShim`/`EnsureTools`/`EnsureGatewayImage` callers in `internal/project/integrations/resolver.go`, `cmd/apothecaryd/wire.go:268`) | the k8s host seeds shim/tools from release images through init containers — verify each caller before deleting; keep whatever the k8s path still uses |
| `ProvisionerDeps.SandboxRuntime`, `APOTHECARY_SANDBOX_RUNTIME` | docker runtime flag; k8s uses a RuntimeClass |
| `sandboxState`'s `legacy` result and every "pre-sandbox env" branch | docker-only legacy |
| `deploy/docker-compose.yaml`, `deploy/docker-compose.local.yaml` | compose deployment |
| Taskfile `local*`, `prod:*` (compose-based), `test:integration:docker` | compose tasks |
| CI matrix entry `docker` in `.github/workflows/ci.yml` | |

`test:integration:harness` currently runs against docker: port it to the k3d
cluster that `test:integration:k8s` already builds (or fold it into that
suite), don't drop the coverage.

The `Host` interface in `internal/adapters/workspace/host.go` (and the
equivalents in `browser` and `egressgw`) now has one implementation. Keep the
seam only if unit-test fakes depend on it; otherwise collapse it into the k8s
type. Prefer the smaller diff that leaves no dead abstraction.

### Data migration

Projects whose provisioner integration is `kind = docker` must not crash the
engine. Add a new numbered migration that marks those integrations disabled
(or rewrites them to `kubernetes` with defaults — pick one and note it in
`docs/IMPLEMENTATION_NOTES.md`), and make the resolver return a clear error
for an unknown kind instead of panicking. Runs on a docker env at upgrade
time are reported as failed with "the docker provisioner was removed in
vX; re-run on Kubernetes".

### Config and docs

- `APOTHECARY_WORKSPACE_DOCKER` (the daemon door) stays: it is about the
  inner dockerd, still meaningful on k8s. Reword its comments to drop docker
  host mentions.
- README: delete the compose "Install" path and the docker-host wording
  throughout ("exactly as on the docker host", the `DOCKER_HOST=unix://…`
  operator tip); Kubernetes becomes the install section (see Part 4).
- `task dev:up` (fake kinds, no cluster) and `task k8s:*` (k3d) stay as the
  two dev loops.

Check `go test ./internal/archtest` and the coverage floor after the delete;
removing well-tested code can drop the percentage — add tests on the k8s
paths rather than lowering the floor.

---

## Part 2 — The chart: standard layout

The conventional layout for an application repo that ships its own chart is
a top-level `charts/<name>/`, published to an OCI registry. Move
`deploy/helm/apothecary` there.

```
charts/apothecary/
  Chart.yaml               # version = app version, kubeVersion: ">=1.29.0-0"
  values.yaml              # every knob, commented; safe defaults
  values.schema.json       # validates values; helm refuses bad input early
  README.md                # generated by helm-docs from values.yaml comments
  templates/
    _helpers.tpl
    NOTES.txt              # prints the URLs and next steps after install
    serviceaccount.yaml
    rbac.yaml              # engine: workspaces ns objects + get its own Services
    secret.yaml            # skipped when existingSecret is set
    pvc.yaml               # runs dir
    deployment.yaml        # engine, replicas: 1, strategy: Recreate
    service.yaml           # HTTP (UI + API + apps-domain routing), ClusterIP
    service-tcp.yaml       # SSH door + port pool (Part 3)
    ingress.yaml           # optional, for HTTP
    postgres.yaml          # optional bundled Postgres (StatefulSet + Service)
    namespace-workspaces.yaml
    tests/connection.yaml  # `helm test`: GET /healthz through the Service
  ci/                      # chart-testing values; each is also a documented example
    devbox-values.yaml
    tailscale-values.yaml
    eks-private-values.yaml
    eks-public-values.yaml
```

Rules:

- `values-k3d.yaml` moves to `ci/k3d-values.yaml` (used by `task k8s:deploy`);
  `values-production.yaml` is replaced by the `eks-*` examples.
- Release workflow (`manual-release.yml`) packages the chart and pushes it to
  `oci://ghcr.io/intradeus/charts/apothecary` at the release version, next to
  the images. Install becomes
  `helm install apothecary oci://ghcr.io/intradeus/charts/apothecary --version X.Y.Z -f my-values.yaml`.
  The chart's default `image.tag` is its appVersion, so a chart version pins
  all five images. Default `pullPolicy` becomes `IfNotPresent` (pinned tags);
  drop the ":latest + Always + rollout restart" upgrade story.
- CI: `task lint:helm` runs `helm lint` and `helm template` against every
  `ci/*-values.yaml` and checks the generated README is current
  (`helm-docs --dry-run` diff). Add `ct lint` if cheap.
- `replicas` is removed from values: the engine holds the port-pool
  listeners and in-process state, so the Deployment is fixed at 1 with
  `strategy: Recreate`. Document why in `values.yaml`.
- Workspaces namespace, RuntimeClass name (`workspaces.runtimeClassName`,
  e.g. `sysbox-runc`), node selectors/tolerations for sandbox pods are values,
  not code constants. Verify which already exist and expose the missing ones.

---

## Part 3 — Network access: one `access` block

Today the engine exposes four things through three unrelated settings
(`service`, `ssh`, `ingress.appsDomain`) and the port relay binds random
host ports that nothing publishes on Kubernetes (the bug that made
`http://<tailscale-ip>:33097` unreachable). Replace that with one block.

### What must be reachable

| Surface | Protocol | Today |
|---|---|---|
| UI + API | HTTP | Service 8080 (+ optional Ingress) |
| Apps domain (`<port>-<run>.<domain>`) | HTTP(S), behind sign-in | engine HTTP server, needs wildcard DNS + cert |
| SSH door | TCP 2222 | `service-ssh.yaml` |
| Exposed workspace ports | any TCP | random engine port, **unreachable on k8s** |

### Values

```yaml
access:
  # What people type to reach Apothecary. Empty = discovered at runtime
  # from the tcp Service's status (LoadBalancer hostname/IP, or the node's
  # address for NodePort). Set it to use a DNS name you own or a MagicDNS name.
  host: ""

  http:
    # The UI/API Service. ClusterIP behind an Ingress, or LoadBalancer /
    # NodePort to reach it directly (dev box).
    service:
      type: ClusterIP
      port: 8080
      annotations: {}
    ingress:
      enabled: false
      className: ""        # traefik | alb | tailscale | nginx ...
      annotations: {}
      host: ""             # apothecary.example.com
      tls: []
    # Optional: HTTPS URLs for web apps, <port>-<run>.<appsDomain>, behind
    # the user's session. Needs a wildcard DNS record + certificate on the
    # ingress. When set, ports declared as http/https use it; everything
    # else still goes through the pool.
    appsDomain: ""

  tcp:
    # One Service carrying the SSH door and the port pool.
    enabled: true
    service:
      type: LoadBalancer   # NodePort | LoadBalancer
      loadBalancerClass: "" # "tailscale" with the Tailscale operator
      annotations: {}      # AWS NLB / Tailscale hostname annotations go here
      # Who may connect (rendered as loadBalancerSourceRanges; NodePort: docs
      # tell you to firewall). Required when the Service is internet-facing.
      allowedCIDRs: []
    ssh:
      enabled: true
      port: 2222
    ports:
      # The pool exposed workspace ports are allocated from. NodePort needs
      # 30000-32767. AWS NLB: one listener + target group per port, default
      # quota 50 per NLB — keep count under it.
      from: 30100
      count: 40
```

The chart renders `service-tcp.yaml` with `ssh` plus `count` ports, each
`port == targetPort (== nodePort when type is NodePort)`, and passes the
engine `APOTHECARY_EXPOSE_PORTS=30100-30139`, `APOTHECARY_EXPOSE_HOST` (from
`access.host`, may be empty), `APOTHECARY_EXPOSE_SERVICE=<tcp service name>`,
`APOTHECARY_SSH_LISTEN=:2222`, `APOTHECARY_PUBLIC_URL` (from the ingress host,
or `http://<host>:8080` when the HTTP Service is direct). Remove the old
`ssh:` and `ingress:` top-level blocks and `service:`; keep backward-compatible
aliases only if trivial — this is a breaking release anyway.

### Engine changes (`internal/workspace/portrelay`, config, wiring)

1. **Pool allocation.** When `APOTHECARY_EXPOSE_PORTS` is set, `Manager.bind`
   picks a free port from the pool instead of `:0`; the persisted allocation
   (`run_exposed_ports`) keeps working unchanged, so a run keeps its port
   across engine restarts. Pool exhausted → a clear error on the exposure
   ("all 40 exposure ports are in use; raise access.tcp.ports.count"), shown
   on the run page. Unset (dev loop, tests) → today's random-port behavior.
2. **Advertised host discovery.** If `APOTHECARY_EXPOSE_HOST` is empty and
   `APOTHECARY_EXPOSE_SERVICE` is set, the engine reads that Service in its
   own namespace (`status.loadBalancer.ingress[0].hostname|ip`; for NodePort,
   the node's ExternalIP else InternalIP) and re-checks until it has one —
   AWS assigns the NLB name after creation. Add `get` on `services` (and
   `nodes` for NodePort, a ClusterRole) to `rbac.yaml`. The same discovered
   host is the SSH door's advertise address unless `APOTHECARY_SSH_ADVERTISE`
   overrides it.
3. **Protocol-aware URLs.** An exposure carries a scheme:
   devcontainer `portsAttributes["<port>"].protocol` (`http`/`https`) wins;
   else a small table of well-known ports (5432 `postgres://`, 6379
   `redis://`, 3306 `mysql://`, 27017 `mongodb://`, 9092/9093/19092 kafka →
   bare `host:port`, 22 `ssh`); anything else is a bare `host:port` (`tcp`).
   Do not sniff traffic to guess. The API's
   `Exposure` gets `protocol` and `address` (`host:port`) beside `url`;
   the run page shows a copy button for the address and an open link only
   for http/https. Regenerate the API (`task api`).
4. **Routing choice per port.** If `appsDomain` is set and the port is
   http/https, the URL is the apps-domain one (HTTPS, behind sign-in);
   otherwise the pool. Both can coexist for the same run.
5. **Refuse unreachable exposure.** Running under Kubernetes with neither a
   pool nor an apps domain configured → exposing a port is an error with the
   fix in the message, and the System hub shows "port exposure not
   configured". No more URLs that cannot work.
6. **Readiness.** The engine's readiness probe stays `/healthz`; the pool
   listeners are created lazily, so an unallocated pool port refuses
   connections — expected, document it.

Security note for docs and `values.yaml`: pool ports carry raw TCP with **no
authentication** — a workspace's Postgres with its dev password is reachable
by anyone who can reach the port. The chart's schema rejects an
internet-facing configuration (AWS annotation
`service.beta.kubernetes.io/aws-load-balancer-scheme: internet-facing`)
without `allowedCIDRs`. Apps-domain URLs are behind sign-in; prefer them for
web apps on public deployments.

Known caveat to document (not to fix now): Kafka/Redpanda clients follow the
broker's advertised listener, so a broker advertising `localhost:9093` is not
usable from outside even with the port reachable. The fix is in the repo's
compose file (advertise `<host>:<pool port>`); a follow-up could inject
`APOTHECARY_EXPOSE_HOST` and the allocation into the workspace env.

---

## Part 4 — Deployment shapes (the `ci/` examples + install docs)

Each shape is one values file and one short doc section. All share: sysbox
installed on the nodes that run sandboxes (`workspaces.runtimeClassName:
sysbox-runc`), Postgres (bundled or external), `existingSecret` or
`masterKey` + `postgres.password`.

### A. Dev box, LAN or public IP (k3s) — `devbox-values.yaml`

k3s's built-in ServiceLB publishes LoadBalancer Services on every node IP.

```yaml
access:
  host: ""                                   # discovered: the node's IP
  http: { service: { type: LoadBalancer } }  # http://<ip>:8080
  tcp:  { service: { type: LoadBalancer } }  # ssh <ip>:2222, pool 30100-30139
```

Optional domain: set `access.host: apothecary.example.com` (A record → the
box) and, for HTTPS, `access.http.ingress` with Traefik + cert-manager and
`appsDomain` with a wildcard record.

### B. Tailnet only (k3s or EKS + Tailscale Kubernetes operator) — `tailscale-values.yaml`

The operator gives each Service/Ingress its own tailnet machine and MagicDNS
name, so Apothecary gets its own name independent of the host's (another app
on the same host keeps the host's name). No wildcard certs on MagicDNS, so no
apps domain; web apps use the pool over the tailnet.

```yaml
access:
  host: apothecary-tcp.<tailnet>.ts.net
  http:
    ingress:
      enabled: true
      className: tailscale                   # https://apothecary.<tailnet>.ts.net
      host: apothecary
  tcp:
    service:
      type: LoadBalancer
      loadBalancerClass: tailscale
      annotations: { tailscale.com/hostname: apothecary-tcp }
```

Docs: install the operator (OAuth client, tags), enable HTTPS on the tailnet.

### C. EKS, private — `eks-private-values.yaml`

Requires the AWS Load Balancer Controller and the EBS CSI driver.

```yaml
access:
  http:
    ingress:
      enabled: true
      className: alb
      host: apothecary.internal.example.com
      annotations:
        alb.ingress.kubernetes.io/scheme: internal
        alb.ingress.kubernetes.io/certificate-arn: <acm wildcard cert>
    appsDomain: apps.internal.example.com    # Route 53 wildcard → the ALB
  tcp:
    service:
      type: LoadBalancer
      annotations:
        service.beta.kubernetes.io/aws-load-balancer-type: external
        service.beta.kubernetes.io/aws-load-balancer-nlb-target-type: ip
        service.beta.kubernetes.io/aws-load-balancer-scheme: internal
      allowedCIDRs: [10.0.0.0/8]
postgres: { enabled: false }                 # RDS; DSN in existingSecret
engine:   { runsDir: { storageClass: gp3 } }
```

Reached over VPN, peering, or a Tailscale subnet router. `access.host` empty →
the engine discovers the NLB DNS name; set it to a Route 53 alias for a nicer
name.

### D. EKS, public — `eks-public-values.yaml`

C with `scheme: internet-facing` on both, and `allowedCIDRs` mandatory
(schema-enforced).

### EKS notes for the docs

- Sandbox nodes need sysbox: a managed node group on an Ubuntu EKS AMI with
  `sysbox-deploy-k8s`, labeled/tainted for sandboxes; check sysbox's
  supported OS/kernel list against the AMI. Bottlerocket is not supported.
- EBS volumes are single-AZ: a run's pod is pinned to its volume's AZ. Use a
  `WaitForFirstConsumer` gp3 StorageClass.
- Images can stay on GHCR or be mirrored to ECR via `image.overrides`;
  `imagePullSecrets` is optional.

### Host bootstrap for dev boxes

Upstream the operator's existing k3s scripts (node setup, sysbox install,
sysbox smoke test, the `hostUsers:false` admission policy) into
`deploy/k3s/` — no secrets, idempotent, `--help`. The README's install
section becomes: 1) `deploy/k3s/install.sh` (or "bring a cluster"), 2) pick
an example values file, 3) `helm install … oci://…`, 4) open the URL
`NOTES.txt` prints and claim the instance.

---

## Part 5 — Order of work and PRs

1. `refactor!: remove the docker provisioner and compose deployment` (Part 1).
2. `refactor(chart)!: move the chart to charts/apothecary, schema, helm-docs,
   OCI publish` (Part 2, without `access`).
3. `feat(workspace)!: port pool, address discovery, protocol-aware exposure`
   (engine side of Part 3) + `feat(chart)!: access block and tcp Service`.
4. `docs: install guides for dev box, tailnet and EKS` + `deploy/k3s/`
   scripts (Part 4).

Each PR keeps CI green on its own. Integration coverage: extend
`test:integration:k8s` (k3d) to expose a port from a test workspace and
connect to it through the NodePort pool, and to ssh through the door.

## Acceptance

- `helm template` renders cleanly for every `ci/*-values.yaml`; schema
  rejects a public NLB without `allowedCIDRs` and a NodePort pool outside
  30000-32767.
- On k3s (shape A): fresh install with only master key + Postgres password
  set; UI at `http://<ip>:8080`; a run exposing 3050 (http) and 5432 shows
  `http://<ip>:301xx` and `postgres://<ip>:301yy`; both connect from another
  machine; `ssh <workspace>@<ip> -p 2222` works.
- On the tailnet (shape B): same, via `apothecary.<tailnet>.ts.net` (HTTPS
  UI) and `apothecary-tcp.<tailnet>.ts.net`.
- EKS (shape C) validated by rendering and by one manual install before the
  release that claims it; the docs say which.
- No reference to the docker provisioner or docker-compose deployment
  remains (`grep -ri "docker-compose\|kind.*docker\|host_docker"` is clean
  except for the inner-dockerd and devcontainer compose wording).
- `task fmt lint test:unit cover:go` green; `docs/IMPLEMENTATION_NOTES.md`
  has an entry per decision above.
