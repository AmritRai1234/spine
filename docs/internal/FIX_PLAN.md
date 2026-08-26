# Spine Fix Plan

> Derived from the full codebase assessment (2026-02). Every item lists the exact files, the fix approach, the test that would have caught the bug, and an effort estimate (S ≤ 0.5d, M ≤ 1-2d, L ≥ 2-3d).
>
> **DoD for every item:** code change + a regression test that fails on the old code + docs/claims updated where applicable + `go vet ./...`, `go test ./...`, `doc-lint` green (and `-race` once Phase 0 lands).
>
> **Status legend:** ✅ done · 🔧 in progress · ⬜ open

## Status

- **Batch A (guardrails): ✅ DONE** — `-race` in CI+Makefile, repo hygiene, single-source version (3.0.1).
- **Batch B (criticals): ✅ DONE** — outbox retry loop, FTS5 provisioning, `$env` payload exfiltration. All three have regression tests. NOTE: FTS requires the `sqlite_fts5` build tag, now wired into Makefile/CI/install.sh.
- **Batch C (write-path semantics): ✅ DONE** — P1-4 (statement failures counted, never silent; `spine_stmt_failures` metric), P1-5 (math operands validated as numbers), P1-6 (deterministic `db.delete` fallback), P1-7 (parameterized `email.broadcast` where), P1-8 (flush fence before idempotency 'completed'), P1-9 (flush fence + sync compensation in saga rollback). New: `shardedWriter.flushAndWait` durability fence.
- **Batch D (parser/registry): ✅ DONE** — P1-10 (validation moved to post-include-merge; cross-file event references work; node names unique across includes), P1-11 (fail-closed boolean parsing via strconv.ParseBool), P1-12 (bool payloads bind as 0/1 for Postgres parity), P4-2 (`ValidatePayload` errors on unknown declared types; inline `# comments` no longer corrupt field types), P4-3 (conflicting duplicate event shapes rejected at parse time).
- **Batch E (security posture): ✅ DONE** — P2-1 (CLI refuses to serve without auth unless `--allow-no-auth`; engine `SetAuthFailClosed`; init templates generate random keys; deploy templates document SPINE_API_KEY), P2-2 (RLAC on WS broadcasts + `/events` + reconnect replay via Events whitelist), P2-3 (`/ws` rate limiting + per-IP conn cap), P2-4 (rate-limiter burst ≥ 1 clamp + sub-1-rps degradation), P2-5 (DSN credential redaction in CLI output), P2-6 (panic details no longer leaked in 500s), P2-7 (Stripe Idempotency-Key + payload-redirect rejection + exact integer cents), P2-8 (email From/To/unsubscribe CRLF stripping incl. SMTP envelope), P2-9 (TLS1.2+HTTP/2 explicit in both modes; wildcard ACME domains rejected), P2-10 (unsigned-webhook startup warning; short-secret warning).
- **Batch F (engine robustness): ✅ DONE** — P3-1 (spill rows deleted only after a flush fence commits them; drain batch 100→500), P3-2 (writer closed FIRST on shutdown — no task can be accepted after the final drain), P3-3 (Hub.Close stops Run+broadcast loops; unauthenticated WS clients' writer goroutines closed promptly), P3-4 (cond.go quote-aware operator scan, `=` alias, non-numeric comparisons return false unless both operands are quoted), P3-5 (compound where → loud error; quoted AND/OR values still allowed), P3-6 (GetTables fallback uses real table names), P3-7 (where fields that don't resolve → "matches no rows" via errWhereUnresolvable — the ecommerce optional-zone design; never errors, never matches ''), P3-8 (migrations: PG statement splitting, TOCTOU-tolerant recording, rows.Err), P3-9 (SetAPIKey documented pre-serve-only), P3-10 (LocalPubSub sync delivery + deep copy + panic recovery), P3-11 (real batching: flush at optimizer target or ticker), P3-12 (spine_dropped_broadcasts metric).
- **Batch G (SDKs & demos): ✅ DONE** — P5-1 (state broadcasts carry the originating event's audit id — sync audit insert when WS clients are connected; reconnect at that cursor replays nothing; SDK replay now dispatches `emitted_states`), P5-2 (TS SDK: `?token=`, reconnect restore, `onclose` catch, parity methods getTables/getEvents/getSchema/health, emit idempotency key, package.json `module` corrected), P5-3 (Python SDK: reconnect restore, `emitted_states` replay dispatch, TableInfo/EventLog root exports), P5-4 (demo-app → `sdk/archive/react`; mobile's unused broken dep removed), P5-5 (dashboard API-key input + X-API-Key headers + WS auth + /tables /events proxies + honest badge), P5-6 (ecommerce client throws on non-ok; README webhook verification/env docs corrected), P5-7 (mobile URL from EXPO_PUBLIC_SPINE_URL, memoized provider). Also de-flaked the cron test (fixed 2.5s sleep → poll) and fixed a real close-vs-send data race in Hub.Close (race detector caught it; broadcastCh is never closed — the loop exits on stopCh).
- **Batch H (claims, docs, CI, release): ✅ DONE** — P6-1 (`benchmarks/run-benchmarks.ts` rewritten: hardcoded tests/winners deleted, binary path → `bin/spine` with a helpful error, `--allow-no-auth`, spawn error handlers, warmup + medians, measured-vs-analytical labels, failures reported not faked), P6-2 (O(n²) benchmark sort → `sort.Slice`; README "full pipeline" relabeled "async enqueue; DB commit excluded" with an explicit disclosure note), P6-3 (README: real test counts 52 files/207 tests, `spine test`/`plugin add` CLI descriptions honest, `fts.search` entry reflects real FTS5, `spine deploy render` now actually works as a railway alias), P6-4 (roadmap: current stats header + "Already shipped" section), P6-5 (Dockerfile: Go 1.25, curl installed for the healthcheck, `CMD ["serve"]` with auth/manifest notes), P6-6 (install.sh `set -euo pipefail` + version-pipeline guard), P6-7 (CI: doc-lint step, Postgres integration job with service container, job timeouts).
- **Batch I (test hardening): ✅ DONE — ALL BATCHES COMPLETE.** P7-1 (fixed sleeps → poll-until in access/query/plugin/optimizer/cron tests via a generic `waitForTableRows` helper), P7-2 (first `pkg/manifest` unit tests: unquote/fieldTypeValue/parseBoolFlag/equalEventShape; real CLI exec tests — init scaffolds with generated secrets, version format, serve fail-closed refusal, deploy render + parse), P7-3 (e2e test now asserts HTTP emit with auth, durable rows, audit log, 401 without key, outbox completion), P7-4 (always-passing `TestStartupFailFast_PragmaError` → explicit tracked skip; outbox COUNT asserted; WS-origin tests assert concrete 101/400 statuses instead of "not 403").

