#!/bin/bash
# Sign a macOS binary with a stable code-signing identity so macOS TCC keeps
# permission grants (Documents/Desktop/Downloads folders, Full Disk Access)
# across rebuilds. `go build` output is linker-signed ad-hoc with a fresh
# CDHash every time, and macOS treats each rebuild as a brand-new app, so
# folder-permission prompts re-appear after every build.
#
# Usage: codesign-darwin.sh <binary-path> [identifier]
# - Skips silently on non-macOS or when codesign is unavailable.
# - Identity resolution: $QUARTET_SIGN_IDENTITY, else the first valid
#   codesigning identity in the keychain; none found -> warn and leave the
#   binary as-is (a missing identity must never break the build).
set -u

bin="$1"
identifier="${2:-com.fanlv.quartet-web}"

if [ "$(uname -s)" != "Darwin" ] || ! command -v codesign >/dev/null 2>&1; then
  exit 0
fi

identity="${QUARTET_SIGN_IDENTITY:-}"
if [ -z "$identity" ]; then
  identity=$(security find-identity -v -p codesigning 2>/dev/null | awk '/^[[:space:]]*[0-9]+\)/ {print $2}' | head -n 1)
fi
if [ -z "$identity" ]; then
  echo "warning: no codesigning identity found; $bin stays ad-hoc signed and macOS will re-ask folder permissions after each rebuild" >&2
  exit 0
fi

if codesign --force --sign "$identity" --identifier "$identifier" "$bin"; then
  echo "✅ Signed $(basename "$bin") (identity: $identity, identifier: $identifier)"
else
  echo "warning: codesign failed for $bin; leaving it ad-hoc signed" >&2
fi
exit 0
