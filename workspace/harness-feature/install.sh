#!/bin/sh
# Feature BUILD step (runs once per base image, as root, feature files in
# CWD): install the project's coding harnesses into a repo-authored dev
# container. That container runs the REPO's image, so it never gets the §8
# pipeline image that bakes the harness in — without this, the first agent
# stage dies with "opencode: command not found".
#
# Three engine-generated files sit next to this script:
#   harness-binaries   one binary name per line (what `command -v` probes)
#   harness-requires   the tools the install commands need (npm, curl, …)
#   harness-install.sh the guarded install commands, one block per harness
#
# NOTHING here assumes a particular runtime: the prerequisites come from
# harness-requires, so a harness that installs without Node never triggers a
# Node install.
set -e

# --- generic helpers --------------------------------------------------------

# install_pkg installs a package with whatever package manager the base image
# has. The package name is assumed to match the command; a harness needing a
# differently-named package should install it in its own command instead.
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

# install_node is the one special case: distro Node is frequently too old for
# these CLIs (Ubuntu 22.04 ships v12), so prefer NodeSource's current LTS.
install_node() {
  if command -v apk >/dev/null 2>&1; then
    apk add --no-cache nodejs npm
    return
  fi
  if command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y --no-install-recommends ca-certificates curl
    if curl -fsSL https://deb.nodesource.com/setup_22.x | bash -; then
      apt-get install -y --no-install-recommends nodejs
    else
      apt-get install -y --no-install-recommends nodejs npm
    fi
    rm -rf /var/lib/apt/lists/*
    return
  fi
  install_pkg nodejs || return 1
  command -v npm >/dev/null 2>&1 || install_pkg npm
}

# ensure_cmd makes one tool available. An image that already has it is left
# completely alone — installing our own Node over a Node project's pinned
# version would make the agent build against the wrong runtime.
ensure_cmd() {
  cmd=$1
  if command -v "$cmd" >/dev/null 2>&1; then
    echo "apothecary-harness: '$cmd' already in the image"
    return 0
  fi
  echo "apothecary-harness: '$cmd' missing — installing it"
  case "$cmd" in
    node | npm | npx) install_node || true ;;
    *) install_pkg "$cmd" || true ;;
  esac
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "apothecary-harness: could not provide '$cmd' on this base image. Add it (or the harness itself) to your devcontainer image." >&2
    exit 1
  fi
}

# --- run --------------------------------------------------------------------

# Nothing to do when the image already ships every harness — a repo that bakes
# its own is left untouched (no prerequisites installed, no commands run).
missing=0
if [ -f ./harness-binaries ]; then
  while IFS= read -r bin; do
    [ -n "$bin" ] || continue
    command -v "$bin" >/dev/null 2>&1 || missing=1
  done < ./harness-binaries
else
  missing=1
fi
if [ "$missing" -eq 0 ]; then
  echo "apothecary-harness: harnesses already present in this image; nothing to install"
  exit 0
fi

if [ -f ./harness-requires ]; then
  while IFS= read -r tool; do
    [ -n "$tool" ] || continue
    ensure_cmd "$tool"
  done < ./harness-requires
fi

sh ./harness-install.sh
