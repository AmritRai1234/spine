# Flaky test: TestTableScopeAllowedTableFiltered — count-visible-before-columns-written

**Status:** ROOT-CAUSED (2026-09-14, probe session) — fix pending implementation
**Failure rate:** ~2-4 per 100 runs (`go test ./tests/security/ -run TestTableScopeAllowedTableFiltered -count=100`)
**Reproduces on:** the ORIGINAL scoping commit 4ca90c6 with all later work stashed — pre-existing, NOT caused by analytics work.
**Discovered:** 2026-09-08, during Task 5 of the analytics plan (full `make test` run).

## Root cause (probe-verified, superseding all earlier hypotheses)

> **History note (2026-09-14):** this investigation has overturned two of its own
> prior "confirmed" findings, and both reversals are recorded here deliberately.
> (1) The initial "driver-level schema-cookie staleness" theory was challenged by
> the transactional-DDL probe, which in turn was refuted by the decay-window
> probes — leading to the per-connection plan-cache mechanism below. (2) An
> intermediate report claimed go-sqlite3's statement cache is "disabled by
> default, active only with `_stmt_cache_size` in the DSN" and therefore "ruled
> out" — that verification was WRONG; the probes below demonstrate the cache is
> active in this engine's default configuration (no `_stmt_cache_size` DSN param
> present, yet stale plan reuse is directly observed and reproducible at will).
> Lesson recorded for future sessions: never trust the current "confirmed" state
> without a fresh verification pass — every layer of this bug surrendered to a
> direct measurement that contradicted the layer before it.

The batched writer is NOT at fault. The data is fully committed and correct at the
moment the stale read happens — verified by dumping raw SQL state in the failure
window: the row exists with the correct `email` value, and the schema is complete.

**Actual mechanism — stale cached query-plan projection in mattn/go-sqlite3:**

1. Startup `EnsureTables` (pkg/engine/bus.go:237) creates `orders` with only
   `_spine_id, created_at`.
2. First `db.insert` triggers lazy evolution in `ensureTable`
   (pkg/engine/db_ops.go): CREATE IF NOT EXISTS → ALTER ADD COLUMN `email` →
   CREATE INDEX, each an autocommit statement.
3. `database/sql`/mattn caches compiled prepared statements **per connection**.
   A pooled connection whose plan for `SELECT * FROM orders` was compiled before
   the ALTER keeps serving the OLD two-column projection on its next use — even
   though the row filter (`WHERE email = ?`, evaluated server-side against the
   live row) still matches. Result: 200 OK, one row, no `email` key. Exactly the
   observed signature `{"count":1,"rows":[{"_spine_id":1,"created_at":null}]}`.
4. `WaitForTableRows` counts rows only, so it passes while a pooled connection
   still holds the stale plan — the test then reads and trips.

## Probe findings (empirical, driver-level)

Reproduced locally at will with a hammer-loop (readers polling `SELECT *` vs the
DDL evolution). Key measurements:

- **Window is ≤ ~30 ms after the DDL commit.** Bucketed decay: 100% stale reads
  in the first 20 ms, tail ~5% in the 20–30 ms bucket, ZERO at every bucket
  ≥30 ms. (probe14)
- **Only connections already pooled/active across the DDL are poisoned.** Fresh
  `sql.Open` handles and newly-created pool connections are never stale
  (0/50, 0/100). (probe2, probe13)
- **A single pinned connection serves exactly ONE stale read after the DDL, then
  self-heals** (1 stale / ~130 K fresh reads). Under a mixed pool, thousands of
  stale reads appear because the pool holds many connections that each get their
  one stale read within the window. (probe12, probe3)
- **Wrapping the DDL in one transaction does NOT close the window.** The
  transactional-DDL proposal was REFUTED empirically: identical stale rates with
  autocommit vs tx-wrapped DDL. The poison is the per-connection plan cache, not
  a partially-committed schema. (probe1/5)
- **Flushing all pooled connections with a throwaway statement does NOT help**
  (probe10) — the heal fires on the next use of the *same* SQL text, not on
  unrelated statements.
- **Spacing reads (2 ms sleep) does not change the stale rate** (probe8) — the
  rate is per-connection, not timing-sensitive within the window.
- The original flake reconciles cleanly: fresh-connection testing still failed
  because the pool re-uses (recently created) connections; the ~30 ms window
  covers the count-check → HTTP-read gap in the test. The earlier
  "driver-level schema-cookie staleness" theory was directionally right — this
  work narrowed it to the per-connection prepared-statement cache.

