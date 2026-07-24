package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// renderNFT builds the full ruleset for the current EXT iface and policy. It is
// re-applied (idempotently) on every policy reload and EXT change. Domain
// allow/deny is enforced in Go (SNI/Host); nft only handles: transparent
// REDIRECT of :80/:443 from every non-EXT (run-net) iface to the filter, an
// L3/L4 backstop for non-HTTP/TLS egress (drop in allow mode, accept-with-denies
// in deny mode), MSS clamping for forwarded traffic, MASQUERADE, and an IPv6
// blanket drop. Interfaces are matched "not EXT" because run-nets appear as
// eth1/eth2/… inside the container (not by docker network name).
func renderNFT(ext string, port int, f File) string {
	var b strings.Builder
	w := func(s string) { b.WriteString(s); b.WriteByte('\n') }

	w("add table ip apo")
	w("flush table ip apo")
	w("add chain ip apo prerouting { type nat hook prerouting priority dstnat ; policy accept ; }")
	w(fmt.Sprintf("add rule ip apo prerouting iifname != %q tcp dport { 80, 443 } redirect to :%d", ext, port))

	w("add chain ip apo forward { type filter hook forward priority filter ; policy drop ; }")
	w("add rule ip apo forward ct state established,related accept")
	w("add rule ip apo forward tcp flags syn tcp option maxseg size set rt mtu")
	if f.normMode() == ModeDeny {
		// allow-all raw egress except explicitly denied IP ranges.
		w("add rule ip apo forward iifname != " + q(ext) + " accept")
		if cidrs := setStr(f.DeniedCIDRs); cidrs != "" {
			w(fmt.Sprintf("add rule ip apo forward iifname != %q ip daddr { %s } drop", ext, cidrs))
		}
	} else {
		// deny-all raw egress except the escape hatch (allowed CIDRs / ports).
		if cidrs := setStr(f.AllowedCIDRs); cidrs != "" {
			w(fmt.Sprintf("add rule ip apo forward iifname != %q ip daddr { %s } accept", ext, cidrs))
		}
		if ports := portSet(f.AllowedPorts); ports != "" {
			w(fmt.Sprintf("add rule ip apo forward iifname != %q tcp dport { %s } accept", ext, ports))
		}
	}

	w("add chain ip apo postrouting { type nat hook postrouting priority srcnat ; policy accept ; }")
	w(fmt.Sprintf("add rule ip apo postrouting oifname %q masquerade", ext))

	// IPv6 is disabled (sysctl) but drop-all forward as belt-and-braces.
	w("add table ip6 apo6")
	w("flush table ip6 apo6")
	w("add chain ip6 apo6 forward { type filter hook forward priority filter ; policy drop ; }")
	return b.String()
}

func applyNFT(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft apply: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func q(s string) string { return strconv.Quote(s) }

func setStr(items []string) string {
	var out []string
	for _, s := range items {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

func portSet(ports []int) string {
	var out []string
	for _, p := range ports {
		if p > 0 {
			out = append(out, strconv.Itoa(p))
		}
	}
	return strings.Join(out, ", ")
}
