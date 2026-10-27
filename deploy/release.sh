#!/usr/bin/env bash
# Release build (DESIGN2 §15 / M11): the version-stamped images of a release.
#
#   deploy/release.sh v2.0.0            build apothecary/*:v2.0.0 (+ :latest)
#   deploy/release.sh                   version = git describe
#   REGISTRY=ghcr.io/you deploy/release.sh v2.0.0
#                                       build as <registry>/apothecary/* and push
#
# The compose file honors APOTHECARY_IMAGE, so a docker deployment pins a
# release with e.g. APOTHECARY_IMAGE=apothecary/apothecaryd:v2.0.0 in
# deploy/.env; the Helm chart pins all five with image.tag.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
if [ -n "${REGISTRY:-}" ]; then
  PREFIX="${REGISTRY}/apothecary" EXTRA_TAGS=latest deploy/images.sh "$VERSION" --push
else
  PREFIX=apothecary EXTRA_TAGS=latest deploy/images.sh "$VERSION"
fi
