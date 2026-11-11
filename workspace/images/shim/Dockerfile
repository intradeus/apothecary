# The static in-workspace shim. Build context: the repo's workspace/ dir.
#
# CI publishes it as the apothecary/shim image a sandbox pod's shim-seed init
# container copies onto the run volume (`shim seed`).
#
# SHIM_HASH is the hash of the shim's sources (`apothecaryd shim-hash`), baked
# in so a seeded shim can be checked against the engine that expects it.
# The gojq and x/crypto pins must equal the root go.mod's
# (TestShimDependencyPinsMatchGoMod).
FROM golang:1.26-alpine AS build
ARG SHIM_HASH=dev
COPY shim/ /src/
RUN cd /src && go mod init apothecary-shim >/dev/null \
    && go get github.com/itchyny/gojq/cli@v0.12.19 \
    && go get golang.org/x/crypto/ssh@v0.57.0 \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.sourceHash=${SHIM_HASH}" -o /apothecary-shim .
FROM scratch
COPY --from=build /apothecary-shim /apothecary-shim
ENTRYPOINT ["/apothecary-shim"]
