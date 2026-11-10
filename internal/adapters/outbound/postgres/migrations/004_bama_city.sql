-- The data repair uses the same source-location normalization as new scrapes.
-- No historical events or observations are rewritten.
UPDATE crawl_jobs j SET updated_at=now()-interval '7 hours'
FROM crawl_searches s WHERE j.search_id=s.id
AND j.status IN ('complete','partial','failed')
AND (s.criteria->>'platform' IS NULL OR s.criteria->>'platform'='bama');
