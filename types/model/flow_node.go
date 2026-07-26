package model

import (
	"regexp"
	"strings"
)

// CopyPath returns a fresh copy of a loop node path slice. Paths may be nil
// (root-level nodes), and copying keeps JSON round-trips from aliasing the
// same backing array between a job and its persisted snapshot.
func CopyPath(p []int) []int {
	if p == nil {
		return nil
	}
	cp := make([]int, len(p))
	copy(cp, p)
	return cp
}

// Template variables like {{title}} or {{ vars.x }} are resolved by the
// handler layer when rendering user-visible content.
var templateVarRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)

// IsTemplateVar reports whether s contains a {{var}} placeholder.
func IsTemplateVar(s string) bool {
	return strings.Contains(s, "{{") && templateVarRe.MatchString(s)
}
