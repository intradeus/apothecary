# Kubernetes (TODO 30)

Workspaces, and the engine itself, deploy on any Kubernetes cluster (k3s
locally through k3d, full k8s in production) with the docker path's features
intact. The plan was reviewed and locked with the user on 2026-10-27.

### Shape: host × definition

`internal/adapters/devcontainer` became `internal/adapters/workspace`, cut
along two axes:

- **Host** (`host.go`) is where the sandbox runs: `dockerHost` (the DinD
  container on the engine's daemon — a pure move; the sandbox lifecycle
  stays on `Adapter`, which the unit tests drive) or `k8sHost` (a pod).
- **Definition** (`definition.go`) is what the repo asks for, detected per
  repo after the clone and recorded per env (`<run>/definition`):
  `devcontainerDef` today. A future `.apothecary/apothecary.json` is a second
  definition, not a third provisioner.

The provisioner kinds are the hosts: `docker` (renamed from `devcontainer`;
unreleased, so no alias — recreate the integration) and `kubernetes`. The
unit tests kept their logic unchanged through the move (one compat helper
file for `configFiles`/`configDigest`).

Path parity is what keeps the seam small: a sandbox pod mounts its volume at
`<RunsDir>/<name>`, so the checkout, the shim dir and the devcontainer CLI's
bind sources are the same strings on both hosts. Only the filesystem they
name differs: `osFS` on docker, `execFS` (one `kubectl exec` script per call)
on Kubernetes. The engine keeps its own `<RunsDir>/<name>` for exec.log and
markers either way.

### Kubernetes host

- kubectl CLI through the adapter's `CmdRunner` seam (`internal/kube`), no
  client-go. `PodRunner` maps exit codes: kubectl prints `command terminated
  with exit code N` for a remote failure (stripped from stderr and the tee);
  a markerless failure matching kubectl's own transport errors is a
  `TransportError`, which `runWith` retries like a failed spawn.
- The clone runs in the pod. The credential rides stdin (a one-line script,
  then the credential) into a 0600 file the clone removes — never argv or
  environment.
- The pod: `shim-seed` (copies the shim from the `apothecary/shim` image,
  writes `shim.hash`), `wg` (native sidecar: the tunnel, then a watchdog),
  `relay` (native sidecar: `shim relay --serve :7070`, the dial-in mux),
  then `sandbox` (`apo-dockerd`: the dind entrypoint with a CGNAT address
  pool and, with a gateway, its DNS, mirror, build cache and the tunnel MTU).
- Dial-in: the engine reaches `sidecar.sock` / `app-<port>.sock` through the
  mux — `APO1 <token> <socket>\n`, answered `OK\n` once the socket connects
  (so "nothing serves it yet" is distinguishable). In-cluster by pod IP,
  out-of-cluster by a supervised `kubectl port-forward`. Never one
  `kubectl exec` per connection: a page load would cost seconds.
- The engine URL is provisioner-supplied (`ports.EngineURLProvider`): the
  docker host keeps loopback parity, the Kubernetes host gives the engine's
  Service. `run.env`, bootstrap, rebuild, envcheck's tunnel probe and
  `waitReady` all use it.
- Stats read the sandbox container's cgroup v2 (`cpu.stat`, `memory.*`,
  `pids.current`, `cpu.max`) and `eth0` from `/proc/net/dev`; CPU% needs two
  readings, so the first sample after an engine start is a gap.
- A shim whose `shim.hash` differs from the engine's `workspace.ShimSourceHash`
  fails a fresh provision (publish the shim image of the engine's version);
  on Reattach it is only logged, since the next pod start reseeds it.

### Egress: one gateway, one tunnel

The gateway daemon and image are the same on both hosts, and on both a run's
egress reaches it over a per-run **WireGuard** peer. What differs is the
network the tunnel itself rides:

- docker: the run's `--internal` network, which also carries DNS, the image
  mirror and the build cache (addressed to the gateway, so the run's subnet
  attributes them; the tunnel address is attributed too). The engine brings
  `wg0` up in the sandbox with a throwaway gateway-image container sharing its
  network namespace (`egressgw/wg_docker.go`: the dind image has no `wg`).
  Identities are files under `<RunsDir>/gw/<project>/` (the gateway's key,
  `peers/<run>.json`). It used to be a plain next hop across that bridge
  (`default via <gateway>`), which put internet-bound frames on the host
  bridge: once anything loaded `br_netfilter` (a local k3s's kube-router, via
  its `-m physdev` rules), the host's iptables judged them and Docker's
  internal-network rule dropped every one — all egress dead, DNS fine;
- Kubernetes: a per-run peer. The pod's default route is `wg0`;
  the network policy allows only UDP 51820 to the project's gateway and TCP to
  the engine. Each peer is allowed exactly its `/32` (`10.90.0.0/16`), which
  is also its `routes.json` subnet, so attribution is the same lookup — and
  unforgeable. Keys are X25519 from stdlib `crypto/ecdh`; run identities live
  in `apo-wg-<run>` secrets and are reused across re-provisions.

Gotchas the design review caught, all handled in `apo-wg-up`:
- the gateway's endpoint needs its own `/32` route out `eth0`, or the tunnel's
  outer packets route into `wg0` and the handshake never completes;
- the CNI's pod-network routes must stay (the engine's dial-in replies use
  them; the network policy is what fences them);
