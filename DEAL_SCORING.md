# User utility scoring and comparison confidence — utility-v3

The API aggregation path and React scorer compare each ad with independent **asking-price** peers in the currently loaded search results. Stored inventory remains source facts; scores and evidence are recomputed when results, details or user weights change. This is a transparent starting heuristic, not a calibrated probability, appraisal or completed-sale estimate.

## Cohort rules

- Require an explicit vehicle type and normalized reported `brandModel`. Do not infer a model from seller prose; apply only explicit Persian/English aliases from the shared normalization contract. Arabic/Persian letters, digits, punctuation and spacing normalize for identity; original display text remains intact.
- Comparison-only Q5 e-tron aliases also match the reported Persian `کیو 5 ای ترون` spelling. Explicit 40/50 suffixes on that model are interpreted as trims when a separate trim is absent or agrees. Conflicting explicit trims, petrol Q5s and different trims stay separate. Source fields are preserved. This fixes model/cohort splits without asserting that two ads describe the same physical vehicle. Other unrecognized aliases may still need explicit mappings.
- Exclude the target and duplicate `(platform,id)` identities. Cross-source ads may still represent the same vehicle; cross-source duplicate grouping remains separate work.
- Exclude known trim mismatches, gearbox mismatches and engine displacements differing by more than max(5 cc, 5%). A missing trim is reported and caps confidence at 65; a missing model/type prevents a cohort entirely. Bama summary trims and labelled detail trims are preserved as `trimName`.
- Compare years within one normalized Solar Hijri year. Compare mileage within max(18,000 km for cars / 6,000 km for motorcycles, 25% of target mileage). Known body conditions must match exactly. Missing peer facts remain candidates but reduce confidence.
- Prefer same-city peers when at least five are available. Otherwise use compatible peers across cities and explicitly label the broader scope; mixed/unknown-region cohorts get a 0.75 confidence factor.
- Use a plain median of peer asking prices, excluding the target. Fewer than three peers means no price benchmark or percentage comparison. Median absolute deviation reduces confidence for dispersed cohorts; a single extreme price does not shift a stable cluster's median.

## Evidence and ranking

`comparableCount` is the deduplicated priced peer count. Completeness is the fraction of eight known fields: price, model, trim, vehicle type, year, mileage, body condition and city. Confidence is rounded from:

`100 × min(peerCount / 10, 1) × targetCompleteness × meanPeerCompleteness × regionFactor / (1 + 4 × relativeMedianAbsoluteDeviation)`

Unpriced targets or fewer than three peers have confidence zero. Missing trim caps it at 65. Labels: low <40, medium 40–74, high >=75. These thresholds are policy choices that need reviewed real-world evaluation. A high label only means stronger available evidence under these rules; it cannot verify seller claims.

Existing price, mileage, condition and year sub-scores remain visible and user weights still apply. The total is the normalized weighted score:

`totalScore = round(sum(subScore × userWeight) / sum(userWeight))`

User utility and comparison confidence are independent. Sliders and presets edit draft weights. Applying normalizes them to integer percentages totaling exactly 100% using largest-remainder rounding, then recomputes scores and ordering from already loaded facts without fetching again. An all-zero draft is rejected in the UI; the utility itself still returns a neutral 50 for zero total weight. Scoring version: `utility-v3`.

Below confidence 40, the deal tier is `insufficient-data` and the label explains limited evidence. Such ads remain browsable, comparable and eligible for the user-utility podium, with their evidence limitations displayed. Missing prices also receive an explicit unavailable label. No peers means a neutral price sub-score of 50; known mileage, condition and year still contribute according to user weights. The total utility is not a claim of fair market value. Cohort years determine the year sub-score range rather than unrelated models in the batch.

Cards, result tables, comparisons and details expose clickable confidence, sample count and city scope. Each opens an accessible dialog with missing fields, confidence reasons, score components/weights, reference median and links to the exact selected peer ads. Peer lists are reconstructed on demand from the score's loaded pool, avoiding quadratic stored peer arrays. The target is excluded, and the dialog makes clear that cross-source ads may duplicate vehicles. Percentage deltas are hidden when a benchmark or target price is absent. JSON exports retain score evidence and weights; CSV includes sample count, confidence, missing fields, price basis and scoring version.

## Validation and limitations

The Go/TypeScript copies of `testdata/cohort_cases.json` define the same 14 hand-specified scenarios: strong/local, sparse (confidence only), self-only, broader region, missing trim, trim/type/model mismatches, Persian digits, Gregorian years, year/mileage/condition incompatibility and unknown price. Both implementations verify the same count, confidence, median and price-only total. Additional tests cover duplicate identities, outlier stability, incremental sample changes, incomplete peers, dispersion and UI explanations.

These are reviewed implementation fixtures with explicit expected outcomes, not independent expert-labelled marketplace evaluations. Before tuning thresholds, collect real ad sets reviewed by vehicle specialists, record whether each peer is genuinely comparable and whether pairwise rankings are sensible, and measure false matches, missing-data coverage and ranking changes when one ad is added/removed. Do not claim expert validation or statistical confidence intervals from these unit tests.

Shared `testdata/q5_pair.json` records public scoring facts from the reported Bama/Divar pair, with Bama's stale city corrected from its reported source location by migration 004. Both scorers verify identical model/trim groups and scores under the same city, weights and peer facts, plus rejection of petrol/trim-conflict variants. The original historical 86/80 cannot be replayed from URLs alone without the original loaded pool and weights. Runtime checks found the same price/year/mileage/body facts but different model labels, separate-vs-inline trim and a stale Bama city; both cohort selection and local preference could therefore differ.

Scoring is limited to loaded inventory (including pagination/freshness restrictions), so confidence is not a statement about the entire market. Normalization is conservative: aliases can miss comparable ads; missing trim can broaden cohorts. No regression model adjusts price for specification differences. Browser grouping caches normalized facts once per scoring pass; peer selection still scales with cohort size. Large homogeneous inventories need further performance work before claiming production-scale latency.

## Delivery checks

The release gate runs backend race/integration tests, static analysis, frontend tests,
TypeScript checks, production builds and desktop browser regressions. Shared fixtures
verify Go/TypeScript agreement. Dedicated sparse-result tests verify preference-driven
ranking without market-comparison confidence. The desktop slider regression verifies
that draft changes leave ranks unchanged, then Apply normalizes weights and reorders
results without another crawl request.
