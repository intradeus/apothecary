# A non-root remote user can use the shim dir (2026-11-10)

- With `remoteUser: node` (common: the Node, JavaScript and TypeScript
  images), the sidecar never started on Kubernetes: it runs as the remote
  user and creates `sidecar.sock` and `ssh.sock` in `/opt/apothecary`, which
  the shim-seed init container makes root's, 0755 ("bind: permission
  denied" in `/apothecary/sidecar.log`). No sidecar means no file tree,
  diff, terminal, Ask pane, editor or SSH door. The docker host has the same
  failure whenever the remote user's uid is not the engine's: the CLI
  remaps it only when it runs as a non-root user (never for the engine
  image, which runs as root) and never for compose. Dev from source hid it
  (engine uid 1000 = `node`).
- The reverse relay behind app port exposure (`app-<port>.sock`, same dir)
  was started with a bare `docker exec`, i.e. as the image's default user:
  an image ending in `USER node` could expose no port.
- `rootInit` (root, after `up`, before the lifecycle hooks) now gives a
  non-root remote user the shim dir's group, writable: `chgrp "$(id -g
  <user>)"` + `chmod g+w`, not recursive. The group, not the owner: the
  engine must keep writing there whatever uid it runs as (`engine.sock`
  after a restart, the shim on upgrade), and on the docker host the dir is
  its own run folder seen through the sandbox. The shim binary and the tools
  bundle keep their owners. The devcontainer spec has no group setting; the
  gid is the one the user's processes run with. The reverse relay now runs
  with `-u root` (it only pipes bytes; its socket is 0666).
- Checked by hand against real `node:22-bookworm-slim` containers, one with
  root and one with `USER node` as default user, the shim dir root-owned
  0755 as the seed leaves it, the real static shim and the exact script
  `rootInit` generates: the sidecar fails with "bind: permission denied"
  before `rootInit`; after it the sidecar starts as `node` on the first
  call, `/health` answers from the host side, `ssh.sock` is served, an app
  port answers through a root-run reverse relay, and the dir stays root's.
  Unit tests pin the script (group and mode, no owner change, no
  recursion, nothing for root) and the relay's `-u root`.
- Workspaces provisioned before this need a rebuild, or the two commands run
  by hand as root inside them.
