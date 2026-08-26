# Spine Commerce

A complete, event-driven e-commerce template for the [Spine](../../README.md) engine.
One manifest (`app.spine`), one React storefront — deployable anywhere Spine runs.

```
app.spine            # Manifest: tables, roles, contracts, routes (the whole backend)
seed.sh              # Sample catalog + coupons + shipping zones + tax rules
.env.example         # Environment template
web/                 # Vite + React storefront & admin panel
tests/               # (repo-level) tests/ecommerce_test.go pins app contracts in CI
```

## Quickstart — one command

    ./run.sh

That's it: builds the engine, installs frontend deps (first run), starts the
backend on :8090 with hot-reload, seeds the demo catalog (products, variants,
coupons, shipping zones, tax rules) when the store is empty, starts the
storefront on :5173, and prints the URLs. Ctrl+C stops everything.
Overrides: `SPINE_PORT`, `VITE_PORT`, `SPINE_DB` (e.g.
`SPINE_PORT=9000 VITE_PORT=3000 ./run.sh`).

- Storefront: http://localhost:5173
- Admin panel: http://localhost:5173/#/admin (key = ADMIN_SECRET in .env)

For a remote server instead: `DEPLOY_HOST=user@vps ./deploy/deploy.sh` (see
Deployment below).

## Walkthrough

```bash
cd apps/ecommerce
cp .env.example .env            # then edit ADMIN_SECRET
spine dev app.spine             # auto-loads .env · hot-reload · default :8080

cd web && npm install
SPINE_API_KEY=sk_shopper_key npm run dev   # storefront on :5173
```

Seed the store (from `apps/ecommerce/`):

```bash
ADMIN_SECRET=<your admin key> ./seed.sh http://localhost:8080
```

Open http://localhost:5173 → browse → detail → cart → checkout → **My Orders** tracking,
all without the admin panel. The admin panel (header → Admin) unlocks with the admin key.

## Code map — every page is wired to the manifest

The whole backend is one file: `app.spine`. Each frontend file is owned by exactly
one manifest **node** (`owns_files`), so you can start from any page and trace it to
its events, routes and tables:

| Page / component | Node | Events it emits | States it listens to |
|---|---|---|---|
| `Catalog.tsx`, `ProductDetail.tsx` | Catalog | PUBLISH_PRODUCT, RETIRE_PRODUCT, PUBLISH_VARIANT | PRODUCT_PUBLISHED, STOCK_ADJUSTED, VARIANT_PUBLISHED |
| `CartDrawer.tsx`, `lib/cart.ts` | Storefront | ADD_TO_CART, UPDATE_CART_ITEM, REMOVE_FROM_CART | CART_UPDATED |
| `Checkout.tsx` | Checkout | PLACE_ORDER, ADD_ORDER_ITEM, VALIDATE_COUPON | ORDER_CREATED, ORDER_STATUS_CHANGED, COUPON_VALIDATED, COUPON_REJECTED |
| `MyOrders.tsx` | Tracking | — | ORDER_STATUS_CHANGED |
| `admin/*`, `Admin.tsx`, `lib/admin.ts`, `lib/store.ts` | Operations | UPDATE_ORDER_STATUS, RESTOCK_ORDER_ITEM, CREATE_COUPON, SAVE_SETTING, SAVE_SHIPPING_ZONE, SAVE_TAX_RULE | ORDER_STATUS_CHANGED, STOCK_ADJUSTED |
| (webhook) | Payments | WEBHOOK_STRIPE | ORDER_STATUS_CHANGED |

Rule of thumb: **change a page → update its node's contract first.** Adding a button
that emits a new event? Declare the event on the owning node, add the route, then
touch the page. Shared runtime (`lib/spine.ts`, `hooks/*`, `components/ui/*`,
`types.ts`) is engine infrastructure, not a domain node.

## Deployment (SSH)

One command deploys the whole stack to any Linux VPS with ssh + rsync access:

    DEPLOY_HOST=user@vps.example.com ./deploy/deploy.sh [port]

What happens: the spine binary is built, the storefront is built with the
production env (`.env.production` → same-origin client: the spine server serves
the SPA **and** the API on one port), everything is shipped to
`/opt/spine-ecommerce` (binary, `app.spine`, `web/dist`, `.env`), and a hardened
systemd unit is installed and started. The `.env` that ships has the dev-only
`SPINE_DB`/`SPINE_PORT` stripped — the server keeps its database at
`/opt/spine-ecommerce/spine.db` and binds the port from the unit (default 8080).

Security: access rules with empty keys refuse to start (fail-closed — an
unset `ADMIN_SECRET` is a hard error, not an open admin). TLS: run the unit with
`--domain your.domain` to get automatic Let's Encrypt certificates.

Prereqs: Go ≥ 1.22 + Node ≥ 20 on the build machine; remote needs systemd and an
open port. For a dry run against a local dir: stage everything exactly as the
script does (`web/dist` next to the binary + `app.spine` + env exported) and run
`spine serve app.spine`.

## Roles

