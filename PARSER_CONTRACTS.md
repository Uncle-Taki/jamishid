# Parser contracts and source change checks

Every source page validates candidate listings before downstream relevance filtering.
Listings need an ID, title and canonical HTTPS source URL; negative/nonfinite prices
and impossible sentinel values are rejected. Unknown price, year or mileage may be
legitimate and are counted separately. A nonempty candidate collection yielding no
valid listing produces a typed parser failure. A recognized empty collection remains
successful. Generic HTML/application shells are not accepted as empty inventories.

`internal/adapters/outbound/scrapers/testdata/contracts/` contains valid, empty and
drift fixtures for Divar, Bama and Sheypoor. Parser tests exercise actual public-page
parsing, not only the validator. Existing HTML fixtures cover recognized empty and
malformed source payloads. Update fixtures from sanitized responses when a source
changes; inspect a failing response before weakening a contract.

`parser_contract` observations distinguish `ok`, `empty`, `partial` and
`schema_failure`. Page reports contain candidate, valid, rejected and missing-field
counts. Source smoke JSON adds duration and classified failures, without listing
bodies or user queries. A zero-listing response alone never raises an alert.

Run `go run ./cmd/sourcecheck` from `backend/`. Optionally set `REDIS_URL` to use
the same provider budget as the API/workers. The checker uses two fixed, distinct
queries per source, skips detail enrichment and bypasses cached pages. Both probes
failing or both showing substantial schema degradation raise an alert and exit 1;
configuration failures exit 2. Network/rate-limit/parser failures stay distinguishable.

`.github/workflows/source-smoke.yml` runs this checker daily and on manual dispatch,
retaining the JSON report as an artifact. Push these workflows to enable them;
scheduled workflows run from the repository's default branch. Public-source checks
are separate from deterministic PR checks because connectivity can fail independently
of application correctness. Review failed workflow runs; no external notification
channel is configured by this implementation.
