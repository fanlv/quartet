#!/usr/bin/env bash
# Build the quartet-cli binary into skill/workflow/bin/.
#
# Run from anywhere; paths are resolved relative to this script. Requires the
# Go toolchain and the quartet repo checkout (the CLI lives at cmd/quartet-cli and
# reuses the repo's types/model package). The skill drives `quartet-cli
# workflow <...>`.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# skill/workflow -> skill -> repo root
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
OUT="$SCRIPT_DIR/bin/quartet-cli"

mkdir -p "$SCRIPT_DIR/bin"
cd "$REPO_ROOT"
go build -o "$OUT" ./cmd/quartet-cli
echo "built $OUT"
