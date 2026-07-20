# Apothecary service image (DESIGN2.md §15): factoryd + built CPUI + the
# pinned coder CLI + the docker CLI (workspace image builds via the mounted
# socket). Build from the repo root:
#
#   docker build -f deploy/Dockerfile -t apothecary/factoryd:dev .

# --- CPUI --------------------------------------------------------------------
FROM node:22-bookworm-slim AS cpui
WORKDIR /src
COPY cpui/package.json cpui/package-lock.json ./
RUN npm ci
COPY cpui/ .
RUN npm run build

# --- factoryd ----------------------------------------------------------------
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/factoryd ./cmd/factoryd

# --- runtime -----------------------------------------------------------------
FROM debian:bookworm-slim

ARG CODER_VERSION=2.35.2

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl docker.io git \
    && rm -rf /var/lib/apt/lists/* \
    # docker.io pulls the daemon too on some bases; only the CLI is needed —
    # the daemon arrives via the mounted /var/run/docker.sock.
    && (dpkg -l docker.io >/dev/null 2>&1 || true)

# coder CLI, pinned per release (CLI<->server minor skew is tolerated; see
# the README for upgrade guidance). The archive member is "./coder".
RUN curl -fsSL "https://github.com/coder/coder/releases/download/v${CODER_VERSION}/coder_${CODER_VERSION}_linux_amd64.tar.gz" \
        | tar -xz -C /usr/local/bin ./coder \
    && chmod 0755 /usr/local/bin/coder

COPY --from=build /out/factoryd /usr/local/bin/factoryd
COPY --from=cpui /src/build /opt/apothecary/cpui

ENV FACTORY_CPUI_DIR=/opt/apothecary/cpui \
    FACTORY_RUNS_DIR=/var/lib/factory/runs

VOLUME /var/lib/factory/runs

ENTRYPOINT ["factoryd"]
CMD ["serve"]
