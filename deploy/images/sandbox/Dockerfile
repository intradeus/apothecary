# The Kubernetes sandbox: one pod per run runs this image as its Docker-in-
# Docker daemon, and the engine `kubectl exec`s the same tools the docker
# host runs engine-side — git (the clone), the devcontainer CLI (build, exec)
# and the docker CLI (with its compose and buildx plugins, which docker:dind
# ships) — against that daemon. WireGuard and iproute2 bring up the run's
# tunnel to its project's egress gateway (apo-wg-up); wireguard-go is that
# tunnel on a kernel without the WireGuard module (a Kata guest's). Build
# context: this dir.
#
# wireguard-go is built from its module at a pinned version: `go install`
# checks the module against the Go checksum database, the pin's checksum.
FROM golang:1.26-alpine AS wireguard-go
ARG WIREGUARD_GO_VERSION=v0.0.0-20261024220952-b3bc704ef0ee
RUN CGO_ENABLED=0 GOBIN=/out go install -trimpath -ldflags="-s -w" golang.zx2c4.com/wireguard@${WIREGUARD_GO_VERSION}

FROM docker:29-dind
ARG DEVCONTAINER_CLI_VERSION=0.86.0
# e2fsprogs: a Kata sandbox on a raw-block claim formats it (apo-dockerd).
RUN apk add --no-cache bash git nodejs npm wireguard-tools-wg iproute2 curl coreutils findutils grep e2fsprogs \
    && npm install -g @devcontainers/cli@${DEVCONTAINER_CLI_VERSION} \
    && npm cache clean --force
COPY --from=wireguard-go /out/wireguard /usr/local/bin/wireguard-go
COPY apo-dockerd apo-wg-up /usr/local/bin/
CMD ["apo-dockerd"]
