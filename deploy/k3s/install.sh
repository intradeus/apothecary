#!/usr/bin/env bash
# Apothecary on a fresh Linux box: k3s, sysbox, and the chart, from nothing.
# Every step checks what is already there, so running it again repairs or
# upgrades instead of starting over. deploy/k3s/README.md explains each step.
set -euo pipefail

usage() {
  cat <<'EOF'
usage: sudo deploy/k3s/install.sh [options]

Turns a fresh Ubuntu box (24.04 or later, amd64 or arm64) into a one-node
Kubernetes cluster that runs Apothecary, with every run's sandbox
unprivileged under sysbox.

  --domain <host>          serve Apothecary at https://<host>, with a Let's
                           Encrypt certificate (needs --email, a DNS record
                           pointing <host> at this box, and ports 80 and 443
                           reachable). Without it: plain HTTP on port 8080 of
                           this box's address, for a private network only.
  --email <address>        the Let's Encrypt account (with --domain)
  --address <ip>           the address people reach this box at (default: its
                           first address)
  --chart-version <X.Y.Z>  the Apothecary release (default: the newest)
  --k3s-channel <channel>  the k3s release channel (default: stable; must be
                           Kubernetes 1.33 or later)
  --sysbox-version <X.Y.Z> the sysbox release (default: deploy/sysbox/sysbox.env's,
                           checked against its pinned sha256; 0.7.1 or later).
                           Another version, or SYSBOX_DEB_URL for another .deb,
                           needs SYSBOX_DEB_SHA256: the release page lists it
  --kata                   also install Kata Containers (a VM per sandbox;
                           amd64 with KVM, /dev/kvm): workshops may then
                           choose kata beside sysbox. Kata sandboxes must be
                           sized (System → Limits) before one is chosen.
  --keep-userns-restriction
                           leave Ubuntu's AppArmor restriction on
                           unprivileged user namespaces in place (tools that
                           sandbox themselves inside a workspace then fail)
  --cluster-only           stop after the smoke test: no Apothecary
  -h, --help               this text

Steps: preflight, kernel, sysbox (and keeping needrestart from restarting
it, containerd or k3s), Kata (with --kata), k3s, the runtimes in k3s, smoke
tests, then Apothecary (cert-manager first with --domain). Logs go to stdout; the generated
secrets to /root/.apothecary/install.env (root only).
EOF
}

HERE=$(cd "$(dirname "$0")" && pwd)
SYSBOX_DIR="$HERE/../sysbox" # its pins (sysbox.env), shared with the test rig
[ -f "$SYSBOX_DIR/sysbox.env" ] || { echo "install: run from a checkout (deploy/sysbox/sysbox.env is missing)" >&2; exit 1; }
# shellcheck source=deploy/sysbox/sysbox.env
. "$SYSBOX_DIR/sysbox.env"
PINNED_SYSBOX="$SYSBOX_VERSION"

DOMAIN="" EMAIL="" ADDRESS="" CHART_VERSION="" K3S_CHANNEL="stable"
KEEP_USERNS=0 CLUSTER_ONLY=0 KATA=0
while [ $# -gt 0 ]; do
  case "$1" in
    --domain) DOMAIN="${2:?--domain needs a host}"; shift 2 ;;
    --email) EMAIL="${2:?--email needs an address}"; shift 2 ;;
    --address) ADDRESS="${2:?--address needs an ip}"; shift 2 ;;
    --chart-version) CHART_VERSION="${2:?--chart-version needs X.Y.Z}"; shift 2 ;;
    --k3s-channel) K3S_CHANNEL="${2:?--k3s-channel needs a channel}"; shift 2 ;;
    --sysbox-version) SYSBOX_VERSION="${2:?--sysbox-version needs X.Y.Z}"; shift 2 ;;
    --kata) KATA=1; shift ;;
    --keep-userns-restriction) KEEP_USERNS=1; shift ;;
    --cluster-only) CLUSTER_ONLY=1; shift ;;
    -h | --help) usage; exit 0 ;;
    *) echo "unknown option: $1 (--help)" >&2; exit 2 ;;
  esac
done

