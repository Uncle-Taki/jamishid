# Summary-first enrichment, lifecycle and provenance

Versioned migration `002_details_lifecycle.sql` extends the durable PostgreSQL store. It adds a detail queue, job-to-detail links, listing events, lifecycle columns and a JSON field-provenance map. Existing source data is preserved; old facts without provenance remain unattributed until observed again. Never invent historical verification timestamps for old rows.

## Delivery and workers

A page worker fetches summaries and commits canonical summaries, page checkpoint, observations and detail tasks in one transaction. It never waits for detail HTTP requests. New/changed/stale items enter a separate durable queue; unchanged fresh details retain their facts and original provenance. Initial `detailsStatus` is `partial`. Two dedicated detail handlers run alongside the three page handlers in the worker process. They share Redis provider budgets and the four-request scraper concurrency limit.

Detail handlers claim a task using `SKIP LOCKED`, with a random ownership token and a 45-second lease. A 15-second handler budget bounds each fetch (the source adapter has its own shorter deadline). Expired leases are reclaimable after crashes. Completion checks both ownership and the summary hash: a late detail response cannot overwrite a newer summary. Successful facts and their event/provenance commit atomically. Each listing has one queue entry; multiple searches link to it instead of duplicating pending work. A detail task may be fetched again after a crash; committed publication is fenced and idempotent.

Network/timeouts and server errors retry up to three attempts, with 10/20-second delays. HTTP 429, budget waits and unavailable proxy pools defer work without exhausting failure attempts. Invalid/parser/client failures are terminal until another refresh/recheck. Exhausted timeout/network/5xx failures enter the separate delayed retry queue, defaulting to one hour; see [delayed retries](INVENTORY_SCHEDULING.md#delayed-retry-queue). Parser/client failures remain terminal. Failures preserve summaries and successful historical facts. `detailsPending`, `detailsFailed` and `crawlDone` separate page completion from enrichment; `done` remains false while linked details are queued/running/retrying, except when the user pauses polling. Pause affects page crawling; detail tasks already queued continue independently. Resume preserves the page cursor. The UI retains comparison selections while refreshed inventory updates partial facts and recomputes scores.

## Lifecycle policy

| Evidence | Stored outcome |
| --- | --- |
| Summary appears in a fetched page | `active`; update last seen and record a summary event. |
| Successful parsed detail response | `active`; update last checked, last verified and detail facts. |
| Explicit detail HTTP 404/410 | `unavailable`; update last checked/verified and record the exact status. This records source absence, not proof of a sale. |
| Timeout, 429, 403, network, 5xx or parser failure | Record failed check; preserve availability and facts. Never infer removal. |
| Missing from a partial, capped or full search | No removal inference. |
| Successful recheck after absence | Restore `active`, retaining the previous absence event. |

A minute scheduler queues at most 100 direct reverifications for listings seen within seven days and not checked in 48 hours. Already queued, running or retrying tasks are excluded before the batch limit, so they do not starve other due listings. These requests use the persisted source URL and bypass the detail cache to obtain current source evidence. Failed checks count as attempted checks, so scheduled retries do not spin continuously. Explicit user rechecks are also available. The queue uses existing provider limits. Unavailable inventory is excluded from stored search and active frontend results; history remains accessible.

The main search reads persisted inventory immediately and enqueues a bounded delta refresh. Independently, native brand partitions for all three sources and both vehicle types are staggered across rolling 48-hour cycles. Active jobs and terminal demand jobs less than five minutes old are reused. PostgreSQL holds responses, observations, URLs and checkpoints; Redis caches inventory queries with transactional revision invalidation. See [inventory-first scheduling](INVENTORY_SCHEDULING.md) for coverage targets, overlap rules, pacing, proxy management and deployment instructions.

Application restarts preserve committed progress when the PostgreSQL volume is retained. An interrupted page/detail request may run again after its 60/45-second lease expires; ownership tokens prevent an old worker from publishing over its replacement. Migration `005_coverage.sql` adds the rolling coverage schedule, cache revision and proxy configuration; existing listings and checkpoints are preserved. Parser or marketplace cursor changes may still require a source-specific retry or new full crawl; cache entries cannot repair an incompatible parser. Keep API and worker versions aligned and follow the release backup/rollback workflow.

Each listing exposes `lifecycle`: availability, firstSeen, lastSeen, lastVerified, lastChecked and checkError. `lastSeen` describes source-page observation; successful direct checks update `lastVerified`. Failed attempts do not advance `lastVerified`.

`provenance[field]` records observationId, source platform, source URL, observedAt and observation kind. Only explicit non-sentinel facts acquire provenance. Detail merges retain inherited summary fields' provenance; fetched detail fields point to the detail observation. Reused cached facts retain their original observation. Full snapshots and failed checks are kept in `listing_events`, so price changes and conflicting observations can be inspected without overwriting history. Source identity `(platform,id)` stays canonical; this does not merge advertisements across marketplaces.

## API and UI

- `GET /api/v1/inventory/:platform/:listingID/history?offset=0`: current listing with lifecycle/provenance, plus up to 50 events newest first. Offset pagination is bounded at 100,000. Unknown listing returns 404.
- `POST /api/v1/inventory/:platform/:listingID/recheck`: queue an existing canonical listing for direct verification, returning 202. This endpoint uses the stored URL rather than accepting a caller-supplied crawl target.
- Job and inventory rows include lifecycle/provenance. Existing JSON contracts remain additive.

The detail drawer shows first/last seen, last definitive check, latest failed attempt, source/observation for core facts and paginated observation/asking-price history. Users can request a recheck and refresh history. The fetched canonical facts update the shared frontend rows and recalculate scores. These are observed asking prices, never completed transactions.

## Verification and operation

Migration `004_bama_city.sql` repairs current Bama city facts from source location labels, without rewriting historical events or observations. Unsupported locations become unknown rather than inheriting the requested city. Existing job snapshots exclude Bama listings outside the selected city; affected terminal jobs stop being reused until fetched again. Source page cache keys use a new namespace, so old incorrectly scoped responses are not reused. Deploy API and worker together: an old worker can reintroduce the incorrect city assignment. Bama page routes and API region values are distinct (for example, `tehran-tehran` and `tehran,tehran`); new pages validate returned city labels and retain continuation cursors even when every result is outside the requested city.

Disposable-database integration tests cover summary visibility before details, queue lease recovery, duplicate publication rejection, stale-summary fencing, successful facts/provenance, timeout/rate-limit/parser failures, 404 absence, reappearance and price history. Source-adapter tests check that HTTP 404/410 types survive the direct-facts adapter. Worker unit tests cover success and error classification. API and worker must run the same migrated schema version.

Use the existing Compose startup; migrations run under an advisory lock. Inspect `detail_tasks` status/attempts/error_kind and `listing_events` for operational diagnosis. Historical events have no automatic pruning; configure retention and backups before sustained production crawling. Rechecks and job-control endpoints still use the application's existing access model; add authentication before exposing this internal control surface to untrusted users.

## Local delivery result — 2026-11-02

All services are healthy after migration. A Bama-only all-Iran search returned its job in 50 ms; nine summaries were visible with nine detail tasks pending. Detail counts fell to zero, all nine completed successfully, and history returned summary/detail events, canonical verification timestamps and field provenance. Backend race/coverage and focused adapter regressions passed, as did static analysis, 61 frontend tests, type checking and production build. Six Chromium/WebKit browser tests passed; physical-device limitations remain documented in the frontend [mobile matrix](../../Jame_e_Jahan_Nama/MOBILE_REGRESSION.md).
