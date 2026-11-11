#!/usr/bin/env bash
# Build (and optionally push) the five images of one apothecary release:
#
#   apothecaryd  the engine + UI               (deploy/Dockerfile)
#   sandbox      a Kubernetes run's DinD pod   (deploy/images/sandbox)
#   shim         the in-workspace shim         (workspace/images/shim)
#   tools        the harness + code-server bundle (workspace/images/tools)
#   egress-gw    the per-project gateway       (workspace/images/gateway)
#
# A deployment pulls all five. The per-project browser is Microsoft's
# Playwright MCP image, pinned by the engine (internal/adapters/browser).
#
#   deploy/images.sh <version> [--push]
#   PREFIX=k3d-registry.localhost:5111/apothecary deploy/images.sh local --push
#   EXTRA_TAGS="latest sha-abc123" deploy/images.sh v2.0.7 --push
#   IMAGES="sandbox shim tools egress-gw" deploy/images.sh local
#
# IMAGES picks some of the five (default: all). CACHE=gha keeps each image's
# build cache in GitHub Actions' cache between CI runs; CACHE=gha-warm only
# fills that cache (nothing is loaded into docker); CACHE=gha-read only reads
# it (a job that needs the images another job built). All three need a buildx
# builder of the docker-container driver and the Actions runtime in the
# environment; gha and gha-read load the images into docker as usual.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: deploy/images.sh <version> [--push]}"
PUSH="${2:-}"
PREFIX="${PREFIX:-ghcr.io/intradeus/apothecary}"
read -r -a EXTRA <<< "${EXTRA_TAGS:-}"
read -r -a WANT <<< "${IMAGES:-apothecaryd sandbox shim tools egress-gw}"

wanted() {
  local w
  for w in "${WANT[@]}"; do [ "$w" = "$1" ] && return 0; done
  return 1
}

tags() {
  local name=$1 t
  printf -- '-t\n%s\n' "$PREFIX/$name:$VERSION"
  for t in ${EXTRA[@]+"${EXTRA[@]}"}; do printf -- '-t\n%s\n' "$PREFIX/$name:$t"; done
}

build() {
  local name=$1; shift
  wanted "$name" || return 0
  echo "==> $PREFIX/$name:$VERSION"
  T=()
  while IFS= read -r t; do T+=("$t"); done < <(tags "$name") # no mapfile: macOS ships bash 3.2
  C=()
  case "${CACHE:-}" in
    gha) C=(--cache-from "type=gha,scope=$name" --cache-to "type=gha,mode=max,scope=$name" --load) ;;
    gha-warm) C=(--cache-from "type=gha,scope=$name" --cache-to "type=gha,mode=max,scope=$name") ;;
    gha-read) C=(--cache-from "type=gha,scope=$name" --load) ;;
  esac
  docker build "${T[@]}" ${C[@]+"${C[@]}"} "$@"
}

# hash <shim-hash|tools-hash>: what this engine expects a seeded shim or tools
# bundle to carry — from the engine image when it is built, else from source.
hash() {
  if wanted apothecaryd && [ "${CACHE:-}" != gha-warm ]; then
    docker run --rm "$PREFIX/apothecaryd:$VERSION" "$1"
  else
    go run ./cmd/apothecaryd "$1"
  fi
}

build apothecaryd -f deploy/Dockerfile --build-arg "VERSION=$VERSION" .
if wanted apothecaryd && [ "${CACHE:-}" != gha-warm ]; then
  docker run --rm "$PREFIX/apothecaryd:$VERSION" version
fi
# The shim's source hash, as this engine computes it: a seeded shim must match.
wanted shim && SHIM_HASH=$(hash shim-hash)
# Same for the tools bundle: the hash of its build directory, as the engine
# computes it (a seeded bundle that differs is warned about, not refused).
wanted tools && TOOLS_HASH=$(hash tools-hash)
build sandbox deploy/images/sandbox
build shim -f workspace/images/shim/Dockerfile --build-arg "SHIM_HASH=${SHIM_HASH:-}" workspace
build tools -f workspace/images/tools/Dockerfile --build-arg "TOOLS_HASH=${TOOLS_HASH:-}" workspace/images/tools
build egress-gw -f workspace/images/gateway/Dockerfile workspace

if [ "$PUSH" = "--push" ]; then
  for name in "${WANT[@]}"; do
    docker push "$PREFIX/$name:$VERSION"
    for t in ${EXTRA[@]+"${EXTRA[@]}"}; do docker push "$PREFIX/$name:$t"; done
  done
fi
echo "done: $PREFIX/{${WANT[*]}}:$VERSION"
