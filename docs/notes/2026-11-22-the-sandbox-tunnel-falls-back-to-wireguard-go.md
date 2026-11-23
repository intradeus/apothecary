# The sandbox tunnel falls back to wireguard-go

Phase 4 of `docs/sandbox-runtimes-plan.md`: a sandbox's WireGuard link to its
gateway, where the kernel has no WireGuard module. A Kata guest's stock
kernel has none.

## What now

- The sandbox image has `wireguard-go`, built in a stage of its own from the
  module `golang.zx2c4.com/wireguard` at a pinned pseudo-version
  (`WIREGUARD_GO_VERSION`). `go install` checks the module against the Go
  checksum database, so the version pin also pins a checksum, as the shim's
  pinned dependencies are. A release binary would have needed a checksum
  kept by hand.
- `apo-wg-up` sets up the link in a function the watchdog can run again. It
  tries `ip link add wg0 type wireguard`. When the kernel refuses the link
  type, it logs the kernel's own words ("Unknown device type", "Operation
  not permitted") and starts `wireguard-go -f wg0` as its own child. If
  userspace fails too (no `/dev/net/tun`), the error repeats the kernel's
  refusal, so a node without the module is named for what it is. Every
  `wg` and `ip` call after that is the same on both paths. The log line and
  `/run/apo/wg-mode` say which WireGuard runs.
- **The watchdog** restarts a dead wireguard-go and puts the link back:
  wg0, its address and the routes all go with the process. The handshake
  rebind is unchanged.
- **`APO_WG_USERSPACE=1`** forces userspace. The adapter sets it from
  `KubeConfig.WireGuardUserspace`, which is the live tests' switch
  (`APOTHECARY_LIVE_WG_USERSPACE=1`); no deployment setting exposes it.
- **`/dev/net/tun`**, verified by hand on this host's docker:
  - absent under plain runc, but `mknod` works with the default
    capabilities, so the script makes it;
  - present in a privileged container;
  - under sysbox, absent and `mknod` is refused (user namespace). Sysbox
    never needs userspace, though: its pods run on the node's kernel,
    which has the module.

  Kata's guest gets `/dev/net/tun` from the `/dev` prelude (phase 5) or
  from this script.

## A finding: the wg AppArmor profile blocks wireguard-go's socket

Ubuntu 26.04's `/etc/apparmor.d/wg` attaches to `/usr/bin/wg` even in a
privileged container (`apo-wg-up`'s header). It lets `wg` read and write
only under `/etc/wireguard`. wireguard-go's control socket is
`/var/run/wireguard/wg0.sock`, a fixed path, so on such a node every `wg
set` failed with "Unable to modify interface: Permission denied".
`apo-wg-up` now links `/var/run/wireguard` to `/etc/wireguard/run`, a
directory the profile already allows. This keeps within the profile's own
rules, as the private key's path already does.

## Tests that hold it

- `TestLiveK8sWireGuardFallback`: the sandbox image's `apo-wg-up` against a
  kernel-WireGuard peer, on the docker host the k3d suite uses, on a
  network whose range docker picks (a fixed one would fail on a docker
  that already holds it):
  - userspace: a ping over the tunnel, then wireguard-go killed and the
    link back;
  - kernel: no wireguard-go runs.
- `TestLiveK8sLifecycle` asserts the mode it asked for. It ran on k3d with
  and without `APOTHECARY_LIVE_WG_USERSPACE=1`.
- `TestWireGuardUserspaceSwitch` (unit): the switch reaches the `wg`
  container.
