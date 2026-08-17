package acp

import (
	"fmt"
	"strings"
	"sync"
)

type RuntimeDefinition struct {
	Program     string
	Args        []string
	EnvKey      string
	Env         []EnvVar
	OverrideEnv bool
}

// allowedAgentCommands is the set of allowed ACP agent commands.
// Populated at startup via RegisterAllowedAgentCommands.
var (
	allowedAgentCommands   = make(map[string]bool)
	allowedAgentRuntimes   = make(map[string]RuntimeDefinition)
	allowedAgentCommandsMu sync.RWMutex
)

// RegisterAllowedAgentCommands registers the set of allowed ACP agent commands.
// Must be called at startup before any NewConn calls.
func RegisterAllowedAgentCommands(commands []string) {
	allowedAgentCommandsMu.Lock()
	defer allowedAgentCommandsMu.Unlock()
	for _, cmd := range commands {
		allowedAgentCommands[cmd] = true
	}
}

func RegisterAgentRuntime(key string, definition RuntimeDefinition) error {
	key = strings.TrimSpace(key)
	definition.Program = strings.TrimSpace(definition.Program)
	if key == "" || definition.Program == "" {
		return fmt.Errorf("register ACP runtime failed: key and program are required")
	}
	allowedAgentCommandsMu.Lock()
	allowedAgentRuntimes[key] = RuntimeDefinition{
		Program:     definition.Program,
		Args:        append([]string(nil), definition.Args...),
		EnvKey:      definition.EnvKey,
		Env:         append([]EnvVar(nil), definition.Env...),
		OverrideEnv: definition.OverrideEnv,
	}
	allowedAgentCommands[key] = true
	allowedAgentCommandsMu.Unlock()
	return nil
}

func UnregisterAgentRuntime(key string) {
	allowedAgentCommandsMu.Lock()
	delete(allowedAgentRuntimes, key)
	delete(allowedAgentCommands, key)
	allowedAgentCommandsMu.Unlock()
}

func AgentRuntime(key string) (RuntimeDefinition, bool) {
	allowedAgentCommandsMu.RLock()
	defer allowedAgentCommandsMu.RUnlock()
	definition, ok := allowedAgentRuntimes[key]
	if !ok {
		return RuntimeDefinition{}, false
	}
	definition.Args = append([]string(nil), definition.Args...)
	definition.Env = append([]EnvVar(nil), definition.Env...)
	return definition, true
}

// IsAllowedAgentCommand checks whether the given agentType is in the whitelist.
func IsAllowedAgentCommand(agentType string) bool {
	allowedAgentCommandsMu.RLock()
	defer allowedAgentCommandsMu.RUnlock()
	return allowedAgentCommands[agentType]
}

// AllowedAgentCommands returns a snapshot of the current allowed command list.
func AllowedAgentCommands() []string {
	allowedAgentCommandsMu.RLock()
	defer allowedAgentCommandsMu.RUnlock()
	cmds := make([]string, 0, len(allowedAgentCommands))
	for cmd := range allowedAgentCommands {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// orphanCleanupKeywords returns distinctive tokens from registered runtime
// programs/args and compatibility commands so the orphan-cleanup scanner can
// match against /proc/<pid>/cmdline even when execution is allowed only through
// a stable runtime key.
func orphanCleanupKeywords() map[string]bool {
	// genericTokens are argv tokens that are too common across ACP agents or
	// unrelated system processes to safely identify a Quartet-owned subprocess.
	// They are excluded from orphan-cleanup keywords, which are later matched
	// against /proc/<pid>/cmdline.
	genericTokens := map[string]bool{
		"npx": true, "npm": true, "exec": true, "node": true,
		"acp": true, "serve": true,
		"-y": true, "--acp": true, "--stdio": true, "--output-format": true,
	}

	allowedAgentCommandsMu.RLock()
	defer allowedAgentCommandsMu.RUnlock()

	keywords := make(map[string]bool)
	addTokens := func(values ...string) {
		for _, value := range values {
			for _, token := range strings.Fields(value) {
				if !genericTokens[token] {
					keywords[token] = true
				}
			}
		}
	}
	for cmd := range allowedAgentCommands {
		addTokens(cmd)
	}
	for _, runtimeDefinition := range allowedAgentRuntimes {
		addTokens(runtimeDefinition.Program)
		for _, arg := range runtimeDefinition.Args {
			addTokens(arg)
		}
	}
	return keywords
}
