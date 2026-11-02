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
