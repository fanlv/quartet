package usagestats

import (
	"strings"
	"unicode"
)

// ResolveToolBucketKey returns the bucket key under which a tool call's
// stats should be filed. The rule is:
//
//   - If the tool name contains "shell" or "bash" (case-insensitive), this is
//     a shell-class tool: parse args as JSON, take the "command" field, take
//     its first whitespace-delimited token, strip surrounding quotes, and
//     skip leading KEY=value environment-prefix tokens. The remaining first
//     command word is the bucket key (canonicalized to upper-case — quoted
//     commands like `"my cmd"` keep their internal space before upper-casing).
//   - Otherwise, the bucket key is the first space-delimited token of the
//     tool name. Upstream (notably ACP / Claude Code) often passes a "title"
//     like `Read web/src/components/ChatPage.tsx` or `Grep /some/path` as
//     the function name; without this normalization every distinct path
//     would split into its own bucket. Tool names without spaces (e.g.
//     `file_read`, `grep_tool`) are canonicalized to upper-case.
//   - If shell parsing yields nothing usable, fall back to the same
//     first-word rule applied to the tool name.
//   - If the tool name itself is empty / whitespace-only, the key is
//     "(unnamed)".
func ResolveToolBucketKey(toolName, args string) string {
	if strings.TrimSpace(toolName) == "" {
		return unnamedToolKey
	}
	if isShellClassTool(toolName) {
		if cmd := extractShellCommand(args); cmd != "" {
			return canonicalToolBucketKey(cmd)
		}
	}
	return canonicalToolBucketKey(firstSpaceWord(toolName))
}

func canonicalToolBucketKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return unnamedToolKey
	}
	return strings.ToUpper(key)
}

// firstSpaceWord returns the prefix of s up to the first whitespace rune.
// If s has no whitespace, s is returned unchanged.
func firstSpaceWord(s string) string {
	for i, r := range s {
		if unicode.IsSpace(r) {
			return s[:i]
		}
	}
	return s
}

const unnamedToolKey = "(UNNAMED)"

func isShellClassTool(name string) bool {
	low := strings.ToLower(name)
	return strings.Contains(low, "shell") || strings.Contains(low, "bash")
}

// extractShellCommand pulls the leading command word out of the args
// payload of a shell-class tool call.
//
// The two-stage logic exists so we can distinguish "JSON parsed with a usable
// command" from "JSON parse failed / missing command". Per the data-model
// contract, malformed or incomplete args must fall back to the original tool
// name instead of guessing from the raw args; otherwise partial JSON streams
// can leak dirty bucket keys into By Tool.
func extractShellCommand(args string) string {
	cmdLine, jsonOK := jsonArgPeekResult(args)
	if !jsonOK {
		return ""
	}
	return firstCommandToken(cmdLine)
}

// firstCommandToken returns the first command word in the line, stripping
// surrounding quotes and skipping leading KEY=value environment prefixes.
// Returns "" when no usable token is present.
func firstCommandToken(line string) string {
	for {
		tok, rest := nextShellWord(line)
		if tok == "" {
			return ""
		}
		if isEnvAssignmentToken(tok) {
			line = rest
			continue
		}
		return tok
	}
}

// nextShellWord consumes the next shell word from line. Quotes are honored
// both when they wrap the whole word (`"my cmd"`) and when they appear inside
// an assignment (`FOO="a b"`). Surrounding quote characters are stripped from
// the returned token. Returns ("", "") when input is empty / whitespace-only.
//
// This is intentionally minimal — full POSIX shell parsing (escapes, $...,
// nested quotes) is out of scope. The aim is just to surface the user's
// intended first command in the common cases shell-class tools produce.
func nextShellWord(line string) (string, string) {
	i := 0
	for i < len(line) && unicode.IsSpace(rune(line[i])) {
		i++
	}
	if i == len(line) {
		return "", ""
	}
	var b strings.Builder
	var quote byte
	for i < len(line) {
		ch := line[i]
		if quote == 0 && unicode.IsSpace(rune(ch)) {
			break
		}
		if ch == '"' || ch == '\'' {
			if quote == 0 {
				quote = ch
				i++
				continue
			}
			if quote == ch {
				quote = 0
				i++
				continue
			}
		}
		b.WriteByte(ch)
		i++
	}
	return b.String(), line[i:]
}

// isEnvAssignmentToken matches tokens of the form "KEY=value" — typical
// environment-variable prefixes the user puts in front of a real command,
// e.g. "FOO=bar ls -la". The full bash spec is more nuanced (must match
// [A-Za-z_][A-Za-z0-9_]*=), but we mimic that constraint to avoid mis-
// classifying tokens that happen to contain "=" (e.g. URLs).
func isEnvAssignmentToken(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	key := tok[:eq]
	for i, r := range key {
		if i == 0 {
			if !(unicode.IsLetter(r) || r == '_') {
				return false
			}
			continue
		}
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return true
}