KATA_DIR="$HERE/../kata" # its pins (kata.env) and configure.sh, shared with the test rig
CHART=oci://ghcr.io/intradeus/charts/apothecary
NAMESPACE=apothecary
STATE_DIR=/root/.apothecary
KUBECONFIG=/etc/rancher/k3s/k3s.yaml
export KUBECONFIG # this box's own cluster, never anyone's ~/.kube/config

step() { printf '\n==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

# version_ge A B: is version A at least B (dotted numbers)?
version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]; }

# --- 1. preflight -------------------------------------------------------------
step "Preflight"
[ "$(id -u)" = 0 ] || die "run as root (sudo)"
[ -n "$DOMAIN" ] && [ -z "$EMAIL" ] && die "--domain needs --email (the Let's Encrypt account)"
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) die "unsupported architecture $(uname -m): amd64 or arm64" ;;
esac
# shellcheck disable=SC1091 # the running system's own
. /etc/os-release
case "${ID:-}" in
  ubuntu) version_ge "${VERSION_ID:-0}" 24.04 || die "Ubuntu ${VERSION_ID} is too old: 24.04 or later" ;;
  debian) note "Debian: sysbox supports it; the AppArmor steps are skipped" ;;
  *) note "${PRETTY_NAME:-this OS} is untested: sysbox supports Ubuntu and Debian; continuing" ;;
esac
KERNEL=$(uname -r | cut -d- -f1)
version_ge "$KERNEL" 6.3 || die "kernel $KERNEL is too old: user namespaces for pods need 6.3 or later"
for tool in curl sort awk; do command -v "$tool" >/dev/null || die "missing $tool"; done
if [ "$KATA" = 1 ]; then
  # Kata runs each sandbox as a VM: the node needs KVM, and the release
  # pinned here is amd64's.
  [ "$ARCH" = amd64 ] || die "--kata: the pinned Kata release is amd64 only"
  [ -c /dev/kvm ] || die "--kata: no /dev/kvm (enable virtualization in the firmware, or nested virtualization on a VM)"
  [ -f "$KATA_DIR/kata.env" ] || die "--kata: run from a checkout (deploy/kata/kata.env is missing)"
fi
if [ -z "$ADDRESS" ]; then
  ADDRESS=$(hostname -I | awk '{print $1}')
fi
[ -n "$ADDRESS" ] || die "cannot tell this box's address: pass --address"
note "Linux ${KERNEL} on ${ARCH}, ${PRETTY_NAME:-unknown OS}, reached at ${ADDRESS}"
mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"

# --- 2. kernel ------------------------------------------------------------------
step "Kernel: wireguard, AppArmor, user namespaces"
# Every run's sandbox tunnels to its project's egress gateway over WireGuard.
modprobe wireguard || die "the wireguard module does not load (in-tree since Linux 5.6)"
echo wireguard >/etc/modules-load.d/apothecary.conf
note "wireguard loaded, and loaded at every boot"

if [ -d /etc/apparmor.d ] && [ -f /etc/apparmor.d/fusermount3 ]; then
  # Ubuntu 25.04+ confines fusermount3, which sysbox-fs uses to mount each
  # sandbox's virtual /proc and /sys under /var/lib/sysboxfs.
  local_profile=/etc/apparmor.d/local/fusermount3
  if ! grep -qs 'var/lib/sysboxfs' "$local_profile"; then
    mkdir -p /etc/apparmor.d/local
    cat >>"$local_profile" <<'EOF'
# allow Sysbox to create fuse mounts (deploy/k3s/install.sh)
mount fstype=@{fuse_types} options=(nosuid,nodev,rw) -> /var/lib/sysboxfs/**/,
mount fstype=@{fuse_types} options=(nosuid,nodev,ro) -> /var/lib/sysboxfs/**/,
umount /var/lib/sysboxfs/**/,
EOF
    apparmor_parser -r /etc/apparmor.d/fusermount3
    note "fusermount3 may now mount for sysbox"
  else
    note "fusermount3 already allows sysbox"
  fi
fi

