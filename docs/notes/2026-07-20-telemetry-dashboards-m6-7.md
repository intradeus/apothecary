# Telemetry & dashboards (M6, §7)

*Superseded by "Observability and run analytics (TODO 23)" below: the
collector/Prometheus/Grafana stack, `APOTHECARY_OTEL_ENDPOINT` and the three
event-table dashboards are gone.*

- OTel deps added (go.opentelemetry.io/otel + SDK + OTLP gRPC exporters) —
  §0-sanctioned by §7's OTel mandate; recorded here per the no-new-deps
  rule.
- **Telemetry is a projection of the event stream**: the engine feeds every
  freshly appended event to an EventSink exactly once (never on recovery
  replay — metrics would double-count; locked by a test). The sink derives
  the §7 span-per-run / span-per-stage-attempt (explicit event timestamps)
  and the four metrics. Harness telemetry stays non-load-bearing.
  Recovered runs whose run_created predates the boot get a lazily started
  span; a stage attempt resumed mid-flight (no fresh stage_started) reports
  its metrics without harness/model attrs — accepted recovery edge.
- **Dashboards query Postgres directly** (Grafana postgres datasource on
  the events/runs tables) — "analytics are queries" per the plan; the
  Prometheus datasource carries the live §7 operational metrics. Three
  provisioned dashboards: cost/run, unwanted pings per merged MR (headline
  stat, target 0), loop heatmap by stage (+ where runs stalled).
- The observability compose stack (collector, Prometheus, Grafana) uses
  host networking, loopback binds, and **deliberately unusual ports**
  (grafana 3001, prometheus 19090, OTLP 14317, exporter 18889): this host
  already ran a grafana/prometheus/collector on the standard ports — three
  separate bind collisions found live, including the collector's own
  self-metrics on 8888 (now disabled).
- M6 acceptance: dashboards render from 6 runs (5 real: two M4 GitLab runs
  imported into the analytics DB, Lu's two CPUI click-through runs, one
  review-abandoned run) — all three dashboards' queries verified returning
  rows via the Grafana API; §7 metrics verified in Prometheus
  (factory_runs_total/pings_total/stage_cost_usd/stage_turns with
  stage+model attributes).
- Operational finding: gpt-5-mini as reviewer wrote unparsable
  verdict.json three times in one run (bare `!` in the comments array) —
  the review self-retry edge and the abandoned outcome behaved exactly as
  designed, and the failure is visible on the loop dashboard. Deployments
  wanting fewer review stalls should pick a stronger reviewer model
  (flows are data).
