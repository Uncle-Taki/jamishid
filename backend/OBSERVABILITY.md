# Source observability and request resilience

The backend records source/page fetching, detail enrichment/parsing, catalog loading, scoring, cache hits/misses, retry scheduling and API duration. Measurements use fixed operation/source/outcome/failure/status/cache labels; search strings, item IDs, URLs and cursors are excluded.

## Inspect locally

The backend's internal metrics listener runs on port 9090. Workspace Compose maps it only to `127.0.0.1:9090`; public nginx does not proxy it. Open `http://localhost:9090/metrics` for Prometheus-compatible cumulative counters and histograms. The optional monitoring profile retains scraped history in a Docker volume:

```sh
docker compose -f build/compose.yaml --profile monitoring up -d prometheus
```

Prometheus is available at `http://localhost:9091`. Example queries:

```promql
# Cold/warm source p95 by marketplace and cache state
histogram_quantile(0.95, sum by (le, source, cache) (rate(jamshid_operation_duration_seconds_bucket{operation="source"}[5m])))

# Source cache hit ratio
sum(rate(jamshid_operation_duration_seconds_count{operation="source",cache="hit"}[5m])) / sum(rate(jamshid_operation_duration_seconds_count{operation="source"}[5m]))

# Failed fetch attempts by source and HTTP status
sum by (source, status, failure) (rate(jamshid_operation_duration_seconds_count{operation="fetch",outcome="error"}[5m]))

# Retries scheduled
sum by (source) (increase(jamshid_operation_duration_seconds_count{operation="retry"}[1h]))
```

`jamshid_valid_listings_total` counts rows returned by an operation, including cached rows. Select a single operation (for example `source`) when calculating requests per returned listing; adding page/source/enrichment counts together double-counts rows. These counters do not represent unique inventory. Histogram quantiles are approximations, and a scrape history is needed for `rate` queries.

Structured JSON completion events and OpenTelemetry JSON spans go to backend stdout (`docker compose -f build/compose.yaml logs backend`). Completion events include a trace ID; parent/child spans connect request, source, page, fetch and parse work. Raw upstream error text is excluded. The batch span exporter flushes on graceful shutdown. This initial implementation has local log/span output, not a hosted trace viewer or an OTLP collector. Metrics reset with the backend process; Prometheus history survives in its volume. Stop/start the optional profile explicitly as needed.

## HTTP and process metrics

The `/metrics` listener stays on `127.0.0.1:9090` (same port as before) and is now served by the official Prometheus client through a single registry. Request instrumentation uses Gin route-template normalization, so parameterized routes collapse to their templates (for example `/api/v1/jobs/:id`) and cardinality stays bounded:

- `jamshid_http_requests_total{route,method,status_class}` — requests counted against the matched route template; `OPTIONS` is normalized to `preflight`, HTTP 405 to `method_not_allowed`, and unmatched paths to `unmatched`.
- `jamshid_http_request_duration_seconds{route,method}` — request latency histogram.
- `jamshid_http_requests_in_flight` — requests currently being served.

The legacy `jamshid_operation_duration_seconds` and `jamshid_valid_listings_total` contracts, including their label sets and the example queries above, are preserved exactly. Standard Go runtime and process collectors are registered alongside.

## Worker instrumentation

The worker process (and any process running the crawl loops) exports:

- Pool capacity and active gauges (`jamshid_worker_capacity`, `jamshid_worker_active` per `pool`); the active gauge is decremented on every task exit path, so it cannot leak.
- Task attempts by outcome (`jamshid_worker_task_attempts_total{pool,source,outcome,failure}`): `success`, `source_failure`, `deferral`, `lease_lost`, `persistence_failure`, `canceled`. Success is only counted after the database commit, so a crash between fetch and commit does not inflate it. These are attempts, not unique listings.
- Loop heartbeat timestamps (`jamshid_worker_loop_last_tick_timestamp_seconds{pool,loop}`) updated on every actual loop iteration, including idle polls where no task is claimed.
- Scheduler attempt/success timestamps and error counters (`jamshid_scheduler_*` per `scheduler`), and claim errors (`jamshid_worker_claim_errors_total{pool}`) reported even when no task was claimed.

## Shared snapshot collector

Queue, inventory, coverage, proxy and dependency gauges describe the whole system, so exactly one process must collect them. `JAMSHID_MONITORING_COLLECTOR=1` designates that collector — in Compose this is the backend API service. The collector polls PostgreSQL and Redis every `JAMSHID_MONITORING_INTERVAL` (default `15s`) with a bounded timeout per poll; polls never overlap. When a poll fails, the last-good values are retained and staleness is marked conspicuously through `jamshid_monitoring_snapshot_success=0` (never by zeroing values). All shared gauges carry the label `collector="primary"` and must never be summed across instances. The `/metrics` endpoint never queries the database synchronously.

