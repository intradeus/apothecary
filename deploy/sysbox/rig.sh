#!/usr/bin/env bash
# A throwaway Kubernetes node whose sandboxes run under sysbox: k3s in one
# privileged container of the docker host, with sysbox's daemons beside it in
# the same container (deploy/sysbox/Dockerfile, start.sh). It needs docker,
# not root, and nothing of sysbox on the host. It is never a node of a real
# cluster: its own image, container, network and volumes, all named
# apo-sysbox-*.
#
#   deploy/sysbox/rig.sh up              build the node's image, create (or
#                                        start) the node; writes
#                                        deploy/.sysbox-kubeconfig
#   deploy/sysbox/rig.sh import <image>… copy images from the docker host into
#                                        the node's containerd (no registry)
#   deploy/sysbox/rig.sh down            delete the node, its network and its
#                                        volumes
#
# The pins: sysbox.env (the sysbox release and its sha256, which install.sh
# installs on a real box) and ../kata/kata.env's K3S_IMAGE (the k3s every rig
# node runs).
#
# What the node adds to k3s: a `sysbox-runc` containerd runtime (sysbox-runc
# behind the runc shim) and the `sysbox-runc` RuntimeClass the provisioner's
# sysbox runtime names. Claims are k3s's local-path ones: sysbox maps a
# pod's volumes into its user namespace (idmapped mounts), so a sysbox
# sandbox keeps its run on a claim and sleeps like a privileged one.
#
# The host needs what sysbox's package would set (rig.sh says which are
# low): inotify and keyring limits, and, on Ubuntu 25.04+, fusermount3
# allowed to mount under /var/lib/sysboxfs (deploy/k3s/install.sh's rules).
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=deploy/sysbox/sysbox.env
. "$HERE/sysbox.env" # SYSBOX_VERSION, SYSBOX_SHA256_AMD64
# shellcheck source=deploy/kata/kata.env
. "$HERE/../kata/kata.env" # K3S_IMAGE
HELPER_IMAGE=alpine:3.21
SMOKE_IMAGE=busybox:1.37

NODE=apo-sysbox-node
NET=apo-sysbox
K3S_VOL=apo-sysbox-k3s
# sysbox's own state: a volume, never the container's overlayfs.
SYSBOX_VOL=apo-sysbox-state
IMAGE=apo-sysbox-node:${SYSBOX_VERSION}-${K3S_IMAGE##*:}
ROOT=$(cd "$HERE/../.." && pwd)
KUBECONFIG_OUT=$ROOT/deploy/.sysbox-kubeconfig

say() { echo "sysbox rig: $*" >&2; }
die() {
  say "$*"
  exit 1
}

containerd_template() {
  cat <<'EOF'
{{ template "base" . }}

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.'sysbox-runc']
  runtime_type = "io.containerd.runc.v2"
  [plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.'sysbox-runc'.options]
    BinaryName = "/usr/bin/sysbox-runc"
    SystemdCgroup = {{ .SystemdCgroup }}
EOF
}

runtime_class() {
  cat <<'EOF'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata: {name: sysbox-runc}
handler: sysbox-runc
EOF
}

# A pod the way the provisioner makes a sysbox sandbox's (the runtime class,
# a user namespace of its own): a node that cannot run it fails here, not
# halfway through the suite.
smoke_pod() {
  cat <<EOF
apiVersion: v1
kind: Pod
metadata: {name: apo-sysbox-smoke, namespace: default}
spec:
  runtimeClassName: sysbox-runc
  hostUsers: false
  restartPolicy: Never
  containers:
    - {name: smoke, image: $SMOKE_IMAGE, command: [sh, -c, 'grep -q . /proc/self/uid_map && echo sysbox-ok']}
EOF
}

kubectl_node() { docker exec -i "$NODE" kubectl "$@"; }

# What sysbox's package sets on a real box; a CI runner sets them before
# `up` (ci.yml). Only said here: changing the host is not the rig's to do.
host_check() {
  local want key have
  for want in fs.inotify.max_queued_events=1048576 fs.inotify.max_user_watches=1048576 \
    fs.inotify.max_user_instances=1048576 kernel.keys.maxkeys=20000 kernel.keys.maxbytes=1400000; do
    key=${want%%=*}
    have=$(sysctl -n "$key" 2>/dev/null || echo 0)
    [ "$have" -ge "${want#*=}" ] || say "warning: $key is $have; sysbox wants ${want#*=} (sudo sysctl -w $want)"
  done
  if [ -f /etc/apparmor.d/fusermount3 ] && ! grep -qs 'var/lib/sysboxfs' /etc/apparmor.d/local/fusermount3; then
    say "warning: AppArmor confines fusermount3 here and does not allow sysbox's mounts (deploy/k3s/install.sh's fusermount3 rules)"
  fi
}

