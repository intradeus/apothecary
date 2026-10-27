#!/bin/sh
# Downloads every tool's release artifact for this architecture into $1:
# gnu/bin, musl/{bin,libexec} and code-server/. The versions arrive as the
# Dockerfile's ARGs (environment). Only omp publishes checksums; for the rest
# TLS to GitHub plus the pinned tag is the trust, as npm over TLS was before.
set -eu
out=${1:?usage: fetch.sh <out-dir>}
case "${TARGETARCH:-$(uname -m)}" in
  # x64 takes the *-baseline builds: no AVX2 requirement (what opencode's own
  # npm installer falls back to on such CPUs); arm64 has no such split.
  amd64|x86_64)  oc=x64-baseline; ocm=x64-baseline-musl; cx=x86_64;  omp=x64;   cs=amd64 ;;
  arm64|aarch64) oc=arm64;        ocm=arm64-musl;        cx=aarch64; omp=arm64; cs=arm64 ;;
  *) echo "fetch.sh: unsupported architecture ${TARGETARCH:-$(uname -m)}" >&2; exit 1 ;;
esac
: "${OPENCODE_VERSION:?}" "${CODEX_VERSION:?}" "${OMP_VERSION:?}" "${CODE_SERVER_VERSION:?}"

mkdir -p "$out/gnu/bin" "$out/gnu/libexec" "$out/musl/bin" "$out/musl/libexec" "$out/musl/lib"
tmp=$(mktemp -d)
gh=https://github.com

get() { # get <url> <dest>
  echo "fetch: $1"
  curl -fsSL --retry 3 --retry-delay 2 -o "$2" "$1"
}
# untar_one <tarball> <dest>: the archive's single regular file, wherever
# the layout puts it (opencode: `opencode` at the root; codex: one file
# named after its target triple).
untar_one() {
  d=$(mktemp -d)
  tar -xzf "$1" -C "$d"
  f=$(find "$d" -type f | head -n 1)
  [ -n "$f" ] || { echo "fetch.sh: $1 holds no file" >&2; exit 1; }
  install -m 0755 "$f" "$2"
  rm -rf "$d"
}

# opencode: bun-built, one tarball per libc.
oc_base=$gh/anomalyco/opencode/releases/download/v$OPENCODE_VERSION
get "$oc_base/opencode-linux-$oc.tar.gz" "$tmp/oc.tgz"
untar_one "$tmp/oc.tgz" "$out/gnu/bin/opencode"
get "$oc_base/opencode-linux-$ocm.tar.gz" "$tmp/ocm.tgz"
untar_one "$tmp/ocm.tgz" "$out/musl/libexec/opencode"

# codex: a static musl build, so the one file serves both libcs.
get "$gh/openai/codex/releases/download/rust-v$CODEX_VERSION/codex-$cx-unknown-linux-musl.tar.gz" "$tmp/codex.tgz"
untar_one "$tmp/codex.tgz" "$out/gnu/bin/codex"
install -m 0755 "$out/gnu/bin/codex" "$out/musl/bin/codex"

# omp: a bare binary per libc, checked against the release's SHA256SUMS.
omp_base=$gh/can1357/oh-my-pi/releases/download/v$OMP_VERSION
get "$omp_base/SHA256SUMS.txt" "$tmp/SHA256SUMS.txt"
for a in "omp-linux-$omp:$out/gnu/bin/omp" "omp-linux-musl-$omp:$out/musl/libexec/omp"; do
  asset=${a%%:*}; dest=${a#*:}
  get "$omp_base/$asset" "$tmp/$asset"
  (cd "$tmp" && grep " $asset\$" SHA256SUMS.txt | sha256sum -c -)
  install -m 0755 "$tmp/$asset" "$dest"
done

# code-server: the standalone tree, which bundles its own node (glibc only —
# the project ships no musl standalone; Alpine images get no editor).
get "$gh/coder/code-server/releases/download/v$CODE_SERVER_VERSION/code-server-$CODE_SERVER_VERSION-linux-$cs.tar.gz" "$tmp/cs.tgz"
mkdir -p "$tmp/cs"
tar -xzf "$tmp/cs.tgz" -C "$tmp/cs"
mv "$tmp/cs/code-server-$CODE_SERVER_VERSION-linux-$cs" "$out/code-server"
[ -x "$out/code-server/bin/code-server" ] || { echo "fetch.sh: code-server tree has no bin/code-server" >&2; exit 1; }

rm -rf "$tmp"
echo "fetch: done"
