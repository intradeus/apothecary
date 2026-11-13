# Instance limits are an admin setting (2026-10-27)

Reverses the TODO 25 decision that the capacity caps live in the deployment
environment. `APOTHECARY_MAX_CONCURRENT_RUNS` and `APOTHECARY_MAX_ENVIRONMENTS`
are gone — no env override at all, by the user's choice. The caps are one row
(`instance_limits`, migration 0014; no row = the defaults 3 and 6, `-1` = no
cap), served to the engine as `engine.StoredLimits`, which the engine already
read on every admission — so a change applies to the next run that starts,
with no restart. `GET /api/limits` is for every user; `PUT /api/limits` is
admin-only and validated (a positive count or -1). System → Limits is
editable for admins (a number and a "No cap" toggle per cap) and read-only
for everyone else. The compose file and the Helm chart no longer set them.
