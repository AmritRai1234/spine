# Spine Commerce — Full Feature Assessment

Date: 2026-08-25 · Source: web research (2026 e-commerce checklists: Litextension 23-point,
Launchwork 50-point, Shopify feature map, Baymard cart-abandonment data, GDPR/PCI guides) +
audit of `apps/ecommerce/` (manifest, storefront, admin).

Status legend: `[x]` shipped · `[~]` partial · `[ ]` missing
Priority: **P0** launch-tier (missing = lost sales) · **P1** first-90-days · **P2** scale

---

## 1. Storefront — catalog & discovery

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 1.1 | Mobile-first responsive layout | [x] | Tailwind responsive; needs real-device pass (P2) |
| 1.2 | Product listing grid w/ images, price, stock | [x] | 5-col grid, stock badges |
| 1.3 | Product detail page | [x] | image gallery (thumbnails + swap), variants + live per-SKU price/stock; no zoom (P2) |
| 1.4 | **Product variants (size/color, per-SKU price+stock)** | [x] | header/detail model (Architect ruling) — PDP matrix, admin editor, server-enforced |
| 1.5 | Search (typo tolerance, autocomplete) | [~] | Client-side substring only; engine `fts.search` unwired |
| 1.6 | Category navigation / collections | [~] | Chips only; no subcategories, no collections |
| 1.7 | Sorting (price, newest, best-selling) | [ ] | P1 |
| 1.8 | Faceted filtering (price range, rating, brand) | [ ] | P1 |
| 1.9 | Breadcrumbs | [ ] | P2 (needs real routing) |
| 1.10 | Related products / cross-sell / upsell | [ ] | P1 |
| 1.11 | Recently viewed | [ ] | P2 |
| 1.12 | Wishlist / save for later | [ ] | P1 (needs identity) |
| 1.13 | Reviews & ratings (verified-buyer) | [ ] | P1 — trust lever (270% per research) |
| 1.14 | Product images: gallery, zoom, lazy | [~] | gallery + upload ✓ (2026-08-25); zoom 360° P2 |

## 2. Cart & checkout

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 2.1 | Persistent cart (cross-session) | [x] | `cart_items` keyed by browser cart_id |
| 2.2 | Cart drawer (mini-cart) | [x] | Built 2026-08-25 |
| 2.3 | Qty edit / remove / subtotal | [x] | drawer + cart page |
| 2.4 | Guest checkout (no forced account) | [x] | email-keyed only |
| 2.5 | Checkout ≤3 steps, progress visibility | [x] | form → placing → confirmed |
| 2.6 | Shipping cost visible upfront | [x] | live estimate per country (Phase 6) |
| 2.7 | Server-calculated totals (shipping/tax/discount) | [x] | `math.calc` + `db.sum` (Phase 6) |
| 2.8 | Coupons: percent | [x] | server-verified |
| 2.9 | Coupons: fixed-amount, free-shipping, usage limits | [ ] | P1 |
| 2.10 | **Real payment capture (Stripe Checkout / PayPal)** | [~] | payments ledger done: Stripe webhook + manual payments/refunds tracked with idempotency + over-refund guard; session *creation* still open |
| 2.11 | Order confirmation email | [x] | `email.send` in `PLACE_ORDER` + shipped notification; SMTP env bridge, silent no-op without `SMTP_HOST` |
| 2.12 | Failed-payment recovery | [ ] | P2 |
| 2.13 | Abandoned-cart recovery | [ ] | P1 (5–15% recovery per research) |
| 2.14 | Free-shipping progress bar | [ ] | P2 (needs threshold config) |
| 2.15 | Returns policy visible at decision points | [ ] | P1 — top-5 abandonment cause |

## 3. Identity & accounts

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 3.1 | Customer login/register | [ ] | P0 — needs per-user key issuance (engine) |
| 3.2 | Password hashing (bcrypt/argon2) | [ ] | with 3.1 |
| 3.3 | Account dashboard (order history) | [~] | email-keyed My Orders; no real account |
| 3.4 | Saved addresses | [ ] | P2 |
| 3.5 | Admin 2FA (TOTP) | [ ] | P1 — security baseline for admin |
| 3.6 | Role-based staff access | [x] | admin/staff RLAC |

## 4. Admin / back-office

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 4.1 | Product CRUD | [x] | publish/retire, upsert by SKU |
| 4.2 | **Variant management (matrix editor)** | [x] | per-product sheet: list + add (option1×option2, SKU/price/stock) |
| 4.3 | Order management (status, cancel-restock) | [x] | + server-side pagination, CSV export |
| 4.4 | Customer list + drill-down | [x] | |
| 4.5 | Analytics (revenue, AOV, chart, top products) | [x] | dashboard |
| 4.6 | Shipping zones + tax rules | [x] | Phase 6 |
| 4.7 | Coupon management | [~] | create only; no list/disable UI |
| 4.8 | Settings (store name, currency, threshold) | [x] | KV |
| 4.9 | Low-stock alerts (dashboard + notify) | [~] | dashboard only; no webhook alert |
| 4.10 | Inventory ledger / audit trail | [ ] | P2 — roadmap epic (coupled to variants) |
| 4.11 | Bulk import/export products (CSV) | [ ] | P2 |
| 4.12 | Review moderation | [ ] | with 1.13 |
| 4.13 | Event log | [x] | |

