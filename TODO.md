
------ 

After 3.0.0 :

- 48) "Queues" : queue tasks, for automatic resolution
- 49) AI native platform - byok, run an agent in a harnesss (in the project's sidecar / gateway ?) so you can ask it to generate flows or do other internal AI tasks (analytics dashboards... find other usecases). Add MCP server etc. Goal is to have AI support in the UI itself - not just in the sandboxed environment.
- 52) ci upgrades (we run CI on MR and on merge on main... can we improve that somehow ? caching or something ? Speed is also VERY slow ... 15min CI) 
- 54) Ask - multi-session support.
- 55) Collaborative planning (back and forth between an agentic stage and the task owner)
- 56) Secrets inside the sandbox - engine's credential manager should be able to inject secrets in the sandbox's secret manager, for usage in scripts (ex: glab or whatever). Those secrets should not be accessible by agent stages directly, but through tool calls (maybe ? Or reuse that nvidia/microsoft technique with secret aliases - agent thinks the secret is 'abc' but all secrets are replaced live by a watcher, since 'abc' = 'password123!'), or by command stages (i guess ? for now). credentials reaper should properly work too, for those injected secrets in the sandbox's cred manager 
- 57) k8s / docker compose. Should we choose 1 and stick to it ? annoying to manage both, will personally only use k3s.
- 58) MORE provisionning logs - now its only :
=== apothecary: provisioning apo-fix-cray-325-devcontain-xbmy @ 2026-11-09T22:13:31Z ===
→ preparing source checkout
→ bringing up the project's egress gateway
→ bringing up the sandbox pod
- 59) "Retry" retries the currently blocked stage. But at the provisionning step, it should retry provisionning too. Or is that the point of rebuild ? In that case, we need rebuild to work while provisionning fails, now the button is disabled.
- 60) While provisionning, the egress gateway should ALLOW all new domains by default, and only block domains that are written as blocked by the workspace.