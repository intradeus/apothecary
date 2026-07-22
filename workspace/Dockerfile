# Apothecary workspace BASE image (DESIGN2 §8.1): the invariants and nothing
# else — Ubuntu, git/ripgrep, Node (the npm-distributed harnesses' runtime),
# code-server, the static shim binary, the firewall init, the non-root
# `apothecary` user. NO language toolchains and NO harness: those are the
# user's Dockerfile fragment and the harness kind's InstallLayer, composed
# per project (§8.2). Tagged apothecary/base:<sha16 of this embedded
# context>, built on demand.

# The shim (ask_human MCP server + engine relay) compiles from the embedded
# source right here — static (CGO off), no toolchain on the engine host.
FROM golang:1.26-alpine AS shimbuild
COPY shim/ /src/
RUN cd /src && go mod init apothecary-shim >/dev/null \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /apothecary-shim .

FROM ubuntu:24.04

ARG NODE_MAJOR=22

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git ripgrep jq unzip \
        openssh-client sudo iptables iproute2 dnsutils \
    && rm -rf /var/lib/apt/lists/*

# Node: the shim's runtime (and the install layer of npm-distributed
# harnesses) via nodesource.
RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# code-server for the IDE deep link (§12 links).
RUN curl -fsSL https://code-server.dev/install.sh | sh

# Non-root user; /apothecary receives run.env + provider.env at bootstrap.
RUN userdel -r ubuntu 2>/dev/null || true \
    && useradd --create-home --shell /bin/bash --uid 1000 apothecary \
    && mkdir -p /apothecary && chown apothecary:apothecary /apothecary

# ask_human shim binary (§10) + firewall init (§11).
COPY --from=shimbuild /apothecary-shim /opt/apothecary/shim
COPY init-firewall.sh /usr/local/bin/init-firewall.sh
RUN chmod 0755 /usr/local/bin/init-firewall.sh /opt/apothecary/shim \
    # The ONLY sudo the apothecary user has: firewall init, no arguments.
    # env_keep passes the container's APOTHECARY_* config through (sudo strips
    # env by default; container root cannot read /proc/1/environ without
    # CAP_SYS_PTRACE). The lock file makes re-runs no-ops, so passing
    # different values later cannot alter the rules.
    && { echo "apothecary ALL=(root) NOPASSWD: /usr/local/bin/init-firewall.sh"; \
         echo 'Defaults!/usr/local/bin/init-firewall.sh env_keep += "APOTHECARY_REPO_URL APOTHECARY_CODER_PORT APOTHECARY_EXTRA_DOMAINS APOTHECARY_EGRESS_PROXY"'; \
       } > /etc/sudoers.d/apothecary-firewall \
    && chmod 0440 /etc/sudoers.d/apothecary-firewall

USER apothecary
WORKDIR /home/apothecary