## 5. Marketing & growth

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 5.1 | Email capture (newsletter) | [x] | `subscribers` table + footer form wired to `SUBSCRIBE_EMAIL`; admin Marketing tab broadcasts via `email.broadcast` with opt-out filtering |
| 5.2 | New-order notification webhook | [x] | `ALERT_WEBHOOK_URL` + durable outbox |
| 5.3 | Discounts: flash sales, bundles, gift cards | [ ] | P2 |
| 5.4 | Loyalty/points | [ ] | P2 |
| 5.5 | Social proof widgets | [ ] | P2 |

## 6. SEO & analytics

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 6.1 | Meta title/description | [~] | static index.html only |
| 6.2 | SEO-friendly URLs | [ ] | P1 — hash routing today; needs real routing |
| 6.3 | XML sitemap + robots.txt | [ ] | P1 — cheap |
| 6.4 | Product schema (JSON-LD) | [ ] | P1 — AI/GEO visibility |
| 6.5 | Canonical tags | [ ] | P2 |
| 6.6 | Analytics (GA4 / event tracking) | [ ] | P2 — engine has /metrics |
| 6.7 | Conversion tracking (add-to-cart, purchase) | [ ] | P2 |

## 7. Security & compliance

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 7.1 | HTTPS/TLS | [x] | `--domain` autocert + HSTS |
| 7.2 | Card data never touches server | [x] | (no payment capture yet) |
| 7.3 | Admin access key-gated | [x] | URL-only + unlock key |
| 7.4 | Admin 2FA | [ ] | P1 — with 3.5 |
| 7.5 | Privacy policy / terms / returns pages | [ ] | P1 |
| 7.6 | Cookie/GDPR consent banner | [ ] | P1 |
| 7.7 | Customer data export/erase | [ ] | P2 (engine /tables helps export) |
| 7.8 | Automated backups | [ ] | P2 — `spine.db` file + docs |
| 7.9 | Rate limiting / WAF basics | [x] | engine middleware |

## 8. Performance & reliability

| # | Feature | Status | Notes |
|---|---------|--------|-------|
| 8.1 | Code splitting / lazy loading | [x] | React.lazy admin, lazy images |
| 8.2 | Core Web Vitals budget | [~] | untested; add lighthouse pass (P2) |
| 8.3 | Durable writes / no lost orders | [x] | spill table + sync inserts (2026-08-25) |
| 8.4 | Outbox retries for webhooks | [x] | |
| 8.5 | CDN-ready (image URL fields) | [x] | external URLs |

---

## Build order (one at a time, tested after each)

1. **[x] P0 — Product variants** (1.4 + 4.2): products header + `product_variants` detail (option
   matrix, per-SKU price/stock), PDP variant picker, admin matrix editor. Zero engine changes
   (Architect ruling). Shipped 2026-08-25: contract tests (price/stock enforcement, variant
   oversell, cancel-restock, backward compat) + live smoke (2×$24.50 → subtotal 49, tax 4.04,
   total 59.03; variant stock 18→16). Unblocks 1.8/2.9/4.10.
2. **P0 — Stripe Checkout** (2.10): create Checkout Session server-side (`http.post` + env
   secret), redirect to hosted payment, existing webhook flips to `paid`. Requires
   `http.post` response capture (engine) if absent — CONFIRMED ABSENT (actions.go:110-139
   discards the response body); small engine addition needed first.
3. **P0 — Customer accounts** (3.1–3.3): per-user key issuance + hashed passwords (engine
   feature), login/register UI, account dashboard. Unblocks wishlist/saved addresses.
4. **P1 — Sorting & filtering** (1.7/1.8) + wire `fts.search` (1.5).
5. **P1 — Reviews & ratings** (1.13/4.12): new table, verified-buyer check, PDP + admin
   moderation.
6. **P1 — Order confirmation emails** (2.11): SMTP bridge + manifest templates + outbox
   (CEO epic 2 — never in engine).
7. **P1 — Trust & policies** (2.15/7.5/7.6): returns/privacy/terms pages, consent banner,
   trust badges at decision points.
8. **P1 — Abandoned-cart recovery** (2.13): cart-age sweep → notify.webhook.
9. **P1 — SEO infra** (6.2–6.4): real routes, sitemap, robots, Product JSON-LD.
10. **P1 — Admin 2FA** (3.5/7.4): TOTP on unlock.
11. **P2 — Coupon depth** (2.9): fixed amount, free shipping, limits.
12. **P2 — Wishlist** (1.12) → **cross-sell** (1.10) → **inventory ledger** (4.10) → **bulk
    CSV** (4.11) → **backups** (7.8) → **GA4** (6.6).

## Sources

- Litextension — 23 must-have e-commerce features (launch / 90-day / scale tiers)
- Launchwork Digital — 50-point e-commerce checklist
- Charle Agency — Shopify feature map 2026 (15 categories)
- Baymard Institute — 70.22% cart abandonment; #1 cause = unexpected costs
- semviet / devstudioit / shivlab — 2026 feature lists (variants, reviews, wishlist, 2FA)
- GDPR/PCI guides (iubenda, usercentrics, Schellman) — consent, hashing, PCI, erasure
