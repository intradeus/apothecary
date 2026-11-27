# syntax=docker/dockerfile:1
# A k3s node that runs inside a userns sandbox (the provisioner's userns
# runtime: plain runc in the pod's own user namespace), on the sandbox's
# docker daemon. One definition, built where it is used, from this file
# alone (no build context: `docker build - < this`):
#   - task k8s:cluster, from a workspace in such a sandbox, as k3d's node
#     image (k3d runs every /bin/k3d-entrypoint-*.sh before k3s);
#   - the live suite's nested check (liveNestedKubernetes), with
#     `docker run` (this image's entrypoint does k3d's cgroup step first).
#
# Outside a user namespace (a laptop, CI, a privileged or kata sandbox) the
# setup does nothing and the node is stock k3s. Inside one it gives k3s:
#   - the kubelet's KubeletInUserNamespace gate: the kubelet otherwise stops
#     at what a user namespace refuses it (oom_score_adj, some sysctls);
#   - with /bin/apo-runc mounted in and a read-only /sys (the sandbox's
#     docker runs this node through apo-runc), apo-runc as containerd's runc:
#     a privileged pod asks for a writable sysfs, which the kernel refuses
#     where only a read-only one is visible (workspace/aporunc says why);
#   - with more than 131072 ids (the provisioner's nested_user_namespaces),
#     what a user-namespaced pod on the node needs: a `kubelet` range above
#     the sandbox's own 65536 ids (a kubelet user and a getsubids that
#     answers it), max-pods that fits that range, and the kubelet's and
#     containerd's state on tmpfs (an idmapped mount needs a filesystem
#     mounted in this user namespace: the node's images are then held in
#     memory, counted against the sandbox's limit).
ARG K3S_IMAGE
FROM ${K3S_IMAGE}
COPY --chmod=0755 <<'EOF' /bin/k3d-entrypoint-apo-userns.sh
#!/bin/sh
set -eu
# The node's own users (the identity map): nothing to do.
awk '$1 == 0 && $2 == 0 { found = 1 } END { exit found ? 0 : 1 }' /proc/self/uid_map && exit 0
ids=$(awk '{ n += $3 } END { print n }' /proc/self/uid_map)
args="  - feature-gates=KubeletInUserNamespace=true"
if [ -x /bin/apo-runc ] && awk '$2 == "/sys" && $4 ~ /(^|,)ro(,|$)/ { found = 1 } END { exit found ? 0 : 1 }' /proc/self/mounts; then
  d=/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.d
  mkdir -p "$d"
  cat >"$d/50-apo-runc.toml" <<'TOML'
version = 3
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc.options]
  BinaryName = "/bin/apo-runc"
TOML
fi
if [ "$ids" -gt 131072 ]; then
  range=$(((ids - 65536) / 65536 * 65536))
  grep -q '^kubelet:' /etc/passwd || echo 'kubelet:x:65535:65535::/:/bin/false' >>/etc/passwd
  printf '#!/bin/sh\n# the kubelet asks: getsubids [-g] kubelet\necho "0: kubelet 65536 %s"\n' "$range" >/bin/getsubids
  chmod 0755 /bin/getsubids
  # Over whatever is there: /var/lib/kubelet is a VOLUME of the k3s image,
  # which docker backs with a directory on its own disk.
  for d in /var/lib/kubelet /var/lib/rancher/k3s/agent/containerd; do
    mkdir -p "$d"
    awk -v d="$d" '$2 == d && $3 == "tmpfs" { found = 1 } END { exit found ? 0 : 1 }' /proc/self/mounts ||
      mount -t tmpfs -o mode=0755 tmpfs "$d"
  done
  args="$args
  - max-pods=$((range / 65536))"
fi
mkdir -p /etc/rancher/k3s/config.yaml.d
printf 'kubelet-arg+:\n%s\n' "$args" >/etc/rancher/k3s/config.yaml.d/50-apo-userns.yaml
EOF
COPY --chmod=0755 <<'EOF' /bin/apo-node
#!/bin/sh
# Outside k3d: k3d's cgroup v2 step (every process into a child cgroup, the
# controllers enabled for the kubelet's), the setup above, then k3s.
set -eu
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
  mkdir -p /sys/fs/cgroup/init
  xargs -rn1 </sys/fs/cgroup/cgroup.procs >/sys/fs/cgroup/init/cgroup.procs || :
  sed -e 's/ / +/g' -e 's/^/+/' </sys/fs/cgroup/cgroup.controllers >/sys/fs/cgroup/cgroup.subtree_control
fi
/bin/k3d-entrypoint-apo-userns.sh
exec /bin/k3s "$@"
EOF
ENTRYPOINT ["/bin/apo-node"]
