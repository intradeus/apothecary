#!/usr/bin/env bash
# The container this runs in, as the docker daemon the CLI drives sees it:
# the devcontainer with the docker-outside-of-docker feature, where `task`
# talks to the very daemon that runs it. The Taskfile asks it what a path or
# an address of this side becomes on the daemon's side:
#
#   deploy/self-container.sh id         this container's id; nothing on a host,
#                                       or in a container of another daemon
#   deploy/self-container.sh path <dir> <dir> as the daemon's host sees it (the
#                                       source of the bind mount holding it);
#                                       <dir> itself on a host
#   deploy/self-container.sh ip <net>   this container's IPv4 on docker network
#                                       <net>; nothing when not attached
#
# Always exits 0: an empty answer means "not in a container", which is the
# host's case, and the callers go on as they do there.
set -euo pipefail

self() {
  local id
  # Docker binds each container's hostname file from its own state directory
  # (<data-root>/containers/<id>/hostname), whatever the storage driver.
  id=$(sed -n 's|.*/containers/\([0-9a-f]\{64\}\)/hostname /etc/hostname .*|\1|p' /proc/self/mountinfo 2>/dev/null | head -n1) || true
  if [ -n "$id" ] && command -v docker >/dev/null && docker inspect "$id" >/dev/null 2>&1; then
    echo "$id"
  fi
}

case "${1:-}" in
  id) self ;;
  path)
    dir="${2:?usage: deploy/self-container.sh path <dir>}"
    id=$(self)
    if [ -z "$id" ]; then echo "$dir"; exit 0; fi
    # The deepest bind mount at or above <dir>, plus the rest of the path.
    docker inspect "$id" | jq -r --arg d "$dir" '
      [.[0].Mounts[] | select(.Type == "bind") | . as $m
       | select($d == $m.Destination or ($d | startswith($m.Destination + "/")))
       | {depth: ($m.Destination | length), src: ($m.Source + ($d | ltrimstr($m.Destination)))}]
      | max_by(.depth) | .src // $d'
    ;;
  ip)
    net="${2:?usage: deploy/self-container.sh ip <network>}"
    id=$(self)
    if [ -z "$id" ]; then exit 0; fi
    docker network inspect "$net" 2>/dev/null |
      jq -r --arg id "$id" '.[0].Containers[$id].IPv4Address // empty | split("/")[0]' || true
    ;;
  *) echo "usage: deploy/self-container.sh id | path <dir> | ip <network>" >&2; exit 2 ;;
esac
