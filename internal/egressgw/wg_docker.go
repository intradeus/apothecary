package egressgw

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"apothecary/internal/egressgw/policyfile"
)

// The docker run link. A sandbox used to default-route to its gateway across
// the run's --internal bridge, so its egress crossed the host bridge still
// addressed to the internet — and a host with bridge netfilter on (a local
// k3s loads br_netfilter) judges bridged frames with its iptables, where
// Docker's internal-network rule drops everything bound outside the subnet.
// Now the sandbox tunnels to the gateway over WireGuard, exactly as a
// Kubernetes pod does: the bridge only ever sees UDP to the gateway's own
// address. The network stays the sandbox's only NIC and still carries DNS,
// the image mirror and the build cache, which are addressed to the gateway.

// gwKeyPath is the project gateway's WireGuard private key, kept next to its
// control token so a recreated gateway keeps its identity — and every live
// sandbox's peer config stays valid.
func (h *DockerHost) gwKeyPath(project string) string {
	return filepath.Join(h.gwDir(project), "wg-private-key")
}

// gatewayKey returns the project gateway's WireGuard keypair, creating it once.
func (h *DockerHost) gatewayKey(project string) (priv, pub string, err error) {
	if b, err := os.ReadFile(h.gwKeyPath(project)); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		priv = strings.TrimSpace(string(b))
		pub, err := publicKey(priv)
		return priv, pub, err
	}
	if priv, pub, err = newKeypair(); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(h.gwDir(project), 0o700); err != nil {
		return "", "", err
	}
	return priv, pub, os.WriteFile(h.gwKeyPath(project), []byte(priv+"\n"), 0o600)
}

func publicKey(priv string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(priv)
	if err != nil {
		return "", fmt.Errorf("egressgw: wireguard key: %w", err)
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("egressgw: wireguard key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// dockerPeer is a run's WireGuard identity on its project's gateway.
type dockerPeer struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	IP         string `json:"ip"`
}

func (h *DockerHost) peersDir(project string) string { return filepath.Join(h.gwDir(project), "peers") }
func (h *DockerHost) peerPath(project, run string) string {
	return filepath.Join(h.peersDir(project), filepath.Base(run)+".json")
}

// runPeer returns the run's WireGuard identity: a keypair and the lowest free
// address in 10.90/16, both kept across re-provisions of the same run. The
// caller holds the project's lock (Manager), so allocation does not race.
func (h *DockerHost) runPeer(project, run string) (dockerPeer, error) {
	var p dockerPeer
	if b, err := os.ReadFile(h.peerPath(project, run)); err == nil && json.Unmarshal(b, &p) == nil && p.PrivateKey != "" {
		return p, nil
	}
	used := map[string]bool{wgGatewayIP: true}
	entries, _ := os.ReadDir(h.peersDir(project))
	for _, e := range entries {
		var other dockerPeer
		if b, err := os.ReadFile(filepath.Join(h.peersDir(project), e.Name())); err == nil && json.Unmarshal(b, &other) == nil {
			used[other.IP] = true
		}
	}
	ip, err := freeIP(used)
	if err != nil {
		return dockerPeer{}, err
	}
	priv, pub, err := newKeypair()
	if err != nil {
		return dockerPeer{}, err
	}
	p = dockerPeer{PrivateKey: priv, PublicKey: pub, IP: ip}
	b, _ := json.Marshal(p)
	if err := os.MkdirAll(h.peersDir(project), 0o700); err != nil {
		return dockerPeer{}, err
	}
	return p, os.WriteFile(h.peerPath(project, run), b, 0o600)
}

// withPeer completes a docker attachment with the run's tunnel identity.
func (h *DockerHost) withPeer(project, run string, att Attachment) (Attachment, error) {
	_, gwPub, err := h.gatewayKey(project)
	if err != nil {
		return Attachment{}, err
	}
	p, err := h.runPeer(project, run)
	if err != nil {
		return Attachment{}, err
	}
	att.Peer = &policyfile.Peer{PublicKey: p.PublicKey, IP: p.IP}
	att.WGEndpoint = net.JoinHostPort(att.GatewayIP, strconv.Itoa(wgPort))
	att.WGPrivateKey = p.PrivateKey
	att.WGGatewayKey = gwPub
	return att, nil
}

// linkScript brings wg0 up in the sandbox's network namespace and makes it
// the default route (idempotent: a re-provision re-runs it on a live
// sandbox). The tunnel's own packets go to the gateway's address on the run
// network, which the on-link subnet route keeps off wg0. MTU: the uplink's
// less the WireGuard overhead (IPv4 20 + UDP 8 + WireGuard 32).
const linkScript = `set -eu
umask 077
printf '%s\n' "$APO_WG_KEY" > /tmp/wg.key
mtu=$(ip -o link show eth0 | awk '{for (i = 1; i < NF; i++) if ($i == "mtu") print $(i+1)}')
ip link show wg0 >/dev/null 2>&1 || ip link add wg0 type wireguard
wg set wg0 private-key /tmp/wg.key peer "$APO_WG_GW_KEY" endpoint "$APO_WG_ENDPOINT" allowed-ips 0.0.0.0/0 persistent-keepalive 25
ip address replace "$APO_WG_IP/32" dev wg0
ip link set wg0 mtu $((${mtu:-1500} - 60)) up
ip route replace default dev wg0
`

// LinkSandbox (SandboxLinker) runs linkScript in a throwaway container of the
// gateway image (it carries ip and wg; the dind sandbox image does not)
// sharing the sandbox's network namespace. The key reaches it through the
// environment, never argv.
func (h *DockerHost) LinkSandbox(ctx context.Context, att Attachment, sandbox string) error {
	if att.Peer == nil || att.WGPrivateKey == "" {
		return fmt.Errorf("egressgw: sandbox %s: no WireGuard identity to link", sandbox)
	}
	img, err := h.ensureImage(ctx)
	if err != nil {
		return fmt.Errorf("egressgw: build gateway image: %w", err)
	}
	if _, err := h.docker(ctx, "run", "--rm", "--network", "container:"+sandbox,
		"--cap-add", "NET_ADMIN",
		"-e", "APO_WG_KEY="+att.WGPrivateKey,
		"-e", "APO_WG_GW_KEY="+att.WGGatewayKey,
		"-e", "APO_WG_ENDPOINT="+att.WGEndpoint,
		"-e", "APO_WG_IP="+att.Peer.IP,
		"--entrypoint", "sh", img, "-c", linkScript); err != nil {
		return fmt.Errorf("egressgw: sandbox %s: bring up its WireGuard link (the host needs the wireguard module): %w", sandbox, err)
	}
	return nil
}

var _ SandboxLinker = (*DockerHost)(nil)
