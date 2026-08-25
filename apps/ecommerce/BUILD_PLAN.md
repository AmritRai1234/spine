# Spine Commerce — Master Build Plan

> Working plan for `apps/ecommerce/`. All 24 known gaps sequenced by dependency.
> Each phase ends with: build ✓ · live smoke test ✓ · commit checkpoint.
>
> Status legend: `[ ]` todo · `[~]` in progress · `[x]` done

---

## Phase 1 — Trust the checkout (engine + manifest)

**Fixes:** #1 stock never decrements · #2 client-trusted prices · #3 no restore on cancel

Two small engine additions in `pkg/engine/actions.go`, following existing action patterns (~60 lines each):

- [x] **`db.lookup`** — merge a found row into the event payload
  - Config: `table`, `key_column`, `value_expr` (supports `$event.payload.x`), optional `as:` prefix
  - Missing row → step fails → existing `on_failure` machinery handles it
- [x] **`db.adjust`** — atomic relative update: `SET col = col ± ?`
  - Config: `table`, `where` (parameterized), `column`, `by` (integer expression, may be negative)
  - Parameterized delta — no interpolation into SQL text

Manifest changes (`app.spine`):

- [x] `PLACE_ORDER` route chains: lookup product row → `if:` guard rejects price mismatch → insert order → adjust stock per unit
- [x] Admin cancel path: per-line restore via `db.adjust (+qty)` driven from admin UI (trusted role)
- [x] Admin Orders page: "Cancel & restock" action on non-cancelled orders

**Gate:** selling phantom inventory impossible · tampered price rejected server-side · cancel restores stock.

---

## Phase 2 — Storefront feels like a store

**Adds:** #5 product detail page · #6 search · #7 category filters · #11 shopper order tracking

- [x] Product detail view (full image, description, qty picker, add-to-cart)
- [x] Search bar + category chips filtering the catalog
  - Client-side first; optional engine enhancement if wanted later: FTS read param on `GET /tables/{name}`
- [x] "My Orders" page: email-keyed, subscribes to `ORDER_STATUS_CHANGED` for live status
- [x] Checkout confirmation links to order tracking

**Gate:** browse → detail → buy → track, entirely without the admin panel.

---

## Phase 3 — Money & identity

**Adds:** #9 addresses · #10 coupons · #8 payments (Stripe) · #4 accounts (honest scope)

- [x] Address block on checkout → new order columns via schema evolution
      (`ship_name`, `address1`, `city`, `country`, `zip`)
- [x] Coupons: `coupons` table (code upsert, percent/fixed) + `APPLY_COUPON` route with condition guards; cart totals respect discount; admin can create codes
- [x] Stripe integration (webhook-first):
      - `http.post` to Stripe API with `$env.STRIPE_SECRET`
      - Existing engine webhook ingestion (`POST /webhook/stripe`) flips orders to `paid`
      - Durable outbox retries cover transient failures — already built into the engine
      - Test-mode runbook only; no live keys in repo
- [x] Accounts boundary (documented decision): engine auth = static API keys. Ship email-keyed
      identity ("My Orders") now; real login requires per-user key issuance (engine feature)
      or external IdP — documented in README, not faked.

**Gate:** address captured with order · coupon math correct · webhook flips status end-to-end in test mode.

---

## Phase 4 — Admin depth

**Adds:** #13 pagination · #14 CSV export · #15 customer drill-down · #16 staff role + attribution · #17 settings · #18 notifications · (#12 uploads deferred)

- [x] Server-side pagination on admin tables (orders server-paged; customers/items via limit/offset walk) (engine `limit&offset` — wire pagers, stop slurping 500 rows)
- [x] CSV export buttons (orders, customers) via client-side Blob download
- [x] Customer drill-down page: click a customer → their orders + items
- [x] Third RLAC role **`staff`**: fulfilment events only (`UPDATE_ORDER_STATUS`) — cannot publish products
- [x] `actor` field convention in admin-emitted payloads → visible attribution in Event Log
- [x] Settings page backed by `store_settings` KV table: currency symbol, low-stock threshold,
      store name (consumed by Dashboard/Products)
- [x] New-order notification (`notify.webhook` no-ops when unset): `http.post` step to `$env.ALERT_WEBHOOK_URL` on `ORDER_CREATED`
      (outbox retry covers downtime)

**Deferred:** #12 image uploads — needs object storage (S3/R2). Keep URL fields; document pattern.

**Gate:** staff key can ship orders but cannot touch the catalog · pagers hit the API, not memory.

---

## Phase 5 — Platform hardening

**Adds:** #19 app tests · #20 docs/deploy · #21 bundle diet · #22 SEO meta · #23 cleanup · #24 .env loading

