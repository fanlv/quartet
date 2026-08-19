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

node - "$NODE_VERSION" "$NPM_VERSION" "$NODE_BIN" "$NPM_BIN" <<'NODE'
const fs = require('fs');

const [nodeVersion, npmVersion, nodeBin, npmBin] = process.argv.slice(2);
const pkg = JSON.parse(fs.readFileSync('package.json', 'utf8'));

function parseVersion(version) {
  const match = String(version).trim().replace(/^v/, '').match(/^(\d+)(?:\.(\d+))?(?:\.(\d+))?/);
  if (!match) return null;
  return [
    Number(match[1]),
    Number(match[2] || 0),
    Number(match[3] || 0),
  ];
}

function compareVersions(left, right) {
  for (let i = 0; i < 3; i += 1) {
    if (left[i] < right[i]) return -1;
    if (left[i] > right[i]) return 1;
  }
  return 0;
}

function satisfies(version, range) {
  const parsed = parseVersion(version);
  if (!parsed || !range) return false;
  const parts = String(range).trim().split(/\s+/).filter(Boolean);
  for (const part of parts) {
    const match = part.match(/^(>=|>|<=|<|=)?(.+)$/);
    if (!match) return false;
    const op = match[1] || '=';
    const target = parseVersion(match[2]);
    if (!target) return false;
    const cmp = compareVersions(parsed, target);
    if (op === '>=' && cmp < 0) return false;
    if (op === '>' && cmp <= 0) return false;
    if (op === '<=' && cmp > 0) return false;
    if (op === '<' && cmp >= 0) return false;
    if (op === '=' && cmp !== 0) return false;
  }
  return true;
}

const checks = [
  ['Node.js', nodeVersion, pkg.engines && pkg.engines.node, nodeBin, 'install/use Node 22.18.x (for Homebrew: brew install node@22 && export PATH="/opt/homebrew/opt/node@22/bin:$PATH")'],
  ['npm', npmVersion, pkg.engines && pkg.engines.npm, npmBin, 'install/use npm >=10.9.0 <11 (for npm: npm install -g npm@10.9.3)'],
];

let ok = true;
for (const [name, version, range, bin, fix] of checks) {
  if (!range) continue;
  if (!satisfies(version, range)) {
    ok = false;
    console.error(`ERROR: Unsupported ${name} version for web frontend`);
    console.error(`   Required: ${range}`);
    console.error(`   Current:  ${version} (${bin})`);
    console.error(`   Fix:      ${fix}`);
  }
}

if (!ok) process.exit(1);

console.log(`OK: Frontend runtime: node v${nodeVersion} (${nodeBin}), npm ${npmVersion} (${npmBin})`);
NODE
