#!/usr/bin/env bash
# A throwaway Kubernetes node whose sandboxes run under Kata Containers: k3s
# in one privileged container of the docker host, with the host's /dev/kvm and
# the Kata static release unpacked beside it. It needs docker and KVM, not
# root. It is never a node of a real cluster: its own container, network and
# volumes, all named apo-kata-*.
#
#   deploy/kata/rig.sh up              create (or start) the node; writes
#                                      deploy/.kata-kubeconfig
#   deploy/kata/rig.sh import <image>… copy images from the docker host into
#                                      the node's containerd (no registry)
#   deploy/kata/rig.sh down            delete the node, its network and its
#                                      k3s volume (the unpacked Kata stays:
#                                      `down all` removes it too)
#
# KATA_TARBALL names a local copy of the release; otherwise it is downloaded
# (about 1 GB). Either way its sha256 must be the one pinned in kata.env.
#
# What the node adds to k3s (docs/sandbox-runtimes-plan.md, phase 5):
#   - containerd: a `kata` runtime, Kata's Rust shim with Cloud Hypervisor,
#     privileged_without_host_devices (a privileged sandbox gets its VM's
#     devices, never the node's), no io.katacontainers.* annotations;
#   - Kata's configuration (configuration-apothecary.toml, from the release's
#     configuration-clh-runtime-rs.toml): each emptyDir an ext4 disk in the
#     guest (block-plain), freed guest memory handed back, the mem-agent on;
#   - the `kata` RuntimeClass, with the VM's own overhead;
#   - raw block volumes, which a Kata sandbox's claim needs to be kept
#     across a stop (the integration's VM storage class) and k3s's
#     local-path cannot make: a storage class `apo-block` and a pool of
#     local Block volumes on loop devices in the node (block_pool).
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=deploy/kata/kata.env
. "$HERE/kata.env" # KATA_VERSION, KATA_SHA256, K3S_IMAGE
HELPER_IMAGE=alpine:3.21

NODE=apo-kata-node
NET=apo-kata
K3S_VOL=apo-kata-k3s
OPT_VOL=apo-kata-opt-$KATA_VERSION
ROOT=$(cd "$HERE/../.." && pwd)
KUBECONFIG_OUT=$ROOT/deploy/.kata-kubeconfig

say() { echo "kata rig: $*" >&2; }
die() {
  say "$*"
  exit 1
}

# The Kata tree, unpacked once into a volume of its own (keyed by version).
unpack() {
  if docker volume inspect "$OPT_VOL" >/dev/null 2>&1 &&
    docker run --rm -v "$OPT_VOL":/x "$HELPER_IMAGE" test -x /x/opt/kata/runtime-rs/bin/containerd-shim-kata-v2; then
    configure
    return
  fi
  command -v zstd >/dev/null || die "zstd is needed to unpack the Kata release"
  local tarball=${KATA_TARBALL:-} tmp=""
  if [ -z "$tarball" ]; then
    tmp=$(mktemp -d)
    tarball=$tmp/kata-static.tar.zst
    say "downloading Kata $KATA_VERSION (about 1 GB)"
    curl -fL --retry 3 -o "$tarball" \
      "https://github.com/kata-containers/kata-containers/releases/download/$KATA_VERSION/kata-static-$KATA_VERSION-amd64.tar.zst"
  fi
  echo "$KATA_SHA256  $tarball" | sha256sum -c --quiet - || die "$tarball is not Kata $KATA_VERSION's static release (sha256)"
  say "unpacking Kata $KATA_VERSION into volume $OPT_VOL"
  docker volume create "$OPT_VOL" >/dev/null
  zstd -dc "$tarball" | docker run --rm -i -v "$OPT_VOL":/x "$HELPER_IMAGE" tar -x -C /x
  [ -z "$tmp" ] || rm -rf "$tmp"
  configure
}

# The configuration the node's runtime entry names: the release's Cloud
# Hypervisor one, edited as phase 5 lists. Written on every `up`, so an edit
# to configure.sh reaches a volume unpacked before it; over stdin, as the
# script's path may not be one the docker daemon sees.
configure() {
  docker run --rm -i -v "$OPT_VOL":/x "$HELPER_IMAGE" \
    sh -s /x/opt/kata/share/defaults/kata-containers/runtime-rs <"$HERE/configure.sh"
}

containerd_template() {
  cat <<'EOF'
{{ template "base" . }}

[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.kata]
  runtime_type = "io.containerd.kata.v2"
  runtime_path = "/opt/kata/runtime-rs/bin/containerd-shim-kata-v2"
  privileged_without_host_devices = true
  [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.kata.options]
    ConfigPath = "/opt/kata/share/defaults/kata-containers/runtime-rs/configuration-apothecary.toml"
EOF
}

# The VM's own memory and CPU, beyond the pod's limits: the scheduler counts
# them, and the pod's cgroup holds them. Measured on the rig (the note
# 2026-11-22-kata-sandboxes-run-on-emptydir-disks).
runtime_class() {
  cat <<'EOF'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata: {name: kata}
handler: kata
overhead:
  podFixed: {cpu: 250m, memory: 320Mi}
EOF
}

kubectl_node() { docker exec -i "$NODE" kubectl "$@"; }

