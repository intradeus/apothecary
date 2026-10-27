#!/usr/bin/env bash
# Build (and optionally push) the six images of one apothecary release:
#
#   apothecaryd  the engine + UI               (deploy/Dockerfile)
#   sandbox      a Kubernetes run's DinD pod   (deploy/images/sandbox)
#   shim         the in-workspace shim         (workspace/images/shim)
#   tools        the harness + code-server bundle (workspace/images/tools)
#   egress-gw    the per-project gateway       (workspace/images/gateway)
#   browser      the per-project browser       (workspace/images/browser)
#
# A docker deployment needs only apothecaryd (it builds the rest itself); a
# Kubernetes one pulls all six.
#
#   deploy/images.sh <version> [--push]
#   PREFIX=k3d-registry.localhost:5111/apothecary deploy/images.sh local --push
#   EXTRA_TAGS="latest sha-abc123" deploy/images.sh v2.0.7 --push
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: deploy/images.sh <version> [--push]}"
PUSH="${2:-}"
PREFIX="${PREFIX:-ghcr.io/intradeus/apothecary}"
read -r -a EXTRA <<< "${EXTRA_TAGS:-}"

tags() {
  local name=$1 t
  printf -- '-t\n%s\n' "$PREFIX/$name:$VERSION"
  for t in "${EXTRA[@]}"; do printf -- '-t\n%s\n' "$PREFIX/$name:$t"; done
}

build() {
  local name=$1; shift
  echo "==> $PREFIX/$name:$VERSION"
  mapfile -t T < <(tags "$name")
  docker build "${T[@]}" "$@"
}

build apothecaryd -f deploy/Dockerfile --build-arg "VERSION=$VERSION" .
docker run --rm "$PREFIX/apothecaryd:$VERSION" version
# The shim's source hash, as this engine computes it: a seeded shim must match.
SHIM_HASH=$(docker run --rm "$PREFIX/apothecaryd:$VERSION" shim-hash)
# Same for the tools bundle: the hash of its build directory, as the engine
# computes it (a seeded bundle that differs is warned about, not refused).
TOOLS_HASH=$(docker run --rm "$PREFIX/apothecaryd:$VERSION" tools-hash)
build sandbox deploy/images/sandbox
build shim -f workspace/images/shim/Dockerfile --build-arg "SHIM_HASH=$SHIM_HASH" workspace
build tools -f workspace/images/tools/Dockerfile --build-arg "TOOLS_HASH=$TOOLS_HASH" workspace/images/tools
build egress-gw -f workspace/images/gateway/Dockerfile workspace
build browser -f workspace/images/browser/Dockerfile --build-arg "SHIM_HASH=$SHIM_HASH" workspace

if [ "$PUSH" = "--push" ]; then
  for name in apothecaryd sandbox shim tools egress-gw browser; do
    docker push "$PREFIX/$name:$VERSION"
    for t in "${EXTRA[@]}"; do docker push "$PREFIX/$name:$t"; done
  done
fi
echo "done: $PREFIX/{apothecaryd,sandbox,shim,tools,egress-gw,browser}:$VERSION"
