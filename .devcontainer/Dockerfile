# The dev container (docker-compose.yml builds it with the repo root as the
# context, so go.mod/go.sum are at hand). Pins, each held to its source of
# truth by internal/devtools/devcontainer, so this container, CI and a laptop
# run the same tools:
#   GO_VERSION         = go.mod's `go` line (the Taskfile's GOTOOLCHAIN)
#   NODE_VERSION       = .nvmrc (deploy/Dockerfile's node image, CI's setup-node)
#   HELM_VERSION       = CI's setup-helm version
#   TASK_VERSION       = CI's setup-task version
#   SHELLCHECK_VERSION = the shellcheck CI installs for actionlint
# Base images come from Docker Hub (a run pulls them through the project
# gateway's mirror); the build dials only the Go module proxy, Debian's
# mirrors and get.helm.sh beyond that.
ARG GO_VERSION=1.26.4
ARG NODE_VERSION=26.13.1

# Node from the official image (same Debian base), no third-party apt repo.
FROM node:${NODE_VERSION}-bookworm-slim AS node

FROM golang:${GO_VERSION}-bookworm
ARG HELM_VERSION=4.3.0
ARG TASK_VERSION=v3.54.0
ARG SHELLCHECK_VERSION=0.11.0
ARG USERNAME=dev

# sudo: a human's convenience. iproute2: testcontainers-go finds the docker
# host through `ip route` when it runs in a container.
RUN apt-get update && apt-get install -y --no-install-recommends \
      sudo iproute2 jq less curl ca-certificates xz-utils \
    && rm -rf /var/lib/apt/lists/*

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

# helm, from its release host, against the checksums it publishes.
RUN set -eu; arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) sum=86584a54def73570558f66f5111cc53dfed56689637ae32c1201205d494f54fb ;; \
      arm64) sum=31c5794dd55c66a51e6b7d2e2ac7a114ae8b1de41ff1d9ba51748ac973b06a08 ;; \
      *) echo "no helm checksum for $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/helm.tgz "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${arch}.tar.gz"; \
    echo "$sum  /tmp/helm.tgz" | sha256sum -c -; \
    tar -xzf /tmp/helm.tgz -C /tmp "linux-${arch}/helm"; \
    mv "/tmp/linux-${arch}/helm" /usr/local/bin/helm; \
    rm -rf /tmp/helm.tgz "/tmp/linux-${arch}"

# go-task through the module proxy (sum.golang.org-verified).
RUN GOBIN=/usr/local/bin go install "github.com/go-task/task/v3/cmd/task@${TASK_VERSION}"

# shellcheck, which actionlint runs on the workflows (task repo:lint). Its
# releases publish no checksums: these were computed when it was pinned, and
# CI's repo-lint job checks the same amd64 one.
RUN set -eu; case "$(dpkg --print-architecture)" in \
      amd64) a=x86_64; sum=8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198 ;; \
      arm64) a=aarch64; sum=12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588 ;; \
      *) echo "no shellcheck checksum for $(dpkg --print-architecture)" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/sc.tar.xz "https://github.com/koalaman/shellcheck/releases/download/v${SHELLCHECK_VERSION}/shellcheck-v${SHELLCHECK_VERSION}.linux.${a}.tar.xz"; \
    echo "$sum  /tmp/sc.tar.xz" | sha256sum -c -; \
    tar -xJf /tmp/sc.tar.xz -C /tmp; \
    mv "/tmp/shellcheck-v${SHELLCHECK_VERSION}/shellcheck" /usr/local/bin/shellcheck; \
    rm -rf /tmp/sc.tar.xz "/tmp/shellcheck-v${SHELLCHECK_VERSION}"

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
