// Package strutil collects small, self-contained string-processing helpers
// shared across the codebase. The current contents are rune-aware truncation
// helpers used by display layers (job/session titles, log previews); future
// generic string utilities (sanitisation, normalisation, masking) belong
// here too rather than being reinvented in each caller's file. Anything
// platform- or domain-specific (e.g. messaging filename rules, IM hex/base64
// codecs) should live in its own dedicated package instead.
package strutil

// TruncateRunes truncates s to at most maxRunes runes.
func TruncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// TruncateRunesWithEllipsis truncates s to at most maxRunes runes,
// appending "..." if the string was truncated.
func TruncateRunesWithEllipsis(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