- MTU is `eth0 − 60` (IPv4 outer header), computed at runtime;
- a replaced gateway pod strands the UDP conntrack entry behind its Service
  IP: the watchdog rebinds the listen port after three minutes without a
  handshake.

The engine↔gateway channel is now one **control API** on both hosts
(`workspace/gateway/control.go`, bearer token): policy and run routes in,
denials, audit and build-cache facts out. Docker publishes it on the engine
host's loopback (`-p 127.0.0.1::9041`; host→container works on hardened
hosts); Kubernetes reaches the gateway's Service. The bind-mounted
`gw/<project>` file store is gone; the gateway keeps its state on its own
volume (`--state-dir`) and generates its registry configs itself. A run link
never reaches the control port (an nft `input` drop for non-uplink
interfaces). Docker gateways of the previous shape are recreated on first use
(`apothecary.gw.shape=2`), their runs re-attached at their old addresses.

`--protect-cidrs` (k3s defaults `10.42.0.0/16,10.43.0.0/16,169.254.0.0/16`)
closes the hole a cluster opens: the gateway's uplink is the pod network, so
without it an allowlisted IP range or a deny-mode project would reach cluster
services and cloud metadata. It is enforced twice — an nft forward drop before
either mode's accepts, and a denial in the TLS/HTTP filter — and the
gateway's own network policy excludes the same ranges.

### Browser, apps, deployment

- Browser pods have no ingress and reach only the engine's `SessionMux`
  (`:7071`, the same preamble with a per-session token) and the cluster DNS.
- `portrelay.RouteByHost`: exposures become `<port>-<run>.<apps domain>`,
  served by the engine's HTTP server. App hosts are other origins, so access
  is handed over per host (`internal/httpapi/apps.go`): a one-time code from
  the UI host to the run's owner, traded for a host-only cookie that never
  reaches the app. code-server (`--auth none`) is therefore behind the user's
  session.
- Images are files now (`workspace/images/*`, `deploy/images/sandbox`), built
  by `wsimage` on a docker host and published by `deploy/images.sh` (six
  images per release since the tools bundle; CI and `release.sh` use it).
- Packaging is a Helm chart (`deploy/helm/apothecary`; `values-k3d.yaml` for
  local). The engine runs in-cluster with `APOTHECARY_K8S_TRANSPORT=direct`;
  its RBAC is a Role in the workspaces namespace only.

### Verified live (k3d, k3s v1.35.5, 2026-10-27)

`TestLiveK8sLifecycle` (`task k8s:test-live`) passes with the gateway on:
provision ≈ 55–63 s (in-pod clone over the gateway's raw-port escape hatch,
`devcontainer up` of a `debian:bookworm-slim` image with, at the time, the
code-server feature — now the tools bundle mounted), exec and exit codes, binary file round trip, declared ports, the
engine Service from inside the workspace, the sidecar and an exposed port
through the dial-in mux over `kubectl port-forward`, cgroup stats, a denied
host recorded and attributed to the run, a live allow passing on the next
request, a route around the tunnel dropped by the network policy, the audit
trail, reattach after an adapter close, and Destroy. `wg0` came up on the
gateway without a manual `modprobe` (the kernel autoloads it), MTU 1390 on
flannel's 1450. The chart installs on k3d (`task k8s:deploy`) and a
`kubernetes` integration with every field empty passes its Test in-cluster.

Bugs the live runs found, fixed:
- the control-port `input` drop also dropped loopback, which is how
  `kubectl port-forward` arrives — now `iifname != { EXT, "lo" }`;
- the gateway Deployment ignored the image pull policy (a rebuilt `:local`
  never reached the node);
- kubectl skips its in-cluster fallback whenever a config override such as
  `--request-timeout` is passed, and dials `localhost:8080` (the engine
  itself). `kube.Kubeconfig` now writes an explicit in-cluster kubeconfig
  (server, CA file, token file — the token by path, since it rotates).

### Deliberate residuals

- Privileged sandbox pods are host root, as on docker; sysbox
  (`runtime_class`, privileged off) is the unprivileged path, designed but not
  live-verified on Kubernetes yet.
- Every exec costs an API round trip. The ~fifteen feature-materialisation
  execs a provision used to cost are gone with the tools bundle (TODO 39);
  one read of the seeded bundle's hash remains.
- An out-of-cluster engine (`go run` against k3d) needs an IP engine URL and
  `engine_cidr` for the policies; it is the test path, not a deployment.
- One engine per workspaces namespace.
