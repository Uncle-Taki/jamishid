# jamishid

## Contracts and release operations

See the [shared Persian/model normalization contract](backend/NORMALIZATION.md),
[parser contracts and scheduled source checks](backend/PARSER_CONTRACTS.md), and
workspace `build/RELEASE_RECOVERY.md` for gated releases, consistent database backups,
isolated restore verification and application image rollback. GitHub workflows in
each child repository run their respective checks after push.

## Source monitoring and resilience

The backend emits source timings, cache/retry counters, structured events and OpenTelemetry traces. Marketplace requests share per-provider pacing and cooldowns, with bounded retries for transient failures. See [the operations guide](backend/OBSERVABILITY.md) for metrics, trace output, Prometheus queries and policy defaults.

## Backend caching

Successful marketplace search pages and item details are cached for up to **24 hours**. With `REDIS_URL`, both caches are shared across API instances using Redis fetch leases and a shared cursor signing key. Without it, the bounded local memory caches are used. Repeating the same search/category/cursor reuses the source page; item facts are reused across searches and cities.

Page keys include the marketplace, query, extra keywords, category, vehicle type, city and continuation cursor. Price/year/mileage/time filters and score weights are reapplied to raw cached rows on every response. Continuation metadata is preserved, and signed pagination tokens last 48 hours so a cached page can still be continued.

Failed requests and invalid item details are not cached. Healthy source pages remain reusable when another source fails. The API's existing `cached` field is true only when every selected source page was served from cache.

Expiry is measured from a successful fetch and is not extended by reads. The standalone memory caches are bounded to 1,000 search pages and 5,000 item details and clear on restart. Workspace Compose configures Redis with AOF persistence, a named data volume and a 256 MB capacity. See [shared cache configuration, behavior and validation status](backend/REDIS_CACHE.md).


### Durable crawling and inventory search

The workspace Compose stack now includes PostgreSQL and a dedicated crawl worker. The frontend submits durable jobs and polls progress; work continues after the browser closes. `POST /api/v1/jobs`, job pause/resume/retry actions, and `GET /api/v1/inventory` provide the new workflow. PostgreSQL stores listings/observations and supports structured filters, normalized Persian search and `pg_trgm` typo matching. Popular searches refresh on a schedule, with selective detail enrichment. See [crawl jobs and search operations](backend/CRAWL_JOBS.md) for policies, limits, API examples and verification.
