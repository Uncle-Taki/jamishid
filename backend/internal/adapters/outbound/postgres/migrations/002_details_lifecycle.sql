ALTER TABLE listings ADD COLUMN availability text NOT NULL DEFAULT 'active', ADD COLUMN last_verified timestamptz, ADD COLUMN last_checked timestamptz, ADD COLUMN check_error text NOT NULL DEFAULT '', ADD COLUMN provenance jsonb NOT NULL DEFAULT '{}';
CREATE TABLE detail_tasks (
 platform text NOT NULL, listing_id text NOT NULL, summary_hash text NOT NULL,
 status text NOT NULL DEFAULT 'queued', attempts int NOT NULL DEFAULT 0,
 available_at timestamptz NOT NULL DEFAULT now(), lease_until timestamptz, token text,
 purpose text NOT NULL DEFAULT 'enrich', error_kind text NOT NULL DEFAULT '',
 PRIMARY KEY(platform,listing_id), FOREIGN KEY(platform,listing_id) REFERENCES listings(platform,id)
);
CREATE INDEX detail_claimable ON detail_tasks(available_at,lease_until) WHERE status IN ('queued','retry','running');
CREATE TABLE job_details (
 job_id text NOT NULL REFERENCES crawl_jobs(id), platform text NOT NULL, listing_id text NOT NULL,
 PRIMARY KEY(job_id,platform,listing_id), FOREIGN KEY(platform,listing_id) REFERENCES detail_tasks(platform,listing_id)
);
CREATE TABLE listing_events (
 observation_id bigserial PRIMARY KEY, platform text NOT NULL, listing_id text NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT now(), kind text NOT NULL, status_code int NOT NULL DEFAULT 0,
 error_kind text NOT NULL DEFAULT '', data jsonb NOT NULL, FOREIGN KEY(platform,listing_id) REFERENCES listings(platform,id)
);
CREATE INDEX listing_events_history ON listing_events(platform,listing_id,observation_id DESC);
CREATE INDEX listings_reverify ON listings(last_checked) WHERE availability='active';
CREATE FUNCTION listing_payload(l listings) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT l.data || jsonb_build_object('lifecycle',jsonb_build_object('availability',l.availability,'firstSeen',l.first_seen,'lastSeen',l.last_seen,'lastVerified',l.last_verified,'lastChecked',l.last_checked,'checkError',l.check_error),'provenance',l.provenance)
$$;
