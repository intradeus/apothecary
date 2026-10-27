#!/bin/sh
# The Kubernetes init container's job (`seed.sh <dir>`): put this image's
# /tools onto the run volume — the directory the sandbox mounts into the
# workspace at /opt/apothecary/tools. A pod restart whose volume already holds
# this exact bundle (TOOLS_HASH) copies nothing; an older one is replaced
# whole: copy beside it, then swap, so a half-copied dir is never the live one.
set -eu
dir=${1:?usage: seed.sh <dir>}
want=$(cat /tools/TOOLS_HASH)
if [ "$(cat "$dir/TOOLS_HASH" 2>/dev/null)" = "$want" ]; then
  echo "tools-seed: $dir already holds bundle $want"
  exit 0
fi
rm -rf "$dir.tmp" "$dir.old"
mkdir -p "$(dirname "$dir")"
cp -a /tools/. "$dir.tmp"
[ ! -e "$dir" ] || mv "$dir" "$dir.old"
mv "$dir.tmp" "$dir"
rm -rf "$dir.old"
echo "tools-seed: seeded bundle $want into $dir"
