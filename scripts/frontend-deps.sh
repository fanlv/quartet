#!/bin/bash
# Install or sync frontend dependencies with useful diagnostics.
set -u

WEB_DIR="${1:-web}"
MODE="${2:-auto}"

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

cd "$WEB_DIR" || fail "Cannot enter frontend directory: $WEB_DIR"

install_needed=0
reason=""
case "$MODE" in
    auto) ;;
    force)
        install_needed=1
        reason="forced"
        ;;
    *)
        fail "Unknown dependency mode: $MODE"
        ;;
esac

if [ "$install_needed" = "0" ]; then
    if [ ! -d node_modules ] || [ ! -x node_modules/.bin/vite ]; then
        install_needed=1
        reason="node_modules missing or vite is not installed"
    elif [ package.json -nt node_modules ] || { [ -f package-lock.json ] && [ package-lock.json -nt node_modules ]; }; then
        install_needed=1
        reason="package.json or package-lock.json is newer than node_modules"
    fi
fi

if [ "$install_needed" = "0" ]; then
    echo "OK: Frontend dependencies are up to date"
    exit 0
fi

echo "Installing frontend dependencies ($reason)..."
echo "   node:     $(node -v 2>/dev/null || echo unavailable)"
echo "   npm:      $(npm -v 2>/dev/null || echo unavailable)"
echo "   registry: $(npm config get registry 2>/dev/null || echo unavailable)"
echo "   cache:    $(npm config get cache 2>/dev/null || echo unavailable)"
echo "   If this step appears stuck, run: cd web && npm ci --foreground-scripts --loglevel verbose"

if [ -f package-lock.json ]; then
    npm ci --foreground-scripts || {
        echo "WARN: npm ci failed; retrying with npm install..." >&2
        npm install --foreground-scripts || exit 1
    }
else
    npm install --foreground-scripts || exit 1
fi

touch node_modules
echo "OK: Frontend dependencies installed"