if [ "$(sysctl -n kernel.apparmor_restrict_unprivileged_userns 2>/dev/null || echo 0)" = 1 ]; then
  if [ "$KEEP_USERNS" = 1 ]; then
    note "keeping the restriction on unprivileged user namespaces (--keep-userns-restriction)"
  else
    # Under sysbox nothing in a sandbox is host root, so a tool that makes a
    # user namespace of its own (bubblewrap, Chrome, rootless podman) needs
    # the restriction lifted.
    echo kernel.apparmor_restrict_unprivileged_userns=0 >/etc/sysctl.d/60-apothecary-userns.conf
    sysctl --system >/dev/null
    note "unprivileged user namespaces allowed (--keep-userns-restriction keeps them restricted)"
  fi
fi

# --- 3. sysbox -------------------------------------------------------------------
step "sysbox ${SYSBOX_VERSION}"
# sysbox must never restart under running sandboxes: sysbox-fs forgets the
# containers it serves and every exec into them fails ("unsafe procfs
# detected"). Ubuntu's needrestart restarts services after unattended
# upgrades (libc, systemd) on its own, so it is told to leave sysbox,
# containerd and k3s alone. Written whether needrestart is installed or not:
# it reads the file whenever it comes.
nrconf=/etc/needrestart/conf.d/apothecary.conf
nrwant=$(cat <<'EOF'
# Apothecary (deploy/k3s/install.sh): restarting these under running
# sandboxes breaks every one of them; restart them only with no run active.
$nrconf{override_rc}{qr(^sysbox)} = 0;
$nrconf{override_rc}{qr(^containerd)} = 0;
$nrconf{override_rc}{qr(^k3s)} = 0;
EOF
)
if [ "$(cat "$nrconf" 2>/dev/null)" != "$nrwant" ]; then
  mkdir -p "$(dirname "$nrconf")"
  printf '%s\n' "$nrwant" >"$nrconf"
  note "needrestart will not restart sysbox, containerd or k3s (${nrconf})"
else
  note "needrestart already leaves sysbox, containerd and k3s alone"
fi
installed=$(sysbox-runc --version 2>/dev/null | awk '/^version:/{print $2}' || true)
if [ -n "$installed" ] && version_ge "$installed" "$SYSBOX_VERSION"; then
  note "sysbox ${installed} is installed"
else
  # The release's asset name (no "-0" since 0.7.0), and the sha256 it must
  # have: the pin for the pinned version, else SYSBOX_DEB_SHA256.
  url="${SYSBOX_DEB_URL:-https://github.com/nestybox/sysbox/releases/download/v${SYSBOX_VERSION}/sysbox-ce_${SYSBOX_VERSION}.linux_${ARCH}.deb}"
  want="${SYSBOX_DEB_SHA256:-}"
  if [ -z "$want" ] && [ -z "${SYSBOX_DEB_URL:-}" ] && [ "$SYSBOX_VERSION" = "$PINNED_SYSBOX" ]; then
    case "$ARCH" in
      amd64) want="$SYSBOX_SHA256_AMD64" ;;
      arm64) want="$SYSBOX_SHA256_ARM64" ;;
    esac
  fi
  [ -n "$want" ] || die "sysbox ${SYSBOX_VERSION} is not the pinned ${PINNED_SYSBOX}: set SYSBOX_DEB_SHA256 to its .deb's sha256 (the release page lists each asset's digest)"
  deb="$STATE_DIR/sysbox.deb"
  note "downloading ${url}"
  curl -fsSL -o "$deb" "$url" || die "cannot download sysbox from ${url}: check the release page (github.com/nestybox/sysbox/releases) for the asset's name, and pass --sysbox-version or SYSBOX_DEB_URL"
  echo "${want}  ${deb}" | sha256sum -c --quiet - || { rm -f "$deb"; die "sysbox's .deb does not have the sha256 ${want}"; }
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$deb"
  rm -f "$deb"
  installed=$(sysbox-runc --version | awk '/^version:/{print $2}')
fi
version_ge "$installed" 0.7.1 || die "sysbox ${installed} is too old: 0.7.1 or later (0.7.0 cannot run on containerd 2 with kernel 6.8+); rerun without --sysbox-version"
systemctl is-active --quiet sysbox || systemctl restart sysbox
systemctl is-active --quiet sysbox || die "sysbox does not run: journalctl -u sysbox"
note "sysbox ${installed} running"

