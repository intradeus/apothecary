#!/bin/sh
# Feature BUILD step (runs once per base image, as root, feature files in
# CWD): install code-server into a repo-authored dev container and pre-bake
# the project's + repo's VS Code extensions. This happens at BUILD time, when
# egress is still open — a RUNTIME install would be blocked by the §11
# default-deny firewall. code-server itself is started lazily on demand (the
# engine's "Open editor" action), never here.
#
# One engine-generated file may sit next to this script:
#   extensions   one VS Code extension id per line (repo customizations ∪ project list)
set -e

# EXT_DIR is baked into the image layer (NOT the per-run bind mount), so the
# extensions persist across runs and are shared by whatever user the repo
# image runs as. The engine starts code-server with this same --extensions-dir.
EXT_DIR=/usr/local/share/apothecary-code-server/extensions

# install_pkg installs a package with whatever package manager the base image
# has (same helper shape as the harness feature).
install_pkg() {
  pkg=$1
  if command -v apk >/dev/null 2>&1; then
    apk add --no-cache "$pkg"
  elif command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y --no-install-recommends "$pkg"
    rm -rf /var/lib/apt/lists/*
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y "$pkg"
  elif command -v microdnf >/dev/null 2>&1; then
    microdnf install -y "$pkg"
  elif command -v yum >/dev/null 2>&1; then
    yum install -y "$pkg"
  else
    return 1
  fi
}

# --- code-server ------------------------------------------------------------

if command -v code-server >/dev/null 2>&1; then
  echo "apothecary-codeserver: code-server already in the image"
else
  echo "apothecary-codeserver: installing code-server"
  command -v curl >/dev/null 2>&1 || install_pkg curl || install_pkg ca-certificates || true
  # The official installer auto-detects deb/rpm/arch and falls back to a
  # self-contained standalone build (its own Node) on musl/unknown distros.
  curl -fsSL https://code-server.dev/install.sh | sh
fi

# --- extensions (baked, shared) --------------------------------------------

mkdir -p "$EXT_DIR"
if [ -f ./extensions ]; then
  while IFS= read -r ext; do
    [ -n "$ext" ] || continue
    echo "apothecary-codeserver: installing extension $ext"
    # Best-effort: a bad id / one missing from Open VSX must not fail the build.
    code-server --install-extension "$ext" --extensions-dir "$EXT_DIR" || \
      echo "apothecary-codeserver: could not install '$ext' (not on Open VSX?)" >&2
  done < ./extensions
fi
# Any container user must be able to READ the baked extensions at runtime.
chmod -R a+rX "$EXT_DIR" 2>/dev/null || true