- [x] **`tests/ecommerce_test.go`** (Go): parse manifest against temp DB and pin every contract +
      Phase 1 integrity rules (stock math, price-guard rejection, cancel-restock). CI fails loudly
      if anyone breaks a route
- [x] `apps/ecommerce/README.md`: quickstart, env var table, TLS runbook (`--domain`),
      `spine deploy` config, coupon/staff/webhook recipes
- [x] Code-split admin pages (`React.lazy`) so recharts leaves the storefront bundle;
      `loading="lazy"` on product images
- [x] SEO meta tags (title/description/og) in `index.html`
- [x] Cleanup: removed dead `void allItemsOk` in Checkout.tsx; extract shared `useStoreMetrics`
      hook (Dashboard + AdminLayout duplicate revenue logic today)
- [x] `spine dev` auto-loads `.env` from the manifest directory (tiny CLI nicety; kills the
      manual `ADMIN_SECRET=… export` dance)

**Gate:** green CI including app tests · storefront bundle lean · folder self-documenting.

---

## Phase 6 — Server-calculated shipping & tax (CEO roadmap epic 1)

**Adds:** shipping zones + tax rules, server-side totals at PLACE_ORDER, checkout estimate,
admin Shipping & Tax page. Dominant value: Safety — the client never supplies dollar amounts.

- [x] Engine: **`math.calc`** action — safe arithmetic evaluator over payload fields
      (`+ - * /`, parens; digits only, injection-proof) in `pkg/engine/math.go`
- [x] Engine: **`db.insert` `sync: true`** flag — synchronous insert so PLACE_ORDER's
      `db.sum` never races the line insert (batched writer is async by default)
- [x] Manifest: `shipping_zones` + `tax_rules` tables; `SAVE_SHIPPING_ZONE` /
      `SAVE_TAX_RULE` admin routes (upsert by country, `*` = catch-all)
- [x] Manifest: `ADD_ORDER_ITEM` computes `line_total = price × qty` server-side;
      `PLACE_ORDER` computes `subtotal` (db.sum over persisted lines), `shipping_cost`,
      `tax_amount`, `coupon_discount` (recomputed, not client-claimed) and `total` —
      all with `math.calc`; empty-cart orders rejected (assert)
- [x] Storefront: checkout shows live shipping/tax estimate per country; confirmed view
      and My Orders display the server's authoritative breakdown
- [x] Admin: **Shipping & Tax** page (zones + tax rules CRUD) · AdminOrders + revenue
      math use server totals (discount is dollars, not percent)
- [x] Seed: US/CA/GB zones + tax rules
- [x] Tests: `math_calc_test.go` (precedence/parens/injection/div-zero) + ecommerce pins
      (server totals to the cent, unconfigured country → 0/0, client total claims ignored)

**Gate:** totals to the cent server-side · tampered client totals impossible · checkout
flow re-ordered (items → order) so subtotal is never empty · full suite + tsc + vite build green.

---

## Reference — original gap list

| # | Gap | Phase |
|---|---|---|
| 1 | Stock never decrements at checkout | 1 |
| 2 | Price is client-trusted | 1 |
| 3 | No inventory restore on cancel/refund | 1 |
| 4 | Customer accounts / order history | 3 (scoped) |
| 5 | Product detail page | 2 |
| 6 | Search (`fts.search` unwired) | 2 |
| 7 | Category filtering on storefront | 2 |
| 8 | Payments (Stripe via webhook + outbox) | 3 |
| 9 | Shipping address / tax lines | 3 |
| 10 | Discount codes | 3 |
| 11 | Order status invisible to shopper | 2 |
| 12 | Image uploads | deferred (needs object storage) |
| 13 | Server-side pagination | 4 |
| 14 | CSV export | 4 |
| 15 | Customer drill-down | 4 |
| 16 | Staff role + audit attribution | 4 |
| 17 | Settings page | 4 |
| 18 | Email/webhook notifications | 4 |
| 19 | Automated tests pinning app contracts | 5 |
| 20 | App README + deploy/TLS runbook | 5 |
| 21 | Code splitting / lazy images | 5 |
| 22 | SEO meta tags | 5 |
| 23 | Dead code cleanup | 5 |
| 24 | `.env` auto-loading for dev | 5 |

## Current servers (dev session)

| Service | URL | Notes |
|---|---|---|
| Storefront | http://localhost:5173 | Vite HMR |
| Backend | http://localhost:8090 | `spine dev` hot-reload watcher |

Keys: admin `$ADMIN_SECRET` (env) · shopper `sk_shopper_key` · staff (Phase 4)