# --- 3b. Kata --------------------------------------------------------------------
if [ "$KATA" = 1 ]; then
  # shellcheck source=deploy/kata/kata.env
  . "$KATA_DIR/kata.env"
  step "Kata Containers ${KATA_VERSION}"
  # The static release, unpacked over /opt/kata (its paths are opt/kata/...),
  # then Apothecary's configuration written beside the release's. A marker
  # holds the version, so a rerun skips the download and a new pin replaces
  # it. Kata VMs already running keep the binaries they started with; new
  # sandboxes get the new release.
  marker=/opt/kata/.apothecary-kata-version
  if [ -x /opt/kata/runtime-rs/bin/containerd-shim-kata-v2 ] && [ "$(cat "$marker" 2>/dev/null)" = "$KATA_VERSION" ]; then
    note "Kata ${KATA_VERSION} is installed"
  else
    command -v zstd >/dev/null || {
      apt-get update -qq
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq zstd
    }
    tarball="$STATE_DIR/kata-static.tar.zst"
    url="https://github.com/kata-containers/kata-containers/releases/download/${KATA_VERSION}/kata-static-${KATA_VERSION}-amd64.tar.zst"
    note "downloading ${url} (about 1 GB)"
    curl -fL --retry 3 -o "$tarball" "$url" || die "cannot download Kata ${KATA_VERSION}"
    echo "${KATA_SHA256}  ${tarball}" | sha256sum -c --quiet - || die "the download is not Kata ${KATA_VERSION}'s static release (sha256)"
    zstd -dc "$tarball" | tar -x -C /
    rm -f "$tarball"
    echo "$KATA_VERSION" >"$marker"
  fi
  sh "$KATA_DIR/configure.sh" /opt/kata/share/defaults/kata-containers/runtime-rs
  note "Kata ${KATA_VERSION} in /opt/kata, configured for Apothecary (configuration-apothecary.toml)"
fi

# --- 4. k3s ------------------------------------------------------------------------
step "k3s (${K3S_CHANNEL} channel)"
if command -v k3s >/dev/null && systemctl is-active --quiet k3s; then
  note "k3s $(k3s --version | awk 'NR==1{print $3}') is installed"
else
  curl -sfL https://get.k3s.io | INSTALL_K3S_CHANNEL="$K3S_CHANNEL" sh -s - server \
    --write-kubeconfig-mode 0600
fi
kube() { k3s kubectl "$@"; }
for _ in $(seq 60); do
  kube get nodes 2>/dev/null | grep -q ' Ready' && break
  sleep 2
done
kube get nodes | grep -q ' Ready' || die "the node never became Ready: journalctl -u k3s"
K8S=$(kube version -o json | awk -F'"' '/"gitVersion"/{print $4; exit}' | sed 's/^v//; s/+.*//')
version_ge "$K8S" 1.33 || die "Kubernetes ${K8S} is too old: 1.33 or later (pick another --k3s-channel)"
CTRD=$(k3s ctr version 2>/dev/null | awk '/Version:/{print $2; exit}' | sed 's/^v//; s/-.*//')
version_ge "$CTRD" 2.0.5 || die "containerd ${CTRD} is too old: 2.0.5 or later"
note "Kubernetes ${K8S}, containerd ${CTRD}, node Ready"

# --- 5. sysbox in k3s ------------------------------------------------------------
step "sysbox (and Kata) as k3s runtimes"
# k3s renders its containerd config from this template when it exists: its
# own settings ("base"), plus a runtime named sysbox-runc and, with --kata,
# one named kata: Kata's Rust shim with Cloud Hypervisor, a privileged
# sandbox given its VM's devices and never the node's
# (privileged_without_host_devices).
tmpl=/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.tmpl
want=$(cat <<'EOF'
{{ template "base" . }}

# sysbox: unprivileged sandboxes that nest a docker daemon (deploy/k3s/install.sh)
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.'sysbox-runc']
  runtime_type = "io.containerd.runc.v2"

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.'sysbox-runc'.options]
  BinaryName = "/usr/bin/sysbox-runc"
  SystemdCgroup = {{ .SystemdCgroup }}