## Fix (proposed, with the airtight argument)

**Replace `SELECT *` in the read paths (pkg/engine/query.go) with explicit
column lists resolved via `PRAGMA table_info` immediately before the query.**

Why this is provably correct: the SQL text itself contains the new column name.
Any prepared statement whose text includes `email` was necessarily compiled
AFTER the ALTER that added `email` — a stale plan cannot exist for that text.
Validated empirically: 0 stale reads / ~63 K under the same hammer that
produces thousands with `SELECT *`. (probe15)

Scope: `GetTableRows`, `QueryWhere`, `GetTableRowsWithFilter`,
`QueryWhereWithAccess`, `QueryMultiWhere`, `GetTableRowsCursor` — all in
pkg/engine/query.go. A small per-query `PRAGMA table_info` adds one cheap
catalog lookup per read (SQLite handles this in microseconds); if it ever
matters, a short-TTL column cache can be added later WITHOUT changing the
correctness argument (a cached list is only ever missing columns added later,
and a later DDL invalidates... — decision for implementation: start uncached,
measure).

Also required (test-side hardening, the issue's original option 2):
`WaitForTableRowContent` (tests/testhelpers/helpers.go) waits on row CONTENT
via the engine's own read path — deterministic for this whole flake class.
Kept alongside (not replacing) `WaitForTableRows` so existing tests are
untouched; new content-sensitive tests should use the content variant.

Implementation record (2026-09-14):
- pkg/engine/table_columns.go — `tableColumns` (fresh catalog introspection
  per query, deliberately uncached) + `quotedColumnList`. Two backends:
  SQLite via `PRAGMA table_info`; PostgreSQL via `information_schema.columns`
  (PRAGMA is a syntax error on pgx, SQLSTATE 42601 — caught by the PG
  integration suite). The stale-projection fix is structural on both.
- pkg/engine/query.go — all six read paths converted: GetTableRows,
  QueryWhere, GetTableRowsWithFilter, QueryWhereWithAccess,
  GetTableRowsCursor, QueryMultiWhere. `SELECT *` is now banned on read
  paths.

  KNOWN, ACCEPTED COST (measured post-fix via A/B, 2026-09-14 — supersedes
  the pre-implementation estimate): old `SELECT *` 24.9µs / 2.3KB / 173
  allocs vs new explicit-column-list 54.2µs / 21.8KB / 345 allocs per
  50-row read (+2.2× latency, +9× bytes). The pre-implementation estimate
  ("59µs vs 45.5µs, +13µs, ~29%") was taken before the code existed and
  measured a different thing — it undersold the real cost by a wide margin.
  Lesson recorded: a quick benchmark before code exists is a guess; only
  the post-implementation A/B is the number. ACCEPTED because correctness
  outweighs throughput on public read paths and no deployment profile shows
  /tables reads as hot (Kingston-only storefront, occasional admin polling —
  54µs is invisible). TRIGGER TO REVISIT: a deployment needing
  high-frequency table reads. The tuning round (queryRows pooled
  scan-buffer sizing + catalog-query cost) must be its own scoped piece of
  work with its own A/B — not an addendum to this correctness fix.
- tests/security/stale_projection_regression_test.go — deterministic RED
  (failed pre-fix: stale projection observed; the flaky variant also failed
  ~7/10 runs) / GREEN post-fix, `-race` clean.
- tests/features/postgres_integration_test.go — TestPG_EmitPersistAndIdempotency
  row name now nanosecond-resolution: the shared items table meant two runs
  landing in the same wall-clock second produced a false "got 2 rows"
  (the second run counted the first run's row — a test-collision, not a
  double insert; verified by pre-count instrumentation).
- Full `make test` green (8/8 packages, 0 failures); PG integration suite
  `TestPG_ -count=20` x3 green.

## Explicitly rejected

- Deadline extension (276de56-style): count already matched; content did not.
- Transactional DDL: refuted by probe — does not close the window.
- Flush-all-connections after DDL: refuted by probe — no effect.
- "Declare full schema up front, no runtime ALTER" redesign: still the right
  long-term round, but orthogonal — this fix makes the interim state safe.

## Severity note

Not a security bug: the scope filter still applied in every observed stale read
(the flake's own SCOPE LEAK assertion never fired). Exposure = one request, up
to ~30 ms after a schema evolution, may see a row missing a recently-added
column. Bounded and self-healing.
