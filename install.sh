#!/usr/bin/env bash
set -euo pipefail

# Spine One-Line Universal Installer Script
# Usage: curl -fsSL https://raw.githubusercontent.com/AmritRai1234/spine/main/install.sh | bash

INSTALL_DIR="/usr/local/bin"

# Fallback to user local bin if /usr/local/bin is not writable
if [ ! -w "$INSTALL_DIR" ]; then
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
fi

echo "========================================================"
echo "      SPINE — Declarative Backend Engine Installer      "
echo "========================================================"
echo ""

# Detect OS and Architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
    x86_64|amd64)
        ARCH="amd64"
        ;;
    arm64|aarch64)
        ARCH="arm64"
        ;;
    *)
        echo "Error: Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

echo "--> Detected OS: $OS ($ARCH)"

# Check if Go is installed for source build
if [ -f "./cmd/spine/main.go" ]; then
    echo "--> Building Spine binary from local source..."
    go build -tags sqlite_fts5 -ldflags="-s -w" -o spine ./cmd/spine/
    mv spine "$INSTALL_DIR/spine"
elif command -v go >/dev/null 2>&1; then
    echo "--> Building Spine binary from remote source using Go..."
    TEMP_DIR="$(mktemp -d)"
    trap 'rm -rf "$TEMP_DIR"' EXIT

    git clone --depth 1 https://github.com/AmritRai1234/spine.git "$TEMP_DIR/spine" >/dev/null 2>&1 || true
    if [ -d "$TEMP_DIR/spine" ]; then
        cd "$TEMP_DIR/spine"
        go build -tags sqlite_fts5 -ldflags="-s -w" -o spine ./cmd/spine/
        mv spine "$INSTALL_DIR/spine"
    else
        echo "--> Go found, building from package remote..."
        GOBIN="$INSTALL_DIR" go install github.com/AmritRai1234/spine/cmd/spine@latest
    fi
else
    echo "Error: Go compiler is required to build Spine from source."
    echo "Please install Go (https://go.dev/doc/install) or use Docker:"
    echo "  docker run -p 8080:8080 -v \$(pwd):/app ghcr.io/amritrai1234/spine:latest"
    exit 1
fi

chmod +x "$INSTALL_DIR/spine"

# Report the version the built binary actually reports (never a hardcoded string).
SPINE_VERSION="$("$INSTALL_DIR/spine" version 2>/dev/null | sed -n 's/^spine v\([0-9][0-9.]*\).*/\1/p' || true)"

echo ""
echo " Successfully installed Spine v${SPINE_VERSION:-unknown} to $INSTALL_DIR/spine"
echo ""
echo "Verify installation:"
echo "  spine version"
echo ""
echo "Start Spine server:"
echo "  spine serve app.spine --port 8080"
echo ""
