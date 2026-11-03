# Inventory-first crawling and rolling coverage

The main search flow reads `GET /api/v1/inventory` immediately, then submits an asynchronous delta job. Closing the browser does not cancel server work. The browser refreshes stored results while the job progresses; job snapshots never replace the inventory with a smaller, search-specific subset. Further result pages read the database/cache. Applying utility factors recomputes only the already loaded rows in the browser.

PostgreSQL owns canonical facts, source URLs, page checkpoints, queues, history and coverage schedules. Migration `005_coverage.sql` adds category discovery state, coverage metadata, an inventory revision and the proxy pool. Redis caches inventory queries for two minutes, keyed by the PostgreSQL revision. A listing mutation increments that revision transactionally, including detail updates and removals. A cache miss or Redis read/write failure falls back to PostgreSQL. API and worker must be deployed together; retain both data volumes and take the normal database backup before migration.

## Coverage and pacing

The worker discovers one due native brand catalog per minute, for each of Divar, Bama and Sheypoor, for cars and motorcycles. Catalog discovery is leased, persisted, and retried after 15 minutes on failure. Each brand is a national partition containing all its models. New partitions receive a stable start offset distributed across the next 48 hours; repeating discovery cannot reset their schedules. Catalogs refresh every 48 hours to discover new brands. Existing brand partitions are retained when a catalog temporarily omits them.

A full scan persists each page cursor and waits initially 60 seconds between pages of a partition. Later scans use the previous page count to spread work across 36 hours (reserving headroom for demand/retries), with an interval of 1–900 seconds. The next cycle is due 48 hours after the previous cycle started, or immediately if already late. Partial source scans retry after 15 minutes. No browser visit is required to start this work. The scheduler and workers recover existing leases after a process restart.

All API/worker source calls, including catalogs, summaries and details, share a Redis budget per marketplace: at least two seconds between outbound attempts, with adaptive backoff and shared `Retry-After` cooldowns. A proxy change does not create another budget. Existing deployments with several replicas still share this limit. Waiting for a source budget or an unavailable proxy pool defers tasks without consuming their failure attempts. HTTP 429 also defers work; 404/410 on detail verification marks the listing unavailable. Actual transport/parser failures remain observable.

48 hours is a scheduling target, not a coverage guarantee. Initial catalog discovery, large inventories, provider pagination limits, rate limits and outages can delay completion. Full scans stop visibly at 10,000 pages per partition; reaching this cap is partial coverage, never successful coverage. This may require finer native partitions for very large brands. Catalog coverage reflects advertised native brands; uncategorized or provider-hidden ads cannot be guaranteed. `GET /api/v1/coverage` and the search page show discovered partitions, partitions completed within the last 48 hours, active work, overdue coverage and catalog failures. A complete source scan means the provider's reachable pagination was exhausted, not that all historical ads were captured.

Search delta jobs have priority over scheduled page jobs. Active jobs and terminal jobs less than five minutes old are reused, including requests with `refresh: true`, preventing repeated clicks from bypassing the cooldown. Delta jobs stop after two consecutive pages with at least five distinct observations and at least 90% IDs present before the job started. IDs already observed within this job cannot count again, so repeated promotions/pages cannot manufacture overlap. Otherwise, a delta is bounded at 20 pages per source and reports partial coverage when capped. Baseline scans continue independently. Absence from these pages never establishes deletion.

Details remain a separate, deduplicated queue. Unchanged detail facts are reused for 48 hours. The verification scheduler queues at most 100 due listings per minute, sharing the same source budgets. Backlogs may exceed the target; inspect task states and coverage rather than assuming elapsed time means completion. Existing source-page/detail cache TTLs remain 24 hours; workers explicitly refresh source evidence when necessary.

Inventory text matching uses the selected model label and optional keywords over normalized stored facts, with city/type filters. It no longer requires that a listing was previously encountered in precisely the same user search. `model` carries the selected vehicle label and `q` carries optional keywords. Legacy clients without `model` retain the original category-membership filter. Scores describe the loaded result set, not all inventory pages.