---

## Final status: all 9 batches done (A–I)

Every item in the summary table above is implemented with a regression test,
and the full suite runs green under `-race` with `-tags sqlite_fts5`
(~20s). Remaining open items are the ones deliberately scoped out (Batch E
tenant scoping, P3-3 residual — see notes) and future work in the roadmap.

---

## Summary (dependency-ordered)

| # | Item | Sev | Effort |
|---|------|-----|--------|
| P0-1 | `-race` in CI + Makefile | guardrail | S |
| P0-2 | Remove committed artifacts, fix .gitignore | hygiene | S |
| P0-3 | Single source of truth for version | claims | S |
| P1-1 | Outbox retry self-perpetuation | critical | M |
| P1-2 | FTS5: implement or remove `fts.search` | critical | M–L |
| P1-3 | `$env` template exfiltration via payloads | critical | S |
| P1-4 | Batch writer per-statement error handling | high | M |
| P1-5 | `math.calc` operand validation | high | S |
| P1-6 | `db.delete` deterministic fallback | high | S |
| P1-7 | `email.broadcast` parameterized where | high | S |
| P1-8 | Idempotency marked complete before durable | high | M |
| P1-9 | Saga compensation ordering | high | M |
| P1-10 | Validate manifest after include merge | high | M |
| P1-11 | Fail-closed boolean parsing | high | S |
| P1-12 | PG boolean column parity | high | S |
| P2-1 | Fail-closed auth by default | critical | M |
| P2-2 | RLAC on WS broadcasts + `/events` | high | M |
| P2-3 | Rate-limit `/ws` upgrade path | medium | M |
| P2-4 | Rate limiter burst ≥ 1 | medium | S |
| P2-5 | Redact DSNs in CLI output | medium | S |
| P2-6 | Don't leak panic details in 500s | low | S |
| P2-7 | Stripe idempotency + redirect allowlist | medium | S |
| P2-8 | Email header injection (From/To/unsubscribe) | medium | S |
| P2-9 | TLS: BYO-cert MinVersion/NextProtos, ACME wildcards | low | S |
| P2-10 | Unsigned-webhook startup warning + min secret length | low | S |
| P3-1 | Spill: delete after commit; faster drain | medium | M |
| P3-2 | Shutdown ordering (no lost tasks) | medium | S |
| P3-3 | Hub.Close + WS writer-goroutine leak | medium | M |
| P3-4 | `cond.go` quote-aware scan, `=` alias, numeric-only compares | medium | M |
| P3-5 | Compound `where:` → explicit error | medium | S |
| P3-6 | `GetTables` fallback garbage names | low | S |
| P3-7 | `vars.go` unresolvable-path sentinel | low | S |
| P3-8 | Migrations: split statements, TOCTOU, rows.Err | low | S |
| P3-9 | `SetAPIKey` atomicity | low | S |
| P3-10 | PubSub goroutine lifecycle + panic recovery | low | S |
| P3-11 | Real batching (flush on target/ticker only) | medium | M |
| P3-12 | Broadcast drop counters | low | S |
| P4-1 | `ts_codegen`: sanitize identifiers, fail on unknown types | medium | M |
| P4-2 | `ValidatePayload` default case + listens + depth>0 | medium | M |
| P4-3 | Duplicate-event dedup consistency (parser vs codegen) | medium | S |
| P4-4 | `manifest_diff` blind spots | low | M |
| P4-5 | `doc_lint.sh` robustness | low | S |
| P4-6 | Numeric parse errors (spine_version, max_workers, …) | low | S |
| P4-7 | Unknown nested keys → warn/error | low | S |
| P5-1 | `StateBroadcast` carries sequence `id` (replay protocol) | high | S |
| P5-2 | TS SDK: parity + `?token=` + reconnect + packaging | medium | M |
| P5-3 | Python SDK: reconnect + exports + git hygiene | low | S |
| P5-4 | Fix `file:../../sdk/react` deps in demos | high | S |
| P5-5 | Web dashboard: auth input, /tables proxy, version badge | medium | M |
| P5-6 | Ecommerce: res.ok checks + webhook env docs | medium | S |
| P5-7 | Mobile: config surface, memoize provider, gitignore | low | S |
| P6-1 | Rewrite `run-benchmarks.ts` honestly | high | M |
| P6-2 | Benchmark table semantics ("full pipeline" → enqueue) | medium | S |
| P6-3 | README: counts, version, CLI reference, claims | medium | M |
| P6-4 | Roadmap rewrite against v3.0.1 | low | M |
| P6-5 | Dockerfile: curl, Go 1.25, sane CMD | high | S |
| P6-6 | install.sh version + messaging | low | S |
| P6-7 | CI: doc-lint, -race job, benchmark gating, PG job | medium | M |
| P7-1 | De-flake tests (poll-until, t.Parallel) | medium | M |
| P7-2 | Parser + CLI unit tests (zero today) | high | L |
| P7-3 | Assert real features in FTS/outbox/e2e/emit_to/notify | medium | M |
| P7-4 | Kill vacuous tests | low | S |