Queue-semantics notes for reading `jamshid_queue_tasks{queue,source,state,mode,purpose}`:

- A delayed task and its retry ticket describe the same pending work; never sum the two.
- Paused parent tasks are excluded from the `ready` count.
- Running work is split into `running_valid` and `running_expired` leases.
- `jamshid_queue_oldest_ready_age_seconds` measures overdue eligibility, not how long ago work was enqueued; `0` means the queue is empty.

## Dashboards and alerts

Grafana and Prometheus run under the optional monitoring profile (`build/compose.yaml`, profile `monitoring`), with Grafana on `127.0.0.1:3001` and Prometheus on `127.0.0.1:9091`. Four dashboards are provisioned from `build/monitoring/grafana/` along with the Prometheus datasource; alert rules live in `build/monitoring/prometheus/rules/alerts.yaml` and are evaluated locally by Prometheus — no notifications are delivered. Operator documentation, including the full metrics reference and troubleshooting, is in `build/monitoring/README.md`.

## Worker environment knobs

| Variable | Default | Purpose |
| --- | --- | --- |
| `WORKER_PAGE_LOOPS` | `3` | Concurrent page-crawl loops (worker pool capacity for the `page` pool) |
| `WORKER_DETAIL_LOOPS` | `2` | Concurrent detail-enrichment loops (worker pool capacity for the `detail` pool) |

## Testing

Telemetry is covered by unit tests for route normalization and label cardinality, and snapshot-collector tests for staleness marking, cancellation and timeout behavior. PostgreSQL monitoring integration tests are gated on `TEST_DATABASE_URL`; never point that variable at a production database. The `cmd/smokeworker` binary runs the real loops, queue semantics and telemetry against deterministic in-process fake marketplaces for the isolated monitoring smoke test (see `build/monitoring/smoke/README.md`); it never contacts real marketplaces and must never run in production.

## Fetch policy

All clients in a backend process share a budget for each production marketplace, including its API subdomain. Catalog, search and detail requests use that budget. With `REDIS_URL`, API and worker processes also share Redis provider slots, adaptive intervals and cooldowns. Without Redis, pacing remains local.

| Behavior | Default |
| --- | --- |
| Maximum attempts | 3 total, within the caller's deadline |
| Retried methods | GET; Divar's known read-only POST search endpoint only |
| Retried failures | HTTP 429, 502, 503, 504 and network errors |
| No automatic retry | Other HTTP errors, invalid cursors, parser/catalog validation errors, oversized bodies, canceled callers |
| Backoff | 250 ms exponential base plus 0–149 ms jitter; 500 ms base on second retry |
| Shared pacing | Starts at 250 ms between request starts; doubles on transient failures up to 4 s, eases by 10% per success back to 250 ms |
| Cooldown | Shared retry delay; after three consecutive transient failures, at least 5 s. `Retry-After` can require a longer delay. |
| `Retry-After` | Nonnegative delay-seconds and HTTP-date. Never shortened to fit the request deadline; waiting callers cancel instead. |

The last failed attempt also updates the shared cooldown. Every subsequent caller checks current cooldown state before reserving its next start; requests already in flight cannot be recalled. There is no retry loop around parsing, so an HTTP-200 app shell or schema change cannot create a request storm. Existing 24-hour caches, singleflight sharing, per-source timeouts and partial-source response contracts remain in place.

The existing detail-specific four-slot concurrency cap and pacing are retained in addition to the broader source budget. Cold crawls may take longer under source throttling; healthy cached pages remain available. These defaults should be tuned using source error rates and measured latency rather than raising throughput blindly.

## Validation on 2026-11-02

The backend suite passed with race detection (74.8% statement coverage); `go vet` and Compose configuration validation passed. A further integration regression verified that an HTTP-200 app shell is classified as a parser failure without retrying the request. Live verification returned nine Bama listings in about 4.8 seconds on a cold search and the same nine cached listings in about 3 ms. Source/cache/fetch histogram series and correlated JSON spans were verified in the running service.

The local runtime was packaged from the successfully compiled, tested binary using the existing backend runtime image because the clean container build stalled while downloading modules. The default source Dockerfile remains unchanged. The optional Prometheus image download repeatedly restarted and was canceled; its Compose profile is configured, but the graphical viewer was not started. The metrics endpoint on port 9090 and stdout trace/log export are running and verified.
