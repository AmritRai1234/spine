#!/usr/bin/env bash
# Seeds the Spine e-commerce catalog with sample products.
# Usage:  ADMIN_SECRET=sk_admin_secret_change_me ./seed.sh [server]
set -euo pipefail

SERVER="${1:-http://localhost:8080}"
ADMIN_KEY="${ADMIN_SECRET:?Set ADMIN_SECRET to the admin key (see .env.example)}"

# sku|name|price|stock|category|description|image_url
products=(
  'spine-tee|Spine Logo Tee|24.50|50|apparel|Soft cotton tee with the Spine logo across the chest. Runs true to size.|https://placehold.co/400x400/10b981/white?text=Tee'
  'spine-hoodie|Spine Hoodie|59.00|30|apparel|Heavyweight fleece hoodie with kangaroo pocket and embroidered spine mark.|https://placehold.co/400x400/6366f1/white?text=Hoodie'
  'spine-mug|Spine Mug|14.99|120|home|14oz ceramic mug, dishwasher safe. Keeps your emit loop warm.|https://placehold.co/400x400/f59e0b/white?text=Mug'
  'spine-stickers|Spine Sticker Pack|6.00|500|swag|Vinyl sticker pack — laptops, servers, and everything in between.|https://placehold.co/400x400/ec4899/white?text=Stickers'
  'spine-cap|Spine Cap|19.00|8|apparel|Six-panel cap with curved brim and woven spine patch.|https://placehold.co/400x400/0ea5e9/white?text=Cap'
  'spine-zip-hoodie|Spine Zip Hoodie|64.00|25|apparel|Full-zip fleece with brushed interior and metal zipper pulls.|https://placehold.co/400x400/8b5cf6/white?text=Zip+Hoodie'
)

for row in "${products[@]}"; do
  IFS='|' read -r sku name price stock category desc img <<<"$row"
  curl -sf -X POST "$SERVER/emit" \
    -H "Content-Type: application/json" \
    -H "X-API-Key: $ADMIN_KEY" \
    -d "{\"event\":\"PUBLISH_PRODUCT\",\"payload\":{\"sku\":\"$sku\",\"name\":\"$name\",\"price\":$price,\"stock\":$stock,\"category\":\"$category\",\"description\":\"$desc\",\"image_url\":\"$img\"}}" \
    >/dev/null && echo "✓ published $name ($sku)"
done

echo "Catalog seeded."

# Sample discount codes (active, percent off)
coupons=(
  'SAVE10|10'
  'WELCOME15|15'
)
for row in "${coupons[@]}"; do
  IFS='|' read -r code percent <<<"$row"
  curl -sf -X POST "$SERVER/emit" \
    -H "Content-Type: application/json" \
    -H "X-API-Key: $ADMIN_KEY" \
    -d "{\"event\":\"CREATE_COUPON\",\"payload\":{\"code\":\"$code\",\"percent_off\":$percent,\"fixed_off\":0,\"active\":\"true\"}}" \
    >/dev/null && echo "✓ coupon $code (${percent}% off)"
done

echo "Coupons seeded."

# Product variants — Spine Tee in sizes (per-SKU stock counters).
# The product upsert is ASYNC (batch writer), so retry the id lookup until
# the row lands; the `|| true` keeps the pipeline from aborting under
# `set -euo pipefail` (run.sh runs the seed inside a strict shell).
tee_id=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
  tee_id=$(curl -s "$SERVER/tables/products?where=sku:spine-tee" -H "X-API-Key: $ADMIN_KEY" \
    | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4 || true)
  [ -n "$tee_id" ] && break
  sleep 0.3
done
if [ -n "$tee_id" ]; then
  for row in 'S|12' 'M|18' 'L|10' 'XL|5'; do
    IFS='|' read -r size qty <<<"$row"
    curl -sf -X POST "$SERVER/emit" \
      -H "Content-Type: application/json" \
      -H "X-API-Key: $ADMIN_KEY" \
      -d "{\"event\":\"PUBLISH_VARIANT\",\"payload\":{\"sku\":\"spine-tee-$size\",\"product_id\":\"$tee_id\",\"option1_name\":\"Size\",\"option1_value\":\"$size\",\"price\":24.5,\"stock\":$qty}}" \
      >/dev/null && echo "✓ variant spine-tee-$size (Size $size, $qty in stock)"
  done
  echo "Variants seeded."
else
  echo "⚠ spine-tee not found — variants skipped (seed the catalog first)"
fi

# Shipping zones (flat rate per order) + tax rules (% of subtotal).
# A country with NO zone/rule falls back to free shipping / 0% tax
# (documented default — the Admin → Shipping & Tax page shows the gaps).
zones=(
  'US|5.99'
  'CA|12.99'
  'GB|9.99'
)
for row in "${zones[@]}"; do
  IFS='|' read -r country rate <<<"$row"
  curl -sf -X POST "$SERVER/emit" \
    -H "Content-Type: application/json" \
    -H "X-API-Key: $ADMIN_KEY" \
    -d "{\"event\":\"SAVE_SHIPPING_ZONE\",\"payload\":{\"country\":\"$country\",\"rate\":$rate}}" \
    >/dev/null && echo "✓ shipping zone $country (\$$rate)"
done

taxes=(
  'US|8.25'
  'CA|5.0'
  'GB|20.0'
)
for row in "${taxes[@]}"; do
  IFS='|' read -r country rate <<<"$row"
  curl -sf -X POST "$SERVER/emit" \
    -H "Content-Type: application/json" \
    -H "X-API-Key: $ADMIN_KEY" \
    -d "{\"event\":\"SAVE_TAX_RULE\",\"payload\":{\"country\":\"$country\",\"rate\":$rate}}" \
    >/dev/null && echo "✓ tax rule $country (${rate}%)"
done

echo "Shipping zones + tax rules seeded."