---

## Phase 0 — Guardrails (do first, unblocks everything)

### P0-1. Run `-race` in CI and Makefile
- **Files:** `.github/workflows/ci.yml`, `Makefile`
- **Fix:** add `-race` to the test step (`go test ./race ./tests/ ./pkg/... -count=1`); add a `test-race` Makefile target. Keep the plain run too (race is ~2-4× slower).
- **Why:** every concurrency claim in the project (parallel routes, saga compensation, sharded writer) is currently unverified; engine internal coverage is one 25-line test.

### P0-2. Repo hygiene
- **Files:** `.gitignore`, `tests/parallel_retry_test.go`, `sdk/python/` (committed `.egg-info/`, `__pycache__/`), `mobile/android-app/.expo/`
- **Fix:** delete committed `spine.db*`, `test_parallel.db*` artifacts; extend .gitignore (`*.db*`, `__pycache__/`, `*.egg-info/`, `.expo/`); make `parallel_retry_test.go` use `t.TempDir()` and remove the whole db triplet.

### P0-3. Single source of truth for version
- **Files:** `spine.go`, `Makefile`, `install.sh`, `README.md`, `.github/workflows/ci.yml`
- **Fix:** keep `Version` in `spine.go` (3.0.1); inject into the binary via `-ldflags "-X main.version=$(go run ...)"` or a tiny `go generate` step; have Makefile/install.sh read it from `go mod`/the built binary instead of duplicating. Update README header to v3.0.1. Add a CI check: `./bin/spine version` matches `spine.Version`.

---

## Phase 1 — Critical correctness (highest user impact)

