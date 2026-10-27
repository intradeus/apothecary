# Apothecary service image (DESIGN2.md §15): apothecaryd + built CPUI + the
# docker CLI (workspace image builds via the mounted socket) + Node & the pinned @devcontainers/cli (the docker provisioner
# shells out to it) + kubectl (the Kubernetes provisioner). Build from the
# repo root:
#
#   docker build -f deploy/Dockerfile -t apothecary/apothecaryd:dev .

# --- CPUI --------------------------------------------------------------------
FROM node:22-bookworm-slim AS cpui
WORKDIR /src
COPY cpui/package.json cpui/package-lock.json ./
RUN npm ci
COPY cpui/ .
RUN npm run build

# --- apothecaryd ----------------------------------------------------------------
FROM golang:1.26-bookworm AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /out/apothecaryd ./cmd/apothecaryd

# --- runtime -----------------------------------------------------------------
FROM debian:bookworm-slim

# @devcontainers/cli is a Node CLI, so Node ships alongside it. Pinned;
# bump per release.
ARG DEVCONTAINER_CLI_VERSION=0.86.0
# Docker CLI plugins. Debian's docker.io ships ONLY the plain `docker` CLI —
# no buildx, no compose — so Compose-based devcontainers (dockerComposeFile)
# fail with `spawn docker-compose ENOENT`. Drop the official plugin binaries
# into the CLI plugins dir; pin + bump per release like the CLIs above.
ARG COMPOSE_VERSION=v2.40.0
ARG BUILDX_VERSION=v0.20.1
# kubectl drives the Kubernetes provisioner (sandbox pods, gateways, browsers)
# the way the docker CLI drives the docker one. Pinned; bump per release.
ARG KUBECTL_VERSION=v1.34.1

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl docker.io git \
    # Node 22 (nodesource) is the runtime for @devcontainers/cli, which the
    # devcontainer provisioner execs (`devcontainer up/exec`). docker.io
    # pulls the daemon too on some bases; only the CLI is used — the daemon
    # arrives via the mounted /var/run/docker.sock.
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && npm install -g @devcontainers/cli@${DEVCONTAINER_CLI_VERSION} \
    && npm cache clean --force \
    && rm -rf /var/lib/apt/lists/*

# Compose v2 + buildx plugins for the docker CLI. `devcontainer up` on a
# Compose project invokes `docker compose`; the docker-compose symlink covers
# tools/paths that still spawn the legacy standalone binary.
RUN mkdir -p /usr/local/lib/docker/cli-plugins \
    && curl -fsSL "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-x86_64" \
        -o /usr/local/lib/docker/cli-plugins/docker-compose \
    && curl -fsSL "https://github.com/docker/buildx/releases/download/${BUILDX_VERSION}/buildx-${BUILDX_VERSION}.linux-amd64" \
        -o /usr/local/lib/docker/cli-plugins/docker-buildx \
    && chmod 0755 /usr/local/lib/docker/cli-plugins/docker-compose \
                  /usr/local/lib/docker/cli-plugins/docker-buildx \
    && ln -s /usr/local/lib/docker/cli-plugins/docker-compose /usr/local/bin/docker-compose

RUN curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" -o /usr/local/bin/kubectl \
    && chmod 0755 /usr/local/bin/kubectl

COPY --from=build /out/apothecaryd /usr/local/bin/apothecaryd
COPY --from=cpui /src/build /opt/apothecary/cpui

ENV APOTHECARY_CPUI_DIR=/opt/apothecary/cpui \
    APOTHECARY_RUNS_DIR=/var/lib/apothecary/runs

VOLUME /var/lib/apothecary/runs

ENTRYPOINT ["apothecaryd"]
CMD ["serve"]
