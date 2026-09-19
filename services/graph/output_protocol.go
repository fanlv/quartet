package graph

import (
	"fmt"
	"sort"
	"strings"
)

// Prompt output variable protocol (§1 输出变量契约). Unlike Shell, Agent nodes
// have no control file, so declared output variables are carried in the model's
// raw output. The primary protocol uses named BEGIN/END blocks so values can
// contain newlines verbatim. The legacy single-line marker remains readable for
// persisted workflows and Agent replies produced before multiline blocks.

const (
	quartetOutputMarker      = "QUARTET_OUTPUT:"
	quartetOutputBeginMarker = "QUARTET_OUTPUT_BEGIN:"
	quartetOutputEndMarker   = "QUARTET_OUTPUT_END:"
)

// OutputParseResult carries the parsed named outputs and any failure detail.
type OutputParseResult struct {
	Variables map[string]string
}

// OutputProtocolError describes a model-output protocol violation with enough
// detail for full error display (§4): which variable, what went wrong, plus the
// caller attaches the raw model output.
type OutputProtocolError struct {
	Variable string
	Message  string
}

func (e *OutputProtocolError) Error() string { return e.Message }

// ParseQuartetOutput extracts named output blocks and legacy single-line markers
// from a model's raw output, then validates them against the declared set.
//
// Rules (§1):
//   - primary multiline form: QUARTET_OUTPUT_BEGIN:<name> may be preceded by
//     accumulated assistant text on its line; the value is every subsequent
//     line verbatim until a dedicated QUARTET_OUTPUT_END:<name> line. It may be
//     empty and may contain newlines, equals signs, quotes, and JSON-like text;
//   - a nested begin marker, mismatched closing name, unexpected closing marker,
//     or missing closing marker fails the node with the complete protocol error;
//   - legacy single-line form remains accepted: QUARTET_OUTPUT:<name>=<value> is
//     matched as a substring anywhere within a line, may occur multiple times on
//     that line, splits on the first '=', and preserves the value verbatim;
//   - variable name must match [A-Za-z_][A-Za-z0-9_]* and must not be reserved
//     (leading '_' or 'QUARTET_');
//   - same name produced multiple times → last value wins;
//   - declaration is OPTIONAL and does NOT gate output (relaxed §1, mirroring
//     ParseShellControl): EVERY named output flows downstream whether or not it
//     was declared. The declared set drives only the completeness check — every
//     DECLARED variable must still be produced (a missing output fails the node).
//
// On any violation it returns the first located error (callers attach the raw
// output); a single error is enough because the whole node fails. Declared
// names are assumed already validated at save time, but reserved/invalid names
// produced by the model are still rejected here.
//
// Trade-off (same as Shell): because undeclared outputs are no longer dropped,
// the save-time parallel-writer conflict check (validateOutputConflicts) — which
// only looks at DECLARED names — cannot catch two parallel Prompt nodes that emit
// the same undeclared variable. That collision is the author's responsibility,
// mirroring the intentionally permissive "set whatever you want" model.
func ParseQuartetOutput(rawOutput string, declared []string) (*OutputParseResult, *OutputProtocolError) {
	parsed := make(map[string]string)
	lines := strings.Split(rawOutput, "\n")
	for lineIndex := 0; lineIndex < len(lines); lineIndex++ {
		line := strings.TrimSuffix(lines[lineIndex], "\r")
		if markerStart := strings.Index(line, quartetOutputBeginMarker); markerStart >= 0 {
			prefix := line[:markerStart]
			trimmedPrefix := strings.TrimSpace(prefix)
			if strings.HasPrefix(trimmedPrefix, quartetOutputEndMarker) {
				name := strings.TrimSpace(strings.TrimPrefix(trimmedPrefix, quartetOutputEndMarker))
				return nil, unexpectedPromptOutputEnd(name)
			}
			if perr := parseLegacyPromptOutputLine(prefix, parsed); perr != nil {
				return nil, perr
			}

			name := strings.TrimSpace(line[markerStart+len(quartetOutputBeginMarker):])
			if perr := validatePromptOutputName(name); perr != nil {
				return nil, perr
			}

			endLine := -1
			remainder := ""
			for candidate := lineIndex + 1; candidate < len(lines); candidate++ {
				candidateLine := strings.TrimSuffix(lines[candidate], "\r")
				trimmed := strings.TrimSpace(candidateLine)
				if strings.HasPrefix(trimmed, quartetOutputEndMarker) {
					closingBody := strings.TrimSpace(strings.TrimPrefix(trimmed, quartetOutputEndMarker))
					closingName := closingBody
					if nextMarker := firstPromptOutputMarker(closingBody); nextMarker >= 0 {
						closingName = strings.TrimSpace(closingBody[:nextMarker])
						remainder = closingBody[nextMarker:]
					}
					if closingName != name {
						return nil, &OutputProtocolError{
							Variable: name,
							Message:  fmt.Sprintf("model output block for variable %q closes with mismatched variable %q", name, closingName),
						}
					}
					endLine = candidate
					break
				}
				if strings.HasPrefix(trimmed, quartetOutputBeginMarker) {
					return nil, &OutputProtocolError{
						Variable: name,
						Message:  fmt.Sprintf("model output block for variable %q contains a nested %s marker before its closing marker", name, quartetOutputBeginMarker),
					}
				}
			}
			if endLine < 0 {
				return nil, &OutputProtocolError{
					Variable: name,
					Message:  fmt.Sprintf("model output block for variable %q is missing closing marker %s%s", name, quartetOutputEndMarker, name),
				}
			}

			valueLines := make([]string, 0, endLine-lineIndex-1)
			for _, valueLine := range lines[lineIndex+1 : endLine] {
				valueLines = append(valueLines, strings.TrimSuffix(valueLine, "\r"))
			}
			parsed[name] = strings.Join(valueLines, "\n")
			if remainder != "" {
				lines[endLine] = remainder
				lineIndex = endLine - 1
			} else {
				lineIndex = endLine
			}
			continue
		}
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, quartetOutputEndMarker) {
			name := strings.TrimSpace(strings.TrimPrefix(trimmedLine, quartetOutputEndMarker))
			return nil, unexpectedPromptOutputEnd(name)
		}
		if perr := parseLegacyPromptOutputLine(line, parsed); perr != nil {
			return nil, perr
		}
	}

	// Every declared variable must be produced.
	var missing []string
	for _, name := range declared {
		if _, ok := parsed[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &OutputProtocolError{
			Variable: missing[0],
			Message:  fmt.Sprintf("model output is missing declared variable(s): %s", strings.Join(missing, ", ")),
		}
	}

	return &OutputParseResult{Variables: parsed}, nil
}

