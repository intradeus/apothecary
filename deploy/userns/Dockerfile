# The userns rig's node (deploy/userns/rig.sh): k3s under systemd in one
# privileged container. systemd is the container's first process because
# the runc-userns handler hands a pod its cgroup only through systemd's
# delegation: under the cgroupfs driver (k3s as the main process, as the
# other rigs run it), the pod's cgroup is mounted read-write but stays owned
# by the node's root, and the sandbox's docker daemon cannot use it. The k3s
# comes from the pin (kata.env's K3S_IMAGE), passed by rig.sh.
ARG K3S_IMAGE
FROM ${K3S_IMAGE} AS k3s

FROM ubuntu:24.04
# systemd and dbus: the cgroup driver; the rest is what k3s's agent calls
# (iptables, mount helpers, ip).
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      systemd systemd-sysv dbus iptables kmod ca-certificates procps iproute2 \
    && rm -rf /var/lib/apt/lists/* \
    && systemctl mask getty.target systemd-logind.service systemd-resolved.service systemd-timesyncd.service \
      systemd-networkd.service apt-daily.timer apt-daily-upgrade.timer >/dev/null 2>&1 || true
# All of k3s's /bin: its CNI plugins (host-local, …) live beside the binary.
COPY --from=k3s /bin /opt/k3s/bin
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/opt/k3s/bin/aux:/opt/k3s/bin
RUN ln -s /opt/k3s/bin/k3s /usr/local/bin/k3s && for b in kubectl ctr crictl; do ln -s /opt/k3s/bin/k3s /usr/local/bin/$b; done
# The kubelet's user-namespace range (rig.sh's IDS_PER_POD, 64 pods of it),
# above the node's own users.
ARG SUBIDS
RUN echo "kubelet:65536:${SUBIDS}" >>/etc/subuid && echo "kubelet:65536:${SUBIDS}" >>/etc/subgid
COPY k3s.service /etc/systemd/system/k3s.service
RUN systemctl enable k3s.service >/dev/null 2>&1
STOPSIGNAL SIGRTMIN+3
ENTRYPOINT ["/sbin/init"]
