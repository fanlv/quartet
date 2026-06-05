#!/usr/bin/env bash
# update-weclaw.sh — show the diff between the weclaw commit pinned in
# pkg/wechat/doc.go and upstream HEAD for the subset of files we ported
# (ilink/ and messaging/cdn.go / media.go / attachment.go).
#
# It doesn't auto-apply anything — iLink is a private Tencent protocol and
# upstream changes may be unsafe to cherry-pick. Review the diff, then update
# pkg/wechat/* by hand and bump the pinned tag in pkg/wechat/doc.go.
#
# Usage:
#   scripts/update-weclaw.sh              # diff pinned -> HEAD
#   scripts/update-weclaw.sh v0.9.0       # diff pinned -> v0.9.0
#   WECLAW_REPO=... scripts/update-weclaw.sh
#
# Requires: git, grep.

set -euo pipefail

REPO_URL="${WECLAW_REPO:-https://github.com/fastclaw-ai/weclaw.git}"
TARGET_REF="${1:-HEAD}"

# Locate repo root (the directory containing pkg/wechat/doc.go).
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DOC_FILE="$REPO_ROOT/pkg/wechat/doc.go"

if [[ ! -f "$DOC_FILE" ]]; then
  echo "error: $DOC_FILE not found; is this the quartet repo root?" >&2
  exit 1
fi

# Extract the pinned tag/sha from the doc.go attribution line, e.g.:
#   "ported from https://github.com/fastclaw-ai/weclaw at **v0.8.0**."
PINNED="$(grep -oE 'weclaw at \*\*[^*]+\*\*' "$DOC_FILE" | sed -E 's/.*\*\*([^*]+)\*\*.*/\1/' | head -n1 || true)"
if [[ -z "$PINNED" ]]; then
  echo "error: could not parse pinned weclaw ref from $DOC_FILE" >&2
  echo "       expected a line like 'ported from ... at **<tag>**.'" >&2
  exit 1
fi

echo "[update-weclaw] pinned ref:  $PINNED"
echo "[update-weclaw] compare ref: $TARGET_REF"
echo

TMPDIR="$(mktemp -d -t weclaw.XXXXXX)"
trap 'rm -rf "$TMPDIR"' EXIT

# Shallow clone including both refs.
git clone --quiet --no-checkout "$REPO_URL" "$TMPDIR" >/dev/null
cd "$TMPDIR"

# Resolve refs (tags, branches, shas all accepted).
git fetch --quiet --tags origin "$PINNED" 2>/dev/null || true
git fetch --quiet origin "$TARGET_REF" 2>/dev/null || true

if ! git rev-parse --verify --quiet "$PINNED^{commit}" >/dev/null; then
  echo "error: cannot resolve pinned ref '$PINNED' in upstream" >&2
  exit 1
fi
if ! git rev-parse --verify --quiet "$TARGET_REF^{commit}" >/dev/null; then
  echo "error: cannot resolve target ref '$TARGET_REF' in upstream" >&2
  exit 1
fi

# Limit the diff to the paths we ported. If weclaw renames things, adjust here.
PATHS=(
  'ilink/'
  'messaging/cdn.go'
  'messaging/media.go'
  'messaging/attachment.go'
)

echo "=== commits in range $PINNED..$TARGET_REF touching ported paths ==="
git log --oneline "$PINNED..$TARGET_REF" -- "${PATHS[@]}" || true
echo

echo "=== diff $PINNED..$TARGET_REF -- ${PATHS[*]} ==="
git diff --stat "$PINNED..$TARGET_REF" -- "${PATHS[@]}" || true
echo
git diff "$PINNED..$TARGET_REF" -- "${PATHS[@]}" || true
