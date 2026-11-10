ALTER TABLE crawl_searches ADD COLUMN coverage boolean NOT NULL DEFAULT false;
ALTER TABLE crawl_searches ADD COLUMN page_interval_seconds integer NOT NULL DEFAULT 60;
ALTER TABLE crawl_tasks ADD COLUMN overlap_pages integer NOT NULL DEFAULT 0;
CREATE INDEX coverage_due ON crawl_searches(next_refresh) WHERE coverage;
CREATE TABLE coverage_catalogs (
 platform text NOT NULL, vehicle_type text NOT NULL, categories jsonb NOT NULL DEFAULT '[]',
 next_refresh timestamptz NOT NULL DEFAULT now(), lease_until timestamptz, token text,
 last_success timestamptz, error_kind text NOT NULL DEFAULT '', PRIMARY KEY(platform,vehicle_type)
);
INSERT INTO coverage_catalogs(platform,vehicle_type) SELECT p,t FROM unnest(ARRAY['bama','divar','sheypoor']) p CROSS JOIN unnest(ARRAY['car','motorcycle']) t;
CREATE TABLE inventory_revision(id boolean PRIMARY KEY DEFAULT true CHECK(id), revision bigint NOT NULL DEFAULT 1);
INSERT INTO inventory_revision DEFAULT VALUES;
CREATE FUNCTION bump_inventory_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE inventory_revision SET revision=revision+1; RETURN NULL; END $$;
CREATE TRIGGER inventory_changed AFTER INSERT OR UPDATE OR DELETE ON listings FOR EACH STATEMENT EXECUTE FUNCTION bump_inventory_revision();
CREATE TABLE egress_proxies (
 id text PRIMARY KEY, endpoint text NOT NULL, enabled boolean NOT NULL DEFAULT true,
 cooldown_until timestamptz NOT NULL DEFAULT now(), failures integer NOT NULL DEFAULT 0,
 last_used timestamptz, last_success timestamptz
);

CREATE INDEX observations_listing ON listing_observations(job_id,platform,listing_id);
