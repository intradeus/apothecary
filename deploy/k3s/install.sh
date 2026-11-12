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
  --sysbox-version <X.Y.Z> the sysbox release (default: 0.7.0); or set
                           SYSBOX_DEB_URL to the .deb to install
  --keep-userns-restriction
                           leave Ubuntu's AppArmor restriction on
                           unprivileged user namespaces in place (tools that
                           sandbox themselves inside a workspace then fail)
  --cluster-only           stop after the smoke test: no Apothecary
  -h, --help               this text

Steps: preflight, kernel, sysbox, k3s, sysbox in k3s, smoke test, then
Apothecary (cert-manager first with --domain). Logs go to stdout; the
generated secrets to /root/.apothecary/install.env (root only).
EOF
}

DOMAIN="" EMAIL="" ADDRESS="" CHART_VERSION="" K3S_CHANNEL="stable"
SYSBOX_VERSION="0.7.0" KEEP_USERNS=0 CLUSTER_ONLY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --domain) DOMAIN="${2:?--domain needs a host}"; shift 2 ;;
    --email) EMAIL="${2:?--email needs an address}"; shift 2 ;;
    --address) ADDRESS="${2:?--address needs an ip}"; shift 2 ;;
    --chart-version) CHART_VERSION="${2:?--chart-version needs X.Y.Z}"; shift 2 ;;
    --k3s-channel) K3S_CHANNEL="${2:?--k3s-channel needs a channel}"; shift 2 ;;
    --sysbox-version) SYSBOX_VERSION="${2:?--sysbox-version needs X.Y.Z}"; shift 2 ;;
    --keep-userns-restriction) KEEP_USERNS=1; shift ;;
    --cluster-only) CLUSTER_ONLY=1; shift ;;
    -h | --help) usage; exit 0 ;;
    *) echo "unknown option: $1 (--help)" >&2; exit 2 ;;
  esac
done

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
installed=$(sysbox-runc --version 2>/dev/null | awk '/^version:/{print $2}' || true)
if [ -n "$installed" ] && version_ge "$installed" "$SYSBOX_VERSION"; then
  note "sysbox ${installed} is installed"
else
  url="${SYSBOX_DEB_URL:-https://github.com/nestybox/sysbox/releases/download/v${SYSBOX_VERSION}/sysbox-ce_${SYSBOX_VERSION}-0.linux_${ARCH}.deb}"
  deb="$STATE_DIR/sysbox.deb"
  note "downloading ${url}"
  curl -fsSL -o "$deb" "$url" || die "cannot download sysbox: check the release page (github.com/nestybox/sysbox/releases) and pass --sysbox-version or SYSBOX_DEB_URL"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$deb"
  rm -f "$deb"
  installed=$(sysbox-runc --version | awk '/^version:/{print $2}')
fi
version_ge "$installed" 0.7.0 || die "sysbox ${installed} is too old: 0.7 or later (user namespaces for pods)"
systemctl is-active --quiet sysbox || systemctl restart sysbox
systemctl is-active --quiet sysbox || die "sysbox does not run: journalctl -u sysbox"
note "sysbox ${installed} running"

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
step "sysbox as a k3s runtime"
# k3s renders its containerd config from this template when it exists: its
# own settings ("base"), plus a runtime named sysbox-runc.
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
if [ "$(cat "$tmpl" 2>/dev/null)" != "$want" ]; then
  mkdir -p "$(dirname "$tmpl")"
  printf '%s\n' "$want" >"$tmpl"
  systemctl restart k3s
  for _ in $(seq 60); do
    kube get nodes 2>/dev/null | grep -q ' Ready' && break
    sleep 2
  done
  note "containerd now knows the sysbox-runc runtime"
else
  note "containerd already knows the sysbox-runc runtime"
fi
kube apply -f - >/dev/null <<'EOF'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: sysbox-runc
handler: sysbox-runc
EOF
note "RuntimeClass sysbox-runc"

# --- 6. smoke test -------------------------------------------------------------
step "Smoke test: a docker daemon in an unprivileged pod"
# What every run's sandbox does: sysbox, a user namespace of the pod's own,
# no privilege, and a working dockerd inside.
kube delete pod apothecary-smoke -n default --ignore-not-found --wait=true >/dev/null
kube apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: apothecary-smoke
  namespace: default
spec:
  runtimeClassName: sysbox-runc
  hostUsers: false
  restartPolicy: Never
  containers:
    - name: dind
      image: docker:29-dind
      command: [sh, -c]
      args:
        - |
          dockerd >/tmp/dockerd.log 2>&1 &
          for i in $(seq 60); do
            if docker info >/dev/null 2>&1; then echo "nested dockerd answers"; exit 0; fi
            sleep 2
          done
          cat /tmp/dockerd.log; exit 1
EOF
phase=""
for _ in $(seq 120); do
  phase=$(kube get pod apothecary-smoke -n default -o jsonpath='{.status.phase}' 2>/dev/null || true)
  case "$phase" in Succeeded | Failed) break ;; esac
  sleep 2
done
logs=$(kube logs apothecary-smoke -n default 2>/dev/null || true)
if [ "$phase" != Succeeded ]; then
  kube describe pod apothecary-smoke -n default | tail -n 20 >&2 || true
  printf '%s\n' "$logs" >&2
  die "the smoke test failed (${phase:-never started}): sandboxes would not start either"
fi
kube delete pod apothecary-smoke -n default --wait=false >/dev/null
note "$logs"

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
if [ -z "$DOMAIN" ]; then
  cat <<EOF

    Plain HTTP: keep port 8080 to your private network (a firewall, your VPN).
    For HTTPS, run this again with --domain <host> --email <address>.
EOF
fi
