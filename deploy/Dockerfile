# Apothecary service image (DESIGN2.md §15): apothecaryd + built CPUI +
# kubectl (the Kubernetes provisioner: sandbox pods, gateways, browsers). The
# devcontainer and docker CLIs run inside each run's sandbox pod (the sandbox
# image), never here. Build from the repo root:
#
#   docker build -f deploy/Dockerfile -t apothecary/apothecaryd:dev .

# --- CPUI --------------------------------------------------------------------
FROM node:26.13.1-bookworm-slim AS cpui
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

# kubectl drives the Kubernetes provisioner (sandbox pods, gateways,
# browsers). Pinned; bump per release, with .devcontainer/Dockerfile's
# KUBECTL_VERSION (internal/devtools/devcontainer holds them together).
ARG KUBECTL_VERSION=v1.34.1

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" -o /usr/local/bin/kubectl \
    && chmod 0755 /usr/local/bin/kubectl \
    && apt-get purge -y curl && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/apothecaryd /usr/local/bin/apothecaryd
COPY --from=cpui /src/build /opt/apothecary/cpui

ENV APOTHECARY_CPUI_DIR=/opt/apothecary/cpui \
    APOTHECARY_RUNS_DIR=/var/lib/apothecary/runs

VOLUME /var/lib/apothecary/runs

ENTRYPOINT ["apothecaryd"]
CMD ["serve"]
