# A fresh box, from nothing: deploy/k3s/install.sh (2026-11-11)

- **There were no scripts to upstream.** Nothing in the repository set up a
  node, so the install was written from the documented requirements. It has
  not been run end to end yet: the first run on a fresh box is its test.
- **One idempotent script** (bash, shellcheck-clean), in this order:
  1. preflight;
  2. wireguard, the sysbox `fusermount3` AppArmor override, the
     unprivileged-userns sysctl;
  3. sysbox from its `.deb`;
  4. k3s from a release channel, refusing Kubernetes < 1.33 and
     containerd < 2.0.5;
  5. a `sysbox-runc` runtime, through k3s's `config-v3.toml.tmpl`, which
     keeps k3s's own settings ("base") and the same `SystemdCgroup` as runc;
     then the RuntimeClass;
  6. a smoke-test pod shaped like a sandbox (sysbox, `hostUsers: false`,
     `docker:29-dind`, no privilege);
  7. Helm, cert-manager with `--domain`, then the chart.
- **No admission policy for `hostUsers: false`** (a deviation from the
  plan). The engine already sets it on every sandbox it creates under a
  runtime class.
- **The master key comes from the release's own `apothecaryd keygen`,** run
  once as a pod, so its format is the engine's by construction. It is kept
  with the database password in `/root/.apothecary/install.env` (0600), and
  reruns reuse them.
- **Two shapes.**
  - LAN: the UI as a LoadBalancer Service on port 8080 of the box (k3s's
    ServiceLB), plain HTTP, `access.publicURL` set to that address.
  - `--domain`: a Traefik ingress with a cert-manager Let's Encrypt
    certificate (HTTP-01).