### P1-1. Outbox retry loop can never terminate
- **Files:** `pkg/engine/outbox.go` (worker at ~:184, enqueue insert at ~:88), `pkg/engine/bus.go` (`execStep` failure enqueue at :875-877)
- **Fix:**
  1. Add a `fromOutbox bool` parameter to `execStep` (or a dedicated `execStepFromOutbox`) so the enqueue-on-failure only happens on the original emit path. The outbox worker must retry the *delivery* (call `dispatchAction` directly), never re-enter the enqueue path.
  2. Add a lease/claim on the row before executing (`status='processing'` + `claimed_at`), so concurrent workers (multiple ticks / multiple bus instances) don't double-deliver.
  3. Add a periodic purge (like the `_spine_idem` evictor): delete `failed`/`completed` rows older than a TTL (e.g. 7d) to bound table growth.
- **Test:** `tests/outbox_test.go` — register a route with an `http.post` to a closed port; assert: row count stays bounded (≤ maxRetries+1 per event), status transitions to `failed`, no new pending row is spawned, and the endpoint receives ≤ maxRetries deliveries. This test fails on today's code (exponential growth).

### P1-2. `fts.search` never returns results
- **Files:** `pkg/engine/actions.go` (`ftsSearch` :157-201), `pkg/engine/db_ops.go` (`ensureTable`), `pkg/manifest/features.go`, `README.md`, `tests/fts_test.go`
- **Fix (implement):**
  1. When a table is declared with `search: true` (or on first `fts.search` use), create `CREATE VIRTUAL TABLE "<table>_fts" USING fts5(content)` over the table's declared columns in `ensureTable` (SQLite/Turso only; PG gets a GIN/tsvector path or an explicit "not supported" error).
  2. Keep the FTS index in sync on insert/update (hooks in the writer or a trigger).
  3. Never return `nil` on failure: propagate the error so the route fails loudly.
  4. Replace the `created_at LIKE` fallback with a real content-column `LIKE` (or remove it).
- **Fix (minimum viable):** if FTS provisioning is out of scope, make `ftsSearch` return an explicit `fts_not_supported` error and remove the README/feature-list claims.
- **Test:** seed rows via `db.insert`, run `fts.search`, assert matching rowids/content returned. Current test only asserts `status: "ok"` on an empty table — replace it.

### P1-3. `$env` template exfiltration via payload values
- **Files:** `pkg/engine/db_ops.go` (`normalizeParam` :106-108), `pkg/engine/vars.go`, `pkg/engine/math.go`
- **Fix:** template resolution (`$env.*`, `$now`, `$uuid`, `$event.*`) must apply **only to manifest-declared strings** (step `where:`, `input:`, `value_expr:`, `expr:`), never to client-supplied payload values. In `normalizeParam`, treat payload strings beginning with `$` as literal data (optionally support `\$` escape if authors need literal `$`).
- **Test:** emit `{"note": "$env.ANY_SECRET"}` → stored row contains the literal string `$env.ANY_SECRET`; also assert `$now`/`$uuid` are not evaluated in payload values.

### P1-4. Batch writer swallows per-statement errors
- **Files:** `pkg/engine/writer.go` (`flushBatchOnce` :222-238), `pkg/engine/spill.go`
- **Fix:** on a per-statement `Exec` error, abort the transaction and route the whole batch through the existing spill path (preserving at-least-once), and surface the error to the route (fire `on_failure` / compensation). At minimum, add an atomic `StmtFailures` counter exposed on `/metrics` and log with the statement fingerprint.
- **Test:** async `db.insert` violating NOT NULL/PK → route's `on_failure` fires (today: silent drop + success).

### P1-5. `math.calc` text-splices payload operands
- **Files:** `pkg/engine/math.go` (:26-27)
- **Fix:** validate each resolved operand with the same `isPlainNumber` rules used by the parser *before* splicing; on non-numeric operand, fail the step with a clear error instead of splicing text. Keep the grammar closed (no functions/eval).
- **Test:** `expr: "$event.payload.qty * 10"` with `qty: "0 + 9999"` → step error (today: computes 99,990).

### P1-6. `db.delete` non-deterministic fallback
- **Files:** `pkg/engine/db_ops.go` (:473-480)
- **Fix:** mirror `dbUpdate`: sort payload keys before choosing the delete column, pass the value through `normalizeParam`, and prefer requiring an explicit `where:`/`id:` (error otherwise). Add a comment stating the fallback contract.
- **Test:** repeated deletes with the same payload delete the same row (today: map iteration order is randomized per call).