up() {
  [ "$(uname -m)" = x86_64 ] || die "the rig pins sysbox's amd64 release"
  host_check
  if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    say "building $IMAGE (sysbox $SYSBOX_VERSION, $K3S_IMAGE)"
    docker build -q -t "$IMAGE" \
      --build-arg K3S_IMAGE="$K3S_IMAGE" --build-arg SYSBOX_VERSION="$SYSBOX_VERSION" \
      --build-arg SYSBOX_SHA256="$SYSBOX_SHA256_AMD64" "$HERE" >/dev/null
  fi
  docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
  if ! docker container inspect "$NODE" >/dev/null 2>&1; then
    say "creating $NODE"
    docker volume create "$K3S_VOL" >/dev/null
    docker volume create "$SYSBOX_VOL" >/dev/null
    # Written into the volume itself: docker cp into a container that has
    # not started lands under its mount point, hidden by the volume.
    containerd_template | docker run --rm -i -v "$K3S_VOL":/k3s "$HELPER_IMAGE" sh -c \
      'mkdir -p /k3s/agent/etc/containerd && cat >/k3s/agent/etc/containerd/config-v3.toml.tmpl'
    docker create --name "$NODE" --hostname "$NODE" --privileged --network "$NET" \
      --tmpfs /run --tmpfs /var/run \
      -v "$K3S_VOL":/var/lib/rancher/k3s -v "$SYSBOX_VOL":/var/lib/sysbox \
      "$IMAGE" server --disable traefik --disable metrics-server --disable servicelb >/dev/null
  fi
  docker start "$NODE" >/dev/null
  say "waiting for the node"
  for _ in $(seq 90); do
    kubectl_node get --raw /readyz >/dev/null 2>&1 && kubectl_node get nodes 2>/dev/null | grep -q ' Ready' && break
    sleep 2
  done
  kubectl_node get nodes | grep -q ' Ready' || die "the node is not Ready after 3 minutes (docker logs $NODE; docker exec $NODE cat /var/log/sysbox-mgr.log /var/log/sysbox-fs.log)"
  runtime_class | kubectl_node apply -f - >/dev/null

  say "running a pod under sysbox"
  # A fresh node is Ready before its controllers made the default service
  # account, which a pod needs.
  for _ in $(seq 60); do
    kubectl_node get serviceaccount default >/dev/null 2>&1 && break
    sleep 1
  done
  kubectl_node delete pod apo-sysbox-smoke --ignore-not-found --wait=true >/dev/null
  smoke_pod | kubectl_node apply -f - >/dev/null
  local phase=""
  for _ in $(seq 90); do
    phase=$(kubectl_node get pod apo-sysbox-smoke -o jsonpath='{.status.phase}' 2>/dev/null || true)
    case "$phase" in Succeeded | Failed) break ;; esac
    sleep 2
  done
  if [ "$phase" != Succeeded ]; then
    kubectl_node describe pod apo-sysbox-smoke >&2 || true
    die "a pod under sysbox did not run (phase ${phase:-none}): docker exec $NODE cat /var/log/sysbox-fs.log"
  fi
  kubectl_node delete pod apo-sysbox-smoke --wait=false >/dev/null

  # In a container of the docker daemon itself (the devcontainer), join the
  # node's network to reach it by address, as the kata rig does; on the
  # host's network, the address is reachable already.
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
  [ $# -gt 0 ] || die "usage: deploy/sysbox/rig.sh import <image>…"
  for img in "$@"; do
    say "importing $img"
    docker save "$img" | docker exec -i "$NODE" ctr -n k8s.io images import - >/dev/null
  done
}

down() {
  local self
  self=$("$ROOT/deploy/self-container.sh" id)
  [ -z "$self" ] || docker network disconnect "$NET" "$self" 2>/dev/null || true
  docker rm -f "$NODE" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  docker volume rm "$K3S_VOL" "$SYSBOX_VOL" >/dev/null 2>&1 || true
  rm -f "$KUBECONFIG_OUT"
}

cmd=${1:-}
shift || true
case "$cmd" in
  up) up ;;
  import) import "$@" ;;
  down) down ;;
  *) die "usage: deploy/sysbox/rig.sh up | import <image>… | down" ;;
esac
