# Persian and model normalization

The versioned contract is `internal/domain/vehicle/normalization_contract.json`.
The frontend keeps an identical copy at `src/utils/normalization_contract.json`.
Run `python3 build/check-contracts.py` from the workspace root to detect drift.
Both Go and TypeScript tests evaluate the same equivalence and distinct-pair corpus.

`NormalizeSearchText` folds Arabic/Persian letters, Persian/Arabic/Latin digits,
diacritics, punctuation and spacing. `NormalizeModelText` additionally separates
letter/number boundaries and applies only the explicit brand/model aliases in
the contract. Thus `TNT180S` and `TNT ۱۸۰ S` are equivalent; `180` and `1800`,
`206` and `207`, and different trim numbers remain distinct. Original display
text and structured catalog IDs are preserved. Frontend input matching permits
alphabetic prefixes while typing; cohort identity uses the complete normalized text.

Backend inventory indexing, search input, cohort identity and source title matching
use these functions. PostgreSQL migration 003 rebuilds existing listing indexes
in bounded batches inside the migration transaction and records the contract version.
Pure numeric query tokens require exact full-text tokens in addition to fuzzy
matching, so trigram similarity cannot silently change an engine/model number.

To add an alias, update the canonical JSON and frontend copy, add equivalent and
nearby distinct examples, then run both test suites and the workspace release gate.
A future contract revision also needs a migration to reindex existing inventory.
This is an explicit equivalence policy, not a complete Persian language analyzer.
