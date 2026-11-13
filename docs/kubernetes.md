# Kubernetes

The cluster side in full; the README has the summary. A fresh Ubuntu box is set up
end to end by `deploy/k3s/install.sh` ([its guide](../deploy/k3s/README.md)).

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

## Cluster requirements

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
  also what opens the [daemon door](../README.md#docker-in-workspaces) under
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

## A local cluster

With [k3d](https://k3d.io) and [helm](https://helm.sh) installed:

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
