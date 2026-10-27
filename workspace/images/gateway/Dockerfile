# The per-project egress gateway. Build context: the repo's workspace/ dir.
#
# Compiles the self-contained gateway daemon (workspace/gateway, pure stdlib)
# into a minimal alpine image with the userland it shells out to: nft and ip,
# plus wg for the Kubernetes run link. It IS the egress path, so nothing can be
# installed at start.
#
# It also carries the distribution `registry` binary (statically linked): the
# gateway supervises it as the project's pull-through Docker Hub cache and
# build cache. Pinned by DIGEST because the image tag hashes this text, not
# upstream content. Digest = registry 2.8.3.
FROM golang:1.26-alpine AS build
COPY gateway/ /src/
RUN cd /src && go mod init apothecary-gateway >/dev/null \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /apothecary-gateway .
FROM registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373 AS registry
FROM alpine:3.21
RUN apk add --no-cache nftables iproute2 ip6tables wireguard-tools-wg
COPY --from=registry /bin/registry /usr/local/bin/registry
COPY --from=build /apothecary-gateway /usr/local/bin/apothecary-gateway
ENTRYPOINT ["/usr/local/bin/apothecary-gateway"]
