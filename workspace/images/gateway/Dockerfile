# The per-project egress gateway. Build context: the repo's workspace/ dir.
#
# Compiles the self-contained gateway daemon (workspace/gateway, pure stdlib)
# into a minimal alpine image with the userland it shells out to: nft and ip,
# plus wg for the Kubernetes run link, and wireguard-go, that link on a kernel
# without the WireGuard module (a Kata guest's, under a k3d cluster in a Kata
# workspace). It IS the egress path, so nothing can be installed at start.
#
# wireguard-go is built from its module at the version the sandbox image pins
# (deploy/images/sandbox/Dockerfile; go test ./internal/devtools/... holds
# the two equal): `go install` checks it against the Go checksum database.
#
# It also carries the distribution `registry` binary (statically linked): the
# gateway supervises it as the project's pull-through Docker Hub cache and
# build cache. Pinned by DIGEST because the image tag hashes this text, not
# upstream content. Digest = registry 2.8.3.
FROM golang:1.26-alpine AS build
COPY gateway/ /src/
RUN cd /src && go mod init apothecary-gateway >/dev/null \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /apothecary-gateway .
FROM golang:1.26-alpine AS wireguard-go
ARG WIREGUARD_GO_VERSION=v0.0.0-20261024220952-b3bc704ef0ee
RUN CGO_ENABLED=0 GOBIN=/out go install -trimpath -ldflags="-s -w" golang.zx2c4.com/wireguard@${WIREGUARD_GO_VERSION}
FROM registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373 AS registry
FROM alpine:3.21
RUN apk add --no-cache nftables iproute2 ip6tables wireguard-tools-wg
COPY --from=registry /bin/registry /usr/local/bin/registry
COPY --from=wireguard-go /out/wireguard /usr/local/bin/wireguard-go
COPY --from=build /apothecary-gateway /usr/local/bin/apothecary-gateway
ENTRYPOINT ["/usr/local/bin/apothecary-gateway"]
