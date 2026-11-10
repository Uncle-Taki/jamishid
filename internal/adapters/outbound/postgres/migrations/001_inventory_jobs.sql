CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE TABLE crawl_searches (
 id text PRIMARY KEY, criteria jsonb NOT NULL, demand double precision NOT NULL DEFAULT 1,
 requested_at timestamptz NOT NULL DEFAULT now(), demand_updated_at timestamptz NOT NULL DEFAULT now(),
 next_refresh timestamptz NOT NULL DEFAULT now(), last_full timestamptz
);
CREATE TABLE crawl_jobs (
 id text PRIMARY KEY, search_id text NOT NULL REFERENCES crawl_searches(id), mode text NOT NULL,
 status text NOT NULL DEFAULT 'queued', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 new_count integer NOT NULL DEFAULT 0, changed_count integer NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX crawl_one_active ON crawl_jobs(search_id) WHERE status IN ('queued','running','paused');
CREATE TABLE crawl_tasks (
 job_id text NOT NULL REFERENCES crawl_jobs(id), platform text NOT NULL, cursor text NOT NULL DEFAULT '', page integer NOT NULL DEFAULT 0,
 status text NOT NULL DEFAULT 'queued', attempts integer NOT NULL DEFAULT 0, available_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz, token text, error_kind text NOT NULL DEFAULT '', next_cursor text NOT NULL DEFAULT '',
 PRIMARY KEY(job_id,platform)
);
CREATE INDEX crawl_claimable ON crawl_tasks(status,available_at,lease_until);
CREATE TABLE listings (
 platform text NOT NULL, id text NOT NULL, data jsonb NOT NULL, normalized_text text NOT NULL,
 search_vector tsvector GENERATED ALWAYS AS (to_tsvector('simple',normalized_text)) STORED,
 vehicle_type text NOT NULL, city text NOT NULL, price double precision NOT NULL, mileage double precision NOT NULL,
 year integer NOT NULL, body text NOT NULL, summary_hash text NOT NULL,
 first_seen timestamptz NOT NULL DEFAULT now(), last_seen timestamptz NOT NULL DEFAULT now(),
 details_at timestamptz, PRIMARY KEY(platform,id)
);
CREATE INDEX listings_trigram ON listings USING gin(normalized_text gin_trgm_ops);
CREATE INDEX listings_fulltext ON listings USING gin(search_vector);
CREATE INDEX listings_filters ON listings(vehicle_type,city,price,year);
CREATE TABLE search_listings (
 search_id text NOT NULL REFERENCES crawl_searches(id), platform text NOT NULL, listing_id text NOT NULL,
 last_seen timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(search_id,platform,listing_id),
 FOREIGN KEY(platform,listing_id) REFERENCES listings(platform,id)
);
CREATE TABLE listing_observations (
 job_id text NOT NULL REFERENCES crawl_jobs(id), platform text NOT NULL, page integer NOT NULL, listing_id text NOT NULL,
 data jsonb NOT NULL, observed_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(job_id,platform,page,listing_id)
);
CREATE INDEX crawl_due ON crawl_searches(next_refresh,requested_at);