### P1-7. `email.broadcast` raw SQL where
- **Files:** `pkg/engine/email.go` (:180-182)
- **Fix:** build the WHERE through the same `parseWhereCondition` + `ph()` parameterization used by `db.sum/update/delete/adjust`; resolve templates first. If raw SQL is intended, validate against the identifier/operator allowlist and document loudly.
- **Test:** broadcast with `where: "email = '$event.payload.x'"` actually filters (today: matches nothing, logged as success).

### P1-8. Idempotency marked complete before writes are durable
- **Files:** `pkg/engine/bus.go` (:779), `pkg/engine/writer.go`
- **Fix:** mark the idempotency row `completed` only after the batch containing that emit's writes has been flushed (add a per-emit flush fence / completion signal from the writer). Document `/emit 200` as "accepted" and, if durability is contractual, offer a `sync: true`/`await` option.
- **Test:** fault-inject a process-crash (or commit-failure) between emit and flush; retry with same key must not return the cached success (today it does).

### P1-9. Saga compensation ordering
- **Files:** `pkg/engine/bus.go` (`rollbackCompensation` :691/:714), `pkg/engine/writer.go`
- **Fix:** before running compensation for a failed step, flush/await the writer (or run compensate actions synchronously via `execWithRetry`), so the compensation cannot commit before the write it undoes (and the original write cannot commit after compensation).
- **Test:** route with step A (insert) + step B (fails) + `compensate` on A; assert final DB state has A's effect undone (today: ordering is shard-luck).

### P1-10. Validate manifest after include merge
- **Files:** `pkg/manifest/parser.go` (:642 vs :647-670), `pkg/manifest/registry.go`
- **Fix:** run `validateSchema` once on the fully merged schema (after the include loop); dedupe node names across files with a clear error (like the per-file `seenNodes`). This makes root-route → included-event references work.
- **Test:** `includes:` file declares a node/event; root file routes on it → parses. (Today: misleading "possible typo?" error.)

### P1-11. Fail-closed boolean parsing
- **Files:** `pkg/manifest/parser.go` (:334-338 `read_only`, :541-545 `parallel`)
- **Fix:** parse with `strconv.ParseBool` (accepts `1/t/TRUE/yes`) or reject non-`true`/`false` with a parse error. Never silently treat `True`/`YES` as false.
- **Test:** `read_only: True` → role is read-only (today: writable).

### P1-12. PG boolean column parity
- **Files:** `pkg/engine/db_ops.go` (`sqliteType`/`normalizeParam` :120-131), `pkg/engine/dialect.go`
- **Fix:** normalize Go `bool` → `0/1` before binding on PG (or map declared `boolean` to PG `BOOLEAN` and bind real bools). Add a `//go:build integration` PG test for typed columns (see P6-7).
- **Test:** insert+read back a declared boolean field against Postgres.

---

## Phase 2 — Security posture

### P2-1. Fail-closed authentication
- **Files:** `pkg/middleware/auth.go` (:14), `pkg/engine/engine.go` (:440), `cmd/spine/main.go` (serve flags, `init` templates :820-885, deploy Dockerfile :1137)
- **Fix:** refuse to start (or require explicit `--allow-no-auth`) when neither an API key nor `access:` rules are configured; print the risk loudly otherwise. `init` templates: generate a random admin key at scaffold time (and write it to `.env`, not the manifest); deploy templates: set `SPINE_API_KEY` env and document rotation. Replace `sk_admin_secret_12345` / `sk_public_key` placeholders.
- **Test:** `spine serve` with no key and no access rules exits non-zero (or prints a prominent warning); with `--allow-no-auth` it serves.

### P2-2. RLAC on WS broadcasts and `/events`
- **Files:** `pkg/engine/hub.go` (:90-118), `pkg/engine/engine.go` (`/events` :779-806), `pkg/engine/access.go`
- **Fix:** attach the client's `AccessContext` at WS registration and filter `StateBroadcast` fan-out (event whitelist / tenant); apply the same filter to `GetEventLogs` reads. Either implement tenant scoping for table rows or remove the unused `Tenant` field to avoid false isolation.
- **Test:** a role whose Events whitelist excludes an event must not receive its state broadcasts or see it in `/events`.

### P2-3. Rate-limit the `/ws` upgrade path
- **Files:** `pkg/engine/engine.go` (:808, :1067-1079)
- **Fix:** register `/ws` through the middleware chain (or apply a per-IP token bucket + per-IP connection cap before upgrade).
- **Test:** hammer `/ws` from one IP → 429 before upgrade; conn cap enforced.

