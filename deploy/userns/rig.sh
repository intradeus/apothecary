#!/usr/bin/env bash
# A throwaway Kubernetes node whose sandboxes run under the userns runtime:
# k3s under systemd in one privileged container of the docker host
# (deploy/userns/Dockerfile), its containerd given a `runc-userns` handler
# (plain runc, cgroup_writable) and its kubelet a wide user-namespace range.
# It needs docker, not root, and nothing installed on the host. It is never
# a node of a real cluster: its own image, container, network and volumes,
# all named apo-userns-*.
#
#   deploy/userns/rig.sh up              build the node's image, create (or
#                                        start) the node; writes
#                                        deploy/.userns-kubeconfig
#   deploy/userns/rig.sh import <image>… copy images from the docker host into
#                                        the node's containerd (no registry)
#   deploy/userns/rig.sh down            delete the node, its network and its
#                                        volumes
#
# The pin: ../kata/kata.env's K3S_IMAGE (the k3s every rig node runs). The
# handler and the kubelet setting are files in the node's k3s volume,
# written before k3s first starts.
#
# What the node adds to k3s (docs/kubernetes.md, "The userns runtime"):
#   - the `runc-userns` handler: runc behind the runc shim, with the pod's
#     cgroup writable (cgroup_writable = true, containerd 2.1 or later), and
#     the RuntimeClass of the same name the provisioner's userns runtime
#     names;
#   - the kubelet's userNamespaces.idsPerPod (IDS_PER_POD) and the `kubelet`
#     range in /etc/subuid and /etc/subgid that holds them, so a sandbox can
#     give a nested cluster's user-namespaced pods ids of their own. Both are
#     set before the node's first pod: a kubelet refuses to start while a
#     user-namespaced pod runs with another length, so they never change on
#     a live node (down, then up).
# systemd is the node's first process: the handler hands a pod its cgroup
# only through systemd's delegation (the Dockerfile says more). The node's
# kubelet data is a volume of its own, so pod volumes sit on the host's
# filesystem, which takes idmapped mounts, not on the container's overlay.
# Claims are k3s's local-path ones.
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=deploy/kata/kata.env
. "$HERE/../kata/kata.env" # K3S_IMAGE
HELPER_IMAGE=alpine:3.21
SMOKE_IMAGE=busybox:1.37

NODE=apo-userns-node
NET=apo-userns
K3S_VOL=apo-userns-k3s
KUBELET_VOL=apo-userns-kubelet
ROOT=$(cd "$HERE/../.." && pwd)
KUBECONFIG_OUT=$ROOT/deploy/.userns-kubeconfig
# 64 pods' worth of 65536 ids each: a sandbox's nested kubelet takes the
# ids above its first 65536, a pod's share of them each.
IDS_PER_POD=4194304
# The kubelet's range: from 65536 (above the node's own users), 64 sandboxes
# wide, a multiple of IDS_PER_POD as the kubelet requires.
SUBIDS=$((IDS_PER_POD * 64))
IMAGE=apo-userns-node:${K3S_IMAGE##*:}-$IDS_PER_POD

say() { echo "userns rig: $*" >&2; }
die() {
  say "$*"
  exit 1
}

containerd_template() {
  cat <<'EOF'
{{ template "base" . }}

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.'runc-userns']
  runtime_type = "io.containerd.runc.v2"
  cgroup_writable = true
  [plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.'runc-userns'.options]
    SystemdCgroup = {{ .SystemdCgroup }}
EOF
}

kubelet_config() {
  cat <<EOF
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
userNamespaces:
  idsPerPod: $IDS_PER_POD
# The suite's images are imported, never pulled: a kubelet that frees disk
# by deleting them leaves the sandbox in ImagePullBackOff. Image GC and the
# disk eviction thresholds are off, the memory one kept at its default.
imageGCHighThresholdPercent: 100
imageGCLowThresholdPercent: 99
evictionHard:
  imagefs.available: "0%"
  nodefs.available: "0%"
  memory.available: 100Mi
EOF
}

runtime_class() {
  cat <<'EOF'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata: {name: runc-userns}
handler: runc-userns
EOF
}

