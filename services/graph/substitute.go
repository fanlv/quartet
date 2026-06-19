package graph

import "strings"

// substituteVariables performs a single-pass replacement of {{name}} placeholders
// in text using an instance's visible variable snapshot (§3 文本替换).
//
// Single-pass via strings.NewReplacer (mirroring the Loop engine's approach in
// services/job/executor_vars.go) avoids nondeterminism from Go map iteration
// order: a replaced value is never re-scanned, so a value containing "{{x}}"
// does not get substituted again.
//
// Semantics (§3):
//   - disabled variable  → replaced with the empty string;
//   - known variable     → replaced with its value;
//   - unknown / pruned-and-never-produced variable → the literal {{name}} is
//     preserved unchanged (no error at substitution time).
//
// Because only declared placeholders are registered with the replacer, any
// {{name}} whose name is neither in vars nor in disabled is left untouched —
// which is exactly the "unknown/pruned → keep literal" rule.
func substituteVariables(text string, vars map[string]string, disabled map[string]struct{}) string {
	if len(vars) == 0 && len(disabled) == 0 {
		return text
	}
	oldnew := make([]string, 0, (len(vars)+len(disabled))*2)
	for k, v := range vars {
		if _, off := disabled[k]; off {
			// Disabled variable renders to empty string, regardless of its
			// stored value (which is kept so re-enabling restores it).
			oldnew = append(oldnew, "{{"+k+"}}", "")
			continue
		}
		oldnew = append(oldnew, "{{"+k+"}}", v)
	}
	// A disabled name with no entry in vars still blanks its placeholder.
	for k := range disabled {
		if _, ok := vars[k]; !ok {
			oldnew = append(oldnew, "{{"+k+"}}", "")
		}
	}
	return strings.NewReplacer(oldnew...).Replace(text)
}
