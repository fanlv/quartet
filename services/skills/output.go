package skills

import (
	"regexp"
	"strings"

	"github.com/fanlv/quartet/types/model"
)

// ANSI control sequences emitted by the skills CLI: CSI (colors, cursor moves,
// line erases), OSC (hyperlinks) and charset selects. Colors are already
// suppressed via NO_COLOR, but spinner/erase sequences are not.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][A-B0-9]`)

// spinnerRunes are the frames the CLI's progress spinner cycles through.
var spinnerRunes = map[rune]bool{
	'◒': true, '◐': true, '◓': true, '◑': true,
	'⠋': true, '⠙': true, '⠹': true, '⠸': true, '⠼': true, '⠴': true, '⠦': true, '⠧': true, '⠇': true, '⠏': true,
}

// CleanTerminalOutput turns raw CLI output into something readable inside a
// <pre> block: escape sequences removed, carriage-return progress rewrites
// resolved to their final text, spinner-only lines dropped, and runs of blank
// lines collapsed. Indentation is preserved — the CLI uses it to nest per-skill
// results under their heading.
func CleanTerminalOutput(raw string) string {
	if raw == "" {
		return ""
	}
	cleaned := ansiRegex.ReplaceAllString(raw, "")

	lines := make([]string, 0, 16)
	blankRun := 0
	for line := range strings.SplitSeq(cleaned, "\n") {
		// A spinner repeatedly rewrites one line with \r; only the last write
		// carries the final text.
		if idx := strings.LastIndex(line, "\r"); idx >= 0 {
			line = line[idx+1:]
		}
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun == 1 && len(lines) > 0 {
				lines = append(lines, "")
			}
			continue
		}
		if isSpinnerOnly(line) {
			continue
		}
		blankRun = 0
		lines = append(lines, line)
	}
	// A trailing blank line adds nothing inside a <pre>.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func isSpinnerOnly(line string) bool {
	seen := false
	for _, r := range line {
		if r == ' ' || r == '\t' {
			continue
		}
		if !spinnerRunes[r] {
			return false
		}
		seen = true
	}
	return seen
}

// findResultRe matches one result row of `skills find`, e.g.
//
//	owner/repo@skill    1.9K installs
//
// The install count is the last field before the literal "installs".
var findResultRe = regexp.MustCompile(`^(\S+)\s+(\S+)\s+installs?$`)

// parseFindOutput parses the human-readable output of `skills find`, which has
// no --json mode. Each result spans two lines:
//
//	<name> <installs> installs
//	└ <url>
func parseFindOutput(raw string) []model.SkillFindResult {
	lines := strings.Split(CleanTerminalOutput(raw), "\n")

	var results []model.SkillFindResult
	for i := 0; i < len(lines); i++ {
		match := findResultRe.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if match == nil {
			continue
		}
		result := model.SkillFindResult{Name: match[1], Installs: match[2]}
		// The URL sits on the next non-empty line, prefixed by a tree glyph.
		for j := i + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if next == "" {
				continue
			}
			next = strings.TrimSpace(strings.TrimPrefix(next, "└"))
			if strings.HasPrefix(next, "http") {
				result.URL = next
				i = j
			}
			break
		}
		results = append(results, result)
	}
	return results
}
