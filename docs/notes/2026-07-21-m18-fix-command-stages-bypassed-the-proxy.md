# M18 fix — command stages bypassed the proxy

Live bug: `git push` (a flow command stage) timed out 133s against
gitlab.com in proxy mode. Cause: only bootstrap steps and agent runs
sourced proxy.env; command stages ran raw via Envp.Exec and went DIRECT,
hitting the firewall DROP (dropped packets → long timeout, not a proxy
403). The allowlist was fine — gitlab.com is contributed by the repository host
account's EgressDomains (github: github.com/api/codeload; gitlab: the
base_url host) and unioned into every run. Fix: the provisioner's Exec sources
proxy.env for EVERY workspace command (one chokepoint — bootstrap,
command stages, agent runs all go through it). The explicit bootstrap/
adapter sourcing stays (redundant, harmless). "Add to egress" button
relabeled "Add to project's allowlist" (it already persisted to
workspace.extra_domains — the label now says so).
