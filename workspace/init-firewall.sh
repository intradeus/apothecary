#!/usr/bin/env bash
# Egress firewall for apothecary workspaces (DESIGN.md §11).
#
# Default-deny with an explicit allowlist: model provider APIs, the repo
# host (derived from APOTHECARY_REPO_URL), Go/npm registries, and the host
# gateway on the engine + Coder ports. The resolve-then-pin pattern: allowed
# names are resolved once here, pinned into /etc/hosts and an ipset, then
# ALL DNS is blocked — other names simply cannot resolve.
#
# Runs as root via a single NOPASSWD sudoers entry (no arguments accepted;
# the APOTHECARY_* env vars pass through sudo env_keep). A lock file makes
# re-runs no-ops, so re-invocation cannot alter the rules.
set -euo pipefail

LOCK=/run/apothecary-firewall.done
if [ -e "$LOCK" ]; then
  echo "firewall: already initialized"
  exit 0
fi

# FAIL-CLOSED (found live in M9): if anything below errors, slam egress
# shut rather than leaving it open. Loopback + established + the Coder
# gateway port stay up so the workspace remains reachable for inspection;
# the lock file is NOT written, so envcheck's firewall_initialized step
# fails loudly.
fail_closed() {
  echo "firewall: FATAL — closing all egress" >&2
  iptables -F OUTPUT 2>/dev/null || true
  iptables -P OUTPUT DROP 2>/dev/null || true
  iptables -A OUTPUT -o lo -j ACCEPT 2>/dev/null || true
  iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || true
  ip6tables -F OUTPUT 2>/dev/null || true
  ip6tables -P OUTPUT DROP 2>/dev/null || true
  GW="$(ip route | awk '/default/ {print $3; exit}')" || true
  [ -n "${GW:-}" ] && iptables -A OUTPUT -d "$GW" -p tcp --dport "${APOTHECARY_CODER_PORT:-7080}" -j ACCEPT 2>/dev/null || true
  # Keep the Coder server + DERP reachable too, so a fail-closed workspace is
  # still inspectable over `coder ssh` / the web terminal.
  CH="$(getent ahostsv4 host.docker.internal 2>/dev/null | awk '{print $1; exit}')" || true
  [ -n "${CH:-}" ] && iptables -A OUTPUT -d "$CH" -p tcp --dport "${APOTHECARY_CODER_PORT:-7080}" -j ACCEPT 2>/dev/null || true
  exit 1
}
trap fail_closed ERR

REPO_URL="${APOTHECARY_REPO_URL:-}"
CODER_PORT="${APOTHECARY_CODER_PORT:-7080}"
EGRESS_PROXY="${APOTHECARY_EGRESS_PROXY:-}"
GATEWAY="$(ip route | awk '/default/ {print $3; exit}')"

# The Coder server — and its EMBEDDED DERP relay, which the agent's tailnet
# needs for `coder ssh`, the web terminal and the IDE — is reached at
# host.docker.internal in the docker deployment. That is a DIFFERENT address
# than this workspace network's default-route gateway (custom docker networks
# gateway ≠ host-gateway). Allowing only $GATEWAY:$CODER_PORT lets the agent's
# *control* connection survive (it is ESTABLISHED before this script runs) but
# silently drops every NEW DERP connection, so the tailnet degrades to derp=0
# and all interactive access hangs (found live 2026-07: derp.Recv deadline
# exceeded). Resolve it from /etc/hosts — docker pins it there, so no DNS is
# needed — and allow it too. Empty on non-docker deployments (rule skipped).
CODER_HOST_IP="$(getent ahostsv4 host.docker.internal 2>/dev/null | awk '{print $1; exit}' || true)"

