# The sysbox rig's node (deploy/sysbox/rig.sh): k3s, and beside it sysbox's
# own daemons, in one privileged container, so a docker host with nothing of
# sysbox installed (a CI runner) gets a node whose pods run under it. The
# k3s and the sysbox release come from the pins (kata.env's K3S_IMAGE,
# sysbox.env's version and sha256), passed by rig.sh.
ARG K3S_IMAGE
FROM ${K3S_IMAGE} AS k3s

# sysbox's three static binaries, from the pinned release's .deb.
FROM ubuntu:24.04 AS sysbox
ARG SYSBOX_VERSION
ARG SYSBOX_SHA256
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL --retry 3 -o /tmp/sysbox.deb \
      "https://github.com/nestybox/sysbox/releases/download/v${SYSBOX_VERSION}/sysbox-ce_${SYSBOX_VERSION}.linux_amd64.deb" \
    && echo "${SYSBOX_SHA256}  /tmp/sysbox.deb" | sha256sum -c --quiet - \
    && dpkg-deb -x /tmp/sysbox.deb /tmp/x \
    && install -m 0755 /tmp/x/usr/bin/sysbox-runc /tmp/x/usr/bin/sysbox-mgr /tmp/x/usr/bin/sysbox-fs /usr/local/bin/

FROM ubuntu:24.04
# fuse3: sysbox-fs serves each sandbox's /proc and /sys over FUSE; the rest
# is what k3s's agent calls (iptables, mount helpers, ip).
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      fuse3 rsync iptables kmod ca-certificates procps iproute2 \
    && rm -rf /var/lib/apt/lists/*
# All of k3s's /bin: its CNI plugins (host-local, …) live beside the binary.
COPY --from=k3s /bin /opt/k3s/bin
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/opt/k3s/bin/aux:/opt/k3s/bin
RUN ln -s /opt/k3s/bin/k3s /bin/k3s && for b in kubectl ctr crictl; do ln -s /bin/k3s /usr/local/bin/$b; done
COPY --from=sysbox /usr/local/bin/ /usr/bin/
# The ids sysbox maps each sandbox's user namespace into (the .deb's
# postinst does this on a real box).
RUN useradd -r -s /usr/sbin/nologin sysbox \
    && echo 'sysbox:200000:268435456' >>/etc/subuid && echo 'sysbox:200000:268435456' >>/etc/subgid
COPY start.sh /start.sh
ENTRYPOINT ["/start.sh"]
