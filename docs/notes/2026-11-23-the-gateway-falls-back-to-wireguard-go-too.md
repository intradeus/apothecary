# The gateway falls back to wireguard-go too

Updates 2026-11-23-a-kata-workspace-runs-user-namespaced-pods-but-not-the-gateways,
which named this as one of the two changes that would let a Kata workspace
run the k8s suites in full, and 2026-11-22-the-sandbox-tunnel-falls-back-to-wireguard-go,
whose sandbox side this mirrors.

## Why

Developing Apothecary in Apothecary is what the Kata runtime is for. Under
Kata, the workspace's docker runs on the guest's kernel, and so does every
k3d node `task sandbox:test:k8s` starts there. The stock guest kernel has
no WireGuard and loads no modules. The sandbox side already fell back to
`wireguard-go`. The egress gateway did not: `ip link add wg0 type
wireguard` failed, and the gateway exited. So nested runs had no egress.

The other way out was a guest kernel rebuilt with `CONFIG_WIREGUARD=y`.
That is a change to each host, repeated on every Kata bump. It also only
helps installations that make it. A workspace should run whatever its
devcontainer sets up, with no per-host or per-project setup. The gateway
is Apothecary's own component, so its fallback belongs in Apothecary.
Every other kernel feature a nested cluster uses is in the stock guest
kernel: overlayfs, bridge and br_netfilter, nftables NAT, conntrack, IPVS,
VXLAN, user namespaces, BPF, TUN.

## What now

- **`setupWG`** (`workspace/gateway/wg.go`) tries the kernel's link first.
  When the kernel refuses the link type, it logs the kernel's own words
  ("Unknown device type") and starts `wireguard-go -f wg0` as its child.
  Every `wg` and `ip` call after that is the same on both paths. If
  userspace fails too, the error repeats the kernel's refusal. The log
  line `gateway: wg0 listening` carries `mode` (`kernel` or `userspace`).
- **TUN and AppArmor**, as on the sandbox. A missing `/dev/net/tun` is made
  (misc 10:200) with the container's default `CAP_MKNOD`. The control
  socket's directory, `/var/run/wireguard`, links to
  `/etc/wireguard/run`, the one place a host's wg AppArmor profile lets
  `wg` reach. The gateway container keeps `NET_ADMIN` and `NET_RAW` and
  nothing more. A pod of that shape could make and open a TUN device on
  this host's k3s (containerd 2, runc) and inside a Kata guest's docker.
- **Supervision.** A dead `wireguard-go` takes wg0 with it, along with its
  key, port, address and every peer. So it would cut off every run of the
  project at once. `superviseWG` starts it again and restores the link,
  then reconciles the last peer set (`reconcilePeers` keeps it). A restart
  that fails is retried every 2 s.
- **`WG_PROCESS_FOREGROUND=1`** in `wireguard-go`'s environment. Without
  it, `wireguard-go` opens with a banner saying the kernel has WireGuard
  and userspace is not needed. That is false here, and misleading in the
  gateway's log.
- **`APO_WG_USERSPACE=1`** forces userspace on a kernel that has the module.
  The egress-gw adapter sets it from `K8sHost.WireGuardUserspace`, which
  `KubeConfig.WireGuardUserspace` feeds. That is the same switch as the
  sandbox's (`APOTHECARY_LIVE_WG_USERSPACE=1` in the live suites), so one
  setting runs both ends in userspace. No deployment setting exposes it.
- **The image** builds `wireguard-go` the way the sandbox image does, at
  the same pinned version. `TestOneWireguardGo` holds the two pins equal.
- **`task k8s:cluster`** no longer requires kernel WireGuard on the docker
  host. Its check passes if a container can make a wireguard link, or else
  can make and open `/dev/net/tun`. Its message no longer suggests
  `modprobe` where that cannot help.

## Measured (in a Kata sandbox on the 4.4.0 stock guest kernel, 6.18.62)

The gateway image of this change ran in the Kata sandbox's docker, with
`NET_ADMIN` and `NET_RAW` only and one peer in `routes.json`. A second
container ran `wireguard-go` as the peer.

- The gateway fell back by itself ("Unknown device type", then `mode:
  userspace`), and the peer handshook.
- A `kill -9` of the gateway's `wireguard-go`: wg0 was back, with its key,
  port and the peer, about 3 ms after the exit. The container never
  restarted.
- With traffic flowing (a ping a second), the peer handshook again 16 s
  after the kill: WireGuard's own rekey once data goes unanswered. With
  keepalives alone it waits for the session to age (about 2 minutes).
  This matches a replaced gateway pod on the kernel path today, and the
  sandbox's watchdog (3 minutes without a handshake: rebind) backstops
  both.

Not measured: userspace throughput and CPU on a busy gateway. It is
slower than the module. That is why the kernel's link stays first, and
why docs/kubernetes.md still asks for the module where nodes can have it.

## Tests that hold it

- `TestSetupWGFallsBackToWireguardGo`, `TestSetupWGUserspaceOnRequest`,
  `TestSetupWGUserspaceFailures`, `TestSuperviseWGRestartsWireguardGo`,
  `TestSuperviseWGStops`, `TestTailLog` (unit, `workspace/gateway`). They
  use stand-ins for a kernel without WireGuard (`kataTools`), and the TUN
  and socket paths are redirected, so no test touches the host's own.
- `TestGatewayManifests`: the switch reaches the gateway's env, and a
  userspace gateway gains no privilege. `TestWireGuardUserspaceSwitch`:
  the workspace's switch reaches the gateway host.
- `TestLiveK8sLifecycle` reads the gateway's mode from its log. It must be
  userspace under `APOTHECARY_LIVE_WG_USERSPACE=1`, and userspace without
  it only after a logged fallback.
- `TestOneWireguardGo`: one `wireguard-go` version across the two images.