# The Block pool: BLOCK_PVS local volumes of BLOCK_SIZE each, every one a
# sparse file in the node's k3s volume on a loop device, offered through
# the no-provisioner class apo-block. A volume a claim released is made
# again from an empty file (a static volume is never reused with its old
# disk), and one still bound is left. The live test's claims are 20Gi each
# (storage_size's default): a sandbox, the cross-run check's second one.
BLOCK_PVS=${BLOCK_PVS:-6}
BLOCK_SIZE=20Gi
block_pool() {
  kubectl_node apply -f - >/dev/null <<'EOF'
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata: {name: apo-block}
provisioner: kubernetes.io/no-provisioner
volumeBindingMode: WaitForFirstConsumer
EOF
  local i pv phase
  for i in $(seq 0 $((BLOCK_PVS - 1))); do
    pv=apo-block-$i
    phase=$(kubectl_node get pv "$pv" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    case "$phase" in Available | Bound | Pending) continue ;; esac
    [ -z "$phase" ] || kubectl_node delete pv "$pv" --wait=true >/dev/null
    # A fresh sparse file (truncate's G is GiB: 20Gi is 20G) on a loop
    # device of its own (made when the node's /dev lacks it: a privileged
    # container's /dev is the host's at start).
    local dev
    dev=$(docker exec "$NODE" sh -euc '
      img=/var/lib/rancher/k3s/apo-block/'"$pv"'.img
      mkdir -p "${img%/*}"
      for d in $(losetup -j "$img" | cut -d: -f1); do losetup -d "$d"; done
      rm -f "$img"; truncate -s '"${BLOCK_SIZE%i}"' "$img"
      d=$(losetup -f)
      [ -e "$d" ] || mknod "$d" b 7 "${d#/dev/loop}"
      losetup "$d" "$img"
      echo "$d"')
    kubectl_node apply -f - >/dev/null <<EOF
apiVersion: v1
kind: PersistentVolume
metadata: {name: $pv}
spec:
  capacity: {storage: $BLOCK_SIZE}
  volumeMode: Block
  accessModes: [ReadWriteOnce]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: apo-block
  local: {path: $dev}
  nodeAffinity:
    required:
      nodeSelectorTerms:
        - matchExpressions:
            - {key: kubernetes.io/hostname, operator: In, values: [$NODE]}
EOF
  done
  say "Block pool: $BLOCK_PVS volumes of $BLOCK_SIZE (storage class apo-block)"
}

up() {
  [ -c /dev/kvm ] || die "no /dev/kvm on this host: Kata needs KVM"
  unpack
  docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
  if ! docker container inspect "$NODE" >/dev/null 2>&1; then
    say "creating $NODE ($K3S_IMAGE)"
    docker volume create "$K3S_VOL" >/dev/null
    # Written into the volume itself: docker cp into a container that has
    # not started lands under its mount point, hidden by the volume.
    containerd_template | docker run --rm -i -v "$K3S_VOL":/k3s "$HELPER_IMAGE" sh -c \
      'mkdir -p /k3s/agent/etc/containerd && cat >/k3s/agent/etc/containerd/config-v3.toml.tmpl'
    docker create --name "$NODE" --hostname "$NODE" --privileged --network "$NET" \
      --tmpfs /run --tmpfs /var/run \
      -v "$K3S_VOL":/var/lib/rancher/k3s \
      --mount "type=volume,src=$OPT_VOL,dst=/opt/kata,volume-subpath=opt/kata,readonly" \
      "$K3S_IMAGE" server --disable traefik --disable metrics-server --disable servicelb >/dev/null
  fi
  docker start "$NODE" >/dev/null
  say "waiting for the node"
  for _ in $(seq 90); do
    kubectl_node get --raw /readyz >/dev/null 2>&1 && kubectl_node get nodes 2>/dev/null | grep -q ' Ready' && break
    sleep 2
  done
  kubectl_node get nodes | grep -q ' Ready' || die "the node is not Ready after 3 minutes (docker logs $NODE)"
  runtime_class | kubectl_node apply -f - >/dev/null
  block_pool

  # In a container of the docker daemon itself (the devcontainer), join the
  # node's network to reach it by address, as k8s:cluster does for k3d; on
  # the host's network, the address is reachable already.
  local self
  self=$("$ROOT/deploy/self-container.sh" id)
  if [ -n "$self" ] && [ "$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$self")" != host ] &&
    ! docker network inspect "$NET" | grep -q "\"$self\""; then
    docker network connect "$NET" "$self"
  fi
  local ip
  ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" "$NODE")
  docker exec "$NODE" cat /etc/rancher/k3s/k3s.yaml | sed "s|https://127.0.0.1:6443|https://$ip:6443|" >"$KUBECONFIG_OUT"
  chmod 600 "$KUBECONFIG_OUT"
  say "up: KUBECONFIG=$KUBECONFIG_OUT"
}

import() {
  [ $# -gt 0 ] || die "usage: deploy/kata/rig.sh import <image>…"
  for img in "$@"; do
    say "importing $img"
    docker save "$img" | docker exec -i "$NODE" ctr -n k8s.io images import - >/dev/null
  done
}

down() {
  local self
  self=$("$ROOT/deploy/self-container.sh" id)
  [ -z "$self" ] || docker network disconnect "$NET" "$self" 2>/dev/null || true
  # Loop devices are the host's: detach the pool's before its files go.
  docker exec "$NODE" sh -c 'for f in /var/lib/rancher/k3s/apo-block/*.img; do
      for d in $(losetup -j "$f" | cut -d: -f1); do losetup -d "$d"; done
    done' 2>/dev/null || true
  docker rm -f "$NODE" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  docker volume rm "$K3S_VOL" >/dev/null 2>&1 || true
  if [ "${1:-}" = all ]; then
    docker volume rm "$OPT_VOL" >/dev/null 2>&1 || true
  fi
  rm -f "$KUBECONFIG_OUT"
}

cmd=${1:-}
shift || true
case "$cmd" in
  up) up ;;
  import) import "$@" ;;
  down) down "$@" ;;
  *) die "usage: deploy/kata/rig.sh up | import <image>… | down [all]" ;;
esac
