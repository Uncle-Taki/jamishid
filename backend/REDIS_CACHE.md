# Shared Redis cache

The backend supports Redis for both source pages and item details. Set `REDIS_URL` to enable it; workspace Compose configures `redis://redis:6379/0`. Without that variable, the bounded in-memory caches remain available for standalone development.

| Setting | Behavior |
| --- | --- |
| Data TTL | 24 hours after a successful fetch; cache reads never renew it |
| Isolation | Versioned `jamshid:cache:v1:` namespace; page/detail lookup keys are hashed |
| Shared fetch coordination | Redis `SET NX` leases plus existing local singleflight |
| Lease lifetime | 30 seconds, longer than current 15-second page and 8-second detail fetch deadlines |
| Waiting callers | Poll for the owner's result; respect cancellation/deadlines |
| Failed fetches | Not cached; lease released so a later attempt can retry |
| Safe publication/release | Lua scripts check the random lease-owner token; an expired owner cannot publish over a newer result or delete another owner's lease |
| Pagination | Shared, non-expiring cursor signing key in Redis; cached tokens remain usable on another API instance and after API restart |
| Persistence | AOF enabled, `appendfsync everysec`, named `redis-data` Docker volume |
| Capacity | Redis maximum memory 256 MB; `noeviction` protects active leases and the cursor signing key |
| Exposure | Redis has no host port; only the private Compose network is used |

The cache stores raw page data and pagination metadata. Application filters and scores are recomputed for every response. Item facts are shared across searches/cities without copying a previous listing's identity into a new result. Canonical inventory and price history still need a separate durable listing store.

## Failure behavior

When Redis is configured, the backend checks connectivity and loads the shared signing key at startup. Startup fails if either step fails; it does not silently select a private memory cache that would break sharing.

During an outage or when Redis refuses writes because memory is full, cache misses cannot trigger uncoordinated upstream fetches. The affected source reports failure; other available source results retain the existing partial-result behavior. Failed publication is also reported instead of claiming a cache write succeeded. Redis read/write metrics use the existing observer, and errors are classified without logging connection credentials.

Redis persistence with `everysec` can lose approximately the most recent second of writes in a crash. The named volume survives container recreation; deleting it removes cached values and the signing key. Ordinary expiry is automatic. Do not selectively delete the signing key while preserving pages containing old tokens.

The leases coordinate cache fills against one Redis service. They are not an exactly-once guarantee during Redis data loss/failover, and this adapter is not a Redis Cluster/Redlock deployment. Provider slots, adaptive intervals and cooldowns are now shared through Redis across configured API and worker processes. See [durable crawling](CRAWL_JOBS.md) for summary refresh and selective detail enrichment.

## Operations

From the workspace root:

```sh
docker compose -f build/compose.yaml up -d --wait redis
docker compose -f build/compose.yaml build backend
docker compose -f build/compose.yaml up -d --wait backend frontend
docker compose -f build/compose.yaml exec -T redis redis-cli INFO persistence
docker compose -f build/compose.yaml exec -T redis redis-cli INFO memory
```

Use the existing metrics listener to inspect `redis_read`/`redis_write` operation outcomes. Monitor Redis memory usage and refused writes; `noeviction` deliberately trades new cache writes for lease/key safety when the capacity limit is reached. Increase the capacity or shorten retention only after reviewing demand and freshness requirements.

## Validation

The full backend suite passed with race detection (**76.8% statement coverage**); `go vet`, Compose configuration validation and whitespace checks passed. Tests cover cross-client reuse, fixed TTLs, data ownership, concurrent cold loads, failed-detail retries, canceled waiters, expired-owner publication, owner-safe lease release, stable signing keys, malformed entries and Redis outages. Application integration tests verify two aggregation services sharing a cold page and detail enrichment across two Redis clients.

Live runtime verification used two API processes against the private Redis service: the first Bama search returned nine listings in about 3.3 seconds, and the second process returned the same nine cached listings in 9 ms. Item details were also shared. After restarting both Redis and the API, the same pages and item details remained cached, the signing-key fingerprint stayed unchanged, and TTLs continued counting down. The restarted API recorded no upstream fetches during these checks. Redis reported AOF enabled and its last write status as `ok`.

The deployed runtime was packaged from the compiled/tested binary using the existing backend runtime image. Redis and the updated API are running; the temporary second API used for verification was removed.
