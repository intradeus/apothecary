# Observability and run analytics (TODO 23, 2026-11-02)

Built from docs/observability-analytics-plan.md, which has the why. Two
products: Grafana over OTel signals for the operator, `/analytics` over
per-user fact tables for everyone. What shipped, and where it differs from
the plan:

- **Failed attempts keep their spend.** `stage_failed` carries `turns_used`
  and `usage`; budgets now count it, so a retry loop stops at
  `max_cost_usd`. runcore's `Cost`/`Turns`/`Unwanted` are gone; the numbers
  live in `run.View.Facts`, folded by `run.Apply`.
- **Facts fold** (`internal/run/facts.go`). Time only advances on
  fact-bearing events: letting service-health events split intervals made the
  live fold and a replay disagree by a rounded millisecond. Times are UTC to
  the microsecond (pgstore `Append` truncates), empty maps are nil and open
  attempts close in sorted stage order — all so a JSON round-trip of the view
  folds to the same facts as `Reduce`.
- **Projections** (migration 0022): `run_facts`, `stage_attempts`,
  `run_skillsets`, written in the append transaction through one `pgx.Batch`.
  Text written there is UTF-8-cleaned (`textArgs`). A migration rebuilds the
  projections only when its first lines say `-- rebuild: projections`.
- **Rebuild speed**: 1.14M events / 10k runs in 9.3s, against 57.1s for
  00b9682 on the same data. Two changes beyond the batch: a decoder goroutine
  streams runs to the projector, and ending a run withdraws its open pings by
  `ping_id` (the `status`-indexed update grew quadratically inside the one
  rebuild transaction).
- **Analytics** (`internal/analytics`, no module imports): a whitelisted
  catalog compiled to SQL with `user_id = $1` in every source, run read-only
  under a 5s statement timeout. Beyond the plan: zero-measure queries (`n` is
  the value); a bucketed result's first dim is `bucket`; `order.by` takes
  `bucket` and 0-based `m<i>`; queries and panels may use built-in or own
  scorecards only (someone else's is a 404); dashboard saves fill missing
  panel ids.
- **CPUI**: the runs list reads `run.facts` (no per-run event fetch). The
  filter bar is a multi-select popover (`FilterPick`), since `Select` picks
  one value; bar charts also drill from the tooltip layer, which takes the
  click; line and scatter drill from the panel menu. Heatmap and box plot
  are layerchart primitives.
- **Telemetry**: standard `OTEL_*` env (autoexport); otel at v1.47.0,
  contrib at v0.70.0. **A trace per stage attempt, not per run** (deviation
  from the plan's one run trace): a run parked on humans for days made one
  unbounded trace, exported only at its end. The run is an instant `run`
  span at `run_created` (trace ID `sha256(run_id)[:16]`); each attempt roots
  its own trace (`sha256(run_id\0stage\0iteration)`: `[:16]` trace, `[16:24]`
  span) linked to it, and `exec.attempt` runs the attempt's step with
  `x.ctx`/`x.bg` on that trace, so its agent invocation, queries, outbound
  calls and log lines land there. `workspace.provision` is a trace of its
  own, linked to the run. Between attempts the loop carries no span; pgx
  traces a statement only under a span, so that work (and background polls)
  makes no one-query traces. Find a run's attempts in Tempo with
  `{span.apothecary.run_id="<id>"}`. `logs.New` drops a context key the call
  site also sets. `telemetry.IDGenerator()` is exported for tests that need
  forced IDs. Outbound clients go through `telemetry.HTTPTransport` (otelhttp)
  except the two harness probes that use `http.DefaultClient` (their tests
  stub its transport).
- **No stored value reaches telemetry or logs**, ciphertext included:
  - DB spans never record arguments; `db.query.text` has every string
    literal blanked (`literalFree`), and a failure is only its SQLSTATE
    (`error.type`, `db.response.status_code`): Postgres messages quote the
    offending input, and pgx's encode errors print the argument (`%#v`).
  - Log lines (stderr and OTLP) pass every error attribute through
    `pgstore.RedactError` (a `logs.ErrorRedactor` wired in `cmdServe`): a
    driver error keeps its SQLSTATE, table, column and constraint, an encode
    error its argument index. Errors flattened to a plain string attribute
    are out of its reach.
  - Outbound spans' `url.full` keeps scheme, host and path with query values
    `REDACTED`; Slack and Discord use `telemetry.SecretURLTransport` (host
    only), since a webhook's path is its token — otelhttp alone recorded it.
- **Health**: `/healthz` and `/readyz` are the route table's only paths
  outside `/api/`. `/readyz` is public, so its `db` answer says only
  `database unreachable` or `database timeout`; the driver's error (DSN user,
  host, port) goes to the log at Warn.
- **Stack**: `observability/docker-compose.yaml` is one pinned
  `grafana/otel-lgtm`; six provisioned dashboards (`platform`, `engine`,
  `llm`, `humans`, `usage`, `runs`), datasources by variable. Verified live:
  every panel query of all six returned rows. Helm renders `OTEL_*` only
  when `otel.endpoint` is set.
- **Known gaps**: the usage gauge's `budget` feature counts every flow (each
  gets a default `max_cost_usd`); outbound-HTTP child spans were not seen
  live (`-dev` kinds make no HTTP calls).
