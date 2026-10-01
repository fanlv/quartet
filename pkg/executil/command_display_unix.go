//go:build !windows

package executil

import (
	"sort"
	"strings"
)

// ShellCommand renders structured argv for a user to run manually. It is not
// used to execute subprocesses, which continue to use Command directly.
func ShellCommand(program string, args []string, env map[string]string) string {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	parts := make([]string, 0, len(env)+len(args)+2)
	if len(env) > 0 {
		parts = append(parts, "env")
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(parts, quote(key+"="+env[key]))
		}
	}
	parts = append(parts, quote(program))
	for _, arg := range args {
		parts = append(parts, quote(arg))
	}
	return strings.Join(parts, " ")
}