EOF
)
if [ "$KATA" = 1 ]; then
  want+=$(cat <<'EOF'


# kata: a VM per sandbox (deploy/k3s/install.sh --kata)
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.'kata']
  runtime_type = "io.containerd.kata.v2"
  runtime_path = "/opt/kata/runtime-rs/bin/containerd-shim-kata-v2"
  privileged_without_host_devices = true

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.'kata'.options]
  ConfigPath = "/opt/kata/share/defaults/kata-containers/runtime-rs/configuration-apothecary.toml"
EOF
)
fi
if [ "$(cat "$tmpl" 2>/dev/null)" != "$want" ]; then
  mkdir -p "$(dirname "$tmpl")"
  printf '%s\n' "$want" >"$tmpl"
  systemctl restart k3s
  for _ in $(seq 60); do
    kube get nodes 2>/dev/null | grep -q ' Ready' && break
    sleep 2
  done
  note "containerd's runtimes written"
else
  note "containerd already has its runtimes"
fi
kube apply -f - >/dev/null <<'EOF'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: sysbox-runc
handler: sysbox-runc
EOF
note "RuntimeClass sysbox-runc"
if [ "$KATA" = 1 ]; then
  # The VM's own memory and CPU, beyond the pod's limits: the scheduler
  # counts them and the pod's cgroup holds them (docs/kubernetes.md).
  kube apply -f - >/dev/null <<'EOF'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: kata
handler: kata
overhead:
  podFixed:
    cpu: 250m
    memory: 320Mi
EOF
  note "RuntimeClass kata"
fi

# --- 6. smoke tests ------------------------------------------------------------
# What every run's sandbox does: a working dockerd inside a pod of the
# runtime's shape. smoke <name> <the pod's spec, after restartPolicy>.
smoke() {
  local name=$1 spec=$2 phase="" logs
  kube delete pod "$name" -n default --ignore-not-found --wait=true >/dev/null
  kube apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${name}
  namespace: default
spec:
  restartPolicy: Never
${spec}
EOF
  for _ in $(seq 120); do
    phase=$(kube get pod "$name" -n default -o jsonpath='{.status.phase}' 2>/dev/null || true)
    case "$phase" in Succeeded | Failed) break ;; esac
    sleep 2
  done
  logs=$(kube logs "$name" -n default 2>/dev/null || true)
  if [ "$phase" != Succeeded ]; then
    kube describe pod "$name" -n default | tail -n 20 >&2 || true
    printf '%s\n' "$logs" >&2
    die "the smoke test ${name} failed (${phase:-never started}): sandboxes would not start either"
  fi
  kube delete pod "$name" -n default --wait=false >/dev/null
  note "$logs"
}
# The nested daemon each smoke pod starts (its container's fields; the
# pod's shell expands them, not this one).
# shellcheck disable=SC2016
dind='      image: docker:29-dind
      command: [sh, -c]
      args:
        - |
          dockerd >/tmp/dockerd.log 2>&1 &
          for i in $(seq 60); do
            if docker info >/dev/null 2>&1; then echo "nested dockerd answers"; exit 0; fi
            sleep 2
          done
          cat /tmp/dockerd.log; exit 1'

step "Smoke test: a docker daemon in an unprivileged pod (sysbox)"
# sysbox, a user namespace of the pod's own, no privilege.
smoke apothecary-smoke "  runtimeClassName: sysbox-runc
  hostUsers: false
  containers:
    - name: dind
${dind}"

if [ "$KATA" = 1 ]; then
  step "Smoke test: a docker daemon in a VM (Kata)"
  # Privileged inside its own VM, sized (a Kata sandbox always is), the
  # inner docker's data on an emptyDir: a disk of its own in the guest.
  smoke apothecary-smoke-kata "  runtimeClassName: kata
  containers:
    - name: dind
      securityContext:
        privileged: true
      resources:
        limits: {cpu: '1', memory: 2Gi}
        requests: {cpu: 250m, memory: 2Gi}
      volumeMounts:
        - {name: docker, mountPath: /var/lib/docker}
${dind}
  volumes:
    - {name: docker, emptyDir: {sizeLimit: 5Gi}}"
fi

if [ "$CLUSTER_ONLY" = 1 ]; then
  step "Cluster ready (--cluster-only): kubeconfig at ${KUBECONFIG}"
  exit 0
