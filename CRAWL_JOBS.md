# Durable crawling and stored search

The default Compose stack runs an API, a dedicated worker (three concurrent page handlers), PostgreSQL 16 and Redis. Start it with `docker compose -f build/compose.yaml up -d --build`. PostgreSQL uses the private Compose network and `postgres-data`; Redis retains the existing 24-hour cache. Standalone API development without `DATABASE_URL` still supports `/crawl`; jobs/inventory return 503 until storage is configured. `APP_ROLE=worker` requires `DATABASE_URL` and `REDIS_URL`. See [inventory-first scheduling](INVENTORY_SCHEDULING.md) for the rolling 48-hour coverage and protected proxy administration. Set the same `REDIS_URL` on every API and worker to share pacing, cooldowns, cache leases and cursor signing.

## Storage and queue

PostgreSQL stores canonical source listings, first/last seen timestamps, page observations, search membership, demand and job checkpoints. Embedded, versioned migrations run under an advisory lock. Initial setup requires permission to install `pg_trgm`. The local Compose credentials are development defaults; configure deployment credentials and database backups separately.

The durable queue uses PostgreSQL `FOR UPDATE SKIP LOCKED`, rather than a separate Asynq broker. This keeps listing writes and page checkpoints in one transaction. Each source has its own task and cursor. Workers claim one page with a 60-second lease and random ownership token. A process crash leaves the page reclaimable after lease expiry. A stale owner cannot publish after reassignment. Listing upserts and unique observation keys make repeated handling safe; external requests can still happen more than once. Shutdown cancels work and leaves uncommitted pages for recovery.

A page has a 35-second work budget. Recoverable source failures retry up to three task attempts, delayed by 10/20 seconds, in addition to bounded transport retries. Exhausted timeout/network/server failures enter the durable delayed retry queue for one hour (configurable to two hours). Parser/validation/client failures are terminal for that source; a user can retry it explicitly. Invalid cursors restart that source from page one. Other sources continue. Shared Redis pacing/cooldowns coordinate workers and API requests; successful requests gradually reduce the adaptive interval. Redis outages do not bypass the shared budget.

Pause prevents new claims and takes effect after an already running page commits. Resume keeps the cursor. Retrying an unavailable source affects that source only. A search has at most one active job, including paused jobs; repeated submissions reuse it. Terminal jobs are reused for five minutes, including public `refresh: true` requests. A resumed old job cannot displace a newer active job.

## Incremental policy

| Rule | Current policy |
| --- | --- |
| National coverage | Native brand partitions for all three sources, cars and motorcycles, staggered across 48 hours. |
| Refresh interval | Baseline cycles start every 48 hours; recent demand searches also become eligible after 48 hours. |
| Active scheduling window | All coverage partitions, plus searches requested within seven days; 20 due searches per minute. |
| Search delta | At most 20 pages per source, stopping earlier after reliable overlap with stored IDs. |
| Full coverage | Reachable provider pagination, capped visibly at 10,000 pages per partition; paced durable page checkpoints. |
| Detail work | New/changed summary hashes and details older than 48 hours; unchanged fresh facts are reused. |
| Ordering safety | Two consecutive pages with at least five observations and 90% preexisting, distinct IDs; repeated pages cannot count twice. |


Explicit job pages refresh summary caches; ordinary synchronous API calls keep their 24-hour cache behavior. Page observations remain durable even when an ID repeats. Jobs expose new/changed counts. A capped source retains its cursor for manual continuation. Job responses display at most 5,000 current listings; use paginated inventory queries beyond that. Counts reflect canonical summary changes, not an inferred sale/removal history.

## API

All paths use `/api/v1`:

```http
POST /jobs
Content-Type: application/json

{"query":"بنلی 180","type":"motorcycle","citySlug":"tehran","bamaCategory":"benelli-tnt180s"}
```

Returns **202** with `id`, `status`, `mode`, `criteria`, `sources`, normalized `data`, `done`, `truncated`, `newCount`, `changedCount` and timestamps. This response does not wait for marketplace requests. Poll `GET /jobs/:id`. Sources retain `page`, `nextCursor`, counts and errors. A `job:pending` cursor is a UI progress sentinel, never an upstream pagination token.

