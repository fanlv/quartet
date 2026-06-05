package acp

import (
	"strings"
	"sync"
)

// allowedAgentCommands is the set of allowed ACP agent commands.
// Populated at startup via RegisterAllowedAgentCommands.
var (
	allowedAgentCommands   = make(map[string]bool)
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

// orphanCleanupKeywords returns distinctive tokens from registered commands
// (skipping generic ones like "npx" / "node") so the orphan-cleanup scanner
// can match against /proc/<pid>/cmdline.
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
	for cmd := range allowedAgentCommands {
		for _, token := range strings.Fields(cmd) {
			if !genericTokens[token] {
				keywords[token] = true
			}
		}
	}
	return keywords
}
