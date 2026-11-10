CREATE TABLE crawler_retries (
 kind text NOT NULL CHECK(kind IN ('page','detail')), platform text NOT NULL,
 subject_id text NOT NULL, job_id text REFERENCES crawl_jobs(id) ON DELETE CASCADE,
 listing_id text, summary_hash text NOT NULL DEFAULT '', error_kind text NOT NULL,
 available_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(kind,platform,subject_id),
 FOREIGN KEY(platform,listing_id) REFERENCES listings(platform,id) ON DELETE CASCADE,
 CHECK((kind='page' AND job_id=subject_id AND listing_id IS NULL) OR (kind='detail' AND listing_id=subject_id AND job_id IS NULL))
);
CREATE INDEX crawler_retries_due ON crawler_retries(available_at);

CREATE INDEX crawler_retries_job ON crawler_retries(job_id) WHERE kind='page';
CREATE INDEX crawler_retries_listing ON crawler_retries(platform,listing_id) WHERE kind='detail';
