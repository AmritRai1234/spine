#!/usr/bin/env bash
# Deploy the Spine e-commerce app to a remote host over SSH.
#
#   DEPLOY_HOST=user@vps.example.com ./deploy/deploy.sh [port]
#
# What it does:
#   1. builds the spine binary (go build)
#   2. builds the storefront (npm ci + vite build, production env → same-origin)
#   3. ships binary + app.spine + web/dist + .env to /opt/spine-ecommerce
#   4. installs + starts the spine-ecommerce systemd unit
#
# Prereqs (remote): ssh + rsync access, systemd, port open. The .env that is
# shipped is the local apps/ecommerce/.env with the dev-only SPINE_DB and
# SPINE_PORT stripped (the server uses ./spine.db and the port in the unit /
# env — set SPINE_PORT on the server if 8080 is taken).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HOST="${DEPLOY_HOST:-${1:-}}"
PORT="${2:-8080}"
APP_DIR="/opt/spine-ecommerce"

if [ -z "$HOST" ]; then
  echo "usage: DEPLOY_HOST=user@host ./deploy/deploy.sh [port]" >&2
  exit 1
fi

echo "→ [1/5] building spine binary…"
(cd "$ROOT/../.." && go build -o /tmp/spine-deploy-bin ./cmd/spine)

echo "→ [2/5] building storefront (production env)…"
(cd "$ROOT/web" && npm ci --silent && npm run build)

echo "→ [3/5] staging bundle…"
TMP="$(mktemp -d)"
mkdir -p "$TMP/web"
cp /tmp/spine-deploy-bin "$TMP/spine"
cp "$ROOT/app.spine" "$TMP/"
cp "$ROOT/deploy/spine-ecommerce.service" "$TMP/"
cp -r "$ROOT/web/dist" "$TMP/web/"
if [ -f "$ROOT/.env" ]; then
  # Keep secrets, drop dev-only DB path + port bindings
  grep -vE '^(SPINE_DB|SPINE_PORT)=' "$ROOT/.env" > "$TMP/.env"
else
  echo "⚠  no .env found — copy one to $APP_DIR/.env on the server manually" >&2
fi

echo "→ [4/5] shipping to ${HOST}:${APP_DIR}…"
ssh "$HOST" "mkdir -p '$APP_DIR'"
rsync -az --delete -e ssh "$TMP/" "$HOST:$APP_DIR/"
rm -rf "$TMP" /tmp/spine-deploy-bin

echo "→ [5/5] installing systemd unit on port ${PORT}…"
ssh "$HOST" "sed 's|ExecStart=.*|ExecStart=/opt/spine-ecommerce/spine serve app.spine --port ${PORT}|' '$APP_DIR/spine-ecommerce.service' > /etc/systemd/system/spine-ecommerce.service"
ssh "$HOST" "systemctl daemon-reload && systemctl enable --now spine-ecommerce && systemctl restart spine-ecommerce"

echo "✓ deployed — service status:"
ssh "$HOST" "systemctl --no-pager status spine-ecommerce --lines=3 | head -8; echo; echo '→ http://$HOST:${PORT}'"