# --- PROXY MODE (M18) ------------------------------------------------------
# The engine runs a hostname-filtering egress proxy, reverse-tunneled onto
# this workspace's own loopback. The firewall then needs only: loopback
# (reaches the proxy, the ask_human shim, code-server), established
# connections, and the Coder gateway port (the agent's own websocket).
# EVERYTHING else — all direct egress, all DNS — is dropped: the workspace
# cannot leave except through the proxy, which filters by hostname and
# whose allowlist (derived domains ∪ project extra_domains) the engine sets
# per run. No IP pinning, no name resolution, no per-provider guessing.
if [ -n "$EGRESS_PROXY" ]; then
  echo "firewall: PROXY MODE (egress via $EGRESS_PROXY)"
  iptables -F OUTPUT
  iptables -P OUTPUT DROP
  iptables -A OUTPUT -o lo -j ACCEPT
  iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
  iptables -A OUTPUT -d "$GATEWAY" -p tcp --dport "$CODER_PORT" -j ACCEPT
  if [ -n "$CODER_HOST_IP" ]; then
    iptables -A OUTPUT -d "$CODER_HOST_IP" -p tcp --dport "$CODER_PORT" -j ACCEPT
  fi
  if [ -f /proc/net/if_inet6 ] && command -v ip6tables >/dev/null; then
    ip6tables -F OUTPUT
    ip6tables -P OUTPUT DROP
    ip6tables -A OUTPUT -o lo -j ACCEPT
    ip6tables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
  fi
  touch "$LOCK"
  echo "firewall: initialized (proxy mode, gateway $GATEWAY, coder :$CODER_PORT)"
  exit 0
fi

REPO_HOST=""
if [ -n "$REPO_URL" ]; then
  REPO_HOST="$(echo "$REPO_URL" | sed -E 's#^[a-z+]+://##; s#^[^@]*@##; s#[:/].*$##')"
fi

# The allowlist is fully DERIVED (DESIGN2 §8.4): the engine computes it —
# repo host ∪ adapter contributions ∪ project extras — and passes it via
# APOTHECARY_EXTRA_DOMAINS. No names are baked into this script; only the
# mechanics live here. REPO_HOST stays as belt-and-braces for custom
# templates that pass v1 params only.
DOMAINS=""
[ -n "$REPO_HOST" ] && DOMAINS="$REPO_HOST"
[ -n "${APOTHECARY_EXTRA_DOMAINS:-}" ] && DOMAINS="$DOMAINS $APOTHECARY_EXTRA_DOMAINS"

# Resolve every allowed name BEFORE any rule changes (DNS dies afterwards),
# pin the results into /etc/hosts, and collect the IPs for the allow rules.
echo "firewall: pinning allowed hosts"
ALLOW_IPS=""
for d in $DOMAINS; do
  # `|| true`: an unresolvable name (typo'd extra_domain) must NOT abort
  # the script under set -e/pipefail — it is simply not allowed, and its
  # envcheck egress step fails by name (found live: the abort left the
  # workspace fail-open).
  ips="$(getent ahostsv4 "$d" | awk '{print $1}' | sort -u || true)"
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
# tunnel on localhost, never through the gateway. host.docker.internal (the
# Coder server + its DERP relay) is a distinct address from the gateway and
# must be allowed too, or the agent's tailnet dies (see CODER_HOST_IP above).
iptables -A OUTPUT -d "$GATEWAY" -p tcp --dport "$CODER_PORT" -j ACCEPT
if [ -n "$CODER_HOST_IP" ]; then
  iptables -A OUTPUT -d "$CODER_HOST_IP" -p tcp --dport "$CODER_PORT" -j ACCEPT
fi
# Allowlisted hosts, https/http only (plain per-IP rules: no ipset kernel
# module dependency; the list is small).
for ip in $ALLOW_IPS; do
  iptables -A OUTPUT -d "$ip" -p tcp -m multiport --dports 443,80 -j ACCEPT
done
# Everything else — including all DNS — is dropped by the OUTPUT policy.

# No IPv6 egress at all. With IPv6 enabled in the kernel, failing to apply
# the drop MUST be fatal (a swallowed error here is silent fail-open on the
# whole v6 stack); without IPv6 there is nothing to close.
if [ -f /proc/net/if_inet6 ]; then
  if command -v ip6tables >/dev/null; then
    ip6tables -F OUTPUT
    ip6tables -P OUTPUT DROP
    ip6tables -A OUTPUT -o lo -j ACCEPT
  else
    echo "firewall: FATAL: kernel has IPv6 but ip6tables is missing" >&2
    false # trips the ERR trap -> fail_closed
  fi
fi

touch "$LOCK"
echo "firewall: initialized (gateway $GATEWAY, coder :$CODER_PORT)"
