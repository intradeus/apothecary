#!/usr/bin/env bash
# Egress firewall for factory workspaces (DESIGN.md §11).
#
# Default-deny with an explicit allowlist: model provider APIs, the repo
# host (derived from FACTORY_REPO_URL), Go/npm registries, and the host
# gateway on the engine + Coder ports. The resolve-then-pin pattern: allowed
# names are resolved once here, pinned into /etc/hosts and an ipset, then
# ALL DNS is blocked — other names simply cannot resolve.
#
# Runs as root via a single NOPASSWD sudoers entry (no arguments accepted;
# the FACTORY_* env vars pass through sudo env_keep). A lock file makes
# re-runs no-ops, so re-invocation cannot alter the rules.
set -euo pipefail

LOCK=/run/factory-firewall.done
if [ -e "$LOCK" ]; then
  echo "firewall: already initialized"
  exit 0
fi

REPO_URL="${FACTORY_REPO_URL:-}"
CODER_PORT="${FACTORY_CODER_PORT:-7080}"

REPO_HOST=""
if [ -n "$REPO_URL" ]; then
  REPO_HOST="$(echo "$REPO_URL" | sed -E 's#^[a-z+]+://##; s#^[^@]*@##; s#[:/].*$##')"
fi

DOMAINS="api.anthropic.com api.openai.com proxy.golang.org sum.golang.org registry.npmjs.org"
[ -n "$REPO_HOST" ] && DOMAINS="$DOMAINS $REPO_HOST"
# Extra provider hosts (template variable, e.g. an API router like
# openrouter.ai when subscription auth goes through one).
[ -n "${FACTORY_EXTRA_DOMAINS:-}" ] && DOMAINS="$DOMAINS $FACTORY_EXTRA_DOMAINS"

GATEWAY="$(ip route | awk '/default/ {print $3; exit}')"

# Resolve every allowed name BEFORE any rule changes (DNS dies afterwards),
# pin the results into /etc/hosts, and collect the IPs for the allow rules.
echo "firewall: pinning allowed hosts"
ALLOW_IPS=""
for d in $DOMAINS; do
  ips="$(getent ahostsv4 "$d" | awk '{print $1}' | sort -u)"
  if [ -z "$ips" ]; then
    echo "firewall: WARNING: cannot resolve $d" >&2
    continue
  fi
  for ip in $ips; do
    ALLOW_IPS="$ALLOW_IPS $ip"
    printf '%s %s\n' "$ip" "$d" >> /etc/hosts
  done
  echo "  $d -> $(echo $ips | tr '\n' ' ')"
done

iptables -F OUTPUT
iptables -P OUTPUT DROP
# DNS dies first — BEFORE the loopback accept. On user-defined docker
# networks the embedded resolver lives at 127.0.0.11, reached through a
# DNAT rule (127.0.0.11:53 -> random port) that rewrites the dport before
# the filter chain — so match the destination IP, not just port 53. The
# daemon would otherwise resolve externally on the container's behalf.
# Allowed names are pinned in /etc/hosts and need no DNS.
iptables -A OUTPUT -d 127.0.0.11 -j DROP
iptables -A OUTPUT -p udp --dport 53 -j DROP
iptables -A OUTPUT -p tcp --dport 53 -j DROP
iptables -A OUTPUT -o lo -j ACCEPT
iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
# Host gateway: ONLY the Coder server port (the agent's websocket — without
# it the workspace agent itself dies). The engine is reached via the reverse
# tunnel on localhost, never through the gateway.
iptables -A OUTPUT -d "$GATEWAY" -p tcp --dport "$CODER_PORT" -j ACCEPT
# Allowlisted hosts, https/http only (plain per-IP rules: no ipset kernel
# module dependency; the list is small).
for ip in $ALLOW_IPS; do
  iptables -A OUTPUT -d "$ip" -p tcp -m multiport --dports 443,80 -j ACCEPT
done
# Everything else — including all DNS — is dropped by the OUTPUT policy.

# No IPv6 egress at all.
if command -v ip6tables >/dev/null; then
  ip6tables -F OUTPUT 2>/dev/null || true
  ip6tables -P OUTPUT DROP 2>/dev/null || true
  ip6tables -A OUTPUT -o lo -j ACCEPT 2>/dev/null || true
fi

touch "$LOCK"
echo "firewall: initialized (gateway $GATEWAY, coder :$CODER_PORT)"