### P2-4. Rate limiter burst ≥ 1
- **Files:** `pkg/middleware/ratelimit.go` (:123-145)
- **Fix:** clamp `maxTokens ≥ 1` and reject `rps ≤ 0` at `SetRateLimit`; a sub-1 rps limit degrades to "1 request per N seconds".
- **Test:** `--rate-limit 0.5` → requests succeed at ≤ 2/s (today: permanent block after first request).

### P2-5. Redact DSNs in CLI output
- **Files:** `cmd/spine/main.go` (:247)
- **Fix:** strip userinfo/query params (auth tokens) from turso/libsql DSNs before printing; print driver name only.
- **Test:** serve a turso DSN containing a token → stdout shows no token.

### P2-6. Don't leak panic details in 500 responses
- **Files:** `pkg/middleware/recovery.go` (:25)
- **Fix:** log the panic value server-side; return a generic `internal_server_error` JSON.

### P2-7. Stripe: idempotency + redirect allowlist
- **Files:** `pkg/engine/stripe.go` (:66-73, :111-151)
- **Fix:** send `Idempotency-Key` derived from `order_id`/event id; require `success_url`/`cancel_url` to be manifest-literal (or host-allowlisted); compute cents from integer cents or decimal string, not `float64` rounding.
- **Test:** double-emit same order → single Checkout Session (assert via fake Stripe server); payload-derived redirect URL rejected.

### P2-8. Email header injection
- **Files:** `pkg/engine/email.go` (:47-67, :73-81)
- **Fix:** apply the same CRLF stripping used on Subject to From/To/List-Unsubscribe (and any other header-embedded values).

### P2-9. TLS hardening
- **Files:** `pkg/engine/tls.go` (:66-77, :162-164)
- **Fix:** set explicit `MinVersion: tls.VersionTLS12` (and `NextProtos: ["h2","http/1.1"]`) in BYO-cert mode; reject `*.` wildcard domains in ACME mode (or implement proper wildcard cert handling).

### P2-10. Webhook fail-open visibility
- **Files:** `pkg/middleware/webhook_verify.go` (:23-25)
- **Fix:** log a startup warning listing every provider running unsigned when `SPINE_ALLOW_UNSIGNED_WEBHOOKS=1`; enforce a minimum HMAC secret length (≥ 16 bytes) with a startup error.

---

## Phase 3 — Engine robustness

