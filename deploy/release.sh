#!/usr/bin/env bash
# Release build (DESIGN2 §15 / M11): version-stamped apothecaryd image.
#
#   deploy/release.sh v2.0.0            build apothecary/apothecaryd:v2.0.0 (+ :latest)
#   deploy/release.sh                   version = git describe
#   REGISTRY=ghcr.io/you deploy/release.sh v2.0.0
#                                       also tag + push to a registry
#
# The compose file honors APOTHECARY_IMAGE, so a deployment pins a release
# with e.g. APOTHECARY_IMAGE=apothecary/apothecaryd:v2.0.0 in deploy/.env.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
IMAGE="apothecary/apothecaryd"

echo "building ${IMAGE}:${VERSION}"
docker build -f deploy/Dockerfile --build-arg "VERSION=${VERSION}" \
  -t "${IMAGE}:${VERSION}" -t "${IMAGE}:latest" .

docker run --rm "${IMAGE}:${VERSION}" version

if [ -n "${REGISTRY:-}" ]; then
  docker tag "${IMAGE}:${VERSION}" "${REGISTRY}/${IMAGE}:${VERSION}"
  docker tag "${IMAGE}:${VERSION}" "${REGISTRY}/${IMAGE}:latest"
  docker push "${REGISTRY}/${IMAGE}:${VERSION}"
  docker push "${REGISTRY}/${IMAGE}:latest"
  echo "pushed ${REGISTRY}/${IMAGE}:${VERSION}"
fi

echo "done: ${IMAGE}:${VERSION}"
