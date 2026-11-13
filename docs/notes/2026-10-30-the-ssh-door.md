# The SSH door (TODO 46, 2026-10-30)

The user: "connect my desktop VS Code to the container, with access to
terminal and all of that" — from a laptop that may not be the engine's
machine, over a VPN (tailscale). Nothing SSH existed: no sshd in any image,
no key model, `vscode`/`ssh` link kinds declared and never produced. And the
engine never dials a workspace IP (host→container blocked on docker; only
the pod mux port on Kubernetes) — it has one path into a workspace, the shim
directory's unix sockets / the token mux, over which `sidecar.sock` is
already reached.

- **Shape.** `apothecaryd` runs an SSH server, the *door* (`internal/sshdoor`),
  on `APOTHECARY_SSH_LISTEN`; a person's `ssh -p 2222 <workspace>@<engine>`
  — or VS Code Remote-SSH, which is the same thing — is authenticated by
  the public keys registered on their account (`ssh_keys`, migration 0020,
  `/api/me/ssh-keys`), routed by *username = workspace name* (the env id, a
  typed `runs.env_id` column cleared when the workspace is destroyed, so
  `RunByEnv` finds exactly the live run) to a run the key's account owns,
  and then proxied — channel for channel, request for request, both ways,
  sshpiper-style, understanding none of it (`proxy.go`) — to an SSH server
  in the workspace's shim sidecar (`workspace/shim/sshd*.go`, on
  `/opt/apothecary/ssh.sock`, as the remote user). The sidecar implements
  sshd's session semantics: a login shell in the repo on the existing PTY
  code, `exec` in `$HOME`, env allowlist, window-change, signals,
  exit-status/exit-signal, `direct-tcpip` dialed inside the workspace (what
  Remote-SSH uses to reach the server it starts). No subsystem (sftp is the
  documented limit: `scp -O`, rsync work), no `-R`.
- **Why not sshd-in-container or Remote Tunnels.** A dropbear/OpenSSH per
  workspace means a third-party binary per libc flavour, user keys copied
  and refreshed in workspaces, a host key per workspace and a ProxyJump
  stanza in every ssh_config (a raw TCP jump routes only by jump target).
  Remote Tunnels relay through Microsoft with a login per workspace and the
  proprietary CLI in the bundle. The door terminates auth against the user
  table (a key added works at once), has one stable host key, needs no
  client configuration, and reuses the sidecar transport and the ownership
  model the instance already has.
- **Keys.** Both of the door's keys derive from sealed instance secrets
  (`InstanceSecret("ssh-door-host")`, `("ssh-door-client")`, the auth-broker
  precedent): the host key so a person verifies one fingerprint for every
  run, the door key so a workspace can admit the door across engine
  restarts. The engine writes the door's public key to
  `/apothecary/ssh_door.pub` right before each dial (`engine.SSHTarget`): a
  rebuilt container self-heals on the next connection. The sidecar re-reads
  the file on every attempt. Not the run token: `Engine.RunToken` exists
  only while a run's loop is up and is regenerated per boot — a completed
  run with a live workspace has none.
- **The shim** is built as its own module in docker
  (`workspace/images/shim/Dockerfile`: `go mod init` + `go get` pins), so
  `golang.org/x/crypto/ssh` is pinned there too, held equal to the root
  go.mod by `TestShimDependencyPinsMatchGoMod`; the shim stays one flat
  `package main` (a sub-package would have two import paths). A sidecar from
  before this change serves no `ssh.sock`; the adapter says so (rebuild).
- **The link.** `vscode://vscode-remote/ssh-remote+<hex(JSON
  {"hostName":"<user>@<host> -p <port>"})>/<path>`: Remote-SSH hex-encodes
  the destination string a person would type into "Connect to Host" (an
  authority is lower-cased and takes no spaces) and decodes it; the plain
  `user@host:port` form is its fallback. `path` is the devcontainer's
  workspace folder as the sidecar's `/health` now reports it
  (`remoteWorkspaceFolder` is not persisted; `$HOME/repo` is a symlink onto
  it). `GET /api/runs/{id}/ssh` serves it with the command, the fingerprint,
  the host key and the caller's key count; the run page's Env ▾ shows the
  rows; Account → SSH keys registers keys.
- **Config.** `APOTHECARY_SSH_LISTEN` (off when unset), `APOTHECARY_SSH_ADVERTISE`
  (derived from the listen host, else the public URL's host; an error when
  nothing can be derived — the address is a one-line fact of the
  deployment). Compose: bind the VPN address, not 0.0.0.0 (host network).
  Helm: `ssh.enabled/port/advertise`, a Service of its own
  (`service-ssh.yaml`; an Ingress cannot carry SSH). Users: the VS Code
  server download rides the project's egress gateway — allow
  `update.code.visualstudio.com` and `vscode.download.prss.microsoft.com`
  there, or set `remote.SSH.localServerDownload: always` on the desktop.
- **Limits.** Sessions drop when the engine restarts, the reaper destroys
  the workspace, or a rebuild replaces the container (VS Code reconnects).
  No run event per session (appending from outside the run loop is
  restricted); the door logs them. Per-address auth throttle (8 failures /
  15 min), key-only, identical refusals for unknown/foreign/gone/disabled.

### The docker-exec "terminal" link is gone (2026-10-30)

`Links()` left the provisioner port with it. The `terminal` access link was
`DOCKER_HOST=unix://<RunsDir>/<workspace>/docker.sock docker exec -it <ctr>
bash` (`kubectl exec … -- docker exec …` on Kubernetes): a copy-paste for an
operator sitting on the engine host, unreachable from anyone's laptop, and
the only link `Links()` still produced once app links moved to the port
relay. The SSH door supersedes it on every axis (over the VPN, per-user keys
and ownership, as the remote user, the same on both hosts), so the Env ▾
popover now opens on the door's rows and lists app/service links under them.
`env_ready.links` carries only the auto-exposed `app` links; `event.Link`
lost its `Command`; `run.workspace` is the workspace's first app link — what
it resolved to in production all along, the terminal link having no URL.
The envcheck "links" step went with it (the "shim" step already proves a
running container). The operator recipe lives in README's SSH section.

The Env ▾ popover also hands out the two desktop `settings.json` lines that
make a person's extensions follow them in — `remote.defaultExtensionsIfInstalledLocally`
(VS Code's own, every remote, installs what the desktop has too) and
`remote.downloadExtensionsLocally` (the desktop fetches the VSIX, so the
workspace never needs Microsoft's marketplace through the gateway). The
list is pre-filled from `GET /api/runs/{id}/ssh`'s `extensions`: what the
workspace's own editor installs, `EditorStarter.EditorExtensions` (the
definition's ∪ the project's), read without an exec. `remote.SSH.defaultExtensions`
was not chosen: Remote-SSH-only, and it installs ids the desktop may not
have. Microsoft's server cannot be pointed at the gateway gallery (its
gallery is baked into product.json), so the two editors keep separate
extension dirs; pre-seeding `~/.vscode-server/extensions` from code-server's
CLI is the follow-up to try, with a per-user extension list.