# A pod shaped as the provisioner's userns sandbox (the class, a user
# namespace of its own, the sandbox container's security context): a node
# that cannot run it fails here, not halfway through the suite. It checks
# what the runtime rests on: a cgroup the pod's root can write (a child made
# and removed), and more than 65536 ids.
smoke_pod() {
  cat <<EOF
apiVersion: v1
kind: Pod
metadata: {name: apo-userns-smoke, namespace: default}
spec:
  runtimeClassName: runc-userns
  hostUsers: false
  restartPolicy: Never
  containers:
    - name: smoke
      image: $SMOKE_IMAGE
      command: [sh, -c, 'mkdir /sys/fs/cgroup/apo-smoke && rmdir /sys/fs/cgroup/apo-smoke && read -r _ _ n </proc/self/uid_map && [ "\$n" -gt 65536 ] && echo userns-ok']
      securityContext:
        procMount: Unmasked
        capabilities: {add: [SYS_ADMIN, NET_ADMIN, MKNOD, SYS_RESOURCE, SYS_PTRACE]}
        seccompProfile: {type: Unconfined}
        appArmorProfile: {type: Unconfined}
EOF
}

kubectl_node() { docker exec -i "$NODE" kubectl "$@"; }

up() {
  if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    say "building $IMAGE ($K3S_IMAGE under systemd)"
    docker build -q -t "$IMAGE" --build-arg K3S_IMAGE="$K3S_IMAGE" --build-arg SUBIDS="$SUBIDS" "$HERE" >/dev/null
  fi
  docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
  if ! docker container inspect "$NODE" >/dev/null 2>&1; then
    say "creating $NODE ($K3S_IMAGE)"
    docker volume create "$K3S_VOL" >/dev/null
    docker volume create "$KUBELET_VOL" >/dev/null
    # Written into the volume itself: docker cp into a container that has
    # not started lands under its mount point, hidden by the volume.
    containerd_template | docker run --rm -i -v "$K3S_VOL":/k3s "$HELPER_IMAGE" sh -c \
      'mkdir -p /k3s/agent/etc/containerd && cat >/k3s/agent/etc/containerd/config-v3.toml.tmpl'
    kubelet_config | docker run --rm -i -v "$K3S_VOL":/k3s "$HELPER_IMAGE" sh -c \
      'mkdir -p /k3s/agent/etc/kubelet.conf.d && cat >/k3s/agent/etc/kubelet.conf.d/50-apo-userns.conf'
    # systemd in a container: its own cgroup namespace, /run on tmpfs.
    docker create --name "$NODE" --hostname "$NODE" --privileged --network "$NET" \
      --cgroupns private --tmpfs /run --tmpfs /run/lock \
      -v "$K3S_VOL":/var/lib/rancher/k3s -v "$KUBELET_VOL":/var/lib/kubelet \
      "$IMAGE" >/dev/null
  fi
  docker start "$NODE" >/dev/null
  say "waiting for the node"
  for _ in $(seq 90); do
    kubectl_node get --raw /readyz >/dev/null 2>&1 && kubectl_node get nodes 2>/dev/null | grep -q ' Ready' && break
    sleep 2
  done
  kubectl_node get nodes | grep -q ' Ready' || die "the node is not Ready after 3 minutes (docker exec $NODE journalctl -u k3s)"
  runtime_class | kubectl_node apply -f - >/dev/null

  say "running a pod under runc-userns"
  # A fresh node is Ready before its controllers made the default service
  # account, which a pod needs.
  for _ in $(seq 60); do
    kubectl_node get serviceaccount default >/dev/null 2>&1 && break
    sleep 1
  done
  kubectl_node delete pod apo-userns-smoke --ignore-not-found --wait=true >/dev/null
  smoke_pod | kubectl_node apply -f - >/dev/null
  local phase=""
  for _ in $(seq 90); do
    phase=$(kubectl_node get pod apo-userns-smoke -o jsonpath='{.status.phase}' 2>/dev/null || true)
    case "$phase" in Succeeded | Failed) break ;; esac
    sleep 2
  done
  if [ "$phase" != Succeeded ]; then
    kubectl_node describe pod apo-userns-smoke >&2 || true
    kubectl_node logs apo-userns-smoke >&2 || true
    die "a pod under runc-userns did not pass (phase ${phase:-none}): it needs a writable cgroup and more than 65536 ids"
  fi
  kubectl_node delete pod apo-userns-smoke --wait=false >/dev/null

  # In a container of the docker daemon itself (the devcontainer), join the
  # node's network to reach it by address, as the other rigs do; on the
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
  [ $# -gt 0 ] || die "usage: deploy/userns/rig.sh import <image>…"
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
  docker volume rm "$K3S_VOL" "$KUBELET_VOL" >/dev/null 2>&1 || true
  rm -f "$KUBECONFIG_OUT"
}

cmd=${1:-}
shift || true
case "$cmd" in
  up) up ;;
  import) import "$@" ;;
  down) down ;;
  *) die "usage: deploy/userns/rig.sh up | import <image>… | down" ;;
esac
