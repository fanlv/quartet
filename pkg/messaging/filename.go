package messaging

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeFileNamePart keeps Unicode letters/digits plus `._-` and replaces
// everything else with '_'. Leading / trailing punctuation is trimmed so the
// result is safe to embed as one segment in a generated filename.
func SanitizeFileNamePart(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "._-")
}

// CapFileNameBytes truncates name to at most maxBytes bytes while preserving a
// short extension (≤16 bytes) when present and never splitting a UTF-8 rune.
func CapFileNameBytes(name string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(name) <= maxBytes {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > 16 || len(ext) >= maxBytes {
		// Unusually long "extension" — probably part of the stem (e.g. no
		// real ext). Drop it and truncate the whole name.
		ext = ""
	}
	budget := maxBytes - len(ext)
	stem := name[:len(name)-len(ext)]
	cut := 0
	for i, r := range stem {
		size := utf8.RuneLen(r)
		if size < 0 {
			size = 1
		}
		if i+size > budget {
			break
		}
		cut = i + size
	}
	return stem[:cut] + ext
}
