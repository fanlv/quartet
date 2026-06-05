package probe

import (
	"os"
	"regexp"
)

// npxENOTEMPTYDestRe matches the `dest` path from an `npm error ENOTEMPTY:
// directory not empty, rename ... -> <dest>` line. npm leaves these
// dotfile temp dirs (e.g. `.codex-acp-linux-x64-rAcBvLrs`) under
// _npx/<hash>/node_modules/<scope>/ when a previous install crashed
// mid-rename; on the next run npm tries to rename the real package to
// the same temp path and gets ENOTEMPTY because the path already exists.
//
// We extract the dest path and rm -rf it so the next refresh cycle can
// install fresh.
var npxENOTEMPTYDestRe = regexp.MustCompile(`ENOTEMPTY:[^\n]*-> '([^']+)'`)

// tryHealNpxENOTEMPTY scans an error text for the npm ENOTEMPTY rename
// signature and removes the dest path it points at. Returns the number
// of stale temp dirs removed (0 if the error did not match, 0 if the
// dest is missing or already a non-dotfile real package directory we
// must not touch).
//
// Conservative: only removes paths whose final segment starts with `.`
// — those are npm's atomic-rename holding directories. Real installed
// packages don't have a leading dot in their name.
func tryHealNpxENOTEMPTY(errText string) int {
	matches := npxENOTEMPTYDestRe.FindAllStringSubmatch(errText, -1)
	if len(matches) == 0 {
		return 0
	}
	removed := 0
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		dest := m[1]
		if !looksLikeNpxStaleTemp(dest) {
			continue
		}
		if err := os.RemoveAll(dest); err == nil {
			removed++
		}
	}
	return removed
}

// looksLikeNpxStaleTemp gates os.RemoveAll on a path before we trust it.
// Two conditions must both hold:
//
//  1. The path must live under .../_npx/<hash>/node_modules/... — that's
//     the cache root npx populates per-invocation; nothing else of value
//     lives there for us to delete.
//  2. The final basename must start with `.`. npm's atomic-rename temp
//     paths (e.g. `.codex-acp-linux-x64-rAcBvLrs`) are dotfiles by
//     convention; real package directories never are.
//
// Either condition alone would be too lax — together they're tight
// enough that an unexpected match still cannot reach into a real
// installed package or anywhere outside the npx cache.
func looksLikeNpxStaleTemp(path string) bool {
	if path == "" || path == "/" {
		return false
	}
	if !regexp.MustCompile(`/_npx/[^/]+/node_modules/`).MatchString(path) {
		return false
	}
	// Last path segment.
	base := path
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '/' {
			base = base[i+1:]
			break
		}
	}
	if base == "" || base[0] != '.' {
		return false
	}
	return true
}
