# The great rename: factory → apothecary (post-M11, Lu's call)

Done pre-first-release — the only cheap moment. Renamed end to end:
- Go module `factory` → `apothecary` (every import path); binary/cmd
  `apothecaryd`; image `apothecary/apothecaryd`; CI image
  `ghcr.io/intradeus/apothecary/apothecaryd`; compose service.
- Env contract `APOTHECARY_*` → `APOTHECARY_*` — boot vars AND the workspace
  contract (run.env keys, template env, sudoers env_keep, firewall
  script, shim), all rolling together via the content-addressed base
  image + template (new hashes ⇒ rebuilt/re-pushed on next use).
- Workspace filesystem `/factory` → `/apothecary`, `/opt/apothecary` →
  `/opt/apothecary`, runs dir `/var/lib/apothecary/runs`; unix user
  `factory` → `apothecary` (home moves accordingly).
- Branch prefix `apothecary/<run>-<slug>`, workspace names
  `apothecary-<id>`, git identity `Apothecary <apothecary@local>`,
  telemetry `apothecary.*` attrs + `apothecary_*` metrics (Grafana
  dashboards updated to match).
- Historical docs swept too (`factoryd`, `FACTORY_*`, `/factory` paths) —
  the design docs now read with current spellings.
**Operational notes**: deploy/.env needs its two var names updated
(APOTHECARY_PG_PASSWORD, APOTHECARY_MASTER_KEY — values unchanged); the
stack must be RECREATED (down + up: new volumes, fresh DB, reconfigure in
the UI); pre-rename workspaces/images/templates are orphaned leftovers;
Prometheus metric history restarts under the new metric names.