```http
POST /jobs/:id/actions
Content-Type: application/json

{"action":"pause"}
```

Actions are `pause`, `resume`, `retry`; retry/resume accept an optional `platform`. Worker operation is independent of browser polling. Repeating the same search reconnects to its active job. The UI reads stored inventory first and starts a background delta refresh unless stored-only mode is selected. Add `statusOnly=1` to job creation/status requests for lightweight metadata with an empty `data` array; existing clients retain full job snapshots.

```http
GET /inventory?q=بنلي%20180&type=motorcycle&city=tehran&minPrice=100000000&maxMileage=30000&sort=relevance&limit=50&offset=0
```

Response: `{ "data": [...], "total": 123, "limit": 50, "offset": 0 }`. Filters: `q`, `model`, `category`, `type`, `city` (`iran` means all cities), `platform`, `body`, `minPrice`, `maxPrice`, `minMileage`, `maxMileage`, `minYear`, `maxYear`. New inventory-first clients supply `model` for matching facts across crawl partitions. Legacy requests without `model` keep category-membership filtering. Years use Solar Hijri. Limit is 1–100; offset is bounded at 100,000. Sorts: `relevance`, `price-asc`, `price-desc`, `mileage-asc`, `year-desc`; default is last observed. Unknown prices/years/mileage do not pass numeric range filters. Query values are bound SQL parameters and sort expressions are whitelisted.

Persian/Arabic letter variants, combining marks and Persian/Arabic digits are normalized before indexing and querying. Full-text (`simple`) and trigram GIN indexes support matching and typo tolerance; structured filters have a composite index. Scores are calculated at display time, not stored as source facts. This is a starting point, not a complete Persian analyzer or a production relevance/latency benchmark.

## Operations and verification

API and worker emit existing telemetry; `crawl_job_page` spans describe page work. The optional Prometheus configuration scrapes both processes. Inspect `docker compose -f build/compose.yaml logs worker` for failures and PostgreSQL `crawl_jobs`/`crawl_tasks` for queue state. Do not delete the Redis cursor key while preserving tasks using signed cursors.

Tests against a **dedicated disposable database**:

```sh
TEST_DATABASE_URL='postgres://user:password@localhost:5432/jamshid_test?sslmode=disable' make test
```

The integration test truncates its database's crawl/listing tables. It covers concurrent submission deduplication, lease recovery and stale-owner fencing, atomic page checkpointing, duplicate commit rejection, pause/resume, unchanged/changed/stale detail decisions, Persian/digit/typo search, numeric/city filters, scheduled shallow/full refresh and bounded source retries. Unit tests cover worker enrichment selection, HTTP validation and shared Redis provider budgets. Frontend tests cover polling cancellation without cancelling the server job and stored search scope/pagination.

Search and job views hide observations not seen for seven days; this is freshness filtering, not proof that a listing was removed. Historical observations currently have no automatic pruning. Set a retention/backup policy before sustained production crawling; source removals require explicit verification. The existing API has no user ownership/authentication layer: do not expose job-control endpoints to untrusted users without adding access controls.

### Local verification (2026-11-02)

Backend race tests (including disposable PostgreSQL integration), Go static analysis, 43 frontend tests, TypeScript checks and the production build passed. The local API, worker, PostgreSQL, Redis and frontend are healthy. A Bama-only job returned HTTP 202 in 27 ms and independently persisted three matching listings. A paused job survived API/worker restarts and resumed with the same ID. Repeated refreshes retained three canonical rows and reported zero new/changed listings after excluding relative-time drift. Stored search returned those listings using Arabic/Persian variants and Persian digits. These small checks are correctness smoke tests, not a production performance benchmark. Live browser visual verification was unavailable because the browser connection exposed no tabs.

## Subsequent delivery update

Page summaries now publish before enrichment through a separate durable detail queue. Lifecycle, direct source reverification and field provenance are implemented. These replace the earlier per-page inline enrichment and removal-state limitations above; see [lifecycle and enrichment](LISTING_LIFECYCLE.md) for the current behavior and API.