func parseLegacyPromptOutputLine(line string, parsed map[string]string) *OutputProtocolError {
	for searchFrom := 0; ; {
		relMarker := strings.Index(line[searchFrom:], quartetOutputMarker)
		if relMarker < 0 {
			return nil
		}
		markerStart := searchFrom + relMarker
		bodyStart := markerStart + len(quartetOutputMarker)
		bodyEnd := len(line)
		if relNext := strings.Index(line[bodyStart:], quartetOutputMarker); relNext >= 0 {
			bodyEnd = bodyStart + relNext
		}

		body := strings.TrimRight(line[bodyStart:bodyEnd], "\r")
		name, value, ok := strings.Cut(body, "=")
		if !ok {
			return &OutputProtocolError{
				Message: fmt.Sprintf("malformed %s entry (missing '='): %q", quartetOutputMarker, line[markerStart:bodyEnd]),
			}
		}
		name = strings.TrimSpace(name)
		if perr := validatePromptOutputName(name); perr != nil {
			return perr
		}
		parsed[name] = value

		if bodyEnd == len(line) {
			return nil
		}
		searchFrom = bodyEnd
	}
}

func firstPromptOutputMarker(value string) int {
	first := -1
	for _, marker := range []string{quartetOutputBeginMarker, quartetOutputEndMarker, quartetOutputMarker} {
		if at := strings.Index(value, marker); at >= 0 && (first < 0 || at < first) {
			first = at
		}
	}
	return first
}

func unexpectedPromptOutputEnd(name string) *OutputProtocolError {
	return &OutputProtocolError{
		Variable: name,
		Message:  fmt.Sprintf("model output contains unexpected closing marker %s%s without a matching begin marker", quartetOutputEndMarker, name),
	}
}

func validatePromptOutputName(name string) *OutputProtocolError {
	if isReservedVar(name) {
		return &OutputProtocolError{
			Variable: name,
			Message:  fmt.Sprintf("model output wrote reserved variable name %q (names starting with '_' or 'QUARTET_' are reserved)", name),
		}
	}
	if !isValidVarName(name) {
		return &OutputProtocolError{
			Variable: name,
			Message:  fmt.Sprintf("model output declared invalid variable name %q (must match [A-Za-z_][A-Za-z0-9_]*)", name),
		}
	}
	return nil
}

// buildOutputProtocolSuffix returns the fixed protocol suffix appended to a
// Prompt node's prompt so the model emits one multiline block per declared
// variable. Returns "" when no output variables are declared.
func buildOutputProtocolSuffix(declared []string) string {
	if len(declared) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n---\n")
	b.WriteString("请在回答的最后，为下面每个变量输出一个多行块。起止标记必须各自独占一行且变量名一致；标记之间的值会按原文保存，可以为空或包含任意换行、引号和等号。直接输出原始内容，不要转换成 JSON 字符串或 Base64，也不要用 Markdown 代码围栏包裹输出块：\n")
	for _, name := range declared {
		b.WriteString(quartetOutputBeginMarker)
		b.WriteString(name)
		b.WriteString("\n<")
		b.WriteString(name)
		b.WriteString(" 的原始值，可多行>\n")
		b.WriteString(quartetOutputEndMarker)
		b.WriteString(name)
		b.WriteString("\n")
	}
	return b.String()
}
