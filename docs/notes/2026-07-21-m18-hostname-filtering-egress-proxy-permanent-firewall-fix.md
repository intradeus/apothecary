# M18 — Hostname-filtering egress proxy (permanent firewall fix)

The resolve-then-pin firewall's failure modes — silent blocks, IP
rotation, and per-provider domain sleuthing (found live: opencode-go's
opencode.ai silently blocked) — are replaced by an engine-side egress
proxy. Design:

- `internal/egressproxy`: a CONNECT + absolute-form HTTP proxy on
  loopback. Per-run Basic credentials `runID:HMAC(secret,runID)`
  (deterministic — survives engine restarts; identifies the run's policy,
  not a high-value secret). Filters BY HOSTNAME (exact or `*.suffix`)
  against a per-run allowlist; ports 443/80 only; records every denial
  (host ×count) for the run. Process-random signing secret.
- Wiring: `main.go` starts the proxy (unless APOTHECARY_EGRESS_PROXY=off),
  passes its port to the resolver (forwarded into every workspace, like
  the engine port) and to the engine
  (`SetEgress`/`EgressController`). The run loop sets the policy from
  `res.EgressDomains` (already = derived domains ∪ project extra_domains)
  before any workspace network use, and drops it at teardown.
- Workspace: bootstrap writes `/apothecary/proxy.env`
  (HTTP(S)_PROXY → 127.0.0.1:proxyPort with the run creds, NO_PROXY =
  loopback); every bootstrap step and both harness adapters source it, so
  git clone and agent invocations all leave through the proxy. The
  template gains an `egress_proxy` parameter → `APOTHECARY_EGRESS_PROXY`;
  when set, `init-firewall.sh` runs in PROXY MODE: OUTPUT drops everything
  except loopback (reaches the tunneled proxy + shim + code-server) and
  established. No IP pinning, no DNS, no
  per-provider guessing.
- Visibility (the point): `GET /api/runs/{id}/egress-denials` +
  a Working-card banner list blocked hosts live — a silent 30-minute hang
  becomes "⚠ egress blocked: telemetry.deepseek.com — add to extra
  domains and restart".
- extra_domains flows in unchanged (resolve.egressDomains already unions
  it → the proxy allowlist). The static provider→domain map stays as the
  auto-allowlist (now incl. opencode/opencode-go); a miss is a visible
  denial, not a hang, so no live catalog fetch on the resolution path.
- Fallback: proxy off (env, or provisioners that ignore the param) → the
  derived IP-allowlist firewall, unchanged. envcheck passes proxyPort 0.
- Not yet: proxy-mode envcheck assertions (checks still target allowlist
  mode); keeping API keys engine-side via proxy injection (future — the
  hook exists now that all egress is mediated).
