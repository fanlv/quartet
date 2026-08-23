#!/usr/bin/env bash
# End-to-end verification for the quartet-workflow skill.
#
# PREREQUISITE: the backend must be running the FRESHLY BUILT binary (the one
# with the `type` field). Restart it first:  make web
#
# Usage:
#   quartet-cli auth login --username <user>
#   bash skill/workflow/verify-e2e.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# The CLI is `quartet-cli`; all workflow operations live under its `workflow`
# command group. qwf() invokes that group with the locally built binary.
CLI="$SCRIPT_DIR/bin/quartet-cli"
qwf() { "$CLI" workflow "$@"; }
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; exit 1; }

MINIMAL='{"nodes":[{"id":"start_1","type":"start"},{"id":"shell_1","type":"shell","config":{"script":"echo hi"}},{"id":"end_1","type":"end"}],"edges":[{"id":"e1","sourceNodeId":"start_1","targetNodeId":"shell_1","sourcePort":"default"},{"id":"e2","sourceNodeId":"shell_1","targetNodeId":"end_1","sourcePort":"default"}]}'
BAD='{"nodes":[{"id":"start_1","type":"start"},{"id":"end_1","type":"end"}],"edges":[{"id":"e1","sourceNodeId":"start_1","targetNodeId":"end_1","sourcePort":"default"}]}'

echo "[1] validate: minimal config should be valid"
echo "$MINIMAL" | qwf validate >/dev/null 2>&1 && pass "minimal valid" || fail "minimal should validate"

echo "[2] validate: pure start->end should be invalid"
echo "$BAD" | qwf validate >/dev/null 2>&1 && fail "pure start->end should be rejected" || pass "invalid config rejected"

echo "[3] create: makes a type=agent workflow"
OUT="$(echo "$MINIMAL" | qwf create --name "ZZ-e2e-$$" 2>/dev/null)"
ID="$(echo "$OUT" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")"
TYPE="$(echo "$OUT" | python3 -c "import sys,json;print(json.load(sys.stdin)['type'])")"
[ -n "$ID" ] || fail "create returned no id"
[ "$TYPE" = "agent" ] && pass "created $ID type=agent" || fail "expected type=agent, got '$TYPE'"

echo "[4] list --type agent: includes the new workflow"
qwf list --type agent 2>/dev/null | grep -q "$ID" && pass "appears in agent list" || fail "missing from agent list"

echo "[5] list --type user: must NOT include it"
qwf list --type user 2>/dev/null | grep -q "$ID" && fail "agent wf leaked into user list" || pass "not in user list"

echo "[6] update: rename succeeds on agent workflow"
qwf update "$ID" --name "ZZ-e2e-$$-renamed" >/dev/null 2>&1 && pass "update accepted" || fail "update should succeed"

echo "[7] delete: removes the agent workflow"
qwf delete "$ID" >/dev/null 2>&1 && pass "delete accepted" || fail "delete should succeed"
qwf list --type all 2>/dev/null | grep -q "$ID" && fail "still listed after delete" || pass "gone after delete"

echo
echo "ALL CHECKS PASSED"
echo
echo "NOTE: the user-library read-only guard is best checked manually:"
echo "  1) create a workflow in the Web UI (it will be type=user)"
echo "  2) run: $CLI workflow delete <that-id>   ->  must refuse with a 'user library' error"
