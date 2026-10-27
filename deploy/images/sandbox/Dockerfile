# The Kubernetes sandbox: one pod per run runs this image as its Docker-in-
# Docker daemon, and the engine `kubectl exec`s the same tools the docker
# host runs engine-side — git (the clone), the devcontainer CLI (build, exec)
# and the docker CLI (with its compose and buildx plugins, which docker:dind
# ships) — against that daemon. WireGuard and iproute2 bring up the run's
# tunnel to its project's egress gateway (apo-wg-up). Build context: this dir.
FROM docker:29-dind
ARG DEVCONTAINER_CLI_VERSION=0.86.0
RUN apk add --no-cache bash git nodejs npm wireguard-tools-wg iproute2 curl coreutils findutils grep \
    && npm install -g @devcontainers/cli@${DEVCONTAINER_CLI_VERSION} \
    && npm cache clean --force
COPY apo-dockerd apo-wg-up /usr/local/bin/
CMD ["apo-dockerd"]
