# Apothecary Helm chart

Apothecary — agent runs in per-run Docker-in-Docker sandbox pods, egress-filtered by a per-project gateway, reached through one HTTPS address.

![Version: 0.0.0-dev](https://img.shields.io/badge/Version-0.0.0--dev-informational?style=flat-square) ![AppVersion: latest](https://img.shields.io/badge/AppVersion-latest-informational?style=flat-square)

The chart deploys the engine (and, optionally, a Postgres). The engine
creates every workspace object itself, at runtime, in the workspaces
namespace: one sandbox pod per run, one egress gateway and one browser per
project.

People reach Apothecary through **one HTTPS address**. The UI, the API and the
`apothecary` CLI's tunnels all go through it:
- `apothecary forward <run>` brings a run's ports to your localhost.
- `ssh <workspace>.apothecary` reaches a workspace over SSH.

No other port is published.

## Install

```sh
helm install apothecary oci://ghcr.io/intradeus/charts/apothecary \
  --version <X.Y.Z> -n apothecary --create-namespace -f my-values.yaml
```

Start `my-values.yaml` from the example that matches your cluster, in
[`ci/`](ci/):

| Example | For |
|---|---|
| `devbox-values.yaml` | A k3s box with sysbox, on a domain you own (Traefik and cert-manager) or a LAN address |
| `private-values.yaml` | A cluster reachable only over a VPN or private network, through any ingress class |
| `eks-private-values.yaml` | EKS inside the VPC: an internal ALB, RDS, gp3 |
| `eks-public-values.yaml` | EKS on the internet: the same, internet-facing |
| `k3d-values.yaml` | Local development (`task k8s:deploy`) |

Then open the address `helm install` prints, claim the instance, and install
the `apothecary` CLI from the release.

## Before you install

- **Sandboxes run unprivileged under sysbox** (`workspaces.runtimeClassName:
  sysbox-runc`). Install sysbox on the nodes that run them. Its RuntimeClass
  places sandbox pods on those nodes. Privileged sandboxes
  (`workspaces.privileged: true`) are for a cluster of your own only.
- **Nodes need the wireguard kernel module**, in-tree since Linux 5.6.
- **Workspace volumes need a local or block storage class.** The inner
  docker daemon cannot run on NFS.
- **Set `workspaces.protectCIDRs`** to your cluster's pod and service ranges,
  plus link-local.

## Requirements

Kubernetes: `>=1.29.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| image.repository | string | `"ghcr.io/intradeus/apothecary"` | Prefix of the five release images: apothecaryd, sandbox, shim, tools, egress-gw (deploy/images.sh). The project browser is Microsoft's Playwright MCP image, pinned by the engine. |
| image.tag | string | `""` | Image tag. Empty: the chart's appVersion, so a chart version pins every image. |
| image.pullPolicy | string | `""` | Pull policy. Empty: Always for a reused tag (latest, local), IfNotPresent for a release tag. |
| image.overrides | object | `{"apothecaryd":"","browser":"","gateway":"","sandbox":"","shim":"","tools":""}` | Per-image overrides (full references), e.g. a mirror such as ECR. browser must be the same image as the engine's pin (its session command depends on the image's layout). |
| imagePullSecrets | list | `[]` | Pull secrets for private registries or mirrors. |
| masterKey | string | `""` | The master key that seals every stored secret (`apothecaryd keygen`). Prefer existingSecret in production. |
| existingSecret | string | `""` | A Secret holding APOTHECARY_MASTER_KEY and APOTHECARY_POSTGRES_DSN; when set, the chart creates no Secret of its own. |
| externalDSN | string | `""` | The Postgres DSN when the bundled Postgres is off and no existingSecret is set. |
| postgres.enabled | bool | `true` | A single-instance Postgres in the release's namespace. For production, disable it and use a managed one (existingSecret or externalDSN). |
| postgres.image | string | `"postgres:16"` | Postgres image. |
| postgres.user | string | `"apothecary"` | Database user. |
| postgres.password | string | `""` | Database password; required when enabled. |
| postgres.database | string | `"apothecary"` | Database name. |
| postgres.storage | string | `"10Gi"` | Volume size. |
| postgres.storageClass | string | `""` | Storage class; empty for the cluster default. |
| access.publicURL | string | `""` | The UI's own address as browsers reach it (https://apothecary.example.com). Derived from the ingress when that is enabled; set it when the HTTP Service is reached directly. Required with an apps domain. |
| access.http.service.type | string | `"ClusterIP"` | The engine's Service type: ClusterIP behind an ingress, or LoadBalancer / NodePort to reach it directly (a LAN dev box). |
| access.http.service.annotations | object | `{}` | Service annotations (a cloud load balancer's, a VPN operator's). |
| access.http.ingress.enabled | bool | `false` | Serve the UI through an Ingress. |
| access.http.ingress.className | string | `""` | Any ingress class: traefik, nginx, alb, a VPN operator's. The chart passes it, the annotations and tls through untouched. |
| access.http.ingress.annotations | object | `{}` | Ingress annotations (e.g. alb.ingress.kubernetes.io/target-type: ip behind an AWS ALB, cert-manager's issuer). |
| access.http.ingress.host | string | `""` | The UI's hostname; required when enabled. |
| access.http.ingress.tls | list | `[]` | Ingress TLS, as the controller wants it. |
| access.http.scheme | string | `"https"` | http or https: how browsers reach the ingress host and the apps domain. |
| access.http.appsDomain | string | `""` | Optional: browser links to web apps a run serves, at <port>-<run>.<appsDomain>, behind the owner's sign-in. Needs a wildcard DNS record and certificate on the ingress, and a domain other than the UI's. Without it, ports are reached with `apothecary forward`. |
| access.http.port | string | `""` | The port browsers use for the ingress when it is not the scheme's default (k3d: 8090). |
| workspaces.namespace | string | `"apothecary-workspaces"` | Where sandboxes, gateways and browsers live. |
| workspaces.createNamespace | bool | `true` | Create that namespace with the chart. |
| workspaces.podSecurity | string | `"privileged"` | The namespace's Pod Security level. Gateways need NET_ADMIN, so privileged even with unprivileged sandboxes. |
| workspaces.runtimeClassName | string | `"sysbox-runc"` | The RuntimeClass that nests each run's dockerd unprivileged (sysbox, installed on the nodes that run sandboxes). The RuntimeClass's own scheduling (nodeSelector, tolerations) places sandbox pods on those nodes. |
| workspaces.privileged | bool | `false` | Run sandboxes privileged instead. Sandbox root is then root on its node: for a cluster of your own without sysbox (k3d, a dev box), never a shared one. Needed when runtimeClassName is empty. |
| workspaces.protectCIDRs | string | `"10.42.0.0/16,10.43.0.0/16,169.254.0.0/16"` | Destinations no workspace may reach through its gateway: the cluster's pod and service ranges and link-local metadata. The defaults are k3s's; set your cluster's (EKS: the VPC CIDR). |
| workspaces.clusterDomain | string | `"cluster.local"` | The cluster's DNS domain. |
| engine.runsDir.size | string | `"20Gi"` | The engine's runs volume: uploads, snapshots, exec logs, markers. |
| engine.runsDir.storageClass | string | `""` | Storage class; empty for the cluster default. |
| engine.portAutoExpose | bool | `false` | Publish ports the workspace starts listening on without a click (with an apps domain). |
| engine.uploads.maxBytes | string | `""` | Upload size cap in bytes; empty for 25 MiB. |
| engine.uploads.mimeAllow | string | `""` | Allowed MIME types, comma-separated; empty for the engine's list. |
| engine.uploads.snapshotMaxBytes | string | `""` | Cap for engine captures in bytes; empty for the upload cap. |
| engine.extraEnv | object | `{}` | Any other APOTHECARY_* variable, e.g. APOTHECARY_WORKSPACE_DOCKER: "on" to hand a run's docker daemon to a docker-outside-of-docker devcontainer under privileged sandboxes too (auto, the default, does so only unprivileged). |
| engine.resources | object | `{}` | The engine container's resources. |
| engine.nodeSelector | object | `{}` | The engine pod's node selector. |
| engine.tolerations | list | `[]` | The engine pod's tolerations. |
| otel.endpoint | string | `""` | OTLP endpoint for traces, metrics and logs; empty exports nothing. |
| otel.protocol | string | `"grpc"` | grpc or http/protobuf. |
| otel.headers | string | `""` | OTEL_EXPORTER_OTLP_HEADERS. |
| otel.resourceAttributes | string | `""` | OTEL_RESOURCE_ATTRIBUTES. |
| log.format | string | `"json"` | json or text. |
| log.level | string | `"info"` | debug, info, warn or error. |
| grafanaUrl | string | `""` | Where the admin's System hub links for platform dashboards; empty hides the link. |
