// Package runtimeenv resolves the effective environment used by built-in ACP
// adapters. It keeps adapter defaults out of persisted user settings.
package runtimeenv

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/services/agent/catalog"
)

// Resolver adds runtime-only defaults for built-in ACP adapters. Explicit
// settings win, including an explicitly empty value; an inherited process
// variable wins next. Otherwise the current CLI is resolved on every call so
// package-manager and PATH changes do not leave a persisted stale path.
type Resolver struct{}

func (Resolver) Effective(agentIdentifier string, configured map[string]string) map[string]string {
	effective := clone(configured)
	agent, ok := catalog.ResolveBuiltin(agentIdentifier)
	if !ok {
		return effective
	}
	envKey := strings.TrimSpace(agent.CLIExecutableEnv)
	if envKey == "" {
		return effective
	}
	if _, exists := effective[envKey]; exists {
		return effective
	}
	if _, inherited := os.LookupEnv(envKey); inherited {
		return effective
	}
	resolved, err := executil.LookPath(agent.Bin)
	if err != nil {
		return effective
	}
	if absolute, err := filepath.Abs(resolved); err == nil {
		resolved = absolute
	}
	if effective == nil {
		effective = make(map[string]string, 1)
	}
	effective[envKey] = resolved
	return effective
}

func clone(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
