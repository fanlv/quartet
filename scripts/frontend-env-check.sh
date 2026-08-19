#!/bin/bash
# Validate the frontend runtime before npm starts doing expensive work.
set -u

WEB_DIR="${1:-web}"

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

cd "$WEB_DIR" || fail "Cannot enter frontend directory: $WEB_DIR"

NODE_BIN="$(command -v node 2>/dev/null || true)"
NPM_BIN="$(command -v npm 2>/dev/null || true)"

[ -n "$NODE_BIN" ] || fail "Node.js is not installed or not on PATH"
[ -n "$NPM_BIN" ] || fail "npm is not installed or not on PATH"
[ -f package.json ] || fail "package.json not found in $PWD"

NODE_VERSION="$(node -p 'process.versions.node' 2>/dev/null || true)"
NPM_VERSION="$(npm -v 2>/dev/null || true)"

[ -n "$NODE_VERSION" ] || fail "Cannot read Node.js version from $NODE_BIN"
[ -n "$NPM_VERSION" ] || fail "Cannot read npm version from $NPM_BIN"

echo "OK: Frontend runtime: node v$NODE_VERSION ($NODE_BIN), npm $NPM_VERSION ($NPM_BIN)"