The legacy `/crawl` and `/listing` endpoints retain their existing behavior for older integrations. The main application uses the inventory/job flow. Source catalog requests from the selector still use their catalog cache and the shared provider budgets.

## Manage outbound IPs

An IP must belong to a reachable operator-managed HTTP/HTTPS/SOCKS5 proxy; adding a bare IP cannot change the machine's network routing. Set `JAMSHID_ADMIN_API_TOKEN` in the Compose environment (prefer an untracked `.env` file), then recreate the API and worker. Do not put proxy credentials in committed files. Administrative routes do not exist when the token is empty. These examples use environment variables populated by the operator:

```bash
# Add or replace an endpoint. The same endpoint with enabled=false disables it.
curl -X PUT "$JAMSHID_BASE/api/v1/admin/proxies/primary" \
  -H "Authorization: Bearer $JAMSHID_ADMIN_API_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"endpoint":"http://proxy-host:3128","enabled":true}'

curl "$JAMSHID_BASE/api/v1/admin/proxies" \
  -H "Authorization: Bearer $JAMSHID_ADMIN_API_TOKEN"

curl -X DELETE "$JAMSHID_BASE/api/v1/admin/proxies/primary" \
  -H "Authorization: Bearer $JAMSHID_ADMIN_API_TOKEN"
```

Proxy configuration is read from PostgreSQL on each source request and shared across running processes. Selection uses least-recently-used eligible endpoints. Network failures cool an endpoint for 30–900 seconds; successful responses update its health. HTTP source errors such as 429 use the marketplace cooldown rather than labeling the proxy unreachable. The listing response exposes endpoint host/scheme and health, never proxy credentials. With no configured proxy records, direct/environment-proxy access is used. With proxy records present but all disabled/unhealthy, crawling defers instead of silently going direct. Deleting the final proxy record restores direct mode. Already in-flight requests may finish after disabling/removing an endpoint.

## Operational checks

```sql
SELECT * FROM coverage_catalogs;
SELECT coverage, count(*), min(next_refresh), max(next_refresh)
FROM crawl_searches GROUP BY coverage;
SELECT j.mode,t.platform,t.status,count(*)
FROM crawl_tasks t JOIN crawl_jobs j ON j.id=t.job_id
GROUP BY j.mode,t.platform,t.status ORDER BY 1,2,3;
SELECT purpose,status,error_kind,count(*) FROM detail_tasks GROUP BY 1,2,3;
```

Keep the worker running throughout the cycle. API/worker startup still requires reachable configured Redis for the existing cursor-signing/budget setup; the PostgreSQL fallback applies to inventory cache failures after startup. Proxy pooling is operational routing and health management, not a guarantee against marketplace blocking.

## Delayed retry queue

Migration `006_delayed_retries.sql` adds the durable `crawler_retries` queue for page and detail timeout, network and HTTP 5xx failures after the existing three task attempts. The default delayed retry is **one hour**. Set `JAMSHID_CRAWLER_RETRY_DELAY=2h` in Compose to use two hours (direct Go deployments use `CRAWLER_RETRY_DELAY`; accepted range: 1h–2h). API and worker should use the same value. Each delayed retry gets a fresh short-attempt budget; repeated exhausted failures return to this queue, preserving source cursors/URLs and known facts.

The minute scheduler releases up to 100 due tickets into normal workers; source-wide cooldowns still apply and can postpone actual requests further. Entries are unique per page task or listing. Paused jobs remain paused, and old detail tickets cannot overwrite a newer summary. Ordinary summary observations, explicit detail rechecks and periodic verification do not reset an unchanged listing's delayed cooldown. A changed summary supersedes its old retry ticket. 404/410 and parser/validation/client errors do not enter this queue. Existing terminal failures are not bulk-requeued by the migration.

Job status exposes `retryAt` and `retriesPending`. When only delayed work remains, browser polling stops and the UI displays the retry time; workers continue independently. `done` means the current foreground wait ended, while `crawlDone` still distinguishes pending delayed page work. To observe eventual results after an hour, search again or reload the results.

```sql
SELECT kind,platform,error_kind,count(*),min(available_at)
FROM crawler_retries GROUP BY kind,platform,error_kind;
```