fi

# --- 7. Apothecary -------------------------------------------------------------
if ! command -v helm >/dev/null; then
  step "Helm"
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi

if [ -n "$DOMAIN" ]; then
  step "cert-manager (Let's Encrypt for ${DOMAIN})"
  helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager \
    -n cert-manager --create-namespace --set crds.enabled=true --wait >/dev/null
  kube apply -f - >/dev/null <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: ${EMAIL}
    privateKeySecretRef:
      name: letsencrypt-account
    solvers:
      - http01:
          ingress:
            ingressClassName: traefik
EOF
  note "ClusterIssuer letsencrypt"
fi

step "Apothecary ${CHART_VERSION:-(newest release)}"
version_args=()
[ -n "$CHART_VERSION" ] && version_args=(--version "$CHART_VERSION")
APP_VERSION=$(helm show chart "$CHART" "${version_args[@]}" | awk '/^appVersion:/{gsub(/"/,"",$2); print $2}')
[ -n "$APP_VERSION" ] || die "cannot read the chart ${CHART}"

# Secrets, made once and kept: the master key seals every stored secret, so
# it never changes under a running instance.
secrets="$STATE_DIR/install.env"
if [ ! -s "$secrets" ]; then
  note "generating the master key (apothecaryd keygen) and the database password"
  key=$(kube run apothecary-keygen -n default --rm -i --restart=Never --quiet \
    --image "ghcr.io/intradeus/apothecary/apothecaryd:${APP_VERSION}" -- keygen | tr -d '\r\n')
  [ -n "$key" ] || die "keygen printed nothing"
  pg=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
  umask 077
  printf 'MASTER_KEY=%s\nPG_PASSWORD=%s\n' "$key" "$pg" >"$secrets"
fi
# shellcheck disable=SC1090
. "$secrets"

values=(
  --set "masterKey=${MASTER_KEY}"
  --set "postgres.password=${PG_PASSWORD}"
)
if [ "$KATA" = 1 ]; then
  # sysbox stays the default; a workshop may choose kata.
  values+=(--set 'workspaces.runtimes={sysbox,kata}')
fi
if [ -n "$DOMAIN" ]; then
  URL="https://${DOMAIN}"
  values+=(
    --set access.http.ingress.enabled=true
    --set access.http.ingress.className=traefik
    --set "access.http.ingress.host=${DOMAIN}"
    --set "access.http.ingress.annotations.cert-manager\.io/cluster-issuer=letsencrypt"
    --set "access.http.ingress.tls[0].secretName=apothecary-tls"
    --set "access.http.ingress.tls[0].hosts[0]=${DOMAIN}"
  )
else
  URL="http://${ADDRESS}:8080"
  values+=(
    --set access.http.service.type=LoadBalancer
    --set access.http.scheme=http
    --set "access.publicURL=${URL}"
  )
fi
helm upgrade --install apothecary "$CHART" "${version_args[@]}" \
  -n "$NAMESPACE" --create-namespace "${values[@]}" --wait --timeout 10m >/dev/null
kube -n "$NAMESPACE" rollout status deploy/apothecaryd --timeout=5m >/dev/null

step "Done"
cat <<EOF
    Apothecary ${APP_VERSION} is up: open ${URL}/ and claim the instance
    (the first account is the admin).

    On your machine, install the apothecary client from the ${APP_VERSION}
    release (github.com/intradeus/apothecary/releases), then:
      apothecary login ${URL}
      apothecary ssh-config

    Secrets: ${secrets} (keep a copy: the master key opens every stored secret)
    Cluster: kubectl --kubeconfig ${KUBECONFIG} ... (or: sudo k3s kubectl ...)
EOF
if [ "$KATA" = 1 ]; then
  cat <<EOF

    Kata: workshops may choose kata (Settings → Workspace). Set a default
    sandbox size first (System → Limits): an unsized Kata sandbox is refused.
EOF
fi
if [ -z "$DOMAIN" ]; then
  cat <<EOF

    Plain HTTP: keep port 8080 to your private network (a firewall, your VPN).
    For HTTPS, run this again with --domain <host> --email <address>.
EOF
fi