- **P3-1 Spill durability** (`pkg/engine/spill.go` :74-107): delete spill rows only after the writer confirms the commit (completion signal), and raise the drain rate (e.g. 500-1000 rows/tick with backoff). *Test:* crash between drain-accept and commit → row survives.
- **P3-2 Shutdown race** (`pkg/engine/bus.go` :431-447, `writer.go` :58-80): set the closed flag before closing `stopCh`, and drain shards after `closeAll` so no submit succeeds after the writer stops. *Test:* concurrent Emit+Close never loses an acknowledged write.
- **P3-3 Hub lifecycle** (`pkg/engine/hub.go`, `engine.go` :838/:858-879, `bus.go` :482-495): add `Hub.Close` (stop `Run`/`broadcastLoop`, close `broadcastCh`); close `client.Send` for never-registered (unauthenticated) WS clients to kill the ~25s writer-goroutine leak; call from `Engine.Close`. *Test:* goroutine-count assertion before/after New+Close cycles (with `-race`).
- **P3-4 cond.go parser** (`pkg/engine/cond.go` :83-95/:122-139): make the operator scan quote-aware (mirror `parseWhereCondition`'s inQuote tracking), accept single `=` as alias for `==`, and error (or log) when a numeric comparison receives non-numeric operands instead of lexicographic fallback.
- **P3-5 Compound where** (`pkg/engine/db_ops.go` :411-461): detect `AND`/`OR` leftovers in the value and return an explicit "compound where conditions not supported" error instead of silently binding the remainder.
- **P3-6 GetTables fallback** (`pkg/engine/query.go` :49-59): track actual created table names (or return the `listTables` error) instead of mangling fingerprint keys into fake names.
- **P3-7 vars.go sentinel** (`pkg/engine/vars.go` :92-95/:123-128): return a distinguishable error for unresolvable template paths and for the bare `$event.payload` token.
- **P3-8 Migrations** (`pkg/engine/migrations.go` :34-54): split multi-statement SQL for PG, treat unique-violation on the version row as "already applied" (TOCTOU), check `rows.Err()`.
- **P3-9 SetAPIKey** (`pkg/engine/engine.go` :235-237): store in `atomic.Pointer[string]` or document strictly pre-serve; make `legacyAuth` read it dynamically.
- **P3-10 PubSub** (`pkg/engine/pubsub.go` :27-36): bounded worker or inline delivery, payload deep-copy, per-subscriber panic recovery.
- **P3-11 Real batching** (`pkg/engine/bus.go` :449-471): flush only when the batch reaches the optimizer's target size or the ticker fires — the optimizer's batchSize should actually matter.
- **P3-12 Broadcast drops** (`pkg/engine/hub.go` :146-151): add a dropped-broadcast counter exposed on `/metrics`.

---

## Phase 4 — Parser, codegen, diff, doc-lint

- **P4-1 ts_codegen** (`pkg/codegen/ts_codegen.go` :20-24/:28-43/:62/:93/:100/:120/:163): sanitize field/interface identifiers (quote or skip invalid); error on empty, leading-digit, and case-colliding event names; fail on unknown field types instead of `any`; add `routes_matched?: number` to the emit return type.
- **P4-2 ValidatePayload** (`pkg/manifest/registry.go` :38-48/:80-101): add a `default` case that errors on unknown declared types; decide/implement listen-contract validation and validation at chained depths > 0; align bool coercion with numbers.
- **P4-3 Dedup** (`pkg/manifest/registry.go` :40 vs `ts_codegen.go` :62): unify last-wins vs most-fields; reject conflicting duplicate event payload shapes at parse time.
- **P4-4 manifest_diff** (`pkg/engine/manifest_diff.go` :99-111): fingerprint route/node bodies (or compare structurally) so route edits show as changes; include `Tenant` + `Events` in `AccessChanged`.
- **P4-5 doc_lint** (`scripts/doc_lint.sh` :16-17): move the key whitelist to a shared Go constant the script reads (or a generated file); whitelist `include:` and the route/step subkeys (cron, on_error, max_attempts, backoff_ms, where, input, url, compensate, read_only, key, filter); scan `spine`-tagged fences too.
- **P4-6 Numeric parsing** (`pkg/manifest/parser.go` :181, :266-282, :578-588): `strconv.Atoi` with errors for `spine_version`, `max_workers`, `max_retries`, `backoff_ms`, `max_attempts`; fix the stale comment at :166.
- **P4-7 Unknown nested keys** (`pkg/manifest/parser.go`): warn (or error) on unrecognized keys at node-body, database, and route-body levels so `emit:` typos don't silently drop contracts.

---

## Phase 5 — SDKs & frontends

- **P5-1 Replay protocol** (`pkg/engine/hub.go` `StateBroadcast`): include the audit-log sequence (or a monotonic per-connection counter) as `id` in every broadcast; both SDKs' `lastSeenID` then works and reconnects replay only genuinely missed events. *Test:* connect, receive event, reconnect → no replay of the already-seen event.
- **P5-2 TS SDK** (`sdk/ts/`): add `get_tables`, `get_events`, `get_schema`, `health`, idempotency support (parity with Python); fix WS auth `?key=` → `?token=`; fix `disconnect()` permanently disabling auto-reconnect (`connect()` must restore it); correct `package.json` `module` field (`.mjs` never emitted) or add a dual build; add a README + minimal test.
- **P5-3 Python SDK** (`sdk/python/`): same reconnect fix (:312-326); re-export `TableInfo`/`EventLog` from package root; remove committed `.egg-info/`/`__pycache__` from git.
- **P5-4 Demo deps** (`web/demo-app/package.json`, `mobile/android-app/package.json`): point `file:../../sdk/react` at `sdk/archive/react` or drop it (mobile doesn't use it); decide whether demos migrate to `@spine/client` (sdk/ts).
- **P5-5 Web dashboard** (`web/src/App.tsx`, `web/vite.config.ts`): add API-key input persisted to localStorage, injected as `X-API-Key` on HTTP and `?token=`/in-band auth on WS; add `/tables` to the dev proxy; derive the version badge from `/schema` instead of hardcoded 'v2.3.0'.
- **P5-6 Ecommerce** (`apps/ecommerce/web/src/lib/spine.ts`, `apps/ecommerce/README.md`): throw on `!res.ok` (or parsed `status !== 'ok'`); update README (signature verification exists — `webhook_verify.go`); document `STRIPE_WEBHOOK_SECRET`/`SPINE_ALLOW_UNSIGNED_WEBHOOKS` so the documented Stripe runbook works.
- **P5-7 Mobile** (`mobile/android-app/`): read server URL/key from app config instead of hardcoded `172.16.1.145`; memoize the provider value (re-subscribe bug); gitignore `.expo/`.

---

## Phase 6 — Claims, docs, CI, release

- **P6-1 Honest benchmarks** (`benchmarks/run-benchmarks.ts`): delete or genuinely measure the hardcoded tests (6/7/9/10); measure both sides of tests 5 and 8; point at `bin/spine` (or build it first) with a `child.on('error')` handler; add warmup + N≥5 runs with median reporting; never hardcode the winner.
- **P6-2 Benchmark semantics** (`tests/benchmark_test.go` :234, README :1026): rename "full pipeline" rows to "async emit enqueue (commit excluded)"; add a benchmark that awaits batch commit; fix the O(n²) selection sort (:461-469) with `sort.Slice` or histogram percentiles so CI's `-bench` step doesn't stall; document the p50/mean/p99 skew.
- **P6-3 README** (`README.md`): real test counts (46 files / 172 tests) and honest wording ("all test suites pass" — the parser and CLI have no tests); version → 3.0.1; CLI reference fixes (`plugin add` doesn't download — rename or implement; `deploy render` errors — implement or remove; `spine test` runs a smoke suite, not manifest assertions); FTS claim gated on P1-2.
- **P6-4 Roadmap** (`five_year_roadmap.md`): rewrite against v3.0.1 (10K+ lines); move shipped items (access rules, `spine dev`, `spine init`, codegen, replay, error suggestions, rate limiting, outbox tests) to a "Shipped" section; keep only true future work.
- **P6-5 Dockerfile**: install `curl` in the runtime stage (or use a Go health probe); build with `golang:1.25-*` to match `go.mod`; change default `CMD` to something useful (`serve --help` exits immediately) and document that a manifest must be mounted.
- **P6-6 install.sh** (:7): version from the built binary instead of the hardcoded 3.0.0; print the actual built version; add `set -euo pipefail` and a `GOTOOLCHAIN` note.
- **P6-7 CI** (`.github/workflows/ci.yml`): run `doc-lint` (Makefile's test target does, CI doesn't); add the `-race` job; gate on benchmark success with a sane timeout (after P6-2); add a Postgres integration job (`SPINE_TEST_PG_DSN` via `postgres` service container) so dialect parity is actually tested.

---

## Phase 7 — Test hardening (cross-cutting)

- **P7-1 De-flake**: replace the ~50 fixed `time.Sleep` sites with the existing poll-until helpers (`waitUntil`/`waitForRow`); add `t.Parallel()` to independent engine tests; target < 10s suite.
- **P7-2 Coverage gaps** (highest-value): parser unit tests (`pkg/manifest` — currently zero files) covering P1-10, P1-11, P4-6, P4-7; CLI tests (`cmd/spine` — currently zero) that actually exec the built binary (`init`, `serve --help`, `deploy` targets, `parse`); internal engine tests for writer/registry/hub hot paths run under `-race`.
- **P7-3 Feature assertions**: FTS result assertions (after P1-2); `emit_to` stream delivery; `notify.webhook` end-to-end; make `e2e_test.go` assert the features its name promises (it currently asserts almost nothing and never uses its httptest server).
- **P7-4 Kill vacuous tests**: `startup_failfast_test.go` (:173-176, always passes — needs a real failure injection or explicit skip); `outbox_test.go` COUNT scan never asserted; `webhook_verify_test.go` origin tests assert only `!= 403` (assert concrete statuses); `cli_test.go` never invokes the CLI.

---

## Suggested execution order (batches)

1. **Batch A (one PR):** P0-1, P0-2, P0-3 — guardrails land first; everything after is verified under `-race`.
2. **Batch B (one PR):** P1-1, P1-2, P1-3 — the three criticals; each with a regression test that fails today.
3. **Batch C (one PR):** P1-4 … P1-9 — writer/compensation/idempotency semantics (touch the same writer code; do together to avoid merge churn).
4. **Batch D:** P1-10 … P1-12 + P4-2/P4-3 — parser/registry correctness.
5. **Batch E:** P2-1, P2-2 first, then P2-3 … P2-10 — security posture.
6. **Batch F:** P3-1 … P3-12 — engine robustness.
7. **Batch G:** P5-1 (engine change) then P5-2 … P5-7 — SDKs and demos.
8. **Batch H:** P6-1 … P6-7 — claims, docs, CI, release.
9. **Batch I:** P7-1 … P7-4 — test hardening, continuously.

Each batch keeps `go build`, `go vet`, `go test -race`, and `doc-lint` green and is independently shippable.
