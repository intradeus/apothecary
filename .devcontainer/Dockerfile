# The dev container (docker-compose.yml builds it with the repo root as the
# context, so go.mod/go.sum are at hand). Pins, each held to its source of
# truth by internal/devtools/devcontainer:
#   GO_VERSION   = go.mod's `go` line
#   NODE_MAJOR   = deploy/Dockerfile's node image
#   HELM_VERSION = .github/workflows/ci.yml's setup-helm version
# Base images come from Docker Hub (a run pulls them through the project
# gateway's mirror); the build dials only the Go module proxy, Debian's
# mirrors and get.helm.sh beyond that.
ARG GO_VERSION=1.26.4
ARG NODE_MAJOR=22

# Node from the official image (same Debian base), no third-party apt repo.
FROM node:${NODE_MAJOR}-bookworm-slim AS node

FROM golang:${GO_VERSION}-bookworm
ARG HELM_VERSION=3.16.2
# go-task (CI: arduino/setup-task "3.x"); check the current v3 when bumping.
ARG TASK_VERSION=v3.54.0
ARG USERNAME=dev

# sudo: a human's convenience. iproute2: testcontainers-go finds the docker
# host through `ip route` when it runs in a container.
RUN apt-get update && apt-get install -y --no-install-recommends \
      sudo iproute2 jq less curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && ln -s ../lib/node_modules/corepack/dist/corepack.js /usr/local/bin/corepack

# helm, from its release host, against the checksums it publishes.
RUN set -eu; arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) sum=9318379b847e333460d33d291d4c088156299a26cd93d570a7f5d0c36e50b5bb ;; \
      arm64) sum=1888301aeb7d08a03b6d9f4d2b73dcd09b89c41577e80e3455c113629fc657a4 ;; \
      *) echo "no helm checksum for $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/helm.tgz "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${arch}.tar.gz"; \
    echo "$sum  /tmp/helm.tgz" | sha256sum -c -; \
    tar -xzf /tmp/helm.tgz -C /tmp "linux-${arch}/helm"; \
    mv "/tmp/linux-${arch}/helm" /usr/local/bin/helm; \
    rm -rf /tmp/helm.tgz "/tmp/linux-${arch}"

# go-task through the module proxy (sum.golang.org-verified).
RUN GOBIN=/usr/local/bin go install "github.com/go-task/task/v3/cmd/task@${TASK_VERSION}"

RUN useradd -m -s /bin/bash -u 1000 "${USERNAME}" \
    && echo "${USERNAME} ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/${USERNAME}" \
    && chmod 0440 "/etc/sudoers.d/${USERNAME}"

# Pre-warm the module cache: the engine's build cache then makes a run's first
# `go build`/`go test` fast. Owned by the dev user so new modules can land.
WORKDIR /tmp/warm
COPY go.mod go.sum ./
RUN go mod download && rm -rf /tmp/warm && chown -R "${USERNAME}:${USERNAME}" /go

# The node_modules volume's mount point, owned by the dev user so the fresh
# volume (docker-compose.yml) is writable by `npm ci`.
RUN mkdir -p /workspaces/apothecary/cpui/node_modules \
    && chown -R "${USERNAME}:${USERNAME}" /workspaces/apothecary
WORKDIR /workspaces/apothecary
