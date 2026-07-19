# Factory workspace image (DESIGN.md §11): Ubuntu base, Go + Node
# toolchains, opencode (pinned), git, ripgrep, the ask_human shim,
# init-firewall.sh, non-root user `factory`. No docker socket is ever
# mounted; NET_ADMIN is granted only for the firewall init (template).
FROM ubuntu:24.04

ARG GO_VERSION=1.24.5
ARG OPENCODE_VERSION=1.17.8
ARG NODE_MAJOR=22

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git ripgrep jq make build-essential unzip \
        openssh-client sudo iptables iproute2 dnsutils \
    && rm -rf /var/lib/apt/lists/*

# Node (toolchain + shim runtime) via nodesource.
RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Go toolchain.
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

# opencode, pinned (§0 harness; flags verified in M3).
RUN npm install -g "opencode-ai@${OPENCODE_VERSION}"

# code-server for the IDE deep link (§12 links).
RUN curl -fsSL https://code-server.dev/install.sh | sh

# Non-root user; /factory receives run.env + provider.env at bootstrap.
RUN userdel -r ubuntu 2>/dev/null || true \
    && useradd --create-home --shell /bin/bash --uid 1000 factory \
    && mkdir -p /factory && chown factory:factory /factory

# ask_human shim (§10) + firewall init (§11).
COPY ask-human.mjs /opt/factory/ask-human.mjs
COPY init-firewall.sh /usr/local/bin/init-firewall.sh
RUN chmod 0755 /usr/local/bin/init-firewall.sh /opt/factory/ask-human.mjs \
    # The ONLY sudo the factory user has: firewall init, no arguments.
    # env_keep passes the container's FACTORY_* config through (sudo strips
    # env by default; container root cannot read /proc/1/environ without
    # CAP_SYS_PTRACE). The lock file makes re-runs no-ops, so passing
    # different values later cannot alter the rules.
    && { echo "factory ALL=(root) NOPASSWD: /usr/local/bin/init-firewall.sh"; \
         echo 'Defaults!/usr/local/bin/init-firewall.sh env_keep += "FACTORY_REPO_URL FACTORY_CODER_PORT FACTORY_EXTRA_DOMAINS"'; \
       } > /etc/sudoers.d/factory-firewall \
    && chmod 0440 /etc/sudoers.d/factory-firewall

USER factory
WORKDIR /home/factory
ENV PATH="/usr/local/go/bin:/home/factory/go/bin:${PATH}"
