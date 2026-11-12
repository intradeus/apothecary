#!/usr/bin/env bash
# Semver releases (rules in .cz.toml; the flow in .github/workflows/manual-release.yml).
# Releases are cut by CI only; a local versioned build is
# `PREFIX=apothecary deploy/images.sh <version>`.
#
#   deploy/release.sh preview             the version + changelog section the next
#                                         release of HEAD would get (dry run)
#   deploy/release.sh plan <sha>          mode=release|resume|superseded|none, version=
#                                         (RELEASE_NOTES_FILE=f also writes the notes)
#   deploy/release.sh cut                 bump: CHANGELOG.md commit + annotated tag on
#                                         HEAD (must be origin/main); prints the tag
#   deploy/release.sh images <tag> [--push]
#                                         build (and push) the five images as <tag>,
#                                         sha-<short> and, for the newest tag, latest
#   deploy/release.sh client <version>    cross-build the apothecary terminal client
#                                         into build/client/ (+ SHA256SUMS)
#   deploy/release.sh chart <tag> [--push]
#                                         package the chart as <tag> (its images'
#                                         tag) into build/chart/, and push it to
#                                         oci://ghcr.io/intradeus/charts
set -euo pipefail
cd "$(dirname "$0")/.."

TAG_GLOB='v[0-9]*.[0-9]*.[0-9]*'

usage() {
  sed -n '2,20p' "$0" >&2
  exit 2
}

die() {
  echo "release: $*" >&2
  exit 1
}

fetch() {
  git fetch --quiet --tags origin +refs/heads/main:refs/remotes/origin/main
}

# The next version of HEAD (v-prefixed), or "" when no commit since the last
# tag bumps it.
next_version() {
  local out rc=0 err
  err=$(mktemp)
  out=$(cz bump --get-next --yes 2>"$err") || rc=$?
  case $rc in
    0) echo "v$out" ;;
    3 | 21) echo "" ;; # no commits / no eligible commit
    *)
      cat "$err" >&2
      rm -f "$err"
      echo "release: cz bump --get-next failed ($rc)" >&2
      exit "$rc"
      ;;
  esac
  rm -f "$err"
}

notes() {
  cz bump --dry-run --yes --changelog-to-stdout
}

preview() {
  local next
  next=$(next_version)
  if [ -z "$next" ]; then
    echo "nothing to release: no commit since $(git describe --tags --abbrev=0 --match "$TAG_GLOB") bumps the version"
    return
  fi
  notes
}

plan() {
  local sha next t
  [ -n "${1:-}" ] || usage
  fetch
  sha=$(git rev-parse "$1^{commit}")
  # A release commit for sha is already tagged (and pushed): the image push
  # failed or is being retried.
  for t in $(git tag --list "$TAG_GLOB" --contains "$sha"); do
    if [ "$(git rev-parse "$t^{commit}^" 2>/dev/null || true)" = "$sha" ]; then
      echo "mode=resume"
      echo "version=$t"
      return
    fi
  done
  if [ "$(git rev-parse origin/main)" != "$sha" ]; then
    echo "mode=superseded"
    echo "version="
    return
  fi
  [ "$(git rev-parse HEAD)" = "$sha" ] || die "plan needs $sha checked out"
  next=$(next_version)
  if [ -z "$next" ]; then
    echo "mode=none"
    echo "version="
    return
  fi
  echo "mode=release"
  echo "version=$next"
  if [ -n "${RELEASE_NOTES_FILE:-}" ]; then
    notes >"$RELEASE_NOTES_FILE"
  fi
}

cut() {
  fetch
  [ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || die "HEAD is not origin/main; refusing to cut"
  cz bump --yes --git-output-to-stderr >&2
  git describe --tags --exact-match --match "$TAG_GLOB" HEAD
}

images() {
  local tag extra
  tag="${1:-}"
  [ -n "$tag" ] || usage
  [ "$(git describe --tags --exact-match --match "$TAG_GLOB" HEAD 2>/dev/null || true)" = "$tag" ] || die "HEAD is not $tag"
  extra="sha-$(git rev-parse --short=7 HEAD)"
  # Only the newest release moves latest: resuming an older one never rolls it back.
  if [ "$tag" = "$(git tag --list "$TAG_GLOB" --sort=-v:refname | sed -n 1p)" ]; then
    extra="$extra latest"
  fi
  EXTRA_TAGS="$extra" deploy/images.sh "$tag" ${2:+"$2"}
}

# The terminal client, for every OS and architecture people run it on: static
# binaries named apothecary-<os>-<arch>, and their checksums.
client() {
  local version os arch out
  version="${1:-}"
  [ -n "$version" ] || usage
  rm -rf build/client
  mkdir -p build/client
  for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
    os=${target%/*}
    arch=${target#*/}
    out="build/client/apothecary-$os-$arch"
    [ "$os" = windows ] && out="$out.exe"
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
      -ldflags "-s -w -X main.version=$version" -o "$out" ./cmd/apothecary
  done
  (cd build/client && sha256sum apothecary-* >SHA256SUMS)
}

# The chart at the release's version: chart version X.Y.Z, appVersion vX.Y.Z,
# so its default image tag is the release's and a chart version pins them all.
chart() {
  local tag
  tag="${1:-}"
  [ -n "$tag" ] || usage
  rm -rf build/chart
  helm package charts/apothecary --version "${tag#v}" --app-version "$tag" -d build/chart >&2
  if [ "${2:-}" = --push ]; then
    helm push "build/chart/apothecary-${tag#v}.tgz" "oci://${CHART_REGISTRY:-ghcr.io/intradeus/charts}"
  fi
}

case "${1:-}" in
  chart) chart "${2:-}" "${3:-}" ;;
  preview) preview ;;
  plan) plan "${2:-}" ;;
  cut) cut ;;
  images) images "${2:-}" "${3:-}" ;;
  client) client "${2:-}" ;;
  *) usage ;;
esac