| Role  | Key source       | Can do |
|-------|------------------|--------|
| admin | `$ADMIN_SECRET`  | everything: catalog, orders, customers, settings, coupons |
| staff | `$STAFF_SECRET`  | fulfilment only: order status updates + cancel-restock |
| shopper | static demo key `sk_shopper_key` | cart, checkout, coupon validation |

RLAC is enforced server-side per event whitelist; the admin panel detects staff keys at
unlock and hides management tabs. Staff keys cannot publish products or alter settings.

## Trust model (what the engine enforces)

- **Prices are server-checked.** Every `ADD_ORDER_ITEM` looks up the catalog row and
  rejects mismatches via an assert guard → `CHECKOUT_FAILED`. Tampered prices never persist.
- **Line totals are server-computed.** Each line's `line_total` is `price × qty` calculated
  by the engine (`math.calc`), never supplied by the client.
- **Order totals are server-calculated (Phase 6).** At `PLACE_ORDER` the engine sums the
  persisted line totals (`db.sum`), applies the country's shipping zone (flat rate) and tax
  rule (percentage) from `shipping_zones` / `tax_rules`, and recomputes the coupon discount
  from the coupons table — `subtotal`, `shipping_cost`, `tax_amount`, `coupon_discount` and
  `total` are all engine-derived dollars. Client-supplied amounts are ignored (the storefront
  only displays an estimate; the confirmed order shows the server's numbers).
- **Stock is atomic.** Checkout decrements through `db.adjust` with a floor of zero;
  overselling is impossible.
- **Coupons are re-verified.** `PLACE_ORDER` re-reads the coupons table and computes the
  discount server-side — claimed discounts cannot be tampered with.
- **Cancel restores stock.** Admin "Cancel & restock" replays each line through
  `RESTOCK_ORDER_ITEM` (+qty) before flipping status.

### Shipping & tax config

Admin → **Shipping & Tax** manages `shipping_zones` (flat rate per country) and `tax_rules`
(percentage of subtotal per country). A country with no rule gets free shipping / 0% tax —
use `*` as a catch-all. `seed.sh` ships US / CA / GB rows. Totals are always recomputed
server-side at `PLACE_ORDER`, so checkout never trusts a client-computed dollar figure.

## Payments (Stripe, test mode)

The engine ingests Stripe webhooks natively:

```
POST /webhook/stripe        → event WEBHOOK_STRIPE
```

Route logic flips matching orders to `paid` when `type == checkout.session.completed`
using `client_reference_id` as the order id.

Test-mode runbook:

1. Create a Checkout Session (API or dashboard) with
   `client_reference_id = <order id>` and success URL pointing back to your storefront.
2. Configure the Stripe webhook endpoint to `https://<host>/webhook/stripe`
   (test mode: use the Stripe CLI: `stripe listen --forward-to localhost:8080/webhook/stripe`).
3. Complete a test payment → the order flips to `paid` on the shopper's screen live.

> Webhook signatures ARE verified: the engine checks the `Stripe-Signature` HMAC
> (300s replay window) against `STRIPE_WEBHOOK_SECRET` (or
> `SPINE_WEBHOOK_SECRET_STRIPE`). In test mode with the Stripe CLI, export the
> CLI's signing secret: `export STRIPE_WEBHOOK_SECRET=$(stripe listen --print-secret)`
> before starting the engine — otherwise the engine returns
> `webhook_provider_not_configured` (503). No live keys belong in this repo.

## Notifications

Set `ALERT_WEBHOOK_URL` and every new order POSTs the payload there. Delivery is backed
by the engine's durable outbox (retries with backoff across restarts). Unset = disabled.

## Deploy

```bash
spine serve app.spine --port 8080 --db spine.db
```

TLS in one flag (Let's Encrypt / autocert):

```bash
spine serve app.spine --domain shop.example.com
```

`spine deploy fly` scaffolds a Fly.io deployment. Put the built `web/dist` behind any
static host or a reverse proxy that forwards `/emit`, `/tables`, `/events`, `/ws`,
`/webhook/*` to the engine.

## Environment

| Variable           | Used by          | Purpose |
|--------------------|------------------|---------|
| `ADMIN_SECRET`     | engine           | admin API key (required) |
| `STAFF_SECRET`     | engine           | staff API key (optional) |
| `ALERT_WEBHOOK_URL`| engine routes    | new-order notification target (optional) |
| `STRIPE_SECRET`    | external tooling | test-mode API calls (never commit) |
| `SPINE_PORT`/`SPINE_DB` | `spine dev` | bind port / sqlite path |

`spine dev` auto-loads `.env` from the manifest directory without overwriting real env vars.

## Accounts boundary (honest scope)

Engine auth is static API keys. Shopper identity here is email-keyed ("My Orders"):
enter your email, see your orders, watch statuses live. Real per-customer login needs
either per-user key issuance (engine feature) or an external IdP — deliberately not faked.

## Deferred

Image uploads need object storage (S3/R2). Products keep `image_url`; point them at any
CDN today.
