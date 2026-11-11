# Apothecary on a fresh box

`install.sh` turns a fresh Ubuntu machine into a one-node Kubernetes cluster
(k3s) running Apothecary. Every run's sandbox is unprivileged under sysbox.

```sh
git clone https://github.com/intradeus/apothecary && cd apothecary
sudo deploy/k3s/install.sh                     # LAN: http://<this box>:8080
sudo deploy/k3s/install.sh --domain apothecary.example.com --email you@example.com   # HTTPS
```

Then open the address it prints, claim the instance (the first account is
the admin), and install the `apothecary` client on your machine from the same
release.

Running the script again is safe. Each step checks what is already in place,
so a rerun repairs, or picks up a newer chart (`--chart-version`), without
starting over. `--help` lists the options.

## Before you run it

- **Ubuntu 24.04 or later**, amd64 or arm64, kernel 6.3 or later. Debian
  works too, without the AppArmor steps. Run it as root, on a machine that
  runs nothing else of note.
- **Ports:**
  - The LAN setup listens on port **8080**, over plain HTTP: keep it to your
    private network, with a firewall or your VPN.
  - With `--domain`, ports **80 and 443** must be reachable from the internet
    (Let's Encrypt checks port 80), and the domain's DNS record must point
    at this box.
- **Disk:** the tools bundle alone is about 2.3 GB per release, and each
  run's workspace gets its own volume. Plan for 50 GB or more.

## What it does, step by step

1. **Preflight.** Checks it runs as root, the architecture, the OS version,
   and the kernel version (pods with a user namespace of their own need 6.3).
   It works out the address people reach the box at (`--address`
   overrides it).
2. **Kernel.**
   - **WireGuard:** loads the `wireguard` module, and makes it load at every
     boot (`/etc/modules-load.d/apothecary.conf`). Every sandbox reaches its
     project's egress gateway through a WireGuard tunnel.
   - **AppArmor and sysbox's mounts (Ubuntu 25.04 and later):** sysbox
     mounts each sandbox's virtual `/proc` and `/sys` with `fusermount3`,
     which AppArmor confines. The script adds sysbox's mounts to the
     profile's local override.
   - **User namespaces (Ubuntu 24.04 and later):** Ubuntu lets only root
     programs create user namespaces. Under sysbox nothing in a sandbox is
     root on the box, so tools that sandbox themselves inside a workspace
     (bubblewrap, Chrome, rootless podman) need that lifted
     (`/etc/sysctl.d/60-apothecary-userns.conf`).
     `--keep-userns-restriction` leaves it in place.
3. **sysbox.** Installs the `sysbox-ce` package from sysbox's GitHub
   releases (default 0.7.0, `--sysbox-version`, or `SYSBOX_DEB_URL` for
   another `.deb`), and checks the service runs. sysbox runs each sandbox
   unprivileged, in a user namespace, while still nesting a docker daemon.
4. **k3s.** Installs k3s from its release channel (default `stable`) and
   waits for the node. It refuses Kubernetes older than 1.33 (user
   namespaces for pods are on by default from 1.33) and containerd older
   than 2.0.5 (the first to run sysbox with them). The kubeconfig is
   `/etc/rancher/k3s/k3s.yaml`, readable by root only.
5. **sysbox in k3s.** Adds a `sysbox-runc` runtime to k3s's containerd
   (through the config template k3s reads, so k3s keeps its own settings),
   and creates the `sysbox-runc` RuntimeClass the chart's sandboxes use.
6. **Smoke test.** Starts the same kind of pod every run's sandbox is:
   `docker:29-dind` under sysbox, with a user namespace of its own and no
   privilege. It passes when the docker daemon inside answers. If it fails,
   the script prints the pod's events and logs and stops: sandboxes would
   fail the same way. `--cluster-only` stops here.
7. **Apothecary.**
   - **Helm:** installs it if it is missing.
   - **HTTPS (with `--domain`):** installs cert-manager and a Let's Encrypt
     `ClusterIssuer`.
   - **Secrets, once:** generates the master key (`apothecaryd keygen`, run
     once as a pod with the release's own engine image) and a database
     password. Both are kept in `/root/.apothecary/install.env`, readable
     by root only. **Keep a copy:** the master key opens every secret
     Apothecary stores, and a new one cannot read them.
   - **The chart:** installs it from `oci://ghcr.io/intradeus/charts/apothecary`
     with its defaults (sysbox sandboxes, the bundled Postgres).
     - Without `--domain`, the UI is a LoadBalancer Service, which k3s
       publishes on port 8080 of the box.
     - With it, the UI is a Traefik ingress with the certificate.

## Afterwards

- **On your machine:** `apothecary login <the address>`, then
  `apothecary ssh-config`. A run's ports come to your machine with
  `apothecary forward <run>`.
- **The cluster:** `sudo k3s kubectl get pods -A`.
- **Upgrade Apothecary:** run the script again with `--chart-version <X.Y.Z>`,
  then `apothecary update` on your machine.
- **More nodes:** join k3s agents
  ([docs.k3s.io](https://docs.k3s.io/quick-start)), and run steps 2, 3 and 5
  on each. The RuntimeClass then needs a `scheduling.nodeSelector` naming the
  nodes that have sysbox.
