#!/bin/sh
# Feature BUILD step (runs once per base image, as root, feature files in
# CWD): install the firewall toolchain init-firewall.sh needs — iptables
# (+ip6tables), iproute2 (`ip`), and a resolver (`getent`) — across the
# common base distros, then install the scripts. Per-run config does NOT
# enter the build: it arrives at container start via /opt/apothecary/
# firewall.env (see apothecary-entrypoint.sh).
#
# init-firewall.sh deliberately uses NO ipset (plain per-IP rules), so the
# dependency set is small. A base image with no supported package manager
# fails the build loudly — the operator can add the tools to their image or
# turn the firewall off on the project (fail-closed by default).
set -e

if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends iptables iproute2 dnsutils
  rm -rf /var/lib/apt/lists/*
elif command -v apk >/dev/null 2>&1; then
  apk add --no-cache iptables ip6tables iproute2
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y iptables iproute
elif command -v microdnf >/dev/null 2>&1; then
  microdnf install -y iptables iproute
elif command -v yum >/dev/null 2>&1; then
  yum install -y iptables iproute
else
  echo "apothecary-firewall: no supported package manager (apt/apk/dnf/yum) — cannot install the egress firewall. Add iptables+iproute2 to the image, or set the devcontainer provisioner's firewall option to off." >&2
  exit 1
fi

install -m 0755 init-firewall.sh /usr/local/bin/init-firewall.sh
install -m 0755 apothecary-entrypoint.sh /usr/local/bin/apothecary-entrypoint.sh
