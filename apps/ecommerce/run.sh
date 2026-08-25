#!/usr/bin/env bash
# One-command local run of the Spine e-commerce template.
#
#   ./run.sh
#
# Builds the spine binary (if needed), installs frontend deps (first run),
# starts the backend (hot-reload, .env loaded), seeds the store when the
# catalog is empty, starts the storefront, and prints the URLs. Ctrl+C stops
# everything. Overrides: SPINE_PORT (backend, default 8090), VITE_PORT
# (frontend, default 5173).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"          # apps/ecommerce
REPO="$(cd "$ROOT/../.." && pwd)"               # spine repo root
SPINE_PORT="${SPINE_PORT:-8090}"
VITE_PORT="${VITE_PORT:-5173}"
BIN="/tmp/spine-bin"
DB="${SPINE_DB:-$ROOT/spine_dev.db}"

echo "→ [1/4] building spine binary…"
(cd "$REPO" && go build -o "$BIN" ./cmd/spine)

echo "→ [2/4] frontend deps (first run only)…"
[ -d "$ROOT/web/node_modules" ] || (cd "$ROOT/web" && npm ci --silent)

echo "→ [3/4] starting backend on :${SPINE_PORT} (hot-reload)…"
SPINE_WS_ORIGINS="${SPINE_WS_ORIGINS:-http://localhost:${VITE_PORT},http://127.0.0.1:${VITE_PORT}}" \
  "$BIN" dev "$ROOT/app.spine" --port "$SPINE_PORT" --db "$DB" &
BACK_PID=$!

for _ in $(seq 1 60); do
  curl -sf "http://localhost:${SPINE_PORT}/health" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "http://localhost:${SPINE_PORT}/health" >/dev/null || {
  echo "✗ backend failed to start — see the log above" >&2
  kill "$BACK_PID" 2>/dev/null
  exit 1
}

# Seed only when the catalog is empty (seed is idempotent, but skip the noise).
# Resolve the URL BEFORE sourcing .env — the file's SPINE_PORT=8080 would
# otherwise clobber the override.
SEED_SERVER="http://localhost:${SPINE_PORT}"
ADMIN_KEY="$(grep -E '^ADMIN_SECRET=' "$ROOT/.env" 2>/dev/null | head -1 | cut -d= -f2)"
if [ -n "$ADMIN_KEY" ]; then
  COUNT="$(curl -s "$SEED_SERVER/tables/products" -H "X-API-Key: $ADMIN_KEY" | grep -oE '"count":[0-9]+' | head -1 | cut -d: -f2)"
  if [ "${COUNT:-1}" = "0" ]; then
    echo "→ seeding demo catalog…"
    (cd "$ROOT" && set -a && . ./.env && set +a && ./seed.sh "$SEED_SERVER") \
      || echo "⚠ demo seed had a hiccup — the store will still run (re-run ./seed.sh later)"
  fi
fi

echo "→ [4/4] starting storefront on :${VITE_PORT}…"
SK="$(grep -A2 'role: shopper' "$ROOT/app.spine" | grep 'key:' | sed 's/.*key: "\(.*\)"/\1/' | tr -d '"')"
(cd "$ROOT/web" && SPINE_URL="http://localhost:${SPINE_PORT}" SPINE_API_KEY="$SK" npm run dev -- --port "$VITE_PORT") &
FRONT_PID=$!

trap 'echo; echo "⏹ stopping…"; kill "$BACK_PID" "$FRONT_PID" 2>/dev/null' EXIT

echo
echo "✓ Storefront: http://localhost:${VITE_PORT}"
echo "✓ Admin:      http://localhost:${VITE_PORT}/#/admin  (key: ADMIN_SECRET in apps/ecommerce/.env)"
echo "  Ctrl+C stops everything."
wait
